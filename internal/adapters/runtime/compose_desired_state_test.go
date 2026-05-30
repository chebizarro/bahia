package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testEnvironmentPlan builds a minimal DesiredEnvironmentPlan for testing.
func testEnvironmentPlan() *domain.DesiredEnvironmentPlan {
	envID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	svcID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	artID := uuid.MustParse("66666666-7777-8888-9999-aaaaaaaaaaaa")

	spec := domain.DesiredServiceSpec{
		SchemaVersion:    "1",
		ServiceID:        svcID,
		EnvironmentID:    envID,
		ArtifactID:       artID,
		StableServiceKey: "web-frontend",
		ImageRef:         "nginx:1.25",
		Ports:            []string{"8080:80"},
		RestartPolicy:    "unless-stopped",
		PullPolicy:       "always",
		Env: map[string]string{
			"APP_ENV": "production",
		},
		Labels: map[string]string{
			"bahia.managed":        "true",
			"bahia.service_id":     svcID.String(),
			"bahia.environment_id": envID.String(),
		},
		ComposeExtension: &domain.ComposeExtension{
			ProjectName: "test-project",
		},
	}
	spec.DesiredHash = spec.ComputeDesiredHash()

	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services:      []domain.DesiredServiceSpec{spec},
	}
	plan.ComputeRevisionHash()
	return plan
}

// testMultiServicePlan builds a plan with two services.
func testMultiServicePlan() *domain.DesiredEnvironmentPlan {
	envID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")

	api := domain.DesiredServiceSpec{
		SchemaVersion:    "1",
		ServiceID:        uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		EnvironmentID:    envID,
		ArtifactID:       uuid.MustParse("66666666-7777-8888-9999-aaaaaaaaaaaa"),
		StableServiceKey: "api-server",
		ImageRef:         "myapp/api:v2.1",
		Ports:            []string{"3000:3000"},
		RestartPolicy:    "unless-stopped",
		Env: map[string]string{
			"DB_HOST": "postgres",
		},
		Labels: map[string]string{
			"bahia.managed": "true",
		},
		ComposeExtension: &domain.ComposeExtension{
			ProjectName: "multi-svc",
		},
	}
	api.DesiredHash = api.ComputeDesiredHash()

	web := domain.DesiredServiceSpec{
		SchemaVersion:    "1",
		ServiceID:        uuid.MustParse("22222222-3333-4444-5555-666666666666"),
		EnvironmentID:    envID,
		ArtifactID:       uuid.MustParse("77777777-8888-9999-aaaa-bbbbbbbbbbbb"),
		StableServiceKey: "web-frontend",
		ImageRef:         "myapp/web:v1.5",
		Ports:            []string{"8080:80"},
		RestartPolicy:    "always",
		Env: map[string]string{
			"API_URL": "http://api-server:3000",
		},
		Labels: map[string]string{
			"bahia.managed": "true",
		},
	}
	web.DesiredHash = web.ComputeDesiredHash()

	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services:      []domain.DesiredServiceSpec{api, web},
	}
	plan.ComputeRevisionHash()
	return plan
}

// composeDirRunner is a mock runner that tracks calls and allows per-command
// success/failure configuration.
type composeDirRunner struct {
	calls   []mockCall
	handler func(name string, args []string, dir string) (string, string, error)
}

func (r *composeDirRunner) RunCommand(_ context.Context, name string, args []string, dir string, _ []string) (string, string, error) {
	r.calls = append(r.calls, mockCall{Name: name, Args: args, Dir: dir})
	if r.handler != nil {
		return r.handler(name, args, dir)
	}
	return "", "", nil
}

// allSuccessRunner returns a runner that succeeds for all commands.
func allSuccessRunner() *composeDirRunner {
	return &composeDirRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			return "", "", nil
		},
	}
}

// setupBahiaOwnedDir creates a temp dir that passes ownership validation.
func setupBahiaOwnedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bahiaDir := filepath.Join(dir, ".bahia")
	if err := os.MkdirAll(bahiaDir, 0o755); err != nil {
		t.Fatalf("create .bahia dir: %v", err)
	}
	renderState := `{"schema_version": 1, "renderer": "compose"}`
	if err := os.WriteFile(filepath.Join(bahiaDir, "render-state.json"), []byte(renderState), 0o644); err != nil {
		t.Fatalf("write render-state.json: %v", err)
	}
	return dir
}

