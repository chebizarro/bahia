package router_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/app"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type readinessTestProvider struct {
	live  app.HealthSnapshot
	ready app.HealthSnapshot
}

func (p readinessTestProvider) Liveness() app.HealthSnapshot  { return p.live }
func (p readinessTestProvider) Readiness() app.HealthSnapshot { return p.ready }

func TestRouterReadinessEndpoints(t *testing.T) {
	provider := readinessTestProvider{
		live: app.HealthSnapshot{
			Status: app.SnapshotStatusHealthy,
			Ready:  true,
		},
		ready: app.HealthSnapshot{
			Status: app.SnapshotStatusHealthy,
			Ready:  true,
			Checks: []app.HealthCheck{
				{Name: "relay_quorum", Status: app.HealthStatusPass, Message: "2 connected, 2 healthy"},
				{Name: "bootstrap_ready", Status: app.HealthStatusPass, Message: "phase=ready"},
			},
		},
	}
	server := httptest.NewServer(newReadinessTestRouter(provider))
	defer server.Close()

	healthResp := getHealthResponse(t, server.URL+"/health", http.StatusOK)
	require.Equal(t, app.SnapshotStatusHealthy, healthResp.Status)
	require.Equal(t, router.Version, healthResp.Version)
	require.True(t, healthResp.Ready)

	readyResp := getHealthResponse(t, server.URL+"/ready", http.StatusOK)
	require.Equal(t, app.SnapshotStatusHealthy, readyResp.Status)
	require.True(t, readyResp.Ready)
	require.Len(t, readyResp.Checks, 2)
	require.Equal(t, "relay_quorum", readyResp.Checks[0].Name)
}

func TestRouterReadinessReturns503WhenProviderNotReady(t *testing.T) {
	provider := readinessTestProvider{
		live: app.HealthSnapshot{Status: app.SnapshotStatusHealthy, Ready: true},
		ready: app.HealthSnapshot{
			Status: app.SnapshotStatusUnhealthy,
			Ready:  false,
			Checks: []app.HealthCheck{{Name: "bootstrap_ready", Status: app.HealthStatusFail, Message: "phase=failed"}},
		},
	}
	server := httptest.NewServer(newReadinessTestRouter(provider))
	defer server.Close()

	readyResp := getHealthResponse(t, server.URL+"/ready", http.StatusServiceUnavailable)
	require.Equal(t, app.SnapshotStatusUnhealthy, readyResp.Status)
	require.False(t, readyResp.Ready)
	require.Len(t, readyResp.Checks, 1)
	require.Equal(t, app.HealthStatusFail, readyResp.Checks[0].Status)
}

func TestRouterRequireRepoGatedRoutesReturn503(t *testing.T) {
	// With nil registry, RequireRepo gates should return 503
	provider := readinessTestProvider{
		live:  app.HealthSnapshot{Status: app.SnapshotStatusHealthy, Ready: true},
		ready: app.HealthSnapshot{Status: app.SnapshotStatusHealthy, Ready: true},
	}
	server := httptest.NewServer(newReadinessTestRouter(provider))
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/services")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "dependency unavailable", body["error"])
}

func newReadinessTestRouter(provider readinessTestProvider) http.Handler {
	return router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		HealthProvider: provider,
	})
}

func getHealthResponse(t *testing.T, url string, wantStatus int) dto.HealthResponse {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	require.Equal(t, wantStatus, resp.StatusCode)

	var health dto.HealthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&health))
	return health
}

func closeResponseBodyReadiness(t *testing.T, body io.Closer) {
	t.Helper()
	require.NoError(t, body.Close())
}
