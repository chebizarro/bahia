package soulfactory

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
)

// StatusSyncHandler subscribes to bahia events and syncs deployment status to soul events.
type StatusSyncHandler struct {
	reactor          *Reactor
	bahiaIntegration *BahiaIntegration
	logger           *slog.Logger

	mu    sync.RWMutex
	souls map[uuid.UUID]*domain.AgentSoul // bahia service ID -> soul
}

// NewStatusSyncHandler creates a new status sync handler.
func NewStatusSyncHandler(reactor *Reactor, bahiaIntegration *BahiaIntegration, logger *slog.Logger) *StatusSyncHandler {
	return &StatusSyncHandler{
		reactor:          reactor,
		bahiaIntegration: bahiaIntegration,
		logger:           logger,
		souls:            make(map[uuid.UUID]*domain.AgentSoul),
	}
}

// RegisterSoul registers a soul for status sync tracking.
func (h *StatusSyncHandler) RegisterSoul(soul *domain.AgentSoul) {
	if soul.BahiaServiceID == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.souls[*soul.BahiaServiceID] = soul
}

// UnregisterSoul removes a soul from status sync tracking.
func (h *StatusSyncHandler) UnregisterSoul(serviceID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.souls, serviceID)
}

// HandleEvent processes bahia events and syncs status.
// This implements the events.Subscriber interface.
func (h *StatusSyncHandler) HandleEvent(ctx context.Context, event events.Event) {
	logger := h.logger.With("event_type", event.Type, "entity_id", event.EntityID)

	// Parse service ID from entity or data
	serviceID, err := h.extractServiceID(event)
	if err != nil {
		logger.Debug("could not extract service ID", "error", err)
		return
	}

	// Look up the soul for this service
	h.mu.RLock()
	soul, exists := h.souls[serviceID]
	h.mu.RUnlock()

	if !exists {
		return // Not a soul-factory managed service
	}

	logger = logger.With("agent_id", soul.AgentID, "service_id", serviceID)

	switch event.Type {
	case events.EventDeploymentRunCompleted:
		h.handleDeploymentComplete(ctx, soul, event)
	case events.EventDeploymentIntentCreated:
		h.handleDeploymentIntentCreated(ctx, soul, event)
	default:
		// Ignore other events
		return
	}
}

// handleDeploymentComplete updates soul status when a deployment completes.
func (h *StatusSyncHandler) handleDeploymentComplete(ctx context.Context, soul *domain.AgentSoul, event events.Event) {
	logger := h.logger.With("agent_id", soul.AgentID)

	// Extract status from event data
	data, ok := event.Data.(map[string]string)
	if !ok {
		logger.Warn("invalid event data format")
		return
	}

	status := data["status"]
	logger.Info("deployment run completed", "status", status)

	// Map bahia status to deploy status
	var deployStatus string
	switch status {
	case string(domain.RunStatusSucceeded):
		deployStatus = "deployed"
	case string(domain.RunStatusFailed):
		deployStatus = "failed"
	case string(domain.RunStatusCancelled):
		deployStatus = "cancelled"
	default:
		deployStatus = status
	}

	// Update soul and republish
	if err := h.updateSoulDeployStatus(ctx, soul, deployStatus); err != nil {
		logger.Error("failed to update soul deploy status", "error", err)
	}
}

// handleDeploymentIntentCreated updates soul status when a new deployment starts.
func (h *StatusSyncHandler) handleDeploymentIntentCreated(ctx context.Context, soul *domain.AgentSoul, event events.Event) {
	logger := h.logger.With("agent_id", soul.AgentID)
	logger.Info("deployment intent created, updating status to deploying")

	if err := h.updateSoulDeployStatus(ctx, soul, "deploying"); err != nil {
		logger.Error("failed to update soul deploy status", "error", err)
	}
}

// updateSoulDeployStatus updates the soul's deploy status and republishes the event.
func (h *StatusSyncHandler) updateSoulDeployStatus(ctx context.Context, soul *domain.AgentSoul, deployStatus string) error {
	// Update local copy
	h.mu.Lock()
	soul.DeployStatus = deployStatus
	h.mu.Unlock()

	if err := h.reactor.PublishSoul(ctx, soul); err != nil {
		return fmt.Errorf("publish soul update: %w", err)
	}

	h.logger.Info("published soul status update",
		"agent_id", soul.AgentID,
		"deploy_status", deployStatus,
	)

	return nil
}

// extractServiceID extracts the service UUID from an event.
func (h *StatusSyncHandler) extractServiceID(event events.Event) (uuid.UUID, error) {
	// Try to get from entity ID first
	if id, err := uuid.Parse(event.EntityID); err == nil {
		return id, nil
	}

	// Try to extract from data
	switch data := event.Data.(type) {
	case *domain.DeploymentIntent:
		return data.ServiceID, nil
	case *domain.DeploymentRun:
		// Need to look up the intent to get service ID
		return uuid.Nil, fmt.Errorf("deployment run requires intent lookup")
	case map[string]interface{}:
		if sid, ok := data["service_id"].(string); ok {
			return uuid.Parse(sid)
		}
	}

	return uuid.Nil, fmt.Errorf("could not extract service ID from event")
}
