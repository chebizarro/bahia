package app

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProjectorWarmStartWiredWhenDomainsEnabled verifies that with
// intent_domains configured, the Projector receives ReadinessTracker and
// IntentDomains options. This is the app-level wiring complement to the
// unit-level warm-start tests in internal/adapters/nostr that verify the
// zero-publish restart behavior.
func TestProjectorWarmStartWiredWhenDomainsEnabled(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := startupTestConfig(ModeEmergency)
	cfg.Nostr.IntentDomains = []string{"service", "environment"}
	cfg.Nostr.PublishEnabled = true

	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	// Verify the projector exists and is configured for warm-start.
	require.NotNil(t, app.NostrProjector, "NostrProjector should be wired")
	require.True(t, app.NostrProjector.WarmStartConfigured(),
		"projector should have warm-start configured when intent_domains is set")
	require.Equal(t, []string{"service", "environment"},
		app.NostrProjector.IntentDomainsMigrated(),
		"projector should have the configured intent domains")

	// Readiness is not yet ready (no EOSE has happened).
	require.False(t, app.IntentReadiness.IsReady(),
		"readiness should be false before subscriber catch-up")
}

// TestProjectorWarmStartNotWiredWithoutDomains verifies that with no
// intent_domains configured, the projector does not have warm-start active.
func TestProjectorWarmStartNotWiredWithoutDomains(t *testing.T) {
	restoreDBHooks := stubDBHooks(t, errors.New("database unavailable"), nil)
	defer restoreDBHooks()

	cfg := startupTestConfig(ModeEmergency)
	// No intent domains configured.

	app, err := New(cfg)
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	require.NotNil(t, app.NostrProjector, "NostrProjector should always be wired")
	require.False(t, app.NostrProjector.WarmStartConfigured(),
		"projector should NOT have warm-start configured when no intent domains")
}
