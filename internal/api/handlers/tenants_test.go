package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type testOrgRepo struct {
	created []*domain.Organization
}

func (r *testOrgRepo) Create(_ context.Context, org *domain.Organization) error {
	r.created = append(r.created, org)
	return nil
}
func (r *testOrgRepo) GetByID(context.Context, uuid.UUID) (*domain.Organization, error) {
	return nil, repository.ErrNotFound
}
func (r *testOrgRepo) GetByName(context.Context, string) (*domain.Organization, error) {
	return nil, repository.ErrNotFound
}
func (r *testOrgRepo) List(context.Context) ([]domain.Organization, error) { return nil, nil }
func (r *testOrgRepo) Update(context.Context, *domain.Organization) error  { return nil }
func (r *testOrgRepo) Delete(context.Context, uuid.UUID) error             { return nil }

type testMemberRepo struct {
	added []*domain.OrgMember
}

func (r *testMemberRepo) Add(_ context.Context, member *domain.OrgMember) error {
	r.added = append(r.added, member)
	return nil
}
func (r *testMemberRepo) GetMember(context.Context, uuid.UUID, string) (*domain.OrgMember, error) {
	return nil, repository.ErrNotFound
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

type testInviteRepo struct{}

func (r *testInviteRepo) Create(context.Context, *domain.OrgInvite) error { return nil }
func (r *testInviteRepo) GetByID(context.Context, uuid.UUID) (*domain.OrgInvite, error) {
	return nil, repository.ErrNotFound
}
func (r *testInviteRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.OrgInvite, error) {
	return nil, nil
}
func (r *testInviteRepo) ListByPubkey(context.Context, string) ([]domain.OrgInvite, error) {
	return nil, nil
}
func (r *testInviteRepo) Delete(context.Context, uuid.UUID) error    { return nil }
func (r *testInviteRepo) DeleteExpired(context.Context) (int, error) { return 0, nil }

func TestCreateOrgBootstrapOwnerAllowlist(t *testing.T) {
	allowedPubkey := strings.Repeat("a", 64)
	notAllowedPubkey := strings.Repeat("b", 64)

	tests := []struct {
		name               string
		allowlist          []string
		principal          *auth.Principal
		wantStatus         int
		wantCreatesOrg     bool
		wantAddsMembership bool
	}{
		{
			name:               "empty allowlist preserves behavior",
			allowlist:          nil,
			principal:          &auth.Principal{Method: auth.MethodNIP98, PubKey: notAllowedPubkey},
			wantStatus:         http.StatusCreated,
			wantCreatesOrg:     true,
			wantAddsMembership: true,
		},
		{
			name:               "listed principal can create org",
			allowlist:          []string{allowedPubkey},
			principal:          &auth.Principal{Method: auth.MethodNIP98, PubKey: allowedPubkey},
			wantStatus:         http.StatusCreated,
			wantCreatesOrg:     true,
			wantAddsMembership: true,
		},
		{
			name:               "unlisted principal gets forbidden",
			allowlist:          []string{allowedPubkey},
			principal:          &auth.Principal{Method: auth.MethodNIP98, PubKey: notAllowedPubkey},
			wantStatus:         http.StatusForbidden,
			wantCreatesOrg:     false,
			wantAddsMembership: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orgs := &testOrgRepo{}
			members := &testMemberRepo{}
			h := NewTenantHandler(orgs, members, &testInviteRepo{}, nil, tt.allowlist, zap.NewNop())

			body := strings.NewReader(`{"name":"demo-org","display_name":"Demo"}`)
			req := httptest.NewRequest(http.MethodPost, "/orgs", body)
			if tt.principal != nil {
				req = req.WithContext(auth.ContextWithPrincipal(req.Context(), tt.principal))
			}
			w := httptest.NewRecorder()

			h.CreateOrg(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := len(orgs.created) > 0; got != tt.wantCreatesOrg {
				t.Fatalf("org created = %v, want %v", got, tt.wantCreatesOrg)
			}
			if got := len(members.added) > 0; got != tt.wantAddsMembership {
				t.Fatalf("owner membership added = %v, want %v", got, tt.wantAddsMembership)
			}
		})
	}
}

func TestCreateOrgBootstrapOwnerAllowlistForbiddenMessage(t *testing.T) {
	allowedPubkey := strings.Repeat("a", 64)
	notAllowedPubkey := strings.Repeat("b", 64)
	h := NewTenantHandler(&testOrgRepo{}, &testMemberRepo{}, &testInviteRepo{}, nil, []string{allowedPubkey}, zap.NewNop())

	req := httptest.NewRequest(http.MethodPost, "/orgs", strings.NewReader(`{"name":"demo-org"}`))
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{Method: auth.MethodNIP98, PubKey: notAllowedPubkey}))
	w := httptest.NewRecorder()

	h.CreateOrg(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errText, _ := resp["error"].(string)
	if !strings.Contains(errText, "bootstrap owner pubkey") {
		t.Fatalf("error message = %q, want bootstrap owner pubkey message", errText)
	}
}

var _ repository.OrganizationRepository = (*testOrgRepo)(nil)
var _ repository.OrgMemberRepository = (*testMemberRepo)(nil)
var _ repository.OrgInviteRepository = (*testInviteRepo)(nil)
