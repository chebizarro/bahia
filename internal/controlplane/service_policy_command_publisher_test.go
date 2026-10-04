package controlplane

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
)

func TestToolApprovalCommandPublisherPublishesCanonicalApprovalResponse(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewToolApprovalCommandPublisher(capture, signer)
	intentID := uuid.New()

	receipt, err := publisher.PublishToolApprovalResponse(ctx, ToolApprovalCommand{IntentID: intentID, Action: "approve", Reason: "operator reviewed", IdempotencyKey: "tool-approval:1"})
	if err != nil {
		t.Fatalf("publish tool approval: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.ResultKind != KindContextVMMessage || receipt.PublishedRelays != 2 || receipt.IntentID != intentID.String() || receipt.Action != "approve" || receipt.DTag != "tool-approval:1" {
		t.Fatalf("unexpected tool approval receipt: %#v", receipt)
	}
	payload := assertContextVMCommand(t, capture.events[0], ContextVMMethodToolApprovalResponse)
	if payload["intent_id"] != intentID.String() || payload["action"] != "approve" || payload["reason"] != "operator reviewed" {
		t.Fatalf("unexpected tool approval payload: %#v", payload)
	}
	assertReactorTag(t, capture.events[0].Tags, "intent", intentID.String())
	assertReactorTag(t, capture.events[0].Tags, "d", "tool-approval:1")
}
