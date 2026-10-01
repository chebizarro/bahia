package boltcoord

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/eventstore/boltdb"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

var (
	testTagBucket      = []byte("testTags")
	testDeletionBucket = []byte("testDeletions")
	testMetaBucket     = []byte("testMeta")
)

// openTestBackend opens a bolt eventstore with the buckets this package's
// types keep next to it.
func openTestBackend(t *testing.T) *boltdb.BoltBackend {
	t.Helper()
	backend := &boltdb.BoltBackend{Path: filepath.Join(t.TempDir(), "events.bolt")}
	require.NoError(t, backend.Init())
	t.Cleanup(backend.Close)
	backend.DB.NoSync = true // a test store needs no durability, and fsyncs dominate its run time
	require.NoError(t, backend.DB.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{testTagBucket, testDeletionBucket, testMetaBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	}))
	return backend
}

// plainScan reads the eventstore directly (the test stores are small, so one
// bounded query stands in for the stores' paging) and copies the result out of
// its read transaction before yielding, as Scan requires.
func plainScan(backend *boltdb.BoltBackend) Scan {
	return func(filter nostr.Filter, limit int) iter.Seq[nostr.Event] {
		return func(yield func(nostr.Event) bool) {
			for _, event := range slices.Collect(backend.QueryEvents(filter, min(limit, 5000))) {
				if !yield(event) {
					return
				}
			}
		}
	}
}

// routedScan is the Scan a store hands to boltcoord: tag filters the
// eventstore cannot answer go through Store.Query.
func routedScan(st Store, backend *boltdb.BoltBackend) Scan {
	plain := plainScan(backend)
	return func(filter nostr.Filter, limit int) iter.Seq[nostr.Event] {
		if NeedsTagIndex(filter) {
			return st.Query(plain, filter, limit)
		}
		return plain(filter, limit)
	}
}

func signed(t *testing.T, sk nostr.SecretKey, kind nostr.Kind, createdAt nostr.Timestamp, tags nostr.Tags, content string) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: kind, CreatedAt: createdAt, Tags: tags, Content: content}
	require.NoError(t, event.Sign(sk))
	return event
}

func idsOf(events iter.Seq[nostr.Event]) []nostr.ID {
	var ids []nostr.ID
	for event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}

func held(backend *boltdb.BoltBackend, id nostr.ID) bool {
	for event := range backend.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1) {
		return event.ID == id
	}
	return false
}

// dCases are coordinates on both sides of the eventstore's tag index limit.
type dCase struct {
	name string
	kind nostr.Kind
	d    string
	tags nostr.Tags
}

func dCases() []dCase {
	longCoordinate := strings.Repeat("c", 40) // d indexed, coordinate over 100 bytes
	longD := strings.Repeat("d", 120)
	return []dCase{
		{name: "short d", kind: 30078, d: "app", tags: nostr.Tags{{"d", "app"}}},
		{name: "coordinate over 100 bytes", kind: 30078, d: longCoordinate, tags: nostr.Tags{{"d", longCoordinate}}},
		{name: "d over 100 bytes", kind: 30078, d: longD, tags: nostr.Tags{{"d", longD}}},
		{name: "empty d", kind: 30078, d: "", tags: nostr.Tags{{"d", ""}}},
		{name: "missing d", kind: 30078, d: "", tags: nil},
		{name: "plain replaceable", kind: 10002, d: "", tags: nil},
	}
}

func TestAddressRoundTripsAndCoordinatesIgnoreDForReplaceableKinds(t *testing.T) {
	author := nostr.Generate().Public()
	for _, d := range []string{"", "app", "with:colons", strings.Repeat("x", 300)} {
		kind, parsedAuthor, parsedD, ok := ParseAddress(Address(30078, author, d))
		require.True(t, ok, "d %q", d)
		require.Equal(t, nostr.Kind(30078), kind)
		require.Equal(t, author, parsedAuthor)
		require.Equal(t, d, parsedD)
	}
	for _, malformed := range []string{"", "30078", "30078:" + author.Hex(), "x:" + author.Hex() + ":d", "70000:" + author.Hex() + ":d", "30078:nothex:d"} {
		_, _, _, ok := ParseAddress(malformed)
		require.False(t, ok, "%q", malformed)
	}

	require.Equal(t, Coordinate{Kind: 10002, Author: author}, NewCoordinate(10002, author, "stray"))
	require.Equal(t, Coordinate{Kind: 30078, Author: author, D: "x"}, NewCoordinate(30078, author, "x"))
	event := nostr.Event{Kind: 30078, PubKey: author, Tags: nostr.Tags{{"d", "x"}}}
	require.Equal(t, "30078:"+author.Hex()+":x", CoordinateOf(event).String())
}

