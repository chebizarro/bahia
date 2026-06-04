package router_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/app"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	mcpserver "github.com/openagentsinc/bahia/internal/mcp"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// --- In-memory mock repositories for HTTP integration tests ---

type mockServiceRepo struct{ services map[uuid.UUID]*domain.Service }

func newMockServiceRepo() *mockServiceRepo {
	return &mockServiceRepo{services: make(map[uuid.UUID]*domain.Service)}
}
func (m *mockServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	if svc.ID == uuid.Nil {
		svc.ID = uuid.New()
	}
	now := time.Now().UTC()
	svc.CreatedAt = now
	svc.UpdatedAt = now
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
	if _, ok := m.services[svc.ID]; !ok {
		return fmt.Errorf("updating service %s: %w", svc.ID, repository.ErrNotFound)
	}
	m.services[svc.ID] = svc
	return nil
}
func (m *mockServiceRepo) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := m.services[id]; !ok {
		return fmt.Errorf("deleting service %s: %w", id, repository.ErrNotFound)
	}
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
	if _, ok := m.envs[env.ID]; !ok {
		return fmt.Errorf("updating environment %s: %w", env.ID, repository.ErrNotFound)
	}
	m.envs[env.ID] = env
	return nil
}
func (m *mockEnvRepo) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := m.envs[id]; !ok {
		return fmt.Errorf("deleting environment %s: %w", id, repository.ErrNotFound)
	}
	delete(m.envs, id)
	return nil
}

type mockBuildRepo struct{ builds map[uuid.UUID]*domain.Build }

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
func (m *mockBuildRepo) ListByService(_ context.Context, serviceID uuid.UUID, _, _ int) ([]domain.Build, error) {
	var result []domain.Build
	for _, b := range m.builds {
		if b.ServiceID == serviceID {
			result = append(result, *b)
		}
	}
	return result, nil
}
func (m *mockBuildRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.BuildStatus) error {
	b, ok := m.builds[id]
	if !ok {
		return fmt.Errorf("updating build %s: %w", id, repository.ErrNotFound)
	}
	b.Status = status
	return nil
}
func (m *mockBuildRepo) GetByCISystemRunID(_ context.Context, _, _ string) (*domain.Build, error) {
	return nil, nil
}

