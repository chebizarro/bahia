package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestApplierWithFragmentHooks creates a ComposeDesiredStateApplier with
// injectable fragment eligibility and renderer functions. Pass nil to use the
// production implementations.
func newTestApplierWithFragmentHooks(
	t *testing.T,
	dir string,
	runner *composeDirRunner,
	eligFn func(*domain.DesiredEnvironmentPlan, *domain.DesiredServiceSpec, *RenderMetadata) *FragmentEligibility,
	rendFn func(string, domain.DesiredServiceSpec) (*FragmentLayout, error),
) *ComposeDesiredStateApplier {
	t.Helper()
	a := newTestApplier(t, dir, runner)
	a.fragmentEligibilityFn = eligFn
	a.fragmentRendererFn = rendFn
	return a
}

// alwaysEligibleFn returns a fragment eligibility function that always
// returns eligible.
func alwaysEligibleFn() func(*domain.DesiredEnvironmentPlan, *domain.DesiredServiceSpec, *RenderMetadata) *FragmentEligibility {
	return func(_ *domain.DesiredEnvironmentPlan, _ *domain.DesiredServiceSpec, _ *RenderMetadata) *FragmentEligibility {
		return &FragmentEligibility{Eligible: true, Reason: "test: always eligible"}
	}
}

// testFragmentRendererFn returns a fragment renderer function that emits
// minimal valid Compose fragment YAML for the given service key.
func testFragmentRendererFn(serviceKey string, imageRef string) func(string, domain.DesiredServiceSpec) (*FragmentLayout, error) {
	yaml := []byte("name: test\nservices:\n  " + serviceKey + ":\n    image: " + imageRef + "\n")
	return func(_ string, _ domain.DesiredServiceSpec) (*FragmentLayout, error) {
		return &FragmentLayout{ServiceKey: serviceKey, FragmentYAML: yaml}, nil
	}
}

// assertHasUpServiceCall asserts that at least one command call contains
// both "--no-deps" and the given service key.
func assertHasUpServiceCall(t *testing.T, calls []mockCall, serviceKey string) {
	t.Helper()
	for _, c := range calls {
		argsStr := strings.Join(c.Args, " ")
		if strings.Contains(argsStr, "--no-deps") && strings.Contains(argsStr, serviceKey) {
			return
		}
	}
	t.Errorf("expected a 'docker compose up --no-deps %s' call; calls:\n%s", serviceKey, sprintCalls(calls))
}

// assertNoUpServiceCall asserts that no command call contains "--no-deps".
func assertNoUpServiceCall(t *testing.T, calls []mockCall) {
	t.Helper()
	for _, c := range calls {
		if strings.Contains(strings.Join(c.Args, " "), "--no-deps") {
			t.Errorf("unexpected fragment 'up --no-deps' call: args=%v", c.Args)
		}
	}
}

// assertHasFullProjectUpCall asserts that at least one command call contains
// both "up" and "--remove-orphans" (the full-project apply signature).
func assertHasFullProjectUpCall(t *testing.T, calls []mockCall) {
	t.Helper()
	for _, c := range calls {
		argsStr := strings.Join(c.Args, " ")
		if strings.Contains(argsStr, "up") && strings.Contains(argsStr, "--remove-orphans") {
			return
		}
	}
	t.Errorf("expected a 'docker compose up --remove-orphans' call; calls:\n%s", sprintCalls(calls))
}

// assertNoFullProjectUpCall asserts that no command call contains
// "--remove-orphans".
func assertNoFullProjectUpCall(t *testing.T, calls []mockCall) {
	t.Helper()
	for _, c := range calls {
		if strings.Contains(strings.Join(c.Args, " "), "--remove-orphans") {
			t.Errorf("unexpected full-project 'up --remove-orphans' call: args=%v", c.Args)
		}
	}
}

// assertNoUpCalls asserts that no "up" command was executed at all.
func assertNoUpCalls(t *testing.T, calls []mockCall) {
	t.Helper()
	for _, c := range calls {
		argsStr := strings.Join(c.Args, " ")
		if strings.Contains(argsStr, " up ") || strings.HasSuffix(argsStr, " up") {
			t.Errorf("unexpected 'up' call during dry run: args=%v", c.Args)
		}
	}
}

