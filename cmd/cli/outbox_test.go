package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func testOutboxEvent(t *testing.T, content string) nostr.Event {
	t.Helper()
	sk := nostr.Generate()
	ev := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Content: content}
	require.NoError(t, ev.Sign(sk))
	return ev
}

func setupTestOutbox(t *testing.T) (*localstore.Outbox, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test-outbox.bolt")
	outbox, err := localstore.OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.Close() })
	return outbox, path
}

// runOutboxCmd executes an outbox subcommand against a test outbox path and
// returns stdout. Flags are passed via cobra args to avoid global-variable
// race with cobra's default-reset behaviour.
func runOutboxCmd(t *testing.T, path string, daemon bool, args ...string) string {
	t.Helper()
	origFormat := outputFormat
	t.Cleanup(func() { outputFormat = origFormat })
	outputFormat = "table"

	root := outboxCommands()
	setSilence(root)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	allArgs := []string{"--outbox-path", path}
	if daemon {
		allArgs = append(allArgs, "--daemon")
	}
	allArgs = append(allArgs, args...)
	root.SetArgs(allArgs)
	require.NoError(t, root.Execute())
	return buf.String()
}

func runOutboxCmdJSON(t *testing.T, path string, args ...string) string {
	t.Helper()
	origFormat := outputFormat
	t.Cleanup(func() { outputFormat = origFormat })
	outputFormat = "json"

	root := outboxCommands()
	setSilence(root)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	allArgs := append([]string{"--outbox-path", path}, args...)
	root.SetArgs(allArgs)
	require.NoError(t, root.Execute())
	return buf.String()
}

func runOutboxCmdErr(t *testing.T, path string, daemon bool, args ...string) error {
	t.Helper()
	origFormat := outputFormat
	t.Cleanup(func() { outputFormat = origFormat })
	outputFormat = "table"

	root := outboxCommands()
	setSilence(root)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	allArgs := []string{"--outbox-path", path}
	if daemon {
		allArgs = append(allArgs, "--daemon")
	}
	allArgs = append(allArgs, args...)
	root.SetArgs(allArgs)
	return root.Execute()
}

func setSilence(cmd *cobra.Command) {
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	for _, c := range cmd.Commands() {
		setSilence(c)
	}
}

// Test (a): enqueue and list pending.
func TestOutboxListShowsPendingEntries(t *testing.T) {
	outbox, path := setupTestOutbox(t)
	ev := testOutboxEvent(t, "pending-event")
	inserted, err := outbox.Enqueue(localstore.OutboxEntry{
		Event:      ev,
		Target:     "control-plane",
		EntityType: "service",
		EnqueuedAt: time.Now(),
	})
	require.NoError(t, err)
	require.True(t, inserted)

	out := runOutboxCmd(t, path, false, "list")
	require.Contains(t, out, ev.ID.Hex())
	require.Contains(t, out, "pending")
	require.Contains(t, out, "control-plane")
}

// Test (b): list failed filters correctly.
func TestOutboxListFiltersByState(t *testing.T) {
	outbox, path := setupTestOutbox(t)
	base := time.Unix(1_700_000_000, 0)

	pendingEv := testOutboxEvent(t, "pending")
	failedEv := testOutboxEvent(t, "failed")
	publishedEv := testOutboxEvent(t, "published")

	for _, ev := range []nostr.Event{pendingEv, failedEv, publishedEv} {
		_, err := outbox.Enqueue(localstore.OutboxEntry{Event: ev, EnqueuedAt: base})
		require.NoError(t, err)
	}
	_, err := outbox.CommitRound(failedEv.ID, localstore.OutboxRound{
		Rounds: 3, State: localstore.OutboxFailed,
		Detail: "all relays rejected", At: base.Add(time.Minute),
	})
	require.NoError(t, err)
	_, err = outbox.CommitRound(publishedEv.ID, localstore.OutboxRound{
		Rounds: 1, State: localstore.OutboxPublished, At: base.Add(time.Minute),
	})
	require.NoError(t, err)

	// Default (pending+failed) should show 2 entries.
	out := runOutboxCmd(t, path, false, "list")
	require.Contains(t, out, pendingEv.ID.Hex())
	require.Contains(t, out, failedEv.ID.Hex())
	require.NotContains(t, out, publishedEv.ID.Hex())

	// --state=failed shows only the failed entry.
	out = runOutboxCmd(t, path, false, "list", "--state=failed")
	require.NotContains(t, out, pendingEv.ID.Hex())
	require.Contains(t, out, failedEv.ID.Hex())
	require.NotContains(t, out, publishedEv.ID.Hex())

	// --state=pending shows only the pending entry.
	out = runOutboxCmd(t, path, false, "list", "--state=pending")
	require.Contains(t, out, pendingEv.ID.Hex())
	require.NotContains(t, out, failedEv.ID.Hex())

	// --state=all shows everything.
	out = runOutboxCmd(t, path, false, "list", "--state=all")
	require.Contains(t, out, pendingEv.ID.Hex())
	require.Contains(t, out, failedEv.ID.Hex())
	require.Contains(t, out, publishedEv.ID.Hex())
}

