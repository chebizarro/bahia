// Package reconcile implements the drift detection and reconciliation loop.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/driftdecision"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	runtimeService "github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// AutoRemediationDeployer applies a persisted desired state through the shared
// runtime lifecycle desired-state deploy helper.
type AutoRemediationDeployer interface {
	AutoRemediateDesiredState(ctx context.Context, serviceID, envID uuid.UUID, statusFn runtimeService.DeployStatusCallback) (*domain.RuntimeObservation, error)
}

type runtimeResolver interface {
	runtime.RuntimeResolver
	runtime.DeploymentUnitRuntimeResolver
}

// Reconciler compares desired state with observed runtime state.
type Reconciler struct {
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	artifacts    repository.ArtifactRepository
	units        repository.DeploymentUnitRepository
	observations repository.RuntimeObservationRepository
	state        repository.EnvironmentServiceStateRepository
	intents      repository.DeploymentIntentRepository
	runs         repository.DeploymentRunRepository
	resolver     runtimeResolver
	publisher    events.Publisher
	interval     time.Duration
	logger       *zap.Logger
	deployer     AutoRemediationDeployer
	// startingTimeout bounds how long a unit may report "starting" before
	// reconcile stops treating it as progress. Zero selects the default.
	startingTimeout time.Duration
}

// WithStartingTimeout bounds how long a deploying unit may report "starting"
// before reconcile treats it as failing to come up.
func WithStartingTimeout(d time.Duration) Option {
	return func(r *Reconciler) {
		if d > 0 {
			r.startingTimeout = d
		}
	}
}

// Option configures reconciliation behavior.
type Option func(*Reconciler)

// WithAutoRemediationDeployer enables policy-driven auto_apply reconciliation
// through the shared runtime lifecycle desired-state deploy helper.
func WithAutoRemediationDeployer(deployer AutoRemediationDeployer) Option {
	return func(r *Reconciler) {
		r.deployer = deployer
	}
}

// WithDeploymentHistory enables repair of route-only runs completed before the
// completion path began transitioning their service state to in_sync.
func WithDeploymentHistory(intents repository.DeploymentIntentRepository, runs repository.DeploymentRunRepository) Option {
	return func(r *Reconciler) {
		r.intents = intents
		r.runs = runs
	}
}

