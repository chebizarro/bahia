package controlplane

import (
	"context"
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
	IdempotencyKey  string `json:"idempotency_key"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	RetryHint       string `json:"retry_hint,omitempty"`
	PublishedRelays int    `json:"published_relays"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	ServiceID       string `json:"service_id,omitempty"`
	EnvironmentID   string `json:"environment_id,omitempty"`
	ArtifactID      string `json:"artifact_id,omitempty"`
}

func (p *ServiceCommandPublisher) PublishDeployRequest(ctx context.Context, cmd ServiceDeployCommand) (*ServiceCommandReceipt, error) {
	content := map[string]any{"service_id": cmd.ServiceID.String(), "environment_id": cmd.EnvironmentID.String(), "artifact_id": cmd.ArtifactID.String()}
	tags := nostr.Tags{{"service", cmd.ServiceID.String()}, {"environment", cmd.EnvironmentID.String()}, {"artifact", cmd.ArtifactID.String()}}
	receipt, err := p.publish(ctx, ContextVMMethodServiceDeploy, tags, content, cmd.IdempotencyKey, cmd.AgentID)
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
	receipt, err := p.publish(ctx, "service/rollback", tags, content, cmd.IdempotencyKey, cmd.AgentID)
	if receipt != nil {
		receipt.ServiceID = cmd.ServiceID.String()
		receipt.EnvironmentID = cmd.EnvironmentID.String()
	}
	return receipt, err
}

func (p *ServiceCommandPublisher) publish(ctx context.Context, method string, tags nostr.Tags, content map[string]any, dTag, agentID string) (*ServiceCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("service command publisher is not configured")
	}
	dTag = strings.TrimSpace(dTag)
	if dTag == "" {
		dTag = fmt.Sprintf("service-command:%s:%s", method, uuid.NewString())
	}
	ev, published, dTag, err := publishContextVMCommand(ctx, p.publisher, p.signer, method, dTag, agentID, tags, content, "service command")
	if err != nil {
		if ev != nil && published > 0 {
			return &ServiceCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: KindContextVMMessage, ResultKind: KindCASControlState, DTag: dTag, IdempotencyKey: dTag, Status: "error", Error: err.Error(), PublishedRelays: published}, nil
		}
		return nil, err
	}
	return &ServiceCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: KindContextVMMessage, ResultKind: KindCASControlState, DTag: dTag, IdempotencyKey: dTag, Status: "submitted", PublishedRelays: published}, nil
}
