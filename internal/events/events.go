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
	EventServiceCreated                 EventType = "service.created"
	EventServiceUpdated                 EventType = "service.updated"
	EventServiceDeleted                 EventType = "service.deleted"
	EventEnvironmentCreated             EventType = "environment.created"
	EventEnvironmentUpdated             EventType = "environment.updated"
	EventEnvironmentDeleted             EventType = "environment.deleted"
	EventBuildRegistered                EventType = "build.registered"
	EventBuildStatusChanged             EventType = "build.status_changed"
	EventArtifactRegistered             EventType = "artifact.registered"
	EventDeploymentIntentCreated        EventType = "deployment_intent.created"
	EventDeploymentIntentApproved       EventType = "deployment_intent.approved"
	EventDeploymentIntentRejected       EventType = "deployment_intent.rejected"
	EventDeploymentRunCreated           EventType = "deployment_run.created"
	EventDeploymentRunStatusChanged     EventType = "deployment_run.status_changed"
	EventDeploymentRunCompleted         EventType = "deployment_run.completed"
	EventRuntimeObservation             EventType = "runtime.observation"
	EventEnvironmentServiceStateChanged EventType = "environment_service_state.changed"
	EventDriftDetected                  EventType = "drift.detected"
	EventReconcileCompleted             EventType = "reconcile.completed"
	EventAdoptionImported               EventType = "adoption.imported"
	EventAdoptionScanCompleted          EventType = "adoption.scan_completed"
	EventRuntimeDeploy                  EventType = "runtime.deploy"
	EventRuntimeRestart                 EventType = "runtime.restart"
	EventRuntimeStop                    EventType = "runtime.stop"
	EventLLMRouteCreated                EventType = "llm_route.created"
	EventLLMRouteUpdated                EventType = "llm_route.updated"
	EventLLMReleaseRegistered           EventType = "llm_release.registered"
	EventLLMDeploymentIntentCreated     EventType = "llm_deployment_intent.created"
	EventLLMDeploymentIntentApproved    EventType = "llm_deployment_intent.approved"
	EventLLMDeploymentIntentRejected    EventType = "llm_deployment_intent.rejected"
	EventLLMDeploymentRunCreated        EventType = "llm_deployment_run.created"
	EventLLMDeploymentRunStatusChanged  EventType = "llm_deployment_run.status_changed"
	EventLLMDeploymentRunCompleted      EventType = "llm_deployment_run.completed"
	EventLLMRouteObservation            EventType = "llm_route.observation"
	EventLLMRouteStateChanged           EventType = "llm_route_state.changed"
	EventLLMRouteDriftDetected          EventType = "llm_route.drift_detected"
	EventLLMGatewayRouteSynced          EventType = "llm_gateway_route.synced"
	EventToolProvisionApprovalRequired  EventType = "tool_provision.approval_required"
	EventToolProvisionCompleted         EventType = "tool_provision.completed"
	EventToolProvisionFailed            EventType = "tool_provision.failed"
	EventWorkerTelemetryObserved        EventType = "worker.telemetry.observed"
	EventWorkerPressureChanged          EventType = "worker.pressure.changed"
	EventWorkerCleanupRequested         EventType = "worker.cleanup.requested"
	EventWorkerCleanupCompleted         EventType = "worker.cleanup.completed"
	EventWorkerCleanupFailed            EventType = "worker.cleanup.failed"
	EventSecurityPolicyBreached         EventType = "security.policy_breached"
)

// ResourceData carries projection-relevant resource identifiers in internal
// events. Fields are strings to keep this package decoupled from domain UUID
// types and to allow callers to include only the identifiers they know.
type ResourceData struct {
	ServiceID     string `json:"service_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	ArtifactID    string `json:"artifact_id,omitempty"`
	RouteID       string `json:"route_id,omitempty"`
	ReleaseID     string `json:"release_id,omitempty"`
	IntentID      string `json:"intent_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	Deleted       bool   `json:"deleted,omitempty"`
}

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

	handlerCtx := context.Background()
	var cancel context.CancelFunc
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			handlerCtx, cancel = context.WithDeadline(handlerCtx, deadline)
		}
	}

	var wg sync.WaitGroup
	for _, h := range handlers {
		h := h
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					p.logger.Error("event handler panic",
						zap.String("event_type", string(e.Type)),
						zap.Any("panic", r),
					)
				}
			}()
			h(handlerCtx, e)
		}()
	}

	if cancel != nil {
		go func() {
			wg.Wait()
			cancel()
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
