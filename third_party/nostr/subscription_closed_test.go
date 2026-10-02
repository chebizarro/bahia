package nostr

import (
	"context"
	"testing"
	"time"
)

func closedTestSubscription(t *testing.T) *Subscription {
	t.Helper()
	ctx, cancel := context.WithCancelCause(t.Context())
	sub := &Subscription{
		Context:           ctx,
		cancel:            cancel,
		Events:            make(chan Event),
		EndOfStoredEvents: make(chan EndOfStoredEvent, 1),
		ClosedReason:      make(chan string, 1),
		eoseTimedOut:      make(chan struct{}),
	}
	sub.live.Store(true)
	t.Cleanup(func() {
		cancel(nil)
		done := make(chan struct{})
		go func() { sub.storedwg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("pending event delivery did not stop after cancellation")
		}
	})
	return sub
}

func TestClosedDrainsStoredEvents(t *testing.T) {
	for _, withEOSE := range []bool{false, true} {
		name := "without_eose"
		if withEOSE {
			name = "after_eose"
		}
		t.Run(name, func(t *testing.T) {
			sub := closedTestSubscription(t)
			const count = 32
			for i := range count {
				sub.dispatchEvent(Event{Content: string(rune('a' + i))})
			}
			if withEOSE {
				sub.dispatchEose(nil)
			}
			sub.handleClosed("finished")

			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			seen := make(map[string]bool)
			for len(seen) < count {
				select {
				case event := <-sub.Events:
					if seen[event.Content] {
						t.Fatal("duplicate event")
					}
					seen[event.Content] = true
				case reason := <-sub.ClosedReason:
					t.Fatalf("CLOSED %q overtook pending events: received %d/%d", reason, len(seen), count)
				case <-sub.Context.Done():
					t.Fatalf("subscription canceled before delivery: received %d/%d", len(seen), count)
				case <-ctx.Done():
					t.Fatal("event delivery timed out")
				}
			}
			select {
			case reason := <-sub.ClosedReason:
				if reason != "finished" {
					t.Fatalf("wrong reason: %q", reason)
				}
			case <-ctx.Done():
				t.Fatal("CLOSED not delivered after pending events")
			}
		})
	}
}

func TestClosedUnblocksWhenPendingDeliveryIsCanceled(t *testing.T) {
	sub := closedTestSubscription(t)
	sub.dispatchEvent(Event{Content: "unread"})
	sub.dispatchEose(nil)
	sub.handleClosed("finished")
	sub.Unsub()
	select {
	case <-sub.ClosedReason:
	case <-time.After(time.Second):
		t.Fatal("CLOSED handler stuck after cancellation")
	}
}

func TestClosedWithoutEvents(t *testing.T) {
	sub := closedTestSubscription(t)
	sub.handleClosed("auth-required: authenticate first")
	select {
	case reason := <-sub.ClosedReason:
		if reason != "auth-required: authenticate first" {
			t.Fatalf("wrong reason: %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("empty subscription rejection timed out")
	}
	select {
	case <-sub.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("rejected subscription was not canceled")
	}
}

// TestSecondClosedDoesNotLeak (bahia-irsry.26): a second CLOSED from the relay
// must not start another goroutine. Before the fix, handleClosed unconditionally
// started a goroutine that waited on storedwg and sent to ClosedReason; a
// second call leaked the goroutine (it blocked forever on the buffered-1 send).
func TestSecondClosedDoesNotLeak(t *testing.T) {
	sub := closedTestSubscription(t)
	sub.handleClosed("first")
	sub.handleClosed("second") // must be a no-op

	select {
	case reason := <-sub.ClosedReason:
		if reason != "first" {
			t.Fatalf("wrong reason: %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("CLOSED not delivered")
	}

	// ClosedReason has capacity 1; if a second goroutine ran, it would
	// block forever on the send (leak). Verify no second reason arrives.
	select {
	case reason := <-sub.ClosedReason:
		t.Fatalf("second CLOSED leaked: %q", reason)
	case <-time.After(100 * time.Millisecond):
		// good: no second reason
	}
}
