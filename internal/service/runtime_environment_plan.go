package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Narrow dependency interfaces — keep the assembler testable without pulling
// in concrete repository implementations.
// ---------------------------------------------------------------------------

// envServiceStateLoader loads environment-service-state rows.
type envServiceStateLoader interface {
	ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error)
}

// envServiceStateWriter persists environment-service-state rows (used for
// opportunistic hydration writes).
type envServiceStateWriter interface {
	Upsert(ctx context.Context, state *domain.EnvironmentServiceState) error
}

// serviceLoader loads service records by ID.
type serviceLoader interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Service, error)
}

// artifactLoader loads artifact records by ID.
type artifactLoader interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
}

// secretLister loads effective secrets for a service+environment pair.
type secretLister interface {
	ListEffective(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error)
}

// deploymentUnitLister loads explicit deployment units for an environment.
type deploymentUnitLister interface {
	ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.DeploymentUnit, error)
}

// ---------------------------------------------------------------------------
// EnvironmentPlanAssembler
// ---------------------------------------------------------------------------

// EnvironmentPlanAssembler loads all managed services in an environment and
// produces a DesiredEnvironmentPlan. It handles three service categories:
//
//  1. Target service — the service being actively deployed; its spec is
//     provided by the caller (freshly built).
//  2. Siblings with stored desired state — their DesiredRuntimeState is
//     loaded directly from the EnvironmentServiceState row.
//  3. Legacy siblings — siblings that predate the desired-state model and
//     have no stored DesiredRuntimeState. These are hydrated by rebuilding
//     the spec from service/artifact/runtime config via DesiredStateBuilder,
//     then persisted opportunistically so future renders find them.
//
// Deleted or tombstoned services (whose Service record no longer exists in
// the repository) are silently excluded from the plan.
type EnvironmentPlanAssembler struct {
	stateLoader envServiceStateLoader
	stateWriter envServiceStateWriter
	services    serviceLoader
	artifacts   artifactLoader
	secrets     secretLister
	units       deploymentUnitLister
	builder     *DesiredStateBuilder
	logger      *slog.Logger
}

// EnvironmentPlanAssemblerDeps groups the dependencies needed to construct an
// EnvironmentPlanAssembler. Callers in app.go can populate this from their
// concrete repositories.
type EnvironmentPlanAssemblerDeps struct {
	StateLoader envServiceStateLoader
	StateWriter envServiceStateWriter
	Services    serviceLoader
	Artifacts   artifactLoader
	Secrets     secretLister
	Units       deploymentUnitLister
	Builder     *DesiredStateBuilder
	Logger      *slog.Logger
}

// NewEnvironmentPlanAssembler creates a new assembler.
func NewEnvironmentPlanAssembler(deps EnvironmentPlanAssemblerDeps) *EnvironmentPlanAssembler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &EnvironmentPlanAssembler{
		stateLoader: deps.StateLoader,
		stateWriter: deps.StateWriter,
		services:    deps.Services,
		artifacts:   deps.Artifacts,
		secrets:     deps.Secrets,
		units:       deps.Units,
		builder:     deps.Builder,
		logger:      logger,
	}
}

