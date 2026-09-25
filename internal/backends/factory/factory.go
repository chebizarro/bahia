package factory

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	Auth       packagebackend.AuthConfig
	TLS        *tls.Config
	Generic    map[string]string
	Redactions []string
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
	if cfg.InsecureSkipVerify {
		return nil, fmt.Errorf("package backends require TLS certificate and hostname verification")
	}
	secrets, err := resolveBackendSecrets(ctx, cfg, resolver)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if secrets.TLS != nil {
		transport.TLSClientConfig = secrets.TLS
	}
	client := &http.Client{Timeout: cfg.Timeout, Transport: transport}
	switch domain.PackageBackendType(strings.TrimSpace(cfg.Type)) {
	case domain.PackageBackendFilesystemMock:
		return nil, filesystem_mock.ErrProductionSelection
	case domain.PackageBackendNexus:
		return nexus.New(nexus.Config{APIVersion: cfg.NexusAPIVersion, BaseURL: cfg.BaseURL, PublicBaseURL: cfg.PublicBaseURL, HTTPClient: client, Auth: secrets.Auth, Secrets: secrets.Generic, Redactions: secrets.Redactions, BlobStoreName: cfg.NexusBlobStoreName, DisableStrictContentTypeValidation: cfg.NexusDisableStrictContentTypeValidation, WritePolicy: cfg.NexusWritePolicy})
	case domain.PackageBackendPulp:
		return pulp.New(pulp.Config{APIVersion: cfg.PulpAPIVersion, BaseURL: cfg.BaseURL, PublicBaseURL: cfg.PublicBaseURL, HTTPClient: client, Auth: secrets.Auth, Secrets: secrets.Generic, Redactions: secrets.Redactions, TaskInterval: cfg.PulpTaskInterval, ConfirmationTimeout: cfg.PulpConfirmationTimeout, EnableCustomMutationAPI: cfg.PulpEnableCustomMutationAPI})
	case domain.PackageBackendAthens, domain.PackageBackendVerdaccio:
		return registryproxy.New(registryproxy.Config{Type: domain.PackageBackendType(strings.TrimSpace(cfg.Type)), BaseURL: cfg.BaseURL, PublicBaseURL: cfg.PublicBaseURL, HTTPClient: client})
	default:
		return nil, fmt.Errorf("unsupported package backend type %q", cfg.Type)
	}
}

