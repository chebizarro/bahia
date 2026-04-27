package events

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestInProcessPublisher(t *testing.T) {
	logger := zap.NewNop()
	pub := NewInProcessPublisher(logger)

	var mu sync.Mutex
	var received []Event

	pub.Subscribe(EventBuildRegistered, func(ctx context.Context, e Event) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, e)
	})

	pub.Publish(context.Background(), Event{
		Type:     EventBuildRegistered,
		EntityID: "test-123",
		Data:     "test data",
	})

	// Wait for async handler to complete.
	time.Sleep(50 * time.Millisecond)

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

	for i := 0; i < 3; i++ {
		pub.Subscribe(EventArtifactRegistered, func(ctx context.Context, e Event) {
			mu.Lock()
			defer mu.Unlock()
			count++
		})
	}

	pub.Publish(context.Background(), Event{Type: EventArtifactRegistered})
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if count != 3 {
		t.Errorf("expected 3 handler calls, got %d", count)
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

	pub.Subscribe(EventBuildRegistered, func(ctx context.Context, e Event) {
		panic("test panic")
	})

	// Should not panic the caller.
	pub.Publish(context.Background(), Event{Type: EventBuildRegistered})
	time.Sleep(50 * time.Millisecond)
}
