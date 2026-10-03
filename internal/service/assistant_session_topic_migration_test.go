package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

// topicMigrationStore is a minimal in-memory NostrEventRepository for testing.
type topicMigrationStore struct {
	mu      sync.Mutex
	records []repository.NostrEventRecord
}

func (s *topicMigrationStore) Record(_ context.Context, rec *repository.NostrEventRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, *rec)
	return true, nil
}

func (s *topicMigrationStore) GetByID(_ context.Context, id string) (*repository.NostrEventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.records {
		if rec.ID == id {
			return &rec, nil
		}
	}
	return nil, nil
}

func (s *topicMigrationStore) ListByKind(_ context.Context, kind int, limit int) ([]repository.NostrEventRecord, error) {
	return s.ListByKinds(context.Background(), []int{kind}, limit)
}

func (s *topicMigrationStore) ListByKinds(_ context.Context, ks []int, limit int) ([]repository.NostrEventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kindSet := map[int]bool{}
	for _, k := range ks {
		kindSet[k] = true
	}
	var out []repository.NostrEventRecord
	for _, rec := range s.records {
		if kindSet[rec.Kind] {
			out = append(out, rec)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *topicMigrationStore) FindByTag(_ context.Context, tagName, tagValue string, ks []int, limit int) ([]repository.NostrEventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kindSet := map[int]bool{}
	for _, k := range ks {
		kindSet[k] = true
	}
	var out []repository.NostrEventRecord
	for _, rec := range s.records {
		if len(ks) > 0 && !kindSet[rec.Kind] {
			continue
		}
		var tags nostr.Tags
		if err := json.Unmarshal(rec.Tags, &tags); err != nil {
			continue
		}
		for _, tag := range tags {
			if len(tag) >= 2 && tag[0] == tagName && tag[1] == tagValue {
				out = append(out, rec)
				break
			}
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *topicMigrationStore) ListByEntity(_ context.Context, _ string, _ uuid.UUID, _ int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}

func (s *topicMigrationStore) LatestCreatedAtForKinds(_ context.Context, _ []int) (*time.Time, error) {
	return nil, nil
}

func (s *topicMigrationStore) LatestCreatedAtForKindsAndAuthors(_ context.Context, _ []int, _ []string) (*time.Time, error) {
	return nil, nil
}

// topicMigrationPublisher captures published events.
type topicMigrationPublisher struct {
	mu        sync.Mutex
	published []nostr.Event
}

func (p *topicMigrationPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, ev)
	return 1, nil
}

func (p *topicMigrationPublisher) events() []nostr.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]nostr.Event, len(p.published))
	copy(out, p.published)
	return out
}

func topicMigrationSigner(t *testing.T) (nostr.Signer, string) {
	t.Helper()
	sk := nostr.Generate()
	signer := keyer.NewPlainKeySigner(sk)
	pub, err := signer.GetPublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return signer, pub.Hex()
}

func makeUntaggedSessionRecord(t *testing.T, signer nostr.Signer, pubkey, sessionID, schema string, createdAt int64) repository.NostrEventRecord {
	t.Helper()
	content := map[string]any{"schema": schema, "session_id": sessionID, "state": "executing"}
	raw, _ := json.Marshal(content)
	tags := nostr.Tags{
		{"d", schema + ":" + sessionID},
		{domain.AssistantSessionTagSchema, schema},
		{"session", sessionID},
		{"p", "operator-pubkey", "", "operator"},
	}
	ev := nostr.Event{
		Kind:      nostr.Kind(domain.KindAssistantSessionState),
		CreatedAt: nostr.Timestamp(createdAt),
		Tags:      tags,
		Content:   string(raw),
	}
	if err := signer.SignEvent(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	return repository.NostrEventRecord{
		ID:        ev.ID.Hex(),
		Kind:      int(ev.Kind),
		PubKey:    pubkey,
		Content:   ev.Content,
		Tags:      tagsJSON,
		Sig:       hex.EncodeToString(ev.Sig[:]),
		CreatedAt: ev.CreatedAt.Time(),
	}
}

func makeTaggedSessionRecord(t *testing.T, signer nostr.Signer, pubkey, sessionID, schema string, createdAt int64) repository.NostrEventRecord {
	t.Helper()
	content := map[string]any{"schema": schema, "session_id": sessionID, "state": "executing"}
	raw, _ := json.Marshal(content)
	tags := nostr.Tags{
		{"d", schema + ":" + sessionID},
		{domain.AssistantSessionTagSchema, schema},
		{"t", kinds.AssistantSessionTopic},
		{"session", sessionID},
		{"p", "operator-pubkey", "", "operator"},
	}
	ev := nostr.Event{
		Kind:      nostr.Kind(domain.KindAssistantSessionState),
		CreatedAt: nostr.Timestamp(createdAt),
		Tags:      tags,
		Content:   string(raw),
	}
	if err := signer.SignEvent(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	return repository.NostrEventRecord{
		ID:        ev.ID.Hex(),
		Kind:      int(ev.Kind),
		PubKey:    pubkey,
		Content:   ev.Content,
		Tags:      tagsJSON,
		Sig:       hex.EncodeToString(ev.Sig[:]),
		CreatedAt: ev.CreatedAt.Time(),
	}
}

// TestAssistantSessionTopicMigrationRetagsLegacyRecords verifies the upgrade
// scenario: an untagged active session is re-published with the t tag.
func TestAssistantSessionTopicMigrationRetagsLegacyRecords(t *testing.T) {
	signer, pubkey := topicMigrationSigner(t)
	store := &topicMigrationStore{}
	publisher := &topicMigrationPublisher{}

	// Seed an untagged V2 session.
	store.records = append(store.records,
		makeUntaggedSessionRecord(t, signer, pubkey, "s-active", domain.AssistantSessionSchemaV2, 1000),
	)

	m := NewAssistantSessionTopicMigration(AssistantSessionTopicMigrationConfig{
		History:       store,
		Signer:        signer,
		Publisher:     publisher,
		ServicePubkey: pubkey,
	})
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	events := publisher.events()
	if len(events) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(events))
	}
	ev := events[0]
	// Verify the t tag is present.
	found := false
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == kinds.AssistantSessionTopic {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("republished event missing t=assistant-session tag")
	}
	// Verify created_at is bumped.
	if ev.CreatedAt != 1001 {
		t.Fatalf("expected created_at=1001, got %d", ev.CreatedAt)
	}
	// Verify content is preserved.
	if ev.Content == "" {
		t.Fatal("content empty")
	}
	// Verify the d coordinate is preserved.
	dTag := ""
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			dTag = tag[1]
			break
		}
	}
	if dTag != domain.AssistantSessionSchemaV2+":s-active" {
		t.Fatalf("d tag = %q, want %q", dTag, domain.AssistantSessionSchemaV2+":s-active")
	}
}

