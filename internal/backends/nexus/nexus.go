package nexus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Config configures a Sonatype Nexus adapter. The adapter uses raw hosted
// repositories so Bahia stays a control plane and does not serve ecosystem
// package-manager protocols itself.
type Config struct {
	BaseURL       string
	PublicBaseURL string
	HTTPClient    *http.Client
	Auth          packagebackend.AuthConfig
	Secrets       map[string]string
}

// Backend implements packagebackend.Backend for Nexus raw repositories.
type Backend struct {
	baseURL       string
	publicBaseURL string
	httpClient    *http.Client
	auth          packagebackend.AuthConfig
	secrets       map[string]string
}

func New(cfg Config) (*Backend, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("nexus base url is required")
	}
	if _, err := url.ParseRequestURI(base); err != nil {
		return nil, fmt.Errorf("invalid nexus base url: %w", err)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	publicBase := strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if publicBase == "" {
		publicBase = base
	}
	return &Backend{baseURL: base, publicBaseURL: publicBase, httpClient: client, auth: cfg.Auth, secrets: cfg.Secrets}, nil
}

func (b *Backend) Type() domain.PackageBackendType { return domain.PackageBackendNexus }

func (b *Backend) Capabilities() packagebackend.Capabilities {
	caps := packagebackend.CommonCapabilities()
	// The skeleton Nexus adapter observes existence via HTTP but does not yet read
	// independent backend checksums for byte-level drift.
	caps.CanObserveDrift = false
	return caps
}

func (b *Backend) EnsureRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	name := backendRepoName(repo)
	if name == "" {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("external repository name is required")
	}
	resp, err := b.do(ctx, http.MethodGet, "/service/rest/v1/repositories/"+url.PathEscape(name), nil, "")
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = resp.Body.Close()
		return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(name)}, nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return packagebackend.RepositoryObservation{}, responseError(resp, "get nexus repository")
	}
	_ = resp.Body.Close()

	payload := map[string]any{
		"name":   name,
		"online": true,
		"storage": map[string]any{
			"blobStoreName":               "default",
			"strictContentTypeValidation": false,
			"writePolicy":                 "allow_once",
		},
	}
	body, _ := json.Marshal(payload)
	resp, err = b.do(ctx, http.MethodPost, "/service/rest/v1/repositories/raw/hosted", bytes.NewReader(body), "application/json")
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(name)}, nil
	}
	return packagebackend.RepositoryObservation{}, responseError(resp, "create nexus repository")
}

func (b *Backend) DeleteRepository(ctx context.Context, repo domain.PackageRepository, force bool) (packagebackend.RepositoryObservation, error) {
	name := backendRepoName(repo)
	if name == "" {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("external repository name is required")
	}
	if !force {
		assets, err := b.ListArtifacts(ctx, repo)
		if err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
		if len(assets) > 0 {
			return packagebackend.RepositoryObservation{}, fmt.Errorf("nexus repository %q is not empty", name)
		}
	}
	resp, err := b.do(ctx, http.MethodDelete, "/service/rest/v1/repositories/"+url.PathEscape(name), nil, "")
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(name)}, nil
	}
	return packagebackend.RepositoryObservation{}, responseError(resp, "delete nexus repository")
}

func (b *Backend) ObserveRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	name := backendRepoName(repo)
	resp, err := b.do(ctx, http.MethodGet, "/service/rest/v1/repositories/"+url.PathEscape(name), nil, "")
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(name)}, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(name)}, nil
	}
	return packagebackend.RepositoryObservation{}, responseError(resp, "observe nexus repository")
}

func (b *Backend) StoreArtifact(ctx context.Context, repo domain.PackageRepository, req packagebackend.StoreArtifactRequest) (packagebackend.ArtifactObservation, error) {
	if req.Reader == nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("artifact reader is required")
	}
	name := backendRepoName(repo)
	relPath, err := packagebackend.ArtifactPath(req.Namespace, req.PackageName, req.Version, req.Filename)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	resp, err := b.do(ctx, http.MethodPut, "/repository/"+url.PathEscape(name)+"/"+escapePath(relPath), req.Reader, req.ContentType)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, SHA256: req.SHA256, SizeBytes: req.SizeBytes}, nil
	}
	return packagebackend.ArtifactObservation{}, responseError(resp, "upload nexus artifact")
}

