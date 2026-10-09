package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ErrToolCallUnauthorized means the caller's principal was refused before the
// tool did anything, so nothing was submitted.
var ErrToolCallUnauthorized = errors.New("MCP tool call not authorized")

func assistantAsyncToolDefinitions() []Tool {
	return []Tool{
		{Name: "bahia_assistant_service_deploy", Description: "Assistant-safe service deploy through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{"service_id": stringProp, "environment_id": stringProp, "artifact_id": stringProp, "idempotency_key": stringProp}, "service_id", "environment_id", "artifact_id", "idempotency_key")},
		{Name: "bahia_assistant_service_rollback", Description: "Assistant-safe service rollback through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{"service_id": stringProp, "environment_id": stringProp, "idempotency_key": stringProp}, "service_id", "environment_id", "idempotency_key")},
		{Name: "bahia_assistant_llm_deploy", Description: "Assistant-safe LLM deploy through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{"route_id": stringProp, "environment_id": stringProp, "release_id": stringProp, "requested_by": stringProp, "idempotency_key": stringProp}, "route_id", "environment_id", "release_id", "idempotency_key")},
		{Name: "bahia_assistant_llm_approve_deployment", Description: "Assistant-safe LLM approval through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{"intent_id": stringProp, "org_id": stringProp, "decision": stringProp, "idempotency_key": stringProp}, "intent_id", "org_id", "decision", "idempotency_key")},
		{Name: "bahia_assistant_llm_rollback", Description: "Assistant-safe LLM rollback through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{"route_id": stringProp, "environment_id": stringProp, "requested_by": stringProp, "idempotency_key": stringProp}, "route_id", "environment_id", "idempotency_key")},
		{Name: "bahia_assistant_ml_deploy", Description: "Assistant-safe ML deploy through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{"endpoint": stringProp, "endpoint_id": stringProp, "model_version": stringProp, "model_version_id": stringProp, "runtime_preference": stringProp, "runtime": stringProp, "idempotency_key": stringProp}, "idempotency_key")},
		{Name: "bahia_assistant_ml_approve_deployment", Description: "Assistant-safe ML approval through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{"intent_id": stringProp, "org_id": stringProp, "decision": stringProp, "expected_updated_at": stringProp, "idempotency_key": stringProp}, "intent_id", "decision", "idempotency_key")},
		{Name: "bahia_assistant_ml_rollback", Description: "Assistant-safe ML rollback through the in-process intent pipeline", InputSchema: objectSchema(map[string]interface{}{"endpoint": stringProp, "endpoint_id": stringProp, "requested_by": stringProp, "idempotency_key": stringProp}, "idempotency_key")},
	}
}

func (s *Server) handleAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	receipt, err := s.invokeAssistantAsyncTool(ctx, name, args) // CallTool already authorized
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return jsonResult(receipt)
}

// InvokeAssistantAsyncTool is the executor's direct entry for async assistant
// tools. It applies the same operator authorization as CallTool, so the
// assistant can do exactly what the principal in ctx could do directly. A
// refusal wraps ErrToolCallUnauthorized and happens before any publication.
func (s *Server) InvokeAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	if denied := s.authorizeToolCall(ctx, name); denied != nil {
		reason := "access denied"
		if len(denied.Content) > 0 {
			reason = denied.Content[0].Text
		}
		return nil, fmt.Errorf("%w: %s", ErrToolCallUnauthorized, reason)
	}
	return s.invokeAssistantAsyncTool(ctx, name, args)
}

func (s *Server) invokeAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	key := strings.TrimSpace(stringArg(args, "idempotency_key"))
	if key == "" {
		return nil, fmt.Errorf("idempotency_key is required")
	}
	switch name {
	case "bahia_assistant_service_deploy", "bahia_assistant_service_rollback",
		"bahia_assistant_llm_deploy", "bahia_assistant_llm_approve_deployment", "bahia_assistant_llm_rollback",
		"bahia_assistant_ml_deploy", "bahia_assistant_ml_approve_deployment", "bahia_assistant_ml_rollback":
		return s.invokeAssistantIntent(ctx, name, args, key)
	default:
		return nil, fmt.Errorf("assistant tool %q is not allowlisted", name)
	}
}

func (s *Server) invokeAssistantIntent(ctx context.Context, name string, args map[string]interface{}, key string) (*domain.AsyncToolReceipt, error) {
	result, err := s.invokeIntentWrite(ctx, name, args)
	if err != nil {
		return nil, err
	}
	if result == nil || len(result.Content) == 0 {
		return nil, fmt.Errorf("intent pipeline returned no result")
	}
	var outcome struct {
		Status   string `json:"status"`
		IntentID string `json:"intent_id"`
		EventID  string `json:"event_id"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &outcome); err != nil {
		return nil, fmt.Errorf("decode intent result: %w", err)
	}
	if result.IsError {
		if outcome.EventID == "" && outcome.Status == "rejected" {
			return nil, fmt.Errorf("%w: %s", ErrToolCallUnauthorized, outcome.Reason)
		}
		// An event-correlated failure may follow a handler side effect. It is
		// not a pre-submission refusal; the executor must treat it as uncertain.
		return nil, fmt.Errorf("intent result %s: %s", outcome.Status, outcome.Reason)
	}
	if outcome.IntentID == "" || outcome.EventID == "" {
		return nil, fmt.Errorf("intent result missing correlation")
	}
	return assistantIntentReceipt(name, key, outcome.IntentID, outcome.EventID), nil
}

func assistantIntentReceipt(name, key, intentID, eventID string) *domain.AsyncToolReceipt {
	return &domain.AsyncToolReceipt{
		ToolName: name, RequestEventID: eventID, RequestKind: 30900,
		StatusKinds: []int{30315}, ResultKinds: []int{30315},
		DTag: intentID, IdempotencyKey: key,
		ResourceTags: map[string]string{"intent_id": intentID},
	}
}

// ResolveAssistantIntentReceipt proves a previously processed assistant call
// from the durable processor marker. The work key includes the persisted
// arguments digest, actor and tool; no unsigned caller-provided event ID is
// accepted as evidence.
func (s *Server) ResolveAssistantIntentReceipt(name, actor, key, eventID string) (*domain.AsyncToolReceipt, error) {
	if s == nil || s.intentProc == nil || !isAssistantIntentTool(name) {
		return nil, fmt.Errorf("assistant intent processor is not configured")
	}
	if isLLMLifecycleMCPTool(name) {
		return nil, fmt.Errorf("LLM assistant receipt recovery is unavailable: a processed marker is not canonical provisioning evidence")
	}
	intentID, err := mcpIntentID(name, strings.ToLower(strings.TrimSpace(actor)), map[string]any{"idempotency_key": key})
	if err != nil {
		return nil, err
	}
	marker := s.intentProc.ProcessedIntent(intentID)
	if marker == nil || marker.Actor != strings.ToLower(strings.TrimSpace(actor)) || marker.EventID != eventID {
		return nil, fmt.Errorf("processed assistant intent evidence does not match work")
	}
	return assistantIntentReceipt(name, key, intentID, eventID), nil
}

func isAssistantIntentTool(name string) bool {
	switch name {
	case "bahia_assistant_service_deploy", "bahia_assistant_service_rollback",
		"bahia_assistant_llm_deploy", "bahia_assistant_llm_approve_deployment", "bahia_assistant_llm_rollback",
		"bahia_assistant_ml_deploy", "bahia_assistant_ml_approve_deployment", "bahia_assistant_ml_rollback":
		return true
	default:
		return false
	}
}
