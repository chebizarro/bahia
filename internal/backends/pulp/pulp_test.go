package pulp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestPulpEnsureRepositoryCreatesRepositoryAndDistributionWithTask(t *testing.T) {
	lookupCount := 0
	var createdRepo, createdDistribution bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/pulp/api/v3/repositories/file/file/":
			lookupCount++
			w.Header().Set("Content-Type", "application/json")
			if lookupCount == 1 {
				io.WriteString(w, `{"count":0,"results":[]}`)
				return
			}
			io.WriteString(w, `{"count":1,"results":[{"name":"file-npm","pulp_href":"/pulp/api/v3/repositories/file/file/file-npm/"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/pulp/api/v3/repositories/file/file/":
			createdRepo = true
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"task":"/pulp/api/v3/tasks/1/"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/pulp/api/v3/tasks/1/":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"state":"completed"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/pulp/api/v3/distributions/file/file/":
			createdDistribution = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL, TaskInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs, err := backend.EnsureRepository(context.Background(), testRepo())
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if lookupCount != 2 || !createdRepo || !createdDistribution || !obs.Exists {
		t.Fatalf("unexpected ensure state lookup=%d repo=%v dist=%v obs=%#v", lookupCount, createdRepo, createdDistribution, obs)
	}
}

func TestPulpEnsureRepositoryPropagatesDistributionFailure(t *testing.T) {
	lookupCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/pulp/api/v3/repositories/file/file/":
			lookupCount++
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"count":1,"results":[{"name":"file-npm","pulp_href":"/pulp/api/v3/repositories/file/file/file-npm/"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/pulp/api/v3/distributions/file/file/":
			http.Error(w, "distribution failed", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL, TaskInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := backend.EnsureRepository(context.Background(), testRepo()); err == nil || !strings.Contains(err.Error(), "distribution") {
		t.Fatalf("expected distribution failure, got %v", err)
	}
	if lookupCount != 1 {
		t.Fatalf("unexpected lookup count %d", lookupCount)
	}
}

func TestPulpStoreAndYankArtifactUseAdapterEndpoints(t *testing.T) {
	var putPath, deletePath, putBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			putPath = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			putBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"task":"/pulp/api/v3/tasks/2/"}`)
		case http.MethodGet:
			if r.URL.Path != "/pulp/api/v3/tasks/2/" {
				t.Fatalf("unexpected task path %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"state":"completed"}`)
		case http.MethodDelete:
			deletePath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL, TaskInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs, err := backend.StoreArtifact(context.Background(), testRepo(), packagebackend.StoreArtifactRequest{Namespace: "scope", PackageName: "pkg", Version: "1.0.0", Filename: "pkg.tgz", Reader: strings.NewReader("artifact"), SHA256: strings.Repeat("a", 64), SizeBytes: 8})
	if err != nil {
		t.Fatalf("StoreArtifact: %v", err)
	}
	wantPath := "/pulp/api/v3/repositories/file/file/file-npm/artifacts/scope/pkg/1.0.0/pkg.tgz"
	if putPath != wantPath || putBody != "artifact" || obs.BackendPath != "scope/pkg/1.0.0/pkg.tgz" {
		t.Fatalf("unexpected put path=%q body=%q obs=%#v", putPath, putBody, obs)
	}
	artifact := domain.PackageArtifact{Namespace: "scope", PackageName: "pkg", Version: "1.0.0", Filename: "pkg.tgz", BackendPath: obs.BackendPath}
	if _, err := backend.YankArtifact(context.Background(), testRepo(), artifact, "bad"); err != nil {
		t.Fatalf("YankArtifact: %v", err)
	}
	if deletePath != wantPath {
		t.Fatalf("unexpected delete path %q", deletePath)
	}
}

func testRepo() domain.PackageRepository {
	return domain.PackageRepository{Name: "repo", ExternalRepositoryName: "file-npm", Format: domain.PackageRepositoryFormatNPM, BackendRef: "pulp", BackendType: domain.PackageBackendPulp}
}
