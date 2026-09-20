package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/soulfactory"
)

// LegacyAgentReconciliationController is implemented by the production
// SoulFactory reconciler.
type LegacyAgentReconciliationController interface {
	Preview(ctx context.Context, request soulfactory.LegacyAgentReconciliationRequest) (soulfactory.LegacyAgentReconciliationReport, error)
	ReconcileApprovedLink(ctx context.Context, request soulfactory.LegacyAgentReconcileApplyRequest, approval soulfactory.LegacyReconcileApproval) (soulfactory.LegacyAgentReconcileReceipt, error)
}

// LegacyAgentReconciliationHandler exposes authenticated, dry-run-first
// reconciliation. NIP-98 signs the complete request body, binding apply input.
type LegacyAgentReconciliationHandler struct {
	controller LegacyAgentReconciliationController
}

func NewLegacyAgentReconciliationHandler(controller LegacyAgentReconciliationController) *LegacyAgentReconciliationHandler {
	return &LegacyAgentReconciliationHandler{controller: controller}
}

func (h *LegacyAgentReconciliationHandler) Preview(w http.ResponseWriter, r *http.Request) {
	principal := auth.GetPrincipal(r.Context())
	if !authenticatedNIP98Principal(principal) {
		writeError(w, http.StatusUnauthorized, "authenticated NIP-98 operator approval is required")
		return
	}
	var request soulfactory.LegacyAgentReconciliationRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid reconciliation request")
		return
	}
	report, err := h.controller.Preview(r.Context(), request)
	if err != nil && !errors.Is(err, soulfactory.ErrLegacyReconciliationAmbiguous) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, report)
}

func (h *LegacyAgentReconciliationHandler) Apply(w http.ResponseWriter, r *http.Request) {
	principal := auth.GetPrincipal(r.Context())
	if !authenticatedNIP98Principal(principal) {
		writeError(w, http.StatusUnauthorized, "authenticated NIP-98 operator approval is required")
		return
	}
	var body struct {
		soulfactory.LegacyAgentReconcileApplyRequest
		ApprovalRef string `json:"approval_ref"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid reconciliation apply request")
		return
	}
	if strings.TrimSpace(body.ApprovalRef) == "" {
		writeError(w, http.StatusBadRequest, "approval_ref is required")
		return
	}
	classification := body.Classification
	receipt, err := h.controller.ReconcileApprovedLink(r.Context(), body.LegacyAgentReconcileApplyRequest, soulfactory.LegacyReconcileApproval{
		AgentID: classification.AgentID, Action: soulfactory.LegacyAgentReconcileActionLink,
		SoulEventID: classification.SoulEventID, SoulContentHash: classification.SoulContentHash,
		ApprovedBy: principal.Subject, ApprovalRef: strings.TrimSpace(body.ApprovalRef), Principal: principal,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeData(w, http.StatusOK, receipt)
}

func authenticatedNIP98Principal(principal *auth.Principal) bool {
	return principal != nil && principal.IsAuthenticated() && principal.Method == auth.MethodNIP98 && strings.TrimSpace(principal.Subject) != ""
}
