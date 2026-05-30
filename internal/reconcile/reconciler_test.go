package reconcile

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	runtimeService "github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type mockObservationRepo struct {
	observations []domain.RuntimeObservation
}

func (m *mockObservationRepo) Create(_ context.Context, obs *domain.RuntimeObservation) error {
	if obs.ID == uuid.Nil {
		obs.ID = uuid.New()
	}
	m.observations = append(m.observations, *obs)
	return nil
}

func (m *mockObservationRepo) GetLatest(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	for i := len(m.observations) - 1; i >= 0; i-- {
		obs := m.observations[i]
		if obs.ServiceID == serviceID && obs.EnvironmentID == envID {
			return &obs, nil
		}
	}
	return nil, nil
}

func (m *mockObservationRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit int) ([]domain.RuntimeObservation, error) {
	var out []domain.RuntimeObservation
	for _, obs := range m.observations {
		if obs.ServiceID == serviceID && obs.EnvironmentID == envID {
			out = append(out, obs)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

type mockDeploymentUnitRepo struct {
	units map[uuid.UUID]*domain.DeploymentUnit
}

func (m *mockDeploymentUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	m.units[unit.ID] = unit
	return nil
}

func (m *mockDeploymentUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	return m.units[id], nil
}

func (m *mockDeploymentUnitRepo) GetByEnvironmentKey(_ context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	for _, unit := range m.units {
		if unit.EnvironmentID == environmentID && unit.Key == key {
			return unit, nil
		}
	}
	return nil, nil
}

func (m *mockDeploymentUnitRepo) ListByEnvironment(_ context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	var out []domain.DeploymentUnit
	for _, unit := range m.units {
		if unit.EnvironmentID == environmentID {
			out = append(out, *unit)
		}
	}
	return out, nil
}

func (m *mockDeploymentUnitRepo) ResolveDefault(_ context.Context, env *domain.Environment) (*domain.DeploymentUnit, error) {
	return domain.NewImplicitDefaultDeploymentUnit(env)
}

type mockRuntimeResolver struct {
	rt runtime.Runtime
}

func (m mockRuntimeResolver) Resolve(_ *domain.Service, _ *domain.Environment) (runtime.Runtime, error) {
	return m.rt, nil
}

func TestReconcilerObserveOnlyRecordsDriftWithoutRemediation(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredArtifactID: &artifactID},
	}}
	obsRepo := &mockObservationRepo{}
	rt := &mockRuntime{observeDigest: "sha256:observed"}
	pub := &mockPublisher{}

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: "sha256:desired"}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		obsRepo,
		stateRepo,
		mockRuntimeResolver{rt: rt},
		pub,
		time.Minute,
		zap.NewNop(),
	)

	reconciler.reconcileAll(ctx)

	require.Len(t, obsRepo.observations, 1)
	updated := stateRepo.states[stateKey]
	require.Equal(t, domain.DriftStatusDrifted, updated.DriftStatus)
	require.NotNil(t, updated.CurrentObservationID)
	require.NotNil(t, updated.LastReconciledAt)

	rt.mu.Lock()
	require.Empty(t, rt.deployed)
	require.Empty(t, rt.undeployed)
	rt.mu.Unlock()
}

type mockAutoRemediationDeployer struct {
	calls int
	err   error
}

func (m *mockAutoRemediationDeployer) AutoRemediateDesiredState(_ context.Context, _, _ uuid.UUID, _ runtimeService.DeployStatusCallback) (*domain.RuntimeObservation, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &domain.RuntimeObservation{ID: uuid.New()}, nil
}

func TestReconcilerApprovalRequiredMarksRemediationNeededWithoutInternalDeploy(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	deployer := &mockAutoRemediationDeployer{}

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredArtifactID: &artifactID},
	}}
	obsRepo := &mockObservationRepo{}

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeApprovalRequired}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: "sha256:desired"}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		obsRepo,
		stateRepo,
		mockRuntimeResolver{rt: &mockRuntime{observeDigest: "sha256:observed"}},
		&mockPublisher{},
		time.Minute,
		zap.NewNop(),
		WithAutoRemediationDeployer(deployer),
	)

	reconciler.reconcileAll(ctx)

	require.Len(t, obsRepo.observations, 1)
	require.Equal(t, domain.DriftStatusRemediationNeeded, stateRepo.states[stateKey].DriftStatus)
	require.Zero(t, deployer.calls)
}

func TestReconcilerAutoApplyUsesSharedDesiredStateHelper(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	deployer := &mockAutoRemediationDeployer{}

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredArtifactID: &artifactID},
	}}

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeAutoApply}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: "sha256:desired"}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{},
		stateRepo,
		mockRuntimeResolver{rt: &mockRuntime{observeDigest: "sha256:observed"}},
		&mockPublisher{},
		time.Minute,
		zap.NewNop(),
		WithAutoRemediationDeployer(deployer),
	)

	reconciler.reconcileAll(ctx)

	require.Equal(t, 1, deployer.calls)
	require.Nil(t, stateRepo.states[stateKey].ReconcileFailureMetadata)
	require.Nil(t, stateRepo.states[stateKey].ReconcileBackoffUntil)
}

func TestReconcilerAutoApplyFailureKeepsDesiredStateAndBacksOff(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	deployer := &mockAutoRemediationDeployer{err: fmt.Errorf("apply failed")}

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredArtifactID: &artifactID, DesiredHash: "sha256:desired-state"},
	}}

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeAutoApply}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: "sha256:desired"}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{},
		stateRepo,
		mockRuntimeResolver{rt: &mockRuntime{observeNormHash: "sha256:observed-state"}},
		&mockPublisher{},
		time.Minute,
		zap.NewNop(),
		WithAutoRemediationDeployer(deployer),
	)

	reconciler.reconcileAll(ctx)

	updated := stateRepo.states[stateKey]
	require.Equal(t, &artifactID, updated.DesiredArtifactID)
	require.Equal(t, "sha256:desired-state", updated.DesiredHash)
	require.NotNil(t, updated.ReconcileBackoffUntil)
	require.Equal(t, 1, updated.ReconcileConsecutiveFailures)
	require.Equal(t, "auto_apply_failed", updated.ReconcileFailureMetadata["reason"])
}

func TestReconcilerSkipsDisabledDeploymentUnit(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	unitID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID, DesiredArtifactID: &artifactID},
	}}
	obsRepo := &mockObservationRepo{}

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: "sha256:desired"}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unitID: {ID: unitID, EnvironmentID: envID, Key: "blue", RuntimeType: domain.RuntimeTypeDocker, ReconcileMode: domain.ReconcileModeDisabled, OwnershipMode: domain.OwnershipModeBahiaManaged}}},
		obsRepo,
		stateRepo,
		mockRuntimeResolver{rt: &mockRuntime{observeDigest: "sha256:observed"}},
		&mockPublisher{},
		time.Minute,
		zap.NewNop(),
	)

	reconciler.reconcileAll(ctx)

	require.Empty(t, obsRepo.observations)
	require.Nil(t, stateRepo.states[stateKey].LastReconciledAt)
}
