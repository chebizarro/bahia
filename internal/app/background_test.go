package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type testRunner struct {
	name    string
	started atomic.Bool
	stopped atomic.Bool
}

func (r *testRunner) Name() string { return r.name }
func (r *testRunner) Run(ctx context.Context) error {
	r.started.Store(true)
	<-ctx.Done()
	r.stopped.Store(true)
	return nil
}

func TestBackgroundManager_RegisterAndStart(t *testing.T) {
	logger := zap.NewNop()
	mgr := NewBackgroundManager(logger)

	r1 := &testRunner{name: "worker-a"}
	r2 := &testRunner{name: "worker-b"}
	mgr.Register(r1)
	mgr.Register(r2)

	require.Equal(t, 2, mgr.Count(), "expected 2 registered runners")

	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)

	// Wait for runners to start using deterministic sync.
	require.Eventually(t, func() bool {
		return r1.started.Load() && r2.started.Load()
	}, 2*time.Second, 10*time.Millisecond, "both workers should have started")

	// Cancel context → runners should stop.
	cancel()
	mgr.Wait()

	require.True(t, r1.stopped.Load(), "worker-a should have stopped")
	require.True(t, r2.stopped.Load(), "worker-b should have stopped")
}

func TestBackgroundManager_EmptyStart(t *testing.T) {
	logger := zap.NewNop()
	mgr := NewBackgroundManager(logger)

	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)
	cancel()
	mgr.Wait() // should not block
}

type testOSVVulnerabilityCachePruner struct {
	calls chan time.Time
	count int64
	err   error
}

func (p *testOSVVulnerabilityCachePruner) PruneExpiredOSVVulnerabilityCache(_ context.Context, now time.Time) (int64, error) {
	select {
	case p.calls <- now:
	default:
	}
	return p.count, p.err
}

func TestOSVVulnerabilityCacheCleanupRunner_PrunesExpiredEntries(t *testing.T) {
	pruner := &testOSVVulnerabilityCachePruner{calls: make(chan time.Time, 1), count: 3}
	core, logs := observer.New(zap.InfoLevel)
	runner := NewOSVVulnerabilityCacheCleanupRunner(pruner, time.Millisecond, zap.New(core))
	require.Equal(t, "osv-vulnerability-cache-cleanup", runner.Name())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	select {
	case now := <-pruner.calls:
		require.False(t, now.IsZero())
	case <-time.After(time.Second):
		t.Fatal("runner did not prune expired OSV cache entries")
	}
	require.Eventually(t, func() bool {
		return logs.FilterMessage("pruned expired osv vulnerability cache").Len() > 0
	}, time.Second, time.Millisecond)

	cancel()
	require.NoError(t, <-done)
}

func TestOSVVulnerabilityCacheCleanupRunner_LogsFailureAndContinues(t *testing.T) {
	pruner := &testOSVVulnerabilityCachePruner{
		calls: make(chan time.Time, 2),
		err:   errors.New("prune failed"),
	}
	core, logs := observer.New(zap.InfoLevel)
	runner := NewOSVVulnerabilityCacheCleanupRunner(pruner, time.Millisecond, zap.New(core))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	for range 2 {
		select {
		case <-pruner.calls:
		case <-time.After(time.Second):
			t.Fatal("runner stopped after prune failure")
		}
	}
	require.GreaterOrEqual(t, logs.FilterMessage("osv vulnerability cache cleanup failed").Len(), 1)

	cancel()
	require.NoError(t, <-done)
}

func TestOSVVulnerabilityCacheCleanupRunner_NilPrunerReturns(t *testing.T) {
	runner := NewOSVVulnerabilityCacheCleanupRunner(nil, 0, nil)
	require.Equal(t, defaultOSVVulnerabilityCacheCleanupInterval, runner.interval)
	require.NoError(t, runner.Run(context.Background()))
}
