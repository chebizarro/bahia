package controlplane

import (
	"context"

	gonostr "fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/events"
)

func (r *Reactor) handleFailoverRequest(ctx context.Context, event *gonostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.logger.Warn("unauthorized failover request", "event_id", event.ID, "requester", event.PubKey)
		return
	}
	request, err := nostradapter.DecodeFailoverRequestEvent(event)
	if err != nil {
		r.logger.Warn("invalid failover request event", "event_id", event.ID, "error", err)
		return
	}
	r.eventBus.Publish(ctx, events.Event{
		Type:     events.EventFailoverRequested,
		EntityID: request.ServiceKey,
		Data:     continuityCommandRequestedEvent(event, request),
	})
}

func (r *Reactor) handleRecoveryRequest(ctx context.Context, event *gonostr.Event) {
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.logger.Warn("unauthorized recovery request", "event_id", event.ID, "requester", event.PubKey)
		return
	}
	request, err := nostradapter.DecodeRecoveryRequestEvent(event)
	if err != nil {
		r.logger.Warn("invalid recovery request event", "event_id", event.ID, "error", err)
		return
	}
	r.eventBus.Publish(ctx, events.Event{
		Type:     events.EventRecoveryRequested,
		EntityID: request.ServiceKey,
		Data:     continuityCommandRequestedEvent(event, request),
	})
}

func continuityCommandRequestedEvent(event *gonostr.Event, request *nostradapter.ContinuityCommandRequest) events.ContinuityCommandRequested {
	return events.ContinuityCommandRequested{
		Source:             continuitySource(event),
		ServiceKey:         request.ServiceKey,
		TargetWorkerPubKey: request.TargetWorkerPubKey,
		TargetProfile:      request.TargetProfile,
		RecipeName:         request.RecipeName,
		IdempotencyKey:     request.IdempotencyKey,
		Reason:             request.Reason,
		Metadata:           request.Metadata,
	}
}
