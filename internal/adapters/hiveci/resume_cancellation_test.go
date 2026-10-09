package hiveci

import (
	"context"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type cancellationResultRepo struct {
	repository.HiveCIRepository
	attempts int
}

type cancellationDuringIncrementRepo struct {
	repository.HiveCIRepository
	result    domain.HiveCIWorkflowResult
	cancel    context.CancelFunc
	returnErr bool
	restored  int
}

func (r *cancellationDuringIncrementRepo) ListPendingResults(context.Context) ([]domain.HiveCIWorkflowResult, error) {
	return []domain.HiveCIWorkflowResult{r.result}, nil
}

func (r *cancellationDuringIncrementRepo) IncrementResultRetry(_ context.Context, _ string, at time.Time) (int, error) {
	r.result.RetryCount++
	r.result.LastRetryAt = &at
	if r.cancel != nil {
		r.cancel()
	}
	if r.returnErr {
		return 0, context.Canceled
	}
	return r.result.RetryCount, nil
}

func (r *cancellationDuringIncrementRepo) RestoreResultRetry(_ context.Context, previous domain.HiveCIWorkflowResult, attempt int, at time.Time) (bool, error) {
	if r.result.RetryCount != attempt || r.result.LastRetryAt == nil || !r.result.LastRetryAt.Equal(at) {
		return false, nil
	}
	r.result.RetryCount = previous.RetryCount
	r.result.LastRetryAt = previous.LastRetryAt
	r.restored++
	return true, nil
}

type cancellationCountingProcessor struct{ calls int }

func (p *cancellationCountingProcessor) ProcessResult(context.Context, string) error {
	p.calls++
	return nil
}

func TestPendingResultResumeRestoresRetryCanceledDuringIncrement(t *testing.T) {
	for _, returnErr := range []bool{false, true} {
		t.Run(map[bool]string{false: "success-after-publish", true: "error-after-publish"}[returnErr], func(t *testing.T) {
			repo := &cancellationDuringIncrementRepo{
				result:    domain.HiveCIWorkflowResult{ResultEventID: "result-1", ProcessingState: domain.HiveCIProcessingStatePendingResult},
				returnErr: returnErr,
			}
			processor := &cancellationCountingProcessor{}
			resumer := NewPendingResultResumer(repo, processor, 2, zap.NewNop())
			for range 3 {
				ctx, cancel := context.WithCancel(context.Background())
				repo.cancel = cancel
				require.ErrorIs(t, resumer.Resume(ctx), context.Canceled)
				cancel()
				require.Equal(t, 0, repo.result.RetryCount, "a canceled reservation must not consume the retry budget")
				require.Nil(t, repo.result.LastRetryAt)
			}
			require.Equal(t, 3, repo.restored)
			require.Zero(t, processor.calls)
			repo.cancel = nil
			repo.returnErr = false
			require.NoError(t, resumer.Resume(context.Background()))
			require.Equal(t, 1, processor.calls)
			require.Equal(t, 1, repo.result.RetryCount)
		})
	}
}

func (*cancellationResultRepo) ListPendingResults(context.Context) ([]domain.HiveCIWorkflowResult, error) {
	return []domain.HiveCIWorkflowResult{
		{ResultEventID: "result-1", ProcessingState: domain.HiveCIProcessingStatePendingResult},
		{ResultEventID: "result-2", ProcessingState: domain.HiveCIProcessingStatePendingResult},
	}, nil
}

func (r *cancellationResultRepo) IncrementResultRetry(context.Context, string, time.Time) (int, error) {
	r.attempts++
	return r.attempts, nil
}

type cancelAfterFirstResult struct {
	cancel context.CancelFunc
	calls  int
}

func (p *cancelAfterFirstResult) ProcessResult(context.Context, string) error {
	p.calls++
	p.cancel()
	return nil
}

func TestPendingResultResumeStopsOnPolicyInvalidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &cancellationResultRepo{}
	processor := &cancelAfterFirstResult{cancel: cancel}
	err := NewPendingResultResumer(repo, processor, 3, zap.NewNop()).Resume(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, processor.calls, "the next result must await a fresh policy pass")
	require.Equal(t, 1, repo.attempts)
}
