package service

import (
	"context"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

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
		if unit == nil || unit.ID == uuid.Nil || unit.OwnershipMode != domain.OwnershipModeBahiaManaged {
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
		unitID := uuid.Nil
		if !unit.Implicit {
			unitID = unit.ID
		}
		key := domain.ManagedInstanceKey{ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: unitID, RuntimeTargetName: svc.RuntimeTargetName()}
		if _, exists := seen[instanceKeyString(key)]; exists {
			continue
		}
		supervisor, ok := supervisorForRuntime(adapter.Type())
		if !ok {
			continue
		}
		result = append(result, SupervisionSpec{Key: key, Host: firstManagedEvidence(unit.EndpointRef, env.Name), SupervisorType: supervisor, RecoveryPolicy: s.Policy, DesiredRunning: true, Observer: observer, Controller: controller, MemoryThresholdRatio: s.MemoryThreshold})
		seen[instanceKeyString(key)] = struct{}{}
	}
	return result, nil
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
