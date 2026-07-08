// Package qdrant adapts Bahia's Qdrant interface to the shared cascadia-go client.
package qdrant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	shared *casqdrant.Client
	logger *slog.Logger
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
		shared: casqdrant.NewClient(casqdrant.Config{
			URL:                           config.URL,
			APIKey:                        config.APIKey,
			AuthHeaderName:                config.AuthHeaderName,
			Timeout:                       config.Timeout,
			HTTPClient:                    httpClient,
			AllowUnauthenticatedLocalhost: config.AllowUnauthenticatedLocal,
		}),
		logger: logger.With("component", "qdrant"),
	}
}

// Configured reports whether this client has an explicit Qdrant endpoint.
func (c *Client) Configured() bool {
	return c != nil && c.shared.Configured()
}

func (c *Client) requireConfigured() error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	return nil
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
	if err := c.requireConfigured(); err != nil {
		return false, err
	}
	return c.shared.CollectionExists(ctx, name)
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
	if err := c.requireConfigured(); err != nil {
		return err
	}
	_, err := c.shared.Health(ctx)
	return err
}
