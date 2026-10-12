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
	"sort"
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

// SecretResolver resolves a backend's secret references (secret UUIDs) as
// one batch through the secret store (secretstore.TextResolver). The values
// may be held until validUntil, the earliest store lease.
type SecretResolver interface {
	ResolveSecrets(ctx context.Context, refs []string) (values []string, validUntil time.Time, err error)
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
		var backend packagebackend.Backend
		var err error
		if hasSecretRefs(backendCfg) {
			// Credentials resolve through the secret store on first use.
			backend, err = newDeferredBackend(ctx, backendCfg, resolver)
		} else {
			backend, err = BuildBackendWithSecrets(ctx, backendCfg, nil)
		}
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
	backend, _, err := buildBackend(ctx, cfg, resolver)
	return backend, err
}

// buildBackend builds the backend and reports until when its credentials
// may be held (zero without secret references).
func buildBackend(ctx context.Context, cfg config.PackageBackendConfig, resolver SecretResolver) (packagebackend.Backend, time.Time, error) {
	if cfg.InsecureSkipVerify {
		return nil, time.Time{}, fmt.Errorf("package backends require TLS certificate and hostname verification")
	}
	secrets, validUntil, err := resolveBackendSecrets(ctx, cfg, resolver)
	if err != nil {
		return nil, time.Time{}, err
	}
	backend, err := newBackend(cfg, secrets)
	return backend, validUntil, err
}

func newBackend(cfg config.PackageBackendConfig, secrets backendSecrets) (packagebackend.Backend, error) {
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

func resolveBackendSecrets(ctx context.Context, cfg config.PackageBackendConfig, resolver SecretResolver) (backendSecrets, time.Time, error) {
	if (cfg.AuthSecretRef != "" && strings.TrimSpace(cfg.AuthSecretRef) == "") || (cfg.TLSSecretRef != "" && strings.TrimSpace(cfg.TLSSecretRef) == "") {
		return backendSecrets{}, time.Time{}, fmt.Errorf("package backend secret references must not be blank")
	}
	authRef, tlsRef := strings.TrimSpace(cfg.AuthSecretRef), strings.TrimSpace(cfg.TLSSecretRef)
	var refs []string
	if authRef != "" {
		refs = append(refs, authRef)
	}
	if tlsRef != "" {
		refs = append(refs, tlsRef)
	}
	genericKeys := make([]string, 0, len(cfg.SecretRefs))
	for key, ref := range cfg.SecretRefs {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(ref) == "" {
			return backendSecrets{}, time.Time{}, fmt.Errorf("secret_refs require nonempty keys and references")
		}
		genericKeys = append(genericKeys, key)
	}
	sort.Strings(genericKeys)
	for _, key := range genericKeys {
		refs = append(refs, strings.TrimSpace(cfg.SecretRefs[key]))
	}
	if len(refs) == 0 {
		return backendSecrets{}, time.Time{}, nil
	}
	if resolver == nil {
		return backendSecrets{}, time.Time{}, fmt.Errorf("package backend secret refs require a secrets resolver")
	}
	// One batch for every credential of the backend.
	payloads, validUntil, err := resolver.ResolveSecrets(ctx, refs)
	if err != nil || len(payloads) != len(refs) {
		return backendSecrets{}, time.Time{}, fmt.Errorf("resolve package backend secret refs: secret unavailable or access denied")
	}
	var out backendSecrets
	next := 0
	if authRef != "" {
		auth, err := parseAuthSecret(payloads[next])
		if err != nil {
			return backendSecrets{}, time.Time{}, fmt.Errorf("invalid auth_secret_ref: %w", err)
		}
		out.Auth = auth
		next++
	}
	if tlsRef != "" {
		tlsCfg, values, err := parseTLSSecret(payloads[next])
		if err != nil {
			return backendSecrets{}, time.Time{}, fmt.Errorf("invalid tls_secret_ref: %w", err)
		}
		out.TLS = tlsCfg
		out.Redactions = values
		next++
	}
	if len(genericKeys) > 0 {
		out.Generic = make(map[string]string, len(genericKeys))
		for _, key := range genericKeys {
			if strings.TrimSpace(payloads[next]) == "" {
				return backendSecrets{}, time.Time{}, fmt.Errorf("resolve secret_refs: secret unavailable or access denied")
			}
			out.Generic[key] = payloads[next]
			next++
		}
	}
	return out, validUntil, nil
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
