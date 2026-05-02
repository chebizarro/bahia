package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	runtimeActionDeployEvent  = events.EventType("runtime.deploy")
	runtimeActionRestartEvent = events.EventType("runtime.restart")
	runtimeActionStopEvent    = events.EventType("runtime.stop")
)

// RuntimeLifecycleService performs direct runtime actions for services resolved to a runtime.
type RuntimeLifecycleService struct {
	registry     *RegistryService
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	artifacts    repository.ArtifactRepository
	state        repository.EnvironmentServiceStateRepository
	resolver     runtime.RuntimeResolver
	publisher    events.Publisher
	logger       *zap.Logger
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
) *RuntimeLifecycleService {
	if logger == nil {
		logger = zap.NewNop()
	}
	if publisher == nil {
		publisher = &events.NoopPublisher{}
	}
	return &RuntimeLifecycleService{
		registry:     registry,
		services:     services,
		environments: environments,
		artifacts:    artifacts,
		state:        state,
		resolver:     resolver,
		publisher:    publisher,
		logger:       logger,
	}
}

// Deploy deploys an artifact directly through the resolved runtime and records a fresh observation.
func (s *RuntimeLifecycleService) Deploy(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID) (*domain.RuntimeObservation, error) {
	svc, env, rt, err := s.resolve(ctx, serviceID, envID)
	if err != nil {
		return nil, err
	}

	artifact, err := s.resolveDeployArtifact(ctx, serviceID, envID, artifactID)
	if err != nil {
		return nil, err
	}

	if artifactID != nil {
		if err := s.updateDesiredArtifact(ctx, serviceID, envID, *artifactID); err != nil {
			return nil, err
		}
	}

	targetName := svc.RuntimeTargetName()
	opts := deployOptionsFromServiceRuntimeConfig(svc)
	if opts.Labels == nil {
		opts.Labels = map[string]string{}
	}
	opts.Labels["bahia.service"] = svc.Name
	opts.Labels["bahia.managed"] = "true"

	if err := rt.Deploy(ctx, targetName, imageRefForArtifact(artifact), opts); err != nil {
		return nil, fmt.Errorf("deploying runtime target %q: %w", targetName, err)
	}
	obs, err := rt.Observe(ctx, serviceID, envID, targetName)
	if err != nil {
		return nil, fmt.Errorf("observing runtime target %q after deploy: %w", targetName, err)
	}
	if err := s.registry.RecordObservation(ctx, obs); err != nil {
		return nil, fmt.Errorf("recording deploy observation: %w", err)
	}
	s.publishAction(ctx, runtimeActionDeployEvent, svc, env, obs, map[string]any{"artifact_id": artifact.ID})
	return obs, nil
}

// Restart restarts a service directly through the resolved runtime and records a fresh observation.
func (s *RuntimeLifecycleService) Restart(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	svc, env, rt, err := s.resolve(ctx, serviceID, envID)
	if err != nil {
		return nil, err
	}
	lifecycle, ok := rt.(runtime.LifecycleRuntime)
	if !ok {
		return nil, fmt.Errorf("runtime %s does not support restart", rt.Type())
	}
	targetName := svc.RuntimeTargetName()
	if err := lifecycle.Restart(ctx, targetName); err != nil {
		return nil, fmt.Errorf("restarting runtime target %q: %w", targetName, err)
	}
	obs, err := rt.Observe(ctx, serviceID, envID, targetName)
	if err != nil {
		return nil, fmt.Errorf("observing runtime target %q after restart: %w", targetName, err)
	}
	if err := s.registry.RecordObservation(ctx, obs); err != nil {
		return nil, fmt.Errorf("recording restart observation: %w", err)
	}
	s.publishAction(ctx, runtimeActionRestartEvent, svc, env, obs, nil)
	return obs, nil
}

// Stop stops a service directly through the resolved runtime and records a fresh observation.
func (s *RuntimeLifecycleService) Stop(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	svc, env, rt, err := s.resolve(ctx, serviceID, envID)
	if err != nil {
		return nil, err
	}
	lifecycle, ok := rt.(runtime.LifecycleRuntime)
	if !ok {
		return nil, fmt.Errorf("runtime %s does not support stop", rt.Type())
	}
	targetName := svc.RuntimeTargetName()
	if err := lifecycle.Stop(ctx, targetName); err != nil {
		return nil, fmt.Errorf("stopping runtime target %q: %w", targetName, err)
	}
	obs, err := rt.Observe(ctx, serviceID, envID, targetName)
	if err != nil {
		return nil, fmt.Errorf("observing runtime target %q after stop: %w", targetName, err)
	}
	if err := s.registry.RecordObservation(ctx, obs); err != nil {
		return nil, fmt.Errorf("recording stop observation: %w", err)
	}
	s.publishAction(ctx, runtimeActionStopEvent, svc, env, obs, nil)
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
	rt, err := s.resolver.Resolve(svc, env)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolving runtime: %w", err)
	}
	return svc, env, rt, nil
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

func (s *RuntimeLifecycleService) updateDesiredArtifact(ctx context.Context, serviceID, envID, artifactID uuid.UUID) error {
	st, err := s.state.Get(ctx, serviceID, envID)
	if err != nil {
		return fmt.Errorf("looking up current state: %w", err)
	}
	if st == nil {
		st = &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID}
	}
	if st.DesiredArtifactID != nil && *st.DesiredArtifactID == artifactID {
		return nil
	}
	st.DesiredArtifactID = &artifactID
	st.DriftStatus = domain.DriftStatusDeploying
	return s.state.Upsert(ctx, st)
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
