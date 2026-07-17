package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// OCIClient implements ImageInspector against any OCI Distribution Spec v2 registry.
// It handles token-based authentication via the standard WWW-Authenticate challenge flow,
// and supports basic auth for registries like Harbor.
type OCIClient struct {
	registryURL string // e.g. "https://ghcr.io" or "https://registry-1.docker.io"
	httpClient  *http.Client
	auth        AuthProvider // bearer token auth (GHCR, Docker Hub)
	basicUser   string       // basic auth username (Harbor, private registries)
	basicPass   string       // basic auth password
	logger      *zap.Logger
}

// AuthProvider obtains a bearer token for a given scope (repository:name:pull).
type AuthProvider interface {
	// Token returns a bearer token for the given scope.
	// scope is typically "repository:<name>:pull".
	Token(ctx context.Context, scope string) (string, error)
}

// RegistryAuthError reports that a registry refused access to a repository.
// It is distinct from a missing image so callers can fail closed or request
// refreshed credentials instead of treating an outage as absence.
type RegistryAuthError struct {
	StatusCode int
	Registry   string
	Repository string
}

func (e *RegistryAuthError) Error() string {
	return fmt.Sprintf("registry authorization failed for %s at %s (status %d)", e.Repository, e.Registry, e.StatusCode)
}

// OCIOption configures an OCIClient.
type OCIOption func(*OCIClient)

// WithAuth sets a token-based auth provider for the OCI client.
func WithAuth(auth AuthProvider) OCIOption {
	return func(c *OCIClient) { c.auth = auth }
}

// WithBasicAuth sets basic authentication credentials.
func WithBasicAuth(username, password string) OCIOption {
	return func(c *OCIClient) {
		c.basicUser = username
		c.basicPass = password
	}
}

// WithHTTPClient sets a custom HTTP client for the OCI client.
func WithHTTPClient(hc *http.Client) OCIOption {
	return func(c *OCIClient) { c.httpClient = hc }
}

// NewOCIClient creates a generic OCI Distribution v2 client.
// registryURL should be the base URL (e.g. "https://ghcr.io").
func NewOCIClient(registryURL string, logger *zap.Logger, opts ...OCIOption) *OCIClient {
	c := &OCIClient{
		registryURL: strings.TrimRight(registryURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// InspectImage retrieves manifest metadata for the given image reference.
func (c *OCIClient) InspectImage(ctx context.Context, repo, reference string) (*ImageInspection, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", c.registryURL, repo, reference)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating manifest request: %w", err)
	}

	// Accept OCI and Docker manifest media types.
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))

	if err := c.setAuth(ctx, req, repo); err != nil {
		return nil, fmt.Errorf("authenticating: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing manifest HEAD: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &ImageInspection{Exists: false}, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &RegistryAuthError{StatusCode: resp.StatusCode, Registry: c.registryURL, Repository: repo}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("registry returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	inspection := &ImageInspection{
		Exists:    true,
		Digest:    resp.Header.Get("Docker-Content-Digest"),
		MediaType: resp.Header.Get("Content-Type"),
	}

	// Parse Content-Length for size.
	if cl := resp.ContentLength; cl > 0 {
		inspection.Size = cl
	}

	// Attempt to fetch full manifest for annotations.
	if annotations, err := c.fetchAnnotations(ctx, repo, reference); err == nil && annotations != nil {
		inspection.Annotations = annotations
	}

	// Attempt to fetch referrers for signatures/SBOMs if we have a digest.
	if inspection.Digest != "" {
		c.enrichWithReferrers(ctx, repo, inspection)
	}

	return inspection, nil
}

// ListTags returns all tags for the given repository.
func (c *OCIClient) ListTags(ctx context.Context, repo string) ([]string, error) {
	url := fmt.Sprintf("%s/v2/%s/tags/list", c.registryURL, repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating tags request: %w", err)
	}

	if err := c.setAuth(ctx, req, repo); err != nil {
		return nil, fmt.Errorf("authenticating: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("registry returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding tags response: %w", err)
	}
	return result.Tags, nil
}

// GetReferrers returns OCI referrers for a given digest using the Referrers API.
// Falls back gracefully if the registry doesn't support it.
func (c *OCIClient) GetReferrers(ctx context.Context, repo, digest string) ([]Referrer, error) {
	url := fmt.Sprintf("%s/v2/%s/referrers/%s", c.registryURL, repo, digest)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating referrers request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json")

	if err := c.setAuth(ctx, req, repo); err != nil {
		return nil, fmt.Errorf("authenticating: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching referrers: %w", err)
	}
	defer resp.Body.Close()

	// Referrers API is optional; many registries return 404.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("registry returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var index struct {
		Manifests []Referrer `json:"manifests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return nil, fmt.Errorf("decoding referrers response: %w", err)
	}
	return index.Manifests, nil
}

// fetchAnnotations does a GET on the manifest to extract annotations.
func (c *OCIClient) fetchAnnotations(ctx context.Context, repo, reference string) (map[string]string, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", c.registryURL, repo, reference)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))

	if err := c.setAuth(ctx, req, repo); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	var manifest struct {
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, nil // best-effort, don't fail the inspection
	}
	return manifest.Annotations, nil
}

// enrichWithReferrers populates Signatures, SBOMRef, ProvenanceRef from referrers.
func (c *OCIClient) enrichWithReferrers(ctx context.Context, repo string, inspection *ImageInspection) {
	referrers, err := c.GetReferrers(ctx, repo, inspection.Digest)
	if err != nil {
		c.logger.Debug("failed to fetch referrers", zap.String("repo", repo), zap.Error(err))
		return
	}

	for _, ref := range referrers {
		switch {
		case isSignatureType(ref.ArtifactType):
			inspection.Signatures = append(inspection.Signatures, ref.Digest)
		case isSBOMType(ref.ArtifactType):
			if inspection.SBOMRef == "" {
				inspection.SBOMRef = ref.Digest
			}
		case isProvenanceType(ref.ArtifactType):
			if inspection.ProvenanceRef == "" {
				inspection.ProvenanceRef = ref.Digest
			}
		}
	}
}

// setAuth adds authorization to the request.
// Uses bearer token auth (via AuthProvider) or basic auth, depending on configuration.
func (c *OCIClient) setAuth(ctx context.Context, req *http.Request, repo string) error {
	// Basic auth takes precedence (simpler, often used by private registries).
	if c.basicUser != "" {
		req.SetBasicAuth(c.basicUser, c.basicPass)
		return nil
	}

	// Token-based auth via AuthProvider.
	if c.auth != nil {
		scope := fmt.Sprintf("repository:%s:pull", repo)
		token, err := c.auth.Token(ctx, scope)
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return nil
}

// Well-known artifact type prefixes for supply-chain metadata.

func isSignatureType(t string) bool {
	return t == "application/vnd.dev.cosign.simplesigning.v1" ||
		strings.HasPrefix(t, "application/vnd.dev.sigstore") ||
		strings.Contains(t, "signature")
}

func isSBOMType(t string) bool {
	return strings.Contains(t, "sbom") ||
		t == "application/spdx+json" ||
		t == "application/vnd.cyclonedx+json"
}

func isProvenanceType(t string) bool {
	return strings.Contains(t, "provenance") ||
		strings.Contains(t, "in-toto") ||
		t == "application/vnd.in-toto+json"
}

// truncate limits a string to n characters for error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Compile-time interface check.
var _ ImageInspector = (*OCIClient)(nil)