func TestDeletedCoordinatesKeepOnlyTheRequestersStateCoordinates(t *testing.T) {
	alice := nostr.Generate()
	mallory := nostr.Generate().Public()
	request := signed(t, alice, nostr.KindDeletion, 100, nostr.Tags{
		{"a", Address(30078, alice.Public(), "app")},
		{"a", Address(30078, alice.Public(), "app")},   // duplicate
		{"a", Address(10002, alice.Public(), "stray")}, // plain replaceable: d dropped
		{"a", Address(30078, mallory, "app")},          // another author
		{"a", Address(1, alice.Public(), "")},          // a regular kind has no coordinate
		{"a", "malformed"},
		{"a"},
		{"e", nostr.Generate().Public().Hex()},
	}, "")
	require.Equal(t, []Coordinate{
		{Kind: 30078, Author: alice.Public(), D: "app"},
		{Kind: 10002, Author: alice.Public()},
	}, DeletedCoordinates(request))
}

func TestReplaceKeepsOnlyTheLatestVersionWhateverTheD(t *testing.T) {
	for _, tc := range dCases() {
		t.Run(tc.name, func(t *testing.T) {
			backend := openTestBackend(t)
			st := NewStore(backend, testTagBucket)
			scan := routedScan(st, backend)
			sk := nostr.Generate()
			c := NewCoordinate(tc.kind, sk.Public(), tc.d)
			v1 := signed(t, sk, tc.kind, 100, tc.tags, "v1")
			v2 := signed(t, sk, tc.kind, 200, tc.tags, "v2")

			stored, err := st.Replace(scan, v1)
			require.NoError(t, err)
			require.True(t, stored)
			stored, err = st.Replace(scan, v2)
			require.NoError(t, err)
			require.True(t, stored)
			require.Equal(t, []nostr.ID{v2.ID}, idsOf(Versions(scan, c, 0, unbounded)))
			require.False(t, held(backend, v1.ID))

			for _, stale := range []nostr.Event{v1, v2} {
				stored, err = st.Replace(scan, stale)
				require.NoError(t, err)
				require.False(t, stored, "%s is not newer than what is held", stale.Content)
			}

			// A created_at tie goes to the lower id.
			a := signed(t, sk, tc.kind, 300, tc.tags, "tie a")
			b := signed(t, sk, tc.kind, 300, tc.tags, "tie b")
			low, high := a, b
			if string(b.ID[:]) < string(a.ID[:]) {
				low, high = b, a
			}
			stored, err = st.Replace(scan, high)
			require.NoError(t, err)
			require.True(t, stored)
			stored, err = st.Replace(scan, low)
			require.NoError(t, err)
			require.True(t, stored)
			stored, err = st.Replace(scan, high)
			require.NoError(t, err)
			require.False(t, stored)
			latest, ok := LatestVersion(scan, c)
			require.True(t, ok)
			require.Equal(t, low.ID, latest.ID)
		})
	}
}

