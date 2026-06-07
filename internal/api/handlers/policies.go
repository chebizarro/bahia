package handlers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// PolicyCommandPublisher publishes signer-first policy mutation commands.
type PolicyCommandPublisher interface {
	PublishPolicyCreateRequest(ctx context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error)
	PublishPolicyUpdateRequest(ctx context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error)
	PublishPolicyDeleteRequest(ctx context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error)
}

// PolicyHandler handles HTTP read requests for deployment policies.
type PolicyHandler struct {
	policies *service.PolicyService
	commands PolicyCommandPublisher
}

// NewPolicyHandler creates a new policy handler.
func NewPolicyHandler(policies *service.PolicyService) *PolicyHandler {
	return &PolicyHandler{policies: policies}
}

func NewPolicyHandlerWithCommands(policies *service.PolicyService, commands PolicyCommandPublisher) *PolicyHandler {
	return &PolicyHandler{policies: policies, commands: commands}
}

type policyMutationRequest struct {
	Name           string              `json:"name"`
	EnvironmentID  *uuid.UUID          `json:"environment_id,omitempty"`
	Rules          []domain.PolicyRule `json:"rules"`
	Enforcement    string              `json:"enforcement"`
	Enabled        *bool               `json:"enabled,omitempty"`
	IdempotencyKey string              `json:"idempotency_key,omitempty"`
}

func (h *PolicyHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWritePolicies) {
		return
	}
	if h.commands == nil {
		writeError(w, http.StatusServiceUnavailable, "policy command publisher is not configured")
		return
	}
	var req policyMutationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	receipt, err := h.commands.PublishPolicyCreateRequest(r.Context(), controlplane.PolicyMutationCommand{Name: req.Name, EnvironmentID: req.EnvironmentID, Rules: req.Rules, Enforcement: req.Enforcement, Enabled: req.Enabled, IdempotencyKey: req.IdempotencyKey})
	if err != nil {
		writeCommandPublishError(w, err)
		return
	}
	writeAcceptedCommandReceipt(w, commandReceiptFromPolicy(receipt))
}

func (h *PolicyHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWritePolicies) {
		return
	}
	if h.commands == nil {
		writeError(w, http.StatusServiceUnavailable, "policy command publisher is not configured")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy ID")
		return
	}
	var req policyMutationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	receipt, err := h.commands.PublishPolicyUpdateRequest(r.Context(), controlplane.PolicyMutationCommand{ID: id, Name: req.Name, EnvironmentID: req.EnvironmentID, Rules: req.Rules, Enforcement: req.Enforcement, Enabled: req.Enabled, IdempotencyKey: req.IdempotencyKey})
	if err != nil {
		writeCommandPublishError(w, err)
		return
	}
	writeAcceptedCommandReceipt(w, commandReceiptFromPolicy(receipt))
}

func (h *PolicyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWritePolicies) {
		return
	}
	if h.commands == nil {
		writeError(w, http.StatusServiceUnavailable, "policy command publisher is not configured")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy ID")
		return
	}
	var req struct {
		IdempotencyKey string `json:"idempotency_key,omitempty"`
	}
	if r.Body != nil && r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	receipt, err := h.commands.PublishPolicyDeleteRequest(r.Context(), controlplane.PolicyMutationCommand{ID: id, IdempotencyKey: req.IdempotencyKey})
	if err != nil {
		writeCommandPublishError(w, err)
		return
	}
	writeAcceptedCommandReceipt(w, commandReceiptFromPolicy(receipt))
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