type mockArtifactRepo struct {
	artifacts map[uuid.UUID]*domain.Artifact
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
func (m *mockArtifactRepo) ListByService(_ context.Context, serviceID uuid.UUID, _, _ int) ([]domain.Artifact, error) {
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
func (m *mockArtifactRepo) GetByImageRepoDigest(_ context.Context, imageRepo, imageDigest string) (*domain.Artifact, error) {
	for _, a := range m.artifacts {
		if a.ImageRepo == imageRepo && a.ImageDigest == imageDigest {
			return a, nil
		}
	}
	return nil, nil
}

type mockIntentRepo struct {
	intents map[uuid.UUID]*domain.DeploymentIntent
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
	return m.intents[id], nil
}
func (m *mockIntentRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	var result []domain.DeploymentIntent
	for _, di := range m.intents {
		if di.ServiceID == serviceID && di.EnvironmentID == envID {
			result = append(result, *di)
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
	di, ok := m.intents[id]
	if !ok {
		return fmt.Errorf("updating intent %s: %w", id, repository.ErrNotFound)
	}
	di.Status = status
	return nil
}
func (m *mockIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	di, ok := m.intents[id]
	if !ok {
		return fmt.Errorf("approving intent %s: %w", id, repository.ErrNotFound)
	}
	di.ApprovalStatus = status
	return nil
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
	dr, ok := m.runs[id]
	if !ok {
		return fmt.Errorf("updating run %s: %w", id, repository.ErrNotFound)
	}
	dr.Status = status
	dr.ExitCode = exitCode
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
func (m *mockObsRepo) ListByServiceEnv(_ context.Context, _, _ uuid.UUID, _ int) ([]domain.RuntimeObservation, error) {
	return nil, nil
}

type mockStateRepo struct {
	states map[string]*domain.EnvironmentServiceState
}

func newMockStateRepo() *mockStateRepo {
	return &mockStateRepo{states: make(map[string]*domain.EnvironmentServiceState)}
}
func sk(svcID, envID uuid.UUID) string { return svcID.String() + ":" + envID.String() }
func (m *mockStateRepo) Upsert(_ context.Context, s *domain.EnvironmentServiceState) error {
	m.states[sk(s.ServiceID, s.EnvironmentID)] = s
	return nil
}
func (m *mockStateRepo) Get(_ context.Context, svcID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return m.states[sk(svcID, envID)], nil
}
func (m *mockStateRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	var r []domain.EnvironmentServiceState
	for _, s := range m.states {
		if s.EnvironmentID == envID {
			r = append(r, *s)
		}
	}
	return r, nil
}
func (m *mockStateRepo) ListByService(_ context.Context, svcID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	var r []domain.EnvironmentServiceState
	for _, s := range m.states {
		if s.ServiceID == svcID {
			r = append(r, *s)
		}
	}
	return r, nil
}
func (m *mockStateRepo) ListDrifted(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	var r []domain.EnvironmentServiceState
	for _, s := range m.states {
		if s.DriftStatus == domain.DriftStatusDrifted {
			r = append(r, *s)
		}
	}
	return r, nil
}
func (m *mockStateRepo) ListAll(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	var r []domain.EnvironmentServiceState
	for _, s := range m.states {
		r = append(r, *s)
	}
	return r, nil
}
func (m *mockStateRepo) ListDueForObservation(_ context.Context, _ time.Time) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

type mockPolicyHTTPRepo struct {
	policies map[uuid.UUID]*domain.DeploymentPolicy
}

func newMockPolicyHTTPRepo() *mockPolicyHTTPRepo {
	return &mockPolicyHTTPRepo{policies: make(map[uuid.UUID]*domain.DeploymentPolicy)}
}
func (m *mockPolicyHTTPRepo) Create(_ context.Context, p *domain.DeploymentPolicy) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	m.policies[p.ID] = p
	return nil
}
func (m *mockPolicyHTTPRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error) {
	policy, ok := m.policies[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return policy, nil
}
func (m *mockPolicyHTTPRepo) GetByName(_ context.Context, name string) (*domain.DeploymentPolicy, error) {
	for _, policy := range m.policies {
		if policy.Name == name {
			return policy, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (m *mockPolicyHTTPRepo) List(_ context.Context, enabledOnly bool) ([]domain.DeploymentPolicy, error) {
	var policies []domain.DeploymentPolicy
	for _, policy := range m.policies {
		if enabledOnly && !policy.Enabled {
			continue
		}
		policies = append(policies, *policy)
	}
	return policies, nil
}
func (m *mockPolicyHTTPRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.DeploymentPolicy, error) {
	var policies []domain.DeploymentPolicy
	for _, policy := range m.policies {
		if policy.EnvironmentID != nil && *policy.EnvironmentID == envID && policy.Enabled {
			policies = append(policies, *policy)
		}
	}
	return policies, nil
}
func (m *mockPolicyHTTPRepo) ListGlobal(_ context.Context) ([]domain.DeploymentPolicy, error) {
	var policies []domain.DeploymentPolicy
	for _, policy := range m.policies {
		if policy.EnvironmentID == nil && policy.Enabled {
			policies = append(policies, *policy)
		}
	}
	return policies, nil
}
func (m *mockPolicyHTTPRepo) Update(_ context.Context, p *domain.DeploymentPolicy) error {
	if _, ok := m.policies[p.ID]; !ok {
		return repository.ErrNotFound
	}
	m.policies[p.ID] = p
	return nil
}
func (m *mockPolicyHTTPRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.policies, id)
	return nil
}

type mockToolProvisioningRepo struct {
	intents  map[uuid.UUID]*domain.ToolProvisionIntent
	denylist map[string]*domain.ToolDenylistEntry
}

func newMockToolProvisioningRepo() *mockToolProvisioningRepo {
	return &mockToolProvisioningRepo{
		intents:  make(map[uuid.UUID]*domain.ToolProvisionIntent),
		denylist: make(map[string]*domain.ToolDenylistEntry),
	}
}
func toolDenylistKey(packageName, manager string) string { return packageName + ":" + manager }
func (m *mockToolProvisioningRepo) CreateIntent(_ context.Context, intent *domain.ToolProvisionIntent) error {
	if intent.ID == uuid.Nil {
		intent.ID = uuid.New()
	}
	m.intents[intent.ID] = intent
	return nil
}
func (m *mockToolProvisioningRepo) GetIntent(_ context.Context, id uuid.UUID) (*domain.ToolProvisionIntent, error) {
	return m.intents[id], nil
}
func (m *mockToolProvisioningRepo) UpdateIntent(_ context.Context, intent *domain.ToolProvisionIntent) error {
	if _, ok := m.intents[intent.ID]; !ok {
		return repository.ErrNotFound
	}
	m.intents[intent.ID] = intent
	return nil
}
func (m *mockToolProvisioningRepo) ListPendingApprovalIntents(_ context.Context) ([]domain.ToolProvisionIntent, error) {
	return m.ListIntentsByStatus(context.Background(), domain.ToolProvisionStatusAwaitingApproval)
}
func (m *mockToolProvisioningRepo) ListIntentsByStatus(_ context.Context, statuses ...domain.ToolProvisionStatus) ([]domain.ToolProvisionIntent, error) {
	wanted := make(map[domain.ToolProvisionStatus]bool, len(statuses))
	for _, status := range statuses {
		wanted[status] = true
	}
	var intents []domain.ToolProvisionIntent
	for _, intent := range m.intents {
		if wanted[intent.Status] {
			intents = append(intents, *intent)
		}
	}
	return intents, nil
}
func (m *mockToolProvisioningRepo) CreateRun(context.Context, *domain.ToolProvisionRun) error {
	return nil
}
func (m *mockToolProvisioningRepo) GetRun(context.Context, uuid.UUID) (*domain.ToolProvisionRun, error) {
	return nil, nil
}
func (m *mockToolProvisioningRepo) UpdateRun(context.Context, *domain.ToolProvisionRun) error {
	return nil
}
func (m *mockToolProvisioningRepo) GetProfileState(context.Context, uuid.UUID, uuid.UUID) (*domain.ToolProfileState, error) {
	return &domain.ToolProfileState{}, nil
}
func (m *mockToolProvisioningRepo) UpsertProfileState(context.Context, *domain.ToolProfileState) error {
	return nil
}
func (m *mockToolProvisioningRepo) AddToDenylist(_ context.Context, entry *domain.ToolDenylistEntry) error {
	if entry.BlockedAt.IsZero() {
		entry.BlockedAt = time.Now().UTC()
	}
	m.denylist[toolDenylistKey(entry.PackageName, entry.Manager)] = entry
	return nil
}
func (m *mockToolProvisioningRepo) RemoveFromDenylist(_ context.Context, packageName, manager string) error {
	delete(m.denylist, toolDenylistKey(packageName, manager))
	return nil
}
func (m *mockToolProvisioningRepo) IsDenylisted(_ context.Context, packageName, manager string) (bool, error) {
	_, ok := m.denylist[toolDenylistKey(packageName, manager)]
	return ok, nil
}
func (m *mockToolProvisioningRepo) ListDenylist(context.Context) ([]domain.ToolDenylistEntry, error) {
	var entries []domain.ToolDenylistEntry
	for _, entry := range m.denylist {
		entries = append(entries, *entry)
	}
	return entries, nil
}
func (m *mockToolProvisioningRepo) LogApproval(context.Context, uuid.UUID, string, string, string) error {
	return nil
}

// --- Test Setup ---

type rbacMemberLookup struct {
	members map[uuid.UUID]map[string]domain.Role
}

func (m *rbacMemberLookup) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	if roles, ok := m.members[orgID]; ok {
		if role, ok := roles[pubkey]; ok {
			return &domain.OrgMember{OrgID: orgID, Pubkey: pubkey, Role: role}, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *rbacMemberLookup) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgMember, error) {
	var out []domain.OrgMember
	for orgID, roles := range m.members {
		if role, ok := roles[pubkey]; ok {
			out = append(out, domain.OrgMember{OrgID: orgID, Pubkey: pubkey, Role: role})
		}
	}
	return out, nil
}

func newTestServer() *httptest.Server {
	srv, _ := newTestServerWithRegistry()
	return srv
}

func newTestServerWithRegistry() (*httptest.Server, *service.RegistryService) {
	registry := newTestRegistryService()
	handler := router.New(registry, zap.NewNop(), config.CORSConfig{AllowedOrigins: []string{"*"}}, nil)
	return httptest.NewServer(handler), registry
}

func seedTestService(t *testing.T, registry *service.RegistryService, name, artifactRepo string) string {
	t.Helper()
	svc := &domain.Service{Name: name, ArtifactRepo: artifactRepo, RuntimeType: domain.RuntimeTypeDocker}
	if err := registry.CreateService(context.Background(), svc); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	return svc.ID.String()
}

func seedTestEnvironment(t *testing.T, registry *service.RegistryService, name string, strategy domain.DeployStrategy, protected bool) string {
	t.Helper()
	env := &domain.Environment{Name: name, DeployStrategy: strategy, Protected: protected}
	if err := registry.CreateEnvironment(context.Background(), env); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	return env.ID.String()
}

func seedTestBuild(t *testing.T, registry *service.RegistryService, serviceID, gitSHA string) string {
	t.Helper()
	build := &domain.Build{
		ServiceID: uuid.MustParse(serviceID),
		GitSHA:    gitSHA,
		GitRef:    "main",
		CISystem:  "test",
		CIRunID:   "run-" + gitSHA,
		Status:    domain.BuildStatusSucceeded,
	}
	if err := registry.RegisterBuild(context.Background(), build); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	return build.ID.String()
}

func seedTestArtifact(t *testing.T, registry *service.RegistryService, serviceID, buildID, repo, tag, digest string) string {
	t.Helper()
	artifact := &domain.Artifact{
		BuildID:     uuid.MustParse(buildID),
		ServiceID:   uuid.MustParse(serviceID),
		ImageRepo:   repo,
		ImageTag:    tag,
		ImageDigest: digest,
		ScanStatus:  domain.ScanStatusClean,
	}
	if err := registry.RegisterArtifact(context.Background(), artifact); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	return artifact.ID.String()
}

func seedTestIntent(t *testing.T, registry *service.RegistryService, serviceID, envID, artifactID, requestedBy string) string {
	t.Helper()
	intent := &domain.DeploymentIntent{
		ServiceID:     uuid.MustParse(serviceID),
		EnvironmentID: uuid.MustParse(envID),
		ArtifactID:    uuid.MustParse(artifactID),
		RequestedBy:   requestedBy,
		SourceKind:    domain.SourceKindManual,
	}
	if err := registry.CreateDeploymentIntent(context.Background(), intent); err != nil {
		t.Fatalf("seed deployment intent: %v", err)
	}
	return intent.ID.String()
}

func seedTestObservation(t *testing.T, registry *service.RegistryService, serviceID, envID, digest string) {
	t.Helper()
	obs := &domain.RuntimeObservation{
		ServiceID:           uuid.MustParse(serviceID),
		EnvironmentID:       uuid.MustParse(envID),
		ObservedImageDigest: digest,
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "test-observer",
	}
	if err := registry.RecordObservation(context.Background(), obs); err != nil {
		t.Fatalf("seed observation: %v", err)
	}
}

func newHealthTestServer(healthProvider *app.HealthProvider) *httptest.Server {
	registry := newTestRegistryService()
	handler := router.NewWithDeps(registry, zap.NewNop(), config.CORSConfig{AllowedOrigins: []string{"*"}}, nil, router.RouterDeps{HealthProvider: healthProvider})
	return httptest.NewServer(handler)
}

func newTestRegistryService() *service.RegistryService {
	return service.NewRegistryService(
		newMockServiceRepo(), newMockEnvRepo(), newMockBuildRepo(), newMockArtifactRepo(),
		newMockIntentRepo(), newMockRunRepo(), newMockObsRepo(), newMockStateRepo(),
		nil, &events.NoopPublisher{}, zap.NewNop(),
	)
}

func doJSON(t *testing.T, method, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("executing request: %v", err)
	}

	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var result map[string]any
	if len(respBody) > 0 {
		trimmed := bytes.TrimSpace(respBody)
		if len(trimmed) > 0 && trimmed[0] != '{' {
			return resp, map[string]any{"raw": string(respBody)}
		}
		if err := json.Unmarshal(respBody, &result); err != nil {
			t.Fatalf("decoding response body %q: %v", string(respBody), err)
		}
	}
	return resp, result
}

func assertDeprecatedMutationRouteRemoved(t *testing.T, method, path string, resp *http.Response, body map[string]any) {
	t.Helper()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("%s %s: expected removed route to return 404 or 405, got %d: %v", method, path, resp.StatusCode, body)
	}
}

const routerNIP98Key = "9a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b"

func makeRouterNIP98Header(t *testing.T, method, url string) string {
	t.Helper()
	return makeRouterNIP98HeaderWithKey(t, routerNIP98Key, method, url)
}

func makeRouterNIP98HeaderWithKey(t *testing.T, privateKey, method, url string) string {
	t.Helper()
	ev := nostr.Event{
		Kind:      27235,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      nostr.Tags{{"u", url}, {"method", method}, {"nonce", uuid.NewString()}},
		Content:   "",
	}
	if err := ev.Sign(privateKey); err != nil {
		t.Fatalf("sign NIP-98 event: %v", err)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal NIP-98 event: %v", err)
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(payload)
}

// --- Health / Ready ---

func TestContinuityRESTRoutesAreRemoved(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	tests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/continuity/status", nil},
		{http.MethodGet, "/api/continuity/topology", nil},
		{http.MethodPost, "/api/continuity/simulate", map[string]string{"worker_pubkey": "worker-a"}},
	}
	for _, tt := range tests {
		resp, body := doJSON(t, tt.method, srv.URL+tt.path, tt.body)
		assertDeprecatedMutationRouteRemoved(t, tt.method, tt.path, resp, body)
	}
}

func TestRouter_NativeMCPRemovesLegacyAgentHTTP(t *testing.T) {
	cfg := config.Defaults()
	mcpH := handlers.NewMCPHandler(mcpserver.NewServer(nil, zap.NewNop()), zap.NewNop())
	handler := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{AllowedOrigins: []string{"*"}}, nil, router.RouterDeps{
		Config: cfg,
		MCP:    mcpH,
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	for _, path := range []string{"/mcp", "/api/v1/mcp"} {
		resp, body := doJSON(t, "POST", srv.URL+path, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
		if resp.StatusCode != http.StatusOK || body["error"] != nil || body["result"] == nil {
			t.Fatalf("%s expected native MCP JSON-RPC success, status=%d body=%#v", path, resp.StatusCode, body)
		}
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/agent/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected legacy agent route to be removed, got status %d", resp.StatusCode)
	}
}

func TestRouter_ConfiguredNIP98AuthRejectsBearerOnProtectedRoutes(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	mcpH := handlers.NewMCPHandler(mcpserver.NewServer(nil, zap.NewNop()), zap.NewNop())
	handler := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{AllowedOrigins: []string{"*"}}, nil, router.RouterDeps{Config: cfg, MCP: mcpH})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	url := srv.URL + "/api/v1/mcp"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer legacy.jwt.token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected protected MCP Bearer request to be rejected, status=%d body=%s", resp.StatusCode, string(body))
	}
}

func TestRouter_ConfiguredNIP98AuthAllowsProtectedRoutesWithoutJWT(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	mcpH := handlers.NewMCPHandler(mcpserver.NewServer(nil, zap.NewNop()), zap.NewNop())
	handler := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{AllowedOrigins: []string{"*"}}, nil, router.RouterDeps{Config: cfg, MCP: mcpH})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	url := srv.URL + "/api/v1/mcp"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", makeRouterNIP98Header(t, http.MethodPost, url))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected protected MCP NIP-98 request to succeed without JWT, status=%d body=%s", resp.StatusCode, string(body))
	}
}

func TestHealth(t *testing.T) {
	healthProvider := app.NewHealthProvider(app.NewModePolicy(app.ModeDegraded), nil)
	srv := newHealthTestServer(healthProvider)
	defer srv.Close()

	resp, body := doJSON(t, "GET", srv.URL+"/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body["status"] != "healthy" {
		t.Errorf("expected status healthy, got %v", body["status"])
	}
	if body["mode"] != "degraded" {
		t.Errorf("expected mode degraded, got %v", body["mode"])
	}
	if body["requested_tier"] != float64(2) || body["active_tier"] != float64(2) {
		t.Errorf("expected requested/active tier 2, got %v/%v", body["requested_tier"], body["active_tier"])
	}
}

func TestReady(t *testing.T) {
	healthProvider := app.NewHealthProvider(app.NewModePolicy(app.ModeEmergency), nil)
	srv := newHealthTestServer(healthProvider)
	defer srv.Close()

	resp, body := doJSON(t, "GET", srv.URL+"/ready", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body["ready"] != true {
		t.Errorf("expected ready true, got %v", body["ready"])
	}
	if body["mode"] != "emergency" {
		t.Errorf("expected mode emergency, got %v", body["mode"])
	}
	if body["requested_tier"] != float64(1) || body["active_tier"] != float64(1) {
		t.Errorf("expected requested/active tier 1, got %v/%v", body["requested_tier"], body["active_tier"])
	}
}

func TestReadyReturnsServiceUnavailableWhenReadinessCheckFails(t *testing.T) {
	healthProvider := app.NewHealthProvider(app.NewModePolicy(app.ModeEmergency), nil)
	healthProvider.RegisterCheck("continuity_runtime", int(app.Tier1), func() app.HealthCheck {
		return app.HealthCheck{Name: "continuity_runtime", Status: app.HealthStatusFail, Message: "not running", Tier: int(app.Tier1)}
	})
	srv := newHealthTestServer(healthProvider)
	defer srv.Close()

	resp, body := doJSON(t, "GET", srv.URL+"/ready", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	if body["ready"] != false {
		t.Errorf("expected ready false, got %v", body["ready"])
	}
	if body["status"] != "unhealthy" {
		t.Errorf("expected status unhealthy, got %v", body["status"])
	}
}

func TestTier0RoutesAlwaysAccessibleWithModePolicy(t *testing.T) {
	policy := app.NewModePolicy(app.ModeFull)
	policy.SetActiveTier(app.Tier1)
	h := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{ModePolicy: policy})

	for _, path := range []string{"/health", "/ready"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestTier2RoutesReturnServiceUnavailableWhenActiveTier1(t *testing.T) {
	policy := app.NewModePolicy(app.ModeFull)
	policy.SetActiveTier(app.Tier1)
	h := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{ModePolicy: policy})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503, body=%s", w.Code, w.Body.String())
	}
}

func TestTier3RoutesReturnServiceUnavailableWithTierBodyWhenActiveTier2(t *testing.T) {
	policy := app.NewModePolicy(app.ModeFull)
	policy.SetActiveTier(app.Tier2)
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{ModePolicy: policy, MLCommands: &captureMLRESTPublisher{}})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ml/imports", strings.NewReader(`{"idempotency_key":"import:1","model":"model:qwen","source":"huggingface"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503, body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "route unavailable in current mode" || body["mode"] != "full" || body["active_tier"] != float64(2) || body["required_tier"] != float64(3) {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestTieredRoutesAccessibleWhenActiveTier3(t *testing.T) {
	policy := app.NewModePolicy(app.ModeFull)
	h := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{ModePolicy: policy, MLCommands: &captureMLRESTPublisher{}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tier2 status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/ml/imports", strings.NewReader(`{"idempotency_key":"import:1","model":"model:qwen","source":"huggingface"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("tier3 status=%d, want 202, body=%s", w.Code, w.Body.String())
	}
}

// --- Service CRUD ---

func TestCoreRoutesEnforceTenantRBAC(t *testing.T) {
	const aliceKey = "0000000000000000000000000000000000000000000000000000000000000001"
	const bobKey = "0000000000000000000000000000000000000000000000000000000000000002"
	alicePubkey, err := nostr.GetPublicKey(aliceKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nostr.GetPublicKey(bobKey); err != nil {
		t.Fatal(err)
	}
	orgA := uuid.New()
	orgB := uuid.New()
	svcA := &domain.Service{ID: uuid.New(), OrgID: orgA, Name: "svc-a", ArtifactRepo: "harbor/svc-a", DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker}
	svcB := &domain.Service{ID: uuid.New(), OrgID: orgB, Name: "svc-b", ArtifactRepo: "harbor/svc-b", DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker}
	svcRepo := newMockServiceRepo()
	svcRepo.services[svcA.ID] = svcA
	svcRepo.services[svcB.ID] = svcB

	registry := service.NewRegistryService(
		svcRepo, newMockEnvRepo(), newMockBuildRepo(), newMockArtifactRepo(),
		newMockIntentRepo(), newMockRunRepo(), newMockObsRepo(), newMockStateRepo(),
		nil, &events.NoopPublisher{}, zap.NewNop(),
	)
	lookup := &rbacMemberLookup{members: map[uuid.UUID]map[string]domain.Role{
		orgA: {alicePubkey: domain.RoleViewer},
	}}
	handler := router.NewWithDeps(registry, zap.NewNop(), config.CORSConfig{AllowedOrigins: []string{"*"}}, nil, router.RouterDeps{
		AuthMiddleware: auth.MiddlewareConfig{Enabled: true, NIP98Validator: auth.NewNIP98Validator(auth.DefaultNIP98Config())},
		Services:       svcRepo,
		Builds:         newMockBuildRepo(),
		Artifacts:      newMockArtifactRepo(),
		RBAC:           auth.NewRBAC(lookup),
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := http.DefaultClient
	allowedReqURL := srv.URL + "/api/v1/services/" + svcA.ID.String()
	allowedReq, _ := http.NewRequest(http.MethodGet, allowedReqURL, nil)
	allowedReq.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, aliceKey, http.MethodGet, allowedReqURL))
	resp, err := client.Do(allowedReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member read status = %d, want 200", resp.StatusCode)
	}

	crossReqURL := srv.URL + "/api/v1/services/" + svcB.ID.String()
	crossReq, _ := http.NewRequest(http.MethodGet, crossReqURL, nil)
	crossReq.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, aliceKey, http.MethodGet, crossReqURL))
	resp, err = client.Do(crossReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-org read status = %d, want 403", resp.StatusCode)
	}

	nonMemberReqURL := srv.URL + "/api/v1/services/" + svcA.ID.String()
	nonMemberReq, _ := http.NewRequest(http.MethodGet, nonMemberReqURL, nil)
	nonMemberReq.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, bobKey, http.MethodGet, nonMemberReqURL))
	resp, err = client.Do(nonMemberReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member read status = %d, want 403", resp.StatusCode)
	}
}

