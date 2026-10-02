package controlplane

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestTrustSet_ConfigBootstrapOwner(t *testing.T) {
	orgID := uuid.New()
	ownerPK := "aaaa000000000000000000000000000000000000000000000000000000000001"

	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{orgID.String(): ownerPK}),
	)

	ctx := context.Background()

	// Bootstrap owner has owner-level permissions.
	assert.True(t, ts.HasPermission(ctx, orgID, ownerPK, domain.PermWriteServices))
	assert.True(t, ts.HasPermission(ctx, orgID, ownerPK, domain.PermManageMembers))

	// Unknown pubkey has no permissions.
	unknownPK := "bbbb000000000000000000000000000000000000000000000000000000000002"
	assert.False(t, ts.HasPermission(ctx, orgID, unknownPK, domain.PermWriteServices))

	// Bootstrap owner in wrong org has no permissions.
	otherOrgID := uuid.New()
	assert.False(t, ts.HasPermission(ctx, otherOrgID, ownerPK, domain.PermWriteServices))
}

func TestTrustSet_FleetOpsAreNotOrgMembers(t *testing.T) {
	orgID := uuid.New()
	fleetPK := "cccc000000000000000000000000000000000000000000000000000000000003"

	ts := NewTrustSet([]string{fleetPK}, zap.NewNop())

	ctx := context.Background()

	// Fleet operators cannot authorize org-scoped mutations.
	assert.False(t, ts.HasPermission(ctx, orgID, fleetPK, domain.PermWriteServices))

	// But they are known principals.
	assert.True(t, ts.IsKnownPrincipal(fleetPK))
}

func TestTrustSet_PostgresSource(t *testing.T) {
	orgID := uuid.New()
	memberPK := "dddd000000000000000000000000000000000000000000000000000000000004"

	members := &mockOrgMemberLookup{
		members: map[string]*domain.OrgMember{
			memberKey(orgID, memberPK): {
				OrgID:  orgID,
				Pubkey: memberPK,
				Role:   domain.RoleAdmin,
			},
		},
	}
	rbac := auth.NewRBAC(members)

	ts := NewTrustSet(nil, zap.NewNop(), WithPostgresRBAC(rbac))
	ctx := context.Background()

	// Admin has write permission.
	assert.True(t, ts.HasPermission(ctx, orgID, memberPK, domain.PermWriteServices))
	// Admin lacks manage-members permission.
	assert.False(t, ts.HasPermission(ctx, orgID, memberPK, domain.PermManageMembers))

	// Viewer role in separate test.
	viewerPK := "eeee000000000000000000000000000000000000000000000000000000000005"
	members.members[memberKey(orgID, viewerPK)] = &domain.OrgMember{
		OrgID:  orgID,
		Pubkey: viewerPK,
		Role:   domain.RoleViewer,
	}
	assert.False(t, ts.HasPermission(ctx, orgID, viewerPK, domain.PermWriteServices))
	assert.True(t, ts.HasPermission(ctx, orgID, viewerPK, domain.PermReadServices))
}

func TestTrustSet_RelayMembersOverridePostgres(t *testing.T) {
	orgID := uuid.New()
	memberPK := "ffff000000000000000000000000000000000000000000000000000000000006"

	// Postgres says viewer.
	members := &mockOrgMemberLookup{
		members: map[string]*domain.OrgMember{
			memberKey(orgID, memberPK): {
				OrgID:  orgID,
				Pubkey: memberPK,
				Role:   domain.RoleViewer,
			},
		},
	}
	rbac := auth.NewRBAC(members)

	ts := NewTrustSet(nil, zap.NewNop(), WithPostgresRBAC(rbac))
	ctx := context.Background()

	// With Postgres only, viewer cannot write.
	assert.False(t, ts.HasPermission(ctx, orgID, memberPK, domain.PermWriteServices))

	// Set relay members with admin role.
	ts.SetRelayMembers(orgID.String(), map[string]domain.Role{
		memberPK: domain.RoleAdmin,
	})

	// Now relay source wins: admin can write.
	assert.True(t, ts.HasPermission(ctx, orgID, memberPK, domain.PermWriteServices))
}

