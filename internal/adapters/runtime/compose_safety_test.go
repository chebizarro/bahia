package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap/zaptest"
)

// ===========================================================================
// Compose Renderer / Apply Safety Tests
//
// These tests verify four safety guarantees:
//   1. Render determinism   — same input always produces identical output
//   2. Staging isolation    — failures never corrupt live files
//   3. Validation-before-promote — unvalidated staged files cannot be promoted
//   4. No --force-recreate  — compose up never uses --force-recreate
// ===========================================================================

// ---------------------------------------------------------------------------
// 1. Render Determinism
// ---------------------------------------------------------------------------

// TestSafety_RenderDeterminism_RepeatedRenders verifies that rendering the
// same plan 10 times produces byte-identical YAML, identical content hashes,
// and identical env material every time.
func TestSafety_RenderDeterminism_RepeatedRenders(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	var refYAML []byte
	var refHash string
	var refEnv map[string]string

	for i := 0; i < 10; i++ {
		result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
		if err != nil {
			t.Fatalf("render iteration %d: %v", i, err)
		}

		if i == 0 {
			refYAML = result.ComposeYAML
			refHash = result.Metadata.ContentHash
			refEnv = result.EnvMaterial
			continue
		}

		if string(result.ComposeYAML) != string(refYAML) {
			t.Errorf("iteration %d: ComposeYAML differs from first render", i)
		}
		if result.Metadata.ContentHash != refHash {
			t.Errorf("iteration %d: ContentHash %q != %q", i, result.Metadata.ContentHash, refHash)
		}
		for key, val := range refEnv {
			if result.EnvMaterial[key] != val {
				t.Errorf("iteration %d: env material for %q differs", i, key)
			}
		}
	}
}

// TestSafety_RenderDeterminism_ContentHashMatchesYAML verifies the content
// hash recorded in metadata is genuinely sha256 of the rendered YAML.
func TestSafety_RenderDeterminism_ContentHashMatchesYAML(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	expected := fmt.Sprintf("sha256:%x", sha256.Sum256(result.ComposeYAML))
	if result.Metadata.ContentHash != expected {
		t.Errorf("content hash mismatch:\n  metadata: %s\n  computed: %s", result.Metadata.ContentHash, expected)
	}
}

// TestSafety_RenderDeterminism_ServiceOrderIndependence verifies that the
// renderer produces the same output regardless of the initial service order
// in the plan.
func TestSafety_RenderDeterminism_ServiceOrderIndependence(t *testing.T) {
	renderer := NewComposeRenderer()

	// Build plan with services in original order.
	planA := testPlan()
	resultA, err := renderer.RenderEnvironmentPlan(context.Background(), planA)
	if err != nil {
		t.Fatalf("render planA: %v", err)
	}

	// Build plan with services reversed.
	planB := testPlan()
	// Reverse the service slice.
	for i, j := 0, len(planB.Services)-1; i < j; i, j = i+1, j-1 {
		planB.Services[i], planB.Services[j] = planB.Services[j], planB.Services[i]
	}
	resultB, err := renderer.RenderEnvironmentPlan(context.Background(), planB)
	if err != nil {
		t.Fatalf("render planB: %v", err)
	}

	if string(resultA.ComposeYAML) != string(resultB.ComposeYAML) {
		t.Error("render output differs when services are provided in reversed order")
		t.Logf("planA:\n%s", resultA.ComposeYAML)
		t.Logf("planB:\n%s", resultB.ComposeYAML)
	}
	if resultA.Metadata.ContentHash != resultB.Metadata.ContentHash {
		t.Errorf("content hash differs for reversed service order: %s vs %s",
			resultA.Metadata.ContentHash, resultB.Metadata.ContentHash)
	}
}

