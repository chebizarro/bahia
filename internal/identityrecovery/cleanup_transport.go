package identityrecovery

import (
	"context"
	"errors"
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
	Status    string                      `json:"status"`
	Reason    string                      `json:"reason,omitempty"`
	PageProof *adapter.StoredPageEvidence `json:"page_proof,omitempty"`
}

type CleanupTransport interface {
	Scan(context.Context, nostr.Filter) ([]nostr.Event, map[string]RelayEvidence)
	FetchCandidates(context.Context, nostr.PubKey, []string) ([]nostr.Event, error)
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
		events, proof, err := s.Pool.ScanStoredPages(ctx, url, filter)
		evidence := RelayEvidence{Status: "complete", PageProof: &proof}
		if err != nil {
			evidence.Status = "incomplete"
			evidence.Reason = "unreachable"
			if errors.Is(err, adapter.ErrStoredHistoryBoundary) {
				evidence.Reason = "timestamp-boundary"
			}
		}
		outcomes[url] = evidence
		for _, ev := range events {
			seen[ev.ID] = ev
		}
	}
	out := make([]nostr.Event, 0, len(seen))
	for _, ev := range seen {
		out = append(out, ev)
	}
	slices.SortFunc(out, func(a, b nostr.Event) int { return slices.Compare(a.ID[:], b.ID[:]) })
	return out, outcomes
}

// FetchCandidates asks each audited relay directly for reviewed IDs that the
// history scan and local store did not expose. A failed relay cannot make a
// missing ID pass the engine's cross-check.
func (s CleanupRelaySource) FetchCandidates(ctx context.Context, pub nostr.PubKey, ids []string) ([]nostr.Event, error) {
	const batchSize = 64
	seen := make(map[nostr.ID]nostr.Event)
	for start := 0; start < len(ids); start += batchSize {
		end := min(start+batchSize, len(ids))
		batch := make([]nostr.ID, 0, end-start)
		for _, raw := range ids[start:end] {
			id, err := nostr.IDFromHex(raw)
			if err != nil {
				return nil, errors.New("invalid reviewed event ID")
			}
			batch = append(batch, id)
		}
		for _, relay := range s.Relays {
			events, err := scanRecoveryRelay(ctx, s.Pool, relay, nostr.Filter{IDs: batch, Authors: []nostr.PubKey{pub}})
			for _, ev := range events {
				seen[ev.ID] = ev
			}
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
		}
	}
	out := make([]nostr.Event, 0, len(seen))
	for _, ev := range seen {
		out = append(out, ev)
	}
	return out, nil
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
