package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

type coreRBACMemberLookup struct {
	member *domain.OrgMember
}

func (m coreRBACMemberLookup) GetMember(context.Context, uuid.UUID, string) (*domain.OrgMember, error) {
	return m.member, nil
}

func (m coreRBACMemberLookup) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	if m.member == nil {
		return nil, nil
	}
	return []domain.OrgMember{*m.member}, nil
}

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

func TestCoreRBACRequiresExactMembership(t *testing.T) {
	orgID := uuid.New()
	gate := coreRBAC(RouterDeps{RBAC: auth.NewRBAC(coreRBACMemberLookup{})}, auth.MiddlewareConfig{Enabled: true}, nil, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	req.Header.Set("X-Bahia-Org-ID", orgID.String())
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{Subject: "unknown", PubKey: "unknown", Method: auth.MethodNIP98}))
	w := httptest.NewRecorder()
	gate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") })).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestCoreRBACRequiresDeclaredWritePermission(t *testing.T) {
	orgID := uuid.New()
	member := &domain.OrgMember{OrgID: orgID, Pubkey: "viewer", Role: domain.RoleViewer}
	gate := coreRBAC(RouterDeps{RBAC: auth.NewRBAC(coreRBACMemberLookup{member: member})}, auth.MiddlewareConfig{Enabled: true}, nil, true, domain.PermWriteSecrets)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/services/id/secrets", nil)
	req.Header.Set("X-Bahia-Org-ID", orgID.String())
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{Subject: "viewer", PubKey: "viewer", Method: auth.MethodNIP98}))
	w := httptest.NewRecorder()
	gate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") })).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestUnaffiliatedOnboardingRoutesAreExact(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
		want   bool
	}{
		{method: http.MethodGet, path: "/api/v1/me/invites", want: true},
		{method: http.MethodPost, path: "/api/v1/invites/123/accept", want: true},
		{method: http.MethodGet, path: "/api/v1/orgs", want: false},
		{method: http.MethodPost, path: "/api/v1/orgs", want: false},
		{method: http.MethodPost, path: "/api/v1/invites/123/revoke", want: false},
		{method: http.MethodGet, path: "/api/v1/invites/123/accept", want: false},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if got := isUnaffiliatedOnboardingRoute(req); got != tc.want {
				t.Fatalf("isUnaffiliatedOnboardingRoute() = %t, want %t", got, tc.want)
			}
		})
	}
}
