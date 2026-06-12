package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

const (
	operatorContextVMRelaySet           = "bahia-contextvm-v1"
	operatorBrowserRelaySet             = "bahia-browser-v1"
	defaultOperatorDiscoveryWaitTimeout = 30 * time.Second
)

// OperatorRelayDiscoveryConfig configures trusted NIP-51 bootstrap discovery for
// signer-first operator ContextVM transport relays.
type OperatorRelayDiscoveryConfig struct {
	BootstrapRelays []string

	// TrustedServicePubkeys is an ordered allowlist. Multiple pubkeys are allowed
	// for deterministic deployment/operator trust lists, not implicit key
	// rotation: relay-set selection prefers bahia-contextvm-v1 over
	// bahia-browser-v1, then the first configured trusted pubkey with a usable
	// set, and latest-wins only within that same pubkey and d tag.
	TrustedServicePubkeys []string

	// DiscoveryWaitTimeout bounds the bootstrap transport wait for relay EOSE.
	// It is a fail-closed guard for unavailable or stalled relay transport; it is
	// never a completion signal and discovered relay sets are not selected until
	// EOSE is observed.
	DiscoveryWaitTimeout time.Duration
}

type operatorDiscoveryTransport interface {
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrpool.MergedSubscription, error)
	Close()
}

type operatorRelaySetCandidate struct {
	author    string
	eventID   string
	createdAt nostr.Timestamp
	relays    []string
}

// DiscoverOperatorRelays resolves ContextVM operator relays from trusted
// service-authored NIP-51 relay sets. It waits for relay EOSE before selecting
// relays, prefers bahia-contextvm-v1 over bahia-browser-v1, and resolves
// multiple trusted service pubkeys by configured trust order rather than by
// cross-key latest timestamp.
func DiscoverOperatorRelays(ctx context.Context, cfg OperatorRelayDiscoveryConfig) ([]string, error) {
	bootstrapRelays := normalizeOperatorRelays(cfg.BootstrapRelays)
	if len(bootstrapRelays) == 0 {
		return nil, fmt.Errorf("operator bootstrap discovery requires at least one bootstrap relay")
	}
	trustedPubkeys, err := normalizeTrustedServicePubkeys(cfg.TrustedServicePubkeys)
	if err != nil {
		return nil, err
	}
	if len(trustedPubkeys) == 0 {
		return nil, fmt.Errorf("operator bootstrap discovery requires at least one trusted service pubkey")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	timeout := cfg.DiscoveryWaitTimeout
	if timeout == 0 {
		timeout = defaultOperatorDiscoveryWaitTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	pool := nostrpool.NewRelayPool(bootstrapRelays, zap.NewNop())
	transport := &relayPoolOperatorTransport{pool: pool}
	defer transport.Close()
	return discoverOperatorRelaysWithTransport(ctx, trustedPubkeys, transport)
}

func discoverOperatorRelaysWithTransport(ctx context.Context, trustedPubkeys []string, transport operatorDiscoveryTransport) ([]string, error) {
	trustedPubkeys, err := normalizeTrustedServicePubkeys(trustedPubkeys)
	if err != nil {
		return nil, err
	}
	if len(trustedPubkeys) == 0 {
		return nil, fmt.Errorf("operator bootstrap discovery requires at least one trusted service pubkey")
	}
	if transport == nil {
		return nil, fmt.Errorf("operator bootstrap discovery transport is not configured")
	}

	trustedAuthors := make([]nostr.PubKey, 0, len(trustedPubkeys))
	for _, pubkey := range trustedPubkeys {
		parsed, err := nostr.PubKeyFromHex(pubkey)
		if err != nil {
			return nil, fmt.Errorf("parse trusted service pubkey: %w", err)
		}
		trustedAuthors = append(trustedAuthors, parsed)
	}
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.RelaySetDiscovery)},
		Authors: trustedAuthors,
		Tags: nostr.TagMap{
			"d": []string{operatorContextVMRelaySet, operatorBrowserRelaySet},
		},
	}
	sub, err := transport.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return nil, fmt.Errorf("subscribe for trusted operator relay discovery: %w", err)
	}
	defer sub.Close()

	trusted := make(map[string]struct{}, len(trustedPubkeys))
	for _, pubkey := range trustedPubkeys {
		trusted[pubkey] = struct{}{}
	}
	seen := map[string]struct{}{}
	relaySets := map[string]operatorRelaySetCandidate{}
	eose := sub.EndOfStoredEvents
	events := sub.Events
	closed := sub.Closed
	var closedReasons []string

	for eose != nil || events != nil || closed != nil {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("trusted operator relay discovery transport guard expired before EOSE: %w", ctx.Err())
			}
			return nil, fmt.Errorf("trusted operator relay discovery transport guard canceled before EOSE: %w", ctx.Err())
		case <-eose:
			drainTrustedOperatorRelayEvents(events, trusted, seen, relaySets)
			return chooseOperatorRelaySet(relaySets, trustedPubkeys, closedReasons)
		case event, ok := <-events:
			if !ok {
				if eose != nil {
					return nil, fmt.Errorf("trusted operator relay discovery event stream closed before EOSE")
				}
				events = nil
				continue
			}
			recordTrustedOperatorRelayEvent(event, trusted, seen, relaySets)
		case relayClosed, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			reason := strings.TrimSpace(relayClosed.Reason)
			if reason == "" {
				reason = "subscription closed"
			}
			if relayClosed.RelayURL != "" {
				reason = relayClosed.RelayURL + ": " + reason
			}
			closedReasons = append(closedReasons, reason)
		}
	}

	return nil, fmt.Errorf("trusted operator relay discovery ended before EOSE")
}

