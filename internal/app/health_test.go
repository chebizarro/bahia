package app

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	signetAdapter "github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestHealthProviderLivenessAlwaysHealthy(t *testing.T) {
	provider := NewHealthProvider(nil, nil)

	snapshot := provider.Liveness()

	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	require.True(t, snapshot.Ready)
}

func TestSignetRecoveryFlipsReadinessHealthyWithoutRestart(t *testing.T) {
	client, err := signetAdapter.NewClient(signetAdapter.Config{AllowMock: true, ConnectTimeout: 50 * time.Millisecond}, slog.Default())
	require.NoError(t, err)
	manager := signetAdapter.NewConnectionManager(client, signetAdapter.ConnectionManagerConfig{Name: "test", HeartbeatInterval: time.Hour})
	provider := NewHealthProvider(nil, nil)
	registerSignetHealthCheck(provider, manager)

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
	provider := NewHealthProvider(nil, NewBackgroundManager(zap.NewNop()))

	snapshot := provider.Readiness()

	require.Equal(t, SnapshotStatusHealthy, snapshot.Status)
	require.True(t, snapshot.Ready)
	requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusPass)
	requireCheckStatus(t, snapshot.Checks, "bootstrap_ready", HealthStatusPass)
	requireCheckStatus(t, snapshot.Checks, "background_runners", HealthStatusPass)
}

func TestHealthProviderReadinessIncludesBootstrapBlockingRelays(t *testing.T) {
	provider := NewHealthProvider(nil, nil)
	provider.SetBootstrapFunc(func() (string, bool) { return "snapshot", false })
	provider.SetBootstrapDetailsFunc(func() map[string]string {
		return map[string]string{
			"current_group":   "state_snapshot",
			"blocking_relays": "wss://slow.example",
		}
	})

	snapshot := provider.Readiness()
	var bootstrap HealthCheck
	for _, check := range snapshot.Checks {
		if check.Name == "bootstrap_ready" {
			bootstrap = check
			break
		}
	}
	require.Equal(t, HealthStatusFail, bootstrap.Status)
	require.Equal(t, "wss://slow.example", bootstrap.Details["blocking_relays"])
}

func TestHealthProviderWarningDependencyIsDegradedButReady(t *testing.T) {
	provider := NewHealthProvider(nil, NewBackgroundManager(zap.NewNop()))
	provider.RegisterCheck("signet-test", func() HealthCheck {
		return HealthCheck{Name: "signet-test", Status: HealthStatusWarn, Message: "disconnected"}
	})

	snapshot := provider.Readiness()

	require.Equal(t, SnapshotStatusDegraded, snapshot.Status)
	require.True(t, snapshot.Ready)
	requireCheckStatus(t, snapshot.Checks, "signet-test", HealthStatusWarn)
}

func TestHealthProviderReadinessWithFailedRequiredRunnerReturnsUnhealthy(t *testing.T) {
	manager := NewBackgroundManager(zap.NewNop())
	manager.Register(&testRunner{name: "required-runner"})
	provider := NewHealthProvider(nil, manager)

	snapshot := provider.Readiness()

	require.Equal(t, SnapshotStatusUnhealthy, snapshot.Status)
	require.False(t, snapshot.Ready)
	requireCheckStatus(t, snapshot.Checks, "background_runners", HealthStatusFail)
}

