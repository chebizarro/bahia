package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

const (
	PrivateOperationPaymentsHistory  = "payments.history"
	PrivateOperationOrgsList         = "orgs.list"
	PrivateOperationOrgsDetail       = "orgs.detail"
	PrivateOperationOrgsCreate       = "orgs.create"
	PrivateOperationOrgsDelete       = "orgs.delete"
	PrivateOperationOrgsMyInvites    = "orgs.my_invites"
	PrivateOperationOrgsAcceptInvite = "orgs.accept_invite"
	PrivateOperationOrgsCreateInvite = "orgs.create_invite"
	PrivateOperationOrgsRevokeInvite = "orgs.revoke_invite"
	PrivateOperationOrgsUpdateRole   = "orgs.update_member_role"
	PrivateOperationOrgsRemoveMember = "orgs.remove_member"
)

var privateOrgNameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// PrivateDomainHandlers adapts sensitive REST-backed domains to encrypted
// signer-first request/result operations. Payloads stay encrypted end-to-end and
// are never emitted as public sidecar read models.
type PrivateDomainHandlers struct {
	payments              *service.PaymentService
	orgs                  repository.OrganizationRepository
	members               repository.OrgMemberRepository
	invites               repository.OrgInviteRepository
	rbac                  *auth.RBAC
	bootstrapOwnerPubkeys map[string]struct{}
	logger                *zap.Logger
}

type PrivateDomainHandlersConfig struct {
	Payments              *service.PaymentService
	Orgs                  repository.OrganizationRepository
	Members               repository.OrgMemberRepository
	Invites               repository.OrgInviteRepository
	RBAC                  *auth.RBAC
	BootstrapOwnerPubkeys []string
	Logger                *zap.Logger
}

func NewPrivateDomainHandlers(cfg PrivateDomainHandlersConfig) *PrivateDomainHandlers {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	allowlist := make(map[string]struct{}, len(cfg.BootstrapOwnerPubkeys))
	for _, pubkey := range cfg.BootstrapOwnerPubkeys {
		normalized := normalizePrivatePubkey(pubkey)
		if normalized != "" {
			allowlist[normalized] = struct{}{}
		}
	}
	return &PrivateDomainHandlers{
		payments:              cfg.Payments,
		orgs:                  cfg.Orgs,
		members:               cfg.Members,
		invites:               cfg.Invites,
		rbac:                  cfg.RBAC,
		bootstrapOwnerPubkeys: allowlist,
		logger:                logger.Named("private-domain-handlers"),
	}
}

func (h *PrivateDomainHandlers) Register(transport *PrivateTransport) {
	if h == nil || transport == nil {
		return
	}
	transport.RegisterHandler(PrivateOperationPaymentsHistory, h.PaymentHistory)
	transport.RegisterHandler(PrivateOperationOrgsList, h.ListOrgs)
	transport.RegisterHandler(PrivateOperationOrgsDetail, h.OrgDetail)
	transport.RegisterHandler(PrivateOperationOrgsCreate, h.CreateOrg)
	transport.RegisterHandler(PrivateOperationOrgsDelete, h.DeleteOrg)
	transport.RegisterHandler(PrivateOperationOrgsMyInvites, h.MyInvites)
	transport.RegisterHandler(PrivateOperationOrgsAcceptInvite, h.AcceptInvite)
	transport.RegisterHandler(PrivateOperationOrgsCreateInvite, h.CreateInvite)
	transport.RegisterHandler(PrivateOperationOrgsRevokeInvite, h.RevokeInvite)
	transport.RegisterHandler(PrivateOperationOrgsUpdateRole, h.UpdateMemberRole)
	transport.RegisterHandler(PrivateOperationOrgsRemoveMember, h.RemoveMember)
}