func TestVersionsHonoursUntilAndLimitForUnindexedD(t *testing.T) {
	backend := openTestBackend(t)
	sk := nostr.Generate()
	longD := strings.Repeat("d", 120)
	var versions []nostr.Event
	for i := range 4 {
		event := signed(t, sk, 30078, nostr.Timestamp(100+i*10), nostr.Tags{{"d", longD}}, fmt.Sprint(i))
		require.NoError(t, backend.SaveEvent(event)) // several versions, as an old store held them
		versions = append(versions, event)
	}
	// Another d of the same author and kind is not a version.
	require.NoError(t, backend.SaveEvent(signed(t, sk, 30078, 500, nostr.Tags{{"d", longD + "x"}}, "other")))
	c := NewCoordinate(30078, sk.Public(), longD)
	scan := plainScan(backend)
	require.Equal(t, []nostr.ID{versions[3].ID, versions[2].ID, versions[1].ID, versions[0].ID}, idsOf(Versions(scan, c, 0, unbounded)))
	require.Equal(t, []nostr.ID{versions[1].ID, versions[0].ID}, idsOf(Versions(scan, c, 115, unbounded)))
	require.Equal(t, []nostr.ID{versions[3].ID}, idsOf(Versions(scan, c, 0, 1)))
}

func TestDeletionIndexAnswersCoordinatesOfAnyLength(t *testing.T) {
	backend := openTestBackend(t)
	ix := NewDeletionIndex(backend.DB, testDeletionBucket)
	alice := nostr.Generate()
	for _, tc := range dCases() {
		c := NewCoordinate(tc.kind, alice.Public(), tc.d)
		request := signed(t, alice, nostr.KindDeletion, 100, nostr.Tags{{"a", c.String()}}, tc.name)
		require.NoError(t, ix.Index(request))
		for since, want := range map[nostr.Timestamp]bool{0: true, 99: true, 100: true, 101: false} {
			deleted, err := ix.Deleted(c, since)
			require.NoError(t, err)
			require.Equal(t, want, deleted, "%s since %d", tc.name, since)
		}
		require.NoError(t, ix.Unindex(request))
		deleted, err := ix.Deleted(c, 0)
		require.NoError(t, err)
		require.False(t, deleted, "%s: unindexed", tc.name)
	}

	// A later request for the same coordinate extends the deletion; removing
	// it leaves the earlier one in force.
	c := NewCoordinate(30078, alice.Public(), "app")
	early := signed(t, alice, nostr.KindDeletion, 100, nostr.Tags{{"a", c.String()}}, "early")
	late := signed(t, alice, nostr.KindDeletion, 200, nostr.Tags{{"a", c.String()}}, "late")
	require.NoError(t, ix.Index(early))
	require.NoError(t, ix.Index(late))
	deleted, err := ix.Deleted(c, 150)
	require.NoError(t, err)
	require.True(t, deleted)
	require.NoError(t, ix.Unindex(late))
	deleted, err = ix.Deleted(c, 150)
	require.NoError(t, err)
	require.False(t, deleted)
	deleted, err = ix.Deleted(c, 100)
	require.NoError(t, err)
	require.True(t, deleted)

	// Another author's coordinate is never indexed.
	foreign := signed(t, nostr.Generate(), nostr.KindDeletion, 300, nostr.Tags{{"a", NewCoordinate(30078, alice.Public(), "x").String()}}, "")
	require.NoError(t, ix.Index(foreign))
	deleted, err = ix.Deleted(NewCoordinate(30078, alice.Public(), "x"), 0)
	require.NoError(t, err)
	require.False(t, deleted)
}

func TestDeletionIndexBackfillCommitsTheMarkerWithTheLastBatch(t *testing.T) {
	backend := openTestBackend(t)
	ix := NewDeletionIndex(backend.DB, testDeletionBucket)
	marker := Marker{Bucket: testMetaBucket, Key: []byte("deletions"), Version: "1"}
	alice := nostr.Generate()
	var requests []nostr.Event
	var coordinates []Coordinate
	for i := range 5 {
		c := NewCoordinate(30078, alice.Public(), strings.Repeat("x", 100+i))
		coordinates = append(coordinates, c)
		requests = append(requests, signed(t, alice, nostr.KindDeletion, 100, nostr.Tags{{"a", c.String()}}, ""))
	}
	done, err := marker.Done(backend.DB)
	require.NoError(t, err)
	require.False(t, done)
	require.NoError(t, ix.Backfill(t.Context(), slices.Values(requests), 2, marker.Set))
	done, err = marker.Done(backend.DB)
	require.NoError(t, err)
	require.True(t, done)
	for _, c := range coordinates {
		deleted, err := ix.Deleted(c, 100)
		require.NoError(t, err)
		require.True(t, deleted)
	}
	// Indexing twice is harmless.
	require.NoError(t, ix.Backfill(t.Context(), slices.Values(requests), 2, nil))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, ix.Backfill(ctx, slices.Values(requests), 2, nil), context.Canceled)
}

