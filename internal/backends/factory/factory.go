package factory

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/backends/filesystem_mock"
	"github.com/openagentsinc/bahia/internal/backends/nexus"
	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/backends/pulp"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// BuildRegistry constructs configured package backends by ref. It is deliberately
// outside the service core so policy/service logic depends only on the pluggable
// packagebackend.Backend interface.
func BuildRegistry(cfg config.PackageControlplaneConfig) (packagebackend.Registry, error) {
	registry := packagebackend.Registry{}
	for ref, backendCfg := range cfg.Backends {
		name := strings.TrimSpace(ref)
		if name == "" {
			return nil, fmt.Errorf("package backend ref must not be empty")
		}
		backend, err := BuildBackend(backendCfg)
		if err != nil {
			return nil, fmt.Errorf("building package backend %q: %w", name, err)
		}
		registry[name] = backend
	}
	return registry, nil
}

func BuildBackend(cfg config.PackageBackendConfig) (packagebackend.Backend, error) {
	if strings.TrimSpace(cfg.AuthSecretRef) != "" || strings.TrimSpace(cfg.TLSSecretRef) != "" || len(cfg.SecretRefs) > 0 {
		return nil, fmt.Errorf("package backend secret refs require a secrets resolver and are not wired in this slice")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit dev/test backend config knob
	}
	client := &http.Client{Timeout: cfg.Timeout, Transport: transport}
	switch domain.PackageBackendType(strings.TrimSpace(cfg.Type)) {
	case domain.PackageBackendFilesystemMock:
		return filesystem_mock.New(filesystem_mock.Config{RootDir: cfg.RootDir, PublicBaseURL: cfg.PublicBaseURL})
	case domain.PackageBackendNexus:
		return nexus.New(nexus.Config{BaseURL: cfg.BaseURL, PublicBaseURL: cfg.PublicBaseURL, HTTPClient: client})
	case domain.PackageBackendPulp:
		return pulp.New(pulp.Config{BaseURL: cfg.BaseURL, PublicBaseURL: cfg.PublicBaseURL, HTTPClient: client})
	default:
		return nil, fmt.Errorf("unsupported package backend type %q", cfg.Type)
	}
}
