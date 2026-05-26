package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// Mock command runner for testing without Docker
// ---------------------------------------------------------------------------

// mockCommandRunner records calls and returns configured results.
type mockCommandRunner struct {
	calls   []mockCall
	handler func(name string, args []string, dir string) (string, string, error)
}

type mockCall struct {
	Name string
	Args []string
	Dir  string
}

func (r *mockCommandRunner) RunCommand(_ context.Context, name string, args []string, dir string, _ []string) (string, string, error) {
	r.calls = append(r.calls, mockCall{Name: name, Args: args, Dir: dir})
	if r.handler != nil {
		return r.handler(name, args, dir)
	}
	return "", "", nil
}

// successRunner returns a mock runner that always succeeds validation.
func successRunner() *mockCommandRunner {
	return &mockCommandRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			return "", "", nil
		},
	}
}

// failureRunner returns a mock runner that fails validation with a message.
func failureRunner(stderr string) *mockCommandRunner {
	return &mockCommandRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			return "", stderr, fmt.Errorf("exit status 1")
		},
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testRenderResult builds a minimal RenderResult for staging tests.
func testRenderResult() *RenderResult {
	return &RenderResult{
		ComposeYAML: []byte(`name: test-project
services:
  web:
    image: nginx:latest
`),
		EnvMaterial: map[string]string{
			"web": "PORT=8080\nHOST=0.0.0.0\n",
		},
		Metadata: RenderMetadata{
			SchemaVersion: 1,
			Renderer:      "compose",
			RenderedAt:    time.Now().UTC(),
			EnvironmentID: "test-env-id",
			RevisionHash:  "sha256:abc123",
			ProjectName:   "test-project",
			ServiceCount:  1,
			ServiceKeys:   []string{"web"},
			ContentHash:   "sha256:def456",
		},
	}
}

// testRenderResultNoEnv builds a RenderResult with no env material.
func testRenderResultNoEnv() *RenderResult {
	return &RenderResult{
		ComposeYAML: []byte(`name: bare-project
services:
  app:
    image: alpine:latest
`),
		EnvMaterial: map[string]string{},
		Metadata: RenderMetadata{
			SchemaVersion: 1,
			Renderer:      "compose",
			RenderedAt:    time.Now().UTC(),
			EnvironmentID: "test-env-bare",
			RevisionHash:  "sha256:bare123",
			ProjectName:   "bare-project",
			ServiceCount:  1,
			ServiceKeys:   []string{"app"},
			ContentHash:   "sha256:bare456",
		},
	}
}

// setupComposeDir creates a temporary directory simulating a Bahia-owned
// compose project.
func setupComposeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Create .bahia/ marker with render-state.json so it looks Bahia-owned.
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

// setupComposeDirWithLiveFiles creates a temp dir with existing live files
// to test that promotion replaces them and failure preserves them.
func setupComposeDirWithLiveFiles(t *testing.T) (string, string, string) {
	t.Helper()
	dir := setupComposeDir(t)

	// Write existing live compose file.
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	liveContent := "name: old-project\nservices:\n  old-svc:\n    image: old:v1\n"
	if err := os.WriteFile(liveCompose, []byte(liveContent), 0o644); err != nil {
		t.Fatalf("write live compose: %v", err)
	}

	// Write existing live render-state.json.
	liveMetadata := filepath.Join(dir, ".bahia", "render-state.json")
	oldMeta := `{"schema_version": 1, "renderer": "compose", "project_name": "old-project"}`
	if err := os.WriteFile(liveMetadata, []byte(oldMeta), 0o644); err != nil {
		t.Fatalf("write live metadata: %v", err)
	}

	// Write existing live env file.
	envDir := filepath.Join(dir, ".bahia", "env")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatalf("create env dir: %v", err)
	}
	liveEnvFile := filepath.Join(envDir, "web.env")
	if err := os.WriteFile(liveEnvFile, []byte("OLD_VAR=old_value\n"), 0o600); err != nil {
		t.Fatalf("write live env: %v", err)
	}

	return dir, liveContent, oldMeta
}

// ---------------------------------------------------------------------------
// Tests: StageAndValidate
// ---------------------------------------------------------------------------

