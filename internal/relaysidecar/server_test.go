package relaysidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestSidecarRejectsBroadRequestKindReadsWithoutAuthorizedAuthors(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	reject, msg := server.Relay().OnRequest(context.Background(), nostr.Filter{Kinds: []nostr.Kind{5963}})
	if !reject {
		t.Fatalf("expected request kind read filter to be rejected")
	}
	if msg == "" {
		t.Fatalf("expected rejection message")
	}
}

func TestSidecarRejectsAuthorScopedLegacyRequestKindReadsForAuthorizedOperators(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	sk := nostr.Generate()
	pubkey := nostr.GetPublicKey(sk)
	cfg.AuthorizedPubkeys = []string{pubkey.Hex()}

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	filter := nostr.Filter{Kinds: []nostr.Kind{5963, 5978, 5979, 5997, 6005, 38390, 38400, 38420, 38421, 38430}, Authors: []nostr.PubKey{pubkey}}
	reject, msg := server.Relay().OnRequest(context.Background(), filter)
	if !reject {
		t.Fatalf("expected author-scoped legacy request kind read filter to be rejected after migration boundary")
	}
	if msg == "" {
		t.Fatalf("expected rejection message")
	}

	encryptedFilter := nostr.Filter{Kinds: []nostr.Kind{5980}, Authors: []nostr.PubKey{pubkey}}
	reject, msg = server.Relay().OnRequest(context.Background(), encryptedFilter)
	if !reject {
		t.Fatalf("expected encrypted request kind read filter to remain blocked")
	}
}

func TestSidecarAllowsCanonicalStatusAndRejectsLegacyStatusResultKinds(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	canonicalFilter := nostr.Filter{Kinds: []nostr.Kind{30315, 30900, 4903}}
	reject, msg := server.Relay().OnRequest(context.Background(), canonicalFilter)
	if reject {
		t.Fatalf("expected canonical status/state/audit kinds to be readable, got rejection %q", msg)
	}

	legacyFilter := nostr.Filter{Kinds: []nostr.Kind{6963, 6978, 6981, 6984, 6997, 7962, 7978, 7979, 7997, 30350, 30353, 31310, 31311, 38395, 38410, 38422, 38423, 32000, 32003}}
	reject, msg = server.Relay().OnRequest(context.Background(), legacyFilter)
	if !reject {
		t.Fatalf("expected legacy signer-first operator status/result/read-model kinds to be rejected after migration boundary")
	}
	if msg == "" {
		t.Fatalf("expected rejection message")
	}
}

func TestSidecarAllowsDiscoveryKinds(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	filter := nostr.Filter{Kinds: []nostr.Kind{10002, 30002, 30078, 11316, 11317, 11318, 11319, 11320, 31410, 31411, 30360}}
	reject, msg := server.Relay().OnRequest(context.Background(), filter)
	if reject {
		t.Fatalf("expected canonical discovery/SBOM kinds to be readable, got rejection %q", msg)
	}

	legacyFilter := nostr.Filter{Kinds: []nostr.Kind{30079, 31400, 31404, 31974, 31975, 31976, 31977, 31978, 31991, 31999}}
	reject, msg = server.Relay().OnRequest(context.Background(), legacyFilter)
	if !reject {
		t.Fatalf("expected legacy discovery/read-model kinds to be rejected after migration boundary")
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

func TestSidecarServesWebsocketAtRootAndConfiguredPath(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334/relay"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	for _, path := range []string{"/", "/relay"} {
		req, err := http.NewRequest(http.MethodGet, httpServer.URL+path, nil)
		if err != nil {
			t.Fatalf("new request %q: %v", path, err)
		}
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %q: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("status at %q = %d, expected non-404 websocket handling", path, resp.StatusCode)
		}
	}
}

func TestSidecarServesNIP11OnConfiguredPath(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334/relay"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	req, err := http.NewRequest(http.MethodGet, httpServer.URL+"/relay", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "application/nostr+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /relay: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
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