// TestSafety_RenderDeterminism_MapKeyStability verifies that maps with
// multiple keys (env, labels, secrets) produce sorted output.
func TestSafety_RenderDeterminism_MapKeyStability(t *testing.T) {
	renderer := NewComposeRenderer()

	// Plan with many env vars and labels to stress map ordering.
	envID := fixedUUID("env-mapstab0")
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services: []domain.DesiredServiceSpec{
			{
				SchemaVersion:    domain.DesiredStateSchemaVersion,
				ServiceID:        fixedUUID("svc-mapstab0"),
				EnvironmentID:    envID,
				ArtifactID:       fixedUUID("art-mapstab0"),
				StableServiceKey: "map-stability-svc",
				ImageRef:         "test:latest",
				Env: map[string]string{
					"ZEBRA": "z", "ALPHA": "a", "MIDDLE": "m",
					"BETA": "b", "OMEGA": "o",
				},
				Labels: map[string]string{
					"z.label": "z", "a.label": "a", "m.label": "m",
				},
				SecretRefs: []domain.DesiredSecretRef{
					{EnvVar: "Z_SECRET", Name: "z-sec", SecretID: fixedUUID("sec-z000001"), RedactedValue: "REDACTED(z-sec)"},
					{EnvVar: "A_SECRET", Name: "a-sec", SecretID: fixedUUID("sec-a000001"), RedactedValue: "REDACTED(a-sec)"},
				},
			},
		},
	}
	for i := range plan.Services {
		plan.Services[i].ComputeDesiredHash()
	}
	plan.ComputeRevisionHash()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	yaml := string(result.ComposeYAML)

	// Env keys should appear sorted in YAML.
	alphaIdx := strings.Index(yaml, "ALPHA")
	betaIdx := strings.Index(yaml, "BETA")
	middleIdx := strings.Index(yaml, "MIDDLE")
	omegaIdx := strings.Index(yaml, "OMEGA")
	zebraIdx := strings.Index(yaml, "ZEBRA")
	if alphaIdx < 0 || betaIdx < 0 || middleIdx < 0 || omegaIdx < 0 || zebraIdx < 0 {
		t.Fatal("not all env keys found in YAML")
	}
	if !(alphaIdx < betaIdx && betaIdx < middleIdx && middleIdx < omegaIdx && omegaIdx < zebraIdx) {
		t.Error("env keys are not in sorted order in rendered YAML")
	}

	// Label keys should appear sorted.
	aLabelIdx := strings.Index(yaml, "a.label")
	mLabelIdx := strings.Index(yaml, "m.label")
	zLabelIdx := strings.Index(yaml, "z.label")
	if aLabelIdx < 0 || mLabelIdx < 0 || zLabelIdx < 0 {
		t.Fatal("not all label keys found in YAML")
	}
	if !(aLabelIdx < mLabelIdx && mLabelIdx < zLabelIdx) {
		t.Error("label keys are not in sorted order in rendered YAML")
	}

	// Secret refs in env material should appear sorted by env var.
	envMat := result.EnvMaterial["map-stability-svc"]
	aSecIdx := strings.Index(envMat, "A_SECRET")
	zSecIdx := strings.Index(envMat, "Z_SECRET")
	if aSecIdx < 0 || zSecIdx < 0 {
		t.Fatal("not all secret env vars found in env material")
	}
	if aSecIdx >= zSecIdx {
		t.Error("secret refs are not sorted by env var in env material")
	}
}

// ---------------------------------------------------------------------------
// 2. Staging Isolation
// ---------------------------------------------------------------------------

// TestSafety_StagingIsolation_ValidationFailurePreservesAllLiveFiles tests
// that when validation fails, all live files (compose, metadata, env) remain
// completely untouched.
func TestSafety_StagingIsolation_ValidationFailurePreservesAllLiveFiles(t *testing.T) {
	dir, oldCompose, oldMeta := setupComposeDirWithLiveFiles(t)

	// Read the old env file content for comparison.
	oldEnv, err := os.ReadFile(filepath.Join(dir, ".bahia", "env", "web.env"))
	if err != nil {
		t.Fatalf("read old env: %v", err)
	}

	runner := failureRunner("validation: network 'missing-net' not declared")
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	result := testRenderResult()
	staged, stageErr := mgr.StageAndValidate(context.Background(), dir, result)
	if stageErr == nil {
		t.Fatal("expected validation failure")
	}

	// Rollback staging.
	mgr.Rollback(context.Background(), staged)

	// Assert every live file is byte-identical to before staging.
	assertFileContent(t, filepath.Join(dir, "docker-compose.yml"), oldCompose, "live compose")
	assertFileContent(t, filepath.Join(dir, ".bahia", "render-state.json"), oldMeta, "live metadata")
	assertFileContent(t, filepath.Join(dir, ".bahia", "env", "web.env"), string(oldEnv), "live env")
}

