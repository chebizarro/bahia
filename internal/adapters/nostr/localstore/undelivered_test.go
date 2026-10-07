package localstore

import (
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

// An abandoned event stays in the store and its coordinate is marked
// undelivered; the marker is cleared by a quorum-accepted publish of the
// event, moves forward when a newer version is abandoned, is not cleared by
// the acceptance of an older version, and is superseded by a newer version
// saved on the coordinate (docs/architecture/outbox-delivery.md).
func TestUndeliveredMarkerFollowsCoordinateDelivery(t *testing.T) {
	store, _ := openTemp(t)
	sk := nostr.Generate()
	now := time.Unix(1_700_000_000, 0).UTC()

	v1 := signed(t, sk, 30078, 100, nostr.Tags{{"d", "pay-1"}}, "v1")
	_, err := store.SaveEvent(v1)
	require.NoError(t, err)

	written, err := store.MarkUndelivered(v1, "abandoned: blocked", now)
	require.NoError(t, err)
	require.True(t, written)
	marker, found, err := store.Undelivered(v1)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, v1.ID, marker.EventID)
	require.Equal(t, "30078:"+sk.Public().Hex()+":pay-1", marker.Coordinate)
	require.Equal(t, "abandoned: blocked", marker.Detail)
	require.Equal(t, now, marker.FailedAt)
	flagged, err := store.UndeliveredEventIDs()
	require.NoError(t, err)
	require.Contains(t, flagged, v1.ID)
	held := 0
	for range store.QueryEvents(nostr.Filter{IDs: []nostr.ID{v1.ID}}) {
		held++
	}
	require.Equal(t, 1, held, "the abandoned event is kept")

	// Acceptance of the event itself clears the marker.
	removed, err := store.ClearUndelivered(v1)
	require.NoError(t, err)
	require.True(t, removed)
	_, found, err = store.Undelivered(v1)
	require.NoError(t, err)
	require.False(t, found)
	written, err = store.MarkUndelivered(v1, "abandoned: blocked", now)
	require.NoError(t, err)
	require.True(t, written)

	// A newer version saved on the coordinate supersedes the abandoned one:
	// its own delivery decides from here on.
	v2 := signed(t, sk, 30078, 200, nostr.Tags{{"d", "pay-1"}}, "v2")
	_, err = store.SaveEvent(v2)
	require.NoError(t, err)
	_, found, err = store.Undelivered(v2)
	require.NoError(t, err)
	require.False(t, found, "a newer version on the coordinate supersedes the marker")

	// Acceptance of the older version does not clear a marker on the newer.
	v2Marked, err := store.MarkUndelivered(v2, "abandoned again", now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, v2Marked, "a newer abandoned version moves the marker forward")
	removed, err = store.ClearUndelivered(v1)
	require.NoError(t, err)
	require.False(t, removed)
	// A late abandonment of the older version does not move it back.
	stale, err := store.MarkUndelivered(v1, "late", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, stale)
	marker, _, err = store.Undelivered(v2)
	require.NoError(t, err)
	require.Equal(t, v2.ID, marker.EventID)

	removed, err = store.ClearUndelivered(v2)
	require.NoError(t, err)
	require.True(t, removed)
	_, found, err = store.Undelivered(v2)
	require.NoError(t, err)
	require.False(t, found)
	markers, err := store.ListUndelivered()
	require.NoError(t, err)
	require.Empty(t, markers)
}

// Regular events are their own coordinate: the marker is keyed by id and
// cleared only by that event's acceptance.
func TestUndeliveredMarkerOnRegularEventIsKeyedByID(t *testing.T) {
	store, _ := openTemp(t)
	sk := nostr.Generate()
	a := signed(t, sk, 4903, 100, nil, "a")
	b := signed(t, sk, 4903, 200, nil, "b")
	_, err := store.MarkUndelivered(a, "abandoned", time.Now())
	require.NoError(t, err)
	removed, err := store.ClearUndelivered(b)
	require.NoError(t, err)
	require.False(t, removed)
	markers, err := store.ListUndelivered()
	require.NoError(t, err)
	require.Len(t, markers, 1)
	require.Equal(t, a.ID.Hex(), markers[0].Coordinate)
	removed, err = store.ClearUndelivered(a)
	require.NoError(t, err)
	require.True(t, removed)
}

// A marker survives reopening the store.
func TestUndeliveredMarkerSurvivesReopen(t *testing.T) {
	store, path := openTemp(t)
	sk := nostr.Generate()
	ev := signed(t, sk, 30078, 100, nostr.Tags{{"d", "x"}}, "v1")
	_, err := store.MarkUndelivered(ev, "abandoned", time.Now())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	defer reopened.Close()
	markers, err := reopened.ListUndelivered()
	require.NoError(t, err)
	require.Len(t, markers, 1)
	require.Equal(t, ev.ID, markers[0].EventID)
}
