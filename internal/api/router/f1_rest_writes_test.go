package router_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/handlers"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Every retired REST write must remain absent even when the daemon has its
// ordinary registry and optional write-path dependencies configured.
func TestPhase5F1DeletedWriteRoutesReturn404(t *testing.T) {
	h := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		ToolProvisioning: newMockToolProvisioningRepo(),
		Payments:         &service.PaymentService{},
		SBOMs:            tenantIsolationSBOMRepo{},
		Artifacts:        newMockArtifactRepo(),
	})
	id := "00000000-0000-0000-0000-000000000001"
	tests := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/repositories/ci/lookup"},
		{http.MethodPost, "/api/v1/blossom/list"},
		{http.MethodPost, "/api/v1/builds"},
		{http.MethodPatch, "/api/v1/builds/" + id + "/status"},
		{http.MethodPost, "/api/v1/ml/imports"},
		{http.MethodPost, "/api/v1/ml/recipes/runs"},
		{http.MethodPost, "/api/v1/ml/deployments"},
		{http.MethodPost, "/api/v1/ml/rollback"},
		{http.MethodPut, "/api/v1/llm/routes/" + id},
		{http.MethodPost, "/api/v1/deployments/runs"},
		{http.MethodPost, "/api/v1/deployments/runs/" + id + "/complete"},
		{http.MethodPost, "/api/v1/payments/estimate"},
		{http.MethodPost, "/api/v1/artifacts/" + id + "/signatures/verify"},
		{http.MethodPost, "/api/v1/notifications/channels/" + id + "/test"},
		{http.MethodPost, "/api/v1/tools/denylist"},
		{http.MethodDelete, "/api/v1/tools/denylist/left-pad/npm"},
		{http.MethodPost, "/api/v1/mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want 404", tt.method, tt.path, rec.Code)
			}
		})
	}
}

// A non-404 response verifies each intentional compatibility boundary is still
// mounted; authorization and payload validation remain the handler's policy.
type f1LegacyReconciler struct {
	handlers.LegacyAgentReconciliationController
}

func TestPhase5F1RetainedWriteBoundariesStayMounted(t *testing.T) {
	h := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		InstanceHealth:            routeInstanceHealthRepo{},
		Services:                  routeInstanceServiceRepo{},
		Environments:              routeInstanceEnvironmentRepo{},
		SBOMs:                     tenantIsolationSBOMRepo{},
		Artifacts:                 newMockArtifactRepo(),
		SBOMImporter:              &service.SBOMOrchestrator{},
		LegacyAgentReconciliation: f1LegacyReconciler{},
	})
	for _, tt := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/services/svc/environments/env/managed-instances/unit/maintenance"},
		{http.MethodDelete, "/api/v1/services/svc/environments/env/managed-instances/unit/maintenance"},
		{http.MethodPost, "/api/v1/artifacts/not-a-uuid/sbom"},
		{http.MethodPost, "/api/v1/soulfactory/legacy-reconciliation/preview"},
		{http.MethodPost, "/api/v1/soulfactory/legacy-reconciliation/apply"},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code == http.StatusNotFound {
				t.Fatalf("retained %s %s returned 404", tt.method, tt.path)
			}
		})
	}
}