// newTestApplier creates a ComposeDesiredStateApplier wired to a temp dir
// and mock runner for testing.
func newTestApplier(t *testing.T, dir string, runner *composeDirRunner) *ComposeDesiredStateApplier {
	t.Helper()
	logger := zaptest.NewLogger(t)
	rt := NewComposeRuntime(dir, logger)
	return NewComposeDesiredStateApplierWithRunner(rt, runner, logger)
}

// ---------------------------------------------------------------------------
// Tests: ApplyDesiredState — success path
// ---------------------------------------------------------------------------

func TestComposeDesiredStateApplier_Success(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	result, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
		PullPolicy:      "always",
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// Verify result fields.
	if result.Renderer != "compose" {
		t.Errorf("renderer: want compose, got %s", result.Renderer)
	}
	if result.ExecutionMode != ExecutionModeCLI {
		t.Errorf("execution mode: want %s, got %s", ExecutionModeCLI, result.ExecutionMode)
	}
	if result.DesiredHash != target.DesiredHash {
		t.Errorf("desired hash mismatch")
	}
	if result.EnvironmentRevision != plan.RevisionHash {
		t.Errorf("revision hash mismatch")
	}
	if len(result.ResourceNames) != 1 || result.ResourceNames[0] != "web-frontend" {
		t.Errorf("resource names: want [web-frontend], got %v", result.ResourceNames)
	}

	// Verify live compose file was written (promoted from staging).
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	data, err := os.ReadFile(liveCompose)
	if err != nil {
		t.Fatalf("read live compose: %v", err)
	}
	if !strings.Contains(string(data), "nginx:1.25") {
		t.Error("live compose should contain the rendered image reference")
	}
	if !strings.Contains(string(data), "web-frontend") {
		t.Error("live compose should contain the service key")
	}
}

func TestComposeDesiredStateApplier_CommandArgs_UpWithRemoveOrphans(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// Find the `up` command call (skip the `config -q` validation call).
	var upCall *mockCall
	for i := range runner.calls {
		argsStr := strings.Join(runner.calls[i].Args, " ")
		if strings.Contains(argsStr, " up ") {
			upCall = &runner.calls[i]
			break
		}
	}
	if upCall == nil {
		t.Fatal("expected a 'docker compose up' command call")
	}

	argsStr := strings.Join(upCall.Args, " ")

	// Must contain: up -d --remove-orphans
	if !strings.Contains(argsStr, "up") {
		t.Error("args should contain 'up'")
	}
	if !strings.Contains(argsStr, "-d") {
		t.Error("args should contain '-d'")
	}
	if !strings.Contains(argsStr, "--remove-orphans") {
		t.Error("args should contain '--remove-orphans'")
	}

	// Must contain --project-directory
	if !strings.Contains(argsStr, "--project-directory") {
		t.Error("args should contain '--project-directory'")
	}

	// Must NOT contain --force-recreate
	if strings.Contains(argsStr, "--force-recreate") {
		t.Error("args must NOT contain '--force-recreate'")
	}

	// Must NOT contain a specific service name (full-project apply).
	// The up command should NOT have the service name appended.
	// After --remove-orphans, there should be no service name argument.
	upIdx := -1
	for i, arg := range upCall.Args {
		if arg == "up" {
			upIdx = i
			break
		}
	}
	if upIdx >= 0 {
		postUpArgs := upCall.Args[upIdx+1:]
		for _, arg := range postUpArgs {
			if arg == "web-frontend" {
				t.Error("up command must NOT target a specific service (full-project apply)")
			}
		}
	}
}

