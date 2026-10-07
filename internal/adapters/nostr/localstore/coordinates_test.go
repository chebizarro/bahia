package localstore

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/boltdb"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

// The bolt eventstore indexes no tag value that is empty or longer than 100
// bytes, so its #d and #a lookups cannot find such
// coordinates. These cases cover both sides of that limit: a d the index
// holds, a d it holds inside a coordinate it does not (any d of 30 bytes or
// more), a d it does not hold, and the empty d of a missing or empty tag and
// of a plain replaceable kind.
type coordinateCase struct {
	name string
	kind nostr.Kind
	d    string
	tags nostr.Tags
}

func coordinateCases() []coordinateCase {
	longCoordinate := strings.Repeat("c", 40)
	longD := strings.Repeat("d", 120)
	return []coordinateCase{
		{name: "short d", kind: 30078, d: "app", tags: nostr.Tags{{"d", "app"}}},
		{name: "coordinate over 100 bytes", kind: 30078, d: longCoordinate, tags: nostr.Tags{{"d", longCoordinate}}},
		{name: "d over 100 bytes", kind: 30078, d: longD, tags: nostr.Tags{{"d", longD}}},
		{name: "empty d", kind: 30078, d: "", tags: nostr.Tags{{"d", ""}}},
		{name: "missing d", kind: 30078, d: "", tags: nil},
		{name: "plain replaceable", kind: 10002, d: "", tags: nil},
	}
}

func (c coordinateCase) address(author nostr.PubKey) string {
	return fmt.Sprintf("%d:%s:%s", c.kind, author.Hex(), c.d)
}

func storedIDs(store *Store, filter nostr.Filter) []nostr.ID {
	return ids(slices.Collect(store.QueryEvents(filter)))
}

func requireSaved(t *testing.T, store *Store, ev nostr.Event, fresh bool, msg string) {
	t.Helper()
	stored, err := store.SaveEvent(ev)
	require.NoError(t, err)
	require.Equal(t, fresh, stored, msg)
}

func TestSaveEventKeepsOnlyTheLatestVersionWhateverTheD(t *testing.T) {
	for _, tc := range coordinateCases() {
		t.Run(tc.name, func(t *testing.T) {
			sk := nostr.Generate()
			v1 := signed(t, sk, tc.kind, 100, tc.tags, "v1")
			v2 := signed(t, sk, tc.kind, 200, tc.tags, "v2")
			v3 := signed(t, sk, tc.kind, 300, tc.tags, "v3")
			filter := nostr.Filter{Kinds: []nostr.Kind{tc.kind}, Authors: []nostr.PubKey{sk.Public()}}

			t.Run("newer first", func(t *testing.T) {
				store, _ := openTemp(t)
				requireSaved(t, store, v2, true, "the first version is new")
				requireSaved(t, store, v1, false, "an older version of a held coordinate is not new")
				require.Equal(t, []nostr.ID{v2.ID}, storedIDs(store, filter))
			})
			t.Run("older first", func(t *testing.T) {
				store, _ := openTemp(t)
				requireSaved(t, store, v1, true, "the first version is new")
				requireSaved(t, store, v2, true, "a newer version is new")
				requireSaved(t, store, v3, true, "a newer version is new")
				require.Equal(t, []nostr.ID{v3.ID}, storedIDs(store, filter), "a newer version replaces every older one")
			})
			t.Run("equal created_at keeps the lower id", func(t *testing.T) {
				store, _ := openTemp(t)
				a := signed(t, sk, tc.kind, 400, tc.tags, "a")
				b := signed(t, sk, tc.kind, 400, tc.tags, "b")
				low, high := a, b
				if b.ID.Hex() < a.ID.Hex() {
					low, high = b, a
				}
				requireSaved(t, store, high, true, "the first version is new")
				requireSaved(t, store, low, true, "the lower id wins a created_at tie")
				requireSaved(t, store, high, false, "the higher id loses a created_at tie")
				require.Equal(t, []nostr.ID{low.ID}, storedIDs(store, filter))
			})
			if tc.kind.IsAddressable() {
				t.Run("other coordinates are kept", func(t *testing.T) {
					store, _ := openTemp(t)
					other := signed(t, sk, tc.kind, 50, nostr.Tags{{"d", "other-" + tc.d}}, "other")
					requireSaved(t, store, other, true, "")
					requireSaved(t, store, v1, true, "")
					requireSaved(t, store, v2, true, "")
					require.ElementsMatch(t, []nostr.ID{v2.ID, other.ID}, storedIDs(store, filter))
				})
			}
		})
	}
}

func TestSaveEventAppliesNIP09ToCoordinatesOfAnyLength(t *testing.T) {
	for _, tc := range coordinateCases() {
		t.Run(tc.name, func(t *testing.T) {
			sk := nostr.Generate()
			target := signed(t, sk, tc.kind, 100, tc.tags, "state")
			request := signed(t, sk, nostr.KindDeletion, 150, nostr.Tags{{"a", tc.address(sk.Public())}}, "")
			recreated := signed(t, sk, tc.kind, 200, tc.tags, "recreated")
			filter := nostr.Filter{Kinds: []nostr.Kind{tc.kind}, Authors: []nostr.PubKey{sk.Public()}}

			t.Run("request after the event", func(t *testing.T) {
				store, _ := openTemp(t)
				requireSaved(t, store, target, true, "")
				requireSaved(t, store, request, true, "a deletion request is new")
				require.Empty(t, storedIDs(store, filter), "the request deletes the version it names")
				require.True(t, store.hasLocked(request.ID), "the request is kept as the tombstone")
				requireSaved(t, store, target, false, "a deleted event is not new again")
				require.Empty(t, storedIDs(store, filter))
				requireSaved(t, store, recreated, true, "a version newer than the request is live")
				require.Equal(t, []nostr.ID{recreated.ID}, storedIDs(store, filter))
			})
			t.Run("request before the event", func(t *testing.T) {
				store, _ := openTemp(t)
				requireSaved(t, store, request, true, "")
				requireSaved(t, store, target, false, "an event already deleted is not new")
				require.Empty(t, storedIDs(store, filter))
				requireSaved(t, store, recreated, true, "a version newer than the request is live")
				require.Equal(t, []nostr.ID{recreated.ID}, storedIDs(store, filter))
			})
		})
	}
}

