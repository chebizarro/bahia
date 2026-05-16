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

// ServiceCommandPublisher emits canonical service deployment command events.
type ServiceCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

func NewServiceCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *ServiceCommandPublisher {
	return &ServiceCommandPublisher{publisher: publisher, signer: signer}
}

type ServiceDeployCommand struct {
	ServiceID      uuid.UUID
	EnvironmentID  uuid.UUID
	ArtifactID     uuid.UUID
	IdempotencyKey string
	AgentID        string
}

type ServiceRollbackCommand struct {
	ServiceID      uuid.UUID
	EnvironmentID  uuid.UUID
	IdempotencyKey string
	AgentID        string
}

type ServiceCommandReceipt struct {
	RequestEventID  string `json:"request_event_id"`
	RequestPubkey   string `json:"request_pubkey"`
	RequestKind     int    `json:"request_kind"`
	StatusKind      int    `json:"status_kind"`
	ResultKind      int    `json:"result_kind"`
	DTag            string `json:"d_tag,omitempty"`
	PublishedRelays int    `json:"published_relays"`
	ServiceID       string `json:"service_id,omitempty"`
	EnvironmentID   string `json:"environment_id,omitempty"`
	ArtifactID      string `json:"artifact_id,omitempty"`
}

func (p *ServiceCommandPublisher) PublishDeployRequest(ctx context.Context, cmd ServiceDeployCommand) (*ServiceCommandReceipt, error) {
	content := map[string]any{"service_id": cmd.ServiceID.String(), "environment_id": cmd.EnvironmentID.String(), "artifact_id": cmd.ArtifactID.String()}
	tags := nostr.Tags{{"service", cmd.ServiceID.String()}, {"environment", cmd.EnvironmentID.String()}, {"artifact", cmd.ArtifactID.String()}}
	receipt, err := p.publish(ctx, KindDeployRequest, tags, content, cmd.IdempotencyKey, cmd.AgentID)
	if receipt != nil {
		receipt.ServiceID = cmd.ServiceID.String()
		receipt.EnvironmentID = cmd.EnvironmentID.String()
		receipt.ArtifactID = cmd.ArtifactID.String()
	}
	return receipt, err
}

func (p *ServiceCommandPublisher) PublishRollbackRequest(ctx context.Context, cmd ServiceRollbackCommand) (*ServiceCommandReceipt, error) {
	content := map[string]any{"service_id": cmd.ServiceID.String(), "environment_id": cmd.EnvironmentID.String()}
	tags := nostr.Tags{{"service", cmd.ServiceID.String()}, {"environment", cmd.EnvironmentID.String()}}
	receipt, err := p.publish(ctx, KindRollbackRequest, tags, content, cmd.IdempotencyKey, cmd.AgentID)
	if receipt != nil {
		receipt.ServiceID = cmd.ServiceID.String()
		receipt.EnvironmentID = cmd.EnvironmentID.String()
	}
	return receipt, err
}

func (p *ServiceCommandPublisher) publish(ctx context.Context, kind int, tags nostr.Tags, content map[string]any, dTag, agentID string) (*ServiceCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("service command publisher is not configured")
	}
	dTag = strings.TrimSpace(dTag)
	if dTag != "" {
		tags = append(nostr.Tags{{"d", dTag}}, tags...)
	}
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		tags = append(tags, nostr.Tag{"agent", agentID})
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal service command content: %w", err)
	}
	ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: string(contentJSON)}
	if err := SignGoNostrEvent(ctx, p.signer, ev); err != nil {
		return nil, fmt.Errorf("sign service command event: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *ev)
	if err != nil {
		return nil, fmt.Errorf("publish service command event: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish service command event: no relay accepted the request")
	}
	return &ServiceCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: kind, StatusKind: KindDeploymentStatus, ResultKind: KindDeploymentResult, DTag: dTag, PublishedRelays: published}, nil
}
