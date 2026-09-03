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

// ServiceCommandPublisher emits canonical service deployment command events.
type ServiceCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

func NewServiceCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *ServiceCommandPublisher {
	return &ServiceCommandPublisher{publisher: publisher, signer: signer}
}

type ServiceCreateCommand struct {
	Name                 string
	OrgID                uuid.UUID
	RepoURL              string
	Repository           any
	ArtifactRepo         string
	DefaultBranch        string
	RuntimeType          string
	ManagedRuntimeConfig *domain.ManagedRuntimeConfig
	IdempotencyKey       string
	AgentID              string
}

type ServiceUpdateCommand struct {
	ID                       uuid.UUID
	Name                     *string
	RepoURL                  *string
	Repository               any
	ArtifactRepo             *string
	DefaultBranch            *string
	RuntimeType              *string
	ManagedRuntimeConfig     *domain.ManagedRuntimeConfig
	AdoptedPublicEnvironment map[string]string
	IdempotencyKey           string
	AgentID                  string
}

type ServiceDeployCommand struct {
	ServiceID        uuid.UUID
	EnvironmentID    uuid.UUID
	DeploymentUnitID *uuid.UUID
	ArtifactID       uuid.UUID
	RequestedBy      string
	SourceKind       string
	Metadata         map[string]any
	IdempotencyKey   string
	AgentID          string
}

type ServiceRollbackCommand struct {
	ServiceID      uuid.UUID
	EnvironmentID  uuid.UUID
	IdempotencyKey string
	AgentID        string
}

type ServiceApprovalCommand struct {
	IntentID       uuid.UUID
	Decision       string
	IdempotencyKey string
	AgentID        string
}

type ServiceCommandReceipt struct {
	RequestEventID  string `json:"request_event_id"`
	RequestPubkey   string `json:"request_pubkey"`
	RequestKind     int    `json:"request_kind"`
	StatusKind      int    `json:"status_kind"`
	ResultKind      int    `json:"result_kind"`
	RegistryKind    int    `json:"registry_kind,omitempty"`
	StateKind       int    `json:"state_kind,omitempty"`
	DTag            string `json:"d_tag,omitempty"`
	IdempotencyKey  string `json:"idempotency_key"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	RetryHint       string `json:"retry_hint,omitempty"`
	PublishedRelays int    `json:"published_relays"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	ServiceID       string `json:"service_id,omitempty"`
	ServiceName     string `json:"service_name,omitempty"`
	EnvironmentID   string `json:"environment_id,omitempty"`
	ArtifactID      string `json:"artifact_id,omitempty"`
	IntentID        string `json:"intent_id,omitempty"`
	Decision        string `json:"decision,omitempty"`
}

