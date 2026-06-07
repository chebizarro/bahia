package runtime

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestConfigRuntimeResolver_Precedence(t *testing.T) {
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

	rt, err := resolver.Resolve(svc, env)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	compose, ok := rt.(*ComposeRuntime)
	if !ok {
		t.Fatalf("Resolve() returned %T, want *ComposeRuntime", rt)
	}
	if compose.projectDir != "/srv/prod-from-env" {
		t.Errorf("compose.projectDir = %q, want env runtime_config override", compose.projectDir)
	}
	if compose.dockerHost != "tcp://prod:2375" {
		t.Errorf("compose.dockerHost = %q, want named environment override", compose.dockerHost)
	}
}

func TestConfigRuntimeResolver_ServiceRuntimeTypeBeatsDefaultType(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{Type: "docker", ComposeDir: "/srv/default", ExecutionMode: "cli"},
	}, zap.NewNop(), nil)

	rt, err := resolver.Resolve(
		resolverTestService(domain.RuntimeTypeCompose),
		resolverTestEnv("staging", nil),
	)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if rt.Type() != domain.RuntimeTypeCompose {
		t.Errorf("runtime type = %s, want compose", rt.Type())
	}
}

func TestConfigRuntimeResolver_LegacyFlatConfigIsNotOverriddenByBuiltInDefaults(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Type:       "docker",
		DockerHost: "remote-docker:2375",
	}, zap.NewNop(), nil)

	rt, err := resolver.Resolve(
		resolverTestService(domain.RuntimeTypeDocker),
		resolverTestEnv("production", nil),
	)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	docker, ok := rt.(*DockerObserver)
	if !ok {
		t.Fatalf("Resolve() returned %T, want *DockerObserver", rt)
	}
	if docker.host != "http://remote-docker:2375" {
		t.Errorf("docker host = %q, want legacy flat docker_host", docker.host)
	}
}

func TestConfigRuntimeResolver_ComposeRequiresComposeDir(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{}, zap.NewNop(), nil)

	_, err := resolver.Resolve(
		resolverTestService(domain.RuntimeTypeCompose),
		resolverTestEnv("production", nil),
	)
	if err == nil {
		t.Fatal("expected missing compose_dir error")
	}
	if !strings.Contains(err.Error(), "compose_dir is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigRuntimeResolver_ComposeBahiaOwnedConfigFeedsOwnershipGate(t *testing.T) {
	dir := t.TempDir()
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{
			Type:          "compose",
			ComposeDir:    dir,
			BahiaOwned:    boolPtr(true),
			ExecutionMode: "cli",
		},
	}, zap.NewNop(), nil)

	rt, err := resolver.Resolve(
		resolverTestService(domain.RuntimeTypeCompose),
		resolverTestEnv("production", nil),
	)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	compose, ok := rt.(*ComposeRuntime)
	if !ok {
		t.Fatalf("Resolve() returned %T, want *ComposeRuntime", rt)
	}
	if err := compose.ValidateOwnership(ComposeOwnershipConfig{}); err != nil {
		t.Fatalf("expected configured bahia_owned=true to pass ownership gate, got: %v", err)
	}
}

func TestConfigRuntimeResolver_ComposeRuntimeConfigBahiaOwnedFalseBlocksUnmarkedDir(t *testing.T) {
	dir := t.TempDir()
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{
			Type:          "compose",
			ComposeDir:    dir,
			BahiaOwned:    boolPtr(true),
			ExecutionMode: "cli",
		},
	}, zap.NewNop(), nil)

	rt, err := resolver.Resolve(
		resolverTestService(domain.RuntimeTypeCompose),
		resolverTestEnv("production", map[string]any{"bahia_owned": false}),
	)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	compose, ok := rt.(*ComposeRuntime)
	if !ok {
		t.Fatalf("Resolve() returned %T, want *ComposeRuntime", rt)
	}
	err = compose.ValidateOwnership(ComposeOwnershipConfig{})
	if err == nil {
		t.Fatal("expected bahia_owned=false with no marker to block ownership gate")
	}
	ownershipErr, ok := AsComposeOwnershipError(err)
	if !ok {
		t.Fatalf("expected ComposeOwnershipError, got %T: %v", err, err)
	}
	if ownershipErr.Reason != OwnershipNotOwned {
		t.Fatalf("ownership reason = %s, want not_owned", ownershipErr.ReasonCode)
	}
}

