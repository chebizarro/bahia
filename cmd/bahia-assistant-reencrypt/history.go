package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/app"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/relayguard"
)

// assistantRelays is every relay the daemon publishes assistant events to
// or treats as a service write relay: the control-plane relays and the
// service write relays of the static relay policy.
func assistantRelays(cfg config.NostrConfig) []string {
	var relays []string
	seen := map[string]bool{}
	for _, relay := range append(app.ControlPlaneRelayURLs(cfg), cfg.ServiceWriteRelays(cfg.ServiceRelayPolicyRelays())...) {
		normalized := relayguard.Normalize(relay)
		if normalized != "" && !seen[normalized] {
			seen[normalized] = true
			relays = append(relays, normalized)
		}
	}
	return relays
}

// publishQuorum is how many of n relays must hold an event, as
// nostr.publish_quorum defines it for the daemon's own publishes.
func publishQuorum(configured, n int) (int, error) {
	switch {
	case configured == config.PublishQuorumAllRelays:
		return n, nil
	case configured < 0:
		return 0, fmt.Errorf("nostr.publish_quorum %d is invalid", configured)
	case configured <= 1:
		return 1, nil
	case configured > n:
		return 0, fmt.Errorf("nostr.publish_quorum %d exceeds the %d relays", configured, n)
	default:
		return configured, nil
	}
}

// heldEvent is one event and the relays that hold it.
type heldEvent struct {
	ev     nostr.Event
	relays map[string]bool
}

// history is one author's assistant events across every relay, by id.
type history map[nostr.ID]*heldEvent

func (h history) events() []nostr.Event {
	out := make([]nostr.Event, 0, len(h))
	for _, held := range h {
		out = append(out, held.ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.Hex() < out[j].ID.Hex() })
	return out
}

// underQuorum is what author's history holds on fewer than quorum relays and
// still matters: every checkpoint (in chain order) and the NIP-01 winner of
// every transcript and session coordinate.
func (h history) underQuorum(author nostr.PubKey, quorum int) []pending {
	winners := map[string]*heldEvent{}
	var checkpoints []*heldEvent
	for _, held := range h {
		ev := held.ev
		if ev.PubKey != author || !ev.CheckID() || !ev.VerifySignature() {
			continue
		}
		if ev.Kind == nostr.Kind(domain.AssistantExecutionCheckpointKind) {
			checkpoints = append(checkpoints, held)
			continue
		}
		key := strconv.Itoa(int(ev.Kind)) + ":" + tagValue(ev, "d")
		cur, ok := winners[key]
		if !ok || ev.CreatedAt > cur.ev.CreatedAt || (ev.CreatedAt == cur.ev.CreatedAt && ev.ID.Hex() < cur.ev.ID.Hex()) {
			winners[key] = held
		}
	}
	sort.Slice(checkpoints, func(i, j int) bool {
		a, b := checkpoints[i].ev, checkpoints[j].ev
		if ka, kb := tagValue(a, "session")+"/"+tagValue(a, "run"), tagValue(b, "session")+"/"+tagValue(b, "run"); ka != kb {
			return ka < kb
		}
		ra, _ := strconv.ParseUint(tagValue(a, "revision"), 10, 64)
		rb, _ := strconv.ParseUint(tagValue(b, "revision"), 10, 64)
		return ra < rb
	})
	var out []pending
	for _, held := range checkpoints {
		if len(held.relays) < quorum {
			out = append(out, pending{ev: held.ev, held: held.relays})
		}
	}
	keys := make([]string, 0, len(winners))
	for key := range winners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if held := winners[key]; len(held.relays) < quorum {
			out = append(out, pending{ev: held.ev, held: held.relays})
		}
	}
	return out
}

func tagValue(ev nostr.Event, name string) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

// historyReader reads one author's assistant history from each relay on
// its own, so it knows which relays hold which event.
type historyReader struct {
	pool      *nostradapter.RelayPool
	relays    []string
	pageLimit int
	timeout   time.Duration
}

