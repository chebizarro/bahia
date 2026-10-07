package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

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

// topicMigrationStore is the daemon's real local event store in a temp dir.
func topicMigrationStore(t *testing.T) *localstore.Store {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedSessionRecord(t *testing.T, store *localstore.Store, signer nostr.Signer, sessionID, schema string, createdAt int64, tagged bool) nostr.Event {
	t.Helper()
	content := map[string]any{"schema": schema, "session_id": sessionID, "state": "executing"}
	raw, _ := json.Marshal(content)
	tags := nostr.Tags{
		{"d", schema + ":" + sessionID},
		{domain.AssistantSessionTagSchema, schema},
		{"session", sessionID},
		{"p", "operator-pubkey", "", "operator"},
	}
	if tagged {
		tags = append(tags, nostr.Tag{"t", kinds.AssistantSessionTopic})
	}
	ev := nostr.Event{
		Kind:      domain.KindAssistantSessionState,
		CreatedAt: nostr.Timestamp(createdAt),
		Tags:      tags,
		Content:   string(raw),
	}
	if err := signer.SignEvent(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveEvent(ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func hasAssistantTopic(ev nostr.Event) bool {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == kinds.AssistantSessionTopic {
			return true
		}
	}
	return false
}

func dTagOf(ev nostr.Event) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			return tag[1]
		}
	}
	return ""
}

// TestAssistantSessionTopicMigrationRetagsLegacyRecords verifies the upgrade
// scenario: an untagged active session is re-published with the t tag.
func TestAssistantSessionTopicMigrationRetagsLegacyRecords(t *testing.T) {
	signer, pubkey := topicMigrationSigner(t)
	store := topicMigrationStore(t)
	publisher := &topicMigrationPublisher{}
	seedSessionRecord(t, store, signer, "s-active", domain.AssistantSessionSchemaV2, 1000, false)

	m := NewAssistantSessionTopicMigration(AssistantSessionTopicMigrationConfig{
		LocalStore: store, Signer: signer, Publisher: publisher, ServicePubkey: pubkey,
	})
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	events := publisher.events()
	if len(events) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(events))
	}
	ev := events[0]
	if !hasAssistantTopic(ev) {
		t.Fatal("republished event missing t=assistant-session tag")
	}
	if ev.CreatedAt != 1001 {
		t.Fatalf("expected created_at=1001, got %d", ev.CreatedAt)
	}
	if ev.Content == "" {
		t.Fatal("content empty")
	}
	if got, want := dTagOf(ev), domain.AssistantSessionSchemaV2+":s-active"; got != want {
		t.Fatalf("d tag = %q, want %q", got, want)
	}
	if !ev.VerifySignature() {
		t.Fatal("republished event is not validly signed")
	}
}

// TestAssistantSessionTopicMigrationIdempotent verifies that running twice is
// a no-op when all records are already tagged.
func TestAssistantSessionTopicMigrationIdempotent(t *testing.T) {
	signer, pubkey := topicMigrationSigner(t)
	store := topicMigrationStore(t)
	publisher := &topicMigrationPublisher{}
	seedSessionRecord(t, store, signer, "s-tagged", domain.AssistantSessionSchemaV2, 1000, true)

	m := NewAssistantSessionTopicMigration(AssistantSessionTopicMigrationConfig{
		LocalStore: store, Signer: signer, Publisher: publisher, ServicePubkey: pubkey,
	})
	for run := 0; run < 2; run++ {
		if err := m.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(publisher.events()) != 0 {
			t.Fatalf("run %d: expected 0 published events for already-tagged record, got %d", run, len(publisher.events()))
		}
	}
}

