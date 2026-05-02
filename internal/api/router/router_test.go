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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/api/router"
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

// --- Test Setup ---

func newTestServer() *httptest.Server {
	registry := service.NewRegistryService(
		newMockServiceRepo(), newMockEnvRepo(), newMockBuildRepo(), newMockArtifactRepo(),
		newMockIntentRepo(), newMockRunRepo(), newMockObsRepo(), newMockStateRepo(),
		nil, &events.NoopPublisher{}, zap.NewNop(),
	)
	handler := router.New(registry, zap.NewNop(), config.CORSConfig{AllowedOrigins: []string{"*"}}, nil)
	return httptest.NewServer(handler)
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
		if err := json.Unmarshal(respBody, &result); err != nil {
			t.Fatalf("decoding response body %q: %v", string(respBody), err)
		}
	}
	return resp, result
}

const routerNIP98Key = "9a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b"

func makeRouterNIP98Header(t *testing.T, method, url string) string {
	t.Helper()
	ev := nostr.Event{
		Kind:      27235,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      nostr.Tags{{"u", url}, {"method", method}},
		Content:   "",
	}
	if err := ev.Sign(routerNIP98Key); err != nil {
		t.Fatalf("sign NIP-98 event: %v", err)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal NIP-98 event: %v", err)
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(payload)
}

// --- Health / Ready ---

func TestRouter_NativeMCPAndDeprecatedAgentHeaders(t *testing.T) {
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

	resp, _ := doJSON(t, "GET", srv.URL+"/api/v1/agent/info", nil)
	if resp.Header.Get("Deprecation") != "true" || resp.Header.Get("Sunset") == "" {
		t.Fatalf("expected deprecation headers on legacy agent route, got %#v", resp.Header)
	}
}

func TestRouter_SystemInfoIsPublicForAuthCapabilityDiscovery(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	cfg.Auth.NIP98Enabled = true
	handler := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{AllowedOrigins: []string{"*"}}, nil, router.RouterDeps{Config: cfg})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/system/info", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected public system info for auth bootstrap, status=%d body=%#v", resp.StatusCode, body)
	}
}

