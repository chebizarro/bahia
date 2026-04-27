// Package harbor provides a client for the Harbor OCI registry API.
package harbor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

// ArtifactInfo holds metadata retrieved from Harbor.
type ArtifactInfo struct {
	Digest    string `json:"digest"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	Tags      []Tag  `json:"tags"`
	ScanStatus string `json:"scan_status,omitempty"`
}

// Tag is an image tag in Harbor.
type Tag struct {
	Name string `json:"name"`
}

// Client interacts with the Harbor v2 API.
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
	logger     *zap.Logger
}

// NewClient creates a new Harbor API client.
func NewClient(cfg config.HarborConfig, logger *zap.Logger) *Client {
	return &Client{
		baseURL:  cfg.URL,
		username: cfg.Username,
		password: cfg.Password,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// ResolveTag resolves an image tag to a digest.
func (c *Client) ResolveTag(ctx context.Context, project, repo, tag string) (*ArtifactInfo, error) {
	url := fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s/artifacts/%s", c.baseURL, project, repo, tag)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("harbor API returned %d: %s", resp.StatusCode, string(body))
	}

	var info ArtifactInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &info, nil
}

// ImageExists checks whether an artifact exists in Harbor.
func (c *Client) ImageExists(ctx context.Context, project, repo, reference string) (bool, error) {
	url := fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s/artifacts/%s", c.baseURL, project, repo, reference)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, fmt.Errorf("creating request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

// GetArtifact retrieves full artifact metadata from Harbor.
func (c *Client) GetArtifact(ctx context.Context, project, repo, reference string) (*ArtifactInfo, error) {
	return c.ResolveTag(ctx, project, repo, reference)
}

func (c *Client) setAuth(req *http.Request) {
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	req.Header.Set("Accept", "application/json")
}