func TestTrustSet_RelayMembersOverrideBootstrapOwner(t *testing.T) {
	orgID := uuid.New()
	ownerPK := "1111000000000000000000000000000000000000000000000000000000000007"
	memberPK := "2222000000000000000000000000000000000000000000000000000000000008"

	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{orgID.String(): ownerPK}),
	)
	ctx := context.Background()

	// Before relay events, bootstrap owner has full permissions.
	assert.True(t, ts.HasPermission(ctx, orgID, ownerPK, domain.PermManageMembers))

	// Set relay members that don't include the bootstrap owner.
	ts.SetRelayMembers(orgID.String(), map[string]domain.Role{
		memberPK: domain.RoleAdmin,
	})

	// Relay source wins: bootstrap owner no longer has permissions.
	assert.False(t, ts.HasPermission(ctx, orgID, ownerPK, domain.PermManageMembers))
	// But the relay member does.
	assert.True(t, ts.HasPermission(ctx, orgID, memberPK, domain.PermWriteServices))
}

func TestTrustSet_AuthorPubkeys(t *testing.T) {
	orgID := uuid.New()
	fleetPK := "3333000000000000000000000000000000000000000000000000000000000009"
	ownerPK := "444400000000000000000000000000000000000000000000000000000000000a"
	relayPK := "555500000000000000000000000000000000000000000000000000000000000b"

	ts := NewTrustSet([]string{fleetPK}, zap.NewNop(),
		WithBootstrapOwners(map[string]string{orgID.String(): ownerPK}),
	)
	ts.SetRelayMembers(orgID.String(), map[string]domain.Role{
		relayPK: domain.RoleAdmin,
	})

	pubkeys := ts.AuthorPubkeys()
	require.Contains(t, pubkeys, fleetPK)
	require.Contains(t, pubkeys, ownerPK)
	require.Contains(t, pubkeys, relayPK)

	// No duplicates.
	seen := make(map[string]bool)
	for _, pk := range pubkeys {
		require.False(t, seen[pk], "duplicate pubkey: %s", pk)
		seen[pk] = true
	}
}

func TestTrustSet_IsKnownPrincipal(t *testing.T) {
	orgID := uuid.New()
	fleetPK := "666600000000000000000000000000000000000000000000000000000000000c"
	ownerPK := "777700000000000000000000000000000000000000000000000000000000000d"
	unknownPK := "888800000000000000000000000000000000000000000000000000000000000e"

	ts := NewTrustSet([]string{fleetPK}, zap.NewNop(),
		WithBootstrapOwners(map[string]string{orgID.String(): ownerPK}),
	)

	assert.True(t, ts.IsKnownPrincipal(fleetPK))
	assert.True(t, ts.IsKnownPrincipal(ownerPK))
	assert.False(t, ts.IsKnownPrincipal(unknownPK))
}

func TestTrustSet_RoleFor(t *testing.T) {
	orgID := uuid.New()
	ownerPK := "999900000000000000000000000000000000000000000000000000000000000f"

	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{orgID.String(): ownerPK}),
	)
	ctx := context.Background()

	assert.Equal(t, domain.RoleOwner, ts.RoleFor(ctx, orgID, ownerPK))

	unknownPK := "aaaa000000000000000000000000000000000000000000000000000000000010"
	assert.Equal(t, domain.Role(""), ts.RoleFor(ctx, orgID, unknownPK))
}

// --- test helpers ---

// memberKey is defined in encrypted_route_handlers_test.go.

type mockOrgMemberLookup struct {
	members map[string]*domain.OrgMember
}

func (m *mockOrgMemberLookup) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	member, ok := m.members[memberKey(orgID, pubkey)]
	if !ok {
		return nil, &auth.AccessDeniedError{Reason: "not a member"}
	}
	return member, nil
}

func (m *mockOrgMemberLookup) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgMember, error) {
	var result []domain.OrgMember
	for _, member := range m.members {
		if member.Pubkey == pubkey {
			result = append(result, *member)
		}
	}
	return result, nil
}
