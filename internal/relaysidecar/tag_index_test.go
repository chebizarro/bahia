package relaysidecar

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/boltdb"
	"github.com/openagentsinc/bahia/internal/boltcoord"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

// configCoordinate is a relay config `a` coordinate: 108 bytes, past the
// eventstore's tag index limit.
func configCoordinate(author nostr.PubKey) string {
	return fmt.Sprintf("30078:%s:%s", author.Hex(), strings.Repeat("c", 108-len("30078:")-64-len(":")))
}

type tagFilterFixture struct {
	alice, bob nostr.SecretKey
	config     string
	longT      string
	byAlice    nostr.Event // #a config
	byBob      nostr.Event // #a config, #p alice
	emptyD     nostr.Event // addressable, d ""
	longTagT   nostr.Event // #t long and short
	shortT     nostr.Event // #t short
}

func newTagFilterFixture(t *testing.T) tagFilterFixture {
	t.Helper()
	base := nostr.Now() - 600
	f := tagFilterFixture{alice: nostr.Generate(), bob: nostr.Generate(), longT: strings.Repeat("t", 150)}
	f.config = configCoordinate(f.alice.Public())
	f.byAlice = signedStoreEvent(t, f.alice, 1, base, nostr.Tags{{"a", f.config}}, "alice")
	f.byBob = signedStoreEvent(t, f.bob, 1, base+1, nostr.Tags{{"a", f.config}, {"p", f.alice.Public().Hex()}}, "bob")
	f.emptyD = signedStoreEvent(t, f.alice, 30078, base+2, nostr.Tags{{"d", ""}}, "empty d")
	f.longTagT = signedStoreEvent(t, f.alice, 1, base+3, nostr.Tags{{"t", f.longT}, {"t", "short"}}, "long t")
	f.shortT = signedStoreEvent(t, f.bob, 1, base+4, nostr.Tags{{"t", "short"}}, "short t")
	return f
}

func (f tagFilterFixture) events() []nostr.Event {
	return []nostr.Event{f.byAlice, f.byBob, f.emptyD, f.longTagT, f.shortT}
}

// cases are filters on tag values the eventstore does not index, with the
// events each must match, newest first.
func (f tagFilterFixture) cases() map[string]struct {
	filter nostr.Filter
	want   []nostr.ID
} {
	byConfig := nostr.TagMap{"a": {f.config}}
	return map[string]struct {
		filter nostr.Filter
		want   []nostr.ID
	}{
		"#a 108-byte coordinate":    {filter: nostr.Filter{Tags: byConfig}, want: []nostr.ID{f.byBob.ID, f.byAlice.ID}},
		"#a with authors":           {filter: nostr.Filter{Authors: []nostr.PubKey{f.alice.Public()}, Tags: byConfig}, want: []nostr.ID{f.byAlice.ID}},
		"#a with limit":             {filter: nostr.Filter{Tags: byConfig, Limit: 1}, want: []nostr.ID{f.byBob.ID}},
		"#a and #p":                 {filter: nostr.Filter{Tags: nostr.TagMap{"a": {f.config}, "p": {f.alice.Public().Hex()}}}, want: []nostr.ID{f.byBob.ID}},
		"empty #d":                  {filter: nostr.Filter{Kinds: []nostr.Kind{30078}, Tags: nostr.TagMap{"d": {""}}}, want: []nostr.ID{f.emptyD.ID}},
		"#t over 100 bytes":         {filter: nostr.Filter{Tags: nostr.TagMap{"t": {f.longT}}}, want: []nostr.ID{f.longTagT.ID}},
		"#t long and short values":  {filter: nostr.Filter{Tags: nostr.TagMap{"t": {f.longT, "short"}}}, want: []nostr.ID{f.shortT.ID, f.longTagT.ID}},
		"#a another coordinate":     {filter: nostr.Filter{Tags: nostr.TagMap{"a": {configCoordinate(f.bob.Public())}}}},
		"#a with a kind it has not": {filter: nostr.Filter{Kinds: []nostr.Kind{7}, Tags: byConfig}},
	}
}

func requireTagFilters(t *testing.T, store *eventStore, f tagFilterFixture) {
	t.Helper()
	for name, tc := range f.cases() {
		require.Equal(t, tc.want, storeIDs(t, store, tc.filter), "REQ %s", name)
		countFilter := tc.filter
		countFilter.Limit = 0
		count, err := store.Count(t.Context(), countFilter)
		require.NoError(t, err)
		want := len(tc.want)
		if name == "#a with limit" {
			want = 2 // COUNT ignores limit
		}
		require.Equal(t, uint32(want), count, "COUNT %s", name)
	}
}

