package runtime

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// RuntimeResolver resolves the concrete runtime adapter for a service in an environment.
type RuntimeResolver interface {
	Resolve(service *domain.Service, env *domain.Environment) (Runtime, error)
}

// ConfigRuntimeResolver resolves runtime targets from global config plus
// environment-scoped overrides and caches runtime instances per resolved target.
type ConfigRuntimeResolver struct {
	cfg          config.RuntimeConfig
	logger       *zap.Logger
	registryAuth *RegistryAuthConfig

	mu    sync.Mutex
	cache map[string]Runtime
}

// NewConfigRuntimeResolver creates a runtime resolver backed by Bahia config.
func NewConfigRuntimeResolver(cfg config.RuntimeConfig, logger *zap.Logger, registryAuth *RegistryAuthConfig) *ConfigRuntimeResolver {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ConfigRuntimeResolver{
		cfg:          cfg,
		logger:       logger,
		registryAuth: registryAuth,
		cache:        make(map[string]Runtime),
	}
}

// Resolve returns a runtime instance for the supplied service and environment.
func (r *ConfigRuntimeResolver) Resolve(service *domain.Service, env *domain.Environment) (Runtime, error) {
	if service == nil {
		return nil, fmt.Errorf("service is required for runtime resolution")
	}
	if env == nil {
		return nil, fmt.Errorf("environment is required for runtime resolution")
	}

	target, envTypeExplicit, err := r.resolveTarget(env)
	if err != nil {
		return nil, err
	}
	serviceType := service.RuntimeType
	if serviceType != "" && envTypeExplicit && target.Type != "" && domain.RuntimeType(target.Type) != serviceType {
		return nil, fmt.Errorf("runtime type conflict for environment %q: service %q requires %q but environment target config specifies %q", env.Name, service.Name, serviceType, target.Type)
	}

	if serviceType != "" {
		target.Type = string(serviceType)
	}

	key := runtimeCacheKey(target)

	r.mu.Lock()
	defer r.mu.Unlock()
	if rt, ok := r.cache[key]; ok {
		return rt, nil
	}

	rt, err := NewRuntime(RuntimeConfig{
		Type:          target.Type,
		DockerHost:    target.DockerHost,
		ComposeDir:    target.ComposeDir,
		BahiaOwned:    target.BahiaOwned,
		ExecutionMode: target.ExecutionMode,
		Endpoint:      target.ResolvedEndpoint,
		RegistryAuth:  r.registryAuth,
		KubeContext:   target.KubeContext,
		KubeNamespace: target.KubeNamespace,
		KubeConfig:    target.KubeConfig,
	}, r.logger)
	if err != nil {
		return nil, err
	}
	r.cache[key] = rt
	return rt, nil
}

func (r *ConfigRuntimeResolver) resolveTarget(env *domain.Environment) (config.RuntimeTargetConfig, bool, error) {
	target := config.RuntimeTargetConfig{
		Type:          r.cfg.Type,
		DockerHost:    r.cfg.DockerHost,
		EndpointRef:   "",
		ComposeDir:    r.cfg.ComposeDir,
		BahiaOwned:    r.cfg.BahiaOwned,
		ExecutionMode: r.cfg.ExecutionMode,
		KubeContext:   r.cfg.KubeContext,
		KubeNamespace: r.cfg.KubeNamespace,
		KubeConfig:    r.cfg.KubeConfig,
	}

	target = overlayTarget(target, r.cfg.Default)

	envTypeExplicit := false
	if r.cfg.Environments != nil {
		if envTarget, ok := r.cfg.Environments[env.Name]; ok {
			if strings.TrimSpace(envTarget.Type) != "" {
				envTypeExplicit = true
			}
			target = overlayTarget(target, envTarget)
		}
	}

	if env.RuntimeConfig != nil {
		var fromRuntimeConfig bool
		target, fromRuntimeConfig = overlayRuntimeConfig(target, env.RuntimeConfig)
		envTypeExplicit = envTypeExplicit || fromRuntimeConfig
	}

	if err := r.applyEndpointRef(&target); err != nil {
		return target, envTypeExplicit, err
	}
	return target, envTypeExplicit, nil
}

