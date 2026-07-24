package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

var ErrExternalLifecycleUnmanaged = errors.New("external LLM backend lifecycle is unmanaged")

// ExternalAPIProvisioner represents externally managed LLM providers.
type ExternalAPIProvisioner struct {
	httpClient *http.Client
	secrets    SecretResolver
}

type ExternalAPIProvisionerOption func(*ExternalAPIProvisioner)

func WithExternalAPISecretResolver(resolver SecretResolver) ExternalAPIProvisionerOption {
	return func(p *ExternalAPIProvisioner) { p.secrets = resolver }
}

// NewExternalAPIProvisioner creates an external_api provisioner.
func NewExternalAPIProvisioner(client *http.Client, opts ...ExternalAPIProvisionerOption) *ExternalAPIProvisioner {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	p := &ExternalAPIProvisioner{httpClient: client}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *ExternalAPIProvisioner) Provision(ctx context.Context, req ProvisionCandidateRequest) (*ProvisionCandidateResult, error) {
	cfg, err := externalBackendConfig(req)
	if err != nil {
		return nil, err
	}
	observation, err := p.Observe(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("observe externally managed backend before attachment: %w", err)
	}
	if observation.HealthStatus != domain.HealthStatusHealthy {
		return nil, fmt.Errorf("cannot attach unhealthy externally managed backend %q", cfg.BaseURL)
	}
	return &ProvisionCandidateResult{
		BackendKind:     domain.LLMBackendKindExternalAPI,
		BackendEndpoint: strings.TrimRight(cfg.BaseURL, "/"),
		TargetName:      targetNameFor(req),
		Metadata: map[string]any{
			"external":                  true,
			"lifecycle_mode":            "unmanaged_attachment",
			"provider_resource_created": false,
			"health_verified":           true,
		},
	}, nil
}

func (p *ExternalAPIProvisioner) Observe(ctx context.Context, req ProvisionCandidateRequest) (*BackendObservation, error) {
	cfg, err := externalBackendConfig(req)
	if err != nil {
		return nil, err
	}
	probeURL := strings.TrimSpace(cfg.HealthURL)
	if probeURL == "" {
		probeURL = cfg.BaseURL
	}
	health := domain.HealthStatusHealthy
	meta := map[string]any{"health_url": probeURL}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		health = domain.HealthStatusUnhealthy
		meta["health_error"] = err.Error()
	} else {
		headers, resolveErr := resolveHeaderSecretRefs(ctx, cfg.HealthHeaders, cfg.HealthHeaderSecretRefs, p.secrets, "llm_external_health_probe")
		if resolveErr != nil {
			health = domain.HealthStatusUnhealthy
			meta["health_error"] = resolveErr.Error()
		} else {
			applyHeaders(httpReq, headers)
			resp, requestErr := p.httpClient.Do(httpReq)
			if requestErr != nil {
				health = domain.HealthStatusUnhealthy
				meta["health_error"] = requestErr.Error()
			} else {
				defer resp.Body.Close()
				meta["health_status_code"] = resp.StatusCode
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					health = domain.HealthStatusUnhealthy
				}
			}
		}
	}
	return &BackendObservation{
		BackendKind:     domain.LLMBackendKindExternalAPI,
		BackendEndpoint: strings.TrimRight(cfg.BaseURL, "/"),
		HealthStatus:    health,
		Source:          "external_api",
		Metadata:        meta,
	}, nil
}

func applyHeaders(req *http.Request, headers map[string]string) {
	for name, value := range headers {
		req.Header.Set(name, value)
	}
}

func (p *ExternalAPIProvisioner) Deprovision(_ context.Context, req ProvisionCandidateRequest) error {
	cfg, err := externalBackendConfig(req)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: Bahia cannot delete provider resource at %s", ErrExternalLifecycleUnmanaged, cfg.BaseURL)
}

func externalBackendConfig(req ProvisionCandidateRequest) (*domain.LLMExternalBackendConfig, error) {
	if req.Release == nil || req.Release.ExternalBackend == nil {
		return nil, fmt.Errorf("external_backend config is required")
	}
	cfg := *req.Release.ExternalBackend
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.HealthURL = strings.TrimSpace(cfg.HealthURL)
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("external_backend.base_url is required")
	}
	return &cfg, nil
}
