package pulp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/domain"
)

var ErrCustomMutationAPIUnavailable = errors.New("pulp custom mutation API is not enabled")

// Config configures a Pulp file-plugin adapter. The concrete endpoints are kept
// isolated here so package control-plane core logic remains backend-agnostic.
type Config struct {
	// APIVersion is the REST API contract, not the product release version.
	APIVersion          string
	BaseURL             string
	PublicBaseURL       string
	HTTPClient          *http.Client
	Auth                packagebackend.AuthConfig
	Secrets             map[string]string
	Redactions          []string
	TaskInterval        time.Duration
	ConfirmationTimeout time.Duration
	// EnableCustomMutationAPI explicitly opts into the non-standard repository
	// mutation endpoints. Production factory wiring leaves this false until a
	// deployment has verified a compatible Pulp plugin/API version.
	EnableCustomMutationAPI bool
}

// Backend implements packagebackend.Backend for Pulp file repositories.
type Backend struct {
	*packagebackend.Requester
	checksumAPI         bool
	publicBaseURL       string
	taskInterval        time.Duration
	confirmationTimeout time.Duration
	customMutationAPI   bool
}

func New(cfg Config) (backend *Backend, err error) {
	base, err := packagebackend.ValidateEndpoint(cfg.BaseURL, "pulp base url")
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
	interval := cfg.TaskInterval
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	confirmationTimeout := cfg.ConfirmationTimeout
	if confirmationTimeout <= 0 {
		confirmationTimeout = 5 * time.Second
	}
	requester := packagebackend.NewRequester(base, client, cfg.Auth, cfg.Secrets, cfg.Redactions...)
	defer requester.ScrubError(&err)
	publicBase := strings.TrimSpace(cfg.PublicBaseURL)
	if publicBase == "" {
		publicBase = base + "/pulp/content"
	} else {
		publicBase, err = packagebackend.ValidateEndpoint(publicBase, "pulp public base url")
		if err != nil {
			return nil, err
		}
	}
	if err := requester.CheckPublic(base, publicBase); err != nil {
		return nil, err
	}
	return &Backend{checksumAPI: cfg.APIVersion == "v3", Requester: requester, publicBaseURL: publicBase, taskInterval: interval, confirmationTimeout: confirmationTimeout, customMutationAPI: cfg.EnableCustomMutationAPI}, nil
}

func (b *Backend) Type() domain.PackageBackendType { return domain.PackageBackendPulp }

func (b *Backend) Capabilities() packagebackend.Capabilities {
	caps := packagebackend.CommonCapabilities()
	caps.CanCreateRepository = b.customMutationAPI
	caps.CanDeleteRepository = b.customMutationAPI
	caps.CanStoreArtifact = b.customMutationAPI
	caps.CanListArtifacts = b.customMutationAPI
	caps.CanPromoteArtifact = b.customMutationAPI
	caps.CanYankArtifact = b.customMutationAPI
	caps.CanObserveDrift = b.checksumAPI
	return caps
}

func (b *Backend) EnsureRepository(ctx context.Context, repo domain.PackageRepository) (result packagebackend.RepositoryObservation, err error) {
	defer b.ScrubError(&err, ErrCustomMutationAPIUnavailable)
	if !b.customMutationAPI {
		return packagebackend.RepositoryObservation{}, ErrCustomMutationAPIUnavailable
	}
	name := packagebackend.BackendRepoName(repo)
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
		if err := b.acceptTask(ctx, resp, "create pulp file repository"); err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
		repoHref, err = b.confirmRepository(ctx, name)
		if err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
	}
	if repoHref == "" {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("pulp repository %q exists without a server-provided href", name)
	}
	if err := b.ensureDistribution(ctx, name, repoHref); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(name), Metadata: map[string]string{"repository_href": repoHref}}, nil
}

