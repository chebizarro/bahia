package identityrecovery

import (
	"context"
	"errors"
	"reflect"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// RelayRecoveryAdapter preserves the accepted policy head and advertised
// relay-set heads. The recovery source/publisher is bootstrapped from the
// operator-controlled relay configuration, never from these untrusted records.
type RelayRecoveryAdapter struct {
	OldPubkey nostr.PubKey
	Source    EventSource
	Local     RecoveryLocalStore
	Ledger    *Ledger
	Static    config.NostrConfig
}

func (a *RelayRecoveryAdapter) core() canonicalHeadAdapter {
	return canonicalHeadAdapter{
		old: a.OldPubkey, source: a.Source, local: a.Local, ledger: a.Ledger,
		kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState), nostr.Kind(kinds.RelaySetDiscovery)}, name: "relay-settings-and-sets",
		belongs: func(ev nostr.Event) bool {
			if ev.Kind == nostr.Kind(kinds.CASControlState) {
				return tag(ev, "d") == controlplane.RelaySettingsDTag
			}
			if ev.Kind != nostr.Kind(kinds.RelaySetDiscovery) {
				return false
			}
			switch tag(ev, "d") {
			case adapter.BrowserRelaySetDTag, adapter.ContextVMRelaySetDTag, adapter.ServiceRelaySetDTag, adapter.NIP34RelaySetDTag:
				return true
			}
			return false
		},
		preflight: a.preflight,
		publish:   a.publish,
	}
}

func (a *RelayRecoveryAdapter) preflight(old nostr.Event) error {
	var tags nostr.Tags
	var content string
	var err error
	switch old.Kind {
	case nostr.Kind(kinds.CASControlState):
		tags, content, err = controlplane.RelaySettingsRecoveryRecord(old, a.Static)
	case nostr.Kind(kinds.RelaySetDiscovery):
		var relays []string
		for _, t := range old.Tags {
			if len(t) >= 2 && t[0] == "relay" {
				relays = append(relays, t[1])
			}
		}
		tags, content, err = adapter.RelaySetRecoveryRecord(tag(old, "d"), relays)
	default:
		return errors.New("unsupported relay recovery kind")
	}
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(tags, old.Tags) || content != old.Content {
		return errors.New("typed relay builder cannot reproduce accepted head")
	}
	return nil
}

func (a *RelayRecoveryAdapter) publish(ctx context.Context, signer nostr.Keyer, old nostr.Event, sink registryRecoverySink) error {
	switch old.Kind {
	case nostr.Kind(kinds.CASControlState):
		tags, content, err := controlplane.RelaySettingsRecoveryRecord(old, a.Static)
		if err != nil {
			return err
		}
		return controlplane.PublishRecoveredRelaySettings(ctx, signer, sink, tags, content)
	case nostr.Kind(kinds.RelaySetDiscovery):
		var relays []string
		for _, t := range old.Tags {
			if len(t) >= 2 && t[0] == "relay" {
				relays = append(relays, t[1])
			}
		}
		pub, err := signer.GetPublicKey(ctx)
		if err != nil {
			return err
		}
		projector := adapter.NewProjector(config.NostrConfig{PublishEnabled: true}, nil, sink, nil, zap.NewNop(), adapter.WithProjectorSigner(signer, pub.Hex()))
		return projector.PublishRecoveredRelaySet(ctx, tag(old, "d"), relays)
	}
	return errors.New("unsupported relay recovery kind")
}

func (a *RelayRecoveryAdapter) Inventory(ctx context.Context) ([]string, error) {
	return a.core().Inventory(ctx)
}
func (a *RelayRecoveryAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	return a.core().Reassert(ctx, signer)
}
func (a *RelayRecoveryAdapter) Verify(ctx context.Context) ([]string, error) {
	return a.core().Verify(ctx)
}
