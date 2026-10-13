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

func TestTrustSet_PostgresRowsDoNotAuthorizeIntents(t *testing.T) {
	orgID := uuid.New()
	memberPK := "dddd000000000000000000000000000000000000000000000000000000000004"
	members := &mockOrgMemberLookup{
		members: map[string]*domain.OrgMember{
			memberKey(orgID, memberPK): {
				OrgID: orgID, Pubkey: memberPK, Role: domain.RoleAdmin,
			},
		},
	}
	ctx := context.Background()
	// The derived PostgreSQL RBAC view grants this permission, but TrustSet
	// must not consume it as intent authority.
	rbac := auth.NewRBAC(members)
	require.NoError(t, rbac.CheckPermission(ctx,
		&auth.Principal{PubKey: memberPK, Method: auth.MethodNIP98}, orgID, domain.PermWriteServices))
	ts := NewTrustSet(nil, zap.NewNop())
	assert.False(t, ts.HasPermission(ctx, orgID, memberPK, domain.PermWriteServices))
	assert.Empty(t, ts.RoleFor(ctx, orgID, memberPK))
	assert.False(t, ts.IsKnownPrincipal(memberPK))
	assert.NotContains(t, ts.AuthorPubkeys(), memberPK)
	handler := &testDomainHandler{}
	proc := NewIntentProcessor(ts, openTestStore(t), nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true}}, zap.NewNop())
	proc.RegisterHandler("test", handler)
	intent := testIntent(t, "create", "pg-only-member")
	intent.OrgID = orgID
	intent.Actor = memberPK
	require.ErrorContains(t, proc.ProcessInProcess(ctx, intent), "untrusted intent actor")
	assert.Empty(t, handler.handled)

	// A canonical membership record, not the SQL row, changes authority.
	ts.SetRelayMembers(orgID.String(), map[string]domain.Role{memberPK: domain.RoleAdmin})
	assert.True(t, ts.HasPermission(ctx, orgID, memberPK, domain.PermWriteServices))
	assert.Equal(t, domain.RoleAdmin, ts.RoleFor(ctx, orgID, memberPK))
	require.NoError(t, proc.ProcessInProcess(ctx, intent))
	assert.Len(t, handler.handled, 1)
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

func TestTrustSetMemberSource_FleetScopeReturnsFleetOps(t *testing.T) {
	fleetPK := "aaaa000000000000000000000000000000000000000000000000000000000001"
	ownerPK := "bbbb000000000000000000000000000000000000000000000000000000000002"
	orgID := uuid.New()

	ts := NewTrustSet([]string{fleetPK}, zap.NewNop(),
		WithBootstrapOwners(map[string]string{orgID.String(): ownerPK}),
	)

	source := NewTrustSetMemberSource(ts, nil)
	ctx := context.Background()

	pubkeys, err := source.OrgMemberPubkeys(ctx, "fleet")
	require.NoError(t, err)
	require.Contains(t, pubkeys, fleetPK, "fleet-ops pubkey should be included")
	require.Contains(t, pubkeys, ownerPK, "bootstrap owner should be included")
}

func TestTrustSetMemberSource_FleetScopeExcludesRandomPubkey(t *testing.T) {
	fleetPK := "cccc000000000000000000000000000000000000000000000000000000000003"
	randomPK := "dddd000000000000000000000000000000000000000000000000000000000004"

	ts := NewTrustSet([]string{fleetPK}, zap.NewNop())

	source := NewTrustSetMemberSource(ts, nil)
	ctx := context.Background()

	pubkeys, err := source.OrgMemberPubkeys(ctx, "fleet")
	require.NoError(t, err)
	require.Contains(t, pubkeys, fleetPK)
	require.NotContains(t, pubkeys, randomPK, "random pubkey should not be in fleet OCK set")
}

func TestTrustSetMemberSource_FleetScopeEmptyWhenNoOps(t *testing.T) {
	ts := NewTrustSet(nil, zap.NewNop())
	source := NewTrustSetMemberSource(ts, nil)
	ctx := context.Background()

	pubkeys, err := source.OrgMemberPubkeys(ctx, "fleet")
	require.NoError(t, err)
	require.Empty(t, pubkeys, "no fleet ops configured → empty set")
}

func TestTrustSetMemberSource_OrgScopeStillWorks(t *testing.T) {
	orgID := uuid.New()
	memberPK := "eeee000000000000000000000000000000000000000000000000000000000005"
	fleetPK := "ffff000000000000000000000000000000000000000000000000000000000006"

	ts := NewTrustSet([]string{fleetPK}, zap.NewNop())
	ts.SetRelayMembers(orgID.String(), map[string]domain.Role{
		memberPK: domain.RoleAdmin,
	})

	source := NewTrustSetMemberSource(ts, nil)
	ctx := context.Background()

	// Org scope should return org members, not fleet ops.
	pubkeys, err := source.OrgMemberPubkeys(ctx, orgID.String())
	require.NoError(t, err)
	require.Contains(t, pubkeys, memberPK)
	require.NotContains(t, pubkeys, fleetPK, "fleet ops should not be in org member set")
}