func (b *Backend) DeleteRepository(ctx context.Context, repo domain.PackageRepository, force bool) (result packagebackend.RepositoryObservation, err error) {
	defer b.ScrubError(&err, ErrCustomMutationAPIUnavailable)
	if !b.customMutationAPI {
		return packagebackend.RepositoryObservation{}, ErrCustomMutationAPIUnavailable
	}
	name := packagebackend.BackendRepoName(repo)
	if !force {
		items, err := b.ListArtifacts(ctx, repo)
		if err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
		if len(items) > 0 {
			return packagebackend.RepositoryObservation{}, fmt.Errorf("pulp repository %q is not empty", name)
		}
	}
	resp, err := b.Do(ctx, http.MethodDelete, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/", nil, "")
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		_ = resp.Body.Close()
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(name)}, nil
	}
	if err := b.acceptTask(ctx, resp, "delete pulp file repository"); err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(name)}, nil
}

func (b *Backend) ObserveRepository(ctx context.Context, repo domain.PackageRepository) (result packagebackend.RepositoryObservation, err error) {
	defer b.ScrubError(&err, ErrCustomMutationAPIUnavailable)
	name := packagebackend.BackendRepoName(repo)
	exists, href, err := b.findRepository(ctx, name)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	return packagebackend.RepositoryObservation{Exists: exists, PublicURL: b.repositoryURL(name), Metadata: map[string]string{"repository_href": href}}, nil
}

func (b *Backend) StoreArtifact(ctx context.Context, repo domain.PackageRepository, req packagebackend.StoreArtifactRequest) (result packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err, ErrCustomMutationAPIUnavailable)
	if !b.customMutationAPI {
		return packagebackend.ArtifactObservation{}, ErrCustomMutationAPIUnavailable
	}
	if req.Reader == nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("artifact reader is required")
	}
	name := packagebackend.BackendRepoName(repo)
	relPath, err := packagebackend.ArtifactPath(req.Namespace, req.PackageName, req.Version, req.Filename)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	resp, err := b.Do(ctx, http.MethodPut, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/artifacts/"+packagebackend.EscapePath(relPath), req.Reader, req.ContentType)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if err := b.acceptTask(ctx, resp, "store pulp artifact"); err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, SHA256: req.SHA256, SizeBytes: req.SizeBytes}, nil
}

func (b *Backend) GetArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (result packagebackend.ArtifactStream, err error) {
	defer b.ScrubError(&err, ErrCustomMutationAPIUnavailable)
	name := packagebackend.BackendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return packagebackend.ArtifactStream{}, err
		}
	}
	resp, err := b.Do(ctx, http.MethodGet, "/pulp/content/"+url.PathEscape(name)+"/"+packagebackend.EscapePath(relPath), nil, "")
	if err != nil {
		return packagebackend.ArtifactStream{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return packagebackend.ArtifactStream{}, fmt.Errorf("pulp artifact: %w", packagebackend.ErrArtifactNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := packagebackend.ResponseError(resp, "get pulp artifact")
		_ = resp.Body.Close()
		return packagebackend.ArtifactStream{}, err
	}
	return packagebackend.ArtifactStream{ReadCloser: resp.Body, ContentType: resp.Header.Get("Content-Type"), SHA256: artifact.SHA256, SizeBytes: resp.ContentLength, BackendPath: relPath}, nil
}

func (b *Backend) ListArtifacts(ctx context.Context, repo domain.PackageRepository) (result []packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err, ErrCustomMutationAPIUnavailable)
	if !b.customMutationAPI {
		return nil, ErrCustomMutationAPIUnavailable
	}
	name := packagebackend.BackendRepoName(repo)
	out := []packagebackend.ArtifactObservation{}
	err = b.GetPages(ctx, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/artifacts/", "list pulp artifacts", func(body io.Reader) (string, error) {
		var payload struct {
			Next    string `json:"next"`
			Results []struct {
				RelativePath string `json:"relative_path"`
				Path         string `json:"path"`
				Digest       string `json:"sha256"`
				Size         int64  `json:"size"`
			} `json:"results"`
		}
		if err := json.NewDecoder(body).Decode(&payload); err != nil {
			return "", fmt.Errorf("decode pulp artifacts: %w", err)
		}
		for _, item := range payload.Results {
			p := item.RelativePath
			if p == "" {
				p = item.Path
			}
			if err := b.CheckPublic(p, item.Digest); err != nil {
				return "", err
			}
			out = append(out, packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(name, p), BackendPath: p, SHA256: item.Digest, SizeBytes: item.Size})
		}
		return payload.Next, nil
	})
	return out, err
}

