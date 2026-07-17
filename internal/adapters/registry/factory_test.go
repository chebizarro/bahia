package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestDetectRegistry(t *testing.T) {
	tests := []struct {
		input string
		want  RegistryType
	}{
		// GHCR
		{"ghcr.io/myorg/myapp", RegistryGHCR},
		{"ghcr.io/myorg/myapp:latest", RegistryGHCR},
		{"https://ghcr.io/myorg/myapp", RegistryGHCR},

		// Docker Hub
		{"docker.io/library/nginx", RegistryDockerHub},
		{"docker.io/myorg/myapp", RegistryDockerHub},
		{"registry-1.docker.io/library/nginx", RegistryDockerHub},
		{"index.docker.io/myorg/myapp", RegistryDockerHub},
		{"nginx", RegistryDockerHub},       // bare image name
		{"myorg/myapp", RegistryDockerHub}, // no host defaults to Docker Hub
		{"", RegistryDockerHub},            // empty defaults to Docker Hub

		// Harbor
		{"harbor.example.com/project/repo", RegistryHarbor},
		{"https://harbor.mycompany.io/prod/app", RegistryHarbor},
		{"my-harbor.internal:5000/proj/img", RegistryHarbor},

		// Generic OCI
		{"registry.example.com/repo", RegistryOCI},
		{"myregistry.io/org/app", RegistryOCI},
		{"https://quay.io/myorg/myapp", RegistryOCI},
		{"localhost:5000/test/image", RegistryOCI},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := DetectRegistry(tt.input)
			if got != tt.want {
				t.Errorf("DetectRegistry(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewInspector_Errors(t *testing.T) {
	logger := zap.NewNop()

	_, err := NewInspector(RegistryConfig{Type: RegistryHarbor}, logger)
	if err == nil {
		t.Fatal("expected error for harbor without URL")
	}

	_, err = NewInspector(RegistryConfig{Type: RegistryOCI}, logger)
	if err == nil {
		t.Fatal("expected error for OCI without URL")
	}

	_, err = NewInspector(RegistryConfig{Type: "unknown"}, logger)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestNewInspector_GHCR(t *testing.T) {
	logger := zap.NewNop()
	inspector, err := NewInspector(RegistryConfig{Type: RegistryGHCR, Password: "test-pat"}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client := assertInspectorType(t, inspector, RegistryGHCR, ghcrRegistryURL)
	auth, ok := client.auth.(*GHCRAuth)
	if !ok || auth.pat != "test-pat" {
		t.Fatalf("GHCR auth = %#v, want configured PAT auth", client.auth)
	}
}

func TestNewInspector_DockerHub(t *testing.T) {
	logger := zap.NewNop()
	inspector, err := NewInspector(RegistryConfig{Type: RegistryDockerHub}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertInspectorType(t, inspector, RegistryDockerHub, dockerHubRegistryURL)
}

func TestNewInspector_AutoDetect(t *testing.T) {
	logger := zap.NewNop()
	inspector, err := NewInspector(RegistryConfig{URL: "https://ghcr.io"}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertInspectorType(t, inspector, RegistryGHCR, ghcrRegistryURL)
}

func TestNewVerifier(t *testing.T) {
	logger := zap.NewNop()
	verifier, err := NewVerifier(RegistryConfig{Type: RegistryDockerHub}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	adapter, ok := verifier.(*VerifierAdapter)
	if !ok {
		t.Fatalf("verifier type = %T, want *VerifierAdapter", verifier)
	}
	assertInspectorType(t, adapter.Inspector, RegistryDockerHub, dockerHubRegistryURL)
}

func TestVerifierAdapter(t *testing.T) {
	// Create a mock test server that returns a manifest HEAD.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/v2/myorg/myapp/manifests/v1.0":
			w.Header().Set("Docker-Content-Digest", "sha256:abc123")
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/myorg/myapp/manifests/v1.0":
			// Annotations fetch
			json.NewEncoder(w).Encode(map[string]interface{}{
				"annotations": map[string]string{"org.opencontainers.image.title": "myapp"},
			})
		case r.URL.Path == "/v2/myorg/myapp/referrers/sha256:abc123":
			// No referrers
			json.NewEncoder(w).Encode(map[string]interface{}{"manifests": []interface{}{}})
		case r.Method == http.MethodHead && r.URL.Path == "/v2/myorg/missing/manifests/v1.0":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	logger := zap.NewNop()
	client := NewOCIClient(ts.URL, logger)
	adapter := &VerifierAdapter{Inspector: client}

	// Test existing image.
	result, err := adapter.VerifyImage(context.Background(), "myorg/myapp", "v1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Exists {
		t.Fatal("expected image to exist")
	}
	if result.Digest != "sha256:abc123" {
		t.Errorf("digest = %q, want %q", result.Digest, "sha256:abc123")
	}

	// Test missing image.
	result, err = adapter.VerifyImage(context.Background(), "myorg/missing", "v1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Exists {
		t.Fatal("expected image not to exist")
	}
}

func TestInspectorForImage(t *testing.T) {
	logger := zap.NewNop()

	tests := []struct {
		name    string
		image   string
		want    RegistryType
		wantURL string
	}{
		{"ghcr", "ghcr.io/myorg/myapp", RegistryGHCR, ghcrRegistryURL},
		{"dockerhub", "docker.io/library/nginx", RegistryDockerHub, dockerHubRegistryURL},
		{"generic", "registry.example.com/myorg/myapp", RegistryOCI, "https://registry.example.com"},
		{"no_host", "nginx", RegistryDockerHub, dockerHubRegistryURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inspector, err := InspectorForImage(tt.image, logger)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertInspectorType(t, inspector, tt.want, tt.wantURL)
		})
	}
}

func assertInspectorType(t *testing.T, inspector ImageInspector, want RegistryType, wantURL string) *OCIClient {
	t.Helper()

	var client *OCIClient
	switch want {
	case RegistryDockerHub:
		dockerHub, ok := inspector.(*dockerHubClient)
		if !ok {
			t.Fatalf("inspector type = %T, want *dockerHubClient", inspector)
		}
		client = dockerHub.OCIClient
		if _, ok := client.auth.(*DockerHubAuth); !ok {
			t.Fatalf("Docker Hub auth type = %T, want *DockerHubAuth", client.auth)
		}
	case RegistryGHCR:
		var ok bool
		client, ok = inspector.(*OCIClient)
		if !ok {
			t.Fatalf("inspector type = %T, want *OCIClient", inspector)
		}
		if _, ok := client.auth.(*GHCRAuth); !ok {
			t.Fatalf("GHCR auth type = %T, want *GHCRAuth", client.auth)
		}
	case RegistryOCI:
		var ok bool
		client, ok = inspector.(*OCIClient)
		if !ok {
			t.Fatalf("inspector type = %T, want *OCIClient", inspector)
		}
		if client.auth != nil {
			t.Fatalf("generic OCI auth type = %T, want no token auth", client.auth)
		}
	default:
		t.Fatalf("unsupported expected registry type %q", want)
	}
	if client.registryURL != wantURL {
		t.Fatalf("registry URL = %q, want %q", client.registryURL, wantURL)
	}
	return client
}

func TestNormalizeDockerHubRepo(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"nginx", "library/nginx"},
		{"library/nginx", "library/nginx"},
		{"myorg/myapp", "myorg/myapp"},
		{"myorg/sub/app", "myorg/sub/app"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeDockerHubRepo(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeDockerHubRepo(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ghcr.io/myorg/myapp", "ghcr.io"},
		{"https://ghcr.io/myorg/myapp", "ghcr.io"},
		{"registry.example.com:5000/repo", "registry.example.com:5000"},
		{"nginx", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractHost(tt.input)
			if got != tt.want {
				t.Errorf("extractHost(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestOCIClient_InspectImage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/v2/myorg/myapp/manifests/latest":
			w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Content-Length", "12345")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/myorg/myapp/manifests/latest":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"annotations": map[string]string{
					"org.opencontainers.image.source": "https://github.com/myorg/myapp",
				},
			})
		case r.URL.Path == "/v2/myorg/myapp/referrers/sha256:deadbeef":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"manifests": []map[string]interface{}{
					{
						"digest":       "sha256:sig001",
						"mediaType":    "application/vnd.oci.image.manifest.v1+json",
						"artifactType": "application/vnd.dev.cosign.simplesigning.v1",
						"size":         100,
					},
					{
						"digest":       "sha256:sbom001",
						"mediaType":    "application/vnd.oci.image.manifest.v1+json",
						"artifactType": "application/spdx+json",
						"size":         5000,
					},
					{
						"digest":       "sha256:prov001",
						"mediaType":    "application/vnd.oci.image.manifest.v1+json",
						"artifactType": "application/vnd.in-toto+json",
						"size":         2000,
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	logger := zap.NewNop()
	client := NewOCIClient(ts.URL, logger)

	inspection, err := client.InspectImage(context.Background(), "myorg/myapp", "latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !inspection.Exists {
		t.Fatal("expected image to exist")
	}
	if inspection.Digest != "sha256:deadbeef" {
		t.Errorf("digest = %q, want %q", inspection.Digest, "sha256:deadbeef")
	}
	if inspection.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("media_type = %q", inspection.MediaType)
	}
	if len(inspection.Signatures) != 1 || inspection.Signatures[0] != "sha256:sig001" {
		t.Errorf("signatures = %v, want [sha256:sig001]", inspection.Signatures)
	}
	if inspection.SBOMRef != "sha256:sbom001" {
		t.Errorf("sbom_ref = %q, want sha256:sbom001", inspection.SBOMRef)
	}
	if inspection.ProvenanceRef != "sha256:prov001" {
		t.Errorf("provenance_ref = %q, want sha256:prov001", inspection.ProvenanceRef)
	}
	if inspection.Annotations["org.opencontainers.image.source"] != "https://github.com/myorg/myapp" {
		t.Errorf("annotations = %v", inspection.Annotations)
	}
}

func TestOCIClient_InspectImageReturnsTypedAuthError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()

			client := NewOCIClient(server.URL, zap.NewNop())
			inspection, err := client.InspectImage(context.Background(), "private/app", "latest")
			if inspection != nil {
				t.Fatalf("inspection = %#v, want nil on authorization failure", inspection)
			}
			var authErr *RegistryAuthError
			if !errors.As(err, &authErr) {
				t.Fatalf("error = %v, want *RegistryAuthError", err)
			}
			if authErr.StatusCode != status || authErr.Repository != "private/app" {
				t.Fatalf("auth error = %#v, want status %d and repository private/app", authErr, status)
			}
		})
	}
}

func TestOCIClient_ListTags(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/myorg/myapp/tags/list" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"tags": []string{"v1.0", "v1.1", "latest"},
			})
		} else if r.URL.Path == "/v2/myorg/empty/tags/list" {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	logger := zap.NewNop()
	client := NewOCIClient(ts.URL, logger)

	tags, err := client.ListTags(context.Background(), "myorg/myapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 3 {
		t.Fatalf("expected 3 tags, got %d", len(tags))
	}

	// Empty repo returns nil.
	tags, err = client.ListTags(context.Background(), "myorg/empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Fatalf("expected nil tags for missing repo, got %v", tags)
	}
}

func TestOCIClient_GetReferrers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/myorg/myapp/referrers/sha256:abc" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"manifests": []map[string]interface{}{
					{
						"digest":       "sha256:ref1",
						"mediaType":    "application/vnd.oci.image.manifest.v1+json",
						"artifactType": "application/vnd.dev.cosign.simplesigning.v1",
						"size":         100,
					},
				},
			})
		} else if r.URL.Path == "/v2/myorg/myapp/referrers/sha256:norefs" {
			// Registry doesn't support referrers.
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	logger := zap.NewNop()
	client := NewOCIClient(ts.URL, logger)

	refs, err := client.GetReferrers(context.Background(), "myorg/myapp", "sha256:abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 referrer, got %d", len(refs))
	}
	if refs[0].ArtifactType != "application/vnd.dev.cosign.simplesigning.v1" {
		t.Errorf("artifact type = %q", refs[0].ArtifactType)
	}

	// No referrers returns nil.
	refs, err = client.GetReferrers(context.Background(), "myorg/myapp", "sha256:norefs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refs != nil {
		t.Fatalf("expected nil referrers, got %v", refs)
	}
}

