package nostr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"go.uber.org/zap"
)

// Inbound resume cursors (bahia-irsry.10.1; audit C-2, C-3, B-15).
//
// A cursor is kept per (relay, filter hash) in the local event store. It is the
// newest created_at, clamped to the local clock, among the events that relay
// delivered for that filter and that were durably stored:
//   - events of one REQ count only once that REQ's EOSE proves the stored
//     history below them complete; after EOSE, live events advance it as they
//     arrive (the rule SoulFactory's resume cursor uses);
//   - the daemon's own events never move it, whatever their created_at;
//   - a future-dated event cannot push it past the local clock;
//   - a REQ during which an event failed to store is never committed, so the
//     event is fetched again on the next resume.
//
// A resume REQ starts at cursor - overlap, so late-propagated and modestly
// backdated events are still delivered; duplicates are dropped by id against
// the local store. Replaceable and addressable sets do not rely on cursors at
// all: they are reconciled in full with NIP-77, or paged in full, on every
// (re)connect (C-3).

const (
	// defaultInboundResumeOverlap is how far before a cursor a resume REQ
	// starts.
	defaultInboundResumeOverlap = 10 * time.Minute
	// defaultInboundRegularLookback is how far back a regular-kind filter
	// with no cursor (a fresh node, a new filter, a deleted store) starts.
	defaultInboundRegularLookback = 24 * time.Hour
	// defaultInboundRegularRetention bounds regular events kept in the local
	// store. It is far longer than the overlap, so an ordinary resume never
	// redelivers a pruned event.
	defaultInboundRegularRetention = 7 * 24 * time.Hour
	// defaultNegentropyTimeout bounds one NIP-77 session. A relay that does
	// not speak NIP-77 ignores NEG-OPEN and never answers.
	defaultNegentropyTimeout = 30 * time.Second
)

// InboundSyncConfig tunes how inbound subscriptions catch up with a relay.
type InboundSyncConfig struct {
	// ResumeOverlap is subtracted from a cursor when a REQ resumes.
	ResumeOverlap time.Duration
	// RegularLookback is where a regular-kind filter with no cursor starts,
	// relative to now; zero replays the relay's whole history.
	RegularLookback time.Duration
	// NegentropyUpload also publishes to a relay the replaceable and
	// addressable events it lacks and the local store holds (relay repair).
	NegentropyUpload bool
	// NegentropyUploadFilter, when set, is consulted per relay before uploading.
	// If it returns false for a relay URL, upload is suppressed even when
	// NegentropyUpload is true. This scopes uploads to the daemon's own relays
	// so control-plane events are not pushed to interop relays (.50 item 4).
	NegentropyUploadFilter func(relayURL string) bool
	// PageLimit is the `limit` of each catch-up REQ page (lowered to a
	// relay's NIP-11 max_limit). A full page is followed by an older page
	// bounded with `until`.
	PageLimit int
	// NegentropyTimeout bounds one NIP-77 session.
	NegentropyTimeout time.Duration
}

// IsZero reports whether the config is the zero value (all scalar fields are
// zero and the function fields are nil). It replaces direct struct comparison
// which is invalid when the struct contains func fields.
func (c InboundSyncConfig) IsZero() bool {
	return c.ResumeOverlap == 0 && c.RegularLookback == 0 && !c.NegentropyUpload &&
		c.NegentropyUploadFilter == nil && c.PageLimit == 0 && c.NegentropyTimeout == 0
}

// DefaultInboundSyncConfig returns the defaults used when none is configured.
func DefaultInboundSyncConfig() InboundSyncConfig {
	return InboundSyncConfig{
		ResumeOverlap:     defaultInboundResumeOverlap,
		RegularLookback:   defaultInboundRegularLookback,
		PageLimit:         defaultBootstrapPageLimit,
		NegentropyTimeout: defaultNegentropyTimeout,
	}
}

func (c InboundSyncConfig) normalized() InboundSyncConfig {
	defaults := DefaultInboundSyncConfig()
	if c.ResumeOverlap <= 0 {
		c.ResumeOverlap = defaults.ResumeOverlap
	}
	if c.RegularLookback < 0 {
		c.RegularLookback = 0
	}
	if c.PageLimit <= 0 {
		c.PageLimit = defaults.PageLimit
	}
	if c.NegentropyTimeout <= 0 {
		c.NegentropyTimeout = defaults.NegentropyTimeout
	}
	return c
}

// resumeSince is where a REQ for a filter resumes: the cursor less the
// overlap or, with no cursor, the configured lookback before now (zero means
// the whole history).
func (c InboundSyncConfig) resumeSince(cursor gonostr.Timestamp, now time.Time) gonostr.Timestamp {
	if cursor > 0 {
		overlap := gonostr.Timestamp(c.ResumeOverlap / time.Second)
		if cursor <= overlap {
			return 0
		}
		return cursor - overlap
	}
	if c.RegularLookback <= 0 {
		return 0
	}
	return gonostr.Timestamp(now.Add(-c.RegularLookback).Unix())
}

// inboundFilter is one filter of an inbound subscription, without the
// since/until/limit a particular REQ adds. Filters never mix persistent kinds
// (see isPersistentKind) with regular ones: the two catch up differently.
type inboundFilter struct {
	filter     gonostr.Filter
	hash       string
	persistent bool
}

