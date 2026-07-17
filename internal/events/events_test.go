package events

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestInProcessPublisher(t *testing.T) {
	logger := zap.NewNop()
	pub := NewInProcessPublisher(logger)

	var mu sync.Mutex
	var received []Event
	var wg sync.WaitGroup
	wg.Add(1)

	pub.Subscribe(EventBuildRegistered, func(ctx context.Context, e Event) {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		received = append(received, e)
	})

	pub.Publish(context.Background(), Event{
		Type:     EventBuildRegistered,
		EntityID: "test-123",
		Data:     "test data",
	})

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}

	if received[0].EntityID != "test-123" {
		t.Errorf("expected entity ID test-123, got %s", received[0].EntityID)
	}
}

func TestInProcessPublisherMultipleHandlers(t *testing.T) {
	logger := zap.NewNop()
	pub := NewInProcessPublisher(logger)

	var mu sync.Mutex
	count := 0
	var wg sync.WaitGroup
	wg.Add(3)

	for i := 0; i < 3; i++ {
		pub.Subscribe(EventArtifactRegistered, func(ctx context.Context, e Event) {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			count++
		})
	}

	pub.Publish(context.Background(), Event{Type: EventArtifactRegistered})
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if count != 3 {
		t.Errorf("expected 3 handler calls, got %d", count)
	}
}

func TestInProcessPublisherRetriesAndLogsHandlerFailures(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	pub := NewInProcessPublisher(zap.New(core))
	attempts := 0
	done := make(chan struct{})
	sentinel := errors.New("temporary handler failure")

	pub.SubscribeWithError(EventBuildRegistered, func(context.Context, Event) error {
		attempts++
		if attempts < 3 {
			return sentinel
		}
		close(done)
		return nil
	})
	pub.Publish(context.Background(), Event{Type: EventBuildRegistered, EntityID: "build-123"})
	<-done

	if attempts != 3 {
		t.Fatalf("handler attempts = %d, want 3", attempts)
	}
	entries := logs.FilterMessage("event handler failed").All()
	if len(entries) != 2 {
		t.Fatalf("failure log count = %d, want 2", len(entries))
	}
	if entries[0].ContextMap()["entity_id"] != "build-123" {
		t.Fatalf("failure log missing entity ID: %+v", entries[0].ContextMap())
	}
}

func TestNoopPublisher(t *testing.T) {
	pub := &NoopPublisher{}
	// Should not panic.
	pub.Publish(context.Background(), Event{})
	pub.Subscribe(EventBuildRegistered, func(ctx context.Context, e Event) {})
}

func TestPublisherPanicRecovery(t *testing.T) {
	logger := zap.NewNop()
	pub := NewInProcessPublisher(logger)

	var wg sync.WaitGroup
	wg.Add(1)

	pub.Subscribe(EventBuildRegistered, func(ctx context.Context, e Event) {
		defer wg.Done()
		panic("test panic")
	})

	// Should not panic the caller.
	pub.Publish(context.Background(), Event{Type: EventBuildRegistered})
	wg.Wait()
}
