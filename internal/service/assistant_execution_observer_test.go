package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

type assistantObserverResult struct {
	out AssistantAsyncObservationOutcome
	err error
}

func TestAssistantExecutionObserverKeepsLiveSubscriptionAfterEOSE(t *testing.T) {
	subscriber := newBlockingAssistantTestSubscriber()
	observer := &AssistantExecutionObserver{Subscriber: subscriber}
	result := make(chan assistantObserverResult, 1)
	receipt := &domain.AsyncToolReceipt{ToolName: "mutate", RequestEventID: "request-1", ResultKinds: []int{7961}}
	go func() {
		out, err := observer.ObserveAssistantAsyncResult(context.Background(), "session", "work", "mutate", receipt)
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
	select {
	case got := <-result:
		if got.err != nil || got.out.Status != "completed" {
			t.Fatalf("observation=%+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live result not observed")
	}
}

func TestAssistantExecutionObserverClosedAndAUTHBlock(t *testing.T) {
	for _, reason := range []string{"closed by relay", "auth-required"} {
		t.Run(reason, func(t *testing.T) {
			subscriber := newBlockingAssistantTestSubscriber()
			observer := &AssistantExecutionObserver{Subscriber: subscriber}
			result := make(chan assistantObserverResult, 1)
			go func() {
				out, err := observer.ObserveAssistantAsyncResult(context.Background(), "s", "w", "mutate", &domain.AsyncToolReceipt{ToolName: "mutate", RequestEventID: "request", ResultKinds: []int{7961}})
				result <- assistantObserverResult{out, err}
			}()
			subscriber.waitForSubscription(t)
			subscriber.mu.Lock()
			sub := subscriber.sub
			subscriber.mu.Unlock()
			sub.closed <- AssistantRelayClosed{RelayURL: "relay.test", Reason: reason}
			select {
			case got := <-result:
				if got.err == nil || got.out.Status != "blocked" || !strings.Contains(got.err.Error(), reason) {
					t.Fatalf("closed result=%+v", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("closed observer did not block")
			}
		})
	}
}
