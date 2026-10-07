package localstore

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func signed(t *testing.T, sk nostr.SecretKey, kind nostr.Kind, createdAt nostr.Timestamp, tags nostr.Tags, content string) nostr.Event {
	t.Helper()
	ev := nostr.Event{Kind: kind, CreatedAt: createdAt, Tags: tags, Content: content}
	require.NoError(t, ev.Sign(sk))
	return ev
}

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache", "daemon.bolt")
	store, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func ids(events []nostr.Event) []nostr.ID {
	out := make([]nostr.ID, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.ID)
	}
	return out
}

func TestSaveEventDedupsByIDAndCollapsesReplaceables(t *testing.T) {
	store, _ := openTemp(t)
	sk := nostr.Generate()

	note := signed(t, sk, 4903, 100, nil, "audit")
	stored, err := store.SaveEvent(note)
	require.NoError(t, err)
	require.True(t, stored)
	stored, err = store.SaveEvent(note)
	require.NoError(t, err)
	require.False(t, stored, "a redelivered id is not new")

	v1 := signed(t, sk, 30078, 100, nostr.Tags{{"d", "app"}}, "v1")
	v2 := signed(t, sk, 30078, 200, nostr.Tags{{"d", "app"}}, "v2")
	stored, err = store.SaveEvent(v2)
	require.NoError(t, err)
	require.True(t, stored)
	stored, err = store.SaveEvent(v1)
	require.NoError(t, err)
	require.False(t, stored, "an older version of a held coordinate is not new")
	require.False(t, store.hasLocked(v1.ID))

	v3 := signed(t, sk, 30078, 300, nostr.Tags{{"d", "app"}}, "v3")
	stored, err = store.SaveEvent(v3)
	require.NoError(t, err)
	require.True(t, stored)
	require.False(t, store.hasLocked(v2.ID), "the newer version replaces the older one")
	require.Equal(t, []nostr.ID{v3.ID}, ids(slices.Collect(store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{30078}}))))

	ephemeral := signed(t, sk, 25910, 100, nil, "")
	stored, err = store.SaveEvent(ephemeral)
	require.NoError(t, err)
	require.True(t, stored)
	require.False(t, store.hasLocked(ephemeral.ID), "ephemeral events are never cached")
}

func TestEventsAndCursorsSurviveReopenAndDeletingTheFileResetsBoth(t *testing.T) {
	store, path := openTemp(t)
	ev := signed(t, nostr.Generate(), 5101, 100, nil, "result")
	_, err := store.SaveEvent(ev)
	require.NoError(t, err)
	require.NoError(t, store.AdvanceCursor("wss://a", "f1", 150))
	require.NoError(t, store.AdvanceCursor("wss://a", "f1", 120), "a cursor never moves back")
	require.NoError(t, store.AdvanceCursor("wss://b", "f1", 90))
	require.NoError(t, store.Close())

	reopened, err := Open(path)
	require.NoError(t, err)
	require.True(t, reopened.hasLocked(ev.ID))
	cursor, err := reopened.Cursor("wss://a", "f1")
	require.NoError(t, err)
	require.EqualValues(t, 150, cursor)
	cursor, err = reopened.Cursor("wss://b", "f1")
	require.NoError(t, err)
	require.EqualValues(t, 90, cursor, "cursors are per relay")
	cursor, err = reopened.Cursor("wss://a", "f2")
	require.NoError(t, err)
	require.Zero(t, cursor, "cursors are per filter")
	require.NoError(t, reopened.Close())

	require.NoError(t, os.Remove(path))
	fresh, err := Open(path)
	require.NoError(t, err)
	defer fresh.Close()
	require.False(t, fresh.hasLocked(ev.ID))
	cursor, err = fresh.Cursor("wss://a", "f1")
	require.NoError(t, err)
	require.Zero(t, cursor)
}

