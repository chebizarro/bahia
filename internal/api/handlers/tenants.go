package handlers

import (
	"net/http"
	"regexp"
	"strings"
	"time"

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
var orgNameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// CreateOrg creates a new organization.
// POST /orgs
func (h *TenantHandler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	p := auth.GetPrincipal(r.Context())
	if p == nil || !p.IsAuthenticated() || p.PubKey == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	principalPubkey := strings.ToLower(strings.TrimSpace(p.PubKey))
	if principalPubkey == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if len(h.bootstrapOwnerPubkeys) > 0 {
		if _, allowed := h.bootstrapOwnerPubkeys[principalPubkey]; !allowed {
			writeError(w, http.StatusForbidden, "organization creation requires a configured bootstrap owner pubkey")
			return
		}
	}

	var req struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate name
	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	if !orgNameRegex.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid org name: must be 3-40 lowercase alphanumeric characters or hyphens, starting and ending with alphanumeric")
		return
	}

	// Check for duplicate name
	if _, err := h.orgs.GetByName(r.Context(), req.Name); err == nil {
		writeError(w, http.StatusConflict, "organization name already exists")
		return
	}

	org := &domain.Organization{
		ID:          uuid.New(),
		Name:        req.Name,
		DisplayName: strings.TrimSpace(req.DisplayName),
		OwnerPubkey: principalPubkey,
	}
	if org.DisplayName == "" {
		org.DisplayName = org.Name
	}

	if err := h.orgs.Create(r.Context(), org); err != nil {
		h.logger.Error("failed to create org", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to create organization")
		return
	}

	// Add creator as owner
	member := &domain.OrgMember{
		OrgID:  org.ID,
		Pubkey: principalPubkey,
		Role:   domain.RoleOwner,
		NIP05:  p.NIP05,
	}
	if err := h.members.Add(r.Context(), member); err != nil {
		h.logger.Error("failed to add owner as member", zap.Error(err))
		// Don't fail - org was created
	}

	h.logger.Info("organization created",
		zap.String("org_id", org.ID.String()),
		zap.String("name", org.Name),
		zap.String("owner", principalPubkey),
	)

	writeData(w, http.StatusCreated, org)
}

// GetOrg retrieves an organization by ID or name.
// GET /orgs/{id}
func (h *TenantHandler) GetOrg(w http.ResponseWriter, r *http.Request) {
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
func (h *TenantHandler) UpdateOrg(w http.ResponseWriter, r *http.Request) {
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

	// Check owner permission
	if err := h.rbac.CheckOrgAccess(r.Context(), p, orgID, domain.RoleOwner); err != nil {
		if auth.IsAccessDenied(err) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "authorization check failed")
		}
		return
	}

	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	org, err := h.orgs.GetByID(r.Context(), orgID)
	if err == repository.ErrNotFound {
		writeError(w, http.StatusNotFound, "organization not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch organization")
		return
	}

	if req.DisplayName != "" {
		org.DisplayName = strings.TrimSpace(req.DisplayName)
	}

	if err := h.orgs.Update(r.Context(), org); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update organization")
		return
	}

	writeData(w, http.StatusOK, org)
}

// DeleteOrg deletes an organization.
// DELETE /orgs/{id}
func (h *TenantHandler) DeleteOrg(w http.ResponseWriter, r *http.Request) {
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

	// Only owner can delete
	if err := h.rbac.CheckOrgAccess(r.Context(), p, orgID, domain.RoleOwner); err != nil {
		if auth.IsAccessDenied(err) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "authorization check failed")
		}
		return
	}

	if err := h.orgs.Delete(r.Context(), orgID); err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "organization not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to delete organization")
		}
		return
	}

	writeMessage(w, http.StatusOK, "organization deleted")
}

