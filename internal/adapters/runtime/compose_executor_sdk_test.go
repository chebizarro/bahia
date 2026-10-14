package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

// fakeComposeService records Up calls. The embedded api.Compose interface is
// nil, so any unstubbed SDK method panics — tests only exercise LoadProject
// and Up.
type fakeComposeService struct {
	api.Compose
	real        api.Compose // delegate for LoadProject
	upCalls     []fakeUpCall
	psCalls     []fakePsCall
	psResults   []api.ContainerSummary
	psErr       error
	pullCalls   []*types.Project
	pullErr     error
	removeCalls []fakeRemoveCall
	upErr       error
	removeErr   error
}

type fakePsCall struct {
	project string
	options api.PsOptions
}

type fakeRemoveCall struct {
	project string
	options api.RemoveOptions
}

type fakeUpCall struct {
	project *types.Project
	options api.UpOptions
}

func (f *fakeComposeService) LoadProject(ctx context.Context, options api.ProjectLoadOptions) (*types.Project, error) {
	return f.real.LoadProject(ctx, options)
}

func (f *fakeComposeService) Up(ctx context.Context, project *types.Project, options api.UpOptions) error {
	f.upCalls = append(f.upCalls, fakeUpCall{project: project, options: options})
	return f.upErr
}

func (f *fakeComposeService) Ps(_ context.Context, project string, options api.PsOptions) ([]api.ContainerSummary, error) {
	f.psCalls = append(f.psCalls, fakePsCall{project: project, options: options})
	return f.psResults, f.psErr
}

func (f *fakeComposeService) Pull(_ context.Context, project *types.Project, _ api.PullOptions) error {
	f.pullCalls = append(f.pullCalls, project)
	return f.pullErr
}

func (f *fakeComposeService) Remove(_ context.Context, project string, options api.RemoveOptions) error {
	f.removeCalls = append(f.removeCalls, fakeRemoveCall{project: project, options: options})
	return f.removeErr
}

// newFakeSDKExecutor returns an executor whose SDK service records Up calls
// while delegating LoadProject to a real (offline) SDK service.
func newFakeSDKExecutor(t *testing.T, dir string) (*SDKComposeExecutor, *fakeComposeService) {
	t.Helper()
	rt := NewComposeRuntime(dir, zap.NewNop())
	real := NewSDKComposeExecutor(rt, zap.NewNop())
	realSvc, err := real.service()
	if err != nil {
		t.Fatalf("building real SDK service: %v", err)
	}
	t.Cleanup(func() { _ = real.Close() })
	fake := &fakeComposeService{real: realSvc}
	e := NewSDKComposeExecutor(rt, zap.NewNop())
	e.newService = func() (api.Compose, error) { return fake, nil }
	return e, fake
}

func writeSDKTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

const sdkTestComposeYAML = `name: sdk-exec-test
services:
  api:
    image: nginx:alpine
  worker:
    image: redis:alpine
    depends_on:
      - api
`

