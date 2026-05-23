package router_test

import (
	"encoding/json"
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
			Status:        app.SnapshotStatusHealthy,
			Mode:          string(app.ModeFull),
			RequestedTier: int(app.Tier3),
			ActiveTier:    int(app.Tier3),
			Ready:         true,
		},
		ready: app.HealthSnapshot{
			Status:        app.SnapshotStatusHealthy,
			Mode:          string(app.ModeFull),
			RequestedTier: int(app.Tier3),
			ActiveTier:    int(app.Tier3),
			Ready:         true,
			Checks: []app.HealthCheck{
				{Name: "relay_quorum", Status: app.HealthStatusPass, Message: "2 connected, 2 healthy", Tier: int(app.Tier3)},
				{Name: "bootstrap_ready", Status: app.HealthStatusPass, Message: "phase=ready", Tier: int(app.Tier3)},
			},
		},
	}
	server := httptest.NewServer(newReadinessTestRouter(provider, app.NewModePolicy(app.ModeFull)))
	defer server.Close()

	healthResp := getHealthResponse(t, server.URL+"/health", http.StatusOK)
	require.Equal(t, app.SnapshotStatusHealthy, healthResp.Status)
	require.Equal(t, router.Version, healthResp.Version)
	require.Equal(t, string(app.ModeFull), healthResp.Mode)
	require.Equal(t, int(app.Tier3), healthResp.RequestedTier)
	require.Equal(t, int(app.Tier3), healthResp.ActiveTier)
	require.True(t, healthResp.Ready)

	readyResp := getHealthResponse(t, server.URL+"/ready", http.StatusOK)
	require.Equal(t, app.SnapshotStatusHealthy, readyResp.Status)
	require.True(t, readyResp.Ready)
	require.Len(t, readyResp.Checks, 2)
	require.Equal(t, "relay_quorum", readyResp.Checks[0].Name)
}

func TestRouterReadinessReturns503WhenProviderNotReady(t *testing.T) {
	provider := readinessTestProvider{
		live: app.HealthSnapshot{Status: app.SnapshotStatusHealthy, Mode: string(app.ModeFull), RequestedTier: int(app.Tier3), ActiveTier: int(app.Tier3), Ready: true},
		ready: app.HealthSnapshot{
			Status:        app.SnapshotStatusUnhealthy,
			Mode:          string(app.ModeFull),
			RequestedTier: int(app.Tier3),
			ActiveTier:    int(app.Tier3),
			Ready:         false,
			Checks:        []app.HealthCheck{{Name: "bootstrap_ready", Status: app.HealthStatusFail, Message: "phase=failed", Tier: int(app.Tier3)}},
		},
	}
	server := httptest.NewServer(newReadinessTestRouter(provider, app.NewModePolicy(app.ModeFull)))
	defer server.Close()

	readyResp := getHealthResponse(t, server.URL+"/ready", http.StatusServiceUnavailable)
	require.Equal(t, app.SnapshotStatusUnhealthy, readyResp.Status)
	require.False(t, readyResp.Ready)
	require.Len(t, readyResp.Checks, 1)
	require.Equal(t, app.HealthStatusFail, readyResp.Checks[0].Status)
}

func TestRouterTierGatedRoutesReturn503Body(t *testing.T) {
	policy := app.NewModePolicy(app.ModeEmergency)
	policy.SetActiveTier(app.Tier1)
	provider := readinessTestProvider{
		live:  app.HealthSnapshot{Status: app.SnapshotStatusHealthy, Mode: string(app.ModeEmergency), RequestedTier: int(app.Tier1), ActiveTier: int(app.Tier1), Ready: true},
		ready: app.HealthSnapshot{Status: app.SnapshotStatusHealthy, Mode: string(app.ModeEmergency), RequestedTier: int(app.Tier1), ActiveTier: int(app.Tier1), Ready: true},
	}
	server := httptest.NewServer(newReadinessTestRouter(provider, policy))
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/services")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "route unavailable in current mode", body["error"])
	require.Equal(t, string(app.ModeEmergency), body["mode"])
	require.Equal(t, float64(app.Tier1), body["active_tier"])
	require.Equal(t, float64(app.Tier2), body["required_tier"])
}

func newReadinessTestRouter(provider readinessTestProvider, policy *app.ModePolicy) http.Handler {
	return router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		HealthProvider: provider,
		ModePolicy:     policy,
	})
}

func getHealthResponse(t *testing.T, url string, wantStatus int) dto.HealthResponse {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, wantStatus, resp.StatusCode)

	var health dto.HealthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&health))
	return health
}
