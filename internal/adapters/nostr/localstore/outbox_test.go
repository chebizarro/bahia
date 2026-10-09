package localstore

import (
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func openTempOutbox(t *testing.T) (*Outbox, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache", "outbox.bolt")
	outbox, err := OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.Close() })
	return outbox, path
}

func outboxEvent(t *testing.T, content string) nostr.Event {
	t.Helper()
	return signed(t, nostr.Generate(), nostr.Kind(4903), nostr.Now(), nil, content)
}

func TestOutboxEnqueueIsIdempotentAndListsPendingPerTargetInOrder(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	base := time.Unix(1_700_000_000, 0)
	first, second, other := outboxEvent(t, "first"), outboxEvent(t, "second"), outboxEvent(t, "other")

	inserted, err := outbox.Enqueue(OutboxEntry{Event: second, Target: "", EnqueuedAt: base.Add(time.Second)})
	require.NoError(t, err)
	require.True(t, inserted)
	_, err = outbox.Enqueue(OutboxEntry{Event: first, Target: "", EnqueuedAt: base})
	require.NoError(t, err)
	_, err = outbox.Enqueue(OutboxEntry{Event: other, Target: "control-plane", EnqueuedAt: base})
	require.NoError(t, err)
	inserted, err = outbox.Enqueue(OutboxEntry{Event: first, Target: "", EnqueuedAt: base.Add(time.Hour)})
	require.NoError(t, err)
	require.False(t, inserted, "the same signed event is never queued twice")

	page, err := outbox.ListPending("", nil, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, first.ID, page[0].Event.ID, "oldest first")
	require.True(t, page[0].Event.VerifySignature(), "the stored event is the signed event")
	page, err = outbox.ListPending("", &OutboxCursor{EnqueuedAt: page[0].EnqueuedAt, ID: page[0].Event.ID}, 10)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, second.ID, page[0].Event.ID, "the cursor is exclusive")

	controlPlane, err := outbox.ListPending("control-plane", nil, 10)
	require.NoError(t, err)
	require.Len(t, controlPlane, 1)
	require.Equal(t, other.ID, controlPlane[0].Event.ID, "a target lists only its own entries")

	counts, err := outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, OutboxCounts{Pending: 3}, counts)
}

func TestOutboxCommitRoundMergesMonotonicallyAndSettlesOnce(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	ev := outboxEvent(t, "merge")
	_, err := outbox.Enqueue(OutboxEntry{Event: ev})
	require.NoError(t, err)

	_, err = outbox.CommitRound(ev.ID, OutboxRound{Rounds: 2, Relays: map[string]RelayDelivery{
		"wss://a": {Accepted: true}, "wss://b": {LastError: "down"},
	}, Delivered: true, State: OutboxPending})
	require.NoError(t, err)
	// A stale delivery (fewer rounds, a not-yet-accepted view of a) must not
	// undo what the store already knows.
	stored, err := outbox.CommitRound(ev.ID, OutboxRound{Rounds: 1, Relays: map[string]RelayDelivery{
		"wss://a": {LastError: "timeout"}, "wss://b": {Rejected: "blocked: no"},
	}, State: OutboxPending})
	require.NoError(t, err)
	require.Equal(t, 2, stored.Rounds)
	require.True(t, stored.Delivered)
	require.True(t, stored.Relays["wss://a"].Accepted)
	require.Empty(t, stored.Relays["wss://a"].LastError)
	require.Equal(t, "blocked: no", stored.Relays["wss://b"].Rejected)

	settledAt := time.Unix(1_700_000_100, 0)
	stored, err = outbox.CommitRound(ev.ID, OutboxRound{Rounds: 3, Relays: stored.Relays, Delivered: true, State: OutboxPublished, At: settledAt})
	require.NoError(t, err)
	require.Equal(t, OutboxPublished, stored.State)
	stored, err = outbox.CommitRound(ev.ID, OutboxRound{Rounds: 4, State: OutboxFailed})
	require.NoError(t, err)
	require.Equal(t, OutboxPublished, stored.State, "a settled entry is never reopened or re-settled")
	require.Equal(t, 3, stored.Rounds)

	pending, err := outbox.ListPending("", nil, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	got, found, err := outbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found, "a settled entry stays readable until pruned")
	require.Equal(t, settledAt.UTC(), got.SettledAt)
}

