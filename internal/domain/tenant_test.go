package domain

import (
	"testing"
	"time"
)

func TestRoleWeight(t *testing.T) {
	tests := []struct {
		role   Role
		weight int
	}{
		{RoleViewer, 1},
		{RoleDeployer, 2},
		{RoleAdmin, 3},
		{RoleOwner, 4},
		{Role("invalid"), 0},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			if got := RoleWeight(tt.role); got != tt.weight {
				t.Errorf("RoleWeight(%s) = %d, want %d", tt.role, got, tt.weight)
			}
		})
	}
}

func TestHasAtLeastRole(t *testing.T) {
	tests := []struct {
		name string
		have Role
		need Role
		want bool
	}{
		{"viewer has viewer", RoleViewer, RoleViewer, true},
		{"viewer lacks deployer", RoleViewer, RoleDeployer, false},
		{"deployer has viewer", RoleDeployer, RoleViewer, true},
		{"deployer has deployer", RoleDeployer, RoleDeployer, true},
		{"deployer lacks admin", RoleDeployer, RoleAdmin, false},
		{"admin has admin", RoleAdmin, RoleAdmin, true},
		{"admin has deployer", RoleAdmin, RoleDeployer, true},
		{"admin lacks owner", RoleAdmin, RoleOwner, false},
		{"owner has everything", RoleOwner, RoleOwner, true},
		{"owner has admin", RoleOwner, RoleAdmin, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasAtLeastRole(tt.have, tt.need); got != tt.want {
				t.Errorf("HasAtLeastRole(%s, %s) = %v, want %v", tt.have, tt.need, got, tt.want)
			}
		})
	}
}

func TestRoleHasPermission(t *testing.T) {
	tests := []struct {
		role Role
		perm Permission
		want bool
	}{
		// Viewer
		{RoleViewer, PermReadServices, true},
		{RoleViewer, PermReadLogs, true},
		{RoleViewer, PermWriteServices, false},
		{RoleViewer, PermWriteDeployments, false},

		// Deployer
		{RoleDeployer, PermReadServices, true},
		{RoleDeployer, PermWriteDeployments, true},
		{RoleDeployer, PermApproveDeployments, true},
		{RoleDeployer, PermWriteServices, false},
		{RoleDeployer, PermReadSecrets, false},

		// Admin
		{RoleAdmin, PermWriteServices, true},
		{RoleAdmin, PermWriteSecrets, true},
		{RoleAdmin, PermReadSecrets, true},
		{RoleAdmin, PermManageMembers, false},

		// Owner
		{RoleOwner, PermManageMembers, true},
		{RoleOwner, PermManageSettings, true},
		{RoleOwner, PermWriteServices, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.role)+"_"+string(tt.perm), func(t *testing.T) {
			if got := RoleHasPermission(tt.role, tt.perm); got != tt.want {
				t.Errorf("RoleHasPermission(%s, %s) = %v, want %v", tt.role, tt.perm, got, tt.want)
			}
		})
	}
}

func TestAllRoles(t *testing.T) {
	roles := AllRoles()
	if len(roles) != 4 {
		t.Errorf("AllRoles() returned %d roles, want 4", len(roles))
	}

	// Verify order (ascending permission)
	for i := 0; i < len(roles)-1; i++ {
		if RoleWeight(roles[i]) >= RoleWeight(roles[i+1]) {
			t.Errorf("roles not in ascending order: %s >= %s", roles[i], roles[i+1])
		}
	}
}

func TestOrgInvite_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"future", time.Now().Add(time.Hour), false},
		{"past", time.Now().Add(-time.Hour), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := &OrgInvite{ExpiresAt: tt.expiresAt}
			if got := inv.IsExpired(); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
