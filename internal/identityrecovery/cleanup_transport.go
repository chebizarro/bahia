package identityrecovery

import (
	"context"
	"slices"
	"strings"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/relayguard"
)

// RelayEvidence contains only protocol outcome classes, never relay-provided
// text (which may contain credentials). The URL is local-ledger evidence;
// signed receipts use its digest instead.
type RelayEvidence struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type CleanupTransport interface {
	Scan(context.Context, nostr.Filter) ([]nostr.Event, map[string]RelayEvidence)
	PublishDeletion(context.Context, nostr.Event) map[string]RelayEvidence
}

type CleanupRelaySource struct {
	Pool   *adapter.RelayPool
	Relays []string
}

func (s CleanupRelaySource) Scan(ctx context.Context, filter nostr.Filter) ([]nostr.Event, map[string]RelayEvidence) {
	outcomes := make(map[string]RelayEvidence, len(s.Relays))
	seen := map[nostr.ID]nostr.Event{}
	for _, raw := range s.Relays {
		url := relayguard.Normalize(raw)
		readCtx, cancel := adapter.BoundStoredEventsWait(ctx, adapter.DefaultStoredEventsTimeout)
		sub, err := s.Pool.SubscribeWithOptions(readCtx, []nostr.Filter{filter}, adapter.SubscribeOptions{Relays: []string{url}, AwaitUnavailableRelays: true, ValidateEvent: func(ev *nostr.Event) bool { return ev.CheckID() && ev.VerifySignature() }})
		if err != nil {
			outcomes[url] = RelayEvidence{Status: "incomplete", Reason: "unreachable"}
			cancel()
			continue
		}
		complete := false
		for !complete {
			select {
			case ev, ok := <-sub.Events:
				if !ok {
					complete = true
					break
				}
				seen[ev.ID] = *ev
			case <-sub.EndOfStoredEvents:
				for draining := true; draining; {
					select {
					case ev, ok := <-sub.Events:
						if !ok {
							draining = false
						} else {
							seen[ev.ID] = *ev
						}
					default:
						draining = false
					}
				}
				complete = true
			case <-readCtx.Done():
				complete = true
			}
		}
		evidence := RelayEvidence{Status: "eose"}
		if !sub.AllRelaysReachedEOSE() || sub.StoredEventsIncomplete(readCtx.Err()) != nil {
			evidence = RelayEvidence{Status: "incomplete", Reason: "unreachable"}
			for _, result := range sub.StoredOutcomes() {
				if result.Truncated {
					evidence.Reason = "truncated"
				} else if result.Status == adapter.RelayStoredClosed {
					evidence.Reason = classifyRelayReason(result.Reason, "closed")
				} else if readCtx.Err() != nil {
					evidence.Reason = "unreachable"
				}
			}
		}
		outcomes[url] = evidence
		sub.Close()
		cancel()
	}
	out := make([]nostr.Event, 0, len(seen))
	for _, ev := range seen {
		out = append(out, ev)
	}
	slices.SortFunc(out, func(a, b nostr.Event) int { return slices.Compare(a.ID[:], b.ID[:]) })
	return out, outcomes
}

func (s CleanupRelaySource) PublishDeletion(ctx context.Context, ev nostr.Event) map[string]RelayEvidence {
	outcomes := make(map[string]RelayEvidence, len(s.Relays))
	for _, raw := range s.Relays {
		outcomes[relayguard.Normalize(raw)] = RelayEvidence{Status: "incomplete", Reason: "unreachable"}
	}
	if ev.Kind != nostr.KindDeletion || !ev.CheckID() || !ev.VerifySignature() {
		return outcomes
	}
	results, err := s.Pool.PublishToRelaysWithResults(ctx, ev, s.Relays)
	if err != nil && len(results) == 0 {
		return outcomes
	}
	for _, result := range results {
		status := RelayEvidence{Status: "incomplete", Reason: "unreachable"}
		switch {
		case result.Cached:
			status = RelayEvidence{Status: "incomplete", Reason: "cached"} // not a fresh external OK
		case result.Accepted:
			status = RelayEvidence{Status: "ok"}
		case result.IsDuplicate():
			status = RelayEvidence{Status: "ok", Reason: "duplicate"}
		case result.Error != nil:
			status.Reason = "unreachable"
		default:
			status.Status = "ok-false"
			status.Reason = classifyRelayReason(result.Reason, "refused")
		}
		outcomes[relayguard.Normalize(result.RelayURL)] = status
	}
	return outcomes
}

func classifyRelayReason(reason, fallback string) string {
	switch {
	case nostrout.IsAuthRequiredReason(reason) || strings.HasPrefix(strings.ToLower(reason), "auth-"):
		return "auth-required"
	case strings.HasPrefix(strings.ToLower(reason), "closed:"):
		return "closed"
	case nostrout.IsRateLimitedReason(reason):
		return "rate-limited"
	case nostrout.IsBlockedReason(reason):
		return "blocked"
	default:
		return fallback
	}
}
