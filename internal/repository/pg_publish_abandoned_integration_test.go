package repository_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func abandonedTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL publish-abandonment test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, db.Migrate(context.Background(), pool, zap.NewNop()))
	return pool
}

// A Security publication recorded as queued under its event id becomes
// failed_terminal, together with its run's publish state, when the outbox
// abandons that event; other publications are untouched and a repeat is a
// no-op.
func TestPgSecurityAbandonPublicationByEventID(t *testing.T) {
	ctx := context.Background()
	pool := abandonedTestPool(t)
	_, err := pool.Exec(ctx, `TRUNCATE security_observable_publications, security_target_latest, security_findings, security_scan_runs, security_scan_targets CASCADE`)
	require.NoError(t, err)

	var targetID, runID, otherRunID string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO security_scan_targets (target_type, target_key, target_key_hash, display) VALUES ('sbom', 'k', 'h', 'd') RETURNING id`).Scan(&targetID))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO security_scan_runs (target_id, target_key_hash, status, trigger_kind, publish_state) VALUES ($1, 'h', 'completed', 'manual', 'published') RETURNING id`, targetID).Scan(&runID))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO security_scan_runs (target_id, target_key_hash, status, trigger_kind, publish_state) VALUES ($1, 'h', 'completed', 'manual', 'published') RETURNING id`, targetID).Scan(&otherRunID))
	insert := func(dTag, runID, state, eventID string) {
		_, err := pool.Exec(ctx, `INSERT INTO security_observable_publications (observable_type, run_id, event_kind, d_tag, schema, publish_state, event_id) VALUES ('summary', $1, 30900, $2, 's', $3, $4)`, runID, dTag, state, eventID)
		require.NoError(t, err)
	}
	insert("queued", runID, "pending", "event-abandoned")
	insert("other-queued", otherRunID, "pending", "event-still-queued")
	insert("delivered", otherRunID, "published", "event-delivered")

	repo := repository.NewPgSecurityRepository(pool)
	changed, err := repo.AbandonSecurityPublication(ctx, "event-abandoned", "abandoned by the outbox")
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)

	var state, lastError string
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state, last_error FROM security_observable_publications WHERE d_tag = 'queued'`).Scan(&state, &lastError))
	require.Equal(t, "failed_terminal", state)
	require.Equal(t, "abandoned by the outbox", lastError)
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state FROM security_scan_runs WHERE id = $1`, runID).Scan(&state))
	require.Equal(t, "failed_terminal", state, "the run no longer reads as published")
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state FROM security_scan_runs WHERE id = $1`, otherRunID).Scan(&state))
	require.Equal(t, "published", state)
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state FROM security_observable_publications WHERE d_tag = 'other-queued'`).Scan(&state))
	require.Equal(t, "pending", state)

	changed, err = repo.AbandonSecurityPublication(ctx, "event-abandoned", "again")
	require.NoError(t, err)
	require.Zero(t, changed, "already terminal")
	changed, err = repo.AbandonSecurityPublication(ctx, "event-delivered", "late")
	require.NoError(t, err)
	require.Zero(t, changed, "a delivered publication is never abandoned")
}

// A manifest published on a reference the outbox later abandoned becomes
// failed and drops out of the published set.
func TestPgSBOMFailManifestByReferenceEvent(t *testing.T) {
	ctx := context.Background()
	pool := abandonedTestPool(t)
	_, err := pool.Exec(ctx, `TRUNCATE sbom_manifests CASCADE`)
	require.NoError(t, err)
	insert := func(subjectID, referenceEventID string) {
		_, err := pool.Exec(ctx, `INSERT INTO sbom_manifests (subject_type, subject_id, subject_digest, format, storage_type, storage_uri, payload_sha256, generator_id, reference_event_id, publish_state, source_kind)
			VALUES ('artifact', $1, 'sha256:'||$1, 'spdx', 'blossom', 'blossom://x', $1, 'syft', $2, 'published', 'generated')`, subjectID, referenceEventID)
		require.NoError(t, err)
	}
	insert("a", "ref-abandoned")
	insert("b", "ref-delivered")

	repo := repository.NewPgSBOMRepository(pool)
	changed, err := repo.FailManifestByReferenceEvent(ctx, "ref-abandoned", "SBOM reference abandoned")
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	published, err := repo.ListPublishedManifests(ctx, 10)
	require.NoError(t, err)
	require.Len(t, published, 1)
	require.Equal(t, "ref-delivered", published[0].ReferenceEventID)
	var state, publishError string
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state, publish_error FROM sbom_manifests WHERE subject_id = 'a'`).Scan(&state, &publishError))
	require.Equal(t, "failed", state)
	require.Equal(t, "SBOM reference abandoned", publishError)
	changed, err = repo.FailManifestByReferenceEvent(ctx, "ref-abandoned", "again")
	require.NoError(t, err)
	require.Zero(t, changed)
}
