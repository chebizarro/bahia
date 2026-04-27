package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

// --- Mock Repositories ---

type mockServiceRepo struct {
	services map[uuid.UUID]*domain.Service
}

func newMockServiceRepo() *mockServiceRepo {
	return &mockServiceRepo{services: make(map[uuid.UUID]*domain.Service)}
}

func (m *mockServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	if svc.ID == uuid.Nil {
		svc.ID = uuid.New()
	}
	m.services[svc.ID] = svc
	return nil
}
func (m *mockServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	return m.services[id], nil
}
func (m *mockServiceRepo) GetByName(_ context.Context, name string) (*domain.Service, error) {
	for _, s := range m.services {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, nil
}
func (m *mockServiceRepo) List(_ context.Context) ([]domain.Service, error) {
	var result []domain.Service
	for _, s := range m.services {
		result = append(result, *s)
	}
	return result, nil
}
func (m *mockServiceRepo) Update(_ context.Context, svc *domain.Service) error {
	m.services[svc.ID] = svc
	return nil
}
func (m *mockServiceRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.services, id)
	return nil
}

type mockEnvRepo struct {
	envs map[uuid.UUID]*domain.Environment
}

func newMockEnvRepo() *mockEnvRepo {
	return &mockEnvRepo{envs: make(map[uuid.UUID]*domain.Environment)}
}

func (m *mockEnvRepo) Create(_ context.Context, env *domain.Environment) error {
	if env.ID == uuid.Nil {
		env.ID = uuid.New()
	}
	m.envs[env.ID] = env
	return nil
}
func (m *mockEnvRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	return m.envs[id], nil
}
func (m *mockEnvRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	for _, e := range m.envs {
		if e.Name == name {
			return e, nil
		}
	}
	return nil, nil
}
func (m *mockEnvRepo) List(_ context.Context) ([]domain.Environment, error) {
	var result []domain.Environment
	for _, e := range m.envs {
		result = append(result, *e)
	}
	return result, nil
}
func (m *mockEnvRepo) Update(_ context.Context, env *domain.Environment) error {
	m.envs[env.ID] = env
	return nil
}
func (m *mockEnvRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.envs, id)
	return nil
}

type mockBuildRepo struct {
	builds map[uuid.UUID]*domain.Build
}

func newMockBuildRepo() *mockBuildRepo {
	return &mockBuildRepo{builds: make(map[uuid.UUID]*domain.Build)}
}

func (m *mockBuildRepo) Create(_ context.Context, b *domain.Build) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	m.builds[b.ID] = b
	return nil
}
func (m *mockBuildRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	return m.builds[id], nil
}
func (m *mockBuildRepo) ListByService(_ context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	var result []domain.Build
	for _, b := range m.builds {
		if b.ServiceID == serviceID {
			result = append(result, *b)
		}
	}
	return result, nil
}
func (m *mockBuildRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.BuildStatus) error {
	if b, ok := m.builds[id]; ok {
		b.Status = status
	}
	return nil
}

type mockArtifactRepo struct {
	artifacts   map[uuid.UUID]*domain.Artifact
	getByIDErr  error
}

func newMockArtifactRepo() *mockArtifactRepo {
	return &mockArtifactRepo{artifacts: make(map[uuid.UUID]*domain.Artifact)}
}

func (m *mockArtifactRepo) Create(_ context.Context, a *domain.Artifact) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	m.artifacts[a.ID] = a
	return nil
}
func (m *mockArtifactRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	return m.artifacts[id], nil
}
func (m *mockArtifactRepo) GetByDigest(_ context.Context, repo, digest string) (*domain.Artifact, error) {
	for _, a := range m.artifacts {
		if a.ImageRepo == repo && a.ImageDigest == digest {
			return a, nil
		}
	}
	return nil, nil
}
func (m *mockArtifactRepo) ListByService(_ context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error) {
	var result []domain.Artifact
	for _, a := range m.artifacts {
		if a.ServiceID == serviceID {
			result = append(result, *a)
		}
	}
	return result, nil
}
func (m *mockArtifactRepo) ListByBuild(_ context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	var result []domain.Artifact
	for _, a := range m.artifacts {
		if a.BuildID == buildID {
			result = append(result, *a)
		}
	}
	return result, nil
}

type mockIntentRepo struct {
	intents        map[uuid.UUID]*domain.DeploymentIntent
	updateStatusErr error
	getByIDErr      error
}

