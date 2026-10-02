package nostr

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestDispatchEventPreservesOrderLive (bahia-irsry.58): live (post-EOSE) events
// dispatched via the inbox must arrive on Events in the same order as
// dispatchEvent was called. Before the inbox fix, each event was dispatched in
// its own goroutine with no ordering guarantee.
//
// Run with: CGO_ENABLED=0 go test -cpu=1,2,8 -count=20 -run TestDispatchEventPreservesOrder
func TestDispatchEventPreservesOrderLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})

	const count = 1024
	sub := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	sub.live.Store(true)
	sub.dispatchEose(nil)
	<-sub.EndOfStoredEvents

	// Dispatch in a separate goroutine: dispatchEvent may block when the
	// bounded inbox is full (this is the desired backpressure behavior),
	// so the dispatcher and the consumer must run concurrently.
	go func() {
		for i := range count {
			sub.dispatchEvent(Event{Kind: Kind(i)})
		}
	}()

	for i := range count {
		select {
		case evt := <-sub.Events:
			if evt.Kind != Kind(i) {
				t.Fatalf("event %d: got kind %d, want %d", i, evt.Kind, i)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for event %d", i)
		}
	}

	sub.cancel(nil)
	for range sub.Events {
	}
}

// TestDispatchEventPreservesOrderStored (bahia-irsry.58): stored (pre-EOSE)
// events must also arrive in order, with EOSE delivered only after all stored
// events.
func TestDispatchEventPreservesOrderStored(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})

	const count = 256
	sub := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	sub.live.Store(true)

	// Dispatch stored events in a goroutine because the inbox may fill
	// while the unbuffered Events channel has no reader yet.
	go func() {
		for i := range count {
			sub.dispatchEvent(Event{Kind: Kind(i)})
		}
		sub.dispatchEose(nil)
	}()

	for i := range count {
		select {
		case evt := <-sub.Events:
			if evt.Kind != Kind(i) {
				t.Fatalf("event %d: got kind %d, want %d", i, evt.Kind, i)
			}
		case <-sub.EndOfStoredEvents:
			t.Fatalf("EOSE arrived before event %d/%d", i, count)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for event %d", i)
		}
	}

	select {
	case <-sub.EndOfStoredEvents:
	case <-ctx.Done():
		t.Fatal("timed out waiting for EOSE")
	}

	sub.cancel(nil)
	for range sub.Events {
	}
}

// TestDispatchEventOrderAcrossStoredAndLive (bahia-irsry.58): a burst of stored
// events followed by a burst of live events must arrive in a single ordered
// sequence with EOSE in between.
func TestDispatchEventOrderAcrossStoredAndLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})

	const storedCount = 128
	const liveCount = 128
	sub := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	sub.live.Store(true)

	// Dispatch stored events in a goroutine.
	go func() {
		for i := range storedCount {
			sub.dispatchEvent(Event{Kind: Kind(i)})
		}
		sub.dispatchEose(nil)
	}()

	// Read all stored events.
	for i := range storedCount {
		select {
		case evt := <-sub.Events:
			if evt.Kind != Kind(i) {
				t.Fatalf("stored event %d: got kind %d, want %d", i, evt.Kind, i)
			}
		case <-ctx.Done():
			t.Fatalf("timed out at stored event %d", i)
		}
	}

	// EOSE must arrive before live events.
	select {
	case <-sub.EndOfStoredEvents:
	case <-ctx.Done():
		t.Fatal("timed out waiting for EOSE")
	}

	// Dispatch live events in a goroutine.
	go func() {
		for i := range liveCount {
			sub.dispatchEvent(Event{Kind: Kind(storedCount + i)})
		}
	}()

	for i := range liveCount {
		select {
		case evt := <-sub.Events:
			want := Kind(storedCount + i)
			if evt.Kind != want {
				t.Fatalf("live event %d: got kind %d, want %d", i, evt.Kind, want)
			}
		case <-ctx.Done():
			t.Fatalf("timed out at live event %d", i)
		}
	}

	sub.cancel(nil)
	for range sub.Events {
	}
}

// TestDispatchEventBackpressureUnblocksOnCancel (bahia-irsry.58): when the inbox
// is full and the context is canceled, dispatchEvent must unblock promptly.
func TestDispatchEventBackpressureUnblocksOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})

	sub := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	sub.live.Store(true)
	sub.dispatchEose(nil)
	<-sub.EndOfStoredEvents

	// Dispatch more events than the inbox can hold without reading Events.
	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		for i := range 300 {
			sub.dispatchEvent(Event{Kind: Kind(i)})
		}
	}()

	// Let some events queue up, then cancel.
	time.Sleep(10 * time.Millisecond)
	sub.cancel(nil)

	// Drain Events to let teardown complete.
	for range sub.Events {
	}

	// The dispatch goroutine must finish promptly after cancellation.
	select {
	case <-dispatched:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch goroutine stuck after cancellation")
	}
}