func TestOutboxPruneKeepsPendingAndRecentlySettledEntries(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	base := time.Unix(1_700_000_000, 0)
	oldPublished, newPublished, oldFailed, pending := outboxEvent(t, "a"), outboxEvent(t, "b"), outboxEvent(t, "c"), outboxEvent(t, "d")
	for _, ev := range []nostr.Event{oldPublished, newPublished, oldFailed, pending} {
		_, err := outbox.Enqueue(OutboxEntry{Event: ev, EnqueuedAt: base})
		require.NoError(t, err)
	}
	commit := func(ev nostr.Event, state string, at time.Time) {
		_, err := outbox.CommitRound(ev.ID, OutboxRound{Rounds: 1, State: state, At: at})
		require.NoError(t, err)
	}
	commit(oldPublished, OutboxPublished, base.Add(time.Minute))
	commit(newPublished, OutboxPublished, base.Add(time.Hour))
	commit(oldFailed, OutboxFailed, base.Add(time.Minute))
	counts, err := outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, OutboxCounts{Pending: 1, Failed: 1}, counts)

	removed, err := outbox.Prune(base.Add(30*time.Minute), base.Add(30*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2, removed)
	for ev, want := range map[nostr.ID]bool{oldPublished.ID: false, newPublished.ID: true, oldFailed.ID: false, pending.ID: true} {
		_, found, err := outbox.Get(ev)
		require.NoError(t, err)
		require.Equal(t, want, found, ev.Hex())
	}
	counts, err = outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, OutboxCounts{Pending: 1}, counts)
}

func TestOutboxSurvivesReopenAndSharesHandlesInProcess(t *testing.T) {
	outbox, path := openTempOutbox(t)
	ev := outboxEvent(t, "durable")
	_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "control-plane", EntityType: "docs"})
	require.NoError(t, err)
	_, err = outbox.CommitRound(ev.ID, OutboxRound{Rounds: 1, Relays: map[string]RelayDelivery{"wss://a": {Accepted: true}}, State: OutboxPending})
	require.NoError(t, err)

	second, err := OpenOutbox(path)
	require.NoError(t, err, "a second handle in the process shares the open file")
	require.NoError(t, outbox.Close())
	got, found, err := second.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, second.Close())

	reopened, err := OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	pending, err := reopened.ListPending("control-plane", nil, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, got, pending[0])
	require.True(t, pending[0].Relays["wss://a"].Accepted, "per-relay acceptance survives a restart")
	require.Equal(t, "docs", pending[0].EntityType)
}

func TestOutboxListFailedReturnsFailedEntriesOldestFirst(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	base := time.Unix(1_700_000_000, 0)
	a, b, c := outboxEvent(t, "first-fail"), outboxEvent(t, "second-fail"), outboxEvent(t, "pending")
	for _, ev := range []nostr.Event{a, b, c} {
		_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "", EnqueuedAt: base})
		require.NoError(t, err)
	}
	// Fail a and b at different times; c stays pending.
	_, err := outbox.CommitRound(a.ID, OutboxRound{Rounds: 3, State: OutboxFailed, Detail: "abandoned: budget exhausted", At: base.Add(time.Minute)})
	require.NoError(t, err)
	_, err = outbox.CommitRound(b.ID, OutboxRound{Rounds: 1, State: OutboxFailed, Detail: "abandoned: rejected by all relays", At: base.Add(2 * time.Minute)})
	require.NoError(t, err)

	failed, err := outbox.ListFailed(10)
	require.NoError(t, err)
	require.Len(t, failed, 2, "only failed entries are returned, not pending")
	require.Equal(t, a.ID, failed[0].Event.ID, "oldest settled first")
	require.Equal(t, b.ID, failed[1].Event.ID)
	require.Equal(t, OutboxFailed, failed[0].State)
	require.Equal(t, 3, failed[0].Rounds)
	require.Equal(t, "abandoned: budget exhausted", failed[0].LastError)

	// With a limit of 1, only the oldest is returned.
	one, err := outbox.ListFailed(1)
	require.NoError(t, err)
	require.Len(t, one, 1)
	require.Equal(t, a.ID, one[0].Event.ID)
}

