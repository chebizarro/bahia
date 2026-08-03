package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	runtimeActionDeployEvent  = events.EventRuntimeDeploy
	runtimeActionRestartEvent = events.EventRuntimeRestart
	runtimeActionStopEvent    = events.EventRuntimeStop

	directRuntimeGuardrailMessage = "direct runtime actions are only allowed for adopted direct_runtime workloads"

	defaultRuntimeHealthTimeout  = 2 * time.Minute
	defaultRuntimeHealthInterval = 2 * time.Second
)

// DeployStep represents a step in the desired-state deploy lifecycle.
type DeployStep string

const (
	DeployStepBuildingDesiredState DeployStep = "building_desired_state"
	DeployStepLockingEnvironment   DeployStep = "locking_environment"
	DeployStepRendering            DeployStep = "rendering"
	DeployStepApplying             DeployStep = "applying"
	DeployStepObserving            DeployStep = "observing"
	DeployStepProjecting           DeployStep = "projecting"
)

// DeployStatusCallback is called during deploy to report step progression.
// Implementations should be non-blocking and best-effort.
type DeployStatusCallback func(ctx context.Context, step DeployStep, message string)

// DeploymentHealthError is safe to project to operators. Cause details remain
// server-side and are never included in Nostr run metadata.
type DeploymentHealthError struct {
	Code    string
	Message string
}

