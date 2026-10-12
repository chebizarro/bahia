package factory

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// batchResolver counts batches and serves a pulp basic-auth credential
// whose password it can change or revoke.
type batchResolver struct {
	mu       sync.Mutex
	batches  [][]string
	password string
	revoked  bool
	lease    time.Time
}

func (b *batchResolver) ResolveSecrets(_ context.Context, refs []string) ([]string, time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batches = append(b.batches, append([]string(nil), refs...))
	if b.revoked {
		return nil, time.Time{}, errors.New("credential is revoked")
	}
	values := make([]string, 0, len(refs))
	for range refs {
		values = append(values, `{"username":"admin","password":"`+b.password+`"}`)
	}
	return values, b.lease, nil
}

// A deferred backend holds its credentials only until the store lease
// ends: the next use resolves again (one batch for all of them), picks up a
// re-pointed credential, and fails closed, dropping the backend, on a
// revoked one.
func TestDeferredBackendFollowsLeaseRepointAndRevocation(t *testing.T) {
	var wantPassword atomic.Value
	wantPassword.Store("first")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pass, ok := r.BasicAuth(); !ok || pass != wantPassword.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	defer server.Close()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	resolver := &batchResolver{password: "first", lease: now.Add(time.Minute)}
	backend, err := newDeferredBackend(context.Background(), config.PackageBackendConfig{
		Type: "pulp", BaseURL: server.URL, AuthSecretRef: "auth", SecretRefs: map[string]string{"extra": "extra-ref"},
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	backend.now = func() time.Time { return now }
	observe := func() error {
		_, err := backend.ObserveRepository(context.Background(), testRepo("repo"))
		return err
	}
	if err := observe(); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if len(resolver.batches) != 1 || len(resolver.batches[0]) != 2 {
		t.Fatalf("batches = %v, want one batch of both references", resolver.batches)
	}

	// Re-pointed: after the lease the backend is rebuilt with the new value.
	resolver.password, resolver.lease = "second", now.Add(2*time.Minute)
	wantPassword.Store("second")
	if err := observe(); err == nil {
		t.Fatal("within the lease the held credential (now rejected upstream) was not used")
	}
	now = now.Add(90 * time.Second)
	if err := observe(); err != nil {
		t.Fatalf("after the lease, the re-pointed credential: %v", err)
	}

	// Revoked: after the lease the rebuild fails and the backend is dropped.
	resolver.revoked = true
	now = now.Add(time.Minute)
	if err := observe(); err == nil {
		t.Fatal("a revoked credential kept working after its lease")
	}
	if backend.backend != nil {
		t.Fatal("the backend built with the revoked credential was not dropped")
	}
}
