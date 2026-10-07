package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// OrgCanonicalPublisher is the narrow interface the org intent handler uses to
// publish canonical cp-state for org, member, and invite entities. Implemented
// by internal/adapters/nostr.OrgCanonicalPublisher.
type OrgCanonicalPublisher interface {
	PublishOrg(ctx context.Context, org *domain.Organization, deleted bool) error
	// PublishMember publishes a canonical member record and drives key lifecycle.
	// prevRole is optional; pass the old role on role-change so the publisher
	// can detect downgrades and rotate the OCK.
	PublishMember(ctx context.Context, member *domain.OrgMember, deleted bool, prevRole ...domain.Role) error
	PublishInvite(ctx context.Context, invite *domain.OrgInvite, deleted bool) error
}

// OrgMemberChangeCallback is invoked after every member add/remove/role-change
// so the TrustSet relay source and IntentAuthorsSyncer stay in sync.
type OrgMemberChangeCallback func(orgID uuid.UUID)

// OrgIntentHandler processes org/member/invite intents. It is the O1 domain
// handler for the intent framework.
//
// Level-triggered: the newest trusted intent's full desired state wins.
// Idempotent by intent_id (handled by the intent processor).
// Authorization is per-operation: fleet-ops for org create, per-org RBAC for
// everything else (SelfAuthorizingHandler).
//
// See docs/architecture/intents-and-authority.md
type OrgIntentHandler struct {
	orgs    repository.OrganizationRepository
	members repository.OrgMemberRepository
	invites repository.OrgInviteRepository

	publisher OrgCanonicalPublisher
	status    *IntentStatusPublisher
	logger    *zap.Logger

	// onMemberChange is called after member add/remove/role-change.
	onMemberChange OrgMemberChangeCallback
}

// DecryptMemberContent decrypts an encrypted membership event content string
// and returns the role for TrustSet hydration. Used by the relay trust source
// at startup and on live membership events. Compatibility O1 format.
func DecryptMemberContent(encryptor interface{ DecryptOrgState(string) ([]byte, error) }, content string) (orgID string, pubkey string, role string, deleted bool, err error) {
	plaintext, err := encryptor.DecryptOrgState(content)
	if err != nil {
		return "", "", "", false, err
	}
	return parseMemberContentFields(plaintext)
}

// DecryptMemberContentConfidential decrypts a member event using the new
// confidential format (per-org content key), falling back to compatibility O1.
// legacyKind, dTag, and topic are the record's coordinate identity for AD
// verification; pass zero values when unknown (e.g. startup hydration from
// history where the event tags are not readily available — AD check will fail
// and the function will fall back to compatibility O1).
func DecryptMemberContentConfidential(
	confidential *ConfidentialEncryptor,
	legacyO1 interface{ DecryptOrgState(string) ([]byte, error) },
	content string,
	legacyKind int, dTag, topic string,
) (orgID string, pubkey string, role string, deleted bool, err error) {
	// Try new confidential format first.
	if confidential != nil {
		plaintext, decErr := confidential.DecryptConfidential(context.Background(), content, legacyKind, dTag, topic)
		if decErr == nil {
			return parseMemberContentFields(plaintext)
		}
	}
	// Fall back to compatibility O1 format.
	if legacyO1 != nil {
		return DecryptMemberContent(legacyO1, content)
	}
	return "", "", "", false, fmt.Errorf("no decryptor available for member event")
}

// parseMemberContentFields extracts org_id, pubkey, role, and deleted from
// decrypted member content JSON.
func parseMemberContentFields(plaintext []byte) (orgID, pubkey, role string, deleted bool, err error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal(plaintext, &parsed); err != nil {
		return "", "", "", false, fmt.Errorf("unmarshal decrypted member content: %w", err)
	}
	orgID, _ = parsed["org_id"].(string)
	pubkey, _ = parsed["pubkey"].(string)
	role, _ = parsed["role"].(string)
	if d, ok := parsed["deleted"].(bool); ok {
		deleted = d
	}
	return orgID, pubkey, role, deleted, nil
}

