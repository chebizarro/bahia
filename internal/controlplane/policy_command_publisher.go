package controlplane

import (
	"context"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ContextVMMethodPolicyEvaluate is the ContextVM method for deployment-policy
// evaluation. It pairs with ContextVMMethodPolicyCreate/Update/Delete and is the
// method name the web control plane already publishes.
const ContextVMMethodPolicyEvaluate = "policy/evaluate"

// PolicyCommandPublisher emits canonical deployment-policy mutation commands as
// signed ContextVM kind 25910 requests. Legacy request kinds 5986-5989 are
// migration inputs only and are never published.
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

func (p *PolicyCommandPublisher) PublishPolicyEvaluateRequest(ctx context.Context, cmd PolicyMutationCommand) (*PolicyCommandReceipt, error) {
	if cmd.ArtifactID == uuid.Nil {
		return nil, fmt.Errorf("artifact id is required")
	}
	if cmd.EnvironmentID == nil || *cmd.EnvironmentID == uuid.Nil {
		return nil, fmt.Errorf("environment id is required")
	}
	return p.publish(ctx, ContextVMMethodPolicyEvaluate, "policy-evaluate", cmd, false)
}

func (p *PolicyCommandPublisher) publish(ctx context.Context, method string, defaultPrefix string, cmd PolicyMutationCommand, includeID bool) (*PolicyCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("policy command publisher is not configured")
	}
	content := map[string]any{}
	if includeID || method == ContextVMMethodPolicyDelete {
		content["id"] = cmd.ID.String()
	}
	if method == ContextVMMethodPolicyEvaluate {
		content["artifact_id"] = cmd.ArtifactID.String()
		if cmd.EnvironmentID != nil && *cmd.EnvironmentID != uuid.Nil {
			content["environment_id"] = cmd.EnvironmentID.String()
		}
		if cmd.ServiceID != nil && *cmd.ServiceID != uuid.Nil {
			content["service_id"] = cmd.ServiceID.String()
		}
	} else if method != ContextVMMethodPolicyDelete {
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
	ev, published, dTag, err := publishContextVMCommand(ctx, p.publisher, p.signer, method, dTag, cmd.AgentID, tags, content, "policy command")
	if err != nil {
		if ev != nil && published > 0 {
			receipt := policyReceiptFromEvent(ev, method, dTag, published, "error")
			receipt.Error = err.Error()
			return receipt, nil
		}
		return nil, err
	}
	return policyReceiptFromEvent(ev, method, dTag, published, "submitted"), nil
}

func policyReceiptFromEvent(ev *nostr.Event, method, dTag string, published int, status string) *PolicyCommandReceipt {
	receipt := &PolicyCommandReceipt{RequestEventID: ev.ID.Hex(), RequestPubkey: ev.PubKey.Hex(), RequestKind: int(ev.Kind), StatusKind: KindNIP38Status, ResultKind: KindContextVMMessage, ReadModelKinds: policyReadModels(method), DTag: dTag, IdempotencyKey: dTag, Status: status, PublishedRelays: published}
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

func policyReadModels(method string) map[string]int {
	if method == ContextVMMethodPolicyEvaluate {
		return nil
	}
	return map[string]int{"policy_registry": KindCASControlState}
}