// TestEventStoreMatchesEmptyAndLongTagValues: REQ and COUNT
// filters on tag values the eventstore does not index (empty, or over 100
// bytes) match the events that carry them, through the sidecar's tag index.
func TestEventStoreMatchesEmptyAndLongTagValues(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	f := newTagFilterFixture(t)
	for _, event := range f.events() {
		saveOrReplace(t, store, event)
	}
	requireTagFilters(t, store, f)

	// Superseded, deleted and expired events leave those results.
	newer := signedStoreEvent(t, f.alice, 30078, f.emptyD.CreatedAt+10, nostr.Tags{{"d", ""}}, "newer empty d")
	saveOrReplace(t, store, newer)
	require.Equal(t, []nostr.ID{newer.ID}, storeIDs(t, store, nostr.Filter{Tags: nostr.TagMap{"d": {""}}}))
	require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, f.bob, nostr.KindDeletion, nostr.Now(), nostr.Tags{{"e", f.byBob.ID.Hex()}}, "")))
	require.Equal(t, []nostr.ID{f.byAlice.ID}, storeIDs(t, store, nostr.Filter{Tags: nostr.TagMap{"a": {f.config}}}))
	count, err := store.Count(t.Context(), nostr.Filter{Tags: nostr.TagMap{"a": {f.config}}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), count)
}

