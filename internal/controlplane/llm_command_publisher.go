package controlplane

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
)

// NostrEventPublisher publishes signed Nostr events to the control-plane relay set.
type NostrEventPublisher interface {
	Publish(ctx context.Context, ev nostr.Event) (int, error)
}

// LLMCommandPublisher emits canonical LLM control-plane request events.
type LLMCommandPublisher struct {
	publisher  NostrEventPublisher
	privateKey string
	signer     EventSigner
}

// NewLLMCommandPublisher creates a publisher for MCP-originated LLM commands.
func NewLLMCommandPublisher(publisher NostrEventPublisher, privateKey string, signer EventSigner) *LLMCommandPublisher {
	return &LLMCommandPublisher{publisher: publisher, privateKey: privateKey, signer: signer}
}

// LLMDeployCommand describes a canonical LLM deploy request.
type LLMDeployCommand struct {
	RouteID       uuid.UUID
	EnvironmentID uuid.UUID
	ReleaseID     uuid.UUID
	RequestedBy   string
	Metadata      map[string]any
}

// LLMApprovalCommand describes a canonical LLM deployment approval/rejection request.
type LLMApprovalCommand struct {
	IntentID uuid.UUID
	Decision string
}

// LLMRollbackCommand describes a canonical LLM rollback request.
type LLMRollbackCommand struct {
	RouteID       uuid.UUID
	EnvironmentID uuid.UUID
	RequestedBy   string
}

// LLMCommandReceipt is the correlation handle returned to synchronous callers.
type LLMCommandReceipt struct {
	RequestEventID  string `json:"request_event_id"`
	RequestPubkey   string `json:"request_pubkey"`
	RequestKind     int    `json:"request_kind"`
	StatusKind      int    `json:"status_kind"`
	ResultKind      int    `json:"result_kind"`
	RegistryKind    int    `json:"registry_kind"`
	StateKind       int    `json:"state_kind"`
	PublishedRelays int    `json:"published_relays"`
	RouteID         string `json:"route_id,omitempty"`
	EnvironmentID   string `json:"environment_id,omitempty"`
	ReleaseID       string `json:"release_id,omitempty"`
	IntentID        string `json:"intent_id,omitempty"`
	Decision        string `json:"decision,omitempty"`
}

// PublishLLMDeployRequest publishes kind:5973 and returns correlation metadata.
func (p *LLMCommandPublisher) PublishLLMDeployRequest(ctx context.Context, cmd LLMDeployCommand) (*LLMCommandReceipt, error) {
	content := map[string]any{
		"route_id":       cmd.RouteID.String(),
		"environment_id": cmd.EnvironmentID.String(),
		"release_id":     cmd.ReleaseID.String(),
	}
	if cmd.RequestedBy != "" {
		content["requested_by"] = cmd.RequestedBy
	}
	if len(cmd.Metadata) > 0 {
		content["metadata"] = cmd.Metadata
	}
	tags := nostr.Tags{
		{"route", cmd.RouteID.String()},
		{"environment", cmd.EnvironmentID.String()},
		{"release", cmd.ReleaseID.String()},
	}
	receipt, err := p.publish(ctx, KindLLMDeployRequest, tags, content)
	if receipt != nil {
		receipt.RouteID = cmd.RouteID.String()
		receipt.EnvironmentID = cmd.EnvironmentID.String()
		receipt.ReleaseID = cmd.ReleaseID.String()
	}
	return receipt, err
}

// PublishLLMApprovalRequest publishes kind:5974 and returns correlation metadata.
func (p *LLMCommandPublisher) PublishLLMApprovalRequest(ctx context.Context, cmd LLMApprovalCommand) (*LLMCommandReceipt, error) {
	content := map[string]any{
		"intent_id": cmd.IntentID.String(),
		"decision":  cmd.Decision,
	}
	tags := nostr.Tags{
		{"intent", cmd.IntentID.String()},
		{"decision", cmd.Decision},
	}
	receipt, err := p.publish(ctx, KindLLMDeploymentApproval, tags, content)
	if receipt != nil {
		receipt.IntentID = cmd.IntentID.String()
		receipt.Decision = cmd.Decision
	}
	return receipt, err
}

// PublishLLMRollbackRequest publishes kind:5975 and returns correlation metadata.
func (p *LLMCommandPublisher) PublishLLMRollbackRequest(ctx context.Context, cmd LLMRollbackCommand) (*LLMCommandReceipt, error) {
	content := map[string]any{
		"route_id":       cmd.RouteID.String(),
		"environment_id": cmd.EnvironmentID.String(),
	}
	if cmd.RequestedBy != "" {
		content["requested_by"] = cmd.RequestedBy
	}
	tags := nostr.Tags{
		{"route", cmd.RouteID.String()},
		{"environment", cmd.EnvironmentID.String()},
	}
	receipt, err := p.publish(ctx, KindLLMRollbackRequest, tags, content)
	if receipt != nil {
		receipt.RouteID = cmd.RouteID.String()
		receipt.EnvironmentID = cmd.EnvironmentID.String()
	}
	return receipt, err
}

func (p *LLMCommandPublisher) publish(ctx context.Context, kind int, tags nostr.Tags, content map[string]any) (*LLMCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("LLM command publisher is not configured")
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal LLM command content: %w", err)
	}
	ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: string(contentJSON)}
	if p.signer != nil {
		if err := p.signer.Sign(ctx, ev); err != nil {
			return nil, fmt.Errorf("sign LLM command event: %w", err)
		}
	} else {
		if p.privateKey == "" {
			return nil, fmt.Errorf("LLM command publisher signing key is not configured")
		}
		if err := ev.Sign(p.privateKey); err != nil {
			return nil, fmt.Errorf("sign LLM command event: %w", err)
		}
	}
	published, err := p.publisher.Publish(ctx, *ev)
	if err != nil {
		return nil, fmt.Errorf("publish LLM command event: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish LLM command event: no relay accepted the request")
	}
	return &LLMCommandReceipt{
		RequestEventID:  ev.ID,
		RequestPubkey:   ev.PubKey,
		RequestKind:     kind,
		StatusKind:      KindLLMDeploymentStatus,
		ResultKind:      KindLLMDeploymentResult,
		RegistryKind:    KindLLMRouteRegistry,
		StateKind:       KindLLMRouteState,
		PublishedRelays: published,
	}, nil
}
