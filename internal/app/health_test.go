package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	signetAdapter "github.com/openagentsinc/bahia/internal/adapters/signet"
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

func TestSignetRecoveryFlipsReadinessHealthyWithoutRestart(t *testing.T) {
	client, err := signetAdapter.NewClient(signetAdapter.Config{AllowMock: true, ConnectTimeout: 50 * time.Millisecond}, slog.Default())
	require.NoError(t, err)
	manager := signetAdapter.NewConnectionManager(client, signetAdapter.ConnectionManagerConfig{Name: "test", HeartbeatInterval: time.Hour})
	provider := NewHealthProvider(NewModePolicy(ModeFull), nil)
	registerSignetHealthCheck(provider, manager, Tier1)

	degraded := provider.Readiness()
	require.Equal(t, SnapshotStatusDegraded, degraded.Status)
	require.True(t, degraded.Ready)
	requireCheckStatus(t, degraded.Checks, manager.Name(), HealthStatusWarn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	waitForManagerConnection(t, manager)

	healthy := provider.Readiness()
	require.Equal(t, SnapshotStatusHealthy, healthy.Status)
	require.True(t, healthy.Ready)
	requireCheckStatus(t, healthy.Checks, manager.Name(), HealthStatusPass)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection manager did not stop")
	}
}

func waitForManagerConnection(t *testing.T, manager *signetAdapter.ConnectionManager) {
	t.Helper()
	if manager.State().Connected {
		return
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case state := <-manager.Changes():
			if state.Connected {
				return
			}
		case <-timer.C:
			t.Fatal("Signet manager did not connect")
		}
	}
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

func TestHealthProviderWarningDependencyIsDegradedButReady(t *testing.T) {
	provider := NewHealthProvider(NewModePolicy(ModeFull), NewBackgroundManager(zap.NewNop()))
	provider.RegisterCheck("signet-test", int(Tier1), func() HealthCheck {
		return HealthCheck{Name: "signet-test", Status: HealthStatusWarn, Message: "disconnected", Tier: int(Tier1)}
	})

	snapshot := provider.Readiness()

	require.Equal(t, SnapshotStatusDegraded, snapshot.Status)
	require.True(t, snapshot.Ready)
	requireCheckStatus(t, snapshot.Checks, "signet-test", HealthStatusWarn)
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

func TestHealthProviderRelayQuorumUsesModeThresholds(t *testing.T) {
	t.Run("full mode requires two healthy relays by default", func(t *testing.T) {
		provider := NewHealthProvider(NewModePolicy(ModeFull), NewBackgroundManager(zap.NewNop()))
		provider.SetRelayHealthFunc(func() (connected, healthy int) { return 3, 1 })

		snapshot := provider.Readiness()

		require.Equal(t, SnapshotStatusUnhealthy, snapshot.Status)
		require.False(t, snapshot.Ready)
		check := requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusFail)
		require.Contains(t, check.Message, "min_required=2")
	})

	t.Run("configured full threshold is honored", func(t *testing.T) {
		provider := NewHealthProvider(NewModePolicy(ModeFull), NewBackgroundManager(zap.NewNop()))
		provider.SetRelayQuorumConfig(RelayQuorumConfig{FullMinHealthy: 3, DegradedMinHealthy: 1, EmergencyMinHealthy: 1})
		provider.SetRelayHealthFunc(func() (connected, healthy int) { return 4, 2 })

		snapshot := provider.Readiness()

		require.Equal(t, SnapshotStatusUnhealthy, snapshot.Status)
		require.False(t, snapshot.Ready)
		check := requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusFail)
		require.Contains(t, check.Message, "min_required=3")
	})

	t.Run("degraded and emergency modes use lower defaults", func(t *testing.T) {
		for _, mode := range []Mode{ModeDegraded, ModeEmergency} {
			provider := NewHealthProvider(NewModePolicy(mode), NewBackgroundManager(zap.NewNop()))
			provider.SetRelayHealthFunc(func() (connected, healthy int) { return 1, 1 })

			snapshot := provider.Readiness()

			require.True(t, snapshot.Ready, "mode %s", mode)
			check := requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusPass)
			require.Contains(t, check.Message, "min_required=1")
		}
	})
}

func requireCheckStatus(t *testing.T, checks []HealthCheck, name string, status string) HealthCheck {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			require.Equal(t, status, check.Status)
			return check
		}
	}
	require.Failf(t, "missing health check", "check %q not found", name)
	return HealthCheck{}
}