func (b *Backend) PromoteArtifact(ctx context.Context, sourceRepo domain.PackageRepository, targetRepo domain.PackageRepository, artifact domain.PackageArtifact, req packagebackend.PromoteArtifactRequest) (result packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err, ErrCustomMutationAPIUnavailable)
	if !b.customMutationAPI {
		return packagebackend.ArtifactObservation{}, ErrCustomMutationAPIUnavailable
	}
	name := packagebackend.BackendRepoName(targetRepo)
	relPath := strings.TrimSpace(artifact.BackendPath)
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return packagebackend.ArtifactObservation{}, err
		}
	}
	payload := map[string]any{"source_repository": packagebackend.BackendRepoName(sourceRepo), "target_repository": name, "path": relPath, "environment": req.Environment, "channel": req.Channel}
	resp, err := b.postJSON(ctx, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/promote/", payload)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if err := b.acceptTask(ctx, resp, "promote pulp artifact"); err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes}, nil
}

func (b *Backend) YankArtifact(ctx context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact, reason string) (result packagebackend.ArtifactObservation, err error) {
	defer b.ScrubError(&err, ErrCustomMutationAPIUnavailable)
	if !b.customMutationAPI {
		return packagebackend.ArtifactObservation{}, ErrCustomMutationAPIUnavailable
	}
	name := packagebackend.BackendRepoName(repo)
	relPath := strings.TrimSpace(artifact.BackendPath)
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return packagebackend.ArtifactObservation{}, err
		}
	}
	resp, err := b.Do(ctx, http.MethodDelete, "/pulp/api/v3/repositories/file/file/"+url.PathEscape(name)+"/artifacts/"+packagebackend.EscapePath(relPath), strings.NewReader(reason), "text/plain")
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		_ = resp.Body.Close()
		return packagebackend.ArtifactObservation{Exists: false, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, Yanked: true}, nil
	}
	if err := b.acceptTask(ctx, resp, "yank pulp artifact"); err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	return packagebackend.ArtifactObservation{Exists: false, DownloadURL: b.artifactURL(name, relPath), BackendPath: relPath, Yanked: true}, nil
}

