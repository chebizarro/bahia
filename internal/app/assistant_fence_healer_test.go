package app

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeRelayConnections struct {
	mu        sync.Mutex
	ch        chan struct{}
	cancelled bool
}

func (f *fakeRelayConnections) NotifyRelayConnected() (<-chan struct{}, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ch = make(chan struct{}, 1)
	return f.ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cancelled = true
	}
}

// reconnect performs the pool's non-blocking, coalescing send.
func (f *fakeRelayConnections) reconnect() {
	select {
	case f.ch <- struct{}{}:
	default:
	}
}

type fakeFencedHealer struct {
	passes chan struct{}
	gate   chan struct{}
}

func (f *fakeFencedHealer) HealFenced(ctx context.Context) int {
	f.passes <- struct{}{}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
		}
	}
	return 0
}

// A reconnect that happens before the runner starts is not lost, a storm
// during a pass costs one further pass, and stopping unregisters.
func TestAssistantFenceHealerRunsOnePassPerCoalescedReconnect(t *testing.T) {
	relays := &fakeRelayConnections{}
	engine := &fakeFencedHealer{passes: make(chan struct{}, 16), gate: make(chan struct{})}
	healer := newAssistantFenceHealer(engine, relays)
	relays.reconnect() // before Run

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = healer.Run(ctx)
	}()
	waitPass := func() {
		t.Helper()
		select {
		case <-engine.passes:
		case <-time.After(10 * time.Second):
			t.Fatal("no heal pass after reconnect")
		}
	}
	waitPass()
	for range 50 {
		relays.reconnect() // storm while the first pass runs
	}
	engine.gate <- struct{}{}
	waitPass()
	engine.gate <- struct{}{}
	select {
	case <-engine.passes:
		t.Fatal("a coalesced storm produced more than one extra pass")
	default:
	}
	cancel()
	<-done
	relays.mu.Lock()
	defer relays.mu.Unlock()
	if !relays.cancelled {
		t.Fatal("healer did not unregister its reconnect listener")
	}
}

func TestAssistantExecutionWiringBuildsFenceHealerFromRelayConnections(t *testing.T) {
	relay := newMemoryRelay()
	wiring, _ := buildTestAssistantExecution(t, wiringConfig(t, true, ""), relay)
	if wiring.Healer != nil {
		t.Fatal("healer built without a relay connection signal")
	}
	relays := &fakeRelayConnections{}
	withHealer, _ := buildTestAssistantExecutionWith(t, wiringConfig(t, true, ""), relay, func(deps *assistantExecutionDeps) { deps.RelayConnections = relays })
	if withHealer.Healer == nil || withHealer.Healer.engine != withHealer.Engine || relays.ch == nil {
		t.Fatalf("healer not wired to the engine and relay signal: %+v", withHealer.Healer)
	}
}
