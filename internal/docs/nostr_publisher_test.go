package docs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// mockEventPublisher captures published events.
type mockEventPublisher struct {
	mu     sync.Mutex
	events []*nostr.Event
	err    error
}

func (m *mockEventPublisher) PublishSignedEvent(_ context.Context, ev *nostr.Event) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
	return nil
}

func (m *mockEventPublisher) published() []*nostr.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*nostr.Event(nil), m.events...)
}

func TestNostrDocsPublisher_SyncToRelay_PublishesAllTopics(t *testing.T) {
	dir := t.TempDir()
	writeTestDoc(t, dir, "getting-started.md", "# Getting Started\n\nWelcome to Bahia.")
	writeTestDoc(t, dir, "features/services.md", "# Services\n\nManage your services.")

	svc := New(dir)
	pub := &mockEventPublisher{}
	publisher := NewNostrDocsPublisher(svc, pub, nil, zap.NewNop())

	if err := publisher.SyncToRelay(context.Background()); err != nil {
		t.Fatalf("SyncToRelay failed: %v", err)
	}

	events := pub.published()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	for _, ev := range events {
		if ev.Kind != kinds.LongFormContent {
			t.Errorf("expected kind %d, got %d", kinds.LongFormContent, ev.Kind)
		}

		dTag := tagValue(ev.Tags, "d")
		title := tagValue(ev.Tags, "title")
		category := tagValue(ev.Tags, "t")
		hash := tagValue(ev.Tags, "content-hash")

		if dTag == "" {
			t.Error("missing d tag")
		}
		if title == "" {
			t.Error("missing title tag")
		}
		if category == "" {
			t.Error("missing category/t tag")
		}
		if hash == "" {
			t.Error("missing content-hash tag")
		}
		if ev.Content == "" {
			t.Error("empty content")
		}
	}
}

func TestNostrDocsPublisher_SyncToRelay_SkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeTestDoc(t, dir, "getting-started.md", "# Getting Started\n\nWelcome.")

	svc := New(dir)
	content := "# Getting Started\n\nWelcome."
	hash := contentHash(content)

	// Querier returns existing event with matching hash.
	querier := DocsQuerierFunc(func(_ context.Context, _ string) ([]*nostr.Event, error) {
		return []*nostr.Event{{
			Kind:    kinds.LongFormContent,
			Content: content,
			Tags: nostr.Tags{
				{"d", "getting-started"},
				{"content-hash", hash},
			},
		}}, nil
	})

	pub := &mockEventPublisher{}
	publisher := NewNostrDocsPublisher(svc, pub, querier, zap.NewNop())

	if err := publisher.SyncToRelay(context.Background()); err != nil {
		t.Fatalf("SyncToRelay failed: %v", err)
	}

	events := pub.published()
	if len(events) != 0 {
		t.Fatalf("expected 0 events (all skipped), got %d", len(events))
	}
}

func TestNostrDocsPublisher_SyncToRelay_PublishesChangedOnly(t *testing.T) {
	dir := t.TempDir()
	writeTestDoc(t, dir, "getting-started.md", "# Getting Started\n\nWelcome.")
	writeTestDoc(t, dir, "core-concepts.md", "# Core Concepts\n\nUpdated content.")

	svc := New(dir)

	// Querier returns existing event with old hash for core-concepts, matching for getting-started.
	gettingStartedHash := contentHash("# Getting Started\n\nWelcome.")
	querier := DocsQuerierFunc(func(_ context.Context, _ string) ([]*nostr.Event, error) {
		return []*nostr.Event{
			{
				Kind: kinds.LongFormContent,
				Tags: nostr.Tags{
					{"d", "getting-started"},
					{"content-hash", gettingStartedHash},
				},
			},
			{
				Kind: kinds.LongFormContent,
				Tags: nostr.Tags{
					{"d", "core-concepts"},
					{"content-hash", "old-hash-that-differs"},
				},
			},
		}, nil
	})

	pub := &mockEventPublisher{}
	publisher := NewNostrDocsPublisher(svc, pub, querier, zap.NewNop())

	if err := publisher.SyncToRelay(context.Background()); err != nil {
		t.Fatalf("SyncToRelay failed: %v", err)
	}

	events := pub.published()
	if len(events) != 1 {
		t.Fatalf("expected 1 event (only changed), got %d", len(events))
	}

	dTag := tagValue(events[0].Tags, "d")
	if dTag != "core-concepts" {
		t.Errorf("expected changed topic 'core-concepts', got %q", dTag)
	}
}

func TestNostrDocsPublisher_SyncToRelay_EmptyCatalog(t *testing.T) {
	dir := t.TempDir()
	svc := New(dir)
	pub := &mockEventPublisher{}
	publisher := NewNostrDocsPublisher(svc, pub, nil, zap.NewNop())

	if err := publisher.SyncToRelay(context.Background()); err != nil {
		t.Fatalf("SyncToRelay failed: %v", err)
	}

	if len(pub.published()) != 0 {
		t.Fatal("expected no events for empty catalog")
	}
}

func TestNostrDocsPublisher_SyncToRelay_NilPublisher(t *testing.T) {
	dir := t.TempDir()
	svc := New(dir)
	publisher := NewNostrDocsPublisher(svc, nil, nil, zap.NewNop())

	if err := publisher.Run(context.Background()); err != nil {
		t.Fatalf("Run with nil publisher should succeed, got: %v", err)
	}
}

func TestNostrDocsPublisher_NIP23EventStructure(t *testing.T) {
	dir := t.TempDir()
	writeTestDoc(t, dir, "test-topic.md", "# Test Topic\n\nSome content here.")

	svc := New(dir)
	pub := &mockEventPublisher{}
	publisher := NewNostrDocsPublisher(svc, pub, nil, zap.NewNop())

	if err := publisher.SyncToRelay(context.Background()); err != nil {
		t.Fatalf("SyncToRelay failed: %v", err)
	}

	events := pub.published()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]

	// Verify NIP-23 structure.
	if ev.Kind != 30023 {
		t.Errorf("kind: want 30023, got %d", ev.Kind)
	}
	if ev.Content != "# Test Topic\n\nSome content here." {
		t.Errorf("content mismatch: %q", ev.Content)
	}
	if tagValue(ev.Tags, "d") != "test-topic" {
		t.Errorf("d tag: want 'test-topic', got %q", tagValue(ev.Tags, "d"))
	}
	if tagValue(ev.Tags, "title") != "Test Topic" {
		t.Errorf("title tag: want 'Test Topic', got %q", tagValue(ev.Tags, "title"))
	}
	if tagValue(ev.Tags, "summary") == "" {
		t.Error("missing summary tag")
	}
	if tagValue(ev.Tags, "published_at") == "" {
		t.Error("missing published_at tag")
	}

	// Verify bahia-docs tag is present.
	found := false
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == "bahia-docs" {
			found = true
			break
		}
	}
	if !found {
		t.Error("missing 'bahia-docs' t tag")
	}
}

func TestContentHash_Deterministic(t *testing.T) {
	content := "# Hello World\n\nSome content."
	h1 := contentHash(content)
	h2 := contentHash(content)
	if h1 != h2 {
		t.Errorf("hash not deterministic: %s != %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex hash, got %d chars", len(h1))
	}

	different := contentHash("# Different Content")
	if h1 == different {
		t.Error("different content should produce different hash")
	}
}

// writeTestDoc creates a markdown file in the test docs directory.
func writeTestDoc(t *testing.T, baseDir, relPath, content string) {
	t.Helper()
	fullPath := baseDir + "/" + relPath

	// Create parent directories.
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
