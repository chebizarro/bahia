package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
)

type ToolHandler struct {
	repo repository.ToolProvisioningRepository
}

func NewToolHandler(repo repository.ToolProvisioningRepository) *ToolHandler {
	return &ToolHandler{repo: repo}
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
