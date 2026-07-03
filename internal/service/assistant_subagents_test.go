package service

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestParseAssistantSubagentValidFrontmatter(t *testing.T) {
	content := "---\nname: researcher\ndescription: Investigate service state\nmodel: gpt-4o\ntools: bahia_list_services, bahia_get_service\n---\nYou are a careful researcher. Report findings only.\n"
	spec, err := ParseAssistantSubagent(content, "researcher.md")
	if err != nil {
		t.Fatalf("ParseAssistantSubagent: %v", err)
	}
	if spec.Name != "researcher" || spec.Description != "Investigate service state" || spec.Model != "gpt-4o" {
		t.Fatalf("spec = %#v", spec)
	}
	if strings.Join(spec.Tools, ",") != "bahia_list_services,bahia_get_service" {
		t.Fatalf("tools = %#v", spec.Tools)
	}
	if !strings.Contains(spec.SystemPrompt, "careful researcher") {
		t.Fatalf("system prompt = %q", spec.SystemPrompt)
	}
}

func TestParseAssistantSubagentInvalidFrontmatter(t *testing.T) {
	cases := map[string]string{
		"missing frontmatter": "You are a subagent with no frontmatter.\n",
		"missing name":        "---\ndescription: no name here\n---\nbody\n",
		"missing description": "---\nname: nameonly\n---\nbody\n",
		"empty body":          "---\nname: x\ndescription: y\n---\n\n",
		"malformed yaml":      "---\nname: [unclosed\n---\nbody\n",
	}
	for label, content := range cases {
		if _, err := ParseAssistantSubagent(content, label+".md"); err == nil {
			t.Fatalf("%s: expected error, got nil", label)
		}
	}
}

func TestAssistantAgentLoopDelegatesSubagentReturningSyncObservation(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"services":[{"name":"api"}],"total":1}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-delegate", Name: assistantDelegateSubagentToolName, Arguments: map[string]any{"subagent": "researcher", "task": "list services"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "child-read", Name: "bahia_list_services"}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("found the api service"), StopReason: llm.AgentStopReasonEndTurn},
		{Content: textBlocks("delegation complete"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	subagents := mustSubagentLibrary(t, AssistantSubagentSpec{Name: "researcher", Description: "Investigate", Tools: []string{"bahia_list_services"}, SystemPrompt: "Research carefully."})
	loop, _ := newAssistantExtLoop(t, model, server, assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services")), domain.AssistantPermissionModeReview, AssistantAgentLoopConfig{Subagents: subagents})
	session := assistantRuntimeSession("session-delegate")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-delegate", Prompt: "delegate to researcher"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Completed {
		t.Fatalf("result = %#v", res)
	}
	if server.callCount() != 1 {
		t.Fatalf("child read should have executed once, got %d", server.callCount())
	}
	if model.callCount() != 4 {
		t.Fatalf("model calls = %d, want 4", model.callCount())
	}
	if !requestHasToolObservation(model.request(3), "call-delegate", domain.AssistantToolObservationSucceeded) {
		t.Fatalf("parent did not receive a successful delegation observation: %#v", model.request(3).Messages)
	}
}

func TestAssistantAgentLoopSubagentToolRestrictionIntersection(t *testing.T) {
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"ok":true}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-delegate", Name: assistantDelegateSubagentToolName, Arguments: map[string]any{"subagent": "reader", "task": "peek"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "child-forbidden", Name: "bahia_forbidden_tool"}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("I was not allowed to use that tool"), StopReason: llm.AgentStopReasonEndTurn},
		{Content: textBlocks("done"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	subagents := mustSubagentLibrary(t, AssistantSubagentSpec{Name: "reader", Description: "Reader", Tools: []string{"bahia_list_services"}, SystemPrompt: "Only read services."})
	loop, _ := newAssistantExtLoop(t, model, server, assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services"), syncDescriptor("bahia_forbidden_tool")), domain.AssistantPermissionModeReview, AssistantAgentLoopConfig{Subagents: subagents})
	session := assistantRuntimeSession("session-restrict")

	res, err := loop.StartTurn(context.Background(), AssistantAgentTurnRequest{Session: session, TurnID: "turn-restrict", Prompt: "delegate to reader"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if !res.Completed {
		t.Fatalf("result = %#v", res)
	}
	if server.callCount() != 0 {
		t.Fatalf("forbidden tool must not execute, server calls = %d", server.callCount())
	}
	if !requestHasToolObservation(model.request(3), "call-delegate", domain.AssistantToolObservationSucceeded) {
		t.Fatalf("delegation should still return a sync observation: %#v", model.request(3).Messages)
	}
}

func mustSubagentLibrary(t *testing.T, specs ...AssistantSubagentSpec) *AssistantSubagentLibrary {
	t.Helper()
	lib := &AssistantSubagentLibrary{byName: map[string]AssistantSubagentSpec{}}
	for _, spec := range specs {
		lib.byName[spec.Name] = spec
		lib.order = append(lib.order, spec.Name)
	}
	return lib
}

// newAssistantExtLoop builds an agent loop wired with the item-10 extensibility
// surface fields carried on cfg (Subagents/Skills/Commands/Hooks/Agentic) while
// reusing the shared runtime/model/transcript test harness.
func newAssistantExtLoop(t *testing.T, model *assistantLoopModel, server *assistantRuntimeMCPServer, registry assistantRuntimeRegistry, mode domain.AssistantPermissionMode, cfg AssistantAgentLoopConfig) (*AssistantAgentLoop, *assistantLoopTranscript) {
	t.Helper()
	transcript := &assistantLoopTranscript{history: map[string][]domain.AssistantAgentMessage{}}
	persister := &assistantLoopPersister{}
	runtime := newAssistantRuntimeForTest(t, server, registry, mode, nil, nil)
	runtime.sessions = persister
	ids := 0
	loopCfg := AssistantAgentLoopConfig{
		ModelClient:    model,
		ToolRuntime:    runtime,
		ContextBuilder: transcript,
		ToolSchemas:    assistantLoopSchemas{schemas: schemasFromRuntimeRegistry(registry)},
		Transcript:     transcript,
		Sessions:       persister,
		Agentic:        cfg.Agentic,
		Subagents:      cfg.Subagents,
		Skills:         cfg.Skills,
		Commands:       cfg.Commands,
		Hooks:          cfg.Hooks,
		Now:            func() time.Time { return time.Unix(2000, 0).UTC() },
		NewID:          func(prefix string) string { ids++; return prefix + "-ext-" + strconv.Itoa(ids) },
	}
	return NewAssistantAgentLoop(loopCfg), transcript
}

var _ = config.AssistantAgenticConfig{}