func overlayTarget(base, override config.RuntimeTargetConfig) config.RuntimeTargetConfig {
	if override.Type != "" {
		base.Type = override.Type
	}
	if override.DockerHost != "" {
		base.DockerHost = override.DockerHost
	}
	if override.EndpointRef != "" {
		base.EndpointRef = override.EndpointRef
	}
	if override.ComposeDir != "" {
		base.ComposeDir = override.ComposeDir
	}
	if override.BahiaOwned != nil {
		base.BahiaOwned = override.BahiaOwned
	}
	if override.ExecutionMode != "" {
		base.ExecutionMode = override.ExecutionMode
	}
	if override.KubeContext != "" {
		base.KubeContext = override.KubeContext
	}
	if override.KubeNamespace != "" {
		base.KubeNamespace = override.KubeNamespace
	}
	if override.KubeConfig != "" {
		base.KubeConfig = override.KubeConfig
	}
	return base
}

func overlayRuntimeConfig(base config.RuntimeTargetConfig, values map[string]any) (config.RuntimeTargetConfig, bool) {
	typeExplicit := false
	for _, key := range sortedRuntimeConfigKeys(values) {
		if key == "bahia_owned" {
			if boolValue, ok := boolValue(values[key]); ok {
				base.BahiaOwned = &boolValue
			}
			continue
		}
		value, ok := stringValue(values[key])
		if !ok || value == "" {
			continue
		}
		switch key {
		case "type":
			base.Type = value
			typeExplicit = true
		case "docker_host":
			base.DockerHost = value
		case "endpoint_ref":
			base.EndpointRef = value
		case "compose_dir":
			base.ComposeDir = value
		case "execution_mode":
			base.ExecutionMode = value
		case "kube_context":
			base.KubeContext = value
		case "kube_namespace":
			base.KubeNamespace = value
		case "kube_config":
			base.KubeConfig = value
		}
	}
	return base, typeExplicit
}

func (r *ConfigRuntimeResolver) applyEndpointRef(target *config.RuntimeTargetConfig) error {
	ref := strings.TrimSpace(target.EndpointRef)
	if ref == "" {
		return nil
	}
	endpoint, ok := r.cfg.Endpoints[ref]
	if !ok {
		return fmt.Errorf("runtime endpoint_ref %q is not configured", ref)
	}
	if strings.TrimSpace(endpoint.DockerHost) == "" {
		return fmt.Errorf("runtime endpoint_ref %q has no docker_host", ref)
	}
	endpoint.Ref = ref
	target.EndpointRef = ref
	target.DockerHost = endpoint.DockerHost
	target.ResolvedEndpoint = endpoint
	return nil
}

func sortedRuntimeConfigKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringValue(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(s), true
}

func boolValue(v any) (bool, bool) {
	switch typed := v.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "y", "on":
			return true, true
		case "false", "0", "no", "n", "off":
			return false, true
		}
	}
	return false, false
}

func targetOwnershipCacheValue(v *bool) string {
	if v == nil {
		return "unset"
	}
	return fmt.Sprintf("%t", *v)
}

func runtimeCacheKey(target config.RuntimeTargetConfig) string {
	return strings.Join([]string{
		target.Type,
		target.DockerHost,
		target.EndpointRef,
		target.ResolvedEndpoint.CACertFile,
		target.ResolvedEndpoint.ClientCertFile,
		target.ResolvedEndpoint.ClientKeyFile,
		fmt.Sprintf("%t", target.ResolvedEndpoint.InsecureSkipVerify),
		target.ComposeDir,
		targetOwnershipCacheValue(target.BahiaOwned),
		target.ExecutionMode,
		target.KubeContext,
		target.KubeNamespace,
		target.KubeConfig,
	}, "\x00")
}

// ResolveDesiredStateApplier resolves a runtime for the given service and
// environment using the full config overlay path (runtime.default, environment
// overrides, service runtime type, endpoint alias resolution, TLS) and then
// probes for the DesiredStateApplier capability.
//
// Returns ErrDesiredStateNotSupported if the resolved runtime does not implement
// desired-state convergence or explicitly reports it as unsupported.
func (r *ConfigRuntimeResolver) ResolveDesiredStateApplier(service *domain.Service, env *domain.Environment) (DesiredStateApplier, error) {
	rt, err := r.Resolve(service, env)
	if err != nil {
		return nil, err
	}

	applier, ok := rt.(DesiredStateApplier)
	if !ok {
		return nil, fmt.Errorf("%w: runtime type %q does not implement DesiredStateApplier", ErrDesiredStateNotSupported, rt.Type())
	}
	if !applier.SupportsDesiredState() {
		return nil, fmt.Errorf("%w: runtime type %q has not been migrated to desired-state convergence", ErrDesiredStateNotSupported, rt.Type())
	}
	return applier, nil
}

var _ RuntimeResolver = (*ConfigRuntimeResolver)(nil)
