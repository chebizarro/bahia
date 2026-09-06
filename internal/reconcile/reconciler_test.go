package reconcile

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	runtimeService "github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

var (
	reconcileDesiredDigest  = "sha256:" + strings.Repeat("a", 64)
	reconcileObservedDigest = "sha256:" + strings.Repeat("b", 64)
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
	err   error
}

func (m *mockDeploymentUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	m.units[unit.ID] = unit
	return nil
}

func (m *mockDeploymentUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	if m.err != nil {
		return nil, m.err
	}
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
	rt                  runtime.Runtime
	deploymentUnitRT    runtime.Runtime
	legacyErr           error
	deploymentUnitErr   error
	legacyCalls         int
	deploymentUnitCalls int
	resolvedUnit        *domain.DeploymentUnit
}

func (m *mockRuntimeResolver) Resolve(_ *domain.Service, _ *domain.Environment) (runtime.Runtime, error) {
	m.legacyCalls++
	return m.rt, m.legacyErr
}

func (m *mockRuntimeResolver) ResolveDeploymentUnit(_ *domain.Service, _ *domain.Environment, unit *domain.DeploymentUnit) (runtime.Runtime, error) {
	m.deploymentUnitCalls++
	m.resolvedUnit = unit
	if m.deploymentUnitRT != nil || m.deploymentUnitErr != nil {
		return m.deploymentUnitRT, m.deploymentUnitErr
	}
	return m.rt, nil
}

type reconcilerRunRepo struct {
	runs []domain.DeploymentRun
}

func (r *reconcilerRunRepo) Create(_ context.Context, run *domain.DeploymentRun) error {
	r.runs = append(r.runs, *run)
	return nil
}
func (r *reconcilerRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	for i := range r.runs {
		if r.runs[i].ID == id {
			return &r.runs[i], nil
		}
	}
	return nil, nil
}
func (r *reconcilerRunRepo) ListByIntent(_ context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	var runs []domain.DeploymentRun
	for _, run := range r.runs {
		if run.DeploymentIntentID == intentID {
			runs = append(runs, run)
		}
	}
	return runs, nil
}
func (r *reconcilerRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	for i := range r.runs {
		if r.runs[i].ID == id {
			r.runs[i].Status = status
			r.runs[i].ExitCode = exitCode
		}
	}
	return nil
}

func TestReconcilerRepairsStuckSuccessfulRouteOnlyState(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	intentID := uuid.New()
	observationID := uuid.New()
	lastRunID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {
			ServiceID: serviceID, EnvironmentID: envID, DesiredIntentID: &intentID,
			CurrentObservationID: &observationID, LastSuccessfulRunID: &lastRunID,
			DriftStatus:                  domain.DriftStatusDeploying,
			ReconcileFailureMetadata:     map[string]any{"reason": "transient"},
			ReconcileConsecutiveFailures: 2,
		},
	}}
	intentRepo := &mockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		intentID: {ID: intentID, ServiceID: serviceID, EnvironmentID: envID, Status: domain.IntentStatusDeployed},
	}}
	now := time.Now().UTC()
	run := domain.DeploymentRun{
		ID: uuid.New(), DeploymentIntentID: intentID,
		LoomJobID: domain.RouteOnlyDeploymentRunLoomJobID, Status: domain.RunStatusSucceeded,
		CreatedAt: now, UpdatedAt: now,
	}
	runRepo := &reconcilerRunRepo{runs: []domain.DeploymentRun{run}}
	publisher := &mockPublisher{}
	core, logs := observer.New(zap.InfoLevel)
	reconciler := NewReconciler(nil, nil, nil, nil, &mockObservationRepo{}, stateRepo, nil, publisher, time.Minute, zap.New(core), WithDeploymentHistory(intentRepo, runRepo))

	require.NoError(t, reconciler.reconcileOne(ctx, stateRepo.states[stateKey]))
	updated := stateRepo.states[stateKey]
	require.Equal(t, domain.DriftStatusInSync, updated.DriftStatus)
	require.Equal(t, observationID, *updated.CurrentObservationID)
	require.Equal(t, lastRunID, *updated.LastSuccessfulRunID)
	require.Equal(t, 2, updated.ReconcileConsecutiveFailures)
	require.Equal(t, "transient", updated.ReconcileFailureMetadata["reason"])
	require.Equal(t, 1, logs.FilterMessage("repaired route-only service state stuck in deploying").Len())
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	stateEvents := 0
	for _, event := range publisher.events {
		if event.Type == events.EventEnvironmentServiceStateChanged {
			stateEvents++
		}
	}
	require.Equal(t, 1, stateEvents)
}

