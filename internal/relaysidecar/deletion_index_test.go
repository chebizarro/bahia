package relaysidecar

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

// dCase is a d value and the coordinate length it gives. The eventstore's tag
// index skips values longer than tagIndexMaxValue (100 bytes) and empty ones.
type dCase struct {
	name string
	d    string
}

// coordinateDCases: a short coordinate; a 108-byte one (the length of relay
// config coordinates) whose d is still indexed; a d itself too long to index;
// and an empty d.
func coordinateDCases() []dCase {
	prefixLen := len("30078:") + 64 + len(":")
	return []dCase{
		{name: "short", d: "app"},
		{name: "108-byte coordinate", d: strings.Repeat("c", 108-prefixLen)},
		{name: "unindexed d", d: strings.Repeat("d", 150)},
		{name: "empty d", d: ""},
	}
}

func authorVersions(t *testing.T, store *eventStore, kind nostr.Kind, author nostr.PubKey) []nostr.ID {
	t.Helper()
	return storeIDs(t, store, nostr.Filter{Kinds: []nostr.Kind{kind}, Authors: []nostr.PubKey{author}})
}

// TestEventStoreNIP09HoldsForCoordinatesOfAnyLength: for
// every coordinate length, an `a` request deletes the stored versions, the
// relay never re-accepts a deleted version (whether the request arrives after
// the event or before it), and newer versions still replace older ones.
func TestEventStoreNIP09HoldsForCoordinatesOfAnyLength(t *testing.T) {
	base := nostr.Timestamp(1_790_000_000)
	for _, tc := range coordinateDCases() {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestEventStore(t, t.TempDir())
			alice := nostr.Generate()
			address := fmt.Sprintf("30078:%s:%s", alice.Public().Hex(), tc.d)
			dTag := nostr.Tags{{"d", tc.d}}

			// The request arrives after the event.
			v1 := signedStoreEvent(t, alice, 30078, base, dTag, "v1")
			v2 := signedStoreEvent(t, alice, 30078, base+5, dTag, "v2")
			require.NoError(t, store.Replace(t.Context(), v1))
			require.NoError(t, store.Replace(t.Context(), v2))
			require.Equal(t, []nostr.ID{v2.ID}, authorVersions(t, store, 30078, alice.Public()), "v2 replaces v1")
			require.ErrorIs(t, store.Replace(t.Context(), v1), eventstore.ErrDupEvent, "an older version is not stored")

			require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, alice, nostr.KindDeletion, base+10, nostr.Tags{{"a", address}}, "")))
			require.Empty(t, authorVersions(t, store, 30078, alice.Public()))
			require.ErrorIs(t, store.Replace(t.Context(), v2), errEventDeleted)
			require.ErrorIs(t, store.Replace(t.Context(), signedStoreEvent(t, alice, 30078, base+10, dTag, "same second")), errEventDeleted)
			require.Empty(t, authorVersions(t, store, 30078, alice.Public()))
			v3 := signedStoreEvent(t, alice, 30078, base+11, dTag, "v3")
			require.NoError(t, store.Replace(t.Context(), v3))
			require.Equal(t, []nostr.ID{v3.ID}, authorVersions(t, store, 30078, alice.Public()))

			// The request arrives first (another kind, so another coordinate).
			early := fmt.Sprintf("30079:%s:%s", alice.Public().Hex(), tc.d)
			require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, alice, nostr.KindDeletion, base+10, nostr.Tags{{"a", early}}, "")))
			require.ErrorIs(t, store.Replace(t.Context(), signedStoreEvent(t, alice, 30079, base+3, dTag, "deleted before it arrived")), errEventDeleted)
			require.Empty(t, authorVersions(t, store, 30079, alice.Public()))
			later := signedStoreEvent(t, alice, 30079, base+12, dTag, "after the request")
			require.NoError(t, store.Replace(t.Context(), later))
			require.Equal(t, []nostr.ID{later.ID}, authorVersions(t, store, 30079, alice.Public()))

			// Another author's request for this coordinate deletes and blocks nothing.
			mallory := nostr.Generate()
			require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, mallory, nostr.KindDeletion, base+20, nostr.Tags{{"a", address}}, "")))
			require.Equal(t, []nostr.ID{v3.ID}, authorVersions(t, store, 30078, alice.Public()))
		})
	}
}

// TestEventStoreNIP09PlainReplaceableIgnoresD: a plain replaceable kind has
// one coordinate per author, so an `a` reference carrying a d still deletes
// it and keeps it deleted.
func TestEventStoreNIP09PlainReplaceableIgnoresD(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	alice := nostr.Generate()
	base := nostr.Timestamp(1_790_000_000)
	relays := signedStoreEvent(t, alice, 10002, base, nil, "")
	require.NoError(t, store.Replace(t.Context(), relays))
	require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, alice, nostr.KindDeletion, base+1, nostr.Tags{{"a", fmt.Sprintf("10002:%s:stray", alice.Public().Hex())}}, "")))
	require.Empty(t, authorVersions(t, store, 10002, alice.Public()))
	require.ErrorIs(t, store.Replace(t.Context(), relays), errEventDeleted)
}