func TestRouter_ConfiguredNIP98AuthAllowsProtectedRoutesWithoutJWT(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	cfg.Auth.NIP98Enabled = true
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
	srv := newTestServer()
	defer srv.Close()

	resp, body := doJSON(t, "GET", srv.URL+"/health", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
}

func TestReady(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, body := doJSON(t, "GET", srv.URL+"/ready", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body["status"] != "ready" {
		t.Errorf("expected status ready, got %v", body["status"])
	}
}

// --- Service CRUD ---

func TestServiceCRUD(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	base := srv.URL + "/api/v1/services"

	// Create
	resp, body := doJSON(t, "POST", base, map[string]any{
		"name":          "my-service",
		"artifact_repo": "harbor/my-service",
		"runtime_type":  "docker",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("Create: expected 201, got %d: %v", resp.StatusCode, body)
	}
	data := body["data"].(map[string]any)
	svcID := data["id"].(string)
	if data["name"] != "my-service" {
		t.Errorf("expected name my-service, got %v", data["name"])
	}
	if data["runtime_type"] != "docker" {
		t.Errorf("expected runtime_type docker, got %v", data["runtime_type"])
	}

	// Get
	resp, body = doJSON(t, "GET", base+"/"+svcID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("Get: expected 200, got %d", resp.StatusCode)
	}
	data = body["data"].(map[string]any)
	if data["name"] != "my-service" {
		t.Errorf("Get: expected name my-service, got %v", data["name"])
	}

	// List
	resp, body = doJSON(t, "GET", base, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("List: expected 200, got %d", resp.StatusCode)
	}

	// Update
	newName := "renamed-service"
	resp, body = doJSON(t, "PUT", base+"/"+svcID, map[string]any{
		"name": newName,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("Update: expected 200, got %d: %v", resp.StatusCode, body)
	}
	data = body["data"].(map[string]any)
	if data["name"] != newName {
		t.Errorf("Update: expected name %s, got %v", newName, data["name"])
	}

	// Delete
	resp, body = doJSON(t, "DELETE", base+"/"+svcID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("Delete: expected 200, got %d", resp.StatusCode)
	}

	// Get after delete → 404
	resp, _ = doJSON(t, "GET", base+"/"+svcID, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("Get after delete: expected 404, got %d", resp.StatusCode)
	}
}

func TestServiceCreate_InvalidBody(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, _ := doJSON(t, "POST", srv.URL+"/api/v1/services", map[string]any{
		"name": "", // empty name
	})
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for empty name, got %d", resp.StatusCode)
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

func TestEnvironmentCRUD(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()
	base := srv.URL + "/api/v1/environments"

	// Create
	resp, body := doJSON(t, "POST", base, map[string]any{
		"name":            "staging",
		"deploy_strategy": "replace",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("Create: expected 201, got %d: %v", resp.StatusCode, body)
	}
	data := body["data"].(map[string]any)
	envID := data["id"].(string)

	// Get
	resp, body = doJSON(t, "GET", base+"/"+envID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("Get: expected 200, got %d", resp.StatusCode)
	}

	// List
	resp, _ = doJSON(t, "GET", base, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("List: expected 200, got %d", resp.StatusCode)
	}

	// Update
	resp, body = doJSON(t, "PUT", base+"/"+envID, map[string]any{
		"name": "production",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("Update: expected 200, got %d: %v", resp.StatusCode, body)
	}

	// Delete
	resp, _ = doJSON(t, "DELETE", base+"/"+envID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("Delete: expected 200, got %d", resp.StatusCode)
	}
}

// --- Build Registration ---

func TestBuildLifecycle(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	// Create a service first.
	resp, body := doJSON(t, "POST", srv.URL+"/api/v1/services", map[string]any{
		"name":          "build-svc",
		"artifact_repo": "harbor/build-svc",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("service create: expected 201, got %d", resp.StatusCode)
	}
	svcID := body["data"].(map[string]any)["id"].(string)

	// Register a build.
	resp, body = doJSON(t, "POST", srv.URL+"/api/v1/builds", map[string]any{
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

// --- Artifact Registration ---

func TestArtifactRegistration(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	// Create service.
	resp, body := doJSON(t, "POST", srv.URL+"/api/v1/services", map[string]any{
		"name": "art-svc", "artifact_repo": "harbor/art-svc",
	})
	svcID := body["data"].(map[string]any)["id"].(string)

	// Register build.
	resp, body = doJSON(t, "POST", srv.URL+"/api/v1/builds", map[string]any{
		"service_id": svcID, "git_sha": "def4567", "git_ref": "main", "ci_run_id": "r1",
	})
	buildID := body["data"].(map[string]any)["id"].(string)

	// Register artifact.
	resp, body = doJSON(t, "POST", srv.URL+"/api/v1/artifacts", map[string]any{
		"build_id":     buildID,
		"service_id":   svcID,
		"image_repo":   "harbor/art-svc",
		"image_tag":    "v1.0",
		"image_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"scan_status":  "clean",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("artifact register: expected 201, got %d: %v", resp.StatusCode, body)
	}
	artID := body["data"].(map[string]any)["id"].(string)

	// Get artifact.
	resp, body = doJSON(t, "GET", srv.URL+"/api/v1/artifacts/"+artID, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("artifact get: expected 200, got %d", resp.StatusCode)
	}
	if body["data"].(map[string]any)["image_tag"] != "v1.0" {
		t.Error("expected image_tag v1.0")
	}

	// List artifacts by service.
	resp, _ = doJSON(t, "GET", fmt.Sprintf("%s/api/v1/services/%s/artifacts", srv.URL, svcID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list artifacts: expected 200, got %d", resp.StatusCode)
	}
}

// --- Deployment Intent & Run Full Flow ---

func TestDeploymentFlow(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	// Create service.
	_, body := doJSON(t, "POST", srv.URL+"/api/v1/services", map[string]any{
		"name": "deploy-svc", "artifact_repo": "harbor/deploy",
	})
	svcID := body["data"].(map[string]any)["id"].(string)

	// Create environment (non-protected, so auto-approved).
	_, body = doJSON(t, "POST", srv.URL+"/api/v1/environments", map[string]any{
		"name": "staging", "deploy_strategy": "replace",
	})
	envID := body["data"].(map[string]any)["id"].(string)

	// Register build + artifact.
	_, body = doJSON(t, "POST", srv.URL+"/api/v1/builds", map[string]any{
		"service_id": svcID, "git_sha": "aaa1111a", "git_ref": "main", "ci_run_id": "r2",
	})
	buildID := body["data"].(map[string]any)["id"].(string)

	_, body = doJSON(t, "POST", srv.URL+"/api/v1/artifacts", map[string]any{
		"build_id": buildID, "service_id": svcID,
		"image_repo": "harbor/deploy", "image_tag": "v2.0",
		"image_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	artID := body["data"].(map[string]any)["id"].(string)

	// Create deployment intent.
	resp, body := doJSON(t, "POST", srv.URL+"/api/v1/deployments/intents", map[string]any{
		"service_id":     svcID,
		"environment_id": envID,
		"artifact_id":    artID,
		"requested_by":   "test-user",
		"source_kind":    "manual",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create intent: expected 201, got %d: %v", resp.StatusCode, body)
	}
	intentData := body["data"].(map[string]any)
	intentID := intentData["id"].(string)
	// Non-protected env → should be auto-approved.
	if intentData["approval_status"] != "not_required" {
		t.Errorf("expected approval_status not_required, got %v", intentData["approval_status"])
	}
	if intentData["status"] != "approved" {
		t.Errorf("expected status approved, got %v", intentData["status"])
	}

	// Get the intent.
	resp, body = doJSON(t, "GET", srv.URL+"/api/v1/deployments/intents/"+intentID, nil)
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

func TestApprovalFlow(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	// Create service.
	_, body := doJSON(t, "POST", srv.URL+"/api/v1/services", map[string]any{
		"name": "approval-svc", "artifact_repo": "harbor/approval",
	})
	svcID := body["data"].(map[string]any)["id"].(string)

	// Create PROTECTED environment.
	_, body = doJSON(t, "POST", srv.URL+"/api/v1/environments", map[string]any{
		"name": "production", "deploy_strategy": "replace", "protected": true,
	})
	envID := body["data"].(map[string]any)["id"].(string)

	// Register build + artifact.
	_, body = doJSON(t, "POST", srv.URL+"/api/v1/builds", map[string]any{
		"service_id": svcID, "git_sha": "bbb2222b", "git_ref": "main", "ci_run_id": "r3",
	})
	buildID := body["data"].(map[string]any)["id"].(string)

	_, body = doJSON(t, "POST", srv.URL+"/api/v1/artifacts", map[string]any{
		"build_id": buildID, "service_id": svcID,
		"image_repo": "harbor/approval", "image_tag": "v3.0",
		"image_digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	})
	artID := body["data"].(map[string]any)["id"].(string)

	// Create intent → should be pending approval.
	resp, body := doJSON(t, "POST", srv.URL+"/api/v1/deployments/intents", map[string]any{
		"service_id": svcID, "environment_id": envID, "artifact_id": artID,
		"requested_by": "deployer", "source_kind": "manual",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create intent: expected 201, got %d: %v", resp.StatusCode, body)
	}
	intentData := body["data"].(map[string]any)
	intentID := intentData["id"].(string)
	if intentData["approval_status"] != "pending" {
		t.Errorf("expected pending approval, got %v", intentData["approval_status"])
	}

	// Approve the intent.
	resp, body = doJSON(t, "POST", srv.URL+"/api/v1/deployments/intents/"+intentID+"/approve", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("approve: expected 200, got %d: %v", resp.StatusCode, body)
	}

	// Trying to approve again should fail.
	resp, _ = doJSON(t, "POST", srv.URL+"/api/v1/deployments/intents/"+intentID+"/approve", nil)
	if resp.StatusCode != 500 {
		t.Fatalf("double approve: expected 500 (business error), got %d", resp.StatusCode)
	}
}

func TestRejectFlow(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	// Create service + protected env + build + artifact.
	_, body := doJSON(t, "POST", srv.URL+"/api/v1/services", map[string]any{
		"name": "reject-svc", "artifact_repo": "harbor/reject",
	})
	svcID := body["data"].(map[string]any)["id"].(string)

	_, body = doJSON(t, "POST", srv.URL+"/api/v1/environments", map[string]any{
		"name": "prod-reject", "deploy_strategy": "replace", "protected": true,
	})
	envID := body["data"].(map[string]any)["id"].(string)

	_, body = doJSON(t, "POST", srv.URL+"/api/v1/builds", map[string]any{
		"service_id": svcID, "git_sha": "ccc3333c", "git_ref": "main", "ci_run_id": "r4",
	})
	buildID := body["data"].(map[string]any)["id"].(string)

	_, body = doJSON(t, "POST", srv.URL+"/api/v1/artifacts", map[string]any{
		"build_id": buildID, "service_id": svcID,
		"image_repo": "harbor/reject", "image_tag": "v4.0",
		"image_digest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	})
	artID := body["data"].(map[string]any)["id"].(string)

	// Create intent.
	_, body = doJSON(t, "POST", srv.URL+"/api/v1/deployments/intents", map[string]any{
		"service_id": svcID, "environment_id": envID, "artifact_id": artID,
		"requested_by": "deployer", "source_kind": "manual",
	})
	intentID := body["data"].(map[string]any)["id"].(string)

	// Reject the intent.
	resp, body := doJSON(t, "POST", srv.URL+"/api/v1/deployments/intents/"+intentID+"/reject", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("reject: expected 200, got %d: %v", resp.StatusCode, body)
	}

	// Creating a run for a rejected intent should fail.
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

// --- Observation Recording ---

func TestRecordObservation(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	// Create service + env.
	_, body := doJSON(t, "POST", srv.URL+"/api/v1/services", map[string]any{
		"name": "obs-svc", "artifact_repo": "harbor/obs",
	})
	svcID := body["data"].(map[string]any)["id"].(string)

	_, body = doJSON(t, "POST", srv.URL+"/api/v1/environments", map[string]any{
		"name": "obs-env", "deploy_strategy": "replace",
	})
	envID := body["data"].(map[string]any)["id"].(string)

	// Record observation.
	resp, body := doJSON(t, "POST", srv.URL+"/api/v1/observations", map[string]any{
		"service_id":            svcID,
		"environment_id":        envID,
		"observed_image_digest": "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		"health_status":         "healthy",
		"source":                "docker-observer",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("record observation: expected 201, got %d: %v", resp.StatusCode, body)
	}

	// Check state was updated.
	resp, body = doJSON(t, "GET", fmt.Sprintf("%s/api/v1/services/%s/environments/%s/state", srv.URL, svcID, envID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get state: expected 200, got %d: %v", resp.StatusCode, body)
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

func TestDeleteNonExistentService(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, body := doJSON(t, "DELETE", srv.URL+"/api/v1/services/"+uuid.New().String(), nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if msg, _ := body["error"].(string); msg != "service not found" {
		t.Errorf("expected 'service not found', got %q", msg)
	}
}

func TestDeleteNonExistentEnvironment(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	resp, body := doJSON(t, "DELETE", srv.URL+"/api/v1/environments/"+uuid.New().String(), nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if msg, _ := body["error"].(string); msg != "environment not found" {
		t.Errorf("expected 'environment not found', got %q", msg)
	}
}

func TestUpdateNonExistentService(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	name := "updated-name"
	resp, body := doJSON(t, "PUT", srv.URL+"/api/v1/services/"+uuid.New().String(), map[string]any{
		"name": name,
	})
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if msg, _ := body["error"].(string); msg != "service not found" {
		t.Errorf("expected 'service not found', got %q", msg)
	}
}

func TestUpdateNonExistentEnvironment(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	name := "updated-env"
	resp, body := doJSON(t, "PUT", srv.URL+"/api/v1/environments/"+uuid.New().String(), map[string]any{
		"name": name,
	})
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if msg, _ := body["error"].(string); msg != "environment not found" {
		t.Errorf("expected 'environment not found', got %q", msg)
	}
}
