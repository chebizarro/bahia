package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type retryRepoStub struct {
	pending          []domain.HiveCIWorkflowResult
	incremented      []string
	markedFailed     []string
	incrementByEvent map[string]int
}

func (r *retryRepoStub) ListPendingResults(_ context.Context) ([]domain.HiveCIWorkflowResult, error) {
	return r.pending, nil
}

func (r *retryRepoStub) IncrementResultRetry(_ context.Context, eventID string, _ time.Time) (int, error) {
	r.incremented = append(r.incremented, eventID)
	r.incrementByEvent[eventID]++
	return r.incrementByEvent[eventID], nil
}

func (r *retryRepoStub) MarkResultFailed(_ context.Context, eventID, _ string) error {
	r.markedFailed = append(r.markedFailed, eventID)
	return nil
}

type retryProcessorStub struct {
	err   error
	calls []string
}

func (p *retryProcessorStub) ProcessResult(_ context.Context, resultEventID string) error {
	p.calls = append(p.calls, resultEventID)
	return p.err
}

func TestHiveCIRetryRunner_StateMachine(t *testing.T) {
	repo := &retryRepoStub{incrementByEvent: map[string]int{}, pending: []domain.HiveCIWorkflowResult{{ResultEventID: "res-1", ProcessingState: domain.HiveCIProcessingStatePendingRun}}}
	proc := &retryProcessorStub{}
	runner := NewHiveCIRetryRunner(repo, proc, 10*time.Millisecond, 10, zap.NewNop())

	runner.runOnce(context.Background())

	require.Equal(t, []string{"res-1"}, repo.incremented)
	require.Equal(t, []string{"res-1"}, proc.calls)
	require.Empty(t, repo.markedFailed)
}

func TestHiveCIRetryRunner_BackoffBehavior(t *testing.T) {
	now := time.Now()
	recent := now.Add(-20 * time.Millisecond)
	repo := &retryRepoStub{incrementByEvent: map[string]int{}, pending: []domain.HiveCIWorkflowResult{{ResultEventID: "res-recent", RetryCount: 2, LastRetryAt: &recent, ProcessingState: domain.HiveCIProcessingStateArtifactPending}}}
	proc := &retryProcessorStub{}
	runner := NewHiveCIRetryRunner(repo, proc, 100*time.Millisecond, 10, zap.NewNop())

	runner.runOnce(context.Background())

	require.Empty(t, repo.incremented)
	require.Empty(t, proc.calls)
}

func TestHiveCIRetryRunner_MaxRetryExhaustion(t *testing.T) {
	repo := &retryRepoStub{incrementByEvent: map[string]int{}, pending: []domain.HiveCIWorkflowResult{{ResultEventID: "res-max", RetryCount: 10, ProcessingState: domain.HiveCIProcessingStatePendingRun}}}
	proc := &retryProcessorStub{err: errors.New("boom")}
	runner := NewHiveCIRetryRunner(repo, proc, 10*time.Millisecond, 10, zap.NewNop())

	runner.runOnce(context.Background())

	require.Equal(t, []string{"res-max"}, repo.markedFailed)
	require.Empty(t, repo.incremented)
	require.Empty(t, proc.calls)
}
