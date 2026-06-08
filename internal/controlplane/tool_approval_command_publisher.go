package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
)

// ToolApprovalCommandPublisher emits canonical tool provisioning approval responses.
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
	body, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal tool approval response: %w", err)
	}
	tags := nostr.Tags{{"d", dTag}, {"intent", cmd.IntentID.String()}, {"action", action}}
	if agentID := strings.TrimSpace(cmd.AgentID); agentID != "" {
		tags = append(tags, nostr.Tag{"agent", agentID})
	}
	ev := &nostr.Event{Kind: KindToolApprovalResponse, CreatedAt: nostr.Now(), Tags: tags, Content: string(body)}
	if err := SignGoNostrEvent(ctx, p.signer, ev); err != nil {
		return nil, fmt.Errorf("sign tool approval response: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *ev)
	if err != nil {
		if published > 0 {
			receipt := toolApprovalReceiptFromEvent(ev, dTag, published, "error")
			receipt.Error = err.Error()
			return receipt, nil
		}
		return nil, fmt.Errorf("publish tool approval response: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish tool approval response: no relay accepted the request; retry after relay reconnect")
	}
	return toolApprovalReceiptFromEvent(ev, dTag, published, "submitted"), nil
}

func toolApprovalReceiptFromEvent(ev *nostr.Event, dTag string, published int, status string) *ToolApprovalCommandReceipt {
	return &ToolApprovalCommandReceipt{
		RequestEventID:  ev.ID,
		RequestPubkey:   ev.PubKey,
		RequestKind:     ev.Kind,
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
