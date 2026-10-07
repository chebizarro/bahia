package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ConfidentialStateEncryptor encrypts and decrypts confidential cp-state
// records using a per-org content key (OCK). C1 path for
// LegacyOrgStateDecryptor. Org members can decrypt the AEAD layer; service-only
// fields require an additional NIP-44 decrypt to the service pubkey.
//
// The legacyKind, dTag, and topic parameters bind the AEAD associated data
// to the record's coordinate identity, preventing ciphertext replay across
// coordinates.
type ConfidentialStateEncryptor interface {
	EncryptConfidential(ctx context.Context, orgID string, plaintext []byte, legacyKind int, dTag, topic string, serviceOnlyPlaintext []byte) (string, error)
	DecryptConfidential(ctx context.Context, content string, legacyKind int, dTag, topic string) ([]byte, error)
	DecryptServiceInner(ctx context.Context, content string) ([]byte, error)
	RotateKey(ctx context.Context, orgID string) error
	RotateKeyExcluding(ctx context.Context, orgID, removedPubkey string) error
	WrapKeyForMember(ctx context.Context, orgID string, pubkey string) error
}

// LegacyOrgStateDecryptor is the read-only compatibility decryption interface.
// Retained during migration so RelayMemberEventHandler can attempt
// old-format O1 decryption. No encrypt path — all new writes use
// ConfidentialStateEncryptor.
type LegacyOrgStateDecryptor interface {
	DecryptOrgState(content string) ([]byte, error)
}

// MemberPublishedCallback is called after a member canonical record is
// published. Parameters are the encrypted content and the record's coordinate
// identity (legacyKind, dTag, topic) for AD verification during decryption.
type MemberPublishedCallback func(ctx context.Context, encryptedContent string, legacyKind int, dTag, topic string)

// OrgCanonicalPublisher publishes canonical cp-state for org, member and invite
// entities through the shared Projector signing/outbox pipeline. Content is
// encrypted with a per-org content key (OCK) so org members can decrypt it
// ( C1, docs/architecture/confidential-state.md).
//
// The outer envelope (d, domain, schema, legacy_kind, deleted, t) from
// controlStateEnvelope is preserved so coordinates and tombstones still work.
// No p tags are emitted (member pubkeys are confidential).
type OrgCanonicalPublisher struct {
	projector         *Projector
	encryptor         ConfidentialStateEncryptor
	onMemberPublished MemberPublishedCallback
	logger            *zap.Logger
	rekeyMu           sync.Mutex
	strictRevocation  func(context.Context, uuid.UUID) (bool, error)
}

// NewOrgCanonicalPublisher creates a publisher backed by the given projector.
// encryptor is required; publishes fail closed without it.
func NewOrgCanonicalPublisher(projector *Projector, encryptor ConfidentialStateEncryptor, logger *zap.Logger) *OrgCanonicalPublisher {
	return &OrgCanonicalPublisher{
		projector: projector,
		encryptor: encryptor,
		logger:    logger.Named("org-canonical-publisher"),
	}
}

// SetOnMemberPublished sets a callback invoked after each member canonical
// record is published. Used to drive relay trust source hydration in real time.
func (p *OrgCanonicalPublisher) SetOnMemberPublished(cb MemberPublishedCallback) {
	p.onMemberPublished = cb
}

// SetStrictRevocationLookup supplies the persisted organization setting.
func (p *OrgCanonicalPublisher) SetStrictRevocationLookup(lookup func(context.Context, uuid.UUID) (bool, error)) {
	p.strictRevocation = lookup
}

// PublishOrg publishes a canonical org registry record.
func (p *OrgCanonicalPublisher) PublishOrg(ctx context.Context, org *domain.Organization, deleted bool) error {
	content := map[string]any{
		"deleted": deleted,
		"id":      org.ID.String(),
	}
	if !deleted {
		content["name"] = org.Name
		content["display_name"] = org.DisplayName
		content["owner_pubkey"] = org.OwnerPubkey
		content["strict_revocation"] = org.StrictRevocation
		putRecordTime(content, "created_at", org.CreatedAt)
		putRecordTime(content, "updated_at", org.UpdatedAt)
	}
	_, err := p.publishEncrypted(ctx, KindOrgRegistry, org.ID.String(), deleted, nil, content, "org.projection", &org.ID, org.ID.String())
	return err
}

