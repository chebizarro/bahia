package readmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

var ErrVirtualizationUnavailable = errors.New("virtualization unavailable")

// VirtualizationQuery shares tenant-fenced repository reads and public mapping
// across REST and ContextVM. Authorization belongs to their ingress handlers.
type VirtualizationQuery struct {
	Repository repository.VirtualizationRepository
}

func (q VirtualizationQuery) Get(ctx context.Context, org uuid.UUID, kind domain.VirtualizationResourceKind, id uuid.UUID) (*dto.VirtualizationResource, error) {
	if org == uuid.Nil || id == uuid.Nil {
		return nil, domain.ErrInvalidValue
	}
	if q.Repository == nil {
		return nil, ErrVirtualizationUnavailable
	}
	var v any
	var err error
	switch kind {
	case domain.VirtualizationHostResource:
		v, err = q.Repository.Hosts().Get(ctx, org, id)
	case domain.VMImageResource:
		v, err = q.Repository.Images().Get(ctx, org, id)
	case domain.PersistentVMResource:
		v, err = q.Repository.Deployments().Get(ctx, org, id)
	case domain.ExecutionPlaneResource:
		v, err = q.Repository.ExecutionPlanes().Get(ctx, org, id)
	case domain.VMCheckpointResource:
		v, err = q.Repository.Checkpoints().Get(ctx, org, id)
	case domain.VMExportResource:
		v, err = q.Repository.Exports().Get(ctx, org, id)
	case domain.VMOperationResource:
		v, err = q.Repository.GetOperation(ctx, org, id)
	default:
		return nil, domain.ErrInvalidValue
	}
	if err != nil {
		return nil, err
	}
	view, err := dto.PublicVirtualizationResource(kind, v)
	if err == nil && (view.OrgID != org || view.ID != id) {
		return nil, repository.ErrNotFound
	}
	return view, err
}
func publicVMList[T any](ctx context.Context, r repository.VirtualizationResources[T], org uuid.UUID, kind domain.VirtualizationResourceKind, limit, offset int) ([]dto.VirtualizationResource, error) {
	rows, err := r.List(ctx, org, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]dto.VirtualizationResource, 0, len(rows))
	for _, v := range rows {
		p, err := dto.PublicVirtualizationResource(kind, v)
		if err != nil {
			return nil, err
		}
		if p.OrgID != org {
			return nil, repository.ErrNotFound
		}
		out = append(out, *p)
	}
	return out, nil
}
func (q VirtualizationQuery) List(ctx context.Context, org uuid.UUID, kind domain.VirtualizationResourceKind, limit, offset int) ([]dto.VirtualizationResource, error) {
	if org == uuid.Nil || limit < 1 || limit > 100 || offset < 0 {
		return nil, domain.ErrInvalidValue
	}
	if q.Repository == nil {
		return nil, ErrVirtualizationUnavailable
	}
	switch kind {
	case domain.VirtualizationHostResource:
		return publicVMList(ctx, q.Repository.Hosts(), org, kind, limit, offset)
	case domain.VMImageResource:
		return publicVMList(ctx, q.Repository.Images(), org, kind, limit, offset)
	case domain.PersistentVMResource:
		return publicVMList(ctx, q.Repository.Deployments(), org, kind, limit, offset)
	case domain.ExecutionPlaneResource:
		return publicVMList(ctx, q.Repository.ExecutionPlanes(), org, kind, limit, offset)
	case domain.VMCheckpointResource:
		return publicVMList(ctx, q.Repository.Checkpoints(), org, kind, limit, offset)
	case domain.VMExportResource:
		return publicVMList(ctx, q.Repository.Exports(), org, kind, limit, offset)
	}
	return nil, domain.ErrInvalidValue
}

type VirtualizationJournal interface {
	ListChanges(context.Context, uuid.UUID, int64, int) ([]repository.VirtualizationResourceChange, error)
}

// The existing durable cursor store is reused in its own namespace. EventID is
// a zero-padded journal sequence and CreatedAt is a fixed epoch, so its monotonic
// keyset update remains valid without a feature-specific migration.
type VirtualizationProjectionStore interface {
	repository.NostrEventOutboxRepository
	GetMigrationCursor(context.Context, string) (*repository.NostrMigrationCursor, error)
	SaveMigrationCursor(context.Context, repository.NostrMigrationCursor) error
	FindLatestByKindPubkeyDTag(context.Context, int, string, string, string) (*repository.NostrEventRecord, error)
}
type VirtualizationSignedPublisher interface {
	PublishSignedEvent(context.Context, *nostr.Event) error
}
type VirtualizationOrganizations interface {
	List(context.Context) ([]domain.Organization, error)
}