func newMockIntentRepo() *mockIntentRepo {
	return &mockIntentRepo{intents: make(map[uuid.UUID]*domain.DeploymentIntent)}
}

func (m *mockIntentRepo) Create(_ context.Context, di *domain.DeploymentIntent) error {
	if di.ID == uuid.Nil {
		di.ID = uuid.New()
	}
	now := time.Now().UTC()
	di.CreatedAt = now
	di.UpdatedAt = now
	m.intents[di.ID] = di
	return nil
}
func (m *mockIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	return m.intents[id], nil
}
func (m *mockIntentRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	var result []domain.DeploymentIntent
	for _, di := range m.intents {
		if di.ServiceID == serviceID && di.EnvironmentID == envID {
			result = append(result, *di)
		}
	}
	// Sort by CreatedAt DESC (newest first) - simple bubble sort for tests.
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].CreatedAt.After(result[i].CreatedAt) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end], nil
}
func (m *mockIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	if m.updateStatusErr != nil {
		return m.updateStatusErr
	}
	if di, ok := m.intents[id]; ok {
		di.Status = status
		di.UpdatedAt = time.Now().UTC()
	}
	return nil
}
func (m *mockIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	if di, ok := m.intents[id]; ok {
		di.ApprovalStatus = status
		di.UpdatedAt = time.Now().UTC()
	}
	return nil
}

type mockRunRepo struct {
	runs map[uuid.UUID]*domain.DeploymentRun
}

func newMockRunRepo() *mockRunRepo {
	return &mockRunRepo{runs: make(map[uuid.UUID]*domain.DeploymentRun)}
}

func (m *mockRunRepo) Create(_ context.Context, dr *domain.DeploymentRun) error {
	if dr.ID == uuid.Nil {
		dr.ID = uuid.New()
	}
	m.runs[dr.ID] = dr
	return nil
}
func (m *mockRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	return m.runs[id], nil
}
func (m *mockRunRepo) ListByIntent(_ context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	var result []domain.DeploymentRun
	for _, dr := range m.runs {
		if dr.DeploymentIntentID == intentID {
			result = append(result, *dr)
		}
	}
	return result, nil
}
func (m *mockRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	if dr, ok := m.runs[id]; ok {
		dr.Status = status
		dr.ExitCode = exitCode
	}
	return nil
}

type mockObsRepo struct {
	observations map[uuid.UUID]*domain.RuntimeObservation
}

func newMockObsRepo() *mockObsRepo {
	return &mockObsRepo{observations: make(map[uuid.UUID]*domain.RuntimeObservation)}
}

func (m *mockObsRepo) Create(_ context.Context, obs *domain.RuntimeObservation) error {
	if obs.ID == uuid.Nil {
		obs.ID = uuid.New()
	}
	m.observations[obs.ID] = obs
	return nil
}
func (m *mockObsRepo) GetLatest(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	var latest *domain.RuntimeObservation
	for _, obs := range m.observations {
		if obs.ServiceID == serviceID && obs.EnvironmentID == envID {
			if latest == nil || obs.ObservedAt.After(latest.ObservedAt) {
				latest = obs
			}
		}
	}
	return latest, nil
}
func (m *mockObsRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit int) ([]domain.RuntimeObservation, error) {
	var result []domain.RuntimeObservation
	for _, obs := range m.observations {
		if obs.ServiceID == serviceID && obs.EnvironmentID == envID {
			result = append(result, *obs)
		}
	}
	return result, nil
}

type mockStateRepo struct {
	states    map[string]*domain.EnvironmentServiceState // key: serviceID+envID
	upsertErr error
	getErr    error
}

func newMockStateRepo() *mockStateRepo {
	return &mockStateRepo{states: make(map[string]*domain.EnvironmentServiceState)}
}

func stateKey(serviceID, envID uuid.UUID) string {
	return serviceID.String() + ":" + envID.String()
}

func (m *mockStateRepo) Upsert(_ context.Context, state *domain.EnvironmentServiceState) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.states[stateKey(state.ServiceID, state.EnvironmentID)] = state
	return nil
}
func (m *mockStateRepo) Get(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.states[stateKey(serviceID, envID)], nil
}
func (m *mockStateRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	var result []domain.EnvironmentServiceState
	for _, s := range m.states {
		if s.EnvironmentID == envID {
			result = append(result, *s)
		}
	}
	return result, nil
}
func (m *mockStateRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	var result []domain.EnvironmentServiceState
	for _, s := range m.states {
		if s.ServiceID == serviceID {
			result = append(result, *s)
		}
	}
	return result, nil
}
func (m *mockStateRepo) ListDrifted(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	var result []domain.EnvironmentServiceState
	for _, s := range m.states {
		if s.DriftStatus == domain.DriftStatusDrifted {
			result = append(result, *s)
		}
	}
	return result, nil
}
func (m *mockStateRepo) ListAll(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	var result []domain.EnvironmentServiceState
	for _, s := range m.states {
		result = append(result, *s)
	}
	return result, nil
}

