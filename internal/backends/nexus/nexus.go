package nexus

import (
	"bytes"
	"context"
	"encoding/hex"
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

	// BlobStoreName is required before repository creation can be advertised or
	// attempted. There is no portable Nexus blob-store default.
	BlobStoreName string
	// DisableStrictContentTypeValidation is an explicit opt-out; validation is on by default.
	DisableStrictContentTypeValidation bool
	// WritePolicy accepts ALLOW, ALLOW_ONCE, or DENY and defaults to ALLOW_ONCE.
	WritePolicy string
}

// Backend implements packagebackend.Backend for Nexus raw repositories.
type Backend struct {
	*packagebackend.Requester
	publicBaseURL               string
	blobStoreName               string
	strictContentTypeValidation bool
	writePolicy                 string
}

func New(cfg Config) (*Backend, error) {
	base, err := packagebackend.ValidateEndpoint(cfg.BaseURL, "nexus base url")
	if err != nil {
		return nil, err
	}
	if err := cfg.Auth.Validate(); err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	publicBase := strings.TrimSpace(cfg.PublicBaseURL)
	if publicBase == "" {
		publicBase = base
	} else {
		publicBase, err = packagebackend.ValidateEndpoint(publicBase, "nexus public base url")
		if err != nil {
			return nil, err
		}
	}
	writePolicy := strings.ToUpper(strings.TrimSpace(cfg.WritePolicy))
	if writePolicy == "" {
		writePolicy = "ALLOW_ONCE"
	}
	switch writePolicy {
	case "ALLOW", "ALLOW_ONCE", "DENY":
	default:
		return nil, fmt.Errorf("invalid nexus write policy %q", cfg.WritePolicy)
	}
	return &Backend{
		Requester:                   packagebackend.NewRequester(base, client, cfg.Auth, cfg.Secrets),
		publicBaseURL:               publicBase,
		blobStoreName:               strings.TrimSpace(cfg.BlobStoreName),
		strictContentTypeValidation: !cfg.DisableStrictContentTypeValidation,
		writePolicy:                 writePolicy,
	}, nil
}

func (b *Backend) Type() domain.PackageBackendType { return domain.PackageBackendNexus }

func (b *Backend) Capabilities() packagebackend.Capabilities {
	caps := packagebackend.CommonCapabilities()
	// Repository creation is unsafe until an operator selects an existing blob store.
	caps.CanCreateRepository = b.blobStoreName != ""
	caps.CanObserveDrift = true
	return caps
}

func (b *Backend) EnsureRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	name := packagebackend.BackendRepoName(repo)
	if name == "" {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("external repository name is required")
	}
	existing, found, err := b.getRepository(ctx, name)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if found {
		if err := b.verifyRepository(existing, name); err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
		return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(name)}, nil
	}
	if b.blobStoreName == "" {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("nexus repository creation is unavailable: blob store name is not configured")
	}

	payload := map[string]any{
		"name":   name,
		"online": true,
		"storage": map[string]any{
			"blobStoreName":               b.blobStoreName,
			"strictContentTypeValidation": b.strictContentTypeValidation,
			"writePolicy":                 b.writePolicy,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	resp, err := b.Do(ctx, http.MethodPost, "/service/rest/v1/repositories/raw/hosted", bytes.NewReader(body), "application/json")
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if resp.StatusCode != http.StatusConflict && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		err := packagebackend.ResponseError(resp, "create nexus repository")
		_ = resp.Body.Close()
		return packagebackend.RepositoryObservation{}, err
	}
	_ = resp.Body.Close()

	// Both successful creates and conflicts must be confirmed against the desired
	// server-observed repository state; a conflict alone proves nothing.
	existing, found, err = b.getRepository(ctx, name)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if !found {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("nexus repository creation was not confirmed by the server")
	}
	if err := b.verifyRepository(existing, name); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(name)}, nil
}

type repositoryConfiguration struct {
	Name    string `json:"name"`
	Format  string `json:"format"`
	Type    string `json:"type"`
	Online  bool   `json:"online"`
	Storage struct {
		BlobStoreName               string `json:"blobStoreName"`
		StrictContentTypeValidation bool   `json:"strictContentTypeValidation"`
		WritePolicy                 string `json:"writePolicy"`
	} `json:"storage"`
}

