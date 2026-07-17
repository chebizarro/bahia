package pulp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	repositoryUUID       = "11111111-1111-1111-1111-111111111111"
	createTaskUUID       = "22222222-2222-2222-2222-222222222222"
	distributionTaskUUID = "33333333-3333-3333-3333-333333333333"
	artifactTaskUUID     = "44444444-4444-4444-4444-444444444444"
)

func TestPulpEnsureRepositoryCreatesRepositoryAndDistributionWithConfirmedTasks(t *testing.T) {
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
			io.WriteString(w, `{"count":1,"results":[{"name":"file-npm","pulp_href":"/pulp/api/v3/repositories/file/file/`+repositoryUUID+`/"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/pulp/api/v3/repositories/file/file/":
			createdRepo = true
			io.WriteString(w, `{"task":"/pulp/api/v3/tasks/`+createTaskUUID+`/"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/pulp/api/v3/tasks/"+createTaskUUID+"/":
			io.WriteString(w, `{"state":"completed"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/pulp/api/v3/distributions/file/file/":
			createdDistribution = true
			io.WriteString(w, `{"task":"/pulp/api/v3/tasks/`+distributionTaskUUID+`/"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/pulp/api/v3/tasks/"+distributionTaskUUID+"/":
			io.WriteString(w, `{"state":"completed"}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL, TaskInterval: time.Millisecond, EnableCustomMutationAPI: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs, err := backend.EnsureRepository(context.Background(), testRepo())
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if lookupCount != 2 || !createdRepo || !createdDistribution || !obs.Exists || obs.Metadata["repository_href"] != "/pulp/api/v3/repositories/file/file/"+repositoryUUID+"/" {
		t.Fatalf("unexpected ensure state lookup=%d repo=%v dist=%v obs=%#v", lookupCount, createdRepo, createdDistribution, obs)
	}
}

func TestPulpEnsureRepositoryFailsWhenCreationIsNotConfirmed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/pulp/api/v3/repositories/file/file/":
			io.WriteString(w, `{"count":0,"results":[]}`)
		case r.Method == http.MethodPost:
			io.WriteString(w, `{"task":"/pulp/api/v3/tasks/`+createTaskUUID+`/"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/pulp/api/v3/tasks/"+createTaskUUID+"/":
			io.WriteString(w, `{"state":"completed"}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL, TaskInterval: time.Millisecond, ConfirmationTimeout: 5 * time.Millisecond, EnableCustomMutationAPI: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := backend.EnsureRepository(context.Background(), testRepo()); err == nil || !strings.Contains(err.Error(), "was not confirmed") {
		t.Fatalf("expected confirmation failure, got %v", err)
	}
}

func TestPulpRejectsMalformedSuccessfulTaskResponse(t *testing.T) {
	for _, body := range []string{`{"unexpected":true}`, `{not-json`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				io.WriteString(w, body)
			}))
			defer server.Close()
			backend, err := New(Config{BaseURL: server.URL, EnableCustomMutationAPI: true})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = backend.StoreArtifact(context.Background(), testRepo(), packagebackend.StoreArtifactRequest{Namespace: "scope", PackageName: "pkg", Version: "1.0.0", Filename: "pkg.tgz", Reader: strings.NewReader("artifact")})
			if err == nil || (!strings.Contains(err.Error(), "did not include a task href") && !strings.Contains(err.Error(), "decode store pulp artifact response")) {
				t.Fatalf("expected task confirmation error, got %v", err)
			}
		})
	}
}

func TestPulpEnsureRepositoryPropagatesDistributionFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/pulp/api/v3/repositories/file/file/":
			io.WriteString(w, `{"count":1,"results":[{"name":"file-npm","pulp_href":"/pulp/api/v3/repositories/file/file/`+repositoryUUID+`/"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/pulp/api/v3/distributions/file/file/":
			http.Error(w, "distribution failed", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL, TaskInterval: time.Millisecond, EnableCustomMutationAPI: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := backend.EnsureRepository(context.Background(), testRepo()); err == nil || !strings.Contains(err.Error(), "distribution") {
		t.Fatalf("expected distribution failure, got %v", err)
	}
}

func TestPulpCapabilitiesFailClosedWithoutVerifiedCustomAPI(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	caps := backend.Capabilities()
	if caps.CanCreateRepository || caps.CanDeleteRepository || caps.CanStoreArtifact || caps.CanListArtifacts || caps.CanPromoteArtifact || caps.CanYankArtifact || caps.CanObserveDrift {
		t.Fatalf("unverified Pulp capabilities were advertised: %#v", caps)
	}
	if _, err := backend.EnsureRepository(context.Background(), testRepo()); !errors.Is(err, ErrCustomMutationAPIUnavailable) {
		t.Fatalf("EnsureRepository error = %v, want ErrCustomMutationAPIUnavailable", err)
	}
}

func TestPulpObserveArtifactDoesNotReuseExpectedChecksumAsObserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/pulp/content/file-npm/scope/pkg/1.0.0/pkg.tgz" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Length", "8")
		io.WriteString(w, "artifact")
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs, err := backend.ObserveArtifact(context.Background(), testRepo(), domain.PackageArtifact{BackendPath: "scope/pkg/1.0.0/pkg.tgz", SHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatalf("ObserveArtifact: %v", err)
	}
	if !obs.Exists || obs.SHA256 != "" || backend.Capabilities().CanObserveDrift {
		t.Fatalf("unexpected observation %#v caps=%#v", obs, backend.Capabilities())
	}
}

func TestPulpStoreAndYankArtifactUseVerifiedCustomAdapterEndpoints(t *testing.T) {
	var putPath, deletePath, putBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			putPath = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			putBody = string(body)
			io.WriteString(w, `{"task":"/pulp/api/v3/tasks/`+artifactTaskUUID+`/"}`)
		case http.MethodGet:
			if r.URL.Path != "/pulp/api/v3/tasks/"+artifactTaskUUID+"/" {
				t.Fatalf("unexpected task path %s", r.URL.Path)
			}
			io.WriteString(w, `{"state":"completed"}`)
		case http.MethodDelete:
			deletePath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL, TaskInterval: time.Millisecond, EnableCustomMutationAPI: true})
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