func (h *PrivateDomainHandlers) PaymentHistory(ctx context.Context, request PrivateRequest) (any, error) {
	if h.payments == nil {
		return nil, fmt.Errorf("payments service is not configured")
	}
	var payload struct {
		Worker string `json:"worker"`
		Limit  int    `json:"limit"`
	}
	if err := decodePrivatePayload(request, &payload); err != nil {
		return nil, err
	}
	worker := strings.TrimSpace(payload.Worker)
	if worker == "" {
		return nil, fmt.Errorf("worker is required")
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 250 {
		limit = 250
	}
	records, err := h.payments.GetPaymentHistory(ctx, worker, limit)
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (h *PrivateDomainHandlers) ListOrgs(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	principal := requestPrincipal(request)
	memberships, err := h.members.ListByPubkey(ctx, principal.PubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to list memberships: %w", err)
	}
	type orgWithRole struct {
		*domain.Organization
		Role domain.Role `json:"role"`
	}
	result := make([]orgWithRole, 0, len(memberships))
	for _, membership := range memberships {
		org, err := h.orgs.GetByID(ctx, membership.OrgID)
		if err != nil {
			continue
		}
		result = append(result, orgWithRole{Organization: org, Role: membership.Role})
	}
	return result, nil
}

func (h *PrivateDomainHandlers) OrgDetail(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := decodePrivatePayload(request, &payload); err != nil {
		return nil, err
	}
	org, err := h.lookupOrg(ctx, payload.ID)
	if err != nil {
		return nil, err
	}
	principal := requestPrincipal(request)
	if err := h.rbac.CheckOrgAccess(ctx, principal, org.ID, domain.RoleViewer); err != nil {
		return nil, err
	}
	members, err := h.members.ListByOrg(ctx, org.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to list members: %w", err)
	}
	myRole := ""
	for _, member := range members {
		if normalizePrivatePubkey(member.Pubkey) == principal.PubKey {
			myRole = string(member.Role)
			break
		}
	}
	var invites []domain.OrgInvite
	if err := h.rbac.CheckOrgAccess(ctx, principal, org.ID, domain.RoleAdmin); err == nil {
		invites, err = h.invites.ListByOrg(ctx, org.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to list invites: %w", err)
		}
	}
	return map[string]any{
		"org":     org,
		"members": members,
		"invites": invites,
		"my_role": myRole,
	}, nil
}

func (h *PrivateDomainHandlers) CreateOrg(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	principal := requestPrincipal(request)
	if len(h.bootstrapOwnerPubkeys) > 0 {
		if _, allowed := h.bootstrapOwnerPubkeys[principal.PubKey]; !allowed {
			return nil, fmt.Errorf("organization creation requires a configured bootstrap owner pubkey")
		}
	}
	var payload struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	}
	if err := decodePrivatePayload(request, &payload); err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(payload.Name))
	if !privateOrgNameRegex.MatchString(name) {
		return nil, fmt.Errorf("invalid org name: must be 3-40 lowercase alphanumeric characters or hyphens, starting and ending with alphanumeric")
	}
	if _, err := h.orgs.GetByName(ctx, name); err == nil {
		return nil, fmt.Errorf("organization name already exists")
	}
	displayName := strings.TrimSpace(payload.DisplayName)
	if displayName == "" {
		displayName = name
	}
	org := &domain.Organization{ID: uuid.New(), Name: name, DisplayName: displayName, OwnerPubkey: principal.PubKey}
	if err := h.orgs.Create(ctx, org); err != nil {
		h.logger.Error("failed to create org from private transport", zap.Error(err))
		return nil, fmt.Errorf("failed to create organization")
	}
	member := &domain.OrgMember{OrgID: org.ID, Pubkey: principal.PubKey, Role: domain.RoleOwner}
	if err := h.members.Add(ctx, member); err != nil {
		h.logger.Error("failed to add private transport org creator as owner", zap.Error(err))
	}
	return org, nil
}

func (h *PrivateDomainHandlers) DeleteOrg(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := decodePrivatePayload(request, &payload); err != nil {
		return nil, err
	}
	orgID, err := parsePrivateUUID(payload.ID, "org ID")
	if err != nil {
		return nil, err
	}
	if err := h.rbac.CheckOrgAccess(ctx, requestPrincipal(request), orgID, domain.RoleOwner); err != nil {
		return nil, err
	}
	if err := h.orgs.Delete(ctx, orgID); err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("organization not found")
		}
		return nil, fmt.Errorf("failed to delete organization: %w", err)
	}
	return map[string]string{"message": "organization deleted"}, nil
}

func (h *PrivateDomainHandlers) MyInvites(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	principal := requestPrincipal(request)
	invites, err := h.invites.ListByPubkey(ctx, principal.PubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to list invites: %w", err)
	}
	type inviteWithOrg struct {
		*domain.OrgInvite
		OrgName        string `json:"org_name"`
		OrgDisplayName string `json:"org_display_name"`
	}
	result := make([]inviteWithOrg, 0, len(invites))
	for _, invite := range invites {
		org, err := h.orgs.GetByID(ctx, invite.OrgID)
		if err != nil {
			continue
		}
		copy := invite
		result = append(result, inviteWithOrg{OrgInvite: &copy, OrgName: org.Name, OrgDisplayName: org.DisplayName})
	}
	return result, nil
}

