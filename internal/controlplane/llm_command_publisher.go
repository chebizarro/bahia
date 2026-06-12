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

// NostrEventPublisher publishes signed Nostr events to the control-plane relay set.
type NostrEventPublisher interface {
	Publish(ctx context.Context, ev nostr.Event) (int, error)
}

// LLMCommandPublisher emits canonical LLM control-plane request events.
type LLMCommandPublisher struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
}

// NewLLMCommandPublisher creates a publisher for MCP-originated LLM commands.
func NewLLMCommandPublisher(publisher NostrEventPublisher, signer canonicalnostr.Signer) *LLMCommandPublisher {
	return &LLMCommandPublisher{publisher: publisher, signer: signer}
}

// LLMRouteCreateCommand describes a canonical LLM route-create request.
type LLMRouteCreateCommand struct {
	Name                   string
	Description            string
	GatewayConfig          *domain.LLMGatewayRouteConfig
	DefaultPlacementPolicy *domain.LLMPlacementPolicy
	DefaultPromotionGate   *domain.LLMPromotionGateConfig
	Metadata               map[string]any
	IdempotencyKey         string
	AgentID                string
}

// LLMReleaseRegisterCommand describes a canonical LLM release-register request.
type LLMReleaseRegisterCommand struct {
	RouteID            uuid.UUID
	Version            string
	ModelRef           string
	ModelSource        string
	ModelRevision      string
	EstimatedVRAMGB    int
	BackendPreferences []domain.LLMBackendKind
	RuntimeBackend     *domain.LLMRuntimeManagedBackendConfig
	ExternalBackend    *domain.LLMExternalBackendConfig
	PlacementPolicy    *domain.LLMPlacementPolicy
	PromotionGate      *domain.LLMPromotionGateConfig
	Metadata           map[string]any
}

// LLMDeployCommand describes a canonical LLM deploy request.
type LLMDeployCommand struct {
	RouteID        uuid.UUID
	EnvironmentID  uuid.UUID
	ReleaseID      uuid.UUID
	RequestedBy    string
	Metadata       map[string]any
	IdempotencyKey string
	AgentID        string
}

// LLMApprovalCommand describes a canonical LLM deployment approval/rejection request.
type LLMApprovalCommand struct {
	IntentID       uuid.UUID
	Decision       string
	IdempotencyKey string
	AgentID        string
}

// LLMRollbackCommand describes a canonical LLM rollback request.
type LLMRollbackCommand struct {
	RouteID        uuid.UUID
	EnvironmentID  uuid.UUID
	RequestedBy    string
	IdempotencyKey string
	AgentID        string
}

// LLMCommandReceipt is the correlation handle returned to synchronous callers.
type LLMCommandReceipt struct {
	RequestEventID  string `json:"request_event_id"`
	RequestPubkey   string `json:"request_pubkey"`
	RequestKind     int    `json:"request_kind"`
	StatusKind      int    `json:"status_kind,omitempty"`
	ResultKind      int    `json:"result_kind"`
	RegistryKind    int    `json:"registry_kind"`
	StateKind       int    `json:"state_kind"`
	DTag            string `json:"d_tag,omitempty"`
	IdempotencyKey  string `json:"idempotency_key"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	RetryHint       string `json:"retry_hint,omitempty"`
	PublishedRelays int    `json:"published_relays"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	RouteID         string `json:"route_id,omitempty"`
	EnvironmentID   string `json:"environment_id,omitempty"`
	ReleaseID       string `json:"release_id,omitempty"`
	IntentID        string `json:"intent_id,omitempty"`
	Decision        string `json:"decision,omitempty"`
}

