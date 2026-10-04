package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ListWorkerStates reads the legacy HTTP worker read model.
// Deprecated: use NostrClient worker-state subscriptions by default.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListWorkerStates(ctx context.Context) ([]domain.Worker, error) {
	var workers []domain.Worker
	err := c.do(ctx, http.MethodGet, "/api/v1/workers", nil, &workers)
	return workers, err
}

// GetWorkerState reads one legacy HTTP worker read model.
// Deprecated: use NostrClient worker-state subscriptions by default.
// Phase 5 F2: retained until F3 lands.
func (c *Client) GetWorkerState(ctx context.Context, pubkey string) (*domain.Worker, error) {
	var worker domain.Worker
	err := c.do(ctx, http.MethodGet, "/api/v1/workers/"+url.PathEscape(pubkey), nil, &worker)
	return &worker, err
}

// GetBuild reads one legacy HTTP build record.
// Deprecated: use NostrClient build-registry subscriptions by default.
// Phase 5 F2: retained until F3 lands.
func (c *Client) GetBuild(ctx context.Context, id string) (*domain.Build, error) {
	var build domain.Build
	err := c.do(ctx, http.MethodGet, "/api/v1/builds/"+url.PathEscape(id), nil, &build)
	return &build, err
}

// ListBuilds reads one legacy HTTP build page.
// Deprecated: use NostrClient build-registry subscriptions by default.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListBuilds(ctx context.Context, serviceID string, limit, offset int) ([]domain.Build, error) {
	var builds []domain.Build
	path := fmt.Sprintf("/api/v1/services/%s/builds?limit=%d&offset=%d", url.PathEscape(serviceID), limit, offset)
	err := c.do(ctx, http.MethodGet, path, nil, &builds)
	return builds, err
}

// GetArtifact reads one legacy HTTP artifact record.
// Deprecated: use NostrClient artifact-registry subscriptions by default.
// Phase 5 F2: retained until F3 lands.
func (c *Client) GetArtifact(ctx context.Context, id string) (*domain.Artifact, error) {
	var artifact domain.Artifact
	err := c.do(ctx, http.MethodGet, "/api/v1/artifacts/"+url.PathEscape(id), nil, &artifact)
	return &artifact, err
}

// ListArtifacts reads one legacy HTTP artifact page.
// Deprecated: use NostrClient artifact-registry subscriptions by default.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListArtifacts(ctx context.Context, serviceID string, limit, offset int) ([]domain.Artifact, error) {
	var artifacts []domain.Artifact
	path := fmt.Sprintf("/api/v1/services/%s/artifacts?limit=%d&offset=%d", url.PathEscape(serviceID), limit, offset)
	err := c.do(ctx, http.MethodGet, path, nil, &artifacts)
	return artifacts, err
}