func TestReconcilerRouteCarryingDesiredAcceptsPreRouteHashAcrossPasses(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	desired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion,
		ServiceID:     serviceID, EnvironmentID: envID, ArtifactID: uuid.New(), StableServiceKey: "api",
	}
	preRouteHash := desired.ComputeDesiredHash()
	desired.PublicRoute = &domain.DesiredPublicRoutePlan{Hostname: "api.example.com"}
	desired.ComputeDesiredHash()
	stateKey := stateMapKey(serviceID, envID)
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {
			ServiceID: serviceID, EnvironmentID: envID,
			DesiredRuntimeState: desired, DesiredHash: desired.DesiredHash,
			DriftStatus: domain.DriftStatusInSync,
		},
	}}
	publisher := &mockPublisher{}
	resolver := &mockRuntimeResolver{rt: &mockRuntime{observeNormHash: preRouteHash}}
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeAutoApply}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{}, stateRepo, resolver, publisher, time.Minute, zap.NewNop(),
	)

	for pass := 0; pass < 2; pass++ {
		require.NoError(t, reconciler.reconcileOne(ctx, stateRepo.states[stateKey]))
		require.Equal(t, domain.DriftStatusInSync, stateRepo.states[stateKey].DriftStatus)
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	for _, event := range publisher.events {
		require.NotEqual(t, events.EventDriftDetected, event.Type)
	}
}

func TestReconcilerKeepsOtherDeployingStatesSkipped(t *testing.T) {
	tests := []struct {
		name string
		runs []domain.DeploymentRun
	}{
		{
			name: "latest run is not route-only",
			runs: []domain.DeploymentRun{{LoomJobID: "runtime:direct", Status: domain.RunStatusSucceeded}},
		},
		{
			name: "latest route-only run did not succeed",
			runs: []domain.DeploymentRun{
				{LoomJobID: domain.RouteOnlyDeploymentRunLoomJobID, Status: domain.RunStatusSucceeded, CreatedAt: time.Now().Add(-time.Minute)},
				{LoomJobID: domain.RouteOnlyDeploymentRunLoomJobID, Status: domain.RunStatusFailed, CreatedAt: time.Now()},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceID := uuid.New()
			envID := uuid.New()
			intentID := uuid.New()
			stateKey := stateMapKey(serviceID, envID)
			state := &domain.EnvironmentServiceState{
				ServiceID: serviceID, EnvironmentID: envID, DesiredIntentID: &intentID,
				DriftStatus: domain.DriftStatusDeploying,
			}
			stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{stateKey: state}}
			intentRepo := &mockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
				intentID: {ID: intentID, ServiceID: serviceID, EnvironmentID: envID, Status: domain.IntentStatusDeployed},
			}}
			for i := range test.runs {
				test.runs[i].ID = uuid.New()
				test.runs[i].DeploymentIntentID = intentID
			}
			publisher := &mockPublisher{}
			reconciler := NewReconciler(nil, nil, nil, nil, &mockObservationRepo{}, stateRepo, nil, publisher, time.Minute, zap.NewNop(), WithDeploymentHistory(intentRepo, &reconcilerRunRepo{runs: test.runs}))

			require.NoError(t, reconciler.reconcileOne(context.Background(), state))
			require.Equal(t, domain.DriftStatusDeploying, stateRepo.states[stateKey].DriftStatus)
			require.Empty(t, publisher.events)
		})
	}
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
	rt := &mockRuntime{observeDigest: reconcileObservedDigest}
	pub := &mockPublisher{}

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		obsRepo,
		stateRepo,
		&mockRuntimeResolver{rt: rt},
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

func TestReconcilerDigestMatchStartingRemainsObservable(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredArtifactID: &artifactID, DesiredHash: "sha256:desired-state"},
	}}
	observations := &mockObservationRepo{}
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}}, observations, stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeDigest: reconcileDesiredDigest, observeHealth: domain.HealthStatusStarting}},
		&mockPublisher{}, time.Minute, zap.NewNop(),
	)
	for pass := 0; pass < 2; pass++ {
		if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
			t.Fatalf("pass %d reconcileOne() error = %v", pass, err)
		}
		if got := stateRepo.states[stateKey].DriftStatus; got != domain.DriftStatusInSync {
			t.Fatalf("pass %d drift status = %q, want in_sync", pass, got)
		}
	}
	if len(observations.observations) != 2 {
		t.Fatalf("observation count = %d, want 2", len(observations.observations))
	}
}