func TestComposeStagingManager_StageAndValidate_Success(t *testing.T) {
	dir := setupComposeDir(t)
	runner := successRunner()
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}

	// Check staged files exist.
	if !staged.Validated {
		t.Error("expected Validated=true")
	}
	if staged.StagingDir == "" {
		t.Fatal("StagingDir should not be empty")
	}

	// Verify compose file was staged.
	data, err := os.ReadFile(staged.ComposeFile)
	if err != nil {
		t.Fatalf("read staged compose: %v", err)
	}
	if string(data) != string(result.ComposeYAML) {
		t.Error("staged compose content does not match render result")
	}

	// Verify env file was staged.
	envData, err := os.ReadFile(staged.EnvFiles["web"])
	if err != nil {
		t.Fatalf("read staged env: %v", err)
	}
	if string(envData) != result.EnvMaterial["web"] {
		t.Error("staged env content does not match")
	}

	// Verify env file permissions are restricted.
	info, err := os.Stat(staged.EnvFiles["web"])
	if err != nil {
		t.Fatalf("stat env file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("env file permissions: want 0600, got %o", info.Mode().Perm())
	}

	// Verify metadata was staged.
	metaData, err := os.ReadFile(staged.MetadataFile)
	if err != nil {
		t.Fatalf("read staged metadata: %v", err)
	}
	if !isValidJSON(metaData) {
		t.Error("staged metadata is not valid JSON")
	}

	// Verify docker compose config -q was called.
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 command call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Name != "docker" {
		t.Errorf("expected command 'docker', got %q", call.Name)
	}
	// Should include compose, -f, <path>, config, -q.
	argsStr := strings.Join(call.Args, " ")
	if !strings.Contains(argsStr, "compose") || !strings.Contains(argsStr, "config") || !strings.Contains(argsStr, "-q") {
		t.Errorf("expected 'compose ... config -q' in args, got %v", call.Args)
	}
	if !strings.Contains(argsStr, staged.ComposeFile) {
		t.Errorf("expected staged compose file path in args, got %v", call.Args)
	}
}

func TestComposeStagingManager_StageAndValidate_NoEnvMaterial(t *testing.T) {
	dir := setupComposeDir(t)
	runner := successRunner()
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResultNoEnv()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}

	if !staged.Validated {
		t.Error("expected Validated=true")
	}
	if len(staged.EnvFiles) != 0 {
		t.Errorf("expected no env files, got %d", len(staged.EnvFiles))
	}

	// Staging env directory should not exist.
	envDir := filepath.Join(staged.StagingDir, bahiaEnvDir)
	if _, err := os.Stat(envDir); !os.IsNotExist(err) {
		t.Error("env dir should not exist when no env material")
	}
}

func TestComposeStagingManager_StageAndValidate_ValidationFailure(t *testing.T) {
	dir := setupComposeDir(t)
	runner := failureRunner("service 'web' has invalid port mapping")
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err == nil {
		t.Fatal("expected validation error")
	}

	// Error should contain the detail.
	if !strings.Contains(err.Error(), "invalid port mapping") {
		t.Errorf("expected error detail, got: %v", err)
	}

	// Staged files should still be on disk (caller decides cleanup).
	if staged == nil {
		t.Fatal("staged should not be nil even on validation failure")
	}
	if staged.Validated {
		t.Error("expected Validated=false on failure")
	}

	// Staged compose file should exist (written before validation).
	if _, statErr := os.Stat(staged.ComposeFile); os.IsNotExist(statErr) {
		t.Error("staged compose file should still exist after validation failure")
	}
}

func TestComposeStagingManager_StageAndValidate_NilResult(t *testing.T) {
	dir := setupComposeDir(t)
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, successRunner())

	_, err := mgr.StageAndValidate(context.Background(), dir, nil)
	if err == nil {
		t.Fatal("expected error for nil result")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("expected nil-related error, got: %v", err)
	}
}

func TestComposeStagingManager_StageAndValidate_CleansExistingStaging(t *testing.T) {
	dir := setupComposeDir(t)
	runner := successRunner()
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	// Pre-create a stale staging directory with old files.
	stagingDir := filepath.Join(dir, ".bahia", "staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatalf("create stale staging: %v", err)
	}
	staleFile := filepath.Join(stagingDir, "stale-artifact.txt")
	if err := os.WriteFile(staleFile, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}

	// Stale file should be gone.
	if _, statErr := os.Stat(staleFile); !os.IsNotExist(statErr) {
		t.Error("stale file should have been cleaned up")
	}

	// New staged files should exist.
	if _, statErr := os.Stat(staged.ComposeFile); os.IsNotExist(statErr) {
		t.Error("new staged compose file should exist")
	}
}