// splitInboundFilter splits base by kind class into at most two filters: the
// replaceable/addressable kinds and the regular kinds.
func splitInboundFilter(base gonostr.Filter) []inboundFilter {
	base.Since, base.Until, base.Limit, base.LimitZero = 0, 0, 0, false
	var persistent, regular []gonostr.Kind
	for _, kind := range base.Kinds {
		if isPersistentKind(kind) {
			persistent = append(persistent, kind)
		} else {
			regular = append(regular, kind)
		}
	}
	out := make([]inboundFilter, 0, 2)
	for _, group := range []struct {
		kinds      []gonostr.Kind
		persistent bool
	}{{persistent, true}, {regular, false}} {
		if len(group.kinds) == 0 {
			continue
		}
		filter := base
		filter.Kinds = group.kinds
		out = append(out, inboundFilter{filter: filter, hash: inboundFilterHash(filter), persistent: group.persistent})
	}
	return out
}

// isPersistentKind reports whether a kind is state that a node must hold in
// full whatever its age: replaceable and addressable events, and NIP-09
// deletion requests, which are the tombstones of that state. These sets are
// reconciled in full on every (re)connect instead of resuming from a cursor.
func isPersistentKind(kind gonostr.Kind) bool {
	return kind.IsReplaceable() || kind.IsAddressable() || kind == gonostr.KindDeletion
}

// inboundFilterHash identifies a filter independently of the order of its
// kinds, authors, ids and tag values, and of since/until/limit. A changed
// filter (new authors, say) gets a new hash and so starts without a cursor.
func inboundFilterHash(filter gonostr.Filter) string {
	canonical := struct {
		IDs     []string   `json:"ids,omitempty"`
		Kinds   []int      `json:"kinds,omitempty"`
		Authors []string   `json:"authors,omitempty"`
		Tags    [][]string `json:"tags,omitempty"`
		Search  string     `json:"search,omitempty"`
	}{Search: filter.Search}
	for _, id := range filter.IDs {
		canonical.IDs = append(canonical.IDs, id.Hex())
	}
	for _, kind := range filter.Kinds {
		canonical.Kinds = append(canonical.Kinds, int(kind))
	}
	for _, author := range filter.Authors {
		canonical.Authors = append(canonical.Authors, author.Hex())
	}
	for name, values := range filter.Tags {
		tag := append([]string{name}, values...)
		sort.Strings(tag[1:])
		canonical.Tags = append(canonical.Tags, slices.Compact(tag))
	}
	sort.Strings(canonical.IDs)
	sort.Ints(canonical.Kinds)
	sort.Strings(canonical.Authors)
	sort.Slice(canonical.Tags, func(i, j int) bool { return canonical.Tags[i][0] < canonical.Tags[j][0] })
	canonical.IDs = slices.Compact(canonical.IDs)
	canonical.Kinds = slices.Compact(canonical.Kinds)
	canonical.Authors = slices.Compact(canonical.Authors)
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}

// clampToClock caps an author-asserted timestamp at the local clock, so a
// future-dated event cannot move a cursor past events not yet seen.
func clampToClock(ts gonostr.Timestamp, now time.Time) gonostr.Timestamp {
	if limit := gonostr.Timestamp(now.Unix()); ts > limit {
		return limit
	}
	return ts
}

type cursorKey struct {
	relay string
	hash  string
}

type cursorGeneration struct {
	newest gonostr.Timestamp
	live   bool
	dirty  bool
}

// cursorTracker applies the cursor rules above for one consumer goroutine; it
// is not safe for concurrent use.
type cursorTracker struct {
	store  *localstore.Store
	self   map[gonostr.PubKey]struct{}
	now    func() time.Time
	logger *zap.Logger
	state  map[cursorKey]*cursorGeneration
}

func newCursorTracker(store *localstore.Store, self map[gonostr.PubKey]struct{}, now func() time.Time, logger *zap.Logger) *cursorTracker {
	return &cursorTracker{store: store, self: self, now: now, logger: logger, state: make(map[cursorKey]*cursorGeneration)}
}

// begin starts a new REQ generation for key.
func (c *cursorTracker) begin(key cursorKey) {
	c.state[key] = &cursorGeneration{}
}

// observe records one event key's relay delivered. stored reports whether the
// event is now durably held (new or already held); an event that failed to
// store marks the generation dirty so it is never committed.
func (c *cursorTracker) observe(key cursorKey, ev *gonostr.Event, stored bool) {
	gen := c.state[key]
	if gen == nil {
		gen = &cursorGeneration{}
		c.state[key] = gen
	}
	if !stored {
		gen.dirty = true
		return
	}
	if _, own := c.self[ev.PubKey]; own {
		return
	}
	ts := clampToClock(ev.CreatedAt, c.now())
	if !gen.live {
		gen.newest = max(gen.newest, ts)
		return
	}
	if !gen.dirty {
		c.advance(key, ts)
	}
}

// commit ends key's stored-history phase (its relay sent EOSE): the
// generation's newest event, or floor if that is later, becomes the cursor,
// and later events advance it live. floor is the `since` a catch-up REQ with
// no prior cursor proved complete from: it anchors a fresh filter whose relay
// returned nothing, so its lookback window does not slide forward.
func (c *cursorTracker) commit(key cursorKey, floor gonostr.Timestamp) {
	gen := c.state[key]
	if gen == nil {
		gen = &cursorGeneration{}
		c.state[key] = gen
	}
	gen.live = true
	if gen.dirty {
		c.logger.Warn("inbound cursor not advanced: an event from this REQ failed to store",
			zap.String("relay", key.relay), zap.String("filter", key.hash))
		return
	}
	c.advance(key, clampToClock(max(gen.newest, floor), c.now()))
}

func (c *cursorTracker) advance(key cursorKey, to gonostr.Timestamp) {
	if err := c.store.AdvanceCursor(key.relay, key.hash, to); err != nil {
		c.logger.Warn("persist inbound cursor failed", zap.String("relay", key.relay), zap.String("filter", key.hash), zap.Error(err))
	}
}