// TestSafety_StagingIsolation_StagingDirRemovedAfterRollback verifies no
// staging artifacts remain after rollback.
func TestSafety_StagingIsolation_StagingDirRemovedAfterRollback(t *testing.T) {
	dir := setupComposeDir(t)
	runner := failureRunner("some error")
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, runner)

	staged, _ := mgr.StageAndValidate(context.Background(), dir, testRenderResult())
	mgr.Rollback(context.Background(), staged)

	// No staging directory should remain.
	stagingDir := filepath.Join(dir, ".bahia", "staging")
	if _, err := os.Stat(stagingDir); !os.IsNotExist(err) {
		t.Error("staging directory should be completely removed after rollback")
	}
}

// TestSafety_StagingIsolation_ConcurrentFailDoesNotLeakPartialState verifies
// that a second stage+validate after a failed first one starts clean.
func TestSafety_StagingIsolation_SequentialFailThenSuccess(t *testing.T) {
	dir, _, _ := setupComposeDirWithLiveFiles(t)
	logger := zaptest.NewLogger(t)

	// First attempt fails.
	mgr1 := NewComposeStagingManagerWithRunner(logger, failureRunner("bad first attempt"))
	staged1, _ := mgr1.StageAndValidate(context.Background(), dir, testRenderResult())
	mgr1.Rollback(context.Background(), staged1)

	// Second attempt succeeds — staging area should be clean.
	mgr2 := NewComposeStagingManagerWithRunner(logger, successRunner())
	staged2, err := mgr2.StageAndValidate(context.Background(), dir, testRenderResult())
	if err != nil {
		t.Fatalf("second StageAndValidate should succeed: %v", err)
	}
	if !staged2.Validated {
		t.Error("second staging should be validated")
	}

	// No stale artifacts from first attempt.
	entries, _ := os.ReadDir(staged2.StagingDir)
	for _, e := range entries {
		if e.Name() == "stale-artifact.txt" {
			t.Error("stale artifact leaked from failed first staging")
		}
	}
}

// TestSafety_StagingIsolation_ApplierRollsBackOnValidationFailure ensures
// the full applier (not just staging manager) cleans up on validation failure.
func TestSafety_StagingIsolation_ApplierRollsBackOnValidationFailure(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := &composeDirRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			argsStr := strings.Join(args, " ")
			if strings.Contains(argsStr, "config") {
				return "", "service references unknown volume", fmt.Errorf("exit status 1")
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

	// Staging dir should be cleaned up by the applier.
	stagingDir := filepath.Join(dir, ".bahia", "staging")
	if _, statErr := os.Stat(stagingDir); !os.IsNotExist(statErr) {
		t.Error("applier should clean up staging directory after validation failure")
	}

	// No live compose file should have been written.
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	if _, statErr := os.Stat(liveCompose); !os.IsNotExist(statErr) {
		t.Error("no live compose file should exist after validation failure")
	}
}

// ---------------------------------------------------------------------------
// 3. Validation-Before-Promote
// ---------------------------------------------------------------------------

// TestSafety_ValidationBeforePromote_CannotPromoteUnvalidated verifies
// that Promote() refuses to proceed when Validated is false.
func TestSafety_ValidationBeforePromote_CannotPromoteUnvalidated(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, successRunner())

	staged := &StagedFiles{
		ComposeDir:      t.TempDir(),
		Validated:       false,
		ComposeFile:     "/tmp/some/compose.yml",
		LiveComposeFile: "/tmp/some/live.yml",
	}

	err := mgr.Promote(context.Background(), staged)
	if err == nil {
		t.Fatal("Promote must reject unvalidated staged files")
	}
	if !strings.Contains(err.Error(), "not been validated") {
		t.Errorf("expected 'not been validated' error, got: %v", err)
	}
}