func TestOpenSharesOneDatabasePerPathAndRecreatesCorruptFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.bolt")
	first, err := Open(path)
	require.NoError(t, err)
	second, err := Open(path)
	require.NoError(t, err, "a replacement App opening the same path must not wait on bbolt's lock")
	ev := signed(t, nostr.Generate(), 4903, 100, nil, "")
	_, err = first.SaveEvent(ev)
	require.NoError(t, err)
	require.True(t, second.hasLocked(ev.ID))
	require.NoError(t, first.Close())
	require.True(t, second.hasLocked(ev.ID), "the database stays open until its last handle closes")
	require.NoError(t, second.Close())
	require.NoError(t, second.Close())

	corrupt := filepath.Join(t.TempDir(), "corrupt.bolt")
	require.NoError(t, os.WriteFile(corrupt, []byte("this is not a bbolt file, it is a cache that was damaged"), 0o600))
	store, err := Open(corrupt)
	require.NoError(t, err)
	defer store.Close()
	matches, err := filepath.Glob(corrupt + ".corrupt-*")
	require.NoError(t, err)
	require.Len(t, matches, 1, "the damaged file is kept aside for inspection")
	_, err = store.SaveEvent(ev)
	require.NoError(t, err)
}

func TestQueryEventsPagesPastTiesAndHonoursLimit(t *testing.T) {
	previous := queryPageSize
	queryPageSize = 3
	t.Cleanup(func() { queryPageSize = previous })
	store, _ := openTemp(t)
	sk := nostr.Generate()
	var all []nostr.Event
	for i := range 10 {
		// Seven events share one timestamp: more than a page.
		createdAt := nostr.Timestamp(100)
		if i >= 7 {
			createdAt = nostr.Timestamp(100 + i)
		}
		ev := signed(t, sk, 4903, createdAt, nil, string(rune('a'+i)))
		_, err := store.SaveEvent(ev)
		require.NoError(t, err)
		all = append(all, ev)
	}
	got := slices.Collect(store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{4903}}))
	require.ElementsMatch(t, ids(all), ids(got))
	require.Len(t, slices.Collect(store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{4903}, Limit: 4})), 4)
}

func TestPruneRegularEventsKeepsReplaceableState(t *testing.T) {
	store, _ := openTemp(t)
	sk := nostr.Generate()
	now := time.Unix(10_000, 0)
	old := signed(t, sk, 4903, 1_000, nil, "old audit")
	recent := signed(t, sk, 4903, 9_000, nil, "recent audit")
	state := signed(t, sk, 30900, 1_000, nostr.Tags{{"d", "svc"}}, "state")
	for _, ev := range []nostr.Event{old, recent, state} {
		_, err := store.SaveEvent(ev)
		require.NoError(t, err)
	}
	removed, err := store.PruneRegularEvents(now.Add(-2 * time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.False(t, store.hasLocked(old.ID))
	require.True(t, store.hasLocked(recent.ID))
	require.True(t, store.hasLocked(state.ID), "replaceable state is bounded by its coordinates, never by age")
}

// NIP-40: an event whose expiration has passed is pruned whatever its kind
// (addressable tombstones included), one that has not, or carries no
// expiration, is kept.
func TestPruneExpiredEventsHonoursNIP40OnEveryKind(t *testing.T) {
	store, _ := openTemp(t)
	sk := nostr.Generate()
	now := time.Unix(1_700_000_000, 0)
	expiredTombstone := signed(t, sk, 30900, 100, nostr.Tags{{"d", "security:run:1"}, {"deleted", "true"}, {"expiration", "1699999999"}}, "{}")
	liveTombstone := signed(t, sk, 30900, 100, nostr.Tags{{"d", "security:run:2"}, {"deleted", "true"}, {"expiration", "1700000001"}}, "{}")
	noExpiry := signed(t, sk, 30900, 100, nostr.Tags{{"d", "security:run:3"}}, "{}")
	expiredRegular := signed(t, sk, 4903, 100, nostr.Tags{{"expiration", "1"}}, "audit")
	malformed := signed(t, sk, 30900, 100, nostr.Tags{{"d", "security:run:4"}, {"expiration", "soon"}}, "{}")
	for _, ev := range []nostr.Event{expiredTombstone, liveTombstone, noExpiry, expiredRegular, malformed} {
		_, err := store.SaveEvent(ev)
		require.NoError(t, err)
	}
	removed, err := store.PruneExpiredEvents(now)
	require.NoError(t, err)
	require.Equal(t, 2, removed)
	var held []nostr.ID
	for ev := range store.QueryEvents(nostr.Filter{}) {
		held = append(held, ev.ID)
	}
	require.ElementsMatch(t, []nostr.ID{liveTombstone.ID, noExpiry.ID, malformed.ID}, held)
	removed, err = store.PruneExpiredEvents(now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, 1, removed, "the deadline is inclusive")
}
