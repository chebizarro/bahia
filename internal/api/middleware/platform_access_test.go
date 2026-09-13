package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

type platformMemberLookup struct {
	memberships []domain.OrgMember
	err         error
}

func (m platformMemberLookup) GetMember(context.Context, uuid.UUID, string) (*domain.OrgMember, error) {
	return nil, errors.New("unused")
}

func (m platformMemberLookup) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return m.memberships, m.err
}

func platformRequest(pubkey string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	if pubkey == "" {
		return req
	}
	principal := &auth.Principal{Subject: pubkey, PubKey: pubkey, Method: auth.MethodNIP98}
	return req.WithContext(auth.ContextWithPrincipal(req.Context(), principal))
}

func TestPlatformAccessRejectsUnknownSignedPrincipal(t *testing.T) {
	gate := PlatformAccess(PlatformAccessConfig{RBAC: auth.NewRBAC(platformMemberLookup{})})
	w := httptest.NewRecorder()
	gate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") })).ServeHTTP(w, platformRequest("unknown"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestPlatformAccessAllowsMemberAndBootstrapOwner(t *testing.T) {
	member := domain.OrgMember{OrgID: uuid.New(), Pubkey: "member", Role: domain.RoleViewer}
	for _, tc := range []struct {
		name   string
		gate   func(http.Handler) http.Handler
		pubkey string
	}{
		{name: "member", gate: PlatformAccess(PlatformAccessConfig{RBAC: auth.NewRBAC(platformMemberLookup{memberships: []domain.OrgMember{member}})}), pubkey: "member"},
		{name: "bootstrap owner", gate: PlatformAccess(PlatformAccessConfig{RBAC: auth.NewRBAC(platformMemberLookup{}), BootstrapOwnerPubkeys: []string{"OWNER"}}), pubkey: "owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.gate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, platformRequest(tc.pubkey))
			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
			}
		})
	}
}

func TestPlatformAccessFailsClosedOnLookupError(t *testing.T) {
	gate := PlatformAccess(PlatformAccessConfig{RBAC: auth.NewRBAC(platformMemberLookup{err: errors.New("database unavailable")})})
	w := httptest.NewRecorder()
	gate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") })).ServeHTTP(w, platformRequest("member"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestPlatformAccessEnforcesMinimumRole(t *testing.T) {
	viewer := domain.OrgMember{OrgID: uuid.New(), Pubkey: "viewer", Role: domain.RoleViewer}
	gate := PlatformAccess(PlatformAccessConfig{
		RBAC:        auth.NewRBAC(platformMemberLookup{memberships: []domain.OrgMember{viewer}}),
		MinimumRole: domain.RoleAdmin,
	})
	w := httptest.NewRecorder()
	gate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") })).ServeHTTP(w, platformRequest("viewer"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestPlatformAccessAllowsExplicitUnaffiliatedOnboardingRoute(t *testing.T) {
	gate := PlatformAccess(PlatformAccessConfig{
		RBAC:              auth.NewRBAC(platformMemberLookup{}),
		AllowUnaffiliated: func(r *http.Request) bool { return r.URL.Path == "/api/v1/me/invites" },
	})
	req := platformRequest("invitee")
	req.URL.Path = "/api/v1/me/invites"
	w := httptest.NewRecorder()
	gate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}
