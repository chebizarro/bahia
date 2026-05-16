package pulp

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

// Config configures a Pulp file-plugin adapter. The concrete endpoints are kept
// isolated here so package control-plane core logic remains backend-agnostic.
type Config struct {
	BaseURL       string
	PublicBaseURL string
	HTTPClient    *http.Client
	Auth          packagebackend.AuthConfig
	Secrets       map[string]string
	TaskInterval  time.Duration
}

// Backend implements packagebackend.Backend for Pulp file repositories.
type Backend struct {
	baseURL       string
	publicBaseURL string
	httpClient    *http.Client
	auth          packagebackend.AuthConfig
	secrets       map[string]string
	taskInterval  time.Duration
}

func New(cfg Config) (*Backend, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("pulp base url is required")
	}
	if _, err := url.ParseRequestURI(base); err != nil {
		return nil, fmt.Errorf("invalid pulp base url: %w", err)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	interval := cfg.TaskInterval
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	publicBase := strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if publicBase == "" {
		publicBase = base + "/pulp/content"
	}
	return &Backend{baseURL: base, publicBaseURL: publicBase, httpClient: client, auth: cfg.Auth, secrets: cfg.Secrets, taskInterval: interval}, nil
}

func (b *Backend) Type() domain.PackageBackendType { return domain.PackageBackendPulp }

func (b *Backend) Capabilities() packagebackend.Capabilities {
	caps := packagebackend.CommonCapabilities()
	// The skeleton Pulp adapter observes existence via HTTP but does not yet read
	// independent backend checksums for byte-level drift.
	caps.CanObserveDrift = false
	return caps
}

func (b *Backend) EnsureRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	name := backendRepoName(repo)
	if name == "" {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("external repository name is required")
	}
	exists, repoHref, err := b.findRepository(ctx, name)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if !exists {
		payload := map[string]any{"name": name}
		resp, err := b.postJSON(ctx, "/pulp/api/v3/repositories/file/file/", payload)
		if err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
		if err := b.acceptTaskOrSuccess(ctx, resp, "create pulp file repository"); err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
		_, repoHref, err = b.findRepository(ctx, name)
		if err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
	}
	if repoHref == "" {
		repoHref = "/pulp/api/v3/repositories/file/file/" + url.PathEscape(name) + "/"
	}
	if err := b.ensureDistribution(ctx, name, repoHref); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(name), Metadata: map[string]string{"repository_href": repoHref}}, nil
}

func (b *Backend) DeleteRepository(ctx context.Context, repo domain.PackageRepository, force bool) (packagebackend.RepositoryObservation, error) {
	name := backendRepoName(repo)
	if !force {
		items, err := b.ListArtifacts(ctx, repo)
		if err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
		if len(items) > 0 {
			return packagebackend.RepositoryObservation{}, fmt.Errorf("pulp repository %q is not empty", name)
		}
	}
	resp, err := b.do(ctx, http.MethodDelete, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/", nil, "")
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		_ = resp.Body.Close()
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(name)}, nil
	}
	if err := b.acceptTaskOrSuccess(ctx, resp, "delete pulp file repository"); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(name)}, nil
}

