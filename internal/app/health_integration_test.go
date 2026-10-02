package app

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestHealthIntegrationColdStartBecomesReadyFromRelayState(t *testing.T) {
	manager := NewBackgroundManager(zap.NewNop())
	manager.Register(&testRunner{name: "relay-projector"})
	manager.markRunnerStarted("relay-projector")

	provider := NewHealthProvider(nil, manager)
	provider.SetRelayHealthFunc(func() (connected, healthy int) { return 3, 2 })
	provider.SetBootstrapFunc(func() (phase string, ready bool) { return "ready", true })
	provider.RegisterCheck("projection_cache", func() HealthCheck {
		return HealthCheck{Name: "projection_cache", Status: HealthStatusPass, Message: "rebuilt from relay events"}
	})

	snapshot := provider.Readiness()

	require.True(t, snapshot.Ready)
	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusPass)
	requireCheckStatus(t, snapshot.Checks, "bootstrap_ready", HealthStatusPass)
	requireCheckStatus(t, snapshot.Checks, "projection_cache", HealthStatusPass)
}

func TestHealthIntegrationWarmRestartWithCheckpointCursorIsReady(t *testing.T) {
	provider := NewHealthProvider(nil, NewBackgroundManager(zap.NewNop()))
	provider.SetRelayHealthFunc(func() (connected, healthy int) { return 3, 3 })
	provider.SetBootstrapFunc(func() (phase string, ready bool) { return "ready:checkpoint", true })
	provider.RegisterCheck("live_catchup", func() HealthCheck {
		return HealthCheck{Name: "live_catchup", Status: HealthStatusPass, Message: "checkpoint cursor caught up"}
	})

	snapshot := provider.Readiness()

	require.True(t, snapshot.Ready)
	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	requireCheckStatus(t, snapshot.Checks, "live_catchup", HealthStatusPass)
}

func TestHealthIntegrationDBAbsentDaemonReady(t *testing.T) {
	provider := NewHealthProvider(nil, NewBackgroundManager(zap.NewNop()))
	provider.SetRelayHealthFunc(func() (connected, healthy int) { return 2, 2 })
	provider.SetBootstrapFunc(func() (phase string, ready bool) { return "ready", true })
	provider.RegisterCheck("continuity_runtime", func() HealthCheck {
		return HealthCheck{Name: "continuity_runtime", Status: HealthStatusPass, Message: "running without postgres cache"}
	})

	snapshot := provider.Readiness()

	require.True(t, snapshot.Ready)
	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
}

func TestHealthIntegrationReadinessTrackerGatesReady(t *testing.T) {
	tracker := controlplane.NewReadinessTracker()
	tracker.RegisterFilter("services")
	tracker.RegisterFilter("environments")

	provider := NewHealthProvider(tracker, NewBackgroundManager(zap.NewNop()))
	provider.SetRelayHealthFunc(func() (connected, healthy int) { return 2, 2 })
	provider.SetBootstrapFunc(func() (phase string, ready bool) { return "ready", true })

	snapshot := provider.Readiness()
	require.False(t, snapshot.Ready, "not ready while filters are syncing")
	requireCheckStatus(t, snapshot.Checks, "intent_readiness", HealthStatusFail)

	tracker.MarkFilterReady("services")
	snapshot = provider.Readiness()
	require.False(t, snapshot.Ready, "not ready with only one filter synced")

	tracker.MarkFilterReady("environments")
	snapshot = provider.Readiness()
	require.True(t, snapshot.Ready, "ready after all filters synced")
	requireCheckStatus(t, snapshot.Checks, "intent_readiness", HealthStatusPass)
}

func TestHealthIntegrationDegradedCheckStillReady(t *testing.T) {
	provider := NewHealthProvider(nil, NewBackgroundManager(zap.NewNop()))
	provider.SetRelayHealthFunc(func() (connected, healthy int) { return 2, 2 })
	provider.SetBootstrapFunc(func() (phase string, ready bool) { return "ready", true })
	provider.RegisterCheck("projection_cache", func() HealthCheck {
		return HealthCheck{Name: "projection_cache", Status: HealthStatusPass, Message: "active cache rebuilt"}
	})
	provider.RegisterCheck("extended_cache", func() HealthCheck {
		return HealthCheck{Name: "extended_cache", Status: HealthStatusWarn, Message: "optional feature degraded"}
	})

	snapshot := provider.Readiness()

	require.True(t, snapshot.Ready)
	require.Equal(t, SnapshotStatusDegraded, snapshot.Status)
	requireCheckStatus(t, snapshot.Checks, "projection_cache", HealthStatusPass)
	requireCheckStatus(t, snapshot.Checks, "extended_cache", HealthStatusWarn)
}
