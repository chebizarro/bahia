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
	Type          string // "docker", "compose", "kubernetes", "podman", "vm-firecracker", "vm-qemu"
	DockerHost    string // Docker socket or TCP address
	PodmanHost    string // Podman socket path (defaults to rootless user socket)
	ComposeDir    string // Directory containing docker-compose.yml
	BahiaOwned    *bool  // Explicit operator assertion that compose_dir is Bahia-owned
	ExecutionMode string // "engine_api", "cli", or "sdk"; compose requires explicit "cli" or "sdk"
	Endpoint      config.RuntimeEndpointConfig
	RegistryAuth  *RegistryAuthConfig
	KubeContext   string                 // Kubernetes context name
	KubeNamespace string                 // Kubernetes namespace
	KubeConfig    string                 // Path to kubeconfig file
	VM            config.RuntimeVMConfig // VM runtime settings (vm-firecracker, vm-qemu only)
}

// NewRuntime creates a Runtime implementation based on the config type.
func NewRuntime(cfg RuntimeConfig, logger *zap.Logger) (Runtime, error) {
	rt := domain.RuntimeType(cfg.Type)
	if rt == "" {
		rt = domain.RuntimeTypeDocker
	}

	// VM settings on a non-VM runtime type are an explicit configuration
	// error rather than silently ignored fields.
	if rt != domain.RuntimeTypeVMFirecracker && rt != domain.RuntimeTypeVMQEMU && !cfg.VM.Empty() {
		return nil, fmt.Errorf("vm.* runtime settings are not valid for runtime type %q (allowed only for vm-firecracker and vm-qemu)", rt)
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
		mode := normalizeRuntimeExecutionMode(cfg.ExecutionMode)
		if mode != ExecutionModeCLI && mode != ExecutionModeSDK {
			return nil, fmt.Errorf("compose runtime requires explicit execution_mode %q (CLI compatibility mode) or %q (embedded Compose SDK)", ExecutionModeCLI, ExecutionModeSDK)
		}
		endpoint := cfg.Endpoint
		if strings.TrimSpace(endpoint.DockerHost) == "" {
			endpoint.DockerHost = cfg.DockerHost
		}
		rt, err := newComposeRuntimeWithEndpointForMode(dir, endpoint, mode, logger)
		if err != nil {
			return nil, err
		}
		rt.ownershipConfig = ComposeOwnershipConfig{BahiaOwned: cfg.BahiaOwned}
		return rt, nil

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
		// When compose_dir is set with Podman, use Podman Compose runtime
		// instead of the Engine API-only PodmanObserver.
		if cfg.ComposeDir != "" {
			if normalizeRuntimeExecutionMode(cfg.ExecutionMode) != ExecutionModeCLI {
				return nil, fmt.Errorf("podman compose runtime requires execution_mode %q", ExecutionModeCLI)
			}
			rt := NewPodmanComposeRuntime(cfg.ComposeDir, host, logger)
			rt.ownershipConfig = ComposeOwnershipConfig{BahiaOwned: cfg.BahiaOwned}
			return rt, nil
		}
		return NewPodmanObserver(host, logger), nil

	case domain.RuntimeTypeVMQEMU:
		if err := validateVMRuntimeConfig(rt, cfg); err != nil {
			return nil, err
		}
		return newVMQEMURuntime(cfg, logger)

	case domain.RuntimeTypeVMFirecracker:
		if err := validateVMRuntimeConfig(rt, cfg); err != nil {
			return nil, err
		}
		// The shared VM core is in place; fail explicitly until the
		// Firecracker hypervisor driver lands (plan work item 6).
		return nil, fmt.Errorf("runtime type %q is not yet wired: the Firecracker hypervisor driver has not landed yet", rt)

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
		ExecutionMode: strings.TrimSpace(target.ExecutionMode),
		KubeNamespace: strings.TrimSpace(target.KubeNamespace),
	}

	// Kubernetes targets resolve through kubeconfig contexts and VM targets
	// are host-local, so neither requires a Docker endpoint reference.
	endpointOptional := rt == domain.RuntimeTypeK8s ||
		rt == domain.RuntimeTypeVMFirecracker ||
		rt == domain.RuntimeTypeVMQEMU
	ref := strings.TrimSpace(target.EndpointRef)
	if ref == "" && !endpointOptional {
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
	case domain.RuntimeTypeVMFirecracker, domain.RuntimeTypeVMQEMU:
		runtimeCfg.VM = cfg.VM
	}

	if runtimeCfg.DockerHost == "" {
		runtimeCfg.DockerHost = strings.TrimSpace(cfg.DockerHost)
	}
	if runtimeCfg.ExecutionMode == "" {
		runtimeCfg.ExecutionMode = strings.TrimSpace(cfg.ExecutionMode)
	}
	return runtimeCfg, nil
}

// validateVMRuntimeConfig rejects VM runtime settings that are unknown for
// the concrete VM runtime type (explicit-failure convention). Non-VM fields
// such as compose_dir or kube_* are not rejected here because the resolver
// overlays them from global defaults in mixed-runtime configurations; only
// fields that must have been set explicitly for a VM target are checked.
func validateVMRuntimeConfig(rt domain.RuntimeType, cfg RuntimeConfig) error {
	if rt == domain.RuntimeTypeVMFirecracker && strings.TrimSpace(cfg.VM.LibvirtURI) != "" {
		return fmt.Errorf("vm.libvirt_uri is not valid for runtime type %q (libvirt applies only to vm-qemu)", rt)
	}
	return nil
}

func normalizeRuntimeExecutionMode(mode string) RuntimeExecutionMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(ExecutionModeCLI):
		return ExecutionModeCLI
	case string(ExecutionModeEngineAPI):
		return ExecutionModeEngineAPI
	case string(ExecutionModeSDK):
		return ExecutionModeSDK
	default:
		return ""
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
