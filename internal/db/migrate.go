package db

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationLockKey is the big-endian int64 encoding of the ASCII bytes "bahia.db".
// Keep it stable: every Bahia process must contend on the same migration lock.
const migrationLockKey int64 = 0x62616869612e6462

// withMigrationLock pins one pool connection for the entire operation. PostgreSQL
// advisory locks belong to sessions, not transactions or pools.
func withMigrationLock(ctx context.Context, pool *pgxpool.Pool, logger *zap.Logger, run func(*pgxpool.Conn) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer conn.Release()
	defer func() {
		// Acquisition can succeed on the server even if cancellation obscures its
		// response. Always attempt unlock before returning the session to the pool.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
			logger.Warn("releasing migration lock; discarding connection", zap.Error(err))
			if err := conn.Conn().Close(unlockCtx); err != nil {
				logger.Warn("closing migration connection", zap.Error(err))
			}
		}
	}()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}
	return run(conn)
}

// Migrate runs all pending up migrations in order.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *zap.Logger) error {
	return withMigrationLock(ctx, pool, logger, func(conn *pgxpool.Conn) error {
		return migrateUp(ctx, conn, logger)
	})
}

func migrateUp(ctx context.Context, conn *pgxpool.Conn, logger *zap.Logger) error {
	// Ensure migrations tracking table exists.
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	versions, err := availableMigrations(migrationsFS)
	if err != nil {
		return err
	}
	for _, version := range versions {
		fname := version + ".up.sql"

		// Check if already applied.
		var count int
		err := conn.QueryRow(ctx,
			"SELECT COUNT(*) FROM schema_migrations WHERE version = $1", version,
		).Scan(&count)
		if err != nil {
			return fmt.Errorf("checking migration %s: %w", version, err)
		}
		if count > 0 {
			logger.Debug("migration already applied", zap.String("version", version))
			continue
		}

		// Read and execute migration.
		sql, err := migrationsFS.ReadFile("migrations/" + fname)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", fname, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning transaction for %s: %w", version, err)
		}

		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("executing migration %s: %w", version, err)
		}

		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations (version) VALUES ($1)", version,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("recording migration %s: %w", version, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing migration %s: %w", version, err)
		}

		logger.Info("migration applied", zap.String("version", version))
	}

	return nil
}
