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
func TestRuntimeInterfaceCompileTime(t *testing.T) {
	// These are compile-time checks; if they compile, they pass.
	var _ Runtime = (*DockerObserver)(nil)
	var _ Runtime = (*ComposeRuntime)(nil)
	var _ Runtime = (*KubernetesRuntime)(nil)
	var _ Runtime = (*PodmanObserver)(nil)
	var _ Observer = (*DockerObserver)(nil)
	var _ Observer = (*ComposeRuntime)(nil)
	var _ Observer = (*KubernetesRuntime)(nil)
	var _ Observer = (*PodmanObserver)(nil)
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
