package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
)

// A nonempty SQL table is deliberately a blocker, even when some rows may be
// derived copies of canonical state. A count cannot establish relay ownership.
// Keep this inventory explicit: missing/renamed tables must fail, not count zero.
type legacyFamily struct {
	name   string
	tables []string
}

// security_osv_vulnerability_cache is fetched external reference data, not
// an intent, finding, or publication ledger. It is explicitly excluded.
var legacyFamilies = []legacyFamily{
	{"dns", []string{"dns_policies", "dns_zones", "dns_record_overrides"}},
	{"ml", []string{"ml_models", "ml_model_versions", "ml_artifact_refs", "ml_provenance_edges", "ml_recipes", "ml_recipe_runs", "ml_inference_endpoints", "ml_deployment_intents", "ml_deployment_runs", "ml_inference_observations", "ml_inference_state", "ml_evaluation_specs", "ml_evaluation_runs"}},
	{"adoption", []string{"adopted_runtime_identity"}},
	{"hive-ci", []string{"hiveci_workflow_runs", "hiveci_workflow_results", "hiveci_pipeline_policies", "hiveci_accepted_releases", "hiveci_release_conflicts"}},
	{"security", []string{"security_scan_targets", "security_scan_runs", "security_target_latest", "security_findings", "security_scan_schedules", "security_policy_breaches", "security_observable_publications"}},
	{"policy", []string{"deployment_policies"}},
	{"initiation", []string{"hiveci_initiations"}},
	{"managed-instance", []string{"managed_instance_health", "managed_instance_health_events", "managed_instance_recovery_attempts", "managed_instance_overrides"}},
}

const legacyCutoverMarkerFamily = "migration"
const legacyCutoverMarkerID = "legacy-sql-empty-cutover-v1"

