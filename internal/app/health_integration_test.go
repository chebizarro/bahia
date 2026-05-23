package app

import (
	"context"
	"testing"
	"time"

	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type healthIntegrationCursorSource struct {
	timestamp time.Time
}

func (s healthIntegrationCursorSource) LatestEventTimestamp(context.Context, []int) (time.Time, error) {
	return s.timestamp, nil
}

func TestHealthIntegrationColdStartWithEmptyDBBecomesReadyFromRelayCanonicalState(t *testing.T) {
	policy := NewModePolicy(ModeFull)
	manager := NewBackgroundManager(zap.NewNop())
	manager.RegisterWithOptions(&testRunner{name: "relay-projector"}, RunnerTier(Tier3), RunnerRequired(true))
	manager.markRunnerStarted("relay-projector")

	provider := NewHealthProvider(policy, manager)
	provider.SetRelayHealthFunc(func() (connected, healthy int) { return 3, 2 })
	provider.SetBootstrapFunc(func() (phase string, ready bool) { return "ready", true })
	provider.RegisterCheck("postgres_cache", int(Tier3), func() HealthCheck {
		return HealthCheck{Name: "postgres_cache", Status: HealthStatusPass, Message: "cache available but empty", Tier: int(Tier3)}
	})
	provider.RegisterCheck("core_projection_cache", int(Tier2), func() HealthCheck {
		return HealthCheck{Name: "core_projection_cache", Status: HealthStatusPass, Message: "rebuilt from relay events", Tier: int(Tier2)}
	})

	snapshot := provider.Readiness()

	require.True(t, snapshot.Ready)
	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	require.Equal(t, int(Tier3), snapshot.ActiveTier)
	requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusPass)
	requireCheckStatus(t, snapshot.Checks, "bootstrap_ready", HealthStatusPass)
	requireCheckStatus(t, snapshot.Checks, "postgres_cache", HealthStatusPass)
}

func TestHealthIntegrationWarmRestartWithCheckpointCursorIsReady(t *testing.T) {
	provider := NewHealthProvider(NewModePolicy(ModeFull), NewBackgroundManager(zap.NewNop()))
	provider.SetRelayHealthFunc(func() (connected, healthy int) { return 3, 3 })
	provider.SetBootstrapFunc(func() (phase string, ready bool) { return "ready:checkpoint", true })
	provider.RegisterCheck("live_catchup", int(Tier3), func() HealthCheck {
		return HealthCheck{Name: "live_catchup", Status: HealthStatusPass, Message: "checkpoint cursor caught up", Tier: int(Tier3)}
	})

	snapshot := provider.Readiness()

	require.True(t, snapshot.Ready)
	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	requireCheckStatus(t, snapshot.Checks, "live_catchup", HealthStatusPass)
}

func TestHealthIntegrationEmergencyBootWithDBAbsentIsTier1Ready(t *testing.T) {
	policy := NewModePolicy(ModeEmergency)
	policy.SetActiveTier(Tier1)
	provider := NewHealthProvider(policy, NewBackgroundManager(zap.NewNop()))
	provider.SetRelayHealthFunc(func() (connected, healthy int) { return 1, 1 })
	provider.SetBootstrapFunc(func() (phase string, ready bool) { return "ready", true })
	provider.RegisterCheck("continuity_runtime", int(Tier1), func() HealthCheck {
		return HealthCheck{Name: "continuity_runtime", Status: HealthStatusPass, Message: "running without postgres cache", Tier: int(Tier1)}
	})

	snapshot := provider.Readiness()

	require.True(t, snapshot.Ready)
	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	require.Equal(t, int(Tier1), snapshot.ActiveTier)
	require.True(t, policy.RouteEnabled(Tier1))
	require.False(t, policy.RouteEnabled(Tier2))
	require.False(t, policy.RouteEnabled(Tier3))
}

func TestHealthIntegrationReconnectGapReplayUsesNewestCursorWithOverlap(t *testing.T) {
	oldCursor := time.Unix(100, 0).UTC()
	checkpointCursor := time.Unix(250, 0).UTC()
	planner := nostradapter.NewReplayCursorPlanner(2*time.Second,
		healthIntegrationCursorSource{timestamp: oldCursor},
		healthIntegrationCursorSource{timestamp: checkpointCursor},
	)

	since := planner.ComputeSince(context.Background(), []int{nostradapter.KindControlPlaneDeployRequest})

	require.NotNil(t, since)
	require.Equal(t, checkpointCursor.Add(-2*time.Second).Unix(), int64(*since))
}

func TestHealthIntegrationRouteGatingAcrossModes(t *testing.T) {
	emergency := NewModePolicy(ModeEmergency)
	require.True(t, emergency.RouteEnabled(Tier0))
	require.True(t, emergency.RouteEnabled(Tier1))
	require.False(t, emergency.RouteEnabled(Tier2))
	require.False(t, emergency.RouteEnabled(Tier3))

	degraded := NewModePolicy(ModeDegraded)
	require.True(t, degraded.RouteEnabled(Tier0))
	require.True(t, degraded.RouteEnabled(Tier1))
	require.True(t, degraded.RouteEnabled(Tier2))
	require.False(t, degraded.RouteEnabled(Tier3))

	full := NewModePolicy(ModeFull)
	require.True(t, full.RouteEnabled(Tier0))
	require.True(t, full.RouteEnabled(Tier1))
	require.True(t, full.RouteEnabled(Tier2))
	require.True(t, full.RouteEnabled(Tier3))
}

func TestHealthIntegrationDegradedReadinessStillReadyAtActiveTier(t *testing.T) {
	policy := NewModePolicy(ModeFull)
	policy.SetActiveTier(Tier2)
	provider := NewHealthProvider(policy, NewBackgroundManager(zap.NewNop()))
	provider.SetRelayHealthFunc(func() (connected, healthy int) { return 2, 1 })
	provider.SetBootstrapFunc(func() (phase string, ready bool) { return "ready:tier2", true })
	provider.RegisterCheck("core_projection_cache", int(Tier2), func() HealthCheck {
		return HealthCheck{Name: "core_projection_cache", Status: HealthStatusPass, Message: "active tier cache rebuilt", Tier: int(Tier2)}
	})
	provider.RegisterCheck("extended_projection_cache", int(Tier3), func() HealthCheck {
		return HealthCheck{Name: "extended_projection_cache", Status: HealthStatusFail, Message: "above active tier", Tier: int(Tier3)}
	})

	snapshot := provider.Readiness()

	require.True(t, snapshot.Ready)
	require.Equal(t, SnapshotStatusDegraded, snapshot.Status)
	require.Equal(t, int(Tier3), snapshot.RequestedTier)
	require.Equal(t, int(Tier2), snapshot.ActiveTier)
	requireCheckStatus(t, snapshot.Checks, "core_projection_cache", HealthStatusPass)
	for _, check := range snapshot.Checks {
		require.NotEqual(t, "extended_projection_cache", check.Name)
	}
}
