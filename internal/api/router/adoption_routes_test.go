package router_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

func TestDeprecatedAdoptionAndDirectRuntimeRoutesNotMounted(t *testing.T) {
	cfg := config.Defaults()
	cfg.Adoption.Enabled = true
	cfg.DirectRuntime.Enabled = true
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Config: cfg})

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
			if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("removed route %s status=%d, want 404 or 405, body=%s", tt.path, w.Code, w.Body.String())
			}
		})
	}
}