func TestServiceReadRoutesRemainAndDeprecatedMutationsAreRemoved(t *testing.T) {
	srv, registry := newTestServerWithRegistry()
	defer srv.Close()
	base := srv.URL + "/api/v1/services"
	svcID := seedTestService(t, registry, "my-service", "harbor/my-service")

	resp, body := doJSON(t, "GET", base+"/"+svcID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Get: expected 200, got %d", resp.StatusCode)
	}
	data := body["data"].(map[string]any)
	if data["name"] != "my-service" {
		t.Errorf("Get: expected name my-service, got %v", data["name"])
	}

	resp, _ = doJSON(t, "GET", base, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("List: expected 200, got %d", resp.StatusCode)
	}

	removedRoutes := []struct {
		method string
		url    string
		body   any
	}{
		{http.MethodPost, base, map[string]any{"name": "removed", "artifact_repo": "harbor/removed"}},
		{http.MethodPut, base + "/" + svcID, map[string]any{"name": "renamed-service"}},
		{http.MethodDelete, base + "/" + svcID, nil},
	}
	for _, route := range removedRoutes {
		resp, body := doJSON(t, route.method, route.url, route.body)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: expected 405 after REST deprecation, got %d: %v", route.method, route.url, resp.StatusCode, body)
		}
	}
}

