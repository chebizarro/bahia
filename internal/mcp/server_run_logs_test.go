package mcp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testRunRepo struct {
	runs map[uuid.UUID]*domain.DeploymentRun
}

func newTestRunRepo() *testRunRepo {
	return &testRunRepo{runs: make(map[uuid.UUID]*domain.DeploymentRun)}
}

func (r *testRunRepo) Create(_ context.Context, run *domain.DeploymentRun) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	r.runs[run.ID] = run
	return nil
}

func (r *testRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	return r.runs[id], nil
}

func (r *testRunRepo) ListByIntent(_ context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	var runs []domain.DeploymentRun
	for _, run := range r.runs {
		if run.DeploymentIntentID == intentID {
			runs = append(runs, *run)
		}
	}
	return runs, nil
}

func (r *testRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	if run := r.runs[id]; run != nil {
		run.Status = status
		run.ExitCode = exitCode
	}
	return nil
}

func newTestMCPRunLogServer(t *testing.T, stdoutContent, stderrContent string, runStatus domain.DeploymentRunStatus) (*Server, uuid.UUID) {
	t.Helper()

	stdoutHash := blossom.ComputeSHA256([]byte(stdoutContent))
	stderrHash := blossom.ComputeSHA256([]byte(stderrContent))
	blossomServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + stdoutHash:
			_, _ = w.Write([]byte(stdoutContent))
		case "/" + stderrHash:
			_, _ = w.Write([]byte(stderrContent))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(blossomServer.Close)

	runID := uuid.New()
	startedAt := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(2 * time.Minute)
	exitCode := 0
	runRepo := newTestRunRepo()
	runRepo.runs[runID] = &domain.DeploymentRun{
		ID:                 runID,
		DeploymentIntentID: uuid.New(),
		Status:             runStatus,
		ExitCode:           &exitCode,
		StdoutRef:          blossomServer.URL + "/" + stdoutHash,
		StderrRef:          blossomServer.URL + "/" + stderrHash,
		StartedAt:          &startedAt,
		FinishedAt:         &finishedAt,
		CreatedAt:          startedAt,
		UpdatedAt:          finishedAt,
	}

	registry := service.NewRegistryService(
		nil,
		nil,
		nil,
		nil,
		nil,
		runRepo,
		nil,
		nil,
		nil,
		events.NewInProcessPublisher(zap.NewNop()),
		zap.NewNop(),
	)
	blossomClient := blossom.NewClient(blossom.Config{Servers: []string{blossomServer.URL}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	logService := adapterruntime.NewLogService(blossomClient, nil, zap.NewNop())

	return NewServerWithOptions(registry, zap.NewNop(), ServerDeps{LogService: logService}), runID
}

func TestGetTools_IncludesRunLogs(t *testing.T) {
	server, _ := newTestMCPRunLogServer(t, "ok", "warn", domain.RunStatusSucceeded)

	for _, tool := range server.GetTools() {
		if tool.Name == "bahia_get_run_logs" {
			if tool.InputSchema["required"] == nil {
				t.Fatalf("bahia_get_run_logs missing required schema")
			}
			return
		}
	}
	t.Fatalf("missing bahia_get_run_logs tool")
}

func TestCallTool_GetRunLogs_TailAndStreams(t *testing.T) {
	server, runID := newTestMCPRunLogServer(t,
		"stdout line 1\nstdout line 2\nstdout line 3",
		"stderr line 1\nstderr line 2",
		domain.RunStatusSucceeded,
	)

	result, err := server.CallTool(context.Background(), "bahia_get_run_logs", map[string]interface{}{
		"run_id": runID.String(),
		"tail":   float64(1),
		"stream": "stdout",
	})
	if err != nil {
		t.Fatalf("get run logs err: %v", err)
	}
	if result.IsError {
		t.Fatalf("get run logs returned error: %s", result.Content[0].Text)
	}
	payload := decodeResultMap(t, result)
	if payload["run_id"] != runID.String() {
		t.Fatalf("run_id = %v, want %s", payload["run_id"], runID.String())
	}
	if payload["stream"] != "stdout" {
		t.Fatalf("stream = %v, want stdout", payload["stream"])
	}
	if payload["stdout"] != "stdout line 3" {
		t.Fatalf("stdout = %q, want tail line", payload["stdout"])
	}
	if _, ok := payload["stderr"]; ok {
		t.Fatalf("stderr should be omitted for stdout stream: %#v", payload["stderr"])
	}
	if payload["exit_code"] != float64(0) {
		t.Fatalf("exit_code = %v, want 0", payload["exit_code"])
	}

	mergedResult, err := server.CallTool(context.Background(), "bahia_get_run_logs", map[string]interface{}{
		"run_id": runID.String(),
	})
	if err != nil {
		t.Fatalf("get merged run logs err: %v", err)
	}
	if mergedResult.IsError {
		t.Fatalf("get merged run logs returned error: %s", mergedResult.Content[0].Text)
	}
	mergedPayload := decodeResultMap(t, mergedResult)
	mergedLogs, _ := mergedPayload["logs"].(string)
	if !strings.Contains(mergedLogs, "stdout line 1") || !strings.Contains(mergedLogs, "stderr line 1") {
		t.Fatalf("merged logs missing expected streams: %q", mergedLogs)
	}
}

func TestCallTool_GetRunLogs_ValidationAndConfiguration(t *testing.T) {
	server, runID := newTestMCPRunLogServer(t, "ok", "warn", domain.RunStatusSucceeded)

	invalidID, err := server.CallTool(context.Background(), "bahia_get_run_logs", map[string]interface{}{
		"run_id": "not-a-uuid",
	})
	if err != nil {
		t.Fatalf("invalid id call err: %v", err)
	}
	if !invalidID.IsError || !strings.Contains(invalidID.Content[0].Text, "invalid run_id") {
		t.Fatalf("expected invalid run_id error, got %#v", invalidID)
	}

	invalidStream, err := server.CallTool(context.Background(), "bahia_get_run_logs", map[string]interface{}{
		"run_id": runID.String(),
		"stream": "combined",
	})
	if err != nil {
		t.Fatalf("invalid stream call err: %v", err)
	}
	if !invalidStream.IsError || !strings.Contains(invalidStream.Content[0].Text, "invalid stream") {
		t.Fatalf("expected invalid stream error, got %#v", invalidStream)
	}

	unconfigured := NewServerWithOptions(server.registry, zap.NewNop(), ServerDeps{})
	missingService, err := unconfigured.CallTool(context.Background(), "bahia_get_run_logs", map[string]interface{}{
		"run_id": runID.String(),
	})
	if err != nil {
		t.Fatalf("unconfigured call err: %v", err)
	}
	if !missingService.IsError || !strings.Contains(missingService.Content[0].Text, "run log tools are not configured") {
		t.Fatalf("expected unconfigured log service error, got %#v", missingService)
	}
}

func TestCallTool_GetRunLogs_RejectsNonTerminalRun(t *testing.T) {
	server, runID := newTestMCPRunLogServer(t, "ok", "warn", domain.RunStatusRunning)

	result, err := server.CallTool(context.Background(), "bahia_get_run_logs", map[string]interface{}{
		"run_id": runID.String(),
	})
	if err != nil {
		t.Fatalf("get run logs err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "not completed") {
		t.Fatalf("expected non-terminal error, got %#v", result)
	}
}
