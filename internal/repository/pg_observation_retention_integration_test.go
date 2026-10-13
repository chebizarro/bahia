//go:build integration

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestRuntimeObservationRetentionBatchesAndRestart(t *testing.T) {
	pool, _ := f74aArchiveDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	serviceID, envID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO services(id,name,artifact_repo) VALUES ($1,$2,'retention-test')`, serviceID, "retention-"+serviceID.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO environments(id,name) VALUES ($1,$2)`, envID, "retention-"+envID.String())
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	cutoff := now.Add(-30 * 24 * time.Hour)
	insert := func(at time.Time) uuid.UUID {
		id := uuid.New()
		obs := &domain.RuntimeObservation{ID: id, ServiceID: serviceID, EnvironmentID: envID,
			ObservedImageDigest: "sha256:aaaa", HealthStatus: domain.HealthStatusHealthy,
			Source: "runtime", ObservedAt: at}
		require.NoError(t, repository.NewPgRuntimeObservationRepository(pool).Create(ctx, obs))
		return id
	}
	linked := insert(cutoff.Add(-time.Hour))
	for i := 0; i < 5; i++ {
		insert(cutoff.Add(-time.Duration(i+2) * time.Hour))
	}
	recent := insert(cutoff.Add(time.Hour))
	_, err = pool.Exec(ctx, `INSERT INTO environment_service_state(service_id,environment_id,current_observation_id) VALUES ($1,$2,$3)`, serviceID, envID, linked)
	require.NoError(t, err)

	pruner := repository.NewPgRuntimeObservationRepository(pool)
	count, err := pruner.DeleteExpiredBatch(ctx, cutoff, 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	// A new repository instance resumes from SQL state, not a process cursor.
	pruner = repository.NewPgRuntimeObservationRepository(pool)
	count, err = pruner.DeleteExpiredBatch(ctx, cutoff, 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	count, err = pruner.DeleteExpiredBatch(ctx, cutoff, 2)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	count, err = pruner.DeleteExpiredBatch(ctx, cutoff, 2)
	require.NoError(t, err)
	require.Zero(t, count)
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE service_id=$1 AND environment_id=$2`, serviceID, envID).Scan(&remaining))
	require.Equal(t, 2, remaining)
	for _, id := range []uuid.UUID{linked, recent} {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_observations WHERE id=$1)`, id).Scan(&exists))
		require.True(t, exists)
	}
	_, err = pruner.DeleteExpiredBatch(ctx, cutoff, 0)
	require.Error(t, err)
}
