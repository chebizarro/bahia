package controlplane

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
)

func TestWorkerLegacyKindConstantsRemainMigrationOnly(t *testing.T) {
	if KindWorkerState != 32000 || KindWorkerAssignmentState != 32001 || KindWorkerDrainStatus != 32002 || KindWorkerEligibilityPreview != 32003 {
		t.Fatalf("unexpected legacy worker read model kind mappings: state=%d assignment=%d drain=%d eligibility=%d", KindWorkerState, KindWorkerAssignmentState, KindWorkerDrainStatus, KindWorkerEligibilityPreview)
	}
}

func TestWorkerReadModelRuntimeAcceptsCanonicalStateKindOnly(t *testing.T) {
	if !isAcceptedWorkerReadModelKind(KindCASControlState) {
		t.Fatalf("canonical worker read-model kind %d should be accepted", KindCASControlState)
	}
	for _, kind := range []int{KindWorkerState, KindWorkerAssignmentState, KindWorkerDrainStatus, KindWorkerEligibilityPreview, 31974, 31991, 31992, 31993, 31994, 31995, 31996, 31997, 31998, 31999, 32010} {
		if isAcceptedWorkerReadModelKind(kind) {
			t.Fatalf("legacy/non-worker read-model kind %d should not be accepted at runtime", kind)
		}
	}
}

func TestWorkerCommandPublisherPublishesLifecycleCommandsWithCorrelation(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
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
	if receipt.RequestKind != KindContextVMMessage || receipt.ResultKind != KindCASControlState || receipt.StateKind != KindCASControlState {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if receipt.WorkerPubKey != workerKey || receipt.DTag != "cordon-1" || receipt.Command != WorkerCommandCordon || receipt.PublishedRelays != 2 {
		t.Fatalf("unexpected receipt correlation: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	content := assertContextVMCommand(t, ev, ContextVMMethodWorkerCordon)
	if tagValueNostr(ev.Tags, "d") != "cordon-1" || tagValueNostr(ev.Tags, "worker") != workerKey || tagValueNostr(ev.Tags, "command") != WorkerCommandCordon || tagValueNostr(ev.Tags, "agent") != "agent-1" {
		t.Fatalf("unexpected tags: %#v", ev.Tags)
	}
	if content["worker_pubkey"] != workerKey || content["reason"] != "kernel upgrade" || content["idempotency_key"] != "cordon-1" {
		t.Fatalf("unexpected content: %#v", content)
	}
	if !ev.VerifySignature() {
		t.Fatalf("signature invalid")
	}
}

func TestWorkerCommandPublisherPublishesLabelsUpdateAndGeneratesIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	publisher := NewWorkerCommandPublisher(capture, signer)

	receipt, err := publisher.PublishWorkerLabelsUpdateRequest(ctx, WorkerLabelsUpdateCommand{WorkerPubKey: workerKey, Labels: map[string]string{"role": "inference"}, Reason: "pool assignment"})
	if err != nil {
		t.Fatalf("publish labels: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.Command != WorkerCommandLabelsUpdate || receipt.DTag == "" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	ev := capture.events[0]
	if tagValueNostr(ev.Tags, "d") != receipt.DTag || tagValueNostr(ev.Tags, "worker") != workerKey {
		t.Fatalf("unexpected tags: %#v", ev.Tags)
	}
	content := assertContextVMCommand(t, ev, "worker/labels-update")
	labels, _ := content["labels"].(map[string]any)
	if content["worker_pubkey"] != workerKey || content["idempotency_key"] != receipt.DTag || labels["role"] != "inference" {
		t.Fatalf("unexpected content: %#v", content)
	}
}

func TestWorkerCommandPublisherPublishesPolicyApplyAndWorkloadPin(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
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
	if policyReceipt.RequestKind != KindContextVMMessage || policyReceipt.Command != WorkerPolicyApplyRequest || policyReceipt.EnvironmentID != "env-1" {
		t.Fatalf("unexpected policy receipt: %#v", policyReceipt)
	}
	policyEvent := capture.events[0]
	assertContextVMCommand(t, policyEvent, "worker/policy-apply")
	if tagValueNostr(policyEvent.Tags, "command") != WorkerPolicyApplyRequest || tagValueNostr(policyEvent.Tags, "environment") != "env-1" {
		t.Fatalf("unexpected policy tags: %#v", policyEvent.Tags)
	}

	pinReceipt, err := publisher.PublishWorkloadPinRequest(ctx, WorkloadPinCommand{EnvironmentID: "env-1", WorkloadID: "endpoint-1", WorkloadKind: "ml_inference", WorkerPubKey: workerKey, IdempotencyKey: "pin-1"})
	if err != nil {
		t.Fatalf("publish workload pin: %v", err)
	}
	if pinReceipt.RequestKind != KindContextVMMessage || pinReceipt.Command != WorkloadPinRequest || pinReceipt.WorkerPubKey != workerKey || pinReceipt.WorkloadKind != "ml_inference" {
		t.Fatalf("unexpected pin receipt: %#v", pinReceipt)
	}
	pinEvent := capture.events[1]
	assertContextVMCommand(t, pinEvent, "worker/workload-pin")
	if tagValueNostr(pinEvent.Tags, "command") != WorkloadPinRequest || tagValueNostr(pinEvent.Tags, "worker") != workerKey || tagValueNostr(pinEvent.Tags, "workload") != "endpoint-1" {
		t.Fatalf("unexpected pin tags: %#v", pinEvent.Tags)
	}
}

func TestWorkerCommandPublisherFailsWhenNoRelayAccepts(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 0}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	publisher := NewWorkerCommandPublisher(capture, signer)

	if _, err := publisher.PublishWorkerDrainRequest(ctx, WorkerLifecycleCommand{WorkerPubKey: workerKey}); err == nil {
		t.Fatal("expected no-relay acceptance error")
	}
}
