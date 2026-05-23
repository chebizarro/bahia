package app

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestHealthProviderLivenessAlwaysHealthy(t *testing.T) {
	provider := NewHealthProvider(NewModePolicy(ModeFull), nil)

	snapshot := provider.Liveness()

	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	require.True(t, snapshot.Ready)
	require.Equal(t, string(ModeFull), snapshot.Mode)
}

func TestHealthProviderReadinessWithNoChecksPasses(t *testing.T) {
	provider := NewHealthProvider(NewModePolicy(ModeFull), NewBackgroundManager(zap.NewNop()))

	snapshot := provider.Readiness()

	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	require.True(t, snapshot.Ready)
	requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusPass)
	requireCheckStatus(t, snapshot.Checks, "bootstrap_ready", HealthStatusPass)
	requireCheckStatus(t, snapshot.Checks, "background_runners", HealthStatusPass)
}

func TestHealthProviderReadinessWithFailedRequiredRunnerReturnsUnhealthy(t *testing.T) {
	manager := NewBackgroundManager(zap.NewNop())
	manager.Register(&testRunner{name: "required-runner"})
	provider := NewHealthProvider(NewModePolicy(ModeFull), manager)

	snapshot := provider.Readiness()

	require.Equal(t, SnapshotStatusUnhealthy, snapshot.Status)
	require.False(t, snapshot.Ready)
	require.Len(t, snapshot.RunnerSummary, 1)
	require.Equal(t, "required-runner", snapshot.RunnerSummary[0].Name)
	require.True(t, snapshot.RunnerSummary[0].Required)
	require.False(t, snapshot.RunnerSummary[0].Running)
	requireCheckStatus(t, snapshot.Checks, "background_runners", HealthStatusFail)
}

func TestHealthProviderReadinessWithRelayHealthFunction(t *testing.T) {
	provider := NewHealthProvider(NewModePolicy(ModeFull), NewBackgroundManager(zap.NewNop()))
	provider.SetRelayHealthFunc(func() (connected, healthy int) {
		return 3, 2
	})

	snapshot := provider.Readiness()

	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	require.True(t, snapshot.Ready)
	requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusPass)

	provider.SetRelayHealthFunc(func() (connected, healthy int) {
		return 1, 0
	})

	snapshot = provider.Readiness()

	require.Equal(t, SnapshotStatusUnhealthy, snapshot.Status)
	require.False(t, snapshot.Ready)
	requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusFail)
}

func TestHealthProviderModeAndTierReflectedInSnapshot(t *testing.T) {
	policy := NewModePolicy(ModeFull)
	policy.SetActiveTier(Tier2)
	provider := NewHealthProvider(policy, NewBackgroundManager(zap.NewNop()))

	snapshot := provider.Readiness()

	require.Equal(t, string(ModeFull), snapshot.Mode)
	require.Equal(t, int(Tier3), snapshot.RequestedTier)
	require.Equal(t, int(Tier2), snapshot.ActiveTier)
	require.Equal(t, SnapshotStatusDegraded, snapshot.Status)
	require.True(t, snapshot.Ready)
}

func requireCheckStatus(t *testing.T, checks []HealthCheck, name string, status string) {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			require.Equal(t, status, check.Status)
			return
		}
	}
	require.Failf(t, "missing health check", "check %q not found", name)
}
