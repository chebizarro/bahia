package relaysidecar

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

func TestSidecarAcceptsAndQueriesSignedInteropEvent(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.MaxQueryLimit = 100

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	sk := nostr.Generate()
	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      10100,
		Tags:      nostr.Tags{nostr.Tag{"agent", "smoke"}},
		Content:   `{"status":"ready"}`,
	}
	if err := event.Sign(sk); err != nil {
		t.Fatalf("sign event: %v", err)
	}

	skipBroadcast, err := server.Relay().AddEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("AddEvent() error: %v", err)
	}
	if skipBroadcast {
		t.Fatalf("AddEvent() skipBroadcast = true")
	}

	var found bool
	for stored := range server.Relay().QueryStored(context.Background(), nostr.Filter{Kinds: []nostr.Kind{10100}}) {
		if stored.ID == event.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("stored event %s was not returned by QueryStored", event.ID.Hex())
	}
}

func TestSidecarRejectsBroadReadFilters(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if reject, _ := server.Relay().OnRequest(context.Background(), nostr.Filter{}); !reject {
		t.Fatalf("expected broad read filter without kinds to be rejected")
	}
}

func TestSidecarDoesNotExposeRequestKindsToPublicReads(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	reject, msg := server.Relay().OnRequest(context.Background(), nostr.Filter{Kinds: []nostr.Kind{5961}})
	if !reject {
		t.Fatalf("expected request kind read filter to be rejected")
	}
	if msg == "" {
		t.Fatalf("expected rejection message")
	}
}

func TestSidecarCountIsNotCappedByQueryLimit(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.MaxQueryLimit = 1

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	for i := 0; i < 2; i++ {
		sk := nostr.Generate()
		event := nostr.Event{CreatedAt: nostr.Now(), Kind: 10100, Content: `{}`}
		if err := event.Sign(sk); err != nil {
			t.Fatalf("sign event: %v", err)
		}
		if _, err := server.Relay().AddEvent(context.Background(), event); err != nil {
			t.Fatalf("AddEvent() error: %v", err)
		}
	}

	count, err := server.Relay().Count(context.Background(), nostr.Filter{Kinds: []nostr.Kind{10100}})
	if err != nil {
		t.Fatalf("Count() error: %v", err)
	}
	if count != 2 {
		t.Fatalf("Count() = %d, want 2", count)
	}
}

func TestSidecarRejectsUnauthorizedRequestKind(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	sk := nostr.Generate()
	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      5961,
		Tags:      nostr.Tags{nostr.Tag{"t", "task-1"}},
		Content:   `{}`,
	}
	if err := event.Sign(sk); err != nil {
		t.Fatalf("sign event: %v", err)
	}

	if _, err := server.Relay().AddEvent(context.Background(), event); err == nil {
		t.Fatalf("expected unauthorized request kind to be rejected")
	}
}
