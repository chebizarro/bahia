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
