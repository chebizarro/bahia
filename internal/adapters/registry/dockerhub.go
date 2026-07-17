package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/httpclient"
	"go.uber.org/zap"
)

const (
	dockerHubRegistryURL = "https://registry-1.docker.io"
	dockerHubAuthURL     = "https://auth.docker.io/token"
	dockerHubService     = "registry.docker.io"
	maxRegistryAuthBody  = 1 << 20
)

// DockerHubAuth provides token authentication for Docker Hub.
// Supports both anonymous and credential-based authentication.
type DockerHubAuth struct {
	username   string
	password   string // password or PAT
	httpClient *http.Client

	mu    sync.Mutex
	cache map[string]*tokenEntry
}

// NewDockerHubAuth creates a Docker Hub auth provider.
// If username/password are empty, anonymous authentication is used (rate-limited).
func NewDockerHubAuth(username, password string) *DockerHubAuth {
	return newDockerHubAuth(username, password, nil)
}

func newDockerHubAuth(username, password string, client *http.Client) *DockerHubAuth {
	return &DockerHubAuth{
		username:   username,
		password:   password,
		httpClient: httpclient.Harden(client, httpclient.DefaultTimeout),
		cache:      make(map[string]*tokenEntry),
	}
}

// Token returns a bearer token for the given scope.
func (a *DockerHubAuth) Token(ctx context.Context, scope string) (string, error) {
	a.mu.Lock()
	if entry, ok := a.cache[scope]; ok && time.Now().Before(entry.expires) {
		token := entry.token
		a.mu.Unlock()
		return token, nil
	}
	a.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dockerHubAuthURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating token request: %w", err)
	}

	q := req.URL.Query()
	q.Set("service", dockerHubService)
	q.Set("scope", scope)
	req.URL.RawQuery = q.Encode()

	// Use credentials if provided (for private repos and higher rate limits).
	if a.username != "" {
		req.SetBasicAuth(a.username, a.password)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting Docker Hub token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Docker Hub token endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryAuthBody+1))
	if err != nil {
		return "", fmt.Errorf("reading Docker Hub token response: %w", err)
	}
	if len(body) > maxRegistryAuthBody {
		return "", fmt.Errorf("Docker Hub token response exceeds %d bytes", maxRegistryAuthBody)
	}
	var result struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decoding Docker Hub token response: %w", err)
	}

	// Cache with a safety margin.
	ttl := time.Duration(result.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 300 * time.Second // Docker Hub tokens default to 300s
	}

	a.mu.Lock()
	a.cache[scope] = &tokenEntry{
		token:   result.Token,
		expires: time.Now().Add(ttl - 10*time.Second),
	}
	a.mu.Unlock()

	return result.Token, nil
}

// NormalizeDockerHubRepo ensures Docker Hub library images have the "library/" prefix.
// "nginx" → "library/nginx", "myorg/myapp" stays as-is.
func NormalizeDockerHubRepo(repo string) string {
	if repo != "" && !containsSlash(repo) {
		return "library/" + repo
	}
	return repo
}

func containsSlash(s string) bool {
	for _, c := range s {
		if c == '/' {
			return true
		}
	}
	return false
}

// dockerHubClient wraps OCIClient to normalize repo names for Docker Hub.
type dockerHubClient struct {
	*OCIClient
}

// InspectImage normalizes the repo name before delegating to the OCI client.
func (d *dockerHubClient) InspectImage(ctx context.Context, repo, reference string) (*ImageInspection, error) {
	return d.OCIClient.InspectImage(ctx, NormalizeDockerHubRepo(repo), reference)
}

// ListTags normalizes the repo name before delegating to the OCI client.
func (d *dockerHubClient) ListTags(ctx context.Context, repo string) ([]string, error) {
	return d.OCIClient.ListTags(ctx, NormalizeDockerHubRepo(repo))
}

// GetReferrers normalizes the repo name before delegating to the OCI client.
func (d *dockerHubClient) GetReferrers(ctx context.Context, repo, digest string) ([]Referrer, error) {
	return d.OCIClient.GetReferrers(ctx, NormalizeDockerHubRepo(repo), digest)
}

// NewDockerHubClient creates an OCI client configured for Docker Hub.
func NewDockerHubClient(username, password string, logger *zap.Logger, opts ...OCIOption) ImageInspector {
	ociClient := NewOCIClient(dockerHubRegistryURL, logger, opts...)
	ociClient.auth = newDockerHubAuth(username, password, ociClient.httpClient)
	return &dockerHubClient{OCIClient: ociClient}
}

// Compile-time interface checks.
var (
	_ AuthProvider   = (*DockerHubAuth)(nil)
	_ ImageInspector = (*dockerHubClient)(nil)
)
