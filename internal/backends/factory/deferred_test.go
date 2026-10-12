package factory

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// A backend whose configuration names secrets is built at startup without
// them, fails closed on use while they cannot be resolved, and resolves them
// once, on the first use that succeeds.
func TestRegistryDefersBackendSecretsToFirstUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "admin" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	defer server.Close()

	var available atomic.Bool
	var resolves atomic.Int32
	resolver := resolverFunc(func(context.Context, string) (string, error) {
		resolves.Add(1)
		if !available.Load() {
			return "", errors.New("secret store is unavailable")
		}
		return `{"username":"admin","password":"secret"}`, nil
	})
	registry, err := BuildRegistryWithSecrets(context.Background(), config.PackageControlplaneConfig{Backends: map[string]config.PackageBackendConfig{
		"pulp": {Type: "pulp", BaseURL: server.URL, AuthSecretRef: "auth", PulpEnableCustomMutationAPI: true},
	}}, resolver)
	if err != nil {
		t.Fatalf("BuildRegistryWithSecrets: %v", err)
	}
	if resolves.Load() != 0 {
		t.Fatal("secrets were resolved at startup")
	}
	backend := registry["pulp"]
	if backend.Type() != domain.PackageBackendPulp || !backend.Capabilities().CanStoreArtifact {
		t.Fatalf("type/capabilities = %s/%+v", backend.Type(), backend.Capabilities())
	}
	if _, err := backend.ObserveRepository(context.Background(), testRepo("repo")); err == nil {
		t.Fatal("use without resolvable credentials succeeded")
	}
	available.Store(true)
	for range 2 {
		if _, err := backend.ObserveRepository(context.Background(), testRepo("repo")); err != nil {
			t.Fatalf("ObserveRepository: %v", err)
		}
	}
	if got := resolves.Load(); got != 2 {
		t.Fatalf("resolved %d times, want once failed and once held", got)
	}
}
