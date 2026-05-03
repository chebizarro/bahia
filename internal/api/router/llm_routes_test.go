package router_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/auth"
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

func TestLLMOperationalRESTCompatibilityRequiresOperatorAccess(t *testing.T) {
	operatorPubkey, err := nostr.GetPublicKey(routerNIP98Key)
	if err != nil {
		t.Fatal(err)
	}
	const userKey = "0000000000000000000000000000000000000000000000000000000000000004"
	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	cfg.LLM.Enabled = true
	cfg.LLM.AllowOperationalREST = true
	cfg.LLM.AllowedPubkeys = []string{operatorPubkey}

	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		Config: cfg,
		AuthMiddleware: auth.MiddlewareConfig{
			Enabled:        true,
			NIP98Validator: auth.NewNIP98Validator(auth.DefaultNIP98Config()),
		},
		LLMRegistry: &service.LLMRegistryService{},
	})

	tests := []struct {
		name       string
		key        string
		wantStatus int
	}{
		{name: "unauthorized", wantStatus: http.StatusUnauthorized},
		{name: "forbidden", key: userKey, wantStatus: http.StatusForbidden},
		{name: "operator reaches compatibility handler", key: routerNIP98Key, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqPath := "/api/v1/llm/intents"
			req := httptest.NewRequest(http.MethodPost, reqPath, strings.NewReader(`not-json`))
			req.Header.Set("Content-Type", "application/json")
			if tt.key != "" {
				req.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, tt.key, http.MethodPost, "http://example.com"+reqPath))
			}
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d, body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}