func drainTrustedOperatorRelayEvents(events <-chan *nostr.Event, trusted map[string]struct{}, seen map[string]struct{}, relaySets map[string]operatorRelaySetCandidate) {
	for events != nil {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			recordTrustedOperatorRelayEvent(event, trusted, seen, relaySets)
		default:
			return
		}
	}
}

func recordTrustedOperatorRelayEvent(event *nostr.Event, trusted map[string]struct{}, seen map[string]struct{}, relaySets map[string]operatorRelaySetCandidate) {
	candidate, dTag, ok := trustedOperatorRelaySetCandidate(event, trusted)
	if !ok {
		return
	}
	eventID := event.ID.Hex()
	if _, duplicate := seen[eventID]; duplicate {
		return
	}
	seen[eventID] = struct{}{}
	key := operatorRelaySetKey(candidate.author, dTag)
	if existing, exists := relaySets[key]; !exists || candidateIsNewer(candidate, existing) {
		relaySets[key] = candidate
	}
}

func trustedOperatorRelaySetCandidate(event *nostr.Event, trusted map[string]struct{}) (operatorRelaySetCandidate, string, bool) {
	if event == nil || event.Kind != nostr.Kind(kinds.RelaySetDiscovery) || !validSignedEvent(event) {
		return operatorRelaySetCandidate{}, "", false
	}
	pubkey := event.PubKey.Hex()
	if _, ok := trusted[pubkey]; !ok {
		return operatorRelaySetCandidate{}, "", false
	}
	dTag := firstTagValue(event.Tags, "d")
	if dTag != operatorContextVMRelaySet && dTag != operatorBrowserRelaySet {
		return operatorRelaySetCandidate{}, "", false
	}
	relays := normalizeOperatorRelays(relaySetRelayTags(event.Tags))
	if len(relays) == 0 {
		return operatorRelaySetCandidate{}, "", false
	}
	return operatorRelaySetCandidate{author: pubkey, eventID: event.ID.Hex(), createdAt: event.CreatedAt, relays: relays}, dTag, true
}

func chooseOperatorRelaySet(relaySets map[string]operatorRelaySetCandidate, trustedPubkeys []string, closedReasons []string) ([]string, error) {
	for _, dTag := range []string{operatorContextVMRelaySet, operatorBrowserRelaySet} {
		for _, pubkey := range trustedPubkeys {
			if candidate, ok := relaySets[operatorRelaySetKey(pubkey, dTag)]; ok && len(candidate.relays) > 0 {
				return candidate.relays, nil
			}
		}
	}
	if len(closedReasons) > 0 {
		return nil, fmt.Errorf("no trusted operator relay set events found before EOSE; relay CLOSED: %s", strings.Join(closedReasons, "; "))
	}
	return nil, fmt.Errorf("no trusted operator relay set events found before EOSE")
}

func operatorRelaySetKey(pubkey, dTag string) string {
	return pubkey + "\x00" + dTag
}

func candidateIsNewer(candidate, existing operatorRelaySetCandidate) bool {
	if candidate.createdAt != existing.createdAt {
		return candidate.createdAt > existing.createdAt
	}
	// NIP-01 replaceable-event ties keep the lowest event ID for equal created_at.
	return candidate.eventID < existing.eventID
}

func relaySetRelayTags(tags nostr.Tags) []string {
	relays := []string{}
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "relay" {
			relays = append(relays, tag[1])
		}
	}
	return relays
}

func normalizeTrustedServicePubkeys(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, pubkey := range strings.Split(value, ",") {
			pubkey = strings.ToLower(strings.TrimSpace(pubkey))
			if pubkey == "" {
				continue
			}
			if len(pubkey) != 64 {
				return nil, fmt.Errorf("trusted service pubkey must be a 64-character hex pubkey")
			}
			if _, err := hex.DecodeString(pubkey); err != nil {
				return nil, fmt.Errorf("trusted service pubkey must be hex: %w", err)
			}
			if _, exists := seen[pubkey]; exists {
				continue
			}
			seen[pubkey] = struct{}{}
			out = append(out, pubkey)
		}
	}
	return out, nil
}