// --- Test Helpers ---

func newTestRegistry() (*RegistryService, *mockServiceRepo, *mockEnvRepo, *mockBuildRepo, *mockArtifactRepo, *mockIntentRepo, *mockRunRepo) {
	svcRepo := newMockServiceRepo()
	envRepo := newMockEnvRepo()
	buildRepo := newMockBuildRepo()
	artRepo := newMockArtifactRepo()
	intentRepo := newMockIntentRepo()
	runRepo := newMockRunRepo()
	obsRepo := newMockObsRepo()
	stateRepo := newMockStateRepo()
	publisher := &events.NoopPublisher{}
	logger := zap.NewNop()

	registry := NewRegistryService(
		svcRepo, envRepo, buildRepo, artRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		nil, publisher, logger,
	)

	return registry, svcRepo, envRepo, buildRepo, artRepo, intentRepo, runRepo
}

func seedServiceAndEnv(t *testing.T, registry *RegistryService) (*domain.Service, *domain.Environment) {
	t.Helper()
	ctx := context.Background()

	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "harbor/test"}
	if err := registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	env := &domain.Environment{Name: "staging", DeployStrategy: domain.DeployStrategyReplace}
	if err := registry.CreateEnvironment(ctx, env); err != nil {
		t.Fatalf("failed to create environment: %v", err)
	}

	return svc, env
}

func seedArtifact(t *testing.T, registry *RegistryService, svc *domain.Service, digest string) *domain.Artifact {
	t.Helper()
	ctx := context.Background()

	build := &domain.Build{
		ServiceID: svc.ID,
		GitSHA:    "abc123",
		GitRef:    "refs/heads/main",
		CISystem:  "hive-ci",
		CIRunID:   uuid.New().String(),
	}
	if err := registry.RegisterBuild(ctx, build); err != nil {
		t.Fatalf("failed to register build: %v", err)
	}

	artifact := &domain.Artifact{
		BuildID:    build.ID,
		ServiceID:  svc.ID,
		ImageRepo:  svc.ArtifactRepo,
		ImageTag:   "v1",
		ImageDigest: digest,
	}
	if err := registry.RegisterArtifact(ctx, artifact); err != nil {
		t.Fatalf("failed to register artifact: %v", err)
	}

	return artifact
}

// --- Tests ---

func TestApproveDeploymentIntent_RejectsAlreadyApproved(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:aaa")

	di := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     artifact.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusPending,
		Status:         domain.IntentStatusPending,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatalf("failed to create intent: %v", err)
	}

	// Manually set it to approved to simulate already-approved state.
	intentRepo.intents[di.ID].ApprovalStatus = domain.ApprovalStatusApproved
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved

	err := registry.ApproveDeploymentIntent(ctx, di.ID)
	if err == nil {
		t.Fatal("expected error when approving already-approved intent")
	}
}

func TestApproveDeploymentIntent_RejectsRejectedIntent(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:bbb")

	di := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     artifact.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusPending,
		Status:         domain.IntentStatusPending,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatalf("failed to create intent: %v", err)
	}

	// Simulate rejection.
	intentRepo.intents[di.ID].ApprovalStatus = domain.ApprovalStatusRejected
	intentRepo.intents[di.ID].Status = domain.IntentStatusRejected

	err := registry.ApproveDeploymentIntent(ctx, di.ID)
	if err == nil {
		t.Fatal("expected error when approving rejected intent")
	}
}

func TestRejectDeploymentIntent_RejectsNonPending(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:ccc")

	di := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     artifact.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusPending,
		Status:         domain.IntentStatusPending,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatalf("failed to create intent: %v", err)
	}

	// Simulate already deployed state.
	intentRepo.intents[di.ID].ApprovalStatus = domain.ApprovalStatusApproved
	intentRepo.intents[di.ID].Status = domain.IntentStatusDeployed

	err := registry.RejectDeploymentIntent(ctx, di.ID)
	if err == nil {
		t.Fatal("expected error when rejecting non-pending intent")
	}
}