func (e *DeploymentHealthError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// DeploymentTargetSummary contains only non-secret deployment-unit identity.
type DeploymentTargetSummary struct {
	UnitID      *uuid.UUID         `json:"unit_id,omitempty"`
	UnitKey     string             `json:"unit_key"`
	RuntimeType domain.RuntimeType `json:"runtime_type"`
	EndpointRef string             `json:"endpoint_ref"`
}

// EnvironmentApplyLocker serializes deploy operations per environment.
type EnvironmentApplyLocker interface {
	Lock(ctx context.Context, environmentID uuid.UUID) (unlock func(), err error)
}

// EnvironmentApplyTryLocker exposes non-blocking acquisition for scheduled
// auto-remediation so user-initiated deploys keep priority on contention.
type EnvironmentApplyTryLocker interface {
	EnvironmentApplyLocker
	TryLock(ctx context.Context, environmentID uuid.UUID) (unlock func(), acquired bool, err error)
}

// ErrEnvironmentApplyLockContended means an internal auto-remediation pass found
// an active user/runtime operation holding the environment apply lock.
var ErrEnvironmentApplyLockContended = fmt.Errorf("environment apply lock contended")

// RuntimeLifecycleService performs direct runtime actions for services resolved to a runtime.
type RuntimeLifecycleService struct {
	registry     *RegistryService
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	artifacts    repository.ArtifactRepository
	state        repository.EnvironmentServiceStateRepository
	units        repository.DeploymentUnitRepository
	resolver     runtime.RuntimeResolver
	secrets      repository.SecretRepository
	publisher    events.Publisher
	logger       *zap.Logger
	applyLock    EnvironmentApplyLocker

	secretEncryptor *secretsAdapter.Encryptor
	healthTimeout   time.Duration
	healthInterval  time.Duration
}

// RuntimeLifecycleOption configures runtime lifecycle behavior.
type RuntimeLifecycleOption func(*RuntimeLifecycleService)

// WithRuntimeLifecycleSecrets merges effective Bahia secrets into direct deploy environment options.
func WithRuntimeLifecycleSecrets(repo repository.SecretRepository, encryptor *secretsAdapter.Encryptor) RuntimeLifecycleOption {
	return func(s *RuntimeLifecycleService) {
		s.secrets = repo
		s.secretEncryptor = encryptor
	}
}

// WithRuntimeApplyLock injects an environment-scoped advisory lock for serializing deploys.
func WithRuntimeApplyLock(lock EnvironmentApplyLocker) RuntimeLifecycleOption {
	return func(s *RuntimeLifecycleService) {
		s.applyLock = lock
	}
}

// WithRuntimeLifecycleDeploymentUnits enables desired environment plans to
// resolve explicit deployment-unit identity during deploy and reconciliation.
func WithRuntimeLifecycleDeploymentUnits(repo repository.DeploymentUnitRepository) RuntimeLifecycleOption {
	return func(s *RuntimeLifecycleService) {
		s.units = repo
	}
}

// WithRuntimeHealthConvergence configures the bounded post-apply health check.
func WithRuntimeHealthConvergence(timeout, interval time.Duration) RuntimeLifecycleOption {
	return func(s *RuntimeLifecycleService) {
		if timeout > 0 {
			s.healthTimeout = timeout
		}
		if interval > 0 {
			s.healthInterval = interval
		}
	}
}

// NewRuntimeLifecycleService creates a RuntimeLifecycleService.
func NewRuntimeLifecycleService(
	registry *RegistryService,
	services repository.ServiceRepository,
	environments repository.EnvironmentRepository,
	artifacts repository.ArtifactRepository,
	state repository.EnvironmentServiceStateRepository,
	resolver runtime.RuntimeResolver,
	publisher events.Publisher,
	logger *zap.Logger,
	opts ...RuntimeLifecycleOption,
) *RuntimeLifecycleService {
	if logger == nil {
		logger = zap.NewNop()
	}
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	svc := &RuntimeLifecycleService{
		registry:       registry,
		services:       services,
		environments:   environments,
		artifacts:      artifacts,
		state:          state,
		resolver:       resolver,
		publisher:      publisher,
		logger:         logger,
		healthTimeout:  defaultRuntimeHealthTimeout,
		healthInterval: defaultRuntimeHealthInterval,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// BuildDesiredStateSnapshot builds the canonical desired-state snapshot that a
// deploy will apply. It resolves the effective deployment unit before intent
// persistence so routing identity and the desired hash describe the same target.
func (s *RuntimeLifecycleService) BuildDesiredStateSnapshot(
	ctx context.Context,
	serviceID, envID, artifactID uuid.UUID,
	requestedUnitID *uuid.UUID,
) (*domain.DesiredServiceSpec, error) {
	return s.buildDesiredStateSnapshot(ctx, serviceID, envID, artifactID, requestedUnitID, nil)
}

// PreviewDesiredStateSnapshot builds the authoritative snapshot using a proposed
// managed definition without persisting it. Signed preview and deploy therefore
// share the same builder, artifact, unit, secret-reference, and hash semantics.
func (s *RuntimeLifecycleService) PreviewDesiredStateSnapshot(
	ctx context.Context,
	serviceID, envID, artifactID uuid.UUID,
	requestedUnitID *uuid.UUID,
	managed *domain.ManagedRuntimeConfig,
) (*domain.DesiredServiceSpec, error) {
	if managed == nil {
		return nil, fmt.Errorf("managed runtime configuration is required for preview")
	}
	managed = domain.NormalizeManagedRuntimeConfig(managed)
	if err := domain.ValidateManagedRuntimeConfig(managed); err != nil {
		return nil, fmt.Errorf("invalid managed runtime configuration: %w", err)
	}
	return s.buildDesiredStateSnapshot(ctx, serviceID, envID, artifactID, requestedUnitID, managed)
}

func (s *RuntimeLifecycleService) buildDesiredStateSnapshot(
	ctx context.Context,
	serviceID, envID, artifactID uuid.UUID,
	requestedUnitID *uuid.UUID,
	managed *domain.ManagedRuntimeConfig,
) (*domain.DesiredServiceSpec, error) {
	svc, env, unit, err := s.resolveDesiredStateDeploymentUnit(ctx, serviceID, envID, requestedUnitID)
	if err != nil {
		return nil, err
	}
	artifact, err := s.resolveDeployArtifact(ctx, serviceID, envID, &artifactID)
	if err != nil {
		return nil, err
	}
	secrets, err := s.effectiveSecrets(ctx, serviceID, envID)
	if err != nil {
		return nil, err
	}
	runtimeConfig := svc.RuntimeConfig
	if managed != nil {
		svcCopy := *svc
		configCopy := domain.ServiceRuntimeConfig{}
		if svc.RuntimeConfig != nil {
			configCopy = *svc.RuntimeConfig
		}
		configCopy.Managed = domain.NormalizeManagedRuntimeConfig(managed)
		svcCopy.RuntimeConfig = &configCopy
		svc = &svcCopy
		runtimeConfig = svc.RuntimeConfig
	}
	return NewDesiredStateBuilder().Build(BuildInput{
		Service:        svc,
		Environment:    env,
		Artifact:       artifact,
		RuntimeConfig:  runtimeConfig,
		DeploymentUnit: unit,
		Secrets:        secrets,
	})
}

func (s *RuntimeLifecycleService) resolveDesiredStateDeploymentUnit(
	ctx context.Context,
	serviceID, envID uuid.UUID,
	requestedUnitID *uuid.UUID,
) (*domain.Service, *domain.Environment, *domain.DeploymentUnit, error) {
	if s.units == nil {
		return nil, nil, nil, fmt.Errorf("deployment unit repository is required to build desired state")
	}
	svc, err := s.services.GetByID(ctx, serviceID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("looking up service: %w", err)
	}
	if svc == nil {
		return nil, nil, nil, fmt.Errorf("service %s not found", serviceID)
	}
	env, err := s.environments.GetByID(ctx, envID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("looking up environment: %w", err)
	}
	if env == nil {
		return nil, nil, nil, fmt.Errorf("environment %s not found", envID)
	}
	if svc.OrgID != uuid.Nil || env.OrgID != uuid.Nil {
		if svc.OrgID == uuid.Nil || env.OrgID == uuid.Nil || svc.OrgID != env.OrgID {
			return nil, nil, nil, fmt.Errorf("service and environment must belong to the same organization")
		}
	}

	var unit *domain.DeploymentUnit
	if requestedUnitID != nil {
		if *requestedUnitID == uuid.Nil {
			return nil, nil, nil, fmt.Errorf("%w: deployment_unit_id", domain.ErrNilUUID)
		}
		unit, err = s.units.GetByID(ctx, *requestedUnitID)
	} else {
		unit, err = s.units.ResolveDefault(ctx, env)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolving deployment unit: %w", err)
	}
	if unit == nil {
		return nil, nil, nil, fmt.Errorf("deployment unit was not found")
	}

	unitCopy := *unit
	domain.NormalizeDeploymentUnitTargeting(&unitCopy)
	if unitCopy.EnvironmentID != env.ID {
		return nil, nil, nil, fmt.Errorf("deployment unit %q belongs to environment %s, not %s", unitCopy.Key, unitCopy.EnvironmentID, env.ID)
	}
	if unitCopy.OwnershipMode != domain.OwnershipModeBahiaManaged {
		return nil, nil, nil, fmt.Errorf("deployment unit %q is not Bahia-managed", unitCopy.Key)
	}
	if err := domain.ValidateDeploymentUnit(&unitCopy); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid deployment unit %q: %w", unitCopy.Key, err)
	}

	svcCopy := *svc
	svcCopy.RuntimeType = unitCopy.RuntimeType
	return &svcCopy, env, &unitCopy, nil
}

// ResolveDeploymentTargetSummary resolves only safe unit identity for intent projections.
func (s *RuntimeLifecycleService) ResolveDeploymentTargetSummary(
	ctx context.Context,
	serviceID, envID uuid.UUID,
	requestedUnitID *uuid.UUID,
) (*DeploymentTargetSummary, error) {
	_, _, unit, err := s.resolveDesiredStateDeploymentUnit(ctx, serviceID, envID, requestedUnitID)
	if err != nil {
		return nil, err
	}
	var unitID *uuid.UUID
	if unit.ID != uuid.Nil {
		id := unit.ID
		unitID = &id
	}
	return &DeploymentTargetSummary{
		UnitID:      unitID,
		UnitKey:     unit.Key,
		RuntimeType: unit.RuntimeType,
		EndpointRef: unit.EndpointRef,
	}, nil
}

// Deploy deploys an artifact directly through the resolved runtime and records a fresh observation.
func (s *RuntimeLifecycleService) Deploy(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.DeployWithStatus(ctx, serviceID, envID, artifactID, nil)
}

// DeployWithStatus deploys an artifact and reports step progression through the callback.
func (s *RuntimeLifecycleService) DeployWithStatus(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID, statusFn DeployStatusCallback) (*domain.RuntimeObservation, error) {
	return s.deployDesiredState(ctx, serviceID, envID, artifactID, nil, nil, statusFn, true)
}

// DeployDeploymentUnit executes an intent against an explicitly resolved,
// Bahia-managed deployment unit. The supplied desired state is the canonical
// intent snapshot; when absent the lifecycle builds it from the service and
// artifact while preserving the resolved unit identity.
func (s *RuntimeLifecycleService) DeployDeploymentUnit(
	ctx context.Context,
	serviceID, envID uuid.UUID,
	artifactID *uuid.UUID,
	unit *domain.DeploymentUnit,
	desiredState *domain.DesiredServiceSpec,
) (*domain.RuntimeObservation, error) {
	return s.DeployDeploymentUnitWithStatus(ctx, serviceID, envID, artifactID, unit, desiredState, nil)
}

// DeployDeploymentUnitWithStatus executes a unit deployment and reports durable phases.
func (s *RuntimeLifecycleService) DeployDeploymentUnitWithStatus(
	ctx context.Context,
	serviceID, envID uuid.UUID,
	artifactID *uuid.UUID,
	unit *domain.DeploymentUnit,
	desiredState *domain.DesiredServiceSpec,
	statusFn DeployStatusCallback,
) (*domain.RuntimeObservation, error) {
	if unit == nil {
		return nil, fmt.Errorf("deployment unit is required")
	}
	return s.deployDesiredState(ctx, serviceID, envID, artifactID, unit, desiredState, statusFn, true)
}

// DeployDesiredStateSnapshot applies a pre-intent unit-aware snapshot without
// re-entering the legacy adopted-workload resolution path.
func (s *RuntimeLifecycleService) DeployDesiredStateSnapshot(
	ctx context.Context,
	serviceID, envID uuid.UUID,
	artifactID *uuid.UUID,
	desiredState *domain.DesiredServiceSpec,
	statusFn DeployStatusCallback,
) (*domain.RuntimeObservation, error) {
	if desiredState == nil {
		return nil, fmt.Errorf("desired state is required")
	}
	_, _, unit, err := s.resolveDesiredStateDeploymentUnit(ctx, serviceID, envID, desiredState.DeploymentUnitID)
	if err != nil {
		return nil, err
	}
	if desiredState.DeploymentUnitID == nil && unit.ID != uuid.Nil {
		return nil, fmt.Errorf("desired-state implicit deployment unit became explicit unit %s", unit.ID)
	}
	if desiredState.DeploymentUnitKey != "" && desiredState.DeploymentUnitKey != unit.Key {
		return nil, fmt.Errorf("desired-state unit key %q does not match resolved unit %q", desiredState.DeploymentUnitKey, unit.Key)
	}
	if desiredState.UnitRuntimeType != "" && desiredState.UnitRuntimeType != unit.RuntimeType {
		return nil, fmt.Errorf("desired-state runtime type %q does not match resolved unit %q type %q", desiredState.UnitRuntimeType, unit.Key, unit.RuntimeType)
	}
	return s.deployDesiredState(ctx, serviceID, envID, artifactID, unit, desiredState, statusFn, true)
}

// AutoRemediateDesiredState applies the currently persisted desired artifact for
// scheduled reconciliation. It uses the same desired-state deploy helper as user
// deploys, but attempts the environment apply lock without blocking so active
// user operations preempt scheduled remediation.
func (s *RuntimeLifecycleService) AutoRemediateDesiredState(ctx context.Context, serviceID, envID uuid.UUID, statusFn DeployStatusCallback) (*domain.RuntimeObservation, error) {
	return s.deployDesiredState(ctx, serviceID, envID, nil, nil, nil, statusFn, false)
}

// deployDesiredState is the shared internal deploy helper used by deployment requests,
// direct runtime action=deploy, and rollback-to-artifact. It acquires the environment
// apply lock, builds deploy options, applies through the runtime adapter, observes,
// persists the outcome, and publishes correlated events.
func (s *RuntimeLifecycleService) deployDesiredState(
	ctx context.Context,
	serviceID, envID uuid.UUID,
	artifactID *uuid.UUID,
	unit *domain.DeploymentUnit,
	canonicalDesiredState *domain.DesiredServiceSpec,
	statusFn DeployStatusCallback,
	waitForLock bool,
) (*domain.RuntimeObservation, error) {
	start := time.Now()
	notify := func(step DeployStep, msg string) {
		if statusFn != nil {
			statusFn(ctx, step, msg)
		}
	}

	// Step: building_desired_state — resolve service, env, runtime, artifact, and build deploy options.
	notify(DeployStepBuildingDesiredState, "Resolving service, environment, and artifact")

	svc, env, rt, err := s.resolveForDeploymentUnit(ctx, serviceID, envID, unit)
	if err != nil {
		s.logRuntimeAction("deploy", nil, nil, serviceID, envID, artifactID, start, "failed", err)
		return nil, err
	}

	artifact, err := s.resolveDeployArtifact(ctx, serviceID, envID, artifactID)
	if err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, artifactID, start, "failed", err)
		return nil, err
	}

	targetName := svc.RuntimeTargetName()
	opts := deployOptionsFromServiceRuntimeConfig(svc)

	secrets, err := s.effectiveSecrets(ctx, serviceID, envID)
	if err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, err
	}

	builder := NewDesiredStateBuilder()
	targetSpec, err := desiredStateForDeployment(builder, svc, env, artifact, unit, secrets, canonicalDesiredState)
	if err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, fmt.Errorf("building desired state: %w", err)
	}
	secretAccesses, err := s.mergeEffectiveSecrets(ctx, secrets, &opts, targetSpec, svc.RuntimeConfig != nil && svc.RuntimeConfig.Managed != nil)
	if err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, err
	}
	plan, err := NewEnvironmentPlanAssembler(EnvironmentPlanAssemblerDeps{
		StateLoader: s.state,
		StateWriter: s.state,
		Services:    s.services,
		Artifacts:   s.artifacts,
		Secrets:     secretListerOrEmpty{s.secrets},
		Units:       s.units,
		Builder:     builder,
	}).Assemble(ctx, envID, serviceID, targetSpec)
	if err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, fmt.Errorf("assembling desired environment plan: %w", err)
	}

	if opts.Labels == nil {
		opts.Labels = map[string]string{}
	}
	opts.Labels["bahia.service"] = svc.Name
	opts.Labels["bahia.managed"] = "true"

	// Compose ownership gate: if the resolved runtime is Compose, validate
	// ownership BEFORE any locking, staging, file writes, validation, pull,
	// or up operations. On failure, attempt best-effort observation (safe
	// and non-mutating), persist failed apply metadata, and publish a
	// correlated failure event with ownership error details.
	if composeRT, ok := rt.(*runtime.ComposeRuntime); ok {
		if ownershipErr := composeRT.ValidateOwnership(runtime.ComposeOwnershipConfig{}); ownershipErr != nil {
			notify(DeployStepRendering, "Compose ownership validation failed")

			// Best-effort observation — non-mutating, safe even for non-owned dirs.
			var bestEffortObs *domain.RuntimeObservation
			if obsResult, obsErr := rt.Observe(ctx, serviceID, envID, targetName); obsErr == nil {
				bestEffortObs = obsResult
				_ = s.registry.RecordObservation(ctx, bestEffortObs)
			}

			// Publish failure event with ownership error details.
			failureData := map[string]any{
				"artifact_id":      artifact.ID,
				"failure_reason":   "compose_ownership_validation_failed",
				"ownership_reason": ownershipErr.Error(),
			}
			if ownershipTyped, ok := runtime.AsComposeOwnershipError(ownershipErr); ok {
				failureData["ownership_reason_code"] = ownershipTyped.ReasonCode
			}
			s.publishFailure(ctx, svc, env, bestEffortObs, failureData)
			s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", ownershipErr)
			return nil, fmt.Errorf("compose ownership validation failed: %w", ownershipErr)
		}
	}

	// Step: locking_environment — acquire environment-scoped advisory lock.
	notify(DeployStepLockingEnvironment, "Acquiring environment apply lock")

	if s.applyLock != nil {
		unlock, lockErr := s.acquireApplyLock(ctx, envID, waitForLock)
		if lockErr != nil {
			s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", lockErr)
			return nil, fmt.Errorf("acquiring environment apply lock: %w", lockErr)
		}
		defer unlock()
	}

	// Step: rendering — prepare the runtime target configuration.
	notify(DeployStepRendering, "Preparing runtime deploy configuration")

	imageRef := imageRefForArtifact(artifact)

	// Step: applying — execute the deploy through the runtime adapter.
	notify(DeployStepApplying, fmt.Sprintf("Deploying %s to %s", imageRef, targetName))

	var applyResult *runtime.DesiredStateApplyResult
	if applier, ok := runtime.AsDesiredStateApplier(rt); ok {
		applyResult, err = applier.ApplyDesiredState(ctx, runtime.DesiredStateApplyRequest{
			EnvironmentPlan: plan,
			TargetService:   targetSpec,
			Secrets:         opts.Environment,
			PullPolicy:      targetSpec.PullPolicy,
		})
		if err != nil {
			s.recordRuntimeApplySecretAudit(ctx, secretAccesses, domain.SecretAccessOutcomeFailure, err)
			s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
			return nil, fmt.Errorf("applying desired state for runtime target %q: %w", targetName, err)
		}
	} else {
		if unit != nil && unit.RuntimeType == domain.RuntimeTypeCompose {
			err := fmt.Errorf("%w: resolved compose deployment unit %q", runtime.ErrDesiredStateNotSupported, unit.Key)
			s.recordRuntimeApplySecretAudit(ctx, secretAccesses, domain.SecretAccessOutcomeFailure, err)
			s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
			return nil, err
		}
		if err := rt.Deploy(ctx, targetName, imageRef, opts); err != nil {
			s.recordRuntimeApplySecretAudit(ctx, secretAccesses, domain.SecretAccessOutcomeFailure, err)
			s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
			return nil, fmt.Errorf("deploying runtime target %q: %w", targetName, err)
		}
	}
	s.recordRuntimeApplySecretAudit(ctx, secretAccesses, domain.SecretAccessOutcomeSuccess, nil)

	// Step: observing — observe the runtime state after deploy.
	notify(DeployStepObserving, "Observing runtime state after deploy")

	waitForHealthy := unit != nil && unit.RuntimeType == domain.RuntimeTypeCompose
	obs, observeErr := s.observeDeploymentHealth(ctx, rt, serviceID, envID, targetName, waitForHealthy, targetSpec.DesiredHash)
	if obs == nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", observeErr)
		return nil, observeErr
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now().UTC()
	}
	if unit != nil && unit.ID != uuid.Nil {
		unitID := unit.ID
		obs.DeploymentUnitID = &unitID
	}

	// Step: projecting — persist state and publish events.
	notify(DeployStepProjecting, "Persisting deployment outcome")

	if err := s.updateDesiredArtifact(ctx, serviceID, envID, artifact.ID, targetSpec); err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, err
	}
	if err := s.registry.RecordObservation(ctx, obs); err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, fmt.Errorf("recording deploy observation: %w", err)
	}
	if observeErr != nil {
		failureCode := "runtime_observation_failed"
		if healthErr, ok := observeErr.(*DeploymentHealthError); ok {
			failureCode = healthErr.Code
		}
		s.publishFailure(ctx, svc, env, obs, map[string]any{
			"artifact_id":    artifact.ID,
			"failure_reason": failureCode,
		})
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", observeErr)
		return obs, observeErr
	}
	actionData := map[string]any{"artifact_id": artifact.ID, "desired_hash": targetSpec.DesiredHash, "environment_revision": plan.RevisionHash}
	if applyResult != nil {
		actionData["renderer"] = applyResult.Renderer
		actionData["execution_mode"] = string(applyResult.ExecutionMode)
		actionData["resource_names"] = applyResult.ResourceNames
		actionData["warnings"] = applyResult.Warnings
	}
	s.publishAction(ctx, runtimeActionDeployEvent, svc, env, obs, actionData)
	s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "success", nil)
	return obs, nil
}