// Assemble builds a DesiredEnvironmentPlan for the given environment.
//
// targetServiceID identifies the service being actively deployed; targetSpec
// is its freshly built DesiredServiceSpec (constructed by the caller before
// assembly). All other active services in the environment are treated as
// siblings and included in the plan from their stored or hydrated state.
//
// The returned plan has services sorted deterministically by StableServiceKey
// and includes a computed environment revision hash.
func (a *EnvironmentPlanAssembler) Assemble(
	ctx context.Context,
	envID uuid.UUID,
	targetServiceID uuid.UUID,
	targetSpec *domain.DesiredServiceSpec,
) (*domain.DesiredEnvironmentPlan, error) {
	if targetSpec == nil {
		return nil, fmt.Errorf("targetSpec is required")
	}

	// 1. Load all active environment-service-state rows for this environment.
	states, err := a.stateLoader.ListByEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("loading environment service states: %w", err)
	}

	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
	}

	unitIndex, err := a.loadDeploymentUnitIndex(ctx, envID)
	if err != nil {
		return nil, err
	}

	// Track whether we've seen the target service in the state rows.
	targetSeen := false

	for _, state := range states {
		// Is this the target service?
		if state.ServiceID == targetServiceID {
			targetSeen = true
			spec := *targetSpec
			if err := a.applyUnitIdentity(&spec, state.DeploymentUnitID, unitIndex); err != nil {
				return nil, fmt.Errorf("resolving target deployment unit: %w", err)
			}
			plan.Services = append(plan.Services, spec)
			continue
		}

		// Sibling service — check it still exists (not deleted/tombstoned).
		svc, err := a.services.GetByID(ctx, state.ServiceID)
		if err != nil {
			return nil, fmt.Errorf("loading service %s: %w", state.ServiceID, err)
		}
		if svc == nil {
			// Service has been deleted; drop from plan.
			a.logger.Info("dropping deleted service from environment plan",
				"service_id", state.ServiceID,
				"environment_id", envID,
			)
			continue
		}

		// Prefer stored desired state when available.
		if state.DesiredRuntimeState != nil {
			spec := *state.DesiredRuntimeState
			if err := a.applyUnitIdentity(&spec, state.DeploymentUnitID, unitIndex); err != nil {
				return nil, fmt.Errorf("resolving deployment unit for service %s: %w", state.ServiceID, err)
			}
			plan.Services = append(plan.Services, spec)
			continue
		}

		// Legacy sibling — hydrate from service/artifact/runtime config.
		spec, err := a.hydrateLegacySibling(ctx, svc, &state)
		if err != nil {
			a.logger.Warn("failed to hydrate legacy sibling; excluding from plan",
				"service_id", state.ServiceID,
				"environment_id", envID,
				"error", err,
			)
			continue
		}

		if err := a.applyUnitIdentity(spec, state.DeploymentUnitID, unitIndex); err != nil {
			return nil, fmt.Errorf("resolving deployment unit for legacy sibling %s: %w", state.ServiceID, err)
		}
		plan.Services = append(plan.Services, *spec)

		// Persist hydrated spec opportunistically so future renders have it.
		a.persistHydratedSpec(ctx, &state, spec)
	}

	// If the target service wasn't already tracked in environment state,
	// include it anyway (first deploy to this environment).
	if !targetSeen {
		spec := *targetSpec
		if err := a.applyUnitIdentity(&spec, spec.DeploymentUnitID, unitIndex); err != nil {
			return nil, fmt.Errorf("resolving target deployment unit: %w", err)
		}
		plan.Services = append(plan.Services, spec)
	}

	if err := validateComposeDependenciesStayWithinUnit(plan.Services); err != nil {
		return nil, err
	}

	// Sort deterministically, group by deployment unit, and compute unit + environment revision hashes.
	plan.ComputeRevisionHash()

	return plan, nil
}

// hydrateLegacySibling reconstructs a DesiredServiceSpec for a sibling that
// has no stored desired state by loading its service config, latest artifact,
// and secrets, then running them through the DesiredStateBuilder.
type deploymentUnitIndex struct {
	byID map[uuid.UUID]domain.DeploymentUnit
}

func (a *EnvironmentPlanAssembler) loadDeploymentUnitIndex(ctx context.Context, envID uuid.UUID) (*deploymentUnitIndex, error) {
	idx := &deploymentUnitIndex{byID: make(map[uuid.UUID]domain.DeploymentUnit)}
	if a.units == nil {
		return idx, nil
	}
	units, err := a.units.ListByEnvironment(ctx, envID)
	if err != nil {
		return nil, fmt.Errorf("loading deployment units: %w", err)
	}
	for _, unit := range units {
		idx.byID[unit.ID] = unit
	}
	return idx, nil
}