func TestServiceGet_NotFound(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, body := doJSON(t, "GET", srv.URL+"/api/v1/services/"+uuid.New().String(), nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d: %v", resp.StatusCode, body)
	}
}

func TestServiceGet_BadUUID(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, _ := doJSON(t, "GET", srv.URL+"/api/v1/services/not-a-uuid", nil)
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// --- Environment CRUD ---

func TestEnvironmentReadRoutesRemainAndDeprecatedMutationsAreRemoved(t *testing.T) {
	srv, registry := newTestServerWithRegistry()
	defer srv.Close()
	base := srv.URL + "/api/v1/environments"
	envID := seedTestEnvironment(t, registry, "staging", domain.DeployStrategyReplace, false)

	resp, _ := doJSON(t, "GET", base+"/"+envID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Get: expected 200, got %d", resp.StatusCode)
	}

	resp, _ = doJSON(t, "GET", base, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("List: expected 200, got %d", resp.StatusCode)
	}

	removedRoutes := []struct {
		method string
		url    string
		body   any
	}{
		{http.MethodPost, base, map[string]any{"name": "removed", "deploy_strategy": "replace"}},
		{http.MethodPut, base + "/" + envID, map[string]any{"name": "production"}},
		{http.MethodDelete, base + "/" + envID, nil},
	}
	for _, route := range removedRoutes {
		resp, body := doJSON(t, route.method, route.url, route.body)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: expected 405 after REST deprecation, got %d: %v", route.method, route.url, resp.StatusCode, body)
		}
	}
}