// TestEventStoreBuildsTheTagIndexForExistingStores: a store written before
// the tag index existed (no entries, no marker) is indexed once on open.
func TestEventStoreBuildsTheTagIndexForExistingStores(t *testing.T) {
	dir := t.TempDir()
	f := newTagFilterFixture(t)
	store, err := openEventStore(t.Context(), dir, nil)
	require.NoError(t, err)
	for _, event := range f.events() {
		saveOrReplace(t, store, event)
	}
	require.NoError(t, store.backend().DB.Update(func(tx *bbolt.Tx) error {
		if err := tx.DeleteBucket(sidecarTagBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucket(sidecarTagBucket); err != nil {
			return err
		}
		return tx.Bucket(sidecarMetaBucket).Delete(tagIndexMarker.Key)
	}))
	require.Empty(t, storeIDs(t, store, nostr.Filter{Tags: nostr.TagMap{"a": {f.config}}}), "precondition: the index is empty")
	require.NoError(t, store.Close())

	store = openTestEventStore(t, dir)
	done, err := tagIndexMarker.Done(store.backend().DB)
	require.NoError(t, err)
	require.True(t, done)
	requireTagFilters(t, store, f)
}

// writePreIrsry44Store writes events into dataDir's events.bolt the way the
// sidecar did before bahia-irsry.44: straight through the eventstore, with
// ReplaceEvent for state (which keeps every version of a d it does not index)
// and kind-5 requests stored without their long coordinates applied, and none
// of Bahia's buckets or markers.
func writePreIrsry44Store(t *testing.T, dataDir string, events ...nostr.Event) {
	t.Helper()
	backend := &boltdb.BoltBackend{Path: filepath.Join(dataDir, eventStoreFile)}
	require.NoError(t, backend.Init())
	for _, event := range events {
		if event.Kind.IsReplaceable() || event.Kind.IsAddressable() {
			_, err := backend.ReplaceEvent(event)
			require.NoError(t, err)
			continue
		}
		require.NoError(t, backend.SaveEvent(event))
	}
	backend.Close()
}

// TestEventStoreRepairsPreIrsry44StoresOnce: on its first
// open after the upgrade, a store written before bahia-irsry.44 drops the
// events its stored kind-5 requests delete and every superseded version of an
// empty or long-d coordinate, behind its own marker. A rerun without the
// marker converges on the same store.
func TestEventStoreRepairsPreIrsry44StoresOnce(t *testing.T) {
	dir := t.TempDir()
	sk := nostr.Generate()
	base := nostr.Now() - 3600
	longD := strings.Repeat("d", 150)
	config := configCoordinate(sk.Public())
	_, _, configD, ok := boltcoord.ParseAddress(config)
	require.True(t, ok)
	stale := signedStoreEvent(t, sk, 30078, base, nostr.Tags{{"d", longD}}, "stale")
	current := signedStoreEvent(t, sk, 30078, base+100, nostr.Tags{{"d", longD}}, "current")
	emptyStale := signedStoreEvent(t, sk, 30079, base, nostr.Tags{{"d", ""}}, "empty stale")
	emptyCurrent := signedStoreEvent(t, sk, 30079, base+100, nil, "missing d current")
	deletedConfig := signedStoreEvent(t, sk, 30078, base, nostr.Tags{{"d", configD}, {"a", config}}, "deleted relay config")
	note := signedStoreEvent(t, sk, 1, base, nil, "deleted note")
	foreign := signedStoreEvent(t, nostr.Generate(), 1, base, nil, "another author's note")
	request := signedStoreEvent(t, sk, nostr.KindDeletion, base+50, nostr.Tags{{"a", config}, {"e", note.ID.Hex()}, {"e", foreign.ID.Hex()}}, "")
	writePreIrsry44Store(t, dir, stale, current, emptyStale, emptyCurrent, deletedConfig, note, foreign, request)

	all := nostr.Filter{Kinds: []nostr.Kind{1, 30078, 30079, nostr.KindDeletion}}
	requireRepaired := func(store *eventStore) {
		t.Helper()
		require.ElementsMatch(t, []nostr.ID{current.ID, emptyCurrent.ID, foreign.ID, request.ID}, storeIDs(t, store, all))
		require.Equal(t, []nostr.ID{current.ID}, storeIDs(t, store, nostr.Filter{Tags: nostr.TagMap{"d": {longD}}}))
		require.Equal(t, []nostr.ID{request.ID}, storeIDs(t, store, nostr.Filter{Tags: nostr.TagMap{"a": {config}}}), "the deleted config left the tag index; the request names the coordinate too")
		require.ErrorIs(t, store.Replace(t.Context(), deletedConfig), errEventDeleted)
		require.ErrorIs(t, store.Save(t.Context(), note), errEventDeleted)
		require.Error(t, store.Replace(t.Context(), stale), "a superseded version is not stored again")
	}

	store, err := openEventStore(t.Context(), dir, nil)
	require.NoError(t, err)
	requireRepaired(store)
	for _, marker := range []struct {
		name string
		done func(*bbolt.DB) (bool, error)
	}{{"repair", coordinateRepairMarker.Done}, {"tag index", tagIndexMarker.Done}, {"deletion index", deletionIndexMarker.Done}} {
		done, err := marker.done(store.backend().DB)
		require.NoError(t, err)
		require.True(t, done, marker.name)
	}
	require.NoError(t, store.Close())

	// Marked: a stale version written behind the store's back afterwards is
	// not collapsed by the next open.
	writePreIrsry44Store(t, dir, stale)
	store, err = openEventStore(t.Context(), dir, nil)
	require.NoError(t, err)
	require.Contains(t, storeIDs(t, store, all), stale.ID, "a marked store is not repaired again")

	// Without the marker the repair runs again, idempotently.
	require.NoError(t, store.backend().DB.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(sidecarMetaBucket).Delete(coordinateRepairMarker.Key)
	}))
	require.NoError(t, store.Close())
	store = openTestEventStore(t, dir)
	requireRepaired(store)
}

// TestSidecarLongAndEmptyTagFiltersOverWebsocket: a client's REQ and COUNT
// on a relay config coordinate (#a, 108 bytes) and on an empty #d get the
// stored events, over a real websocket (EOSE- and COUNT-driven).
func TestSidecarLongAndEmptyTagFiltersOverWebsocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	_, relayURL := startSidecarForFanoutTest(t)
	client := dialRawRelay(t, ctx, relayURL)
	f := newTagFilterFixture(t)
	for _, event := range f.events() {
		client.publish(event)
	}
	for name, tc := range f.cases() {
		require.ElementsMatch(t, tc.want, client.storedIDs("req", tc.filter), "REQ %s", name)
		if tc.filter.Limit == 0 {
			require.Equal(t, uint32(len(tc.want)), client.count("count", tc.filter), "COUNT %s", name)
		}
	}
}

// count sends a NIP-45 COUNT and returns the relay's answer.
func (c *rawRelayClient) count(subID string, filter nostr.Filter) uint32 {
	c.t.Helper()
	c.send("COUNT", subID, filter)
	for {
		frame := c.next()
		switch {
		case frame.label == "COUNT" && frame.subID == subID:
			return frame.count
		case frame.label == "CLOSED" && frame.subID == subID:
			c.t.Fatalf("COUNT %s refused: %s", subID, frame.reason)
		}
	}
}
