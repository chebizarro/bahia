package factory

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/backends/nexus"
	"github.com/openagentsinc/bahia/internal/backends/pulp"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

type mapResolver map[string]string

func (m mapResolver) ResolveSecret(_ context.Context, ref string) (string, error) {
	value, ok := m[ref]
	if !ok {
		return "", fmt.Errorf("missing secret %s", ref)
	}
	return value, nil
}

func TestBuildBackendRejectsSecretRefsWithoutResolver(t *testing.T) {
	_, err := BuildBackend(config.PackageBackendConfig{Type: "nexus", BaseURL: "https://nexus.example.com", AuthSecretRef: "secret/nexus"})
	if err == nil || !strings.Contains(err.Error(), "secrets resolver") {
		t.Fatalf("expected explicit secret resolver error, got %v", err)
	}
}

func TestBuildBackendWithSecretsResolvesAuthTLSAndGenericSecrets(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer package-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	backend, err := BuildBackendWithSecrets(context.Background(), config.PackageBackendConfig{
		Type:          "nexus",
		BaseURL:       server.URL,
		AuthSecretRef: "secret/auth",
		TLSSecretRef:  "secret/tls",
		SecretRefs:    map[string]string{"client_ca": "secret/generic"},
	}, mapResolver{
		"secret/auth":    `{"token":"package-token"}`,
		"secret/tls":     `{"ca_cert":` + fmt.Sprintf("%q", serverCACertPEM(t, server)) + `}`,
		"secret/generic": "generic-value",
	})
	if err != nil {
		t.Fatalf("BuildBackendWithSecrets() error = %v", err)
	}
	n, ok := backend.(*nexus.Backend)
	if !ok {
		t.Fatalf("backend type = %T, want *nexus.Backend", backend)
	}
	if got, ok := n.Secret("client_ca"); !ok || got != "generic-value" {
		t.Fatalf("generic secret = %q, %v", got, ok)
	}
	_, err = n.ObserveRepository(context.Background(), testRepo("repo"))
	if err != nil {
		t.Fatalf("ObserveRepository() error = %v", err)
	}
}

func TestPulpClientUsesBasicAuthHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "secret" {
			t.Fatalf("BasicAuth = %q/%q ok=%v", user, pass, ok)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	defer server.Close()

	backend, err := BuildBackendWithSecrets(context.Background(), config.PackageBackendConfig{
		Type:          "pulp",
		BaseURL:       server.URL,
		AuthSecretRef: "secret/auth",
	}, mapResolver{"secret/auth": `{"username":"admin","password":"secret"}`})
	if err != nil {
		t.Fatalf("BuildBackendWithSecrets() error = %v", err)
	}
	p, ok := backend.(*pulp.Backend)
	if !ok {
		t.Fatalf("backend type = %T, want *pulp.Backend", backend)
	}
	_, err = p.ObserveRepository(context.Background(), testRepo("repo"))
	if err != nil {
		t.Fatalf("ObserveRepository() error = %v", err)
	}
}

func TestBuildBackendWithSecretsReturnsClearErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.PackageBackendConfig
		res  mapResolver
		want string
	}{
		{
			name: "missing auth ref",
			cfg:  config.PackageBackendConfig{Type: "nexus", BaseURL: "https://nexus.example.com", AuthSecretRef: "missing"},
			res:  mapResolver{},
			want: `resolve auth_secret_ref "missing"`,
		},
		{
			name: "invalid auth payload",
			cfg:  config.PackageBackendConfig{Type: "nexus", BaseURL: "https://nexus.example.com", AuthSecretRef: "bad"},
			res:  mapResolver{"bad": `{"username":"admin"}`},
			want: `invalid auth_secret_ref "bad"`,
		},
		{
			name: "invalid tls payload",
			cfg:  config.PackageBackendConfig{Type: "nexus", BaseURL: "https://nexus.example.com", TLSSecretRef: "bad-tls"},
			res:  mapResolver{"bad-tls": `{"ca_cert":"not pem"}`},
			want: `invalid tls_secret_ref "bad-tls"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildBackendWithSecrets(context.Background(), tt.cfg, tt.res)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func serverCACertPEM(t *testing.T, server *httptest.Server) string {
	t.Helper()
	cert, err := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parse test server cert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
}

func testRepo(name string) domain.PackageRepository { return domain.PackageRepository{Name: name} }