func (h *PrivateDomainHandlers) AcceptInvite(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	var payload struct {
		InviteID string `json:"invite_id"`
	}
	if err := decodePrivatePayload(request, &payload); err != nil {
		return nil, err
	}
	inviteID, err := parsePrivateUUID(payload.InviteID, "invite ID")
	if err != nil {
		return nil, err
	}
	invite, err := h.invites.GetByID(ctx, inviteID)
	if err == repository.ErrNotFound {
		return nil, fmt.Errorf("invite not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch invite: %w", err)
	}
	principal := requestPrincipal(request)
	if normalizePrivatePubkey(invite.Pubkey) != principal.PubKey {
		return nil, fmt.Errorf("invite is for a different user")
	}
	if invite.IsExpired() {
		return nil, fmt.Errorf("invite has expired")
	}
	member := &domain.OrgMember{OrgID: invite.OrgID, Pubkey: principal.PubKey, Role: invite.Role}
	if err := h.members.Add(ctx, member); err != nil {
		return nil, fmt.Errorf("failed to join organization: %w", err)
	}
	_ = h.invites.Delete(ctx, inviteID)
	return member, nil
}

func (h *PrivateDomainHandlers) CreateInvite(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	var payload struct {
		OrgID     string      `json:"org_id"`
		Pubkey    string      `json:"pubkey"`
		Role      domain.Role `json:"role"`
		ExpiresIn int         `json:"expires_in"` // hours, same contract as REST handler
	}
	if err := decodePrivatePayload(request, &payload); err != nil {
		return nil, err
	}
	orgID, err := parsePrivateUUID(payload.OrgID, "org ID")
	if err != nil {
		return nil, err
	}
	principal := requestPrincipal(request)
	if err := h.rbac.CheckOrgAccess(ctx, principal, orgID, domain.RoleAdmin); err != nil {
		return nil, err
	}
	pubkey := normalizePrivatePubkey(payload.Pubkey)
	if pubkey == "" {
		return nil, fmt.Errorf("pubkey is required")
	}
	role := payload.Role
	if role == "" {
		role = domain.RoleViewer
	}
	if !validPrivateRole(role) {
		return nil, fmt.Errorf("invalid role")
	}
	authz, _ := h.rbac.LoadAuthzContext(ctx, principal, orgID)
	if role == domain.RoleOwner && !authz.IsOwner() {
		return nil, fmt.Errorf("only owners can grant owner role")
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 72
	}
	invite := &domain.OrgInvite{OrgID: orgID, Pubkey: pubkey, Role: role, InvitedBy: principal.PubKey, ExpiresAt: time.Now().Add(time.Duration(expiresIn) * time.Hour)}
	if err := h.invites.Create(ctx, invite); err != nil {
		return nil, fmt.Errorf("failed to create invite: %w", err)
	}
	return invite, nil
}

func (h *PrivateDomainHandlers) RevokeInvite(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	var payload struct {
		OrgID    string `json:"org_id"`
		InviteID string `json:"invite_id"`
	}
	if err := decodePrivatePayload(request, &payload); err != nil {
		return nil, err
	}
	orgID, err := parsePrivateUUID(payload.OrgID, "org ID")
	if err != nil {
		return nil, err
	}
	inviteID, err := parsePrivateUUID(payload.InviteID, "invite ID")
	if err != nil {
		return nil, err
	}
	if err := h.rbac.CheckOrgAccess(ctx, requestPrincipal(request), orgID, domain.RoleAdmin); err != nil {
		return nil, err
	}
	if err := h.invites.Delete(ctx, inviteID); err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("invite not found")
		}
		return nil, fmt.Errorf("failed to revoke invite: %w", err)
	}
	return map[string]string{"message": "invite revoked"}, nil
}

