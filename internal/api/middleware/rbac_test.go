package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

// mockMemberLookup is a test mock for auth.OrgMemberLookup.
type mockMemberLookup struct {
	member  *domain.OrgMember
	members []domain.OrgMember
}

func (m *mockMemberLookup) GetMember(ctx context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	if m.member != nil {
		return m.member, nil
	}
	return nil, nil
}

func (m *mockMemberLookup) ListByPubkey(ctx context.Context, pubkey string) ([]domain.OrgMember, error) {
	return m.members, nil
}

func TestRBACMiddleware_LoadsAuthz(t *testing.T) {
	orgID := uuid.New()
	member := &domain.OrgMember{
		OrgID:  orgID,
		Pubkey: "testpubkey",
		Role:   domain.RoleAdmin,
	}
	lookup := &mockMemberLookup{member: member}
	rbac := auth.NewRBAC(lookup)

	middleware := RBAC(RBACConfig{
		RBAC:       rbac,
		OrgIDParam: "orgId",
	})

	var gotAuthz *auth.AuthzContext

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthz = GetAuthz(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/orgs/"+orgID.String()+"/test", nil)
	// Add principal to context
	principal := &auth.Principal{
		Subject: "test",
		Method:  auth.MethodNIP98,
		PubKey:  "testpubkey",
	}
	ctx := auth.ContextWithPrincipal(req.Context(), principal)

	// Add chi route context
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", orgID.String())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)

	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	if gotAuthz == nil {
		t.Fatal("authz should be loaded")
	}

	if gotAuthz.Member == nil {
		t.Error("authz.Member should not be nil")
	}

	if gotAuthz.Member.Role != domain.RoleAdmin {
		t.Errorf("authz.Member.Role = %s, want admin", gotAuthz.Member.Role)
	}
}

func TestRBACMiddleware_RequiredAuth(t *testing.T) {
	lookup := &mockMemberLookup{}
	rbac := auth.NewRBAC(lookup)

	middleware := RBAC(RBACConfig{
		RBAC:     rbac,
		Required: true,
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No principal
	req := httptest.NewRequest("GET", "/test", nil)
	rctx := chi.NewRouteContext()
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestRBACMiddleware_InvalidOrgID(t *testing.T) {
	lookup := &mockMemberLookup{}
	rbac := auth.NewRBAC(lookup)

	middleware := RBAC(RBACConfig{
		RBAC:       rbac,
		OrgIDParam: "orgId",
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/orgs/invalid/test", nil)
	principal := &auth.Principal{Subject: "test", Method: auth.MethodJWT}
	ctx := auth.ContextWithPrincipal(req.Context(), principal)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", "invalid")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)

	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestRequireRole(t *testing.T) {
	middleware := RequireRole(domain.RoleAdmin)

	t.Run("has role", func(t *testing.T) {
		authz := &auth.AuthzContext{
			Member: &domain.OrgMember{Role: domain.RoleAdmin},
			OrgID:  uuid.New(),
		}

		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		ctx := ContextWithAuthz(req.Context(), authz)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("lacks role", func(t *testing.T) {
		authz := &auth.AuthzContext{
			Member: &domain.OrgMember{Role: domain.RoleViewer},
			OrgID:  uuid.New(),
		}

		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		ctx := ContextWithAuthz(req.Context(), authz)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})
}

func TestRequirePermission(t *testing.T) {
	middleware := RequirePermission(domain.PermWriteServices)

	t.Run("has permission", func(t *testing.T) {
		authz := &auth.AuthzContext{
			Member: &domain.OrgMember{Role: domain.RoleAdmin},
			OrgID:  uuid.New(),
		}

		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		ctx := ContextWithAuthz(req.Context(), authz)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("lacks permission", func(t *testing.T) {
		authz := &auth.AuthzContext{
			Member: &domain.OrgMember{Role: domain.RoleViewer},
			OrgID:  uuid.New(),
		}

		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		ctx := ContextWithAuthz(req.Context(), authz)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})
}

func TestRequireMember(t *testing.T) {
	middleware := RequireMember()

	t.Run("is member", func(t *testing.T) {
		authz := &auth.AuthzContext{
			Member: &domain.OrgMember{},
			OrgID:  uuid.New(),
		}

		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		ctx := ContextWithAuthz(req.Context(), authz)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("not member", func(t *testing.T) {
		authz := &auth.AuthzContext{
			Member: nil,
			OrgID:  uuid.New(),
		}

		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("GET", "/test", nil)
		ctx := ContextWithAuthz(req.Context(), authz)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})
}

func TestGetAuthz_NoContext(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	authz := GetAuthz(req.Context())

	if authz != nil {
		t.Error("GetAuthz should return nil when no authz in context")
	}
}