// PublishMember publishes a canonical org member record. Required OCK rotation
// precedes the canonical record, so a failed rekey cannot commit a revocation:
// - Deleted member → RotateKey so the removed member cannot decrypt future records.
// - Added/updated member → WrapKeyForMember so they can read existing records.
// - Role downgrade (prevRole provided and higher than current) → RotateKey.
//
// prevRole is optional; pass the old role when known (role-change paths in
// OrgIntentHandler and EncryptedDomainHandlers) so the publisher can detect
// downgrades. When omitted, no downgrade check is performed.
func (p *OrgCanonicalPublisher) PublishMember(ctx context.Context, member *domain.OrgMember, deleted bool, prevRole ...domain.Role) error {
	// Do not interleave another membership change or manual rekey between
	// exclusion, canonical publication, and strict refounding.
	p.rekeyMu.Lock()
	defer p.rekeyMu.Unlock()
	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing member publish")
	}
	orgID := member.OrgID.String()
	downgrade := !deleted && len(prevRole) > 0 && prevRole[0] != "" && domain.RoleWeight(member.Role) < domain.RoleWeight(prevRole[0])
	strict := false
	if (deleted || downgrade) && p.strictRevocation != nil {
		var err error
		strict, err = p.strictRevocation(ctx, member.OrgID)
		if err != nil {
			return fmt.Errorf("load strict revocation for org %s: %w", member.OrgID, err)
		}
	}
	if deleted {
		if err := p.encryptor.RotateKeyExcluding(ctx, orgID, member.Pubkey); err != nil {
			return fmt.Errorf("rotate OCK before member removal for org %s: %w", orgID, err)
		}
	} else if downgrade {
		if err := p.encryptor.RotateKey(ctx, orgID); err != nil {
			return fmt.Errorf("rotate OCK before role downgrade for org %s: %w", orgID, err)
		}
	}
	if strict && (deleted || downgrade) {
		// Finish strict refounding before committing membership too. An error
		// must not leave a canonical removal paired with the old repository role.
		if _, _, err := p.refoundCurrent(ctx, orgID); err != nil {
			return err
		}
	}
	dTag := orgMemberDTag(member.OrgID, member.Pubkey)
	content := map[string]any{
		"deleted": deleted,
		"org_id":  member.OrgID.String(),
		"pubkey":  member.Pubkey,
	}
	if !deleted {
		content["role"] = string(member.Role)
		content["nip05"] = member.NIP05
		putRecordTime(content, "joined_at", member.JoinedAt)
		putRecordTime(content, "updated_at", member.UpdatedAt)
	}
	// No p tags — member pubkeys are confidential (docs/architecture/confidential-state.md).
	entityID := uuid.NewSHA1(member.OrgID, []byte(member.Pubkey))
	legacyKind := KindOrgMemberRegistry
	topic := ""
	if fam, ok := cpStateFamilies[legacyKind]; ok {
		topic = fam.topic
	}
	encryptedContent, err := p.publishEncrypted(ctx, legacyKind, dTag, deleted, nil, content, "org_member.projection", &entityID, member.OrgID.String())
	if err != nil {
		return err
	}
	if p.onMemberPublished != nil && encryptedContent != "" {
		p.onMemberPublished(ctx, encryptedContent, legacyKind, dTag, topic)
	}
	if !deleted && !downgrade {
		// Add/update only: a failed wrap withholds access, not secrecy.
		if wrapErr := p.encryptor.WrapKeyForMember(ctx, orgID, member.Pubkey); wrapErr != nil {
			p.logger.Warn("OCK wrap for new member failed",
				zap.String("org_id", orgID), zap.Error(wrapErr))
		}
	}
	return nil
}

// PublishInvite publishes a canonical org invite record.
func (p *OrgCanonicalPublisher) PublishInvite(ctx context.Context, invite *domain.OrgInvite, deleted bool) error {
	content := map[string]any{
		"deleted": deleted,
		"id":      invite.ID.String(),
		"org_id":  invite.OrgID.String(),
	}
	if !deleted {
		content["pubkey"] = invite.Pubkey
		content["role"] = string(invite.Role)
		content["invited_by"] = invite.InvitedBy
		putRecordTime(content, "expires_at", invite.ExpiresAt)
		putRecordTime(content, "created_at", invite.CreatedAt)
	}
	// No p tags — invitee pubkeys are confidential (docs/architecture/confidential-state.md).
	_, err := p.publishEncrypted(ctx, KindOrgInviteRegistry, invite.ID.String(), deleted, nil, content, "org_invite.projection", &invite.ID, invite.OrgID.String())
	return err
}

// publishEncrypted marshals content, encrypts it with the confidential
// encryptor, publishes through publishControlState, and returns the encrypted content.
func (p *OrgCanonicalPublisher) publishEncrypted(ctx context.Context, legacyKind int, dTag string, deleted bool, extraTags gonostr.Tags, content map[string]any, entityType string, entityID *uuid.UUID, orgID string) (string, error) {
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("marshal org state content: %w", err)
	}

	// Determine topic for associated data binding.
	topic := ""
	if fam, ok := cpStateFamilies[legacyKind]; ok {
		topic = fam.topic
	}

	// Org state is confidential (docs/architecture/confidential-state.md): never publish without an
	// encryptor, so there is no plaintext path to a relay.
	if p.encryptor == nil {
		return "", fmt.Errorf("confidential encryptor not configured; refusing plaintext publish")
	}
	publishContent, err := p.encryptor.EncryptConfidential(ctx, orgID, contentJSON, legacyKind, dTag, topic, nil)
	if err != nil {
		return "", fmt.Errorf("encrypt org state: %w", err)
	}

	err = p.projector.publishControlState(ctx, legacyKind, dTag, deleted, extraTags, publishContent, entityType, entityID)
	if err != nil {
		return "", err
	}
	return publishContent, nil
}

// orgMemberDTag returns the d-tag coordinate for an org member record:
// "org:member:<org-id>:<pubkey>", matching the design's membership event model (docs/architecture/intents-and-authority.md).
func orgMemberDTag(orgID uuid.UUID, pubkey string) string {
	return "org:member:" + orgID.String() + ":" + pubkey
}
