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

func TestControlPlaneRelayURLsUseContextVMPolicyWhenSidecarDisabled(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Relays = []string{"wss://interop.example"}
	cfg.BrowserRelays = []string{"wss://browser.example"}
	cfg.ContextVMRelays = []string{"wss://contextvm.example"}
	cfg.Sidecar.Enabled = false

	got := controlPlaneRelayURLs(cfg)
	want := []string{"wss://contextvm.example"}
	if !slices.Equal(got, want) {
		t.Fatalf("controlPlaneRelayURLs() = %v, want %v", got, want)
	}
}

func TestControlPlaneRelayURLsFallBackToBrowserPolicyWhenContextVMUnset(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Relays = []string{"wss://interop.example"}
	cfg.BrowserRelays = []string{"wss://browser.example"}
	cfg.ContextVMRelays = nil
	cfg.Sidecar.Enabled = false

	got := controlPlaneRelayURLs(cfg)
	want := []string{"wss://browser.example"}
	if !slices.Equal(got, want) {
		t.Fatalf("controlPlaneRelayURLs() = %v, want %v", got, want)
	}
}

func TestContextVMRelayURLsUsesSidecarAndConfiguredPolicyUnion(t *testing.T) {
	tests := []struct {
		name            string
		sidecarEnabled  bool
		contextVMRelays []string
		browserRelays   []string
		want            []string
	}{
		{name: "sidecar on context set browser set", sidecarEnabled: true, contextVMRelays: []string{"wss://contextvm.example"}, browserRelays: []string{"wss://browser.example"}, want: []string{"ws://relay:3334", "wss://contextvm.example"}},
		{name: "sidecar on context set browser unset", sidecarEnabled: true, contextVMRelays: []string{"wss://contextvm.example"}, want: []string{"ws://relay:3334", "wss://contextvm.example"}},
		{name: "sidecar on context unset browser set", sidecarEnabled: true, browserRelays: []string{"wss://browser.example"}, want: []string{"ws://relay:3334", "wss://browser.example"}},
		{name: "sidecar on context unset browser unset", sidecarEnabled: true, want: []string{"ws://relay:3334"}},
		{name: "sidecar off context set browser set", contextVMRelays: []string{"wss://contextvm.example"}, browserRelays: []string{"wss://browser.example"}, want: []string{"wss://contextvm.example"}},
		{name: "sidecar off context set browser unset", contextVMRelays: []string{"wss://contextvm.example"}, want: []string{"wss://contextvm.example"}},
		{name: "sidecar off context unset browser set", browserRelays: []string{"wss://browser.example"}, want: []string{"wss://browser.example"}},
		{name: "sidecar off context unset browser unset", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults().Nostr
			cfg.Sidecar.Enabled = tt.sidecarEnabled
			cfg.Sidecar.BackendURL = "ws://relay:3334"
			cfg.Sidecar.PublicURL = "wss://sidecar.example/relay"
			cfg.ContextVMRelays = tt.contextVMRelays
			cfg.BrowserRelays = tt.browserRelays
			if got := contextVMRelayURLs(cfg); !slices.Equal(got, tt.want) {
				t.Fatalf("contextVMRelayURLs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContextVMRelayURLsDeduplicatesSidecarAndPolicy(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.BackendURL = "ws://relay:3334"
	cfg.ContextVMRelays = []string{"ws://relay:3334", "wss://contextvm.example", "wss://contextvm.example"}
	want := []string{"ws://relay:3334", "wss://contextvm.example"}
	if got := contextVMRelayURLs(cfg); !slices.Equal(got, want) {
		t.Fatalf("contextVMRelayURLs() = %v, want %v", got, want)
	}
}

func TestRelayPolicyHydrationRelayURLsIncludesEveryEligibleRelay(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.BackendURL = "ws://relay:3334"
	cfg.Sidecar.PublicURL = "wss://sidecar.example/relay"
	cfg.ContextVMRelays = []string{"wss://primary.example"}
	cfg.BrowserRelays = []string{"wss://secondary.example"}
	cfg.ServiceRelays = []string{"wss://service.example"}
	cfg.Relays = []string{"wss://legacy.example"}
	cfg.NIP34Relays = []string{"wss://nip34.example"}

	got := relayPolicyHydrationRelayURLs(cfg)
	want := []string{
		"ws://relay:3334",
		"wss://sidecar.example/relay",
		"wss://primary.example",
		"wss://secondary.example",
		"wss://service.example",
		"wss://legacy.example",
		"wss://nip34.example",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("relayPolicyHydrationRelayURLs() = %v, want %v", got, want)
	}
}

func TestRelayPolicyHydrationRelayURLsRetainsStoredPolicyRelaysAcrossUpgrade(t *testing.T) {
	configured := []string{"wss://new-image-bootstrap.example"}
	state := controlplane.RelayPolicyState{
		Schema:          controlplane.RelaySettingsSchema,
		BrowserRelays:   []string{"wss://stored-browser.example"},
		ContextVMRelays: []string{"wss://stored-contextvm.example"},
		ServiceRelays:   []string{"wss://stored-service.example"},
	}
	got := relayPolicyHydrationRelayURLsForState(configured, state)
	want := []string{
		"wss://new-image-bootstrap.example",
		"wss://stored-contextvm.example",
		"wss://stored-browser.example",
		"wss://stored-service.example",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("relayPolicyHydrationRelayURLsForState() = %v, want %v", got, want)
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

func TestInteropRelayURLsWithoutMirrorKeepsSidecarAndUpstreamRelays(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.Relays = []string{"wss://upstream.example"}
	cfg.Loom.Relays = []string{"wss://loom.example"}
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.MirrorExternal = false

	got := interopRelayURLs(cfg, []string{"ws://relay:3334"})
	want := []string{"ws://relay:3334", "wss://upstream.example", "wss://loom.example"}
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
	requestPool := nostrAdapter.NewRelayPool([]string{"wss://old-contextvm.example"}, zap.NewNop())
	responsePool := nostrAdapter.NewRelayPool([]string{"wss://old-contextvm.example"}, zap.NewNop())
	servicePool := nostrAdapter.NewRelayPool([]string{"wss://old-service.example", "wss://loom.example"}, zap.NewNop())
	coordinator := newRelayTopologyCoordinator(relayTopologyCoordinatorConfig{
		ControlPlanePool:      controlPlanePool,
		ContextVMRequestPool:  requestPool,
		ContextVMResponsePool: responsePool,
		ServicePool:           servicePool,
		NostrConfig:           cfg.Nostr,
		LoomRelays:            cfg.Loom.Relays,
		Logger:                zap.NewNop(),
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
	if got, want := requestPool.URLs(), []string{"wss://new-contextvm.example"}; !slices.Equal(got, want) {
		t.Fatalf("ContextVM request pool URLs = %v, want %v", got, want)
	}
	if got, want := responsePool.URLs(), []string{"wss://new-contextvm.example"}; !slices.Equal(got, want) {
		t.Fatalf("ContextVM response pool URLs = %v, want %v", got, want)
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
	requestPool := nostrAdapter.NewRelayPool([]string{"ws://relay:3334"}, zap.NewNop())
	responsePool := nostrAdapter.NewRelayPool([]string{"ws://relay:3334"}, zap.NewNop())
	servicePool := nostrAdapter.NewRelayPool([]string{"ws://relay:3334", "wss://loom.example"}, zap.NewNop())
	coordinator := newRelayTopologyCoordinator(relayTopologyCoordinatorConfig{
		ControlPlanePool:      controlPlanePool,
		ContextVMRequestPool:  requestPool,
		ContextVMResponsePool: responsePool,
		ServicePool:           servicePool,
		NostrConfig:           cfg.Nostr,
		LoomRelays:            cfg.Loom.Relays,
		Logger:                zap.NewNop(),
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
	if got, want := requestPool.URLs(), []string{"ws://relay:3334", "wss://external-contextvm.example"}; !slices.Equal(got, want) {
		t.Fatalf("ContextVM request pool URLs = %v, want union %v", got, want)
	}
	if got, want := responsePool.URLs(), []string{"ws://relay:3334", "wss://external-contextvm.example"}; !slices.Equal(got, want) {
		t.Fatalf("ContextVM response pool URLs = %v, want union %v", got, want)
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
