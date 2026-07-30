package factory

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/backends/filesystem_mock"
	"github.com/openagentsinc/bahia/internal/backends/nexus"
	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/backends/pulp"
	"github.com/openagentsinc/bahia/internal/backends/registryproxy"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// SecretResolver resolves production secret references into plaintext secret
// payloads. The concrete resolver is supplied by the secrets adapter layer.
type SecretResolver interface {
	ResolveSecret(ctx context.Context, ref string) (string, error)
}

type backendSecrets struct {
	Auth    packagebackend.AuthConfig
	TLS     *tls.Config
	Generic map[string]string
}

// BuildRegistry constructs configured package backends by ref. It is deliberately
// outside the service core so policy/service logic depends only on the pluggable
// packagebackend.Backend interface.
func BuildRegistry(cfg config.PackageControlplaneConfig) (packagebackend.Registry, error) {
	return BuildRegistryWithSecrets(context.Background(), cfg, nil)
}

func BuildRegistryWithSecrets(ctx context.Context, cfg config.PackageControlplaneConfig, resolver SecretResolver) (packagebackend.Registry, error) {
	registry := packagebackend.Registry{}
	for ref, backendCfg := range cfg.Backends {
		name := strings.TrimSpace(ref)
		if name == "" {
			return nil, fmt.Errorf("package backend ref must not be empty")
		}
		backend, err := BuildBackendWithSecrets(ctx, backendCfg, resolver)
		if err != nil {
			return nil, fmt.Errorf("building package backend %q: %w", name, err)
		}
		registry[name] = backend
	}
	return registry, nil
}

func BuildBackend(cfg config.PackageBackendConfig) (packagebackend.Backend, error) {
	return BuildBackendWithSecrets(context.Background(), cfg, nil)
}

func BuildBackendWithSecrets(ctx context.Context, cfg config.PackageBackendConfig, resolver SecretResolver) (packagebackend.Backend, error) {
	secrets, err := resolveBackendSecrets(ctx, cfg, resolver)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit dev/test backend config knob
	}
	if secrets.TLS != nil {
		transport.TLSClientConfig = secrets.TLS
	}
	client := &http.Client{Timeout: cfg.Timeout, Transport: transport}
	switch domain.PackageBackendType(strings.TrimSpace(cfg.Type)) {
	case domain.PackageBackendFilesystemMock:
		return nil, filesystem_mock.ErrProductionSelection
	case domain.PackageBackendNexus:
		return nexus.New(nexus.Config{BaseURL: cfg.BaseURL, PublicBaseURL: cfg.PublicBaseURL, HTTPClient: client, Auth: secrets.Auth, Secrets: secrets.Generic})
	case domain.PackageBackendPulp:
		return pulp.New(pulp.Config{BaseURL: cfg.BaseURL, PublicBaseURL: cfg.PublicBaseURL, HTTPClient: client, Auth: secrets.Auth, Secrets: secrets.Generic})
	case domain.PackageBackendAthens, domain.PackageBackendVerdaccio:
		return registryproxy.New(registryproxy.Config{Type: domain.PackageBackendType(strings.TrimSpace(cfg.Type)), BaseURL: cfg.BaseURL, PublicBaseURL: cfg.PublicBaseURL, HTTPClient: client})
	default:
		return nil, fmt.Errorf("unsupported package backend type %q", cfg.Type)
	}
}

func resolveBackendSecrets(ctx context.Context, cfg config.PackageBackendConfig, resolver SecretResolver) (backendSecrets, error) {
	needsResolver := strings.TrimSpace(cfg.AuthSecretRef) != "" || strings.TrimSpace(cfg.TLSSecretRef) != "" || len(cfg.SecretRefs) > 0
	if needsResolver && resolver == nil {
		return backendSecrets{}, fmt.Errorf("package backend secret refs require a secrets resolver")
	}
	var out backendSecrets
	if ref := strings.TrimSpace(cfg.AuthSecretRef); ref != "" {
		payload, err := resolver.ResolveSecret(ctx, ref)
		if err != nil {
			return backendSecrets{}, fmt.Errorf("resolve auth_secret_ref %q: %w", ref, err)
		}
		auth, err := parseAuthSecret(payload)
		if err != nil {
			return backendSecrets{}, fmt.Errorf("invalid auth_secret_ref %q: %w", ref, err)
		}
		out.Auth = auth
	}
	if ref := strings.TrimSpace(cfg.TLSSecretRef); ref != "" {
		payload, err := resolver.ResolveSecret(ctx, ref)
		if err != nil {
			return backendSecrets{}, fmt.Errorf("resolve tls_secret_ref %q: %w", ref, err)
		}
		tlsCfg, err := parseTLSSecret(payload)
		if err != nil {
			return backendSecrets{}, fmt.Errorf("invalid tls_secret_ref %q: %w", ref, err)
		}
		out.TLS = tlsCfg
	}
	if len(cfg.SecretRefs) > 0 {
		out.Generic = make(map[string]string, len(cfg.SecretRefs))
		for key, ref := range cfg.SecretRefs {
			ref = strings.TrimSpace(ref)
			payload, err := resolver.ResolveSecret(ctx, ref)
			if err != nil {
				return backendSecrets{}, fmt.Errorf("resolve secret_refs[%q] %q: %w", key, ref, err)
			}
			out.Generic[key] = payload
		}
	}
	return out, nil
}

func parseAuthSecret(payload string) (packagebackend.AuthConfig, error) {
	var raw struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		token := strings.TrimSpace(payload)
		if token == "" {
			return packagebackend.AuthConfig{}, fmt.Errorf("empty auth secret")
		}
		return packagebackend.AuthConfig{BearerToken: token}, nil
	}
	if strings.TrimSpace(raw.Token) != "" {
		return packagebackend.AuthConfig{BearerToken: strings.TrimSpace(raw.Token)}, nil
	}
	if raw.Username != "" || raw.Password != "" {
		if raw.Username == "" || raw.Password == "" {
			return packagebackend.AuthConfig{}, fmt.Errorf("username and password must both be set")
		}
		return packagebackend.AuthConfig{Username: raw.Username, Password: raw.Password}, nil
	}
	return packagebackend.AuthConfig{}, fmt.Errorf("auth secret must contain token or username/password")
}

func parseTLSSecret(payload string) (*tls.Config, error) {
	var raw struct {
		CACert     string `json:"ca_cert"`
		ClientCert string `json:"client_cert"`
		ClientKey  string `json:"client_key"`
	}
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return nil, fmt.Errorf("tls secret must be JSON: %w", err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if strings.TrimSpace(raw.CACert) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(raw.CACert)) {
			return nil, fmt.Errorf("ca_cert must contain PEM certificates")
		}
		cfg.RootCAs = pool
	}
	if strings.TrimSpace(raw.ClientCert) != "" || strings.TrimSpace(raw.ClientKey) != "" {
		if strings.TrimSpace(raw.ClientCert) == "" || strings.TrimSpace(raw.ClientKey) == "" {
			return nil, fmt.Errorf("client_cert and client_key must both be set")
		}
		cert, err := tls.X509KeyPair([]byte(raw.ClientCert), []byte(raw.ClientKey))
		if err != nil {
			return nil, fmt.Errorf("client certificate/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if cfg.RootCAs == nil && len(cfg.Certificates) == 0 {
		return nil, fmt.Errorf("tls secret must contain ca_cert or client_cert/client_key")
	}
	return cfg, nil
}
