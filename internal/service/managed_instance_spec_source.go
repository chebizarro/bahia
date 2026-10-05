package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

// LocalSupervisionSpecSource resolves the supervised set exclusively from
// relay-derived service, environment and desired-runtime cp-state records.
type LocalSupervisionSpecSource struct {
	Configured      []SupervisionSpec
	State           LocalSupervisionState
	Resolver        runtime.RuntimeResolver
	Policy          domain.RecoveryPolicy
	MemoryThreshold float64
}

func (s *LocalSupervisionSpecSource) SupervisionSpecs(ctx context.Context) ([]SupervisionSpec, error) {
	result := append([]SupervisionSpec(nil), s.Configured...)
	if s.Resolver == nil {
		return result, nil
	}
	serviceEvents, err := s.State.records(kinds.CPStateTopicServiceRegistry)
	if err != nil {
		return nil, err
	}
	environmentEvents, err := s.State.records(kinds.CPStateTopicEnvironmentRegistry)
	if err != nil {
		return nil, err
	}
	stateEvents, err := s.State.records(kinds.CPStateTopicServiceState)
	if err != nil {
		return nil, err
	}
	services := make(map[uuid.UUID]*domain.Service)
	for _, event := range serviceEvents {
		var service domain.Service
		if localStateContent(event, &service) && service.ID != uuid.Nil {
			services[service.ID] = &service
		}
	}
	type environmentRecord struct {
		domain.Environment
		DeploymentUnits []domain.DeploymentUnit `json:"deployment_units"`
	}
	environments := make(map[uuid.UUID]*environmentRecord)
	for _, event := range environmentEvents {
		var record environmentRecord
		if localStateContent(event, &record) && record.ID != uuid.Nil {
			environments[record.ID] = &record
		}
	}
	seen := make(map[string]struct{}, len(result))
	for _, spec := range result {
		seen[instanceKeyString(spec.Key)] = struct{}{}
	}
	for _, event := range stateEvents {
		var state domain.EnvironmentServiceState
		if !localStateContent(event, &state) || (state.DesiredArtifactID == nil && state.DesiredRuntimeState == nil) {
			continue
		}
		svc, env := services[state.ServiceID], environments[state.EnvironmentID]
		if svc == nil || env == nil {
			continue
		}
		domain.NormalizeEnvironmentTargeting(&env.Environment)
		var unit *domain.DeploymentUnit
		for i := range env.DeploymentUnits {
			candidate := &env.DeploymentUnits[i]
			if state.DeploymentUnitID != nil && candidate.ID == *state.DeploymentUnitID || state.DeploymentUnitID == nil && candidate.Key == env.Targeting.DefaultUnitKey {
				unit = candidate
				break
			}
		}
		if unit != nil && unit.Implicit {
			unit, err = domain.NewImplicitDefaultDeploymentUnit(&env.Environment)
			if err != nil {
				return nil, err
			}
		}
		if unit == nil && state.DeploymentUnitID == nil && len(env.DeploymentUnits) == 0 {
			unit, err = domain.NewImplicitDefaultDeploymentUnit(&env.Environment)
			if err != nil {
				return nil, err
			}
		}
		if unit == nil || unit.OwnershipMode != domain.OwnershipModeBahiaManaged || (!unit.Implicit && unit.ID == uuid.Nil) {
			continue
		}
		unit.EnvironmentID = env.ID
		var adapter runtime.Runtime
		if explicit, ok := s.Resolver.(runtime.DeploymentUnitRuntimeResolver); ok {
			adapter, err = explicit.ResolveDeploymentUnit(svc, &env.Environment, unit)
		} else {
			adapter, err = s.Resolver.Resolve(svc, &env.Environment)
		}
		if err != nil {
			return nil, err
		}
		observer, observerOK := adapter.(runtime.HealthObserver)
		controller, controllerOK := adapter.(runtime.ManagedInstanceController)
		if !observerOK || !controllerOK {
			continue
		}
		key := domain.ManagedInstanceKey{ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: unit.ID, RuntimeTargetName: svc.RuntimeTargetName()}
		if _, exists := seen[instanceKeyString(key)]; exists {
			continue
		}
		supervisor, ok := supervisorForRuntime(adapter.Type())
		if !ok {
			continue
		}
		result = append(result, SupervisionSpec{Key: key, Host: strings.TrimSpace(env.Name), SupervisorType: supervisor, RecoveryPolicy: s.Policy, DesiredRunning: desiredRuntimeStateRunning(state), Observer: observer, Controller: controller, MemoryThresholdRatio: s.MemoryThreshold})
		seen[instanceKeyString(key)] = struct{}{}
	}
	return result, nil
}

