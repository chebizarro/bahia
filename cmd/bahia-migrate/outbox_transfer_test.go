package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

type transferFakeSource struct {
	rows  []repository.NostrEventRecord
	moved []string
}

func (f *transferFakeSource) ListUnpublishedAfter(_ context.Context, target string, cursor *repository.NostrOutboxCursor, limit int) ([]repository.NostrEventRecord, error) {
	var result []repository.NostrEventRecord
	for _, row := range f.rows {
		if row.PublishState != repository.NostrPublishStatePending || row.PublishTarget != target {
			continue
		}
		if cursor != nil && (row.ReceivedAt.Before(cursor.ReceivedAt) || row.ReceivedAt.Equal(cursor.ReceivedAt) && row.ID <= cursor.ID) {
			continue
		}
		result = append(result, row)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}
func (f *transferFakeSource) TransferUnattemptedToLocalOutbox(_ context.Context, id, target string) (bool, error) {
	for i := range f.rows {
		if f.rows[i].ID == id && f.rows[i].PublishTarget == target && f.rows[i].PublishAttempts == 0 && f.rows[i].LastPublishError == "" {
			f.rows[i].PublishTarget = repository.LocalOutboxArchiveTarget(target)
			f.moved = append(f.moved, id)
			return true, nil
		}
	}
	return false, nil
}
func signedTransferRow(t *testing.T, at time.Time, target string) repository.NostrEventRecord {
	t.Helper()
	sk := gonostr.Generate()
	ev := gonostr.Event{Kind: 30078, CreatedAt: gonostr.Timestamp(at.Unix()), Tags: gonostr.Tags{{"t", "transfer-test"}}, Content: `{"ok":true}`}
	require.NoError(t, keyer.NewPlainKeySigner(sk).SignEvent(context.Background(), &ev))
	tags, err := json.Marshal(ev.Tags)
	require.NoError(t, err)
	return repository.NostrEventRecord{ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Sig: hex.EncodeToString(ev.Sig[:]), Content: ev.Content, Tags: tags, CreatedAt: at, ReceivedAt: at, PublishTarget: target, PublishState: repository.NostrPublishStatePending}
}
func TestOutboxTransferInventoryAndApplyPreserveSignedEvent(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	row := signedTransferRow(t, at, repository.NostrPublishTargetDefault)
	attempted := signedTransferRow(t, at.Add(time.Second), repository.NostrPublishTargetDefault)
	attempted.PublishAttempts = 1
	source := &transferFakeSource{rows: []repository.NostrEventRecord{row, attempted}}
	opts := outboxTransferOptions{maxRows: 10, maxPending: 10}
	var output bytes.Buffer
	stats, err := transferOutboxRows(ctx, source, nil, "", opts, &output)
	require.NoError(t, err)
	require.Equal(t, 2, stats.inspected)
	require.Equal(t, 1, stats.replayCandidates)
	require.Equal(t, 1, stats.conflicts)
	require.Empty(t, source.moved)
	require.Contains(t, output.String(), "relay-attempt state cannot be preserved")
	outboxPath := filepath.Join(t.TempDir(), "outbox.bolt")
	outbox, err := localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	opts.maxRows = 1
	stats, err = transferOutboxRows(ctx, source, outbox, "", opts, &output)
	require.NoError(t, err)
	require.Equal(t, 1, stats.transferred)
	require.True(t, stats.more)
	ev, err := gonostr.IDFromHex(row.ID)
	require.NoError(t, err)
	entry, found, err := outbox.Get(ev)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, row.ID, entry.Event.ID.Hex())
	require.Equal(t, row.Sig, hex.EncodeToString(entry.Event.Sig[:]))
	require.True(t, entry.Event.CheckID())
	require.True(t, entry.Event.VerifySignature())
	// A relay may have accepted before the old process crashed, without SQL
	// incrementing publish_attempts. The transfer must not invent that OK.
	require.Empty(t, entry.Relays)
	require.False(t, entry.Delivered)
	require.Zero(t, entry.Rounds)
	require.NoError(t, outbox.Close())
	outbox, err = localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	defer outbox.Close()
	stats, err = transferOutboxRows(ctx, source, outbox, "", opts, &output)
	require.NoError(t, err)
	require.Equal(t, 1, stats.conflicts)
	require.Equal(t, 0, stats.transferred)
	require.Equal(t, []string{row.ID}, source.moved)
}
func TestOutboxTransferRejectsCollisionAndAdmissionCap(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1700000000, 0).UTC()
	row := signedTransferRow(t, at, "")
	source := &transferFakeSource{rows: []repository.NostrEventRecord{row}}
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	ev, err := gonostr.IDFromHex(row.ID)
	require.NoError(t, err)
	other := gonostr.Event{ID: ev}
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: other, Target: "control-plane"})
	require.NoError(t, err)
	var output bytes.Buffer
	stats, err := transferOutboxRows(ctx, source, outbox, "", outboxTransferOptions{maxRows: 10, maxPending: 10}, &output)
	require.NoError(t, err)
	require.Equal(t, 1, stats.conflicts)
	require.Empty(t, source.moved)
	require.Contains(t, output.String(), "local event ID conflicts")
	// A fresh outbox at capacity refuses admission without moving SQL ownership.
	full, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "full.bolt"))
	require.NoError(t, err)
	defer full.Close()
	other.ID[0] ^= 1
	_, err = full.Enqueue(localstore.OutboxEntry{Event: other, Target: "control-plane"})
	require.NoError(t, err)
	_, err = transferOutboxRows(ctx, source, full, "", outboxTransferOptions{maxRows: 10, maxPending: 1}, &output)
	require.ErrorContains(t, err, "admission cap")
	require.Empty(t, source.moved)
}
func TestOutboxTransferRequiresExplicitTargetAndConfirmation(t *testing.T) {
	for _, args := range [][]string{{"outbox-transfer"}, {"outbox-transfer", "--target", "default", "--apply"}, {"outbox-transfer", "--target", "wrong"}} {
		var out, errors bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &out, &errors))
		require.True(t, strings.Contains(errors.String(), "requires --target") || strings.Contains(errors.String(), "requires --confirm-quiesced"))
	}
}

