package controlplane

import (
	"context"
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
}

func (p *PolicyCommandPublisher) PublishPolicyCreateRequest(ctx context.Context, cmd PolicyMutationCommand) (*PolicyCommandReceipt, error) {
	if strings.TrimSpace(cmd.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	return p.publish(ctx, ContextVMMethodPolicyCreate, "policy-create", cmd, false)
}

func (p *PolicyCommandPublisher) PublishPolicyUpdateRequest(ctx context.Context, cmd PolicyMutationCommand) (*PolicyCommandReceipt, error) {
	if cmd.ID == uuid.Nil {
		return nil, fmt.Errorf("policy id is required")
	}
	return p.publish(ctx, ContextVMMethodPolicyUpdate, "policy-update", cmd, true)
}

func (p *PolicyCommandPublisher) PublishPolicyDeleteRequest(ctx context.Context, cmd PolicyMutationCommand) (*PolicyCommandReceipt, error) {
	if cmd.ID == uuid.Nil {
		return nil, fmt.Errorf("policy id is required")
	}
	return p.publish(ctx, ContextVMMethodPolicyDelete, "policy-delete", cmd, true)
}

func (p *PolicyCommandPublisher) publish(ctx context.Context, method, defaultPrefix string, cmd PolicyMutationCommand, includeID bool) (*PolicyCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("policy command publisher is not configured")
	}
	content := map[string]any{}
	if includeID {
		content["id"] = cmd.ID.String()
	}
	if method != ContextVMMethodPolicyDelete {
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
	dTag := strings.TrimSpace(cmd.IdempotencyKey)
	if dTag == "" {
		dTag = defaultPrefix + ":" + uuid.NewString()
	}
	ev, published, dTag, err := publishContextVMCommand(ctx, p.publisher, p.signer, method, dTag, cmd.AgentID, tags, content, "policy command")
	if err != nil {
		if ev != nil && published > 0 {
			receipt := &PolicyCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: KindContextVMMessage, StatusKind: KindNIP38Status, ResultKind: KindContextVMMessage, ReadModelKinds: policyReadModels(), DTag: dTag, IdempotencyKey: dTag, Status: "error", Error: err.Error(), PublishedRelays: published}
			populatePolicyReceiptTags(receipt, ev.Tags)
			return receipt, nil
		}
		return nil, err
	}
	receipt := &PolicyCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: KindContextVMMessage, StatusKind: KindNIP38Status, ResultKind: KindContextVMMessage, ReadModelKinds: policyReadModels(), DTag: dTag, IdempotencyKey: dTag, Status: "submitted", PublishedRelays: published}
	populatePolicyReceiptTags(receipt, ev.Tags)
	return receipt, nil
}

func populatePolicyReceiptTags(receipt *PolicyCommandReceipt, tags nostr.Tags) {
	receipt.PolicyID = tagValueNostr(tags, "policy")
	receipt.PolicyName = tagValueNostr(tags, "policy_name")
	receipt.EnvironmentID = tagValueNostr(tags, "environment")
}

func policyReadModels() map[string]int {
	return map[string]int{"policy_registry": KindCASControlState}
}
