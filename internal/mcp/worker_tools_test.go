package mcp

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

type captureWorkerCommandPublisher struct {
	lifecycle *controlplane.WorkerLifecycleCommand
	labels    *controlplane.WorkerLabelsUpdateCommand
	kind      int
	command   string
}

func (p *captureWorkerCommandPublisher) PublishWorkerCordonRequest(_ context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error) {
	p.lifecycle = &cmd
	p.kind = controlplane.KindContextVMMessage
	p.command = controlplane.WorkerCommandCordon
	return workerTestReceipt(p.kind, p.command, cmd.WorkerPubKey, cmd.IdempotencyKey), nil
}
func (p *captureWorkerCommandPublisher) PublishWorkerUncordonRequest(_ context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error) {
	p.lifecycle = &cmd
	p.kind = controlplane.KindContextVMMessage
	p.command = controlplane.WorkerCommandUncordon
	return workerTestReceipt(p.kind, p.command, cmd.WorkerPubKey, cmd.IdempotencyKey), nil
}
func (p *captureWorkerCommandPublisher) PublishWorkerDrainRequest(_ context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error) {
	p.lifecycle = &cmd
	p.kind = controlplane.KindContextVMMessage
	p.command = controlplane.WorkerCommandDrain
	return workerTestReceipt(p.kind, p.command, cmd.WorkerPubKey, cmd.IdempotencyKey), nil
}
func (p *captureWorkerCommandPublisher) PublishWorkerUndrainRequest(_ context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error) {
	p.lifecycle = &cmd
	p.kind = controlplane.KindContextVMMessage
	p.command = controlplane.WorkerCommandUndrain
	return workerTestReceipt(p.kind, p.command, cmd.WorkerPubKey, cmd.IdempotencyKey), nil
}
func (p *captureWorkerCommandPublisher) PublishWorkerMaintenanceEnterRequest(_ context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error) {
	p.lifecycle = &cmd
	p.kind = controlplane.KindContextVMMessage
	p.command = controlplane.WorkerCommandMaintenanceEnter
	return workerTestReceipt(p.kind, p.command, cmd.WorkerPubKey, cmd.IdempotencyKey), nil
}
func (p *captureWorkerCommandPublisher) PublishWorkerMaintenanceExitRequest(_ context.Context, cmd controlplane.WorkerLifecycleCommand) (*controlplane.WorkerCommandReceipt, error) {
	p.lifecycle = &cmd
	p.kind = controlplane.KindContextVMMessage
	p.command = controlplane.WorkerCommandMaintenanceExit
	return workerTestReceipt(p.kind, p.command, cmd.WorkerPubKey, cmd.IdempotencyKey), nil
}
func (p *captureWorkerCommandPublisher) PublishWorkerLabelsUpdateRequest(_ context.Context, cmd controlplane.WorkerLabelsUpdateCommand) (*controlplane.WorkerCommandReceipt, error) {
	p.labels = &cmd
	p.kind = controlplane.KindContextVMMessage
	p.command = controlplane.WorkerCommandLabelsUpdate
	return workerTestReceipt(p.kind, p.command, cmd.WorkerPubKey, cmd.IdempotencyKey), nil
}

func workerTestReceipt(kind int, command, worker, dTag string) *controlplane.WorkerCommandReceipt {
	return &controlplane.WorkerCommandReceipt{RequestEventID: "worker-event", RequestPubkey: "operator-pubkey", RequestKind: kind, ResultKind: controlplane.KindCASControlState, StateKind: controlplane.KindCASControlState, DTag: dTag, PublishedRelays: 2, WorkerPubKey: worker, Command: command}
}

func TestGetToolsIncludesWorkerManagementAndReadModelTools(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	required := map[string]bool{"bahia_worker_cordon": false, "bahia_worker_uncordon": false, "bahia_worker_drain": false, "bahia_worker_undrain": false, "bahia_worker_maintenance_enter": false, "bahia_worker_maintenance_exit": false, "bahia_worker_labels_update": false, "bahia_worker_get_assignments": false, "bahia_worker_get_drain_status": false, "bahia_worker_preview_eligibility": false}
	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing worker tool %s", name)
		}
	}
}

func TestWorkerMutatingToolsPublishSignerFirstRequestsAndReturnCorrelation(t *testing.T) {
	publisher := &captureWorkerCommandPublisher{}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{WorkerCommandPublisher: publisher})
	res, err := server.CallTool(authorizedMCPContext(), "bahia_worker_drain", map[string]interface{}{"worker_pubkey": "worker-pubkey", "reason": "kernel upgrade", "idempotency_key": "drain:1"})
	if err != nil {
		t.Fatalf("drain call: %v", err)
	}
	if res.IsError {
		t.Fatalf("drain returned error: %s", res.Content[0].Text)
	}
	payload := decodeResultMap(t, res)
	if payload["request_event_id"] != "worker-event" || payload["request_kind"].(float64) != float64(controlplane.KindContextVMMessage) || payload["result_kind"].(float64) != float64(controlplane.KindCASControlState) {
		t.Fatalf("unexpected worker receipt: %#v", payload)
	}
	if publisher.lifecycle == nil || publisher.lifecycle.WorkerPubKey != "worker-pubkey" || publisher.lifecycle.Reason != "kernel upgrade" || publisher.lifecycle.IdempotencyKey != "drain:1" {
		t.Fatalf("unexpected lifecycle command: %#v", publisher.lifecycle)
	}
	correlation := payload["correlation_tags"].(map[string]interface{})
	if correlation["worker"] != "worker-pubkey" || correlation["d"] != "drain:1" || correlation["command"] != controlplane.WorkerCommandDrain {
		t.Fatalf("missing correlation tags: %#v", correlation)
	}
	readKinds := payload["read_model_kinds"].([]interface{})
	if len(readKinds) != 1 || readKinds[0].(float64) != float64(controlplane.KindCASControlState) {
		t.Fatalf("expected canonical CAS read model kind: %#v", readKinds)
	}

	labelsRes, err := server.CallTool(authorizedMCPContext(), "bahia_worker_labels_update", map[string]interface{}{"worker_pubkey": "worker-pubkey", "idempotency_key": "labels:1", "labels": map[string]interface{}{"role": "inference"}})
	if err != nil || labelsRes.IsError {
		t.Fatalf("labels result=%#v err=%v", labelsRes, err)
	}
	if publisher.labels == nil || publisher.labels.Labels["role"] != "inference" {
		t.Fatalf("unexpected labels command: %#v", publisher.labels)
	}
}

func TestWorkerMutatingToolsRequirePublisher(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := server.CallTool(authorizedMCPContext(), "bahia_worker_cordon", map[string]interface{}{"worker_pubkey": "worker"})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected missing publisher error, got %#v", res)
	}
}