func (b *Backend) getRepository(ctx context.Context, name string) (repositoryConfiguration, bool, error) {
	resp, err := b.Do(ctx, http.MethodGet, "/service/rest/v1/repositories/"+url.PathEscape(name), nil, "")
	if err != nil {
		return repositoryConfiguration{}, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return repositoryConfiguration{}, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return repositoryConfiguration{}, false, packagebackend.ResponseError(resp, "get nexus repository")
	}
	var repository repositoryConfiguration
	if err := json.NewDecoder(resp.Body).Decode(&repository); err != nil {
		return repositoryConfiguration{}, false, fmt.Errorf("decode nexus repository: %w", err)
	}
	return repository, true, nil
}

func (b *Backend) verifyRepository(repository repositoryConfiguration, name string) error {
	if b.blobStoreName == "" {
		return fmt.Errorf("cannot verify nexus repository %q policy: blob store name is not configured", name)
	}
	var mismatches []string
	if repository.Name != name {
		mismatches = append(mismatches, fmt.Sprintf("name=%q", repository.Name))
	}
	if !strings.EqualFold(repository.Format, "raw") {
		mismatches = append(mismatches, fmt.Sprintf("format=%q", repository.Format))
	}
	if !strings.EqualFold(repository.Type, "hosted") {
		mismatches = append(mismatches, fmt.Sprintf("type=%q", repository.Type))
	}
	if !repository.Online {
		mismatches = append(mismatches, "online=false")
	}
	if repository.Storage.BlobStoreName != b.blobStoreName {
		mismatches = append(mismatches, fmt.Sprintf("blobStoreName=%q", repository.Storage.BlobStoreName))
	}
	if repository.Storage.StrictContentTypeValidation != b.strictContentTypeValidation {
		mismatches = append(mismatches, fmt.Sprintf("strictContentTypeValidation=%v", repository.Storage.StrictContentTypeValidation))
	}
	if !strings.EqualFold(repository.Storage.WritePolicy, b.writePolicy) {
		mismatches = append(mismatches, fmt.Sprintf("writePolicy=%q", repository.Storage.WritePolicy))
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("nexus repository %q does not match configured policy: %s", name, strings.Join(mismatches, ", "))
	}
	return nil
}

func (b *Backend) DeleteRepository(ctx context.Context, repo domain.PackageRepository, force bool) (packagebackend.RepositoryObservation, error) {
	name := packagebackend.BackendRepoName(repo)
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
	resp, err := b.Do(ctx, http.MethodDelete, "/service/rest/v1/repositories/"+url.PathEscape(name), nil, "")
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(name)}, nil
	}
	return packagebackend.RepositoryObservation{}, packagebackend.ResponseError(resp, "delete nexus repository")
}

func (b *Backend) ObserveRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	name := packagebackend.BackendRepoName(repo)
	resp, err := b.Do(ctx, http.MethodGet, "/service/rest/v1/repositories/"+url.PathEscape(name), nil, "")
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(name)}, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(name)}, nil
	}
	return packagebackend.RepositoryObservation{}, packagebackend.ResponseError(resp, "observe nexus repository")
}

func (b *Backend) StoreArtifact(ctx context.Context, repo domain.PackageRepository, req packagebackend.StoreArtifactRequest) (packagebackend.ArtifactObservation, error) {
	if req.Reader == nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("artifact reader is required")
	}
	name := packagebackend.BackendRepoName(repo)
	relPath, err := packagebackend.ArtifactPath(req.Namespace, req.PackageName, req.Version, req.Filename)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	resp, err := b.Do(ctx, http.MethodPut, "/repository/"+url.PathEscape(name)+"/"+packagebackend.EscapePath(relPath), req.Reader, req.ContentType)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, SHA256: req.SHA256, SizeBytes: req.SizeBytes}, nil
	}
	return packagebackend.ArtifactObservation{}, packagebackend.ResponseError(resp, "upload nexus artifact")
}