// NewReconciler creates a new Reconciler.
func NewReconciler(
	services repository.ServiceRepository,
	environments repository.EnvironmentRepository,
	artifacts repository.ArtifactRepository,
	units repository.DeploymentUnitRepository,
	observations repository.RuntimeObservationRepository,
	state repository.EnvironmentServiceStateRepository,
	resolver runtimeResolver,
	publisher events.Publisher,
	interval time.Duration,
	logger *zap.Logger,
	opts ...Option,
) *Reconciler {
	reconciler := &Reconciler{
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
	for _, opt := range opts {
		opt(reconciler)
	}
	return reconciler
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

func (r *Reconciler) loadDeploymentUnit(ctx context.Context, deploymentUnitID *uuid.UUID) (*domain.DeploymentUnit, error) {
	if deploymentUnitID == nil || *deploymentUnitID == uuid.Nil {
		return nil, nil
	}
	if r.units == nil {
		r.logger.Warn("deployment unit repository unavailable; falling back to legacy runtime resolution",
			zap.String("deployment_unit_id", deploymentUnitID.String()),
		)
		return nil, nil
	}
	unit, err := r.units.GetByID(ctx, *deploymentUnitID)
	if err != nil {
		return nil, err
	}
	if unit == nil {
		r.logger.Warn("deployment unit not found; falling back to legacy runtime resolution",
			zap.String("deployment_unit_id", deploymentUnitID.String()),
		)
		return nil, nil
	}
	return unit, nil
}

func (r *Reconciler) reconcileMode(env *domain.Environment, unit *domain.DeploymentUnit) (domain.ReconcileMode, error) {
	if unit != nil {
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
	// Deploying entries remain excluded from runtime observation. The sole
	// exception repairs route-only runs completed before their success path
	// explicitly transitioned state to in_sync.
	if currentState.DriftStatus == domain.DriftStatusDeploying {
		if err := r.repairStuckRouteOnlyState(ctx, currentState); err != nil {
			return err
		}
		return nil
	}
	if currentState.ReconcileBackoffUntil != nil && currentState.ReconcileBackoffUntil.After(time.Now().UTC()) {
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

	unit, err := r.loadDeploymentUnit(ctx, currentState.DeploymentUnitID)
	if err != nil {
		return err
	}
	mode, err := r.reconcileMode(env, unit)
	if err != nil {
		return err
	}
	if mode == domain.ReconcileModeDisabled {
		return nil
	}

	var rt runtime.Runtime
	if unit != nil {
		rt, err = r.resolver.ResolveDeploymentUnit(svc, env, unit)
	} else {
		rt, err = r.resolver.Resolve(svc, env)
	}
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
	previousDrift := currentState.DriftStatus
	newDrift := domain.DriftStatusUnknown
	desiredHash := currentState.DesiredHash
	observedHash := obs.NormalizedHash
	if obs.NormalizedState != nil && obs.NormalizedState.ObservationHash != "" {
		observedHash = obs.NormalizedState.ObservationHash
	}
	desiredDigest := ""
	observedDigest := domain.NormalizeImageDigest(obs.ObservedImageDigest)
	decisionBranch := "early-out"
	if currentState.DesiredHash != "" || currentState.DesiredRuntimeState != nil {
		if currentState.DesiredHash != "" && observedHash != "" {
			decisionBranch = "hash-compare"
			hashesMatch := currentState.DesiredHash == observedHash
			if currentState.DesiredRuntimeState != nil {
				hashesMatch = hashesMatch || currentState.DesiredRuntimeState.MatchesRuntimeConvergenceHash(observedHash)
			}
			if hashesMatch {
				// A matching desired hash proves the intended CONFIG is applied.
				// It says nothing about whether the container actually came up,
				// so convergence additionally requires acceptable runtime health.
				// Without this, a crash-looping container reports in_sync.
				observedAt := time.Now().UTC()
				switch obs.HealthStatus {
				case domain.HealthStatusHealthy:
					newDrift = domain.DriftStatusInSync
				case domain.HealthStatusStarting:
					markStarting(currentState, observedAt)
					if r.startingTimeoutExceeded(currentState, observedAt) {
						newDrift = r.driftStatusForMode(mode)
						r.recordStartingTimeout(currentState, obs, observedAt)
						r.publishDriftDetected(ctx, currentState, svc, env, map[string]string{
							"desired_hash":  currentState.DesiredHash,
							"observed_hash": observedHash,
							"health_status": string(obs.HealthStatus),
						})
					} else {
						// Still within the startup budget: pending, not failed.
						//
						// Deliberately NOT DriftStatusDeploying. reconcileOne
						// early-returns for units already marked deploying, so
						// parking a starting container there would be a terminal
						// trap: health would never be re-evaluated, the startup
						// budget could never elapse, and a container that later
						// became healthy would never converge. "unknown" keeps
						// the unit in the reconcile loop while refusing to claim
						// success.
						newDrift = domain.DriftStatusUnknown
					}
				default:
					newDrift = r.driftStatusForMode(mode)
					recordUnhealthyEvidence(currentState, obs, fmt.Sprintf(
						"desired configuration is applied but the runtime is %s; the deployment is not healthy",
						obs.HealthStatus))
					r.publishDriftDetected(ctx, currentState, svc, env, map[string]string{
						"desired_hash":  currentState.DesiredHash,
						"observed_hash": observedHash,
						"health_status": string(obs.HealthStatus),
					})
				}
			} else {
				newDrift = r.driftStatusForMode(mode)
				r.publishDriftDetected(ctx, currentState, svc, env, map[string]string{
					"desired_hash":  currentState.DesiredHash,
					"observed_hash": observedHash,
				})
			}
		} else if observedHash == "" && currentState.DesiredArtifactID != nil {
			decisionBranch = "digest-fallback"
			desiredDigest = driftdecision.DesiredArtifactDigest(ctx, r.artifacts, currentState.DesiredArtifactID, r.logger)
			newDrift = r.digestFallbackStatus(desiredDigest, observedDigest, obs.HealthStatus, mode)
			if newDrift == domain.DriftStatusDrifted || newDrift == domain.DriftStatusRemediationNeeded {
				r.publishDriftDetected(ctx, currentState, svc, env, map[string]string{
					"desired_digest":  desiredDigest,
					"observed_digest": observedDigest,
				})
			}
		}
	} else if currentState.DesiredArtifactID != nil {
		decisionBranch = "digest-fallback"
		desiredDigest = driftdecision.DesiredArtifactDigest(ctx, r.artifacts, currentState.DesiredArtifactID, r.logger)
		newDrift = r.digestFallbackStatus(desiredDigest, observedDigest, obs.HealthStatus, mode)
		if newDrift == domain.DriftStatusDrifted || newDrift == domain.DriftStatusRemediationNeeded {
			r.publishDriftDetected(ctx, currentState, svc, env, map[string]string{
				"desired_digest":  desiredDigest,
				"observed_digest": observedDigest,
			})
		}
	}

	// Update state.
	now := time.Now().UTC()
	currentState.CurrentObservationID = &obs.ID
	currentState.DriftStatus = newDrift
	currentState.LastReconciledAt = &now
	if newDrift == domain.DriftStatusInSync {
		currentState.ReconcileFailureMetadata = nil
		currentState.ReconcileBackoffUntil = nil
		currentState.ReconcileConsecutiveFailures = 0
	}

	if err := r.state.Upsert(ctx, currentState); err != nil {
		return err
	}
	if newDrift != domain.DriftStatusInSync && newDrift != previousDrift {
		if desiredDigest == "" && currentState.DesiredArtifactID != nil {
			desiredDigest = driftdecision.DesiredArtifactDigest(ctx, r.artifacts, currentState.DesiredArtifactID, r.logger)
		}
		driftdecision.Log(r.logger, driftdecision.LogInput{
			Service: svc.Name, Environment: env.Name,
			ServiceID: currentState.ServiceID, EnvironmentID: currentState.EnvironmentID,
			Status: currentState.DriftStatus, PreviousStatus: previousDrift, Branch: decisionBranch,
			DesiredHash: desiredHash, ObservedHash: observedHash,
			DesiredDigest: desiredDigest, ObservedDigest: observedDigest,
			Health: obs.HealthStatus, ObservationID: obs.ID, Source: obs.Source,
		})
	}
	r.publisher.Publish(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: currentState.ServiceID.String() + ":" + currentState.EnvironmentID.String(),
		Data: events.ResourceData{
			ServiceID:     currentState.ServiceID.String(),
			EnvironmentID: currentState.EnvironmentID.String(),
		},
	})
	if newDrift == domain.DriftStatusDrifted && mode == domain.ReconcileModeAutoApply {
		return r.autoApplyDesiredState(ctx, currentState)
	}
	return nil
}

func (r *Reconciler) repairStuckRouteOnlyState(ctx context.Context, currentState *domain.EnvironmentServiceState) error {
	if r.intents == nil || r.runs == nil || currentState.DesiredIntentID == nil {
		return nil
	}
	intent, err := r.intents.GetByID(ctx, *currentState.DesiredIntentID)
	if err != nil {
		return err
	}
	if intent == nil || intent.Status != domain.IntentStatusDeployed {
		return nil
	}
	runs, err := r.runs.ListByIntent(ctx, intent.ID)
	if err != nil {
		return err
	}
	latest := latestDeploymentRun(runs)
	if latest == nil || latest.Status != domain.RunStatusSucceeded || !domain.IsRouteOnlyDeploymentRun(latest) {
		return nil
	}

	// Reload immediately before the write so only DriftStatus is merged into the
	// latest observation, run-linkage, and reconcile-health fields.
	state, err := r.state.Get(ctx, currentState.ServiceID, currentState.EnvironmentID)
	if err != nil {
		return err
	}
	if state == nil || state.DriftStatus != domain.DriftStatusDeploying || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID {
		return nil
	}
	state.DriftStatus = domain.DriftStatusInSync
	if err := r.state.Upsert(ctx, state); err != nil {
		return err
	}
	r.publisher.Publish(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: state.ServiceID.String() + ":" + state.EnvironmentID.String(),
		Data: events.ResourceData{
			ServiceID:     state.ServiceID.String(),
			EnvironmentID: state.EnvironmentID.String(),
			IntentID:      intent.ID.String(),
			RunID:         latest.ID.String(),
		},
	})
	r.logger.Info("repaired route-only service state stuck in deploying",
		zap.String("service_id", state.ServiceID.String()),
		zap.String("environment_id", state.EnvironmentID.String()),
		zap.String("intent_id", intent.ID.String()),
		zap.String("run_id", latest.ID.String()),
	)
	return nil
}

func latestDeploymentRun(runs []domain.DeploymentRun) *domain.DeploymentRun {
	if len(runs) == 0 {
		return nil
	}
	latest := &runs[0]
	for i := 1; i < len(runs); i++ {
		candidate := &runs[i]
		if candidate.CreatedAt.After(latest.CreatedAt) ||
			(candidate.CreatedAt.Equal(latest.CreatedAt) && candidate.UpdatedAt.After(latest.UpdatedAt)) ||
			(candidate.CreatedAt.Equal(latest.CreatedAt) && candidate.UpdatedAt.Equal(latest.UpdatedAt) && candidate.ID.String() > latest.ID.String()) {
			latest = candidate
		}
	}
	return latest
}

func (r *Reconciler) publishDriftDetected(ctx context.Context, currentState *domain.EnvironmentServiceState, svc *domain.Service, env *domain.Environment, extra map[string]string) {
	data := map[string]string{
		"service_id":     currentState.ServiceID.String(),
		"environment_id": currentState.EnvironmentID.String(),
		"service":        svc.Name,
		"environment":    env.Name,
	}
	for k, v := range extra {
		data[k] = v
	}
	r.publisher.Publish(ctx, events.Event{
		Type:     events.EventDriftDetected,
		EntityID: currentState.ServiceID.String(),
		Data:     data,
	})
}

const (
	// startingSinceKey marks when the current non-converged episode first saw a
	// "starting" unit. It lives in ReconcileFailureMetadata because that map is
	// the reconciler's durable non-converged diagnostics channel and is cleared
	// automatically once the unit reaches in_sync, so the marker cannot leak
	// across episodes.
	startingSinceKey = "starting_since"
	// unhealthyEvidenceKey carries the operator-facing reason a unit is not
	// converging.
	unhealthyEvidenceKey = "unhealthy_evidence"

	// defaultStartingTimeout bounds "starting" before it is treated as failure.
	defaultStartingTimeout = 10 * time.Minute
)

func (r *Reconciler) effectiveStartingTimeout() time.Duration {
	if r.startingTimeout > 0 {
		return r.startingTimeout
	}
	return defaultStartingTimeout
}

// markStarting records the first observation of a starting unit in this episode.
// It never overwrites an existing marker, because the elapsed time must be
// measured from when the unit began starting, not from the latest reconcile.
func markStarting(state *domain.EnvironmentServiceState, now time.Time) {
	if state.ReconcileFailureMetadata == nil {
		state.ReconcileFailureMetadata = map[string]any{}
	}
	if _, ok := state.ReconcileFailureMetadata[startingSinceKey]; ok {
		return
	}
	state.ReconcileFailureMetadata[startingSinceKey] = now.UTC().Format(time.RFC3339)
}

// startingTimeoutExceeded reports whether a unit has been starting for longer
// than the configured bound.
//
// It fails SAFE: with no usable marker the unit is treated as still starting, so
// a missing or unparseable timestamp can never manufacture a spurious failure.
func (r *Reconciler) startingTimeoutExceeded(state *domain.EnvironmentServiceState, now time.Time) bool {
	raw, ok := state.ReconcileFailureMetadata[startingSinceKey]
	if !ok {
		return false
	}
	text, ok := raw.(string)
	if !ok {
		return false
	}
	since, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return false
	}
	return now.UTC().Sub(since.UTC()) > r.effectiveStartingTimeout()
}

// recordStartingTimeout surfaces a unit that never became healthy.
func (r *Reconciler) recordStartingTimeout(state *domain.EnvironmentServiceState, obs *domain.RuntimeObservation, now time.Time) {
	evidence := fmt.Sprintf("container did not become healthy within %s; last observed health %s",
		r.effectiveStartingTimeout(), obs.HealthStatus)
	recordUnhealthyEvidence(state, obs, evidence)
	r.logger.Warn("deploying unit exceeded startup health timeout",
		zap.String("service_id", state.ServiceID.String()),
		zap.String("environment_id", state.EnvironmentID.String()),
		zap.String("health_status", string(obs.HealthStatus)),
		zap.Duration("startup_timeout", r.effectiveStartingTimeout()),
		zap.String("evidence", evidence))
}

// recordUnhealthyEvidence stores why a unit is not converging, including the raw
// runtime verdict, so operators can read the cause from normal state output
// instead of inspecting the database or the container host.
func recordUnhealthyEvidence(state *domain.EnvironmentServiceState, obs *domain.RuntimeObservation, reason string) {
	if state.ReconcileFailureMetadata == nil {
		state.ReconcileFailureMetadata = map[string]any{}
	}
	state.ReconcileFailureMetadata[unhealthyEvidenceKey] = reason
	state.ReconcileFailureMetadata["observed_health"] = string(obs.HealthStatus)
	for _, key := range []string{"docker_state", "docker_status"} {
		if value, ok := obs.Metadata[key]; ok {
			state.ReconcileFailureMetadata[key] = value
		}
	}
}

func (r *Reconciler) driftStatusForMode(mode domain.ReconcileMode) domain.DriftStatus {
	if mode == domain.ReconcileModeApprovalRequired {
		return domain.DriftStatusRemediationNeeded
	}
	return domain.DriftStatusDrifted
}

func (r *Reconciler) digestFallbackStatus(desiredDigest, observedDigest string, health domain.HealthStatus, mode domain.ReconcileMode) domain.DriftStatus {
	// "starting" keeps its existing observe-only verdict here on purpose. This
	// weaker digest-only path has no desired-state hash, and flapping it would
	// change convergence for every legacy observe-only environment. The incident
	// this gate addresses is caught upstream: a crash-looping container now
	// observes as unhealthy, which this function already reports as drifted.
	status := domain.ArtifactDigestDriftStatus(desiredDigest, observedDigest, health, domain.DriftStatusInSync)
	if status == domain.DriftStatusDrifted {
		return r.driftStatusForMode(mode)
	}
	return status
}

func (r *Reconciler) autoApplyDesiredState(ctx context.Context, currentState *domain.EnvironmentServiceState) error {
	if r.deployer == nil {
		return r.recordReconcileFailure(ctx, currentState, "auto_apply_unavailable", "runtime lifecycle desired-state deploy helper is unavailable")
	}
	_, err := r.deployer.AutoRemediateDesiredState(ctx, currentState.ServiceID, currentState.EnvironmentID, nil)
	if err == nil {
		return r.clearReconcileFailure(ctx, currentState.ServiceID, currentState.EnvironmentID)
	}
	reason := "auto_apply_failed"
	if errors.Is(err, runtimeService.ErrEnvironmentApplyLockContended) {
		reason = "environment_apply_lock_contended"
	}
	return r.recordReconcileFailure(ctx, currentState, reason, err.Error())
}

func (r *Reconciler) recordReconcileFailure(ctx context.Context, currentState *domain.EnvironmentServiceState, reason, message string) error {
	failureCount := currentState.ReconcileConsecutiveFailures + 1
	backoff := r.reconcileBackoff(failureCount)
	now := time.Now().UTC()
	backoffUntil := now.Add(backoff)
	currentState.ReconcileConsecutiveFailures = failureCount
	currentState.ReconcileBackoffUntil = &backoffUntil
	currentState.ReconcileFailureMetadata = map[string]any{
		"reason":        reason,
		"message":       message,
		"failed_at":     now.Format(time.RFC3339Nano),
		"backoff":       backoff.String(),
		"failure_count": failureCount,
	}
	return r.state.Upsert(ctx, currentState)
}

func (r *Reconciler) clearReconcileFailure(ctx context.Context, serviceID, envID uuid.UUID) error {
	state, err := r.state.Get(ctx, serviceID, envID)
	if err != nil || state == nil {
		return err
	}
	state.ReconcileFailureMetadata = nil
	state.ReconcileBackoffUntil = nil
	state.ReconcileConsecutiveFailures = 0
	return r.state.Upsert(ctx, state)
}

func (r *Reconciler) reconcileBackoff(failureCount int) time.Duration {
	base := r.interval
	if base <= 0 {
		base = time.Minute
	}
	if failureCount < 1 {
		failureCount = 1
	}
	if failureCount > 6 {
		failureCount = 6
	}
	backoff := base * time.Duration(1<<uint(failureCount-1))
	max := 30 * time.Minute
	if backoff > max {
		return max
	}
	return backoff
}