func TestReconcilerDeletedContainerDriftsAndAutoApplies(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredArtifactID: &artifactID, DesiredHash: "sha256:desired-state", DriftStatus: domain.DriftStatusInSync},
	}}
	publisher := &mockPublisher{}
	deployer := &mockAutoRemediationDeployer{}
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeAutoApply}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}}, &mockObservationRepo{}, stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeHealth: domain.HealthStatusStopped}}, publisher,
		time.Minute, zap.NewNop(), WithAutoRemediationDeployer(deployer),
	)
	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("reconcileOne() error = %v", err)
	}
	if got := stateRepo.states[stateKey].DriftStatus; got != domain.DriftStatusDrifted {
		t.Fatalf("drift status = %q, want drifted", got)
	}
	if deployer.calls != 1 {
		t.Fatalf("auto-remediation calls = %d, want 1", deployer.calls)
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	driftEvents := 0
	for _, event := range publisher.events {
		if event.Type == events.EventDriftDetected {
			driftEvents++
		}
	}
	if driftEvents != 1 {
		t.Fatalf("drift event count = %d, want 1", driftEvents)
	}
}

func TestReconcilerHealthyEmptyDigestIsUnknown(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredArtifactID: &artifactID, DesiredHash: "sha256:desired-state", DriftStatus: domain.DriftStatusInSync},
	}}
	publisher := &mockPublisher{}
	deployer := &mockAutoRemediationDeployer{}
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeAutoApply}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}}, &mockObservationRepo{}, stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeHealth: domain.HealthStatusHealthy}}, publisher,
		time.Minute, zap.NewNop(), WithAutoRemediationDeployer(deployer),
	)
	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("reconcileOne() error = %v", err)
	}
	if got := stateRepo.states[stateKey].DriftStatus; got != domain.DriftStatusUnknown {
		t.Fatalf("drift status = %q, want unknown", got)
	}
	if deployer.calls != 0 {
		t.Fatalf("auto-remediation calls = %d, want 0", deployer.calls)
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	for _, event := range publisher.events {
		if event.Type == events.EventDriftDetected {
			t.Fatalf("unexpected drift event: %#v", event)
		}
	}
}

func TestReconcilerLogsDigestFallbackDecisionOnStatusChange(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredArtifactID: &artifactID, DesiredHash: "sha256:desired-state", DriftStatus: domain.DriftStatusInSync},
	}}
	core, logs := observer.New(zap.InfoLevel)
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{}, stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeDigest: "registry.example/api@" + reconcileObservedDigest}},
		&mockPublisher{}, time.Minute, zap.New(core),
	)
	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("reconcileOne() error = %v", err)
	}
	entries := logs.FilterMessage("runtime drift decision changed from in_sync").All()
	if len(entries) != 1 || entries[0].Level != zap.WarnLevel {
		t.Fatalf("warn decision logs = %#v", entries)
	}
	fields := entries[0].ContextMap()
	if fields["branch"] != "digest-fallback" || fields["status"] != string(domain.DriftStatusDrifted) || fields["service"] != "api" || fields["environment"] != "prod" || fields["observation_source"] != "mock" {
		t.Fatalf("unexpected decision fields: %#v", fields)
	}
	if fields["desired_digest_prefix"] != "sha256:aaaaa" || fields["observed_digest_prefix"] != "sha256:bbbbb" {
		t.Fatalf("digest prefixes were not bounded: %#v", fields)
	}
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
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		obsRepo,
		stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeDigest: reconcileObservedDigest}},
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
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{},
		stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeDigest: reconcileObservedDigest}},
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

func TestReconcilerAutoApplyRestoresStoppedDesiredService(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	deployer := &mockAutoRemediationDeployer{}

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {
			ServiceID:         serviceID,
			EnvironmentID:     envID,
			DesiredArtifactID: &artifactID,
			DesiredHash:       "sha256:desired-state",
		},
	}}
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeAutoApply}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{},
		stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeDigest: reconcileDesiredDigest, observeHealth: domain.HealthStatusStopped}},
		&mockPublisher{},
		time.Minute,
		zap.NewNop(),
		WithAutoRemediationDeployer(deployer),
	)

	reconciler.reconcileAll(ctx)

	require.Equal(t, 1, deployer.calls)
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
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{},
		stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeNormHash: "sha256:observed-state"}},
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

