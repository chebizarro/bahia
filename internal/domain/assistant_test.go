package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAssistantApprovalRequestJSONBackCompat(t *testing.T) {
	legacy := []byte(`{"session_id":"session-1","plan_hash":"hash-1","decision":"approve"}`)
	var req AssistantApprovalRequest
	if err := json.Unmarshal(legacy, &req); err != nil {
		t.Fatalf("unmarshal legacy approval request: %v", err)
	}
	if req.SessionID != "session-1" || req.PlanHash != "hash-1" || req.Decision != "approve" {
		t.Fatalf("legacy fields not preserved: %+v", req)
	}
	if req.ActionID != "" || req.CancelScope != "" {
		t.Fatalf("new fields should be empty for legacy JSON: %+v", req)
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal legacy approval request: %v", err)
	}
	if strings.Contains(string(encoded), "action_id") || strings.Contains(string(encoded), "cancel_scope") {
		t.Fatalf("empty additive fields should omit from legacy JSON: %s", encoded)
	}
}

func TestAssistantApprovalRequestActionFieldsJSON(t *testing.T) {
	req := AssistantApprovalRequest{
		SessionID:   "session-1",
		PlanHash:    "hash-1",
		ActionID:    "action-1",
		CancelScope: "action",
		Decision:    "approve",
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal approval request: %v", err)
	}
	if !strings.Contains(string(encoded), `"action_id":"action-1"`) || !strings.Contains(string(encoded), `"cancel_scope":"action"`) {
		t.Fatalf("action fields missing from JSON: %s", encoded)
	}
}

func TestAssistantTranscriptEnvelopeConstants(t *testing.T) {
	if KindAssistantTranscript != 30316 {
		t.Fatalf("KindAssistantTranscript = %d", KindAssistantTranscript)
	}
	envelope := AssistantTranscriptAEADEnvelope{
		Schema:     AssistantTranscriptSchema,
		Envelope:   AssistantTranscriptEnvelopeServiceHeldAEAD,
		Algorithm:  AssistantTranscriptAEADAlgorithmXChaCha20,
		KeyRef:     "assistant-transcript/default",
		Nonce:      "nonce",
		Ciphertext: "ciphertext",
		AssociatedData: map[string]string{
			AssistantTranscriptTagSession: "session-1",
		},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal transcript envelope: %v", err)
	}
	if !strings.Contains(string(encoded), AssistantTranscriptEnvelopeServiceHeldAEAD) || !strings.Contains(string(encoded), "key_ref") {
		t.Fatalf("transcript envelope does not expose symmetric-key metadata: %s", encoded)
	}
}

func TestAssistantV2RequestFieldsRemainAdditive(t *testing.T) {
	var oldPrompt AssistantPromptRequest
	if err := json.Unmarshal([]byte(`{"session_id":"s","turn_id":"t","prompt":"hello"}`), &oldPrompt); err != nil {
		t.Fatal(err)
	}
	if oldPrompt.ContractVersion != 0 || oldPrompt.Workflow != "" {
		t.Fatalf("legacy prompt changed: %#v", oldPrompt)
	}
	var newPrompt AssistantPromptRequest
	if err := json.Unmarshal([]byte(`{"contract_version":2,"workflow":"batch","session_id":"s","turn_id":"t","prompt":"hello"}`), &newPrompt); err != nil {
		t.Fatal(err)
	}
	if newPrompt.ContractVersion != 2 || newPrompt.Workflow != AssistantWorkflowBatch {
		t.Fatalf("v2 prompt lost fields: %#v", newPrompt)
	}
	var approval AssistantApprovalRequest
	if err := json.Unmarshal([]byte(`{"contract_version":2,"request_id":"approval-1","session_id":"s","run_id":"r","workflow":"batch","proposal_id":"p","base_revision":1,"base_plan_hash":"base","approved_revision":2,"approved_plan_hash":"edited","decision":"approve"}`), &approval); err != nil {
		t.Fatal(err)
	}
	if approval.ProposalID != "p" || approval.BaseRevision != 1 || approval.ApprovedRevision != 2 || approval.PlanHash != "" {
		t.Fatalf("v2 approval lost fields: %#v", approval)
	}
}
