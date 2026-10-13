package app

import (
	"context"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"go.uber.org/zap"
)

const (
	runtimeObservationRetention  = 30 * 24 * time.Hour
	runtimeObservationBatchSize  = 500
	runtimeObservationBatchPause = 250 * time.Millisecond
	runtimeObservationIdlePause  = time.Hour
	runtimeObservationErrorPause = time.Minute
)

type runtimeObservationBatchDeleter interface {
	DeleteExpiredBatch(context.Context, time.Time, int) (int64, error)
}

// RuntimeObservationRetentionRunner drains the derived PostgreSQL history in
// paced, independently committed batches without participating in readiness.
type RuntimeObservationRetentionRunner struct {
	deleter                           runtimeObservationBatchDeleter
	logger                            *zap.Logger
	now                               func() time.Time
	batchPause, idlePause, errorPause time.Duration
}

func NewRuntimeObservationRetentionRunner(deleter runtimeObservationBatchDeleter, logger *zap.Logger) *RuntimeObservationRetentionRunner {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RuntimeObservationRetentionRunner{
		deleter: deleter, logger: logger, now: time.Now,
		batchPause: runtimeObservationBatchPause, idlePause: runtimeObservationIdlePause, errorPause: runtimeObservationErrorPause,
	}
}

func (*RuntimeObservationRetentionRunner) Name() string { return "runtime-observation-retention" }

func (r *RuntimeObservationRetentionRunner) Run(ctx context.Context) error {
	if r.deleter == nil {
		return nil
	}
	//nostr:allow-poll operational retention: wall-clock expiry has no event; this timer never waits for relay delivery
	for {
		if ctx.Err() != nil {
			return nil
		}
		count, err := r.deleter.DeleteExpiredBatch(ctx, r.now().UTC().Add(-runtimeObservationRetention), runtimeObservationBatchSize)
		if ctx.Err() != nil {
			return nil
		}
		pause := r.batchPause
		outcome := "success"
		if err != nil {
			outcome = "failure"
			pause = r.errorPause
			r.logger.Warn("runtime observation retention batch failed", zap.Error(err))
		} else if count < runtimeObservationBatchSize {
			pause = r.idlePause
		}
		telemetry.RecordRuntimeObservationRetention(ctx, count, outcome)
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
