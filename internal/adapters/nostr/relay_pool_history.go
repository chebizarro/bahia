package nostr

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"fiatjaf.com/nostr"
)

// StoredPageEvidence records a per-relay chain of bounded REQs. EOSE closes
// each page; only an empty final page proves the window was exhausted.
type StoredPageEvidence struct {
	Pages          int                `json:"pages"`
	UniqueIDs      int                `json:"unique_ids"`
	MinCreatedAt   int64              `json:"min_created_at,omitempty"`
	MaxCreatedAt   int64              `json:"max_created_at,omitempty"`
	PageLimit      int                `json:"page_limit"`
	NIP11MaxLimit  int                `json:"nip11_max_limit,omitempty"`
	FinalEmptyPage bool               `json:"final_empty_page"`
	Windows        []StoredPageWindow `json:"windows"`
}

type StoredPageWindow struct {
	Until           int64 `json:"until"`
	Events          int   `json:"events"`
	NewIDs          int   `json:"new_ids"`
	OldestCreatedAt int64 `json:"oldest_created_at,omitempty"`
	EOSE            bool  `json:"eose"`
}

// Complete checks the persisted proof's page chain, not just its final flag.
func (e StoredPageEvidence) Complete() bool {
	if !e.FinalEmptyPage || e.PageLimit < 1 || e.Pages != len(e.Windows) || e.Pages == 0 {
		return false
	}
	newIDs := 0
	for i, page := range e.Windows {
		if !page.EOSE || page.NewIDs < 0 || page.NewIDs > page.Events || page.Events < 0 || page.Events > e.PageLimit {
			return false
		}
		if i > 0 {
			prev := e.Windows[i-1]
			want := prev.OldestCreatedAt
			if prev.Events < e.PageLimit {
				want--
			}
			if page.Until != want {
				return false
			}
		}
		if i == e.Pages-1 {
			if page.Events != 0 {
				return false
			}
		} else if page.Events == 0 || page.OldestCreatedAt <= 1 {
			return false
		}
		newIDs += page.NewIDs
	}
	return newIDs == e.UniqueIDs
}

var ErrStoredHistoryBoundary = errors.New("stored history cannot prove timestamp boundary completeness")

// ScanStoredPages exhausts one relay's stored window without treating a
// limit-truncated EOSE as completeness. The same relayPageLimit policy as
// inbound sync and the pool's stored pager applies to each REQ.
func (p *RelayPool) ScanStoredPages(ctx context.Context, relayURL string, base nostr.Filter) ([]nostr.Event, StoredPageEvidence, error) {
	const defaultPageLimit = 500
	if relays := p.subscriptionRelays([]string{relayURL}); len(relays) == 1 {
		p.awaitRelayLimits(ctx, relays[0])
	}
	if p.GetRelayInfo(relayURL) == nil {
		// A caller may use the pool before Connect. An unavailable NIP-11
		// document leaves the conservative default in force.
		_, _ = p.FetchRelayInfo(ctx, relayURL, false)
	}
	evidence := StoredPageEvidence{NIP11MaxLimit: p.GetMaxLimit(relayURL)}
	evidence.PageLimit = p.relayPageLimit(relayURL, defaultPageLimit)
	if base.Limit > 0 && base.Limit < evidence.PageLimit {
		evidence.PageLimit = base.Limit
	}
	seen := make(map[nostr.ID]nostr.Event)
	until := base.Until
	for {
		req := base
		req.Limit = evidence.PageLimit
		req.Until = until
		page, err := p.storedHistoryPage(ctx, relayURL, req)
		evidence.Pages++
		if err != nil {
			return nil, evidence, err
		}
		if len(page) == 0 {
			evidence.Windows = append(evidence.Windows, StoredPageWindow{Until: int64(until), EOSE: true})
			evidence.FinalEmptyPage = true
			break
		}
		oldest := page[0].CreatedAt
		newIDs := 0
		for _, ev := range page {
			if ev.CreatedAt < oldest {
				oldest = ev.CreatedAt
			}
			if _, exists := seen[ev.ID]; exists {
				continue
			}
			seen[ev.ID] = ev
			newIDs++
			at := int64(ev.CreatedAt)
			if evidence.UniqueIDs == 0 || at < evidence.MinCreatedAt {
				evidence.MinCreatedAt = at
			}
			if evidence.UniqueIDs == 0 || at > evidence.MaxCreatedAt {
				evidence.MaxCreatedAt = at
			}
			evidence.UniqueIDs++
		}
		evidence.Windows = append(evidence.Windows, StoredPageWindow{Until: int64(until), Events: len(page), NewIDs: newIDs, OldestCreatedAt: int64(oldest), EOSE: true})
		if oldest <= 1 || until != 0 && oldest > until || newIDs == 0 && len(page) >= evidence.PageLimit {
			return nil, evidence, ErrStoredHistoryBoundary
		}
		// A full page overlaps its boundary second. A short page has
		// exhausted it and can advance to the preceding second. A full
		// duplicate-only page cannot prove what else shares that second.
		if len(page) < evidence.PageLimit {
			until = oldest - 1
		} else {
			until = oldest
		}
	}
	out := make([]nostr.Event, 0, len(seen))
	for _, ev := range seen {
		out = append(out, ev)
	}
	if !evidence.Complete() {
		return nil, evidence, errors.New("stored history pagination proof incomplete")
	}
	slices.SortFunc(out, func(a, b nostr.Event) int { return slices.Compare(a.ID[:], b.ID[:]) })
	return out, evidence, nil
}

func (p *RelayPool) storedHistoryPage(ctx context.Context, relayURL string, filter nostr.Filter) ([]nostr.Event, error) {
	readCtx, cancel := BoundStoredEventsWait(ctx, DefaultStoredEventsTimeout)
	defer cancel()
	sub, err := p.SubscribeWithOptions(readCtx, []nostr.Filter{filter}, SubscribeOptions{
		Relays: []string{relayURL}, AwaitUnavailableRelays: true,
		ValidateEvent: func(ev *nostr.Event) bool { return ev.CheckID() && ev.VerifySignature() },
	})
	if err != nil {
		return nil, errors.New("audited relay subscription failed")
	}
	defer sub.Close()
	var page []nostr.Event
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				return nil, errors.New("relay stream closed before EOSE")
			}
			page = append(page, *ev)
		case <-sub.EndOfStoredEvents:
			if !sub.AllRelaysReachedEOSE() || sub.StoredEventsIncomplete(nil) != nil {
				return nil, errors.New("audited relay page incomplete")
			}
			for {
				select {
				case ev, ok := <-sub.Events:
					if !ok {
						return page, nil
					}
					page = append(page, *ev)
				default:
					return page, nil
				}
			}
		case <-readCtx.Done():
			return nil, fmt.Errorf("audited relay page incomplete: %w", readCtx.Err())
		}
	}
}
