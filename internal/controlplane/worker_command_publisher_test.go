package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestWorkerKindConstantsPreserveWireCompatibility(t *testing.T) {
	if KindWorkerState != 31974 {
		t.Fatalf("KindWorkerState must remain wire-compatible at kind 31974, got %d", KindWorkerState)
	}
	if KindWorkerAssignmentState != 31991 || KindWorkerDrainStatus != 31992 || KindWorkerEligibilityPreview != 31993 {
		t.Fatalf("unexpected worker read model kinds: assignment=%d drain=%d eligibility=%d", KindWorkerAssignmentState, KindWorkerDrainStatus, KindWorkerEligibilityPreview)
	}
}

func TestWorkerCommandPublisherPublishesLifecycleCommandsWithCorrelation(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey, err := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("worker pubkey: %v", err)
	}
	publisher := NewWorkerCommandPublisher(capture, signer)

	receipt, err := publisher.PublishWorkerCordonRequest(ctx, WorkerLifecycleCommand{
		WorkerPubKey:     workerKey,
		Reason:           "kernel upgrade",
		OperatorMetadata: map[string]any{"operator": "alice"},
		IdempotencyKey:   "cordon-1",
		AgentID:          "agent-1",
	})
	if err != nil {
		t.Fatalf("publish cordon: %v", err)
	}
	if receipt.RequestKind != KindWorkerCordonRequest || receipt.StatusKind != KindWorkerStatus || receipt.ResultKind != KindWorkerResult || receipt.StateKind != KindWorkerState {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if receipt.WorkerPubKey != workerKey || receipt.DTag != "cordon-1" || receipt.Command != WorkerCommandCordon || receipt.PublishedRelays != 2 {
		t.Fatalf("unexpected receipt correlation: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	if ev.Kind != KindWorkerCordonRequest {
		t.Fatalf("expected worker cordon kind, got %d", ev.Kind)
	}
	if tagValueNostr(ev.Tags, "d") != "cordon-1" || tagValueNostr(ev.Tags, "worker") != workerKey || tagValueNostr(ev.Tags, "command") != WorkerCommandCordon || tagValueNostr(ev.Tags, "agent") != "agent-1" {
		t.Fatalf("unexpected tags: %#v", ev.Tags)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("content json: %v", err)
	}
	if content["worker_pubkey"] != workerKey || content["reason"] != "kernel upgrade" || content["idempotency_key"] != "cordon-1" {
		t.Fatalf("unexpected content: %#v", content)
	}
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("signature invalid: ok=%v err=%v", ok, err)
	}
}

func TestWorkerCommandPublisherPublishesLabelsUpdateAndGeneratesIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	publisher := NewWorkerCommandPublisher(capture, signer)

	receipt, err := publisher.PublishWorkerLabelsUpdateRequest(ctx, WorkerLabelsUpdateCommand{WorkerPubKey: workerKey, Labels: map[string]string{"role": "inference"}, Reason: "pool assignment"})
	if err != nil {
		t.Fatalf("publish labels: %v", err)
	}
	if receipt.RequestKind != KindWorkerLabelsUpdate || receipt.Command != WorkerCommandLabelsUpdate || receipt.DTag == "" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	ev := capture.events[0]
	if tagValueNostr(ev.Tags, "d") != receipt.DTag || tagValueNostr(ev.Tags, "worker") != workerKey {
		t.Fatalf("unexpected tags: %#v", ev.Tags)
	}
	var content struct {
		WorkerPubKey   string            `json:"worker_pubkey"`
		IdempotencyKey string            `json:"idempotency_key"`
		Labels         map[string]string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatalf("content json: %v", err)
	}
	if content.WorkerPubKey != workerKey || content.IdempotencyKey != receipt.DTag || content.Labels["role"] != "inference" {
		t.Fatalf("unexpected content: %#v", content)
	}
}

func TestWorkerCommandPublisherPublishesPolicyApplyAndWorkloadPin(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	publisher := NewWorkerCommandPublisher(capture, signer)

	policyReceipt, err := publisher.PublishWorkerPolicyApplyRequest(ctx, WorkerPolicyApplyCommand{
		EnvironmentID:  "env-1",
		IdempotencyKey: "policy-1",
		Policy: map[string]any{
			"label_selector": map[string]any{"role": "inference"},
			"rollout": map[string]any{
				"from_labels": map[string]any{"track": "canary"},
				"to_labels":   map[string]any{"track": "stable"},
			},
		},
	})
	if err != nil {
		t.Fatalf("publish policy apply: %v", err)
	}
	if policyReceipt.RequestKind != KindWorkerPolicyApplyRequest || policyReceipt.Command != WorkerPolicyApplyRequest || policyReceipt.EnvironmentID != "env-1" {
		t.Fatalf("unexpected policy receipt: %#v", policyReceipt)
	}
	policyEvent := capture.events[0]
	if tagValueNostr(policyEvent.Tags, "command") != WorkerPolicyApplyRequest || tagValueNostr(policyEvent.Tags, "environment") != "env-1" {
		t.Fatalf("unexpected policy tags: %#v", policyEvent.Tags)
	}

	pinReceipt, err := publisher.PublishWorkloadPinRequest(ctx, WorkloadPinCommand{EnvironmentID: "env-1", WorkloadID: "endpoint-1", WorkloadKind: "ml_inference", WorkerPubKey: workerKey, IdempotencyKey: "pin-1"})
	if err != nil {
		t.Fatalf("publish workload pin: %v", err)
	}
	if pinReceipt.RequestKind != KindWorkloadPinRequest || pinReceipt.Command != WorkloadPinRequest || pinReceipt.WorkerPubKey != workerKey || pinReceipt.WorkloadKind != "ml_inference" {
		t.Fatalf("unexpected pin receipt: %#v", pinReceipt)
	}
	pinEvent := capture.events[1]
	if tagValueNostr(pinEvent.Tags, "command") != WorkloadPinRequest || tagValueNostr(pinEvent.Tags, "worker") != workerKey || tagValueNostr(pinEvent.Tags, "workload") != "endpoint-1" {
		t.Fatalf("unexpected pin tags: %#v", pinEvent.Tags)
	}
}

func TestWorkerCommandPublisherFailsWhenNoRelayAccepts(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 0}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	publisher := NewWorkerCommandPublisher(capture, signer)

	if _, err := publisher.PublishWorkerDrainRequest(ctx, WorkerLifecycleCommand{WorkerPubKey: workerKey}); err == nil {
		t.Fatal("expected no-relay acceptance error")
	}
}