// Test (c): retry re-enqueues a failed entry.
func TestOutboxRetryReEnqueuesFailed(t *testing.T) {
	outbox, path := setupTestOutbox(t)
	base := time.Unix(1_700_000_000, 0)

	ev := testOutboxEvent(t, "will-fail")
	_, err := outbox.Enqueue(localstore.OutboxEntry{
		Event: ev, Target: "control-plane", EnqueuedAt: base,
	})
	require.NoError(t, err)
	_, err = outbox.CommitRound(ev.ID, localstore.OutboxRound{
		Rounds: 3, State: localstore.OutboxFailed,
		Detail: "budget exhausted", At: base.Add(time.Minute),
	})
	require.NoError(t, err)

	// Verify it's failed.
	counts, err := outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.Failed)
	require.Equal(t, int64(0), counts.Pending)

	// Retry via CLI.
	out := runOutboxCmd(t, path, false, "retry", ev.ID.Hex())
	require.Contains(t, out, "Retried entry")
	require.Contains(t, out, "pending")

	// Now it's pending again.
	counts, err = outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(0), counts.Failed)
	require.Equal(t, int64(1), counts.Pending)

	// The entry's state is reset.
	entry, found, err := outbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, entry.State)
	require.Equal(t, 0, entry.Rounds)
	require.Empty(t, entry.LastError)
}

// Test (c) continued: retry --all re-enqueues all failed entries.
func TestOutboxRetryAllReEnqueuesAllFailed(t *testing.T) {
	outbox, path := setupTestOutbox(t)
	base := time.Unix(1_700_000_000, 0)

	a, b := testOutboxEvent(t, "fail-a"), testOutboxEvent(t, "fail-b")
	for _, ev := range []nostr.Event{a, b} {
		_, err := outbox.Enqueue(localstore.OutboxEntry{Event: ev, EnqueuedAt: base})
		require.NoError(t, err)
		_, err = outbox.CommitRound(ev.ID, localstore.OutboxRound{
			Rounds: 2, State: localstore.OutboxFailed, At: base.Add(time.Minute),
		})
		require.NoError(t, err)
	}

	counts, err := outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(2), counts.Failed)

	out := runOutboxCmd(t, path, false, "retry", "--all")
	require.Contains(t, out, "Retried 2 failed entries")

	counts, err = outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(0), counts.Failed)
	require.Equal(t, int64(2), counts.Pending)
}

// Test (d): counts match.
func TestOutboxCountsMatch(t *testing.T) {
	outbox, path := setupTestOutbox(t)
	base := time.Unix(1_700_000_000, 0)

	pending := testOutboxEvent(t, "p")
	failed := testOutboxEvent(t, "f")
	_, err := outbox.Enqueue(localstore.OutboxEntry{Event: pending, EnqueuedAt: base})
	require.NoError(t, err)
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: failed, EnqueuedAt: base})
	require.NoError(t, err)
	_, err = outbox.CommitRound(failed.ID, localstore.OutboxRound{
		Rounds: 1, State: localstore.OutboxFailed, At: base.Add(time.Minute),
	})
	require.NoError(t, err)

	out := runOutboxCmd(t, path, false, "counts")
	require.Contains(t, out, "Pending: 1")
	require.Contains(t, out, "Failed:  1")

	// JSON output.
	jsonOut := runOutboxCmdJSON(t, path, "counts")
	var counts localstore.OutboxCounts
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &counts))
	require.Equal(t, int64(1), counts.Pending)
	require.Equal(t, int64(1), counts.Failed)
}

// Test: --daemon opens read-only; retry and prune fail.
func TestOutboxDaemonReadOnly(t *testing.T) {
	// Create an outbox with a failed entry, then close so read-only can open.
	path := filepath.Join(t.TempDir(), "daemon-outbox.bolt")
	outbox, err := localstore.OpenOutbox(path)
	require.NoError(t, err)

	ev := testOutboxEvent(t, "daemon-test")
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: ev, EnqueuedAt: time.Now()})
	require.NoError(t, err)
	_, err = outbox.CommitRound(ev.ID, localstore.OutboxRound{
		Rounds: 1, State: localstore.OutboxFailed, At: time.Now(),
	})
	require.NoError(t, err)
	require.NoError(t, outbox.Close())

	// Read-only list should work.
	out := runOutboxCmd(t, path, true, "list", "--state=failed")
	require.Contains(t, out, ev.ID.Hex())

	// Read-only counts should work.
	out = runOutboxCmd(t, path, true, "counts")
	require.Contains(t, out, "Failed:  1")

	// Retry should fail with a clear error.
	err = runOutboxCmdErr(t, path, true, "retry", ev.ID.Hex())
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")

	// Prune --confirm should fail.
	err = runOutboxCmdErr(t, path, true, "prune", "--confirm")
	require.Error(t, err)
	require.Contains(t, err.Error(), "read-only")
}

// Test: prune dry-run default.
func TestOutboxPruneDryRunDefault(t *testing.T) {
	outbox, path := setupTestOutbox(t)
	base := time.Now().Add(-10 * 24 * time.Hour) // 10 days ago

	ev := testOutboxEvent(t, "old-published")
	_, err := outbox.Enqueue(localstore.OutboxEntry{Event: ev, EnqueuedAt: base})
	require.NoError(t, err)
	_, err = outbox.CommitRound(ev.ID, localstore.OutboxRound{
		Rounds: 1, State: localstore.OutboxPublished, At: base.Add(time.Minute),
	})
	require.NoError(t, err)

	// Default (dry run) should report but not remove.
	out := runOutboxCmd(t, path, false, "prune")
	require.Contains(t, out, "Dry run")
	require.Contains(t, out, "would prune 1 entries")
	require.Contains(t, out, "--confirm")

	// Verify the entry is still there.
	_, found, err := outbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found, "dry run should not remove entries")

	// With --confirm, actually prune.
	out = runOutboxCmd(t, path, false, "prune", "--confirm")
	require.Contains(t, out, "Pruned 1 settled entries")

	// Entry is gone.
	_, found, err = outbox.Get(ev.ID)
	require.NoError(t, err)
	require.False(t, found)
}