func TestHealthProviderReadinessWithRelayHealthFunction(t *testing.T) {
	provider := NewHealthProvider(nil, NewBackgroundManager(zap.NewNop()))
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

func TestHealthProviderReadinessTrackerIntegration(t *testing.T) {
	tracker := controlplane.NewReadinessTracker()
	tracker.RegisterFilter("intents")
	provider := NewHealthProvider(tracker, NewBackgroundManager(zap.NewNop()))

	snapshot := provider.Readiness()
	require.False(t, snapshot.Ready, "should not be ready while filters are syncing")
	requireCheckStatus(t, snapshot.Checks, "intent_readiness", HealthStatusFail)

	tracker.MarkFilterReady("intents")

	snapshot = provider.Readiness()
	require.True(t, snapshot.Ready, "should be ready after all filters synced")
	requireCheckStatus(t, snapshot.Checks, "intent_readiness", HealthStatusPass)
}

func TestHealthProviderRelayQuorumUsesConfiguredThreshold(t *testing.T) {
	t.Run("default requires two healthy relays", func(t *testing.T) {
		provider := NewHealthProvider(nil, NewBackgroundManager(zap.NewNop()))
		provider.SetRelayHealthFunc(func() (connected, healthy int) { return 3, 1 })

		snapshot := provider.Readiness()

		require.Equal(t, SnapshotStatusUnhealthy, snapshot.Status)
		require.False(t, snapshot.Ready)
		check := requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusFail)
		require.Contains(t, check.Message, "min_required=2")
	})

	t.Run("configured threshold is honored", func(t *testing.T) {
		provider := NewHealthProvider(nil, NewBackgroundManager(zap.NewNop()))
		provider.SetRelayQuorumConfig(RelayQuorumConfig{FullMinHealthy: 3, DegradedMinHealthy: 1, EmergencyMinHealthy: 1})
		provider.SetRelayHealthFunc(func() (connected, healthy int) { return 4, 2 })

		snapshot := provider.Readiness()

		require.Equal(t, SnapshotStatusUnhealthy, snapshot.Status)
		require.False(t, snapshot.Ready)
		check := requireCheckStatus(t, snapshot.Checks, "relay_quorum", HealthStatusFail)
		require.Contains(t, check.Message, "min_required=3")
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

func TestOCKRotationDegradesReadinessUntilRecovery(t *testing.T) {
	tracker := controlplane.NewReadinessTracker()
	tracker.RegisterFilter("intent")
	provider := NewHealthProvider(tracker, nil)
	pending := []string{"org-a", "org-b"}
	registerOCKRotationHealthCheck(provider, func() []string { return pending })
	require.False(t, provider.Readiness().Ready, "rotation must not bypass EOSE readiness")
	tracker.MarkFilterReady("intent")
	snapshot := provider.Readiness()
	require.True(t, snapshot.Ready, "operator intents must remain available to retry")
	require.Equal(t, SnapshotStatusDegraded, snapshot.Status)
	requireCheckStatus(t, snapshot.Checks, "ock_rotation", HealthStatusWarn)
	for _, check := range snapshot.Checks {
		if check.Name == "ock_rotation" {
			require.Equal(t, "org org-a: confidential publishes withheld pending key rotation; org org-b: confidential publishes withheld pending key rotation", check.Message)
			require.Equal(t, "org-a,org-b", check.Details["orgs"])
		}
	}
	pending = nil
	require.Equal(t, SnapshotStatusHealthy, provider.Readiness().Status)
	requireCheckStatus(t, provider.Readiness().Checks, "ock_rotation", HealthStatusPass)
}

// A coordinate the outbox abandoned after its producer was told it was queued
// degrades readiness (warn, still ready) until a publish of it reaches the
// quorum (docs/architecture/outbox-delivery.md).
func TestUndeliveredCanonicalStateDegradesReadinessUntilDelivered(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "daemon.bolt"))
	require.NoError(t, err)
	defer store.Close()
	tracker := controlplane.NewReadinessTracker()
	provider := NewHealthProvider(tracker, nil)
	registerUndeliveredHealthCheck(provider, store)
	require.Equal(t, SnapshotStatusHealthy, provider.Readiness().Status)
	requireCheckStatus(t, provider.Readiness().Checks, "canonical_delivery", HealthStatusPass)

	sk := nostr.Generate()
	ev := nostr.Event{Kind: 30900, CreatedAt: 100, Tags: nostr.Tags{{"d", "payment:1"}}, Content: "{}"}
	require.NoError(t, ev.Sign(sk))
	_, err = store.SaveEvent(ev)
	require.NoError(t, err)
	_, err = store.MarkUndelivered(ev, "abandoned after 5 publish attempts", time.Unix(1_700_000_000, 0))
	require.NoError(t, err)

	snapshot := provider.Readiness()
	require.True(t, snapshot.Ready, "committed state is still served; the daemon is degraded, not unready")
	require.Equal(t, SnapshotStatusDegraded, snapshot.Status)
	requireCheckStatus(t, snapshot.Checks, "canonical_delivery", HealthStatusWarn)
	for _, check := range snapshot.Checks {
		if check.Name == "canonical_delivery" {
			require.Contains(t, check.Message, "1 canonical record(s) abandoned")
			require.Contains(t, check.Message, "abandoned after 5 publish attempts")
			require.Equal(t, "1", check.Details["count"])
			require.Equal(t, "30900:"+sk.Public().Hex()+":payment:1", check.Details["coordinates"])
			require.Equal(t, "2023-11-14T22:13:20Z", check.Details["oldest_at"])
		}
	}

	_, err = store.ClearUndelivered(ev)
	require.NoError(t, err)
	require.Equal(t, SnapshotStatusHealthy, provider.Readiness().Status)
	requireCheckStatus(t, provider.Readiness().Checks, "canonical_delivery", HealthStatusPass)
}