func TestMarkerRecordsOneVersion(t *testing.T) {
	backend := openTestBackend(t)
	v1 := Marker{Bucket: testMetaBucket, Key: []byte("step"), Version: "1"}
	v2 := Marker{Bucket: testMetaBucket, Key: []byte("step"), Version: "2"}
	require.NoError(t, backend.DB.Update(v1.Set))
	done, err := v1.Done(backend.DB)
	require.NoError(t, err)
	require.True(t, done)
	done, err = v2.Done(backend.DB)
	require.NoError(t, err)
	require.False(t, done, "a newer version of the step has not run")
}

// tagQueryFixture stores events carrying tag values on both sides of the
// eventstore's limit.
type tagQueryFixture struct {
	backend  *boltdb.BoltBackend
	st       Store
	scan     Scan
	alice    nostr.SecretKey
	bob      nostr.SecretKey
	config   string // a 108-byte relay config coordinate
	longT    string
	byAlice  []nostr.Event // #a config, kind 1, created 100..104
	byBob    nostr.Event   // #a config, kind 1, created 110
	emptyD   nostr.Event   // addressable with an empty d
	longTagT nostr.Event   // #t long and #t short
	shortT   nostr.Event   // #t short only
}

func newTagQueryFixture(t *testing.T) tagQueryFixture {
	t.Helper()
	backend := openTestBackend(t)
	st := NewStore(backend, testTagBucket)
	f := tagQueryFixture{backend: backend, st: st, scan: routedScan(st, backend), alice: nostr.Generate(), bob: nostr.Generate()}
	f.config = Address(30078, f.alice.Public(), strings.Repeat("c", 108-len("30078:")-64-len(":")))
	require.Len(t, f.config, 108)
	f.longT = strings.Repeat("t", 150)
	for i := range 5 {
		event := signed(t, f.alice, 1, nostr.Timestamp(100+i), nostr.Tags{{"a", f.config}}, fmt.Sprint(i))
		require.NoError(t, st.Save(event))
		f.byAlice = append(f.byAlice, event)
	}
	f.byBob = signed(t, f.bob, 1, 110, nostr.Tags{{"a", f.config}, {"p", f.alice.Public().Hex()}}, "bob")
	require.NoError(t, st.Save(f.byBob))
	f.emptyD = signed(t, f.alice, 30078, 120, nostr.Tags{{"d", ""}}, "empty d")
	stored, err := st.Replace(f.scan, f.emptyD)
	require.NoError(t, err)
	require.True(t, stored)
	f.longTagT = signed(t, f.alice, 1, 130, nostr.Tags{{"t", f.longT}, {"t", "short"}, {"t", f.longT}}, "long and short t")
	require.NoError(t, st.Save(f.longTagT))
	f.shortT = signed(t, f.bob, 1, 140, nostr.Tags{{"t", "short"}}, "short t")
	require.NoError(t, st.Save(f.shortT))
	return f
}