// TestAssistantSessionTopicMigrationIdempotent verifies that running twice is
// a no-op when all records are already tagged.
func TestAssistantSessionTopicMigrationIdempotent(t *testing.T) {
	signer, pubkey := topicMigrationSigner(t)
	store := &topicMigrationStore{}
	publisher := &topicMigrationPublisher{}

	// Seed an already-tagged session.
	store.records = append(store.records,
		makeTaggedSessionRecord(t, signer, pubkey, "s-tagged", domain.AssistantSessionSchemaV2, 1000),
	)

	m := NewAssistantSessionTopicMigration(AssistantSessionTopicMigrationConfig{
		History:       store,
		Signer:        signer,
		Publisher:     publisher,
		ServicePubkey: pubkey,
	})

	// First run: no-op because the record is already tagged.
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events()) != 0 {
		t.Fatalf("expected 0 published events for already-tagged record, got %d", len(publisher.events()))
	}

	// Second run: still a no-op.
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events()) != 0 {
		t.Fatalf("expected 0 published events on second run, got %d", len(publisher.events()))
	}
}

// TestAssistantSessionTopicMigrationBothSchemas verifies that both V1 and V2
// untagged records are migrated.
func TestAssistantSessionTopicMigrationBothSchemas(t *testing.T) {
	signer, pubkey := topicMigrationSigner(t)
	store := &topicMigrationStore{}
	publisher := &topicMigrationPublisher{}

	store.records = append(store.records,
		makeUntaggedSessionRecord(t, signer, pubkey, "s-v1", domain.AssistantSessionSchema, 1000),
		makeUntaggedSessionRecord(t, signer, pubkey, "s-v2", domain.AssistantSessionSchemaV2, 2000),
	)

	m := NewAssistantSessionTopicMigration(AssistantSessionTopicMigrationConfig{
		History:       store,
		Signer:        signer,
		Publisher:     publisher,
		ServicePubkey: pubkey,
	})
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := publisher.events()
	if len(events) != 2 {
		t.Fatalf("expected 2 published events, got %d", len(events))
	}
	for _, ev := range events {
		found := false
		for _, tag := range ev.Tags {
			if len(tag) >= 2 && tag[0] == "t" && tag[1] == kinds.AssistantSessionTopic {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("republished event missing t tag: %v", ev.Tags)
		}
	}
}

// TestAssistantSessionTopicMigrationSkipsOtherAuthors verifies that records
// from other pubkeys are ignored.
func TestAssistantSessionTopicMigrationSkipsOtherAuthors(t *testing.T) {
	signer, pubkey := topicMigrationSigner(t)
	otherSigner, otherPubkey := topicMigrationSigner(t)
	store := &topicMigrationStore{}
	publisher := &topicMigrationPublisher{}

	// Create a record signed by otherSigner but attributed to otherPubkey.
	store.records = append(store.records,
		makeUntaggedSessionRecord(t, otherSigner, otherPubkey, "s-other", domain.AssistantSessionSchemaV2, 1000),
	)

	m := NewAssistantSessionTopicMigration(AssistantSessionTopicMigrationConfig{
		History:       store,
		Signer:        signer,
		Publisher:     publisher,
		ServicePubkey: pubkey,
	})
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events()) != 0 {
		t.Fatalf("expected 0 published events for other-author record, got %d", len(publisher.events()))
	}
}
