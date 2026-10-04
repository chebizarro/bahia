package router_test

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// The deleted reads must stay absent even if write-only ML/LLM handlers exist.
func TestPhase5D1DeletedReadsReturn404(t *testing.T) {
	cfg := config.Defaults()
	cfg.SoulFactory.Enabled = true
	h := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		LLMRegistry: &service.LLMRegistryService{},
		Config:      cfg,
		MLCommands:  &captureMLRESTPublisher{},
	})
	paths := []string{
		"/api/v1/services",
		"/api/v1/services/00000000-0000-0000-0000-000000000001",
		"/api/v1/environments",
		"/api/v1/environments/00000000-0000-0000-0000-000000000001",
		"/api/v1/builds/00000000-0000-0000-0000-000000000001",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/builds",
		"/api/v1/artifacts/00000000-0000-0000-0000-000000000001",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/artifacts",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/runtime-releases",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/runtime-releases/rollback",
		"/api/v1/state",
		"/api/v1/state/drifted",
		"/api/v1/instance-health",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000001/managed-instances/00000000-0000-0000-0000-000000000001/health",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000001/managed-instances/00000000-0000-0000-0000-000000000001/health/events",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000001/managed-instances/00000000-0000-0000-0000-000000000001/health/recovery-attempts",
		"/api/v1/route-canaries",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000001/routes/example.com/canary",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/environments/00000000-0000-0000-0000-000000000001/routes/example.com/canary/events",
		"/api/v1/workers",
		"/api/v1/workers/worker-pubkey",
		"/api/v1/policies",
		"/api/v1/policies/00000000-0000-0000-0000-000000000001",
		"/api/v1/artifacts/00000000-0000-0000-0000-000000000001/sbom",
		"/api/v1/artifacts/00000000-0000-0000-0000-000000000001/sbom/packages",
		"/api/v1/sbom/search",
		"/api/v1/artifacts/00000000-0000-0000-0000-000000000001/signatures",
		"/api/v1/artifacts/00000000-0000-0000-0000-000000000001/signatures/verified",
		"/api/v1/artifacts/00000000-0000-0000-0000-000000000001/signatures/check",
		"/api/v1/signatures/00000000-0000-0000-0000-000000000001",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/secrets",
		"/api/v1/notifications/channels",
		"/api/v1/notifications/channels/00000000-0000-0000-0000-000000000001",
		"/api/v1/notifications/log",
		"/api/v1/tools/pending",
		"/api/v1/tools/00000000-0000-0000-0000-000000000001",
		"/api/v1/tools/denylist",
		"/api/v1/services/00000000-0000-0000-0000-000000000001/tools",
		"/api/v1/soulfactory/runtimes",
		"/api/v1/blossom/servers",
		"/api/v1/blossom/health",
		"/api/v1/blossom/stats",
		"/api/v1/orgs",
		"/api/v1/orgs/00000000-0000-0000-0000-000000000001",
		"/api/v1/orgs/00000000-0000-0000-0000-000000000001/members",
		"/api/v1/orgs/00000000-0000-0000-0000-000000000001/invites",
		"/api/v1/me/invites",

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

func TestPhase5D1bRetainedHTTPNativeReadsReturn200(t *testing.T) {
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{})
	for _, path := range []string{"/health", "/ready"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func TestPhase5D1bBlossomBlobFetchRemains200(t *testing.T) {
	content := []byte("retained blossom blob")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+hash {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(content)
	}))
	defer upstream.Close()
	client := blossom.NewClient(blossom.Config{Servers: []string{upstream.URL}, MaxRetries: 1}, slog.Default())
	h := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Blossom: client})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/blossom/blob/"+hash, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != string(content) {
		t.Fatalf("blob fetch = %d %q, want 200 and exact content", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/v1/blossom/servers", "/api/v1/blossom/health", "/api/v1/blossom/stats"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/blossom/list", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("POST /api/v1/blossom/list = %d, want 404", response.Code)
	}
}

func TestPhase5D1bDeletedToolReadsAre404WithWritesMounted(t *testing.T) {
	h := router.NewWithDeps(newTestRegistryService(), zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{ToolProvisioning: newMockToolProvisioningRepo()})
	for _, path := range []string{"/api/v1/tools/pending", "/api/v1/tools/denylist", "/api/v1/tools/00000000-0000-0000-0000-000000000001"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}
