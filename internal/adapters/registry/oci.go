package registry

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// ResolveObjectByDigest retrieves immutable manifest/blob bytes from the
// configured registry. Fully-qualified repository identity must match the
// configured registry authority; redirects remain governed by the HTTP client.
func (c *OCIClient) ResolveObjectByDigest(
	ctx context.Context,
	repository, digest, mediaType string,
	maxSize int64,
) (ImmutableObject, error) {
	if c == nil || c.httpClient == nil || maxSize <= 0 {
		return ImmutableObject{}, fmt.Errorf("registry digest object resolver is not configured")
	}
	if len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:") {
		return ImmutableObject{}, fmt.Errorf("immutable sha256 digest is required")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")); err != nil {
		return ImmutableObject{}, fmt.Errorf("immutable sha256 digest is invalid")
	}
	base, err := url.Parse(c.registryURL)
	if err != nil || base.Host == "" {
		return ImmutableObject{}, fmt.Errorf("configured registry URL is invalid")
	}
	repository = strings.TrimSpace(strings.TrimPrefix(repository, "https://"))
	repository = strings.TrimPrefix(repository, "http://")
	parts := strings.SplitN(repository, "/", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], base.Host) || strings.TrimSpace(parts[1]) == "" {
		return ImmutableObject{}, fmt.Errorf("release repository authority does not match configured registry")
	}
	repoPath := parts[1]
	resource := "blobs"
	if strings.TrimSpace(strings.Split(mediaType, ";")[0]) == "application/vnd.oci.image.manifest.v1+json" {
		resource = "manifests"
	}
	segments := strings.Split(repoPath, "/")
	for index := range segments {
		if strings.TrimSpace(segments[index]) == "" {
			return ImmutableObject{}, fmt.Errorf("release repository path is invalid")
		}
		segments[index] = url.PathEscape(segments[index])
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/v2/" + strings.Join(segments, "/") +
		"/" + resource + "/" + url.PathEscape(digest)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return ImmutableObject{}, fmt.Errorf("create registry evidence request: %w", err)
	}
	if strings.TrimSpace(mediaType) != "" {
		request.Header.Set("Accept", mediaType)
	}
	if err := c.setAuth(ctx, request, repoPath); err != nil {
		return ImmutableObject{}, fmt.Errorf("authenticate registry evidence request: %w", err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return ImmutableObject{}, fmt.Errorf("fetch registry evidence object: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return ImmutableObject{}, fmt.Errorf("registry evidence object is missing")
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ImmutableObject{}, &RegistryAuthError{
			StatusCode: response.StatusCode, Registry: c.registryURL, Repository: repoPath,
		}
	}
	if response.StatusCode != http.StatusOK {
		return ImmutableObject{}, fmt.Errorf("registry evidence request returned status %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxSize+1))
	if err != nil {
		return ImmutableObject{}, fmt.Errorf("read registry evidence object: %w", err)
	}
	if int64(len(content)) > maxSize {
		return ImmutableObject{}, fmt.Errorf("registry evidence object exceeds signed descriptor size")
	}
	// OCI blob storage does not retain a descriptor media type and Harbor
	// commonly serves blobs as application/octet-stream. Bind blob bytes to
	// the signed producer descriptor; manifest responses retain the registry
	// content type so the caller can reject an incompatible structure.
	resolvedMediaType := strings.TrimSpace(mediaType)
	if resource == "manifests" {
		resolvedMediaType = strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
		if resolvedMediaType == "" {
			return ImmutableObject{}, fmt.Errorf("registry manifest response omitted content type")
		}
	}
	return ImmutableObject{Content: content, MediaType: resolvedMediaType, Size: int64(len(content))}, nil
}

var _ DigestObjectResolver = (*OCIClient)(nil)

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
