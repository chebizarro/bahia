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

// PublishLLMRouteCreateRequest publishes kind:5971 and returns correlation metadata.
func (p *LLMCommandPublisher) PublishLLMRouteCreateRequest(ctx context.Context, cmd LLMRouteCreateCommand) (*LLMCommandReceipt, error) {
	content := map[string]any{
		"name": cmd.Name,
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
	return p.publish(ctx, KindLLMRouteCreate, 0, KindLLMRouteCreateResult, nil, content)
}

// PublishLLMReleaseRegisterRequest publishes kind:5972 and returns correlation metadata.
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
	receipt, err := p.publish(ctx, KindLLMReleaseRegister, 0, KindLLMReleaseRegisterResult, tags, content)
	if receipt != nil {
		receipt.RouteID = cmd.RouteID.String()
	}
	return receipt, err
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
	appendLLMCommandTags(&tags, cmd.IdempotencyKey, cmd.AgentID)
	receipt, err := p.publish(ctx, KindLLMDeployRequest, KindLLMDeploymentStatus, KindLLMDeploymentResult, tags, content)
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
	appendLLMCommandTags(&tags, cmd.IdempotencyKey, cmd.AgentID)
	receipt, err := p.publish(ctx, KindLLMDeploymentApproval, KindLLMDeploymentStatus, KindLLMDeploymentResult, tags, content)
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
	appendLLMCommandTags(&tags, cmd.IdempotencyKey, cmd.AgentID)
	receipt, err := p.publish(ctx, KindLLMRollbackRequest, KindLLMDeploymentStatus, KindLLMDeploymentResult, tags, content)
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

func (p *LLMCommandPublisher) publish(ctx context.Context, kind, statusKind, resultKind int, tags nostr.Tags, content map[string]any) (*LLMCommandReceipt, error) {
	if p == nil || p.publisher == nil {
		return nil, fmt.Errorf("LLM command publisher is not configured")
	}
	dTag := strings.TrimSpace(tagValueNostr(tags, "d"))
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal LLM command content: %w", err)
	}
	ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: string(contentJSON)}
	if err := SignGoNostrEvent(ctx, p.signer, ev); err != nil {
		return nil, fmt.Errorf("sign LLM command event: %w", err)
	}
	published, err := p.publisher.Publish(ctx, *ev)
	if err != nil {
		if published > 0 {
			return &LLMCommandReceipt{RequestEventID: ev.ID, RequestPubkey: ev.PubKey, RequestKind: kind, StatusKind: statusKind, ResultKind: resultKind, RegistryKind: KindLLMRouteRegistry, StateKind: KindLLMRouteState, DTag: dTag, IdempotencyKey: dTag, Status: "error", Error: err.Error(), PublishedRelays: published}, nil
		}
		return nil, fmt.Errorf("publish LLM command event: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("publish LLM command event: no relay accepted the request; retry after relay reconnect")
	}
	return &LLMCommandReceipt{
		RequestEventID:  ev.ID,
		RequestPubkey:   ev.PubKey,
		RequestKind:     kind,
		StatusKind:      statusKind,
		ResultKind:      resultKind,
		RegistryKind:    KindLLMRouteRegistry,
		StateKind:       KindLLMRouteState,
		DTag:            dTag,
		IdempotencyKey:  dTag,
		Status:          "submitted",
		PublishedRelays: published,
	}, nil
}
