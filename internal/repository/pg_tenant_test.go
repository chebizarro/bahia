package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgOrganizationRepository_CreateNormalizesOwnerPubkey(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgOrganizationRepositoryWithDB(mock)
	org := &domain.Organization{
		ID:          uuid.New(),
		Name:        "acme",
		DisplayName: "ACME",
		OwnerPubkey: "  ABCDEF1234  ",
	}

	mock.ExpectExec("INSERT INTO organizations").
		WithArgs(org.ID, org.Name, org.DisplayName, "abcdef1234", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = repo.Create(ctx, org)
	require.NoError(t, err)
	require.Equal(t, "abcdef1234", org.OwnerPubkey)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgOrgMemberRepository_AddNormalizesPubkey(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgOrgMemberRepositoryWithDB(mock)
	member := &domain.OrgMember{
		OrgID:  uuid.New(),
		Pubkey: "  AABBCC  ",
		Role:   domain.RoleAdmin,
		NIP05:  "alice@example.com",
	}

	mock.ExpectExec("INSERT INTO org_members").
		WithArgs(member.OrgID, "aabbcc", member.Role, member.NIP05, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = repo.Add(ctx, member)
	require.NoError(t, err)
	require.Equal(t, "aabbcc", member.Pubkey)
	require.WithinDuration(t, time.Now().UTC(), member.JoinedAt, 2*time.Second)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgOrgMemberRepository_UpdateRoleNormalizesLookupPubkey(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgOrgMemberRepositoryWithDB(mock)
	orgID := uuid.New()

	mock.ExpectExec("UPDATE org_members SET role").
		WithArgs(orgID, "ffeedd", domain.RoleOwner, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = repo.UpdateRole(ctx, orgID, "  FFEEDD  ", domain.RoleOwner)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgOrgInviteRepository_CreateNormalizesPubkeys(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgOrgInviteRepositoryWithDB(mock)
	invite := &domain.OrgInvite{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		Pubkey:    "  A1B2C3  ",
		Role:      domain.RoleViewer,
		InvitedBy: "  D4E5F6  ",
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}

	mock.ExpectExec("INSERT INTO org_invites").
		WithArgs(invite.ID, invite.OrgID, "a1b2c3", invite.Role, "d4e5f6", invite.ExpiresAt, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = repo.Create(ctx, invite)
	require.NoError(t, err)
	require.Equal(t, "a1b2c3", invite.Pubkey)
	require.Equal(t, "d4e5f6", invite.InvitedBy)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgOrgInviteRepository_GetByIDScopesOrganization(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgOrgInviteRepositoryWithDB(mock)
	orgID := uuid.New()
	inviteID := uuid.New()
	mock.ExpectQuery("FROM org_invites WHERE org_id = \\$1 AND id = \\$2").
		WithArgs(orgID, inviteID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "pubkey", "role", "invited_by", "expires_at", "created_at"}))

	invite, err := repo.GetByID(ctx, orgID, inviteID)
	require.ErrorIs(t, err, ErrNotFound)
	require.Nil(t, invite)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantRepositoriesSharedScannersServeGetAndList(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("organizations", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		id := uuid.New()
		columns := []string{"id", "name", "display_name", "owner_pubkey", "created_at", "updated_at"}
		row := func() *pgxmock.Rows {
			return pgxmock.NewRows(columns).AddRow(id, "acme", "ACME", "owner", now, now)
		}
		mock.ExpectQuery("FROM organizations WHERE id = \\$1").WithArgs(id).WillReturnRows(row())
		mock.ExpectQuery("FROM organizations ORDER BY name").WillReturnRows(row())
		repo := newPgOrganizationRepositoryWithDB(mock)
		org, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		orgs, err := repo.List(ctx)
		require.NoError(t, err)
		require.Equal(t, []domain.Organization{*org}, orgs)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("memberships", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		orgID := uuid.New()
		columns := []string{"org_id", "pubkey", "role", "nip05", "joined_at", "updated_at"}
		row := func() *pgxmock.Rows {
			return pgxmock.NewRows(columns).AddRow(orgID, "member", "admin", "member@example.com", now, now)
		}
		mock.ExpectQuery("FROM org_members WHERE org_id = \\$1 AND pubkey = \\$2").WithArgs(orgID, "member").WillReturnRows(row())
		mock.ExpectQuery("FROM org_members WHERE org_id = \\$1 ORDER BY joined_at").WithArgs(orgID).WillReturnRows(row())
		repo := newPgOrgMemberRepositoryWithDB(mock)
		member, err := repo.GetMember(ctx, orgID, "member")
		require.NoError(t, err)
		members, err := repo.ListByOrg(ctx, orgID)
		require.NoError(t, err)
		require.Equal(t, []domain.OrgMember{*member}, members)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("invitations", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		orgID, inviteID := uuid.New(), uuid.New()
		expiresAt := now.Add(time.Hour)
		columns := []string{"id", "org_id", "pubkey", "role", "invited_by", "expires_at", "created_at"}
		row := func() *pgxmock.Rows {
			return pgxmock.NewRows(columns).AddRow(inviteID, orgID, "invitee", "viewer", "owner", expiresAt, now)
		}
		mock.ExpectQuery("FROM org_invites WHERE org_id = \\$1 AND id = \\$2").WithArgs(orgID, inviteID).WillReturnRows(row())
		mock.ExpectQuery("FROM org_invites WHERE org_id = \\$1 AND expires_at > NOW\\(\\) ORDER BY created_at").WithArgs(orgID).WillReturnRows(row())
		repo := newPgOrgInviteRepositoryWithDB(mock)
		invite, err := repo.GetByID(ctx, orgID, inviteID)
		require.NoError(t, err)
		invites, err := repo.ListByOrg(ctx, orgID)
		require.NoError(t, err)
		require.Equal(t, []domain.OrgInvite{*invite}, invites)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
