// Package reconcile implements the drift detection and reconciliation loop.
package reconcile

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Reconciler compares desired state with observed runtime state.
type Reconciler struct {
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	artifacts    repository.ArtifactRepository
	units        repository.DeploymentUnitRepository
	observations repository.RuntimeObservationRepository
	state        repository.EnvironmentServiceStateRepository
	resolver     runtime.RuntimeResolver
	publisher    events.Publisher
	interval     time.Duration
	logger       *zap.Logger
}

// NewReconciler creates a new Reconciler.
func NewReconciler(
	services repository.ServiceRepository,
	environments repository.EnvironmentRepository,
	artifacts repository.ArtifactRepository,
	units repository.DeploymentUnitRepository,
	observations repository.RuntimeObservationRepository,
	state repository.EnvironmentServiceStateRepository,
	resolver runtime.RuntimeResolver,
	publisher events.Publisher,
	interval time.Duration,
	logger *zap.Logger,
) *Reconciler {
	return &Reconciler{
		services:     services,
		environments: environments,
		artifacts:    artifacts,
		units:        units,
		observations: observations,
		state:        state,
		resolver:     resolver,
		publisher:    publisher,
		interval:     interval,
		logger:       logger,
	}
}

// Run starts the reconciliation loop. It blocks until the context is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	r.logger.Info("reconciler started", zap.Duration("interval", r.interval))
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Run immediately on start.
	r.reconcileAll(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reconciler stopped")
			return
		case <-ticker.C:
			r.reconcileAll(ctx)
		}
	}
}

func (r *Reconciler) reconcileAll(ctx context.Context) {
	dueBefore := time.Now().UTC().Add(-r.interval)
	states, err := r.state.ListDueForObservation(ctx, dueBefore)
	if err != nil {
		r.logger.Error("failed to list all states for reconciliation", zap.Error(err))
		return
	}

	for i := range states {
		if err := r.reconcileOne(ctx, &states[i]); err != nil {
			r.logger.Error("reconciliation failed",
				zap.String("service_id", states[i].ServiceID.String()),
				zap.String("environment_id", states[i].EnvironmentID.String()),
				zap.Error(err),
			)
		}
	}

	r.publisher.Publish(ctx, events.Event{
		Type: events.EventReconcileCompleted,
		Data: map[string]int{"checked": len(states)},
	})
}

func (r *Reconciler) reconcileMode(ctx context.Context, env *domain.Environment, deploymentUnitID *uuid.UUID) (domain.ReconcileMode, error) {
	if deploymentUnitID != nil && *deploymentUnitID != uuid.Nil && r.units != nil {
		unit, err := r.units.GetByID(ctx, *deploymentUnitID)
		if err != nil || unit == nil {
			return "", err
		}
		domain.NormalizeDeploymentUnitTargeting(unit)
		if err := domain.ValidateReconcileMode(unit.ReconcileMode); err != nil {
			return "", err
		}
		return unit.ReconcileMode, nil
	}
	domain.NormalizeEnvironmentTargeting(env)
	if err := domain.ValidateReconcileMode(env.Targeting.DefaultReconcileMode); err != nil {
		return "", err
	}
	return env.Targeting.DefaultReconcileMode, nil
}

func (r *Reconciler) reconcileOne(ctx context.Context, currentState *domain.EnvironmentServiceState) error {
	// Skip entries currently deploying.
	if currentState.DriftStatus == domain.DriftStatusDeploying {
		return nil
	}

	// Look up the service name for container label matching.
	svc, err := r.services.GetByID(ctx, currentState.ServiceID)
	if err != nil || svc == nil {
		return err
	}

	// Look up the target environment so runtime selection can be scoped per environment.
	env, err := r.environments.GetByID(ctx, currentState.EnvironmentID)
	if err != nil || env == nil {
		return err
	}

	mode, err := r.reconcileMode(ctx, env, currentState.DeploymentUnitID)
	if err != nil {
		return err
	}
	if mode == domain.ReconcileModeDisabled {
		return nil
	}

	rt, err := r.resolver.Resolve(svc, env)
	if err != nil {
		r.logger.Warn("failed to resolve runtime",
			zap.String("service", svc.Name),
			zap.String("environment", env.Name),
			zap.Error(err),
		)
		return nil
	}

	// Observe actual runtime state.
	obs, err := rt.Observe(ctx, currentState.ServiceID, currentState.EnvironmentID, svc.RuntimeTargetName())
	if err != nil {
		r.logger.Warn("failed to observe runtime",
			zap.String("service", svc.Name),
			zap.Error(err),
		)
		return nil // Non-fatal; we'll try again next cycle.
	}

	obs.DeploymentUnitID = currentState.DeploymentUnitID

	// Record the observation.
	if err := r.observations.Create(ctx, obs); err != nil {
		return err
	}

	// Determine drift status.
	newDrift := domain.DriftStatusUnknown
	if currentState.DesiredArtifactID != nil {
		desired, err := r.artifacts.GetByID(ctx, *currentState.DesiredArtifactID)
		if err == nil && desired != nil {
			if obs.ObservedImageDigest == desired.ImageDigest {
				newDrift = domain.DriftStatusInSync
			} else {
				newDrift = domain.DriftStatusDrifted
				r.logger.Warn("drift detected",
					zap.String("service", svc.Name),
					zap.String("desired_digest", desired.ImageDigest),
					zap.String("observed_digest", obs.ObservedImageDigest),
				)
				r.publisher.Publish(ctx, events.Event{
					Type:     events.EventDriftDetected,
					EntityID: currentState.ServiceID.String(),
					Data: map[string]string{
						"service_id":      currentState.ServiceID.String(),
						"environment_id":  currentState.EnvironmentID.String(),
						"service":         svc.Name,
						"environment":     env.Name,
						"desired_digest":  desired.ImageDigest,
						"observed_digest": obs.ObservedImageDigest,
					},
				})
			}
		}
	}

	// Update state.
	now := time.Now().UTC()
	currentState.CurrentObservationID = &obs.ID
	currentState.DriftStatus = newDrift
	currentState.LastReconciledAt = &now

	if err := r.state.Upsert(ctx, currentState); err != nil {
		return err
	}
	r.publisher.Publish(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: currentState.ServiceID.String() + ":" + currentState.EnvironmentID.String(),
		Data: events.ResourceData{
			ServiceID:     currentState.ServiceID.String(),
			EnvironmentID: currentState.EnvironmentID.String(),
		},
	})
	return nil
}