// sprintCalls formats command calls for test output.
func sprintCalls(calls []mockCall) string {
	var b strings.Builder
	for i, c := range calls {
		b.WriteString("  [")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("] ")
		b.WriteString(c.Name)
		b.WriteString(" ")
		b.WriteString(strings.Join(c.Args, " "))
		b.WriteString("\n")
	}
	return b.String()
}

// setupExplicitOwnedDirNoMetadata creates a temp dir with explicit Bahia
// ownership (bypassing the marker + render-state.json check) but WITHOUT a
// render-state.json file. Used to test the "no baseline / first render" case.
func setupExplicitOwnedDirNoMetadata(t *testing.T) (string, *ComposeRuntime) {
	t.Helper()
	dir := t.TempDir()
	// Create .bahia/ dir so fragment writes can land there, but no render-state.json.
	if err := os.MkdirAll(filepath.Join(dir, ".bahia"), 0o755); err != nil {
		t.Fatalf("create .bahia dir: %v", err)
	}
	logger := zaptest.NewLogger(t)
	trueVal := true
	rt := NewComposeRuntime(dir, logger)
	rt.ownershipConfig = ComposeOwnershipConfig{BahiaOwned: &trueVal}
	return dir, rt
}

// ---------------------------------------------------------------------------
// Tests: LoadRenderMetadata
// ---------------------------------------------------------------------------

