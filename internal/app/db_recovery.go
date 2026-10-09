package app

import (
	"context"
	"errors"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

var errBackgroundRestartRequired = errors.New("background restart required")

// Recovery runs outside readiness and may need to build a large derived index.
const databaseRecoveryAttemptTimeout = 5 * time.Minute

type databaseRecoveryRunner struct {
	cfg      config.DBConfig
	interval time.Duration
	logger   *zap.Logger
}

func newDatabaseRecoveryRunner(cfg config.DBConfig, interval time.Duration, logger *zap.Logger) *databaseRecoveryRunner {
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &databaseRecoveryRunner{cfg: cfg, interval: interval, logger: logger}
}

func (r *databaseRecoveryRunner) Name() string { return "database-recovery" }

func (r *databaseRecoveryRunner) Run(ctx context.Context) error {
	//nostr:allow-poll reconnect backoff: probes the optional PostgreSQL index until it is reachable again
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		if r.tryRecover(ctx) {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *databaseRecoveryRunner) tryRecover(ctx context.Context) bool {
	attemptCtx, cancel := context.WithTimeout(ctx, databaseRecoveryAttemptTimeout)
	defer cancel()
	pool, err := dbConnect(attemptCtx, r.cfg, r.logger)
	if err != nil {
		r.logger.Debug("database recovery probe failed", zap.Error(err))
		return false
	}
	if pool != nil {
		defer pool.Close()
	}
	if attemptCtx.Err() != nil {
		return false
	}
	if err := dbMigrate(attemptCtx, pool, r.logger); err != nil {
		r.logger.Debug("database recovery migration probe failed", zap.Error(err))
		return false
	}
	if attemptCtx.Err() != nil {
		return false
	}

	r.logger.Info("postgres cache recovered; derived SQL capabilities become available on a subsequent daemon start")
	return true
}
