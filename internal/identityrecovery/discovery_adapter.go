package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

type discoverySnapshot []nostr.Event

func (s discoverySnapshot) QueryEvents(f nostr.Filter) iter.Seq[nostr.Event] {
	return func(yield func(nostr.Event) bool) {
		for _, ev := range s {
			if f.Matches(ev) && !yield(ev) {
				return
			}
		}
	}
}

func (a *DiscoveryRecoveryAdapter) observedSnapshot(ctx context.Context, pub nostr.PubKey) (discoverySnapshot, error) {
	events, err := a.Local.Events(ctx, pub)
	if err != nil {
		return nil, err
	}
	heads := map[string]nostr.Event{}
	for _, ev := range events {
		if ev.PubKey != pub || !ev.CheckID() || !ev.VerifySignature() {
			return nil, errors.New("discovery observed local event invalid")
		}
		key := fmt.Sprintf("%d:%s", ev.Kind, tag(ev, "d"))
		if prior, ok := heads[key]; !ok || newer(ev, prior) {
			heads[key] = ev
		}
	}
	result := make(discoverySnapshot, 0, len(heads))
	for _, ev := range heads {
		result = append(result, ev)
	}
	return result, nil
}

// DiscoveryRecoveryAdapter is a regeneration domain. Its new-key content is
// built from current deployment configuration, not compared to the stale
// old-key announcement. Verification requires the live publisher's envelope.
type DiscoveryRecoveryAdapter struct {
	OldPubkey           nostr.PubKey
	Source              EventSource
	Local               RecoveryLocalStore
	Ledger              *Ledger
	Config              *config.Config
	MCPTransportEnabled bool
}

func (a *DiscoveryRecoveryAdapter) core() canonicalHeadAdapter {
	return canonicalHeadAdapter{old: a.OldPubkey, source: a.Source, local: a.Local, ledger: a.Ledger,
		kinds: []nostr.Kind{nostr.Kind(kinds.SystemDiscovery), nostr.Kind(kinds.ContextVMServerAnnouncement)}, name: "discovery-system",
		belongs:   func(ev nostr.Event) bool { return tag(ev, "d") == adapter.SystemDiscoveryDTag },
		preflight: validateDiscoveryHead,
		publish: func(context.Context, nostr.Keyer, nostr.Event, registryRecoverySink) error {
			return ErrAdapterNotRegistered
		},
	}
}

func validateDiscoveryHead(ev nostr.Event) error {
	if ev.Kind != nostr.Kind(kinds.SystemDiscovery) && ev.Kind != nostr.Kind(kinds.ContextVMServerAnnouncement) || !ev.CheckID() || !ev.VerifySignature() || !reflect.DeepEqual(ev.Tags, nostr.Tags{{"d", adapter.SystemDiscoveryDTag}, {"schema", adapter.SystemDiscoverySchema}, {"name", adapter.SystemDiscoveryName}}) {
		return errors.New("system discovery signature or envelope invalid")
	}
	var body struct {
		Schema       string          `json:"schema"`
		Registries   json.RawMessage `json:"registries"`
		ControlPlane json.RawMessage `json:"control_plane"`
		Features     json.RawMessage `json:"features"`
	}
	if json.Unmarshal([]byte(ev.Content), &body) != nil || body.Schema != adapter.SystemDiscoverySchema || len(body.Registries) == 0 || len(body.ControlPlane) == 0 || len(body.Features) == 0 {
		return errors.New("system discovery body malformed")
	}
	return nil
}

func (a *DiscoveryRecoveryAdapter) Inventory(ctx context.Context) ([]string, error) {
	return a.core().Inventory(ctx)
}

func (a *DiscoveryRecoveryAdapter) head(ctx context.Context, pub nostr.PubKey) (*nostr.Event, *nostr.Event, error) {
	kind := nostr.Kind(kinds.ContextVMServerAnnouncement)
	// 11316 is replaceable by (kind, author), not by d. A newer wrong-d head
	// must not be hidden by a d-scoped filter and mistaken for a valid one.
	relay, err := a.Source.Read(ctx, nostr.Filter{Authors: []nostr.PubKey{pub}, Kinds: []nostr.Kind{kind}})
	if err != nil {
		return nil, nil, err
	}
	local, err := a.Local.Events(ctx, pub)
	if err != nil {
		return nil, nil, err
	}
	return latestDiscovery(relay, pub, kind), latestDiscovery(local, pub, kind), nil
}

