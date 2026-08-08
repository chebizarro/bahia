package relaysidecar

import (
	"context"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestSweepRetentionDeletesExpiredEventsAndKeepsFreshEvents(t *testing.T) {
	store, err := newSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close SQLite store: %v", err)
		}
	})

	now := time.Unix(2_000_000, 0)
	testCases := []struct {
		content     string
		kind        int
		age         time.Duration
		keep        bool
		replaceable bool
	}{
		{content: "expired ContextVM message", kind: kinds.ContextVMMessage, age: 25 * time.Hour},
		{content: "expired gift wrap", kind: kinds.ContextVMGiftWrap, age: 25 * time.Hour},
		{content: "expired ephemeral gift wrap", kind: kinds.ContextVMEphemeralGiftWrap, age: 25 * time.Hour},
		{content: "fresh ContextVM message", kind: kinds.ContextVMMessage, age: 23 * time.Hour, keep: true},
		{content: "expired durable event", kind: 30900, age: 8 * 24 * time.Hour},
		{content: "fresh durable event", kind: 30900, age: 6 * 24 * time.Hour, keep: true},
		{content: "old current Soul read model", kind: 31951, age: 30 * 24 * time.Hour, keep: true, replaceable: true},
	}
	sk := nostr.Generate()
	for _, tc := range testCases {
		event := nostr.Event{
			CreatedAt: nostr.Timestamp(now.Add(-tc.age).Unix()),
			Kind:      nostr.Kind(tc.kind),
			Content:   tc.content,
		}
		if tc.replaceable {
			event.Tags = nostr.Tags{{"d", "marjam"}}
		}
		if err := event.Sign(sk); err != nil {
			t.Fatalf("sign %q: %v", tc.content, err)
		}
		save := store.Save
		if tc.replaceable {
			save = store.Replace
		}
		if err := save(context.Background(), event); err != nil {
			t.Fatalf("save %q: %v", tc.content, err)
		}
	}

	deleted, err := store.SweepRetention(context.Background(), now, 7*24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("sweep retention: %v", err)
	}
	if deleted != 4 {
		t.Fatalf("expected 4 expired events deleted, got %d", deleted)
	}

	got := make(map[string]bool)
	for event := range store.Query(context.Background(), nostr.Filter{}, 0) {
		got[event.Content] = true
	}
	for _, tc := range testCases {
		if got[tc.content] != tc.keep {
			t.Errorf("event %q retained=%v, want %v", tc.content, got[tc.content], tc.keep)
		}
	}
}

func TestRelayQuerySQLScopesCanonicalDiscoveryBeforeReplay(t *testing.T) {
	author := nostr.PubKey{1}
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{11316, 30002},
		Authors: []nostr.PubKey{author},
		Since:   100,
		Until:   200,
	}

	query, args := relayQuerySQL(filter)
	for _, fragment := range []string{
		"kind IN (?,?)",
		"pubkey IN (?)",
		"created_at >= ?",
		"created_at <= ?",
		"ORDER BY created_at DESC, id",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query %q missing %q", query, fragment)
		}
	}
	if len(args) != 5 {
		t.Fatalf("expected 5 query arguments, got %d: %#v", len(args), args)
	}
}

func TestRelayQuerySQLEmptyExplicitKindsMatchesNothing(t *testing.T) {
	query, _ := relayQuerySQL(nostr.Filter{Kinds: []nostr.Kind{}})
	if !strings.Contains(query, "1 = 0") {
		t.Fatalf("explicit empty kinds must match nothing: %q", query)
	}
}
