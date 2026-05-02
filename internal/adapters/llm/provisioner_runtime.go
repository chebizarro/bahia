package llm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// RuntimeFactory resolves a Bahia runtime adapter for a worker runtime target.
type RuntimeFactory func(target *domain.WorkerRuntimeTarget) (runtimeadapter.Runtime, error)

// RuntimeProvisioner provisions runtime-managed LLM backend families.
type RuntimeProvisioner struct {
	kind           domain.LLMBackendKind
	runtimeFactory RuntimeFactory
	httpClient     *http.Client
}

// RuntimeProvisionerOption customizes a RuntimeProvisioner.
type RuntimeProvisionerOption func(*RuntimeProvisioner)

// WithRuntimeFactory injects a runtime factory, primarily for tests.
func WithRuntimeFactory(factory RuntimeFactory) RuntimeProvisionerOption {
	return func(p *RuntimeProvisioner) {
		if factory != nil {
			p.runtimeFactory = factory
		}
	}
}

// WithHTTPClient injects the health-probe HTTP client.
func WithHTTPClient(client *http.Client) RuntimeProvisionerOption {
	return func(p *RuntimeProvisioner) {
		if client != nil {
			p.httpClient = client
		}
	}
}

// NewRuntimeProvisioner creates a provisioner for vllm, ollama, or llama_cpp.
func NewRuntimeProvisioner(kind domain.LLMBackendKind, runtimeCfg config.RuntimeConfig, logger *zap.Logger, opts ...RuntimeProvisionerOption) (*RuntimeProvisioner, error) {
	if !runtimeManagedKind(kind) {
		return nil, fmt.Errorf("backend kind %q is not runtime-managed", kind)
	}
	p := &RuntimeProvisioner{
		kind: kind,
		runtimeFactory: func(target *domain.WorkerRuntimeTarget) (runtimeadapter.Runtime, error) {
			return runtimeadapter.NewRuntimeFromWorkerTarget(target, runtimeCfg, logger)
		},
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

func (p *RuntimeProvisioner) Provision(ctx context.Context, req ProvisionCandidateRequest) (*ProvisionCandidateResult, error) {
	if req.BackendKind == "" {
		req.BackendKind = p.kind
	}
	if req.BackendKind != p.kind {
		return nil, fmt.Errorf("runtime provisioner for %q cannot provision %q", p.kind, req.BackendKind)
	}
	cfg, err := runtimeBackendConfig(req)
	if err != nil {
		return nil, err
	}
	if req.Worker == nil || req.Worker.RuntimeTarget == nil {
		return nil, fmt.Errorf("worker runtime_target is required for %s backend", req.BackendKind)
	}
	rt, err := p.runtimeFactory(req.Worker.RuntimeTarget)
	if err != nil {
		return nil, fmt.Errorf("resolve worker runtime target: %w", err)
	}
	targetName := targetNameFor(req)
	opts := deployOptionsFromRuntimeBackend(req, cfg)
	if err := rt.Deploy(ctx, targetName, cfg.Image, opts); err != nil {
		return nil, fmt.Errorf("deploy LLM backend %q: %w", targetName, err)
	}
	endpoint, err := backendEndpoint(req.Worker.RuntimeTarget, cfg)
	if err != nil {
		return nil, err
	}
	return &ProvisionCandidateResult{
		BackendKind:     req.BackendKind,
		BackendEndpoint: endpoint,
		EndpointRef:     req.Worker.RuntimeTarget.EndpointRef,
		TargetName:      targetName,
		WorkerPubkey:    req.Worker.PubKey,
		WorkerName:      req.Worker.Name,
		Metadata: map[string]any{
			"runtime_target": req.Worker.RuntimeTarget,
			"health_path":    cfg.HealthPath,
		},
	}, nil
}

func (p *RuntimeProvisioner) Observe(ctx context.Context, req ProvisionCandidateRequest) (*BackendObservation, error) {
	if req.BackendKind == "" {
		req.BackendKind = p.kind
	}
	cfg, err := runtimeBackendConfig(req)
	if err != nil {
		return nil, err
	}
	if req.Worker == nil || req.Worker.RuntimeTarget == nil {
		return nil, fmt.Errorf("worker runtime_target is required for %s backend", req.BackendKind)
	}
	targetName := targetNameFor(req)
	endpoint, err := backendEndpoint(req.Worker.RuntimeTarget, cfg)
	if err != nil {
		return nil, err
	}

	metadata := map[string]any{}
	fallbackHealth := domain.HealthStatusUnknown
	if rt, err := p.runtimeFactory(req.Worker.RuntimeTarget); err == nil {
		routeID, envID := routeEnvIDs(req)
		if obs, err := rt.Observe(ctx, routeID, envID, targetName); err == nil && obs != nil {
			fallbackHealth = obs.HealthStatus
			metadata["runtime_health"] = obs.HealthStatus
			metadata["runtime_source"] = obs.Source
			metadata["container_id"] = obs.ObservedContainerID
		} else if err != nil {
			metadata["runtime_observe_error"] = err.Error()
		}
	} else {
		metadata["runtime_resolve_error"] = err.Error()
	}

	health, probeMeta := p.probeHealth(ctx, endpoint, cfg.HealthPath, fallbackHealth)
	for k, v := range probeMeta {
		metadata[k] = v
	}
	return &BackendObservation{
		BackendKind:     req.BackendKind,
		BackendEndpoint: endpoint,
		HealthStatus:    health,
		Source:          "runtime",
		Metadata:        metadata,
	}, nil
}

func (p *RuntimeProvisioner) Deprovision(ctx context.Context, req ProvisionCandidateRequest) error {
	if req.Worker == nil || req.Worker.RuntimeTarget == nil {
		return nil
	}
	rt, err := p.runtimeFactory(req.Worker.RuntimeTarget)
	if err != nil {
		return fmt.Errorf("resolve worker runtime target: %w", err)
	}
	return rt.Undeploy(ctx, targetNameFor(req))
}

func (p *RuntimeProvisioner) probeHealth(ctx context.Context, endpoint, healthPath string, fallback domain.HealthStatus) (domain.HealthStatus, map[string]any) {
	meta := map[string]any{"health_url": joinURL(endpoint, healthPath)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta["health_url"].(string), nil)
	if err != nil {
		meta["health_error"] = err.Error()
		return fallbackOrUnhealthy(fallback), meta
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		meta["health_error"] = err.Error()
		return fallbackOrUnhealthy(fallback), meta
	}
	defer resp.Body.Close()
	meta["health_status_code"] = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return domain.HealthStatusHealthy, meta
	}
	return domain.HealthStatusUnhealthy, meta
}

func runtimeBackendConfig(req ProvisionCandidateRequest) (*domain.LLMRuntimeManagedBackendConfig, error) {
	if req.Release == nil || req.Release.RuntimeBackend == nil {
		return nil, fmt.Errorf("runtime_backend config is required")
	}
	cfg := *req.Release.RuntimeBackend
	if cfg.Scheme == "" {
		cfg.Scheme = "http"
	}
	if cfg.HealthPath == "" {
		cfg.HealthPath = "/health"
	}
	if cfg.ContainerPort <= 0 || cfg.HostPort <= 0 {
		return nil, fmt.Errorf("runtime_backend host_port and container_port are required")
	}
	if strings.TrimSpace(cfg.Image) == "" {
		return nil, fmt.Errorf("runtime_backend image is required")
	}
	return &cfg, nil
}

func deployOptionsFromRuntimeBackend(req ProvisionCandidateRequest, cfg *domain.LLMRuntimeManagedBackendConfig) runtimeadapter.DeployOptions {
	env := map[string]string{}
	for k, v := range cfg.Environment {
		env[k] = v
	}
	if req.Release != nil {
		env["BAHIA_MODEL_REF"] = req.Release.ModelRef
		env["BAHIA_MODEL_SOURCE"] = req.Release.ModelSource
		env["BAHIA_MODEL_VERSION"] = req.Release.Version
	}
	if req.Route != nil && req.Route.GatewayConfig != nil {
		env["BAHIA_PUBLIC_MODEL"] = req.Route.GatewayConfig.PublicModel
	} else if req.Route != nil {
		env["BAHIA_PUBLIC_MODEL"] = req.Route.Name
	}

	labels := map[string]string{
		"bahia.managed": "true",
	}
	if req.Route != nil {
		labels["bahia.llm_route"] = req.Route.ID.String()
	}
	if req.Release != nil {
		labels["bahia.llm_release"] = req.Release.ID.String()
	}
	if req.Environment != nil {
		labels["bahia.environment"] = req.Environment.ID.String()
	}
	if req.Run != nil {
		labels["bahia.llm_run"] = req.Run.ID.String()
	}

	return runtimeadapter.DeployOptions{
		Environment: env,
		Labels:      labels,
		Ports:       []string{fmt.Sprintf("%d:%d", cfg.HostPort, cfg.ContainerPort)},
		Volumes:     append([]string(nil), cfg.Volumes...),
		Restart:     "unless-stopped",
		Command:     append([]string(nil), cfg.Command...),
		Entrypoint:  append([]string(nil), cfg.Entrypoint...),
		WorkingDir:  cfg.WorkingDir,
		NetworkMode: cfg.NetworkMode,
		PullAlways:  cfg.PullAlways,
	}
}

func backendEndpoint(target *domain.WorkerRuntimeTarget, cfg *domain.LLMRuntimeManagedBackendConfig) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(target.PublicBaseURL), "/")
	if base == "" {
		return "", fmt.Errorf("worker runtime_target.public_base_url is required")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse worker public_base_url: %w", err)
	}
	if parsed.Scheme == "" {
		parsed.Scheme = cfg.Scheme
	}
	parsed.Host = hostWithPort(parsed.Host, cfg.HostPort)
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func hostWithPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasSuffix(host, "]") {
		if h, _, err := net.SplitHostPort(host); err == nil {
			return net.JoinHostPort(h, fmt.Sprintf("%d", port))
		}
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), fmt.Sprintf("%d", port))
}

func routeEnvIDs(req ProvisionCandidateRequest) (routeID, envID uuid.UUID) {
	if req.Route != nil {
		routeID = req.Route.ID
	}
	if req.Environment != nil {
		envID = req.Environment.ID
	}
	return routeID, envID
}

func fallbackOrUnhealthy(fallback domain.HealthStatus) domain.HealthStatus {
	switch fallback {
	case domain.HealthStatusHealthy, domain.HealthStatusStarting, domain.HealthStatusStopped:
		return fallback
	default:
		return domain.HealthStatusUnhealthy
	}
}

func joinURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.TrimSpace(path) == "" {
		return base
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}
