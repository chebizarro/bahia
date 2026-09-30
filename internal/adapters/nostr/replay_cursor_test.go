package nostr

import (
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func openTestLocalStore(t *testing.T, path string) *localstore.Store {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "daemon.bolt")
	}
	store, err := localstore.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func localStoreHas(store *localstore.Store, id gonostr.ID) bool {
	for range store.QueryEvents(gonostr.Filter{IDs: []gonostr.ID{id}}) {
		return true
	}
	return false
}

func TestInboundFilterHashIgnoresOrderAndReqBounds(t *testing.T) {
	a := gonostr.Generate().Public()
	b := gonostr.Generate().Public()
	base := gonostr.Filter{Kinds: []gonostr.Kind{5101, 4903}, Authors: []gonostr.PubKey{a, b}, Tags: gonostr.TagMap{"t": {"y", "x"}}}
	reordered := gonostr.Filter{Kinds: []gonostr.Kind{4903, 5101}, Authors: []gonostr.PubKey{b, a}, Tags: gonostr.TagMap{"t": {"x", "y"}}, Since: 10, Until: 20, Limit: 5}
	require.Equal(t, inboundFilterHash(base), inboundFilterHash(reordered))

	narrowed := base
	narrowed.Authors = []gonostr.PubKey{a}
	require.NotEqual(t, inboundFilterHash(base), inboundFilterHash(narrowed), "a changed author scope starts a new cursor")
}

func TestResumeSinceUsesOverlapOrLookbackNeverNow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cfg := InboundSyncConfig{ResumeOverlap: 10 * time.Minute, RegularLookback: time.Hour}.normalized()
	require.EqualValues(t, 1_000_000-600-1, cfg.resumeSince(1_000_000-1, now))
	require.EqualValues(t, 1_000_000-3600, cfg.resumeSince(0, now), "a fresh filter starts at the lookback window, not now (C-3)")
	require.Zero(t, cfg.resumeSince(300, now), "an overlap reaching past the epoch starts at the beginning")

	full := InboundSyncConfig{ResumeOverlap: time.Minute}
	full.RegularLookback = 0
	require.Zero(t, full.resumeSince(0, now), "a zero lookback replays the whole history")
}

func TestCursorTrackerRules(t *testing.T) {
	store := openTestLocalStore(t, "")
	self := gonostr.Generate()
	foreign := gonostr.Generate()
	now := time.Unix(10_000, 0)
	tracker := newCursorTracker(store, map[gonostr.PubKey]struct{}{self.Public(): {}}, func() time.Time { return now }, zap.NewNop())
	key := cursorKey{relay: "wss://a.example", hash: "f"}
	event := func(sk gonostr.SecretKey, createdAt gonostr.Timestamp) *gonostr.Event {
		ev := &gonostr.Event{Kind: 4903, CreatedAt: createdAt}
		require.NoError(t, ev.Sign(sk))
		return ev
	}
	cursor := func() gonostr.Timestamp {
		value, err := store.Cursor(key.relay, key.hash)
		require.NoError(t, err)
		return value
	}

	tracker.begin(key)
	tracker.observe(key, event(foreign, 9_000), true)
	tracker.observe(key, event(self, 9_900), true)
	require.Zero(t, cursor(), "stored history does not move the cursor before EOSE")
	tracker.commit(key, 0)
	require.EqualValues(t, 9_000, cursor(), "the daemon's own events never advance an inbound cursor")

	tracker.observe(key, event(foreign, 9_500), true)
	require.EqualValues(t, 9_500, cursor(), "after EOSE live events advance the cursor")
	tracker.observe(key, event(foreign, 99_999), true)
	require.EqualValues(t, 10_000, cursor(), "a future-dated event is capped at the local clock")

	other := cursorKey{relay: "wss://b.example", hash: "f"}
	tracker.begin(other)
	tracker.observe(other, event(foreign, 9_800), false)
	tracker.observe(other, event(foreign, 9_700), true)
	tracker.commit(other, 0)
	value, err := store.Cursor(other.relay, other.hash)
	require.NoError(t, err)
	require.Zero(t, value, "a REQ in which an event failed to store is not committed")

	empty := cursorKey{relay: "wss://c.example", hash: "f"}
	tracker.begin(empty)
	tracker.commit(empty, 4_000)
	value, err = store.Cursor(empty.relay, empty.hash)
	require.NoError(t, err)
	require.EqualValues(t, 4_000, value, "an empty REQ anchors the cursor at the since it proved complete")
}