func TestCreateDeploymentRun_RejectsPendingIntent(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:ddd")

	di := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     artifact.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusPending,
		Status:         domain.IntentStatusPending,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatalf("failed to create intent: %v", err)
	}

	// Keep it in pending state.
	intentRepo.intents[di.ID].Status = domain.IntentStatusPending

	run := &domain.DeploymentRun{
		DeploymentIntentID: di.ID,
		LoomJobID:          "test-job",
	}
	err := registry.CreateDeploymentRun(ctx, run)
	if err == nil {
		t.Fatal("expected error when creating run for pending intent")
	}
}

func TestCreateDeploymentRun_AcceptsApprovedIntent(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:eee")

	di := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     artifact.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired,
		Status:         domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatalf("failed to create intent: %v", err)
	}

	// Ensure it's approved.
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved

	run := &domain.DeploymentRun{
		DeploymentIntentID: di.ID,
		LoomJobID:          "test-job",
	}
	err := registry.CreateDeploymentRun(ctx, run)
	if err != nil {
		t.Fatalf("expected success for approved intent, got: %v", err)
	}
}

func TestCompleteDeploymentRun_RejectsNonTerminalStatus(t *testing.T) {
	registry, _, _, _, _, _, _ := newTestRegistry()
	ctx := context.Background()

	err := registry.CompleteDeploymentRun(ctx, uuid.New(), domain.RunStatusRunning, nil)
	if err == nil {
		t.Fatal("expected error for non-terminal status")
	}
}

func TestCompleteDeploymentRun_RejectsAlreadyCompleted(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:fff")

	di := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     artifact.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired,
		Status:         domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatalf("failed to create intent: %v", err)
	}
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved

	run := &domain.DeploymentRun{
		DeploymentIntentID: di.ID,
		LoomJobID:          "test-job",
		Status:             domain.RunStatusQueued,
	}
	if err := registry.CreateDeploymentRun(ctx, run); err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	// Complete the run first.
	if err := registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, nil); err != nil {
		t.Fatalf("first completion should succeed: %v", err)
	}

	// Try to complete again.
	err := registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusFailed, nil)
	if err == nil {
		t.Fatal("expected error when completing already-completed run")
	}
}

func TestRollback_FindsPreviousSuccessfulArtifact(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifactV1 := seedArtifact(t, registry, svc, "sha256:v1digest")
	artifactV2 := seedArtifact(t, registry, svc, "sha256:v2digest")

	// Create first deployment (v1) - mark as deployed.
	di1 := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     artifactV1.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired,
		Status:         domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di1); err != nil {
		t.Fatalf("failed to create intent 1: %v", err)
	}
	intentRepo.intents[di1.ID].Status = domain.IntentStatusDeployed

	// Small delay to ensure ordering.
	time.Sleep(2 * time.Millisecond)

	// Create second deployment (v2) - mark as deployed.
	di2 := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     artifactV2.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired,
		Status:         domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di2); err != nil {
		t.Fatalf("failed to create intent 2: %v", err)
	}
	intentRepo.intents[di2.ID].Status = domain.IntentStatusDeployed

	// Rollback should target v1's artifact, not v2's.
	rollback, err := registry.Rollback(ctx, svc.ID, env.ID, "operator")
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	if rollback.ArtifactID != artifactV1.ID {
		t.Errorf("expected rollback to artifact %s (v1), got %s", artifactV1.ID, rollback.ArtifactID)
	}
	if rollback.SourceKind != domain.SourceKindRollback {
		t.Errorf("expected source kind rollback, got %s", rollback.SourceKind)
	}
}

func TestRollback_SkipsFailedIntents(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifactV1 := seedArtifact(t, registry, svc, "sha256:v1good")
	artifactV2 := seedArtifact(t, registry, svc, "sha256:v2good")
	artifactV3 := seedArtifact(t, registry, svc, "sha256:v3bad")

	// v1: deployed successfully
	di1 := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifactV1.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di1); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[di1.ID].Status = domain.IntentStatusDeployed
	time.Sleep(2 * time.Millisecond)

	// v2: deployed successfully
	di2 := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifactV2.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di2); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[di2.ID].Status = domain.IntentStatusDeployed
	time.Sleep(2 * time.Millisecond)

	// v3: FAILED - should be skipped by rollback
	di3 := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifactV3.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di3); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[di3.ID].Status = domain.IntentStatusFailed

	// Rollback from current state (desired = v3's artifact).
	// Should skip v3 (failed) and current desired, and target v2's artifact.
	rollback, err := registry.Rollback(ctx, svc.ID, env.ID, "operator")
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	// The rollback should target v2 (the last successfully deployed that differs from current desired).
	if rollback.ArtifactID != artifactV2.ID {
		// v2 is the most recent successfully deployed. The current desired is v3's artifact.
		// v2 is deployed and differs from v3, so it should be the target.
		t.Errorf("expected rollback to artifact %s (v2), got %s", artifactV2.ID, rollback.ArtifactID)
	}
}