func TestOutboxTransferRestartAfterLocalEnqueueBeforeSQLRetarget(t *testing.T) {
	ctx := context.Background()
	row := signedTransferRow(t, time.Unix(1700000000, 0).UTC(), "")
	source := &transferFakeSource{rows: []repository.NostrEventRecord{row}}
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	ev, err := nostradapter.SignedEventFromRecord(row)
	require.NoError(t, err)
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: ev, Target: "", EnqueuedAt: row.ReceivedAt})
	require.NoError(t, err)
	// The old process may have crashed after local bbolt commit but before SQL
	// retarget. A restart recognizes the exact signed event and completes SQL.
	var output bytes.Buffer
	stats, err := transferOutboxRows(ctx, source, outbox, "", outboxTransferOptions{maxRows: 10, maxPending: 10}, &output)
	require.NoError(t, err)
	require.Equal(t, 1, stats.transferred)
	require.Zero(t, stats.conflicts)
	require.Equal(t, []string{row.ID}, source.moved)
	entry, found, err := outbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, ev, entry.Event)
}

func TestOutboxTransferInvalidSignatureIsReportedNotMutated(t *testing.T) {
	row := signedTransferRow(t, time.Unix(1700000000, 0).UTC(), "")
	row.Content = "tampered"
	source := &transferFakeSource{rows: []repository.NostrEventRecord{row}}
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	var output bytes.Buffer
	stats, err := transferOutboxRows(context.Background(), source, outbox, "", outboxTransferOptions{maxRows: 10, maxPending: 10}, &output)
	require.NoError(t, err)
	require.Equal(t, 1, stats.conflicts)
	require.Empty(t, source.moved)
	require.Contains(t, output.String(), "invalid signed event")
}

func TestOutboxInteropRelaysMatchDaemonTargetPolicy(t *testing.T) {
	cfg := &config.Config{}
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.BackendURL = "ws://sidecar.internal"
	cfg.Nostr.Relays = []string{"wss://external.example"}
	cfg.Loom.Relays = []string{"wss://loom.example"}
	require.Equal(t, []string{"ws://sidecar.internal", "wss://external.example", "wss://loom.example"}, outboxInteropRelays(cfg))
	cfg.Nostr.Sidecar.MirrorExternal = true
	require.Equal(t, []string{"ws://sidecar.internal", "wss://loom.example"}, outboxInteropRelays(cfg))
}
