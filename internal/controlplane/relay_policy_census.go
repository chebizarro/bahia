package controlplane

import (
	"context"
	"fmt"
	"slices"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// CanonicalRelayPolicyHead is a signed relay-settings state independently
// observed at every relay in a read set. It is not an SQL projection.
type CanonicalRelayPolicyHead struct {
	EventID string
	State   RelayPolicyState
}

// PolicyCensusBootstrapRelays returns the daemon's initial control-plane
// read set. A missing set is not filled by an operator-supplied URL.
func PolicyCensusBootstrapRelays(cfg config.NostrConfig) ([]string, error) {
	if cfg.Sidecar.Enabled {
		if cfg.Sidecar.BackendURL == "" {
			return nil, fmt.Errorf("enabled sidecar has no backend relay URL")
		}
		return policyCensusRelaySet([]string{cfg.Sidecar.BackendURL})
	}
	return policyCensusRelaySet(cfg.ContextVMRelayPolicyRelays())
}

// PolicyCensusEffectiveRelays applies the daemon's control-plane topology
// precedence to an observed canonical state. A policy with no control-plane
// topology is refused rather than falling back to an unproven config value.
func PolicyCensusEffectiveRelays(cfg config.NostrConfig, state RelayPolicyState) ([]string, error) {
	if cfg.Sidecar.Enabled {
		return PolicyCensusBootstrapRelays(cfg)
	}
	if len(state.ContextVMRelays) > 0 {
		return policyCensusRelaySet(state.ContextVMRelays)
	}
	if len(state.ServiceRelays) > 0 {
		return policyCensusRelaySet(state.ServiceRelays)
	}
	return nil, fmt.Errorf("canonical relay policy has no control-plane topology")
}

// VerifyPolicyCensusRelays binds operator input to the signed head and the
// daemon's effective control-plane relay precedence. Ordering is irrelevant;
// adding or omitting a relay is not.
func VerifyPolicyCensusRelays(cfg config.NostrConfig, head CanonicalRelayPolicyHead, supplied []string) ([]string, error) {
	if head.EventID == "" {
		return nil, fmt.Errorf("signed canonical relay policy head is required")
	}
	effective, err := PolicyCensusEffectiveRelays(cfg, head.State)
	if err != nil {
		return nil, err
	}
	operator, err := policyCensusRelaySet(supplied)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(operator, effective) {
		return nil, fmt.Errorf("operator relays %v do not match signed effective control-plane relays %v", operator, effective)
	}
	return effective, nil
}

func policyCensusRelaySet(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("relay policy has no relays")
	}
	seen := make(map[string]struct{}, len(values))
	urls := make([]string, 0, len(values))
	for _, value := range values {
		url := nostr.NormalizeURL(value)
		if url == "" {
			return nil, fmt.Errorf("relay policy contains invalid URL %q", value)
		}
		if _, ok := seen[url]; ok {
			return nil, fmt.Errorf("relay policy contains duplicate URL %q", url)
		}
		seen[url] = struct{}{}
		urls = append(urls, url)
	}
	slices.Sort(urls)
	return urls, nil
}

// ReadCanonicalRelayPolicyHead requires the same valid signed policy event
// from each configured relay. A complete EOSE with no valid state cannot
// certify absence because the Nostr transport may discard invalid frames.
func ReadCanonicalRelayPolicyHead(ctx context.Context, pool *nostradapter.RelayPool, author nostr.PubKey) (CanonicalRelayPolicyHead, error) {
	if pool == nil || len(pool.URLs()) == 0 || author == (nostr.PubKey{}) {
		return CanonicalRelayPolicyHead{}, fmt.Errorf("canonical relay policy read requires relays and author")
	}
	var head CanonicalRelayPolicyHead
	for _, relay := range pool.URLs() {
		one, err := readCanonicalRelayPolicyFromRelay(ctx, pool, relay, author)
		if err != nil {
			return CanonicalRelayPolicyHead{}, fmt.Errorf("relay %s: %w", relay, err)
		}
		if head.EventID != "" && head.EventID != one.EventID {
			return CanonicalRelayPolicyHead{}, fmt.Errorf("canonical relay policy heads disagree between required relays: %s and %s", head.EventID, one.EventID)
		}
		head = one
	}
	return head, nil
}

func readCanonicalRelayPolicyFromRelay(ctx context.Context, pool *nostradapter.RelayPool, relay string, author nostr.PubKey) (CanonicalRelayPolicyHead, error) {
	ctx, cancel := nostradapter.BoundStoredEventsWait(ctx, nostradapter.DefaultStoredEventsTimeout)
	defer cancel()
	filter := nostr.Filter{Kinds: []nostr.Kind{kinds.CASControlState}, Authors: []nostr.PubKey{author},
		Tags: nostr.TagMap{kinds.CASControlStateTagD: {RelaySettingsDTag}}, Limit: 10}
	sub, err := pool.SubscribeWithOptions(ctx, []nostr.Filter{filter}, nostradapter.SubscribeOptions{Relays: []string{relay}, AwaitUnavailableRelays: true})
	if err != nil {
		return CanonicalRelayPolicyHead{}, err
	}
	defer sub.Close()
	var head CanonicalRelayPolicyHead
	consume := func(ev *nostr.Event) error {
		if err := nostradapter.ValidateInboundEvent(ev, nostr.Now().Time(), nostradapter.InboundEventMaxFutureSkew); err != nil {
			return fmt.Errorf("invalid signed relay policy: %w", err)
		}
		state, err := relayPolicyStateFromCanonicalEvent(ev, author.Hex())
		if err != nil {
			return fmt.Errorf("invalid canonical relay policy: %w", err)
		}
		if head.EventID != "" && head.EventID != ev.ID.Hex() {
			return fmt.Errorf("relay returned multiple policy heads")
		}
		head = CanonicalRelayPolicyHead{EventID: ev.ID.Hex(), State: *state}
		return nil
	}
	finish := func() (CanonicalRelayPolicyHead, error) {
		if err := sub.StoredEventsIncomplete(nil); err != nil {
			return CanonicalRelayPolicyHead{}, err
		}
		outcomes := sub.StoredOutcomes()
		if len(outcomes) != 1 || outcomes[0].RelayURL != relay {
			return CanonicalRelayPolicyHead{}, fmt.Errorf("required relay was not covered by EOSE")
		}
		for {
			select {
			case ev, ok := <-sub.Events:
				if !ok {
					return CanonicalRelayPolicyHead{}, fmt.Errorf("relay policy stream closed while draining EOSE")
				}
				if err := consume(ev); err != nil {
					return CanonicalRelayPolicyHead{}, err
				}
			default:
				if head.EventID == "" {
					return CanonicalRelayPolicyHead{}, fmt.Errorf("no valid signed canonical relay policy; absence is unprovable")
				}
				return head, nil
			}
		}
	}
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				return CanonicalRelayPolicyHead{}, fmt.Errorf("relay policy stream ended before complete EOSE")
			}
			if err := consume(ev); err != nil {
				return CanonicalRelayPolicyHead{}, err
			}
		case <-sub.EndOfStoredEvents:
			return finish()
		case <-ctx.Done():
			return CanonicalRelayPolicyHead{}, fmt.Errorf("relay policy history incomplete: %w", sub.StoredEventsIncomplete(ctx.Err()))
		}
	}
}
