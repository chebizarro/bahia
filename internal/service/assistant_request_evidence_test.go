package service

import (
	"context"
	"errors"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAssistantIntentRequestEvidenceRequiresDurableProcessorMarker(t *testing.T) {
	x := domain.AssistantExecution{SessionID: "session", RunID: "run", OperatorPubkey: "operator"}
	w := domain.AssistantWorkItem{WorkID: "work", ToolName: "bahia_assistant_service_deploy", IdempotencyKey: "assistant-agent:session:run:work"}
	const eventID = "signed-intent-event"
	markerFound := false
	resolver := &AssistantIntentRequestEvidenceResolver{IntentEvidence: func(name, actor, key, gotEventID string) (*domain.AsyncToolReceipt, error) {
		if name != w.ToolName || actor != x.OperatorPubkey || key != w.IdempotencyKey || gotEventID != eventID {
			t.Fatalf("incorrect marker lookup: %q %q %q %q", name, actor, key, gotEventID)
		}
		if !markerFound {
			return nil, errors.New("processed assistant intent evidence does not match work")
		}
		return &domain.AsyncToolReceipt{ToolName: name, RequestEventID: gotEventID, RequestKind: 30900, ResultKinds: []int{30315}, IdempotencyKey: key}, nil
	}}
	if _, err := resolver.ResolveAssistantRequestEvidence(context.Background(), eventID, x, w); err == nil {
		t.Fatal("an operator-provided event ID without a processor marker was accepted")
	}
	markerFound = true
	receipt, err := resolver.ResolveAssistantRequestEvidence(context.Background(), eventID, x, w)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RequestEventID != eventID || receipt.RequestKind != 30900 || receipt.ResultKinds[0] != 30315 {
		t.Fatalf("receipt = %+v", receipt)
	}
	var none *AssistantIntentRequestEvidenceResolver
	if _, err := none.ResolveAssistantRequestEvidence(context.Background(), eventID, x, w); err == nil {
		t.Fatal("unconfigured resolver accepted evidence")
	}
}
