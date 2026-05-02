package router_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

func TestPrivilegedRoutesDisabledByDefault(t *testing.T) {
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: config.Defaults()})

	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "adoption scan", path: "/api/v1/adoption/scan", body: `{}`},
		{name: "adoption import", path: "/api/v1/adoption/import", body: `{}`},
		{name: "runtime deploy", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/deploy", body: `{}`},
		{name: "runtime restart", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/restart", body: `{}`},
		{name: "runtime stop", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/stop", body: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("disabled route %s status=%d, want 404, body=%s", tt.path, w.Code, w.Body.String())
			}
		})
	}
}

func TestPrivilegedRoutesRequireOperatorAccess(t *testing.T) {
	const secret = "test-secret"
	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	cfg.Auth.JWTSecret = secret
	cfg.Adoption.Enabled = true
	cfg.Adoption.AllowedSubjects = []string{"ops"}
	cfg.DirectRuntime.Enabled = true
	cfg.DirectRuntime.AllowedSubjects = []string{"ops"}

	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		Config: cfg,
		AuthMiddleware: auth.MiddlewareConfig{
			Enabled:   true,
			JWTSecret: secret,
		},
	})

	operatorToken, err := auth.GenerateToken("ops", secret, time.Hour)
	if err != nil {
		t.Fatalf("operator token: %v", err)
	}
	userToken, err := auth.GenerateToken("developer", secret, time.Hour)
	if err != nil {
		t.Fatalf("user token: %v", err)
	}

	tests := []struct {
		name       string
		path       string
		token      string
		wantStatus int
	}{
		{name: "adoption unauthorized", path: "/api/v1/adoption/scan", wantStatus: http.StatusUnauthorized},
		{name: "adoption forbidden", path: "/api/v1/adoption/scan", token: userToken, wantStatus: http.StatusForbidden},
		{name: "adoption operator reaches handler", path: "/api/v1/adoption/scan", token: operatorToken, wantStatus: http.StatusServiceUnavailable},
		{name: "runtime unauthorized", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/restart", wantStatus: http.StatusUnauthorized},
		{name: "runtime forbidden", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/restart", token: userToken, wantStatus: http.StatusForbidden},
		{name: "runtime operator reaches handler", path: "/api/v1/services/11111111-1111-1111-1111-111111111111/environments/22222222-2222-2222-2222-222222222222/restart", token: operatorToken, wantStatus: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d, body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}
