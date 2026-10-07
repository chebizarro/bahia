package reconcile

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

// StateTombstoneHandler subscribes to EventEnvironmentServiceStateChanged on
// the in-process event bus and publishes a cp-state tombstone to relays when
// the event carries Deleted: true. This replaces the projector's handleEvent
// state tombstone case ( S1).
type StateTombstoneHandler struct {
	publisher RuntimeStatePublisher
	logger    *zap.Logger
}

// NewStateTombstoneHandler creates a handler that publishes state tombstones.
func NewStateTombstoneHandler(publisher RuntimeStatePublisher, logger *zap.Logger) *StateTombstoneHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &StateTombstoneHandler{publisher: publisher, logger: logger}
}

// SetupSubscriptions registers the tombstone handler on the event bus.
func (h *StateTombstoneHandler) SetupSubscriptions(bus events.Publisher) {
	bus.Subscribe(events.EventEnvironmentServiceStateChanged, func(ctx context.Context, e events.Event) {
		h.handleEvent(ctx, e)
	})
}

func (h *StateTombstoneHandler) handleEvent(ctx context.Context, e events.Event) {
	res, ok := e.Data.(events.ResourceData)
	if !ok {
		if ptr, ok := e.Data.(*events.ResourceData); ok && ptr != nil {
			res = *ptr
		} else {
			return
		}
	}
	if !res.Deleted {
		return
	}
	serviceID, err := uuid.Parse(res.ServiceID)
	if err != nil {
		return
	}
	envID, err := uuid.Parse(res.EnvironmentID)
	if err != nil {
		return
	}
	if pubErr := h.publisher.PublishStateTombstone(ctx, serviceID, envID); pubErr != nil {
		h.logger.Warn("publish state tombstone failed",
			zap.String("service_id", res.ServiceID),
			zap.String("environment_id", res.EnvironmentID),
			zap.Error(pubErr))
	}
}
