package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
)

func requireFileHolds(t *testing.T, path, secret string, want bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, bytes.Contains(data, []byte(secret)), "%s holding the d value", path)
}

// purge-local lists without --execute and changes nothing; with --execute it
// removes the leaked identity's status events, their undelivered markers and
// outbox entries in every state, keeps everything else, and compacts both
// files so the leaked key's bytes are gone from disk.
func TestPurgeLocalRemovesLeakedEventsFromStoreAndOutbox(t *testing.T) {
	f := newLeakFixture(t)
	dir := t.TempDir()
	storePath := filepath.Join(dir, "daemon.bolt")
	outboxPath := filepath.Join(dir, "outbox.bolt")
	leaked := f.history(t, 6)
	other := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "cp-state:1"}}, Content: "canonical"}
	require.NoError(t, other.Sign(nostr.Generate()))
	sameAuthorOtherKind := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "note"}
	require.NoError(t, sameAuthorOtherKind.Sign(f.secret))

	store, err := localstore.Open(storePath)
	require.NoError(t, err)
	for _, ev := range append(slices.Clone(leaked), other, sameAuthorOtherKind) {
		_, err := store.SaveEvent(ev)
		require.NoError(t, err)
	}
	marked, err := store.MarkUndelivered(leaked[0], "abandoned: quorum unreachable", time.Now())
	require.NoError(t, err)
	require.True(t, marked)
	require.NoError(t, store.Close())

	outbox, err := localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	for _, ev := range []nostr.Event{leaked[0], leaked[1], other} {
		_, err := outbox.Enqueue(localstore.OutboxEntry{Event: ev, Target: "default"})
		require.NoError(t, err)
	}
	_, err = outbox.CommitRound(leaked[1].ID, localstore.OutboxRound{State: localstore.OutboxFailed, Detail: "blocked: no"})
	require.NoError(t, err)
	require.NoError(t, outbox.Close())
	requireFileHolds(t, storePath, f.dValue, true)
	requireFileHolds(t, outboxPath, f.dValue, true)

	args := []string{"purge-local", "--old-pubkey", f.author.Hex(), "--event-store", storePath, "--outbox", outboxPath}
	dry := runCLI(t, testDeps(nil), args...)
	require.Equal(t, exitOK, dry.code, dry.stderr+dry.stdout)
	// The store keeps the latest version per addressable coordinate: one
	// event per leaked kind.
	require.Contains(t, dry.stdout, "3 leaked events, 1 undelivered markers")
	require.Contains(t, dry.stdout, "2 leaked entries")
	require.Contains(t, dry.stdout, "dry run")
	for _, id := range idsOf(leaked[:3]) {
		require.Contains(t, dry.stdout, id)
	}
	requireFileHolds(t, storePath, f.dValue, true)
	requireFileHolds(t, outboxPath, f.dValue, true)

	res := runCLI(t, testDeps(nil), append(args, "--execute")...)
	require.Equal(t, exitOK, res.code, res.stderr+res.stdout)
	for _, out := range []string{dry.stdout, dry.stderr, res.stdout, res.stderr} {
		for _, secret := range f.forbiddenStrings() {
			require.NotContains(t, out, secret)
		}
	}
	requireFileHolds(t, storePath, f.dValue, false)
	requireFileHolds(t, outboxPath, f.dValue, false)

	store, err = localstore.Open(storePath)
	require.NoError(t, err)
	remaining := slices.Collect(store.QueryEvents(nostr.Filter{Authors: []nostr.PubKey{f.author}}))
	require.Len(t, remaining, 1)
	require.Equal(t, sameAuthorOtherKind.ID, remaining[0].ID, "only the leaked kinds are purged")
	kept := slices.Collect(store.QueryEvents(nostr.Filter{IDs: []nostr.ID{other.ID}}))
	require.Len(t, kept, 1)
	markers, err := store.ListUndelivered()
	require.NoError(t, err)
	require.Empty(t, markers)
	require.NoError(t, store.Close())

	outbox, err = localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	entries, err := outbox.ListEntries(nil, 100)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, other.ID, entries[0].Event.ID)
	require.NoError(t, outbox.Close())
}

// A missing path is refused rather than created, so a typo cannot report
// "nothing to purge" from a fresh empty store.
func TestPurgeLocalRefusesMissingStore(t *testing.T) {
	f := newLeakFixture(t)
	missing := filepath.Join(t.TempDir(), "absent.bolt")
	res := runCLI(t, testDeps(nil), "purge-local", "--old-pubkey", f.author.Hex(), "--event-store", missing, "--execute")
	require.Equal(t, exitFailure, res.code)
	_, err := os.Stat(missing)
	require.ErrorIs(t, err, os.ErrNotExist)
}