func (b *Backend) GetArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (packagebackend.ArtifactStream, error) {
	name := backendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
	var err error
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return packagebackend.ArtifactStream{}, err
		}
	}
	resp, err := b.do(ctx, http.MethodGet, "/repository/"+url.PathEscape(name)+"/"+escapePath(relPath), nil, "")
	if err != nil {
		return packagebackend.ArtifactStream{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return packagebackend.ArtifactStream{}, fmt.Errorf("nexus artifact not found")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := responseError(resp, "get nexus artifact")
		_ = resp.Body.Close()
		return packagebackend.ArtifactStream{}, err
	}
	return packagebackend.ArtifactStream{ReadCloser: resp.Body, ContentType: resp.Header.Get("Content-Type"), SHA256: artifact.SHA256, SizeBytes: resp.ContentLength, BackendPath: relPath}, nil
}

func (b *Backend) ListArtifacts(ctx context.Context, repo domain.PackageRepository) ([]packagebackend.ArtifactObservation, error) {
	name := backendRepoName(repo)
	resp, err := b.do(ctx, http.MethodGet, "/service/rest/v1/search/assets?repository="+url.QueryEscape(name), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError(resp, "list nexus assets")
	}
	var payload struct {
		Items []struct {
			Path        string `json:"path"`
			DownloadURL string `json:"downloadUrl"`
			Checksum    struct {
				SHA256 string `json:"sha256"`
			} `json:"checksum"`
			FileSize int64 `json:"fileSize"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode nexus assets: %w", err)
	}
	out := make([]packagebackend.ArtifactObservation, 0, len(payload.Items))
	for _, item := range payload.Items {
		out = append(out, packagebackend.ArtifactObservation{Exists: true, DownloadURL: item.DownloadURL, BackendPath: item.Path, SHA256: item.Checksum.SHA256, SizeBytes: item.FileSize})
	}
	return out, nil
}

func (b *Backend) PromoteArtifact(ctx context.Context, sourceRepo domain.PackageRepository, targetRepo domain.PackageRepository, artifact domain.PackageArtifact, req packagebackend.PromoteArtifactRequest) (packagebackend.ArtifactObservation, error) {
	stream, err := b.GetArtifact(ctx, sourceRepo, artifact)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	defer stream.ReadCloser.Close()
	return b.StoreArtifact(ctx, targetRepo, packagebackend.StoreArtifactRequest{Namespace: artifact.Namespace, PackageName: artifact.PackageName, Version: artifact.Version, Filename: artifact.Filename, ContentType: artifact.ContentType, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Metadata: req.Metadata, Reader: stream.ReadCloser})
}

func (b *Backend) YankArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact, reason string) (packagebackend.ArtifactObservation, error) {
	name := backendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
	var err error
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return packagebackend.ArtifactObservation{}, err
		}
	}
	resp, err := b.do(ctx, http.MethodDelete, "/repository/"+url.PathEscape(name)+"/"+escapePath(relPath), strings.NewReader(reason), "text/plain")
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return packagebackend.ArtifactObservation{Exists: false, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, Yanked: true}, nil
	}
	return packagebackend.ArtifactObservation{}, responseError(resp, "yank nexus artifact")
}

func (b *Backend) ObserveArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (packagebackend.ArtifactObservation, error) {
	stream, err := b.GetArtifact(ctx, repo, artifact)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return packagebackend.ArtifactObservation{Exists: false, DownloadURL: artifact.DownloadURL, BackendPath: artifact.BackendPath}, nil
		}
		return packagebackend.ArtifactObservation{}, err
	}
	defer stream.ReadCloser.Close()
	return packagebackend.ArtifactObservation{Exists: true, DownloadURL: artifact.DownloadURL, BackendPath: stream.BackendPath, SHA256: stream.SHA256, SizeBytes: stream.SizeBytes}, nil
}

func (b *Backend) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, b.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	b.applyAuth(req)
	return b.httpClient.Do(req)
}

func (b *Backend) applyAuth(req *http.Request) {
	if strings.TrimSpace(b.auth.BearerToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(b.auth.BearerToken))
		return
	}
	if b.auth.Username != "" || b.auth.Password != "" {
		req.SetBasicAuth(b.auth.Username, b.auth.Password)
	}
}

func (b *Backend) Secret(name string) (string, bool) {
	if b.secrets == nil {
		return "", false
	}
	value, ok := b.secrets[name]
	return value, ok
}

func responseError(resp *http.Response, action string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("%s failed: status=%d body=%s", action, resp.StatusCode, strings.TrimSpace(string(body)))
}

func backendRepoName(repo domain.PackageRepository) string {
	if name := strings.TrimSpace(repo.ExternalRepositoryName); name != "" {
		return name
	}
	return strings.TrimSpace(repo.Name)
}

func (b *Backend) repositoryURL(name string) string {
	return b.publicBaseURL + "/repository/" + url.PathEscape(name)
}

func (b *Backend) artifactURL(name, relPath string) string {
	return b.repositoryURL(name) + "/" + escapePath(relPath)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
