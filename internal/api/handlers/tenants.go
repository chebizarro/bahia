package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// TenantHandler handles organization and member management requests.
type TenantHandler struct {
	orgs                  repository.OrganizationRepository
	members               repository.OrgMemberRepository
	invites               repository.OrgInviteRepository
	rbac                  *auth.RBAC
	bootstrapOwnerPubkeys map[string]struct{}
	logger                *zap.Logger
}

// NewTenantHandler creates a new TenantHandler.
func NewTenantHandler(
	orgs repository.OrganizationRepository,
	members repository.OrgMemberRepository,
	invites repository.OrgInviteRepository,
	rbac *auth.RBAC,
	bootstrapOwnerPubkeys []string,
	logger *zap.Logger,
) *TenantHandler {
	allowlist := make(map[string]struct{}, len(bootstrapOwnerPubkeys))
	for _, pubkey := range bootstrapOwnerPubkeys {
		normalized := strings.ToLower(strings.TrimSpace(pubkey))
		if normalized == "" {
			continue
		}
		allowlist[normalized] = struct{}{}
	}
	return &TenantHandler{
		orgs:                  orgs,
		members:               members,
		invites:               invites,
		rbac:                  rbac,
		bootstrapOwnerPubkeys: allowlist,
		logger:                logger,
	}
}

// orgNameRegex validates organization names (lowercase alphanumeric with hyphens).
func (h *TenantHandler) GetOrg(w http.ResponseWriter, r *http.Request) {
	p := auth.GetPrincipal(r.Context())
	if p == nil || !p.IsAuthenticated() {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	idOrName := chi.URLParam(r, "id")

	var org *domain.Organization
	var err error

	// Try UUID first
	if id, parseErr := uuid.Parse(idOrName); parseErr == nil {
		org, err = h.orgs.GetByID(r.Context(), id)
	} else {
		org, err = h.orgs.GetByName(r.Context(), idOrName)
	}

	if err == repository.ErrNotFound {
		writeError(w, http.StatusNotFound, "organization not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch organization")
		return
	}
	if err := h.rbac.CheckOrgAccess(r.Context(), p, org.ID, domain.RoleViewer); err != nil {
		if auth.IsAccessDenied(err) {
			writeError(w, http.StatusForbidden, "access denied")
		} else {
			writeError(w, http.StatusInternalServerError, "authorization check failed")
		}
		return
	}

	writeData(w, http.StatusOK, org)
}

// ListOrgs lists organizations the current user is a member of.
// GET /orgs
func (h *TenantHandler) ListOrgs(w http.ResponseWriter, r *http.Request) {
	p := auth.GetPrincipal(r.Context())
	if p == nil || !p.IsAuthenticated() || p.PubKey == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	memberships, err := h.members.ListByPubkey(r.Context(), p.PubKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list memberships")
		return
	}

	// Fetch org details for each membership
	type orgWithRole struct {
		*domain.Organization
		Role domain.Role `json:"role"`
	}

	result := make([]orgWithRole, 0, len(memberships))
	for _, m := range memberships {
		org, err := h.orgs.GetByID(r.Context(), m.OrgID)
		if err != nil {
			continue
		}
		result = append(result, orgWithRole{Organization: org, Role: m.Role})
	}

	writeData(w, http.StatusOK, result)
}

// UpdateOrg updates an organization's settings.
// PUT /orgs/{id}
func (h *TenantHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	p := auth.GetPrincipal(r.Context())
	if p == nil || !p.IsAuthenticated() {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid org ID")
		return
	}

	// Any member can list members
	if err := h.rbac.CheckOrgAccess(r.Context(), p, orgID, domain.RoleViewer); err != nil {
		if auth.IsAccessDenied(err) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "authorization check failed")
		}
		return
	}

	members, err := h.members.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}

	writeData(w, http.StatusOK, members)
}

// AddMember adds a member to an organization.
// POST /orgs/{id}/members
func (h *TenantHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	p := auth.GetPrincipal(r.Context())
	if p == nil || !p.IsAuthenticated() || p.PubKey == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	inviteID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid invite ID")
		return
	}
	orgID, err := uuid.Parse(r.URL.Query().Get("org_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "valid org_id is required")
		return
	}

	invite, err := h.invites.GetByID(r.Context(), orgID, inviteID)
	if err == repository.ErrNotFound {
		writeError(w, http.StatusNotFound, "invite not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch invite")
		return
	}

	// Check invite is for this user
	if invite.Pubkey != p.PubKey {
		writeError(w, http.StatusForbidden, "invite is for a different user")
		return
	}

	if invite.IsExpired() {
		writeError(w, http.StatusGone, "invite has expired")
		return
	}

	// Add as member
	member := &domain.OrgMember{
		OrgID:  invite.OrgID,
		Pubkey: p.PubKey,
		Role:   invite.Role,
		NIP05:  p.NIP05,
	}

	if err := h.members.Add(r.Context(), member); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to join organization")
		return
	}

	// Delete invite
	if err := h.invites.Delete(r.Context(), inviteID); err != nil {
		h.logger.Error("failed to delete accepted invite", zap.String("invite_id", inviteID.String()), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to delete accepted invite")
		return
	}

	writeData(w, http.StatusOK, member)
}

// ListInvites lists pending invitations for an organization.
// GET /orgs/{id}/invites
func (h *TenantHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	p := auth.GetPrincipal(r.Context())
	if p == nil || !p.IsAuthenticated() {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid org ID")
		return
	}

	// Admin/owner can list invites
	if err := h.rbac.CheckOrgAccess(r.Context(), p, orgID, domain.RoleAdmin); err != nil {
		if auth.IsAccessDenied(err) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "authorization check failed")
		}
		return
	}

	invites, err := h.invites.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list invites")
		return
	}

	writeData(w, http.StatusOK, invites)
}

// MyInvites lists invitations for the current user.
// GET /invites
func (h *TenantHandler) MyInvites(w http.ResponseWriter, r *http.Request) {
	p := auth.GetPrincipal(r.Context())
	if p == nil || !p.IsAuthenticated() || p.PubKey == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	invites, err := h.invites.ListByPubkey(r.Context(), p.PubKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list invites")
		return
	}

	// Enrich with org details
	type inviteWithOrg struct {
		*domain.OrgInvite
		OrgName        string `json:"org_name"`
		OrgDisplayName string `json:"org_display_name"`
	}

	result := make([]inviteWithOrg, 0, len(invites))
	for _, inv := range invites {
		org, err := h.orgs.GetByID(r.Context(), inv.OrgID)
		if err != nil {
			continue
		}
		result = append(result, inviteWithOrg{
			OrgInvite:      &inv,
			OrgName:        org.Name,
			OrgDisplayName: org.DisplayName,
		})
	}

	writeData(w, http.StatusOK, result)
}

// RevokeInvite revokes an invitation.
// DELETE /orgs/{id}/invites/{inviteId}