// TestSafety_ValidationBeforePromote_NilStagedRejected verifies nil staged
// files are rejected.
func TestSafety_ValidationBeforePromote_NilStagedRejected(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, successRunner())

	err := mgr.Promote(context.Background(), nil)
	if err == nil {
		t.Fatal("Promote must reject nil staged files")
	}
}

// TestSafety_ValidationBeforePromote_ValidatedFlagSetOnlyOnSuccess verifies
// that Validated is only set when validation passes.
func TestSafety_ValidationBeforePromote_ValidatedFlagSetOnlyOnSuccess(t *testing.T) {
	dir := setupComposeDir(t)
	logger := zaptest.NewLogger(t)

	// Failure case.
	failMgr := NewComposeStagingManagerWithRunner(logger, failureRunner("bad config"))
	staged, _ := failMgr.StageAndValidate(context.Background(), dir, testRenderResult())
	if staged != nil && staged.Validated {
		t.Error("Validated must be false when validation fails")
	}
	failMgr.Rollback(context.Background(), staged)

	// Success case.
	okMgr := NewComposeStagingManagerWithRunner(logger, successRunner())
	staged2, err := okMgr.StageAndValidate(context.Background(), dir, testRenderResult())
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}
	if !staged2.Validated {
		t.Error("Validated must be true when validation succeeds")
	}
}

// TestSafety_ValidationBeforePromote_ApplierDryRunDoesNotPromote ensures
// dry-run mode validates but does not promote or run compose up.
func TestSafety_ValidationBeforePromote_ApplierDryRunDoesNotPromote(t *testing.T) {
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
		t.Fatalf("dry-run should succeed: %v", err)
	}

	// Should have a warning about dry-run.
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "dry-run") {
			found = true
			break
		}
	}
	if !found {
		t.Error("dry-run result should contain a dry-run warning")
	}

	// No up command should exist.
	for _, call := range runner.calls {
		argsStr := strings.Join(call.Args, " ")
		if strings.Contains(argsStr, " up ") {
			t.Error("dry-run must NOT execute compose up")
		}
	}

	// No live compose file should exist.
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	if _, statErr := os.Stat(liveCompose); !os.IsNotExist(statErr) {
		t.Error("dry-run must not write live compose file")
	}
}

// ---------------------------------------------------------------------------
// 4. No --force-recreate
// ---------------------------------------------------------------------------

// TestSafety_NoForceRecreate_SingleService verifies that a single-service
// apply never includes --force-recreate in its compose up command.
func TestSafety_NoForceRecreate_SingleService(t *testing.T) {
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

	assertNoForceRecreate(t, runner.calls)
}

// TestSafety_NoForceRecreate_MultiService verifies --force-recreate is absent
// for multi-service plans.
func TestSafety_NoForceRecreate_MultiService(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testMultiServicePlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
		PullPolicy:      "never",
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	assertNoForceRecreate(t, runner.calls)
}

// TestSafety_NoForceRecreate_AllPullPolicies exhaustively checks that no
// pull policy variant introduces --force-recreate.
func TestSafety_NoForceRecreate_AllPullPolicies(t *testing.T) {
	policies := []string{"", "always", "never", "missing", "if-not-present", "ifnotpresent"}

	for _, policy := range policies {
		t.Run("pull_"+policy, func(t *testing.T) {
			dir := setupBahiaOwnedDir(t)
			runner := allSuccessRunner()
			applier := newTestApplier(t, dir, runner)

			plan := testEnvironmentPlan()
			target := &plan.Services[0]

			_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
				EnvironmentPlan: plan,
				TargetService:   target,
				PullPolicy:      policy,
			})
			if err != nil {
				t.Fatalf("ApplyDesiredState: %v", err)
			}

			assertNoForceRecreate(t, runner.calls)
		})
	}
}