// OrgIntentHandlerConfig configures the org intent handler.
type OrgIntentHandlerConfig struct {
	Orgs    repository.OrganizationRepository
	Members repository.OrgMemberRepository
	Invites repository.OrgInviteRepository

	Publisher      OrgCanonicalPublisher
	Status         *IntentStatusPublisher
	Logger         *zap.Logger
	OnMemberChange OrgMemberChangeCallback
}

// NewOrgIntentHandler creates an org intent handler.
func NewOrgIntentHandler(cfg OrgIntentHandlerConfig) *OrgIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OrgIntentHandler{
		orgs:           cfg.Orgs,
		members:        cfg.Members,
		invites:        cfg.Invites,
		publisher:      cfg.Publisher,
		status:         cfg.Status,
		logger:         logger.Named("org-intent"),
		onMemberChange: cfg.OnMemberChange,
	}
}

// orgIntentNameRegex validates organization names.
var orgIntentNameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// orgSubEntity determines the sub-entity type from the intent's schema tag.
type orgSubEntity int

const (
	orgSubOrg orgSubEntity = iota
	orgSubMember
	orgSubInvite
)

func classifyOrgIntent(intent *Intent) orgSubEntity {
	switch intent.Schema {
	case "bahia.intent.org-member.v1":
		return orgSubMember
	case "bahia.intent.org-invite.v1":
		return orgSubInvite
	default:
		// "bahia.intent.org.v1" or fallback: check coordinate pattern.
		if strings.HasPrefix(intent.Coordinate, "org:member:") {
			return orgSubMember
		}
		if strings.HasPrefix(intent.Coordinate, "org:invite:") {
			return orgSubInvite
		}
		return orgSubOrg
	}
}

// HandleIntent processes an org/member/invite intent. The processor has already
// deduplicated, validated, and authorized the intent (via AuthorizeIntent).
func (h *OrgIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	sub := classifyOrgIntent(intent)
	switch sub {
	case orgSubOrg:
		return h.handleOrg(ctx, intent)
	case orgSubMember:
		return h.handleMember(ctx, intent)
	case orgSubInvite:
		return h.handleInvite(ctx, intent)
	default:
		return fmt.Errorf("unknown org sub-entity for coordinate %q", intent.Coordinate)
	}
}

// PermissionFor returns the permission for the org domain. Since the org domain
// uses SelfAuthorizingHandler, this is only called as a fallback label for
// rejection messages.
func (h *OrgIntentHandler) PermissionFor(op string) domain.Permission {
	return domain.PermManageSettings
}

// AuthorizeIntent implements SelfAuthorizingHandler. It checks authorization
// based on the sub-entity type:
// - Org create: actor must be a fleet operator (docs/architecture/intents-and-authority.md)
// - Org update/delete: actor must have PermManageSettings in the org
// - Member/invite ops: actor must have PermManageMembers in the org
func (h *OrgIntentHandler) AuthorizeIntent(ctx context.Context, trustSet *TrustSet, intent *Intent) error {
	sub := classifyOrgIntent(intent)
	switch sub {
	case orgSubOrg:
		if intent.Op == "rekey" {
			orgID := stringField(intent.Content, "org_id")
			if orgID == kinds.FleetOCKScope {
				if intent.OrgID != uuid.Nil {
					return fmt.Errorf("fleet rekey must use the fleet key scope")
				}
				for _, pk := range trustSet.FleetOps() {
					if pk == intent.Actor {
						return nil
					}
				}
				return fmt.Errorf("fleet rekey requires fleet operator authorization")
			}
			id, err := uuid.Parse(orgID)
			if err != nil || id == uuid.Nil || id != intent.OrgID {
				return fmt.Errorf("rekey org_id must match the intent org tag")
			}
			if !domain.HasAtLeastRole(trustSet.RoleFor(ctx, id, intent.Actor), domain.RoleAdmin) {
				return fmt.Errorf("org rekey requires owner or admin authorization")
			}
			return nil
		}
		if intent.Op == "create" {
			// Org create requires fleet-ops.
			for _, pk := range trustSet.FleetOps() {
				if pk == intent.Actor {
					return nil
				}
			}
			return fmt.Errorf("org create requires fleet operator authorization")
		}
		// Org update/delete.
		if !trustSet.HasPermission(ctx, intent.OrgID, intent.Actor, domain.PermManageSettings) {
			return fmt.Errorf("insufficient permission: %s", domain.PermManageSettings)
		}
		return nil

	case orgSubMember, orgSubInvite:
		if sub == orgSubMember && intent.Op == "create" && stringField(intent.Content, "invite_id") != "" {
			_, err := h.acceptanceInvite(ctx, intent)
			return err
		}
		if !trustSet.HasPermission(ctx, intent.OrgID, intent.Actor, domain.PermManageMembers) {
			return fmt.Errorf("insufficient permission: %s", domain.PermManageMembers)
		}
		return nil

	default:
		return fmt.Errorf("unknown org sub-entity")
	}
}