func TestComposeDesiredStateApplier_NoServiceImageEnv(t *testing.T) {
	dir := setupBahiaOwnedDir(t)

	// Track env vars passed to commands.
	runner := &composeDirRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			return "", "", nil
		},
	}
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// The live compose file should reference the image directly, not via
	// env substitution like ${WEB_FRONTEND_IMAGE}.
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	data, err := os.ReadFile(liveCompose)
	if err != nil {
		t.Fatalf("read live compose: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "_IMAGE") {
		t.Error("compose file must NOT contain <SERVICE>_IMAGE env var substitution")
	}
	if strings.Contains(content, "${") {
		t.Error("compose file must NOT contain environment variable interpolation")
	}
}

// ---------------------------------------------------------------------------
// Tests: Pull policy
// ---------------------------------------------------------------------------

func TestComposeDesiredStateApplier_PullPolicy_Always(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
		PullPolicy:      "always",
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	assertUpCallHasPullFlag(t, runner.calls, "always")
}

func TestComposeDesiredStateApplier_PullPolicy_Never(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
		PullPolicy:      "never",
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	assertUpCallHasPullFlag(t, runner.calls, "never")
}

func TestComposeDesiredStateApplier_PullPolicy_Missing(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
		PullPolicy:      "if-not-present",
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	assertUpCallHasPullFlag(t, runner.calls, "missing")
}

func TestComposeDesiredStateApplier_PullPolicy_Empty_OmitsFlag(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
		PullPolicy:      "",
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// The up command should NOT have --pull flag.
	for _, call := range runner.calls {
		argsStr := strings.Join(call.Args, " ")
		if strings.Contains(argsStr, " up ") {
			if strings.Contains(argsStr, "--pull") {
				t.Error("empty pull policy should NOT add --pull flag")
			}
			return
		}
	}
}

// assertUpCallHasPullFlag checks that the docker compose up command includes
// --pull <expected>.
func assertUpCallHasPullFlag(t *testing.T, calls []mockCall, expected string) {
	t.Helper()
	for _, call := range calls {
		argsStr := strings.Join(call.Args, " ")
		if strings.Contains(argsStr, " up ") {
			if !strings.Contains(argsStr, "--pull") {
				t.Errorf("up command should contain --pull flag")
				return
			}
			if !strings.Contains(argsStr, "--pull "+expected) {
				// Check for adjacent args.
				for i, arg := range call.Args {
					if arg == "--pull" && i+1 < len(call.Args) {
						if call.Args[i+1] != expected {
							t.Errorf("--pull value: want %s, got %s", expected, call.Args[i+1])
						}
						return
					}
				}
				t.Errorf("--pull flag found but value %q not adjacent", expected)
			}
			return
		}
	}
	t.Error("no 'up' command found in calls")
}

// ---------------------------------------------------------------------------
// Tests: Dry run
// ---------------------------------------------------------------------------

func TestComposeDesiredStateApplier_DryRun(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	result, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
		DryRun:          true,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState dry run: %v", err)
	}

	if result.Renderer != "compose" {
		t.Errorf("renderer: want compose, got %s", result.Renderer)
	}
	if result.ExecutionMode != ExecutionModeCLI {
		t.Errorf("execution mode: want %s, got %s", ExecutionModeCLI, result.ExecutionMode)
	}
	if len(result.Warnings) == 0 {
		t.Error("dry run should include a warning")
	}

	// No `up` command should have been executed.
	for _, call := range runner.calls {
		argsStr := strings.Join(call.Args, " ")
		if strings.Contains(argsStr, " up ") {
			t.Error("dry run should NOT execute docker compose up")
		}
	}

	// Live compose file should NOT have been modified.
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	if _, err := os.Stat(liveCompose); err == nil {
		t.Error("dry run should not write live compose file")
	}
}

// ---------------------------------------------------------------------------
// Tests: Error paths
// ---------------------------------------------------------------------------

func TestComposeDesiredStateApplier_NilPlan(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService: &domain.DesiredServiceSpec{},
	})
	if err == nil {
		t.Fatal("expected error for nil plan")
	}
	if !strings.Contains(err.Error(), "environment plan is nil") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestComposeDesiredStateApplier_NilTargetService(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
	})
	if err == nil {
		t.Fatal("expected error for nil target service")
	}
	if !strings.Contains(err.Error(), "target service is nil") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestComposeDesiredStateApplier_OwnershipFailure(t *testing.T) {
	// Use a directory without .bahia/ marker — ownership check should fail.
	dir := t.TempDir()
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err == nil {
		t.Fatal("expected ownership error")
	}
	if !strings.Contains(err.Error(), "ownership") {
		t.Errorf("expected ownership error, got: %v", err)
	}

	// No commands should have been executed.
	if len(runner.calls) != 0 {
		t.Errorf("no commands should run after ownership failure, got %d calls", len(runner.calls))
	}
}

