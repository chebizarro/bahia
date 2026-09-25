package db

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationLockKey is the big-endian int64 encoding of the ASCII bytes "bahia.db".
// Keep it stable: every Bahia process must contend on the same migration lock.
const migrationLockKey int64 = 0x62616869612e6462

// Migrate runs all pending up migrations in order.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *zap.Logger) error {
	// Session locks require exclusive ownership of one connection for the entire
	// run, including the applied checks and every per-migration transaction.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer conn.Release()
	defer func() {
		// Also attempt unlock if acquisition was canceled in flight. The server
		// may have acquired the lock before the client received its response.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
			logger.Warn("releasing migration lock; discarding connection", zap.Error(err))
			// Never return a potentially locked session to the pool.
			if err := conn.Conn().Close(unlockCtx); err != nil {
				logger.Warn("closing migration connection", zap.Error(err))
			}
		}
	}()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}

	// Ensure migrations tracking table exists.
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	// Read available migration files.
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("reading migrations directory: %w", err)
	}

	var upFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			upFiles = append(upFiles, e.Name())
		}
	}
	sort.Strings(upFiles)

	for _, fname := range upFiles {
		version := strings.TrimSuffix(fname, ".up.sql")

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