func TestComposeStagingManager_StageAndValidate_StagingLayout(t *testing.T) {
	dir := setupComposeDir(t)
	runner := successRunner()
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}

	absDir, _ := filepath.Abs(dir)

	// Staging dir should be under .bahia/staging/.
	expectedStagingDir := filepath.Join(absDir, ".bahia", "staging")
	if staged.StagingDir != expectedStagingDir {
		t.Errorf("staging dir: want %s, got %s", expectedStagingDir, staged.StagingDir)
	}

	// Staged compose file should be in staging dir.
	if staged.ComposeFile != filepath.Join(expectedStagingDir, "docker-compose.yml") {
		t.Errorf("staged compose file path wrong: %s", staged.ComposeFile)
	}

	// Live compose file should be at project root.
	if staged.LiveComposeFile != filepath.Join(absDir, "docker-compose.yml") {
		t.Errorf("live compose file path wrong: %s", staged.LiveComposeFile)
	}

	// Live metadata should be under .bahia/.
	if staged.LiveMetadataFile != filepath.Join(absDir, ".bahia", "render-state.json") {
		t.Errorf("live metadata path wrong: %s", staged.LiveMetadataFile)
	}

	// Live env file should be under .bahia/env/.
	if staged.LiveEnvFiles["web"] != filepath.Join(absDir, ".bahia", "env", "web.env") {
		t.Errorf("live env file path wrong: %s", staged.LiveEnvFiles["web"])
	}
}

// ---------------------------------------------------------------------------
// Tests: Promote
// ---------------------------------------------------------------------------

func TestComposeStagingManager_Promote_Success(t *testing.T) {
	dir := setupComposeDir(t)
	runner := successRunner()
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}

	err = mgr.Promote(context.Background(), staged)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Live compose file should have new content.
	data, err := os.ReadFile(staged.LiveComposeFile)
	if err != nil {
		t.Fatalf("read live compose: %v", err)
	}
	if string(data) != string(result.ComposeYAML) {
		t.Error("live compose content does not match rendered output")
	}

	// Live env file should have new content.
	envData, err := os.ReadFile(staged.LiveEnvFiles["web"])
	if err != nil {
		t.Fatalf("read live env: %v", err)
	}
	if string(envData) != result.EnvMaterial["web"] {
		t.Error("live env content does not match")
	}

	// Live metadata should exist and be valid JSON.
	metaData, err := os.ReadFile(staged.LiveMetadataFile)
	if err != nil {
		t.Fatalf("read live metadata: %v", err)
	}
	if !isValidJSON(metaData) {
		t.Error("live metadata is not valid JSON")
	}

	// Staging directory should be cleaned up.
	if _, statErr := os.Stat(staged.StagingDir); !os.IsNotExist(statErr) {
		t.Error("staging directory should be removed after promotion")
	}
}

func TestComposeStagingManager_Promote_ReplacesExistingLiveFiles(t *testing.T) {
	dir, oldCompose, _ := setupComposeDirWithLiveFiles(t)
	runner := successRunner()
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}

	// Verify old content is still live before promotion.
	preData, _ := os.ReadFile(staged.LiveComposeFile)
	if string(preData) != oldCompose {
		t.Error("live compose should still have old content before promote")
	}

	err = mgr.Promote(context.Background(), staged)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Now live should have new content.
	postData, _ := os.ReadFile(staged.LiveComposeFile)
	if string(postData) != string(result.ComposeYAML) {
		t.Error("live compose should have new content after promote")
	}
}

func TestComposeStagingManager_Promote_RejectsUnvalidated(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, successRunner())

	staged := &StagedFiles{
		ComposeDir:  "/tmp/test",
		Validated:   false,
		ComposeFile: "/tmp/test/staged.yml",
	}

	err := mgr.Promote(context.Background(), staged)
	if err == nil {
		t.Fatal("expected error for unvalidated staged files")
	}
	if !strings.Contains(err.Error(), "not been validated") {
		t.Errorf("expected validation error, got: %v", err)
	}
}