func (a *EnvironmentPlanAssembler) applyUnitIdentity(spec *domain.DesiredServiceSpec, unitID *uuid.UUID, idx *deploymentUnitIndex) error {
	var unit *domain.DeploymentUnit
	if unitID != nil && *unitID != uuid.Nil {
		resolved, ok := idx.byID[*unitID]
		if !ok {
			return fmt.Errorf("deployment unit %s not found in environment", *unitID)
		}
		unit = &resolved
	}
	if unit != nil {
		domain.NormalizeDesiredServiceUnitIdentity(spec, &unit.ID, unit.Key, unit.RuntimeType)
	} else {
		domain.NormalizeDesiredServiceUnitIdentity(spec, nil, "", "")
	}
	spec.Labels = copyStringMap(spec.Labels)
	if spec.Labels == nil {
		spec.Labels = map[string]string{}
	}
	spec.Labels["bahia.deployment_unit_key"] = spec.DeploymentUnitKey
	if spec.DeploymentUnitID != nil {
		spec.Labels["bahia.deployment_unit_id"] = spec.DeploymentUnitID.String()
	} else {
		delete(spec.Labels, "bahia.deployment_unit_id")
	}
	spec.ComputeDesiredHash()
	spec.Labels["bahia.desired_hash"] = spec.DesiredHash
	return nil
}

func validateComposeDependenciesStayWithinUnit(services []domain.DesiredServiceSpec) error {
	unitsByServiceKey := make(map[string]string, len(services))
	for _, svc := range services {
		unitsByServiceKey[svc.StableServiceKey] = svc.DeploymentUnitKey
	}
	for _, svc := range services {
		if svc.ComposeExtension == nil {
			continue
		}
		for _, depKey := range svc.DependsOn {
			if err := validateDependencyUnit(svc, depKey, unitsByServiceKey); err != nil {
				return err
			}
		}
		for depKey := range svc.ComposeExtension.DependsOn {
			if err := validateDependencyUnit(svc, depKey, unitsByServiceKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateDependencyUnit(svc domain.DesiredServiceSpec, depKey string, unitsByServiceKey map[string]string) error {
	depUnit, ok := unitsByServiceKey[depKey]
	if !ok {
		return nil
	}
	if depUnit != svc.DeploymentUnitKey {
		return fmt.Errorf("compose dependency %q for service %q crosses deployment units (%s -> %s)", depKey, svc.StableServiceKey, svc.DeploymentUnitKey, depUnit)
	}
	return nil
}

func (a *EnvironmentPlanAssembler) hydrateLegacySibling(
	ctx context.Context,
	svc *domain.Service,
	state *domain.EnvironmentServiceState,
) (*domain.DesiredServiceSpec, error) {
	// Resolve artifact — prefer the desired artifact from state, fall back
	// to nil which the builder will reject.
	var artifact *domain.Artifact
	if state.DesiredArtifactID != nil {
		var err error
		artifact, err = a.artifacts.GetByID(ctx, *state.DesiredArtifactID)
		if err != nil {
			return nil, fmt.Errorf("loading artifact %s: %w", *state.DesiredArtifactID, err)
		}
	}
	if artifact == nil {
		return nil, fmt.Errorf("no artifact available for legacy sibling %s", svc.ID)
	}

	// Load effective secrets for this service+environment.
	secrets, err := a.secrets.ListEffective(ctx, svc.ID, state.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("loading secrets for %s: %w", svc.ID, err)
	}

	// Build the environment object for the builder. We only need the ID;
	// the builder uses it for labeling.
	env := &domain.Environment{ID: state.EnvironmentID}

	input := BuildInput{
		Service:       svc,
		Environment:   env,
		Artifact:      artifact,
		RuntimeConfig: svc.RuntimeConfig,
		Secrets:       secrets,
	}

	spec, err := a.builder.Build(input)
	if err != nil {
		return nil, fmt.Errorf("building spec for legacy sibling %s: %w", svc.ID, err)
	}

	return spec, nil
}

// persistHydratedSpec writes a hydrated spec back to the environment service
// state row. This is best-effort; failures are logged but do not break assembly.
func (a *EnvironmentPlanAssembler) persistHydratedSpec(
	ctx context.Context,
	state *domain.EnvironmentServiceState,
	spec *domain.DesiredServiceSpec,
) {
	state.DesiredRuntimeState = spec
	state.DesiredHash = spec.DesiredHash

	if err := a.stateWriter.Upsert(ctx, state); err != nil {
		a.logger.Warn("failed to persist hydrated legacy sibling spec",
			"service_id", state.ServiceID,
			"environment_id", state.EnvironmentID,
			"error", err,
		)
	} else {
		a.logger.Info("persisted hydrated legacy sibling spec",
			"service_id", state.ServiceID,
			"environment_id", state.EnvironmentID,
			"desired_hash", spec.DesiredHash,
		)
	}
}