// ListMembers lists members of an organization.
// GET /orgs/{id}/members
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
func (h *TenantHandler) AddMember(w http.ResponseWriter, r *http.Request) {
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

	// Only admin/owner can add members
	if err := h.rbac.CheckOrgAccess(r.Context(), p, orgID, domain.RoleAdmin); err != nil {
		if auth.IsAccessDenied(err) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "authorization check failed")
		}
		return
	}

	var req struct {
		Pubkey string      `json:"pubkey"`
		Role   domain.Role `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Pubkey == "" {
		writeError(w, http.StatusBadRequest, "pubkey is required")
		return
	}

	// Validate role
	if req.Role == "" {
		req.Role = domain.RoleViewer
	}
	validRole := false
	for _, r := range domain.AllRoles() {
		if r == req.Role {
			validRole = true
			break
		}
	}
	if !validRole {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}

	// Non-owners cannot grant owner role
	authz, _ := h.rbac.LoadAuthzContext(r.Context(), p, orgID)
	if req.Role == domain.RoleOwner && !authz.IsOwner() {
		writeError(w, http.StatusForbidden, "only owners can grant owner role")
		return
	}

	member := &domain.OrgMember{
		OrgID:  orgID,
		Pubkey: req.Pubkey,
		Role:   req.Role,
	}

	if err := h.members.Add(r.Context(), member); err != nil {
		h.logger.Error("failed to add member", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to add member")
		return
	}

	h.logger.Info("member added",
		zap.String("org_id", orgID.String()),
		zap.String("pubkey", req.Pubkey),
		zap.String("role", string(req.Role)),
	)

	writeData(w, http.StatusCreated, member)
}

// UpdateMemberRole updates a member's role.
// PUT /orgs/{id}/members/{pubkey}
func (h *TenantHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
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

	targetPubkey := chi.URLParam(r, "pubkey")

	// Only admin/owner can update roles
	if err := h.rbac.CheckOrgAccess(r.Context(), p, orgID, domain.RoleAdmin); err != nil {
		if auth.IsAccessDenied(err) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "authorization check failed")
		}
		return
	}

	var req struct {
		Role domain.Role `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate role
	validRole := false
	for _, r := range domain.AllRoles() {
		if r == req.Role {
			validRole = true
			break
		}
	}
	if !validRole {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}

	authz, _ := h.rbac.LoadAuthzContext(r.Context(), p, orgID)

	// Non-owners cannot grant or revoke owner role
	if req.Role == domain.RoleOwner && !authz.IsOwner() {
		writeError(w, http.StatusForbidden, "only owners can grant owner role")
		return
	}

	// Check if target is currently an owner
	targetMember, err := h.members.GetMember(r.Context(), orgID, targetPubkey)
	if err == repository.ErrNotFound {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch member")
		return
	}

	if targetMember.Role == domain.RoleOwner && !authz.IsOwner() {
		writeError(w, http.StatusForbidden, "only owners can demote other owners")
		return
	}

	if err := h.members.UpdateRole(r.Context(), orgID, targetPubkey, req.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update role")
		return
	}

	writeMessage(w, http.StatusOK, "role updated")
}

// RemoveMember removes a member from an organization.
// DELETE /orgs/{id}/members/{pubkey}
func (h *TenantHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
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

	targetPubkey := chi.URLParam(r, "pubkey")

	// Users can remove themselves, admins/owners can remove others
	if targetPubkey != p.PubKey {
		if err := h.rbac.CheckOrgAccess(r.Context(), p, orgID, domain.RoleAdmin); err != nil {
			if auth.IsAccessDenied(err) {
				writeError(w, http.StatusForbidden, err.Error())
			} else {
				writeError(w, http.StatusInternalServerError, "authorization check failed")
			}
			return
		}
	}

	// Cannot remove last owner
	targetMember, err := h.members.GetMember(r.Context(), orgID, targetPubkey)
	if err == repository.ErrNotFound {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch member")
		return
	}

	if targetMember.Role == domain.RoleOwner {
		// Count owners
		members, _ := h.members.ListByOrg(r.Context(), orgID)
		ownerCount := 0
		for _, m := range members {
			if m.Role == domain.RoleOwner {
				ownerCount++
			}
		}
		if ownerCount <= 1 {
			writeError(w, http.StatusBadRequest, "cannot remove last owner")
			return
		}
	}

	if err := h.members.Remove(r.Context(), orgID, targetPubkey); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove member")
		return
	}

	writeMessage(w, http.StatusOK, "member removed")
}

// CreateInvite creates an invitation to join an organization.
// POST /orgs/{id}/invites
func (h *TenantHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
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

	// Admin/owner can invite
	if err := h.rbac.CheckOrgAccess(r.Context(), p, orgID, domain.RoleAdmin); err != nil {
		if auth.IsAccessDenied(err) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "authorization check failed")
		}
		return
	}

	var req struct {
		Pubkey    string      `json:"pubkey"`
		Role      domain.Role `json:"role"`
		ExpiresIn int         `json:"expires_in"` // hours, default 72
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Pubkey == "" {
		writeError(w, http.StatusBadRequest, "pubkey is required")
		return
	}

	if req.Role == "" {
		req.Role = domain.RoleViewer
	}
	if req.ExpiresIn <= 0 {
		req.ExpiresIn = 72
	}

	invite := &domain.OrgInvite{
		OrgID:     orgID,
		Pubkey:    req.Pubkey,
		Role:      req.Role,
		InvitedBy: p.PubKey,
		ExpiresAt: time.Now().Add(time.Duration(req.ExpiresIn) * time.Hour),
	}

	if err := h.invites.Create(r.Context(), invite); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create invite")
		return
	}

	writeData(w, http.StatusCreated, invite)
}

// AcceptInvite accepts an invitation to join an organization.
// POST /invites/{id}/accept
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

	invite, err := h.invites.GetByID(r.Context(), inviteID)
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
	h.invites.Delete(r.Context(), inviteID)

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
func (h *TenantHandler) RevokeInvite(w http.ResponseWriter, r *http.Request) {
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

	// Admin/owner can revoke invites
	if err := h.rbac.CheckOrgAccess(r.Context(), p, orgID, domain.RoleAdmin); err != nil {
		if auth.IsAccessDenied(err) {
			writeError(w, http.StatusForbidden, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "authorization check failed")
		}
		return
	}

	inviteID, err := uuid.Parse(chi.URLParam(r, "inviteId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid invite ID")
		return
	}

	if err := h.invites.Delete(r.Context(), inviteID); err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "invite not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to revoke invite")
		}
		return
	}

	writeMessage(w, http.StatusOK, "invite revoked")
}
