package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// PolicyHandler handles HTTP requests for deployment policy CRUD and evaluation.
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

// Create creates a new deployment policy.
// POST /policies
func (h *PolicyHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string               `json:"name"`
		EnvironmentID *uuid.UUID           `json:"environment_id,omitempty"`
		Rules         []domain.PolicyRule   `json:"rules"`
		Enforcement   string               `json:"enforcement"`
		Enabled       *bool                `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	enforcement := domain.PolicyEnforcementWarn
	if req.Enforcement == "block" {
		enforcement = domain.PolicyEnforcementBlock
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	policy := &domain.DeploymentPolicy{
		Name:          req.Name,
		EnvironmentID: req.EnvironmentID,
		Rules:         req.Rules,
		Enforcement:   enforcement,
		Enabled:       enabled,
	}

	if err := h.policies.CreatePolicy(r.Context(), policy); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusCreated, policy)
}

// Update modifies an existing policy.
// PUT /policies/{id}
func (h *PolicyHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy ID")
		return
	}

	existing, err := h.policies.GetPolicy(r.Context(), id)
	if err != nil {
		if err == repository.ErrNotFound {
			writeError(w, http.StatusNotFound, "policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req struct {
		Name          *string              `json:"name"`
		EnvironmentID *uuid.UUID           `json:"environment_id"`
		Rules         []domain.PolicyRule   `json:"rules"`
		Enforcement   *string              `json:"enforcement"`
		Enabled       *bool                `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.EnvironmentID != nil {
		existing.EnvironmentID = req.EnvironmentID
	}
	if req.Rules != nil {
		existing.Rules = req.Rules
	}
	if req.Enforcement != nil {
		existing.Enforcement = domain.PolicyEnforcement(*req.Enforcement)
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}

	if err := h.policies.UpdatePolicy(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, existing)
}

// Delete removes a policy.
// DELETE /policies/{id}
func (h *PolicyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy ID")
		return
	}

	if err := h.policies.DeletePolicy(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeMessage(w, http.StatusOK, "policy deleted")
}

// Evaluate evaluates policies against an artifact for an environment.
// POST /policies/evaluate
func (h *PolicyHandler) Evaluate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ArtifactID    string `json:"artifact_id"`
		EnvironmentID string `json:"environment_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	artifactID, err := uuid.Parse(req.ArtifactID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact_id")
		return
	}

	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid environment_id")
		return
	}

	eval, err := h.policies.Evaluate(r.Context(), artifactID, envID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, eval)
}