func TestOCIClient_WithBasicAuth(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method == http.MethodHead {
			w.Header().Set("Docker-Content-Digest", "sha256:abc")
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	logger := zap.NewNop()
	client := NewOCIClient(ts.URL, logger, WithBasicAuth("admin", "secret"))

	_, err := client.InspectImage(context.Background(), "proj/repo", "latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAuth == "" {
		t.Fatal("expected Authorization header to be set")
	}
	if !startsWith(gotAuth, "Basic ") {
		t.Errorf("expected Basic auth, got %q", gotAuth)
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestArtifactTypeClassifiers(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) bool
		yes  []string
		no   []string
	}{
		{
			name: "signature",
			fn:   isSignatureType,
			yes:  []string{"application/vnd.dev.cosign.simplesigning.v1", "application/vnd.dev.sigstore.bundle.v0.3+json", "my-signature-type"},
			no:   []string{"application/json", "application/spdx+json"},
		},
		{
			name: "sbom",
			fn:   isSBOMType,
			yes:  []string{"application/spdx+json", "application/vnd.cyclonedx+json", "my-sbom-format"},
			no:   []string{"application/json", "application/vnd.dev.cosign.simplesigning.v1"},
		},
		{
			name: "provenance",
			fn:   isProvenanceType,
			yes:  []string{"application/vnd.in-toto+json", "slsa-provenance-type", "in-toto-statement"},
			no:   []string{"application/json", "application/spdx+json"},
		},
	}

	for _, tt := range tests {
		for _, s := range tt.yes {
			t.Run(fmt.Sprintf("%s/%s/yes", tt.name, s), func(t *testing.T) {
				if !tt.fn(s) {
					t.Errorf("expected %q to match %s type", s, tt.name)
				}
			})
		}
		for _, s := range tt.no {
			t.Run(fmt.Sprintf("%s/%s/no", tt.name, s), func(t *testing.T) {
				if tt.fn(s) {
					t.Errorf("expected %q to NOT match %s type", s, tt.name)
				}
			})
		}
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" {
		t.Error("short string should not be truncated")
	}
	if truncate("hello world", 5) != "hello..." {
		t.Error("long string should be truncated")
	}
}
