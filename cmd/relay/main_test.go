package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

type supervisorTestRuntime struct {
	started chan struct{}
	stopped atomic.Bool
}

func (r *supervisorTestRuntime) Run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	r.stopped.Store(true)
	return nil
}

func (r *supervisorTestRuntime) Close() error { return nil }

func TestReloadInitializationFailureKeepsActiveRuntimeServing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	current := &supervisorTestRuntime{started: make(chan struct{})}
	supervisor := &runtimeSupervisor{
		rootCtx: ctx,
		logger:  zap.NewNop(),
		factory: func(config.NostrConfig, *zap.Logger) (sidecarRuntime, error) {
			return nil, errors.New("replacement initialization failed")
		},
	}
	supervisor.start(current)
	<-current.started

	cfg := config.Defaults()
	cfg.Nostr.Sidecar.Enabled = true
	if err := supervisor.replace(cfg); err == nil {
		t.Fatal("replace() error = nil, want initialization failure")
	}
	if current.stopped.Load() {
		t.Fatal("active runtime stopped before replacement initialized")
	}
	if supervisor.active == nil || supervisor.active.runtime != current {
		t.Fatal("active runtime was replaced after replacement initialization failure")
	}
	if err := supervisor.stop(); err != nil {
		t.Fatalf("stop active runtime: %v", err)
	}
}
