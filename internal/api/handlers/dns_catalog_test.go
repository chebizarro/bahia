package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

type dnsCatalogProviderStub struct {
	endpoints []domain.DNSEndpoint
	err       error
}

func (s dnsCatalogProviderStub) ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error) {
	return s.endpoints, s.err
}

func TestDNSCatalogListFiltersHealthyEndpointsAndPaginates(t *testing.T) {
	h := NewDNSCatalogHandler(dnsCatalogProviderStub{endpoints: []domain.DNSEndpoint{
		dnsCatalogEndpoint("api", "api.prod.example.com", "example.com", "prod", domain.HealthStatusHealthy, domain.DriftStatusInSync, "go", "cpu", []string{"http"}, time.Unix(10, 0)),
		dnsCatalogEndpoint("worker", "worker.prod.example.com", "example.com", "prod", domain.HealthStatusHealthy, domain.DriftStatusInSync, "go", "gpu", []string{"compute"}, time.Unix(11, 0)),
		dnsCatalogEndpoint("api-stage", "api.stage.example.com", "example.com", "stage", domain.HealthStatusHealthy, domain.DriftStatusInSync, "go", "cpu", []string{"http"}, time.Unix(12, 0)),
		dnsCatalogEndpoint("db", "db.prod.example.com", "example.com", "prod", domain.HealthStatusUnhealthy, domain.DriftStatusDrifted, "postgres", "cpu", []string{"sql"}, time.Unix(13, 0)),
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dns/catalog?zone=example.com&environment=prod&capability=http&runtime=go&hardware=cpu&offset=0&limit=1", nil)
	w := httptest.NewRecorder()

	h.ListCatalog(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	list := decodeDNSCatalogListResponse(t, w)
	require.Equal(t, float64(1), list["total"])
	require.Equal(t, float64(1), list["limit"])
	require.Equal(t, float64(0), list["offset"])
	items, ok := list["data"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	endpoint, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "api.prod.example.com", endpoint["fqdn"])
	require.Equal(t, "example.com", endpoint["zone"])
	require.Equal(t, "healthy", endpoint["health"])
}

func TestDNSCatalogListAllowsExplicitHealthFilter(t *testing.T) {
	h := NewDNSCatalogHandler(dnsCatalogProviderStub{endpoints: []domain.DNSEndpoint{
		dnsCatalogEndpoint("api", "api.prod.example.com", "example.com", "prod", domain.HealthStatusHealthy, domain.DriftStatusInSync, "go", "cpu", []string{"http"}, time.Unix(10, 0)),
		dnsCatalogEndpoint("db", "db.prod.example.com", "example.com", "prod", domain.HealthStatusUnhealthy, domain.DriftStatusDrifted, "postgres", "cpu", []string{"sql"}, time.Unix(13, 0)),
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dns/catalog?health=unhealthy", nil)
	w := httptest.NewRecorder()

	h.ListCatalog(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	list := decodeDNSCatalogListResponse(t, w)
	items, ok := list["data"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	endpoint, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "db.prod.example.com", endpoint["fqdn"])
	require.Equal(t, "unhealthy", endpoint["health"])
}

func TestDNSCatalogGetEndpointByFQDN(t *testing.T) {
	h := NewDNSCatalogHandler(dnsCatalogProviderStub{endpoints: []domain.DNSEndpoint{
		dnsCatalogEndpoint("api", "api.prod.example.com", "example.com", "prod", domain.HealthStatusHealthy, domain.DriftStatusInSync, "go", "cpu", []string{"http"}, time.Unix(10, 0)),
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dns/catalog/api.prod.example.com", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fqdn", "api.prod.example.com")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	h.GetCatalogEndpoint(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp dto.APIResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	endpoint, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "api.prod.example.com", endpoint["fqdn"])
}

func TestDNSCatalogGetEndpointNotFoundForUnhealthyEndpoint(t *testing.T) {
	h := NewDNSCatalogHandler(dnsCatalogProviderStub{endpoints: []domain.DNSEndpoint{
		dnsCatalogEndpoint("db", "db.prod.example.com", "example.com", "prod", domain.HealthStatusUnhealthy, domain.DriftStatusDrifted, "postgres", "cpu", []string{"sql"}, time.Unix(13, 0)),
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dns/catalog/db.prod.example.com", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fqdn", "db.prod.example.com")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	h.GetCatalogEndpoint(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	var resp dto.APIResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "DNS endpoint not found", resp.Error)
}

func TestDNSCatalogListZonesReturnsZoneStatus(t *testing.T) {
	h := NewDNSCatalogHandler(dnsCatalogProviderStub{endpoints: []domain.DNSEndpoint{
		dnsCatalogEndpoint("api", "api.prod.example.com", "example.com", "prod", domain.HealthStatusHealthy, domain.DriftStatusInSync, "go", "cpu", []string{"http"}, time.Unix(10, 0)),
		dnsCatalogEndpoint("db", "db.prod.example.com", "example.com", "prod", domain.HealthStatusUnhealthy, domain.DriftStatusDrifted, "postgres", "cpu", []string{"sql"}, time.Unix(13, 0)),
		dnsCatalogEndpoint("edge", "edge.edge.example.net", "edge.example.net", "prod", domain.HealthStatusHealthy, domain.DriftStatusInSync, "go", "cpu", []string{"http"}, time.Unix(12, 0)),
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dns/zones", nil)
	w := httptest.NewRecorder()

	h.ListZones(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp dto.APIResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	zones, ok := resp.Data.([]any)
	require.True(t, ok)
	require.Len(t, zones, 2)
	first, ok := zones[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "edge.example.net", first["name"])
	require.Equal(t, "healthy", first["health"])
	second, ok := zones[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "example.com", second["name"])
	require.Equal(t, "unhealthy", second["health"])
	require.Equal(t, float64(2), second["endpoint_count"])
}

func TestDNSCatalogListDriftReturnsRecentDriftEvents(t *testing.T) {
	h := NewDNSCatalogHandler(dnsCatalogProviderStub{endpoints: []domain.DNSEndpoint{
		dnsCatalogEndpoint("in-sync", "sync.prod.example.com", "example.com", "prod", domain.HealthStatusHealthy, domain.DriftStatusInSync, "go", "cpu", []string{"http"}, time.Unix(10, 0)),
		dnsCatalogEndpoint("older", "older.prod.example.com", "example.com", "prod", domain.HealthStatusHealthy, domain.DriftStatusDrifted, "go", "cpu", []string{"http"}, time.Unix(11, 0)),
		dnsCatalogEndpoint("newer", "newer.prod.example.com", "example.com", "prod", domain.HealthStatusHealthy, domain.DriftStatusDeploying, "go", "cpu", []string{"http"}, time.Unix(12, 0)),
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dns/drift?limit=1", nil)
	w := httptest.NewRecorder()

	h.ListDrift(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	list := decodeDNSCatalogListResponse(t, w)
	require.Equal(t, float64(2), list["total"])
	items, ok := list["data"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	endpoint, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "newer.prod.example.com", endpoint["fqdn"])
	require.Equal(t, "deploying", endpoint["drift_status"])
}

func TestDNSCatalogHandlerFailsClosedWithoutProvider(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dns/catalog", nil)
	w := httptest.NewRecorder()

	NewDNSCatalogHandler(nil).ListCatalog(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var resp dto.APIResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "DNS catalog provider unavailable", resp.Error)
}

func TestDNSCatalogHandlerReturnsProviderErrors(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dns/catalog", nil)
	w := httptest.NewRecorder()

	NewDNSCatalogHandler(dnsCatalogProviderStub{err: errors.New("projection read failed")}).ListCatalog(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var resp dto.APIResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "projection read failed", resp.Error)
}

func dnsCatalogEndpoint(name, fqdn, zone, environment string, health domain.HealthStatus, drift domain.DriftStatus, runtime, hardware string, capabilities []string, materializedAt time.Time) domain.DNSEndpoint {
	endpoint := domain.DNSEndpoint{
		Family:         domain.DNSEndpointFamilyService,
		Name:           name,
		Environment:    environment,
		Zone:           zone,
		FQDN:           fqdn,
		Protocol:       "http",
		Address:        "10.0.0.10",
		Runtime:        runtime,
		Hardware:       hardware,
		Capabilities:   capabilities,
		Health:         health,
		DriftStatus:    drift,
		Source:         "test",
		MaterializedAt: materializedAt,
	}
	if err := domain.ValidateDNSEndpoint(&endpoint); err != nil {
		panic(err)
	}
	return endpoint
}

func decodeDNSCatalogListResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp dto.APIResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	list, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	return list
}