// TestEventStoreBuildsTheDeletionIndexForExistingStores: a store written
// before the deletion index existed (requests stored, no index, no marker)
// gets its requests indexed on the next open.
func TestEventStoreBuildsTheDeletionIndexForExistingStores(t *testing.T) {
	dir := t.TempDir()
	alice := nostr.Generate()
	base := nostr.Timestamp(1_790_000_000)
	d := coordinateDCases()[1].d
	target := newCoordinate(30078, alice.Public(), d)

	store, err := openEventStore(t.Context(), dir, nil)
	require.NoError(t, err)
	require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, alice, nostr.KindDeletion, base+10, nostr.Tags{{"a", target.String()}}, "")))
	require.NoError(t, store.backend().DB.Update(func(tx *bbolt.Tx) error {
		if err := tx.DeleteBucket(sidecarDeletionBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucket(sidecarDeletionBucket); err != nil {
			return err
		}
		return tx.Bucket(sidecarMetaBucket).Delete(deletionIndexVersionKey)
	}))
	deleted, err := store.coordinateDeleted(target, base)
	require.NoError(t, err)
	require.False(t, deleted, "precondition: the index is empty")
	require.NoError(t, store.Close())

	store = openTestEventStore(t, dir)
	deleted, err = store.coordinateDeleted(target, base)
	require.NoError(t, err)
	require.True(t, deleted)
	require.ErrorIs(t, store.Replace(t.Context(), signedStoreEvent(t, alice, 30078, base, nostr.Tags{{"d", d}}, "")), errEventDeleted)
}

// TestEventStoreSweptDeletionRequestReleasesItsCoordinate: the index holds
// exactly the stored requests. A request with a NIP-40 expiration blocks its
// coordinate until the sweep removes it, as an `e` tombstone does.
func TestEventStoreSweptDeletionRequestReleasesItsCoordinate(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	alice := nostr.Generate()
	base := nostr.Timestamp(1_790_000_000)
	d := coordinateDCases()[1].d
	address := fmt.Sprintf("30078:%s:%s", alice.Public().Hex(), d)
	expiring := nostr.Tags{{"a", address}, {"expiration", strconv.FormatInt(int64(base+100), 10)}}
	require.NoError(t, store.Save(t.Context(), signedStoreEvent(t, alice, nostr.KindDeletion, base+10, expiring, "")))
	state := signedStoreEvent(t, alice, 30078, base, nostr.Tags{{"d", d}}, "")
	require.ErrorIs(t, store.Replace(t.Context(), state), errEventDeleted)

	result, err := store.SweepRetention(t.Context(), time.Unix(int64(base+200), 0), retentionPolicy{})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Expired)
	require.NoError(t, store.backend().DB.View(func(tx *bbolt.Tx) error {
		key, _ := tx.Bucket(sidecarDeletionBucket).Cursor().First()
		require.Nil(t, key, "the swept request's entries are gone")
		return nil
	}))
	require.NoError(t, store.Replace(t.Context(), state))
}

