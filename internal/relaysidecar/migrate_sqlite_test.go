package relaysidecar

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
	"go.uber.org/zap"
)

// writeLegacySQLiteStore creates events.sqlite with the pre-eventstore schema
// and rows. A nil replaceable key reproduces rows written before the key
// existed, including a stale version of a coordinate.
func writeLegacySQLiteStore(t *testing.T, dir string, rows []legacyRow) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, legacySQLiteFile))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	_, err = db.Exec(`
		CREATE TABLE events (
			id TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL,
			kind INTEGER NOT NULL,
			pubkey TEXT NOT NULL,
			replaceable_key TEXT,
			event_json BLOB NOT NULL
		);
		CREATE UNIQUE INDEX events_replaceable_key ON events(replaceable_key) WHERE replaceable_key IS NOT NULL;
		CREATE INDEX events_created_at ON events(created_at DESC);`)
	require.NoError(t, err)
	for i, row := range rows {
		encoded := row.raw
		if encoded == nil {
			encoded, err = json.Marshal(row.event)
			require.NoError(t, err)
		}
		id := row.event.ID.Hex()
		if row.raw != nil {
			id = "corrupt-" + strconv.Itoa(i)
		}
		_, err = db.Exec(`INSERT INTO events (id, created_at, kind, pubkey, replaceable_key, event_json) VALUES (?, ?, ?, ?, ?, ?)`,
			id, int64(row.event.CreatedAt), int(row.event.Kind), row.event.PubKey.Hex(), row.key, encoded)
		require.NoError(t, err)
	}
}

type legacyRow struct {
	event nostr.Event
	key   any // replaceable_key column: nil or string
	raw   []byte
}

func legacyKey(event nostr.Event) any {
	if !event.Kind.IsReplaceable() && !event.Kind.IsAddressable() {
		return nil
	}
	if event.Kind.IsReplaceable() {
		return strconv.Itoa(int(event.Kind)) + ":" + event.PubKey.Hex()
	}
	return strconv.Itoa(int(event.Kind)) + ":" + event.PubKey.Hex() + ":" + event.Tags.GetD()
}

