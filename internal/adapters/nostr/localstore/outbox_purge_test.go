package localstore

import (
	"bytes"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

// PurgeEvents removes matching entries in every state together with their
// index rows and keeps everything else.
func TestOutboxPurgeEventsRemovesMatchingEntriesInEveryState(t *testing.T) {
	outbox, path := openTempOutbox(t)
	leaked := nostr.Generate()
	base := time.Unix(1_700_000_000, 0)
	pending := signed(t, leaked, nostr.Kind(31410), nostr.Now(), nostr.Tags{{"d", "secret-d"}}, "pending")
	published := signed(t, leaked, nostr.Kind(31411), nostr.Now(), nostr.Tags{{"d", "secret-d"}}, "published")
	failed := signed(t, leaked, nostr.Kind(30360), nostr.Now(), nostr.Tags{{"d", "secret-d"}}, "failed")
	keep := outboxEvent(t, "unrelated")
	for i, ev := range []nostr.Event{pending, published, failed, keep} {
		_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "default", EnqueuedAt: base.Add(time.Duration(i) * time.Second)})
		require.NoError(t, err)
	}
	_, err := outbox.CommitRound(published.ID, OutboxRound{State: OutboxPublished, Delivered: true, At: base.Add(time.Minute)})
	require.NoError(t, err)
	_, err = outbox.CommitRound(failed.ID, OutboxRound{State: OutboxFailed, Detail: "blocked: no", At: base.Add(time.Minute)})
	require.NoError(t, err)

	removed, err := outbox.PurgeEvents(func(ev nostr.Event) bool { return ev.PubKey == leaked.Public() })
	require.NoError(t, err)
	require.ElementsMatch(t, []nostr.ID{pending.ID, published.ID, failed.ID}, removed)

	for _, ev := range []nostr.Event{pending, published, failed} {
		_, found, err := outbox.Get(ev.ID)
		require.NoError(t, err)
		require.False(t, found, "purged entry %s is still readable", ev.ID.Hex())
	}
	_, found, err := outbox.Get(keep.ID)
	require.NoError(t, err)
	require.True(t, found)
	counts, err := outbox.Counts()
	require.NoError(t, err)
	require.EqualValues(t, 1, counts.Pending)
	require.Zero(t, counts.Failed)
	listed, err := outbox.ListPending("default", nil, 10)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, keep.ID, listed[0].Event.ID)

	// No state index row still names a purged id.
	require.NoError(t, outbox.Close())
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.View(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{outboxPendingBucket, outboxPublishedBucket, outboxFailedBucket} {
			require.NoError(t, tx.Bucket(name).ForEach(func(key, _ []byte) error {
				for _, ev := range []nostr.Event{pending, published, failed} {
					require.False(t, bytes.HasSuffix(key, ev.ID[:]), "index %s still holds purged id %s", name, ev.ID.Hex())
				}
				return nil
			}))
		}
		return nil
	}))
}

// An entry carrying delivery evidence is refused and a refused purge removes
// nothing; a read-only handle cannot purge.
func TestOutboxPurgeEventsRefusesDeliveryProofsAndReadOnly(t *testing.T) {
	outbox, path := openTempOutbox(t)
	ev := backupConfigProofEvent(t)
	other := outboxEvent(t, "matches too")
	_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "control-plane"})
	require.NoError(t, err)
	_, err = outbox.Enqueue(OutboxEntry{Event: other, Target: "default"})
	require.NoError(t, err)
	policy := DeliveryPolicy{WriteRelays: []string{"wss://a"}, Required: 1}
	_, err = outbox.CommitPublisherRound(ev.ID, OutboxRound{Target: "control-plane", Delivered: true, State: OutboxPublished, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}}})
	require.NoError(t, err)

	removed, err := outbox.PurgeEvents(func(nostr.Event) bool { return true })
	require.ErrorContains(t, err, "holds a delivery proof")
	require.Empty(t, removed)
	for _, id := range []nostr.ID{ev.ID, other.ID} {
		_, found, err := outbox.Get(id)
		require.NoError(t, err)
		require.True(t, found, "a refused purge removes nothing")
	}

	require.NoError(t, outbox.Close())
	readOnly, err := OpenOutboxReadOnly(path)
	require.NoError(t, err)
	defer readOnly.Close()
	_, err = readOnly.PurgeEvents(func(nostr.Event) bool { return true })
	require.ErrorIs(t, err, ErrReadOnly)
}
