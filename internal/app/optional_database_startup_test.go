package app

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func replaceOptionalDBHooks(t *testing.T, connect func(context.Context, config.DBConfig, *zap.Logger) (*pgxpool.Pool, error), migrate func(context.Context, *pgxpool.Pool, *zap.Logger) error) {
	t.Helper()
	originalConnect, originalMigrate := dbConnect, dbMigrate
	dbConnect, dbMigrate = connect, migrate
	t.Cleanup(func() { dbConnect, dbMigrate = originalConnect, originalMigrate })
}

func TestOptionalDatabaseStartupDoesNotWaitForHungConnect(t *testing.T) {
	replaceOptionalDBHooks(t, func(ctx context.Context, _ config.DBConfig, _ *zap.Logger) (*pgxpool.Pool, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, func(context.Context, *pgxpool.Pool, *zap.Logger) error {
		t.Fatal("migration must not run after a failed connection")
		return nil
	})

	started := time.Now()
	pool, available := connectOptionalDatabase(context.Background(), &config.Config{DB: config.DBConfig{StartupProbeTimeout: 20 * time.Millisecond}}, zap.NewNop())
	require.Nil(t, pool)
	require.False(t, available)
	require.Less(t, time.Since(started), 500*time.Millisecond)
}

func TestOptionalDatabaseStartupDoesNotWaitForHungMigration(t *testing.T) {
	replaceOptionalDBHooks(t, func(context.Context, config.DBConfig, *zap.Logger) (*pgxpool.Pool, error) {
		return nil, nil
	}, func(ctx context.Context, _ *pgxpool.Pool, _ *zap.Logger) error {
		<-ctx.Done()
		return ctx.Err()
	})

	started := time.Now()
	pool, available := connectOptionalDatabase(context.Background(), &config.Config{DB: config.DBConfig{StartupProbeTimeout: 20 * time.Millisecond}}, zap.NewNop())
	require.Nil(t, pool)
	require.False(t, available)
	require.Less(t, time.Since(started), 500*time.Millisecond)
}

func TestOptionalDatabaseStartupAdmitsFastMigratedIndex(t *testing.T) {
	var migrated bool
	replaceOptionalDBHooks(t, func(ctx context.Context, _ config.DBConfig, _ *zap.Logger) (*pgxpool.Pool, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		remaining := time.Until(deadline)
		require.Greater(t, remaining, time.Duration(0))
		require.LessOrEqual(t, remaining, 500*time.Millisecond)
		return nil, nil
	}, func(context.Context, *pgxpool.Pool, *zap.Logger) error {
		migrated = true
		return nil
	})

	pool, available := connectOptionalDatabase(context.Background(), &config.Config{DB: config.DBConfig{StartupProbeTimeout: 500 * time.Millisecond}}, zap.NewNop())
	require.Nil(t, pool)
	require.True(t, available)
	require.True(t, migrated)
}

func TestNewKeepsRecoveryNonRequiredWhenOptionalDatabaseStalls(t *testing.T) {
	for _, stalled := range []string{"connect", "migrate"} {
		t.Run(stalled, func(t *testing.T) {
			replaceOptionalDBHooks(t, func(ctx context.Context, _ config.DBConfig, _ *zap.Logger) (*pgxpool.Pool, error) {
				if stalled == "connect" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, nil
			}, func(ctx context.Context, _ *pgxpool.Pool, _ *zap.Logger) error {
				<-ctx.Done()
				return ctx.Err()
			})

			started := time.Now()
			cfg := startupTestConfig("full")
			cfg.DB.StartupProbeTimeout = 20 * time.Millisecond
			app, err := New(cfg)
			require.NoError(t, err)
			defer closeRelayPools(app.relayPools...)
			require.Less(t, time.Since(started), 2*time.Second)
			require.Nil(t, app.DB)
			require.True(t, appHasRunner(app, "database-recovery"))
		})
	}
}

func TestDatabaseRecoveryDoesNotRestartAfterCancelledMigration(t *testing.T) {
	replaceOptionalDBHooks(t, func(context.Context, config.DBConfig, *zap.Logger) (*pgxpool.Pool, error) {
		return nil, nil
	}, func(ctx context.Context, _ *pgxpool.Pool, _ *zap.Logger) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), databaseRecoveryAttemptTimeout)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	recovered := newDatabaseRecoveryRunner(config.DBConfig{}, time.Second, zap.NewNop()).tryRecover(ctx)
	require.False(t, recovered)
}
