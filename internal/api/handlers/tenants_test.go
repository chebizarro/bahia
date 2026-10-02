package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type testOrgRepo struct {
	created []*domain.Organization
	org     *domain.Organization
}

func (r *testOrgRepo) Create(_ context.Context, org *domain.Organization) error {
	r.created = append(r.created, org)
	return nil
}
func (r *testOrgRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	if r.org == nil || r.org.ID != id {
		return nil, repository.ErrNotFound
	}
	copy := *r.org
	return &copy, nil
}
func (r *testOrgRepo) GetByName(_ context.Context, name string) (*domain.Organization, error) {
	if r.org == nil || r.org.Name != name {
		return nil, repository.ErrNotFound
	}
	copy := *r.org
	return &copy, nil
}
func (r *testOrgRepo) List(context.Context) ([]domain.Organization, error) { return nil, nil }
func (r *testOrgRepo) Update(context.Context, *domain.Organization) error  { return nil }
func (r *testOrgRepo) Delete(context.Context, uuid.UUID) error             { return nil }

type testMemberRepo struct {
	added  []*domain.OrgMember
	member *domain.OrgMember
}

func (r *testMemberRepo) Add(_ context.Context, member *domain.OrgMember) error {
	r.added = append(r.added, member)
	return nil
}
func (r *testMemberRepo) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	if r.member == nil || r.member.OrgID != orgID || r.member.Pubkey != pubkey {
		return nil, repository.ErrNotFound
	}
	copy := *r.member
	return &copy, nil
}

func TestGetOrgRequiresMembershipInRequestedOrganization(t *testing.T) {
	org := &domain.Organization{ID: uuid.New(), Name: "private-org"}
	pubkey := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name       string
		membership *domain.OrgMember
		wantStatus int
	}{
		{name: "unknown signed principal", wantStatus: http.StatusForbidden},
		{name: "member", membership: &domain.OrgMember{OrgID: org.ID, Pubkey: pubkey, Role: domain.RoleViewer}, wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orgs := &testOrgRepo{org: org}
			members := &testMemberRepo{member: tc.membership}
			h := NewTenantHandler(orgs, members, &testInviteRepo{}, auth.NewRBAC(members), nil, zap.NewNop())
			req := httptest.NewRequest(http.MethodGet, "/orgs/"+org.ID.String(), nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", org.ID.String())
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{Method: auth.MethodNIP98, PubKey: pubkey}))
			w := httptest.NewRecorder()

			h.GetOrg(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}
func (r *testMemberRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.OrgMember, error) {
	return nil, nil
}
func (r *testMemberRepo) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return nil, nil
}
func (r *testMemberRepo) UpdateRole(context.Context, uuid.UUID, string, domain.Role) error {
	return nil
}
func (r *testMemberRepo) Remove(context.Context, uuid.UUID, string) error { return nil }

type testInviteRepo struct {
	lookupOrgID    uuid.UUID
	lookupInviteID uuid.UUID
	invite         *domain.OrgInvite
	deleteErr      error
}

func (r *testInviteRepo) Create(context.Context, *domain.OrgInvite) error { return nil }
func (r *testInviteRepo) GetByID(_ context.Context, orgID, inviteID uuid.UUID) (*domain.OrgInvite, error) {
	r.lookupOrgID = orgID
	r.lookupInviteID = inviteID
	if r.invite == nil || r.invite.OrgID != orgID || r.invite.ID != inviteID {
		return nil, repository.ErrNotFound
	}
	copy := *r.invite
	return &copy, nil
}
func (r *testInviteRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.OrgInvite, error) {
	return nil, nil
}
func (r *testInviteRepo) ListByPubkey(context.Context, string) ([]domain.OrgInvite, error) {
	return nil, nil
}
func (r *testInviteRepo) Delete(context.Context, uuid.UUID) error    { return r.deleteErr }
func (r *testInviteRepo) DeleteExpired(context.Context) (int, error) { return 0, nil }

func TestAcceptInviteScopesLookupByOrganization(t *testing.T) {
	orgID := uuid.New()
	inviteID := uuid.New()
	pubkey := strings.Repeat("a", 64)
	invites := &testInviteRepo{invite: &domain.OrgInvite{
		ID: inviteID, OrgID: orgID, Pubkey: pubkey, Role: domain.RoleViewer, ExpiresAt: time.Now().Add(time.Hour),
	}}
	h := NewTenantHandler(&testOrgRepo{}, &testMemberRepo{}, invites, nil, nil, zap.NewNop())

	req := httptest.NewRequest(http.MethodPost, "/invites/"+inviteID.String()+"/accept?org_id="+orgID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", inviteID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{Method: auth.MethodNIP98, PubKey: pubkey}))
	w := httptest.NewRecorder()

	h.AcceptInvite(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if invites.lookupOrgID != orgID || invites.lookupInviteID != inviteID {
		t.Fatalf("lookup = (%s, %s), want (%s, %s)", invites.lookupOrgID, invites.lookupInviteID, orgID, inviteID)
	}
}

func TestAcceptInviteReportsInviteDeletionFailure(t *testing.T) {
	orgID := uuid.New()
	inviteID := uuid.New()
	pubkey := strings.Repeat("a", 64)
	invites := &testInviteRepo{
		invite:    &domain.OrgInvite{ID: inviteID, OrgID: orgID, Pubkey: pubkey, Role: domain.RoleViewer, ExpiresAt: time.Now().Add(time.Hour)},
		deleteErr: errors.New("delete failed"),
	}
	h := NewTenantHandler(&testOrgRepo{}, &testMemberRepo{}, invites, nil, nil, zap.NewNop())
	req := httptest.NewRequest(http.MethodPost, "/invites/"+inviteID.String()+"/accept?org_id="+orgID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", inviteID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{Method: auth.MethodNIP98, PubKey: pubkey}))
	w := httptest.NewRecorder()

	h.AcceptInvite(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

var _ repository.OrganizationRepository = (*testOrgRepo)(nil)
var _ repository.OrgMemberRepository = (*testMemberRepo)(nil)
var _ repository.OrgInviteRepository = (*testInviteRepo)(nil)