func latestDiscovery(events []nostr.Event, pub nostr.PubKey, kind nostr.Kind) *nostr.Event {
	var head *nostr.Event
	for _, ev := range events {
		if ev.PubKey != pub || ev.Kind != kind {
			continue
		}
		if head == nil || newer(ev, *head) {
			copy := ev
			head = &copy
		}
	}
	return head
}

func (a *DiscoveryRecoveryAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	core := a.core()
	if _, err := core.heads(ctx); err != nil {
		return nil, err
	}
	s, err := core.state()
	if err != nil {
		return nil, err
	}
	if a.Config == nil || len(a.Config.Nostr.BrowserRelayPolicyRelays()) == 0 || signer == nil {
		return nil, fmt.Errorf("discovery live configuration: %w", ErrAdapterNotRegistered)
	}
	pub, err := signer.GetPublicKey(ctx)
	if err != nil || pub.Hex() != s.NewPubkey || pub == a.OldPubkey {
		return nil, errors.New("discovery new signer mismatch")
	}
	cfg := *a.Config
	cfg.Nostr.PublicKey = pub.Hex()
	observed, err := a.observedSnapshot(ctx, pub)
	if err != nil {
		return nil, err
	}
	wantTags, wantContent, err := adapter.SystemDiscoveryRecoveryRecord(ctx, &cfg, a.MCPTransportEnabled, signer, pub.Hex(), observed)
	if err != nil {
		return nil, err
	}
	relay, local, err := a.head(ctx, pub)
	if err != nil {
		return nil, err
	}
	for _, head := range []*nostr.Event{relay, local} {
		if head != nil {
			if err := validateDiscoveryHead(*head); err != nil {
				return nil, err
			}
		}
	}
	current := func(ev *nostr.Event) bool {
		return ev != nil && ev.Content == wantContent && reflect.DeepEqual(ev.Tags, wantTags)
	}
	switch {
	case current(relay) && current(local) && relay.ID == local.ID:
	case current(relay) && local == nil:
		err = a.Local.StoreEvent(ctx, *relay)
	case current(local) && relay == nil:
		err = a.Source.Publish(ctx, *local)
	default:
		sink := registryRecoverySink{source: a.Source, local: a.Local}
		projector := adapter.NewProjector(config.NostrConfig{PublishEnabled: true}, nil, sink, nil, zap.NewNop(), adapter.WithProjectorSigner(signer, pub.Hex()), adapter.WithSystemDiscoveryConfig(&cfg, a.MCPTransportEnabled), adapter.WithObservedDeploymentState(observed))
		err = adapter.NewSystemConfigCanonicalPublisher(projector, zap.NewNop()).PublishObservedDeploymentsAnnouncement(ctx, &cfg)
	}
	if err != nil {
		return nil, err
	}
	relay, local, err = a.head(ctx, pub)
	if err != nil || relay == nil || local == nil {
		return nil, fmt.Errorf("discovery regeneration: %w", ErrIncomplete)
	}
	if err = validateDiscoveryHead(*relay); err != nil {
		return nil, err
	}
	if err = validateDiscoveryHead(*local); err != nil {
		return nil, err
	}
	if relay.ID != local.ID {
		return nil, errors.New("discovery relay/local heads differ")
	}
	if relay.Content != wantContent || !reflect.DeepEqual(relay.Tags, wantTags) {
		return nil, errors.New("discovery live configuration did not reach new-key head")
	}
	return []string{relay.ID.Hex()}, nil
}

func (a *DiscoveryRecoveryAdapter) Verify(ctx context.Context) ([]string, error) {
	core := a.core()
	if _, err := core.heads(ctx); err != nil {
		return nil, err
	}
	s, err := core.state()
	if err != nil {
		return nil, err
	}
	pub, err := nostr.PubKeyFromHex(s.NewPubkey)
	if err != nil || pub == a.OldPubkey {
		return nil, errors.New("discovery new identity unavailable")
	}
	relay, local, err := a.head(ctx, pub)
	if err != nil || relay == nil || local == nil {
		return nil, fmt.Errorf("discovery new-key head: %w", ErrIncomplete)
	}
	for _, head := range []*nostr.Event{relay, local} {
		if err := validateDiscoveryHead(*head); err != nil {
			return nil, err
		}
		if head.PubKey != pub {
			return nil, errors.New("discovery wrong author")
		}
	}
	if !slices.Equal(relay.ID[:], local.ID[:]) {
		return nil, errors.New("discovery relay/local heads differ")
	}
	return []string{relay.ID.Hex()}, nil
}
