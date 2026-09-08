package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/auth"
)

func TestCoreRBACFailsClosedWhenAuthEnabledWithoutRBAC(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})
	handler := coreRBAC(RouterDeps{}, auth.MiddlewareConfig{Enabled: true}, nil, true)(next)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))

	if called {
		t.Fatal("nil RBAC allowed the protected handler to run")
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(recorder.Body.String(), "authorization not configured") {
		t.Fatalf("body = %q, want fail-closed authorization error", recorder.Body.String())
	}
}