func (b *Backend) ObserveRepository(ctx context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	name := backendRepoName(repo)
	exists, href, err := b.findRepository(ctx, name)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return packagebackend.RepositoryObservation{Exists: exists, PublicURL: b.repositoryURL(name), Metadata: map[string]string{"repository_href": href}}, nil
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
	resp, err := b.do(ctx, http.MethodPut, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/artifacts/"+escapePath(relPath), req.Reader, req.ContentType)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if err := b.acceptTaskOrSuccess(ctx, resp, "store pulp artifact"); err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, SHA256: req.SHA256, SizeBytes: req.SizeBytes}, nil
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
	resp, err := b.do(ctx, http.MethodGet, "/pulp/content/"+url.PathEscape(name)+"/"+escapePath(relPath), nil, "")
	if err != nil {
		return packagebackend.ArtifactStream{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return packagebackend.ArtifactStream{}, fmt.Errorf("pulp artifact not found")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := responseError(resp, "get pulp artifact")
		_ = resp.Body.Close()
		return packagebackend.ArtifactStream{}, err
	}
	return packagebackend.ArtifactStream{ReadCloser: resp.Body, ContentType: resp.Header.Get("Content-Type"), SHA256: artifact.SHA256, SizeBytes: resp.ContentLength, BackendPath: relPath}, nil
}

func (b *Backend) ListArtifacts(ctx context.Context, repo domain.PackageRepository) ([]packagebackend.ArtifactObservation, error) {
	name := backendRepoName(repo)
	resp, err := b.do(ctx, http.MethodGet, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/artifacts/", nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError(resp, "list pulp artifacts")
	}
	var payload struct {
		Results []struct {
			RelativePath string `json:"relative_path"`
			Path         string `json:"path"`
			Digest       string `json:"sha256"`
			Size         int64  `json:"size"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode pulp artifacts: %w", err)
	}
	out := make([]packagebackend.ArtifactObservation, 0, len(payload.Results))
	for _, item := range payload.Results {
		p := item.RelativePath
		if p == "" {
			p = item.Path
		}
		out = append(out, packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(name, p), BackendPath: p, SHA256: item.Digest, SizeBytes: item.Size})
	}
	return out, nil
}

func (b *Backend) PromoteArtifact(ctx context.Context, sourceRepo domain.PackageRepository, targetRepo domain.PackageRepository, artifact domain.PackageArtifact, req packagebackend.PromoteArtifactRequest) (packagebackend.ArtifactObservation, error) {
	name := backendRepoName(targetRepo)
	relPath := strings.TrimSpace(artifact.BackendPath)
	var err error
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return packagebackend.ArtifactObservation{}, err
		}
	}
	payload := map[string]any{"source_repository": backendRepoName(sourceRepo), "target_repository": name, "path": relPath, "environment": req.Environment, "channel": req.Channel}
	resp, err := b.postJSON(ctx, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/promote/", payload)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if err := b.acceptTaskOrSuccess(ctx, resp, "promote pulp artifact"); err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes}, nil
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
	resp, err := b.do(ctx, http.MethodDelete, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/artifacts/"+escapePath(relPath), strings.NewReader(reason), "text/plain")
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		_ = resp.Body.Close()
		return packagebackend.ArtifactObservation{Exists: false, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, Yanked: true}, nil
	}
	if err := b.acceptTaskOrSuccess(ctx, resp, "yank pulp artifact"); err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return packagebackend.ArtifactObservation{Exists: false, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, Yanked: true}, nil
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

func (b *Backend) findRepository(ctx context.Context, name string) (bool, string, error) {
	resp, err := b.do(ctx, http.MethodGet, "/pulp/api/v3/repositories/file/file/?name="+url.QueryEscape(name), nil, "")
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, "", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "", responseError(resp, "find pulp repository")
	}
	var payload struct {
		Count   int `json:"count"`
		Results []struct {
			PulpHref string `json:"pulp_href"`
			Name     string `json:"name"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return false, "", fmt.Errorf("decode pulp repository lookup: %w", err)
	}
	if payload.Count == 0 || len(payload.Results) == 0 {
		return false, "", nil
	}
	return true, payload.Results[0].PulpHref, nil
}

func (b *Backend) ensureDistribution(ctx context.Context, name, repoHref string) error {
	payload := map[string]any{"name": name, "base_path": name, "repository": repoHref}
	resp, err := b.postJSON(ctx, "/pulp/api/v3/distributions/file/file/", payload)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusConflict {
		_ = resp.Body.Close()
		return nil
	}
	return b.acceptTaskOrSuccess(ctx, resp, "ensure pulp distribution")
}

func (b *Backend) postJSON(ctx context.Context, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return b.do(ctx, http.MethodPost, path, bytes.NewReader(body), "application/json")
}

func (b *Backend) acceptTaskOrSuccess(ctx context.Context, resp *http.Response, action string) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var payload struct {
			Task     string `json:"task"`
			TaskHref string `json:"task_href"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if len(strings.TrimSpace(string(data))) == 0 {
			return nil
		}
		_ = json.Unmarshal(data, &payload)
		task := payload.Task
		if task == "" {
			task = payload.TaskHref
		}
		if task == "" {
			return nil
		}
		return b.waitTask(ctx, task)
	}
	return responseError(resp, action)
}

func (b *Backend) waitTask(ctx context.Context, taskHref string) error {
	if strings.HasPrefix(taskHref, b.baseURL) {
		taskHref = strings.TrimPrefix(taskHref, b.baseURL)
	}
	for {
		resp, err := b.do(ctx, http.MethodGet, taskHref, nil, "")
		if err != nil {
			return err
		}
		var payload struct {
			State string `json:"state"`
			Error any    `json:"error"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("pulp task lookup failed: status=%d", resp.StatusCode)
		}
		if decodeErr != nil {
			return fmt.Errorf("decode pulp task: %w", decodeErr)
		}
		switch strings.ToLower(payload.State) {
		case "completed":
			return nil
		case "failed", "canceled", "cancelled":
			return fmt.Errorf("pulp task %s ended in state %s: %v", taskHref, payload.State, payload.Error)
		}
		timer := time.NewTimer(b.taskInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (b *Backend) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
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
	return b.publicBaseURL + "/" + url.PathEscape(name)
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
