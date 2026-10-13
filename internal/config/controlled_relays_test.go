package config

import (
	"os"
	"path/filepath"
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
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.PublicURL = testSidecarPublicURL
	cfg.Nostr.Sidecar.BackendURL = "ws://relay:3334/relay"
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

func TestControlPlanePublishQuorumValidationAndResolution(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.PublicURL = testSidecarPublicURL
	cfg.Nostr.Sidecar.BackendURL = "ws://localhost:3334/relay"
	cfg.Nostr.ServiceRelays = []string{testSidecarPublicURL, testSharedRelay}
	cfg.Nostr.ControlledRelays = []string{testSharedRelay}
	cfg.Nostr.PublishQuorum = 2
	if err := cfg.validate(); err != nil {
		t.Fatalf("inherited capped quorum must load with warning, not fail: %v", err)
	}
	if requested, effective, capped := cfg.Nostr.ResolveControlPlanePublishQuorum(1); requested != 2 || effective != 1 || !capped {
		t.Fatalf("default resolution = %d/%d capped=%t, want 2/1 true", requested, effective, capped)
	}
	one := 1
	cfg.Nostr.ControlPlanePublishQuorum = &one
	if err := cfg.validate(); err != nil {
		t.Fatalf("explicit quorum one: %v", err)
	}
	if requested, effective, capped := cfg.Nostr.ResolveControlPlanePublishQuorum(1); requested != 1 || effective != 1 || capped {
		t.Fatalf("explicit resolution = %d/%d capped=%t, want 1/1 false", requested, effective, capped)
	}
	two := 2
	cfg.Nostr.ControlPlanePublishQuorum = &two
	requireValidationError(t, cfg, "fewer than nostr.control_plane_publish_quorum 2")
	zero := 0
	cfg.Nostr.ControlPlanePublishQuorum = &zero
	requireValidationError(t, cfg, "nostr.control_plane_publish_quorum must be >= 1")
	all := PublishQuorumAllRelays
	cfg.Nostr.ControlPlanePublishQuorum = &all
	if err := cfg.validate(); err != nil {
		t.Fatalf("explicit all-relays quorum: %v", err)
	}
}

func TestCanonicalControlPlanePolicyCannotShrinkExplicitQuorum(t *testing.T) {
	cfg := Defaults().Nostr
	cfg.Sidecar.Enabled = false
	cfg.ControlledRelays = []string{"wss://first.example", "wss://second.example"}
	two := 2
	cfg.ControlPlanePublishQuorum = &two
	lists := ControlPlaneRelays{ContextVM: []string{"wss://first.example", "wss://second.example"}}
	if err := cfg.ValidateControlPlaneRelays("canonical relay settings", lists); err != nil {
		t.Fatalf("two-relay policy: %v", err)
	}
	lists.ContextVM = []string{"wss://first.example"}
	if err := cfg.ValidateControlPlaneRelays("canonical relay settings", lists); err == nil || !strings.Contains(err.Error(), "fewer than nostr.control_plane_publish_quorum 2") {
		t.Fatalf("shrunken policy must fail closed: %v", err)
	}
}

func TestControlPlanePublishQuorumLoadsFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("nostr:\n  contextvm_relays: [wss://controlled.example]\n  control_plane_publish_quorum: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Nostr.ControlPlanePublishQuorum == nil || *cfg.Nostr.ControlPlanePublishQuorum != 1 {
		t.Fatalf("loaded control-plane quorum = %v, want 1", cfg.Nostr.ControlPlanePublishQuorum)
	}
}

func TestMirrorExternalFailsClosedOnTargetsAndDaemonQuorum(t *testing.T) {
	cfg := Defaults()
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.MirrorExternal = true
	cfg.Nostr.Sidecar.BackendURL = "ws://localhost:3334/relay"
	cfg.Nostr.Sidecar.PublicURL = testSidecarPublicURL
	cfg.Nostr.ServiceRelays = []string{testSidecarPublicURL, testSharedRelay}
	cfg.Nostr.ControlledRelays = []string{testSharedRelay}
	cfg.Nostr.PublishQuorum = 2
	requireValidationError(t, cfg, "fewer than nostr.publish_quorum 2")
	cfg.Nostr.PublishQuorum = 1
	if err := cfg.validate(); err != nil {
		t.Fatalf("controlled mirror with daemon quorum one: %v", err)
	}
	assertStringSlice(t, cfg.Nostr.ServiceWriteRelays(cfg.Nostr.ServiceRelays), []string{"ws://localhost:3334/relay"})
	assertStringSlice(t, mustMirrorTargets(t, cfg.Nostr), []string{testSharedRelay})
	if err := cfg.Nostr.ValidateControlPlaneRelays("canonical relay settings", ControlPlaneRelays{
		Service: []string{testSidecarPublicURL, "wss://different.example"},
	}); err == nil {
		t.Fatal("canonical mirror target change accepted")
	}
	cfg.Nostr.ControlledRelays = nil
	requireValidationError(t, cfg, "not in nostr.controlled_relays")
	cfg.Nostr.ControlledRelays = []string{testSharedRelay}
	cfg.Nostr.ServiceRelays = []string{testSidecarPublicURL, testPublicRelay}
	requireValidationError(t, cfg, "refuses public service relay")
}

func mustMirrorTargets(t *testing.T, cfg NostrConfig) []string {
	t.Helper()
	targets, err := cfg.MirrorExternalRelays()
	if err != nil {
		t.Fatal(err)
	}
	return targets
}
