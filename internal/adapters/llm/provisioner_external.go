package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ExternalAPIProvisioner represents externally managed LLM providers.
type ExternalAPIProvisioner struct {
	httpClient *http.Client
}

// NewExternalAPIProvisioner creates an external_api provisioner.
func NewExternalAPIProvisioner(client *http.Client) *ExternalAPIProvisioner {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &ExternalAPIProvisioner{httpClient: client}
}

func (p *ExternalAPIProvisioner) Provision(_ context.Context, req ProvisionCandidateRequest) (*ProvisionCandidateResult, error) {
	cfg, err := externalBackendConfig(req)
	if err != nil {
		return nil, err
	}
	return &ProvisionCandidateResult{
		BackendKind:     domain.LLMBackendKindExternalAPI,
		BackendEndpoint: strings.TrimRight(cfg.BaseURL, "/"),
		TargetName:      targetNameFor(req),
		Metadata: map[string]any{
			"external": true,
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
	} else if resp, err := p.httpClient.Do(httpReq); err != nil {
		health = domain.HealthStatusUnhealthy
		meta["health_error"] = err.Error()
	} else {
		defer resp.Body.Close()
		meta["health_status_code"] = resp.StatusCode
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			health = domain.HealthStatusUnhealthy
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

func (p *ExternalAPIProvisioner) Deprovision(context.Context, ProvisionCandidateRequest) error {
	return nil
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
