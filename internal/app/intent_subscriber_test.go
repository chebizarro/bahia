package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
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

// TestIntentSubscriberWiredWhenDomainsEnabled verifies that with a test domain
// enabled, the intent subscriber is constructed and the processor pipeline
// delivers a signed intent to the registered handler, with readiness tracking.
func TestIntentSubscriberWiredWhenDomainsEnabled(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := startupTestConfig("emergency")
	// Generate a separate keypair for the intent actor.
	actorKey := nostr.Generate()
	actorPubkey := actorKey.Public().Hex()
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	// Configure the actor as a bootstrap owner for the test org so the
	// TrustSet grants it org-level permissions.
	cfg.Nostr.AuthorizedPubkeys = []string{actorPubkey}
	cfg.Nostr.IntentDomains = []string{"service"}
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

	// Send through the in-process path (dual dispatch), which shares the
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

// TestIntentSubscriberNotWiredWithoutDomains verifies that the subscriber is
// nil when no intent domains are configured.
func TestIntentSubscriberNotWiredWithoutDomains(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := startupTestConfig("emergency")
	// No intent domains configured.

	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	require.Nil(t, app.IntentSubscriber, "IntentSubscriber should be nil when no domains are configured")
	// Readiness is vacuously true when no filters are registered.
	require.True(t, app.IntentReadiness.IsReady(), "readiness should be vacuously true with no filters")
}
