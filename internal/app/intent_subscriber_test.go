package app

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

// testDomainHandler is a mock DomainHandler that records the intents it
// receives, for verifying the wiring in app-level tests.
type testDomainHandler struct {
	mu      sync.Mutex
	intents []*controlplane.Intent
}

func (h *testDomainHandler) HandleIntent(_ context.Context, intent *controlplane.Intent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.intents = append(h.intents, intent)
	return nil
}

func (h *testDomainHandler) PermissionFor(_ string) domain.Permission {
	return domain.PermWriteServices
}

func (h *testDomainHandler) received() []*controlplane.Intent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*controlplane.Intent(nil), h.intents...)
}

// intentSubscriberTestConfig gives each test its own local store. These tests
// submit intents with fixed intent ids, and the processed-intent ledger lives
// in the local store: sharing the package-wide store made every run after the
// first (go test -count=N) skip the intent as already processed.
func intentSubscriberTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := startupTestConfig("emergency")
	cfg.Nostr.LocalStore.Path = filepath.Join(t.TempDir(), "daemon.bolt")
	return cfg
}

// TestIntentSubscriberWiredWhenDomainsEnabled verifies that with a test domain
// enabled, the intent subscriber is constructed and the processor pipeline
// delivers a signed intent to the registered handler, with readiness tracking.
func TestIntentSubscriberWiredWhenDomainsEnabled(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := intentSubscriberTestConfig(t)
	// Generate a separate keypair for the intent actor.
	actorKey := nostr.Generate()
	actorPubkey := actorKey.Public().Hex()
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	// Configure the actor as a bootstrap owner for the test org so the
	// TrustSet grants it org-level permissions.
	cfg.Nostr.AuthorizedPubkeys = []string{actorPubkey}
	cfg.Nostr.BootstrapOwners = map[string]string{orgID.String(): actorPubkey}

	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	// Subscriber is wired when domains are enabled.
	require.NotNil(t, app.IntentSubscriber, "IntentSubscriber should be wired when domains are enabled")
	require.NotNil(t, app.IntentProcessor, "IntentProcessor should be wired")
	require.NotNil(t, app.TrustSet, "TrustSet should be wired")
	require.NotNil(t, app.IntentReadiness, "IntentReadiness should be wired")

	// Readiness starts not-ready (no catch-up yet).
	require.False(t, app.IntentReadiness.IsReady(), "readiness should be false before catch-up")

	// Register a test domain handler.
	handler := &testDomainHandler{}
	app.IntentProcessor.RegisterHandler("service", handler)

	intentEvent := nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "svc-test-e2e"},
			{"t", "bahia-intent"},
			{"domain", "service"},
			{"op", "create"},
			{"org", orgID.String()},
			{"intent_id", "test-e2e-001"},
		},
		Content: `{"name":"test-service"}`,
	}
	require.NoError(t, intentEvent.Sign(actorKey))

	// Send through the in-process MCP path, which shares the
	// same pipeline and idempotency store as the relay path.
	intent, err := controlplane.ParseIntent(&intentEvent)
	require.NoError(t, err)
	intent.Actor = actorPubkey

	err = app.IntentProcessor.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err)

	// Verify the handler received the intent.
	received := handler.received()
	require.Len(t, received, 1, "handler should receive exactly one intent")
	require.Equal(t, "service", received[0].Domain)
	require.Equal(t, "create", received[0].Op)
	require.Equal(t, "svc-test-e2e", received[0].Coordinate)
	require.Equal(t, orgID, received[0].OrgID)

	// Verify idempotency: sending the same intent again should be a no-op.
	err = app.IntentProcessor.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, handler.received(), 1, "duplicate intent should be deduplicated")

	// Mark readiness (simulating catch-up) and verify it flips.
	app.IntentReadiness.MarkFilterReady("intent-30900")
	require.True(t, app.IntentReadiness.IsReady(), "readiness should be true after marking filter ready")
}

// TestIntentSubscriberNotWiredWhenAllDomainsDisabled preserves the compatibility
// ContextVM-only path for an explicit all-domain opt-out.
func TestIntentSubscriberNotWiredWhenAllDomainsDisabled(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := intentSubscriberTestConfig(t)
	cfg.Nostr.IntentDomainsDisabled = append([]string(nil), controlplane.RegisteredIntentDomains...)

	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	require.Nil(t, app.IntentSubscriber, "IntentSubscriber should be nil when all domains are disabled")
	// Readiness is vacuously true when no filters are registered.
	require.True(t, app.IntentReadiness.IsReady(), "readiness should be vacuously true with no filters")
}

