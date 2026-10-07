package repository_test

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

// bahia-irsry.40: a Security publication recorded as queued becomes published
// when the outbox delivers its event. A pending run becomes published only
// once none of its publications is pending or failed; a failed_terminal run
// stays failed. A repeat is a no-op.
func TestPgSecurityDeliverPublicationByEventID(t *testing.T) {
	ctx := context.Background()
	pool := abandonedTestPool(t)
	_, err := pool.Exec(ctx, `TRUNCATE security_observable_publications, security_target_latest, security_findings, security_scan_runs, security_scan_targets CASCADE`)
	require.NoError(t, err)

	var targetID string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO security_scan_targets (target_type, target_key, target_key_hash, display) VALUES ('sbom', 'k', 'h', 'd') RETURNING id`).Scan(&targetID))
	newRun := func(state string) string {
		var id string
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO security_scan_runs (target_id, target_key_hash, status, trigger_kind, publish_state) VALUES ($1, 'h', 'cancelled', 'manual', $2) RETURNING id`, targetID, state).Scan(&id))
		return id
	}
	pendingRun, failedRun := newRun("pending"), newRun("failed_terminal")
	insert := func(dTag, runID, state, eventID string) {
		_, err := pool.Exec(ctx, `INSERT INTO security_observable_publications (observable_type, run_id, event_kind, d_tag, schema, publish_state, event_id) VALUES ('summary', $1, 30900, $2, 's', $3, $4)`, runID, dTag, state, eventID)
		require.NoError(t, err)
	}
	insert("first", pendingRun, "pending", "event-first")
	insert("second", pendingRun, "pending", "event-second")
	insert("on-failed-run", failedRun, "pending", "event-on-failed-run")
	insert("abandoned", failedRun, "failed_terminal", "event-abandoned")

	repo := repository.NewPgSecurityRepository(pool)
	state := func(query string, arg any) string {
		var value string
		require.NoError(t, pool.QueryRow(ctx, query, arg).Scan(&value))
		return value
	}
	publication := func(dTag string) string {
		return state(`SELECT publish_state FROM security_observable_publications WHERE d_tag = $1`, dTag)
	}
	run := func(id string) string { return state(`SELECT publish_state FROM security_scan_runs WHERE id = $1`, id) }

	changed, err := repo.DeliverSecurityPublication(ctx, "event-first")
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	require.Equal(t, "published", publication("first"))
	require.Equal(t, "pending", run(pendingRun), "the run waits for its other publication")

	changed, err = repo.DeliverSecurityPublication(ctx, "event-second")
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	require.Equal(t, "published", run(pendingRun))

	changed, err = repo.DeliverSecurityPublication(ctx, "event-on-failed-run")
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	require.Equal(t, "failed_terminal", run(failedRun), "a failed run is not reopened")

	changed, err = repo.DeliverSecurityPublication(ctx, "event-first")
	require.NoError(t, err)
	require.Zero(t, changed, "delivery is reported at least once; a repeat changes nothing")
	changed, err = repo.DeliverSecurityPublication(ctx, "event-abandoned")
	require.NoError(t, err)
	require.Zero(t, changed, "an abandoned publication is never delivered")
}

// A manifest recorded as pending on a queued reference becomes published
// when the outbox delivers the reference, and can still be failed if it is
// abandoned instead.
func TestPgSBOMMarkManifestDeliveredByReferenceEvent(t *testing.T) {
	ctx := context.Background()
	pool := abandonedTestPool(t)
	_, err := pool.Exec(ctx, `TRUNCATE sbom_manifests CASCADE`)
	require.NoError(t, err)
	insert := func(subjectID, referenceEventID, state string) {
		_, err := pool.Exec(ctx, `INSERT INTO sbom_manifests (subject_type, subject_id, subject_digest, format, storage_type, storage_uri, payload_sha256, generator_id, reference_event_id, publish_state, source_kind)
			VALUES ('artifact', $1, 'sha256:'||$1, 'spdx', 'blossom', 'blossom://x', $1, 'syft', $2, $3, 'generated')`, subjectID, referenceEventID, state)
		require.NoError(t, err)
	}
	insert("delivered", "ref-delivered", "pending")
	insert("abandoned", "ref-abandoned", "pending")

	repo := repository.NewPgSBOMRepository(pool)
	changed, err := repo.MarkManifestDeliveredByReferenceEvent(ctx, "ref-delivered")
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	published, err := repo.ListPublishedManifests(ctx, 10)
	require.NoError(t, err)
	require.Len(t, published, 1)
	require.Equal(t, "ref-delivered", published[0].ReferenceEventID)
	require.NotNil(t, published[0].PublishedAt)

	changed, err = repo.FailManifestByReferenceEvent(ctx, "ref-abandoned", "SBOM reference abandoned")
	require.NoError(t, err)
	require.Equal(t, int64(1), changed, "a pending manifest is failed when its reference is abandoned")
	changed, err = repo.MarkManifestDeliveredByReferenceEvent(ctx, "ref-abandoned")
	require.NoError(t, err)
	require.Zero(t, changed, "a failed manifest is not published")
}
