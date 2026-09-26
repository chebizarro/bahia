//go:build integration

package db

import (
	"context"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func migrationFileCopy(t *testing.T) fstest.MapFS {
	t.Helper()
	copy := fstest.MapFS{}
	err := fs.WalkDir(migrationsFS, "migrations", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := migrationsFS.ReadFile(path)
		if err == nil {
			copy[path] = &fstest.MapFile{Data: content}
		}
		return err
	})
	require.NoError(t, err)
	return copy
}

func TestMigrationStatusReadOnly(t *testing.T) {
	_, pool := migrationPostgres(t)
	ctx := t.Context()
	status, err := Status(ctx, pool, zap.NewNop())
	require.NoError(t, err)
	require.Empty(t, status.Applied)
	require.Equal(t, migrationVersions(t), status.Pending)
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists))
	require.False(t, exists, "status must not create tracking table")
	require.NoError(t, Migrate(ctx, pool, zap.NewNop()))
	status, err = Status(ctx, pool, zap.NewNop())
	require.NoError(t, err)
	require.Empty(t, status.Pending)
	require.Len(t, status.Applied, len(migrationVersions(t)))
	for _, item := range status.Applied {
		require.False(t, item.AppliedAt.IsZero(), item.Version)
	}
}

func TestMigrationDownGuardedRoundTrips(t *testing.T) {
	_, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	logger := zap.NewNop()
	require.NoError(t, Migrate(ctx, pool, logger))

	// 000070 has a locking emptiness guard, not a bare DROP TABLE.
	_, err := Down(ctx, pool, logger, DownOptions{})
	require.ErrorContains(t, err, "confirmation")
	_, err = pool.Exec(ctx, "INSERT INTO hiveci_initiations (source_event_id, build_id, stage, document) VALUES ($1, $2, $3, $4)",
		"guarded-event", "00000000-0000-0000-0000-000000000001", "claimed", []byte{1})
	require.NoError(t, err)
	_, err = Down(ctx, pool, logger, DownOptions{Confirm: true})
	require.ErrorContains(t, err, "HiveCI initiation rollback refused")
	var guardedCount int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = '000070_hiveci_initiations'").Scan(&guardedCount))
	require.Equal(t, 1, guardedCount)
	_, err = pool.Exec(ctx, "DELETE FROM hiveci_initiations WHERE source_event_id = 'guarded-event'")
	require.NoError(t, err)
	rolled, err := Down(ctx, pool, logger, DownOptions{Confirm: true})
	require.NoError(t, err)
	require.Equal(t, []string{"000070_hiveci_initiations"}, rolled)
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('hiveci_initiations') IS NOT NULL").Scan(&exists))
	require.False(t, exists)
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = $1", rolled[0]).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, Migrate(ctx, pool, logger))
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('hiveci_initiations') IS NOT NULL").Scan(&exists))
	require.True(t, exists)
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = $1", rolled[0]).Scan(&count))
	require.Equal(t, 1, count)

	// Down-to retains the exact named stem and unwinds everything newer.
	rolled, err = Down(ctx, pool, logger, DownOptions{Confirm: true, To: "000065_runtime_release_deployment_intents"})
	require.NoError(t, err)
	require.Equal(t, []string{
		"000070_hiveci_initiations", "000069_package_authorization", "000068_relay_projection_wire_time",
		"000067_vm_measured_adoption", "000066_vm_control_plane",
	}, rolled)
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('virtualization_hosts') IS NOT NULL").Scan(&exists))
	require.False(t, exists)
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = '000066_vm_control_plane'").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, Migrate(ctx, pool, logger))
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('virtualization_hosts') IS NOT NULL").Scan(&exists))
	require.True(t, exists)
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = '000066_vm_control_plane'").Scan(&count))
	require.Equal(t, 1, count)
}

func TestMigrationDownMissingAndAtomicFailure(t *testing.T) {
	_, pool := migrationPostgres(t)
	ctx := t.Context()
	logger := zap.NewNop()
	require.NoError(t, Migrate(ctx, pool, logger))
	files := migrationFileCopy(t)
	delete(files, "migrations/000066_vm_control_plane.down.sql")
	_, err := downWithFS(ctx, pool, logger, files, DownOptions{Confirm: true, To: "000065_runtime_release_deployment_intents"})
	require.ErrorContains(t, err, "missing down migration")
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count))
	require.Equal(t, len(migrationVersions(t)), count)

	files = migrationFileCopy(t)
	files["migrations/000070_hiveci_initiations.down.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE down_failure_marker (id int); SELECT 1/0;")}
	_, err = downWithFS(ctx, pool, logger, files, DownOptions{Confirm: true})
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "22012", pgErr.Code)
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('down_failure_marker') IS NOT NULL").Scan(&exists))
	require.False(t, exists, "failed down SQL must roll back its DDL")
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = '000070_hiveci_initiations'").Scan(&count))
	require.Equal(t, 1, count)
}

func TestMigrationDownRefusesOutOfOrderHistory(t *testing.T) {
	_, pool := migrationPostgres(t)
	ctx := t.Context()
	logger := zap.NewNop()
	require.NoError(t, Migrate(ctx, pool, logger))
	_, err := pool.Exec(ctx, "UPDATE schema_migrations SET applied_at = now() + interval '1 hour' WHERE version = '000066_vm_control_plane'")
	require.NoError(t, err)
	_, err = Down(ctx, pool, logger, DownOptions{Confirm: true})
	require.ErrorContains(t, err, "not the latest applied")
	// Force bypasses ordering policy only. The SQL still fails on dependencies
	// from newer migrations, and its transaction preserves the version row.
	_, err = Down(ctx, pool, logger, DownOptions{Confirm: true, Force: true})
	require.ErrorContains(t, err, "other objects depend on it")
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = '000066_vm_control_plane'").Scan(&count))
	require.Equal(t, 1, count)
}

func TestMigrationAllDownScripts(t *testing.T) {
	_, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	logger := zap.NewNop()
	require.NoError(t, Migrate(ctx, pool, logger))
	versions := migrationVersions(t)
	for i := len(versions) - 1; i >= 0; i-- {
		rolled, err := Down(ctx, pool, logger, DownOptions{Confirm: true})
		require.NoError(t, err, versions[i])
		require.Equal(t, []string{versions[i]}, rolled)
	}
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, Migrate(ctx, pool, logger))
}

func TestMigrationDownSharesStartupLock(t *testing.T) {
	admin, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	logger := zap.NewNop()
	require.NoError(t, Migrate(ctx, pool, logger))
	holder, err := admin.Acquire(ctx)
	require.NoError(t, err)
	defer holder.Release()
	_, err = holder.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey)
	require.NoError(t, err)
	defer func() {
		unlock, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, err := holder.Exec(unlock, "SELECT pg_advisory_unlock($1)", migrationLockKey)
		require.NoError(t, err)
	}()
	waitCtx, cancelWait := context.WithCancel(ctx)
	var runners sync.WaitGroup
	defer func() { cancelWait(); runners.Wait() }()
	result := make(chan error, 1)
	runners.Go(func() {
		_, err := Down(waitCtx, pool, logger, DownOptions{Confirm: true})
		result <- err
	})
	waitForMigrationLockWaiter(t, ctx, admin, result)
	cancelWait()
	require.ErrorIs(t, <-result, context.Canceled)
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = '000070_hiveci_initiations'").Scan(&count))
	require.Equal(t, 1, count)
}