func TestOutboxLatestByCoordinateUsesDurableIndexAndPrunesAtomically(t *testing.T) {
	outbox, path := openTempOutbox(t)
	key := nostr.Generate()
	author := key.Public()
	now := nostr.Now()
	d := "artifact:sbom-package:v2:fixture"
	old := signed(t, key, nostr.Kind(30900), now-1, nostr.Tags{{"d", d}}, `{"id":"old"}`)
	newer := signed(t, key, nostr.Kind(30900), now, nostr.Tags{{"d", d}}, `{"id":"new"}`)
	other := signed(t, key, nostr.Kind(30900), now+1, nostr.Tags{{"d", d + ":other"}}, `{"id":"other"}`)
	for _, ev := range []nostr.Event{old, newer, other} {
		_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "control-plane"})
		require.NoError(t, err)
	}
	entry, found, err := outbox.LatestByCoordinate("control-plane", 30900, author, d)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, newer.ID, entry.Event.ID)
	_, found, err = outbox.LatestByCoordinate("other-target", 30900, author, d)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = outbox.LatestByCoordinate("control-plane", 30900, nostr.Generate().Public(), d)
	require.NoError(t, err)
	require.False(t, found)

	settledAt := time.Now().UTC()
	_, err = outbox.CommitRound(newer.ID, OutboxRound{State: OutboxFailed, At: settledAt})
	require.NoError(t, err)
	require.NoError(t, outbox.Close())
	reopened, err := OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	entry, found, err = reopened.LatestByCoordinate("control-plane", 30900, author, d)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, newer.ID, entry.Event.ID)
	require.Equal(t, OutboxFailed, entry.State)

	removed, err := reopened.Prune(settledAt.Add(time.Second), settledAt.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	entry, found, err = reopened.LatestByCoordinate("control-plane", 30900, author, d)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, old.ID, entry.Event.ID, "pruning newest entry reveals earlier retained coordinate")
}

func TestOutboxLatestByCoordinateBreaksTimestampTiesByLowestID(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	key := nostr.Generate()
	d := "artifact:sbom-package:v2:tie"
	at := nostr.Now()
	a := signed(t, key, 30900, at, nostr.Tags{{"d", d}}, `{"id":"a"}`)
	b := signed(t, key, 30900, at, nostr.Tags{{"d", d}}, `{"id":"b"}`)
	for _, ev := range []nostr.Event{a, b} {
		_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "control-plane"})
		require.NoError(t, err)
	}
	entry, found, err := outbox.LatestByCoordinate("control-plane", 30900, key.Public(), d)
	require.NoError(t, err)
	require.True(t, found)
	want := a.ID
	if b.ID.Hex() < a.ID.Hex() {
		want = b.ID
	}
	require.Equal(t, want, entry.Event.ID)
}

func TestOutboxOpenBackfillsCoordinateIndexForPreIndexEntries(t *testing.T) {
	outbox, path := openTempOutbox(t)
	key := nostr.Generate()
	d := "artifact:sbom-package:v2:upgrade"
	ev := signed(t, key, 30900, nostr.Now(), nostr.Tags{{"d", d}}, `{"id":"package"}`)
	_, err := outbox.Enqueue(OutboxEntry{Event: ev, Target: "control-plane"})
	require.NoError(t, err)
	// Older outbox files contain the durable entry and pending index, but
	// neither the package-coordinate bucket nor its completion marker.
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		return tx.DeleteBucket(outboxCoordinatesBucket)
	}))
	require.NoError(t, outbox.Close())

	reopened, err := OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	entry, found, err := reopened.LatestByCoordinate("control-plane", 30900, key.Public(), d)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, ev.ID, entry.Event.ID)
	require.Equal(t, OutboxPending, entry.State)
}
