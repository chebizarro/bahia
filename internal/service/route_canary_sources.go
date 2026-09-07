package service

import (
	"context"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// DesiredStateRoutePlanSource enumerates managed routes from environment service
// state.
//
// Desired state is the correct source of truth for what should be probed. Live
// provider configuration is not: a route that vanished from Cloudflare must
// still be probed so its disappearance is detected, and a route withdrawn from
// desired state must stop being probed so it does not alarm forever.
type DesiredStateRoutePlanSource struct {
	states repository.EnvironmentServiceStateRepository
}

// NewDesiredStateRoutePlanSource builds a plan source over environment state.
func NewDesiredStateRoutePlanSource(states repository.EnvironmentServiceStateRepository) *DesiredStateRoutePlanSource {
	return &DesiredStateRoutePlanSource{states: states}
}

// ListManagedRoutePlans returns the route plan of every service whose desired
// runtime state carries one.
func (s *DesiredStateRoutePlanSource) ListManagedRoutePlans(ctx context.Context) ([]*domain.DesiredPublicRoutePlan, error) {
	if s == nil || s.states == nil {
		return nil, nil
	}
	states, err := s.states.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	plans := make([]*domain.DesiredPublicRoutePlan, 0, len(states))
	for i := range states {
		desired := states[i].DesiredRuntimeState
		if desired == nil || desired.PublicRoute == nil {
			continue
		}
		plans = append(plans, desired.PublicRoute)
	}
	return plans, nil
}

// ManagedInstanceRouteHealthSource reports the container-level status of the
// deployment unit behind a route.
//
// It exists purely to annotate route findings. A route verdict never depends on
// it, so a missing or stale health row degrades the annotation rather than
// changing whether an outage is declared.
type ManagedInstanceRouteHealthSource struct {
	health repository.ManagedInstanceHealthRepository
}

// NewManagedInstanceRouteHealthSource builds the contrast source.
func NewManagedInstanceRouteHealthSource(health repository.ManagedInstanceHealthRepository) *ManagedInstanceRouteHealthSource {
	return &ManagedInstanceRouteHealthSource{health: health}
}

// InstanceStatusForRoute returns the worst status observed for the route's
// deployment unit.
//
// The worst status is reported so the contrast is never flattering: if any
// instance behind the route is unhealthy, the operator should not be told the
// service was fine while its route broke.
func (s *ManagedInstanceRouteHealthSource) InstanceStatusForRoute(ctx context.Context, key domain.RouteCanaryKey) (domain.InstanceHealthStatus, bool) {
	if s == nil || s.health == nil {
		return "", false
	}
	rows, err := s.health.ListHealthByService(ctx, key.ServiceID)
	if err != nil {
		return "", false
	}
	var (
		worst     domain.InstanceHealthStatus
		worstRank = -1
	)
	for _, row := range rows {
		if row.EnvironmentID != key.EnvironmentID {
			continue
		}
		if key.DeploymentUnitID != nil && row.DeploymentUnitID != *key.DeploymentUnitID {
			continue
		}
		if rank := instanceStatusSeverityRank(row.Status); rank > worstRank {
			worst, worstRank = row.Status, rank
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
