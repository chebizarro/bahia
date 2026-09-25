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
	// APIVersion is the REST API contract, not the product release version.
	APIVersion    string
	BaseURL       string
	PublicBaseURL string
	HTTPClient    *http.Client
	Auth          packagebackend.AuthConfig
	Secrets       map[string]string
	Redactions    []string

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
	checksumAPI                 bool
	publicBaseURL               string
	blobStoreName               string
	strictContentTypeValidation bool
	writePolicy                 string
}

func New(cfg Config) (backend *Backend, err error) {
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
	requester := packagebackend.NewRequester(base, client, cfg.Auth, cfg.Secrets, cfg.Redactions...)
	defer requester.ScrubError(&err)
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
	if err := requester.CheckPublic(base, publicBase); err != nil {
		return nil, err
	}
	return &Backend{
		checksumAPI:                 cfg.APIVersion == "v1",
		Requester:                   requester,
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
	caps.CanObserveDrift = b.checksumAPI
	return caps
}

func (b *Backend) EnsureRepository(ctx context.Context, repo domain.PackageRepository) (result packagebackend.RepositoryObservation, err error) {
	defer b.ScrubError(&err)
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

func (b *Backend) DeleteRepository(ctx context.Context, repo domain.PackageRepository, force bool) (result packagebackend.RepositoryObservation, err error) {
	defer b.ScrubError(&err)
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

func (b *Backend) ObserveRepository(ctx context.Context, repo domain.PackageRepository) (result packagebackend.RepositoryObservation, err error) {
	defer b.ScrubError(&err)
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

func (b *Backend) StoreArtifact(ctx context.Context, repo domain.PackageRepository, req packagebackend.StoreArtifactRequest) (result packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err)
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

func (b *Backend) GetArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (result packagebackend.ArtifactStream, err error) {
	defer b.ScrubError(&err)
	name := packagebackend.BackendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
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

func (b *Backend) ListArtifacts(ctx context.Context, repo domain.PackageRepository) (result []packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err)
	name := packagebackend.BackendRepoName(repo)
	return b.listAssets(ctx, "/service/rest/v1/search/assets?repository="+url.QueryEscape(name), "list nexus assets")
}

func (b *Backend) PromoteArtifact(ctx context.Context, sourceRepo domain.PackageRepository, targetRepo domain.PackageRepository, artifact domain.PackageArtifact, req packagebackend.PromoteArtifactRequest) (result packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err)
	stream, err := b.GetArtifact(ctx, sourceRepo, artifact)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	defer func() { _ = stream.ReadCloser.Close() }()
	return b.StoreArtifact(ctx, targetRepo, packagebackend.StoreArtifactRequest{Namespace: artifact.Namespace, PackageName: artifact.PackageName, Version: artifact.Version, Filename: artifact.Filename, ContentType: artifact.ContentType, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Metadata: req.Metadata, Reader: stream.ReadCloser})
}

func (b *Backend) YankArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact, reason string) (result packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err)
	name := packagebackend.BackendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
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

func (b *Backend) ObserveArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (result packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err)
	if !b.checksumAPI {
		return result, packagebackend.ErrChecksumUnavailable
	}
	if !packagebackend.ValidSHA256(artifact.SHA256) {
		return result, fmt.Errorf("artifact has no valid expected SHA-256 for comparison")
	}
	name := packagebackend.BackendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
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
		return packagebackend.ArtifactObservation{Exists: false, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath}, nil
	}
	if !packagebackend.ValidSHA256(matched.SHA256) {
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
				Repository string `json:"repository"`
				Path       string `json:"path"`
				Checksum   struct {
					SHA256 string `json:"sha256"`
				} `json:"checksum"`
				FileSize int64 `json:"fileSize"`
			} `json:"items"`
			ContinuationToken json.RawMessage `json:"continuationToken"`
		}
		if err := packagebackend.DecodeJSON(body, &payload); err != nil {
			return "", fmt.Errorf("decode nexus assets: %w", err)
		}
		if payload.Items == nil || len(payload.ContinuationToken) == 0 {
			return "", fmt.Errorf("nexus assets response omitted items or continuationToken")
		}
		var token string
		if err := json.Unmarshal(payload.ContinuationToken, &token); err != nil || (token != "" && strings.TrimSpace(token) == "") {
			return "", fmt.Errorf("nexus assets response has an invalid continuationToken")
		}
		for _, item := range payload.Items {
			if item.Repository != requestURL.Query().Get("repository") || item.Path == "" {
				return "", fmt.Errorf("nexus asset has invalid repository or path")
			}
			if err := b.CheckPublic(item.Path, item.Checksum.SHA256); err != nil {
				return "", err
			}
			// Construct public URLs from trusted configuration, never signed or
			// credential-bearing downloadUrl values supplied by the server.
			out = append(out, packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(item.Repository, item.Path), BackendPath: item.Path, SHA256: strings.ToLower(item.Checksum.SHA256), SizeBytes: item.FileSize})
		}
		if token == "" {
			return "", nil
		}
		query := requestURL.Query()
		query.Set("continuationToken", token)
		return (&url.URL{Path: requestURL.Path, RawQuery: query.Encode()}).String(), nil
	})
	return out, err
}

func (b *Backend) repositoryURL(name string) string {
	return b.publicBaseURL + "/repository/" + url.PathEscape(name)
}

func (b *Backend) artifactURL(name, relPath string) string {
	return b.repositoryURL(name) + "/" + packagebackend.EscapePath(relPath)
}
