package app

import (
	"slices"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
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
	cfg.Nostr.PrivateRelays = []string{"wss://private.example"}
	cfg.Loom.Relays = []string{"wss://loom.example"}
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.MirrorExternal = true

	got := interopRelayURLs(cfg, []string{"ws://relay:3334"})
	want := []string{"ws://relay:3334", "wss://private.example", "wss://loom.example"}
	if !slices.Equal(got, want) {
		t.Fatalf("interopRelayURLs() = %v, want %v", got, want)
	}
}

func TestInteropRelayURLsWithoutMirrorKeepsUpstreamRelays(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.Relays = []string{"wss://upstream.example"}
	cfg.Nostr.PrivateRelays = []string{"wss://private.example"}
	cfg.Loom.Relays = []string{"wss://loom.example"}
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.MirrorExternal = false

	got := interopRelayURLs(cfg, []string{"ws://relay:3334"})
	want := []string{"wss://upstream.example", "wss://private.example", "wss://loom.example"}
	if !slices.Equal(got, want) {
		t.Fatalf("interopRelayURLs() = %v, want %v", got, want)
	}
}
