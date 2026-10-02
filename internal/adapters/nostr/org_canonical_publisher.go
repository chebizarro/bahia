package nostr

import (
	"context"
	"encoding/json"
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// OrgCanonicalPublisher publishes canonical cp-state for org, member and invite
// entities through the shared Projector signing/outbox pipeline. It follows the
// BackupCanonicalPublisher pattern: the cpStateFamilies table is the single
// envelope source, and publishControlState is the single publish path.
//
// Phase 3 Wave 5 O1.
type OrgCanonicalPublisher struct {
	projector *Projector
	logger    *zap.Logger
}

// NewOrgCanonicalPublisher creates a publisher backed by the given projector.
func NewOrgCanonicalPublisher(projector *Projector, logger *zap.Logger) *OrgCanonicalPublisher {
	return &OrgCanonicalPublisher{
		projector: projector,
		logger:    logger.Named("org-canonical-publisher"),
	}
}

// PublishOrg publishes a canonical org registry record.
func (p *OrgCanonicalPublisher) PublishOrg(ctx context.Context, org *domain.Organization, deleted bool) error {
	content := map[string]any{
		"deleted": deleted,
		"id":      org.ID.String(),
	}
	tags := gonostr.Tags{}
	if !deleted {
		content["name"] = org.Name
		content["display_name"] = org.DisplayName
		content["owner_pubkey"] = org.OwnerPubkey
		putRecordTime(content, "created_at", org.CreatedAt)
		putRecordTime(content, "updated_at", org.UpdatedAt)
		tags = append(tags, gonostr.Tag{"name", org.Name})
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("marshal org content: %w", err)
	}
	return p.projector.publishControlState(ctx, KindOrgRegistry, org.ID.String(), deleted, tags, string(contentJSON), "org.projection", &org.ID)
}

// PublishMember publishes a canonical org member record.
func (p *OrgCanonicalPublisher) PublishMember(ctx context.Context, member *domain.OrgMember, deleted bool) error {
	dTag := orgMemberDTag(member.OrgID, member.Pubkey)
	content := map[string]any{
		"deleted": deleted,
		"org_id":  member.OrgID.String(),
		"pubkey":  member.Pubkey,
	}
	tags := gonostr.Tags{}
	if !deleted {
		content["role"] = string(member.Role)
		content["nip05"] = member.NIP05
		putRecordTime(content, "joined_at", member.JoinedAt)
		putRecordTime(content, "updated_at", member.UpdatedAt)
		tags = append(tags,
			gonostr.Tag{"p", member.Pubkey},
			gonostr.Tag{"org", member.OrgID.String()},
		)
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("marshal member content: %w", err)
	}
	entityID := uuid.NewSHA1(member.OrgID, []byte(member.Pubkey))
	return p.projector.publishControlState(ctx, KindOrgMemberRegistry, dTag, deleted, tags, string(contentJSON), "org_member.projection", &entityID)
}

// PublishInvite publishes a canonical org invite record.
func (p *OrgCanonicalPublisher) PublishInvite(ctx context.Context, invite *domain.OrgInvite, deleted bool) error {
	content := map[string]any{
		"deleted": deleted,
		"id":      invite.ID.String(),
		"org_id":  invite.OrgID.String(),
	}
	tags := gonostr.Tags{}
	if !deleted {
		content["pubkey"] = invite.Pubkey
		content["role"] = string(invite.Role)
		content["invited_by"] = invite.InvitedBy
		putRecordTime(content, "expires_at", invite.ExpiresAt)
		putRecordTime(content, "created_at", invite.CreatedAt)
		tags = append(tags,
			gonostr.Tag{"p", invite.Pubkey},
			gonostr.Tag{"org", invite.OrgID.String()},
		)
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("marshal invite content: %w", err)
	}
	return p.projector.publishControlState(ctx, KindOrgInviteRegistry, invite.ID.String(), deleted, tags, string(contentJSON), "org_invite.projection", &invite.ID)
}

// orgMemberDTag returns the d-tag coordinate for an org member record:
// "org:member:<org-id>:<pubkey>", matching the design's membership event model (§2.2).
func orgMemberDTag(orgID uuid.UUID, pubkey string) string {
	return "org:member:" + orgID.String() + ":" + pubkey
}
