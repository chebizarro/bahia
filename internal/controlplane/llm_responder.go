package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// LLMResponder publishes threaded Nostr lifecycle replies for background LLM provisioning.
type LLMResponder struct {
	pool       *nostrpool.RelayPool
	privateKey string
	signer     EventSigner
	logger     *zap.Logger
	eventRepo  repository.NostrEventRepository
}

func NewLLMResponder(pool *nostrpool.RelayPool, privateKey string, signer EventSigner, logger *zap.Logger, eventRepos ...repository.NostrEventRepository) *LLMResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	var eventRepo repository.NostrEventRepository
	if len(eventRepos) > 0 {
		eventRepo = eventRepos[0]
	}
	return &LLMResponder{pool: pool, privateKey: privateKey, signer: signer, logger: logger.Named("llm-responder"), eventRepo: eventRepo}
}

func (r *LLMResponder) PublishStatus(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step, message string) error {
	return r.publish(ctx, KindLLMDeploymentStatus, intent, run, "processing", step, message, nil)
}

func (r *LLMResponder) PublishResult(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, status, message string) error {
	return r.publish(ctx, KindLLMDeploymentResult, intent, run, status, "completed", message, nil)
}

func (r *LLMResponder) PublishError(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step string, cause error) error {
	return r.publish(ctx, KindLLMDeploymentResult, intent, run, "error", step, cause.Error(), cause)
}

func (r *LLMResponder) publish(ctx context.Context, kind int, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, status, step, message string, cause error) error {
	if r == nil || r.pool == nil || intent == nil {
		return nil
	}
	requestEventID, requestPubkey := llmNostrCorrelation(intent)
	if requestEventID == "" || requestPubkey == "" {
		return nil
	}
	content := map[string]any{
		"status":         status,
		"step":           step,
		"message":        message,
		"intent_id":      intent.ID.String(),
		"route_id":       intent.RouteID.String(),
		"environment_id": intent.EnvironmentID.String(),
		"release_id":     intent.ReleaseID.String(),
		"created_at":     time.Now().UTC().Format(time.RFC3339),
	}
	if run != nil {
		content["run_id"] = run.ID.String()
		content["backend_kind"] = string(run.BackendKind)
		content["backend_endpoint"] = run.BackendEndpoint
	}
	if cause != nil {
		content["error"] = cause.Error()
	}
	body, _ := json.Marshal(content)
	tags := nostr.Tags{
		{"e", requestEventID, "", "reply"},
		{"p", requestPubkey},
		{"status", status},
		{"step", step},
		{"route", intent.RouteID.String()},
		{"environment", intent.EnvironmentID.String()},
		{"release", intent.ReleaseID.String()},
		{"intent", intent.ID.String()},
	}
	if run != nil {
		tags = append(tags, nostr.Tag{"run", run.ID.String()})
	}
	if cause != nil {
		tags = append(tags, nostr.Tag{"error", cause.Error()})
	}
	ev := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: string(body)}
	if err := r.sign(ctx, ev); err != nil {
		return err
	}
	_, err := r.pool.Publish(ctx, *ev)
	if err != nil {
		return err
	}
	r.record(ctx, ev, intent)
	return nil
}

func (r *LLMResponder) record(ctx context.Context, ev *nostr.Event, intent *domain.LLMDeploymentIntent) {
	if r.eventRepo == nil || ev == nil || intent == nil {
		return
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	entityID := intent.RouteID
	if entityID == uuid.Nil {
		entityID = intent.ID
	}
	if err := r.eventRepo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID, Kind: ev.Kind, PubKey: ev.PubKey, Content: ev.Content, Tags: tagsJSON, Sig: ev.Sig, CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(), EntityType: "llm.provisioning.reply", EntityID: &entityID}); err != nil {
		r.logger.Warn("failed to record LLM provisioning reply", zap.String("event_id", ev.ID), zap.Error(err))
	}
}

func (r *LLMResponder) sign(ctx context.Context, ev *nostr.Event) error {
	if r.signer != nil {
		return r.signer.Sign(ctx, ev)
	}
	if r.privateKey == "" {
		return fmt.Errorf("no private key or signer configured")
	}
	return ev.Sign(r.privateKey)
}

func llmNostrCorrelation(intent *domain.LLMDeploymentIntent) (eventID, pubkey string) {
	if intent == nil || intent.Metadata == nil {
		return "", ""
	}
	if v, ok := intent.Metadata["nostr_event_id"].(string); ok {
		eventID = v
	}
	if v, ok := intent.Metadata["nostr_request_pubkey"].(string); ok {
		pubkey = v
	}
	return eventID, pubkey
}