func TestRollback_NoHistory(t *testing.T) {
	registry, _, _, _, _, _, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)

	_, err := registry.Rollback(ctx, svc.ID, env.ID, "operator")
	if err == nil {
		t.Fatal("expected error when no deployment state exists")
	}
}

func TestCreateService_DefaultValues(t *testing.T) {
	registry, _, _, _, _, _, _ := newTestRegistry()
	ctx := context.Background()

	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "harbor/test"}
	if err := registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("failed: %v", err)
	}

	if svc.RuntimeType != domain.RuntimeTypeDocker {
		t.Errorf("expected default runtime docker, got %s", svc.RuntimeType)
	}
	if svc.DefaultBranch != "main" {
		t.Errorf("expected default branch main, got %s", svc.DefaultBranch)
	}
}

func TestRegisterBuild_PublishesEvent(t *testing.T) {
	svcRepo := newMockServiceRepo()
	envRepo := newMockEnvRepo()
	buildRepo := newMockBuildRepo()
	artRepo := newMockArtifactRepo()
	intentRepo := newMockIntentRepo()
	runRepo := newMockRunRepo()
	obsRepo := newMockObsRepo()
	stateRepo := newMockStateRepo()
	logger := zap.NewNop()
	_ = runRepo // used below

	published := make([]events.Event, 0)
	pub := events.NewInProcessPublisher(logger)
	pub.Subscribe(events.EventBuildRegistered, func(ctx context.Context, e events.Event) {
		published = append(published, e)
	})

	registry := NewRegistryService(
		svcRepo, envRepo, buildRepo, artRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		nil, pub, logger,
	)

	ctx := context.Background()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "harbor/test"}
	_ = registry.CreateService(ctx, svc)

	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "1"}
	if err := registry.RegisterBuild(ctx, build); err != nil {
		t.Fatalf("register build: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if len(published) == 0 {
		t.Error("expected build.registered event to be published")
	}
}

func TestCompleteDeploymentRun_UpdatesIntentStatus(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:ggg")

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved

	run := &domain.DeploymentRun{DeploymentIntentID: di.ID, LoomJobID: "j1"}
	if err := registry.CreateDeploymentRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	if err := registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}

	// Verify intent status was updated to deployed.
	updatedIntent := intentRepo.intents[di.ID]
	if updatedIntent.Status != domain.IntentStatusDeployed {
		t.Errorf("expected intent status 'deployed', got '%s'", updatedIntent.Status)
	}
}

func TestIsTerminalRunStatus(t *testing.T) {
	terminals := []domain.DeploymentRunStatus{
		domain.RunStatusSucceeded, domain.RunStatusFailed,
		domain.RunStatusCancelled, domain.RunStatusTimeout,
	}
	for _, s := range terminals {
		if !isTerminalRunStatus(s) {
			t.Errorf("expected %s to be terminal", s)
		}
	}

	nonTerminals := []domain.DeploymentRunStatus{
		domain.RunStatusQueued, domain.RunStatusRunning, "unknown",
	}
	for _, s := range nonTerminals {
		if isTerminalRunStatus(s) {
			t.Errorf("expected %s to NOT be terminal", s)
		}
	}
}

