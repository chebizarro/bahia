package controlplane

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type toolApprovalDecisionRepository interface {
	ApplyToolApprovalDecision(context.Context, uuid.UUID, domain.ToolProvisionStatus, string, time.Time) (*domain.ToolProvisionIntent, error)
}

type toolApprovalProcessor interface {
	ProcessIntent(context.Context, uuid.UUID) error
	ProcessApprovedIntent(context.Context, uuid.UUID) error
}

// RegisterToolApprovalContextVMHandlers exposes the single-use tool approval
// decision through the authenticated ContextVM mutation transport.
func (r *Reactor) RegisterToolApprovalContextVMHandlers(transport *EncryptedRequestTransport, gate *FleetOperatorGate) {
	if transport == nil || r == nil {
		return
	}
	transport.RegisterContextVMHandler(ContextVMMethodToolApprovalResponse, gate.wrap(r.handleToolApprovalContextVM))
}

func (r *Reactor) handleToolApprovalContextVM(ctx context.Context, request ContextVMRequest) (any, error) {
	if request.Event == nil {
		return nil, fmt.Errorf("tool approval request event is required")
	}
	event := *request.Event
	event.Content = string(request.RPC.Params)
	if err := r.handleToolApprovalResponse(ctx, &event); err != nil {
		return nil, err
	}
	return map[string]any{"status": "applied"}, nil
}
