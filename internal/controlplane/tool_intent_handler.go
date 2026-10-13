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

// ToolCanonicalExecutor consumes only fully signed, locally observed tool
// intents. Its implementation binds the request and decision under the
// non-PostgreSQL effect fence; no SQL provisioning row authorizes a step.
type ToolCanonicalExecutor interface {
	AdmitRequest(context.Context, *Intent) error
	Decide(context.Context, *Intent) error
}

// ToolIntentHandler validates operator-signed approval intents before the
// reactor's explicit refusal; SQL-derived execution inputs are not authority.
type ToolIntentHandler struct {
	reactor  *Reactor
	executor ToolCanonicalExecutor
}

var errToolApprovalAuthority = errors.New("tool approval signed authority is invalid")

func NewToolIntentHandler(reactor *Reactor, executor ...ToolCanonicalExecutor) *ToolIntentHandler {
	h := &ToolIntentHandler{reactor: reactor}
	if len(executor) > 0 {
		h.executor = executor[0]
	}
	return h
}
func (*ToolIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermApproveDeployments
}
func (*ToolIntentHandler) IsFleetScoped() bool { return true }

func (h *ToolIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if intent != nil && intent.Op == "request" {
		if err := validateToolRequestSignedEnvelope(intent); err != nil {
			return err
		}
		if h.executor == nil {
			return fmt.Errorf("bahia-4f10q.2.1: canonical tool request executor is unavailable")
		}
		return h.executor.AdmitRequest(ctx, intent)
	}
	if err := validateToolApprovalSignedEnvelope(intent); err != nil {
		return err
	}
	if h.executor != nil {
		return h.executor.Decide(ctx, intent)
	}
	if h.reactor == nil {
		return fmt.Errorf("tool approval reactor is not configured")
	}
	return h.reactor.handleToolApprovalResponse(ctx, intent.Event)
}

func (h *ToolIntentHandler) AfterAccepted(ctx context.Context, intent *Intent) {
	if after, ok := h.executor.(interface {
		AfterAccepted(context.Context, *Intent)
	}); ok {
		after.AfterAccepted(ctx, intent)
	}
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
		case "intent_id", "action", "reason", "request_event_id":
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

func validateToolRequestSignedEnvelope(intent *Intent) error {
	if intent == nil || intent.Op != "request" || intent.Event == nil || !intent.Event.CheckID() ||
		!intent.Event.VerifySignature() || intent.Actor != intent.Event.PubKey.Hex() {
		return fmt.Errorf("%w: tool request requires an exact operator-signed event", errToolApprovalAuthority)
	}
	parsed, err := ParseIntent(intent.Event)
	if err != nil || parsed.Domain != "tool" || parsed.Op != "request" || parsed.Schema != "bahia.intent.tool.v1" ||
		parsed.Coordinate != intent.Coordinate || parsed.IntentID != intent.IntentID ||
		!toolApprovalSameContent(parsed.Content, intent.Content) ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "d", intent.Coordinate) ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "domain", "tool") ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "op", "request") ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "schema", "bahia.intent.tool.v1") ||
		!toolApprovalTagExactlyOnce(intent.Event.Tags, "intent_id", intent.IntentID) {
		return fmt.Errorf("%w: tool request envelope differs from its signed event", errToolApprovalAuthority)
	}
	for key := range parsed.Content {
		switch key {
		case "id", "service_id", "environment_id", "operation", "tools", "reason", "intent_id":
		default:
			return fmt.Errorf("%w: unsupported tool request field %q", errToolApprovalAuthority, key)
		}
	}
	var request struct {
		ID            uuid.UUID            `json:"id"`
		ServiceID     uuid.UUID            `json:"service_id"`
		EnvironmentID uuid.UUID            `json:"environment_id"`
		Operation     string               `json:"operation"`
		Tools         []domain.ToolRequest `json:"tools"`
		Reason        string               `json:"reason"`
	}
	if err := json.Unmarshal([]byte(intent.Event.Content), &request); err != nil {
		return fmt.Errorf("%w: parse tool request: %v", errToolApprovalAuthority, err)
	}
	if request.ID == uuid.Nil || request.ID.Version() != 7 || request.ServiceID == uuid.Nil ||
		request.EnvironmentID == uuid.Nil || intent.Coordinate != "tool-request:"+request.ID.String() ||
		request.Operation != "install" || len(request.Tools) == 0 || strings.TrimSpace(request.Reason) == "" ||
		intent.ExpectedUpdatedAt != nil {
		return fmt.Errorf("%w: tool request ID, coordinate, target, operation, tools, or reason is invalid", errToolApprovalAuthority)
	}
	for _, tool := range request.Tools {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Manager) == "" ||
			strings.TrimSpace(tool.Version) == "" || strings.EqualFold(strings.TrimSpace(tool.Version), "latest") {
			return fmt.Errorf("%w: tool requests require explicit package names, managers and pinned versions", errToolApprovalAuthority)
		}
	}
	return nil
}

// ValidateToolRequestSignedEnvelope is also used by the DB-less execution
// source when replaying a retained request after restart.
func ValidateToolRequestSignedEnvelope(intent *Intent) error {
	return validateToolRequestSignedEnvelope(intent)
}

// ValidateToolApprovalSignedEnvelope gives the replay source the exact same
// signed-decision grammar as live intent processing.
func ValidateToolApprovalSignedEnvelope(intent *Intent) error {
	return validateToolApprovalSignedEnvelope(intent)
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