func TestApproveNonExistentIntent(t *testing.T) {
	registry, _, _, _, _, _, _ := newTestRegistry()
	err := registry.ApproveDeploymentIntent(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error for non-existent intent")
	}
	expected := "not found"
	if !containsSubstring(err.Error(), expected) {
		t.Errorf("expected error containing %q, got: %v", expected, err)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	return fmt.Sprintf("%s", s) != "" && len(sub) > 0 && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// newTestRegistryAll returns a registry along with ALL mock repos (including state and obs)
// for tests that need to inject errors into state or observation repos.
func newTestRegistryAll() (*RegistryService, *mockServiceRepo, *mockEnvRepo, *mockBuildRepo, *mockArtifactRepo, *mockIntentRepo, *mockRunRepo, *mockObsRepo, *mockStateRepo) {
	svcRepo := newMockServiceRepo()
	envRepo := newMockEnvRepo()
	buildRepo := newMockBuildRepo()
	artRepo := newMockArtifactRepo()
	intentRepo := newMockIntentRepo()
	runRepo := newMockRunRepo()
	obsRepo := newMockObsRepo()
	stateRepo := newMockStateRepo()
	publisher := &events.NoopPublisher{}
	logger := zap.NewNop()

	registry := NewRegistryService(
		svcRepo, envRepo, buildRepo, artRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		nil, publisher, logger,
	)

	return registry, svcRepo, envRepo, buildRepo, artRepo, intentRepo, runRepo, obsRepo, stateRepo
}

// --- Error Propagation Tests (bahia-bmn) ---

func TestCompleteDeploymentRun_PropagatesIntentUpdateStatusError(t *testing.T) {
	registry, _, _, _, _, intentRepo, _, _, _ := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:err1")

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved

	run := &domain.DeploymentRun{DeploymentIntentID: di.ID, LoomJobID: "j-err1"}
	if err := registry.CreateDeploymentRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Inject error: intent UpdateStatus will fail.
	intentRepo.updateStatusErr = fmt.Errorf("db connection lost")

	err := registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, nil)
	if err == nil {
		t.Fatal("expected error to propagate from intent UpdateStatus")
	}
	if !containsSubstring(err.Error(), "updating intent status") {
		t.Errorf("expected wrapped error message, got: %v", err)
	}
}

func TestCompleteDeploymentRun_PropagatesIntentGetByIDError(t *testing.T) {
	registry, _, _, _, _, intentRepo, _, _, _ := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:err2")

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved

	run := &domain.DeploymentRun{DeploymentIntentID: di.ID, LoomJobID: "j-err2"}
	if err := registry.CreateDeploymentRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Inject error: GetByID will fail AFTER UpdateStatus succeeds.
	// We need GetByID to work during CreateDeploymentRun but fail during CompleteDeploymentRun.
	// The trick: set the error right before completing, after the run was created.
	intentRepo.getByIDErr = fmt.Errorf("intent lookup timeout")

	// This will fail at the UpdateStatus step since GetByID is used there too.
	// Actually no - CompleteDeploymentRun first calls runs.GetByID (not intents), then
	// runs.UpdateStatus, then intents.UpdateStatus, then intents.GetByID.
	// But intents.UpdateStatus also doesn't call GetByID internally.
	// So the first call to intents.GetByID is in the "if succeeded" block.
	// Wait - but the updateStatusErr is nil, so UpdateStatus succeeds, then GetByID errors.
	err := registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, nil)
	if err == nil {
		t.Fatal("expected error to propagate from intent GetByID")
	}
	if !containsSubstring(err.Error(), "fetching intent for state update") {
		t.Errorf("expected wrapped error message, got: %v", err)
	}
}

func TestCompleteDeploymentRun_PropagatesStateUpsertError(t *testing.T) {
	registry, _, _, _, _, intentRepo, _, _, stateRepo := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:err3")

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved

	run := &domain.DeploymentRun{DeploymentIntentID: di.ID, LoomJobID: "j-err3"}
	if err := registry.CreateDeploymentRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Inject error: state Upsert will fail (but only after the run is completed and intent fetched).
	stateRepo.upsertErr = fmt.Errorf("state storage full")

	err := registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, nil)
	if err == nil {
		t.Fatal("expected error to propagate from state Upsert")
	}
	if !containsSubstring(err.Error(), "upserting environment state") {
		t.Errorf("expected wrapped error message, got: %v", err)
	}
}

func TestRecordObservation_PropagatesStateGetError(t *testing.T) {
	registry, _, _, _, _, _, _, _, stateRepo := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)

	// Inject error: state Get will fail.
	stateRepo.getErr = fmt.Errorf("state db unreachable")

	obs := &domain.RuntimeObservation{
		ServiceID:           svc.ID,
		EnvironmentID:       env.ID,
		ObservedImageDigest: "sha256:abc",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "docker",
		ObservedAt:          time.Now().UTC(),
	}
	err := registry.RecordObservation(ctx, obs)
	if err == nil {
		t.Fatal("expected error to propagate from state Get")
	}
	if !containsSubstring(err.Error(), "getting environment service state") {
		t.Errorf("expected wrapped error message, got: %v", err)
	}
}

