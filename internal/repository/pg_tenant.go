package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// OrganizationRepository manages organization records.
type OrganizationRepository interface {
	Create(ctx context.Context, org *domain.Organization) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Organization, error)
	GetByName(ctx context.Context, name string) (*domain.Organization, error)
	List(ctx context.Context) ([]domain.Organization, error)
	Update(ctx context.Context, org *domain.Organization) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// OrgMemberRepository manages organization member records.
type OrgMemberRepository interface {
	Add(ctx context.Context, member *domain.OrgMember) error
	GetMember(ctx context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error)
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.OrgMember, error)
	ListByPubkey(ctx context.Context, pubkey string) ([]domain.OrgMember, error)
	UpdateRole(ctx context.Context, orgID uuid.UUID, pubkey string, role domain.Role) error
	Remove(ctx context.Context, orgID uuid.UUID, pubkey string) error
}

// OrgInviteRepository manages organization invite records.
type OrgInviteRepository interface {
	Create(ctx context.Context, invite *domain.OrgInvite) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.OrgInvite, error)
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.OrgInvite, error)
	ListByPubkey(ctx context.Context, pubkey string) ([]domain.OrgInvite, error)
	Delete(ctx context.Context, id uuid.UUID) error
	DeleteExpired(ctx context.Context) (int, error)
}

// PgOrganizationRepository implements OrganizationRepository with PostgreSQL.
type PgOrganizationRepository struct {
	pool *pgxpool.Pool
}

// NewPgOrganizationRepository creates a new PgOrganizationRepository.
func NewPgOrganizationRepository(pool *pgxpool.Pool) *PgOrganizationRepository {
	return &PgOrganizationRepository{pool: pool}
}