// TestSafety_NoForceRecreate_UpCommandStructure verifies the exact structure
// of the compose up command: must include up, -d, --remove-orphans but NOT
// --force-recreate or service-scoped arguments.
func TestSafety_NoForceRecreate_UpCommandStructure(t *testing.T) {
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

	upCall := findUpCall(t, runner.calls)
	argsStr := strings.Join(upCall.Args, " ")

	required := []string{"up", "-d", "--remove-orphans"}
	for _, req := range required {
		if !strings.Contains(argsStr, req) {
			t.Errorf("up command missing required argument %q, got: %v", req, upCall.Args)
		}
	}

	forbidden := []string{"--force-recreate", "--no-deps", "--build"}
	for _, f := range forbidden {
		if strings.Contains(argsStr, f) {
			t.Errorf("up command must NOT contain %q, got: %v", f, upCall.Args)
		}
	}
}

// TestSafety_NoServiceImageEnvOverrides verifies the rendered compose file
// contains no ${SERVICE_IMAGE} style variable interpolation.
func TestSafety_NoServiceImageEnvOverrides(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testMultiServicePlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	liveCompose := filepath.Join(dir, "docker-compose.yml")
	data, err := os.ReadFile(liveCompose)
	if err != nil {
		t.Fatalf("read live compose: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "${") {
		t.Error("live compose must NOT contain variable interpolation (${...})")
	}
	if strings.Contains(content, "_IMAGE") {
		t.Error("live compose must NOT contain <SERVICE>_IMAGE env var pattern")
	}
}

// ---------------------------------------------------------------------------
// Edge Cases & Error Paths
// ---------------------------------------------------------------------------

// TestSafety_ErrorPath_NilPlanRender verifies renderer rejects nil plan.
func TestSafety_ErrorPath_NilPlanRender(t *testing.T) {
	renderer := NewComposeRenderer()
	_, err := renderer.RenderEnvironmentPlan(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil plan")
	}
}

// TestSafety_ErrorPath_EmptyServicesPlan verifies renderer rejects empty services.
func TestSafety_ErrorPath_EmptyServicesPlan(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: uuid.New(),
		Services:      []domain.DesiredServiceSpec{},
	}
	_, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err == nil {
		t.Fatal("expected error for empty services")
	}
}

// TestSafety_ErrorPath_NilRenderResult verifies staging rejects nil result.
func TestSafety_ErrorPath_NilRenderResult(t *testing.T) {
	dir := setupComposeDir(t)
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, successRunner())

	_, err := mgr.StageAndValidate(context.Background(), dir, nil)
	if err == nil {
		t.Fatal("expected error for nil render result")
	}
}

// TestSafety_ErrorPath_ApplierNilPlan verifies applier rejects nil plan.
func TestSafety_ErrorPath_ApplierNilPlan(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	applier := newTestApplier(t, dir, allSuccessRunner())

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		TargetService: &domain.DesiredServiceSpec{},
	})
	if err == nil {
		t.Fatal("expected error for nil plan")
	}
}

// TestSafety_ErrorPath_ApplierNilTarget verifies applier rejects nil target.
func TestSafety_ErrorPath_ApplierNilTarget(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	applier := newTestApplier(t, dir, allSuccessRunner())
	plan := testEnvironmentPlan()

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
	})
	if err == nil {
		t.Fatal("expected error for nil target")
	}
}

// TestSafety_ErrorPath_OwnershipBlocksApply verifies that apply is blocked
// without .bahia ownership marker, and no commands are executed.
func TestSafety_ErrorPath_OwnershipBlocksApply(t *testing.T) {
	dir := t.TempDir() // No .bahia marker.
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
	if len(runner.calls) != 0 {
		t.Errorf("no commands should run after ownership failure, got %d", len(runner.calls))
	}
}

