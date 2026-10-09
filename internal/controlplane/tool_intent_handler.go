package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type toolApprovalProcessor interface {
	ProcessIntent(context.Context, uuid.UUID) error
	ProcessApprovedIntent(context.Context, uuid.UUID) error
}

// ToolIntentHandler validates operator-signed approval intents before the
// reactor's explicit refusal; SQL-derived execution inputs are not authority.
type ToolIntentHandler struct{ reactor *Reactor }

var errToolApprovalAuthority = errors.New("tool approval signed authority is invalid")

func NewToolIntentHandler(reactor *Reactor) *ToolIntentHandler {
	return &ToolIntentHandler{reactor: reactor}
}
func (*ToolIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermApproveDeployments
}
func (*ToolIntentHandler) IsFleetScoped() bool { return true }

func (h *ToolIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if err := validateToolApprovalSignedEnvelope(intent); err != nil {
		return err
	}
	if h.reactor == nil {
		return fmt.Errorf("tool approval reactor is not configured")
	}
	return h.reactor.handleToolApprovalResponse(ctx, intent.Event)
}

func validateToolApprovalSignedEnvelope(intent *Intent) error {
	if intent == nil {
		return fmt.Errorf("%w: source event is missing", errToolApprovalAuthority)
	}
	if intent.Op != "approval-response" {
		return fmt.Errorf("unsupported tool operation %q", intent.Op)
	}
	if intent.Event == nil || !intent.Event.CheckID() || !intent.Event.VerifySignature() || intent.Actor != intent.Event.PubKey.Hex() {
		return fmt.Errorf("%w: operator signature is missing or does not match the actor", errToolApprovalAuthority)
	}
	parsed, err := ParseIntent(intent.Event)
	if err != nil || parsed.Domain != "tool" || parsed.Op != "approval-response" ||
		parsed.Schema != "bahia.intent.tool.v1" || parsed.Schema != intent.Schema || parsed.Coordinate != intent.Coordinate ||
		parsed.IntentID != intent.IntentID || parsed.OrgID != intent.OrgID ||
		!toolApprovalSameContent(parsed.Content, intent.Content) ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "d", parsed.Coordinate) ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "domain", "tool") ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "op", "approval-response") ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "schema", "bahia.intent.tool.v1") ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "intent_id", intent.IntentID) {
		return fmt.Errorf("%w: envelope differs from the signed intent", errToolApprovalAuthority)
	}
	for key := range parsed.Content {
		switch key {
		case "intent_id", "action", "reason":
		default:
			return fmt.Errorf("tool approval contains unsupported field %q", key)
		}
	}
	raw := []byte(intent.Event.Content)
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
	if strings.TrimSpace(decision.Reason) == "" {
		return fmt.Errorf("tool approval requires a reason")
	}
	if intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("tool approval does not support expected_updated_at")
	}
	return nil
}

func toolApprovalSameContent(left, right map[string]any) bool {
	a, aErr := json.Marshal(left)
	b, bErr := json.Marshal(right)
	return aErr == nil && bErr == nil && bytes.Equal(a, b)
}

func toolApprovalTagExactlyOnce(tags nostr.Tags, key, value string) bool {
	count := 0
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == key {
			if len(tag) != 2 || tag[1] != value {
				return false
			}
			count++
		}
	}
	return count == 1
}
