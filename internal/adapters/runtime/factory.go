package runtime

import (
	"fmt"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// RuntimeConfig holds the configuration needed to create a Runtime.
type RuntimeConfig struct {
	Type          string // "docker", "compose", "kubernetes"
	DockerHost    string // Docker socket or TCP address
	ComposeDir    string // Directory containing docker-compose.yml
	KubeContext   string // Kubernetes context name
	KubeNamespace string // Kubernetes namespace
	KubeConfig    string // Path to kubeconfig file
}

// NewRuntime creates a Runtime implementation based on the config type.
func NewRuntime(cfg RuntimeConfig, logger *zap.Logger) (Runtime, error) {
	rt := domain.RuntimeType(cfg.Type)
	if rt == "" {
		rt = domain.RuntimeTypeDocker
	}

	switch rt {
	case domain.RuntimeTypeDocker:
		host := cfg.DockerHost
		if host == "" {
			host = "unix:///var/run/docker.sock"
		}
		return NewDockerObserver(host, logger), nil

	case domain.RuntimeTypeCompose:
		dir := cfg.ComposeDir
		if dir == "" {
			dir = "."
		}
		return NewComposeRuntime(dir, logger), nil

	case domain.RuntimeTypeK8s:
		return NewKubernetesRuntime(
			cfg.KubeContext,
			cfg.KubeNamespace,
			cfg.KubeConfig,
			logger,
		), nil

	default:
		return nil, fmt.Errorf("unsupported runtime type: %q", cfg.Type)
	}
}

// NewObserver creates a Runtime and returns it as an Observer.
// This is a convenience for callers that only need the Observer interface
// (e.g., the reconciler).
func NewObserver(cfg RuntimeConfig, logger *zap.Logger) (Observer, error) {
	return NewRuntime(cfg, logger)
}

// MustRuntime creates a Runtime, panicking on failure. Useful in tests.
func MustRuntime(cfg RuntimeConfig, logger *zap.Logger) Runtime {
	rt, err := NewRuntime(cfg, logger)
	if err != nil {
		panic(err)
	}
	return rt
}
