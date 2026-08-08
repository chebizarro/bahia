package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestNewRuntime_Docker(t *testing.T) {
	logger := zap.NewNop()
	rt, err := NewRuntime(RuntimeConfig{Type: "docker", DockerHost: "unix:///var/run/docker.sock"}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Type() != domain.RuntimeTypeDocker {
		t.Errorf("expected docker, got %s", rt.Type())
	}
	if _, ok := rt.(*DockerObserver); !ok {
		t.Error("expected *DockerObserver")
	}
}

func TestNewRuntime_DockerDefault(t *testing.T) {
	logger := zap.NewNop()
	// Empty type should default to docker.
	rt, err := NewRuntime(RuntimeConfig{}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Type() != domain.RuntimeTypeDocker {
		t.Errorf("expected docker, got %s", rt.Type())
	}
}

func TestNewRuntime_Compose(t *testing.T) {
	logger := zap.NewNop()
	rt, err := NewRuntime(RuntimeConfig{Type: "compose", ComposeDir: "/tmp/project", DockerHost: "tcp://compose-docker:2375", ExecutionMode: "cli"}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Type() != domain.RuntimeTypeCompose {
		t.Errorf("expected compose, got %s", rt.Type())
	}
	cr, ok := rt.(*ComposeRuntime)
	if !ok {
		t.Fatal("expected *ComposeRuntime")
	}
	if cr.projectDir != "/tmp/project" {
		t.Errorf("expected /tmp/project, got %s", cr.projectDir)
	}
	if cr.dockerHost != "tcp://compose-docker:2375" {
		t.Errorf("expected compose docker host override, got %s", cr.dockerHost)
	}
}

func TestRuntimeConfigFromWorkerTargetResolvesEndpointRef(t *testing.T) {
	cfg, err := RuntimeConfigFromWorkerTarget(&domain.WorkerRuntimeTarget{
		Type:          domain.RuntimeTypeCompose,
		EndpointRef:   "worker-docker",
		ComposeDir:    "/srv/llm",
		ExecutionMode: "cli",
	}, config.RuntimeConfig{
		Endpoints: map[string]config.RuntimeEndpointConfig{
			"worker-docker": {DockerHost: "tcp://worker:2376"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Type != "compose" || cfg.ComposeDir != "/srv/llm" || cfg.ExecutionMode != "cli" {
		t.Fatalf("unexpected runtime config: %#v", cfg)
	}
	if cfg.DockerHost != "tcp://worker:2376" || cfg.Endpoint.Ref != "worker-docker" {
		t.Fatalf("endpoint_ref not resolved: %#v", cfg)
	}
}

func TestRuntimeConfigFromWorkerTargetRejectsMissingEndpointRef(t *testing.T) {
	_, err := RuntimeConfigFromWorkerTarget(&domain.WorkerRuntimeTarget{
		Type:        domain.RuntimeTypeDocker,
		EndpointRef: "missing",
	}, config.RuntimeConfig{Endpoints: map[string]config.RuntimeEndpointConfig{}})
	if err == nil {
		t.Fatal("expected missing endpoint_ref error")
	}
}

func TestRuntimeConfigFromWorkerTargetRequiresEndpointRefForDocker(t *testing.T) {
	_, err := RuntimeConfigFromWorkerTarget(&domain.WorkerRuntimeTarget{
		Type: domain.RuntimeTypeDocker,
	}, config.RuntimeConfig{DockerHost: "unix:///var/run/docker.sock"})
	if err == nil {
		t.Fatal("expected endpoint_ref required error")
	}
}

func TestNewRuntime_Kubernetes(t *testing.T) {
	logger := zap.NewNop()
	rt, err := NewRuntime(RuntimeConfig{
		Type:          "kubernetes",
		KubeContext:   "my-cluster",
		KubeNamespace: "production",
		KubeConfig:    "/home/user/.kube/config",
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Type() != domain.RuntimeTypeK8s {
		t.Errorf("expected kubernetes, got %s", rt.Type())
	}
	kr, ok := rt.(*KubernetesRuntime)
	if !ok {
		t.Fatal("expected *KubernetesRuntime")
	}
	if kr.kubeContext != "my-cluster" {
		t.Errorf("expected my-cluster, got %s", kr.kubeContext)
	}
	if kr.kubeNamespace != "production" {
		t.Errorf("expected production, got %s", kr.kubeNamespace)
	}
	if kr.kubeConfig != "/home/user/.kube/config" {
		t.Errorf("expected /home/user/.kube/config, got %s", kr.kubeConfig)
	}
}

func TestNewRuntime_KubernetesDefaultNamespace(t *testing.T) {
	logger := zap.NewNop()
	rt, err := NewRuntime(RuntimeConfig{Type: "kubernetes"}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	kr := rt.(*KubernetesRuntime)
	if kr.kubeNamespace != "default" {
		t.Errorf("expected default namespace, got %s", kr.kubeNamespace)
	}
}

func TestNewRuntime_Podman(t *testing.T) {
	logger := zap.NewNop()
	rt, err := NewRuntime(RuntimeConfig{Type: "podman", PodmanHost: "unix:///run/podman/podman.sock"}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Type() != domain.RuntimeTypePodman {
		t.Errorf("expected podman, got %s", rt.Type())
	}
	if _, ok := rt.(*PodmanObserver); !ok {
		t.Error("expected *PodmanObserver")
	}
}

func TestNewRuntime_PodmanDefaultSocket(t *testing.T) {
	logger := zap.NewNop()
	// Empty PodmanHost should default to rootless socket.
	rt, err := NewRuntime(RuntimeConfig{Type: "podman"}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Type() != domain.RuntimeTypePodman {
		t.Errorf("expected podman, got %s", rt.Type())
	}
}

func TestNewRuntime_UnsupportedType(t *testing.T) {
	logger := zap.NewNop()
	_, err := NewRuntime(RuntimeConfig{Type: "lxc"}, logger)
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
	if !contains(err.Error(), "unsupported runtime type") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewRuntime_VMFirecrackerConstructsRuntime(t *testing.T) {
	rt, err := NewRuntime(RuntimeConfig{
		Type: "vm-firecracker",
		VM:   config.RuntimeVMConfig{StateDir: t.TempDir(), ImageRoot: t.TempDir()},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Type() != domain.RuntimeTypeVMFirecracker {
		t.Errorf("expected vm-firecracker, got %s", rt.Type())
	}
	if _, ok := rt.(LifecycleRuntime); !ok {
		t.Error("vm-firecracker runtime should implement LifecycleRuntime")
	}
}

func TestNewRuntime_VMFirecrackerRequiresStateDirAndImageRoot(t *testing.T) {
	_, err := NewRuntime(RuntimeConfig{Type: "vm-firecracker"}, zap.NewNop())
	if err == nil || !contains(err.Error(), "state_dir") {
		t.Fatalf("expected state_dir error, got %v", err)
	}
	_, err = NewRuntime(RuntimeConfig{
		Type: "vm-firecracker",
		VM:   config.RuntimeVMConfig{StateDir: t.TempDir()},
	}, zap.NewNop())
	if err == nil || !contains(err.Error(), "image_root") {
		t.Fatalf("expected image_root error, got %v", err)
	}
}

func TestNewRuntime_VMQEMUConstructsRuntime(t *testing.T) {
	rt, err := NewRuntime(RuntimeConfig{
		Type: "vm-qemu",
		VM: config.RuntimeVMConfig{
			StateDir:   t.TempDir(),
			ImageRoot:  t.TempDir(),
			LibvirtURI: "qemu:///session",
		},
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.Type() != domain.RuntimeTypeVMQEMU {
		t.Errorf("expected vm-qemu, got %s", rt.Type())
	}
	if _, ok := rt.(LifecycleRuntime); !ok {
		t.Error("vm-qemu runtime should implement LifecycleRuntime")
	}
}

func TestNewRuntime_VMQEMURequiresStateDirAndImageRoot(t *testing.T) {
	_, err := NewRuntime(RuntimeConfig{Type: "vm-qemu"}, zap.NewNop())
	if err == nil || !contains(err.Error(), "state_dir") {
		t.Fatalf("expected state_dir error, got %v", err)
	}
	_, err = NewRuntime(RuntimeConfig{
		Type: "vm-qemu",
		VM:   config.RuntimeVMConfig{StateDir: t.TempDir()},
	}, zap.NewNop())
	if err == nil || !contains(err.Error(), "image_root") {
		t.Fatalf("expected image_root error, got %v", err)
	}
}

func TestNewRuntime_VMConfigRejectedForNonVMTypes(t *testing.T) {
	for _, typ := range []string{"docker", "compose", "kubernetes", "podman"} {
		_, err := NewRuntime(RuntimeConfig{
			Type: typ,
			VM:   config.RuntimeVMConfig{StateDir: "/var/lib/bahia/vm"},
		}, zap.NewNop())
		if err == nil {
			t.Fatalf("expected vm.* rejection error for %s", typ)
		}
		if !contains(err.Error(), "vm.* runtime settings are not valid") {
			t.Errorf("unexpected error message for %s: %v", typ, err)
		}
	}
}

func TestNewRuntime_VMFirecrackerRejectsLibvirtURI(t *testing.T) {
	_, err := NewRuntime(RuntimeConfig{
		Type: "vm-firecracker",
		VM:   config.RuntimeVMConfig{LibvirtURI: "qemu:///system"},
	}, zap.NewNop())
	if err == nil {
		t.Fatal("expected libvirt_uri rejection for vm-firecracker")
	}
	if !contains(err.Error(), "libvirt_uri") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewRuntime_ComposeRequiresComposeDir(t *testing.T) {
	_, err := NewRuntime(RuntimeConfig{Type: "compose", ExecutionMode: "cli"}, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for missing compose_dir")
	}
	if !contains(err.Error(), "compose_dir is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewRuntime_ComposeRequiresExplicitCLIExecutionMode(t *testing.T) {
	_, err := NewRuntime(RuntimeConfig{Type: "compose", ComposeDir: "/tmp/project"}, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for missing compose execution_mode")
	}
	if !contains(err.Error(), "execution_mode") || !contains(err.Error(), "cli") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewRuntime_ComposeRejectsEngineAPIExecutionMode(t *testing.T) {
	_, err := NewRuntime(RuntimeConfig{Type: "compose", ComposeDir: "/tmp/project", ExecutionMode: "engine_api"}, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for engine_api compose execution_mode")
	}
	if !contains(err.Error(), "execution_mode") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewRuntime_ComposeAcceptsSDKExecutionMode(t *testing.T) {
	rt, err := NewRuntime(RuntimeConfig{Type: "compose", ComposeDir: "/tmp/project", ExecutionMode: "sdk"}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	compose, ok := rt.(*ComposeRuntime)
	if !ok {
		t.Fatalf("expected *ComposeRuntime, got %T", rt)
	}
	if compose.ExecutionMode() != ExecutionModeSDK {
		t.Errorf("execution mode = %q, want %q", compose.ExecutionMode(), ExecutionModeSDK)
	}
}

func TestNewRuntime_ComposeCLIExecutionModeDefaultsExecutor(t *testing.T) {
	rt, err := NewRuntime(RuntimeConfig{Type: "compose", ComposeDir: "/tmp/project", ExecutionMode: "cli"}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	compose, ok := rt.(*ComposeRuntime)
	if !ok {
		t.Fatalf("expected *ComposeRuntime, got %T", rt)
	}
	if compose.ExecutionMode() != ExecutionModeCLI {
		t.Errorf("execution mode = %q, want %q", compose.ExecutionMode(), ExecutionModeCLI)
	}
}

func TestNewRuntime_PodmanComposeRejectsSDKExecutionMode(t *testing.T) {
	_, err := NewRuntime(RuntimeConfig{Type: "podman", ComposeDir: "/tmp/project", ExecutionMode: "sdk"}, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for podman compose with sdk execution_mode")
	}
	if !contains(err.Error(), "execution_mode") || !contains(err.Error(), "cli") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNormalizeRuntimeExecutionMode_SDK(t *testing.T) {
	if got := normalizeRuntimeExecutionMode(" SDK "); got != ExecutionModeSDK {
		t.Errorf("normalizeRuntimeExecutionMode(\" SDK \") = %q, want %q", got, ExecutionModeSDK)
	}
}

func TestNewObserver(t *testing.T) {
	logger := zap.NewNop()
	obs, err := NewObserver(RuntimeConfig{Type: "docker"}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs == nil {
		t.Fatal("expected non-nil observer")
	}
}

func TestMustRuntime_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unsupported type")
		}
	}()
	MustRuntime(RuntimeConfig{Type: "invalid"}, zap.NewNop())
}

func TestMapDockerState(t *testing.T) {
	cases := []struct {
		state    string
		expected domain.HealthStatus
	}{
		{"running", domain.HealthStatusHealthy},
		{"Running", domain.HealthStatusHealthy},
		{"created", domain.HealthStatusStarting},
		{"restarting", domain.HealthStatusStarting},
		{"exited", domain.HealthStatusStopped},
		{"dead", domain.HealthStatusStopped},
		{"removing", domain.HealthStatusStopped},
		{"paused", domain.HealthStatusUnhealthy},
		{"weird", domain.HealthStatusUnknown},
	}
	for _, tc := range cases {
		got := mapDockerState(tc.state)
		if got != tc.expected {
			t.Errorf("mapDockerState(%q) = %s, want %s", tc.state, got, tc.expected)
		}
	}
}

func TestMapK8sPhase(t *testing.T) {
	cases := []struct {
		phase    string
		expected domain.HealthStatus
	}{
		{"Running", domain.HealthStatusHealthy},
		{"running", domain.HealthStatusHealthy},
		{"Pending", domain.HealthStatusStarting},
		{"Succeeded", domain.HealthStatusStopped},
		{"Failed", domain.HealthStatusUnhealthy},
		{"Unknown", domain.HealthStatusUnknown},
		{"weird", domain.HealthStatusUnknown},
	}
	for _, tc := range cases {
		got := mapK8sPhase(tc.phase)
		if got != tc.expected {
			t.Errorf("mapK8sPhase(%q) = %s, want %s", tc.phase, got, tc.expected)
		}
	}
}

func TestExtractDigest(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"sha256:abc123def", "sha256:abc123def"},
		{"docker-pullable://nginx@sha256:abc123", "sha256:abc123"},
		{"some-id", "some-id"},
	}
	for _, tc := range cases {
		got := extractDigest(tc.input)
		if got != tc.expected {
			t.Errorf("extractDigest(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestKubernetesRuntime_BaseArgs(t *testing.T) {
	kr := &KubernetesRuntime{
		kubeContext:   "prod",
		kubeNamespace: "app",
		kubeConfig:    "/custom/config",
		logger:        zap.NewNop(),
	}

	args := kr.baseArgs("get", "pods")
	expected := []string{"kubectl", "--kubeconfig", "/custom/config", "--context", "prod", "-n", "app", "get", "pods"}
	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
	for i, a := range args {
		if a != expected[i] {
			t.Errorf("arg[%d] = %q, want %q", i, a, expected[i])
		}
	}
}

func TestKubernetesRuntime_BaseArgsMinimal(t *testing.T) {
	kr := &KubernetesRuntime{
		kubeNamespace: "default",
		logger:        zap.NewNop(),
	}

	args := kr.baseArgs("get", "deployments")
	// Should only have kubectl -n default get deployments (no --context, no --kubeconfig).
	expected := []string{"kubectl", "-n", "default", "get", "deployments"}
	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
	for i, a := range args {
		if a != expected[i] {
			t.Errorf("arg[%d] = %q, want %q", i, a, expected[i])
		}
	}
}

func TestComposeRuntime_ComposeArgs(t *testing.T) {
	cr := &ComposeRuntime{
		projectDir: "/my/project",
		binary:     "docker compose",
		logger:     zap.NewNop(),
	}

	args := cr.composeArgs("ps", "--format", "json")
	if args[0] != "docker" || args[1] != "compose" {
		t.Errorf("expected 'docker compose', got %v", args[:2])
	}
	// Should contain --project-directory /my/project.
	found := false
	for i, a := range args {
		if a == "--project-directory" && i+1 < len(args) && args[i+1] == "/my/project" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --project-directory /my/project in args: %v", args)
	}
}

func TestComposeRuntime_ComposeArgsV1(t *testing.T) {
	cr := &ComposeRuntime{
		projectDir: "/my/project",
		binary:     "docker-compose",
		logger:     zap.NewNop(),
	}

	args := cr.composeArgs("up", "-d")
	if args[0] != "docker-compose" {
		t.Errorf("expected docker-compose, got %s", args[0])
	}
}

// TestRuntimeInterfaceCompileTime verifies all runtime types satisfy the interface.
// NOTE: vm-firecracker and vm-qemu share vmRuntimeAdapter over their
// respective hypervisor drivers.
func TestRuntimeInterfaceCompileTime(t *testing.T) {
	// These are compile-time checks; if they compile, they pass.
	var _ Runtime = (*DockerObserver)(nil)
	var _ Runtime = (*ComposeRuntime)(nil)
	var _ Runtime = (*KubernetesRuntime)(nil)
	var _ Runtime = (*PodmanObserver)(nil)
	var _ Runtime = (*vmRuntimeAdapter)(nil)
	var _ Observer = (*DockerObserver)(nil)
	var _ Observer = (*ComposeRuntime)(nil)
	var _ Observer = (*KubernetesRuntime)(nil)
	var _ Observer = (*PodmanObserver)(nil)
	var _ Observer = (*vmRuntimeAdapter)(nil)
	var _ LifecycleRuntime = (*vmRuntimeAdapter)(nil)
}

func TestDeployOptions(t *testing.T) {
	opts := DeployOptions{
		Environment: map[string]string{"APP_ENV": "production"},
		Labels:      map[string]string{"tier": "frontend"},
		Ports:       []string{"8080:80", "443:443"},
		Restart:     "always",
		PullAlways:  true,
	}
	if opts.Environment["APP_ENV"] != "production" {
		t.Error("expected APP_ENV=production")
	}
	if len(opts.Ports) != 2 {
		t.Errorf("expected 2 ports, got %d", len(opts.Ports))
	}
}

func TestLogEntry(t *testing.T) {
	entry := LogEntry{
		Timestamp: time.Now().UTC(),
		Stream:    "stderr",
		Message:   "error: connection refused",
	}
	if entry.Stream != "stderr" {
		t.Error("expected stderr")
	}
}

// mockObserver is used for reconciler tests — verify it satisfies Observer.
type mockObserver struct {
	obs *domain.RuntimeObservation
	err error
}

func (m *mockObserver) Observe(_ context.Context, serviceID, envID uuid.UUID, _ string) (*domain.RuntimeObservation, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.obs, nil
}

var _ Observer = (*mockObserver)(nil)

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ===========================================================================
// Podman Compose factory wiring (bahia-zgov)
// ===========================================================================

func TestNewRuntime_PodmanWithComposeDir_CreatesPodmanComposeRuntime(t *testing.T) {
	t.Parallel()
	rt, err := NewRuntime(RuntimeConfig{
		Type:          "podman",
		PodmanHost:    "unix:///run/podman/podman.sock",
		ComposeDir:    "/tmp/test-compose",
		ExecutionMode: "cli",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pcr, ok := rt.(*PodmanComposeRuntime)
	if !ok {
		t.Fatalf("expected PodmanComposeRuntime, got %T", rt)
	}
	if pcr.Type() != domain.RuntimeTypePodman {
		t.Errorf("Type() = %q, want %q", pcr.Type(), domain.RuntimeTypePodman)
	}
	if pcr.projectDir != "/tmp/test-compose" {
		t.Errorf("projectDir = %q, want /tmp/test-compose", pcr.projectDir)
	}
}

func TestNewRuntime_PodmanWithComposeDir_RequiresCLIExecutionMode(t *testing.T) {
	t.Parallel()
	_, err := NewRuntime(RuntimeConfig{
		Type:          "podman",
		PodmanHost:    "unix:///run/podman/podman.sock",
		ComposeDir:    "/tmp/test-compose",
		ExecutionMode: "engine_api",
	}, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for engine_api execution mode with podman compose")
	}
	if !contains(err.Error(), "cli") {
		t.Errorf("error should mention cli, got: %v", err)
	}
}

func TestNewRuntime_PodmanWithoutComposeDir_CreatesPodmanObserver(t *testing.T) {
	t.Parallel()
	rt, err := NewRuntime(RuntimeConfig{
		Type:       "podman",
		PodmanHost: "unix:///run/podman/podman.sock",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, ok := rt.(*PodmanObserver)
	if !ok {
		t.Fatalf("expected PodmanObserver, got %T", rt)
	}
}

func TestPodmanComposeRuntime_Type(t *testing.T) {
	t.Parallel()
	rt := NewPodmanComposeRuntime("/tmp/compose", "unix:///run/podman/podman.sock", zap.NewNop())
	if rt.Type() != domain.RuntimeTypePodman {
		t.Errorf("Type() = %q, want %q", rt.Type(), domain.RuntimeTypePodman)
	}
}

func TestPodmanComposeRuntime_SupportsDesiredState(t *testing.T) {
	t.Parallel()
	rt := NewPodmanComposeRuntime("/tmp/compose", "unix:///run/podman/podman.sock", zap.NewNop())
	if !rt.SupportsDesiredState() {
		t.Error("PodmanComposeRuntime should support desired state (inherited from ComposeRuntime)")
	}
}
