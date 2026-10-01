package nostr

import (
	"fiatjaf.com/nostr"
	"go.uber.org/zap"
)

// storedPager pages one REQ's stored events with `until` when the relay's
// NIP-11 max_limit capped the REQ's limit below the caller's (bahia-irsry.49).
// Without it a caller asking for more than max_limit, and not paging itself,
// would silently get only the newest max_limit events.
//
// Relays answer a REQ newest first, so a page that comes back full is
// followed by a page bounded by its oldest created_at (until is inclusive;
// events already seen are dropped), until the caller's limit is met or a page
// comes back short. Only then does the relay's EOSE reach the caller. The
// last page carries an until, so a fresh REQ from the newest stored event
// then follows the relay live.
//
// NIP-01 cannot page within one second: when more events share one
// created_at than a page holds, the pager steps past that second and marks
// the answer Truncated rather than loop.
type storedPager struct {
	// base is the first page's filter; base.Limit is the page size.
	base nostr.Filter
	// want is the caller's limit: the pager stops once it has seen this many
	// distinct events, and drops any beyond it.
	want int
	seen map[nostr.ID]struct{}
	// until bounds the current page (base.Until for the first page).
	until nostr.Timestamp
	// continued is set once a page beyond the first has been requested.
	continued bool
	// received and oldest describe the current page: every event the relay
	// sent for it and the oldest created_at among them.
	received int
	oldest   nostr.Timestamp
	// newest is the newest created_at the answer delivered, clamped to the
	// clock: the live follow-up REQ starts there.
	newest    nostr.Timestamp
	truncated bool
}

func newStoredPager(first nostr.Filter, want int) *storedPager {
	return &storedPager{base: first, want: want, seen: make(map[nostr.ID]struct{}), until: first.Until}
}

// beginPage resets the per-page counters before a page's REQ is sent (again,
// after a drop).
func (p *storedPager) beginPage() {
	p.received, p.oldest = 0, 0
}

// pageFilter is the filter for the current page.
func (p *storedPager) pageFilter() nostr.Filter {
	filter := p.base
	filter.Until = p.until
	return filter
}

// observe counts one event the relay sent for the current page and reports
// whether to forward it: an event beyond the caller's limit is not.
func (p *storedPager) observe(ev *nostr.Event) bool {
	p.received++
	if p.oldest == 0 || ev.CreatedAt < p.oldest {
		p.oldest = ev.CreatedAt
	}
	createdAt := ev.CreatedAt
	if now := nostr.Now(); createdAt > now {
		createdAt = now
	}
	if createdAt > p.newest {
		p.newest = createdAt
	}
	if _, dup := p.seen[ev.ID]; dup {
		return true
	}
	if len(p.seen) >= p.want {
		return false
	}
	p.seen[ev.ID] = struct{}{}
	return true
}

// advance runs at a page's EOSE and reports whether another page is due; if
// so, pageFilter now returns it.
func (p *storedPager) advance() bool {
	full := p.received >= p.base.Limit
	oldest := p.oldest
	if !full || len(p.seen) >= p.want {
		return false
	}
	next := oldest
	if p.until != 0 && next >= p.until {
		next = p.until - 1
		p.truncated = true
	}
	if next <= 0 || next < p.base.Since {
		return false
	}
	p.until = next
	p.continued = true
	return true
}

// pagedFollowOverlap starts the live REQ that follows a paged answer this far
// before the newest stored event. The first page's REQ, which would have
// stayed open for realtime events, was closed to page; an event a publisher
// with a lagging clock sent meanwhile is still caught, and deduplication
// absorbs the replay.
const pagedFollowOverlap = nostr.Timestamp(60)

// followSince is the since of the live REQ that follows a paged answer.
func (p *storedPager) followSince() nostr.Timestamp {
	return max(p.base.Since, p.newest-pagedFollowOverlap, 1)
}

// recordTruncated reports a paged answer that had to skip events; when it is
// the relay's first answer to the subscription it marks the relay's stored
// outcome Truncated, which makes StoredEventsIncomplete report it.
func (s *activeMergedSubscription) recordTruncated(relayURL string, filter nostr.Filter, firstAnswer bool) {
	s.pool.logger.Warn("more stored events share one created_at than the relay's NIP-11 max_limit lets a page hold; some were skipped",
		zap.String("relay", relayURL), zap.Int("max_limit", filter.Limit), zap.Ints("kinds", kindsToInts(filter.Kinds)))
	if !firstAnswer {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if outcome := s.outcomes[relayURL]; outcome != nil {
		outcome.Truncated = true
	}
}
