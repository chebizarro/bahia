package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// LLMResponder publishes threaded Nostr lifecycle replies for background LLM provisioning.
type LLMResponder struct {
	pool      *nostrpool.RelayPool
	signer    canonicalnostr.Signer
	logger    *zap.Logger
	eventRepo repository.NostrEventRepository
}

func NewLLMResponder(pool *nostrpool.RelayPool, signer canonicalnostr.Signer, logger *zap.Logger, eventRepos ...repository.NostrEventRepository) *LLMResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	var eventRepo repository.NostrEventRepository
	if len(eventRepos) > 0 {
		eventRepo = eventRepos[0]
	}
	return &LLMResponder{pool: pool, signer: signer, logger: logger.Named("llm-responder"), eventRepo: eventRepo}
}

func (r *LLMResponder) PublishStatus(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step, message string) error {
	return r.publish(ctx, true, intent, run, "processing", step, message, nil)
}

func (r *LLMResponder) PublishResult(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, status, message string) error {
	return r.publish(ctx, false, intent, run, status, "completed", message, nil)
}

func (r *LLMResponder) PublishError(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step string, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return r.publish(ctx, false, intent, run, "error", step, msg, cause)
}

func (r *LLMResponder) publish(ctx context.Context, progress bool, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, status, step, message string, cause error) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("LLM responder: %w", ErrResponderNotConfigured)
	}
	if intent == nil {
		return fmt.Errorf("LLM deployment intent: %w", ErrResponderInvalidInput)
	}
	requestEventID, requestPubkey := llmNostrCorrelation(intent)
	if requestEventID == "" || requestPubkey == "" {
		return fmt.Errorf("LLM deployment intent %s: %w", intent.ID, ErrResponderCorrelationMissing)
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
	kind := KindContextVMMessage
	bodyPayload := any(ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: llmContextVMReplyID(intent, requestEventID), Result: content})
	if progress {
		kind = KindNIP38Status
		bodyPayload = content
		tags = append(nostr.Tags{{"d", "llm-status:" + requestEventID + ":" + step}}, tags...)
	} else {
		tags = append(tags, nostr.Tag{ContextVMRoutingTag, ContextVMWireVersion})
		if cause != nil {
			bodyPayload = ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: llmContextVMReplyID(intent, requestEventID), Error: &JSONRPCError{Code: -32000, Message: cause.Error()}}
		}
	}
	body, _ := json.Marshal(bodyPayload)
	ev := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: string(body)}
	if err := r.sign(ctx, ev); err != nil {
		return err
	}
	published, err := r.pool.Publish(ctx, *ev)
	if err != nil {
		return err
	}
	if published == 0 {
		return fmt.Errorf("LLM response: %w", ErrResponderNoRelayAccepted)
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
	if _, err := r.eventRepo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Content: ev.Content, Tags: tagsJSON, Sig: nostr.HexEncodeToString(ev.Sig[:]), CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(), EntityType: "llm.provisioning.reply", EntityID: &entityID}); err != nil {
		r.logger.Warn("failed to record LLM provisioning reply", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
	}
}

func (r *LLMResponder) sign(ctx context.Context, ev *nostr.Event) error {
	return SignGoNostrEvent(ctx, r.signer, ev)
}

func llmContextVMReplyID(intent *domain.LLMDeploymentIntent, fallback string) json.RawMessage {
	if intent != nil && intent.Metadata != nil {
		if dTag, ok := intent.Metadata["nostr_d_tag"].(string); ok && dTag != "" {
			body, _ := json.Marshal(dTag)
			return body
		}
	}
	body, _ := json.Marshal(fallback)
	return body
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