// acceptanceInvite permits only the named, unexpired invitee to join with the
// server-side invite role. It never authorizes an existing member role change.
func (h *OrgIntentHandler) acceptanceInvite(ctx context.Context, intent *Intent) (*domain.OrgInvite, error) {
	inviteID, err := uuid.Parse(stringField(intent.Content, "invite_id"))
	if err != nil || inviteID == uuid.Nil || h.invites == nil || h.members == nil {
		return nil, fmt.Errorf("invalid invite acceptance")
	}
	invite, err := h.invites.GetByID(ctx, intent.OrgID, inviteID)
	if err != nil || invite == nil || invite.OrgID != intent.OrgID || invite.IsExpired() ||
		normalizeEncryptedPubkey(invite.Pubkey) != intent.Actor ||
		normalizeEncryptedPubkey(stringField(intent.Content, "pubkey")) != intent.Actor ||
		string(invite.Role) != stringField(intent.Content, "role") {
		return nil, fmt.Errorf("invite is missing, expired, or does not match the signer and role")
	}
	if member, err := h.members.GetMember(ctx, intent.OrgID, intent.Actor); err != repository.ErrNotFound || member != nil {
		return nil, fmt.Errorf("invite acceptance cannot change an existing membership")
	}
	return invite, nil
}

// --- Org operations ---

func (h *OrgIntentHandler) handleOrg(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "rekey":
		if intent.Schema != "bahia.intent.org.v1" {
			return fmt.Errorf("rekey requires bahia.intent.org.v1 schema")
		}
		if h.publisher == nil {
			return fmt.Errorf("org rekey publisher is not configured")
		}
		publisher, ok := h.publisher.(interface {
			Rekey(context.Context, string) (string, int, error)
		})
		if !ok {
			return fmt.Errorf("org rekey publisher does not support refounding")
		}
		orgID := stringField(intent.Content, "org_id")
		if orgID != kinds.FleetOCKScope {
			id, err := uuid.Parse(orgID)
			if err != nil || id == uuid.Nil || id != intent.OrgID {
				return fmt.Errorf("rekey org_id must match the intent org tag")
			}
			if _, err := h.orgs.GetByID(ctx, id); err != nil {
				return fmt.Errorf("load rekey org: %w", err)
			}
		}
		if reason, exists := intent.Content["reason"]; exists {
			if _, ok := reason.(string); !ok {
				return fmt.Errorf("rekey reason must be a string")
			}
		}
		version, count, err := publisher.Rekey(ctx, orgID)
		if err != nil {
			return err
		}
		intent.StatusData = map[string]any{"key_version": version, "records_republished": count}
		intent.Result = intent.StatusData
		return nil
	case "delete":
		return h.deleteOrg(ctx, intent)
	default:
		return h.createOrUpdateOrg(ctx, intent)
	}
}

