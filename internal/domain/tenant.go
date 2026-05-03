// Package domain contains core business types.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Role represents a user's access level within an organization.
type Role string

const (
	// RoleViewer can read services, environments, deployments, and logs.
	RoleViewer Role = "viewer"
	// RoleDeployer can create/approve deployments in addition to viewer permissions.
	RoleDeployer Role = "deployer"
	// RoleAdmin can CRUD services, environments, secrets in addition to deployer permissions.
	RoleAdmin Role = "admin"
	// RoleOwner has full control including org settings and member management.
	RoleOwner Role = "owner"
)

// AllRoles returns all valid roles in ascending permission order.
func AllRoles() []Role {
	return []Role{RoleViewer, RoleDeployer, RoleAdmin, RoleOwner}
}

// RoleWeight returns the numeric weight of a role for comparison.
// Higher weight means more permissions.
func RoleWeight(r Role) int {
	switch r {
	case RoleViewer:
		return 1
	case RoleDeployer:
		return 2
	case RoleAdmin:
		return 3
	case RoleOwner:
		return 4
	default:
		return 0
	}
}

// HasAtLeastRole checks if role `have` is at least as powerful as `need`.
func HasAtLeastRole(have, need Role) bool {
	return RoleWeight(have) >= RoleWeight(need)
}

// Organization represents a multi-tenant organization.
type Organization struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`         // Unique slug (lowercase, no spaces)
	DisplayName string    `json:"display_name"` // Human-readable name
	OwnerPubkey string    `json:"owner_pubkey"` // Canonical lowercase hex Nostr pubkey of the owner
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// OrgMember represents a user's membership in an organization.
type OrgMember struct {
	OrgID     uuid.UUID `json:"org_id"`
	Pubkey    string    `json:"pubkey"`     // Canonical lowercase hex Nostr pubkey
	Role      Role      `json:"role"`       // viewer, deployer, admin, owner
	NIP05     string    `json:"nip05"`      // Resolved NIP-05 identifier (cached)
	JoinedAt  time.Time `json:"joined_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OrgInvite represents a pending invitation to join an organization.
type OrgInvite struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	Pubkey    string    `json:"pubkey"`    // Invitee's canonical lowercase hex Nostr pubkey
	Role      Role      `json:"role"`      // Role to grant upon acceptance
	InvitedBy string    `json:"invited_by"` // Inviter's canonical lowercase hex Nostr pubkey
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// IsExpired returns true if the invitation has expired.
func (i *OrgInvite) IsExpired() bool {
	return time.Now().After(i.ExpiresAt)
}

// Permission represents a specific action that can be performed.
type Permission string

const (
	// Read permissions
	PermReadServices     Permission = "services:read"
	PermReadEnvironments Permission = "environments:read"
	PermReadDeployments  Permission = "deployments:read"
	PermReadLogs         Permission = "logs:read"
	PermReadSecrets      Permission = "secrets:read"

	// Write permissions
	PermWriteServices     Permission = "services:write"
	PermWriteEnvironments Permission = "environments:write"
	PermWriteDeployments  Permission = "deployments:write"
	PermWriteSecrets      Permission = "secrets:write"
	PermApproveDeployments Permission = "deployments:approve"

	// Admin permissions
	PermManageMembers Permission = "members:manage"
	PermManageSettings Permission = "settings:manage"
)

// RolePermissions maps roles to their granted permissions.
var RolePermissions = map[Role][]Permission{
	RoleViewer: {
		PermReadServices,
		PermReadEnvironments,
		PermReadDeployments,
		PermReadLogs,
	},
	RoleDeployer: {
		PermReadServices,
		PermReadEnvironments,
		PermReadDeployments,
		PermReadLogs,
		PermWriteDeployments,
		PermApproveDeployments,
	},
	RoleAdmin: {
		PermReadServices,
		PermReadEnvironments,
		PermReadDeployments,
		PermReadLogs,
		PermReadSecrets,
		PermWriteServices,
		PermWriteEnvironments,
		PermWriteDeployments,
		PermWriteSecrets,
		PermApproveDeployments,
	},
	RoleOwner: {
		PermReadServices,
		PermReadEnvironments,
		PermReadDeployments,
		PermReadLogs,
		PermReadSecrets,
		PermWriteServices,
		PermWriteEnvironments,
		PermWriteDeployments,
		PermWriteSecrets,
		PermApproveDeployments,
		PermManageMembers,
		PermManageSettings,
	},
}

// RoleHasPermission checks if a role has a specific permission.
func RoleHasPermission(role Role, perm Permission) bool {
	perms, ok := RolePermissions[role]
	if !ok {
		return false
	}
	for _, p := range perms {
		if p == perm {
			return true
		}
	}
	return false
}
