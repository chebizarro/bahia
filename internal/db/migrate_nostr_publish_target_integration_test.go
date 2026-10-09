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

type publishTargetRow struct {
	state, target, lastError string
}

func readPublishTargetRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, withTarget bool) publishTargetRow {
	t.Helper()
	var row publishTargetRow
	if withTarget {
		require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state, publish_target, last_publish_error FROM nostr_events WHERE id = $1`, id).Scan(&row.state, &row.target, &row.lastError))
		return row
	}
	require.NoError(t, pool.QueryRow(ctx, `SELECT publish_state, last_publish_error FROM nostr_events WHERE id = $1`, id).Scan(&row.state, &row.lastError))
	return row
}

// 000071 up tags pending config-fabric rows for the control-plane runner and
// widens the publish-state check (NOT VALID, validated online later) without
// touching indexes; down restores the single-runner schema without leaving any
// row pending for a runner that no longer exists, and up applies again cleanly.
func TestNostrPublishTargetMigrationRoundTrip(t *testing.T) {
	_, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	logger := zap.NewNop()
	require.NoError(t, Migrate(ctx, pool, logger))
	var outboxIndexBefore string
	require.NoError(t, pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_nostr_events_publish_outbox' AND schemaname = current_schema()`).Scan(&outboxIndexBefore))
	// Rolling back to 000070 unwinds every newer migration, including the
	// archive and revision schema, before 000071.
	toBefore071 := DownOptions{Confirm: true, To: "000070_hiveci_initiations"}
	rolled, err := Down(ctx, pool, logger, toBefore071)
	require.NoError(t, err)
	require.Equal(t, []string{"000078_f74a_backdated_successor", "000077_f74a_unit_tombstones", "000076_f74a_observation_archive", "000075_org_strict_revocation", "000074_ml_model_version_revision", "000073_sbom_pending_publication", "000072_security_retire_failed_retryable", "000071_nostr_publish_target"}, rolled)

	insert := func(id, entityType, state, lastError string) {
		_, err := pool.Exec(ctx, `INSERT INTO nostr_events (id, kind, pubkey, content, sig, created_at, entity_type, publish_state, last_publish_error)
			VALUES ($1, 30078, 'pk', '{}', 'sig', now(), $2, $3, $4)`, id, entityType, state, lastError)
		require.NoError(t, err)
	}
	insert("config-pending", "config-fabric.desired", "pending", "wss://cp: connection refused")
	insert("config-published", "config-fabric.desired", "published", "")
	insert("audit-pending", "build.registered", "pending", "")
	insert("inbound", "hiveci_workflow_run", "not_applicable", "")

	require.NoError(t, Migrate(ctx, pool, logger))
	require.Equal(t, publishTargetRow{"pending", "control-plane", "wss://cp: connection refused"}, readPublishTargetRow(t, ctx, pool, "config-pending", true))
	require.Equal(t, publishTargetRow{"published", "", ""}, readPublishTargetRow(t, ctx, pool, "config-published", true))
	require.Equal(t, publishTargetRow{"pending", "", ""}, readPublishTargetRow(t, ctx, pool, "audit-pending", true))
	require.Equal(t, publishTargetRow{"not_applicable", "", ""}, readPublishTargetRow(t, ctx, pool, "inbound", true))
	var indexDef string
	require.NoError(t, pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_nostr_events_publish_outbox' AND schemaname = current_schema()`).Scan(&indexDef))
	require.Equal(t, outboxIndexBefore, indexDef, "the pending outbox index is left untouched")
	var validated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conname = 'nostr_events_publish_state_check' AND connamespace = current_schema()::regnamespace`).Scan(&validated))
	require.False(t, validated, "startup adds the check NOT VALID")
	insert("new-failed", "build.registered", "failed", "abandoned: blocked: no")
	_, err = pool.Exec(ctx, `UPDATE nostr_events SET publish_state = 'bogus' WHERE id = 'inbound'`)
	require.ErrorContains(t, err, "nostr_events_publish_state_check", "NOT VALID still checks new writes")
	_, err = pool.Exec(ctx, `ALTER TABLE nostr_events VALIDATE CONSTRAINT nostr_events_publish_state_check`)
	require.NoError(t, err, "online validation accepts every existing row")

	rolled, err = Down(ctx, pool, logger, toBefore071)
	require.NoError(t, err)
	require.Equal(t, []string{"000078_f74a_backdated_successor", "000077_f74a_unit_tombstones", "000076_f74a_observation_archive", "000075_org_strict_revocation", "000074_ml_model_version_revision", "000073_sbom_pending_publication", "000072_security_retire_failed_retryable", "000071_nostr_publish_target"}, rolled)
	var hasTarget bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'nostr_events' AND column_name = 'publish_target')`).Scan(&hasTarget))
	require.False(t, hasTarget)
	cpRow := readPublishTargetRow(t, ctx, pool, "config-pending", false)
	require.Equal(t, "not_applicable", cpRow.state, "the old interop-only runner must not retry a control-plane row")
	require.Equal(t, "abandoned: publish target control-plane removed by migration rollback", cpRow.lastError)
	require.Equal(t, "pending", readPublishTargetRow(t, ctx, pool, "audit-pending", false).state, "default-target rows stay queued for the interop runner")
	require.Equal(t, publishTargetRow{state: "not_applicable", lastError: "abandoned: blocked: no"}, readPublishTargetRow(t, ctx, pool, "new-failed", false))
	require.NoError(t, pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_nostr_events_publish_outbox' AND schemaname = current_schema()`).Scan(&indexDef))
	require.Equal(t, outboxIndexBefore, indexDef)
	_, err = pool.Exec(ctx, `UPDATE nostr_events SET publish_state = 'failed' WHERE id = 'inbound'`)
	require.ErrorContains(t, err, "nostr_events_publish_state_check", "the pre-000071 constraint has no failed state")
	_, err = pool.Exec(ctx, `ALTER TABLE nostr_events VALIDATE CONSTRAINT nostr_events_publish_state_check`)
	require.NoError(t, err, "rolled-back rows satisfy the restored constraint")

	require.NoError(t, Migrate(ctx, pool, logger))
	require.Equal(t, publishTargetRow{"not_applicable", "", "abandoned: blocked: no"}, readPublishTargetRow(t, ctx, pool, "new-failed", true))
}
