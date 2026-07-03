package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestParseAssistantHookDocumentValid(t *testing.T) {
	doc := []byte(`{
		"PreToolUse": [
			{"matcher": "bahia_.*", "hooks": [{"type": "prompt", "prompt": "gate mutations"}]}
		],
		"Stop": [
			{"matcher": "*", "hooks": [{"type": "mcp-tool", "tool": "bahia_list_services"}]}
		]
	}`)
	set, err := ParseAssistantHookDocument(doc, "hooks.json")
	if err != nil {
		t.Fatalf("ParseAssistantHookDocument: %v", err)
	}
	if len(set.byEvent[AssistantHookEventPreToolUse]) != 1 || len(set.byEvent[AssistantHookEventStop]) != 1 {
		t.Fatalf("hook set = %#v", set)
	}
}

func TestParseAssistantHookDocumentRejectsUnsupported(t *testing.T) {
	cases := map[string][]byte{
		"shell handler":  []byte(`{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"rm -rf /"}]}]}`),
		"unknown event":  []byte(`{"NotAnEvent":[{"matcher":"*","hooks":[{"type":"prompt","prompt":"x"}]}]}`),
		"empty prompt":   []byte(`{"Stop":[{"matcher":"*","hooks":[{"type":"prompt","prompt":"  "}]}]}`),
		"empty mcp tool": []byte(`{"Stop":[{"matcher":"*","hooks":[{"type":"mcp-tool","tool":""}]}]}`),
		"malformed json": []byte(`{"Stop": [ `),
	}
	for label, doc := range cases {
		if _, err := ParseAssistantHookDocument(doc, label); err == nil {
			t.Fatalf("%s: expected error, got nil", label)
		}
	}
}

func TestAssistantAgentLoopPreToolUseHookDeniesTool(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"total":1}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-read", Name: "bahia_list_services"}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("I could not read services"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	hooks := hookRunnerWith(map[AssistantHookEvent][]AssistantHookMatcher{
		AssistantHookEventPreToolUse: {{Matcher: "*", Handlers: []AssistantHookHandler{{Type: AssistantHookHandlerPrompt, Prompt: "gate"}}}},
	}, &scriptedHookEvaluator{outcomes: map[string][]AssistantHookOutcome{"gate": {{Decision: AssistantHookDecisionDeny, Reason: "reads are disabled by policy"}}}})
	loop, _ := newAssistantExtLoop(t, model, server, assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services")), domain.AssistantPermissionModeReview, AssistantAgentLoopConfig{Hooks: hooks})
	session := assistantRuntimeSession("session-hook-deny")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-hook-deny", Prompt: "read services"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Completed {
		t.Fatalf("result = %#v", res)
	}
	if server.callCount() != 0 {
		t.Fatalf("hook deny should block execution, server calls = %d", server.callCount())
	}
	if !requestHasToolObservation(model.request(1), "call-read", domain.AssistantToolObservationDenied) {
		t.Fatalf("model did not receive a denied observation: %#v", model.request(1).Messages)
	}
}

func TestAssistantAgentLoopPreToolUseHookAsksForApproval(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"total":1}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-read", Name: "bahia_list_services"}}, StopReason: llm.AgentStopReasonToolCalls},
	}}
	hooks := hookRunnerWith(map[AssistantHookEvent][]AssistantHookMatcher{
		AssistantHookEventPreToolUse: {{Matcher: "*", Handlers: []AssistantHookHandler{{Type: AssistantHookHandlerPrompt, Prompt: "gate"}}}},
	}, &scriptedHookEvaluator{outcomes: map[string][]AssistantHookOutcome{"gate": {{Decision: AssistantHookDecisionAsk, Reason: "confirm read"}}}})
	loop, _ := newAssistantExtLoop(t, model, server, assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services")), domain.AssistantPermissionModeAudited, AssistantAgentLoopConfig{Hooks: hooks})
	session := assistantRuntimeSession("session-hook-ask")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-hook-ask", Prompt: "read services"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Suspended || res.DeferredAction == nil || res.State != domain.AssistantAgentLoopStateAwaitingApproval {
		t.Fatalf("hook ask should defer for approval: %#v", res)
	}
	if server.callCount() != 0 {
		t.Fatalf("hook ask should not execute, server calls = %d", server.callCount())
	}
}

