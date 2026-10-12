package nostr

import (
	"context"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type switchableRelaySource struct {
	relays AdvertisedRelays
	ready  bool
}

func (s *switchableRelaySource) AdvertisedRelays() (AdvertisedRelays, bool) { return s.relays, s.ready }

func relayTagValues(ev gonostr.Event) []string {
	var relays []string
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "relay" {
			relays = append(relays, tag[1])
		}
	}
	return relays
}

// The advertised bahia-service-v1 relay set follows the relay lists in force,
// not the static configuration, and is withheld until they are known.
func TestProjectorAdvertisesEffectiveRelaysOnlyOnceKnown(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = projectorTestPrivateKey
	cfg.Nostr.PublishEnabled = true
	cfg.Nostr.BrowserRelays = []string{"wss://bahia.sharegap.net/relay"}
	cfg.Nostr.ServiceRelays = []string{"wss://relay.sharegap.net", "wss://relay.static.example"}

	source := &switchableRelaySource{}
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(cfg.Nostr, newFakeProjectionSource(), sink, nil, zap.NewNop(),
		WithSystemDiscoveryConfig(cfg, true), WithAdvertisedRelaySource(source))

	require.NoError(t, projector.publishSystemDiscovery(ctx))
	assertOneSignedKind(t, sink, kinds.ContextVMServerAnnouncement)
	assertNoPublishedKind(t, sink, kinds.RelaySetDiscovery)
	assertNoPublishedKind(t, sink, kinds.NIP65RelayList)

	canonical := []string{"wss://relay.sharegap.net", "wss://bahia.sharegap.net/relay"}
	source.relays = AdvertisedRelays{
		Browser:   []string{"wss://bahia.sharegap.net/relay"},
		ContextVM: []string{"wss://bahia.sharegap.net/relay"},
		Service:   canonical,
	}
	source.ready = true
	require.NoError(t, projector.PublishAdvertisedRelays(ctx))
	service := assertOneRelaySet(t, sink, ServiceRelaySetDTag)
	require.Equal(t, canonical, relayTagValues(service), "bahia-service-v1 must list the canonical service relays")
	assertOneSignedKind(t, sink, kinds.NIP65RelayList)
	assertNoRelaySet(t, sink, NIP34RelaySetDTag)
}
