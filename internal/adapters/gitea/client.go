// Package gitea implements the production fleet Gitea private-mirror and
// Hive-CI build initiation adapter (controlplane.HiveCIBuildStarter).
//
// Security invariants:
//   - GitHub credentials are resolved server-side from opaque secret
//     references and travel only inside HTTPS request bodies to the fleet
//     Gitea API. They never enter Nostr events, logs, process argv, or
//     Docker build args.
//   - All errors returned from this package are scrubbed of resolved
//     credential material before propagation.
package gitea

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
)

// APIClient is a minimal fleet Gitea API client covering private-mirror
// provisioning, mirror synchronization, and ref-to-commit resolution.
type APIClient struct {
	baseURL    string
	adminToken string
	httpClient *http.Client
}

func NewAPIClient(baseURL, adminToken string, httpClient *http.Client) *APIClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	// Never follow redirects: a 307/308 would re-send request bodies that can
	// carry resolved upstream credentials to a redirect target.
	client := *httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &APIClient{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		adminToken: strings.TrimSpace(adminToken),
		httpClient: &client,
	}
}

// validateBaseURL enforces HTTPS credential transport. Plain HTTP is allowed
// only toward loopback hosts (local development and tests).
func (c *APIClient) validateBaseURL() error {
	parsed, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("invalid gitea base URL")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("gitea base URL must use https for non-loopback hosts")
	default:
		return fmt.Errorf("gitea base URL must use https")
	}
}

// MigrateMirrorRequest describes a Gitea repository migration that creates a
// continuously synced private mirror of an upstream repository. AuthToken is
// the resolved upstream (GitHub) credential; it is sent only in the HTTPS
// request body.
type MigrateMirrorRequest struct {
	Owner     string
	Name      string
	CloneAddr string
	AuthToken string
}

func (c *APIClient) do(ctx context.Context, method, path string, body any, out any, secretsToScrub ...string) (int, error) {
	if err := c.validateBaseURL(); err != nil {
		return 0, err
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode gitea request body: %w", scrubSecrets(err, secretsToScrub...))
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, scrubSecrets(err, secretsToScrub...)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.adminToken != "" {
		req.Header.Set("Authorization", "token "+c.adminToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, scrubSecrets(err, append([]string{c.adminToken}, secretsToScrub...)...)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("gitea %s %s failed: status %d", method, path, resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode gitea %s %s response: %w", method, path, scrubSecrets(err, secretsToScrub...))
		}
	}
	return resp.StatusCode, nil
}

// RepoInfo is the subset of fleet Gitea repository metadata needed to decide
// whether an existing repository is a trustworthy private mirror.
type RepoInfo struct {
	Private     bool   `json:"private"`
	Mirror      bool   `json:"mirror"`
	OriginalURL string `json:"original_url"`
}

// GetRepo returns repository metadata, or (nil, nil) when it does not exist.
func (c *APIClient) GetRepo(ctx context.Context, owner, name string) (*RepoInfo, error) {
	var info RepoInfo
	status, err := c.do(ctx, http.MethodGet, "/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, &info)
	if status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// MigrateMirror creates a private, continuously synced mirror of the upstream
// repository. The upstream credential travels only in the request body.
func (c *APIClient) MigrateMirror(ctx context.Context, req MigrateMirrorRequest) error {
	body := map[string]any{
		"clone_addr":      req.CloneAddr,
		"repo_owner":      req.Owner,
		"repo_name":       req.Name,
		"mirror":          true,
		"private":         true,
		"service":         "github",
		"auth_token":      req.AuthToken,
		"mirror_interval": "10m",
	}
	status, err := c.do(ctx, http.MethodPost, "/api/v1/repos/migrate", body, nil, req.AuthToken)
	if err != nil {
		// A concurrent initiation may have created the mirror first. The caller
		// re-validates the existing repository's metadata before trusting it.
		if status == http.StatusConflict {
			return nil
		}
		return err
	}
	return nil
}

// SyncMirror requests an immediate mirror synchronization pass.
func (c *APIClient) SyncMirror(ctx context.Context, owner, name string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/mirror-sync", nil, nil)
	return err
}

// ResolveRef resolves a branch, tag, or commit reference to an immutable
// full-length commit SHA on the fleet Gitea mirror.
func (c *APIClient) ResolveRef(ctx context.Context, owner, name, ref string) (string, error) {
	repoPath := "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	if isFullCommitSHA(ref) {
		var commit struct {
			SHA string `json:"sha"`
		}
		if _, err := c.do(ctx, http.MethodGet, repoPath+"/git/commits/"+url.PathEscape(ref), nil, &commit); err != nil {
			return "", err
		}
		if !isFullCommitSHA(commit.SHA) {
			return "", fmt.Errorf("gitea did not return an immutable commit for %q", ref)
		}
		return strings.ToLower(commit.SHA), nil
	}

	var branch struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	status, err := c.do(ctx, http.MethodGet, repoPath+"/branches/"+url.PathEscape(ref), nil, &branch)
	if err == nil && isFullCommitSHA(branch.Commit.ID) {
		return strings.ToLower(branch.Commit.ID), nil
	}
	if err != nil && status != http.StatusNotFound {
		return "", err
	}

	var tag struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	status, err = c.do(ctx, http.MethodGet, repoPath+"/tags/"+url.PathEscape(ref), nil, &tag)
	if err == nil && isFullCommitSHA(tag.Commit.SHA) {
		return strings.ToLower(tag.Commit.SHA), nil
	}
	if err != nil && status != http.StatusNotFound {
		return "", err
	}
	return "", fmt.Errorf("ref %q not found on fleet mirror %s/%s", ref, owner, name)
}

func isFullCommitSHA(v string) bool {
	if len(v) != 40 {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// scrubSecrets removes secret material from error text so credential values
// can never leak through error propagation into logs or Nostr responses.
func scrubSecrets(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	scrubbed := msg
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		scrubbed = strings.ReplaceAll(scrubbed, secret, "[redacted]")
	}
	if scrubbed == msg {
		return err
	}
	return fmt.Errorf("%s", scrubbed)
}
