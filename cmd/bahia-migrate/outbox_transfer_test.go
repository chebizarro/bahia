package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

type inventorySource struct {
	rows  []repository.NostrEventRecord
	calls int
}

func (f *inventorySource) ListUnpublishedAfter(_ context.Context, target string, cursor *repository.NostrOutboxCursor, limit int) ([]repository.NostrEventRecord, error) {
	f.calls++
	var result []repository.NostrEventRecord
	for _, row := range f.rows {
		if row.PublishTarget != target || row.PublishState != repository.NostrPublishStatePending {
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
func signedTransferRow(t *testing.T, at time.Time, target string) repository.NostrEventRecord {
	t.Helper()
	sk := gonostr.Generate()
	ev := gonostr.Event{Kind: 30078, CreatedAt: gonostr.Timestamp(at.Unix()), Tags: gonostr.Tags{{"t", "inventory-test"}}, Content: `{"ok":true}`}
	require.NoError(t, keyer.NewPlainKeySigner(sk).SignEvent(context.Background(), &ev))
	tags, err := json.Marshal(ev.Tags)
	require.NoError(t, err)
	return repository.NostrEventRecord{ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Sig: hex.EncodeToString(ev.Sig[:]), Content: ev.Content, Tags: tags, CreatedAt: at, ReceivedAt: at, PublishTarget: target, PublishState: repository.NostrPublishStatePending}
}
func TestOutboxInventoryContinuationCarriesConflictAcrossPageBoundary(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	invalid := signedTransferRow(t, at, "")
	invalid.Content = "tampered"
	valid := signedTransferRow(t, at.Add(time.Second), "")
	source := &inventorySource{rows: []repository.NostrEventRecord{invalid, valid}}
	var first bytes.Buffer
	stats, err := inventoryOutboxRows(context.Background(), source, "", nil, 1, &first)
	require.NoError(t, err)
	require.Equal(t, 1, stats.conflicts)
	require.Equal(t, 1, stats.cumulativeConflicts)
	require.NotEmpty(t, stats.nextAfter)
	require.Contains(t, first.String(), "invalid signed event")
	cursor, err := decodeOutboxInventoryCursor(stats.nextAfter, "")
	require.NoError(t, err)
	var second bytes.Buffer
	next, err := inventoryOutboxRows(context.Background(), source, "", cursor, 1, &second)
	require.NoError(t, err)
	require.Zero(t, next.conflicts)
	require.Equal(t, 1, next.cumulativeConflicts)
	require.Equal(t, 1, next.signedUnattempted)
	require.NotEmpty(t, next.nextAfter)
	// Even a later page with no new conflict cannot claim a clean scan.
	var output, errors bytes.Buffer
	code := runOutboxTransfer(context.Background(), source, outboxTransferOptions{target: "default", after: stats.nextAfter, maxRows: 1}, &output, &errors)
	require.Equal(t, 1, code)
	require.Contains(t, output.String(), "cumulative_conflicts=1")
}
func TestOutboxInventoryRejectsTargetMismatchAndMalformedCursor(t *testing.T) {
	token, err := encodeOutboxInventoryCursor(outboxInventoryCursor{Target: "control-plane", ReceivedAt: time.Now().UTC(), ID: "abc"})
	require.NoError(t, err)
	_, err = decodeOutboxInventoryCursor(token, "")
	require.ErrorContains(t, err, "does not match")
	_, err = decodeOutboxInventoryCursor("not-base64!", "")
	require.ErrorContains(t, err, "invalid --after")
}
func TestOutboxInventoryIsReadOnlyAndFlagsRejectApply(t *testing.T) {
	source := &inventorySource{rows: []repository.NostrEventRecord{signedTransferRow(t, time.Unix(1700000000, 0).UTC(), "")}}
	var output, errors bytes.Buffer
	code := runOutboxTransfer(context.Background(), source, outboxTransferOptions{target: "default", maxRows: 10}, &output, &errors)
	require.Zero(t, code)
	require.Contains(t, output.String(), "read-only inventory")
	require.Equal(t, repository.NostrPublishStatePending, source.rows[0].PublishState)
	require.Equal(t, "", source.rows[0].PublishTarget)
	for _, args := range [][]string{{"outbox-transfer"}, {"outbox-transfer", "--target", "default", "--confirm-quiesced"}} {
		output.Reset()
		errors.Reset()
		require.Equal(t, 1, run(context.Background(), args, &output, &errors))
		require.True(t, strings.Contains(errors.String(), "requires --target") || strings.Contains(errors.String(), "only valid for f74a-import"))
	}
	output.Reset()
	errors.Reset()
	require.Equal(t, 1, run(context.Background(), []string{"outbox-transfer", "--target", "default", "--apply"}, &output, &errors))
	require.Contains(t, errors.String(), "--apply is disabled")
	require.Contains(t, errors.String(), "no row was claimed or enqueued")
}
func TestOutboxInventoryReportsRecordedAttemptsAndBadSignature(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	attempted := signedTransferRow(t, at, "")
	attempted.PublishAttempts = 1
	bad := signedTransferRow(t, at.Add(time.Second), "")
	bad.Sig = strings.Repeat("0", 128)
	source := &inventorySource{rows: []repository.NostrEventRecord{attempted, bad}}
	var output bytes.Buffer
	stats, err := inventoryOutboxRows(context.Background(), source, "", nil, 10, &output)
	require.NoError(t, err)
	require.Equal(t, 2, stats.conflicts)
	require.Zero(t, stats.signedUnattempted)
	require.Contains(t, output.String(), "relay-attempt state cannot be preserved")
	require.Contains(t, output.String(), "invalid signed event")
}

func TestOutboxInventoryCrashBeforeSQLAttemptCounterUpdateIsStillReadOnly(t *testing.T) {
	// The relay accepted this event, but the old process crashed before the
	// SQL attempt counter was updated. A zero counter is not evidence of no OK.
	row := signedTransferRow(t, time.Unix(1700000000, 0).UTC(), "")
	require.Zero(t, row.PublishAttempts)
	source := &inventorySource{rows: []repository.NostrEventRecord{row}}
	var output, errors bytes.Buffer
	code := runOutboxTransfer(context.Background(), source, outboxTransferOptions{target: "default", maxRows: 10}, &output, &errors)
	require.Zero(t, code)
	require.Contains(t, output.String(), "signed_unattempted=1")
	require.Contains(t, output.String(), "signed_unattempted event_id="+row.ID+" prior_relay_acceptance=unknown")
	require.Contains(t, output.String(), "prior_relay_acceptance=unknown")
	require.Contains(t, output.String(), "no page proves prior relay acceptance")
	require.Equal(t, row, source.rows[0], "inventory must not change SQL ownership or signature")
}

func TestOutboxTransferApplyRefusesBeforeOpeningSources(t *testing.T) {
	for _, args := range [][]string{
		{"outbox-transfer", "--config", "/nonexistent/bahia-transfer.yaml", "--target", "default", "--apply"},
		{"outbox-transfer", "--config", "/nonexistent/bahia-transfer.yaml", "--target", "control-plane", "--after", "stale", "--apply"},
	} {
		var stdout, stderr bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &stdout, &stderr))
		require.Empty(t, stdout.String())
		require.Contains(t, stderr.String(), "--apply is disabled")
		require.NotContains(t, stderr.String(), "loading Bahia config")
	}
}
