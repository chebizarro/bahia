package service

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Command scope with the real iterative proposer: nil is unrestricted, a
// non-nil empty list permits nothing, and a named tool outside the scope is
// denied at dispatch even though the model named it. The persisted scope
// survives a restart between model calls.
func TestAssistantIterativeAllowedToolsNilEmptyAndOutOfScopeAcrossRestart(t *testing.T) {
	t.Run("empty scope advertises and permits nothing", func(t *testing.T) {
		relay := newAssistantTestRelay()
		server := newAssistantTestToolServer(relay.touch)
		model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
			{ToolCalls: []domain.AssistantAgentToolCall{{ID: "c", Name: "read-one", Arguments: map[string]any{}}}, StopReason: llm.AgentStopReasonToolCalls},
		}}
		commands := mustCommandLibrary(AssistantCommandSpec{Name: "chat", Template: "Just talk.", AllowedTools: []string{}})
		st := newAssistantLoopStack(t, relay, testAssistantSigner(t), server, model, assistantLoopStackOptions{commands: commands})
		x := st.runIterative(t, relay, "s-empty", "/chat")
		if x.Scope.AllowedTools == nil || len(x.Scope.AllowedTools) != 0 {
			t.Fatalf("empty scope widened to %v", x.Scope.AllowedTools)
		}
		if len(requestToolNames(model.request(0))) != 0 || assistantWorkState(x, 0) != domain.AssistantWorkDenied || server.total() != 0 {
			t.Fatalf("empty scope: tools=%v work=%+v calls=%d", requestToolNames(model.request(0)), x.Work, server.total())
		}
	})
	t.Run("nil scope is unrestricted", func(t *testing.T) {
		relay := newAssistantTestRelay()
		server := newAssistantTestToolServer(relay.touch)
		model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
			{ToolCalls: []domain.AssistantAgentToolCall{{ID: "c", Name: "read-two", Arguments: map[string]any{}}}, StopReason: llm.AgentStopReasonToolCalls},
		}}
		st := newAssistantLoopStack(t, relay, testAssistantSigner(t), server, model, assistantLoopStackOptions{})
		x := st.runIterative(t, relay, "s-nil", "anything")
		if x.Scope.AllowedTools != nil || len(requestToolNames(model.request(0))) != 3 || server.count("read-two") != 1 {
			t.Fatalf("nil scope: scope=%v tools=%v calls=%d", x.Scope.AllowedTools, requestToolNames(model.request(0)), server.count("read-two"))
		}
	})
	t.Run("out-of-scope call denied and scope survives restart", func(t *testing.T) {
		relay := newAssistantTestRelay()
		signer := testAssistantSigner(t)
		server := newAssistantTestToolServer(relay.touch)
		model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
			{ToolCalls: []domain.AssistantAgentToolCall{{ID: "m", Name: "mutate", Arguments: map[string]any{"zone": "a"}}}, StopReason: llm.AgentStopReasonToolCalls},
			{ToolCalls: []domain.AssistantAgentToolCall{{ID: "r", Name: "read-one", Arguments: map[string]any{}}}, StopReason: llm.AgentStopReasonToolCalls},
		}}
		commands := mustCommandLibrary(AssistantCommandSpec{Name: "change", Template: "Change zone.", AllowedTools: []string{"mutate"}})
		first := newAssistantLoopStack(t, relay, signer, server, model, assistantLoopStackOptions{commands: commands})
		first.runIterativeUntil(t, relay, "s-scope-restart", "/change", func(x domain.AssistantExecution) bool {
			return assistantWorkState(x, 0) == domain.AssistantWorkWaitingAsync
		})
		first.crash()
		second := newAssistantLoopStack(t, relay, signer, server, model, assistantLoopStackOptions{commands: commands})
		second.recover(t, relay)
		publishAssistantResult(t, relay, assistantTestRequestID(server.keysFor("mutate")[0]), "completed")
		x := second.settle(t, relay, "s-scope-restart")
		if len(x.Work) < 2 || x.Work[1].ToolName != "read-one" || x.Work[1].State != domain.AssistantWorkDenied || server.count("read-one") != 0 {
			t.Fatalf("out-of-scope call after restart: work=%+v", x.Work)
		}
		if tools := requestToolNames(model.request(1)); len(tools) != 1 || !tools["mutate"] {
			t.Fatalf("continuation after restart advertised %v", tools)
		}
	})
}