func TestComposeDesiredStateApplier_ValidationFailure(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := &composeDirRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			argsStr := strings.Join(args, " ")
			if strings.Contains(argsStr, "config") {
				return "", "invalid compose file", fmt.Errorf("exit status 1")
			}
			return "", "", nil
		},
	}
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "stage/validate") {
		t.Errorf("expected stage/validate error, got: %v", err)
	}

	// No `up` command should have been run.
	for _, call := range runner.calls {
		argsStr := strings.Join(call.Args, " ")
		if strings.Contains(argsStr, " up ") {
			t.Error("should NOT run up after validation failure")
		}
	}
}

func TestComposeDesiredStateApplier_UpFailure(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := &composeDirRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			argsStr := strings.Join(args, " ")
			if strings.Contains(argsStr, " up ") {
				return "Error: pull access denied", "", fmt.Errorf("exit status 1")
			}
			return "", "", nil
		},
	}
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err == nil {
		t.Fatal("expected up failure")
	}
	if !strings.Contains(err.Error(), "up failed") {
		t.Errorf("expected up error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Multi-service plan
// ---------------------------------------------------------------------------

func TestComposeDesiredStateApplier_MultiService(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testMultiServicePlan()
	target := &plan.Services[0]

	result, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
		PullPolicy:      "always",
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	if len(result.ResourceNames) != 2 {
		t.Errorf("expected 2 resource names, got %d", len(result.ResourceNames))
	}

	// Verify the live compose file contains both services.
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	data, err := os.ReadFile(liveCompose)
	if err != nil {
		t.Fatalf("read live compose: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "api-server") {
		t.Error("compose should contain api-server service")
	}
	if !strings.Contains(content, "web-frontend") {
		t.Error("compose should contain web-frontend service")
	}
	if !strings.Contains(content, "myapp/api:v2.1") {
		t.Error("compose should contain api image reference")
	}
	if !strings.Contains(content, "myapp/web:v1.5") {
		t.Error("compose should contain web image reference")
	}
}

// ---------------------------------------------------------------------------
// Tests: normalizePullPolicy
// ---------------------------------------------------------------------------

func TestNormalizeComposePullPolicy(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"always", "always"},
		{"ALWAYS", "always"},
		{"never", "never"},
		{"NEVER", "never"},
		{"missing", "missing"},
		{"if-not-present", "missing"},
		{"ifnotpresent", "missing"},
		{"", ""},
		{"unknown", ""},
		{"  always  ", "always"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeComposePullPolicy(tt.input)
			if got != tt.want {
				t.Errorf("normalizePullPolicy(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: ComposeRuntime.SupportsDesiredState
// ---------------------------------------------------------------------------

func TestComposeRuntime_SupportsDesiredState(t *testing.T) {
	logger := zaptest.NewLogger(t)
	rt := NewComposeRuntime(t.TempDir(), logger)

	if !rt.SupportsDesiredState() {
		t.Error("ComposeRuntime should support desired state")
	}
}

// ---------------------------------------------------------------------------
// Tests: Staging cleanup on failure
// ---------------------------------------------------------------------------

func TestComposeDesiredStateApplier_StagingCleanedOnValidationFailure(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := &composeDirRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			argsStr := strings.Join(args, " ")
			if strings.Contains(argsStr, "config") {
				return "", "bad config", fmt.Errorf("exit status 1")
			}
			return "", "", nil
		},
	}
	applier := newTestApplier(t, dir, runner)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, _ = applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})

	// Staging directory should be cleaned up after failure.
	stagingDir := filepath.Join(dir, ".bahia", "staging")
	if _, err := os.Stat(stagingDir); !os.IsNotExist(err) {
		t.Error("staging directory should be cleaned up after validation failure")
	}
}
