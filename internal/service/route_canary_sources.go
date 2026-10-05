package service

import (
	"context"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// LocalRoutePlanSource enumerates managed routes from the service-state
// records in the local event store.
//
// Desired state is the correct source of truth for what should be probed. Live
// provider configuration is not: a route that vanished from Cloudflare must
// still be probed so its disappearance is detected, and a route withdrawn from
// desired state must stop being probed so it does not alarm forever. A
// replacement or tombstone received from a relay changes the next sweep's set.
type LocalRoutePlanSource struct{ State LocalSupervisionState }

// ListManagedRoutePlans returns the route plan of every service whose latest
// desired runtime state carries one.
func (s LocalRoutePlanSource) ListManagedRoutePlans(ctx context.Context) ([]*domain.DesiredPublicRoutePlan, error) {
	records, err := s.State.family(ctx, kinds.CPStateTopicServiceState)
	if err != nil {
		return nil, err
	}
	plans := make([]*domain.DesiredPublicRoutePlan, 0, len(records))
	for _, record := range records {
		state, ok := decodeServiceStateRecord(record)
		if !ok || state.DesiredRuntimeState == nil || state.DesiredRuntimeState.PublicRoute == nil {
			continue
		}
		plans = append(plans, state.DesiredRuntimeState.PublicRoute)
	}
	return plans, nil
}

// LocalRouteInstanceHealthSource reports the container-level status of the
// deployment unit behind a route from the canonical managed-instance health
// records in the local event store.
//
// It exists purely to annotate route findings. A route verdict never depends on
// it, so a missing or stale health record degrades the annotation rather than
// changing whether an outage is declared.
type LocalRouteInstanceHealthSource struct{ State LocalSupervisionState }

// InstanceStatusForRoute returns the worst status observed for the route's
// deployment unit.
//
// The worst status is reported so the contrast is never flattering: if any
// instance behind the route is unhealthy, the operator should not be told the
// service was fine while its route broke.
func (s LocalRouteInstanceHealthSource) InstanceStatusForRoute(ctx context.Context, key domain.RouteCanaryKey) (domain.InstanceHealthStatus, bool) {
	records, err := s.State.family(ctx, kinds.CPStateTopicManagedInstanceHealth)
	if err != nil {
		return "", false
	}
	var (
		worst     domain.InstanceHealthStatus
		worstRank = -1
	)
	for _, record := range records {
		health, ok := decodeManagedHealthRecord(record)
		if !ok || health.ServiceID != key.ServiceID || health.EnvironmentID != key.EnvironmentID {
			continue
		}
		if key.DeploymentUnitID != nil && health.DeploymentUnitID != *key.DeploymentUnitID {
			continue
		}
		if rank := instanceStatusSeverityRank(health.Status); rank > worstRank {
			worst, worstRank = health.Status, rank
		}
	}
	if worstRank < 0 {
		return "", false
	}
	return worst, true
}

// instanceStatusSeverityRank orders container-level statuses so the least
// healthy instance behind a route wins the contrast annotation.
func instanceStatusSeverityRank(status domain.InstanceHealthStatus) int {
	switch status {
	case domain.InstanceHealthStatusHealthy, domain.InstanceHealthStatusRunning:
		return 0
	case domain.InstanceHealthStatusManualOverride:
		return 1
	case domain.InstanceHealthStatusUnknown:
		return 2
	case domain.InstanceHealthStatusDegraded:
		return 3
	case domain.InstanceHealthStatusStopped:
		return 4
	case domain.InstanceHealthStatusUnhealthy:
		return 5
	case domain.InstanceHealthStatusRestartLoop:
		return 6
	case domain.InstanceHealthStatusOOMKilled:
		return 7
	default:
		return 2
	}
}
