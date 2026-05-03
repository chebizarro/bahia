package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

type ToolHandler struct {
	repo     repository.ToolProvisioningRepository
	registry *service.RegistryService
}

func NewToolHandler(repo repository.ToolProvisioningRepository, registry *service.RegistryService) *ToolHandler {
	return &ToolHandler{repo: repo, registry: registry}
}

type toolDecisionRequest struct {
	Reason string `json:"reason"`
}

type toolDenylistRequest struct {
	Package string `json:"package"`
	Manager string `json:"manager"`
	Reason  string `json:"reason"`
}

func (h *ToolHandler) ListPending(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	intents, err := h.repo.ListPendingApprovalIntents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pending tool approvals")
		return
	}
	writeData(w, http.StatusOK, intents)
}

func (h *ToolHandler) GetIntent(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	intent, err := h.repo.GetIntent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get tool intent")
		return
	}
	if intent == nil {
		writeError(w, http.StatusNotFound, "tool intent not found")
		return
	}
	writeData(w, http.StatusOK, intent)
}

func (h *ToolHandler) ApproveIntent(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermApproveDeployments) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	var req toolDecisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	intent, err := h.repo.GetIntent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get tool intent")
		return
	}
	if intent == nil {
		writeError(w, http.StatusNotFound, "tool intent not found")
		return
	}
	now := time.Now().UTC()
	intent.Status = domain.ToolProvisionStatusApproved
	intent.ApprovedAt = &now
	intent.ApprovedBy = toolActor(r)
	if err := h.repo.UpdateIntent(r.Context(), intent); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tool intent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update tool intent")
		return
	}
	_ = h.repo.LogApproval(r.Context(), id, "approved", toolActor(r), req.Reason)
	writeMessage(w, http.StatusOK, "tool intent approved")
}

func (h *ToolHandler) RejectIntent(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermApproveDeployments) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid intent id")
		return
	}
	var req toolDecisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	intent, err := h.repo.GetIntent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get tool intent")
		return
	}
	if intent == nil {
		writeError(w, http.StatusNotFound, "tool intent not found")
		return
	}
	intent.Status = domain.ToolProvisionStatusRejected
	if err := h.repo.UpdateIntent(r.Context(), intent); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "tool intent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update tool intent")
		return
	}
	_ = h.repo.LogApproval(r.Context(), id, "rejected", toolActor(r), req.Reason)
	writeMessage(w, http.StatusOK, "tool intent rejected")
}

func (h *ToolHandler) ListDenylist(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	entries, err := h.repo.ListDenylist(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list denylist")
		return
	}
	writeData(w, http.StatusOK, entries)
}

func (h *ToolHandler) AddDenylist(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermApproveDeployments) {
		return
	}
	var req toolDenylistRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Package == "" || req.Manager == "" || req.Reason == "" {
		writeError(w, http.StatusBadRequest, "package, manager, and reason are required")
		return
	}
	entry := &domain.ToolDenylistEntry{PackageName: req.Package, Manager: req.Manager, Reason: req.Reason, BlockedBy: toolActor(r)}
	if err := h.repo.AddToDenylist(r.Context(), entry); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add denylist entry")
		return
	}
	writeData(w, http.StatusCreated, entry)
}

func (h *ToolHandler) RemoveDenylist(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermApproveDeployments) {
		return
	}
	pkg := chi.URLParam(r, "package")
	manager := chi.URLParam(r, "manager")
	if pkg == "" || manager == "" {
		writeError(w, http.StatusBadRequest, "package and manager are required")
		return
	}
	if err := h.repo.RemoveFromDenylist(r.Context(), pkg, manager); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove denylist entry")
		return
	}
	writeMessage(w, http.StatusOK, "denylist entry removed")
}

func (h *ToolHandler) GetProfile(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	envIDStr := r.URL.Query().Get("environment_id")
	if envIDStr == "" {
		writeError(w, http.StatusBadRequest, "environment_id query param is required")
		return
	}
	envID, err := uuid.Parse(envIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment_id")
		return
	}
	state, err := h.repo.GetProfileState(r.Context(), serviceID, envID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get tool profile")
		return
	}
	writeData(w, http.StatusOK, state)
}

func toolActor(r *http.Request) string {
	if p := auth.GetPrincipal(r.Context()); p != nil && p.IsAuthenticated() {
		return p.Subject
	}
	return "api"
}
