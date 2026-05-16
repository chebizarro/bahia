package router_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestLLMOperationalRESTRoutesDisabledByDefault(t *testing.T) {
	cfg := config.Defaults()
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		Config:      cfg,
		LLMRegistry: &service.LLMRegistryService{},
	})

	assertLLMOperationalRESTNotMounted(t, h)

	primaryRoutes := []string{
		"/api/v1/llm/routes",
		"/api/v1/llm/routes/11111111-1111-1111-1111-111111111111/releases",
	}
	for _, path := range primaryRoutes {
		t.Run("primary "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`not-json`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code == http.StatusNotFound {
				t.Fatalf("primary LLM route/release CRUD should remain mounted, got 404")
			}
		})
	}
}

func TestLLMOperationalRESTRoutesNotMountedEvenWhenCompatibilityFlagEnabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.LLM.Enabled = true
	cfg.LLM.AllowOperationalREST = true

	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		Config:      cfg,
		LLMRegistry: &service.LLMRegistryService{},
	})

	assertLLMOperationalRESTNotMounted(t, h)
}

func assertLLMOperationalRESTNotMounted(t *testing.T, h http.Handler) {
	t.Helper()
	operationalRoutes := []string{
		"/api/v1/llm/intents",
		"/api/v1/llm/intents/11111111-1111-1111-1111-111111111111/approve",
		"/api/v1/llm/intents/11111111-1111-1111-1111-111111111111/reject",
		"/api/v1/llm/rollback",
		"/api/v1/llm/hosts",
		"/api/v1/llm/observations",
	}
	for _, path := range operationalRoutes {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404, body=%s", w.Code, w.Body.String())
			}
		})
	}
}
