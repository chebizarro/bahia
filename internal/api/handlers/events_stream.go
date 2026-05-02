package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

// StreamEvent is the JSON payload sent to SSE/WebSocket clients.
type StreamEvent struct {
	Type     string `json:"type"`
	EntityID string `json:"entity_id,omitempty"`
	Data     any    `json:"data,omitempty"`
	Time     string `json:"time"`
}

// EventStreamHub manages SSE client connections and broadcasts events.
// It subscribes to the internal event publisher and fans out to connected clients.
type EventStreamHub struct {
	mu      sync.RWMutex
	clients map[*sseClient]struct{}
	logger  *zap.Logger
}

type sseClient struct {
	ch      chan StreamEvent
	filters sseFilters
}

type sseFilters struct {
	serviceID     string
	environmentID string
	eventTypes    map[string]bool
}

// NewEventStreamHub creates a hub and subscribes to all internal event types.
func NewEventStreamHub(publisher events.Publisher, logger *zap.Logger) *EventStreamHub {
	hub := &EventStreamHub{
		clients: make(map[*sseClient]struct{}),
		logger:  logger,
	}

	// Subscribe to all event types.
	allTypes := []events.EventType{
		events.EventBuildRegistered,
		events.EventBuildStatusChanged,
		events.EventArtifactRegistered,
		events.EventDeploymentIntentCreated,
		events.EventDeploymentIntentApproved,
		events.EventDeploymentRunCreated,
		events.EventDeploymentRunCompleted,
		events.EventRuntimeObservation,
		events.EventDriftDetected,
		events.EventReconcileCompleted,
	}

	for _, et := range allTypes {
		et := et
		publisher.Subscribe(et, func(ctx context.Context, e events.Event) {
			hub.broadcast(StreamEvent{
				Type:     string(e.Type),
				EntityID: e.EntityID,
				Data:     e.Data,
				Time:     time.Now().UTC().Format(time.RFC3339),
			})
		})
	}

	return hub
}

// broadcast sends an event to all connected clients that match the filters.
func (h *EventStreamHub) broadcast(ev StreamEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		if !client.filters.matches(ev) {
			continue
		}
		// Non-blocking send with backpressure: drop if buffer is full.
		select {
		case client.ch <- ev:
		default:
			h.logger.Debug("dropping event for slow SSE client",
				zap.String("event_type", ev.Type),
			)
		}
	}
}

func (f *sseFilters) matches(ev StreamEvent) bool {
	// Type filter.
	if len(f.eventTypes) > 0 && !f.eventTypes[ev.Type] {
		return false
	}
	// Service/environment filter could be checked against ev.Data,
	// but we'd need to type-assert. For now, accept all if no entity-level
	// filtering is implemented (would require event data introspection).
	return true
}

// addClient registers a new SSE client.
func (h *EventStreamHub) addClient(c *sseClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

// removeClient unregisters an SSE client.
func (h *EventStreamHub) removeClient(c *sseClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	close(c.ch)
}

// ClientCount returns the number of connected clients (for metrics/health).
func (h *EventStreamHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// StreamSSE handles the SSE endpoint.
// GET /api/v1/events/stream?types=build.registered,drift.detected
func (h *EventStreamHub) StreamSSE(w http.ResponseWriter, r *http.Request) {
	writeDeprecationHeaders(w)

	// Check that we can flush (required for SSE).
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Parse filters from query params.
	filters := sseFilters{
		serviceID:     r.URL.Query().Get("service"),
		environmentID: r.URL.Query().Get("environment"),
	}
	if types := r.URL.Query().Get("types"); types != "" {
		filters.eventTypes = make(map[string]bool)
		for _, t := range splitCSV(types) {
			filters.eventTypes[t] = true
		}
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Register client with a buffered channel (backpressure).
	client := &sseClient{
		ch:      make(chan StreamEvent, 64),
		filters: filters,
	}
	h.addClient(client)
	defer h.removeClient(client)

	h.logger.Info("SSE client connected",
		zap.Int("total_clients", h.ClientCount()),
	)

	// Heartbeat ticker.
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			h.logger.Info("SSE client disconnected")
			return
		case ev := <-client.ch:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// splitCSV splits a comma-separated string into trimmed parts.
func splitCSV(s string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			// Trim spaces.
			for len(part) > 0 && part[0] == ' ' {
				part = part[1:]
			}
			for len(part) > 0 && part[len(part)-1] == ' ' {
				part = part[:len(part)-1]
			}
			if part != "" {
				parts = append(parts, part)
			}
			start = i + 1
		}
	}
	return parts
}