func (h *PrivateDomainHandlers) UpdateMemberRole(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	var payload struct {
		OrgID  string      `json:"org_id"`
		Pubkey string      `json:"pubkey"`
		Role   domain.Role `json:"role"`
	}
	if err := decodePrivatePayload(request, &payload); err != nil {
		return nil, err
	}
	orgID, err := parsePrivateUUID(payload.OrgID, "org ID")
	if err != nil {
		return nil, err
	}
	targetPubkey := normalizePrivatePubkey(payload.Pubkey)
	if targetPubkey == "" {
		return nil, fmt.Errorf("pubkey is required")
	}
	if !validPrivateRole(payload.Role) {
		return nil, fmt.Errorf("invalid role")
	}
	principal := requestPrincipal(request)
	if err := h.rbac.CheckOrgAccess(ctx, principal, orgID, domain.RoleAdmin); err != nil {
		return nil, err
	}
	authz, _ := h.rbac.LoadAuthzContext(ctx, principal, orgID)
	if payload.Role == domain.RoleOwner && !authz.IsOwner() {
		return nil, fmt.Errorf("only owners can grant owner role")
	}
	targetMember, err := h.members.GetMember(ctx, orgID, targetPubkey)
	if err == repository.ErrNotFound {
		return nil, fmt.Errorf("member not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch member: %w", err)
	}
	if targetMember.Role == domain.RoleOwner && !authz.IsOwner() {
		return nil, fmt.Errorf("only owners can demote other owners")
	}
	if err := h.members.UpdateRole(ctx, orgID, targetPubkey, payload.Role); err != nil {
		return nil, fmt.Errorf("failed to update role: %w", err)
	}
	return map[string]string{"message": "role updated"}, nil
}

func (h *PrivateDomainHandlers) RemoveMember(ctx context.Context, request PrivateRequest) (any, error) {
	if err := h.requireOrgDeps(); err != nil {
		return nil, err
	}
	var payload struct {
		OrgID  string `json:"org_id"`
		Pubkey string `json:"pubkey"`
	}
	if err := decodePrivatePayload(request, &payload); err != nil {
		return nil, err
	}
	orgID, err := parsePrivateUUID(payload.OrgID, "org ID")
	if err != nil {
		return nil, err
	}
	targetPubkey := normalizePrivatePubkey(payload.Pubkey)
	if targetPubkey == "" {
		return nil, fmt.Errorf("pubkey is required")
	}
	principal := requestPrincipal(request)
	if targetPubkey != principal.PubKey {
		if err := h.rbac.CheckOrgAccess(ctx, principal, orgID, domain.RoleAdmin); err != nil {
			return nil, err
		}
	}
	targetMember, err := h.members.GetMember(ctx, orgID, targetPubkey)
	if err == repository.ErrNotFound {
		return nil, fmt.Errorf("member not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch member: %w", err)
	}
	if targetMember.Role == domain.RoleOwner {
		members, _ := h.members.ListByOrg(ctx, orgID)
		ownerCount := 0
		for _, member := range members {
			if member.Role == domain.RoleOwner {
				ownerCount++
			}
		}
		if ownerCount <= 1 {
			return nil, fmt.Errorf("cannot remove last owner")
		}
	}
	if err := h.members.Remove(ctx, orgID, targetPubkey); err != nil {
		return nil, fmt.Errorf("failed to remove member: %w", err)
	}
	return map[string]string{"message": "member removed"}, nil
}

func (h *PrivateDomainHandlers) lookupOrg(ctx context.Context, idOrName string) (*domain.Organization, error) {
	idOrName = strings.TrimSpace(idOrName)
	if idOrName == "" {
		return nil, fmt.Errorf("org ID is required")
	}
	if id, err := uuid.Parse(idOrName); err == nil {
		org, err := h.orgs.GetByID(ctx, id)
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("organization not found")
		}
		return org, err
	}
	org, err := h.orgs.GetByName(ctx, idOrName)
	if err == repository.ErrNotFound {
		return nil, fmt.Errorf("organization not found")
	}
	return org, err
}

func (h *PrivateDomainHandlers) requireOrgDeps() error {
	if h.orgs == nil || h.members == nil || h.invites == nil || h.rbac == nil {
		return fmt.Errorf("organization service is not configured")
	}
	return nil
}

func requestPrincipal(request PrivateRequest) *auth.Principal {
	pubkey := normalizePrivatePubkey(request.Event.PubKey)
	return &auth.Principal{Subject: pubkey, Method: auth.MethodNIP98, PubKey: pubkey}
}

func decodePrivatePayload(request PrivateRequest, out any) error {
	if len(request.Envelope.Payload) == 0 || string(request.Envelope.Payload) == "null" {
		return nil
	}
	if err := json.Unmarshal(request.Envelope.Payload, out); err != nil {
		return fmt.Errorf("invalid private request payload: %w", err)
	}
	return nil
}

func parsePrivateUUID(value, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s", field)
	}
	return id, nil
}

func normalizePrivatePubkey(pubkey string) string {
	return strings.ToLower(strings.TrimSpace(pubkey))
}

func validPrivateRole(role domain.Role) bool {
	for _, candidate := range domain.AllRoles() {
		if candidate == role {
			return true
		}
	}
	return false
}