func (s *RuntimeLifecycleService) observeDeploymentHealth(
	ctx context.Context,
	rt runtime.Runtime,
	serviceID, envID uuid.UUID,
	targetName string,
	waitForHealthy bool,
	desiredHash string,
) (*domain.RuntimeObservation, error) {
	var latest *domain.RuntimeObservation
	for {
		obs, err := rt.Observe(ctx, serviceID, envID, targetName)
		if err == nil && obs != nil {
			latest = obs
			if !waitForHealthy || deploymentObservationConverged(obs, desiredHash) {
				return obs, nil
			}
		} else if !waitForHealthy {
			return nil, fmt.Errorf("observing runtime target %q after deploy: %w", targetName, err)
		}

		if !waitForHealthy {
			return latest, nil
		}
		timeout := s.healthTimeout
		if timeout <= 0 {
			timeout = defaultRuntimeHealthTimeout
		}
		interval := s.healthInterval
		if interval <= 0 {
			interval = defaultRuntimeHealthInterval
		}
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		for {
			retry := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				retry.Stop()
				return latest, ctx.Err()
			case <-deadline.C:
				retry.Stop()
				if latest != nil {
					if latest.HealthStatus == domain.HealthStatusHealthy && strings.TrimSpace(desiredHash) != "" && observedStateHash(latest) != desiredHash {
						return latest, &DeploymentHealthError{
							Code:    "desired_state_mismatch",
							Message: "The healthy runtime did not converge to the reviewed desired state.",
						}
					}
					return latest, &DeploymentHealthError{
						Code:    "health_check_timeout",
						Message: "The deployed service did not become healthy before the health deadline.",
					}
				}
				return nil, &DeploymentHealthError{
					Code:    "runtime_observation_failed",
					Message: "Bahia could not confirm the deployed service health.",
				}
			case <-retry.C:
				obs, err := rt.Observe(ctx, serviceID, envID, targetName)
				if err == nil && obs != nil {
					latest = obs
					if deploymentObservationConverged(obs, desiredHash) {
						return obs, nil
					}
				}
			}
		}
	}
}

