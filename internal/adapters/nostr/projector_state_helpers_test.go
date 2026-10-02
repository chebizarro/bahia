package nostr

import (
	"context"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
)

// publishStateForTest is a test-only method that publishes a runtime state
// record through the projector's dedupe pipeline using the shared record
// builders from control_state_contract.go. It replaces the removed
// publishState method for test coverage of the dedupe/outbox infrastructure.
func (p *Projector) publishStateForTest(ctx context.Context, state *domain.EnvironmentServiceState, obs ...*domain.RuntimeObservation) error {
	var observation *domain.RuntimeObservation
	if len(obs) > 0 {
		observation = obs[0]
	}
	tags, content := RuntimeStateRecord(state, observation)
	return p.publishControlState(ctx, KindServiceState, serviceStateDTag(state.ServiceID, state.EnvironmentID), false, tags, content, "state.projection", &state.ServiceID)
}

// publishStateTombstoneForTest is a test-only method that publishes a state
// tombstone through the projector's dedupe pipeline.
func (p *Projector) publishStateTombstoneForTest(ctx context.Context, res events.ResourceData) error {
	serviceID, serviceOK := parseUUID(res.ServiceID)
	envID, envOK := parseUUID(res.EnvironmentID)
	if !serviceOK || !envOK {
		return nil
	}
	tags, content := RuntimeStateTombstoneRecord(serviceID, envID)
	return p.publishControlState(ctx, KindServiceState, serviceStateDTag(serviceID, envID), true, tags, content, "state.projection", &serviceID)
}
