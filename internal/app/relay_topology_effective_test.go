package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

const (
	effectiveSidecarRelay = "wss://bahia.sharegap.net/relay"
	effectiveSharedRelay  = "wss://relay.sharegap.net"
	effectiveLegacyRelay  = "wss://legacy.sharegap.example"
	effectiveLoomRelay    = "wss://loom.example"
)

type countingAdvertiser struct{ calls int }

func (a *countingAdvertiser) advertise(context.Context) error {
	a.calls++
	return nil
}

func newEffectiveTopologyFixture(t *testing.T) (*relayTopologyCoordinator, *nostrAdapter.RelayPool, *countingAdvertiser) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Nostr.ServiceRelays = []string{effectiveSharedRelay, effectiveLegacyRelay}
	cfg.Nostr.BrowserRelays = []string{effectiveSidecarRelay}
	cfg.Nostr.ControlledRelays = []string{effectiveSidecarRelay, effectiveSharedRelay, effectiveLegacyRelay}
	cfg.Nostr.PublishQuorum = 2
	cfg.Loom.Relays = []string{effectiveLoomRelay}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("static config: %v", err)
	}
	servicePool := nostrAdapter.NewRelayPool(serviceRelayPoolURLs(cfg.Nostr, cfg.Nostr.ServiceRelays, cfg.Loom.Relays), zap.NewNop())
	coordinator := newRelayTopologyCoordinator(relayTopologyCoordinatorConfig{
		ServicePool: servicePool,
		NostrConfig: cfg.Nostr,
		LoomRelays:  cfg.Loom.Relays,
		Logger:      zap.NewNop(),
	})
	advertiser := &countingAdvertiser{}
	coordinator.SetAdvertiser(advertiser.advertise)
	return coordinator, servicePool, advertiser
}

// bahia-zv6zb: static and canonical relay lists diverge; once the canonical
// settings arrive they drive the service pool, the publisher's write set and
// quorum accounting, and the advertised relay set, and health reports the
// drift.
func TestRelayTopologyCanonicalSettingsGovernOverStaticConfig(t *testing.T) {
	coordinator, servicePool, advertiser := newEffectiveTopologyFixture(t)

	health := coordinator.Health()
	if health.Status != HealthStatusPass || health.Details["source"] != relayTopologySourceBootstrap {
		t.Fatalf("bootstrap health = %#v", health)
	}
	if got, want := coordinator.ServiceWriteRelays(), []string{effectiveSharedRelay, effectiveLegacyRelay}; !slices.Equal(got, want) {
		t.Fatalf("bootstrap write relays = %v, want %v", got, want)
	}
	if _, ok := coordinator.AdvertisedRelays(); ok {
		t.Fatal("nothing is advertised before the canonical relay settings catch up")
	}

	canonical := []string{effectiveSharedRelay, effectiveSidecarRelay}
	if err := coordinator.ApplySnapshot(t.Context(), controlplane.RelayPolicyState{
		Schema:        controlplane.RelaySettingsSchema,
		ServiceRelays: canonical,
		BrowserRelays: []string{effectiveSidecarRelay},
	}); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if advertiser.calls != 0 {
		t.Fatalf("advertised %d times before catch-up", advertiser.calls)
	}
	if got, want := coordinator.ServiceWriteRelays(), canonical; !slices.Equal(got, want) {
		t.Fatalf("write relays = %v, want canonical %v", got, want)
	}
	if got, want := servicePool.URLs(), append(slices.Clone(canonical), effectiveLoomRelay); !slices.Equal(got, want) {
		t.Fatalf("service pool = %v, want %v", got, want)
	}

	coordinator.MarkCaughtUp(t.Context())
	if advertiser.calls != 1 {
		t.Fatalf("advertiser calls after catch-up = %d, want 1", advertiser.calls)
	}
	advertised, ok := coordinator.AdvertisedRelays()
	if !ok || !slices.Equal(advertised.Service, canonical) {
		t.Fatalf("advertised service relays = %v (ok=%v), want canonical %v", advertised.Service, ok, canonical)
	}

	health = coordinator.Health()
	if health.Status != HealthStatusWarn || health.Details["source"] != relayTopologySourceCanonical || health.Details["drift"] != "service_relays" {
		t.Fatalf("drift health = %#v", health)
	}
	if health.Details["required_acceptances"] != "2" || health.Details["service_write_relays"] != effectiveSharedRelay+","+effectiveSidecarRelay {
		t.Fatalf("quorum health details = %#v", health.Details)
	}

	// A later change applies and is advertised at once.
	if err := coordinator.ApplySnapshot(t.Context(), controlplane.RelayPolicyState{
		Schema:        controlplane.RelaySettingsSchema,
		ServiceRelays: []string{effectiveSharedRelay, effectiveLegacyRelay},
		BrowserRelays: []string{effectiveSidecarRelay},
	}); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if advertiser.calls != 2 {
		t.Fatalf("advertiser calls after change = %d, want 2", advertiser.calls)
	}
	if health := coordinator.Health(); health.Status != HealthStatusPass {
		t.Fatalf("canonical settings matching static config must not report drift: %#v", health)
	}
}