func deploymentObservationConverged(obs *domain.RuntimeObservation, desiredHash string) bool {
	if obs == nil || obs.HealthStatus != domain.HealthStatusHealthy {
		return false
	}
	desiredHash = strings.TrimSpace(desiredHash)
	return desiredHash == "" || observedStateHash(obs) == desiredHash
}

// Restart restarts a service directly through the resolved runtime and records a fresh observation.
func (s *RuntimeLifecycleService) Restart(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	start := time.Now()
	svc, env, rt, err := s.resolve(ctx, serviceID, envID)
	if err != nil {
		s.logRuntimeAction("restart", nil, nil, serviceID, envID, nil, start, "failed", err)
		return nil, err
	}
	lifecycle, ok := rt.(runtime.LifecycleRuntime)
	if !ok {
		err := fmt.Errorf("runtime %s does not support restart", rt.Type())
		s.logRuntimeAction("restart", svc, env, serviceID, envID, nil, start, "failed", err)
		return nil, err
	}
	targetName := svc.RuntimeTargetName()
	if err := lifecycle.Restart(ctx, targetName); err != nil {
		s.logRuntimeAction("restart", svc, env, serviceID, envID, nil, start, "failed", err)
		return nil, fmt.Errorf("restarting runtime target %q: %w", targetName, err)
	}
	obs, err := rt.Observe(ctx, serviceID, envID, targetName)
	if err != nil {
		s.logRuntimeAction("restart", svc, env, serviceID, envID, nil, start, "failed", err)
		return nil, fmt.Errorf("observing runtime target %q after restart: %w", targetName, err)
	}
	if err := s.registry.RecordObservation(ctx, obs); err != nil {
		s.logRuntimeAction("restart", svc, env, serviceID, envID, nil, start, "failed", err)
		return nil, fmt.Errorf("recording restart observation: %w", err)
	}
	s.publishAction(ctx, runtimeActionRestartEvent, svc, env, obs, nil)
	s.logRuntimeAction("restart", svc, env, serviceID, envID, nil, start, "success", nil)
	return obs, nil
}

