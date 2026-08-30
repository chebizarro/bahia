package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
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
func (m *mockServiceRepo) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	return m.GetByID(ctx, id)
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
func (m *mockServiceRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Service, error) {
	var result []domain.Service
	for _, s := range m.services {
		if s.OrgID == orgID {
			result = append(result, *s)
		}
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

type serviceMutationTxExecutor struct{ services *mockServiceRepo }

func (e *serviceMutationTxExecutor) WithinTx(ctx context.Context, fn func(repos repository.TxRepos) error) error {
	txServices := newMockServiceRepo()
	for id, svc := range e.services.services {
		copy := *svc
		txServices.services[id] = &copy
	}
	if err := fn(repository.TxRepos{Services: txServices}); err != nil {
		return err
	}
	e.services.services = txServices.services
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
func (m *mockEnvRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	var result []domain.Environment
	for _, e := range m.envs {
		if e.OrgID == orgID {
			result = append(result, *e)
		}
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
func (m *mockBuildRepo) GetByCISystemRunID(_ context.Context, ciSystem, ciRunID string) (*domain.Build, error) {
	for _, b := range m.builds {
		if b.CISystem == ciSystem && b.CIRunID == ciRunID {
			return b, nil
		}
	}
	return nil, nil
}

type mockArtifactRepo struct {
	artifacts  map[uuid.UUID]*domain.Artifact
	getByIDErr error
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
func (m *mockArtifactRepo) GetByImageRepoDigest(_ context.Context, imageRepo, imageDigest string) (*domain.Artifact, error) {
	for _, a := range m.artifacts {
		if a.ImageRepo == imageRepo && a.ImageDigest == imageDigest {
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
	intents         map[uuid.UUID]*domain.DeploymentIntent
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
func (m *mockIntentRepo) GetByHiveResultEventID(_ context.Context, eventID string) (*domain.DeploymentIntent, error) {
	for _, di := range m.intents {
		if di.Metadata != nil {
			if v, ok := di.Metadata["hive_ci_result_event_id"].(string); ok && v == eventID {
				return di, nil
			}
		}
	}
	return nil, nil
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
func (m *mockIntentRepo) UpdateDesiredState(_ context.Context, id uuid.UUID, desiredState *domain.DesiredServiceSpec, desiredHash string) error {
	if di, ok := m.intents[id]; ok {
		di.DesiredState = desiredState
		di.DesiredHash = desiredHash
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
func (m *mockStateRepo) ListDueForObservation(_ context.Context, dueBefore time.Time) ([]domain.EnvironmentServiceState, error) {
	var result []domain.EnvironmentServiceState
	for _, s := range m.states {
		if s.LastReconciledAt == nil || !s.LastReconciledAt.After(dueBefore) {
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

type mockSecretRepo struct {
	secrets map[uuid.UUID]*domain.ServiceSecret
}

func newMockSecretRepo() *mockSecretRepo {
	return &mockSecretRepo{secrets: make(map[uuid.UUID]*domain.ServiceSecret)}
}

func (m *mockSecretRepo) Create(_ context.Context, s *domain.ServiceSecret) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.Version == 0 {
		s.Version = 1
	}
	m.secrets[s.ID] = s
	return nil
}
func (m *mockSecretRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.ServiceSecret, error) {
	return m.secrets[id], nil
}
func (m *mockSecretRepo) GetCurrentVersion(_ context.Context, secretID uuid.UUID) (*domain.SecretVersion, error) {
	if s, ok := m.secrets[secretID]; ok {
		return &domain.SecretVersion{ID: uuid.New(), SecretID: secretID, Version: s.Version, EncryptedValue: s.EncryptedValue, EncryptionMethod: s.EncryptionMethod, CreatedBy: s.CreatedBy, CreatedAt: s.UpdatedAt}, nil
	}
	return nil, nil
}
func (m *mockSecretRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error) {
	var result []domain.ServiceSecret
	for _, s := range m.secrets {
		if s.ServiceID == serviceID {
			result = append(result, *s)
		}
	}
	return result, nil
}
func (m *mockSecretRepo) ListByServiceAndEnv(_ context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	var result []domain.ServiceSecret
	for _, s := range m.secrets {
		if s.ServiceID == serviceID && s.EnvironmentID != nil && *s.EnvironmentID == envID {
			result = append(result, *s)
		}
	}
	return result, nil
}
func (m *mockSecretRepo) ListEffective(_ context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	byName := map[string]domain.ServiceSecret{}
	for _, s := range m.secrets {
		if s.ServiceID != serviceID {
			continue
		}
		if s.EnvironmentID == nil {
			byName[s.Name] = *s
		}
	}
	for _, s := range m.secrets {
		if s.ServiceID == serviceID && s.EnvironmentID != nil && *s.EnvironmentID == envID {
			byName[s.Name] = *s
		}
	}
	result := make([]domain.ServiceSecret, 0, len(byName))
	for _, s := range byName {
		result = append(result, s)
	}
	return result, nil
}
func (m *mockSecretRepo) Update(_ context.Context, s *domain.ServiceSecret) error {
	if existing, ok := m.secrets[s.ID]; ok {
		s.Version = existing.Version + 1
	}
	m.secrets[s.ID] = s
	return nil
}
func (m *mockSecretRepo) RecordSecretAccessAudit(context.Context, *domain.SecretAccessAudit) error {
	return nil
}
func (m *mockSecretRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.secrets, id)
	return nil
}
func (m *mockSecretRepo) DeleteByName(_ context.Context, serviceID uuid.UUID, envID *uuid.UUID, name string) error {
	for id, s := range m.secrets {
		if s.ServiceID != serviceID || s.Name != name {
			continue
		}
		if envID == nil && s.EnvironmentID == nil {
			delete(m.secrets, id)
			continue
		}
		if envID != nil && s.EnvironmentID != nil && *envID == *s.EnvironmentID {
			delete(m.secrets, id)
		}
	}
	return nil
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
		echoDigestVerifier{}, publisher, logger,
		WithManualArtifactRegistration(true),
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

type echoDigestVerifier struct{}

func (echoDigestVerifier) VerifyImage(_ context.Context, _ string, reference string) (*ImageVerification, error) {
	return &ImageVerification{Exists: true, Digest: reference}, nil
}

func seedArtifact(t *testing.T, registry *RegistryService, svc *domain.Service, digest string) *domain.Artifact {
	t.Helper()
	ctx := context.Background()
	if !immutableArtifactDigest.MatchString(digest) {
		sum := sha256.Sum256([]byte(digest))
		digest = fmt.Sprintf("sha256:%x", sum)
	}

	build := &domain.Build{
		ServiceID: svc.ID,
		GitSHA:    "abc123",
		GitRef:    "refs/heads/main",
		CISystem:  "hive-ci",
		CIRunID:   uuid.New().String(),
		Status:    domain.BuildStatusSucceeded,
	}
	if err := registry.RegisterBuild(ctx, build); err != nil {
		t.Fatalf("failed to register build: %v", err)
	}

	artifact := &domain.Artifact{
		BuildID:     build.ID,
		ServiceID:   svc.ID,
		ImageRepo:   svc.ArtifactRepo,
		ImageTag:    "v1",
		ImageDigest: digest,
	}
	proof := ArtifactVerificationProof{
		Source:            "embedded_oci_layout",
		ManifestDigest:    digest,
		TagResolvedDigest: digest,
		MediaType:         "application/vnd.oci.image.manifest.v1+json",
		PolicyState:       "test",
	}
	if err := registry.RegisterVerifiedArtifact(ctx, artifact, proof); err != nil {
		t.Fatalf("failed to register verified artifact: %v", err)
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

func TestCompleteDeploymentRunPreservesHistoricalDeploymentUnitIdentity(t *testing.T) {
	tests := []struct {
		name         string
		runUnit      *uuid.UUID
		intentUnit   *uuid.UUID
		desiredUnit  *uuid.UUID
		expectedUnit uuid.UUID
	}{
		{
			name:        "run identity wins",
			runUnit:     uuidPointer(uuid.New()),
			intentUnit:  uuidPointer(uuid.New()),
			desiredUnit: uuidPointer(uuid.New()),
		},
		{
			name:        "intent identity is fallback",
			intentUnit:  uuidPointer(uuid.New()),
			desiredUnit: uuidPointer(uuid.New()),
		},
		{
			name:        "desired state identity supports older intents",
			desiredUnit: uuidPointer(uuid.New()),
		},
	}
	for i := range tests {
		test := &tests[i]
		switch {
		case test.runUnit != nil:
			test.expectedUnit = *test.runUnit
		case test.intentUnit != nil:
			test.expectedUnit = *test.intentUnit
		default:
			test.expectedUnit = *test.desiredUnit
		}
		t.Run(test.name, func(t *testing.T) {
			registry, _, _, _, _, intentRepo, _, _, stateRepo := newTestRegistryAll()
			ctx := context.Background()
			svc, env := seedServiceAndEnv(t, registry)
			artifact := seedArtifact(t, registry, svc, "sha256:unit-identity")
			desired := &domain.DesiredServiceSpec{
				ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
				DeploymentUnitID: test.desiredUnit,
			}
			intent := &domain.DeploymentIntent{
				ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
				DeploymentUnitID: test.intentUnit, DesiredState: desired,
				RequestedBy: "test", SourceKind: domain.SourceKindManual,
				ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
			}
			if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
				t.Fatalf("CreateDeploymentIntent: %v", err)
			}
			intentRepo.intents[intent.ID].Status = domain.IntentStatusApproved
			run := &domain.DeploymentRun{
				DeploymentIntentID: intent.ID,
				DeploymentUnitID:   test.runUnit,
				LoomJobID:          "unit-aware-run",
			}
			if err := registry.CreateDeploymentRun(ctx, run); err != nil {
				t.Fatalf("CreateDeploymentRun: %v", err)
			}
			if run.DeploymentUnitID == nil || *run.DeploymentUnitID != test.expectedUnit {
				t.Fatalf("run deployment unit = %v, want %s", run.DeploymentUnitID, test.expectedUnit)
			}
			if err := registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, nil); err != nil {
				t.Fatalf("CompleteDeploymentRun: %v", err)
			}
			state := stateRepo.states[stateKey(svc.ID, env.ID)]
			if state == nil || state.DeploymentUnitID == nil || *state.DeploymentUnitID != test.expectedUnit {
				t.Fatalf("deployment unit = %v, want %s", state.DeploymentUnitID, test.expectedUnit)
			}
		})
	}
}

func TestCompleteDeploymentRunPreservesHealthyInSyncObservation(t *testing.T) {
	registry, _, _, _, _, intentRepo, _, _, stateRepo := newTestRegistryAll()
	ctx := context.Background()
	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:healthy")
	desired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion,
		ServiceID:     svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		DesiredHash: "sha256:desired",
	}
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		DesiredState: desired, DesiredHash: desired.DesiredHash,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[intent.ID].Status = domain.IntentStatusApproved
	run := &domain.DeploymentRun{DeploymentIntentID: intent.ID, LoomJobID: "runtime:direct"}
	if err := registry.CreateDeploymentRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	observationID := uuid.New()
	stateRepo.states[stateKey(svc.ID, env.ID)] = &domain.EnvironmentServiceState{
		ServiceID: svc.ID, EnvironmentID: env.ID,
		DesiredArtifactID: &artifact.ID, DesiredIntentID: &intent.ID,
		DesiredRuntimeState: desired, DesiredHash: desired.DesiredHash,
		CurrentObservationID: &observationID, DriftStatus: domain.DriftStatusInSync,
	}
	if err := registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	state := stateRepo.states[stateKey(svc.ID, env.ID)]
	if state.DriftStatus != domain.DriftStatusInSync {
		t.Fatalf("completion regressed drift status to %q", state.DriftStatus)
	}
	if state.CurrentObservationID == nil || *state.CurrentObservationID != observationID {
		t.Fatalf("completion lost current observation: %v", state.CurrentObservationID)
	}
}

func TestRecordObservationStartingDoesNotClaimInSync(t *testing.T) {
	registry, _, _, _, _, _, _, _, stateRepo := newTestRegistryAll()
	ctx := context.Background()
	svc, env := seedServiceAndEnv(t, registry)
	stateRepo.states[stateKey(svc.ID, env.ID)] = &domain.EnvironmentServiceState{
		ServiceID: svc.ID, EnvironmentID: env.ID,
		DesiredHash:         "sha256:same",
		DesiredRuntimeState: &domain.DesiredServiceSpec{DesiredHash: "sha256:same"},
	}
	obs := &domain.RuntimeObservation{
		ServiceID: svc.ID, EnvironmentID: env.ID,
		NormalizedHash: "sha256:same", HealthStatus: domain.HealthStatusStarting,
		ObservedAt: time.Now().UTC(),
	}
	if err := registry.RecordObservation(ctx, obs); err != nil {
		t.Fatal(err)
	}
	if got := stateRepo.states[stateKey(svc.ID, env.ID)].DriftStatus; got != domain.DriftStatusDeploying {
		t.Fatalf("starting observation drift = %q, want deploying", got)
	}
}

func TestRecordObservationDelayedProjectionCannotRegressCurrentState(t *testing.T) {
	registry, _, _, _, _, _, _, _, stateRepo := newTestRegistryAll()
	ctx := context.Background()
	svc, env := seedServiceAndEnv(t, registry)
	stateRepo.states[stateKey(svc.ID, env.ID)] = &domain.EnvironmentServiceState{
		ServiceID: svc.ID, EnvironmentID: env.ID,
		DesiredHash:         "sha256:same",
		DesiredRuntimeState: &domain.DesiredServiceSpec{DesiredHash: "sha256:same"},
	}
	newer := &domain.RuntimeObservation{
		ServiceID: svc.ID, EnvironmentID: env.ID,
		NormalizedHash: "sha256:same", HealthStatus: domain.HealthStatusHealthy,
		ObservedAt: time.Now().UTC(),
	}
	if err := registry.RecordObservation(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older := &domain.RuntimeObservation{
		ServiceID: svc.ID, EnvironmentID: env.ID,
		NormalizedHash: "sha256:old", HealthStatus: domain.HealthStatusUnhealthy,
		ObservedAt: newer.ObservedAt.Add(-time.Minute),
	}
	if err := registry.RecordObservation(ctx, older); err != nil {
		t.Fatal(err)
	}
	state := stateRepo.states[stateKey(svc.ID, env.ID)]
	if state.CurrentObservationID == nil || *state.CurrentObservationID != newer.ID {
		t.Fatalf("delayed observation replaced current observation: %v", state.CurrentObservationID)
	}
	if state.DriftStatus != domain.DriftStatusInSync {
		t.Fatalf("delayed observation regressed drift to %q", state.DriftStatus)
	}
}

func uuidPointer(id uuid.UUID) *uuid.UUID {
	return &id
}

func TestUpdateServiceWithExpectedRevisionRejectsStaleRevisionWithoutMutation(t *testing.T) {
	registry, services, _, _, _, _, _ := newTestRegistry()
	registry.txExecutor = &serviceMutationTxExecutor{services: services}
	currentRevision := time.Now().UTC()
	serviceID := uuid.New()
	services.services[serviceID] = &domain.Service{ID: serviceID, Name: "current", CreatedAt: currentRevision.Add(-time.Hour), UpdatedAt: currentRevision}
	updated := *services.services[serviceID]
	updated.Name = "stale-overwrite"

	err := registry.UpdateServiceWithExpectedRevision(context.Background(), &updated, currentRevision.Add(-time.Second))
	if !errors.Is(err, repository.ErrConflict) || !errors.Is(err, repository.ErrStaleRevision) {
		t.Fatalf("error = %v, want revision conflict", err)
	}
	if got := services.services[serviceID]; got.Name != "current" || !got.UpdatedAt.Equal(currentRevision) {
		t.Fatalf("stale update mutated service: %#v", got)
	}
}

func TestCreateDeploymentIntentProtectedRollbackCannotBypassApproval(t *testing.T) {
	registry, _, _, _, _, intentRepo, _ := newTestRegistry()
	ctx := context.Background()
	svc, env := seedServiceAndEnv(t, registry)
	env.Protected = true
	if err := registry.UpdateEnvironment(ctx, env); err != nil {
		t.Fatalf("protect environment: %v", err)
	}
	artifact := seedArtifact(t, registry, svc, "sha256:rollback")
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "operator", SourceKind: domain.SourceKindRollback,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}

	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("CreateDeploymentIntent: %v", err)
	}
	if intent.ApprovalStatus != domain.ApprovalStatusPending || intent.Status != domain.IntentStatusPending {
		t.Fatalf("protected rollback bypassed approval: approval=%q status=%q", intent.ApprovalStatus, intent.Status)
	}
	persisted := intentRepo.intents[intent.ID]
	if persisted == nil || persisted.ApprovalStatus != domain.ApprovalStatusPending || persisted.Status != domain.IntentStatusPending {
		t.Fatalf("persisted protected rollback bypassed approval: %#v", persisted)
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

	published := make(chan events.Event, 1)
	pub := events.NewInProcessPublisher(logger)
	pub.Subscribe(events.EventBuildRegistered, func(ctx context.Context, e events.Event) {
		published <- e
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

	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("expected build.registered event to be published")
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

func TestRecordObservation_DesiredStateHashMatchHealthy_SetsInSync(t *testing.T) {
	registry, _, _, _, _, _, _, obsRepo, stateRepo := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	desiredHash := "sha256:desired-state"
	stateRepo.states[stateKey(svc.ID, env.ID)] = &domain.EnvironmentServiceState{
		ServiceID:     svc.ID,
		EnvironmentID: env.ID,
		DesiredHash:   desiredHash,
		DriftStatus:   domain.DriftStatusUnknown,
	}

	obs := &domain.RuntimeObservation{
		ServiceID:     svc.ID,
		EnvironmentID: env.ID,
		HealthStatus:  domain.HealthStatusHealthy,
		Source:        "docker",
		NormalizedState: &domain.NormalizedObservation{
			ObservationHash: desiredHash,
		},
		ObservedAt: time.Now().UTC(),
	}
	if err := registry.RecordObservation(ctx, obs); err != nil {
		t.Fatalf("record observation: %v", err)
	}

	state := stateRepo.states[stateKey(svc.ID, env.ID)]
	if state.DriftStatus != domain.DriftStatusInSync {
		t.Fatalf("drift status = %q, want %q", state.DriftStatus, domain.DriftStatusInSync)
	}
	if state.CurrentObservationID == nil || *state.CurrentObservationID != obs.ID {
		t.Fatalf("state current observation not updated")
	}
	if got := obsRepo.observations[obs.ID].NormalizedHash; got != desiredHash {
		t.Fatalf("persisted normalized hash = %q, want %q", got, desiredHash)
	}
}

func TestRecordObservation_DesiredStateHashMismatch_SetsDrifted(t *testing.T) {
	registry, _, _, _, _, _, _, _, stateRepo := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	stateRepo.states[stateKey(svc.ID, env.ID)] = &domain.EnvironmentServiceState{
		ServiceID:     svc.ID,
		EnvironmentID: env.ID,
		DesiredHash:   "sha256:desired-state",
		DriftStatus:   domain.DriftStatusInSync,
	}

	obs := &domain.RuntimeObservation{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		HealthStatus:   domain.HealthStatusHealthy,
		Source:         "docker",
		NormalizedHash: "sha256:observed-state",
		ObservedAt:     time.Now().UTC(),
	}
	if err := registry.RecordObservation(ctx, obs); err != nil {
		t.Fatalf("record observation: %v", err)
	}

	state := stateRepo.states[stateKey(svc.ID, env.ID)]
	if state.DriftStatus != domain.DriftStatusDrifted {
		t.Fatalf("drift status = %q, want %q", state.DriftStatus, domain.DriftStatusDrifted)
	}
}

func TestRecordObservation_DesiredStateMissingObservedHash_SetsUnknown(t *testing.T) {
	registry, _, _, _, _, _, _, _, stateRepo := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	stateRepo.states[stateKey(svc.ID, env.ID)] = &domain.EnvironmentServiceState{
		ServiceID:     svc.ID,
		EnvironmentID: env.ID,
		DesiredHash:   "sha256:desired-state",
		DriftStatus:   domain.DriftStatusInSync,
	}

	obs := &domain.RuntimeObservation{
		ServiceID:     svc.ID,
		EnvironmentID: env.ID,
		HealthStatus:  domain.HealthStatusHealthy,
		Source:        "docker",
		ObservedAt:    time.Now().UTC(),
	}
	if err := registry.RecordObservation(ctx, obs); err != nil {
		t.Fatalf("record observation: %v", err)
	}

	state := stateRepo.states[stateKey(svc.ID, env.ID)]
	if state.DriftStatus != domain.DriftStatusUnknown {
		t.Fatalf("drift status = %q, want %q", state.DriftStatus, domain.DriftStatusUnknown)
	}
}

func TestRecordObservation_NonDesiredStateArtifactDigestPathStillWorks(t *testing.T) {
	registry, _, _, _, _, _, _, _, stateRepo := newTestRegistryAll()
	ctx := context.Background()

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:artifact")
	desiredID := artifact.ID
	stateRepo.states[stateKey(svc.ID, env.ID)] = &domain.EnvironmentServiceState{
		ServiceID:         svc.ID,
		EnvironmentID:     env.ID,
		DesiredArtifactID: &desiredID,
		DriftStatus:       domain.DriftStatusUnknown,
	}

	obs := &domain.RuntimeObservation{
		ServiceID:           svc.ID,
		EnvironmentID:       env.ID,
		ObservedImageDigest: artifact.ImageDigest,
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "docker",
		ObservedAt:          time.Now().UTC(),
	}
	if err := registry.RecordObservation(ctx, obs); err != nil {
		t.Fatalf("record observation: %v", err)
	}
	if got := stateRepo.states[stateKey(svc.ID, env.ID)].DriftStatus; got != domain.DriftStatusInSync {
		t.Fatalf("matching artifact digest drift status = %q, want %q", got, domain.DriftStatusInSync)
	}

	obs = &domain.RuntimeObservation{
		ServiceID:           svc.ID,
		EnvironmentID:       env.ID,
		ObservedImageDigest: "sha256:other-artifact",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "docker",
		ObservedAt:          time.Now().UTC().Add(time.Second),
	}
	if err := registry.RecordObservation(ctx, obs); err != nil {
		t.Fatalf("record observation: %v", err)
	}
	if got := stateRepo.states[stateKey(svc.ID, env.ID)].DriftStatus; got != domain.DriftStatusDrifted {
		t.Fatalf("mismatched artifact digest drift status = %q, want %q", got, domain.DriftStatusDrifted)
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
	result    *ImageVerification
	err       error
	reference string
}

func (m *mockVerifier) VerifyImage(_ context.Context, _ string, reference string) (*ImageVerification, error) {
	m.reference = reference
	return m.result, m.err
}

func TestRegisterArtifact_VerificationPasses(t *testing.T) {
	registry, _, _, _, _, _, _ := newTestRegistry()
	registry.verifier = &mockVerifier{result: &ImageVerification{
		Exists: true, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	ctx := context.Background()

	svc, _ := seedServiceAndEnv(t, registry)
	build := &domain.Build{
		ServiceID: svc.ID, GitSHA: "abc123", GitRef: "main", CISystem: "ci", CIRunID: "1",
		Status: domain.BuildStatusSucceeded,
	}
	if err := registry.RegisterBuild(ctx, build); err != nil {
		t.Fatal(err)
	}

	// The test registry uses an explicit digest-returning verifier.
	art := &domain.Artifact{
		BuildID: build.ID, ServiceID: svc.ID,
		ImageRepo: svc.ArtifactRepo, ImageTag: "v1",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	err := registry.RegisterArtifact(ctx, art)
	if err != nil {
		t.Fatalf("expected success with digest verifier, got: %v", err)
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
		WithManualArtifactRegistration(true),
	)

	ctx := context.Background()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "project/image"}
	_ = registry.CreateService(ctx, svc)

	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "1", Status: domain.BuildStatusSucceeded}
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
		WithManualArtifactRegistration(true),
	)

	ctx := context.Background()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "project/image"}
	_ = registry.CreateService(ctx, svc)

	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "1", Status: domain.BuildStatusSucceeded}
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
		WithManualArtifactRegistration(true),
	)

	ctx := context.Background()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "project/image"}
	_ = registry.CreateService(ctx, svc)

	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "1", Status: domain.BuildStatusSucceeded}
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
	if !containsSubstring(err.Error(), "verifying image") {
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
		WithManualArtifactRegistration(true),
	)

	ctx := context.Background()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "project/image"}
	_ = registry.CreateService(ctx, svc)

	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "1", Status: domain.BuildStatusSucceeded}
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
	if verifier.reference != art.ImageTag {
		t.Fatalf("manual verification reference = %q, want tag %q", verifier.reference, art.ImageTag)
	}
}

func TestRegisterArtifactRefusesManualPathWhenPolicyDisabled(t *testing.T) {
	registry, _, _, _, _, _, _ := newTestRegistry()
	registry.allowManualArtifactRegistration = false
	ctx := context.Background()
	svc, _ := seedServiceAndEnv(t, registry)
	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "ci", CIRunID: "manual-disabled"}
	if err := registry.RegisterBuild(ctx, build); err != nil {
		t.Fatal(err)
	}
	artifact := &domain.Artifact{
		BuildID: build.ID, ServiceID: svc.ID, ImageRepo: svc.ArtifactRepo, ImageTag: "v1",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if err := registry.RegisterArtifact(ctx, artifact); err == nil || !strings.Contains(err.Error(), "disabled by policy") {
		t.Fatalf("manual registration policy error = %v", err)
	}
}

func TestNoopImageVerifierDoesNotClaimDigestVerification(t *testing.T) {
	v := &NoopImageVerifier{}
	result, err := v.VerifyImage(context.Background(), "any/repo", "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("noop verifier should not error: %v", err)
	}
	if result.Digest != "" {
		t.Fatalf("noop verifier must not claim a verified digest: %#v", result)
	}
}