func (h *OrgIntentHandler) createOrUpdateOrg(ctx context.Context, intent *Intent) error {
	org, err := orgFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse org intent content: %w", err)
	}

	// Check expected_updated_at revision if present.
	if intent.ExpectedUpdatedAt != nil {
		return h.updateOrgWithRevision(ctx, org, intent)
	}

	// Level-triggered: try to load existing, create or update accordingly.
	existing, _ := h.orgs.GetByID(ctx, org.ID)
	if existing == nil {
		return h.createOrg(ctx, org, intent)
	}
	return h.updateOrg(ctx, existing, org, intent)
}

func (h *OrgIntentHandler) createOrg(ctx context.Context, org *domain.Organization, intent *Intent) error {
	if err := h.orgs.Create(ctx, org); err != nil {
		return fmt.Errorf("create org: %w", err)
	}

	// Add creator as owner.
	member := &domain.OrgMember{
		OrgID:  org.ID,
		Pubkey: intent.Actor,
		Role:   domain.RoleOwner,
	}
	if err := h.members.Add(ctx, member); err != nil {
		h.logger.Error("failed to add org creator as owner", zap.Error(err))
	}

	// Publish canonical state.
	if h.publisher != nil {
		if err := h.publisher.PublishOrg(ctx, org, false); err != nil {
			h.logger.Warn("failed to publish org state", zap.Error(err))
		}
		if err := h.publisher.PublishMember(ctx, member, false); err != nil {
			h.logger.Warn("failed to publish member state", zap.Error(err))
		}
	}

	h.notifyMemberChange(org.ID)

	h.logger.Info("org created via intent",
		zap.String("org_id", org.ID.String()),
		zap.String("name", org.Name),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

func (h *OrgIntentHandler) updateOrg(ctx context.Context, existing *domain.Organization, intent_org *domain.Organization, intent *Intent) error {
	if _, ok := intent.Content["strict_revocation"]; ok {
		existing.StrictRevocation = intent_org.StrictRevocation
	}
	if intent_org.DisplayName != "" {
		existing.DisplayName = intent_org.DisplayName
	}
	if intent_org.Name != "" && intent_org.Name != existing.Name {
		existing.Name = intent_org.Name
	}

	if err := h.orgs.Update(ctx, existing); err != nil {
		return fmt.Errorf("update org: %w", err)
	}

	if h.publisher != nil {
		if err := h.publisher.PublishOrg(ctx, existing, false); err != nil {
			h.logger.Warn("failed to publish org state", zap.Error(err))
		}
	}

	h.logger.Info("org updated via intent",
		zap.String("org_id", existing.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

func (h *OrgIntentHandler) updateOrgWithRevision(ctx context.Context, org *domain.Organization, intent *Intent) error {
	existing, _ := h.orgs.GetByID(ctx, org.ID)
	if existing == nil {
		if h.status != nil {
			h.status.PublishConflict(ctx, intent)
		}
		return fmt.Errorf("org %s not found for revisioned update", org.ID)
	}

	// Check revision.
	expectedTime := *intent.ExpectedUpdatedAt
	if !intent.RevisionMatches(existing.UpdatedAt) {
		if h.status != nil {
			h.status.PublishConflict(ctx, intent)
		}
		return &revisionConflictError{entityID: org.ID, expected: expectedTime, actual: existing.UpdatedAt}
	}

	return h.updateOrg(ctx, existing, org, intent)
}

func (h *OrgIntentHandler) deleteOrg(ctx context.Context, intent *Intent) error {
	idStr, _ := intent.Content["id"].(string)
	id, err := uuid.Parse(idStr)
	if err != nil || id == uuid.Nil {
		id, err = uuid.Parse(intent.Coordinate)
		if err != nil || id == uuid.Nil {
			return fmt.Errorf("delete org intent must carry entity id")
		}
	}

	org, _ := h.orgs.GetByID(ctx, id)

	if err := h.orgs.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete org: %w", err)
	}

	if h.publisher != nil && org != nil {
		if err := h.publisher.PublishOrg(ctx, org, true); err != nil {
			h.logger.Warn("failed to publish org tombstone", zap.Error(err))
		}
	}

	h.logger.Info("org deleted via intent",
		zap.String("org_id", id.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// --- Member operations ---

func (h *OrgIntentHandler) handleMember(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "delete":
		return h.removeMember(ctx, intent)
	default:
		return h.addOrUpdateMember(ctx, intent)
	}
}

func (h *OrgIntentHandler) addOrUpdateMember(ctx context.Context, intent *Intent) error {
	var acceptedInvite *domain.OrgInvite
	if stringField(intent.Content, "invite_id") != "" {
		var err error
		acceptedInvite, err = h.acceptanceInvite(ctx, intent)
		if err != nil {
			return err
		}
	}
	pubkey := normalizeEncryptedPubkey(stringField(intent.Content, "pubkey"))
	if pubkey == "" {
		return fmt.Errorf("member intent must carry pubkey")
	}

	roleStr := stringField(intent.Content, "role")
	role := domain.Role(roleStr)
	if role == "" {
		role = domain.RoleViewer
	}
	if !validEncryptedRole(role) {
		return fmt.Errorf("invalid role: %s", role)
	}

	existing, _ := h.members.GetMember(ctx, intent.OrgID, pubkey)
	if existing != nil {
		// Update role.
		if existing.Role == role {
			// A previous attempt may have committed the role but failed while
			// refounding. Repeating a strict downgrade repairs that projection
			// before this intent can be marked accepted.
			if intent.Op == "update" && role != domain.RoleOwner && h.publisher != nil && h.orgs != nil {
				org, err := h.orgs.GetByID(ctx, intent.OrgID)
				if err != nil {
					return fmt.Errorf("load org for member retry: %w", err)
				}
				if org.StrictRevocation {
					if err := h.publisher.PublishMember(ctx, existing, false, domain.RoleOwner); err != nil {
						return fmt.Errorf("repair strict member role projection: %w", err)
					}
				}
			}
			// No change; idempotent.
			h.logger.Debug("member role unchanged, idempotent",
				zap.String("org_id", intent.OrgID.String()),
				zap.String("pubkey", pubkey),
				zap.String("intent_id", intent.IntentID),
			)
			return nil
		}
		oldRole := existing.Role
		updated := *existing
		updated.Role = role
		if h.publisher != nil {
			// The relay projection is authoritative. A failed rotation must not
			// mutate even the derived repository role.
			if err := h.publisher.PublishMember(ctx, &updated, false, oldRole); err != nil {
				return fmt.Errorf("publish member role state: %w", err)
			}
		}
		if err := h.members.UpdateRole(ctx, intent.OrgID, pubkey, role); err != nil {
			return fmt.Errorf("update member role: %w", err)
		}
		h.notifyMemberChange(intent.OrgID)
		h.logger.Info("member role updated via intent",
			zap.String("org_id", intent.OrgID.String()),
			zap.String("pubkey", pubkey),
			zap.String("role", string(role)),
			zap.String("intent_id", intent.IntentID),
		)
		return nil
	}

	// Add new member.
	member := &domain.OrgMember{
		OrgID:  intent.OrgID,
		Pubkey: pubkey,
		Role:   role,
		NIP05:  stringField(intent.Content, "nip05"),
	}
	if err := h.members.Add(ctx, member); err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	if acceptedInvite != nil {
		if err := h.invites.Delete(ctx, acceptedInvite.ID); err != nil {
			h.logger.Warn("accepted invite could not be removed", zap.Error(err))
		} else if h.publisher != nil {
			if err := h.publisher.PublishInvite(ctx, acceptedInvite, true); err != nil {
				h.logger.Warn("failed to publish accepted invite tombstone", zap.Error(err))
			}
		}
	}

	if h.publisher != nil {
		if err := h.publisher.PublishMember(ctx, member, false); err != nil {
			h.logger.Warn("failed to publish member state", zap.Error(err))
		}
	}
	h.notifyMemberChange(intent.OrgID)

	h.logger.Info("member added via intent",
		zap.String("org_id", intent.OrgID.String()),
		zap.String("pubkey", pubkey),
		zap.String("role", string(role)),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

func (h *OrgIntentHandler) removeMember(ctx context.Context, intent *Intent) error {
	pubkey := normalizeEncryptedPubkey(stringField(intent.Content, "pubkey"))
	if pubkey == "" {
		return fmt.Errorf("remove member intent must carry pubkey")
	}

	existing, _ := h.members.GetMember(ctx, intent.OrgID, pubkey)
	if existing == nil {
		// A previous attempt may have removed the row but failed while
		// publishing the tombstone or refounding its old key scope.
		if h.publisher != nil && h.orgs != nil {
			org, err := h.orgs.GetByID(ctx, intent.OrgID)
			if err != nil {
				return fmt.Errorf("load org for member removal retry: %w", err)
			}
			if org.StrictRevocation {
				member := &domain.OrgMember{OrgID: intent.OrgID, Pubkey: pubkey}
				if err := h.publisher.PublishMember(ctx, member, true); err != nil {
					return fmt.Errorf("repair strict member tombstone: %w", err)
				}
			}
		}
		// Already removed; idempotent.
		return nil
	}

	// Cannot remove last owner.
	if existing.Role == domain.RoleOwner {
		members, _ := h.members.ListByOrg(ctx, intent.OrgID)
		ownerCount := 0
		for _, m := range members {
			if m.Role == domain.RoleOwner {
				ownerCount++
			}
		}
		if ownerCount <= 1 {
			return fmt.Errorf("cannot remove last owner")
		}
	}

	if h.publisher != nil {
		if err := h.publisher.PublishMember(ctx, existing, true); err != nil {
			return fmt.Errorf("publish member tombstone: %w", err)
		}
	}
	if err := h.members.Remove(ctx, intent.OrgID, pubkey); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	h.notifyMemberChange(intent.OrgID)

	h.logger.Info("member removed via intent",
		zap.String("org_id", intent.OrgID.String()),
		zap.String("pubkey", pubkey),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// --- Invite operations ---

func (h *OrgIntentHandler) handleInvite(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "delete":
		return h.revokeInvite(ctx, intent)
	default:
		return h.createInvite(ctx, intent)
	}
}

func (h *OrgIntentHandler) createInvite(ctx context.Context, intent *Intent) error {
	pubkey := normalizeEncryptedPubkey(stringField(intent.Content, "pubkey"))
	if pubkey == "" {
		return fmt.Errorf("invite intent must carry pubkey")
	}
	roleStr := stringField(intent.Content, "role")
	role := domain.Role(roleStr)
	if role == "" {
		role = domain.RoleViewer
	}
	if !validEncryptedRole(role) {
		return fmt.Errorf("invalid role: %s", role)
	}

	expiresIn := 72 // hours default
	if v, ok := intent.Content["expires_in"].(float64); ok && v > 0 {
		expiresIn = int(v)
	}

	invite := &domain.OrgInvite{
		OrgID:     intent.OrgID,
		Pubkey:    pubkey,
		Role:      role,
		InvitedBy: intent.Actor,
		ExpiresAt: time.Now().Add(time.Duration(expiresIn) * time.Hour),
	}

	// Use invite ID from content if available, otherwise from coordinate.
	if idStr := stringField(intent.Content, "id"); idStr != "" {
		if id, err := uuid.Parse(idStr); err == nil && id != uuid.Nil {
			invite.ID = id
		}
	}
	if invite.ID == uuid.Nil {
		if id, err := uuid.Parse(intent.Coordinate); err == nil && id != uuid.Nil {
			invite.ID = id
		}
	}

	if err := h.invites.Create(ctx, invite); err != nil {
		return fmt.Errorf("create invite: %w", err)
	}

	if h.publisher != nil {
		if err := h.publisher.PublishInvite(ctx, invite, false); err != nil {
			h.logger.Warn("failed to publish invite state", zap.Error(err))
		}
	}

	h.logger.Info("invite created via intent",
		zap.String("org_id", intent.OrgID.String()),
		zap.String("pubkey", pubkey),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

func (h *OrgIntentHandler) revokeInvite(ctx context.Context, intent *Intent) error {
	idStr := stringField(intent.Content, "id")
	if idStr == "" {
		idStr = intent.Coordinate
	}
	id, err := uuid.Parse(idStr)
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("revoke invite intent must carry invite id")
	}

	invite, _ := h.invites.GetByID(ctx, intent.OrgID, id)

	if err := h.invites.Delete(ctx, id); err != nil {
		if err == repository.ErrNotFound {
			// Already deleted; idempotent.
			return nil
		}
		return fmt.Errorf("revoke invite: %w", err)
	}

	if h.publisher != nil && invite != nil {
		if err := h.publisher.PublishInvite(ctx, invite, true); err != nil {
			h.logger.Warn("failed to publish invite tombstone", zap.Error(err))
		}
	}

	h.logger.Info("invite revoked via intent",
		zap.String("org_id", intent.OrgID.String()),
		zap.String("invite_id", id.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// --- Helpers ---

func (h *OrgIntentHandler) notifyMemberChange(orgID uuid.UUID) {
	if h.onMemberChange != nil {
		h.onMemberChange(orgID)
	}
}

func orgFromIntentContent(intent *Intent) (*domain.Organization, error) {
	content := intent.Content
	if content == nil {
		return nil, fmt.Errorf("intent content is nil")
	}

	org := &domain.Organization{}

	if idStr := stringField(content, "id"); idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			return nil, fmt.Errorf("invalid org id %q: %w", idStr, err)
		}
		org.ID = id
	}
	if org.ID == uuid.Nil {
		id, err := uuid.Parse(intent.Coordinate)
		if err != nil {
			return nil, fmt.Errorf("cannot derive org id from coordinate %q: %w", intent.Coordinate, err)
		}
		org.ID = id
	}

	if name := stringField(content, "name"); name != "" {
		org.Name = strings.ToLower(strings.TrimSpace(name))
		if !orgIntentNameRegex.MatchString(org.Name) {
			return nil, fmt.Errorf("invalid org name: must be 3-40 lowercase alphanumeric characters or hyphens")
		}
	}

	if displayName := stringField(content, "display_name"); displayName != "" {
		org.DisplayName = strings.TrimSpace(displayName)
	}
	if org.DisplayName == "" && org.Name != "" {
		org.DisplayName = org.Name
	}

	org.OwnerPubkey = intent.Actor
	if value, ok := content["strict_revocation"]; ok {
		strict, valid := value.(bool)
		if !valid {
			return nil, fmt.Errorf("strict_revocation must be a boolean")
		}
		org.StrictRevocation = strict
	}

	return org, nil
}

func stringField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// MemberEventHistory is a narrow interface for reading the daemon's own
// published org-member events from history. Satisfied by nostr.Projector's
// underlying ProjectionHistory, passed through app.go without the
// controlplane importing the nostr adapter package.
type MemberEventHistory interface {
	FindByTag(ctx context.Context, tagName, tagValue string, kinds []int, limit int) ([]repository.NostrEventRecord, error)
}

// RelayMemberEventHandler processes encrypted member canonical events from the
// relay subscription and updates TrustSet relay members accordingly. This is the
// production path for hydrating TrustSet from the daemon's own published
// encrypted membership events (docs/architecture/intents-and-authority.md).
//
// Two entry points:
// - HandleEncryptedMemberEvent: live, called after OrgCanonicalPublisher
// publishes a member record (both intent and compatibility ContextVM paths).
// - HydrateTrustSetFromHistory: startup, scans the daemon's own published
// member events from history and populates TrustSet before the intent
// subscriber's author filter is computed.
type RelayMemberEventHandler struct {
	confidential *ConfidentialEncryptor
	legacyO1     interface{ DecryptOrgState(string) ([]byte, error) }
	trustSet     *TrustSet
	members      repository.OrgMemberRepository
	logger       *zap.Logger
}

// NewRelayMemberEventHandler creates a handler for encrypted relay member
// events. confidential is the new per-org content key decryptor; legacyO1 is
// supports dual-read during migration.
func NewRelayMemberEventHandler(
	confidential *ConfidentialEncryptor,
	legacyO1 interface{ DecryptOrgState(string) ([]byte, error) },
	trustSet *TrustSet,
	members repository.OrgMemberRepository,
	logger *zap.Logger,
) *RelayMemberEventHandler {
	return &RelayMemberEventHandler{
		confidential: confidential,
		legacyO1:     legacyO1,
		trustSet:     trustSet,
		members:      members,
		logger:       logger,
	}
}

// HandleEncryptedMemberEvent decrypts an encrypted member event content string,
// then updates TrustSet relay members for the org. If Postgres is configured,
// it seeds the member list from the repo before applying the committed event;
// otherwise it merges the event into existing relay state. Supports both the new confidential
// format and the compatibility O1 format (dual-read during migration).
//
// legacyKind, dTag, and topic are the record's coordinate identity from the
// signed event tags. Pass zero values when unavailable (the decrypt will fall
// back to compatibility O1).
func (h *RelayMemberEventHandler) HandleEncryptedMemberEvent(ctx context.Context, content string, legacyKind int, dTag, topic string) error {
	orgID, pubkey, role, deleted, err := DecryptMemberContentConfidential(
		h.confidential, h.legacyO1, content, legacyKind, dTag, topic)
	if err != nil {
		return fmt.Errorf("decrypt member event: %w", err)
	}
	if orgID == "" || pubkey == "" {
		return fmt.Errorf("encrypted member event missing org_id or pubkey")
	}

	orgUUID, err := uuid.Parse(orgID)
	if err != nil {
		return fmt.Errorf("invalid org_id in member event: %w", err)
	}

	// Publication precedes the derived repository update. Always apply this
	// committed event, even when the repository still contains the old role.
	existing := h.trustSet.RelayMembersFor(orgID)
	if h.members != nil {
		members, err := h.members.ListByOrg(ctx, orgUUID)
		if err == nil {
			existing = make(map[string]domain.Role, len(members))
			for _, m := range members {
				existing[m.Pubkey] = m.Role
			}
		} else {
			h.logger.Debug("member repo unavailable, using single-event relay update",
				zap.String("org_id", orgID), zap.Error(err))
		}
	}

	if existing == nil {
		existing = make(map[string]domain.Role)
	}
	if deleted {
		delete(existing, pubkey)
	} else {
		existing[pubkey] = domain.Role(role)
	}
	h.trustSet.SetRelayMembers(orgID, existing)
	return nil
}

// HydrateTrustSetFromHistory scans the daemon's own published encrypted member
// events from history and populates TrustSet relay members. This is the
// startup path (docs/architecture/intents-and-authority.md): TrustSet is populated before the intent
// subscriber's author filter is computed, so relay-sourced members are included
// from the start. Runs once at startup; the live path (HandleEncryptedMemberEvent)
// keeps it in sync afterwards.
func (h *RelayMemberEventHandler) HydrateTrustSetFromHistory(ctx context.Context, history MemberEventHistory) {
	if history == nil {
		return
	}
	// Query member events by topic tag. Wire kind is 30900 (CASControlState),
	// and org member events have t=org-member.
	records, err := history.FindByTag(ctx, "t", "org-member", nil, 10000)
	if err != nil {
		h.logger.Warn("TrustSet warm-start: failed to query member history", zap.Error(err))
		return
	}
	if len(records) == 0 {
		h.logger.Debug("TrustSet warm-start: no member events in history")
		return
	}

	hydrated := 0
	for _, rec := range records {
		if err := h.HandleEncryptedMemberEvent(ctx, rec.Content, 0, "", ""); err != nil {
			h.logger.Debug("TrustSet warm-start: failed to process member event",
				zap.String("event_id", rec.ID), zap.Error(err))
			continue
		}
		hydrated++
	}
	h.logger.Info("TrustSet warm-start: hydrated relay members from history",
		zap.Int("records", len(records)),
		zap.Int("hydrated", hydrated))
}
