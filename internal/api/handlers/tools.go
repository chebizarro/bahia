package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type ToolHandler struct {
	repo repository.ToolProvisioningRepository
}

func NewToolHandler(repo repository.ToolProvisioningRepository) *ToolHandler {
	return &ToolHandler{repo: repo}
}

type toolDenylistRequest struct {
	Package string `json:"package"`
	Manager string `json:"manager"`
	Reason  string `json:"reason"`
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

func toolActor(r *http.Request) string {
	if p := auth.GetPrincipal(r.Context()); p != nil && p.IsAuthenticated() {
		return p.Subject
	}
	return "api"
}
