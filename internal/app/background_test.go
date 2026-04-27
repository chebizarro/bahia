package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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