func resolveBackendSecrets(ctx context.Context, cfg config.PackageBackendConfig, resolver SecretResolver) (backendSecrets, error) {
	if (cfg.AuthSecretRef != "" && strings.TrimSpace(cfg.AuthSecretRef) == "") || (cfg.TLSSecretRef != "" && strings.TrimSpace(cfg.TLSSecretRef) == "") {
		return backendSecrets{}, fmt.Errorf("package backend secret references must not be blank")
	}
	needsResolver := strings.TrimSpace(cfg.AuthSecretRef) != "" || strings.TrimSpace(cfg.TLSSecretRef) != "" || len(cfg.SecretRefs) > 0
	if needsResolver && resolver == nil {
		return backendSecrets{}, fmt.Errorf("package backend secret refs require a secrets resolver")
	}
	var out backendSecrets
	if ref := strings.TrimSpace(cfg.AuthSecretRef); ref != "" {
		payload, err := resolver.ResolveSecret(ctx, ref)
		if err != nil {
			return backendSecrets{}, fmt.Errorf("resolve auth_secret_ref: secret unavailable or access denied")
		}
		auth, err := parseAuthSecret(payload)
		if err != nil {
			return backendSecrets{}, fmt.Errorf("invalid auth_secret_ref: %w", err)
		}
		out.Auth = auth
	}
	if ref := strings.TrimSpace(cfg.TLSSecretRef); ref != "" {
		payload, err := resolver.ResolveSecret(ctx, ref)
		if err != nil {
			return backendSecrets{}, fmt.Errorf("resolve tls_secret_ref: secret unavailable or access denied")
		}
		tlsCfg, values, err := parseTLSSecret(payload)
		if err != nil {
			return backendSecrets{}, fmt.Errorf("invalid tls_secret_ref: %w", err)
		}
		out.TLS = tlsCfg
		out.Redactions = values
	}
	if len(cfg.SecretRefs) > 0 {
		out.Generic = make(map[string]string, len(cfg.SecretRefs))
		for key, ref := range cfg.SecretRefs {
			ref = strings.TrimSpace(ref)
			if strings.TrimSpace(key) == "" || ref == "" {
				return backendSecrets{}, fmt.Errorf("secret_refs require nonempty keys and references")
			}
			payload, err := resolver.ResolveSecret(ctx, ref)
			if err != nil || strings.TrimSpace(payload) == "" {
				return backendSecrets{}, fmt.Errorf("resolve secret_refs: secret unavailable or access denied")
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
	payload = strings.TrimSpace(payload)
	var auth packagebackend.AuthConfig
	if strings.HasPrefix(payload, "{") {
		decoder := json.NewDecoder(strings.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&raw); err != nil || !json.Valid([]byte(payload)) {
			return auth, fmt.Errorf("auth secret must be valid credential JSON")
		}
		auth = packagebackend.AuthConfig{Username: raw.Username, Password: raw.Password, BearerToken: raw.Token}
	} else {
		auth.BearerToken = payload
	}
	if !auth.Configured() {
		return packagebackend.AuthConfig{}, fmt.Errorf("auth secret must contain token or username/password")
	}
	if err := auth.Validate(); err != nil {
		return packagebackend.AuthConfig{}, err
	}
	return auth, nil
}

func parseTLSSecret(payload string) (*tls.Config, []string, error) {
	var raw struct {
		CACert     string `json:"ca_cert"`
		ClientCert string `json:"client_cert"`
		ClientKey  string `json:"client_key"`
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil || !json.Valid([]byte(payload)) {
		return nil, nil, fmt.Errorf("tls secret must be valid TLS material JSON")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if strings.TrimSpace(raw.CACert) != "" {
		pool := x509.NewCertPool()
		if !completePEM(raw.CACert, "CERTIFICATE") || !pool.AppendCertsFromPEM([]byte(raw.CACert)) {
			return nil, nil, fmt.Errorf("ca_cert must contain only valid PEM certificates")
		}
		cfg.RootCAs = pool
	}
	if strings.TrimSpace(raw.ClientCert) != "" || strings.TrimSpace(raw.ClientKey) != "" {
		if strings.TrimSpace(raw.ClientCert) == "" || strings.TrimSpace(raw.ClientKey) == "" {
			return nil, nil, fmt.Errorf("client_cert and client_key must both be set")
		}
		if !completePEM(raw.ClientCert, "CERTIFICATE") || !completePEM(raw.ClientKey, "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY") {
			return nil, nil, fmt.Errorf("client certificate/key must contain only valid PEM material")
		}
		cert, err := tls.X509KeyPair([]byte(raw.ClientCert), []byte(raw.ClientKey))
		if err != nil {
			return nil, nil, fmt.Errorf("invalid client certificate/key pair")
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if cfg.RootCAs == nil && len(cfg.Certificates) == 0 {
		return nil, nil, fmt.Errorf("tls secret must contain ca_cert or client_cert/client_key")
	}
	return cfg, []string{payload, raw.CACert, raw.ClientCert, raw.ClientKey}, nil
}

func completePEM(value string, types ...string) bool {
	rest := bytes.TrimSpace([]byte(value))
	for len(rest) > 0 {
		block, remaining := pem.Decode(rest)
		if block == nil || len(block.Headers) != 0 || !bytes.HasPrefix(rest, []byte("-----BEGIN "+block.Type+"-----")) {
			return false
		}
		allowed := false
		for _, typ := range types {
			allowed = allowed || block.Type == typ
		}
		if !allowed {
			return false
		}
		if block.Type == "CERTIFICATE" {
			if _, err := x509.ParseCertificate(block.Bytes); err != nil {
				return false
			}
		} else if len(bytes.TrimSpace(remaining)) != 0 {
			return false // A client has exactly one private key.
		}
		rest = bytes.TrimSpace(remaining)
	}
	return strings.TrimSpace(value) != ""
}