// Stop stops a service directly through the resolved runtime and records a fresh observation.
func (s *RuntimeLifecycleService) Stop(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	start := time.Now()
	svc, env, rt, err := s.resolve(ctx, serviceID, envID)
	if err != nil {
		s.logRuntimeAction("stop", nil, nil, serviceID, envID, nil, start, "failed", err)
		return nil, err
	}
	lifecycle, ok := rt.(runtime.LifecycleRuntime)
	if !ok {
		err := fmt.Errorf("runtime %s does not support stop", rt.Type())
		s.logRuntimeAction("stop", svc, env, serviceID, envID, nil, start, "failed", err)
		return nil, err
	}
	targetName := svc.RuntimeTargetName()
	if err := lifecycle.Stop(ctx, targetName); err != nil {
		s.logRuntimeAction("stop", svc, env, serviceID, envID, nil, start, "failed", err)
		return nil, fmt.Errorf("stopping runtime target %q: %w", targetName, err)
	}
	obs, err := rt.Observe(ctx, serviceID, envID, targetName)
	if err != nil {
		s.logRuntimeAction("stop", svc, env, serviceID, envID, nil, start, "failed", err)
		return nil, fmt.Errorf("observing runtime target %q after stop: %w", targetName, err)
	}
	if err := s.registry.RecordObservation(ctx, obs); err != nil {
		s.logRuntimeAction("stop", svc, env, serviceID, envID, nil, start, "failed", err)
		return nil, fmt.Errorf("recording stop observation: %w", err)
	}
	s.publishAction(ctx, runtimeActionStopEvent, svc, env, obs, nil)
	s.logRuntimeAction("stop", svc, env, serviceID, envID, nil, start, "success", nil)
	return obs, nil
}

func (s *RuntimeLifecycleService) acquireApplyLock(ctx context.Context, envID uuid.UUID, wait bool) (func(), error) {
	if s.applyLock == nil {
		return func() {}, nil
	}
	if wait {
		return s.applyLock.Lock(ctx, envID)
	}
	tryLock, ok := s.applyLock.(EnvironmentApplyTryLocker)
	if !ok {
		return nil, ErrEnvironmentApplyLockContended
	}
	unlock, acquired, err := tryLock.TryLock(ctx, envID)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrEnvironmentApplyLockContended
	}
	return unlock, nil
}

