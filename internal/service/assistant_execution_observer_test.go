package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

type assistantObserverResult struct {
	out AssistantAsyncObservationOutcome
	err error
}

func waitAssistantObserverResult(t *testing.T, ch <-chan assistantObserverResult) assistantObserverResult {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("observer did not return")
		return assistantObserverResult{}
	}
}

func TestAssistantExecutionObserverKeepsLiveSubscriptionAfterEOSE(t *testing.T) {
	subscriber := newBlockingAssistantTestSubscriber()
	observer := &AssistantExecutionObserver{Subscriber: subscriber}
	result := make(chan assistantObserverResult, 1)
	receipt := &domain.AsyncToolReceipt{ToolName: "mutate", RequestEventID: "request-1", ResultKinds: []int{7961}}
	go func() {
		out, err := observer.ObserveWork(context.Background(), AssistantWorkObservationRequest{SessionID: "session", WorkID: "work", ToolName: "mutate", Receipt: receipt})
		result <- assistantObserverResult{out, err}
	}()
	subscriber.waitForSubscription(t)
	subscriber.mu.Lock()
	sub := subscriber.sub
	subscriber.mu.Unlock()
	sub.eose <- struct{}{}
	select {
	case <-result:
		t.Fatal("EOSE incorrectly completed observation")
	default:
	}
	subscriber.publishResult(assistantSignedResultEvent(t, "unrelated", 7961, "another-request", "completed"))
	subscriber.publishResult(assistantSignedResultEvent(t, "terminal", 7961, "request-1", "completed"))
	got := waitAssistantObserverResult(t, result)
	if got.err != nil || got.out.Status != "completed" {
		t.Fatalf("observation=%+v", got)
	}
}

// CLOSED (including auth-required) blocks observation without inferring any
// outcome; the subscription is reissued after reconnect backoff and the fresh
// backfill delivers a result that arrived while blind.
func TestAssistantExecutionObserverClosedAndAUTHBlockThenReissue(t *testing.T) {
	for _, reason := range []string{"error: shutting down", "auth-required: authenticate first"} {
		t.Run(reason, func(t *testing.T) {
			relay := newAssistantTestRelay()
			reissue := make(chan struct{})
			observer := &AssistantExecutionObserver{Subscriber: relay, ReissueWait: func(ctx context.Context, attempt int) error {
				select {
				case <-reissue:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			var mu sync.Mutex
			signals := []AssistantObservationSignal{}
			signalled := make(chan struct{}, 8)
			result := make(chan assistantObserverResult, 1)
			go func() {
				out, err := observer.ObserveWork(context.Background(), AssistantWorkObservationRequest{SessionID: "s", RunID: "r", WorkID: "w", ToolName: "mutate", Receipt: &domain.AsyncToolReceipt{ToolName: "mutate", RequestEventID: "request", ResultKinds: []int{7961}},
					OnSignal: func(sig AssistantObservationSignal) {
						mu.Lock()
						signals = append(signals, sig)
						mu.Unlock()
						signalled <- struct{}{}
					}})
				result <- assistantObserverResult{out, err}
			}()
			relay.waitFor(t, "subscription", func() bool { return relay.liveSubs(7961) == 1 })
			<-signalled // live after EOSE
			relay.closeMatching(7961, reason)
			blocked := func() bool {
				mu.Lock()
				defer mu.Unlock()
				for _, sig := range signals {
					if sig.Blocked && strings.Contains(sig.Reason, reason) {
						return true
					}
				}
				return false
			}
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			for !blocked() {
				select {
				case <-signalled:
				case <-deadline.C:
					t.Fatal("no blocked signal after CLOSED")
				}
			}
			select {
			case got := <-result:
				t.Fatalf("CLOSED completed observation: %+v", got)
			default:
			}
			publishAssistantResult(t, relay, "request", "failed")
			reissue <- struct{}{}
			got := waitAssistantObserverResult(t, result)
			if got.err != nil || got.out.Status != "failed" {
				t.Fatalf("reissued observation=%+v", got)
			}
		})
	}
}

// Only signed, correctly correlated events from the expected author count.
func TestAssistantExecutionObserverValidatesProvenance(t *testing.T) {
	relay := newAssistantTestRelay()
	authorKey := nostr.Generate()
	receipt := &domain.AsyncToolReceipt{ToolName: "mutate", RequestEventID: "request", ResultKinds: []int{7961}, ResourceTags: map[string]string{"author": nostr.GetPublicKey(authorKey).Hex()}}
	observer := &AssistantExecutionObserver{Subscriber: relay}
	result := make(chan assistantObserverResult, 1)
	go func() {
		out, err := observer.ObserveWork(context.Background(), AssistantWorkObservationRequest{ToolName: "mutate", Receipt: receipt})
		result <- assistantObserverResult{out, err}
	}()
	relay.waitFor(t, "subscription", func() bool { return relay.liveSubs(7961) == 1 })
	// Wrong author: filtered by the relay and rejected locally.
	publishAssistantResult(t, relay, "request", "completed")
	sign := func(status string) *nostr.Event {
		ev := &nostr.Event{Kind: 7961, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"e", "request"}, {"status", status}}, Content: `{"status":"` + status + `"}`}
		if err := ev.Sign(authorKey); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	pending := sign("pending")
	if _, err := relay.Publish(context.Background(), *pending); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		t.Fatalf("non-terminal or foreign event completed observation: %+v", got)
	default:
	}
	terminal := sign("completed")
	if _, err := relay.Publish(context.Background(), *terminal); err != nil {
		t.Fatal(err)
	}
	got := waitAssistantObserverResult(t, result)
	if got.err != nil || got.out.Event == nil || got.out.Event.ID != terminal.ID {
		t.Fatalf("observation=%+v", got)
	}
}
