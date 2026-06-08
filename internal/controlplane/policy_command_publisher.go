package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PolicyCommandPublisher emits canonical deployment-policy mutation commands.
type PolicyCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

func NewPolicyCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *PolicyCommandPublisher {
	return &PolicyCommandPublisher{publisher: publisher, signer: signer}
}

type PolicyMutationCommand struct {
	ID             uuid.UUID
	Name           string
	EnvironmentID  *uuid.UUID
	ArtifactID     uuid.UUID
	ServiceID      *uuid.UUID
	Rules          []domain.PolicyRule
	Enforcement    string
	Enabled        *bool
	IdempotencyKey string
	AgentID        string
}

type PolicyCommandReceipt struct {
	RequestEventID  string         `json:"request_event_id"`
	RequestPubkey   string         `json:"request_pubkey"`
	RequestKind     int            `json:"request_kind"`
	StatusKind      int            `json:"status_kind,omitempty"`
	ResultKind      int            `json:"result_kind"`
	ReadModelKinds  map[string]int `json:"read_model_kinds,omitempty"`
	DTag            string         `json:"d_tag,omitempty"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Status          string         `json:"status"`
	Error           string         `json:"error,omitempty"`
	RetryHint       string         `json:"retry_hint,omitempty"`
	PublishedRelays int            `json:"published_relays"`
	TimeoutSeconds  int            `json:"timeout_seconds,omitempty"`
	PolicyID        string         `json:"policy_id,omitempty"`
	PolicyName      string         `json:"policy_name,omitempty"`
	EnvironmentID   string         `json:"environment_id,omitempty"`
	ArtifactID      string         `json:"artifact_id,omitempty"`
	ServiceID       string         `json:"service_id,omitempty"`
}

func (p *PolicyCommandPublisher) PublishPolicyCreateRequest(ctx context.Context, cmd PolicyMutationCommand) (*PolicyCommandReceipt, error) {
	if strings.TrimSpace(cmd.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	if len(cmd.Rules) == 0 {
		return nil, fmt.Errorf("rules is required")
	}
	return p.publish(ctx, KindPolicyCreate, "policy-create", cmd, false)
}

func (p *PolicyCommandPublisher) PublishPolicyUpdateRequest(ctx context.Context, cmd PolicyMutationCommand) (*PolicyCommandReceipt, error) {
	if cmd.ID == uuid.Nil {
		return nil, fmt.Errorf("policy id is required")
	}
	return p.publish(ctx, KindPolicyUpdate, "policy-update", cmd, true)
}

func (p *PolicyCommandPublisher) PublishPolicyDeleteRequest(ctx context.Context, cmd PolicyMutationCommand) (*PolicyCommandReceipt, error) {
	if cmd.ID == uuid.Nil {
		return nil, fmt.Errorf("policy id is required")
	}
	return p.publish(ctx, KindPolicyDelete, "policy-delete", cmd, true)
}

func (p *PolicyCommandPublisher) PublishPolicyEvaluateRequest(ctx context.Context, cmd PolicyMutationCommand) (*PolicyCommandReceipt, error) {
	if cmd.ArtifactID == uuid.Nil {
		return nil, fmt.Errorf("artifact id is required")
	}
	if cmd.EnvironmentID == nil || *cmd.EnvironmentID == uuid.Nil {
		return nil, fmt.Errorf("environment id is required")
	}
	return p.publish(ctx, KindPolicyEvaluate, "policy-evaluate", cmd, false)
}

func (p *PolicyCommandPublisher) publish(ctx context.Context, kind int, defaultPrefix string, cmd PolicyMutationCommand, includeID bool) (*PolicyCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("policy command publisher is not configured")
	}
	content := map[string]any{}
	if includeID || kind == KindPolicyDelete {
		content["id"] = cmd.ID.String()
	}
	if kind == KindPolicyEvaluate {
		content["artifact_id"] = cmd.ArtifactID.String()
		if cmd.EnvironmentID != nil && *cmd.EnvironmentID != uuid.Nil {
			content["environment_id"] = cmd.EnvironmentID.String()
		}
		if cmd.ServiceID != nil && *cmd.ServiceID != uuid.Nil {
			content["service_id"] = cmd.ServiceID.String()
		}
	} else if kind != KindPolicyDelete {
		if strings.TrimSpace(cmd.Name) != "" {
			content["name"] = strings.TrimSpace(cmd.Name)
		}
		if cmd.Rules != nil {
			content["rules"] = cmd.Rules
		}
		if strings.TrimSpace(cmd.Enforcement) != "" {
			content["enforcement"] = cmd.Enforcement
		}
		if cmd.Enabled != nil {
			content["enabled"] = *cmd.Enabled
		}
		if cmd.EnvironmentID != nil && *cmd.EnvironmentID != uuid.Nil {
			content["environment_id"] = cmd.EnvironmentID.String()
		}
	}
	tags := nostr.Tags{}
	if cmd.ID != uuid.Nil {
		tags = append(tags, nostr.Tag{"policy", cmd.ID.String()})
	}
	if strings.TrimSpace(cmd.Name) != "" {
		tags = append(tags, nostr.Tag{"policy_name", strings.TrimSpace(cmd.Name)})
	}
	if cmd.EnvironmentID != nil && *cmd.EnvironmentID != uuid.Nil {
		tags = append(tags, nostr.Tag{"environment", cmd.EnvironmentID.String()})
	}
	if cmd.ArtifactID != uuid.Nil {
		tags = append(tags, nostr.Tag{"artifact", cmd.ArtifactID.String()})
	}
	if cmd.ServiceID != nil && *cmd.ServiceID != uuid.Nil {
		tags = append(tags, nostr.Tag{"service", cmd.ServiceID.String()})
	}
	dTag := strings.TrimSpace(cmd.IdempotencyKey)
	if dTag == "" {
		dTag = defaultPrefix + ":" + uuid.NewString()
	}
	body, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal policy command: %w", err)
	}
	eventTags := nostr.Tags{{"d", dTag}}
	eventTags = append(eventTags, compactTags(tags)...)
	if agentID := strings.TrimSpace(cmd.AgentID); agentID != "" {
		eventTags = append(eventTags, nostr.Tag{"agent", agentID})
	}
	ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: eventTags, Content: string(body)}
	if err := SignGoNostrEvent(ctx, p.signer, ev); err != nil {
		return nil, fmt.Errorf("sign policy command: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *ev)
	if err != nil {
		if published > 0 {
			receipt := policyReceiptFromEvent(ev, dTag, published, "error")
			receipt.Error = err.Error()
			return receipt, nil
		}
		return nil, fmt.Errorf("publish policy command: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish policy command: no relay accepted the request; retry after relay reconnect")
	}
	return policyReceiptFromEvent(ev, dTag, published, "submitted"), nil
}

func policyReceiptFromEvent(ev *nostr.Event, dTag string, published int, status string) *PolicyCommandReceipt {
	receipt := &PolicyCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: ev.Kind, ResultKind: KindContextVMMessage, ReadModelKinds: policyReadModels(ev.Kind), DTag: dTag, IdempotencyKey: dTag, Status: status, PublishedRelays: published}
	populatePolicyReceiptTags(receipt, ev.Tags)
	return receipt
}

func populatePolicyReceiptTags(receipt *PolicyCommandReceipt, tags nostr.Tags) {
	receipt.PolicyID = tagValueNostr(tags, "policy")
	receipt.PolicyName = tagValueNostr(tags, "policy_name")
	receipt.EnvironmentID = tagValueNostr(tags, "environment")
	receipt.ArtifactID = tagValueNostr(tags, "artifact")
	receipt.ServiceID = tagValueNostr(tags, "service")
}

func policyReadModels(kind int) map[string]int {
	if kind == KindPolicyEvaluate {
		return nil
	}
	return map[string]int{"policy_registry": KindCASControlState}
}