// TestSidecarNIP09LongCoordinateTombstonesOverWebsocket: over
// a real websocket, for short and long coordinates and for `e` references, a
// deleted event is refused on re-publish (OK false, NIP-09) and absent from
// REQ results, whether the deletion request arrives after the event or before
// it. Every wait is on OK or EOSE.
func TestSidecarNIP09LongCoordinateTombstonesOverWebsocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	_, relayURL := startSidecarForFanoutTest(t)
	client := dialRawRelay(t, ctx, relayURL)
	requireRefused := func(event nostr.Event) {
		t.Helper()
		ok := client.publishFrame(event)
		require.False(t, ok.ok, "kind %d re-accepted after deletion", event.Kind)
		require.True(t, strings.HasPrefix(ok.reason, "blocked: "), ok.reason)
		require.Contains(t, ok.reason, "NIP-09")
	}
	stored := func(subID string, kind nostr.Kind, author nostr.PubKey) []nostr.ID {
		t.Helper()
		return client.storedIDs(subID, nostr.Filter{Kinds: []nostr.Kind{kind}, Authors: []nostr.PubKey{author}})
	}

	for i, tc := range coordinateDCases()[:3] {
		alice := nostr.Generate()
		dTag := nostr.Tags{{"d", tc.d}}
		address := fmt.Sprintf("30078:%s:%s", alice.Public().Hex(), tc.d)
		if i > 0 {
			require.Greater(t, len(address), tagIndexMaxValue, tc.name)
		}

		// After: the event, then the request.
		state := signedNowEvent(t, alice, 30078, 0, dTag, "state")
		note := signedNowEvent(t, alice, 1, 0, nil, "note")
		client.publish(state)
		client.publish(note)
		client.publish(signedNowEvent(t, alice, nostr.KindDeletion, 10, nostr.Tags{{"a", address}, {"e", note.ID.Hex()}}, ""))
		require.Empty(t, stored(tc.name+"/after", 30078, alice.Public()))
		require.Empty(t, client.storedIDs(tc.name+"/note", nostr.Filter{IDs: []nostr.ID{note.ID}}))
		requireRefused(state)
		requireRefused(note)
		require.Empty(t, stored(tc.name+"/republished", 30078, alice.Public()), "a refused re-publish stays hidden")

		// Before: the request, then the events it names.
		early := fmt.Sprintf("30079:%s:%s", alice.Public().Hex(), tc.d)
		earlyNote := signedNowEvent(t, alice, 1, 1, nil, "early note")
		client.publish(signedNowEvent(t, alice, nostr.KindDeletion, 10, nostr.Tags{{"a", early}, {"e", earlyNote.ID.Hex()}}, ""))
		requireRefused(signedNowEvent(t, alice, 30079, 2, dTag, "deleted before it arrived"))
		requireRefused(earlyNote)
		require.Empty(t, stored(tc.name+"/before", 30079, alice.Public()))
		require.Empty(t, client.storedIDs(tc.name+"/early-note", nostr.Filter{IDs: []nostr.ID{earlyNote.ID}}))

		// A version newer than the request is accepted.
		newer := signedNowEvent(t, alice, 30078, 11, dTag, "newer")
		client.publish(newer)
		require.Equal(t, []nostr.ID{newer.ID}, stored(tc.name+"/newer", 30078, alice.Public()))
	}
}

// TestSidecarAgeCapSparesStateKinds covers the write side of over a real
// websocket: replaceable and addressable events and deletion requests older
// than a year are accepted and served, while regular and ephemeral events
// that old are still refused.
func TestSidecarAgeCapSparesStateKinds(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	_, relayURL := startSidecarForFanoutTest(t)
	client := dialRawRelay(t, ctx, relayURL)
	alice := nostr.Generate()
	twoYearsAgo := nostr.Now() - nostr.Timestamp((2 * maxEventAge).Seconds())

	var accepted []nostr.ID
	for _, event := range []nostr.Event{
		signedStoreEvent(t, alice, 0, twoYearsAgo, nil, "{}"),
		signedStoreEvent(t, alice, 10002, twoYearsAgo, nil, ""),
		signedStoreEvent(t, alice, 30078, twoYearsAgo, nostr.Tags{{"d", "old-config"}}, "addressable"),
		signedStoreEvent(t, alice, nostr.KindDeletion, twoYearsAgo, nostr.Tags{{"e", nostr.Generate().Public().Hex()}}, ""),
	} {
		client.publish(event)
		accepted = append(accepted, event.ID)
	}
	served := client.storedIDs("old-state", nostr.Filter{Authors: []nostr.PubKey{alice.Public()}})
	require.ElementsMatch(t, accepted, served)

	for _, kind := range []nostr.Kind{1, 20001} {
		ok := client.publishFrame(signedStoreEvent(t, alice, kind, twoYearsAgo, nil, "old"))
		require.False(t, ok.ok, "kind %d", kind)
		require.Contains(t, ok.reason, "invalid: created_at too far in the past")
	}
	// The cap is a year, not anything older than now.
	client.publish(signedStoreEvent(t, alice, 1, nostr.Now()-nostr.Timestamp((maxEventAge-time.Hour).Seconds()), nil, "recent enough"))
}

// TestSidecarPolicyAppliesTheSharedAgeCap: the sidecar's
// write policy refuses a two-year-old event exactly when nostrutil.AgeCapped
// caps its kind, the rule the daemon's ValidateInboundEvent applies too.
func TestSidecarPolicyAppliesTheSharedAgeCap(t *testing.T) {
	now := nostr.Timestamp(1_800_000_000)
	pol := &policy{now: func() nostr.Timestamp { return now }}
	alice := nostr.Generate()
	old := now - nostr.Timestamp((2 * nostrutil.MaxEventAge).Seconds())
	for _, kind := range []nostr.Kind{0, 3, 10002, 19999, 30000, 30078, 39999, nostr.KindDeletion, 1, 4, 1059, 9999, 20000, 29999, 40000, 65535} {
		event := signedStoreEvent(t, alice, kind, old, nil, "old")
		rejected, reason := pol.acceptEvent(t.Context(), event)
		require.Equal(t, nostrutil.AgeCapped(kind), rejected, "kind %d: %s", kind, reason)
		if rejected {
			require.Contains(t, reason, "invalid: created_at too far in the past")
		}
	}
}
