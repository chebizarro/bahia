package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// RegisterAssistantContextVMHandlers registers the operator assistant mutation
// intents on the canonical ContextVM request transport. Durable assistant state
// is emitted by the orchestrator as 30900/30315 observables; these handlers only
// return the ContextVM JSON-RPC result payload for the initiating request.
func RegisterAssistantContextVMHandlers(transport *EncryptedRequestTransport, orchestrator *service.AssistantOrchestrator) {
	if transport == nil || orchestrator == nil {
		return
	}
	adapter := assistantContextVMAdapter{orchestrator: orchestrator}
	transport.RegisterContextVMHandler(domain.AssistantContextVMMethodPrompt, adapter.handlePrompt)
	transport.RegisterContextVMHandler(domain.AssistantContextVMMethodApproval, adapter.handleApproval)
}

type assistantContextVMAdapter struct {
	orchestrator *service.AssistantOrchestrator
}

func (a assistantContextVMAdapter) handlePrompt(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload domain.AssistantPromptRequest
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("invalid assistant prompt params: %w", err)
	}
	if strings.TrimSpace(payload.SessionID) == "" || strings.TrimSpace(payload.TurnID) == "" || strings.TrimSpace(payload.Prompt) == "" {
		return service.AssistantOperationResult{"status": "failed", "step": "validation_error", "session_id": payload.SessionID, "summary": "prompt request requires session_id, turn_id, and prompt", "error": "prompt request requires session_id, turn_id, and prompt"}, nil
	}
	if !a.orchestrator.IsSessionParticipant(payload.SessionID, request.Event.PubKey.Hex()) {
		return service.AssistantOperationResult{"status": "failed", "step": "unauthorized_participant", "session_id": payload.SessionID, "summary": "requester is not a participant in this assistant session", "error": "requester is not a participant in this assistant session"}, nil
	}
	return a.orchestrator.HandlePromptRequest(ctx, assistantSourceFromContextVM(request), payload)
}

func (a assistantContextVMAdapter) handleApproval(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload domain.AssistantApprovalRequest
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("invalid assistant approval params: %w", err)
	}
	payload.Decision = strings.ToLower(strings.TrimSpace(payload.Decision))
	if strings.TrimSpace(payload.SessionID) == "" || strings.TrimSpace(payload.PlanHash) == "" || payload.Decision == "" {
		return service.AssistantOperationResult{"status": "failed", "step": "validation_error", "session_id": payload.SessionID, "summary": "approval request requires session_id, plan_hash, and decision", "error": "approval request requires session_id, plan_hash, and decision"}, nil
	}
	if payload.Decision != "approve" && payload.Decision != "reject" && payload.Decision != "cancel" {
		return service.AssistantOperationResult{"status": "failed", "step": "validation_error", "session_id": payload.SessionID, "summary": "decision must be approve, reject, or cancel", "error": "decision must be approve, reject, or cancel"}, nil
	}
	if !a.orchestrator.IsSessionParticipant(payload.SessionID, request.Event.PubKey.Hex()) {
		return service.AssistantOperationResult{"status": "failed", "step": "unauthorized_participant", "session_id": payload.SessionID, "summary": "requester is not a participant in this assistant session", "error": "requester is not a participant in this assistant session"}, nil
	}
	return a.orchestrator.HandleApprovalRequest(ctx, assistantSourceFromContextVM(request), payload)
}

func assistantSourceFromContextVM(request ContextVMRequest) service.AssistantRequestSource {
	dedupKey := strings.TrimSpace(request.ProgressToken)
	if dedupKey == "" && request.Event != nil {
		dedupKey = request.Event.ID.Hex()
	}
	source := service.AssistantRequestSource{Event: request.Event, DedupKey: dedupKey}
	if request.Event != nil {
		source.OperatorPubkey = request.Event.PubKey.Hex()
		source.RequestID = request.Event.ID.Hex()
	}
	return source
}

func decodeContextVMParams(params json.RawMessage, out any) error {
	if len(params) == 0 || string(params) == "null" {
		return json.Unmarshal([]byte(`{}`), out)
	}
	return json.Unmarshal(params, out)
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
