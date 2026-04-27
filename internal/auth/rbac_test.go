package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// mockMemberLookup is a test mock for OrgMemberLookup.
type mockMemberLookup struct {
	member   *domain.OrgMember
	members  []domain.OrgMember
	getErr   error
	listErr  error
}

func (m *mockMemberLookup) GetMember(ctx context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.member, nil
}

func (m *mockMemberLookup) ListByPubkey(ctx context.Context, pubkey string) ([]domain.OrgMember, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.members, nil
}

func TestAuthzContext_HasRole(t *testing.T) {
	tests := []struct {
		name     string
		member   *domain.OrgMember
		required domain.Role
		want     bool
	}{
		{"nil member", nil, domain.RoleViewer, false},
		{"viewer has viewer", &domain.OrgMember{Role: domain.RoleViewer}, domain.RoleViewer, true},
		{"viewer lacks admin", &domain.OrgMember{Role: domain.RoleViewer}, domain.RoleAdmin, false},
		{"owner has viewer", &domain.OrgMember{Role: domain.RoleOwner}, domain.RoleViewer, true},
		{"admin has deployer", &domain.OrgMember{Role: domain.RoleAdmin}, domain.RoleDeployer, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authz := &AuthzContext{Member: tt.member}
			if got := authz.HasRole(tt.required); got != tt.want {
				t.Errorf("HasRole(%s) = %v, want %v", tt.required, got, tt.want)
			}
		})
	}
}

func TestAuthzContext_HasPermission(t *testing.T) {
	tests := []struct {
		name   string
		role   domain.Role
		perm   domain.Permission
		want   bool
	}{
		{"viewer read services", domain.RoleViewer, domain.PermReadServices, true},
		{"viewer write services", domain.RoleViewer, domain.PermWriteServices, false},
		{"admin write services", domain.RoleAdmin, domain.PermWriteServices, true},
		{"owner manage members", domain.RoleOwner, domain.PermManageMembers, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authz := &AuthzContext{
				Member: &domain.OrgMember{Role: tt.role},
			}
			if got := authz.HasPermission(tt.perm); got != tt.want {
				t.Errorf("HasPermission(%s) = %v, want %v", tt.perm, got, tt.want)
			}
		})
	}
}

func TestAuthzContext_IsMember(t *testing.T) {
	tests := []struct {
		name   string
		member *domain.OrgMember
		want   bool
	}{
		{"nil member", nil, false},
		{"has member", &domain.OrgMember{Pubkey: "abc"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authz := &AuthzContext{Member: tt.member}
			if got := authz.IsMember(); got != tt.want {
				t.Errorf("IsMember() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthzContext_RequireRole(t *testing.T) {
	tests := []struct {
		name     string
		role     domain.Role
		required domain.Role
		wantErr  bool
	}{
		{"has role", domain.RoleAdmin, domain.RoleDeployer, false},
		{"lacks role", domain.RoleViewer, domain.RoleAdmin, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authz := &AuthzContext{
				Member: &domain.OrgMember{Role: tt.role},
				OrgID:  uuid.New(),
			}
			err := authz.RequireRole(tt.required)
			if (err != nil) != tt.wantErr {
				t.Errorf("RequireRole() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !IsAccessDenied(err) {
				t.Errorf("RequireRole() should return AccessDeniedError")
			}
		})
	}
}

func TestAuthzContext_RequireMember(t *testing.T) {
	tests := []struct {
		name    string
		member  *domain.OrgMember
		wantErr bool
	}{
		{"is member", &domain.OrgMember{}, false},
		{"not member", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authz := &AuthzContext{Member: tt.member, OrgID: uuid.New()}
			err := authz.RequireMember()
			if (err != nil) != tt.wantErr {
				t.Errorf("RequireMember() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRBAC_LoadAuthzContext(t *testing.T) {
	orgID := uuid.New()
	pubkey := "abc123"

	t.Run("authenticated member", func(t *testing.T) {
		member := &domain.OrgMember{
			OrgID:  orgID,
			Pubkey: pubkey,
			Role:   domain.RoleAdmin,
		}
		lookup := &mockMemberLookup{member: member}
		rbac := NewRBAC(lookup)

		principal := &Principal{
			Subject: "test",
			Method:  MethodNIP98,
			PubKey:  pubkey,
		}

		authz, err := rbac.LoadAuthzContext(context.Background(), principal, orgID)
		if err != nil {
			t.Fatalf("LoadAuthzContext() error = %v", err)
		}

		if authz.Member == nil {
			t.Error("authz.Member should not be nil")
		}
		if authz.Member.Role != domain.RoleAdmin {
			t.Errorf("authz.Member.Role = %s, want admin", authz.Member.Role)
		}
	})

	t.Run("unauthenticated", func(t *testing.T) {
		lookup := &mockMemberLookup{}
		rbac := NewRBAC(lookup)

		authz, err := rbac.LoadAuthzContext(context.Background(), nil, orgID)
		if err != nil {
			t.Fatalf("LoadAuthzContext() error = %v", err)
		}

		if authz.Member != nil {
			t.Error("authz.Member should be nil for unauthenticated")
		}
	})
}

func TestRBAC_CheckOrgAccess(t *testing.T) {
	orgID := uuid.New()
	pubkey := "abc123"

	t.Run("has access", func(t *testing.T) {
		member := &domain.OrgMember{
			OrgID:  orgID,
			Pubkey: pubkey,
			Role:   domain.RoleAdmin,
		}
		lookup := &mockMemberLookup{member: member}
		rbac := NewRBAC(lookup)

		principal := &Principal{
			Subject: "test",
			Method:  MethodNIP98,
			PubKey:  pubkey,
		}

		err := rbac.CheckOrgAccess(context.Background(), principal, orgID, domain.RoleDeployer)
		if err != nil {
			t.Errorf("CheckOrgAccess() error = %v, want nil", err)
		}
	})

	t.Run("lacks access", func(t *testing.T) {
		member := &domain.OrgMember{
			OrgID:  orgID,
			Pubkey: pubkey,
			Role:   domain.RoleViewer,
		}
		lookup := &mockMemberLookup{member: member}
		rbac := NewRBAC(lookup)

		principal := &Principal{
			Subject: "test",
			Method:  MethodNIP98,
			PubKey:  pubkey,
		}

		err := rbac.CheckOrgAccess(context.Background(), principal, orgID, domain.RoleAdmin)
		if err == nil {
			t.Error("CheckOrgAccess() should return error")
		}
		if !IsAccessDenied(err) {
			t.Errorf("CheckOrgAccess() should return AccessDeniedError, got %T", err)
		}
	})
}

func TestAccessDeniedError(t *testing.T) {
	orgID := uuid.New()
	err := &AccessDeniedError{
		Reason:   "not authorized",
		OrgID:    orgID,
		Required: "admin",
	}

	if !IsAccessDenied(err) {
		t.Error("IsAccessDenied should return true")
	}

	errStr := err.Error()
	if errStr == "" {
		t.Error("Error() should return non-empty string")
	}
}

func TestRBAC_GetUserOrgs(t *testing.T) {
	members := []domain.OrgMember{
		{OrgID: uuid.New(), Pubkey: "abc", Role: domain.RoleAdmin},
		{OrgID: uuid.New(), Pubkey: "abc", Role: domain.RoleViewer},
	}
	lookup := &mockMemberLookup{members: members}
	rbac := NewRBAC(lookup)

	result, err := rbac.GetUserOrgs(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetUserOrgs() error = %v", err)
	}

	if len(result) != 2 {
		t.Errorf("GetUserOrgs() returned %d orgs, want 2", len(result))
	}
}