func TestComposeRuntimeSDKLegacyOperations(t *testing.T) {
	dir := t.TempDir()
	writeSDKTestFile(t, dir, composeFileName, `name: sdk-exec-test
services:
  api:
    image: nginx:alpine
  worker:
    image: ${WORKER_IMAGE:-redis:alpine}
    depends_on:
      - api
`)
	createBahiaMarker(t, dir)
	executor, fake := newFakeSDKExecutor(t, dir)
	r := &ComposeRuntime{projectDir: dir, executionMode: ExecutionModeSDK, sdkExecutor: executor, logger: zap.NewNop()}
	if err := r.Deploy(context.Background(), "worker", "redis:7", DeployOptions{Environment: map[string]string{"WORKER_IMAGE": "redis:old"}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.pullCalls) != 1 || len(fake.upCalls) != 1 {
		t.Fatalf("pull/up calls = %d/%d", len(fake.pullCalls), len(fake.upCalls))
	}
	up := fake.upCalls[0]
	if len(up.project.Services) != 1 || up.project.Services["worker"].Image != "redis:7" || up.options.Create.Recreate != api.RecreateForce || !up.options.Create.IgnoreOrphans || len(up.options.Create.Services) != 1 || up.options.Create.Services[0] != "worker" {
		t.Fatalf("SDK service up = %#v, project services = %#v", up.options, up.project.Services)
	}
	if err := r.Undeploy(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if len(fake.removeCalls) != 1 || fake.removeCalls[0].project != "sdk-exec-test" || !fake.removeCalls[0].options.Stop || !fake.removeCalls[0].options.Force || len(fake.removeCalls[0].options.Services) != 1 || fake.removeCalls[0].options.Services[0] != "worker" {
		t.Fatalf("SDK remove = %#v", fake.removeCalls)
	}
}

func TestComposeRuntimeSDKDeployRejectsUnsupportedOptions(t *testing.T) {
	dir := t.TempDir()
	createBahiaMarker(t, dir)
	executor, fake := newFakeSDKExecutor(t, dir)
	r := &ComposeRuntime{projectDir: dir, executionMode: ExecutionModeSDK, sdkExecutor: executor, logger: zap.NewNop()}
	for _, opts := range []DeployOptions{{Ports: []string{"8080:80"}}, {Labels: map[string]string{"k": "v"}}, {PullAlways: true}} {
		err := r.Deploy(context.Background(), "worker", "redis:7", opts)
		var unsupported *ComposeSDKUnsupportedOptionError
		if !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "execution_mode: cli") {
			t.Fatalf("Deploy(%#v) error = %v", opts, err)
		}
	}
	if len(fake.pullCalls) != 0 || len(fake.upCalls) != 0 {
		t.Fatalf("rejected deploy performed SDK mutation")
	}
}

func TestComposeRuntimeSDKTransportErrorsDoNotFallBackToCLI(t *testing.T) {
	dir := t.TempDir()
	writeSDKTestFile(t, dir, composeFileName, sdkTestComposeYAML)
	createBahiaMarker(t, dir)
	executor, fake := newFakeSDKExecutor(t, dir)
	r := &ComposeRuntime{projectDir: dir, executionMode: ExecutionModeSDK, sdkExecutor: executor, logger: zap.NewNop()}
	fake.psErr = errors.New("SDK ps failure")
	if _, err := r.Observe(context.Background(), uuid.New(), uuid.New(), "worker"); err == nil || !strings.Contains(err.Error(), "SDK ps failure") {
		t.Fatalf("Observe error = %v", err)
	}
	fake.upErr = errors.New("SDK up failure")
	if err := r.Deploy(context.Background(), "worker", "", DeployOptions{}); err == nil || !strings.Contains(err.Error(), "SDK up failure") {
		t.Fatalf("Deploy error = %v", err)
	}
	fake.removeErr = errors.New("SDK remove failure")
	if err := r.Undeploy(context.Background(), "worker"); err == nil || !strings.Contains(err.Error(), "SDK remove failure") {
		t.Fatalf("Undeploy error = %v", err)
	}
}

func TestComposeRuntimeSDKRetireClosesExecutor(t *testing.T) {
	dir := t.TempDir()
	r := &ComposeRuntime{projectDir: dir, executionMode: ExecutionModeSDK, logger: zap.NewNop()}
	applier := NewComposeDesiredStateApplier(r, zap.NewNop())
	executor, ok := applier.executor.(*SDKComposeExecutor)
	if !ok || r.sdkExecutor != executor {
		t.Fatal("desired-state applier must share the runtime-owned SDK executor")
	}
	executor.newService = func() (api.Compose, error) { return &fakeComposeService{}, nil }
	if _, err := executor.service(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if executor.svc != nil || r.sdkExecutor != nil {
		t.Fatal("SDK executor was not closed on runtime retirement")
	}
}

func TestSDKComposeExecutor_ExecutionMode(t *testing.T) {
	e := NewSDKComposeExecutor(NewComposeRuntime(t.TempDir(), zap.NewNop()), zap.NewNop())
	if got := e.ExecutionMode(); got != ExecutionModeSDK {
		t.Errorf("ExecutionMode() = %q, want %q", got, ExecutionModeSDK)
	}
}

func TestSDKComposeExecutor_Validate(t *testing.T) {
	dir := t.TempDir()
	stagingDir := filepath.Join(dir, ".bahia", "staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	composeFile := writeSDKTestFile(t, stagingDir, composeFileName, sdkTestComposeYAML)

	e := NewSDKComposeExecutor(NewComposeRuntime(dir, zap.NewNop()), zap.NewNop())
	staged := &StagedFiles{
		ComposeDir:  dir,
		StagingDir:  stagingDir,
		ComposeFile: composeFile,
	}

	if _, _, err := e.Validate(context.Background(), staged); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !staged.Validated {
		t.Error("expected staged.Validated to be set")
	}
}

func TestSDKComposeExecutor_Validate_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	composeFile := writeSDKTestFile(t, dir, composeFileName, "services:\n  api:\n    image: [broken\n")

	e := NewSDKComposeExecutor(NewComposeRuntime(dir, zap.NewNop()), zap.NewNop())
	staged := &StagedFiles{ComposeDir: dir, StagingDir: dir, ComposeFile: composeFile}

	if _, _, err := e.Validate(context.Background(), staged); err == nil {
		t.Fatal("expected validation error for invalid YAML")
	}
	if staged.Validated {
		t.Error("staged.Validated must not be set on failure")
	}
}

func TestSDKComposeExecutor_Validate_NilStaged(t *testing.T) {
	e := NewSDKComposeExecutor(NewComposeRuntime(t.TempDir(), zap.NewNop()), zap.NewNop())
	if _, _, err := e.Validate(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil staged files")
	}
}

func TestSDKComposeExecutor_ValidateWithFragment(t *testing.T) {
	dir := t.TempDir()
	writeSDKTestFile(t, dir, composeFileName, sdkTestComposeYAML)
	fragment := writeSDKTestFile(t, dir, "fragment.yml", `name: sdk-exec-test
services:
  api:
    image: nginx:1.27-alpine
`)

	e := NewSDKComposeExecutor(NewComposeRuntime(dir, zap.NewNop()), zap.NewNop())
	if _, _, err := e.ValidateWithFragment(context.Background(), dir, fragment); err != nil {
		t.Fatalf("ValidateWithFragment: %v", err)
	}
}

func TestSDKComposeExecutor_ValidateWithFragment_UnknownServiceStillValid(t *testing.T) {
	// A fragment introducing a new service merges cleanly; validation only
	// checks the merged model, mirroring `docker compose config -q`.
	dir := t.TempDir()
	writeSDKTestFile(t, dir, composeFileName, sdkTestComposeYAML)
	fragment := writeSDKTestFile(t, dir, "fragment.yml", `name: sdk-exec-test
services:
  metrics:
    image: prom/prometheus:latest
`)

	e := NewSDKComposeExecutor(NewComposeRuntime(dir, zap.NewNop()), zap.NewNop())
	if _, _, err := e.ValidateWithFragment(context.Background(), dir, fragment); err != nil {
		t.Fatalf("ValidateWithFragment: %v", err)
	}
}

func TestSDKComposeExecutor_LoadProjectSelectsNoDeps(t *testing.T) {
	// Verify the --no-deps selection semantics used by UpService: selecting
	// "worker" with IgnoreDependencies must exclude "api".
	dir := t.TempDir()
	writeSDKTestFile(t, dir, composeFileName, sdkTestComposeYAML)

	e := NewSDKComposeExecutor(NewComposeRuntime(dir, zap.NewNop()), zap.NewNop())
	project, err := e.loadProject(context.Background(), dir, []string{filepath.Join(dir, composeFileName)})
	if err != nil {
		t.Fatalf("loadProject: %v", err)
	}

	selected, err := project.WithSelectedServices([]string{"worker"}, types.IgnoreDependencies)
	if err != nil {
		t.Fatalf("WithSelectedServices: %v", err)
	}
	if _, ok := selected.Services["worker"]; !ok {
		t.Error("expected worker in selected services")
	}
	if _, ok := selected.Services["api"]; ok {
		t.Error("api must not be selected with IgnoreDependencies")
	}
}

func TestApplyComposePullPolicy(t *testing.T) {
	project := &types.Project{
		Services: types.Services{
			"api":    {Name: "api", Image: "api:v1"},
			"worker": {Name: "worker", Image: "worker:v1", PullPolicy: "never"},
		},
	}

	applyComposePullPolicy(project, "always")
	for name, svc := range project.Services {
		if svc.PullPolicy != "always" {
			t.Errorf("service %s pull policy = %q, want %q", name, svc.PullPolicy, "always")
		}
	}

	// Empty policy leaves the project untouched.
	applyComposePullPolicy(project, "")
	for name, svc := range project.Services {
		if svc.PullPolicy != "always" {
			t.Errorf("service %s pull policy changed by empty policy: %q", name, svc.PullPolicy)
		}
	}

	// Nil project must not panic.
	applyComposePullPolicy(nil, "always")
}

func TestSDKComposeExecutor_Up_Options(t *testing.T) {
	dir := t.TempDir()
	writeSDKTestFile(t, dir, composeFileName, sdkTestComposeYAML)

	e, fake := newFakeSDKExecutor(t, dir)
	if _, _, err := e.Up(context.Background(), dir, "always"); err != nil {
		t.Fatalf("Up: %v", err)
	}

	if len(fake.upCalls) != 1 {
		t.Fatalf("expected 1 Up call, got %d", len(fake.upCalls))
	}
	call := fake.upCalls[0]
	if !call.options.Create.RemoveOrphans {
		t.Error("full-project Up must set RemoveOrphans (≙ --remove-orphans)")
	}
	if len(call.options.Create.Services) != 0 {
		t.Errorf("full-project Up must not scope services, got %v", call.options.Create.Services)
	}
	if call.options.Start.Wait {
		t.Error("detached Up must not wait")
	}
	if call.options.Start.Attach != nil {
		t.Error("detached Up must not attach")
	}
	if len(call.project.Services) != 2 {
		t.Errorf("expected 2 services in project, got %d", len(call.project.Services))
	}
	for name, svc := range call.project.Services {
		if svc.PullPolicy != "always" {
			t.Errorf("service %s pull policy = %q, want %q (≙ --pull always)", name, svc.PullPolicy, "always")
		}
	}
}

func TestSDKComposeExecutor_Up_UsesDefaultDiscovery(t *testing.T) {
	// The CLI executor runs `docker compose up` without -f, so Compose default
	// discovery merges docker-compose.override.yml. The SDK path must match.
	dir := t.TempDir()
	writeSDKTestFile(t, dir, composeFileName, sdkTestComposeYAML)
	writeSDKTestFile(t, dir, "docker-compose.override.yml", "services:\n  extra:\n    image: busybox:latest\n")

	e, fake := newFakeSDKExecutor(t, dir)
	if _, _, err := e.Up(context.Background(), dir, ""); err != nil {
		t.Fatalf("Up: %v", err)
	}
	project := fake.upCalls[0].project
	if _, ok := project.Services["extra"]; !ok {
		t.Errorf("expected override service 'extra' via default discovery, got services %v", projectServiceNames(project))
	}
}

func TestSDKComposeExecutor_UpService_Options(t *testing.T) {
	dir := t.TempDir()
	writeSDKTestFile(t, dir, composeFileName, sdkTestComposeYAML)
	fragment := writeSDKTestFile(t, dir, "fragment.yml", `name: sdk-exec-test
services:
  worker:
    image: redis:7-alpine
`)

	e, fake := newFakeSDKExecutor(t, dir)
	if _, _, err := e.UpService(context.Background(), dir, fragment, "worker", "missing"); err != nil {
		t.Fatalf("UpService: %v", err)
	}

	if len(fake.upCalls) != 1 {
		t.Fatalf("expected 1 Up call, got %d", len(fake.upCalls))
	}
	call := fake.upCalls[0]
	if got := call.options.Create.Services; len(got) != 1 || got[0] != "worker" {
		t.Errorf("Create.Services = %v, want [worker]", got)
	}
	if call.options.Create.RemoveOrphans {
		t.Error("service-scoped Up must not remove orphans")
	}
	if !call.options.Create.IgnoreOrphans {
		t.Error("service-scoped Up must ignore orphans")
	}
	// --no-deps: dependency 'api' must not be part of the selected project.
	if _, ok := call.project.Services["api"]; ok {
		t.Error("dependency 'api' selected despite --no-deps semantics")
	}
	worker, ok := call.project.Services["worker"]
	if !ok {
		t.Fatal("worker missing from selected project")
	}
	if worker.Image != "redis:7-alpine" {
		t.Errorf("fragment overlay not applied: image = %q", worker.Image)
	}
	if worker.PullPolicy != "missing" {
		t.Errorf("worker pull policy = %q, want %q", worker.PullPolicy, "missing")
	}
}

func projectServiceNames(p *types.Project) []string {
	names := make([]string, 0, len(p.Services))
	for name := range p.Services {
		names = append(names, name)
	}
	return names
}

func TestSDKClientOptions_TLS(t *testing.T) {
	dir := t.TempDir()
	ca := writeSDKTestFile(t, dir, "custom-ca.crt", "ca")
	cert := writeSDKTestFile(t, dir, "client.crt", "cert")
	key := writeSDKTestFile(t, dir, "client.key", "key")

	cases := []struct {
		name     string
		endpoint config.RuntimeEndpointConfig
		wantTLS  bool
		wantVfy  bool
		wantSkip bool
	}{
		{
			name:     "no TLS",
			endpoint: config.RuntimeEndpointConfig{DockerHost: "tcp://h:2375"},
		},
		{
			name:     "verified mTLS with arbitrary file names",
			endpoint: config.RuntimeEndpointConfig{DockerHost: "tcp://h:2376", CACertFile: ca, ClientCertFile: cert, ClientKeyFile: key},
			wantTLS:  true,
			wantVfy:  true,
		},
		{
			name:     "insecure skip verify",
			endpoint: config.RuntimeEndpointConfig{DockerHost: "tcp://h:2376", CACertFile: ca, InsecureSkipVerify: true},
			wantTLS:  true,
			wantVfy:  false,
			wantSkip: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := newComposeRuntimeWithEndpointForMode(dir, tc.endpoint, ExecutionModeSDK, zap.NewNop())
			if err != nil {
				t.Fatalf("newComposeRuntimeWithEndpointForMode: %v", err)
			}
			opts := sdkClientOptions(rt)
			if opts.TLS != tc.wantTLS {
				t.Errorf("TLS = %v, want %v", opts.TLS, tc.wantTLS)
			}
			if !tc.wantTLS {
				if opts.TLSOptions != nil {
					t.Error("TLSOptions must be nil without TLS")
				}
				return
			}
			if opts.TLSVerify != tc.wantVfy {
				t.Errorf("TLSVerify = %v, want %v", opts.TLSVerify, tc.wantVfy)
			}
			if opts.TLSOptions == nil {
				t.Fatal("TLSOptions must be set with TLS")
			}
			if opts.TLSOptions.InsecureSkipVerify != tc.wantSkip {
				t.Errorf("InsecureSkipVerify = %v, want %v", opts.TLSOptions.InsecureSkipVerify, tc.wantSkip)
			}
			if opts.TLSOptions.CAFile != tc.endpoint.CACertFile {
				t.Errorf("CAFile = %q, want %q", opts.TLSOptions.CAFile, tc.endpoint.CACertFile)
			}
		})
	}
}

func TestSDKModeAcceptsArbitraryCertPaths_CLIRejects(t *testing.T) {
	// The CLI path requires DOCKER_CERT_PATH naming (ca.pem/cert.pem/key.pem);
	// the SDK path must accept arbitrary certificate file paths.
	dir := t.TempDir()
	endpoint := config.RuntimeEndpointConfig{
		DockerHost:     "tcp://h:2376",
		CACertFile:     writeSDKTestFile(t, dir, "my-ca.crt", "ca"),
		ClientCertFile: writeSDKTestFile(t, dir, "my-cert.crt", "cert"),
		ClientKeyFile:  writeSDKTestFile(t, dir, "my-key.pem", "key"),
	}

	if _, err := newComposeRuntimeWithEndpointForMode(dir, endpoint, ExecutionModeSDK, zap.NewNop()); err != nil {
		t.Errorf("sdk mode must accept arbitrary cert paths: %v", err)
	}
	if _, err := newComposeRuntimeWithEndpointForMode(dir, endpoint, ExecutionModeCLI, zap.NewNop()); err == nil {
		t.Error("cli mode should reject non-DOCKER_CERT_PATH cert names")
	}
}

func TestSDKModeRejectsUnpairedClientCert(t *testing.T) {
	dir := t.TempDir()
	endpoint := config.RuntimeEndpointConfig{
		DockerHost:     "tcp://h:2376",
		ClientCertFile: writeSDKTestFile(t, dir, "client.crt", "cert"),
	}
	if _, err := newComposeRuntimeWithEndpointForMode(dir, endpoint, ExecutionModeSDK, zap.NewNop()); err == nil {
		t.Error("expected error for client cert without key")
	}
}

func TestSDKComposeExecutor_CloseBeforeUse(t *testing.T) {
	e := NewSDKComposeExecutor(NewComposeRuntime(t.TempDir(), zap.NewNop()), zap.NewNop())
	if err := e.Close(); err != nil {
		t.Errorf("Close before use: %v", err)
	}
}

func TestNewComposeExecutor_SelectsByExecutionMode(t *testing.T) {
	logger := zap.NewNop()
	runner := &execCommandRunner{}

	cliRT := NewComposeRuntime(t.TempDir(), logger)
	if _, ok := newComposeExecutor(cliRT, runner, logger).(*CLIComposeExecutor); !ok {
		t.Error("default execution mode must select CLIComposeExecutor")
	}

	sdkRT := NewComposeRuntime(t.TempDir(), logger)
	sdkRT.executionMode = ExecutionModeSDK
	if _, ok := newComposeExecutor(sdkRT, runner, logger).(*SDKComposeExecutor); !ok {
		t.Error("sdk execution mode must select SDKComposeExecutor")
	}
}
