package telemetry

import (
	"context"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

const (
	fleetHealthWorkerLimit = 1000
	driftStuckAfter        = 15 * time.Minute
	telemetryStaleAfter    = 10 * time.Minute
)

type fleetHealthWorkerSource interface {
	List(context.Context, string, int) ([]domain.Worker, error)
}

type fleetHealthStateSource interface {
	ListAll(context.Context) ([]domain.EnvironmentServiceState, error)
}

// FleetHealthSnapshot is a bounded, label-safe projection of Bahia read models.
type FleetHealthSnapshot struct {
	WorkerCapacity      map[string]int
	TelemetryFreshness  map[string]int
	HeartbeatLagSeconds map[string]float64
	DriftStates         map[string]int
	ServiceHealth       map[string]int
	PressureActions     map[string]int
	MaxDriftAgeSeconds  float64
	StuckDriftStates    int
}

// SetFleetHealthSources connects /metrics to authoritative read models.
func (p *Provider) SetFleetHealthSources(workers fleetHealthWorkerSource, states fleetHealthStateSource) {
	p.fleetHealthWorkers = workers
	p.fleetHealthStates = states
}

func (p *Provider) fleetHealthSnapshot(ctx context.Context, now time.Time) FleetHealthSnapshot {
	s := FleetHealthSnapshot{
		WorkerCapacity: map[string]int{}, TelemetryFreshness: map[string]int{},
		HeartbeatLagSeconds: map[string]float64{}, DriftStates: map[string]int{},
		ServiceHealth: map[string]int{}, PressureActions: map[string]int{},
	}
	if p.fleetHealthWorkers != nil {
		workers, err := p.fleetHealthWorkers.List(ctx, "", fleetHealthWorkerLimit)
		if err != nil {
			p.logger.Warn("collect fleet-health worker metrics", zap.Error(err))
		} else {
			for _, worker := range workers {
				capacity := string(domain.WorkerCapacityReduced)
				action := string(domain.WorkerPressureActionOperatorIntervention)
				if worker.Pressure != nil {
					capacity = string(worker.Pressure.CapacityClass)
					action = string(worker.Pressure.RecommendedAction)
				}
				s.WorkerCapacity[capacity]++
				s.PressureActions[action]++
				freshness := "absent"
				if worker.Telemetry != nil {
					freshness = "fresh"
					if worker.Telemetry.SampledAt.IsZero() || now.Sub(worker.Telemetry.SampledAt) > telemetryStaleAfter {
						freshness = "stale"
					}
				}
				s.TelemetryFreshness[freshness]++
				if worker.LastHeartbeatAt != nil {
					lag := now.Sub(worker.LastHeartbeatAt.UTC()).Seconds()
					if lag < 0 {
						lag = 0
					}
					s.HeartbeatLagSeconds[worker.PubKey] = lag
				}
			}
		}
	}
	if p.fleetHealthStates != nil {
		states, err := p.fleetHealthStates.ListAll(ctx)
		if err != nil {
			p.logger.Warn("collect fleet-health service metrics", zap.Error(err))
		} else {
			for _, state := range states {
				s.DriftStates[string(state.DriftStatus)]++
				health := "healthy"
				if state.DriftStatus == domain.DriftStatusDrifted {
					health = "degraded"
					age := now.Sub(state.UpdatedAt.UTC()).Seconds()
					if age < 0 {
						age = 0
					}
					if age > s.MaxDriftAgeSeconds {
						s.MaxDriftAgeSeconds = age
					}
					if age >= driftStuckAfter.Seconds() || state.ReconcileConsecutiveFailures > 0 {
						s.StuckDriftStates++
					}
				} else if state.DriftStatus != domain.DriftStatusInSync {
					health = "unknown"
				}
				s.ServiceHealth[health]++
			}
		}
	}
	return s
}
