//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// 000073 lets a manifest wait as 'pending' on a queued reference; down maps
// pending back to 'published' (what a queued reference meant before) and
// restores the narrower check, and up applies cleanly afterwards.
func TestSBOMPendingPublicationMigrationRoundTrip(t *testing.T) {
	_, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	logger := zap.NewNop()
	require.NoError(t, Migrate(ctx, pool, logger))

	insertManifest(t, ctx, pool, "pending-subject", "pending")
	insertManifest(t, ctx, pool, "published-subject", "published")
	_, err := pool.Exec(ctx, `UPDATE sbom_manifests SET publish_state = 'bogus' WHERE subject_id = 'pending-subject'`)
	require.ErrorContains(t, err, "sbom_manifests_publish_state_check")

	rolled, err := Down(ctx, pool, logger, DownOptions{Confirm: true, To: "000072_security_retire_failed_retryable"})
	require.NoError(t, err)
	require.Equal(t, []string{"000081_f74a_confirmed_deletion_seam", "000080_service_secret_data_keys", "000079_f74a_hot_observation_immutable", "000078_f74a_backdated_successor", "000077_f74a_unit_tombstones", "000076_f74a_observation_archive", "000075_org_strict_revocation", "000074_ml_model_version_revision", "000073_sbom_pending_publication"}, rolled)
	require.Equal(t, "published", manifestState(t, ctx, pool, "pending-subject"))
	require.Equal(t, "published", manifestState(t, ctx, pool, "published-subject"))
	_, err = pool.Exec(ctx, `UPDATE sbom_manifests SET publish_state = 'pending' WHERE subject_id = 'published-subject'`)
	require.ErrorContains(t, err, "sbom_manifests_publish_state_check", "the restored check refuses pending")

	require.NoError(t, Migrate(ctx, pool, logger))
	_, err = pool.Exec(ctx, `UPDATE sbom_manifests SET publish_state = 'pending' WHERE subject_id = 'published-subject'`)
	require.NoError(t, err)
	var validated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conname = 'sbom_manifests_publish_state_check' AND connamespace = current_schema()::regnamespace`).Scan(&validated))
	require.True(t, validated, "the small table's check is added validated")
}

func insertManifest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, subjectID, state string) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO sbom_manifests (subject_type, subject_id, subject_digest, format, storage_type, storage_uri, payload_sha256, generator_id, source_kind, publish_state)
		VALUES ('artifact', $1, 'sha256:abc', 'spdx', 'blossom', 'https://blossom.example/x', 'deadbeef', 'syft', 'generated', $2)`, subjectID, state)
	require.NoError(t, err)
}

func manifestState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, subjectID string) string {
	t.Helper()
	var state string
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state FROM sbom_manifests WHERE subject_id = $1`, subjectID).Scan(&state))
	return state
}