func TestStoreQueryMatchesEmptyAndLongTagValues(t *testing.T) {
	f := newTagQueryFixture(t)
	alicesNewestFirst := []nostr.ID{f.byAlice[4].ID, f.byAlice[3].ID, f.byAlice[2].ID, f.byAlice[1].ID, f.byAlice[0].ID}

	byConfig := nostr.Filter{Tags: nostr.TagMap{"a": []string{f.config}}}
	require.True(t, NeedsTagIndex(byConfig))
	require.Empty(t, idsOf(plainScan(f.backend)(byConfig, 100)), "precondition: the eventstore alone matches nothing")
	require.Equal(t, append([]nostr.ID{f.byBob.ID}, alicesNewestFirst...), idsOf(f.st.Query(plainScan(f.backend), byConfig, 0)))

	for name, tc := range map[string]struct {
		filter nostr.Filter
		limit  int
		want   []nostr.ID
	}{
		"limit":           {filter: byConfig, limit: 2, want: []nostr.ID{f.byBob.ID, f.byAlice[4].ID}},
		"authors":         {filter: nostr.Filter{Authors: []nostr.PubKey{f.alice.Public()}, Tags: byConfig.Tags}, want: alicesNewestFirst},
		"kinds":           {filter: nostr.Filter{Kinds: []nostr.Kind{7}, Tags: byConfig.Tags}},
		"since and until": {filter: nostr.Filter{Since: 101, Until: 103, Tags: byConfig.Tags}, want: []nostr.ID{f.byAlice[3].ID, f.byAlice[2].ID, f.byAlice[1].ID}},
		"another tag":     {filter: nostr.Filter{Tags: nostr.TagMap{"a": {f.config}, "p": {f.alice.Public().Hex()}}}, want: []nostr.ID{f.byBob.ID}},
		"empty d":         {filter: nostr.Filter{Kinds: []nostr.Kind{30078}, Tags: nostr.TagMap{"d": {""}}}, want: []nostr.ID{f.emptyD.ID}},
		"long t once":     {filter: nostr.Filter{Tags: nostr.TagMap{"t": {f.longT}}}, want: []nostr.ID{f.longTagT.ID}},
		// Long and short values of one tag are merged newest first, and an
		// event matching both is yielded once.
		"long and short values": {filter: nostr.Filter{Tags: nostr.TagMap{"t": {"short", f.longT}}}, want: []nostr.ID{f.shortT.ID, f.longTagT.ID}},
		"two long tags":         {filter: nostr.Filter{Tags: nostr.TagMap{"a": {f.config}, "t": {f.longT}}}},
		"no such value":         {filter: nostr.Filter{Tags: nostr.TagMap{"a": {f.config + "x"}}}},
		"limit zero":            {filter: nostr.Filter{LimitZero: true, Tags: byConfig.Tags}},
	} {
		require.Equal(t, tc.want, idsOf(f.st.Query(plainScan(f.backend), tc.filter, tc.limit)), name)
	}

	// Filters the eventstore answers are passed straight through.
	short := nostr.Filter{Tags: nostr.TagMap{"t": {"short"}}}
	require.False(t, NeedsTagIndex(short))
	require.Equal(t, []nostr.ID{f.shortT.ID, f.longTagT.ID}, idsOf(f.st.Query(plainScan(f.backend), short, 0)))
	require.False(t, NeedsTagIndex(nostr.Filter{Tags: nostr.TagMap{"long": {f.longT}}}), "multi-letter tags are not filterable")
}

func TestStoreKeepsTheTagIndexInStepWithDeletes(t *testing.T) {
	f := newTagQueryFixture(t)
	byConfig := nostr.Filter{Tags: nostr.TagMap{"a": {f.config}}}
	query := func(filter nostr.Filter) []nostr.ID { return idsOf(f.st.Query(plainScan(f.backend), filter, 0)) }

	// Delete removes the event and its entries.
	require.NoError(t, f.st.Delete(f.byBob))
	require.NotContains(t, query(byConfig), f.byBob.ID)
	require.NoError(t, f.backend.DB.View(func(tx *bbolt.Tx) error {
		for _, key := range tagKeys(f.byBob) {
			require.Nil(t, tx.Bucket(testTagBucket).Get(key))
		}
		return nil
	}))

	// A replaced version leaves the index, whether ReplaceEvent or Replace
	// itself removes it.
	for _, d := range []string{"", strings.Repeat("d", 120), "short"} {
		v1 := signed(t, f.alice, 30079, 100, nostr.Tags{{"d", d}, {"a", f.config}}, "v1")
		v2 := signed(t, f.alice, 30079, 200, nostr.Tags{{"d", d}, {"a", f.config}}, "v2")
		for _, v := range []nostr.Event{v1, v2} {
			stored, err := f.st.Replace(f.scan, v)
			require.NoError(t, err)
			require.True(t, stored)
		}
		filter := nostr.Filter{Kinds: []nostr.Kind{30079}, Tags: byConfig.Tags}
		require.Contains(t, query(filter), v2.ID, "d %q", d)
		require.NotContains(t, query(filter), v1.ID, "d %q", d)
		require.NoError(t, f.backend.DB.View(func(tx *bbolt.Tx) error {
			for _, key := range tagKeys(v1) {
				require.Nil(t, tx.Bucket(testTagBucket).Get(key), "d %q", d)
			}
			return nil
		}))
		require.NoError(t, f.st.Delete(v2))
	}

	// An entry whose event never made it into the store is skipped.
	ghost := signed(t, f.alice, 1, 150, nostr.Tags{{"a", f.config}}, "ghost")
	require.NoError(t, f.st.tags.index(ghost))
	require.NotContains(t, query(byConfig), ghost.ID)

	// Saving a held event again keeps its entries.
	require.ErrorIs(t, f.st.Save(f.byAlice[0]), eventstore.ErrDupEvent)
	require.Contains(t, query(byConfig), f.byAlice[0].ID)
}

