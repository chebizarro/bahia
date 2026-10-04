package router_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// The deleted reads must stay absent even if write-only ML/LLM handlers exist.
type d1WorkerRepo struct{ repository.WorkerRepository }

func TestPhase5D1DeletedReadsReturn404(t *testing.T) {
	h := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		LLMRegistry: &service.LLMRegistryService{},
		Workers:     d1WorkerRepo{},
		MLCommands:  &captureMLRESTPublisher{},
	})
	paths := []string{
		"/api/v1/deployments/intents/00000000-0000-0000-0000-000000000001",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000002/intents",
		"/api/v1/deployments/runs/00000000-0000-0000-0000-000000000001",
		"/api/v1/deployments/intents/00000000-0000-0000-0000-000000000001/runs",
		"/api/v1/environments/00000000-0000-0000-0000-000000000002/state",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000002/state",
		"/api/v1/workers/worker-pubkey/pricing",
		"/api/v1/ml/models",
		"/api/v1/ml/models/00000000-0000-0000-0000-000000000001",
		"/api/v1/ml/models/00000000-0000-0000-0000-000000000001/versions",
		"/api/v1/ml/model-versions/00000000-0000-0000-0000-000000000001",
		"/api/v1/ml/endpoints",
		"/api/v1/ml/endpoints/00000000-0000-0000-0000-000000000001",
		"/api/v1/ml/state",
		"/api/v1/ml/endpoints/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000002/state",
		"/api/v1/ml/artifacts/00000000-0000-0000-0000-000000000001/provenance",
		"/api/v1/llm/routes",
		"/api/v1/llm/routes/00000000-0000-0000-0000-000000000001",
		"/api/v1/llm/routes/00000000-0000-0000-0000-000000000001/releases",
		"/api/v1/llm/releases/00000000-0000-0000-0000-000000000001",
		"/api/v1/llm/intents/00000000-0000-0000-0000-000000000001",
		"/api/v1/llm/routes/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000002/intents",
		"/api/v1/llm/runs/00000000-0000-0000-0000-000000000001",
		"/api/v1/llm/intents/00000000-0000-0000-0000-000000000001/runs",
		"/api/v1/llm/state",
		"/api/v1/llm/state/drifted",
		"/api/v1/llm/environments/00000000-0000-0000-0000-000000000002/state",
		"/api/v1/llm/routes/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000002/state",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET %s = %d, want 404", path, rec.Code)
			}
		})
	}
}

func TestPhase5D1RouterConstructsWithoutDeletedDomainDeps(t *testing.T) {
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{})
	for _, path := range []string{"/health", "/ready"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func TestPhase5D1RetainedHTTPFallbackReadsReturn200(t *testing.T) {
	server := newTestServer()
	defer server.Close()
	for _, path := range []string{
		"/api/v1/services",
		"/api/v1/environments",
		"/api/v1/state",
		"/api/v1/state/drifted",
	} {
		resp, _ := doJSON(t, http.MethodGet, server.URL+path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
}
