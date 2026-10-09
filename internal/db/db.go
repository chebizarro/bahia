// Package db provides database connection and migration utilities.
package db

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

var parsePoolConfig = pgxpool.ParseConfig

var canceledPoolCleanups atomic.Int32

// CanceledPoolCleanupPending reports whether pgx is still closing a pool
// after a canceled operation. A caller can avoid starting another optional
// connection while the previous transport is being torn down.
func CanceledPoolCleanupPending() bool { return canceledPoolCleanups.Load() != 0 }

// CloseCanceledPool releases a pool whose in-flight pgx operation was
// canceled. pgx bounds canceled-query transport cleanup to 15 seconds, but
// Pool.Close waits for it; keep that driver cleanup off readiness.
func CloseCanceledPool(pool *pgxpool.Pool, logger *zap.Logger) {
	if pool == nil {
		return
	}
	canceledPoolCleanups.Add(1)
	go func() {
		defer canceledPoolCleanups.Add(-1)
		pool.Close()
		if logger != nil {
			logger.Debug("canceled postgres pool cleanup complete")
		}
	}()
}

// Connect creates a new PostgreSQL connection pool.
func Connect(ctx context.Context, cfg config.DBConfig, logger *zap.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := parsePoolConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parsing database DSN: %w", cfg.RedactError(err))
	}

	poolCfg.MaxConns = int32(cfg.MaxOpenConns)
	poolCfg.MinConns = int32(cfg.MaxIdleConns)
	poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", cfg.RedactError(err))
	}

	// Verify connectivity.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		if pingCtx.Err() != nil {
			CloseCanceledPool(pool, logger)
		} else {
			pool.Close()
		}
		return nil, fmt.Errorf("pinging database: %w", cfg.RedactError(err))
	}

	logger.Info("database connection established",
		zap.String("host", cfg.Host),
		zap.Int("port", cfg.Port),
		zap.String("database", cfg.Name),
	)

	return pool, nil
}