// TestAssistantSessionTopicMigrationBothSchemas verifies that both V1 and V2
// untagged records are migrated and other 30900 families are left alone.
func TestAssistantSessionTopicMigrationBothSchemas(t *testing.T) {
	signer, pubkey := topicMigrationSigner(t)
	store := topicMigrationStore(t)
	publisher := &topicMigrationPublisher{}
	seedSessionRecord(t, store, signer, "s-v1", domain.AssistantSessionSchema, 1000, false)
	seedSessionRecord(t, store, signer, "s-v2", domain.AssistantSessionSchemaV2, 2000, false)
	other := nostr.Event{Kind: domain.KindAssistantSessionState, CreatedAt: 3000, Tags: nostr.Tags{{"d", "service:x"}, {domain.AssistantSessionTagSchema, "bahia.other.v1"}}, Content: "{}"}
	if err := signer.SignEvent(context.Background(), &other); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveEvent(other); err != nil {
		t.Fatal(err)
	}

	m := NewAssistantSessionTopicMigration(AssistantSessionTopicMigrationConfig{
		LocalStore: store, Signer: signer, Publisher: publisher, ServicePubkey: pubkey,
	})
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := publisher.events()
	if len(events) != 2 {
		t.Fatalf("expected 2 published events, got %d", len(events))
	}
	for _, ev := range events {
		if !hasAssistantTopic(ev) {
			t.Fatalf("republished event missing t tag: %v", ev.Tags)
		}
	}
}

// TestAssistantSessionTopicMigrationSkipsOtherAuthors verifies that records
// from other pubkeys are ignored.
func TestAssistantSessionTopicMigrationSkipsOtherAuthors(t *testing.T) {
	signer, pubkey := topicMigrationSigner(t)
	otherSigner, _ := topicMigrationSigner(t)
	store := topicMigrationStore(t)
	publisher := &topicMigrationPublisher{}
	seedSessionRecord(t, store, otherSigner, "s-other", domain.AssistantSessionSchemaV2, 1000, false)

	m := NewAssistantSessionTopicMigration(AssistantSessionTopicMigrationConfig{
		LocalStore: store, Signer: signer, Publisher: publisher, ServicePubkey: pubkey,
	})
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events()) != 0 {
		t.Fatalf("expected 0 published events for other-author record, got %d", len(publisher.events()))
	}
}

// TestAssistantSessionTopicMigrationMigratesMoreThanFiveHundred proves the
// enumeration is complete: with more untagged legacy records than the old
// bounded query returned, every one is migrated, including the oldest, and
// nothing is migrated twice.
func TestAssistantSessionTopicMigrationMigratesMoreThanFiveHundred(t *testing.T) {
	const legacy = 650
	signer, pubkey := topicMigrationSigner(t)
	store := topicMigrationStore(t)
	publisher := &topicMigrationPublisher{}
	want := map[string]struct{}{}
	for i := 0; i < legacy; i++ {
		schema := domain.AssistantSessionSchemaV2
		if i%3 == 0 {
			schema = domain.AssistantSessionSchema
		}
		ev := seedSessionRecord(t, store, signer, fmt.Sprintf("s-%04d", i), schema, int64(1000+i), false)
		want[dTagOf(ev)] = struct{}{}
	}
	for i := 0; i < 20; i++ {
		seedSessionRecord(t, store, signer, fmt.Sprintf("tagged-%02d", i), domain.AssistantSessionSchemaV2, int64(5000+i), true)
	}

	m := NewAssistantSessionTopicMigration(AssistantSessionTopicMigrationConfig{
		LocalStore: store, Signer: signer, Publisher: publisher, ServicePubkey: pubkey,
	})
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := publisher.events()
	if len(events) != legacy {
		t.Fatalf("migrated %d records, want all %d", len(events), legacy)
	}
	got := map[string]struct{}{}
	for _, ev := range events {
		if !hasAssistantTopic(ev) {
			t.Fatalf("republished event missing t tag: %v", ev.Tags)
		}
		d := dTagOf(ev)
		if _, dup := got[d]; dup {
			t.Fatalf("record %s migrated twice", d)
		}
		got[d] = struct{}{}
		if _, ok := want[d]; !ok {
			t.Fatalf("unexpected record %s migrated", d)
		}
	}
	if _, ok := got[domain.AssistantSessionSchema+":s-0000"]; !ok {
		t.Fatal("the oldest legacy record was not migrated")
	}
}