// TestDefaultIntentDomainsProcessEveryRegisteredHandler guards both startup
// wiring and in-process delivery when no domain config is supplied.
func TestDefaultIntentDomainsProcessEveryRegisteredHandler(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := intentSubscriberTestConfig(t)
	actorKey := nostr.Generate()
	actor := actorKey.Public().Hex()
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	cfg.Nostr.AuthorizedPubkeys = []string{actor}
	cfg.Nostr.BootstrapOwners = map[string]string{orgID.String(): actor}

	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)
	require.NotNil(t, app.IntentSubscriber)
	require.False(t, app.IntentReadiness.IsReady())

	for _, domainName := range controlplane.RegisteredIntentDomains {
		t.Run(domainName, func(t *testing.T) {
			handler := &testDomainHandler{}
			app.IntentProcessor.RegisterHandler(domainName, handler)
			event := nostr.Event{
				Kind: 30900, CreatedAt: nostr.Now(),
				Tags: nostr.Tags{{"d", domainName + "-test"}, {"t", "bahia-intent"},
					{"domain", domainName}, {"op", "create"}, {"org", orgID.String()},
					{"intent_id", "default-" + domainName}},
				Content: `{"name":"test"}`,
			}
			require.NoError(t, event.Sign(actorKey))
			intent, err := controlplane.ParseIntent(&event)
			require.NoError(t, err)
			intent.Actor = actor
			require.NoError(t, app.IntentProcessor.ProcessInProcess(context.Background(), intent))
			require.Len(t, handler.received(), 1)
		})
	}
	app.IntentReadiness.MarkFilterReady("intent-30900")
	require.True(t, app.IntentReadiness.IsReady())
}

func TestIntentDomainsDisabledStopsExactlyListedDomains(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()
	cfg := intentSubscriberTestConfig(t)
	cfg.Nostr.IntentDomainsDisabled = []string{"service", "policy"}
	actorKey := nostr.Generate()
	actor := actorKey.Public().Hex()
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	cfg.Nostr.AuthorizedPubkeys = []string{actor}
	cfg.Nostr.BootstrapOwners = map[string]string{orgID.String(): actor}
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)
	require.NotNil(t, app.IntentSubscriber)

	for _, domainName := range controlplane.RegisteredIntentDomains {
		handler := &testDomainHandler{}
		app.IntentProcessor.RegisterHandler(domainName, handler)
		event := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(),
			Tags: nostr.Tags{{"d", domainName + "-opt-out"}, {"t", "bahia-intent"},
				{"domain", domainName}, {"op", "create"}, {"org", orgID.String()},
				{"intent_id", "opt-out-" + domainName}},
			Content: `{"name":"test"}`}
		require.NoError(t, event.Sign(actorKey))
		intent, err := controlplane.ParseIntent(&event)
		require.NoError(t, err)
		intent.Actor = actor
		err = app.IntentProcessor.ProcessInProcess(context.Background(), intent)
		if domainName == "service" || domainName == "policy" {
			require.ErrorContains(t, err, "disabled", domainName)
			require.Empty(t, handler.received(), domainName)
		} else {
			require.NoError(t, err, domainName)
			require.Len(t, handler.received(), 1, domainName)
		}
	}
}

func TestDefaultSensitiveIntentDomainsRequireGiftWrap(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()
	cfg := intentSubscriberTestConfig(t)
	actorKey := nostr.Generate()
	actor := actorKey.Public().Hex()
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	cfg.Nostr.AuthorizedPubkeys = []string{actor}
	cfg.Nostr.BootstrapOwners = map[string]string{orgID.String(): actor}
	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	for _, domainName := range []string{"org", "secret", "notification"} {
		event := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(),
			Tags: nostr.Tags{{"d", domainName + "-plaintext"}, {"t", "bahia-intent"},
				{"domain", domainName}, {"op", "create"}, {"org", orgID.String()},
				{"intent_id", "plaintext-" + domainName}},
			Content: `{"name":"test"}`}
		require.NoError(t, event.Sign(actorKey))
		require.ErrorContains(t, app.IntentProcessor.ProcessRelayIntent(context.Background(), &event),
			"plaintext intent rejected", domainName)
	}
}

func TestIntentDomainRegistryCoversAppHandlers(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	require.NoError(t, err)
	registered := make(map[string]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "RegisterHandler" {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		name, err := strconv.Unquote(literal.Value)
		require.NoError(t, err)
		registered[name] = true
		return true
	})
	require.Len(t, registered, len(controlplane.RegisteredIntentDomains))
	for _, name := range controlplane.RegisteredIntentDomains {
		require.True(t, registered[name], "missing app.RegisterHandler for %q", name)
	}
}
