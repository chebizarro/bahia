package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// LocalSupervisionSpecSource adds Bahia-managed desired deployment units to
// the configured specs. The desired set is the service-state records in the
// local event store, joined with the service and environment registry records
// (an environment record carries its deployment units). A replacement or
// tombstone received from a relay changes the next evaluation's set.
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
	stateRecords, err := s.State.family(ctx, kinds.CPStateTopicServiceState)
	if err != nil {
		return nil, err
	}
	serviceRecords, err := s.State.family(ctx, kinds.CPStateTopicServiceRegistry)
	if err != nil {
		return nil, err
	}
	environmentRecords, err := s.State.family(ctx, kinds.CPStateTopicEnvironmentRegistry)
	if err != nil {
		return nil, err
	}
	services := make(map[uuid.UUID]*domain.Service, len(serviceRecords))
	for _, record := range serviceRecords {
		if svc, ok := decodeServiceRecord(record); ok {
			services[svc.ID] = svc
		}
	}
	environments := make(map[uuid.UUID]*localEnvironment, len(environmentRecords))
	for _, record := range environmentRecords {
		if env, ok := decodeEnvironmentRecord(record); ok {
			environments[env.Environment.ID] = env
		}
	}
	seen := map[string]struct{}{}
	for _, spec := range result {
		seen[instanceKeyString(spec.Key)] = struct{}{}
	}
	for _, record := range stateRecords {
		state, ok := decodeServiceStateRecord(record)
		if !ok || (state.DesiredArtifactID == nil && state.DesiredRuntimeState == nil) {
			continue
		}
		// A state whose service or environment record has not arrived cannot
		// be resolved to a runtime target yet; it joins the set once it has.
		svc, env := services[state.ServiceID], environments[state.EnvironmentID]
		if svc == nil || env == nil {
			continue
		}
		unit := env.unit(state.DeploymentUnitID)
		if unit == nil || unit.OwnershipMode != domain.OwnershipModeBahiaManaged || (!unit.Implicit && unit.ID == uuid.Nil) {
			continue
		}
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
		key := domain.ManagedInstanceKey{ServiceID: svc.ID, EnvironmentID: env.Environment.ID, DeploymentUnitID: unit.ID, RuntimeTargetName: svc.RuntimeTargetName()}
		if _, exists := seen[instanceKeyString(key)]; exists {
			continue
		}
		supervisor, ok := supervisorForRuntime(adapter.Type())
		if !ok {
			continue
		}
		result = append(result, SupervisionSpec{Key: key, Host: strings.TrimSpace(env.Environment.Name), SupervisorType: supervisor, RecoveryPolicy: s.Policy, DesiredRunning: desiredRuntimeStateRunning(state), Observer: observer, Controller: controller, MemoryThresholdRatio: s.MemoryThreshold})
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

var _ SupervisionSpecSource = (*LocalSupervisionSpecSource)(nil)