func (r *PgOrganizationRepository) Create(ctx context.Context, org *domain.Organization) error {
	if org.ID == uuid.Nil {
		org.ID = uuid.New()
	}
	now := time.Now().UTC()
	org.CreatedAt = now
	org.UpdatedAt = now

	_, err := r.pool.Exec(ctx, `
		INSERT INTO organizations (id, name, display_name, owner_pubkey, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, org.ID, org.Name, org.DisplayName, org.OwnerPubkey, org.CreatedAt, org.UpdatedAt)
	return err
}

func (r *PgOrganizationRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Organization, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, display_name, owner_pubkey, created_at, updated_at
		FROM organizations WHERE id = $1
	`, id)

	var org domain.Organization
	err := row.Scan(&org.ID, &org.Name, &org.DisplayName, &org.OwnerPubkey, &org.CreatedAt, &org.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &org, nil
}

func (r *PgOrganizationRepository) GetByName(ctx context.Context, name string) (*domain.Organization, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, display_name, owner_pubkey, created_at, updated_at
		FROM organizations WHERE name = $1
	`, name)

	var org domain.Organization
	err := row.Scan(&org.ID, &org.Name, &org.DisplayName, &org.OwnerPubkey, &org.CreatedAt, &org.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &org, nil
}

func (r *PgOrganizationRepository) List(ctx context.Context) ([]domain.Organization, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, display_name, owner_pubkey, created_at, updated_at
		FROM organizations ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []domain.Organization
	for rows.Next() {
		var org domain.Organization
		if err := rows.Scan(&org.ID, &org.Name, &org.DisplayName, &org.OwnerPubkey, &org.CreatedAt, &org.UpdatedAt); err != nil {
			return nil, err
		}
		orgs = append(orgs, org)
	}
	return orgs, rows.Err()
}

func (r *PgOrganizationRepository) Update(ctx context.Context, org *domain.Organization) error {
	org.UpdatedAt = time.Now().UTC()
	tag, err := r.pool.Exec(ctx, `
		UPDATE organizations SET display_name = $2, owner_pubkey = $3, updated_at = $4
		WHERE id = $1
	`, org.ID, org.DisplayName, org.OwnerPubkey, org.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PgOrganizationRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PgOrgMemberRepository implements OrgMemberRepository with PostgreSQL.
type PgOrgMemberRepository struct {
	pool *pgxpool.Pool
}

// NewPgOrgMemberRepository creates a new PgOrgMemberRepository.
func NewPgOrgMemberRepository(pool *pgxpool.Pool) *PgOrgMemberRepository {
	return &PgOrgMemberRepository{pool: pool}
}

func (r *PgOrgMemberRepository) Add(ctx context.Context, member *domain.OrgMember) error {
	now := time.Now().UTC()
	member.JoinedAt = now
	member.UpdatedAt = now

	_, err := r.pool.Exec(ctx, `
		INSERT INTO org_members (org_id, pubkey, role, nip05, joined_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_id, pubkey) DO UPDATE SET role = $3, nip05 = $4, updated_at = $6
	`, member.OrgID, member.Pubkey, member.Role, member.NIP05, member.JoinedAt, member.UpdatedAt)
	return err
}

func (r *PgOrgMemberRepository) GetMember(ctx context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT org_id, pubkey, role, nip05, joined_at, updated_at
		FROM org_members WHERE org_id = $1 AND pubkey = $2
	`, orgID, pubkey)

	var m domain.OrgMember
	err := row.Scan(&m.OrgID, &m.Pubkey, &m.Role, &m.NIP05, &m.JoinedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *PgOrgMemberRepository) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.OrgMember, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT org_id, pubkey, role, nip05, joined_at, updated_at
		FROM org_members WHERE org_id = $1 ORDER BY joined_at
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []domain.OrgMember
	for rows.Next() {
		var m domain.OrgMember
		if err := rows.Scan(&m.OrgID, &m.Pubkey, &m.Role, &m.NIP05, &m.JoinedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (r *PgOrgMemberRepository) ListByPubkey(ctx context.Context, pubkey string) ([]domain.OrgMember, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT org_id, pubkey, role, nip05, joined_at, updated_at
		FROM org_members WHERE pubkey = $1 ORDER BY joined_at
	`, pubkey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []domain.OrgMember
	for rows.Next() {
		var m domain.OrgMember
		if err := rows.Scan(&m.OrgID, &m.Pubkey, &m.Role, &m.NIP05, &m.JoinedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (r *PgOrgMemberRepository) UpdateRole(ctx context.Context, orgID uuid.UUID, pubkey string, role domain.Role) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE org_members SET role = $3, updated_at = $4
		WHERE org_id = $1 AND pubkey = $2
	`, orgID, pubkey, role, time.Now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PgOrgMemberRepository) Remove(ctx context.Context, orgID uuid.UUID, pubkey string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM org_members WHERE org_id = $1 AND pubkey = $2`, orgID, pubkey)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PgOrgInviteRepository implements OrgInviteRepository with PostgreSQL.
type PgOrgInviteRepository struct {
	pool *pgxpool.Pool
}

// NewPgOrgInviteRepository creates a new PgOrgInviteRepository.
func NewPgOrgInviteRepository(pool *pgxpool.Pool) *PgOrgInviteRepository {
	return &PgOrgInviteRepository{pool: pool}
}

func (r *PgOrgInviteRepository) Create(ctx context.Context, invite *domain.OrgInvite) error {
	if invite.ID == uuid.Nil {
		invite.ID = uuid.New()
	}
	invite.CreatedAt = time.Now().UTC()

	_, err := r.pool.Exec(ctx, `
		INSERT INTO org_invites (id, org_id, pubkey, role, invited_by, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, invite.ID, invite.OrgID, invite.Pubkey, invite.Role, invite.InvitedBy, invite.ExpiresAt, invite.CreatedAt)
	return err
}

func (r *PgOrgInviteRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.OrgInvite, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, org_id, pubkey, role, invited_by, expires_at, created_at
		FROM org_invites WHERE id = $1
	`, id)

	var inv domain.OrgInvite
	err := row.Scan(&inv.ID, &inv.OrgID, &inv.Pubkey, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func (r *PgOrgInviteRepository) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.OrgInvite, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, org_id, pubkey, role, invited_by, expires_at, created_at
		FROM org_invites WHERE org_id = $1 AND expires_at > NOW() ORDER BY created_at
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []domain.OrgInvite
	for rows.Next() {
		var inv domain.OrgInvite
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Pubkey, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt); err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

func (r *PgOrgInviteRepository) ListByPubkey(ctx context.Context, pubkey string) ([]domain.OrgInvite, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, org_id, pubkey, role, invited_by, expires_at, created_at
		FROM org_invites WHERE pubkey = $1 AND expires_at > NOW() ORDER BY created_at
	`, pubkey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []domain.OrgInvite
	for rows.Next() {
		var inv domain.OrgInvite
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Pubkey, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt); err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

func (r *PgOrgInviteRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM org_invites WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PgOrgInviteRepository) DeleteExpired(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM org_invites WHERE expires_at <= NOW()`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