func verifyCutoverOutboxPath(configured, supplied string) (string, error) {
	if !filepath.IsAbs(configured) {
		return "", fmt.Errorf("configured daemon outbox path %q is relative; configure an absolute nostr.local_store.outbox_path (or absolute local_store.path)", configured)
	}
	if !filepath.IsAbs(supplied) {
		return "", fmt.Errorf("--outbox-path %q must be absolute", supplied)
	}
	configured, supplied = filepath.Clean(configured), filepath.Clean(supplied)
	if configured != supplied {
		return "", fmt.Errorf("--outbox-path %q does not match configured daemon outbox %q", supplied, configured)
	}
	info, err := os.Lstat(supplied)
	if err != nil {
		return "", fmt.Errorf("stat daemon outbox: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("daemon outbox must be an existing nonempty regular file")
	}
	return supplied, nil
}

type legacyCount struct {
	Family string `json:"family"`
	Table  string `json:"table"`
	Rows   int64  `json:"rows"`
}

type legacyCutoverReport struct {
	Version             int           `json:"version"`
	CheckedAt           time.Time     `json:"checked_at"`
	Counts              []legacyCount `json:"counts"`
	BlockedFamilies     []string      `json:"blocked_families"`
	PendingSignedOutbox int64         `json:"pending_signed_outbox"`
	// Failed rows may represent an abandoned signed event. They remain a
	// separate operator decision; this command never claims them.
	FailedSignedOutbox   int64 `json:"failed_signed_outbox"`
	EligibleForEmptySeal bool  `json:"eligible_for_empty_seal"`
}

func validEmptyCutoverMarker(report legacyCutoverReport) bool {
	if report.Version != 1 || report.CheckedAt.IsZero() || !report.EligibleForEmptySeal ||
		len(report.BlockedFamilies) != 0 || report.PendingSignedOutbox != 0 || report.FailedSignedOutbox != 0 {
		return false
	}
	want := 0
	for _, family := range legacyFamilies {
		for _, table := range family.tables {
			if want >= len(report.Counts) || report.Counts[want] != (legacyCount{Family: family.name, Table: table, Rows: 0}) {
				return false
			}
			want++
		}
	}
	return len(report.Counts) == want
}

// legacyCounter is small so the read-only census can be tested without a DB.
type legacyCounter interface {
	Count(context.Context, string) (int64, error)
}

type pgLegacyCounter struct{ tx pgx.Tx }

func (c pgLegacyCounter) Count(ctx context.Context, table string) (int64, error) {
	// Names come only from legacyFamilies or the two fixed outbox predicates.
	var query string
	switch table {
	case "nostr_events_pending":
		query = "SELECT count(*) FROM nostr_events WHERE publish_state = 'pending'"
	case "nostr_events_failed":
		query = "SELECT count(*) FROM nostr_events WHERE publish_state = 'failed'"
	default:
		query = "SELECT count(*) FROM " + pgx.Identifier{table}.Sanitize()
	}
	var count int64
	err := c.tx.QueryRow(ctx, query).Scan(&count)
	return count, err
}

func censusLegacy(ctx context.Context, counter legacyCounter, now time.Time) (legacyCutoverReport, error) {
	report := legacyCutoverReport{Version: 1, CheckedAt: now.UTC(), EligibleForEmptySeal: true}
	for _, family := range legacyFamilies {
		blocked := false
		for _, table := range family.tables {
			if err := ctx.Err(); err != nil {
				report.EligibleForEmptySeal = false
				return report, err
			}
			rows, err := counter.Count(ctx, table)
			if err != nil {
				report.EligibleForEmptySeal = false
				return report, fmt.Errorf("count %s/%s: %w", family.name, table, err)
			}
			if rows < 0 {
				report.EligibleForEmptySeal = false
				return report, fmt.Errorf("negative count for %s", table)
			}
			report.Counts = append(report.Counts, legacyCount{Family: family.name, Table: table, Rows: rows})
			blocked = blocked || rows != 0
		}
		if blocked {
			report.BlockedFamilies = append(report.BlockedFamilies, family.name)
			report.EligibleForEmptySeal = false
		}
	}
	var err error
	report.PendingSignedOutbox, err = counter.Count(ctx, "nostr_events_pending")
	if err != nil {
		report.EligibleForEmptySeal = false
		return report, fmt.Errorf("count pending signed outbox: %w", err)
	}
	report.FailedSignedOutbox, err = counter.Count(ctx, "nostr_events_failed")
	if err != nil {
		report.EligibleForEmptySeal = false
		return report, fmt.Errorf("count failed signed outbox: %w", err)
	}
	if report.PendingSignedOutbox < 0 || report.FailedSignedOutbox < 0 {
		report.EligibleForEmptySeal = false
		return report, fmt.Errorf("negative signed outbox count")
	}
	if report.PendingSignedOutbox != 0 || report.FailedSignedOutbox != 0 {
		report.EligibleForEmptySeal = false
	}
	return report, nil
}

func runLegacyCutover(ctx context.Context, pool *pgxpool.Pool, outboxPath string, confirmQuiesced bool, stdout, stderr io.Writer) int {
	// A repeatable-read, read-only snapshot gives internally consistent counts.
	// It cannot prove no writer exists; the explicit quiescence assertion is a
	// prerequisite for a durable empty-state marker.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return reportError(stderr, "legacy-cutover begin census: %v", err)
	}
	defer tx.Rollback(ctx)
	report, err := censusLegacy(ctx, pgLegacyCounter{tx: tx}, time.Now())
	if err != nil {
		return reportError(stderr, "legacy-cutover census incomplete (no marker written): %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return reportError(stderr, "legacy-cutover commit census: %v", err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return reportError(stderr, "legacy-cutover encode census: %v", err)
	}
	if _, err := fmt.Fprintln(stdout, string(encoded)); err != nil {
		return 1
	}
	if !confirmQuiesced {
		_, err = fmt.Fprintln(stdout, "dry-run only: no SQL rows, local records, or relay events changed; --confirm-quiesced can seal an empty inventory only")
		if err != nil {
			return 1
		}
		return 0
	}
	if !report.EligibleForEmptySeal {
		return reportError(stderr, "legacy-cutover blocked: nonempty or unproven SQL state; no migration primitives invoked and no marker written")
	}
	outbox, err := localstore.OpenExistingOutboxStrict(outboxPath)
	if err != nil {
		return reportError(stderr, "legacy-cutover requires intact exclusive local outbox (stop daemon first): %v", err)
	}
	defer outbox.Close()
	existing, err := outbox.GetControlRecord(legacyCutoverMarkerFamily, legacyCutoverMarkerID)
	if err != nil {
		return reportError(stderr, "legacy-cutover read marker: %v", err)
	}
	if existing != nil {
		var previous legacyCutoverReport
		if err := json.Unmarshal(existing, &previous); err != nil || !validEmptyCutoverMarker(previous) {
			return reportError(stderr, "legacy-cutover existing marker is invalid; no overwrite")
		}
		_, err = fmt.Fprintln(stdout, "empty-state cutover already sealed; current SQL census is still empty")
		if err != nil {
			return 1
		}
		return 0
	}
	if err := outbox.PutControlRecord(legacyCutoverMarkerFamily, legacyCutoverMarkerID, encoded); err != nil {
		return reportError(stderr, "legacy-cutover write marker: %v", err)
	}
	if _, err := fmt.Fprintln(stdout, "empty-state cutover sealed locally; this is not proof of migration or relay delivery"); err != nil {
		return 1
	}
	return 0
}
