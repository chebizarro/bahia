package nostr

import (
	"context"
	"math"
	"strings"
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

	// With the non-blocking inbox, push never blocks, so all dispatches
	// complete without a concurrent reader. We still dispatch in a
	// goroutine so the test structure matches the real relay read loop
	// (a single goroutine dispatching while the consumer reads).
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

// TestDispatchEventNonBlockingUnblocksOnCancel (bahia-irsry.58): dispatching
// more events than the old channel capacity (256) without a reader must
// complete without blocking, and cancel must still shut everything down.
func TestDispatchEventNonBlockingUnblocksOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})

	sub := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	sub.live.Store(true)
	sub.dispatchEose(nil)
	<-sub.EndOfStoredEvents

	// Dispatch more events than the old channel cap (256) without reading.
	// With the non-blocking inbox, every push returns immediately.
	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		for i := range 1000 {
			sub.dispatchEvent(Event{Kind: Kind(i)})
		}
	}()

	// All 1000 pushes must complete promptly (well under 1s).
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("dispatchEvent blocked — read loop would be stalled")
	}

	sub.cancel(nil)
	for range sub.Events {
	}
}

// TestInboxDoesNotBlockOtherSubscriptions (bahia-irsry.58): a blocked consumer
// on subscription A must not delay event delivery to subscription B on the
// same relay connection. With the old channel-based inbox, a full inbox blocked
// dispatchEvent on the main loop, stalling all other subscriptions.
func TestInboxDoesNotBlockOtherSubscriptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})

	subA := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	subB := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	subA.live.Store(true)
	subB.live.Store(true)
	subA.dispatchEose(nil)
	subB.dispatchEose(nil)
	<-subA.EndOfStoredEvents
	<-subB.EndOfStoredEvents

	// Fill sub A's inbox well beyond the old 256-slot channel without
	// reading. With the non-blocking inbox this completes immediately.
	for i := range 2000 {
		subA.dispatchEvent(Event{Kind: Kind(i)})
	}

	// Now dispatch to sub B from a goroutine that simulates the read loop.
	// This must not block even though sub A has thousands of pending events.
	bDone := make(chan struct{})
	go func() {
		subB.dispatchEvent(Event{Kind: 42})
		close(bDone)
	}()

	select {
	case <-bDone:
		// good: push to B was non-blocking
	case <-time.After(time.Second):
		t.Fatal("dispatchEvent for sub B blocked — head-of-line blocking detected")
	}

	// Sub B must receive the event promptly.
	select {
	case evt := <-subB.Events:
		if evt.Kind != 42 {
			t.Fatalf("wrong kind: got %d, want 42", evt.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("sub B did not receive event")
	}

	cancel()
	for range subA.Events {
	}
	for range subB.Events {
	}
}

// TestPublishFromEventHandlerDoesNotDeadlock (bahia-irsry.58): a consumer that
// publishes to the same relay and waits for OK inside its event handler must
// not deadlock. The relay's read loop must remain free to deliver the OK after
// dispatching events.
//
// Scenario: the read loop dispatches 500 events (more than the old 256-slot
// channel) to sub A, then delivers an OK callback. If dispatchEvent blocked
// the read loop (as with the old bounded channel), the OK would never be
// delivered while the inbox was full — deadlocking the consumer.
func TestPublishFromEventHandlerDoesNotDeadlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})

	sub := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	sub.live.Store(true)
	sub.dispatchEose(nil)
	<-sub.EndOfStoredEvents

	// Simulate the read loop: dispatch events then deliver an OK.
	okDelivered := make(chan struct{})
	readLoopDone := make(chan struct{})
	go func() {
		defer close(readLoopDone)
		// Dispatch 500 events (more than old cap 256). With non-blocking
		// inbox, all pushes complete without blocking.
		for i := range 500 {
			sub.dispatchEvent(Event{Kind: Kind(i)})
		}
		// After dispatching, the read loop processes an OK callback.
		close(okDelivered)
	}()

	// The consumer reads the first event, "publishes" to the same relay,
	// and waits for the OK (simulated by okDelivered). With a blocking
	// inbox, the read loop would be stuck at event ~257, and this would
	// deadlock.
	select {
	case <-sub.Events:
		// Consumer got an event. "Publish" and wait for OK.
		select {
		case <-okDelivered:
			// OK delivered: no deadlock.
		case <-time.After(5 * time.Second):
			t.Fatal("OK callback not delivered — deadlock: read loop blocked by full inbox")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event received")
	}

	<-readLoopDone
	cancel()
	for range sub.Events {
	}
}

// TestInboxOverflowClosesSubscription (bahia-irsry.58): when the inbox exceeds
// its capacity without the consumer reading, the subscription must be closed
// with an overflow reason. No events may be silently dropped — the consumer
// knows the subscription was closed and resubscribes from its cursor.
func TestInboxOverflowClosesSubscription(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})

	sub := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	sub.live.Store(true)
	sub.dispatchEose(nil)
	<-sub.EndOfStoredEvents

	// Use a small cap so overflow is guaranteed within a few pushes,
	// regardless of how the dispatcher schedules relative to pushes.
	const testCap = 8
	sub.inbox.mu.Lock()
	sub.inbox.cap = testCap
	sub.inbox.mu.Unlock()

	// Dispatch many more events than the test cap without reading Events.
	// All pushes must complete without blocking.
	const totalEvents = 200
	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		for i := range totalEvents {
			sub.dispatchEvent(Event{Kind: Kind(i)})
		}
	}()

	select {
	case <-dispatched:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatches blocked — inbox push is not non-blocking")
	}

	// The subscription must be closed with an overflow reason.
	select {
	case reason := <-sub.ClosedReason:
		if !strings.Contains(reason, "overflow") {
			t.Fatalf("wrong close reason: %q (expected to contain \"overflow\")", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription not closed after overflow")
	}

	// Context must be canceled.
	select {
	case <-sub.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("context not canceled after overflow close")
	}

	// Events channel must eventually be closed (teardown completes).
	for range sub.Events {
	}
}
