package readmodel

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type vmJournalFixture struct {
	mu      sync.Mutex
	changes []repository.VirtualizationResourceChange
}

func (j *vmJournalFixture) ListChanges(_ context.Context, org uuid.UUID, after int64, limit int) ([]repository.VirtualizationResourceChange, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []repository.VirtualizationResourceChange{}
	for _, c := range j.changes {
		if c.OrgID == org && c.Sequence > after {
			out = append(out, c)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func vmChange(org, id uuid.UUID, seq int64) repository.VirtualizationResourceChange {
	v := domain.PersistentVMDeployment{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: id, OrgID: org, Generation: seq, UpdatedAt: time.Unix(seq, 0)}, LifecycleClass: domain.VMLifecyclePersistent, Provider: domain.VMProviderLibvirt, Purpose: domain.VMPurposeDesktop, DesiredPower: domain.VMDesiredRunning, DisplayName: "password=sentinel"}
	data, _ := json.Marshal(v)
	return repository.VirtualizationResourceChange{Sequence: seq, SchemaVersion: 1, OrgID: org, ResourceID: id, ResourceKind: domain.PersistentVMResource, Generation: seq, LifecycleClasses: []domain.VMLifecycleClass{domain.VMLifecyclePersistent}, ChangeType: "updated", Document: data, OccurredAt: time.Unix(seq, 0)}
}

type vmProjectionStore struct {
	*repository.InMemoryNostrEventRepository
	failCursor   bool
	checkpointed chan struct{}
}

func (s *vmProjectionStore) SaveMigrationCursor(ctx context.Context, c repository.NostrMigrationCursor) error {
	if s.failCursor {
		return errors.New("cursor unavailable")
	}
	err := s.InMemoryNostrEventRepository.SaveMigrationCursor(ctx, c)
	if err == nil && s.checkpointed != nil {
		select {
		case s.checkpointed <- struct{}{}:
		default:
		}
	}
	return err
}

type vmDurablePublisher struct {
	store        *vmProjectionStore
	signer       nostr.Signer
	calls        int
	failBefore   bool
	relayFailure bool
	published    []nostr.Event
}

func (p *vmDurablePublisher) PublishSignedEvent(ctx context.Context, e *nostr.Event) error {
	p.calls++
	if p.failBefore {
		return errors.New("signing unavailable")
	}
	if err := p.signer.SignEvent(ctx, e); err != nil {
		return err
	}
	tags, _ := json.Marshal(e.Tags)
	_, err := p.store.Record(ctx, &repository.NostrEventRecord{ID: e.ID.Hex(), Kind: int(e.Kind), PubKey: e.PubKey.Hex(), Content: e.Content, Tags: tags, Sig: hex.EncodeToString(e.Sig[:]), CreatedAt: e.CreatedAt.Time(), PublishState: repository.NostrPublishStatePending})
	if err != nil {
		return err
	}
	p.published = append(p.published, *e)
	if p.relayFailure {
		return errors.New("relay rejected")
	}
	return p.store.MarkPublished(ctx, e.ID.Hex(), time.Now())
}
func vmProjectorFixture(t *testing.T) (*VirtualizationProjector, *vmJournalFixture, *vmProjectionStore, *vmDurablePublisher) {
	t.Helper()
	secret := [32]byte{1}
	signer := keyer.NewPlainKeySigner(secret)
	pubkey, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	journal := &vmJournalFixture{}
	store := &vmProjectionStore{InMemoryNostrEventRepository: repository.NewInMemoryNostrEventRepository()}
	pub := &vmDurablePublisher{store: store, signer: signer}
	p, err := NewVirtualizationProjector(journal, store, pub, pubkey.Hex())
	if err != nil {
		t.Fatal(err)
	}
	p.now = func() time.Time { return time.Unix(100, 0) }
	p.waitUntil = func(context.Context, time.Time) error { return nil }
	t.Cleanup(p.Close)
	return p, journal, store, pub
}
func TestVirtualizationJournalRecoveryCoalescesStatePreservesAudit(t *testing.T) {
	p, j, store, pub := vmProjectorFixture(t)
	org, id := uuid.New(), uuid.New()
	j.changes = []repository.VirtualizationResourceChange{vmChange(org, id, 1), vmChange(org, id, 4), vmChange(org, id, 8)}
	if err := p.Recover(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	audits, _ := store.ListByKind(context.Background(), kinds.CASAudit, 100)
	states, _ := store.ListByKind(context.Background(), kinds.CASControlState, 100)
	if len(audits) != 3 || len(states) != 1 {
		t.Fatalf("audit=%d state=%d", len(audits), len(states))
	}
	for _, event := range pub.published {
		if strings.Contains(event.Content, "sentinel") || strings.Contains(event.Content, "bootstrap") {
			t.Fatal("secret projection")
		}
		for _, tag := range []string{"domain", "entity", "schema", "org", "sequence", "lifecycle_class"} {
			if event.Tags.Find(tag) == nil {
				t.Fatalf("missing %s", tag)
			}
		}
	}
	if err := p.Handle(context.Background(), events.Event{Data: events.VirtualizationChange{OrgID: org.String(), Sequence: 99}}); err != nil {
		t.Fatal(err)
	}
	if pub.calls != 4 {
		t.Fatal("duplicate projection")
	}
	// Recovery after a lost live signal starts from the persisted sequence, including
	// gaps in BIGSERIAL occupied by another tenant (not a missing event).
	j.changes = append(j.changes, vmChange(org, id, 10))
	waited := false
	p.waitUntil = func(_ context.Context, at time.Time) error {
		waited = true
		if at.Unix() != 101 {
			t.Fatal(at)
		}
		return nil
	}
	restarted, err := NewVirtualizationProjector(j, store, pub, p.author)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.now = p.now
	restarted.waitUntil = p.waitUntil
	if err := restarted.Recover(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	if !waited || pub.calls != 6 {
		t.Fatalf("recovery calls=%d rate_limited=%v", pub.calls, waited)
	}
}
func TestVirtualizationDurableAdmissionAndCursorFailureRetry(t *testing.T) {
	p, j, store, pub := vmProjectorFixture(t)
	org, id := uuid.New(), uuid.New()
	j.changes = []repository.VirtualizationResourceChange{vmChange(org, id, 1)}
	pub.failBefore = true
	if err := p.Recover(context.Background(), org); err == nil {
		t.Fatal("advanced without durable event")
	}
	cursor, _ := store.GetMigrationCursor(context.Background(), "virtualization:"+p.author+":"+org.String())
	if cursor != nil {
		t.Fatal("checkpoint on failure")
	}
	pub.failBefore = false
	pub.relayFailure = true
	store.failCursor = true
	if err := p.Recover(context.Background(), org); err == nil {
		t.Fatal("lost cursor failure")
	}
	before := pub.calls
	ids := []string{pub.published[0].ID.Hex(), pub.published[1].ID.Hex()}
	store.failCursor = false
	if err := p.Recover(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	if pub.calls != before {
		t.Fatal("re-signed persisted events")
	}
	pending, _ := store.ListUnpublished(context.Background(), 100)
	if len(pending) != 2 {
		t.Fatal("relay failure lost outbox")
	}
	for _, rec := range pending {
		if rec.ID != ids[0] && rec.ID != ids[1] {
			t.Fatal("event changed")
		}
	}
}
func TestVirtualizationLiveBusAndShutdown(t *testing.T) {
	p, j, store, pub := vmProjectorFixture(t)
	store.checkpointed = make(chan struct{}, 1)
	org, id := uuid.New(), uuid.New()
	j.changes = []repository.VirtualizationResourceChange{vmChange(org, id, 1)}
	bus := events.NewInProcessPublisher(zap.NewNop())
	if err := p.Subscribe(bus); err != nil {
		t.Fatal(err)
	}
	bus.Publish(context.Background(), events.Event{Type: events.EventVirtualizationResourceChanged, Data: events.VirtualizationChange{OrgID: org.String()}})
	// Await a durable checkpoint signal, never a sleep or completion timeout.
	<-store.checkpointed
	p.mu.Lock()
	p.mu.Unlock()
	if pub.calls != 2 {
		t.Fatalf("calls %d", pub.calls)
	}
	p.Close()
	if err := p.Recover(context.Background(), org); !errors.Is(err, context.Canceled) {
		t.Fatalf("after shutdown: %v", err)
	}
}
func TestVirtualizationRejectsCorruptJournal(t *testing.T) {
	p, j, _, _ := vmProjectorFixture(t)
	org, id := uuid.New(), uuid.New()
	change := vmChange(org, id, 1)
	change.ResourceID = uuid.New()
	j.changes = []repository.VirtualizationResourceChange{change}
	if err := p.Recover(context.Background(), org); err == nil {
		t.Fatal("identity mismatch accepted")
	}
}
func TestVirtualizationQueryTenantFenceAndPagination(t *testing.T) {
	q := VirtualizationQuery{}
	if _, err := q.List(context.Background(), uuid.New(), domain.PersistentVMResource, 101, 0); !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatal(err)
	}
	if _, err := q.Get(context.Background(), uuid.New(), domain.PersistentVMResource, uuid.New()); !errors.Is(err, ErrVirtualizationUnavailable) {
		t.Fatal(fmt.Sprint(err))
	}
}
