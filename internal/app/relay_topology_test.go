package app

import (
	"context"
	"slices"
	"testing"

	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

func TestControlPlaneRelayURLsPreferSidecarBackend(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Relays = []string{"wss://upstream.example"}
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3000/relay"
	cfg.Sidecar.BackendURL = "ws://relay:3334"

	got := controlPlaneRelayURLs(cfg)
	want := []string{"ws://relay:3334"}
	if !slices.Equal(got, want) {
		t.Fatalf("controlPlaneRelayURLs() = %v, want %v", got, want)
	}
}

func TestInteropRelayURLsMirrorExternalUsesSidecarBoundary(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.Relays = []string{"wss://upstream.example"}
	cfg.Loom.Relays = []string{"wss://loom.example"}
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.MirrorExternal = true

	got := interopRelayURLs(cfg, []string{"ws://relay:3334"})
	want := []string{"ws://relay:3334", "wss://loom.example"}
	if !slices.Equal(got, want) {
		t.Fatalf("interopRelayURLs() = %v, want %v", got, want)
	}
}

func TestInteropRelayURLsWithoutMirrorKeepsUpstreamRelays(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.Relays = []string{"wss://upstream.example"}
	cfg.Loom.Relays = []string{"wss://loom.example"}
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.MirrorExternal = false

	got := interopRelayURLs(cfg, []string{"ws://relay:3334"})
	want := []string{"wss://upstream.example", "wss://loom.example"}
	if !slices.Equal(got, want) {
		t.Fatalf("interopRelayURLs() = %v, want %v", got, want)
	}
}

func TestRelayTopologyCoordinatorReconfiguresPoolsFromCanonicalSnapshot(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.Relays = []string{"wss://old-service.example"}
	cfg.Nostr.ServiceRelays = []string{"wss://old-service.example"}
	cfg.Nostr.ContextVMRelays = []string{"wss://old-contextvm.example"}
	cfg.Loom.Relays = []string{"wss://loom.example"}
	controlPlanePool := nostrAdapter.NewRelayPool([]string{"wss://old-contextvm.example"}, zap.NewNop())
	servicePool := nostrAdapter.NewRelayPool([]string{"wss://old-service.example", "wss://loom.example"}, zap.NewNop())
	coordinator := newRelayTopologyCoordinator(relayTopologyCoordinatorConfig{
		ControlPlanePool: controlPlanePool,
		ServicePool:      servicePool,
		NostrConfig:      cfg.Nostr,
		LoomRelays:       cfg.Loom.Relays,
		Logger:           zap.NewNop(),
	})

	if err := coordinator.ApplySnapshot(context.Background(), controlplane.RelayPolicyState{
		Schema:          controlplane.RelaySettingsSchema,
		ContextVMRelays: []string{"wss://new-contextvm.example"},
		ServiceRelays:   []string{"wss://new-service.example"},
	}); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if got, want := controlPlanePool.URLs(), []string{"wss://new-contextvm.example"}; !slices.Equal(got, want) {
		t.Fatalf("control plane pool URLs = %v, want %v", got, want)
	}
	if got, want := servicePool.URLs(), []string{"wss://new-service.example", "wss://loom.example"}; !slices.Equal(got, want) {
		t.Fatalf("service pool URLs = %v, want %v", got, want)
	}
	if got, want := cfg.Nostr.Relays, []string{"wss://old-service.example"}; !slices.Equal(got, want) {
		t.Fatalf("coordinator mutated shared config relays = %v, want %v", got, want)
	}
}

func TestRelayTopologyCoordinatorPreservesSidecarPrecedence(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.Relays = []string{"wss://old-upstream.example"}
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.BackendURL = "ws://relay:3334"
	cfg.Nostr.Sidecar.MirrorExternal = true
	cfg.Loom.Relays = []string{"wss://loom.example"}
	controlPlanePool := nostrAdapter.NewRelayPool([]string{"ws://relay:3334"}, zap.NewNop())
	servicePool := nostrAdapter.NewRelayPool([]string{"ws://relay:3334", "wss://loom.example"}, zap.NewNop())
	coordinator := newRelayTopologyCoordinator(relayTopologyCoordinatorConfig{
		ControlPlanePool: controlPlanePool,
		ServicePool:      servicePool,
		NostrConfig:      cfg.Nostr,
		LoomRelays:       cfg.Loom.Relays,
		Logger:           zap.NewNop(),
	})

	if err := coordinator.ApplySnapshot(context.Background(), controlplane.RelayPolicyState{
		Schema:          controlplane.RelaySettingsSchema,
		ContextVMRelays: []string{"wss://external-contextvm.example"},
		ServiceRelays:   []string{"wss://external-service.example"},
	}); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if got, want := controlPlanePool.URLs(), []string{"ws://relay:3334"}; !slices.Equal(got, want) {
		t.Fatalf("control plane pool URLs = %v, want sidecar %v", got, want)
	}
	if got, want := servicePool.URLs(), []string{"ws://relay:3334", "wss://loom.example"}; !slices.Equal(got, want) {
		t.Fatalf("service pool URLs = %v, want sidecar mirror boundary %v", got, want)
	}
}

func TestRelayTopologyCoordinatorDoesNotCollapseRuntimePoolsForTopologyEmptySnapshot(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.Relays = []string{"wss://old-service.example"}
	controlPlanePool := nostrAdapter.NewRelayPool([]string{"wss://old-contextvm.example"}, zap.NewNop())
	servicePool := nostrAdapter.NewRelayPool([]string{"wss://old-service.example"}, zap.NewNop())
	coordinator := newRelayTopologyCoordinator(relayTopologyCoordinatorConfig{
		ControlPlanePool: controlPlanePool,
		ServicePool:      servicePool,
		NostrConfig:      cfg.Nostr,
		Logger:           zap.NewNop(),
	})

	if err := coordinator.ApplySnapshot(context.Background(), controlplane.RelayPolicyState{
		Schema: controlplane.RelaySettingsSchema,
		DMRelayLists: []controlplane.RelayPolicyDMRelayList{{
			Enabled:  true,
			Feature:  config.DMRelayListFeatureNotifications,
			Identity: config.DMRelayListIdentityService,
			Relays:   []string{"wss://dm.example"},
		}},
	}); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if got, want := controlPlanePool.URLs(), []string{"wss://old-contextvm.example"}; !slices.Equal(got, want) {
		t.Fatalf("control plane pool URLs = %v, want retained %v", got, want)
	}
	if got, want := servicePool.URLs(), []string{"wss://old-service.example"}; !slices.Equal(got, want) {
		t.Fatalf("service pool URLs = %v, want retained %v", got, want)
	}
}

func TestRelayTopologyCoordinatorDoesNotCollapseServicePoolWhenSnapshotOnlyMovesContextVM(t *testing.T) {
	cfg := config.Defaults()
	controlPlanePool := nostrAdapter.NewRelayPool([]string{"wss://old-contextvm.example"}, zap.NewNop())
	servicePool := nostrAdapter.NewRelayPool([]string{"wss://old-service.example"}, zap.NewNop())
	coordinator := newRelayTopologyCoordinator(relayTopologyCoordinatorConfig{
		ControlPlanePool: controlPlanePool,
		ServicePool:      servicePool,
		NostrConfig:      cfg.Nostr,
		Logger:           zap.NewNop(),
	})

	if err := coordinator.ApplySnapshot(context.Background(), controlplane.RelayPolicyState{
		Schema:          controlplane.RelaySettingsSchema,
		ContextVMRelays: []string{"wss://new-contextvm.example"},
	}); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if got, want := controlPlanePool.URLs(), []string{"wss://new-contextvm.example"}; !slices.Equal(got, want) {
		t.Fatalf("control plane pool URLs = %v, want %v", got, want)
	}
	if got, want := servicePool.URLs(), []string{"wss://old-service.example"}; !slices.Equal(got, want) {
		t.Fatalf("service pool URLs = %v, want retained %v", got, want)
	}
}