// --- Build Registration ---

func TestBuildLifecycle(t *testing.T) {
	srv, registry := newTestServerWithRegistry()
	defer srv.Close()
	svcID := seedTestService(t, registry, "build-svc", "harbor/build-svc")

	// Register a build.
	resp, body := doJSON(t, "POST", srv.URL+"/api/v1/builds", map[string]any{
		"service_id": svcID,
		"git_sha":    "abc1234",
		"git_ref":    "refs/heads/main",
		"ci_run_id":  "run-123",
		"status":     "running",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("build register: expected 201, got %d: %v", resp.StatusCode, body)
	}
	buildID := body["data"].(map[string]any)["id"].(string)

	// Get the build.
	resp, body = doJSON(t, "GET", srv.URL+"/api/v1/builds/"+buildID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("build get: expected 200, got %d", resp.StatusCode)
	}
	if body["data"].(map[string]any)["git_sha"] != "abc1234" {
		t.Error("expected git_sha abc1234")
	}

	// Update build status.
	resp, _ = doJSON(t, "PATCH", srv.URL+"/api/v1/builds/"+buildID+"/status", map[string]any{
		"status": "succeeded",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("build status update: expected 200, got %d", resp.StatusCode)
	}

	// List builds by service.
	resp, _ = doJSON(t, "GET", fmt.Sprintf("%s/api/v1/services/%s/builds", srv.URL, svcID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list builds: expected 200, got %d", resp.StatusCode)
	}
}

// --- Artifacts ---

func TestArtifactReadRoutesRemainAndDeprecatedRegisterIsRemoved(t *testing.T) {
	srv, registry := newTestServerWithRegistry()
	defer srv.Close()
	svcID := seedTestService(t, registry, "art-svc", "harbor/art-svc")
	buildID := seedTestBuild(t, registry, svcID, "def4567")
	artID := seedTestArtifact(t, registry, svcID, buildID, "harbor/art-svc", "v1.0", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	resp, body := doJSON(t, "GET", srv.URL+"/api/v1/artifacts/"+artID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("artifact get: expected 200, got %d", resp.StatusCode)
	}
	if body["data"].(map[string]any)["image_tag"] != "v1.0" {
		t.Error("expected image_tag v1.0")
	}

	resp, _ = doJSON(t, "GET", fmt.Sprintf("%s/api/v1/services/%s/artifacts", srv.URL, svcID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list artifacts: expected 200, got %d", resp.StatusCode)
	}

	resp, body = doJSON(t, "POST", srv.URL+"/api/v1/artifacts", map[string]any{
		"build_id":     buildID,
		"service_id":   svcID,
		"image_repo":   "harbor/art-svc",
		"image_tag":    "v1.1",
		"image_digest": "sha256:abababababababababababababababababababababababababababababababab",
	})
	assertDeprecatedMutationRouteRemoved(t, http.MethodPost, "/api/v1/artifacts", resp, body)
}

// --- Deployment Intent & Run Full Flow ---

func TestDeploymentFlow(t *testing.T) {
	srv, registry := newTestServerWithRegistry()
	defer srv.Close()
	svcID := seedTestService(t, registry, "deploy-svc", "harbor/deploy")
	envID := seedTestEnvironment(t, registry, "staging", domain.DeployStrategyReplace, false)
	buildID := seedTestBuild(t, registry, svcID, "aaa1111a")
	artID := seedTestArtifact(t, registry, svcID, buildID, "harbor/deploy", "v2.0", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	intentID := seedTestIntent(t, registry, svcID, envID, artID, "test-user")

	resp, body := doJSON(t, "GET", srv.URL+"/api/v1/deployments/intents/"+intentID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get intent: expected 200, got %d", resp.StatusCode)
	}

	// Create deployment run.
	resp, body = doJSON(t, "POST", srv.URL+"/api/v1/deployments/runs", map[string]any{
		"deployment_intent_id": intentID,
		"loom_job_id":          "loom-123",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create run: expected 201, got %d: %v", resp.StatusCode, body)
	}
	runID := body["data"].(map[string]any)["id"].(string)

	// Get the run.
	resp, _ = doJSON(t, "GET", srv.URL+"/api/v1/deployments/runs/"+runID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get run: expected 200, got %d", resp.StatusCode)
	}

	// Complete the run.
	resp, body = doJSON(t, "POST", srv.URL+"/api/v1/deployments/runs/"+runID+"/complete", map[string]any{
		"status": "succeeded",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("complete run: expected 200, got %d: %v", resp.StatusCode, body)
	}

	// List intents by service+env.
	resp, _ = doJSON(t, "GET", fmt.Sprintf("%s/api/v1/services/%s/environments/%s/intents", srv.URL, svcID, envID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list intents: expected 200, got %d", resp.StatusCode)
	}

	// List runs by intent.
	resp, _ = doJSON(t, "GET", fmt.Sprintf("%s/api/v1/deployments/intents/%s/runs", srv.URL, intentID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list runs: expected 200, got %d", resp.StatusCode)
	}
}

// --- Approval Flow (Protected Environment) ---

func TestApprovalRoutesAreRemoved(t *testing.T) {
	srv, registry := newTestServerWithRegistry()
	defer srv.Close()
	svcID := seedTestService(t, registry, "approval-svc", "harbor/approval")
	envID := seedTestEnvironment(t, registry, "production", domain.DeployStrategyReplace, true)
	buildID := seedTestBuild(t, registry, svcID, "bbb2222b")
	artID := seedTestArtifact(t, registry, svcID, buildID, "harbor/approval", "v3.0", "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	intentID := seedTestIntent(t, registry, svcID, envID, artID, "deployer")

	resp, body := doJSON(t, "GET", srv.URL+"/api/v1/deployments/intents/"+intentID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get seeded protected intent: expected 200, got %d: %v", resp.StatusCode, body)
	}
	intentData := body["data"].(map[string]any)
	if intentData["approval_status"] != string(domain.ApprovalStatusPending) {
		t.Errorf("expected pending approval, got %v", intentData["approval_status"])
	}

	path := "/api/v1/deployments/intents/" + intentID + "/approve"
	resp, body = doJSON(t, http.MethodPost, srv.URL+path, nil)
	assertDeprecatedMutationRouteRemoved(t, http.MethodPost, path, resp, body)
}

func TestRejectFlow(t *testing.T) {
	srv, registry := newTestServerWithRegistry()
	defer srv.Close()
	svcID := seedTestService(t, registry, "reject-svc", "harbor/reject")
	envID := seedTestEnvironment(t, registry, "prod-reject", domain.DeployStrategyReplace, true)
	buildID := seedTestBuild(t, registry, svcID, "ccc3333c")
	artID := seedTestArtifact(t, registry, svcID, buildID, "harbor/reject", "v4.0", "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	intentID := seedTestIntent(t, registry, svcID, envID, artID, "deployer")

	path := "/api/v1/deployments/intents/" + intentID + "/reject"
	resp, body := doJSON(t, http.MethodPost, srv.URL+path, nil)
	assertDeprecatedMutationRouteRemoved(t, http.MethodPost, path, resp, body)

	if err := registry.RejectDeploymentIntent(context.Background(), uuid.MustParse(intentID)); err != nil {
		t.Fatalf("reject seeded intent through registry: %v", err)
	}

	resp, _ = doJSON(t, "POST", srv.URL+"/api/v1/deployments/runs", map[string]any{
		"deployment_intent_id": intentID,
	})
	if resp.StatusCode != 500 {
		t.Fatalf("run on rejected intent: expected 500, got %d", resp.StatusCode)
	}
}

// --- State Endpoints ---

func TestStateEndpoints(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	// List all states (empty).
	resp, _ := doJSON(t, "GET", srv.URL+"/api/v1/state", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list all state: expected 200, got %d", resp.StatusCode)
	}

	// List drifted states (empty).
	resp, _ = doJSON(t, "GET", srv.URL+"/api/v1/state/drifted", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list drifted: expected 200, got %d", resp.StatusCode)
	}
}

// --- Observation State ---

func TestObservationStateReadRemainsAndDeprecatedRecordRouteIsRemoved(t *testing.T) {
	srv, registry := newTestServerWithRegistry()
	defer srv.Close()
	svcID := seedTestService(t, registry, "obs-svc", "harbor/obs")
	envID := seedTestEnvironment(t, registry, "obs-env", domain.DeployStrategyReplace, false)
	seedTestObservation(t, registry, svcID, envID, "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")

	resp, body := doJSON(t, "GET", fmt.Sprintf("%s/api/v1/services/%s/environments/%s/state", srv.URL, svcID, envID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get state: expected 200, got %d: %v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/observations", map[string]any{
		"service_id":            svcID,
		"environment_id":        envID,
		"observed_image_digest": "sha256:efefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefef",
		"health_status":         "healthy",
		"source":                "docker-observer",
	})
	assertDeprecatedMutationRouteRemoved(t, http.MethodPost, "/api/v1/observations", resp, body)
}

func TestDeprecatedDeploymentObservationArtifactMutationRoutesAreRemoved(t *testing.T) {
	srv, registry := newTestServerWithRegistry()
	defer srv.Close()
	svcID := seedTestService(t, registry, "removed-deploy-svc", "harbor/removed-deploy")
	envID := seedTestEnvironment(t, registry, "removed-env", domain.DeployStrategyReplace, true)
	buildID := seedTestBuild(t, registry, svcID, "fff6666f")
	artID := seedTestArtifact(t, registry, svcID, buildID, "harbor/removed-deploy", "v6.0", "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	intentID := seedTestIntent(t, registry, svcID, envID, artID, "deployer")

	tests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/v1/deployments/intents", map[string]any{"service_id": svcID, "environment_id": envID, "artifact_id": artID, "requested_by": "deployer"}},
		{http.MethodPost, "/api/v1/deployments/intents/" + intentID + "/approve", nil},
		{http.MethodPost, "/api/v1/deployments/intents/" + intentID + "/reject", nil},
		{http.MethodPost, "/api/v1/rollback", map[string]any{"service_id": svcID, "environment_id": envID, "requested_by": "deployer"}},
		{http.MethodPost, "/api/v1/observations", map[string]any{"service_id": svcID, "environment_id": envID, "observed_image_digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111", "health_status": "healthy", "source": "test"}},
		{http.MethodPost, "/api/v1/artifacts", map[string]any{"build_id": buildID, "service_id": svcID, "image_repo": "harbor/removed-deploy", "image_tag": "v6.1", "image_digest": "sha256:1212121212121212121212121212121212121212121212121212121212121212"}},
	}
	for _, tt := range tests {
		resp, body := doJSON(t, tt.method, srv.URL+tt.path, tt.body)
		assertDeprecatedMutationRouteRemoved(t, tt.method, tt.path, resp, body)
	}
}

func TestPolicyReadRoutesRemainAndDeprecatedMutationsAreRemoved(t *testing.T) {
	policyRepo := newMockPolicyHTTPRepo()
	policySvc := service.NewPolicyService(policyRepo, nil, nil, zap.NewNop())
	policy := &domain.DeploymentPolicy{
		Name:        "require-sbom",
		Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSBOM}},
		Enforcement: domain.PolicyEnforcementBlock,
		Enabled:     true,
	}
	if err := policyRepo.Create(context.Background(), policy); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	handler := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Policies: policySvc})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/policies", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list policies: expected 200, got %d: %v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/policies/"+policy.ID.String(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get policy: expected 200, got %d: %v", resp.StatusCode, body)
	}

	tests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/v1/policies", map[string]any{"name": "removed", "rules": []any{}, "enforcement": "warn"}},
		{http.MethodPut, "/api/v1/policies/" + policy.ID.String(), map[string]any{"name": "updated"}},
		{http.MethodDelete, "/api/v1/policies/" + policy.ID.String(), nil},
		{http.MethodPost, "/api/v1/policies/evaluate", map[string]any{"artifact_id": uuid.New().String(), "environment_id": uuid.New().String()}},
	}
	for _, tt := range tests {
		resp, body := doJSON(t, tt.method, srv.URL+tt.path, tt.body)
		assertDeprecatedMutationRouteRemoved(t, tt.method, tt.path, resp, body)
	}
}

func TestToolDenylistRoutesRemainAndDeprecatedApprovalRoutesAreRemoved(t *testing.T) {
	toolRepo := newMockToolProvisioningRepo()
	intentID := uuid.New()
	if err := toolRepo.CreateIntent(context.Background(), &domain.ToolProvisionIntent{
		ID:             intentID,
		ServiceID:      uuid.New(),
		EnvironmentID:  uuid.New(),
		RequestedTools: []domain.ToolRequest{{Name: "curl", Version: "8"}},
		Status:         domain.ToolProvisionStatusAwaitingApproval,
	}); err != nil {
		t.Fatalf("seed tool intent: %v", err)
	}

	handler := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{ToolProvisioning: toolRepo})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/tools/pending", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list pending tools: expected 200, got %d: %v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/tools/denylist", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list tool denylist: expected 200, got %d: %v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/tools/denylist", map[string]any{
		"package": "left-pad",
		"manager": "npm",
		"reason":  "blocked by policy",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add tool denylist: expected 201, got %d: %v", resp.StatusCode, body)
	}

	resp, body = doJSON(t, http.MethodDelete, srv.URL+"/api/v1/tools/denylist/left-pad/npm", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove tool denylist: expected 200, got %d: %v", resp.StatusCode, body)
	}

	for _, path := range []string{
		"/api/v1/tools/" + intentID.String() + "/approve",
		"/api/v1/tools/" + intentID.String() + "/reject",
	} {
		resp, body := doJSON(t, http.MethodPost, srv.URL+path, map[string]any{"reason": "reviewed"})
		assertDeprecatedMutationRouteRemoved(t, http.MethodPost, path, resp, body)
	}
}

func TestDeprecatedPolicyAndToolApprovalMutationRoutesAreRemoved(t *testing.T) {
	policyRepo := newMockPolicyHTTPRepo()
	policySvc := service.NewPolicyService(policyRepo, nil, nil, zap.NewNop())
	policyID := uuid.New()
	toolRepo := newMockToolProvisioningRepo()
	intentID := uuid.New()
	handler := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Policies: policySvc, ToolProvisioning: toolRepo})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/v1/policies", map[string]any{"name": "removed", "rules": []any{}, "enforcement": "warn"}},
		{http.MethodPut, "/api/v1/policies/" + policyID.String(), map[string]any{"name": "updated"}},
		{http.MethodDelete, "/api/v1/policies/" + policyID.String(), nil},
		{http.MethodPost, "/api/v1/policies/evaluate", map[string]any{"artifact_id": uuid.New().String(), "environment_id": uuid.New().String()}},
		{http.MethodPost, "/api/v1/tools/" + intentID.String() + "/approve", map[string]any{"reason": "approved"}},
		{http.MethodPost, "/api/v1/tools/" + intentID.String() + "/reject", map[string]any{"reason": "rejected"}},
	}
	for _, tt := range tests {
		resp, body := doJSON(t, tt.method, srv.URL+tt.path, tt.body)
		assertDeprecatedMutationRouteRemoved(t, tt.method, tt.path, resp, body)
	}
}

// --- 404 on Non-Existent Resources ---

func TestGetNonExistentBuild(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, _ := doJSON(t, "GET", srv.URL+"/api/v1/builds/"+uuid.New().String(), nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetNonExistentArtifact(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, _ := doJSON(t, "GET", srv.URL+"/api/v1/artifacts/"+uuid.New().String(), nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetNonExistentIntent(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, _ := doJSON(t, "GET", srv.URL+"/api/v1/deployments/intents/"+uuid.New().String(), nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetNonExistentRun(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, _ := doJSON(t, "GET", srv.URL+"/api/v1/deployments/runs/"+uuid.New().String(), nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDeprecatedServiceAndEnvironmentMutationRoutesAreRemoved(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	tests := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodDelete, "/api/v1/services/" + uuid.New().String(), nil},
		{http.MethodDelete, "/api/v1/environments/" + uuid.New().String(), nil},
		{http.MethodPut, "/api/v1/services/" + uuid.New().String(), map[string]any{"name": "updated-name"}},
		{http.MethodPut, "/api/v1/environments/" + uuid.New().String(), map[string]any{"name": "updated-env"}},
	}
	for _, tt := range tests {
		resp, body := doJSON(t, tt.method, srv.URL+tt.path, tt.body)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: expected 405 after REST deprecation, got %d: %v", tt.method, tt.path, resp.StatusCode, body)
		}
	}
}
