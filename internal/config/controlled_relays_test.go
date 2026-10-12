package config

import (
	"strings"
	"testing"
)

const (
	testSidecarPublicURL = "wss://bahia.sharegap.net/relay"
	testSharedRelay      = "wss://relay.sharegap.net"
	testPublicRelay      = "wss://relay.damus.io"
)

func requireValidationError(t *testing.T, cfg *Config, want string) {
	t.Helper()
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("validate() error = %v, want it to contain %q", err, want)
	}
}

// The 2026-10-11 production configuration published the control plane to a
// public relay; it now fails startup.
func TestControlPlaneRelaysRejectPublicRelays(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.Relays = []string{testSharedRelay, testPublicRelay}
	requireValidationError(t, cfg, "nostr service_relays lists public relay "+testPublicRelay)

	cfg = Defaults()
	cfg.Nostr.ServiceRelays = []string{testSharedRelay}
	cfg.Nostr.BrowserRelays = []string{testPublicRelay}
	requireValidationError(t, cfg, "lists public relay "+testPublicRelay)

	// allowed_public_relays is for non-control-plane lists only.
	cfg = Defaults()
	cfg.Nostr.ServiceRelays = []string{testSharedRelay, testPublicRelay}
	cfg.Nostr.AllowedPublicRelays = []string{testPublicRelay}
	requireValidationError(t, cfg, "lists public relay "+testPublicRelay)

	cfg = Defaults()
	cfg.Nostr.ServiceRelays = []string{testSharedRelay}
	cfg.Nostr.ControlledRelays = []string{testSharedRelay, "wss://nos.lol"}
	requireValidationError(t, cfg, "nostr.controlled_relays lists public relay wss://nos.lol")
}

func TestControlPlaneRelaysMustBeControlled(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.ServiceRelays = []string{testSidecarPublicURL, testSharedRelay}
	cfg.Nostr.ControlledRelays = []string{testSidecarPublicURL}
	requireValidationError(t, cfg, "nostr service_relays lists "+testSharedRelay+", which is not in nostr.controlled_relays")

	cfg = Defaults()
	cfg.Nostr.ServiceRelays = []string{testSidecarPublicURL}
	cfg.Nostr.ContextVMRelays = []string{"wss://contextvm.example"}
	cfg.Nostr.ControlledRelays = []string{testSidecarPublicURL}
	requireValidationError(t, cfg, "contextvm_relays lists wss://contextvm.example")

	// A publishing service identity must name its controlled relays: an
	// unset list never silently trusts the relays it is meant to check.
	cfg = Defaults()
	cfg.Nostr.PrivateKey = "test-secret-key"
	cfg.Nostr.ServiceRelays = []string{testSidecarPublicURL, "wss://relay.nostr.net"}
	requireValidationError(t, cfg, "nostr.controlled_relays is required when the daemon publishes with a service identity")

	// Unset means the sidecar alone: a sidecar-only topology needs no list.
	cfg = Defaults()
	cfg.Nostr.PrivateKey = "test-secret-key"
	cfg.Nostr.Sidecar = RelaySidecarConfig{Enabled: true, PublicURL: testSidecarPublicURL, BackendURL: "ws://relay:3334/relay"}
	cfg.Nostr.ContextVMRelays = []string{"ws://relay:3334/relay"}
	cfg.Nostr.BrowserRelays = []string{testSidecarPublicURL}
	if got := cfg.Nostr.ControlledRelaySet(); len(got) != 2 {
		t.Fatalf("ControlledRelaySet() = %v, want the sidecar URLs", got)
	}
	if !cfg.Nostr.ControlledRelaysUnset() {
		t.Fatal("ControlledRelaysUnset() = false for a signing daemon without controlled_relays")
	}
	err := cfg.Nostr.ValidateControlPlaneRelays("canonical relay settings", ControlPlaneRelays{Service: []string{testSharedRelay}})
	if err == nil || !strings.Contains(err.Error(), "not in nostr.controlled_relays") {
		t.Fatalf("canonical relay outside the sidecar-only controlled set: err = %v", err)
	}
}

func TestPublishQuorumMustFitServiceWriteRelays(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.ServiceRelays = []string{testSidecarPublicURL, testSharedRelay}
	cfg.Nostr.PublishQuorum = 2
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() error for the production target: %v", err)
	}
	assertStringSlice(t, cfg.Nostr.ServiceWriteRelays(cfg.Nostr.ServiceRelays), []string{testSidecarPublicURL, testSharedRelay})

	cfg = Defaults()
	cfg.Nostr.ServiceRelays = []string{testSidecarPublicURL, testSharedRelay}
	cfg.Nostr.PublishQuorum = 3
	requireValidationError(t, cfg, "fewer than nostr.publish_quorum 3")

	cfg = Defaults()
	cfg.Nostr.ServiceRelays = []string{testSidecarPublicURL, testSharedRelay}
	cfg.Nostr.PublishQuorum = PublishQuorumAllRelays
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() error with publish_quorum -1: %v", err)
	}
}

func TestServiceWriteRelaysCountTheSidecarOnce(t *testing.T) {
	cfg := NostrConfig{Sidecar: RelaySidecarConfig{Enabled: true, PublicURL: testSidecarPublicURL, BackendURL: "ws://relay:3334/relay"}}
	assertStringSlice(t, cfg.ServiceWriteRelays([]string{testSidecarPublicURL, testSharedRelay}), []string{"ws://relay:3334/relay", testSharedRelay})
	assertStringSlice(t, cfg.ServiceWriteRelays(nil), []string{"ws://relay:3334/relay"})

	cfg.Sidecar.MirrorExternal = true
	assertStringSlice(t, cfg.ServiceWriteRelays([]string{testSharedRelay}), []string{"ws://relay:3334/relay"})

	cfg = NostrConfig{PublishQuorum: 2, Sidecar: RelaySidecarConfig{Enabled: true, PublicURL: testSidecarPublicURL, BackendURL: "ws://relay:3334/relay"}}
	err := cfg.ValidateControlPlaneRelays("canonical relay settings", ControlPlaneRelays{Service: []string{testSidecarPublicURL}})
	if err == nil || !strings.Contains(err.Error(), "fewer than nostr.publish_quorum 2") {
		t.Fatalf("sidecar public URL counted as a second write relay: err = %v", err)
	}
	if got := cfg.ControlledRelaySet(); len(got) != 2 {
		t.Fatalf("ControlledRelaySet() = %v, want the sidecar backend and public URLs", got)
	}
}

func TestNonControlPlaneListsNeedExplicitPublicRelayAllowance(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.ServiceRelays = []string{testSharedRelay}
	cfg.Loom.Relays = []string{testPublicRelay}
	requireValidationError(t, cfg, "loom.relays lists public relay "+testPublicRelay+"; list it in nostr.allowed_public_relays")

	cfg = Defaults()
	cfg.Nostr.ServiceRelays = []string{testSharedRelay}
	cfg.Nostr.NIP34Relays = []string{"wss://nos.lol"}
	requireValidationError(t, cfg, "nostr.nip34_relays lists public relay wss://nos.lol")

	cfg = Defaults()
	cfg.Nostr.ServiceRelays = []string{testSharedRelay}
	cfg.Loom.Relays = []string{testPublicRelay}
	cfg.Nostr.NIP34Relays = []string{"wss://nos.lol/"}
	cfg.Nostr.AllowedPublicRelays = []string{testPublicRelay, "wss://nos.lol"}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() error with explicitly allowed public relays: %v", err)
	}
}
