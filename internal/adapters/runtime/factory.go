package runtime

import (
	"fmt"
	"os"
	"strings"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// RuntimeConfig holds the configuration needed to create a Runtime.
type RuntimeConfig struct {
	Type          string // "docker", "compose", "kubernetes", "podman"
	DockerHost    string // Docker socket or TCP address
	PodmanHost    string // Podman socket path (defaults to rootless user socket)
	ComposeDir    string // Directory containing docker-compose.yml
	Endpoint      config.RuntimeEndpointConfig
	RegistryAuth  *RegistryAuthConfig
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
		endpoint := cfg.Endpoint
		if strings.TrimSpace(endpoint.DockerHost) == "" {
			endpoint.DockerHost = cfg.DockerHost
		}
		if strings.TrimSpace(endpoint.DockerHost) == "" {
			endpoint.DockerHost = "unix:///var/run/docker.sock"
		}
		rt, err := NewDockerObserverWithEndpoint(endpoint, logger)
		if err != nil {
			return nil, err
		}
		rt.registryAuth = cfg.RegistryAuth
		return rt, nil

	case domain.RuntimeTypeCompose:
		dir := cfg.ComposeDir
		if dir == "" {
			return nil, fmt.Errorf("compose_dir is required for compose runtime")
		}
		endpoint := cfg.Endpoint
		if strings.TrimSpace(endpoint.DockerHost) == "" {
			endpoint.DockerHost = cfg.DockerHost
		}
		return NewComposeRuntimeWithEndpoint(dir, endpoint, logger)

	case domain.RuntimeTypeK8s:
		return NewKubernetesRuntime(
			cfg.KubeContext,
			cfg.KubeNamespace,
			cfg.KubeConfig,
			logger,
		), nil

	case domain.RuntimeTypePodman:
		host := cfg.PodmanHost
		if host == "" {
			// Default to rootless Podman socket
			host = fmt.Sprintf("unix:///run/user/%d/podman/podman.sock", os.Getuid())
		}
		return NewPodmanObserver(host, logger), nil

	default:
		return nil, fmt.Errorf("unsupported runtime type: %q", cfg.Type)
	}
}

// NewRuntimeFromWorkerTarget creates a Runtime from a worker-advertised runtime
// target using the same endpoint resolution and adapter construction as
// NewRuntime. It is intended for higher-level provisioners that place work on a
// selected worker instead of resolving a service/environment pair.
func NewRuntimeFromWorkerTarget(target *domain.WorkerRuntimeTarget, cfg config.RuntimeConfig, logger *zap.Logger) (Runtime, error) {
	runtimeCfg, err := RuntimeConfigFromWorkerTarget(target, cfg)
	if err != nil {
		return nil, err
	}
	return NewRuntime(runtimeCfg, logger)
}

// RuntimeConfigFromWorkerTarget translates a worker-advertised runtime target
// into the concrete RuntimeConfig consumed by NewRuntime.
func RuntimeConfigFromWorkerTarget(target *domain.WorkerRuntimeTarget, cfg config.RuntimeConfig) (RuntimeConfig, error) {
	if target == nil {
		return RuntimeConfig{}, fmt.Errorf("worker runtime target is required")
	}
	rt := target.Type
	if rt == "" {
		rt = domain.RuntimeTypeDocker
	}
	if err := domain.ValidateRuntimeType(rt); err != nil {
		return RuntimeConfig{}, err
	}

	runtimeCfg := RuntimeConfig{
		Type:          string(rt),
		ComposeDir:    strings.TrimSpace(target.ComposeDir),
		KubeNamespace: strings.TrimSpace(target.KubeNamespace),
	}

	ref := strings.TrimSpace(target.EndpointRef)
	if ref == "" && rt != domain.RuntimeTypeK8s {
		return RuntimeConfig{}, fmt.Errorf("worker runtime target endpoint_ref is required for %s runtime", rt)
	}
	if ref != "" {
		endpoint, ok := cfg.Endpoints[ref]
		if !ok {
			return RuntimeConfig{}, fmt.Errorf("runtime endpoint_ref %q is not configured", ref)
		}
		if strings.TrimSpace(endpoint.DockerHost) == "" {
			return RuntimeConfig{}, fmt.Errorf("runtime endpoint_ref %q has no docker_host", ref)
		}
		endpoint.Ref = ref
		runtimeCfg.Endpoint = endpoint
		runtimeCfg.DockerHost = endpoint.DockerHost
	}

	switch rt {
	case domain.RuntimeTypeCompose:
		if runtimeCfg.ComposeDir == "" {
			runtimeCfg.ComposeDir = strings.TrimSpace(cfg.ComposeDir)
		}
		if runtimeCfg.ComposeDir == "" {
			return RuntimeConfig{}, fmt.Errorf("compose_dir is required for worker runtime target")
		}
	case domain.RuntimeTypeK8s:
		if runtimeCfg.KubeNamespace == "" {
			runtimeCfg.KubeNamespace = strings.TrimSpace(cfg.KubeNamespace)
		}
		runtimeCfg.KubeContext = strings.TrimSpace(cfg.KubeContext)
		runtimeCfg.KubeConfig = strings.TrimSpace(cfg.KubeConfig)
	}

	if runtimeCfg.DockerHost == "" {
		runtimeCfg.DockerHost = strings.TrimSpace(cfg.DockerHost)
	}
	return runtimeCfg, nil
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