func TestComposeStagingManager_Promote_NilStaged(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, successRunner())

	err := mgr.Promote(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil staged files")
	}
}

// ---------------------------------------------------------------------------
// Tests: Rollback
// ---------------------------------------------------------------------------

func TestComposeStagingManager_Rollback_CleansStaging(t *testing.T) {
	dir := setupComposeDir(t)
	runner := failureRunner("some validation error")
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err == nil {
		t.Fatal("expected validation failure")
	}

	// Staging dir should exist before rollback.
	if _, statErr := os.Stat(staged.StagingDir); os.IsNotExist(statErr) {
		t.Fatal("staging dir should exist before rollback")
	}

	mgr.Rollback(context.Background(), staged)

	// Staging dir should be gone after rollback.
	if _, statErr := os.Stat(staged.StagingDir); !os.IsNotExist(statErr) {
		t.Error("staging dir should be removed after rollback")
	}
}

func TestComposeStagingManager_Rollback_NilStaged(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, successRunner())

	// Should not panic.
	mgr.Rollback(context.Background(), nil)
}

func TestComposeStagingManager_Rollback_EmptyStagingDir(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, successRunner())

	// Should not panic.
	mgr.Rollback(context.Background(), &StagedFiles{})
}

// ---------------------------------------------------------------------------
// Tests: Validation failure preserves live files
// ---------------------------------------------------------------------------