func TestSaveEventAppliesNIP09OnlyToTheRequestersOwnEvents(t *testing.T) {
	alice, mallory := nostr.Generate(), nostr.Generate()
	longD := strings.Repeat("d", 120)
	state := signed(t, alice, 30078, 100, nostr.Tags{{"d", longD}}, "state")
	note := signed(t, alice, 1, 100, nil, "note")
	other := signed(t, alice, 1, 101, nil, "other note")
	address := fmt.Sprintf("30078:%s:%s", alice.Public().Hex(), longD)

	store, _ := openTemp(t)
	for _, ev := range []nostr.Event{state, note, other} {
		requireSaved(t, store, ev, true, "")
	}
	forged := signed(t, mallory, nostr.KindDeletion, 150, nostr.Tags{{"a", address}, {"e", note.ID.Hex()}}, "")
	requireSaved(t, store, forged, true, "")
	require.True(t, store.hasLocked(state.ID), "another author's request deletes nothing")
	require.True(t, store.hasLocked(note.ID), "another author's request deletes nothing")

	byID := signed(t, alice, nostr.KindDeletion, 150, nostr.Tags{{"e", note.ID.Hex()}}, "")
	requireSaved(t, store, byID, true, "")
	require.False(t, store.hasLocked(note.ID), "an e reference deletes the author's event")
	require.True(t, store.hasLocked(other.ID))
	requireSaved(t, store, note, false, "an event deleted by id is not new again")
	require.True(t, store.hasLocked(state.ID))
}

// writeAsBeforeTheIndex stores events the way the store did before
// straight through the eventstore, with ReplaceEvent for
// state (which keeps every version of a d it does not index) and no NIP-09.
func writeAsBeforeTheIndex(t *testing.T, path string, events ...nostr.Event) {
	t.Helper()
	backend := &boltdb.BoltBackend{Path: path}
	require.NoError(t, backend.Init())
	for _, ev := range events {
		if ev.Kind.IsReplaceable() || ev.Kind.IsAddressable() {
			_, err := backend.ReplaceEvent(ev)
			require.NoError(t, err)
			continue
		}
		require.NoError(t, backend.SaveEvent(ev))
	}
	backend.Close()
}

func TestOpenRepairsStoresWrittenBeforeTheDeletionIndexOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.bolt")
	sk := nostr.Generate()
	longD := strings.Repeat("d", 120)
	longCoordinate := strings.Repeat("c", 40)
	stale := signed(t, sk, 30078, 100, nostr.Tags{{"d", longD}}, "stale")
	current := signed(t, sk, 30078, 200, nostr.Tags{{"d", longD}}, "current")
	emptyStale := signed(t, sk, 30078, 100, nostr.Tags{{"d", ""}}, "stale")
	emptyCurrent := signed(t, sk, 30078, 200, nil, "current")
	deletedState := signed(t, sk, 30078, 100, nostr.Tags{{"d", longCoordinate}}, "deleted")
	note := signed(t, sk, 1, 100, nil, "deleted note")
	request := signed(t, sk, nostr.KindDeletion, 150, nostr.Tags{
		{"a", fmt.Sprintf("30078:%s:%s", sk.Public().Hex(), longCoordinate)},
		{"e", note.ID.Hex()},
	}, "")
	writeAsBeforeTheIndex(t, path, stale, current, emptyStale, emptyCurrent, deletedState, note, request)

	store, err := Open(path)
	require.NoError(t, err)
	for _, ev := range []nostr.Event{stale, emptyStale, deletedState, note} {
		require.False(t, store.hasLocked(ev.ID), "%q is gone after the repair", ev.Content)
	}
	for _, ev := range []nostr.Event{current, emptyCurrent, request} {
		require.True(t, store.hasLocked(ev.ID), "%q is kept", ev.Content)
	}
	requireSaved(t, store, deletedState, false, "the stored request's coordinate is indexed")
	requireSaved(t, store, note, false, "the stored request's id reference still applies")
	requireSaved(t, store, stale, false, "")
	require.NoError(t, store.backend().DB.View(func(tx *bbolt.Tx) error {
		require.Equal(t, coordinateRepairMarker.Version, string(tx.Bucket(metaBucket).Get(coordinateRepairMarker.Key)))
		return nil
	}))
	require.NoError(t, store.Close())

	// The marker makes the repair run once: a stale version written behind
	// the store's back afterwards is not collapsed by the next open.
	writeAsBeforeTheIndex(t, path, stale)
	reopened, err := Open(path)
	require.NoError(t, err)
	defer reopened.Close()
	require.True(t, reopened.hasLocked(stale.ID), "a marked store is not repaired again")
	require.True(t, reopened.hasLocked(current.ID))
}

func TestOpenMarksNewStoresRepaired(t *testing.T) {
	store, _ := openTemp(t)
	done, err := coordinateRepairMarker.Done(store.backend().DB)
	require.NoError(t, err)
	require.True(t, done)
}