type VirtualizationProjector struct {
	journal   VirtualizationJournal
	store     VirtualizationProjectionStore
	publisher VirtualizationSignedPublisher
	author    string
	mu        sync.Mutex
	closed    bool
	lifecycle context.Context
	cancel    context.CancelFunc
	now       func() time.Time
	waitUntil func(context.Context, time.Time) error
}

func NewVirtualizationProjector(journal VirtualizationJournal, store VirtualizationProjectionStore, publisher VirtualizationSignedPublisher, author string) (*VirtualizationProjector, error) {
	if journal == nil || store == nil || publisher == nil {
		return nil, ErrVirtualizationUnavailable
	}
	if _, err := nostr.PubKeyFromHex(author); err != nil {
		return nil, ErrVirtualizationUnavailable
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &VirtualizationProjector{journal: journal, store: store, publisher: publisher, author: author, lifecycle: ctx, cancel: cancel, now: time.Now, waitUntil: waitVirtualizationPublication}, nil
}

// The timer is outbound rate limiting, not completion detection. Distinct
// snapshots at one coordinate must not share NIP-01's second-resolution timestamp.
func waitVirtualizationPublication(ctx context.Context, at time.Time) error {
	delay := time.Until(at)
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (p *VirtualizationProjector) Subscribe(bus events.ErrorSubscriber) error {
	if bus == nil {
		return ErrVirtualizationUnavailable
	}
	for _, typ := range []events.EventType{events.EventVirtualizationResourceChanged, events.EventVMOperationTransitioned, events.EventExecutionPlaneProbeChanged, events.EventVirtualizationProjectionGap} {
		bus.SubscribeWithError(typ, p.Handle)
	}
	return nil
}
func (p *VirtualizationProjector) Handle(ctx context.Context, e events.Event) error {
	signal, ok := e.Data.(events.VirtualizationChange)
	if !ok {
		return domain.ErrInvalidValue
	}
	org, err := uuid.Parse(signal.OrgID)
	if err != nil || org == uuid.Nil || signal.Sequence < 0 {
		return domain.ErrInvalidValue
	}
	// Every signal drains the authoritative journal, covering dropped, reordered,
	// duplicate and unknown-sequence notifications without trusting their contents.
	return p.Recover(ctx, org)
}
func (p *VirtualizationProjector) Available() bool {
	select {
	case <-p.lifecycle.Done():
		return false
	default:
		return true
	}
}
func (p *VirtualizationProjector) Close() {
	p.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
}
func (p *VirtualizationProjector) Run(ctx context.Context, orgs VirtualizationOrganizations) error {
	defer p.Close()
	if orgs == nil {
		return ErrVirtualizationUnavailable
	}
	rows, err := orgs.List(ctx)
	if err != nil {
		return err
	}
	for _, org := range rows {
		if err := p.Recover(ctx, org.ID); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case <-p.lifecycle.Done():
		return nil
	}
}
func (p *VirtualizationProjector) Recover(ctx context.Context, org uuid.UUID) error {
	select {
	case <-p.lifecycle.Done():
		return context.Canceled
	default:
	}
	if org == uuid.Nil {
		return domain.ErrInvalidValue
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.lifecycle, cancel)
	defer stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := "virtualization:" + p.author + ":" + org.String()
	cursor, err := p.store.GetMigrationCursor(ctx, name)
	if err != nil {
		return err
	}
	var after int64
	if cursor != nil {
		after, err = strconv.ParseInt(cursor.EventID, 10, 64)
		if err != nil || after < 0 {
			return domain.ErrInvalidValue
		}
	}
	for {
		changes, err := p.journal.ListChanges(ctx, org, after, 100)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		newest := map[string]int64{}
		views := make([]*dto.VirtualizationResource, len(changes))
		last := after
		for i, c := range changes {
			if c.OrgID != org || c.Sequence <= last || c.SchemaVersion != domain.VirtualizationSchemaVersion {
				return domain.ErrInvalidValue
			}
			last = c.Sequence
			v, err := dto.PublicVirtualizationDocument(c.ResourceKind, c.Document, c.Observation)
			if err != nil {
				return err
			}
			if v.ID != c.ResourceID || v.OrgID != org || v.Generation != c.Generation {
				return domain.ErrInvalidValue
			}
			views[i] = v
			coordinate, err := dto.VirtualizationCoordinate(c.ResourceKind, c.ResourceID)
			if err != nil {
				return err
			}
			newest[coordinate] = c.Sequence
		}
		for i, c := range changes {
			coordinate, _ := dto.VirtualizationCoordinate(c.ResourceKind, c.ResourceID)
			if err := p.project(ctx, c, views[i], coordinate, false); err != nil {
				return err
			}
			if newest[coordinate] == c.Sequence {
				if err := p.project(ctx, c, views[i], coordinate, true); err != nil {
					return err
				}
			}
		}
		after = last
		if err := p.store.SaveMigrationCursor(ctx, repository.NostrMigrationCursor{Name: name, CreatedAt: time.Unix(0, 0).UTC(), EventID: fmt.Sprintf("%020d", after)}); err != nil {
			return err
		}
	}
}
func (p *VirtualizationProjector) project(ctx context.Context, c repository.VirtualizationResourceChange, v *dto.VirtualizationResource, coordinate string, state bool) error {
	kind := kinds.CASAudit
	schema := kinds.VirtualizationAuditSchema
	suffix := "audit"
	if state {
		kind = kinds.CASControlState
		schema = kinds.VirtualizationStateSchema
		suffix = "state"
	}
	journalKey := c.OrgID.String() + ":" + strconv.FormatInt(c.Sequence, 10) + ":" + suffix
	records, err := p.store.FindByTag(ctx, kinds.VirtualizationTagJournal, journalKey, []int{kind}, 100)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if rec.PubKey == p.author && (rec.PublishState == repository.NostrPublishStatePending || rec.PublishState == repository.NostrPublishStatePublished) {
			return nil
		}
	}
	switch c.ChangeType {
	case "created", "updated", "observed", "session_changed", "reserved", "released", "approval_created", "approval_consumed":
	default:
		return domain.ErrInvalidValue
	}
	entity, _, _ := strings.Cut(coordinate, ":")
	tags := nostr.Tags{{kinds.CASControlStateTagDomain, kinds.VirtualizationDomain}, {kinds.CASControlStateTagEntity, entity}, {kinds.CASControlStateTagSchema, schema}, {kinds.VirtualizationTagOrg, c.OrgID.String()}, {kinds.VirtualizationTagGeneration, strconv.FormatInt(c.Generation, 10)}, {kinds.VirtualizationTagSequence, strconv.FormatInt(c.Sequence, 10)}, {kinds.VirtualizationTagJournal, journalKey}}
	for _, class := range v.LifecycleClasses {
		tags = append(tags, nostr.Tag{kinds.VirtualizationTagClass, string(class)})
	}
	at := p.now().UTC()
	if state {
		tags = append(tags, nostr.Tag{kinds.CASControlStateTagD, coordinate})
		prev, err := p.store.FindLatestByKindPubkeyDTag(ctx, kind, p.author, coordinate, "")
		if err != nil {
			return err
		}
		if prev != nil && !at.Truncate(time.Second).After(prev.CreatedAt) {
			at = prev.CreatedAt.Add(time.Second)
			if err := p.waitUntil(ctx, at); err != nil {
				return err
			}
		}
	} else {
		tags = append(tags, nostr.Tag{"type", c.ChangeType}, nostr.Tag{"state", coordinate}, nostr.Tag{"protected", "true"})
	}
	if v.Actor != "" {
		tags = append(tags, nostr.Tag{"p", v.Actor})
	}
	if v.Operation != nil {
		tags = append(tags, nostr.Tag{"correlation", v.Operation.CorrelationID.String()})
	}
	content, err := json.Marshal(struct {
		Schema     string                      `json:"schema"`
		Sequence   int64                       `json:"sequence"`
		ChangeType string                      `json:"change_type"`
		OccurredAt time.Time                   `json:"occurred_at"`
		ApprovalID *uuid.UUID                  `json:"approval_id,omitempty"`
		Resource   *dto.VirtualizationResource `json:"resource"`
	}{schema, c.Sequence, c.ChangeType, c.OccurredAt, c.ApprovalID, v})
	if err != nil {
		return err
	}
	ev := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Timestamp(at.Unix()), Tags: tags, Content: string(content)}
	publishErr := p.publisher.PublishSignedEvent(ctx, ev)
	// A relay failure is not data loss after durable outbox admission. Its existing
	// worker retries the identical signed event. Never advance on signing/DB failure.
	rec, err := p.store.GetByID(ctx, ev.ID.Hex())
	if err != nil {
		return err
	}
	if rec == nil || rec.PubKey != p.author || (rec.PublishState != repository.NostrPublishStatePending && rec.PublishState != repository.NostrPublishStatePublished) {
		if publishErr != nil {
			return publishErr
		}
		return ErrVirtualizationUnavailable
	}
	return nil
}
