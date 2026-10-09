package controlplane

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type toolApprovalProcessor interface {
	ProcessIntent(context.Context, uuid.UUID) error
	ProcessApprovedIntent(context.Context, uuid.UUID) error
}

// ToolIntentHandler validates approval intents before the reactor's explicit
// fail-closed refusal; SQL-derived execution inputs are not authoritative.
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
	return h.reactor.handleToolApprovalResponse(ctx, &event)
}
