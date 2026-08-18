package signet

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type managedClientFake struct {
	mu           sync.Mutex
	attempts     int
	failures     int
	pingFailures int
	block        bool
	connected    bool
	active       atomic.Int32
}

func (f *managedClientFake) Connect(ctx context.Context) error {
	f.active.Add(1)
	defer f.active.Add(-1)
	f.mu.Lock()
	f.attempts++
	attempt := f.attempts
	block := f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if attempt <= f.failures {
		return errors.New("relay unavailable")
	}
	f.mu.Lock()
	f.connected = true
	f.mu.Unlock()
	return nil
}

func (f *managedClientFake) Ping(context.Context) error {
	f.mu.Lock()
	if f.pingFailures > 0 {
		f.pingFailures--
		f.mu.Unlock()
		return errors.New("bunker ping failed")
	}
	f.mu.Unlock()
	if !f.IsConnected() {
		return ErrNotConnected
	}
	return nil
}

func (f *managedClientFake) Attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *managedClientFake) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *managedClientFake) Close() error {
	f.mu.Lock()
	f.connected = false
	f.mu.Unlock()
	return nil
}

func TestConnectionManagerTimesOutInitialAttemptAndStopsCleanly(t *testing.T) {
	client := &managedClientFake{block: true}
	manager := NewConnectionManager(client, ConnectionManagerConfig{
		Name: "stale", AttemptTimeout: 10 * time.Millisecond,
		MinBackoff: 5 * time.Millisecond, MaxBackoff: 5 * time.Millisecond,
		HeartbeatInterval: time.Hour, JitterFraction: -1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	waitManagerState(t, manager, func(state ConnectionState) bool { return state.LastError != "" })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop during retry")
	}
	if active := client.active.Load(); active != 0 {
		t.Fatalf("active Connect goroutines = %d, want 0", active)
	}
}

func TestConnectionManagerRecoversAfterRelayFailure(t *testing.T) {
	client := &managedClientFake{failures: 1}
	manager := NewConnectionManager(client, ConnectionManagerConfig{
		Name: "recovering", AttemptTimeout: 50 * time.Millisecond,
		MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		HeartbeatInterval: time.Hour, JitterFraction: -1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	waitManagerState(t, manager, func(state ConnectionState) bool { return state.Connected })
	state := manager.State()
	if state.LastAttempt.IsZero() || state.LastSuccess.IsZero() || state.LastError != "" {
		t.Fatalf("unexpected recovered state: %+v", state)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop")
	}
}

func TestConnectionManagerReconnectsAfterBunkerHeartbeatFailure(t *testing.T) {
	client := &managedClientFake{pingFailures: 1}
	manager := NewConnectionManager(client, ConnectionManagerConfig{
		Name: "heartbeat", AttemptTimeout: 50 * time.Millisecond,
		MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		HeartbeatInterval: time.Millisecond, JitterFraction: -1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	waitManagerState(t, manager, func(state ConnectionState) bool {
		return state.Connected && client.Attempts() >= 2
	})
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop")
	}
}

func waitManagerState(t *testing.T, manager *ConnectionManager, condition func(ConnectionState) bool) {
	t.Helper()
	if condition(manager.State()) {
		return
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case state := <-manager.Changes():
			if condition(state) {
				return
			}
		case <-timer.C:
			t.Fatal("condition was not satisfied before timeout")
		}
	}
}
