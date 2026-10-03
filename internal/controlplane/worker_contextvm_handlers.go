package controlplane

import (
	"context"
	"fmt"
	"strings"
)

// RegisterWorkerContextVMHandlers registers encrypted ContextVM worker command
// entrypoints. The handlers publish canonical worker command events and return
// an immediate receipt; durable state and terminal outcomes are emitted by the
// worker control-plane handlers as worker status/result/state observables.
func RegisterWorkerContextVMHandlers(transport *EncryptedRequestTransport, gate *FleetOperatorGate, processors ...*IntentProcessor) {
	if transport == nil || transport.responder == nil {
		return
	}
	h := workerContextVMHandlers{publisher: NewWorkerCommandPublisher(transport.responder.publisher, transport.responder.signer)}
	if len(processors) > 0 {
		h.intentProcessor = processors[0]
	}
	transport.RegisterContextVMHandler(ContextVMMethodWorkerCleanup, gate.wrap(h.cleanup))
	transport.RegisterContextVMHandler(ContextVMMethodWorkerCordon, gate.wrap(h.cordon))
	transport.RegisterContextVMHandler(ContextVMMethodWorkerDrain, gate.wrap(h.drain))
	transport.RegisterContextVMHandler(ContextVMMethodWorkerMaintenanceExit, gate.wrap(h.maintenanceExit))
	transport.RegisterContextVMHandler(ContextVMMethodWorkerLabelsUpdate, gate.wrap(h.labelsUpdate))
}

type workerContextVMHandlers struct {
	publisher       *WorkerCommandPublisher
	intentProcessor *IntentProcessor
}

type workerContextVMPayload struct {
	WorkerPubKey     string            `json:"worker_pubkey"`
	Reason           string            `json:"reason,omitempty"`
	OperatorMetadata map[string]any    `json:"operator_metadata,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
	AgentID          string            `json:"agent_id,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	CleanupMode      string            `json:"cleanup_mode,omitempty"`
}

func (h workerContextVMHandlers) cleanup(ctx context.Context, request ContextVMRequest) (any, error) {
	payload, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	if h.intentEnabled() {
		return h.dualDispatch(ctx, request, "cleanup", payload, "")
	}
	receipt, err := h.publisher.PublishWorkerCleanupRequest(ctx, cmd, payload.CleanupMode)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) cordon(ctx context.Context, request ContextVMRequest) (any, error) {
	payload, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	if h.intentEnabled() {
		return h.dualDispatch(ctx, request, "cordon", payload, "cordoned")
	}
	receipt, err := h.publisher.PublishWorkerCordonRequest(ctx, cmd)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) drain(ctx context.Context, request ContextVMRequest) (any, error) {
	payload, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	if h.intentEnabled() {
		return h.dualDispatch(ctx, request, "drain", payload, "draining")
	}
	receipt, err := h.publisher.PublishWorkerDrainRequest(ctx, cmd)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) maintenanceExit(ctx context.Context, request ContextVMRequest) (any, error) {
	payload, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	if h.intentEnabled() {
		return h.dualDispatch(ctx, request, "maintenance-exit", payload, "active")
	}
	receipt, err := h.publisher.PublishWorkerMaintenanceExitRequest(ctx, cmd)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) labelsUpdate(ctx context.Context, request ContextVMRequest) (any, error) {
	payload, _, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	if len(payload.Labels) == 0 {
		return nil, fmt.Errorf("labels are required")
	}
	if h.intentEnabled() {
		return h.dualDispatch(ctx, request, "labels-update", payload, "")
	}
	receipt, err := h.publisher.PublishWorkerLabelsUpdateRequest(ctx, WorkerLabelsUpdateCommand{
		WorkerPubKey:     payload.WorkerPubKey,
		Labels:           payload.Labels,
		Reason:           payload.Reason,
		OperatorMetadata: payload.OperatorMetadata,
		IdempotencyKey:   payload.IdempotencyKey,
		AgentID:          payload.AgentID,
	})
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) lifecycleCommand(request ContextVMRequest) (workerContextVMPayload, WorkerLifecycleCommand, error) {
	var payload workerContextVMPayload
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return payload, WorkerLifecycleCommand{}, err
	}
	payload.WorkerPubKey = strings.TrimSpace(payload.WorkerPubKey)
	payload.IdempotencyKey = strings.TrimSpace(payload.IdempotencyKey)
	if payload.WorkerPubKey == "" {
		return payload, WorkerLifecycleCommand{}, fmt.Errorf("worker_pubkey is required")
	}
	cmd := WorkerLifecycleCommand{
		WorkerPubKey:     payload.WorkerPubKey,
		Reason:           payload.Reason,
		OperatorMetadata: payload.OperatorMetadata,
		IdempotencyKey:   payload.IdempotencyKey,
		AgentID:          payload.AgentID,
	}
	return payload, cmd, nil
}

func (h workerContextVMHandlers) intentEnabled() bool {
	return h.intentProcessor != nil && h.intentProcessor.Handler("worker") != nil
}

func (h workerContextVMHandlers) dualDispatch(ctx context.Context, request ContextVMRequest, op string, payload workerContextVMPayload, state string) (any, error) {
	if request.Event == nil {
		return nil, fmt.Errorf("worker intent requires an authenticated requester")
	}
	handler, ok := h.intentProcessor.Handler("worker").(*WorkerIntentHandler)
	if !ok || handler.reactor == nil || handler.reactor.workerRepo == nil {
		return nil, fmt.Errorf("worker intent handler is not configured")
	}
	worker, err := handler.reactor.workerRepo.GetByPubKey(ctx, payload.WorkerPubKey)
	if err != nil {
		return nil, err
	}
	desiredState := "active"
	labels := map[string]interface{}{}
	if worker != nil {
		if worker.SchedulingState != "" {
			desiredState = string(worker.SchedulingState)
		}
		for k, v := range worker.Labels {
			labels[k] = v
		}
	}
	if state != "" {
		desiredState = state
	}
	if op == "labels-update" {
		labels = make(map[string]interface{}, len(payload.Labels))
		for k, v := range payload.Labels {
			labels[k] = v
		}
	}
	content := map[string]interface{}{"worker_pubkey": payload.WorkerPubKey, "reason": payload.Reason,
		"scheduling_state": desiredState, "labels": labels}
	if op == "cleanup" {
		content["cleanup_mode"] = payload.CleanupMode
	}
	intentID := effectiveIdempotencyKey(request, request.Event.ID.Hex())
	if err := h.intentProcessor.ProcessInProcess(ctx, &Intent{Domain: "worker", Op: op, Coordinate: "worker:" + payload.WorkerPubKey, IntentID: intentID, Content: content, Actor: request.Event.PubKey.Hex()}); err != nil {
		return nil, err
	}
	return map[string]any{"status": "succeeded", "command": op, "worker_pubkey": payload.WorkerPubKey}, nil
}

func workerCommandAck(receipt *WorkerCommandReceipt) map[string]any {
	if receipt == nil {
		return map[string]any{"status": "accepted"}
	}
	return map[string]any{"status": "accepted", "receipt": receipt, "request_event_id": receipt.RequestEventID, "d_tag": receipt.DTag, "command": receipt.Command}
}