func TestComposeStagingManager_ValidationFailure_PreservesLiveFiles(t *testing.T) {
	dir, oldCompose, oldMeta := setupComposeDirWithLiveFiles(t)
	runner := failureRunner("services.web references undefined network")
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err == nil {
		t.Fatal("expected validation failure")
	}

	// Roll back staging.
	mgr.Rollback(context.Background(), staged)

	// Live compose file should be untouched.
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	data, readErr := os.ReadFile(liveCompose)
	if readErr != nil {
		t.Fatalf("read live compose: %v", readErr)
	}
	if string(data) != oldCompose {
		t.Error("live compose file was modified despite validation failure")
	}

	// Live metadata should be untouched.
	liveMetadata := filepath.Join(dir, ".bahia", "render-state.json")
	metaData, readErr := os.ReadFile(liveMetadata)
	if readErr != nil {
		t.Fatalf("read live metadata: %v", readErr)
	}
	if string(metaData) != oldMeta {
		t.Error("live metadata was modified despite validation failure")
	}

	// Live env file should be untouched.
	liveEnv := filepath.Join(dir, ".bahia", "env", "web.env")
	envData, readErr := os.ReadFile(liveEnv)
	if readErr != nil {
		t.Fatalf("read live env: %v", readErr)
	}
	if string(envData) != "OLD_VAR=old_value\n" {
		t.Error("live env file was modified despite validation failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: Full workflow (stage → validate → promote → verify)
// ---------------------------------------------------------------------------

func TestComposeStagingManager_FullWorkflow(t *testing.T) {
	dir, oldCompose, _ := setupComposeDirWithLiveFiles(t)
	runner := successRunner()
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResult()

	// Stage and validate.
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}

	// Live files should still have old content.
	liveData, _ := os.ReadFile(staged.LiveComposeFile)
	if string(liveData) != oldCompose {
		t.Error("live files modified before promote")
	}

	// Promote.
	if err := mgr.Promote(context.Background(), staged); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Live files should now have new content.
	liveData, _ = os.ReadFile(staged.LiveComposeFile)
	if string(liveData) != string(result.ComposeYAML) {
		t.Error("live compose not updated after promote")
	}

	// Staging should be cleaned up.
	if _, statErr := os.Stat(staged.StagingDir); !os.IsNotExist(statErr) {
		t.Error("staging dir should be removed after promote")
	}

	// The .bahia/ marker should still exist (it's the parent, not staging).
	bahiaDir := filepath.Join(dir, ".bahia")
	if _, statErr := os.Stat(bahiaDir); os.IsNotExist(statErr) {
		t.Error(".bahia/ marker dir should still exist after promote")
	}
}

func TestComposeStagingManager_FullWorkflow_FailureThenSuccess(t *testing.T) {
	dir, oldCompose, _ := setupComposeDirWithLiveFiles(t)
	logger := zaptest.NewLogger(t)

	// First attempt: validation fails.
	failRunner := failureRunner("invalid service definition")
	mgr1 := NewComposeStagingManagerWithRunner(logger, failRunner)

	result := testRenderResult()
	staged1, err := mgr1.StageAndValidate(context.Background(), dir, result)
	if err == nil {
		t.Fatal("expected first attempt to fail")
	}
	mgr1.Rollback(context.Background(), staged1)

	// Live files still have old content.
	liveData, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if string(liveData) != oldCompose {
		t.Error("live files modified after failed attempt")
	}

	// Second attempt: validation succeeds.
	okRunner := successRunner()
	mgr2 := NewComposeStagingManagerWithRunner(logger, okRunner)

	staged2, err := mgr2.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("second StageAndValidate: %v", err)
	}

	if err := mgr2.Promote(context.Background(), staged2); err != nil {
		t.Fatalf("second Promote: %v", err)
	}

	// Now live files have new content.
	liveData, _ = os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if string(liveData) != string(result.ComposeYAML) {
		t.Error("live compose not updated after second (successful) attempt")
	}
}

// ---------------------------------------------------------------------------
// Tests: Multiple env files
// ---------------------------------------------------------------------------

func TestComposeStagingManager_MultipleEnvFiles(t *testing.T) {
	dir := setupComposeDir(t)
	runner := successRunner()
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := &RenderResult{
		ComposeYAML: []byte(`name: multi-env
services:
  api:
    image: api:latest
  web:
    image: web:latest
  worker:
    image: worker:latest
`),
		EnvMaterial: map[string]string{
			"api":    "DB_URL=postgres://...\n",
			"web":    "PORT=3000\n",
			"worker": "QUEUE=default\nCONCURRENCY=5\n",
		},
		Metadata: RenderMetadata{
			SchemaVersion: 1,
			Renderer:      "compose",
			RenderedAt:    time.Now().UTC(),
			EnvironmentID: "multi-env",
			ProjectName:   "multi-env",
			ServiceCount:  3,
			ServiceKeys:   []string{"api", "web", "worker"},
			ContentHash:   "sha256:multi",
		},
	}

	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}

	if len(staged.EnvFiles) != 3 {
		t.Errorf("expected 3 env files, got %d", len(staged.EnvFiles))
	}

	for _, svcKey := range []string{"api", "web", "worker"} {
		path, ok := staged.EnvFiles[svcKey]
		if !ok {
			t.Errorf("missing staged env file for %s", svcKey)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read staged env %s: %v", svcKey, err)
			continue
		}
		if string(data) != result.EnvMaterial[svcKey] {
			t.Errorf("env content mismatch for %s", svcKey)
		}
	}

	// Promote and verify all env files are in live locations.
	if err := mgr.Promote(context.Background(), staged); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	for _, svcKey := range []string{"api", "web", "worker"} {
		livePath := staged.LiveEnvFiles[svcKey]
		data, err := os.ReadFile(livePath)
		if err != nil {
			t.Errorf("read live env %s: %v", svcKey, err)
			continue
		}
		if string(data) != result.EnvMaterial[svcKey] {
			t.Errorf("live env content mismatch for %s", svcKey)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: NewComposeStagingManager (production constructor)
// ---------------------------------------------------------------------------

func TestNewComposeStagingManager_UsesExecRunner(t *testing.T) {
	logger := zap.NewNop()
	mgr := NewComposeStagingManager(logger)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if mgr.runner == nil {
		t.Fatal("expected non-nil runner")
	}
	// Verify it's the exec runner type.
	if _, ok := mgr.runner.(*execCommandRunner); !ok {
		t.Error("expected execCommandRunner for production constructor")
	}
}

// ---------------------------------------------------------------------------
// Tests: StagedAt timestamp
// ---------------------------------------------------------------------------

func TestComposeStagingManager_StagedAt(t *testing.T) {
	dir := setupComposeDir(t)
	runner := successRunner()
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	before := time.Now().UTC()
	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}
	after := time.Now().UTC()

	if staged.StagedAt.Before(before) || staged.StagedAt.After(after) {
		t.Errorf("StagedAt %v not between %v and %v", staged.StagedAt, before, after)
	}
}