func TestAssistantAgentLoopStopHookBlocksThenCompletes(t *testing.T) {
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{Content: textBlocks("first attempt"), StopReason: llm.AgentStopReasonEndTurn},
		{Content: textBlocks("second attempt after being told to continue"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	hooks := hookRunnerWith(map[AssistantHookEvent][]AssistantHookMatcher{
		AssistantHookEventStop: {{Matcher: "*", Handlers: []AssistantHookHandler{{Type: AssistantHookHandlerPrompt, Prompt: "check"}}}},
	}, &scriptedHookEvaluator{outcomes: map[string][]AssistantHookOutcome{"check": {{Decision: AssistantHookDecisionBlock, Reason: "keep going"}, {Decision: AssistantHookDecisionAllow}}}})
	loop, persister := newAssistantExtLoopWithPersister(t, model, &assistantRuntimeMCPServer{}, assistantRuntimeRegistryWith(), domain.AssistantPermissionModeReview, AssistantAgentLoopConfig{Hooks: hooks})
	session := assistantRuntimeSession("session-stop-block")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-stop-block", Prompt: "do the task"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Completed {
		t.Fatalf("loop should complete after the stop block clears: %#v", res)
	}
	if model.callCount() != 2 {
		t.Fatalf("stop block should force a second model turn, model calls = %d", model.callCount())
	}
	if !persister.hasStatusPhase("stop_hook_blocked") {
		t.Fatalf("expected a stop_hook_blocked status, got %#v", persister.statuses)
	}
}

func TestAssistantAgentLoopPreToolUseHookCannotRewriteArgsToEscapeAsk(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"ok":true}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-danger", Name: "bahia_danger", Arguments: map[string]any{"target": "delete prod"}}}, StopReason: llm.AgentStopReasonToolCalls},
	}}
	// audited mode: destructive args ("delete") upgrade risk to ask. A hook that
	// rewrites the args to a benign value must NOT loosen the decision to allow.
	hooks := hookRunnerWith(map[AssistantHookEvent][]AssistantHookMatcher{
		AssistantHookEventPreToolUse: {{Matcher: "*", Handlers: []AssistantHookHandler{{Type: AssistantHookHandlerPrompt, Prompt: "sanitize"}}}},
	}, &scriptedHookEvaluator{outcomes: map[string][]AssistantHookOutcome{"sanitize": {{UpdatedInput: map[string]any{"target": "read"}}}}})
	descriptor := AssistantToolRuntimeToolDescriptor{Name: "bahia_danger", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow}
	loop, _ := newAssistantExtLoop(t, model, server, assistantRuntimeRegistryWith(descriptor), domain.AssistantPermissionModeAudited, AssistantAgentLoopConfig{Hooks: hooks})
	session := assistantRuntimeSession("session-hook-escape")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-hook-escape", Prompt: "do danger"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Suspended || res.DeferredAction == nil || res.State != domain.AssistantAgentLoopStateAwaitingApproval {
		t.Fatalf("hook arg rewrite must not escape the ask decision: %#v", res)
	}
	if server.callCount() != 0 {
		t.Fatalf("tool must not execute, server calls = %d", server.callCount())
	}
}