func TestLoadRenderMetadata_Missing(t *testing.T) {
	dir := t.TempDir()
	// No .bahia/ directory and no render-state.json.
	metadata, err := LoadRenderMetadata(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if metadata != nil {
		t.Errorf("expected nil metadata for missing file, got: %+v", metadata)
	}
}

func TestLoadRenderMetadata_Valid(t *testing.T) {
	dir := t.TempDir()
	bahiaDir := filepath.Join(dir, ".bahia")
	if err := os.MkdirAll(bahiaDir, 0o755); err != nil {
		t.Fatalf("create .bahia dir: %v", err)
	}

	meta := RenderMetadata{
		SchemaVersion: 1,
		Renderer:      "compose",
		EnvironmentID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		RevisionHash:  "abc123",
		ServiceKeys:   []string{"web-frontend", "api-server"},
		ServiceCount:  2,
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(bahiaDir, "render-state.json"), data, 0o644); err != nil {
		t.Fatalf("write render-state.json: %v", err)
	}

	got, err := LoadRenderMetadata(dir)
	if err != nil {
		t.Fatalf("LoadRenderMetadata: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil metadata")
	}
	if got.Renderer != "compose" {
		t.Errorf("renderer: want compose, got %s", got.Renderer)
	}
	if got.RevisionHash != "abc123" {
		t.Errorf("revision_hash: want abc123, got %s", got.RevisionHash)
	}
	if len(got.ServiceKeys) != 2 {
		t.Errorf("service_keys: want 2 entries, got %v", got.ServiceKeys)
	}
}

func TestLoadRenderMetadata_Invalid(t *testing.T) {
	dir := t.TempDir()
	bahiaDir := filepath.Join(dir, ".bahia")
	if err := os.MkdirAll(bahiaDir, 0o755); err != nil {
		t.Fatalf("create .bahia dir: %v", err)
	}
	// Write deliberately invalid JSON.
	if err := os.WriteFile(filepath.Join(bahiaDir, "render-state.json"), []byte("not json {{"), 0o644); err != nil {
		t.Fatalf("write render-state.json: %v", err)
	}

	_, err := LoadRenderMetadata(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Fragment apply — eligible path
// ---------------------------------------------------------------------------

func TestFragmentApply_EligibleSingleServiceChange(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()

	applier := newTestApplierWithFragmentHooks(t, dir, runner,
		alwaysEligibleFn(),
		testFragmentRendererFn("web-frontend", "nginx:1.26"),
	)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	result, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// Fragment service-scoped up should have been called.
	assertHasUpServiceCall(t, runner.calls, "web-frontend")

	// Full-project up --remove-orphans must NOT have been called (fragment took over).
	assertNoFullProjectUpCall(t, runner.calls)

	// Result fields.
	if result.Renderer != "compose" {
		t.Errorf("renderer: want compose, got %s", result.Renderer)
	}
	if len(result.ResourceNames) == 0 {
		t.Error("expected non-empty ResourceNames")
	}
	// Fragment apply sets a warning.
	var hasFragmentWarning bool
	for _, w := range result.Warnings {
		if strings.Contains(w, "fragment") {
			hasFragmentWarning = true
		}
	}
	if !hasFragmentWarning {
		t.Errorf("expected a fragment warning in result.Warnings, got: %v", result.Warnings)
	}
}

func TestFragmentApply_EligibleSingleServiceChange_UpServiceArgs(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()

	applier := newTestApplierWithFragmentHooks(t, dir, runner,
		alwaysEligibleFn(),
		testFragmentRendererFn("web-frontend", "nginx:1.26"),
	)

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

	// Find the up --no-deps call.
	var upSvcCall *mockCall
	for i := range runner.calls {
		argsStr := strings.Join(runner.calls[i].Args, " ")
		if strings.Contains(argsStr, "--no-deps") {
			upSvcCall = &runner.calls[i]
			break
		}
	}
	if upSvcCall == nil {
		t.Fatal("expected a 'docker compose up --no-deps' call")
	}

	argsStr := strings.Join(upSvcCall.Args, " ")

	// Must have up, -d, --no-deps, pull policy, service name.
	for _, want := range []string{"up", "-d", "--no-deps", "--pull", "always", "web-frontend"} {
		if !strings.Contains(argsStr, want) {
			t.Errorf("up service args should contain %q; got: %s", want, argsStr)
		}
	}

	// Must NOT have --remove-orphans.
	if strings.Contains(argsStr, "--remove-orphans") {
		t.Error("up service call must NOT contain '--remove-orphans'")
	}
}

// ---------------------------------------------------------------------------
// Tests: Fragment apply — ineligible / fallback paths
// ---------------------------------------------------------------------------

func TestFragmentApply_IneligibleFallsToFullProject(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()

	// Use nil hooks → production stubs (always ineligible).
	applier := newTestApplierWithFragmentHooks(t, dir, runner, nil, nil)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// Full-project path must have been used.
	assertHasFullProjectUpCall(t, runner.calls)

	// No fragment --no-deps call.
	assertNoUpServiceCall(t, runner.calls)
}

func TestFragmentApply_NoBaselineFallsToFullProject(t *testing.T) {
	// Explicit ownership, no render-state.json → LoadRenderMetadata returns nil.
	_, rt := setupExplicitOwnedDirNoMetadata(t)
	runner := allSuccessRunner()
	logger := zaptest.NewLogger(t)

	applier := &ComposeDesiredStateApplier{
		runtime:  rt,
		renderer: NewComposeRenderer(),
		staging:  NewComposeStagingManagerWithRunner(logger, runner),
		runner:   runner,
		executor: NewCLIComposeExecutor(rt, runner, logger),
		logger:   logger,
		// Inject eligible hooks — but without a baseline the fragment path must
		// still fall through before even checking eligibility.
		fragmentEligibilityFn: alwaysEligibleFn(),
		fragmentRendererFn: testFragmentRendererFn("web-frontend", "nginx:1.25"),
	}

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// No baseline → full-project path taken, no --no-deps call.
	assertHasFullProjectUpCall(t, runner.calls)
	assertNoUpServiceCall(t, runner.calls)
}

func TestFragmentApply_FragmentValidationFailure(t *testing.T) {
	dir := setupBahiaOwnedDir(t)

	// Runner fails when it sees "fragments" in the -f path (fragment validation).
	// Succeeds for all other commands (staging validation, full-project up, etc.).
	runner := &composeDirRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			argsStr := strings.Join(args, " ")
			if strings.Contains(argsStr, "fragments") && strings.Contains(argsStr, "config") {
				return "", "fragment overlay: service config error", fmt.Errorf("exit status 1")
			}
			return "", "", nil
		},
	}

	applier := newTestApplierWithFragmentHooks(t, dir, runner,
		alwaysEligibleFn(),
		testFragmentRendererFn("web-frontend", "nginx:1.26"),
	)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v — fragment validation failure should fall back, not error", err)
	}

	// Fell back to full-project.
	assertHasFullProjectUpCall(t, runner.calls)
	assertNoUpServiceCall(t, runner.calls)
}