func TestRecordObservation_ArtifactGetError_SetsDriftUnknown(t *testing.T) {
	registry, _, _, _, artRepo, _, _, _, stateRepo := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:drift1")

	// Set up an existing state with a desired artifact.
	desiredID := artifact.ID
	stateRepo.states[stateKey(svc.ID, env.ID)] = &domain.EnvironmentServiceState{
		ServiceID:         svc.ID,
		EnvironmentID:     env.ID,
		DesiredArtifactID: &desiredID,
		DriftStatus:       domain.DriftStatusInSync,
	}

	// Inject error: artifact GetByID will fail during drift check.
	artRepo.getByIDErr = fmt.Errorf("artifact store timeout")

	obs := &domain.RuntimeObservation{
		ServiceID:           svc.ID,
		EnvironmentID:       env.ID,
		ObservedImageDigest: "sha256:drift1",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "docker",
		ObservedAt:          time.Now().UTC(),
	}
	// Should NOT return error - the observation is still recorded, drift is just unknown.
	err := registry.RecordObservation(ctx, obs)
	if err != nil {
		t.Fatalf("expected observation to succeed despite artifact lookup error, got: %v", err)
	}

	// Verify drift was set to unknown (not left as in_sync).
	state := stateRepo.states[stateKey(svc.ID, env.ID)]
	if state.DriftStatus != domain.DriftStatusUnknown {
		t.Errorf("expected drift status 'unknown' when artifact lookup fails, got '%s'", state.DriftStatus)
	}
}

func TestCompleteDeploymentRun_FailedRun_PropagatesIntentUpdateError(t *testing.T) {
	registry, _, _, _, _, intentRepo, _, _, _ := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:err4")

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved

	run := &domain.DeploymentRun{DeploymentIntentID: di.ID, LoomJobID: "j-err4"}
	if err := registry.CreateDeploymentRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Inject error: intent UpdateStatus will fail.
	intentRepo.updateStatusErr = fmt.Errorf("db write failed")

	// Complete with FAILED status (not succeeded) - the intent update to "failed" should still error.
	err := registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusFailed, nil)
	if err == nil {
		t.Fatal("expected error to propagate from intent UpdateStatus on failed run")
	}
	if !containsSubstring(err.Error(), "updating intent status") {
		t.Errorf("expected wrapped error message, got: %v", err)
	}
}

// --- Image Verification Tests ---

// mockVerifier is a configurable ImageVerifier for testing.
type mockVerifier struct {
	result *ImageVerification
	err    error
}

func (m *mockVerifier) VerifyImage(_ context.Context, _, _ string) (*ImageVerification, error) {
	return m.result, m.err
}

