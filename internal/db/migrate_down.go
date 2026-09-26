package db

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// AppliedMigration is a version recorded by the database, including its commit time.
type AppliedMigration struct {
	Version   string
	AppliedAt time.Time
}

// MigrationStatus is a read-only snapshot. Pending contains full filename stems.
type MigrationStatus struct {
	Applied []AppliedMigration
	Pending []string
}

func availableMigrations(files fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading migrations directory: %w", err)
	}
	var versions []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") {
			versions = append(versions, strings.TrimSuffix(entry.Name(), ".up.sql"))
		}
	}
	sort.Strings(versions)
	return versions, nil
}

// Status reads migration state without creating the tracking table or writing SQL.
// It shares the session lock with startup migrations and rollback so the snapshot
// cannot observe one of those operations in progress.
func Status(ctx context.Context, pool *pgxpool.Pool, logger *zap.Logger) (MigrationStatus, error) {
	return statusWithFS(ctx, pool, logger, migrationsFS)
}

func statusWithFS(ctx context.Context, pool *pgxpool.Pool, logger *zap.Logger, files fs.FS) (MigrationStatus, error) {
	var status MigrationStatus
	err := withMigrationLock(ctx, pool, logger, func(conn *pgxpool.Conn) error {
		versions, err := availableMigrations(files)
		if err != nil {
			return err
		}
		var tableExists bool
		if err := conn.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&tableExists); err != nil {
			return fmt.Errorf("checking migration table: %w", err)
		}
		applied := make(map[string]bool)
		if tableExists {
			rows, err := conn.Query(ctx, "SELECT version, applied_at FROM schema_migrations ORDER BY applied_at, version")
			if err != nil {
				return fmt.Errorf("reading applied migrations: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var item AppliedMigration
				if err := rows.Scan(&item.Version, &item.AppliedAt); err != nil {
					return fmt.Errorf("scanning applied migration: %w", err)
				}
				status.Applied = append(status.Applied, item)
				applied[item.Version] = true
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("reading applied migrations: %w", err)
			}
		}
		for _, version := range versions {
			if !applied[version] {
				status.Pending = append(status.Pending, version)
			}
		}
		return nil
	})
	return status, err
}

// DownOptions controls an explicitly confirmed rollback. To names the version
// that remains applied; an empty To rolls back exactly one migration.
type DownOptions struct {
	To      string
	Confirm bool
	Force   bool
}

// Down rolls back applied migrations newest first. Each SQL body and version
// deletion share one transaction. Force permits out-of-order applied history;
// it does not bypass confirmation, missing scripts, or SQL safety guards.
func Down(ctx context.Context, pool *pgxpool.Pool, logger *zap.Logger, opts DownOptions) ([]string, error) {
	return downWithFS(ctx, pool, logger, migrationsFS, opts)
}

func downWithFS(ctx context.Context, pool *pgxpool.Pool, logger *zap.Logger, files fs.FS, opts DownOptions) ([]string, error) {
	if !opts.Confirm {
		return nil, fmt.Errorf("down requires explicit confirmation")
	}
	var rolledBack []string
	err := withMigrationLock(ctx, pool, logger, func(conn *pgxpool.Conn) error {
		versions, err := availableMigrations(files)
		if err != nil {
			return err
		}
		known := make(map[string]bool, len(versions))
		for _, v := range versions {
			known[v] = true
		}
		var tableExists bool
		if err := conn.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&tableExists); err != nil {
			return fmt.Errorf("checking migration table: %w", err)
		}
		if !tableExists {
			return fmt.Errorf("no applied migrations: schema_migrations does not exist")
		}
		rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC")
		if err != nil {
			return fmt.Errorf("reading applied migrations: %w", err)
		}
		var applied []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return fmt.Errorf("scanning applied migration: %w", err)
			}
			applied = append(applied, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("reading applied migrations: %w", err)
		}
		if len(applied) == 0 {
			return fmt.Errorf("no applied migrations")
		}
		limit := 1
		if opts.To != "" {
			limit = -1
			for i, v := range applied {
				if v == opts.To {
					limit = i
					break
				}
			}
			if limit < 0 {
				return fmt.Errorf("target migration %q is not applied", opts.To)
			}
		}
		// Preflight the entire plan so a missing script cannot cause a partial
		// multi-step rollback. Keep each actual down independently transactional.
		scripts := make([][]byte, limit)
		for i, v := range applied[:limit] {
			if !known[v] {
				return fmt.Errorf("applied migration %q has no embedded up file", v)
			}
			if !opts.Force {
				for _, other := range applied[i+1:] {
					if other > v {
						return fmt.Errorf("migration %q is not the latest applied in filename order; use --force to override", v)
					}
				}
			}
			scripts[i], err = fs.ReadFile(files, "migrations/"+v+".down.sql")
			if err != nil {
				return fmt.Errorf("missing down migration for %s: %w", v, err)
			}
		}
		for i, v := range applied[:limit] {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return fmt.Errorf("beginning down transaction for %s: %w", v, err)
			}
			if _, err := tx.Exec(ctx, string(scripts[i])); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("executing down migration %s: %w", v, err)
			}
			result, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", v)
			if err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("deleting migration %s: %w", v, err)
			}
			if result.RowsAffected() != 1 {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("deleting migration %s: expected one version row, got %d", v, result.RowsAffected())
			}
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("committing down migration %s: %w", v, err)
			}
			rolledBack = append(rolledBack, v)
			logger.Info("migration rolled back", zap.String("version", v))
		}
		return nil
	})
	return rolledBack, err
}