func TestMigrateLegacySQLitePreservesEventsAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	alice, bob := nostr.Generate(), nostr.Generate()
	now := nostr.Now()
	recipient := bob.Public().Hex()
	regular := []nostr.Event{
		signedStoreEvent(t, alice, 4903, now-3000, nostr.Tags{{"t", "audit"}}, "audit"),
		signedStoreEvent(t, alice, kinds.ContextVMGiftWrap, now-2000, nostr.Tags{{"p", recipient}}, "wrap"),
		signedStoreEvent(t, bob, 1, now-1000, nil, "note"),
		signedStoreEvent(t, bob, nostr.KindDeletion, now-900, nostr.Tags{{"e", nostr.Generate().Public().Hex()}}, ""),
	}
	staleState := signedStoreEvent(t, alice, 30900, now-500, nostr.Tags{{"d", "service:web"}}, `{"v":1}`)
	latestState := signedStoreEvent(t, alice, 30900, now-400, nostr.Tags{{"d", "service:web"}}, `{"v":2}`)
	relayList := signedStoreEvent(t, bob, 10002, now-300, nil, "")
	expiring := signedStoreEvent(t, alice, 1, now-200, nostr.Tags{{"expiration", strconv.FormatInt(int64(now+3600), 10)}}, "expiring")
	oversize := signedStoreEvent(t, alice, kinds.ContextVMGiftWrap, now-100, nil, strings.Repeat("x", 70000))
	tampered := signedStoreEvent(t, alice, 1, now-50, nil, "tampered")
	tampered.Content = "changed after signing"

	rows := []legacyRow{
		// The stale version comes second so import order, not row order, decides.
		{event: latestState, key: legacyKey(latestState)},
		{event: staleState, key: nil},
		{event: relayList, key: legacyKey(relayList)},
		{event: expiring},
		{event: oversize},
		{event: tampered},
		{event: nostr.Event{CreatedAt: now - 10, Kind: 1}, raw: []byte(`{`)},
	}
	for _, event := range regular {
		rows = append(rows, legacyRow{event: event})
	}
	writeLegacySQLiteStore(t, dir, rows)

	store, err := openEventStore(t.Context(), dir, zap.NewNop())
	require.NoError(t, err)
	want := append([]nostr.Event{latestState, relayList, expiring}, regular...)
	requireStoreHolds := func(store *eventStore) {
		t.Helper()
		for _, event := range want {
			got := storeIDs(t, store, nostr.Filter{IDs: []nostr.ID{event.ID}})
			require.Equal(t, []nostr.ID{event.ID}, got, "kind %d %q", event.Kind, event.Content)
		}
		require.Equal(t, []nostr.ID{latestState.ID}, storeIDs(t, store, coordinateFilter(30900, alice.Public(), "service:web")), "latest-wins")
		require.Equal(t, []nostr.ID{relayList.ID}, storeIDs(t, store, coordinateFilter(10002, bob.Public(), "")))
		require.Empty(t, storeIDs(t, store, nostr.Filter{IDs: []nostr.ID{staleState.ID, oversize.ID, tampered.ID}}))
		// Tag indexes are built for imported events.
		require.Equal(t, []nostr.ID{regular[1].ID}, storeIDs(t, store, nostr.Filter{Tags: nostr.TagMap{"p": {recipient}}}))
		count, err := store.Count(t.Context(), nostr.Filter{})
		require.NoError(t, err)
		require.EqualValues(t, len(want), count)
	}
	requireStoreHolds(store)

	var marker sqliteMigrationResult
	require.NoError(t, store.backend().DB.View(func(tx *bbolt.Tx) error {
		return json.Unmarshal(tx.Bucket(sidecarMetaBucket).Get(sqliteMigrationKey), &marker)
	}))
	require.Equal(t, len(rows), marker.Rows)
	// Oldest first: the stale version is written, then superseded by the
	// latest one, so it counts as imported but is not kept.
	require.Equal(t, len(want)+1, marker.Imported)
	require.Zero(t, marker.AlreadyPresent)
	require.Equal(t, 2, marker.SkippedInvalid, "tampered id and corrupt JSON")
	require.Equal(t, 1, marker.SkippedOversize)

	// The imported expiration is indexed for the NIP-40 sweep.
	result, err := store.sweepExpired(t.Context(), now+3600)
	require.NoError(t, err)
	require.EqualValues(t, 1, result)
	want = slicesWithout(want, expiring.ID)

	// Rerun with the marker: skipped. Rerun without it (as after a crash
	// before the marker was written): nothing new, the same set.
	again, err := migrateLegacySQLite(t.Context(), store, dir, zap.NewNop())
	require.NoError(t, err)
	require.True(t, again.Skipped)
	require.NoError(t, store.backend().DB.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(sidecarMetaBucket).Delete(sqliteMigrationKey)
	}))
	rerun, err := migrateLegacySQLite(t.Context(), store, dir, zap.NewNop())
	require.NoError(t, err)
	require.False(t, rerun.Skipped)
	// Everything is already present except the event the NIP-40 sweep
	// removed, which a forced rerun restores (it has not expired yet).
	require.Equal(t, 1, rerun.Imported)
	require.Equal(t, len(want)+1, rerun.AlreadyPresent, "the stale version also loses to the stored latest")
	want = append(want, expiring)
	require.NoError(t, store.Close())

	// Restarting reuses the store and does not import again; the legacy file
	// stays as a rollback copy.
	reopened := openTestEventStore(t, dir)
	requireStoreHolds(reopened)
	require.FileExists(t, filepath.Join(dir, legacySQLiteFile))
}

func TestMigrateLegacySQLiteWithoutSourceIsANoop(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	result, err := migrateLegacySQLite(context.Background(), store, t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	require.True(t, result.Skipped)
}

func slicesWithout(events []nostr.Event, id nostr.ID) []nostr.Event {
	var kept []nostr.Event
	for _, event := range events {
		if event.ID != id {
			kept = append(kept, event)
		}
	}
	return kept
}
