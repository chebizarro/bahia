// Package auth provides authentication and authorization.
package auth

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// OrgMemberLookup is an interface for looking up organization memberships.
type OrgMemberLookup interface {
	GetMember(ctx context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error)
	ListByPubkey(ctx context.Context, pubkey string) ([]domain.OrgMember, error)
}

// RBAC provides role-based access control for organization resources.
type RBAC struct {
	members OrgMemberLookup
}

// NewRBAC creates a new RBAC instance.
func NewRBAC(members OrgMemberLookup) *RBAC {
	return &RBAC{members: members}
}

// AuthzContext contains authorization context for a request.
type AuthzContext struct {
	// Principal is the authenticated user.
	Principal *Principal
	// OrgID is the organization being accessed.
	OrgID uuid.UUID
	// Member is the user's membership in the organization (nil if not a member).
	Member *domain.OrgMember
}

// LoadAuthzContext loads the authorization context for a principal and org.
func (r *RBAC) LoadAuthzContext(ctx context.Context, p *Principal, orgID uuid.UUID) (*AuthzContext, error) {
	if p == nil || !p.IsAuthenticated() {
		return &AuthzContext{Principal: p, OrgID: orgID}, nil
	}

	member, err := r.members.GetMember(ctx, orgID, p.PubKey)
	if err != nil {
		// Not a member - that's okay, member will be nil
		return &AuthzContext{
			Principal: p,
			OrgID:     orgID,
		}, nil
	}

	return &AuthzContext{
		Principal: p,
		OrgID:     orgID,
		Member:    member,
	}, nil
}

// IsMember returns true if the principal is a member of the organization.
func (a *AuthzContext) IsMember() bool {
	return a.Member != nil
}

// HasRole returns true if the principal has at least the given role.
func (a *AuthzContext) HasRole(required domain.Role) bool {
	if a.Member == nil {
		return false
	}
	return domain.HasAtLeastRole(a.Member.Role, required)
}

// HasPermission returns true if the principal has the given permission.
func (a *AuthzContext) HasPermission(perm domain.Permission) bool {
	if a.Member == nil {
		return false
	}
	return domain.RoleHasPermission(a.Member.Role, perm)
}

// RequireRole returns an error if the principal doesn't have the required role.
func (a *AuthzContext) RequireRole(required domain.Role) error {
	if !a.HasRole(required) {
		return &AccessDeniedError{
			Reason:   fmt.Sprintf("requires %s role", required),
			OrgID:    a.OrgID,
			Required: string(required),
		}
	}
	return nil
}

// RequirePermission returns an error if the principal doesn't have the required permission.
func (a *AuthzContext) RequirePermission(perm domain.Permission) error {
	if !a.HasPermission(perm) {
		return &AccessDeniedError{
			Reason:   fmt.Sprintf("requires %s permission", perm),
			OrgID:    a.OrgID,
			Required: string(perm),
		}
	}
	return nil
}

// RequireMember returns an error if the principal is not a member.
func (a *AuthzContext) RequireMember() error {
	if !a.IsMember() {
		return &AccessDeniedError{
			Reason: "not a member of this organization",
			OrgID:  a.OrgID,
		}
	}
	return nil
}

// IsOwner returns true if the principal is the organization owner.
func (a *AuthzContext) IsOwner() bool {
	return a.Member != nil && a.Member.Role == domain.RoleOwner
}

// AccessDeniedError represents an authorization failure.
type AccessDeniedError struct {
	Reason   string
	OrgID    uuid.UUID
	Required string
}

func (e *AccessDeniedError) Error() string {
	if e.Required != "" {
		return fmt.Sprintf("access denied: %s (org: %s, required: %s)", e.Reason, e.OrgID, e.Required)
	}
	return fmt.Sprintf("access denied: %s (org: %s)", e.Reason, e.OrgID)
}

// IsAccessDenied returns true if the error is an access denied error.
func IsAccessDenied(err error) bool {
	_, ok := err.(*AccessDeniedError)
	return ok
}

// CheckOrgAccess is a convenience function to check org membership and role.
func (r *RBAC) CheckOrgAccess(ctx context.Context, p *Principal, orgID uuid.UUID, requiredRole domain.Role) error {
	authz, err := r.LoadAuthzContext(ctx, p, orgID)
	if err != nil {
		return fmt.Errorf("loading auth context: %w", err)
	}

	if err := authz.RequireMember(); err != nil {
		return err
	}

	return authz.RequireRole(requiredRole)
}

// CheckPermission is a convenience function to check a specific permission.
func (r *RBAC) CheckPermission(ctx context.Context, p *Principal, orgID uuid.UUID, perm domain.Permission) error {
	authz, err := r.LoadAuthzContext(ctx, p, orgID)
	if err != nil {
		return fmt.Errorf("loading auth context: %w", err)
	}

	if err := authz.RequireMember(); err != nil {
		return err
	}

	return authz.RequirePermission(perm)
}

// GetUserOrgs returns all organizations the user is a member of.
func (r *RBAC) GetUserOrgs(ctx context.Context, pubkey string) ([]domain.OrgMember, error) {
	return r.members.ListByPubkey(ctx, pubkey)
}
