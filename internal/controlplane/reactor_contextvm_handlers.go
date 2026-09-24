package controlplane

import (
	"context"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// RegisterMutationContextVMHandlers binds completed reactor mutations directly
// to the transport. The transport owns response correlation, encryption and replay;
// handlers must not republish the incoming command or send a second response.
func (r *Reactor) RegisterMutationContextVMHandlers(transport *EncryptedRequestTransport, gate *FleetOperatorGate) {
	if transport == nil || r == nil {
		return
	}
	transport.RegisterContextVMHandler(ContextVMMethodWorkerUncordon, gate.wrap(r.handleWorkerUncordonRequest))
	transport.RegisterContextVMHandler(ContextVMMethodWorkerUndrain, gate.wrap(r.handleWorkerUndrainRequest))
	transport.RegisterContextVMHandler(ContextVMMethodWorkerMaintenanceEnter, gate.wrap(r.handleWorkerMaintenanceEnterRequest))
	for method, handler := range map[string]ContextVMHandler{
		ContextVMMethodPolicyCreate:   r.handlePolicyCreate,
		ContextVMMethodPolicyUpdate:   r.handlePolicyUpdate,
		ContextVMMethodPolicyDelete:   r.handlePolicyDelete,
		ContextVMMethodPolicyEvaluate: r.handlePolicyEvaluate,
	} {
		transport.RegisterContextVMHandler(method, gate.wrap(func(ctx context.Context, request ContextVMRequest) (any, error) {
			if r.policyService == nil {
				return nil, fmt.Errorf("policy service is not configured")
			}
			return handler(ctx, request)
		}))
	}
}

func (r *Reactor) handleWorkerUncordonRequest(ctx context.Context, request ContextVMRequest) (any, error) {
	return r.handleWorkerSchedulingContextVM(ctx, request, WorkerCommandUncordon, domain.WorkerSchedulingActive)
}

func (r *Reactor) handleWorkerUndrainRequest(ctx context.Context, request ContextVMRequest) (any, error) {
	return r.handleWorkerSchedulingContextVM(ctx, request, WorkerCommandUndrain, domain.WorkerSchedulingActive)
}

func (r *Reactor) handleWorkerMaintenanceEnterRequest(ctx context.Context, request ContextVMRequest) (any, error) {
	return r.handleWorkerSchedulingContextVM(ctx, request, WorkerCommandMaintenanceEnter, domain.WorkerSchedulingMaintenance)
}

func (r *Reactor) handleWorkerSchedulingContextVM(ctx context.Context, request ContextVMRequest, command string, target domain.WorkerSchedulingState) (any, error) {
	var payload workerContextVMPayload
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	payload.WorkerPubKey = strings.TrimSpace(payload.WorkerPubKey)
	if !isHexNostrPubKey(payload.WorkerPubKey) {
		return nil, fmt.Errorf("worker_pubkey must be a 32-byte lowercase hex Nostr public key")
	}
	if !consistentOptionalTag(workerPubKeyFromEvent(request.Event), payload.WorkerPubKey) {
		return nil, fmt.Errorf("worker tag and worker_pubkey content must match")
	}
	if request.ProgressToken == "" {
		return nil, fmt.Errorf("idempotency_key or _meta.progressToken is required")
	}
	if !consistentOptionalTag(workerIdempotencyKey(request.Event), request.ProgressToken) {
		return nil, fmt.Errorf("d tag and idempotency key must match")
	}
	worker, _, err := r.updateWorkerSchedulingState(ctx, payload.WorkerPubKey, payload.Reason, command, target)
	if err != nil {
		return nil, err
	}
	if err := r.publishWorkerState(ctx, worker); err != nil {
		return nil, fmt.Errorf("worker scheduling state updated but publication failed: %w", err)
	}
	return map[string]any{"status": "succeeded", "command": command, "worker_pubkey": worker.PubKey, "scheduling_state": worker.SchedulingState, "worker": worker}, nil
}
