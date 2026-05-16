package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

const fallbackAssistantAgentID = "bahia-operator-assistant"

func (r *Reactor) handleAssistantPromptRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received assistant prompt request")

	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized assistant prompt request")
		_ = r.publishAssistantFailure(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	if r.assistantOrchestrator == nil {
		_ = r.publishAssistantFailure(ctx, event, "assistant_unavailable", "assistant orchestrator is not configured")
		return
	}
	var req domain.AssistantPromptRequest
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		_ = r.assistantOrchestrator.PublishFailure(ctx, event, assistantSessionFromEvent(event), "parse_error", fmt.Sprintf("invalid prompt request JSON: %v", err))
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.TurnID) == "" || strings.TrimSpace(req.Prompt) == "" || tagValueNostr(event.Tags, "d") == "" || tagValueNostr(event.Tags, "session") == "" {
		_ = r.assistantOrchestrator.PublishFailure(ctx, event, firstNonEmpty(req.SessionID, assistantSessionFromEvent(event)), "validation_error", "prompt request requires d and session tags plus session_id, turn_id, and prompt content fields")
		return
	}
	if servicePubkey := r.servicePubkey(); servicePubkey != "" && !assistantHasPTag(event.Tags, servicePubkey) {
		_ = r.assistantOrchestrator.PublishFailure(ctx, event, req.SessionID, "wrong_service", "prompt request is not addressed to this Bahia service pubkey")
		return
	}

	go func() {
		if err := r.assistantOrchestrator.HandlePrompt(ctx, event); err != nil {
			logger.Error("assistant prompt orchestration failed", "error", err)
		}
	}()
}

func (r *Reactor) handleAssistantApprovalRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received assistant approval request")

	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized assistant approval request")
		_ = r.publishAssistantFailure(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}
	if r.assistantOrchestrator == nil {
		_ = r.publishAssistantFailure(ctx, event, "assistant_unavailable", "assistant orchestrator is not configured")
		return
	}
	if tagValueNostr(event.Tags, "d") == "" || tagValueNostr(event.Tags, "session") == "" || tagValueNostr(event.Tags, "plan-hash") == "" || tagValueNostr(event.Tags, "decision") == "" {
		_ = r.assistantOrchestrator.PublishFailure(ctx, event, assistantSessionFromEvent(event), "validation_error", "approval request requires d, session, plan-hash, and decision tags")
		return
	}
	if strings.TrimSpace(event.Content) != "" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(event.Content), &raw); err != nil {
			_ = r.assistantOrchestrator.PublishFailure(ctx, event, assistantSessionFromEvent(event), "parse_error", fmt.Sprintf("invalid approval request JSON: %v", err))
			return
		}
	}

	go func() {
		if err := r.assistantOrchestrator.HandleApproval(ctx, event); err != nil {
			logger.Error("assistant approval orchestration failed", "error", err)
		}
	}()
}

func (r *Reactor) publishAssistantFailure(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	sessionID := assistantSessionFromEvent(requestEvent)
	content, _ := json.Marshal(map[string]any{"status": "failed", "step": step, "summary": message, "error": message})
	tags := nostr.Tags{
		{"session", sessionID},
		{"agent", fallbackAssistantAgentID},
		{"status", "failed"},
		{"step", step},
	}
	if requestEvent != nil {
		tags = append(tags, nostr.Tag{"e", requestEvent.ID, "", "reply"}, nostr.Tag{"p", requestEvent.PubKey})
	}
	event := &nostr.Event{Kind: domain.KindAssistantResult, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign assistant failure: %w", err)
	}
	_, err := r.publishEvent(ctx, event)
	return err
}

func (r *Reactor) servicePubkey() string {
	if r == nil || strings.TrimSpace(r.config.PrivateKey) == "" {
		return ""
	}
	pubkey, err := nostr.GetPublicKey(r.config.PrivateKey)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(pubkey))
}

func assistantHasPTag(tags nostr.Tags, pubkey string) bool {
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	if pubkey == "" {
		return true
	}
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "p" && strings.ToLower(strings.TrimSpace(tag[1])) == pubkey {
			return true
		}
	}
	return false
}

func assistantSessionFromEvent(event *nostr.Event) string {
	if event == nil {
		return ""
	}
	if value := tagValueNostr(event.Tags, "session"); value != "" {
		return value
	}
	var raw struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal([]byte(event.Content), &raw)
	return strings.TrimSpace(raw.SessionID)
}