// PublishLLMRouteCreateRequest publishes a ContextVM route-create request and returns correlation metadata.
func (p *LLMCommandPublisher) PublishLLMRouteCreateRequest(ctx context.Context, cmd LLMRouteCreateCommand) (*LLMCommandReceipt, error) {
	name := strings.TrimSpace(cmd.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	content := map[string]any{
		"name": name,
	}
	if cmd.Description != "" {
		content["description"] = cmd.Description
	}
	if cmd.GatewayConfig != nil {
		content["gateway_config"] = cmd.GatewayConfig
	}
	if cmd.DefaultPlacementPolicy != nil {
		content["default_placement_policy"] = cmd.DefaultPlacementPolicy
	}
	if cmd.DefaultPromotionGate != nil {
		content["default_promotion_gate"] = cmd.DefaultPromotionGate
	}
	if len(cmd.Metadata) > 0 {
		content["metadata"] = cmd.Metadata
	}
	tags := nostr.Tags{{"route", name}}
	appendLLMCommandTags(&tags, cmd.IdempotencyKey, cmd.AgentID)
	return p.publish(ctx, "llm/route-create", KindNIP38Status, KindContextVMMessage, tags, content)
}

// PublishLLMReleaseRegisterRequest publishes a ContextVM release-register request and returns correlation metadata.
func (p *LLMCommandPublisher) PublishLLMReleaseRegisterRequest(ctx context.Context, cmd LLMReleaseRegisterCommand) (*LLMCommandReceipt, error) {
	content := map[string]any{
		"route_id":     cmd.RouteID.String(),
		"version":      cmd.Version,
		"model_ref":    cmd.ModelRef,
		"model_source": cmd.ModelSource,
	}
	if cmd.ModelRevision != "" {
		content["model_revision"] = cmd.ModelRevision
	}
	if cmd.EstimatedVRAMGB > 0 {
		content["estimated_vram_gb"] = cmd.EstimatedVRAMGB
	}
	if len(cmd.BackendPreferences) > 0 {
		content["backend_preferences"] = cmd.BackendPreferences
	}
	if cmd.RuntimeBackend != nil {
		content["runtime_backend"] = cmd.RuntimeBackend
	}
	if cmd.ExternalBackend != nil {
		content["external_backend"] = cmd.ExternalBackend
	}
	if cmd.PlacementPolicy != nil {
		content["placement_policy"] = cmd.PlacementPolicy
	}
	if cmd.PromotionGate != nil {
		content["promotion_gate"] = cmd.PromotionGate
	}
	if len(cmd.Metadata) > 0 {
		content["metadata"] = cmd.Metadata
	}
	tags := nostr.Tags{{"route", cmd.RouteID.String()}}
	receipt, err := p.publish(ctx, "llm/release-register", KindNIP38Status, KindContextVMMessage, tags, content)
	if receipt != nil {
		receipt.RouteID = cmd.RouteID.String()
	}
	return receipt, err
}

// PublishLLMDeployRequest publishes a ContextVM deploy request and returns correlation metadata.
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
	appendLLMCommandTags(&tags, cmd.IdempotencyKey, cmd.AgentID)
	receipt, err := p.publish(ctx, "llm/deploy", KindNIP38Status, KindContextVMMessage, tags, content)
	if receipt != nil {
		receipt.RouteID = cmd.RouteID.String()
		receipt.EnvironmentID = cmd.EnvironmentID.String()
		receipt.ReleaseID = cmd.ReleaseID.String()
	}
	return receipt, err
}

// PublishLLMApprovalRequest publishes a ContextVM approval request and returns correlation metadata.
func (p *LLMCommandPublisher) PublishLLMApprovalRequest(ctx context.Context, cmd LLMApprovalCommand) (*LLMCommandReceipt, error) {
	content := map[string]any{
		"intent_id": cmd.IntentID.String(),
		"decision":  cmd.Decision,
	}
	tags := nostr.Tags{
		{"intent", cmd.IntentID.String()},
		{"decision", cmd.Decision},
	}
	appendLLMCommandTags(&tags, cmd.IdempotencyKey, cmd.AgentID)
	receipt, err := p.publish(ctx, "llm/approval", KindNIP38Status, KindContextVMMessage, tags, content)
	if receipt != nil {
		receipt.IntentID = cmd.IntentID.String()
		receipt.Decision = cmd.Decision
	}
	return receipt, err
}

// PublishLLMRollbackRequest publishes a ContextVM rollback request and returns correlation metadata.
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
	appendLLMCommandTags(&tags, cmd.IdempotencyKey, cmd.AgentID)
	receipt, err := p.publish(ctx, "llm/rollback", KindNIP38Status, KindContextVMMessage, tags, content)
	if receipt != nil {
		receipt.RouteID = cmd.RouteID.String()
		receipt.EnvironmentID = cmd.EnvironmentID.String()
	}
	return receipt, err
}

func appendLLMCommandTags(tags *nostr.Tags, dTag, agentID string) {
	dTag = strings.TrimSpace(dTag)
	if dTag == "" {
		dTag = "llm-command:" + uuid.NewString()
	}
	*tags = append(nostr.Tags{{"d", dTag}}, (*tags)...)
	if agentID != "" {
		*tags = append(*tags, nostr.Tag{"agent", agentID})
	}
}

func (p *LLMCommandPublisher) publish(ctx context.Context, method string, statusKind, resultKind int, tags nostr.Tags, content map[string]any) (*LLMCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("LLM command publisher is not configured")
	}
	dTag := strings.TrimSpace(tagValueNostr(tags, "d"))
	if dTag == "" {
		dTag = "llm-command:" + method + ":" + uuid.NewString()
	}
	ev, published, dTag, err := publishContextVMCommand(ctx, p.publisher, p.signer, method, dTag, "", tags, content, "LLM command")
	if err != nil {
		if ev != nil && published > 0 {
			return &LLMCommandReceipt{RequestEventID: ev.ID.Hex(), RequestPubkey: ev.PubKey.Hex(), RequestKind: KindContextVMMessage, StatusKind: statusKind, ResultKind: resultKind, RegistryKind: KindCASControlState, StateKind: KindCASControlState, DTag: dTag, IdempotencyKey: dTag, Status: "error", Error: err.Error(), PublishedRelays: published}, nil
		}
		return nil, err
	}
	return &LLMCommandReceipt{
		RequestEventID:  ev.ID.Hex(),
		RequestPubkey:   ev.PubKey.Hex(),
		RequestKind:     KindContextVMMessage,
		StatusKind:      statusKind,
		ResultKind:      resultKind,
		RegistryKind:    KindCASControlState,
		StateKind:       KindCASControlState,
		DTag:            dTag,
		IdempotencyKey:  dTag,
		Status:          "submitted",
		PublishedRelays: published,
	}, nil
}
