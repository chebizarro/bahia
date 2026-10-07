package localstore

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type localTagFixture struct {
	config   string // a 108-byte relay config coordinate
	longT    string
	byAlice  nostr.Event
	byBob    nostr.Event
	emptyD   nostr.Event
	longTagT nostr.Event
	shortT   nostr.Event
}

func newLocalTagFixture(t *testing.T) (localTagFixture, nostr.SecretKey) {
	t.Helper()
	alice, bob := nostr.Generate(), nostr.Generate()
	f := localTagFixture{longT: strings.Repeat("t", 150)}
	f.config = fmt.Sprintf("30078:%s:%s", alice.Public().Hex(), strings.Repeat("c", 108-len("30078:")-64-len(":")))
	f.byAlice = signed(t, alice, 1, 100, nostr.Tags{{"a", f.config}}, "alice")
	f.byBob = signed(t, bob, 1, 101, nostr.Tags{{"a", f.config}, {"p", alice.Public().Hex()}}, "bob")
	f.emptyD = signed(t, alice, 30078, 102, nostr.Tags{{"d", ""}}, "empty d")
	f.longTagT = signed(t, alice, 1, 103, nostr.Tags{{"t", f.longT}, {"t", "short"}}, "long t")
	f.shortT = signed(t, bob, 1, 104, nostr.Tags{{"t", "short"}}, "short t")
	return f, alice
}

func (f localTagFixture) events() []nostr.Event {
	return []nostr.Event{f.byAlice, f.byBob, f.emptyD, f.longTagT, f.shortT}
}

func requireLocalTagFilters(t *testing.T, store *Store, f localTagFixture, alice nostr.PubKey) {
	t.Helper()
	byConfig := nostr.TagMap{"a": {f.config}}
	for name, tc := range map[string]struct {
		filter nostr.Filter
		want   []nostr.ID
	}{
		"#a 108-byte coordinate":   {filter: nostr.Filter{Tags: byConfig}, want: []nostr.ID{f.byBob.ID, f.byAlice.ID}},
		"#a with authors":          {filter: nostr.Filter{Authors: []nostr.PubKey{alice}, Tags: byConfig}, want: []nostr.ID{f.byAlice.ID}},
		"#a with limit":            {filter: nostr.Filter{Tags: byConfig, Limit: 1}, want: []nostr.ID{f.byBob.ID}},
		"#a and #p":                {filter: nostr.Filter{Tags: nostr.TagMap{"a": {f.config}, "p": {alice.Hex()}}}, want: []nostr.ID{f.byBob.ID}},
		"empty #d":                 {filter: nostr.Filter{Kinds: []nostr.Kind{30078}, Tags: nostr.TagMap{"d": {""}}}, want: []nostr.ID{f.emptyD.ID}},
		"#t over 100 bytes":        {filter: nostr.Filter{Tags: nostr.TagMap{"t": {f.longT}}}, want: []nostr.ID{f.longTagT.ID}},
		"#t long and short values": {filter: nostr.Filter{Tags: nostr.TagMap{"t": {f.longT, "short"}}}, want: []nostr.ID{f.shortT.ID, f.longTagT.ID}},
		"#a with since":            {filter: nostr.Filter{Since: 101, Tags: byConfig}, want: []nostr.ID{f.byBob.ID}},
	} {
		require.Equal(t, tc.want, storedIDs(store, tc.filter), name)
	}
}

// TestQueryEventsMatchesEmptyAndLongTagValues: QueryEvents
// filters on tag values the eventstore does not index (empty, or over 100
// bytes) match the events that carry them, through the local tag index, and
// follow replacement and deletion.
func TestQueryEventsMatchesEmptyAndLongTagValues(t *testing.T) {
	store, _ := openTemp(t)
	f, alice := newLocalTagFixture(t)
	for _, ev := range f.events() {
		requireSaved(t, store, ev, true, ev.Content)
	}
	requireLocalTagFilters(t, store, f, alice.Public())

	newer := signed(t, alice, 30078, 200, nil, "missing d supersedes empty d")
	requireSaved(t, store, newer, true, "")
	require.Empty(t, storedIDs(store, nostr.Filter{Tags: nostr.TagMap{"d": {""}}}), "the superseded version left the index")
	require.NoError(t, store.DeleteEvent(f.byAlice.ID))
	require.Equal(t, []nostr.ID{f.byBob.ID}, storedIDs(store, nostr.Filter{Tags: nostr.TagMap{"a": {f.config}}}))
	pruned, err := store.PruneRegularEvents(time.Unix(150, 0))
	require.NoError(t, err)
	require.Positive(t, pruned)
	require.Empty(t, storedIDs(store, nostr.Filter{Tags: nostr.TagMap{"t": {f.longT}}}), "pruned events left the index")
}

// TestOpenBuildsTheTagIndexForExistingStores: a store written before the tag
// index existed is indexed once on open (tagIndexMarker).
func TestOpenBuildsTheTagIndexForExistingStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.bolt")
	f, alice := newLocalTagFixture(t)
	writeAsBeforeTheIndex(t, path, f.events()...)

	store, err := Open(path)
	require.NoError(t, err)
	defer store.Close()
	require.Empty(t, slices.Collect(store.pagedQuery(nostr.Filter{Tags: nostr.TagMap{"a": {f.config}}}, 0)), "precondition: the eventstore alone matches nothing")
	done, err := tagIndexMarker.Done(store.backend().DB)
	require.NoError(t, err)
	require.True(t, done)
	requireLocalTagFilters(t, store, f, alice.Public())
}