func TestStoreQueryPagesThroughTheTagIndex(t *testing.T) {
	backend := openTestBackend(t)
	st := NewStore(backend, testTagBucket)
	sk := nostr.Generate()
	value := strings.Repeat("v", 101)
	total := 2*tagPageSize + 3
	var want []nostr.ID
	for i := range total {
		// Pairs share a created_at, so pages also split ties.
		// Unsigned (the store does not verify), to keep the test fast.
		event := nostr.Event{PubKey: sk.Public(), Kind: 1, CreatedAt: nostr.Timestamp(1000 + i/2), Tags: nostr.Tags{{"r", value}}, Content: fmt.Sprint(i)}
		event.ID = event.GetID()
		require.NoError(t, st.Save(event))
		want = append(want, event.ID)
	}
	got := idsOf(st.Query(plainScan(backend), nostr.Filter{Tags: nostr.TagMap{"r": {value}}}, 0))
	require.ElementsMatch(t, want, got)
	require.Len(t, got, total)
	events := slices.Collect(st.Query(plainScan(backend), nostr.Filter{Tags: nostr.TagMap{"r": {value}}}, 0))
	require.True(t, slices.IsSortedFunc(events, func(a, b nostr.Event) int { return int(b.CreatedAt - a.CreatedAt) }), "newest first")
}

// writeUnindexed stores events the way stores did before boltcoord: straight
// through the eventstore, ReplaceEvent for state, no tag or deletion index.
func writeUnindexed(t *testing.T, backend *boltdb.BoltBackend, events ...nostr.Event) {
	t.Helper()
	for _, event := range events {
		if event.Kind.IsReplaceable() || event.Kind.IsAddressable() {
			_, err := backend.ReplaceEvent(event)
			require.NoError(t, err)
			continue
		}
		require.NoError(t, backend.SaveEvent(event))
	}
}

func TestBuildTagIndexIndexesAnExistingStoreOnce(t *testing.T) {
	f := newTagQueryFixture(t)
	marker := Marker{Bucket: testMetaBucket, Key: []byte("tags"), Version: "1"}
	old := signed(t, f.alice, 1, 160, nostr.Tags{{"a", f.config}}, "written before the index")
	writeUnindexed(t, f.backend, old)
	byConfig := nostr.Filter{Tags: nostr.TagMap{"a": {f.config}}}
	require.NotContains(t, idsOf(f.st.Query(plainScan(f.backend), byConfig, 0)), old.ID, "precondition")

	require.NoError(t, f.st.BuildTagIndex(t.Context(), plainScan(f.backend), marker, 2))
	require.Equal(t, old.ID, idsOf(f.st.Query(plainScan(f.backend), byConfig, 1))[0])
	done, err := marker.Done(f.backend.DB)
	require.NoError(t, err)
	require.True(t, done)

	// Marked: a later build reads nothing.
	later := signed(t, f.alice, 1, 170, nostr.Tags{{"a", f.config}}, "behind the store's back")
	writeUnindexed(t, f.backend, later)
	require.NoError(t, f.st.BuildTagIndex(t.Context(), plainScan(f.backend), marker, 2))
	require.NotContains(t, idsOf(f.st.Query(plainScan(f.backend), byConfig, 0)), later.ID)
}

