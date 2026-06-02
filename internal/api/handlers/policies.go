package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// PolicyHandler handles HTTP read requests for deployment policies.
type PolicyHandler struct {
	policies *service.PolicyService
}

// NewPolicyHandler creates a new policy handler.
func NewPolicyHandler(policies *service.PolicyService) *PolicyHandler {
	return &PolicyHandler{policies: policies}
}

// List returns all policies.
// GET /policies?enabled=true
func (h *PolicyHandler) List(w http.ResponseWriter, r *http.Request) {
	enabledOnly := r.URL.Query().Get("enabled") == "true"

	policies, err := h.policies.ListPolicies(r.Context(), enabledOnly)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, policies)
}

// Get returns a policy by ID.
// GET /policies/{id}
func (h *PolicyHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy ID")
		return
	}

	policy, err := h.policies.GetPolicy(r.Context(), id)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, policy)
}
