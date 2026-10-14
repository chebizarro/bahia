package nostr

import (
	"context"
	"math"
	"runtime"
	"testing"
	"time"
)

// A COUNT reply can arrive as soon as the subscription is published. Keep
// observing the map from another goroutine so -race also catches moving the
// countResult assignment back after Store.
func TestCountResultReadyWhenSubscriptionPublished(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	relay := NewRelay(ctx, "ws://127.0.0.1:1", RelayOptions{})
	done := make(chan struct{})
	observed := make(chan bool, 1)
	go func() {
		defer close(observed)
		for {
			select {
			case <-done:
				return
			default:
			}
			for _, sub := range relay.Subscriptions.Range {
				if sub.countResult == nil {
					observed <- false
					return
				}
			}
			runtime.Gosched()
		}
	}()
	for range 128 {
		sub := relay.PrepareSubscription(ctx, Filter{}, SubscriptionOptions{isCount: true, MaxWaitForEOSE: math.MaxInt64})
		if sub.countResult == nil {
			t.Fatal("COUNT channel was not initialized before publication")
		}
		count := uint32(1)
		sub.dispatchCount(CountEnvelope{Count: &count})
		select {
		case got := <-sub.countResult:
			if got.Count == nil || *got.Count != 1 {
				t.Fatalf("wrong COUNT result: %+v", got)
			}
		case <-ctx.Done():
			t.Fatal("COUNT result was lost")
		}
		sub.cancel(nil)
		for range sub.Events {
		}
	}
	close(done)
	if value, ok := <-observed; ok && !value {
		t.Fatal("published COUNT subscription had no result channel")
	}
}
