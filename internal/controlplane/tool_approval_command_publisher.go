package controlplane

import (
	"context"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
)

// ContextVMMethodToolApprovalResponse is the operator reply to the
// "tool/approval-request" ContextVM message Bahia's ToolResponder emits.
const ContextVMMethodToolApprovalResponse = "tool/approval-response"

// ToolApprovalCommandPublisher emits canonical tool provisioning approval
// responses as signed ContextVM kind 25910 requests. Legacy kind 7977 is a
// migration input only and is never published.
type ToolApprovalCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

func NewToolApprovalCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *ToolApprovalCommandPublisher {
	return &ToolApprovalCommandPublisher{publisher: publisher, signer: signer}
}

type ToolApprovalCommand struct {
	IntentID       uuid.UUID
	Action         string
	Reason         string
	IdempotencyKey string
	AgentID        string
}

type ToolApprovalCommandReceipt struct {
	RequestEventID  string `json:"request_event_id"`
	RequestPubkey   string `json:"request_pubkey"`
	RequestKind     int    `json:"request_kind"`
	ResultKind      int    `json:"result_kind"`
	ReadModelKind   int    `json:"read_model_kind"`
	DTag            string `json:"d_tag"`
	IdempotencyKey  string `json:"idempotency_key"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	PublishedRelays int    `json:"published_relays"`
	IntentID        string `json:"intent_id"`
	Action          string `json:"action"`
}

func (p *ToolApprovalCommandPublisher) PublishToolApprovalResponse(ctx context.Context, cmd ToolApprovalCommand) (*ToolApprovalCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("tool approval command publisher is not configured")
	}
	if cmd.IntentID == uuid.Nil {
		return nil, fmt.Errorf("intent id is required")
	}
	action := strings.ToLower(strings.TrimSpace(cmd.Action))
	if action != "approve" && action != "reject" {
		return nil, fmt.Errorf("action must be 'approve' or 'reject'")
	}
	reason := strings.TrimSpace(cmd.Reason)
	if reason == "" {
		return nil, fmt.Errorf("reason is required")
	}
	dTag := strings.TrimSpace(cmd.IdempotencyKey)
	if dTag == "" {
		dTag = "tool-approval:" + cmd.IntentID.String() + ":" + action
	}
	content := map[string]any{"intent_id": cmd.IntentID.String(), "action": action, "reason": reason}
	tags := nostr.Tags{{"intent", cmd.IntentID.String()}, {"action", action}}
	ev, published, dTag, err := publishContextVMCommand(ctx, p.publisher, p.signer, ContextVMMethodToolApprovalResponse, dTag, cmd.AgentID, tags, content, "tool approval response")
	if err != nil {
		if ev != nil && published > 0 {
			receipt := toolApprovalReceiptFromEvent(ev, dTag, published, "error")
			receipt.Error = err.Error()
			return receipt, nil
		}
		return nil, err
	}
	return toolApprovalReceiptFromEvent(ev, dTag, published, "submitted"), nil
}

func toolApprovalReceiptFromEvent(ev *nostr.Event, dTag string, published int, status string) *ToolApprovalCommandReceipt {
	return &ToolApprovalCommandReceipt{
		RequestEventID:  ev.ID.Hex(),
		RequestPubkey:   ev.PubKey.Hex(),
		RequestKind:     int(ev.Kind),
		ResultKind:      KindContextVMMessage,
		ReadModelKind:   KindCASControlState,
		DTag:            dTag,
		IdempotencyKey:  dTag,
		Status:          status,
		PublishedRelays: published,
		IntentID:        tagValueNostr(ev.Tags, "intent"),
		Action:          tagValueNostr(ev.Tags, "action"),
	}
}
