package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeLegacyCounter struct {
	counts   map[string]int64
	errTable string
	seen     []string
}

func (c *fakeLegacyCounter) Count(_ context.Context, table string) (int64, error) {
	c.seen = append(c.seen, table)
	if table == c.errTable {
		return 0, errors.New("unavailable")
	}
	return c.counts[table], nil
}

func TestLegacyCutoverCensusEmptyIsEligibleButDoesNotAssertMigration(t *testing.T) {
	counter := &fakeLegacyCounter{counts: map[string]int64{}}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	report, err := censusLegacy(context.Background(), counter, at)
	require.NoError(t, err)
	require.True(t, report.EligibleForEmptySeal)
	require.True(t, validEmptyCutoverMarker(report))
	require.Equal(t, at, report.CheckedAt)
	require.Empty(t, report.BlockedFamilies)
	require.Len(t, counter.seen, len(report.Counts)+2)
	require.Equal(t, "nostr_events_pending", counter.seen[len(counter.seen)-2])
	require.Equal(t, "nostr_events_failed", counter.seen[len(counter.seen)-1])
	names := map[string]bool{}
	for _, count := range report.Counts {
		require.False(t, names[count.Table], "duplicate table %s", count.Table)
		names[count.Table] = true
	}
	for _, table := range []string{"dns_zones", "ml_models", "adopted_runtime_identity", "hiveci_pipeline_policies", "security_scan_targets", "security_observable_publications", "deployment_policies", "hiveci_initiations", "managed_instance_health"} {
		require.True(t, names[table], "missing family table %s", table)
	}
	require.False(t, names["security_osv_vulnerability_cache"], "fetched OSV reference cache is not a cutover source")
}

func TestLegacyCutoverSecurityPublicationLedgerBlocksSeal(t *testing.T) {
	report, err := censusLegacy(context.Background(), &fakeLegacyCounter{counts: map[string]int64{"security_observable_publications": 1}}, time.Now())
	require.NoError(t, err)
	require.Equal(t, []string{"security"}, report.BlockedFamilies)
	require.False(t, report.EligibleForEmptySeal)
	require.False(t, validEmptyCutoverMarker(report))
}

func TestVerifyCutoverOutboxPathRequiresExistingAbsoluteDaemonPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.bolt")
	_, err := verifyCutoverOutboxPath(path, path)
	require.ErrorContains(t, err, "stat daemon outbox")
	require.NoError(t, os.WriteFile(path, []byte("existing"), 0o600))
	verified, err := verifyCutoverOutboxPath(path, path)
	require.NoError(t, err)
	require.Equal(t, path, verified)
	_, err = verifyCutoverOutboxPath("relative/outbox.bolt", path)
	require.ErrorContains(t, err, "relative")
	_, err = verifyCutoverOutboxPath(path, "relative/outbox.bolt")
	require.ErrorContains(t, err, "must be absolute")
	_, err = verifyCutoverOutboxPath(path, filepath.Join(filepath.Dir(path), "other.bolt"))
	require.ErrorContains(t, err, "does not match")
	link := filepath.Join(filepath.Dir(path), "link.bolt")
	require.NoError(t, os.Symlink(path, link))
	_, err = verifyCutoverOutboxPath(link, link)
	require.ErrorContains(t, err, "regular file")
}

func TestLegacyCutoverCensusBlocksNonemptyFamiliesAndSignedOutbox(t *testing.T) {
	counter := &fakeLegacyCounter{counts: map[string]int64{
		"dns_zones": 3, "ml_models": 2, "nostr_events_pending": 1,
	}}
	report, err := censusLegacy(context.Background(), counter, time.Now())
	require.NoError(t, err)
	require.False(t, report.EligibleForEmptySeal)
	require.False(t, validEmptyCutoverMarker(report))
	require.Equal(t, []string{"dns", "ml"}, report.BlockedFamilies)
	require.Equal(t, int64(1), report.PendingSignedOutbox)
	require.Len(t, counter.seen, len(report.Counts)+2, "do not stop scanning after first blocker")
}

func TestLegacyCutoverRejectsIncompleteOrForgedMarker(t *testing.T) {
	report, err := censusLegacy(context.Background(), &fakeLegacyCounter{counts: map[string]int64{}}, time.Now())
	require.NoError(t, err)
	require.True(t, validEmptyCutoverMarker(report))
	report.Counts = report.Counts[:len(report.Counts)-1]
	require.False(t, validEmptyCutoverMarker(report))
}

func TestLegacyCutoverCensusFailsClosedOnMissingTableOrCancellation(t *testing.T) {
	counter := &fakeLegacyCounter{counts: map[string]int64{}, errTable: "security_scan_targets"}
	report, err := censusLegacy(context.Background(), counter, time.Now())
	require.ErrorContains(t, err, "security/security_scan_targets")
	require.False(t, report.EligibleForEmptySeal)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = censusLegacy(ctx, &fakeLegacyCounter{counts: map[string]int64{}}, time.Now())
	require.ErrorIs(t, err, context.Canceled)
}

func TestLegacyCutoverCommandRecognizedAndSealFlagScoped(t *testing.T) {
	for _, args := range [][]string{{"legacy-cutover", "--config", "missing.yaml"}, {"--config", "missing.yaml", "--confirm-quiesced", "--outbox-path", "/absolute/outbox.bolt", "legacy-cutover"}} {
		var out, stderr bytes.Buffer
		require.Equal(t, 1, run(context.Background(), args, &out, &stderr))
		require.NotContains(t, stderr.String(), "unknown migration action")
		require.Contains(t, stderr.String(), "loading Bahia config")
	}
	var out, stderr bytes.Buffer
	require.Equal(t, 1, run(context.Background(), []string{"status", "--confirm-quiesced"}, &out, &stderr))
	require.True(t, strings.Contains(stderr.String(), "only valid for f74a-import or legacy-cutover"))
	stderr.Reset()
	require.Equal(t, 1, run(context.Background(), []string{"legacy-cutover", "--confirm-quiesced"}, &out, &stderr))
	require.Contains(t, stderr.String(), "requires --outbox-path")
}