func TestFragmentApply_UpServiceFailureFallsToFullProject(t *testing.T) {
	dir := setupBahiaOwnedDir(t)

	// Runner fails when it sees "--no-deps" (the fragment up call).
	runner := &composeDirRunner{
		handler: func(name string, args []string, dir string) (string, string, error) {
			if strings.Contains(strings.Join(args, " "), "--no-deps") {
				return "", "container start failed", fmt.Errorf("exit status 1")
			}
			return "", "", nil
		},
	}

	applier := newTestApplierWithFragmentHooks(t, dir, runner,
		alwaysEligibleFn(),
		testFragmentRendererFn("web-frontend", "nginx:1.26"),
	)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState should fall back on up-service failure, got: %v", err)
	}

	// Must have fallen back to full-project.
	assertHasFullProjectUpCall(t, runner.calls)
}

// ---------------------------------------------------------------------------
// Tests: Dry run with fragment
// ---------------------------------------------------------------------------

func TestFragmentApply_DryRunWithFragment(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()

	applier := newTestApplierWithFragmentHooks(t, dir, runner,
		alwaysEligibleFn(),
		testFragmentRendererFn("web-frontend", "nginx:1.26"),
	)

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

	// No up calls of any kind during dry run.
	assertNoUpCalls(t, runner.calls)

	// Result should mention the fragment.
	if result.Renderer != "compose" {
		t.Errorf("renderer: want compose, got %s", result.Renderer)
	}
	var hasFragmentDryRunWarning bool
	for _, w := range result.Warnings {
		if strings.Contains(w, "fragment") || strings.Contains(w, "dry-run") {
			hasFragmentDryRunWarning = true
		}
	}
	if !hasFragmentDryRunWarning {
		t.Errorf("dry run should report fragment in warnings; got: %v", result.Warnings)
	}

	// No live compose file should be written (dry run does not promote).
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
		t.Error("dry run must not write the live docker-compose.yml")
	}
}

func TestFragmentApply_DryRunIneligibleFallsToFullDryRun(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()

	// Ineligible → dry run goes through full-project path.
	applier := newTestApplierWithFragmentHooks(t, dir, runner, nil, nil)

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

	assertNoUpCalls(t, runner.calls)

	var hasDryRunWarning bool
	for _, w := range result.Warnings {
		if strings.Contains(w, "dry-run") {
			hasDryRunWarning = true
		}
	}
	if !hasDryRunWarning {
		t.Errorf("expected dry-run warning, got: %v", result.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Tests: Full project stays current after fragment apply
// ---------------------------------------------------------------------------

func TestFragmentApply_FullProjectStaysCurrentAfterFragment(t *testing.T) {
	dir := setupBahiaOwnedDir(t)
	runner := allSuccessRunner()

	applier := newTestApplierWithFragmentHooks(t, dir, runner,
		alwaysEligibleFn(),
		testFragmentRendererFn("web-frontend", "nginx:1.26"),
	)

	plan := testEnvironmentPlan()
	target := &plan.Services[0]

	_, err := applier.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   target,
	})
	if err != nil {
		t.Fatalf("ApplyDesiredState: %v", err)
	}

	// docker-compose.yml must have been written (from full-project sync).
	liveCompose := filepath.Join(dir, "docker-compose.yml")
	data, err := os.ReadFile(liveCompose)
	if err != nil {
		t.Fatalf("docker-compose.yml should be written after fragment apply: %v", err)
	}
	if !strings.Contains(string(data), "web-frontend") {
		t.Error("live docker-compose.yml should contain the service key")
	}

	// render-state.json must have been updated with richer metadata than the
	// minimal seed written by setupBahiaOwnedDir.
	renderStateData, err := os.ReadFile(filepath.Join(dir, ".bahia", "render-state.json"))
	if err != nil {
		t.Fatalf("render-state.json should be present after fragment apply: %v", err)
	}
	renderStateStr := string(renderStateData)

	// After a full sync the metadata should contain service_keys.
	if !strings.Contains(renderStateStr, "service_keys") {
		t.Errorf("render-state.json should contain 'service_keys' after sync; got:\n%s", renderStateStr)
	}
	// And the environment_id from the plan.
	if !strings.Contains(renderStateStr, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Errorf("render-state.json should contain environment_id; got:\n%s", renderStateStr)
	}
}
