package nexus

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestNexusEnsureRepositoryCreatesRawHostedRepository(t *testing.T) {
	var sawGet, sawPost bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/service/rest/v1/repositories/raw-npm":
			sawGet = true
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/service/rest/v1/repositories/raw/hosted":
			sawPost = true
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create payload: %v", err)
			}
			if payload["name"] != "raw-npm" {
				t.Fatalf("unexpected payload: %#v", payload)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs, err := backend.EnsureRepository(context.Background(), testRepo())
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if !sawGet || !sawPost || !obs.Exists || !strings.Contains(obs.PublicURL, "/repository/raw-npm") {
		t.Fatalf("unexpected result get=%v post=%v obs=%#v", sawGet, sawPost, obs)
	}
}

func TestNexusUploadAndYankUseRawRepositoryPaths(t *testing.T) {
	var uploadedPath, deletedPath, uploadedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			uploadedPath = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			uploadedBody = string(body)
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			deletedPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := packagebackend.StoreArtifactRequest{Namespace: "scope", PackageName: "pkg", Version: "1.0.0", Filename: "pkg.tgz", Reader: strings.NewReader("artifact"), SHA256: strings.Repeat("a", 64), SizeBytes: 8}
	obs, err := backend.StoreArtifact(context.Background(), testRepo(), req)
	if err != nil {
		t.Fatalf("StoreArtifact: %v", err)
	}
	wantPath := "/repository/raw-npm/scope/pkg/1.0.0/pkg.tgz"
	if uploadedPath != wantPath || uploadedBody != "artifact" || obs.BackendPath != "scope/pkg/1.0.0/pkg.tgz" {
		t.Fatalf("unexpected upload path=%q body=%q obs=%#v", uploadedPath, uploadedBody, obs)
	}
	artifact := domain.PackageArtifact{Namespace: "scope", PackageName: "pkg", Version: "1.0.0", Filename: "pkg.tgz", BackendPath: obs.BackendPath}
	if _, err := backend.YankArtifact(context.Background(), testRepo(), artifact, "bad"); err != nil {
		t.Fatalf("YankArtifact: %v", err)
	}
	if deletedPath != wantPath {
		t.Fatalf("unexpected delete path %q", deletedPath)
	}
}

func testRepo() domain.PackageRepository {
	return domain.PackageRepository{Name: "repo", ExternalRepositoryName: "raw-npm", Format: domain.PackageRepositoryFormatNPM, BackendRef: "nexus", BackendType: domain.PackageBackendNexus}
}
