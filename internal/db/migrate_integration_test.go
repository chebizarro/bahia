//go:build integration

package db

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Use a disposable PostgreSQL 16 database, like the VM integration fixtures.
// Run with -tags=integration -p 1: pgcrypto creation is database-wide.
func migrationPostgres(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("BAHIA_MIGRATE_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("BAHIA_VM_TEST_DATABASE_URL")
	}
	require.NotEmpty(t, dsn, "integration requires BAHIA_MIGRATE_TEST_DATABASE_URL")
	admin, err := pgxpool.New(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	schema := "migrate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(t.Context(), `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return admin, pool
}

func migrationVersions(t *testing.T) []string {
	t.Helper()
	files, err := fs.Glob(migrationsFS, "migrations/*.up.sql")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	versions := make([]string, 0, len(files))
	for _, file := range files {
		versions = append(versions, strings.TrimSuffix(strings.TrimPrefix(file, "migrations/"), ".up.sql"))
	}
	return versions
}

func migrationLockCount(ctx context.Context, pool *pgxpool.Pool, granted bool) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		AND classid = $1::oid AND objid = $2::oid AND objsubid = 1 AND granted = $3`,
		migrationLockKey>>32, migrationLockKey&0xffffffff, granted).Scan(&count)
	return count, err
}

func waitForMigrationLockWaiter(t *testing.T, ctx context.Context, admin *pgxpool.Pool, result <-chan error) {
	t.Helper()
	// Observe the server's actual wait queue, not a sleep or a goroutine-start
	// signal. The caller holds the leader at a known boundary until this returns.
	for {
		select {
		case err := <-result:
			t.Fatalf("second Migrate returned instead of waiting for the advisory lock: %v", err)
		default:
		}
		count, err := migrationLockCount(ctx, admin, false)
		require.NoError(t, err, "waiting for PostgreSQL to report a blocked migration lock")
		if count == 1 {
			return
		}
	}
}

func requireMigrationsApplied(t *testing.T, ctx context.Context, admin, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count))
	require.Equal(t, len(migrationVersions(t)), count)
	locks, err := migrationLockCount(ctx, admin, true)
	require.NoError(t, err)
	require.Zero(t, locks, "completed runs must not leave session locks in the pool")
}

func TestMigrateConcurrentRunners(t *testing.T) {
	admin, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	var runners sync.WaitGroup
	defer func() {
		cancel()
		runners.Wait()
	}()
	firstCore, firstLogs := observer.New(zap.DebugLevel)
	secondCore, secondLogs := observer.New(zap.DebugLevel)
	firstApplied := make(chan struct{})
	resumeFirst := make(chan struct{})
	var once sync.Once
	firstLogger := zap.New(firstCore, zap.Hooks(func(entry zapcore.Entry) error {
		if entry.Message == "migration applied" {
			once.Do(func() {
				close(firstApplied)
				select {
				case <-resumeFirst:
				case <-ctx.Done():
				}
			})
		}
		return nil
	}))
	firstResult := make(chan error, 1)
	runners.Go(func() { firstResult <- Migrate(ctx, pool, firstLogger) })
	select {
	case <-firstApplied:
	case err := <-firstResult:
		t.Fatalf("first Migrate returned before applying a migration: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	// The first transaction has committed. A pool.Exec/pool.Begin runner would
	// return its locked session here, letting the second runner reenter the same
	// session lock. A correctly pinned connection remains exclusively acquired.
	secondResult := make(chan error, 1)
	runners.Go(func() { secondResult <- Migrate(ctx, pool, zap.New(secondCore)) })
	waitForMigrationLockWaiter(t, ctx, admin, secondResult)
	require.EqualValues(t, 2, pool.Stat().AcquiredConns())
	require.Empty(t, secondLogs.All(), "waiter must not check applied state before locking")
	close(resumeFirst)
	require.NoError(t, <-firstResult)
	require.NoError(t, <-secondResult)
	for _, version := range migrationVersions(t) {
		field := zap.String("version", version)
		require.Equal(t, 1, firstLogs.FilterMessage("migration applied").FilterField(field).Len(), version)
		require.Equal(t, 1, secondLogs.FilterMessage("migration already applied").FilterField(field).Len(), version)
	}
	require.Zero(t, firstLogs.FilterMessage("migration already applied").Len())
	require.Zero(t, secondLogs.FilterMessage("migration applied").Len())
	requireMigrationsApplied(t, ctx, admin, pool)
}

func TestMigrateFailureReleasesLock(t *testing.T) {
	admin, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	// A real DDL failure in the first embedded migration, without replacing the
	// migration FS or changing production SQL. Rollback must leave no version.
	_, err := pool.Exec(ctx, "CREATE TABLE services (sentinel boolean)")
	require.NoError(t, err)
	err = Migrate(ctx, pool, zap.NewNop())
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "42P07", pgErr.Code) // duplicate_table
	require.ErrorContains(t, err, "executing migration 000001_init")
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count))
	require.Zero(t, count)
	_, err = pool.Exec(ctx, "DROP TABLE services")
	require.NoError(t, err)

	// Keep the failed runner's pool alive. A different pool guarantees the retry
	// cannot mask a leaked lock by reacquiring it reentrantly on the same session.
	retry, err := pgxpool.NewWithConfig(ctx, pool.Config())
	require.NoError(t, err)
	defer retry.Close()
	require.NoError(t, Migrate(ctx, retry, zap.NewNop()))
	requireMigrationsApplied(t, ctx, admin, retry)
}

func TestMigrateCanceledLockWait(t *testing.T) {
	admin, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	holder, err := admin.Acquire(ctx)
	require.NoError(t, err)
	defer holder.Release()
	defer func() { require.NoError(t, holder.Conn().Close(context.Background())) }()
	_, err = holder.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey)
	require.NoError(t, err)
	waitCtx, cancelWait := context.WithCancel(ctx)
	var runners sync.WaitGroup
	defer func() {
		cancelWait()
		runners.Wait()
	}()
	result := make(chan error, 1)
	runners.Go(func() { result <- Migrate(waitCtx, pool, zap.NewNop()) })
	waitForMigrationLockWaiter(t, ctx, admin, result)
	cancelWait()
	require.ErrorIs(t, <-result, context.Canceled)
	_, err = holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockKey)
	require.NoError(t, err)
	require.NoError(t, Migrate(ctx, pool, zap.NewNop()))
	requireMigrationsApplied(t, ctx, admin, pool)
}

func TestMigrateUnlockWithCanceledContext(t *testing.T) {
	admin, pool := migrationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var beforePID, afterPID uint32
	require.NoError(t, pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&beforePID))
	versions := migrationVersions(t)
	var applied int
	core, _ := observer.New(zap.InfoLevel)
	logger := zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
		if entry.Message == "migration applied" {
			applied++
			if applied == len(versions) {
				cancel() // All SQL committed, but deferred unlock has not run yet.
			}
		}
		return nil
	}))
	require.NoError(t, Migrate(ctx, pool, logger))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	checkCtx, checkCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer checkCancel()
	requireMigrationsApplied(t, checkCtx, admin, pool)
	require.NoError(t, pool.QueryRow(checkCtx, "SELECT pg_backend_pid()").Scan(&afterPID))
	require.Equal(t, beforePID, afterPID, "successful unlock should preserve the session")
}
