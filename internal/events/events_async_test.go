package events

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPublishAsyncHandlerSurvivesCanceledRequestContext(t *testing.T) {
	pub := NewInProcessPublisher(zap.NewNop())
	received := make(chan error, 1)

	pub.Subscribe(EventDeploymentIntentApproved, func(ctx context.Context, _ Event) {
		select {
		case <-ctx.Done():
			received <- ctx.Err()
		default:
			received <- nil
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pub.Publish(ctx, Event{Type: EventDeploymentIntentApproved, EntityID: "test"})

	select {
	case err := <-received:
		if err != nil {
			t.Fatalf("handler received canceled context: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async handler")
	}
}

func TestPublishAsyncHandlerKeepsDeadlineContextAliveAfterPublishReturns(t *testing.T) {
	pub := NewInProcessPublisher(zap.NewNop())
	entered := make(chan struct{})
	release := make(chan struct{})
	received := make(chan error, 1)

	pub.Subscribe(EventDeploymentIntentApproved, func(ctx context.Context, _ Event) {
		close(entered)
		<-release
		select {
		case <-ctx.Done():
			received <- ctx.Err()
		default:
			received <- nil
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	pub.Publish(ctx, Event{Type: EventDeploymentIntentApproved, EntityID: "test"})

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async handler to start")
	}
	close(release)

	select {
	case err := <-received:
		if err != nil {
			t.Fatalf("handler context was canceled immediately after Publish returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async handler")
	}
}
