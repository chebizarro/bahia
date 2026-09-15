package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/soulfactory"
)

type fakeLegacyReconciliationController struct {
	previewCalls int
	applyCalls   int
	approval     soulfactory.LegacyReconcileApproval
}

func (f *fakeLegacyReconciliationController) Preview(_ context.Context, request soulfactory.LegacyAgentReconciliationRequest) (soulfactory.LegacyAgentReconciliationReport, error) {
	f.previewCalls++
	return soulfactory.LegacyAgentReconciliationReport{ReadOnly: true}, nil
}
func (f *fakeLegacyReconciliationController) ReconcileApprovedLink(_ context.Context, request soulfactory.LegacyAgentReconcileApplyRequest, approval soulfactory.LegacyReconcileApproval) (soulfactory.LegacyAgentReconcileReceipt, error) {
	f.applyCalls++
	f.approval = approval
	return soulfactory.LegacyAgentReconcileReceipt{AgentID: request.AgentID}, nil
}

func TestLegacyAgentReconciliationHandlerRefusesUnauthenticatedApproval(t *testing.T) {
	controller := &fakeLegacyReconciliationController{}
	handler := NewLegacyAgentReconciliationHandler(controller)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/soulfactory/legacy-reconciliation/apply", strings.NewReader(`{"agent_id":"bravo","approval_ref":"forged"}`))
	res := httptest.NewRecorder()
	handler.Apply(res, req)
	if res.Code != http.StatusUnauthorized || controller.applyCalls != 0 {
		t.Fatalf("status=%d calls=%d", res.Code, controller.applyCalls)
	}
}

func TestLegacyAgentReconciliationHandlerBindsSignedPrincipalToApply(t *testing.T) {
	controller := &fakeLegacyReconciliationController{}
	handler := NewLegacyAgentReconciliationHandler(controller)
	body := `{"agent_id":"bravo","classification":{"agent_id":"bravo","soul_event_id":"event-a","soul_content_hash":"hash-a","status":"unlinked"},"approval_ref":"nip98-event"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/soulfactory/legacy-reconciliation/apply", strings.NewReader(body))
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{Subject: "operator-pubkey", PubKey: "operator-pubkey", Method: auth.MethodNIP98}))
	res := httptest.NewRecorder()
	handler.Apply(res, req)
	if res.Code != http.StatusOK || controller.applyCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", res.Code, controller.applyCalls, res.Body.String())
	}
	if controller.approval.ApprovedBy != "operator-pubkey" || controller.approval.Principal == nil || controller.approval.SoulEventID != "event-a" || controller.approval.SoulContentHash != "hash-a" {
		t.Fatalf("approval not bound: %+v", controller.approval)
	}
}
