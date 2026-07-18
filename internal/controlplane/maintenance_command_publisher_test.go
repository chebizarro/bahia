package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
)

func TestMaintenanceCommandPublisherPublishesIntentsWithCorrelation(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 2}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	publisher := NewMaintenanceCommandPublisher(capture, signer)

	receipt, err := publisher.PublishScan(ctx, MaintenanceCommand{
		WorkerPubKey:   workerKey,
		Reason:         "periodic hygiene scan",
		IdempotencyKey: "scan-1",
		AgentID:        "swabbie",
	})
	if err != nil {
		t.Fatalf("publish scan: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.Command != MaintenanceCommandScan || receipt.DTag != "scan-1" || receipt.PublishedRelays != 2 {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	assertContextVMCommand(t, ev, ContextVMMethodMaintenanceScan)
	if tagValueNostr(ev.Tags, "worker") != workerKey || tagValueNostr(ev.Tags, "p") != workerKey || tagValueNostr(ev.Tags, "command") != MaintenanceCommandScan {
		t.Fatalf("unexpected tags: %#v", ev.Tags)
	}
}

func TestMaintenanceCommandPublisherQuarantineCarriesPaths(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	publisher := NewMaintenanceCommandPublisher(capture, signer)

	if _, err := publisher.PublishQuarantine(ctx, MaintenanceCommand{WorkerPubKey: workerKey}); err == nil {
		t.Fatal("quarantine without paths must fail")
	}
	if _, err := publisher.PublishQuarantine(ctx, MaintenanceCommand{WorkerPubKey: workerKey, Paths: []string{"/x/dup"}, IdempotencyKey: "q-1"}); err != nil {
		t.Fatalf("publish quarantine: %v", err)
	}
	var rpc ContextVMJSONRPCRequest
	if err := json.Unmarshal([]byte(capture.events[0].Content), &rpc); err != nil {
		t.Fatalf("decode rpc: %v", err)
	}
	var params struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(rpc.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if len(params.Paths) != 1 || params.Paths[0] != "/x/dup" {
		t.Fatalf("params = %#v", params)
	}
}

func TestMaintenanceCommandPublisherPurgeRequiresConfirm(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	workerKey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	publisher := NewMaintenanceCommandPublisher(capture, signer)

	if _, err := publisher.PublishPurge(ctx, MaintenanceCommand{WorkerPubKey: workerKey}); err == nil {
		t.Fatal("purge without confirm must fail at the control plane")
	}
	if _, err := publisher.PublishPurge(ctx, MaintenanceCommand{WorkerPubKey: workerKey, Confirm: true, IdempotencyKey: "purge-1"}); err != nil {
		t.Fatalf("publish purge: %v", err)
	}
}