// TestSafety_ErrorPath_UpFailureDoesNotRollbackPromotedFiles verifies that
// when compose up fails after promotion, the promoted files remain in place
// (they were already validated).
func TestSafety_ErrorPath_UpFailurePreservesPromotedFiles(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := &composeDirRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			argsStr := strings.Join(args, " ")
			if strings.Contains(argsStr, " up ") {
				return "", "pull access denied", fmt.Errorf("exit status 1")
			}
			return "", "", nil // config validation passes
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

	// Live compose file should exist (promoted before up failed).
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	data, readErr := os.ReadFile(liveCompose)
	if readErr != nil {
		t.Fatalf("live compose should exist after up failure: %v", readErr)
	}
	if !strings.Contains(string(data), "nginx:1.25") {
		t.Error("promoted compose file should contain rendered content")
	}
}

// TestSafety_EnvFilePermissions verifies that staged env files have
// restricted permissions (0600) to protect secrets.
func TestSafety_EnvFilePermissions(t *testing.T) {
	dir := setupComposeDir(t)
	logger := zaptest.NewLogger(t)
	mgr := NewComposeStagingManagerWithRunner(logger, successRunner())

	result := testRenderResult()
	staged, err := mgr.StageAndValidate(context.Background(), dir, result)
	if err != nil {
		t.Fatalf("StageAndValidate: %v", err)
	}

	for svcKey, path := range staged.EnvFiles {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Errorf("stat env file %s: %v", svcKey, statErr)
			continue
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("env file %s has permissions %o, want 0600", svcKey, info.Mode().Perm())
		}
	}
}

// TestSafety_SecretsNeverInYAMLOrMetadata verifies secrets don't leak into
// rendered YAML or metadata JSON.
func TestSafety_SecretsNeverInYAMLOrMetadata(t *testing.T) {
	renderer := NewComposeRenderer()
	plan := testPlan()

	result, err := renderer.RenderEnvironmentPlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	yaml := string(result.ComposeYAML)
	metaJSON, _ := result.Metadata.MetadataJSON()
	meta := string(metaJSON)

	secrets := []string{"DB_PASSWORD", "API_KEY", "SESSION_SECRET", "REDACTED"}
	for _, s := range secrets {
		if strings.Contains(yaml, s) {
			t.Errorf("secret-related string %q must NOT appear in Compose YAML", s)
		}
		if strings.Contains(meta, s) {
			t.Errorf("secret-related string %q must NOT appear in metadata JSON", s)
		}
	}
}

// TestSafety_FullProjectApply_NoServiceScopedUp verifies the up command
// applies to the full project, not a specific service.
func TestSafety_FullProjectApply_NoServiceScopedUp(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()
	applier := newTestApplier(t, dir, runner)

	plan := testMultiServicePlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	upCall := findUpCall(t, runner.calls)

	// After "up", the only args should be flags, not service names.
	serviceNames := []string{"api-server", "web-frontend"}
	for _, sn := range serviceNames {
		for _, arg := range upCall.Args {
			if arg == sn {
				t.Errorf("up command must NOT target specific service %q (full-project apply)", sn)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// assertNoForceRecreate checks that no call in the list contains --force-recreate.
func assertNoForceRecreate(t *testing.T, calls []mockCall) {
	t.Helper()
	for _, call := range calls {
		for _, arg := range call.Args {
			if arg == "--force-recreate" {
				t.Errorf("command %q must NOT contain --force-recreate, args: %v", call.Name, call.Args)
			}
		}
	}
}

// findUpCall locates the compose up call in the recorded commands.
func findUpCall(t *testing.T, calls []mockCall) mockCall {
	t.Helper()
	for _, call := range calls {
		argsStr := strings.Join(call.Args, " ")
		if strings.Contains(argsStr, " up ") || strings.Contains(argsStr, " up") {
			return call
		}
	}
	t.Fatal("no 'up' command found in recorded calls")
	return mockCall{}
}

// assertFileContent reads a file and asserts its content matches expected.
func assertFileContent(t *testing.T, path, expected, label string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (%s): %v", label, path, err)
	}
	if string(data) != expected {
		t.Errorf("%s was modified (should be unchanged):\n  want: %q\n  got:  %q", label, expected, string(data))
	}
}
