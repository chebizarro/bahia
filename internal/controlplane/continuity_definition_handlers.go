package controlplane

import (
	"context"

	gonostr "github.com/nbd-wtf/go-nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
)

func (r *Reactor) handleContinuityProfileDefinition(ctx context.Context, event *gonostr.Event) {
	profile, err := nostradapter.DecodeContinuityProfileEvent(event)
	if err != nil {
		r.logger.Warn("invalid continuity profile event", "event_id", event.ID, "error", err)
		return
	}
	r.eventBus.Publish(ctx, events.Event{
		Type:     events.EventContinuityProfileObserved,
		EntityID: profile.ServiceKey,
		Data: events.ContinuityProfileObserved{
			Source:  continuitySource(event),
			Profile: *profile,
		},
	})
}

func (r *Reactor) handleFailoverPolicyDefinition(ctx context.Context, event *gonostr.Event) {
	recipe, err := nostradapter.DecodeFailoverPolicyEvent(event)
	if err != nil {
		r.logger.Warn("invalid failover policy event", "event_id", event.ID, "error", err)
		return
	}
	r.eventBus.Publish(ctx, events.Event{
		Type:     events.EventFailoverPolicyObserved,
		EntityID: recipe.ServiceKey,
		Data: events.ContinuityRecipeObserved{
			Source: continuitySource(event),
			Recipe: *recipe,
		},
	})
}

func (r *Reactor) handleStandbyNodeDefinition(ctx context.Context, event *gonostr.Event) {
	definition, err := nostradapter.DecodeStandbyNodeDefinitionEvent(event)
	if err != nil {
		r.logger.Warn("invalid standby node definition event", "event_id", event.ID, "error", err)
		return
	}
	r.eventBus.Publish(ctx, events.Event{
		Type:     events.EventStandbyNodeDefinitionObserved,
		EntityID: definition.ServiceKey,
		Data: events.StandbyNodeDefinitionObserved{
			Source:       continuitySource(event),
			WorkerPubKey: definition.WorkerPubKey,
			Host:         definition.Host,
			Role:         definition.Role,
			ServiceKey:   definition.ServiceKey,
			Tier:         definition.Tier,
			Supports:     append([]string(nil), definition.Supports...),
			Profiles:     append([]domain.ContinuityMode(nil), definition.Profiles...),
		},
	})
}

func (r *Reactor) handleReplicationPolicyDefinition(ctx context.Context, event *gonostr.Event) {
	policy, err := nostradapter.DecodeReplicationPolicyEvent(event)
	if err != nil {
		r.logger.Warn("invalid replication policy event", "event_id", event.ID, "error", err)
		return
	}
	r.eventBus.Publish(ctx, events.Event{
		Type:     events.EventReplicationPolicyObserved,
		EntityID: policy.ServiceKey,
		Data: events.ReplicationPolicyObserved{
			Source: continuitySource(event),
			Policy: *policy,
		},
	})
}

func (r *Reactor) handleRecoveryWorkflowDefinition(ctx context.Context, event *gonostr.Event) {
	recipe, err := nostradapter.DecodeRecoveryWorkflowEvent(event)
	if err != nil {
		r.logger.Warn("invalid recovery workflow event", "event_id", event.ID, "error", err)
		return
	}
	r.eventBus.Publish(ctx, events.Event{
		Type:     events.EventRecoveryWorkflowObserved,
		EntityID: recipe.ServiceKey,
		Data: events.ContinuityRecipeObserved{
			Source: continuitySource(event),
			Recipe: *recipe,
		},
	})
}

func (r *Reactor) handleHeartbeatObservation(ctx context.Context, event *gonostr.Event) {
	observation, err := nostradapter.DecodeHeartbeatObservationEvent(event)
	if err != nil {
		r.logger.Warn("invalid heartbeat observation event", "event_id", event.ID, "error", err)
		return
	}
	r.eventBus.Publish(ctx, events.Event{
		Type:     events.EventHeartbeatObserved,
		EntityID: observation.WorkerPubKey,
		Data: events.HeartbeatObserved{
			Source:      continuitySource(event),
			Observation: *observation,
		},
	})
}

func continuitySource(event *gonostr.Event) events.NostrSource {
	return events.NostrSource{
		EventID:   event.ID,
		Kind:      event.Kind,
		PubKey:    event.PubKey,
		CreatedAt: event.CreatedAt.Time(),
	}
}
