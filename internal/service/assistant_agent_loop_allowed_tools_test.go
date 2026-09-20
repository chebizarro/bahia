package service

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

// A command's allowed-tools scope is an authorization boundary, not a hint to
// the model. These tests drive the loop the way a compromised or confused model
// would: by naming a tool that was never advertised to it.

func deniedObservationForCall(res *AssistantAgentLoopResult, callID string) *domain.AssistantToolObservation {
	if res == nil {
		return nil
	}
	for _, obs := range res.Observations {
		if obs != nil && obs.ToolCallID == callID && obs.Status == domain.AssistantToolObservationDenied {
			return obs
		}
	}
	return nil
}

// An empty but non-nil allow list means "no tools", not "all tools". The old
// len()==0 check conflated it with an absent scope.
func TestAssistantAgentLoopEmptyAllowedToolsPermitsNothing(t *testing.T) {
	lib := mustCommandLibrary(AssistantCommandSpec{
		Name:         "chat",
		Template:     "Answer without tools: $ARGUMENTS",
		AllowedTools: []string{},
	})
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-denied", Name: "bahia_list_services"}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("answered without tools"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"services":[]}`}}}}
	loop, _ := newAssistantExtLoop(t, model, server, assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services")), domain.AssistantPermissionModeReview, AssistantAgentLoopConfig{Commands: lib})
	session := assistantRuntimeSession("session-empty-allowlist")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-empty", Prompt: "/chat what is running"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if got := model.request(0); len(got.Tools) != 0 {
		t.Fatalf("empty allow list advertised %d tools, want 0", len(got.Tools))
	}
	if server.callCount() != 0 {
		t.Fatalf("tool executed under an empty allow list: server calls = %d", server.callCount())
	}
	if !requestHasToolObservation(model.request(1), "call-denied", domain.AssistantToolObservationDenied) {
		t.Fatalf("model was not told the call was denied: %#v", model.request(1).Messages)
	}
	_ = res
}

// The allow list must gate execution, not just advertisement: a model can name
// a tool that never appeared in its schema list.
func TestAssistantAgentLoopDeniesToolOutsideAllowedTools(t *testing.T) {
	lib := mustCommandLibrary(AssistantCommandSpec{
		Name:         "list",
		Template:     "List services: $ARGUMENTS",
		AllowedTools: []string{"bahia_list_services"},
	})
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-outside", Name: "bahia_assistant_policy_change", Arguments: map[string]any{"target": "prod"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("stopped"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"changed":true}`}}}}
	registry := assistantRuntimeRegistryWith(
		syncDescriptor("bahia_list_services"),
		AssistantToolRuntimeToolDescriptor{Name: "bahia_assistant_policy_change", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskHigh},
	)
	loop, _ := newAssistantExtLoop(t, model, server, registry, domain.AssistantPermissionModeReview, AssistantAgentLoopConfig{Commands: lib})
	session := assistantRuntimeSession("session-outside-allowlist")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-outside", Prompt: "/list all"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if res.Suspended {
		t.Fatalf("out-of-scope mutation reached the approval queue instead of being denied: %#v", res.DeferredAction)
	}
	if server.callCount() != 0 {
		t.Fatalf("out-of-scope tool executed: server calls = %d", server.callCount())
	}
	if !requestHasToolObservation(model.request(1), "call-outside", domain.AssistantToolObservationDenied) {
		t.Fatalf("out-of-scope call was not denied: %#v", model.request(1).Messages)
	}
}

// The scope must survive suspension. It lived only in the in-memory run, so an
// approval pause silently restored unrestricted access.
func TestAssistantAgentLoopAllowedToolsSurviveApprovalResume(t *testing.T) {
	lib := mustCommandLibrary(AssistantCommandSpec{
		Name:         "policy",
		Template:     "Change policy: $ARGUMENTS",
		AllowedTools: []string{"bahia_assistant_policy_change"},
	})
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-approved", Name: "bahia_assistant_policy_change", Arguments: map[string]any{"target": "prod"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-after-resume", Name: "bahia_list_services"}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("done"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"changed":true}`}}}}
	registry := assistantRuntimeRegistryWith(
		syncDescriptor("bahia_list_services"),
		AssistantToolRuntimeToolDescriptor{Name: "bahia_assistant_policy_change", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskHigh},
	)
	loop, _ := newAssistantExtLoop(t, model, server, registry, domain.AssistantPermissionModeReview, AssistantAgentLoopConfig{Commands: lib})
	session := assistantRuntimeSession("session-resume-allowlist")

	started, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-resume", Prompt: "/policy prod"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !started.Suspended || started.DeferredAction == nil {
		t.Fatalf("expected the high-risk call to await approval: %#v", started)
	}

	resumed, err := loop.ResumeAfterActionDecision(context.Background(), AssistantAgentActionDecisionRequest{Session: session, ActionID: started.DeferredAction.ActionID, Decision: "approve"})
	if err != nil {
		t.Fatalf("ResumeAfterActionDecision: %v", err)
	}
	if resumed == nil {
		t.Fatal("resume returned no result")
	}
	// bahia_list_services is registered and would run happily, but it is outside
	// the command scope the turn started with.
	if server.callCount() != 1 {
		t.Fatalf("server calls = %d, want 1 (only the approved in-scope tool)", server.callCount())
	}
	if !requestHasToolObservation(model.request(2), "call-after-resume", domain.AssistantToolObservationDenied) {
		t.Fatalf("allow list was lost across resume; post-resume call was not denied: %#v", model.request(2).Messages)
	}
}
