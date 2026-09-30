package nostr

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestClosedTeardownDoesNotRaceInFlightDispatch (bahia-irsry.17): a CLOSED
// that arrives while live (post-EOSE) events are still being dispatched must
// not let the teardown goroutine close sub.Events underneath a sender. Before
// the fix this reported a data race under -race and could panic with
// "send on closed channel".
func TestClosedTeardownDoesNotRaceInFlightDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})

	for round := range 200 {
		sub := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
		sub.live.Store(true)
		sub.dispatchEose(nil)
		<-sub.EndOfStoredEvents

		for range 32 {
			sub.dispatchEvent(Event{Kind: 1})
		}
		// read a couple so some senders are mid-handoff when CLOSED lands
		for range 2 {
			<-sub.Events
		}
		sub.handleClosed("error: overflow")

		select {
		case <-sub.ClosedReason:
		case <-ctx.Done():
			t.Fatalf("round %d: CLOSED not delivered", round)
		}
		// Events must be closed exactly once, after every sender is gone.
		for range sub.Events {
		}
	}
}

// TestCountAfterTeardownIsDropped: a late COUNT frame for a subscription that
// has already been torn down must not send on the closed countResult channel.
func TestCountAfterTeardownIsDropped(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})
	sub := r.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
	sub.countResult = make(chan CountEnvelope, 1)
	sub.cancel(nil)
	for range sub.Events {
	}
	sub.dispatchCount(CountEnvelope{}) // must not panic
}