func TestReconcilerExplicitDeploymentUnitUsesUnitRuntime(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	unitID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	state := &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{stateKey: state}}
	obsRepo := &mockObservationRepo{}
	unitRT := &mockRuntime{observeDigest: "sha256:unit-runtime"}
	resolver := &mockRuntimeResolver{
		rt:               &mockRuntime{observeDigest: "sha256:legacy-runtime"},
		deploymentUnitRT: unitRT,
		legacyErr:        fmt.Errorf("runtime type conflict"),
	}
	unit := &domain.DeploymentUnit{
		ID:            unitID,
		EnvironmentID: envID,
		Key:           "default-docker",
		RuntimeType:   domain.RuntimeTypeDocker,
		ReconcileMode: domain.ReconcileModeObserveOnly,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "astillero", RuntimeType: domain.RuntimeTypeCompose}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unitID: unit}},
		obsRepo,
		stateRepo,
		resolver,
		&mockPublisher{},
		time.Minute,
		zap.NewNop(),
	)

	require.NoError(t, reconciler.reconcileOne(ctx, state))
	require.Zero(t, resolver.legacyCalls)
	require.Equal(t, 1, resolver.deploymentUnitCalls)
	require.Same(t, unit, resolver.resolvedUnit)
	require.Len(t, obsRepo.observations, 1)
	require.Equal(t, "sha256:unit-runtime", obsRepo.observations[0].ObservedImageDigest)
}

func TestReconcilerImplicitPlacementUsesLegacyRuntime(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	state := &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{stateKey: state}}
	obsRepo := &mockObservationRepo{}
	resolver := &mockRuntimeResolver{
		rt:               &mockRuntime{observeDigest: "sha256:legacy-runtime"},
		deploymentUnitRT: &mockRuntime{observeDigest: "sha256:unit-runtime"},
	}

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		obsRepo,
		stateRepo,
		resolver,
		&mockPublisher{},
		time.Minute,
		zap.NewNop(),
	)

	require.NoError(t, reconciler.reconcileOne(ctx, state))
	require.Equal(t, 1, resolver.legacyCalls)
	require.Zero(t, resolver.deploymentUnitCalls)
	require.Len(t, obsRepo.observations, 1)
	require.Equal(t, "sha256:legacy-runtime", obsRepo.observations[0].ObservedImageDigest)
}

func TestReconcilerDeploymentUnitLoadErrorSkipsCycle(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	unitID := uuid.New()
	artifactID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	state := &domain.EnvironmentServiceState{
		ServiceID:         serviceID,
		EnvironmentID:     envID,
		DeploymentUnitID:  &unitID,
		DesiredArtifactID: &artifactID,
	}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{stateKey: state}}
	obsRepo := &mockObservationRepo{}
	resolver := &mockRuntimeResolver{rt: &mockRuntime{observeDigest: "sha256:legacy-runtime"}}
	deployer := &mockAutoRemediationDeployer{}

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeAutoApply}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}, err: fmt.Errorf("unit store unavailable")},
		obsRepo,
		stateRepo,
		resolver,
		&mockPublisher{},
		time.Minute,
		zap.NewNop(),
		WithAutoRemediationDeployer(deployer),
	)

	require.EqualError(t, reconciler.reconcileOne(ctx, state), "unit store unavailable")
	require.Zero(t, resolver.legacyCalls)
	require.Zero(t, resolver.deploymentUnitCalls)
	require.Zero(t, deployer.calls)
	require.Empty(t, obsRepo.observations)
	require.Nil(t, state.LastReconciledAt)
}

func TestReconcilerDeploymentUnitNotFoundFallsBackToLegacyRuntime(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	unitID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	state := &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID, DeploymentUnitID: &unitID}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{stateKey: state}}
	obsRepo := &mockObservationRepo{}
	resolver := &mockRuntimeResolver{rt: &mockRuntime{observeDigest: "sha256:legacy-runtime"}}
	core, logs := observer.New(zap.WarnLevel)

	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		obsRepo,
		stateRepo,
		resolver,
		&mockPublisher{},
		time.Minute,
		zap.New(core),
	)

	require.NoError(t, reconciler.reconcileOne(ctx, state))
	require.Equal(t, 1, resolver.legacyCalls)
	require.Zero(t, resolver.deploymentUnitCalls)
	require.Len(t, obsRepo.observations, 1)
	require.Equal(t, 1, logs.FilterMessage("deployment unit not found; falling back to legacy runtime resolution").Len())
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
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{artifactID: {ID: artifactID, ServiceID: serviceID, ImageDigest: reconcileDesiredDigest}}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unitID: {ID: unitID, EnvironmentID: envID, Key: "blue", RuntimeType: domain.RuntimeTypeDocker, ReconcileMode: domain.ReconcileModeDisabled, OwnershipMode: domain.OwnershipModeBahiaManaged}}},
		obsRepo,
		stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeDigest: reconcileObservedDigest}},
		&mockPublisher{},
		time.Minute,
		zap.NewNop(),
	)

	reconciler.reconcileAll(ctx)

	require.Empty(t, obsRepo.observations)
	require.Nil(t, stateRepo.states[stateKey].LastReconciledAt)
}