func (b *Backend) GetArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (packagebackend.ArtifactStream, error) {
	name := packagebackend.BackendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
	var err error
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return packagebackend.ArtifactStream{}, err
		}
	}
	resp, err := b.Do(ctx, http.MethodGet, "/repository/"+url.PathEscape(name)+"/"+packagebackend.EscapePath(relPath), nil, "")
	if err != nil {
		return packagebackend.ArtifactStream{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return packagebackend.ArtifactStream{}, fmt.Errorf("nexus artifact: %w", packagebackend.ErrArtifactNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := packagebackend.ResponseError(resp, "get nexus artifact")
		_ = resp.Body.Close()
		return packagebackend.ArtifactStream{}, err
	}
	return packagebackend.ArtifactStream{ReadCloser: resp.Body, ContentType: resp.Header.Get("Content-Type"), SHA256: artifact.SHA256, SizeBytes: resp.ContentLength, BackendPath: relPath}, nil
}

func (b *Backend) ListArtifacts(ctx context.Context, repo domain.PackageRepository) ([]packagebackend.ArtifactObservation, error) {
	name := packagebackend.BackendRepoName(repo)
	return b.listAssets(ctx, "/service/rest/v1/search/assets?repository="+url.QueryEscape(name), "list nexus assets")
}

func (b *Backend) PromoteArtifact(ctx context.Context, sourceRepo domain.PackageRepository, targetRepo domain.PackageRepository, artifact domain.PackageArtifact, req packagebackend.PromoteArtifactRequest) (packagebackend.ArtifactObservation, error) {
	stream, err := b.GetArtifact(ctx, sourceRepo, artifact)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	defer func() { _ = stream.ReadCloser.Close() }()
	return b.StoreArtifact(ctx, targetRepo, packagebackend.StoreArtifactRequest{Namespace: artifact.Namespace, PackageName: artifact.PackageName, Version: artifact.Version, Filename: artifact.Filename, ContentType: artifact.ContentType, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Metadata: req.Metadata, Reader: stream.ReadCloser})
}

func (b *Backend) YankArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact, reason string) (packagebackend.ArtifactObservation, error) {
	name := packagebackend.BackendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
	var err error
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return packagebackend.ArtifactObservation{}, err
		}
	}
	resp, err := b.Do(ctx, http.MethodDelete, "/repository/"+url.PathEscape(name)+"/"+packagebackend.EscapePath(relPath), strings.NewReader(reason), "text/plain")
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return packagebackend.ArtifactObservation{Exists: false, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, Yanked: true}, nil
	}
	return packagebackend.ArtifactObservation{}, packagebackend.ResponseError(resp, "yank nexus artifact")
}

func (b *Backend) ObserveArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (packagebackend.ArtifactObservation, error) {
	name := packagebackend.BackendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
	var err error
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return packagebackend.ArtifactObservation{}, err
		}
	}
	assets, err := b.searchAssets(ctx, name, relPath)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	var matched *packagebackend.ArtifactObservation
	for i := range assets {
		if assets[i].BackendPath != relPath {
			continue
		}
		if matched != nil {
			return packagebackend.ArtifactObservation{}, fmt.Errorf("nexus returned multiple assets for backend path %q", relPath)
		}
		matched = &assets[i]
	}
	if matched == nil {
		return packagebackend.ArtifactObservation{Exists: false, DownloadURL: artifact.DownloadURL, BackendPath: relPath}, nil
	}
	if !validSHA256(matched.SHA256) {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("nexus asset %q did not provide a valid backend SHA-256", relPath)
	}
	return *matched, nil
}

func (b *Backend) searchAssets(ctx context.Context, repository, relPath string) ([]packagebackend.ArtifactObservation, error) {
	return b.listAssets(ctx, "/service/rest/v1/search/assets?repository="+url.QueryEscape(repository)+"&name="+url.QueryEscape(relPath), "observe nexus asset")
}

func (b *Backend) listAssets(ctx context.Context, firstPath, action string) ([]packagebackend.ArtifactObservation, error) {
	requestURL, _ := url.Parse(firstPath)
	out := []packagebackend.ArtifactObservation{}
	err := b.GetPages(ctx, firstPath, action, func(body io.Reader) (string, error) {
		var payload struct {
			Items []struct {
				Path        string `json:"path"`
				DownloadURL string `json:"downloadUrl"`
				Checksum    struct {
					SHA256 string `json:"sha256"`
				} `json:"checksum"`
				FileSize int64 `json:"fileSize"`
			} `json:"items"`
			ContinuationToken string `json:"continuationToken"`
		}
		if err := json.NewDecoder(body).Decode(&payload); err != nil {
			return "", fmt.Errorf("decode nexus assets: %w", err)
		}
		for _, item := range payload.Items {
			out = append(out, packagebackend.ArtifactObservation{Exists: true, DownloadURL: item.DownloadURL, BackendPath: item.Path, SHA256: strings.ToLower(item.Checksum.SHA256), SizeBytes: item.FileSize})
		}
		if strings.TrimSpace(payload.ContinuationToken) == "" {
			return "", nil
		}
		query := requestURL.Query()
		query.Set("continuationToken", payload.ContinuationToken)
		return (&url.URL{Path: requestURL.Path, RawQuery: query.Encode()}).String(), nil
	})
	return out, err
}

func validSHA256(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (b *Backend) repositoryURL(name string) string {
	return b.publicBaseURL + "/repository/" + url.PathEscape(name)
}

func (b *Backend) artifactURL(name, relPath string) string {
	return b.repositoryURL(name) + "/" + packagebackend.EscapePath(relPath)
}
