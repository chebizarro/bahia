// Package events defines the internal event system for Bahia.
// Events are published in-process and optionally forwarded to Nostr relays.
package events

import (
	"context"
	"sync"

	"go.uber.org/zap"
)

// EventType identifies the kind of event.
type EventType string

const (
	EventBuildRegistered          EventType = "build.registered"
	EventBuildStatusChanged       EventType = "build.status_changed"
	EventArtifactRegistered       EventType = "artifact.registered"
	EventDeploymentIntentCreated  EventType = "deployment_intent.created"
	EventDeploymentIntentApproved EventType = "deployment_intent.approved"
	EventDeploymentRunCreated     EventType = "deployment_run.created"
	EventDeploymentRunCompleted   EventType = "deployment_run.completed"
	EventRuntimeObservation       EventType = "runtime.observation"
	EventDriftDetected            EventType = "drift.detected"
	EventReconcileCompleted       EventType = "reconcile.completed"
	EventAdoptionImported         EventType = "adoption.imported"
	EventAdoptionScanCompleted    EventType = "adoption.scan_completed"
	EventRuntimeDeploy            EventType = "runtime.deploy"
	EventRuntimeRestart           EventType = "runtime.restart"
	EventRuntimeStop              EventType = "runtime.stop"
)

// Event represents an internal domain event.
type Event struct {
	Type     EventType
	EntityID string
	Data     any
}

// Handler is a function that processes an event.
type Handler func(ctx context.Context, e Event)

// Publisher publishes events to subscribers.
type Publisher interface {
	Publish(ctx context.Context, e Event)
	Subscribe(eventType EventType, handler Handler)
}

// InProcessPublisher is an in-memory event publisher.
type InProcessPublisher struct {
	mu       sync.RWMutex
	handlers map[EventType][]Handler
	logger   *zap.Logger
}

// NewInProcessPublisher creates a new InProcessPublisher.
func NewInProcessPublisher(logger *zap.Logger) *InProcessPublisher {
	return &InProcessPublisher{
		handlers: make(map[EventType][]Handler),
		logger:   logger,
	}
}

// Publish dispatches an event to all registered handlers asynchronously.
func (p *InProcessPublisher) Publish(ctx context.Context, e Event) {
	p.mu.RLock()
	handlers := p.handlers[e.Type]
	p.mu.RUnlock()

	for _, h := range handlers {
		h := h
		go func() {
			defer func() {
				if r := recover(); r != nil {
					p.logger.Error("event handler panic",
						zap.String("event_type", string(e.Type)),
						zap.Any("panic", r),
					)
				}
			}()
			h(ctx, e)
		}()
	}
}

// Subscribe registers a handler for a specific event type.
func (p *InProcessPublisher) Subscribe(eventType EventType, handler Handler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handlers[eventType] = append(p.handlers[eventType], handler)
}

// NoopPublisher is a publisher that does nothing. Useful for testing.
type NoopPublisher struct{}

func (n *NoopPublisher) Publish(_ context.Context, _ Event) {}
func (n *NoopPublisher) Subscribe(_ EventType, _ Handler)   {}
