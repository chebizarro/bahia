package app

import (
	"context"
	"errors"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

var errBackgroundRestartRequired = errors.New("background restart required")

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
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		if recovered, err := r.tryRecover(ctx); recovered {
			if err != nil {
				return err
			}
			return errBackgroundRestartRequired
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *databaseRecoveryRunner) tryRecover(ctx context.Context) (bool, error) {
	pool, err := dbConnect(ctx, r.cfg, r.logger)
	if err != nil {
		r.logger.Debug("database recovery probe failed", zap.Error(err))
		return false, nil
	}
	if pool != nil {
		defer pool.Close()
	}
	if err := dbMigrate(ctx, pool, r.logger); err != nil {
		r.logger.Debug("database recovery migration probe failed", zap.Error(err))
		return false, nil
	}

	r.logger.Info("postgres cache recovered; requesting process restart so higher tiers can be rebuilt")
	return true, nil
}
