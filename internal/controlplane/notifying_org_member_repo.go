package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// NotifyingOrgMemberRepository wraps an OrgMemberRepository and calls the
// IntentAuthorsSyncer's TrackPubkey/UntrackPubkey on mutations so the sidecar's
// intent authors set stays in sync with Postgres org membership without
// requiring a daemon restart or a polling loop.
//
// Read-only operations are passed through unchanged.
type NotifyingOrgMemberRepository struct {
	inner  repository.OrgMemberRepository
	syncer *IntentAuthorsSyncer
}

// NewNotifyingOrgMemberRepository wraps inner with notifications to syncer.
func NewNotifyingOrgMemberRepository(inner repository.OrgMemberRepository, syncer *IntentAuthorsSyncer) *NotifyingOrgMemberRepository {
	return &NotifyingOrgMemberRepository{inner: inner, syncer: syncer}
}

func (r *NotifyingOrgMemberRepository) Add(ctx context.Context, member *domain.OrgMember) error {
	if err := r.inner.Add(ctx, member); err != nil {
		return err
	}
	r.syncer.TrackPubkey(member.Pubkey)
	return nil
}

func (r *NotifyingOrgMemberRepository) GetMember(ctx context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	return r.inner.GetMember(ctx, orgID, pubkey)
}

func (r *NotifyingOrgMemberRepository) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.OrgMember, error) {
	return r.inner.ListByOrg(ctx, orgID)
}

func (r *NotifyingOrgMemberRepository) ListByPubkey(ctx context.Context, pubkey string) ([]domain.OrgMember, error) {
	return r.inner.ListByPubkey(ctx, pubkey)
}

func (r *NotifyingOrgMemberRepository) UpdateRole(ctx context.Context, orgID uuid.UUID, pubkey string, role domain.Role) error {
	if err := r.inner.UpdateRole(ctx, orgID, pubkey, role); err != nil {
		return err
	}
	// Role changes do not affect the intent authors set (the sidecar admits
	// by pubkey, the intent processor checks permission by role), but we
	// notify so the syncer can re-push in case AuthorPubkeys changed.
	r.syncer.Notify()
	return nil
}

func (r *NotifyingOrgMemberRepository) Remove(ctx context.Context, orgID uuid.UUID, pubkey string) error {
	if err := r.inner.Remove(ctx, orgID, pubkey); err != nil {
		return err
	}
	r.syncer.UntrackPubkey(pubkey)
	return nil
}
