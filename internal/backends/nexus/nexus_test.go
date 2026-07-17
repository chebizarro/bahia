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

func TestNexusEnsureRepositoryCreatesAndVerifiesConfiguredPolicy(t *testing.T) {
	getCount := 0
	var sawPost bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/service/rest/v1/repositories/raw-npm":
			getCount++
			if getCount == 1 {
				http.NotFound(w, r)
				return
			}
			writeNexusRepository(t, w, "raw-npm", "packages", true, "ALLOW_ONCE")
		case r.Method == http.MethodPost && r.URL.Path == "/service/rest/v1/repositories/raw/hosted":
			sawPost = true
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create payload: %v", err)
			}
			storage := payload["storage"].(map[string]any)
			if payload["name"] != "raw-npm" || storage["blobStoreName"] != "packages" || storage["strictContentTypeValidation"] != true || storage["writePolicy"] != "ALLOW_ONCE" {
				t.Fatalf("unexpected payload: %#v", payload)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL, BlobStoreName: "packages"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs, err := backend.EnsureRepository(context.Background(), testRepo())
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if getCount != 2 || !sawPost || !obs.Exists || !strings.Contains(obs.PublicURL, "/repository/raw-npm") {
		t.Fatalf("unexpected result gets=%d post=%v obs=%#v", getCount, sawPost, obs)
	}
}

func TestNexusConflictMustMatchConfiguredPolicy(t *testing.T) {
	getCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCount++
			if getCount == 1 {
				http.NotFound(w, r)
				return
			}
			writeNexusRepository(t, w, "raw-npm", "wrong-store", false, "ALLOW")
		case http.MethodPost:
			w.WriteHeader(http.StatusConflict)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL, BlobStoreName: "packages"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := backend.EnsureRepository(context.Background(), testRepo()); err == nil || !strings.Contains(err.Error(), "does not match configured policy") {
		t.Fatalf("expected policy mismatch, got %v", err)
	}
}

func TestNexusCreationCapabilityRequiresBlobStore(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if backend.Capabilities().CanCreateRepository {
		t.Fatal("creation capability advertised without a configured blob store")
	}
	if _, err := backend.EnsureRepository(context.Background(), testRepo()); err == nil || !strings.Contains(err.Error(), "blob store name is not configured") {
		t.Fatalf("expected explicit unavailable error, got %v", err)
	}
}

func TestNexusObserveArtifactUsesBackendChecksum(t *testing.T) {
	backendHash := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/service/rest/v1/search/assets" || r.URL.Query().Get("repository") != "raw-npm" || r.URL.Query().Get("name") != "scope/pkg/1.0.0/pkg.tgz" {
			t.Fatalf("unexpected request %s", r.URL.String())
		}
		io.WriteString(w, `{"items":[{"path":"scope/pkg/1.0.0/pkg.tgz","downloadUrl":"https://nexus.example/repository/raw-npm/scope/pkg/1.0.0/pkg.tgz","checksum":{"sha256":"`+backendHash+`"},"fileSize":8}],"continuationToken":null}`)
	}))
	defer server.Close()
	backend, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	artifact := domain.PackageArtifact{BackendPath: "scope/pkg/1.0.0/pkg.tgz", SHA256: strings.Repeat("a", 64)}
	obs, err := backend.ObserveArtifact(context.Background(), testRepo(), artifact)
	if err != nil {
		t.Fatalf("ObserveArtifact: %v", err)
	}
	if !obs.Exists || obs.SHA256 != backendHash || !backend.Capabilities().CanObserveDrift {
		t.Fatalf("unexpected observation %#v caps=%#v", obs, backend.Capabilities())
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

func writeNexusRepository(t *testing.T, w http.ResponseWriter, name, blobStore string, strict bool, writePolicy string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"name": name, "format": "raw", "type": "hosted", "online": true,
		"storage": map[string]any{"blobStoreName": blobStore, "strictContentTypeValidation": strict, "writePolicy": writePolicy},
	}); err != nil {
		t.Fatalf("encode repository: %v", err)
	}
}

func testRepo() domain.PackageRepository {
	return domain.PackageRepository{Name: "repo", ExternalRepositoryName: "raw-npm", Format: domain.PackageRepositoryFormatNPM, BackendRef: "nexus", BackendType: domain.PackageBackendNexus}
}
