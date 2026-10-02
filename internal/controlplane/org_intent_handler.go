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
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// OrgCanonicalPublisher is the narrow interface the org intent handler uses to
// publish canonical cp-state for org, member, and invite entities. Implemented
// by internal/adapters/nostr.OrgCanonicalPublisher.
type OrgCanonicalPublisher interface {
	PublishOrg(ctx context.Context, org *domain.Organization, deleted bool) error
	PublishMember(ctx context.Context, member *domain.OrgMember, deleted bool) error
	PublishInvite(ctx context.Context, invite *domain.OrgInvite, deleted bool) error
}

// OrgMemberChangeCallback is invoked after every member add/remove/role-change
// so the TrustSet relay source and IntentAuthorsSyncer stay in sync.
type OrgMemberChangeCallback func(orgID uuid.UUID)

// OrgIntentHandler processes org/member/invite intents. It is the O1 domain
// handler for the Phase 3 intent framework.
//
// Level-triggered: the newest trusted intent's full desired state wins.
// Idempotent by intent_id (handled by the intent processor).
// Authorization is per-operation: fleet-ops for org create, per-org RBAC for
// everything else (SelfAuthorizingHandler).
//
// See design §7 Wave 5 O1 and §10.
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
// at startup and on live membership events.
func DecryptMemberContent(encryptor interface{ DecryptOrgState(string) ([]byte, error) }, content string) (orgID string, pubkey string, role string, deleted bool, err error) {
	plaintext, err := encryptor.DecryptOrgState(content)
	if err != nil {
		return "", "", "", false, err
	}
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
//   - Org create: actor must be a fleet operator (design §7 Wave 5 O1)
//   - Org update/delete: actor must have PermManageSettings in the org
//   - Member/invite ops: actor must have PermManageMembers in the org
func (h *OrgIntentHandler) AuthorizeIntent(ctx context.Context, trustSet *TrustSet, intent *Intent) error {
	sub := classifyOrgIntent(intent)
	switch sub {
	case orgSubOrg:
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
		if !trustSet.HasPermission(ctx, intent.OrgID, intent.Actor, domain.PermManageMembers) {
			return fmt.Errorf("insufficient permission: %s", domain.PermManageMembers)
		}
		return nil

	default:
		return fmt.Errorf("unknown org sub-entity")
	}
}

// --- Org operations ---

func (h *OrgIntentHandler) handleOrg(ctx context.Context, intent *Intent) error {
	switch intent.Op {
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
	expectedNanos := *intent.ExpectedUpdatedAt
	expectedTime := time.Unix(0, expectedNanos)
	if raw, ok := intent.Content["expected_updated_at"]; ok {
		if v, ok := raw.(string); ok {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, v); parseErr == nil {
				expectedTime = parsed
			}
		}
	}
	if !existing.UpdatedAt.Equal(expectedTime) {
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
			// No change; idempotent.
			h.logger.Debug("member role unchanged, idempotent",
				zap.String("org_id", intent.OrgID.String()),
				zap.String("pubkey", pubkey),
				zap.String("intent_id", intent.IntentID),
			)
			return nil
		}
		if err := h.members.UpdateRole(ctx, intent.OrgID, pubkey, role); err != nil {
			return fmt.Errorf("update member role: %w", err)
		}
		existing.Role = role
		if h.publisher != nil {
			if err := h.publisher.PublishMember(ctx, existing, false); err != nil {
				h.logger.Warn("failed to publish member state", zap.Error(err))
			}
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

	if err := h.members.Remove(ctx, intent.OrgID, pubkey); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}

	if h.publisher != nil {
		if err := h.publisher.PublishMember(ctx, existing, true); err != nil {
			h.logger.Warn("failed to publish member tombstone", zap.Error(err))
		}
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

// RelayMemberEventHandler processes encrypted member canonical events from the
// relay subscription and updates TrustSet relay members accordingly. This is the
// production path for hydrating TrustSet from the daemon's own published
// encrypted membership events (design §2.5 item 3).
type RelayMemberEventHandler struct {
	encryptor interface{ DecryptOrgState(string) ([]byte, error) }
	trustSet  *TrustSet
	members   repository.OrgMemberRepository
	logger    *zap.Logger
}

// NewRelayMemberEventHandler creates a handler for encrypted relay member events.
func NewRelayMemberEventHandler(
	encryptor interface{ DecryptOrgState(string) ([]byte, error) },
	trustSet *TrustSet,
	members repository.OrgMemberRepository,
	logger *zap.Logger,
) *RelayMemberEventHandler {
	return &RelayMemberEventHandler{
		encryptor: encryptor,
		trustSet:  trustSet,
		members:   members,
		logger:    logger,
	}
}

// HandleEncryptedMemberEvent decrypts an encrypted member event content string,
// then updates TrustSet relay members for the org. If Postgres is configured,
// it reads the full member list from the repo; if not, it merges the single
// event into the existing relay state.
func (h *RelayMemberEventHandler) HandleEncryptedMemberEvent(ctx context.Context, content string) error {
	orgID, pubkey, role, deleted, err := DecryptMemberContent(h.encryptor, content)
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

	// If we have a member repo, rebuild from authoritative source.
	if h.members != nil {
		members, err := h.members.ListByOrg(ctx, orgUUID)
		if err == nil {
			roleMap := make(map[string]domain.Role, len(members))
			for _, m := range members {
				roleMap[m.Pubkey] = m.Role
			}
			h.trustSet.SetRelayMembers(orgID, roleMap)
			return nil
		}
		h.logger.Debug("member repo unavailable, using single-event relay update",
			zap.String("org_id", orgID), zap.Error(err))
	}

	// No member repo: merge single event into relay state.
	// Read existing relay members, apply change, write back.
	existing := h.trustSet.RelayMembersFor(orgID)
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