func TestRegisterArtifact_VerificationPasses(t *testing.T) {
	registry, _, _, _, _, _, _ := newTestRegistry()
	ctx := context.Background()

	svc, _ := seedServiceAndEnv(t, registry)
	build := &domain.Build{
		ServiceID: svc.ID, GitSHA: "abc123", GitRef: "main", CISystem: "ci", CIRunID: "1",
	}
	if err := registry.RegisterBuild(ctx, build); err != nil {
		t.Fatal(err)
	}

	// Default registry uses NoopImageVerifier, so this should succeed.
	art := &domain.Artifact{
		BuildID: build.ID, ServiceID: svc.ID,
		ImageRepo: "project/image", ImageTag: "v1",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	err := registry.RegisterArtifact(ctx, art)
	if err != nil {
		t.Fatalf("expected success with noop verifier, got: %v", err)
	}
}

func TestRegisterArtifact_VerificationRejectsNonExistent(t *testing.T) {
	svcRepo := newMockServiceRepo()
	envRepo := newMockEnvRepo()
	buildRepo := newMockBuildRepo()
	artRepo := newMockArtifactRepo()
	intentRepo := newMockIntentRepo()
	runRepo := newMockRunRepo()
	obsRepo := newMockObsRepo()
	stateRepo := newMockStateRepo()

	verifier := &mockVerifier{
		result: &ImageVerification{Exists: false},
	}

	registry := NewRegistryService(
		svcRepo, envRepo, buildRepo, artRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		verifier, &events.NoopPublisher{}, zap.NewNop(),
	)

	ctx := context.Background()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "project/image"}
	_ = registry.CreateService(ctx, svc)

	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "1"}
	_ = registry.RegisterBuild(ctx, build)

	art := &domain.Artifact{
		BuildID: build.ID, ServiceID: svc.ID,
		ImageRepo: "project/image", ImageTag: "v1",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	err := registry.RegisterArtifact(ctx, art)
	if err == nil {
		t.Fatal("expected error for non-existent image")
	}
	if !containsSubstring(err.Error(), "not found in container registry") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestRegisterArtifact_VerificationDigestMismatch(t *testing.T) {
	svcRepo := newMockServiceRepo()
	envRepo := newMockEnvRepo()
	buildRepo := newMockBuildRepo()
	artRepo := newMockArtifactRepo()
	intentRepo := newMockIntentRepo()
	runRepo := newMockRunRepo()
	obsRepo := newMockObsRepo()
	stateRepo := newMockStateRepo()

	verifier := &mockVerifier{
		result: &ImageVerification{
			Exists: true,
			Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}

	registry := NewRegistryService(
		svcRepo, envRepo, buildRepo, artRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		verifier, &events.NoopPublisher{}, zap.NewNop(),
	)

	ctx := context.Background()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "project/image"}
	_ = registry.CreateService(ctx, svc)

	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "1"}
	_ = registry.RegisterBuild(ctx, build)

	art := &domain.Artifact{
		BuildID: build.ID, ServiceID: svc.ID,
		ImageRepo: "project/image", ImageTag: "v1",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	err := registry.RegisterArtifact(ctx, art)
	if err == nil {
		t.Fatal("expected error for digest mismatch")
	}
	if !containsSubstring(err.Error(), "digest mismatch") {
		t.Errorf("expected 'digest mismatch' error, got: %v", err)
	}
}

func TestRegisterArtifact_VerificationError(t *testing.T) {
	svcRepo := newMockServiceRepo()
	envRepo := newMockEnvRepo()
	buildRepo := newMockBuildRepo()
	artRepo := newMockArtifactRepo()
	intentRepo := newMockIntentRepo()
	runRepo := newMockRunRepo()
	obsRepo := newMockObsRepo()
	stateRepo := newMockStateRepo()

	verifier := &mockVerifier{
		err: fmt.Errorf("harbor unreachable"),
	}

	registry := NewRegistryService(
		svcRepo, envRepo, buildRepo, artRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		verifier, &events.NoopPublisher{}, zap.NewNop(),
	)

	ctx := context.Background()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "project/image"}
	_ = registry.CreateService(ctx, svc)

	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "1"}
	_ = registry.RegisterBuild(ctx, build)

	art := &domain.Artifact{
		BuildID: build.ID, ServiceID: svc.ID,
		ImageRepo: "project/image", ImageTag: "v1",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	err := registry.RegisterArtifact(ctx, art)
	if err == nil {
		t.Fatal("expected error when verifier fails")
	}
	if !containsSubstring(err.Error(), "verifying image in registry") {
		t.Errorf("expected 'verifying image' error, got: %v", err)
	}
}

func TestRegisterArtifact_VerificationAdoptsScanStatus(t *testing.T) {
	svcRepo := newMockServiceRepo()
	envRepo := newMockEnvRepo()
	buildRepo := newMockBuildRepo()
	artRepo := newMockArtifactRepo()
	intentRepo := newMockIntentRepo()
	runRepo := newMockRunRepo()
	obsRepo := newMockObsRepo()
	stateRepo := newMockStateRepo()

	verifier := &mockVerifier{
		result: &ImageVerification{
			Exists:     true,
			Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ScanStatus: string(domain.ScanStatusClean),
		},
	}

	registry := NewRegistryService(
		svcRepo, envRepo, buildRepo, artRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		verifier, &events.NoopPublisher{}, zap.NewNop(),
	)

	ctx := context.Background()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "project/image"}
	_ = registry.CreateService(ctx, svc)

	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "1"}
	_ = registry.RegisterBuild(ctx, build)

	art := &domain.Artifact{
		BuildID: build.ID, ServiceID: svc.ID,
		ImageRepo: "project/image", ImageTag: "v1",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		// ScanStatus left empty — should be set to "unknown" by default, then adopted from verifier.
	}
	err := registry.RegisterArtifact(ctx, art)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	if art.ScanStatus != domain.ScanStatusClean {
		t.Errorf("expected scan status to be adopted from verifier as 'clean', got '%s'", art.ScanStatus)
	}
}

func TestNoopImageVerifier(t *testing.T) {
	v := &NoopImageVerifier{}
	result, err := v.VerifyImage(context.Background(), "any/repo", "any-ref")
	if err != nil {
		t.Fatalf("noop verifier should not error: %v", err)
	}
	if !result.Exists {
		t.Error("noop verifier should always report image exists")
	}
}