func (s *RuntimeLifecycleService) resolveForDeploymentUnit(
	ctx context.Context,
	serviceID, envID uuid.UUID,
	unit *domain.DeploymentUnit,
) (*domain.Service, *domain.Environment, runtime.Runtime, error) {
	if unit == nil {
		return s.resolve(ctx, serviceID, envID)
	}
	if s.resolver == nil {
		return nil, nil, nil, fmt.Errorf("runtime resolver is required")
	}
	svc, err := s.services.GetByID(ctx, serviceID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("looking up service: %w", err)
	}
	if svc == nil {
		return nil, nil, nil, fmt.Errorf("service %s not found", serviceID)
	}
	env, err := s.environments.GetByID(ctx, envID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("looking up environment: %w", err)
	}
	if env == nil {
		return nil, nil, nil, fmt.Errorf("environment %s not found", envID)
	}

	unitCopy := *unit
	domain.NormalizeDeploymentUnitTargeting(&unitCopy)
	if unitCopy.EnvironmentID != env.ID {
		return nil, nil, nil, fmt.Errorf("deployment unit %q belongs to environment %s, not %s", unitCopy.Key, unitCopy.EnvironmentID, env.ID)
	}
	if unitCopy.OwnershipMode != domain.OwnershipModeBahiaManaged {
		return nil, nil, nil, fmt.Errorf("deployment unit %q is not Bahia-managed", unitCopy.Key)
	}
	if err := domain.ValidateDeploymentUnit(&unitCopy); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid deployment unit %q: %w", unitCopy.Key, err)
	}
	// Explicit Bahia-managed deployment units are provisionable on first deploy;
	// adopted-workload state guardrails remain in the legacy environment-only path.
	unitResolver, ok := s.resolver.(runtime.DeploymentUnitRuntimeResolver)
	if !ok {
		return nil, nil, nil, fmt.Errorf("runtime resolver does not support deployment-unit targets")
	}
	rt, err := unitResolver.ResolveDeploymentUnit(svc, env, &unitCopy)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolving deployment-unit runtime: %w", err)
	}
	if rt.Type() != unitCopy.RuntimeType {
		return nil, nil, nil, fmt.Errorf("resolved runtime type %q does not match deployment unit %q type %q", rt.Type(), unitCopy.Key, unitCopy.RuntimeType)
	}

	svcCopy := *svc
	svcCopy.RuntimeType = unitCopy.RuntimeType
	return &svcCopy, env, rt, nil
}

func (s *RuntimeLifecycleService) resolve(ctx context.Context, serviceID, envID uuid.UUID) (*domain.Service, *domain.Environment, runtime.Runtime, error) {
	if s.resolver == nil {
		return nil, nil, nil, fmt.Errorf("runtime resolver is required")
	}
	svc, err := s.services.GetByID(ctx, serviceID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("looking up service: %w", err)
	}
	if svc == nil {
		return nil, nil, nil, fmt.Errorf("service %s not found", serviceID)
	}
	env, err := s.environments.GetByID(ctx, envID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("looking up environment: %w", err)
	}
	if env == nil {
		return nil, nil, nil, fmt.Errorf("environment %s not found", envID)
	}
	if err := validateDirectRuntimeWorkload(svc, env); err != nil {
		return nil, nil, nil, err
	}
	if err := s.validateDirectRuntimeState(ctx, svc.ID, env.ID); err != nil {
		return nil, nil, nil, err
	}
	rt, err := s.resolver.Resolve(svc, env)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolving runtime: %w", err)
	}
	return svc, env, rt, nil
}

func (s *RuntimeLifecycleService) validateDirectRuntimeState(ctx context.Context, serviceID, envID uuid.UUID) error {
	if s.state == nil {
		return fmt.Errorf(directRuntimeGuardrailMessage)
	}
	st, err := s.state.Get(ctx, serviceID, envID)
	if err != nil {
		return fmt.Errorf("looking up direct runtime state: %w", err)
	}
	if st == nil {
		return fmt.Errorf(directRuntimeGuardrailMessage)
	}
	return nil
}

func validateDirectRuntimeWorkload(svc *domain.Service, env *domain.Environment) error {
	if svc == nil || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		return fmt.Errorf(directRuntimeGuardrailMessage)
	}
	if env == nil || env.RuntimeConfig == nil {
		return fmt.Errorf(directRuntimeGuardrailMessage)
	}
	mode, _ := stringFromAny(env.RuntimeConfig["management_mode"])
	if mode != "direct_runtime" {
		return fmt.Errorf(directRuntimeGuardrailMessage)
	}
	adopted := svc.RuntimeConfig.Adopted
	hostAlias, _ := stringFromAny(env.RuntimeConfig["host_alias"])
	if adopted.HostAlias == "" || hostAlias != adopted.HostAlias {
		return fmt.Errorf(directRuntimeGuardrailMessage)
	}
	envEndpointRef, _ := stringFromAny(env.RuntimeConfig["endpoint_ref"])
	if envEndpointRef != adopted.EndpointRef {
		return fmt.Errorf(directRuntimeGuardrailMessage)
	}
	return nil
}

