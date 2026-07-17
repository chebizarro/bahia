package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// ToolResponder handles publishing tool provisioning status/result events.
type ToolResponder struct {
	publisher NostrEventPublisher
	signer    canonicalnostr.Signer
	nostrRepo repository.NostrEventRepository
	logger    *zap.Logger
}

func NewToolResponder(publisher NostrEventPublisher, signer canonicalnostr.Signer, logger *zap.Logger, nostrRepo repository.NostrEventRepository) *ToolResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ToolResponder{publisher: publisher, signer: signer, nostrRepo: nostrRepo, logger: logger.Named("tool-responder")}
}

// PublishStatus publishes tool provisioning status as canonical CAS state.
func (r *ToolResponder) PublishStatus(ctx context.Context, requestEvent *nostr.Event, intent *domain.ToolProvisionIntent, step string, message string) error {
	return r.publishReply(ctx, requestEvent, intent, "processing", step, message, "")
}

// PublishResult publishes the final tool provisioning result as canonical CAS state.
func (r *ToolResponder) PublishResult(ctx context.Context, requestEvent *nostr.Event, intent *domain.ToolProvisionIntent, success bool, errMsg string) error {
	status := "success"
	step := "completed"
	message := "tool provisioning completed"
	if !success {
		status = "error"
		step = "failed"
		message = errMsg
	}
	return r.publishReply(ctx, requestEvent, intent, status, step, message, errMsg)
}

// PublishApprovalRequest publishes a ContextVM approval request to the operator.
func (r *ToolResponder) PublishApprovalRequest(ctx context.Context, intent *domain.ToolProvisionIntent, operatorPubkey string) error {
	if r == nil || r.publisher == nil {
		return fmt.Errorf("tool approval responder: %w", ErrResponderNotConfigured)
	}
	if intent == nil || operatorPubkey == "" {
		return fmt.Errorf("tool approval request: %w", ErrResponderInvalidInput)
	}
	content := map[string]any{
		"intent_id":       intent.ID.String(),
		"service_id":      intent.ServiceID.String(),
		"environment_id":  intent.EnvironmentID.String(),
		"requested_tools": intent.RequestedTools,
		"approval_flags":  intent.ApprovalFlags,
		"status":          intent.Status,
		"created_at":      time.Now().UTC().Format(time.RFC3339),
	}
	tags := nostr.Tags{
		{"p", operatorPubkey},
		{"status", string(intent.Status)},
		{"intent", intent.ID.String()},
		{"service", intent.ServiceID.String()},
		{"environment", intent.EnvironmentID.String()},
	}
	ev, _, _, err := publishContextVMCommand(ctx, r.publisher, r.signer, "tool/approval-request", "tool-approval:"+intent.ID.String(), "", tags, content, "tool approval")
	if err != nil {
		return err
	}
	r.record(ctx, ev, "tool.provisioning.approval.request", &intent.ID)
	return nil
}

func (r *ToolResponder) publishReply(ctx context.Context, requestEvent *nostr.Event, intent *domain.ToolProvisionIntent, status, step, message, errMsg string) error {
	if r == nil || r.publisher == nil {
		return fmt.Errorf("tool responder: %w", ErrResponderNotConfigured)
	}
	if requestEvent == nil || intent == nil {
		return fmt.Errorf("tool response: %w", ErrResponderInvalidInput)
	}
	if requestEvent.ID == (nostr.ID{}) || requestEvent.PubKey == (nostr.PubKey{}) {
		return fmt.Errorf("tool response: %w", ErrResponderCorrelationMissing)
	}
	content := map[string]any{
		"status":         status,
		"step":           step,
		"message":        message,
		"intent_id":      intent.ID.String(),
		"service_id":     intent.ServiceID.String(),
		"environment_id": intent.EnvironmentID.String(),
		"created_at":     time.Now().UTC().Format(time.RFC3339),
	}
	if errMsg != "" {
		content["error"] = errMsg
	}
	body, _ := json.Marshal(content)
	tags := nostr.Tags{
		{"e", requestEvent.ID.Hex(), "", "reply"},
		{"p", requestEvent.PubKey.Hex()},
		{"status", status},
		{"step", step},
		{"intent", intent.ID.String()},
		{"service", intent.ServiceID.String()},
		{"environment", intent.EnvironmentID.String()},
	}
	if errMsg != "" {
		tags = append(tags, nostr.Tag{"error", errMsg})
	}
	tags = append(nostr.Tags{{"d", "tool-provisioning:" + intent.ID.String()}, {"domain", "tool"}, {"entity", "provisioning"}, {"schema", "bahia.cp-state.v1"}}, tags...)
	ev := &nostr.Event{Kind: KindCASControlState, CreatedAt: nostr.Now(), Tags: tags, Content: string(body)}
	if err := SignGoNostrEvent(ctx, r.signer, ev); err != nil {
		return err
	}
	published, err := r.publisher.Publish(ctx, *ev)
	if err != nil {
		return err
	}
	if published == 0 {
		return fmt.Errorf("tool response: %w", ErrResponderNoRelayAccepted)
	}
	r.record(ctx, ev, "tool.provisioning.reply", &intent.ID)
	return nil
}

func (r *ToolResponder) record(ctx context.Context, ev *nostr.Event, entityType string, entityID *uuid.UUID) {
	if r.nostrRepo == nil || ev == nil {
		return
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	if _, err := r.nostrRepo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Content: ev.Content, Tags: tagsJSON, Sig: nostr.HexEncodeToString(ev.Sig[:]), CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(), EntityType: entityType, EntityID: entityID}); err != nil {
		r.logger.Warn("failed to record tool provisioning event", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
	}
}
