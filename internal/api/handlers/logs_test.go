package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// testLogger returns a slog logger that discards all output (for blossom)
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testZapLogger returns a zap logger for components that still use zap
func testZapLogger() *zap.Logger {
	return zap.NewNop()
}

// mockDeploymentRunRepo is a test mock for DeploymentRunRepository
type mockDeploymentRunRepo struct {
	run *domain.DeploymentRun
	err error
}

func (m *mockDeploymentRunRepo) Create(_ context.Context, _ *domain.DeploymentRun) error {
	return nil
}

func (m *mockDeploymentRunRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.DeploymentRun, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.run, nil
}

func (m *mockDeploymentRunRepo) ListByIntent(_ context.Context, _ uuid.UUID) ([]domain.DeploymentRun, error) {
	return nil, nil
}

func (m *mockDeploymentRunRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ domain.DeploymentRunStatus, _ *int) error {
	return nil
}

// mockServiceRepo is a test mock for ServiceRepository
type mockServiceRepo struct {
	svc *domain.Service
	err error
}

func (m *mockServiceRepo) Create(_ context.Context, _ *domain.Service) error { return nil }
func (m *mockServiceRepo) Update(_ context.Context, _ *domain.Service) error { return nil }
func (m *mockServiceRepo) Delete(_ context.Context, _ uuid.UUID) error       { return nil }
func (m *mockServiceRepo) List(_ context.Context) ([]domain.Service, error)  { return nil, nil }
func (m *mockServiceRepo) GetByName(_ context.Context, _ string) (*domain.Service, error) {
	return m.svc, m.err
}
func (m *mockServiceRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Service, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.svc, nil
}

// mockEnvRepo is a test mock for EnvironmentRepository
type mockEnvRepo struct {
	env *domain.Environment
	err error
}

func (m *mockEnvRepo) Create(_ context.Context, _ *domain.Environment) error { return nil }
func (m *mockEnvRepo) Update(_ context.Context, _ *domain.Environment) error { return nil }
func (m *mockEnvRepo) Delete(_ context.Context, _ uuid.UUID) error           { return nil }
func (m *mockEnvRepo) List(_ context.Context) ([]domain.Environment, error)  { return nil, nil }
func (m *mockEnvRepo) GetByName(_ context.Context, _ string) (*domain.Environment, error) {
	return m.env, m.err
}
func (m *mockEnvRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Environment, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.env, nil
}

// mockEnvStateRepo is a test mock for EnvironmentServiceStateRepository
type mockLiveLogResolver struct {
	rt runtime.Runtime
}

func (m *mockLiveLogResolver) Resolve(_ *domain.Service, _ *domain.Environment) (runtime.Runtime, error) {
	return m.rt, nil
}

type mockLiveLogRuntime struct {
	serviceName string
}

func (m *mockLiveLogRuntime) Type() domain.RuntimeType { return domain.RuntimeTypeDocker }
func (m *mockLiveLogRuntime) Observe(_ context.Context, serviceID, envID uuid.UUID, _ string) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{ServiceID: serviceID, EnvironmentID: envID}, nil
}
func (m *mockLiveLogRuntime) Deploy(_ context.Context, _, _ string, _ runtime.DeployOptions) error {
	return nil
}
func (m *mockLiveLogRuntime) Undeploy(_ context.Context, _ string) error { return nil }
func (m *mockLiveLogRuntime) StreamLogs(_ context.Context, serviceName string, _ runtime.LogOptions) (<-chan runtime.LogEntry, error) {
	m.serviceName = serviceName
	ch := make(chan runtime.LogEntry)
	close(ch)
	return ch, nil
}

type mockEnvStateRepo struct{}

