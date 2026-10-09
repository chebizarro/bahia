//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const securityRetryIndex = "idx_security_observable_publications_retry"

type securityPublicationRow struct {
	state     string
	lastError *string
	retryAt   *time.Time
}

func readSecurityPublication(t *testing.T, ctx context.Context, pool *pgxpool.Pool, dTag string) securityPublicationRow {
	t.Helper()
	var row securityPublicationRow
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state, last_error, next_retry_at FROM security_observable_publications WHERE d_tag = $1`, dTag).Scan(&row.state, &row.lastError, &row.retryAt))
	return row
}

func securityConstraintValidated(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var validated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conname = $1 AND connamespace = current_schema()::regnamespace`, name).Scan(&validated))
	return validated
}

// 000072 up converts leftover failed_retryable publications through the retry
// partial index, drops that index and narrows both Security publish-state
// checks NOT VALID; the out-of-band half (ensure-indexes) converts leftover
// runs and validates. Down widens the checks again (NOT VALID, no index
// build), and up applies cleanly afterwards.
func TestSecurityRetireFailedRetryableMigrationRoundTrip(t *testing.T) {
	_, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	logger := zap.NewNop()
	require.NoError(t, Migrate(ctx, pool, logger))
	// Rolling back to 000071 unwinds all newer schema before 000072.
	toBefore072 := DownOptions{Confirm: true, To: "000071_nostr_publish_target"}
	rolled, err := Down(ctx, pool, logger, toBefore072)
	require.NoError(t, err)
	require.Equal(t, []string{"000077_f74a_unit_tombstones", "000076_f74a_observation_archive", "000075_org_strict_revocation", "000074_ml_model_version_revision", "000073_sbom_pending_publication", "000072_security_retire_failed_retryable"}, rolled)
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, securityRetryIndex).Scan(&exists))
	require.False(t, exists, "the rollback does not rebuild the retired index")
	// Recreate it exactly as 000044 did, so the schema matches a database
	// that never ran 000072.
	_, err = pool.Exec(ctx, `CREATE INDEX `+securityRetryIndex+` ON security_observable_publications(publish_state, next_retry_at) WHERE publish_state = 'failed_retryable'`)
	require.NoError(t, err)

	var targetID string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO security_scan_targets (target_type, target_key, target_key_hash, display) VALUES ('sbom', 'k', 'h', 'd') RETURNING id`).Scan(&targetID))
	insertRun := func(state string) string {
		var id string
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO security_scan_runs (target_id, target_key_hash, status, trigger_kind, publish_state) VALUES ($1, 'h', 'completed', 'manual', $2) RETURNING id`, targetID, state).Scan(&id))
		return id
	}
	retryRun := insertRun("failed_retryable")
	publishedRun := insertRun("published")
	insertPublication := func(dTag, state string, lastError *string) {
		_, err := pool.Exec(ctx, `INSERT INTO security_observable_publications (observable_type, run_id, event_kind, d_tag, schema, publish_state, last_error, next_retry_at)
			VALUES ('audit', $1, 4903, $2, 's', $3, $4, CASE WHEN $3 = 'failed_retryable' THEN now() ELSE NULL END)`, retryRun, dTag, state, lastError)
		require.NoError(t, err)
	}
	relayDown := "wss://relay: connection refused"
	insertPublication("retry-with-error", "failed_retryable", &relayDown)
	insertPublication("retry-no-error", "failed_retryable", nil)
	insertPublication("pending", "pending", nil)

	require.NoError(t, Migrate(ctx, pool, logger))
	const retired = "failed_retryable retired by migration 000072, the outbox owns retries"
	withError := readSecurityPublication(t, ctx, pool, "retry-with-error")
	require.Equal(t, "failed_terminal", withError.state)
	require.Equal(t, relayDown+" | "+retired, *withError.lastError, "the original failure is kept")
	require.Nil(t, withError.retryAt)
	noError := readSecurityPublication(t, ctx, pool, "retry-no-error")
	require.Equal(t, "failed_terminal", noError.state)
	require.Equal(t, retired, *noError.lastError)
	require.Equal(t, "pending", readSecurityPublication(t, ctx, pool, "pending").state)
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, securityRetryIndex).Scan(&exists))
	require.False(t, exists, "the retry index is retired")
	for _, name := range []string{"security_observable_publications_publish_state_check", "security_scan_runs_publish_state_check"} {
		require.False(t, securityConstraintValidated(t, ctx, pool, name), "startup adds %s NOT VALID", name)
	}
	_, err = pool.Exec(ctx, `UPDATE security_observable_publications SET publish_state = 'failed_retryable' WHERE d_tag = 'pending'`)
	require.ErrorContains(t, err, "security_observable_publications_publish_state_check", "NOT VALID still checks new writes")
	_, err = pool.Exec(ctx, `UPDATE security_scan_runs SET publish_state = 'failed_retryable' WHERE id = $1`, publishedRun)
	require.ErrorContains(t, err, "security_scan_runs_publish_state_check")

	// The leftover run is converted out of band, not by the startup migration.
	var runState string
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state FROM security_scan_runs WHERE id = $1`, retryRun).Scan(&runState))
	require.Equal(t, "failed_retryable", runState)
	_, err = pool.Exec(ctx, `ALTER TABLE security_scan_runs VALIDATE CONSTRAINT security_scan_runs_publish_state_check`)
	require.ErrorContains(t, err, "security_scan_runs_publish_state_check", "validation needs the out-of-band conversion first")
	for range 2 { // idempotent
		for _, statement := range repository.SecurityPublishStateValidationStatements() {
			_, err := pool.Exec(ctx, statement)
			require.NoError(t, err, statement)
		}
	}
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state FROM security_scan_runs WHERE id = $1`, retryRun).Scan(&runState))
	require.Equal(t, "failed_terminal", runState)
	for _, name := range []string{"security_observable_publications_publish_state_check", "security_scan_runs_publish_state_check"} {
		require.True(t, securityConstraintValidated(t, ctx, pool, name), "ensure-indexes validates %s", name)
	}

	rolled, err = Down(ctx, pool, logger, toBefore072)
	require.NoError(t, err)
	require.Equal(t, []string{"000077_f74a_unit_tombstones", "000076_f74a_observation_archive", "000075_org_strict_revocation", "000074_ml_model_version_revision", "000073_sbom_pending_publication", "000072_security_retire_failed_retryable"}, rolled)
	for _, name := range []string{"security_observable_publications_publish_state_check", "security_scan_runs_publish_state_check"} {
		require.False(t, securityConstraintValidated(t, ctx, pool, name), "rollback restores %s NOT VALID", name)
	}
	_, err = pool.Exec(ctx, `UPDATE security_observable_publications SET publish_state = 'failed_retryable' WHERE d_tag = 'pending'`)
	require.NoError(t, err, "the restored check accepts failed_retryable again")
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, securityRetryIndex).Scan(&exists))
	require.False(t, exists, "rollback builds no index")

	require.NoError(t, Migrate(ctx, pool, logger))
	require.Equal(t, "failed_terminal", readSecurityPublication(t, ctx, pool, "pending").state, "reapplying retires the row written after rollback")
}