func (r historyReader) history(ctx context.Context, author nostr.PubKey) (history, error) {
	filters := []nostr.Filter{
		{Kinds: []nostr.Kind{nostr.Kind(domain.AssistantExecutionCheckpointKind), nostr.Kind(domain.KindAssistantTranscript)}, Authors: []nostr.PubKey{author}},
		{Kinds: []nostr.Kind{nostr.Kind(domain.KindAssistantSessionState)}, Authors: []nostr.PubKey{author}, Tags: nostr.TagMap{"t": {kinds.AssistantSessionTopic}}},
	}
	h := history{}
	for _, relay := range r.relays {
		for _, filter := range filters {
			events, err := r.pages(ctx, relay, filter)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", relay, err)
			}
			for _, ev := range events {
				if ev.PubKey != author {
					continue
				}
				held, ok := h[ev.ID]
				if !ok {
					held = &heldEvent{ev: ev, relays: map[string]bool{}}
					h[ev.ID] = held
				}
				held.relays[nostrout.NormalizeRelayURL(relay)] = true
			}
		}
	}
	return h, nil
}

// pages reads every stored event of filter from relay with REQ pages of
// pageLimit bounded by until, each complete only at the relay's EOSE, without
// trusting the relay to advertise or honour a limit. The next page ends at
// the oldest created_at seen (until is inclusive; seen events are dropped);
// after a page with nothing new it steps past that second, and the read
// ends at an empty page.
//
// NIP-01 cannot page within one second. A page of only already-seen events
// as large as the largest page may be a second holding more events than the
// relay returns at once; the read is then incomplete if the relay is shown to
// cap its answers (it answered fewer events than asked and still had older
// ones) or the page was full.
func (r historyReader) pages(ctx context.Context, relay string, base nostr.Filter) ([]nostr.Event, error) {
	seen := map[nostr.ID]bool{}
	var out []nostr.Event
	largest := 0
	capped, lastShort := false, false
	var crowded nostr.Timestamp
	crowdedFull := false
	until, bounded := nostr.Timestamp(0), false
	for {
		filter := base
		filter.Limit = r.pageLimit
		if bounded {
			filter.Until = until
		}
		page, err := r.page(ctx, relay, filter)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			if crowded != 0 && (capped || crowdedFull) {
				return nil, fmt.Errorf("%d or more events share created_at %d and the relay returns at most %d at once: it cannot be paged completely", largest, crowded, largest)
			}
			return out, nil
		}
		fresh := 0
		oldest := page[0].CreatedAt
		for _, ev := range page {
			oldest = min(oldest, ev.CreatedAt)
			if !seen[ev.ID] {
				seen[ev.ID] = true
				out = append(out, ev)
				fresh++
			}
		}
		if fresh > 0 && lastShort {
			capped = true
		}
		if fresh == 0 && len(page) >= largest {
			crowded = oldest
			crowdedFull = crowdedFull || len(page) >= r.pageLimit
		}
		lastShort = len(page) < r.pageLimit
		largest = max(largest, len(page))
		next := oldest
		if fresh == 0 {
			if oldest <= 1 {
				return out, nil
			}
			next = oldest - 1
		}
		until, bounded = next, true
	}
}

// page is one REQ on one relay, read through its EOSE.
func (r historyReader) page(ctx context.Context, relay string, filter nostr.Filter) ([]nostr.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	sub, err := r.pool.SubscribeWithOptions(ctx, []nostr.Filter{filter}, nostradapter.SubscribeOptions{Relays: []string{relay}, AwaitUnavailableRelays: true})
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	var events []nostr.Event
	keep := func(ev *nostr.Event) {
		if ev != nil {
			events = append(events, *ev)
		}
	}
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				return nil, fmt.Errorf("stream ended before EOSE: %w", sub.StoredEventsIncomplete(nil))
			}
			keep(ev)
		case <-sub.EndOfStoredEvents:
			if err := sub.StoredEventsIncomplete(nil); err != nil {
				return nil, err
			}
			if len(sub.StoredOutcomes()) != 1 {
				return nil, fmt.Errorf("the relay did not answer the REQ")
			}
			// Each relay worker forwards its buffered EVENTs before it records
			// EOSE; take the ones already forwarded.
			for {
				select {
				case ev, ok := <-sub.Events:
					if !ok {
						return events, nil
					}
					keep(ev)
				default:
					return events, nil
				}
			}
		case <-ctx.Done():
			return nil, sub.StoredEventsIncomplete(ctx.Err())
		}
	}
}
