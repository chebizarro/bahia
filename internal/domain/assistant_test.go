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
