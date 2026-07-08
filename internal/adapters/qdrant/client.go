// Package qdrant adapts Bahia's Qdrant interface to the shared cascadia-go client.
package qdrant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	casqdrant "git.sharegap.net/cascadia/cascadia-go/qdrant"
)

// ErrNotConfigured is returned when a Qdrant operation is attempted without an explicit URL.
var ErrNotConfigured = errors.New("qdrant client not configured")

// ErrMissingAuth is returned when a Qdrant endpoint is configured without required auth.
var ErrMissingAuth = errors.New("qdrant auth not configured")

// Client communicates with Qdrant REST API through cascadia-go/qdrant.
type Client struct {
	baseURL     string
	apiKey      string
	authHeader  string
	allowNoAuth bool
	shared      *casqdrant.Client
	httpClient  *http.Client
	logger      *slog.Logger
}

// Config holds Qdrant client configuration.
type Config struct {
	URL                       string
	Timeout                   time.Duration
	APIKey                    string
	AuthHeaderName            string
	AllowUnauthenticatedLocal bool
}

// CollectionConfig holds collection creation parameters.
type CollectionConfig = casqdrant.CollectionConfig

// DefaultCollectionConfig returns the default config for agent collections.
func DefaultCollectionConfig() CollectionConfig {
	return CollectionConfig{VectorSize: 768, Distance: "Cosine", OnDiskPayload: true}
}

// NewClient creates a new Qdrant client.
func NewClient(config Config, logger *slog.Logger) *Client {
	config.URL = strings.TrimRight(strings.TrimSpace(config.URL), "/")
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.AuthHeaderName = strings.TrimSpace(config.AuthHeaderName)
	if config.AuthHeaderName == "" {
		config.AuthHeaderName = "api-key"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	httpClient := &http.Client{Timeout: config.Timeout}
	return &Client{
		baseURL:     config.URL,
		apiKey:      config.APIKey,
		authHeader:  config.AuthHeaderName,
		allowNoAuth: config.AllowUnauthenticatedLocal,
		shared: casqdrant.NewClient(casqdrant.Config{
			URL:            config.URL,
			APIKey:         config.APIKey,
			AuthHeaderName: config.AuthHeaderName,
			Timeout:        config.Timeout,
			HTTPClient:     httpClient,
		}),
		httpClient: httpClient,
		logger:     logger.With("component", "qdrant"),
	}
}

// Configured reports whether this client has an explicit Qdrant endpoint.
func (c *Client) Configured() bool {
	return c != nil && strings.TrimSpace(c.baseURL) != ""
}

func (c *Client) requireConfigured() error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	if strings.TrimSpace(c.apiKey) == "" && !c.allowNoAuth {
		return ErrMissingAuth
	}
	if strings.TrimSpace(c.apiKey) == "" && c.allowNoAuth && !isLocalQdrantEndpoint(c.baseURL) {
		return fmt.Errorf("%w: unauthenticated mode is only allowed for localhost or loopback endpoints", ErrMissingAuth)
	}
	return nil
}

func isLocalQdrantEndpoint(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if err := c.requireConfigured(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set(c.authHeader, c.apiKey)
	}
	return req, nil
}

// CreateCollection creates a new vector collection.
func (c *Client) CreateCollection(ctx context.Context, name string, config CollectionConfig) error {
	if err := c.requireConfigured(); err != nil {
		return err
	}
	c.logger.Info("creating Qdrant collection", "name", name, "vector_size", config.VectorSize, "distance", config.Distance)
	if err := c.shared.EnsureCollection(ctx, name, config); err != nil {
		return err
	}
	c.logger.Info("Qdrant collection ready", "name", name)
	return nil
}

// CollectionExists checks if a collection exists.
func (c *Client) CollectionExists(ctx context.Context, name string) (bool, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/collections/%s", name), nil)
	if err != nil {
		return false, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}
	return true, nil
}

// DeleteCollection deletes a collection.
func (c *Client) DeleteCollection(ctx context.Context, name string) error {
	if err := c.requireConfigured(); err != nil {
		return err
	}
	c.logger.Info("deleting Qdrant collection", "name", name)
	return c.shared.DeleteCollection(ctx, name)
}

// UpsertPoints inserts or updates points in a collection.
func (c *Client) UpsertPoints(ctx context.Context, collection string, points []Point) error {
	if err := c.requireConfigured(); err != nil {
		return err
	}
	sharedPoints := make([]casqdrant.Point, 0, len(points))
	for _, point := range points {
		sharedPoints = append(sharedPoints, casqdrant.Point{ID: point.ID, Vector: point.Vector, Payload: point.Payload})
	}
	return c.shared.Upsert(ctx, collection, sharedPoints)
}

// Search performs a vector similarity search.
func (c *Client) Search(ctx context.Context, collection string, vector []float32, limit int) ([]SearchResult, error) {
	if err := c.requireConfigured(); err != nil {
		return nil, err
	}
	results, err := c.shared.Search(ctx, collection, casqdrant.SearchRequest{Vector: vector, Limit: limit, WithPayload: true})
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(results))
	for _, result := range results {
		out = append(out, SearchResult{ID: fmt.Sprint(result.ID), Score: result.Score, Payload: result.Payload})
	}
	return out, nil
}

// Point represents a vector point with payload.
type Point struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload,omitempty"`
}

// SearchResult represents a search result.
type SearchResult struct {
	ID      string         `json:"id"`
	Score   float32        `json:"score"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Health checks Qdrant connectivity.
func (c *Client) Health(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