func TestRepairAppliesStoredDeletionsAndCollapsesUnindexedCoordinatesOnce(t *testing.T) {
	backend := openTestBackend(t)
	st := NewStore(backend, testTagBucket)
	scan := routedScan(st, backend)
	marker := Marker{Bucket: testMetaBucket, Key: []byte("repair"), Version: "1"}
	sk := nostr.Generate()
	longD := strings.Repeat("d", 120)
	longCoordinate := strings.Repeat("c", 40)
	stale := signed(t, sk, 30078, 100, nostr.Tags{{"d", longD}}, "stale")
	current := signed(t, sk, 30078, 200, nostr.Tags{{"d", longD}}, "current")
	emptyStale := signed(t, sk, 30078, 100, nostr.Tags{{"d", ""}}, "empty stale")
	emptyCurrent := signed(t, sk, 30078, 200, nil, "missing d current")
	deletedState := signed(t, sk, 30078, 100, nostr.Tags{{"d", longCoordinate}}, "deleted state")
	note := signed(t, sk, 1, 100, nil, "deleted note")
	foreignNote := signed(t, nostr.Generate(), 1, 100, nil, "another author's note")
	request := signed(t, sk, nostr.KindDeletion, 150, nostr.Tags{
		{"a", Address(30078, sk.Public(), longCoordinate)},
		{"e", note.ID.Hex()},
		{"e", foreignNote.ID.Hex()},
	}, "")
	writeUnindexed(t, backend, stale, current, emptyStale, emptyCurrent, deletedState, note, foreignNote, request)
	require.NoError(t, st.BuildTagIndex(t.Context(), plainScan(backend), Marker{Bucket: testMetaBucket, Key: []byte("tags"), Version: "1"}, 100))

	require.NoError(t, st.Repair(t.Context(), scan, marker))
	for _, event := range []nostr.Event{stale, emptyStale, deletedState, note} {
		require.False(t, held(backend, event.ID), "%q is gone", event.Content)
	}
	for _, event := range []nostr.Event{current, emptyCurrent, foreignNote, request} {
		require.True(t, held(backend, event.ID), "%q is kept", event.Content)
	}
	require.Empty(t, idsOf(st.Query(plainScan(backend), nostr.Filter{Tags: nostr.TagMap{"d": {longD}}, Until: 150}, 0)), "the collapsed version left the tag index")

	// Marked: a stale version written afterwards is not collapsed.
	writeUnindexed(t, backend, stale)
	require.NoError(t, st.Repair(t.Context(), scan, marker))
	require.True(t, held(backend, stale.ID))

	// Unmarked, the repair runs again and converges on the same store.
	require.NoError(t, backend.DB.Update(func(tx *bbolt.Tx) error { return tx.Bucket(testMetaBucket).Delete(marker.Key) }))
	require.NoError(t, st.Repair(t.Context(), scan, marker))
	require.False(t, held(backend, stale.ID))
	require.True(t, held(backend, current.ID))
}

func TestApplyDeletionCountsEachTargetOnce(t *testing.T) {
	backend := openTestBackend(t)
	st := NewStore(backend, testTagBucket)
	scan := routedScan(st, backend)
	sk := nostr.Generate()
	state := signed(t, sk, 30078, 100, nostr.Tags{{"d", ""}}, "state")
	stored, err := st.Replace(scan, state)
	require.NoError(t, err)
	require.True(t, stored)
	request := signed(t, sk, nostr.KindDeletion, 200, nostr.Tags{{"a", Address(30078, sk.Public(), "")}, {"e", state.ID.Hex()}}, "")
	removed, err := st.ApplyDeletion(t.Context(), scan, request)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.False(t, held(backend, state.ID))
	removed, err = st.ApplyDeletion(t.Context(), scan, request)
	require.NoError(t, err)
	require.Zero(t, removed, "applying twice is harmless")
}
