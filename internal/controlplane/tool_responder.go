package controlplane

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// ToolResponder handles publishing tool provisioning status/result events.
type ToolResponder struct {
	publisher *nostradapter.Publisher
	nostrRepo repository.NostrEventRepository
	logger    *zap.Logger
}

func NewToolResponder(publisher *nostradapter.Publisher, logger *zap.Logger, nostrRepo repository.NostrEventRepository) *ToolResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ToolResponder{publisher: publisher, nostrRepo: nostrRepo, logger: logger.Named("tool-responder")}
}

// PublishStatus publishes a kind 6976 status update.
func (r *ToolResponder) PublishStatus(ctx context.Context, requestEvent *nostr.Event, intent *domain.ToolProvisionIntent, step string, message string) error {
	return r.publishReply(ctx, KindToolProvisionStatus, requestEvent, intent, "processing", step, message, "")
}

// PublishResult publishes a kind 7976 final result.
func (r *ToolResponder) PublishResult(ctx context.Context, requestEvent *nostr.Event, intent *domain.ToolProvisionIntent, success bool, errMsg string) error {
	status := "success"
	step := "completed"
	message := "tool provisioning completed"
	if !success {
		status = "error"
		step = "failed"
		message = errMsg
	}
	return r.publishReply(ctx, KindToolProvisionResult, requestEvent, intent, status, step, message, errMsg)
}

// PublishApprovalRequest publishes a kind 5977 to operator.
func (r *ToolResponder) PublishApprovalRequest(ctx context.Context, intent *domain.ToolProvisionIntent, operatorPubkey string) error {
	if r == nil || r.publisher == nil || intent == nil || operatorPubkey == "" {
		return nil
	}
	content := map[string]any{
		"intent_id":      intent.ID.String(),
		"service_id":     intent.ServiceID.String(),
		"environment_id": intent.EnvironmentID.String(),
		"requested_tools": intent.RequestedTools,
		"approval_flags":  intent.ApprovalFlags,
		"status":          intent.Status,
		"created_at":      time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(content)
	ev := &nostr.Event{Kind: KindToolApprovalRequest, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"p", operatorPubkey},
		{"status", string(intent.Status)},
		{"intent", intent.ID.String()},
		{"service", intent.ServiceID.String()},
		{"environment", intent.EnvironmentID.String()},
	}, Content: string(body)}
	if err := r.publisher.PublishSignedEvent(ctx, ev); err != nil {
		return err
	}
	r.record(ctx, ev, "tool.provisioning.approval.request", &intent.ID)
	return nil
}

func (r *ToolResponder) publishReply(ctx context.Context, kind int, requestEvent *nostr.Event, intent *domain.ToolProvisionIntent, status, step, message, errMsg string) error {
	if r == nil || r.publisher == nil || requestEvent == nil || intent == nil {
		return nil
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
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", status},
		{"step", step},
		{"intent", intent.ID.String()},
		{"service", intent.ServiceID.String()},
		{"environment", intent.EnvironmentID.String()},
	}
	if errMsg != "" {
		tags = append(tags, nostr.Tag{"error", errMsg})
	}
	ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: string(body)}
	if err := r.publisher.PublishSignedEvent(ctx, ev); err != nil {
		return err
	}
	r.record(ctx, ev, "tool.provisioning.reply", &intent.ID)
	return nil
}

func (r *ToolResponder) record(ctx context.Context, ev *nostr.Event, entityType string, entityID *uuid.UUID) {
	if r.nostrRepo == nil || ev == nil {
		return
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	if _, err := r.nostrRepo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID, Kind: ev.Kind, PubKey: ev.PubKey, Content: ev.Content, Tags: tagsJSON, Sig: ev.Sig, CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(), EntityType: entityType, EntityID: entityID}); err != nil {
		r.logger.Warn("failed to record tool provisioning event", zap.String("event_id", ev.ID), zap.Error(err))
	}
}
