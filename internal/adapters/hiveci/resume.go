package hiveci

import (
	"context"
	"fmt"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// ResultProcessor consumes one retained signed result; the pipeline bridge
// satisfies it.
type ResultProcessor interface {
	ProcessResult(ctx context.Context, resultEventID string) error
}

// PendingResultResumer re-attempts the results whose processing is not
// finished. It replaces the timer that scanned SQL for them:
// processing is triggered by the arrival of a result, by the arrival of the
// run an orphaned result waits for, and, through Resume, once per start from
// the canonical result states the local event store retains. Each attempt is
// counted on the result's canonical state; past maxAttempts the result is
// marked failed instead of being attempted again.
//
// There is deliberately no backoff timer behind these triggers.
// Every transient cause of a failed attempt already has a signal that ends in
// one of them: an unreachable PostgreSQL build registry is probed by the
// database-recovery runner, which restarts the process into Resume; a
// manifest the registry cannot yet serve leaves the result artifact_pending
// for the operator, since the attestor signs after the push; a policy or
// service misconfiguration is fixed by the operator and restarts into Resume.
// A timer would re-run registry and policy lookups on a schedule with no
// signal that anything changed, which is the "waiting and checking" the
// architecture rejects. Once attempts are exhausted the contract is the
// signer-first build-result action (pipeline.Bridge.RegisterBuildResult): a
// failed result may be finished by it, and only by it.
type PendingResultResumer struct {
	repo        repository.HiveCIRepository
	processor   ResultProcessor
	maxAttempts int
	logger      *zap.Logger
	now         func() time.Time
}

func NewPendingResultResumer(repo repository.HiveCIRepository, processor ResultProcessor, maxAttempts int, logger *zap.Logger) *PendingResultResumer {
	if logger == nil {
		logger = zap.NewNop()
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	return &PendingResultResumer{repo: repo, processor: processor, maxAttempts: maxAttempts, logger: logger.Named("hiveci-resume"), now: func() time.Time { return time.Now().UTC() }}
}

// Resume attempts every unfinished result once. A result still waiting for
// its run is left to the run's arrival. It returns the first error it saw
// after attempting the rest.
func (r *PendingResultResumer) Resume(ctx context.Context) error {
	if r == nil || r.repo == nil || r.processor == nil {
		return fmt.Errorf("Hive-CI result resumer is not configured")
	}
	pending, err := r.repo.ListPendingResults(ctx)
	if err != nil {
		return fmt.Errorf("list pending Hive-CI results: %w", err)
	}
	var first error
	for _, result := range pending {
		if result.ProcessingState == domain.HiveCIProcessingStatePendingRun {
			continue
		}
		if err := r.attempt(ctx, result); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (r *PendingResultResumer) attempt(ctx context.Context, result domain.HiveCIWorkflowResult) error {
	if result.RetryCount >= r.maxAttempts {
		return r.repo.MarkResultFailed(ctx, result.ResultEventID, "max retries exceeded")
	}
	attempt, err := r.repo.IncrementResultRetry(ctx, result.ResultEventID, r.now())
	if err != nil {
		return fmt.Errorf("count Hive-CI result attempt %s: %w", result.ResultEventID, err)
	}
	if err := r.processor.ProcessResult(ctx, result.ResultEventID); err != nil {
		r.logger.Warn("resumed Hive-CI result processing failed", zap.String("result_event_id", result.ResultEventID), zap.Int("attempt", attempt), zap.Error(err))
		if attempt >= r.maxAttempts {
			return r.repo.MarkResultFailed(ctx, result.ResultEventID, "max retries exceeded")
		}
		return err
	}
	return nil
}
