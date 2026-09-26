package service

import (
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
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

func TestAssistantIterativeDelegatesSubagentThroughExecutor(t *testing.T) {
	relay := newAssistantTestRelay()
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"services":[{"name":"api"}],"total":1}`}}}}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-delegate", Name: assistantDelegateSubagentToolName, Arguments: map[string]any{"subagent": "researcher", "task": "list services"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "child-read", Name: "bahia_list_services"}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("found the api service"), StopReason: llm.AgentStopReasonEndTurn},
		{Content: textBlocks("delegation complete"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	subagents := mustSubagentLibrary(t, AssistantSubagentSpec{Name: "researcher", Description: "Investigate", Tools: []string{"bahia_list_services"}, SystemPrompt: "Research carefully."})
	st := newAssistantLoopStack(t, relay, testAssistantSigner(t), server, model, assistantLoopStackOptions{registry: assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services")), agentic: AssistantAgentLoopConfig{Subagents: subagents}, permMode: domain.AssistantPermissionModeReview})

	x := st.runIterative(t, relay, "session-delegate", "delegate to researcher")
	if x.Phase != domain.AssistantExecutionCompleted || len(x.Work) != 1 || x.Work[0].ToolName != assistantDelegateSubagentToolName || x.Work[0].State != domain.AssistantWorkSucceeded {
		t.Fatalf("delegation must run as one executor work item: phase=%s work=%+v", x.Phase, x.Work)
	}
	if server.callCount() != 1 || server.invokeCount() != 0 {
		t.Fatalf("child read should execute once through the gate: calls=%d invokes=%d", server.callCount(), server.invokeCount())
	}
	if model.callCount() != 4 {
		t.Fatalf("model calls = %d, want 4", model.callCount())
	}
	if !requestHasToolObservation(model.request(3), "call-delegate", domain.AssistantToolObservationSucceeded) {
		t.Fatalf("parent did not receive a successful delegation observation: %#v", model.request(3).Messages)
	}
}

func TestAssistantIterativeSubagentToolRestrictionIntersection(t *testing.T) {
	relay := newAssistantTestRelay()
	server := &assistantRuntimeMCPServer{}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-delegate", Name: assistantDelegateSubagentToolName, Arguments: map[string]any{"subagent": "reader", "task": "peek"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "child-forbidden", Name: "bahia_forbidden_tool"}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("I was not allowed to use that tool"), StopReason: llm.AgentStopReasonEndTurn},
		{Content: textBlocks("done"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	subagents := mustSubagentLibrary(t, AssistantSubagentSpec{Name: "reader", Description: "Reader", Tools: []string{"bahia_list_services"}, SystemPrompt: "Only read services."})
	st := newAssistantLoopStack(t, relay, testAssistantSigner(t), server, model, assistantLoopStackOptions{registry: assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services"), syncDescriptor("bahia_forbidden_tool")), agentic: AssistantAgentLoopConfig{Subagents: subagents}, permMode: domain.AssistantPermissionModeReview})

	x := st.runIterative(t, relay, "session-restrict", "delegate to reader")
	if x.Phase != domain.AssistantExecutionCompleted {
		t.Fatalf("phase = %s", x.Phase)
	}
	if server.callCount() != 0 {
		t.Fatalf("forbidden tool must not execute, server calls = %d", server.callCount())
	}
	if !requestHasToolObservation(model.request(3), "call-delegate", domain.AssistantToolObservationSucceeded) {
		t.Fatalf("delegation should still return a sync observation: %#v", model.request(3).Messages)
	}
}

// A command's allowed-tools scope also bounds delegation: the delegate tool is
// not advertised or dispatchable unless the scope names it.
func TestAssistantIterativeCommandScopeWithholdsSubagentDelegation(t *testing.T) {
	relay := newAssistantTestRelay()
	server := &assistantRuntimeMCPServer{}
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "call-delegate", Name: assistantDelegateSubagentToolName, Arguments: map[string]any{"subagent": "reader", "task": "peek"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("delegation withheld"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	subagents := mustSubagentLibrary(t, AssistantSubagentSpec{Name: "reader", Description: "Reader", SystemPrompt: "Only read services."})
	commands := mustCommandLibrary(AssistantCommandSpec{Name: "inspect", Template: "Inspect services.", AllowedTools: []string{"bahia_list_services"}})
	st := newAssistantLoopStack(t, relay, testAssistantSigner(t), server, model, assistantLoopStackOptions{registry: assistantRuntimeRegistryWith(syncDescriptor("bahia_list_services")), agentic: AssistantAgentLoopConfig{Subagents: subagents}, commands: commands})

	x := st.runIterative(t, relay, "session-scope-delegate", "/inspect")
	if x.Phase != domain.AssistantExecutionCompleted || assistantWorkState(x, 0) != domain.AssistantWorkDenied || model.callCount() != 2 {
		t.Fatalf("out-of-scope delegation: phase=%s work=%+v model=%d", x.Phase, x.Work, model.callCount())
	}
	if requestToolNames(model.request(0))[assistantDelegateSubagentToolName] {
		t.Fatal("delegate tool advertised outside the command scope")
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