// bahia-ud5sw: canonical relay settings cannot move the control plane onto
// a public or uncontrolled relay, or below the publish quorum.
func TestRelayTopologyRefusesCanonicalSettingsOutsideTheGuard(t *testing.T) {
	for name, service := range map[string][]string{
		"public relay":          {effectiveSharedRelay, "wss://relay.damus.io"},
		"uncontrolled relay":    {effectiveSharedRelay, "wss://attacker.example"},
		"fewer than the quorum": {effectiveSharedRelay},
	} {
		t.Run(name, func(t *testing.T) {
			coordinator, servicePool, advertiser := newEffectiveTopologyFixture(t)
			coordinator.MarkCaughtUp(t.Context())
			before := servicePool.URLs()

			err := coordinator.ApplySnapshot(t.Context(), controlplane.RelayPolicyState{
				Schema:        controlplane.RelaySettingsSchema,
				ServiceRelays: service,
			})
			if !errors.Is(err, errRelayPolicyRefused) {
				t.Fatalf("ApplySnapshot error = %v, want errRelayPolicyRefused", err)
			}
			if got := servicePool.URLs(); !slices.Equal(got, before) {
				t.Fatalf("refused settings moved the service pool to %v", got)
			}
			if got, want := coordinator.ServiceWriteRelays(), []string{effectiveSharedRelay, effectiveLegacyRelay}; !slices.Equal(got, want) {
				t.Fatalf("refused settings changed the write relays to %v", got)
			}
			if advertiser.calls != 1 {
				t.Fatalf("refused settings were advertised (%d calls)", advertiser.calls)
			}
			health := coordinator.Health()
			if health.Status != HealthStatusWarn || health.Details["refused"] == "" || health.Details["source"] != relayTopologySourceBootstrap {
				t.Fatalf("refusal health = %#v", health)
			}
		})
	}
}

func TestRelayTopologyHealthWarnsWhenCatchUpStalls(t *testing.T) {
	coordinator, _, advertiser := newEffectiveTopologyFixture(t)
	start := coordinator.startedAt
	clock := start
	coordinator.now = func() time.Time { return clock }

	clock = start.Add(relayTopologyCatchUpWarnAfter - time.Second)
	if health := coordinator.Health(); health.Status != HealthStatusPass {
		t.Fatalf("health within the catch-up bound = %#v", health)
	}
	clock = start.Add(relayTopologyCatchUpWarnAfter + time.Second)
	health := coordinator.Health()
	if health.Status != HealthStatusWarn || !strings.Contains(health.Message, "have not caught up") {
		t.Fatalf("health past the catch-up bound = %#v", health)
	}
	coordinator.MarkCaughtUp(t.Context())
	if health := coordinator.Health(); health.Status != HealthStatusPass || advertiser.calls != 1 {
		t.Fatalf("health after catch-up = %#v (advertised %d)", health, advertiser.calls)
	}
}

func TestRelayTopologyHealthWarnsWhileControlledRelaysUnset(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = "test-secret-key"
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.BackendURL = "ws://relay:3334/relay"
	cfg.Nostr.Sidecar.PublicURL = effectiveSidecarRelay
	cfg.Nostr.ContextVMRelays = []string{"ws://relay:3334/relay"}
	coordinator := newRelayTopologyCoordinator(relayTopologyCoordinatorConfig{NostrConfig: cfg.Nostr, Logger: zap.NewNop()})
	health := coordinator.Health()
	if health.Status != HealthStatusWarn || !strings.Contains(health.Message, "nostr.controlled_relays is unset") {
		t.Fatalf("health with controlled_relays unset = %#v", health)
	}
}

// The hydrator's validator records refusals for health before anything is
// promoted, including public relays in the canonical NIP-34 and DM lists.
func TestRelayTopologyValidateSnapshotRecordsRefusal(t *testing.T) {
	coordinator, _, _ := newEffectiveTopologyFixture(t)
	err := coordinator.ValidateSnapshot(controlplane.RelayPolicyState{
		Schema:      controlplane.RelaySettingsSchema,
		NIP34Relays: []string{"wss://relay.damus.io"},
	})
	if !errors.Is(err, errRelayPolicyRefused) {
		t.Fatalf("ValidateSnapshot error = %v, want errRelayPolicyRefused", err)
	}
	health := coordinator.Health()
	if health.Status != HealthStatusWarn || !strings.Contains(health.Details["refused"], "nip34_relays lists public relay") {
		t.Fatalf("refusal health = %#v", health)
	}
	if err := coordinator.ValidateSnapshot(controlplane.RelayPolicyState{Schema: controlplane.RelaySettingsSchema, ServiceRelays: []string{effectiveSharedRelay, effectiveSidecarRelay}}); err != nil {
		t.Fatalf("valid settings: %v", err)
	}
}
