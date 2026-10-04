package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
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
	if r.intentProcessor != nil && r.intentProcessor.Handler("tool") != nil {
		var content map[string]any
		if err := json.Unmarshal(request.RPC.Params, &content); err != nil {
			return nil, err
		}
		id, err := uuid.Parse(fmt.Sprint(content["intent_id"]))
		if err != nil || id == uuid.Nil {
			return nil, fmt.Errorf("tool provisioning intent_id must be a UUID")
		}
		intent := &Intent{Event: request.Event, Domain: "tool", Op: "approval-response",
			IntentID: effectiveIdempotencyKey(request, request.Event.ID.Hex()), Coordinate: "tool-approval:" + id.String(),
			Content: content, Actor: request.Event.PubKey.Hex()}
		if err := r.intentProcessor.ProcessInProcess(ctx, intent); err != nil {
			return nil, err
		}
		return map[string]any{"status": "applied"}, nil
	}
	event := *request.Event
	event.Content = string(request.RPC.Params)
	if err := r.handleToolApprovalResponse(ctx, &event); err != nil {
		return nil, err
	}
	return map[string]any{"status": "applied"}, nil
}

// ToolIntentHandler applies one atomic approval decision. The reactor method
// owns approval logging, processing and result publication on both transports.
type ToolIntentHandler struct{ reactor *Reactor }

func NewToolIntentHandler(reactor *Reactor) *ToolIntentHandler {
	return &ToolIntentHandler{reactor: reactor}
}
func (*ToolIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermApproveDeployments
}
func (*ToolIntentHandler) IsFleetScoped() bool { return true }

func (h *ToolIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if intent.Op != "approval-response" {
		return fmt.Errorf("unsupported tool operation %q", intent.Op)
	}
	if h.reactor == nil {
		return fmt.Errorf("tool approval reactor is not configured")
	}
	if intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("tool approval does not support expected_updated_at")
	}
	raw, err := json.Marshal(intent.Content)
	if err != nil {
		return err
	}
	var decision struct {
		IntentID uuid.UUID `json:"intent_id"`
		Action   string    `json:"action"`
		Reason   string    `json:"reason"`
	}
	if err := json.Unmarshal(raw, &decision); err != nil {
		return err
	}
	if decision.IntentID == uuid.Nil || intent.Coordinate != "tool-approval:"+decision.IntentID.String() {
		return fmt.Errorf("tool approval coordinate does not match intent_id")
	}
	if decision.Action != "approve" && decision.Action != "reject" {
		return fmt.Errorf("tool approval action must be approve or reject")
	}
	if intent.Event == nil {
		return fmt.Errorf("tool approval requires a signed source event")
	}
	event := *intent.Event
	event.Content = string(raw)
	if err := h.reactor.handleToolApprovalResponse(ctx, &event); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return &intentStateConflictError{message: "tool approval decision conflicts with current provisioning state"}
		}
		return err
	}
	intent.StatusData = map[string]any{"provisioning_intent_id": decision.IntentID.String(), "action": decision.Action}
	intent.Result = intent.StatusData
	return nil
}
