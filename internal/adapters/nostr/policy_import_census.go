package nostr

import (
	"context"
	"fmt"
	"sort"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// PolicyCoordinateCensus is an EOSE-bounded relay observation, not a write
// permit. A relay-present coordinate wins over SQL regardless of its content.
type PolicyCoordinateCensus struct {
	PolicyID uuid.UUID
	EventIDs []string
	Relays   []string
}

// CensusPolicyCoordinate reads the addressable deployment-policy coordinate
// from every relay in the supplied pool. A cold local cache is never evidence
// of absence. The caller must independently establish that this exact relay
// set is the effective canonical read/write set before using an absence to
// make an import decision.
func CensusPolicyCoordinate(ctx context.Context, pool *RelayPool, author nostr.PubKey, id uuid.UUID) (PolicyCoordinateCensus, error) {
	if pool == nil || len(pool.URLs()) == 0 || author == (nostr.PubKey{}) || id == uuid.Nil {
		return PolicyCoordinateCensus{}, fmt.Errorf("policy census requires relay pool, author, and policy id")
	}
	ctx, cancel := BoundStoredEventsWait(ctx, DefaultStoredEventsTimeout)
	defer cancel()
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{author},
		Tags: nostr.TagMap{"d": {id.String()}, "t": {kinds.CPStateTopicPolicyRegistry}}}
	sub, err := pool.SubscribeWithOptions(ctx, []nostr.Filter{filter}, SubscribeOptions{AwaitUnavailableRelays: true})
	if err != nil {
		return PolicyCoordinateCensus{}, fmt.Errorf("subscribe policy coordinate %s: %w", id, err)
	}
	defer sub.Close()
	seen := make(map[string]struct{})
	consume := func(ev *nostr.Event) error {
		if ev == nil || !ev.CheckID() || !ev.VerifySignature() || ev.PubKey != author ||
			ev.Kind != nostr.Kind(kinds.CASControlState) || policyCensusTag(ev.Tags, "d") != id.String() ||
			policyCensusTag(ev.Tags, "t") != kinds.CPStateTopicPolicyRegistry || policyCensusTag(ev.Tags, "domain") != "policy" {
			return fmt.Errorf("relay returned invalid or mismatched policy coordinate %s", id)
		}
		seen[ev.ID.Hex()] = struct{}{}
		return nil
	}
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				return PolicyCoordinateCensus{}, fmt.Errorf("policy coordinate %s event stream ended before complete EOSE: %v", id, sub.StoredEventsIncomplete(nil))
			}
			if err := consume(ev); err != nil {
				return PolicyCoordinateCensus{}, err
			}
		case <-sub.EndOfStoredEvents:
			if err := sub.StoredEventsIncomplete(nil); err != nil {
				return PolicyCoordinateCensus{}, fmt.Errorf("policy coordinate %s: %w", id, err)
			}
			// Each relay worker forwards its buffered EVENTs before recording EOSE.
			// Drain those already forwarded when aggregate EOSE closes.
			for {
				select {
				case ev, ok := <-sub.Events:
					if !ok {
						return PolicyCoordinateCensus{}, fmt.Errorf("policy coordinate %s stream closed while draining EOSE", id)
					}
					if err := consume(ev); err != nil {
						return PolicyCoordinateCensus{}, err
					}
				default:
					ids := make([]string, 0, len(seen))
					for eventID := range seen {
						ids = append(ids, eventID)
					}
					sort.Strings(ids)
					relays := pool.URLs()
					outcomes := sub.StoredOutcomes()
					if len(outcomes) != len(relays) {
						return PolicyCoordinateCensus{}, fmt.Errorf("policy coordinate %s missed required relays: %d of %d answered", id, len(outcomes), len(relays))
					}
					return PolicyCoordinateCensus{PolicyID: id, EventIDs: ids, Relays: relays}, nil
				}
			}
		case <-ctx.Done():
			return PolicyCoordinateCensus{}, fmt.Errorf("policy coordinate %s: %w", id, sub.StoredEventsIncomplete(ctx.Err()))
		}
	}
}

func policyCensusTag(tags nostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}