func TestConfigRuntimeResolver_EnvironmentTypeConflict(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{Type: "docker"},
		Environments: map[string]config.RuntimeTargetConfig{
			"production": {Type: "compose"},
		},
	}, zap.NewNop(), nil)

	_, err := resolver.Resolve(
		resolverTestService(domain.RuntimeTypeDocker),
		resolverTestEnv("production", nil),
	)
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !strings.Contains(err.Error(), "runtime type conflict") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigRuntimeResolver_RuntimeConfigTypeConflict(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{}, zap.NewNop(), nil)

	_, err := resolver.Resolve(
		resolverTestService(domain.RuntimeTypeDocker),
		resolverTestEnv("production", map[string]any{"type": "compose"}),
	)
	if err == nil {
		t.Fatal("expected conflict error")
	}
}

func TestConfigRuntimeResolver_CachesByResolvedTarget(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{Type: "docker", DockerHost: "unix:///var/run/docker.sock"},
	}, zap.NewNop(), nil)

	svc := resolverTestService(domain.RuntimeTypeDocker)
	env := resolverTestEnv("production", nil)

	rt1, err := resolver.Resolve(svc, env)
	if err != nil {
		t.Fatalf("Resolve() #1 error: %v", err)
	}
	rt2, err := resolver.Resolve(svc, env)
	if err != nil {
		t.Fatalf("Resolve() #2 error: %v", err)
	}
	if rt1 != rt2 {
		t.Fatal("expected cached runtime instance")
	}
}

func TestConfigRuntimeResolver_EndpointRef(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{
		Default: config.RuntimeTargetConfig{Type: "docker"},
		Endpoints: map[string]config.RuntimeEndpointConfig{
			"prod-docker": {
				DockerHost:         "tcp://docker-prod:2376",
				InsecureSkipVerify: true,
			},
		},
	}, zap.NewNop(), nil)

	rt, err := resolver.Resolve(
		resolverTestService(domain.RuntimeTypeDocker),
		resolverTestEnv("production", map[string]any{"endpoint_ref": "prod-docker"}),
	)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	docker, ok := rt.(*DockerObserver)
	if !ok {
		t.Fatalf("Resolve() returned %T, want *DockerObserver", rt)
	}
	if docker.host != "https://docker-prod:2376" {
		t.Fatalf("docker host = %q, want TLS endpoint host", docker.host)
	}
}

func TestConfigRuntimeResolver_UnknownEndpointRef(t *testing.T) {
	resolver := NewConfigRuntimeResolver(config.RuntimeConfig{}, zap.NewNop(), nil)
	_, err := resolver.Resolve(
		resolverTestService(domain.RuntimeTypeDocker),
		resolverTestEnv("production", map[string]any{"endpoint_ref": "missing"}),
	)
	if err == nil || !strings.Contains(err.Error(), `endpoint_ref "missing"`) {
		t.Fatalf("Resolve() error = %v, want unknown endpoint_ref", err)
	}
}

func resolverTestService(rt domain.RuntimeType) *domain.Service {
	return &domain.Service{
		ID:          uuid.New(),
		Name:        "api",
		RuntimeType: rt,
	}
}

func resolverTestEnv(name string, runtimeConfig map[string]any) *domain.Environment {
	return &domain.Environment{
		ID:            uuid.New(),
		Name:          name,
		RuntimeConfig: runtimeConfig,
	}
}