func (b *Backend) findRepository(ctx context.Context, name string) (bool, string, error) {
	resp, err := b.Do(ctx, http.MethodGet, "/pulp/api/v3/repositories/file/file/?name="+url.QueryEscape(name), nil, "")
	if err != nil {
		return false, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return false, "", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "", packagebackend.ResponseError(resp, "find pulp repository")
	}
	var payload struct {
		Count   *int `json:"count"`
		Results []struct {
			PulpHref string `json:"pulp_href"`
			Name     string `json:"name"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return false, "", fmt.Errorf("decode pulp repository lookup: %w", err)
	}
	if payload.Count == nil {
		return false, "", fmt.Errorf("decode pulp repository lookup: response omitted count")
	}
	if *payload.Count == 0 {
		if len(payload.Results) != 0 {
			return false, "", fmt.Errorf("pulp repository lookup returned results with count zero")
		}
		return false, "", nil
	}
	var href string
	for _, result := range payload.Results {
		if result.Name != name {
			continue
		}
		if href != "" {
			return false, "", fmt.Errorf("pulp repository lookup returned multiple exact matches for %q", name)
		}
		normalized, err := validPulpPath(result.PulpHref, "/pulp/api/v3/repositories/file/file/", "repository href")
		if err != nil {
			return false, "", err
		}
		href = normalized
	}
	if href == "" {
		return false, "", fmt.Errorf("pulp repository lookup count=%d contained no exact server-identified match for %q", *payload.Count, name)
	}
	return true, href, nil
}

func (b *Backend) confirmRepository(ctx context.Context, name string) (string, error) {
	deadline := time.NewTimer(b.confirmationTimeout)
	defer deadline.Stop()
	for {
		exists, href, err := b.findRepository(ctx, name)
		if err != nil {
			return "", err
		}
		if exists && href != "" {
			return href, nil
		}
		timer := time.NewTimer(b.taskInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return "", fmt.Errorf("pulp repository %q was not confirmed within %s", name, b.confirmationTimeout)
		case <-timer.C:
		}
	}
}

func (b *Backend) ensureDistribution(ctx context.Context, name, repoHref string) error {
	payload := map[string]any{"name": name, "base_path": name, "repository": repoHref}
	resp, err := b.postJSON(ctx, "/pulp/api/v3/distributions/file/file/", payload)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusConflict {
		err := packagebackend.ResponseError(resp, "ensure pulp distribution (existing distribution was not verified)")
		_ = resp.Body.Close()
		return err
	}
	return b.acceptTask(ctx, resp, "ensure pulp distribution")
}

func (b *Backend) postJSON(ctx context.Context, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return b.Do(ctx, http.MethodPost, path, bytes.NewReader(body), "application/json")
}

func (b *Backend) acceptTask(ctx context.Context, resp *http.Response, action string) error {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return packagebackend.ResponseError(resp, action)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil {
		return fmt.Errorf("read %s response: %w", action, err)
	}
	if len(data) > 8192 {
		return fmt.Errorf("%s response exceeded 8192 bytes", action)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return fmt.Errorf("%s returned success without a task confirmation", action)
	}
	var payload struct {
		Task     string `json:"task"`
		TaskHref string `json:"task_href"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode %s response: %w", action, err)
	}
	task := strings.TrimSpace(payload.Task)
	if task == "" {
		task = strings.TrimSpace(payload.TaskHref)
	}
	if task == "" {
		return fmt.Errorf("%s response did not include a task href", action)
	}
	task, err = validPulpPath(task, "/pulp/api/v3/tasks/", "task href")
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return b.waitTask(ctx, task)
}

func (b *Backend) waitTask(ctx context.Context, taskHref string) error {
	for {
		resp, err := b.Do(ctx, http.MethodGet, taskHref, nil, "")
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
		switch strings.ToLower(strings.TrimSpace(payload.State)) {
		case "completed":
			return nil
		case "failed", "canceled", "cancelled":
			return fmt.Errorf("pulp task %s ended in state %s: %v", taskHref, payload.State, payload.Error)
		case "waiting", "running", "canceling", "cancelling":
			// Poll until the task reaches a documented terminal state.
		default:
			return fmt.Errorf("pulp task %s returned unknown state %q", taskHref, payload.State)
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

func validPulpPath(raw, prefix, label string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid pulp %s: %w", label, err)
	}
	if parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid pulp %s: must be a server-relative API path", label)
	}
	path := parsed.Path
	if !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("invalid pulp %s %q: expected prefix %s", label, raw, prefix)
	}
	identifier := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if strings.Contains(identifier, "/") || !validUUID(identifier) {
		return "", fmt.Errorf("invalid pulp %s %q: expected UUID resource identifier", label, raw)
	}
	return prefix + identifier + "/", nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}

func (b *Backend) repositoryURL(name string) string {
	return b.publicBaseURL + "/" + url.PathEscape(name)
}

func (b *Backend) artifactURL(name, relPath string) string {
	return b.repositoryURL(name) + "/" + packagebackend.EscapePath(relPath)
}
