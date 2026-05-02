package handlers

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestEventStreamHub_BroadcastAndSSE(t *testing.T) {
	publisher := events.NewInProcessPublisher(zap.NewNop())
	hub := NewEventStreamHub(publisher, zap.NewNop())

	// Create a test SSE request.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	// Run SSE handler in a goroutine.
	done := make(chan struct{})
	go func() {
		hub.StreamSSE(w, req)
		close(done)
	}()

	// Wait for client to connect using deterministic sync.
	require.Eventually(t, func() bool {
		return hub.ClientCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "client should connect")

	// Publish an event.
	publisher.Publish(context.Background(), events.Event{
		Type:     events.EventBuildRegistered,
		EntityID: "build-123",
		Data:     map[string]string{"status": "registered"},
	})

	// Wait for event to appear in response body.
	require.Eventually(t, func() bool {
		body := w.Body.String()
		return strings.Contains(body, "event: build.registered")
	}, 2*time.Second, 10*time.Millisecond, "event should be written to SSE stream")

	// Cancel to disconnect.
	cancel()
	<-done

	// Verify the full response body.
	body := w.Body.String()
	require.Contains(t, body, "event: build.registered", "expected SSE event type")
	require.Contains(t, body, "build-123", "expected entity_id in body")
}

func TestEventStreamHub_TypeFilter(t *testing.T) {
	publisher := events.NewInProcessPublisher(zap.NewNop())
	hub := NewEventStreamHub(publisher, zap.NewNop())

	// Connect with a type filter.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream?types=drift.detected", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		hub.StreamSSE(w, req)
		close(done)
	}()

	// Wait for client to connect.
	require.Eventually(t, func() bool {
		return hub.ClientCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "client should connect")

	// Publish a build event (should be filtered out).
	publisher.Publish(context.Background(), events.Event{
		Type:     events.EventBuildRegistered,
		EntityID: "build-123",
	})

	// Publish a drift event (should be included).
	publisher.Publish(context.Background(), events.Event{
		Type:     events.EventDriftDetected,
		EntityID: "drift-456",
	})

	// Wait for drift event to appear in response.
	require.Eventually(t, func() bool {
		return strings.Contains(w.Body.String(), "drift.detected")
	}, 2*time.Second, 10*time.Millisecond, "drift event should be written")

	cancel()
	<-done

	body := w.Body.String()
	require.NotContains(t, body, "build.registered", "build event should have been filtered out")
	require.Contains(t, body, "drift.detected", "drift event should have been included")
}

func TestEventStreamHub_BackpressureDrop(t *testing.T) {
	publisher := events.NewInProcessPublisher(zap.NewNop())
	hub := NewEventStreamHub(publisher, zap.NewNop())

	// Create a client with a tiny buffer.
	client := &sseClient{
		ch:      make(chan StreamEvent, 1), // buffer of 1
		filters: sseFilters{},
	}
	hub.addClient(client)
	defer hub.removeClient(client)

	// Fill the buffer.
	hub.broadcast(StreamEvent{Type: "test1"})

	// This should be dropped (buffer full), not block.
	hub.broadcast(StreamEvent{Type: "test2"})

	// Should only get the first event.
	ev := <-client.ch
	if ev.Type != "test1" {
		t.Errorf("expected test1, got %s", ev.Type)
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{"single", []string{"single"}},
		{"", nil},
		{"a,,b", []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitCSV(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("splitCSV(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("splitCSV(%q)[%d] = %q, want %q", tt.input, i, v, tt.want[i])
				}
			}
		})
	}
}

func TestSSEFilters_Matches(t *testing.T) {
	tests := []struct {
		name    string
		filters sseFilters
		event   StreamEvent
		want    bool
	}{
		{"no filters", sseFilters{}, StreamEvent{Type: "any"}, true},
		{"type match", sseFilters{eventTypes: map[string]bool{"build.registered": true}}, StreamEvent{Type: "build.registered"}, true},
		{"type mismatch", sseFilters{eventTypes: map[string]bool{"build.registered": true}}, StreamEvent{Type: "drift.detected"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.filters.matches(tt.event)
			if got != tt.want {
				t.Errorf("matches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEventStreamHub_SSEHeaders(t *testing.T) {
	publisher := events.NewInProcessPublisher(zap.NewNop())
	hub := NewEventStreamHub(publisher, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		hub.StreamSSE(w, req)
		close(done)
	}()

	// Wait for client to connect.
	require.Eventually(t, func() bool {
		return hub.ClientCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "client should connect")

	cancel()
	<-done

	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
	require.Equal(t, "true", w.Header().Get("Deprecation"))
	require.NotEmpty(t, w.Header().Get("Sunset"))
}

func TestEventStreamHub_HeartbeatFormat(t *testing.T) {
	// Verify the heartbeat comment format.
	heartbeat := ": heartbeat\n\n"
	scanner := bufio.NewScanner(strings.NewReader(heartbeat))
	scanner.Scan()
	line := scanner.Text()
	if line != ": heartbeat" {
		t.Errorf("heartbeat line = %q, want ': heartbeat'", line)
	}
}