// RepositorySupervisionSpecSource adds Bahia-managed desired deployment units to configured specs.
type RepositorySupervisionSpecSource struct {
	Configured      []SupervisionSpec
	States          repository.EnvironmentServiceStateRepository
	Services        repository.ServiceRepository
	Environments    repository.EnvironmentRepository
	Units           repository.DeploymentUnitRepository
	Resolver        runtime.RuntimeResolver
	Policy          domain.RecoveryPolicy
	MemoryThreshold float64
}

func (s *RepositorySupervisionSpecSource) SupervisionSpecs(ctx context.Context) ([]SupervisionSpec, error) {
	result := append([]SupervisionSpec(nil), s.Configured...)
	if s.States == nil || s.Services == nil || s.Environments == nil || s.Units == nil || s.Resolver == nil {
		return result, nil
	}
	states, err := s.States.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, spec := range result {
		seen[instanceKeyString(spec.Key)] = struct{}{}
	}
	for _, state := range states {
		if state.DesiredArtifactID == nil && state.DesiredRuntimeState == nil {
			continue
		}
		svc, err := s.Services.GetByID(ctx, state.ServiceID)
		if err != nil {
			return nil, err
		}
		env, err := s.Environments.GetByID(ctx, state.EnvironmentID)
		if err != nil {
			return nil, err
		}
		var unit *domain.DeploymentUnit
		if state.DeploymentUnitID != nil {
			unit, err = s.Units.GetByID(ctx, *state.DeploymentUnitID)
		} else {
			unit, err = s.Units.ResolveDefault(ctx, env)
		}
		if err != nil {
			return nil, err
		}
		if unit == nil || unit.OwnershipMode != domain.OwnershipModeBahiaManaged || (!unit.Implicit && unit.ID == uuid.Nil) {
			continue
		}
		var adapter runtime.Runtime
		if explicit, ok := s.Resolver.(runtime.DeploymentUnitRuntimeResolver); ok {
			adapter, err = explicit.ResolveDeploymentUnit(svc, env, unit)
		} else {
			adapter, err = s.Resolver.Resolve(svc, env)
		}
		if err != nil {
			return nil, err
		}
		observer, observerOK := adapter.(runtime.HealthObserver)
		controller, controllerOK := adapter.(runtime.ManagedInstanceController)
		if !observerOK || !controllerOK {
			continue
		}
		key := domain.ManagedInstanceKey{ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: unit.ID, RuntimeTargetName: svc.RuntimeTargetName()}
		if _, exists := seen[instanceKeyString(key)]; exists {
			continue
		}
		supervisor, ok := supervisorForRuntime(adapter.Type())
		if !ok {
			continue
		}
		result = append(result, SupervisionSpec{Key: key, Host: strings.TrimSpace(env.Name), SupervisorType: supervisor, RecoveryPolicy: s.Policy, DesiredRunning: desiredRuntimeStateRunning(state), Observer: observer, Controller: controller, MemoryThresholdRatio: s.MemoryThreshold})
		seen[instanceKeyString(key)] = struct{}{}
	}
	return result, nil
}

func desiredRuntimeStateRunning(state domain.EnvironmentServiceState) bool {
	desired := state.DesiredRuntimeState
	if desired == nil || desired.ServiceID == uuid.Nil || desired.EnvironmentID == uuid.Nil || desired.ArtifactID == uuid.Nil || strings.TrimSpace(desired.StableServiceKey) == "" {
		return false
	}
	return desired.ServiceID == state.ServiceID && desired.EnvironmentID == state.EnvironmentID
}

func supervisorForRuntime(runtimeType domain.RuntimeType) (domain.InstanceSupervisorType, bool) {
	switch runtimeType {
	case domain.RuntimeTypeDocker:
		return domain.InstanceSupervisorDocker, true
	case domain.RuntimeTypeCompose:
		return domain.InstanceSupervisorCompose, true
	default:
		return "", false
	}
}

var _ SupervisionSpecSource = (*RepositorySupervisionSpecSource)(nil)