func (p *ServiceCommandPublisher) PublishServiceCreateRequest(ctx context.Context, cmd ServiceCreateCommand) (*ServiceCommandReceipt, error) {
	name := strings.TrimSpace(cmd.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	content := map[string]any{"name": name}
	if cmd.OrgID != uuid.Nil {
		content["org_id"] = cmd.OrgID.String()
	}
	if cmd.RepoURL != "" {
		content["repo_url"] = cmd.RepoURL
	}
	if cmd.Repository != nil {
		content["repository"] = cmd.Repository
	}
	if cmd.ArtifactRepo != "" {
		content["artifact_repo"] = cmd.ArtifactRepo
	}
	if cmd.DefaultBranch != "" {
		content["default_branch"] = cmd.DefaultBranch
	}
	if cmd.RuntimeType != "" {
		content["runtime_type"] = cmd.RuntimeType
	}
	if cmd.ManagedRuntimeConfig != nil {
		content["managed_runtime_config"] = domain.NormalizeManagedRuntimeConfig(cmd.ManagedRuntimeConfig)
	}
	tags := nostr.Tags{{"service", name}}
	receipt, err := p.publish(ctx, ContextVMMethodServiceCreate, tags, content, cmd.IdempotencyKey, cmd.AgentID)
	if receipt != nil {
		receipt.ServiceName = name
		receipt.RegistryKind = KindCASControlState
		receipt.StateKind = KindCASControlState
	}
	return receipt, err
}

func (p *ServiceCommandPublisher) PublishServiceUpdateRequest(ctx context.Context, cmd ServiceUpdateCommand) (*ServiceCommandReceipt, error) {
	if cmd.ID == uuid.Nil {
		return nil, fmt.Errorf("service_id is required")
	}
	content := map[string]any{"id": cmd.ID.String()}
	if cmd.Name != nil {
		content["name"] = strings.TrimSpace(*cmd.Name)
	}
	if cmd.RepoURL != nil {
		content["repo_url"] = strings.TrimSpace(*cmd.RepoURL)
	}
	if cmd.Repository != nil {
		content["repository"] = cmd.Repository
	}
	if cmd.ArtifactRepo != nil {
		content["artifact_repo"] = strings.TrimSpace(*cmd.ArtifactRepo)
	}
	if cmd.DefaultBranch != nil {
		content["default_branch"] = strings.TrimSpace(*cmd.DefaultBranch)
	}
	if cmd.RuntimeType != nil {
		content["runtime_type"] = strings.TrimSpace(*cmd.RuntimeType)
	}
	if cmd.ManagedRuntimeConfig != nil {
		content["managed_runtime_config"] = domain.NormalizeManagedRuntimeConfig(cmd.ManagedRuntimeConfig)
	}
	if len(cmd.AdoptedPublicEnvironment) > 0 {
		content["adopted_public_environment"] = cmd.AdoptedPublicEnvironment
	}
	tags := nostr.Tags{{"service", cmd.ID.String()}}
	receipt, err := p.publish(ctx, ContextVMMethodServiceUpdate, tags, content, cmd.IdempotencyKey, cmd.AgentID)
	if receipt != nil {
		receipt.ServiceID = cmd.ID.String()
		receipt.RegistryKind = KindCASControlState
		receipt.StateKind = KindCASControlState
	}
	return receipt, err
}

func (p *ServiceCommandPublisher) PublishDeployRequest(ctx context.Context, cmd ServiceDeployCommand) (*ServiceCommandReceipt, error) {
	content := map[string]any{"service_id": cmd.ServiceID.String(), "environment_id": cmd.EnvironmentID.String(), "artifact_id": cmd.ArtifactID.String()}
	if cmd.DeploymentUnitID != nil && *cmd.DeploymentUnitID != uuid.Nil {
		content["deployment_unit_id"] = cmd.DeploymentUnitID.String()
	}
	if cmd.RequestedBy != "" {
		content["requested_by"] = cmd.RequestedBy
	}
	if cmd.SourceKind != "" {
		content["source_kind"] = cmd.SourceKind
	}
	if len(cmd.Metadata) > 0 {
		content["metadata"] = cmd.Metadata
	}
	tags := nostr.Tags{{"service", cmd.ServiceID.String()}, {"environment", cmd.EnvironmentID.String()}, {"artifact", cmd.ArtifactID.String()}}
	if cmd.DeploymentUnitID != nil && *cmd.DeploymentUnitID != uuid.Nil {
		tags = append(tags, nostr.Tag{"deployment_unit", cmd.DeploymentUnitID.String()})
	}
	receipt, err := p.publish(ctx, ContextVMMethodServiceDeploy, tags, content, cmd.IdempotencyKey, cmd.AgentID)
	if receipt != nil {
		receipt.ServiceID = cmd.ServiceID.String()
		receipt.EnvironmentID = cmd.EnvironmentID.String()
		receipt.ArtifactID = cmd.ArtifactID.String()
		receipt.RegistryKind = KindDeploymentIntentRegistry
		receipt.StateKind = KindCASControlState
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
		receipt.RegistryKind = KindDeploymentIntentRegistry
		receipt.StateKind = KindCASControlState
	}
	return receipt, err
}

func (p *ServiceCommandPublisher) PublishDeploymentApprovalRequest(ctx context.Context, cmd ServiceApprovalCommand) (*ServiceCommandReceipt, error) {
	decision := strings.ToLower(strings.TrimSpace(cmd.Decision))
	if decision != "approve" && decision != "reject" {
		return nil, fmt.Errorf("decision must be approve or reject")
	}
	if cmd.IntentID == uuid.Nil {
		return nil, fmt.Errorf("intent_id is required")
	}
	method := ContextVMMethodApprovalApprove
	if decision == "reject" {
		method = "approval/reject"
	}
	content := map[string]any{"intent_id": cmd.IntentID.String(), "decision": decision, "approved": decision == "approve"}
	tags := nostr.Tags{{"intent", cmd.IntentID.String()}, {"decision", decision}}
	receipt, err := p.publish(ctx, method, tags, content, cmd.IdempotencyKey, cmd.AgentID)
	if receipt != nil {
		receipt.IntentID = cmd.IntentID.String()
		receipt.Decision = decision
		receipt.RegistryKind = KindDeploymentIntentRegistry
		receipt.StateKind = KindCASControlState
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
			return &ServiceCommandReceipt{RequestEventID: ev.ID.Hex(), RequestPubkey: ev.PubKey.Hex(), RequestKind: KindContextVMMessage, StatusKind: KindNIP38Status, ResultKind: KindContextVMMessage, DTag: dTag, IdempotencyKey: dTag, Status: "error", Error: err.Error(), PublishedRelays: published}, nil
		}
		return nil, err
	}
	return &ServiceCommandReceipt{RequestEventID: ev.ID.Hex(), RequestPubkey: ev.PubKey.Hex(), RequestKind: KindContextVMMessage, StatusKind: KindNIP38Status, ResultKind: KindContextVMMessage, DTag: dTag, IdempotencyKey: dTag, Status: "submitted", PublishedRelays: published}, nil
}