func TestAssistantHookModelPromptEvaluatorCallsModelWithPayload(t *testing.T) {
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{{Content: textBlocks(`{"permissionDecision":"ask","reason":"production change","updatedInput":{"environment":"prod"}}`), StopReason: llm.AgentStopReasonEndTurn}}}
	evaluator := NewAssistantHookModelPromptEvaluator(AssistantHookModelPromptEvaluatorConfig{ModelClient: model, Model: "hook-model", MaxTokens: 123})

	outcome, err := evaluator.EvaluateHookPrompt(context.Background(), AssistantHookPromptRequest{Event: AssistantHookEventPreToolUse, Prompt: "Gate production", Input: AssistantHookInput{SessionID: "session-1", ToolName: "bahia_deploy", ToolArgs: map[string]any{"environment": "prod"}}})
	if err != nil {
		t.Fatalf("EvaluateHookPrompt: %v", err)
	}
	if outcome.Decision != AssistantHookDecisionAsk || outcome.Reason != "production change" || outcome.UpdatedInput["environment"] != "prod" {
		t.Fatalf("outcome = %#v", outcome)
	}
	req := model.request(0)
	if req.Model != "hook-model" || req.MaxTokens != 123 || req.ToolChoice.Mode != llm.AgentToolChoiceNone {
		t.Fatalf("model request = %#v", req)
	}
	if len(req.Messages) != 2 || !strings.Contains(req.Messages[1].Content[0].Text, "Gate production") || !strings.Contains(req.Messages[1].Content[0].Text, "bahia_deploy") {
		t.Fatalf("model messages = %#v", req.Messages)
	}
}

func TestAssistantReadOnlyMCPHookCallerAllowsOnlyReadOnlySyncTools(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"permissionDecision":"deny","reason":"maintenance window closed"}`}}}}
	caller := NewAssistantReadOnlyMCPHookCaller(AssistantReadOnlyMCPHookCallerConfig{MCPServer: server, Registry: assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services"))})

	out, err := caller.CallReadOnlyTool(context.Background(), "bahia_list_services", map[string]any{"limit": float64(1)})
	if err != nil {
		t.Fatalf("CallReadOnlyTool: %v", err)
	}
	if out["permissionDecision"] != "deny" || out["reason"] != "maintenance window closed" {
		t.Fatalf("outcome map = %#v", out)
	}
	if server.callCount() != 1 {
		t.Fatalf("server calls = %d", server.callCount())
	}

	mutation := AssistantToolRuntimeToolDescriptor{Name: "bahia_mutate", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow}
	mutationCaller := NewAssistantReadOnlyMCPHookCaller(AssistantReadOnlyMCPHookCallerConfig{MCPServer: server, Registry: assistantRuntimeRegistryWith(mutation)})
	if _, err := mutationCaller.CallReadOnlyTool(context.Background(), "bahia_mutate", nil); err == nil || !strings.Contains(err.Error(), "not read-only") {
		t.Fatalf("mutation tool error = %v, want read-only rejection", err)
	}
}

func hookRunnerWith(byEvent map[AssistantHookEvent][]AssistantHookMatcher, evaluator AssistantHookPromptEvaluator) *AssistantHookRunner {
	return NewAssistantHookRunner(AssistantHookRunnerConfig{Set: AssistantHookSet{byEvent: byEvent}, Prompt: evaluator})
}

func newAssistantExtLoopWithPersister(t *testing.T, model *assistantLoopModel, server *assistantRuntimeMCPServer, registry assistantRuntimeRegistry, mode domain.AssistantPermissionMode, cfg AssistantAgentLoopConfig) (*AssistantAgentLoop, *assistantLoopPersister) {
	t.Helper()
	loop, _ := newAssistantExtLoop(t, model, server, registry, mode, cfg)
	return loop, loop.sessions.(*assistantLoopPersister)
}

type scriptedHookEvaluator struct {
	outcomes map[string][]AssistantHookOutcome
}

func (s *scriptedHookEvaluator) EvaluateHookPrompt(_ context.Context, req AssistantHookPromptRequest) (AssistantHookOutcome, error) {
	list := s.outcomes[req.Prompt]
	if len(list) == 0 {
		return AssistantHookOutcome{}, nil
	}
	out := list[0]
	if len(list) > 1 {
		s.outcomes[req.Prompt] = list[1:]
	}
	return out, nil
}

func (p *assistantLoopPersister) hasStatusPhase(phase string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, status := range p.statuses {
		if status["phase"] == phase {
			return true
		}
	}
	return false
}