func desiredStateForDeployment(
	builder *DesiredStateBuilder,
	svc *domain.Service,
	env *domain.Environment,
	artifact *domain.Artifact,
	unit *domain.DeploymentUnit,
	secrets []domain.ServiceSecret,
	canonical *domain.DesiredServiceSpec,
) (*domain.DesiredServiceSpec, error) {
	if canonical == nil {
		return builder.Build(BuildInput{
			Service:        svc,
			Environment:    env,
			Artifact:       artifact,
			RuntimeConfig:  svc.RuntimeConfig,
			DeploymentUnit: unit,
			Secrets:        secrets,
		})
	}

	spec := *canonical
	switch {
	case spec.ServiceID != svc.ID:
		return nil, fmt.Errorf("desired state service %s does not match service %s", spec.ServiceID, svc.ID)
	case spec.EnvironmentID != env.ID:
		return nil, fmt.Errorf("desired state environment %s does not match environment %s", spec.EnvironmentID, env.ID)
	case spec.ArtifactID != artifact.ID:
		return nil, fmt.Errorf("desired state artifact %s does not match artifact %s", spec.ArtifactID, artifact.ID)
	}

	spec.Labels = copyStringMap(spec.Labels)
	if unit != nil {
		if spec.DeploymentUnitID == nil && unit.ID != uuid.Nil {
			return nil, fmt.Errorf("desired state implicit deployment unit became explicit unit %s", unit.ID)
		}
		if spec.DeploymentUnitID != nil && *spec.DeploymentUnitID != uuid.Nil && unit.ID != uuid.Nil && *spec.DeploymentUnitID != unit.ID {
			return nil, fmt.Errorf("desired state deployment unit %s does not match resolved unit %s", *spec.DeploymentUnitID, unit.ID)
		}
		if spec.DeploymentUnitKey != "" && spec.DeploymentUnitKey != unit.Key {
			return nil, fmt.Errorf("desired state deployment unit key %q does not match resolved unit %q", spec.DeploymentUnitKey, unit.Key)
		}
		if spec.UnitRuntimeType != "" && spec.UnitRuntimeType != unit.RuntimeType {
			return nil, fmt.Errorf("desired state runtime type %q does not match resolved unit %q type %q", spec.UnitRuntimeType, unit.Key, unit.RuntimeType)
		}
		var unitID *uuid.UUID
		if unit.ID != uuid.Nil {
			id := unit.ID
			unitID = &id
		}
		domain.NormalizeDesiredServiceUnitIdentity(&spec, unitID, unit.Key, unit.RuntimeType)
	}
	if spec.Labels == nil {
		spec.Labels = map[string]string{}
	}
	spec.Labels["bahia.managed"] = "true"
	spec.Labels["bahia.service_id"] = svc.ID.String()
	spec.Labels["bahia.environment_id"] = env.ID.String()
	spec.Labels["bahia.artifact_id"] = artifact.ID.String()
	spec.Labels["bahia.deployment_unit_key"] = spec.DeploymentUnitKey
	if spec.DeploymentUnitID != nil {
		spec.Labels["bahia.deployment_unit_id"] = spec.DeploymentUnitID.String()
	}
	if unit != nil && unit.RuntimeType == domain.RuntimeTypeCompose && spec.ComposeExtension == nil {
		spec.ComposeExtension = &domain.ComposeExtension{}
	}
	spec.ComputeDesiredHash()
	spec.Labels["bahia.desired_hash"] = spec.DesiredHash
	if err := ValidateSpec(&spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func (s *RuntimeLifecycleService) resolveDeployArtifact(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID) (*domain.Artifact, error) {
	if artifactID == nil {
		st, err := s.state.Get(ctx, serviceID, envID)
		if err != nil {
			return nil, fmt.Errorf("looking up desired state: %w", err)
		}
		if st == nil || st.DesiredArtifactID == nil {
			return nil, fmt.Errorf("no desired artifact for service %s in environment %s", serviceID, envID)
		}
		artifactID = st.DesiredArtifactID
	}
	artifact, err := s.artifacts.GetByID(ctx, *artifactID)
	if err != nil {
		return nil, fmt.Errorf("looking up artifact: %w", err)
	}
	if artifact == nil {
		return nil, fmt.Errorf("artifact %s not found", *artifactID)
	}
	if artifact.ServiceID != serviceID {
		return nil, fmt.Errorf("artifact %s belongs to service %s, not %s", artifact.ID, artifact.ServiceID, serviceID)
	}
	return artifact, nil
}

func (s *RuntimeLifecycleService) effectiveSecrets(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	if s.secrets == nil {
		return nil, nil
	}
	return s.secrets.ListEffective(ctx, serviceID, envID)
}

type secretListerOrEmpty struct{ repo repository.SecretRepository }

func (l secretListerOrEmpty) ListEffective(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	if l.repo == nil {
		return nil, nil
	}
	return l.repo.ListEffective(ctx, serviceID, envID)
}

func (s *RuntimeLifecycleService) mergeEffectiveSecrets(ctx context.Context, secrets []domain.ServiceSecret, opts *runtime.DeployOptions, desiredState *domain.DesiredServiceSpec, managed bool) ([]domain.SecretAccessManifest, error) {
	bindings, err := desiredSecretBindings(desiredState, managed)
	if err != nil {
		return nil, err
	}
	if len(secrets) == 0 {
		if len(bindings) > 0 {
			return nil, fmt.Errorf("managed desired state references unavailable secrets")
		}
		return nil, nil
	}
	if s.secretEncryptor == nil {
		return nil, fmt.Errorf("effective secrets are configured but secret encryption is unavailable")
	}
	if opts.Environment == nil {
		opts.Environment = map[string]string{}
	}
	accesses := make([]domain.SecretAccessManifest, 0, len(secrets))
	resolvedBindings := make(map[uuid.UUID]struct{}, len(bindings))
	for _, secret := range secrets {
		envVar := secret.Name
		if managed {
			var selected bool
			envVar, selected = bindings[secret.ID]
			if !selected {
				continue
			}
			resolvedBindings[secret.ID] = struct{}{}
		}
		if secret.Name == "" || envVar == "" {
			continue
		}
		version, err := s.secrets.GetCurrentVersion(ctx, secret.ID)
		if err != nil {
			return nil, fmt.Errorf("loading current version for effective secret %q: %w", secret.Name, err)
		}
		if version == nil {
			return nil, fmt.Errorf("effective secret %q has no versioned payload", secret.Name)
		}
		accessedAt := time.Now().UTC()
		value, decryptErr := s.secretEncryptor.Decrypt(version.EncryptedValue, version.EncryptionMethod)
		manifest := domain.SecretAccessManifest{
			SecretID:      secret.ID,
			VersionID:     version.ID,
			Version:       version.Version,
			ServiceID:     secret.ServiceID,
			EnvironmentID: secret.EnvironmentID,
			Name:          secret.Name,
			Operation:     domain.SecretAccessOperationResolve,
			Outcome:       domain.SecretAccessOutcomeSuccess,
			AccessedAt:    accessedAt,
		}
		audit := &domain.SecretAccessAudit{
			SecretID:      secret.ID,
			VersionID:     version.ID,
			Version:       version.Version,
			ServiceID:     secret.ServiceID,
			EnvironmentID: secret.EnvironmentID,
			Operation:     domain.SecretAccessOperationResolve,
			Outcome:       domain.SecretAccessOutcomeSuccess,
			Reason:        "runtime_desired_state_apply",
			AccessedAt:    accessedAt,
		}
		if decryptErr != nil {
			manifest.Outcome = domain.SecretAccessOutcomeFailure
			audit.Outcome = domain.SecretAccessOutcomeFailure
			audit.Error = decryptErr.Error()
		}
		if auditErr := s.secrets.RecordSecretAccessAudit(ctx, audit); auditErr != nil {
			return nil, auditErr
		}
		accesses = append(accesses, manifest)
		if decryptErr != nil {
			return accesses, fmt.Errorf("decrypting effective secret %q version %d: %w", secret.Name, version.Version, decryptErr)
		}
		opts.Environment[envVar] = value
	}
	if managed && len(resolvedBindings) != len(bindings) {
		return accesses, fmt.Errorf("managed desired state references unavailable secrets")
	}
	return accesses, nil
}

func desiredSecretBindings(desiredState *domain.DesiredServiceSpec, managed bool) (map[uuid.UUID]string, error) {
	if !managed {
		return nil, nil
	}
	if desiredState == nil {
		return nil, fmt.Errorf("managed desired state is required to resolve secrets")
	}
	bindings := make(map[uuid.UUID]string, len(desiredState.SecretRefs))
	for _, ref := range desiredState.SecretRefs {
		if ref.SecretID == uuid.Nil || strings.TrimSpace(ref.EnvVar) == "" {
			return nil, fmt.Errorf("managed desired state contains an invalid secret reference")
		}
		if _, exists := bindings[ref.SecretID]; exists {
			return nil, fmt.Errorf("managed desired state contains a duplicate secret reference")
		}
		bindings[ref.SecretID] = ref.EnvVar
	}
	return bindings, nil
}

func (s *RuntimeLifecycleService) recordRuntimeApplySecretAudit(ctx context.Context, accesses []domain.SecretAccessManifest, outcome domain.SecretAccessOutcome, applyErr error) {
	if s == nil || s.secrets == nil || len(accesses) == 0 {
		return
	}
	errorText := ""
	if applyErr != nil {
		errorText = applyErr.Error()
	}
	for _, access := range accesses {
		if access.SecretID == uuid.Nil || access.VersionID == uuid.Nil {
			continue
		}
		err := s.secrets.RecordSecretAccessAudit(ctx, &domain.SecretAccessAudit{
			SecretID:      access.SecretID,
			VersionID:     access.VersionID,
			Version:       access.Version,
			ServiceID:     access.ServiceID,
			EnvironmentID: access.EnvironmentID,
			Operation:     domain.SecretAccessOperationRuntimeApply,
			Outcome:       outcome,
			Reason:        "runtime_desired_state_apply",
			Error:         errorText,
			AccessedAt:    time.Now().UTC(),
		})
		if err != nil {
			s.logger.Warn("failed to record secret apply audit", zap.Error(err), zap.String("secret_id", access.SecretID.String()))
		}
	}
}

func (s *RuntimeLifecycleService) updateDesiredArtifact(ctx context.Context, serviceID, envID, artifactID uuid.UUID, desiredState *domain.DesiredServiceSpec) error {
	st, err := s.state.Get(ctx, serviceID, envID)
	if err != nil {
		return fmt.Errorf("looking up current state: %w", err)
	}
	if st == nil {
		st = &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID}
	}
	st.DesiredArtifactID = &artifactID
	st.DesiredRuntimeState = desiredState
	if desiredState != nil {
		st.DesiredHash = desiredState.DesiredHash
		if desiredState.DeploymentUnitID != nil {
			unitID := *desiredState.DeploymentUnitID
			st.DeploymentUnitID = &unitID
		}
	}
	st.DriftStatus = domain.DriftStatusDeploying
	return s.state.Upsert(ctx, st)
}

func (s *RuntimeLifecycleService) publishFailure(ctx context.Context, svc *domain.Service, env *domain.Environment, obs *domain.RuntimeObservation, extra map[string]any) {
	data := map[string]any{
		"service_id":     svc.ID,
		"environment_id": env.ID,
		"service":        svc.Name,
		"environment":    env.Name,
		"runtime_target": svc.RuntimeTargetName(),
		"result":         "failed",
	}
	if obs != nil {
		data["observation_id"] = obs.ID
		data["health_status"] = obs.HealthStatus
	}
	for k, v := range extra {
		data[k] = v
	}
	s.publisher.Publish(ctx, events.Event{Type: runtimeActionDeployEvent, EntityID: svc.ID.String(), Data: data})
}

func (s *RuntimeLifecycleService) publishAction(ctx context.Context, eventType events.EventType, svc *domain.Service, env *domain.Environment, obs *domain.RuntimeObservation, extra map[string]any) {
	data := map[string]any{
		"service_id":     svc.ID,
		"environment_id": env.ID,
		"service":        svc.Name,
		"environment":    env.Name,
		"runtime_target": svc.RuntimeTargetName(),
		"observation_id": obs.ID,
		"health_status":  obs.HealthStatus,
	}
	for k, v := range extra {
		data[k] = v
	}
	s.publisher.Publish(ctx, events.Event{Type: eventType, EntityID: svc.ID.String(), Data: data})
}

func (s *RuntimeLifecycleService) logRuntimeAction(action string, svc *domain.Service, env *domain.Environment, serviceID, envID uuid.UUID, artifactID *uuid.UUID, start time.Time, result string, err error) {
	fields := []zap.Field{
		zap.String("action", action),
		zap.String("service_id", serviceID.String()),
		zap.String("environment_id", envID.String()),
		zap.Int64("duration_ms", time.Since(start).Milliseconds()),
		zap.String("result", result),
	}
	if artifactID != nil {
		fields = append(fields, zap.String("artifact_id", artifactID.String()))
	}
	if svc != nil {
		fields = append(fields,
			zap.String("service_name", svc.Name),
			zap.String("target_name", svc.RuntimeTargetName()),
		)
		if svc.RuntimeConfig != nil && svc.RuntimeConfig.Adopted != nil {
			fields = append(fields, zap.String("endpoint_ref", svc.RuntimeConfig.Adopted.EndpointRef))
		}
	}
	if env != nil {
		fields = append(fields, zap.String("environment_name", env.Name))
		if env.RuntimeConfig != nil {
			if endpointRef, ok := stringFromAny(env.RuntimeConfig["endpoint_ref"]); ok {
				fields = append(fields, zap.String("endpoint_ref", endpointRef))
			}
		}
	}
	if err != nil {
		fields = append(fields, zap.String("error", err.Error()))
	}
	s.logger.Info("direct runtime action completed", fields...)
}

func deployOptionsFromServiceRuntimeConfig(svc *domain.Service) runtime.DeployOptions {
	if svc == nil || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		return runtime.DeployOptions{}
	}
	adopted := svc.RuntimeConfig.Adopted
	return runtime.DeployOptions{
		Environment: copyStringMap(adopted.Environment),
		Labels:      copyStringMap(adopted.Labels),
		Ports:       append([]string(nil), adopted.Ports...),
		Volumes:     append([]string(nil), adopted.Volumes...),
		Restart:     adopted.Restart,
		Command:     append([]string(nil), adopted.Command...),
		Entrypoint:  append([]string(nil), adopted.Entrypoint...),
		WorkingDir:  adopted.WorkingDir,
		NetworkMode: adopted.NetworkMode,
	}
}

func imageRefForArtifact(artifact *domain.Artifact) string {
	if artifact == nil {
		return ""
	}
	if artifact.ImageDigest != "" {
		return artifact.ImageRepo + "@" + artifact.ImageDigest
	}
	return artifact.ImageRepo + ":" + artifact.ImageTag
}
