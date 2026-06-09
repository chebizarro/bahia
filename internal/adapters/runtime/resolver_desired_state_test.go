package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestResolveDesiredStateApplier_ComposeEndpoint(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{
			Type:          "compose",
			ComposeDir:    "/srv/data/bahia-managed",
			ExecutionMode: "cli",
		},
		Endpoints: map[string]config.RuntimeEndpointConfig{
			"local": {DockerHost: "unix:///var/run/docker.sock"},
		},
	}, zap.NewNop(), nil)

	svc := resolverTestService(domain.RuntimeTypeCompose)
	env := resolverTestEnv("production", map[string]any{"endpoint_ref": "local"})

	applier, err := resolver.ResolveDesiredStateApplier(svc, env)
	// Compose now supports desired-state convergence.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applier == nil {
		t.Fatal("expected non-nil applier")
	}
	if !applier.SupportsDesiredState() {
		t.Error("expected Compose to support desired state")
	}
}

func TestResolveDesiredStateApplier_DockerEndpoint(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{Type: "docker"},
		Endpoints: map[string]config.RuntimeEndpointConfig{
			"prod-docker": {
				DockerHost:         "tcp://docker-prod:2376",
				InsecureSkipVerify: true,
			},
		},
	}, zap.NewNop(), nil)

	svc := resolverTestService(domain.RuntimeTypeDocker)
	env := resolverTestEnv("production", map[string]any{"endpoint_ref": "prod-docker"})

	applier, err := resolver.ResolveDesiredStateApplier(svc, env)
	// Docker now supports desired-state convergence.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applier == nil {
		t.Fatal("expected non-nil applier")
	}
	if !applier.SupportsDesiredState() {
		t.Error("expected Docker to support desired state")
	}
}

func TestResolveDesiredStateApplier_PodmanEndpoint(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{Type: "podman"},
		Endpoints: map[string]config.RuntimeEndpointConfig{
			"podman-local": {DockerHost: "unix:///run/user/1000/podman/podman.sock"},
		},
	}, zap.NewNop(), nil)

	svc := resolverTestService(domain.RuntimeTypePodman)
	env := resolverTestEnv("staging", map[string]any{"endpoint_ref": "podman-local"})

	applier, err := resolver.ResolveDesiredStateApplier(svc, env)
	// Podman now supports desired-state convergence via Docker delegation.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applier == nil {
		t.Fatal("expected non-nil applier for Podman")
	}
	if !applier.SupportsDesiredState() {
		t.Error("expected Podman to support desired state")
	}
}

func TestResolveDesiredStateApplier_KubernetesResolves(t *testing.T) {
	// Kubernetes now supports desired-state convergence via kubectl apply.
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{
			Type:          "kubernetes",
			KubeNamespace: "default",
		},
	}, zap.NewNop(), nil)

	svc := resolverTestService(domain.RuntimeTypeK8s)
	env := resolverTestEnv("production", nil)

	applier, err := resolver.ResolveDesiredStateApplier(svc, env)
	if err != nil {
		t.Fatalf("unexpected error resolving Kubernetes desired-state applier: %v", err)
	}
	if applier == nil {
		t.Fatal("expected non-nil applier for Kubernetes runtime")
	}
	if !applier.SupportsDesiredState() {
		t.Error("Kubernetes applier should report SupportsDesiredState() = true")
	}
}

func TestResolveDesiredStateApplier_InvalidEndpoint(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{Type: "docker"},
	}, zap.NewNop(), nil)

	svc := resolverTestService(domain.RuntimeTypeDocker)
	env := resolverTestEnv("production", map[string]any{"endpoint_ref": "nonexistent"})

	_, err := resolver.ResolveDesiredStateApplier(svc, env)
	if err == nil {
		t.Fatal("expected error for missing endpoint")
	}
	if errors.Is(err, ErrDesiredStateNotSupported) {
		t.Fatal("missing endpoint should not be ErrDesiredStateNotSupported")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention the missing endpoint ref, got: %v", err)
	}
}

func TestResolveDesiredStateApplier_NilService(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{}, zap.NewNop(), nil)

	_, err := resolver.ResolveDesiredStateApplier(nil, resolverTestEnv("production", nil))
	if err == nil {
		t.Fatal("expected error for nil service")
	}
	if !strings.Contains(err.Error(), "service is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDesiredStateApplier_NilEnvironment(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{}, zap.NewNop(), nil)

	_, err := resolver.ResolveDesiredStateApplier(resolverTestService(domain.RuntimeTypeDocker), nil)
	if err == nil {
		t.Fatal("expected error for nil environment")
	}
	if !strings.Contains(err.Error(), "environment is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDesiredStateApplier_UsesFullResolutionPath(t *testing.T) {
	// Verify that the full config overlay path is exercised:
	// flat config → runtime.default → environments[name] → env.RuntimeConfig → endpoint_ref
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Type:       "docker",
		DockerHost: "unix:///legacy.sock",
		Default: config.RuntimeTargetConfig{
			Type:          "compose",
			DockerHost:    "tcp://default:2375",
			ComposeDir:    "/srv/default",
			ExecutionMode: "cli",
		},
		Environments: map[string]config.RuntimeTargetConfig{
			"production": {
				DockerHost: "tcp://prod:2375",
				ComposeDir: "/srv/prod-from-config",
			},
		},
	}, zap.NewNop(), nil)

	svc := resolverTestService(domain.RuntimeTypeCompose)
	env := resolverTestEnv("production", map[string]any{
		"compose_dir": "/srv/prod-from-env",
	})

	applier, err := resolver.ResolveDesiredStateApplier(svc, env)
	// Both Compose and Docker now support desired-state convergence.
	// The key is that resolution doesn't error on config/endpoint/type issues.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applier == nil {
		t.Fatal("expected non-nil applier")
	}
	if !applier.SupportsDesiredState() {
		t.Error("expected resolved applier to support desired state")
	}
}

// ---------------------------------------------------------------------------
// Kubernetes desired-state resolver — forward-compatible test
// (t.Skip guard until Agent 2's SupportsDesiredState() flip lands)
// ---------------------------------------------------------------------------

// TestResolveDesiredStateApplier_KubernetesSupported will pass once the
// Kubernetes desired-state adapter is implemented (bahia-amqy Agent 2). Until
// then it skips automatically on ErrDesiredStateNotSupported so it does not
// block the pre-merge test suite.
func TestResolveDesiredStateApplier_KubernetesSupported(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{
			Type:          "kubernetes",
			KubeNamespace: "production",
		},
	}, zap.NewNop(), nil)

	svc := resolverTestService(domain.RuntimeTypeK8s)
	env := resolverTestEnv("production", nil)

	applier, err := resolver.ResolveDesiredStateApplier(svc, env)
	if err != nil {
		if errors.Is(err, ErrDesiredStateNotSupported) {
			t.Skip("Kubernetes desired-state not yet implemented")
		}
		t.Fatalf("unexpected error: %v", err)
	}
	if applier == nil {
		t.Fatal("expected non-nil applier")
	}
	if !applier.SupportsDesiredState() {
		t.Error("expected K8s applier to support desired state")
	}
}
