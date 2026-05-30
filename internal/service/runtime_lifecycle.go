package service

import (
	"context"
	"fmt"
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

// EnvironmentApplyLocker serializes deploy operations per environment.
type EnvironmentApplyLocker interface {
	Lock(ctx context.Context, environmentID uuid.UUID) (unlock func(), err error)
}

// RuntimeLifecycleService performs direct runtime actions for services resolved to a runtime.
type RuntimeLifecycleService struct {
	registry     *RegistryService
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	artifacts    repository.ArtifactRepository
	state        repository.EnvironmentServiceStateRepository
	resolver     runtime.RuntimeResolver
	secrets      repository.SecretRepository
	publisher    events.Publisher
	logger       *zap.Logger
	applyLock    EnvironmentApplyLocker

	secretEncryptor *secretsAdapter.Encryptor
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
		registry:     registry,
		services:     services,
		environments: environments,
		artifacts:    artifacts,
		state:        state,
		resolver:     resolver,
		publisher:    publisher,
		logger:       logger,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// BuildDesiredStateSnapshot builds the canonical desired-state snapshot that a
// deploy will apply. It is used before intent persistence so request records
// carry the same deterministic desired hash as runtime state rows.
func (s *RuntimeLifecycleService) BuildDesiredStateSnapshot(ctx context.Context, serviceID, envID, artifactID uuid.UUID) (*domain.DesiredServiceSpec, error) {
	svc, env, _, err := s.resolve(ctx, serviceID, envID)
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
	return NewDesiredStateBuilder().Build(BuildInput{
		Service:       svc,
		Environment:   env,
		Artifact:      artifact,
		RuntimeConfig: svc.RuntimeConfig,
		Secrets:       secrets,
	})
}

// Deploy deploys an artifact directly through the resolved runtime and records a fresh observation.
func (s *RuntimeLifecycleService) Deploy(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.DeployWithStatus(ctx, serviceID, envID, artifactID, nil)
}

// DeployWithStatus deploys an artifact and reports step progression through the callback.
func (s *RuntimeLifecycleService) DeployWithStatus(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID, statusFn DeployStatusCallback) (*domain.RuntimeObservation, error) {
	return s.deployDesiredState(ctx, serviceID, envID, artifactID, statusFn)
}

// deployDesiredState is the shared internal deploy helper used by deployment requests,
// direct runtime action=deploy, and rollback-to-artifact. It acquires the environment
// apply lock, builds deploy options, applies through the runtime adapter, observes,
// persists the outcome, and publishes correlated events.
func (s *RuntimeLifecycleService) deployDesiredState(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID, statusFn DeployStatusCallback) (*domain.RuntimeObservation, error) {
	start := time.Now()
	notify := func(step DeployStep, msg string) {
		if statusFn != nil {
			statusFn(ctx, step, msg)
		}
	}

	// Step: building_desired_state — resolve service, env, runtime, artifact, and build deploy options.
	notify(DeployStepBuildingDesiredState, "Resolving service, environment, and artifact")

	svc, env, rt, err := s.resolve(ctx, serviceID, envID)
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
	if err := s.mergeEffectiveSecrets(ctx, serviceID, envID, &opts); err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, err
	}

	secrets, err := s.effectiveSecrets(ctx, serviceID, envID)
	if err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, err
	}

	builder := NewDesiredStateBuilder()
	targetSpec, err := builder.Build(BuildInput{
		Service:       svc,
		Environment:   env,
		Artifact:      artifact,
		RuntimeConfig: svc.RuntimeConfig,
		Secrets:       secrets,
	})
	if err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, fmt.Errorf("building desired state: %w", err)
	}
	plan, err := NewEnvironmentPlanAssembler(EnvironmentPlanAssemblerDeps{
		StateLoader: s.state,
		StateWriter: s.state,
		Services:    s.services,
		Artifacts:   s.artifacts,
		Secrets:     secretListerOrEmpty{s.secrets},
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
		unlock, lockErr := s.applyLock.Lock(ctx, envID)
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
			s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
			return nil, fmt.Errorf("applying desired state for runtime target %q: %w", targetName, err)
		}
	} else {
		if err := rt.Deploy(ctx, targetName, imageRef, opts); err != nil {
			s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
			return nil, fmt.Errorf("deploying runtime target %q: %w", targetName, err)
		}
	}

	// Step: observing — observe the runtime state after deploy.
	notify(DeployStepObserving, "Observing runtime state after deploy")

	obs, err := rt.Observe(ctx, serviceID, envID, targetName)
	if err != nil {
		s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "failed", err)
		return nil, fmt.Errorf("observing runtime target %q after deploy: %w", targetName, err)
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
	actionData := map[string]any{"artifact_id": artifact.ID, "desired_hash": targetSpec.DesiredHash, "environment_revision": plan.RevisionHash}
	if applyResult != nil {
		actionData["renderer"] = applyResult.Renderer
		actionData["resource_names"] = applyResult.ResourceNames
		actionData["warnings"] = applyResult.Warnings
	}
	s.publishAction(ctx, runtimeActionDeployEvent, svc, env, obs, actionData)
	s.logRuntimeAction("deploy", svc, env, serviceID, envID, &artifact.ID, start, "success", nil)
	return obs, nil
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

func (s *RuntimeLifecycleService) mergeEffectiveSecrets(ctx context.Context, serviceID, envID uuid.UUID, opts *runtime.DeployOptions) error {
	secrets, err := s.effectiveSecrets(ctx, serviceID, envID)
	if err != nil {
		return fmt.Errorf("loading effective secrets: %w", err)
	}
	if len(secrets) == 0 {
		return nil
	}
	if s.secretEncryptor == nil {
		return fmt.Errorf("effective secrets are configured but secret encryption is unavailable")
	}
	if opts.Environment == nil {
		opts.Environment = map[string]string{}
	}
	for _, secret := range secrets {
		if secret.Name == "" {
			continue
		}
		value, err := s.secretEncryptor.Decrypt(secret.EncryptedValue, secret.EncryptionMethod)
		if err != nil {
			return fmt.Errorf("decrypting effective secret %q: %w", secret.Name, err)
		}
		opts.Environment[secret.Name] = value
	}
	return nil
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