func (m *mockEnvStateRepo) Upsert(_ context.Context, _ *domain.EnvironmentServiceState) error {
	return nil
}
func (m *mockEnvStateRepo) Get(_ context.Context, _, _ uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (m *mockEnvStateRepo) ListByEnvironment(_ context.Context, _ uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (m *mockEnvStateRepo) ListByService(_ context.Context, _ uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (m *mockEnvStateRepo) ListDrifted(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (m *mockEnvStateRepo) ListAll(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

func TestLogHandler_GetRunLogs(t *testing.T) {
	stdoutContent := "starting app\nrunning"
	stderrContent := "warning: debug mode"

	stdoutHash := blossom.ComputeSHA256([]byte(stdoutContent))
	stderrHash := blossom.ComputeSHA256([]byte(stderrContent))

	// Create mock Blossom server
	blossomServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/"+stdoutHash {
			w.Write([]byte(stdoutContent))
		} else if path == "/"+stderrHash {
			w.Write([]byte(stderrContent))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer blossomServer.Close()

	blossomClient := blossom.NewClient(blossom.Config{
		Servers: []string{blossomServer.URL},
	}, testLogger())

	logService := runtime.NewLogService(blossomClient, nil, testZapLogger())

	startedAt := time.Now().Add(-1 * time.Minute)
	finishedAt := time.Now()
	exitCode := 0
	runID := uuid.New()

	runRepo := &mockDeploymentRunRepo{
		run: &domain.DeploymentRun{
			ID:         runID,
			Status:     domain.RunStatusSucceeded,
			StdoutRef:  blossomServer.URL + "/" + stdoutHash,
			StderrRef:  blossomServer.URL + "/" + stderrHash,
			StartedAt:  &startedAt,
			FinishedAt: &finishedAt,
			ExitCode:   &exitCode,
		},
	}

	handler := NewLogHandler(logService, runRepo, nil, nil, nil, testZapLogger())

	// Create request with chi URL params
	req := httptest.NewRequest("GET", "/deployments/runs/"+runID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.GetRunLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GetRunLogs() status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data runtime.RunLogs `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp.Data.Stdout != stdoutContent {
		t.Errorf("Stdout = %q, want %q", resp.Data.Stdout, stdoutContent)
	}
	if resp.Data.Stderr != stderrContent {
		t.Errorf("Stderr = %q, want %q", resp.Data.Stderr, stderrContent)
	}
}

func TestLogHandler_GetRunLogs_NotFound(t *testing.T) {
	runRepo := &mockDeploymentRunRepo{
		err: repository.ErrNotFound,
	}

	handler := NewLogHandler(nil, runRepo, nil, nil, nil, testZapLogger())

	runID := uuid.New()
	req := httptest.NewRequest("GET", "/deployments/runs/"+runID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.GetRunLogs(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("GetRunLogs() status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestLogHandler_GetRunLogs_InProgress(t *testing.T) {
	runID := uuid.New()
	runRepo := &mockDeploymentRunRepo{
		run: &domain.DeploymentRun{
			ID:     runID,
			Status: domain.RunStatusRunning, // Still running
		},
	}

	handler := NewLogHandler(nil, runRepo, nil, nil, nil, testZapLogger())

	req := httptest.NewRequest("GET", "/deployments/runs/"+runID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.GetRunLogs(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("GetRunLogs() status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestLogHandler_GetRunLogs_InvalidID(t *testing.T) {
	handler := NewLogHandler(nil, nil, nil, nil, nil, testZapLogger())

	req := httptest.NewRequest("GET", "/deployments/runs/invalid/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "invalid")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.GetRunLogs(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("GetRunLogs() status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestLogHandler_GetRunLogs_WithTail(t *testing.T) {
	// 10 lines of content
	stdoutContent := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	stdoutHash := blossom.ComputeSHA256([]byte(stdoutContent))

	blossomServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(stdoutContent))
	}))
	defer blossomServer.Close()

	blossomClient := blossom.NewClient(blossom.Config{
		Servers: []string{blossomServer.URL},
	}, testLogger())

	logService := runtime.NewLogService(blossomClient, nil, testZapLogger())

	runID := uuid.New()
	runRepo := &mockDeploymentRunRepo{
		run: &domain.DeploymentRun{
			ID:        runID,
			Status:    domain.RunStatusSucceeded,
			StdoutRef: blossomServer.URL + "/" + stdoutHash,
		},
	}

	handler := NewLogHandler(logService, runRepo, nil, nil, nil, testZapLogger())

	req := httptest.NewRequest("GET", "/deployments/runs/"+runID.String()+"/logs?tail=3", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.GetRunLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GetRunLogs() status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data runtime.RunLogs `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Should only have last 3 lines
	expected := "line8\nline9\nline10"
	if resp.Data.Stdout != expected {
		t.Errorf("Stdout with tail=3 = %q, want %q", resp.Data.Stdout, expected)
	}
}

func TestLogHandler_GetRunLogs_StreamFilter(t *testing.T) {
	stdoutContent := "stdout content"
	stderrContent := "stderr content"

	stdoutHash := blossom.ComputeSHA256([]byte(stdoutContent))
	stderrHash := blossom.ComputeSHA256([]byte(stderrContent))

	blossomServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/"+stdoutHash {
			w.Write([]byte(stdoutContent))
		} else if path == "/"+stderrHash {
			w.Write([]byte(stderrContent))
		}
	}))
	defer blossomServer.Close()

	blossomClient := blossom.NewClient(blossom.Config{
		Servers: []string{blossomServer.URL},
	}, testLogger())

	logService := runtime.NewLogService(blossomClient, nil, testZapLogger())

	runID := uuid.New()
	runRepo := &mockDeploymentRunRepo{
		run: &domain.DeploymentRun{
			ID:        runID,
			Status:    domain.RunStatusSucceeded,
			StdoutRef: blossomServer.URL + "/" + stdoutHash,
			StderrRef: blossomServer.URL + "/" + stderrHash,
		},
	}

	handler := NewLogHandler(logService, runRepo, nil, nil, nil, testZapLogger())

	// Test stdout only
	req := httptest.NewRequest("GET", "/deployments/runs/"+runID.String()+"/logs?stream=stdout", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.GetRunLogs(rr, req)

	var resp struct {
		Data runtime.RunLogs `json:"data"`
	}
	json.NewDecoder(rr.Body).Decode(&resp)

	if resp.Data.Stdout != stdoutContent {
		t.Errorf("stdout filter: Stdout = %q, want %q", resp.Data.Stdout, stdoutContent)
	}
	if resp.Data.Stderr != "" {
		t.Errorf("stdout filter: Stderr should be empty, got %q", resp.Data.Stderr)
	}

	// Test stderr only
	req = httptest.NewRequest("GET", "/deployments/runs/"+runID.String()+"/logs?stream=stderr", nil)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr = httptest.NewRecorder()
	handler.GetRunLogs(rr, req)

	var resp2 struct {
		Data runtime.RunLogs `json:"data"`
	}
	json.NewDecoder(rr.Body).Decode(&resp2)

	if resp2.Data.Stderr != stderrContent {
		t.Errorf("stderr filter: Stderr = %q, want %q", resp2.Data.Stderr, stderrContent)
	}
	if resp2.Data.Stdout != "" {
		t.Errorf("stderr filter: Stdout should be empty, got %q", resp2.Data.Stdout)
	}
}

func TestLogHandler_StreamLiveLogs_ServiceNotFound(t *testing.T) {
	svcRepo := &mockServiceRepo{
		err: repository.ErrNotFound,
	}

	handler := NewLogHandler(nil, nil, svcRepo, nil, nil, testZapLogger())

	serviceID := uuid.New()
	envID := uuid.New()
	req := httptest.NewRequest("GET", "/services/"+serviceID.String()+"/environments/"+envID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", serviceID.String())
	rctx.URLParams.Add("envId", envID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.StreamLiveLogs(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("StreamLiveLogs() status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestLogHandler_StreamLiveLogs_UsesRuntimeTargetName(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	svcRepo := &mockServiceRepo{svc: &domain.Service{
		ID:   serviceID,
		Name: "api",
		RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: &domain.AdoptedRuntimeConfig{
			TargetName: "legacy-api",
		}},
	}}
	envRepo := &mockEnvRepo{env: &domain.Environment{ID: envID, Name: "prod"}}
	rt := &mockLiveLogRuntime{}
	handler := NewLogHandlerWithResolver(nil, &mockLiveLogResolver{rt: rt}, nil, svcRepo, envRepo, &mockEnvStateRepo{}, testZapLogger())

	req := httptest.NewRequest("GET", "/services/"+serviceID.String()+"/environments/"+envID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", serviceID.String())
	rctx.URLParams.Add("envId", envID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.StreamLiveLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("StreamLiveLogs() status = %d, want %d", rr.Code, http.StatusOK)
	}
	if rt.serviceName != "legacy-api" {
		t.Fatalf("StreamLogs called with serviceName %q, want legacy-api", rt.serviceName)
	}
}

func TestLogHandler_StreamLiveLogs_EnvironmentNotFound(t *testing.T) {
	svcRepo := &mockServiceRepo{
		svc: &domain.Service{
			ID:   uuid.New(),
			Name: "test-service",
		},
	}
	envRepo := &mockEnvRepo{
		err: repository.ErrNotFound,
	}

	handler := NewLogHandler(nil, nil, svcRepo, envRepo, nil, testZapLogger())

	serviceID := uuid.New()
	envID := uuid.New()
	req := httptest.NewRequest("GET", "/services/"+serviceID.String()+"/environments/"+envID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", serviceID.String())
	rctx.URLParams.Add("envId", envID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.StreamLiveLogs(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("StreamLiveLogs() status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}
