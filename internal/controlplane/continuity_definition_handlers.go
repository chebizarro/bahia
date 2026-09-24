package controlplane

import (
	"context"
	"fmt"
	"time"

	gonostr "fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
)

func (r *Reactor) handleContinuityProfileDefinition(ctx context.Context, event *gonostr.Event) {
	if !r.authorizeContinuityDefinition(event) {
		return
	}
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

func (h *ContinuityRuntime) handleFailoverPolicyDefinition(event *gonostr.Event) error {
	definition, err := nostradapter.DecodeFailoverPolicyEvent(event)
	if err != nil {
		return err
	}
	_, err = h.definitions.StoreRecipe(*definition)
	return err
}

func (r *Reactor) handleStandbyNodeDefinition(ctx context.Context, event *gonostr.Event) {
	if !r.authorizeContinuityDefinition(event) {
		return
	}
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
			ArtifactRef:  definition.ArtifactRef,
			Supports:     append([]string(nil), definition.Supports...),
			Profiles:     append([]domain.ContinuityMode(nil), definition.Profiles...),
		},
	})
}

func (h *ContinuityRuntime) handleReplicationPolicyDefinition(event *gonostr.Event) error {
	definition, err := nostradapter.DecodeReplicationPolicyEvent(event)
	if err != nil {
		return err
	}
	_, err = h.definitions.StoreReplicationPolicy(*definition)
	return err
}

func (h *ContinuityRuntime) handleRecoveryWorkflowDefinition(event *gonostr.Event) error {
	definition, err := nostradapter.DecodeRecoveryWorkflowEvent(event)
	if err != nil {
		return err
	}
	_, err = h.definitions.StoreRecipe(*definition)
	return err
}

func (r *Reactor) handleHeartbeatObservation(ctx context.Context, event *gonostr.Event) {
	if !r.authorizeContinuityDefinition(event) {
		return
	}
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

func (r *Reactor) authorizeContinuityDefinition(event *gonostr.Event) bool {
	if event == nil {
		r.logger.Warn("nil continuity definition event")
		return false
	}
	if !r.isAuthorized(event.PubKey.Hex()) {
		r.logger.Warn("unauthorized continuity definition event", "event_id", event.ID, "kind", event.Kind, "requester", event.PubKey)
		return false
	}
	return true
}

func continuitySource(event *gonostr.Event) events.NostrSource {
	return events.NostrSource{
		EventID:   event.ID.Hex(),
		Kind:      int(event.Kind),
		PubKey:    event.PubKey.Hex(),
		CreatedAt: event.CreatedAt.Time(),
	}
}

// handleDefinition shares the fail-closed operator gate with commands, but
// consumes signed canonical definitions rather than inventing RPC apply methods.
func (h *ContinuityRuntime) handleDefinition(ctx context.Context, event *gonostr.Event) error {
	if err := nostradapter.ValidateInboundEvent(event, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		return err
	}
	_, err := h.gate.wrap(func(_ context.Context, request ContextVMRequest) (any, error) {
		if h.definitions == nil {
			return nil, fmt.Errorf("continuity definition store is not configured")
		}
		switch request.Event.Kind {
		case nostradapter.KindContinuityProfile:
			profile, err := nostradapter.DecodeContinuityProfileEvent(request.Event)
			if err != nil {
				return nil, err
			}
			_, err = h.definitions.StoreProfile(*profile)
			return nil, err
		case nostradapter.KindFailoverPolicy:
			return nil, h.handleFailoverPolicyDefinition(request.Event)
		case nostradapter.KindReplicationPolicy:
			return nil, h.handleReplicationPolicyDefinition(request.Event)
		case nostradapter.KindRecoveryWorkflow:
			return nil, h.handleRecoveryWorkflowDefinition(request.Event)
		default:
			return nil, fmt.Errorf("unsupported continuity definition kind %d", request.Event.Kind)
		}
	})(ctx, ContextVMRequest{Event: event})
	return err
}
