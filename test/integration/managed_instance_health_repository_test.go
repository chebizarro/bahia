//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestManagedInstanceHealthTransactionalWrites(t *testing.T) {
	skipIfNoDatabase(t)
	ctx := context.Background()
	pool := managedInstanceTestPool(t, ctx)
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))

	serviceRepo := repository.NewPgServiceRepository(pool)
	environmentRepo := repository.NewPgEnvironmentRepository(pool)
	healthRepo := repository.NewPgManagedInstanceHealthRepository(pool)
	svc := &domain.Service{Name: "managed-health-tx-" + uuid.NewString(), ArtifactRepo: "example.invalid/test"}
	env := &domain.Environment{Name: "managed-health-tx-" + uuid.NewString()}
	require.NoError(t, serviceRepo.Create(ctx, svc))
	require.NoError(t, environmentRepo.Create(ctx, env))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM services WHERE id = $1", svc.ID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM environments WHERE id = $1", env.ID)
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	key := domain.ManagedInstanceKey{ServiceID: svc.ID, EnvironmentID: env.ID, RuntimeTargetName: "api"}
	unhealthy := &domain.ManagedInstanceHealth{ManagedInstanceKey: key, SupervisorType: domain.InstanceSupervisorDocker, Status: domain.InstanceHealthStatusUnhealthy, LastObservedAt: now}
	invalidEvent := &domain.ManagedInstanceHealthEvent{ID: uuid.New(), ManagedInstanceKey: key, Status: domain.InstanceHealthStatus("invalid"), ObservedAt: now}
	require.Error(t, healthRepo.UpsertHealthWithEvent(ctx, unhealthy, invalidEvent))
	stored, err := healthRepo.GetHealth(ctx, key)
	require.NoError(t, err)
	require.Nil(t, stored, "health upsert must roll back when transition append fails")

	initialEvent := &domain.ManagedInstanceHealthEvent{ID: uuid.New(), ManagedInstanceKey: key, Status: unhealthy.Status, ObservedAt: now}
	require.NoError(t, healthRepo.UpsertHealthWithEvent(ctx, unhealthy, initialEvent))

	pending := &domain.RecoveryAttempt{ID: uuid.New(), ManagedInstanceKey: key, CorrelationID: uuid.NewString(), RequestedAt: now, Result: domain.RecoveryAttemptPending}
	inserted, err := healthRepo.RecordRecoveryAttempt(ctx, pending)
	require.NoError(t, err)
	require.True(t, inserted)
	healthy := &domain.ManagedInstanceHealth{ManagedInstanceKey: key, SupervisorType: domain.InstanceSupervisorDocker, Status: domain.InstanceHealthStatusHealthy, LastObservedAt: now.Add(time.Second), LastRecoveryAttempt: pending}
	completed, err := healthRepo.CompleteRecoveryAttemptWithHealthEvent(ctx, pending.CorrelationID, domain.RecoveryAttemptSuccess, "recovered", healthy, invalidEvent)
	require.Error(t, err)
	require.False(t, completed)
	attempts, err := healthRepo.ListRecentRecoveryAttempts(ctx, key, 10)
	require.NoError(t, err)
	require.Equal(t, domain.RecoveryAttemptPending, attempts[0].Result, "attempt completion must roll back when transition append fails")
	stored, err = healthRepo.GetHealth(ctx, key)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceHealthStatusUnhealthy, stored.Status)

	recoveryEvent := &domain.ManagedInstanceHealthEvent{ID: uuid.New(), ManagedInstanceKey: key, PreviousStatus: unhealthy.Status, Status: healthy.Status, ObservedAt: healthy.LastObservedAt}
	completed, err = healthRepo.CompleteRecoveryAttemptWithHealthEvent(ctx, pending.CorrelationID, domain.RecoveryAttemptSuccess, "recovered", healthy, recoveryEvent)
	require.NoError(t, err)
	require.True(t, completed)
	stored, err = healthRepo.GetHealth(ctx, key)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceHealthStatusHealthy, stored.Status)
}

func managedInstanceTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	var (
		pool *pgxpool.Pool
		err  error
	)
	if databaseURL := os.Getenv("BAHIA_TEST_DB_URL"); databaseURL != "" {
		pool, err = pgxpool.New(ctx, databaseURL)
	} else {
		pool, err = db.Connect(ctx, config.Defaults().DB, zap.NewNop())
	}
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}
