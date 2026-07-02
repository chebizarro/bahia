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
func RegisterWorkerContextVMHandlers(transport *EncryptedRequestTransport) {
	if transport == nil || transport.responder == nil {
		return
	}
	h := workerContextVMHandlers{publisher: NewWorkerCommandPublisher(transport.responder.publisher, transport.responder.signer)}
	transport.RegisterContextVMHandler(ContextVMMethodWorkerCleanup, h.cleanup)
	transport.RegisterContextVMHandler(ContextVMMethodWorkerCordon, h.cordon)
	transport.RegisterContextVMHandler(ContextVMMethodWorkerUncordon, h.uncordon)
	transport.RegisterContextVMHandler(ContextVMMethodWorkerDrain, h.drain)
	transport.RegisterContextVMHandler(ContextVMMethodWorkerUndrain, h.undrain)
	transport.RegisterContextVMHandler(ContextVMMethodWorkerMaintenanceEnter, h.maintenanceEnter)
	transport.RegisterContextVMHandler(ContextVMMethodWorkerMaintenanceExit, h.maintenanceExit)
	transport.RegisterContextVMHandler(ContextVMMethodWorkerLabelsUpdate, h.labelsUpdate)
}

type workerContextVMHandlers struct {
	publisher *WorkerCommandPublisher
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
	receipt, err := h.publisher.PublishWorkerCleanupRequest(ctx, cmd, payload.CleanupMode)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) cordon(ctx context.Context, request ContextVMRequest) (any, error) {
	_, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	receipt, err := h.publisher.PublishWorkerCordonRequest(ctx, cmd)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) uncordon(ctx context.Context, request ContextVMRequest) (any, error) {
	_, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	receipt, err := h.publisher.PublishWorkerUncordonRequest(ctx, cmd)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) drain(ctx context.Context, request ContextVMRequest) (any, error) {
	_, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	receipt, err := h.publisher.PublishWorkerDrainRequest(ctx, cmd)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) undrain(ctx context.Context, request ContextVMRequest) (any, error) {
	_, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	receipt, err := h.publisher.PublishWorkerUndrainRequest(ctx, cmd)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) maintenanceEnter(ctx context.Context, request ContextVMRequest) (any, error) {
	_, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
	}
	receipt, err := h.publisher.PublishWorkerMaintenanceEnterRequest(ctx, cmd)
	return workerCommandAck(receipt), err
}

func (h workerContextVMHandlers) maintenanceExit(ctx context.Context, request ContextVMRequest) (any, error) {
	_, cmd, err := h.lifecycleCommand(request)
	if err != nil {
		return nil, err
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

func workerCommandAck(receipt *WorkerCommandReceipt) map[string]any {
	if receipt == nil {
		return map[string]any{"status": "accepted"}
	}
	return map[string]any{"status": "accepted", "receipt": receipt, "request_event_id": receipt.RequestEventID, "d_tag": receipt.DTag, "command": receipt.Command}
}
