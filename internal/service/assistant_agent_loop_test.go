package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// The proposer returns every call of one model response; the executor runs
// them all before the next model request, which sees every observation.
func TestAssistantIterativeProposerReturnsWholeResponseAndContinuesFromTranscript(t *testing.T) {
	relay := newAssistantTestRelay()
	server := newAssistantTestToolServer(relay.touch)
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "a", Name: "read-one", Arguments: map[string]any{"q": 1}}, {ID: "b", Name: "read-two", Arguments: map[string]any{}}}, StopReason: llm.AgentStopReasonToolCalls},
		{Content: textBlocks("both reads done"), StopReason: llm.AgentStopReasonEndTurn},
	}}
	st := newAssistantLoopStack(t, relay, testAssistantSigner(t), server, model, assistantLoopStackOptions{})

	x := st.runIterative(t, relay, "s-multi", "read twice")
	if x.Phase != domain.AssistantExecutionCompleted || len(x.Work) != 2 || assistantWorkState(x, 0) != domain.AssistantWorkSucceeded || assistantWorkState(x, 1) != domain.AssistantWorkSucceeded {
		t.Fatalf("phase=%s work=%+v", x.Phase, x.Work)
	}
	if model.callCount() != 2 || server.count("read-one") != 1 || server.count("read-two") != 1 {
		t.Fatalf("model=%d read-one=%d read-two=%d", model.callCount(), server.count("read-one"), server.count("read-two"))
	}
	second := model.request(1)
	if !requestHasToolObservation(second, "a", domain.AssistantToolObservationSucceeded) || !requestHasToolObservation(second, "b", domain.AssistantToolObservationSucceeded) {
		t.Fatalf("continuation lacked an observation: %#v", second.Messages)
	}
	if !requestHasText(second, "read twice") {
		t.Fatalf("continuation lost the operator prompt from transcript history: %#v", second.Messages)
	}
	final := st.status.phase("loop_completed")
	if final == nil || final["summary"] != "both reads done" || final["message"] != "both reads done" {
		t.Fatalf("final answer not surfaced: %#v", st.status.statuses)
	}
}

// The model iteration cap is owned by the iterative proposer and derived from
// the durable transcript, so a restart between model calls cannot reset it.
func TestAssistantIterativeProposerEnforcesIterationCapAcrossRestart(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	model := &assistantLoopModel{responses: []*llm.AgentModelResponse{
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "m1", Name: "mutate", Arguments: map[string]any{"zone": "a"}}}, StopReason: llm.AgentStopReasonToolCalls},
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "r2", Name: "read-one", Arguments: map[string]any{}}}, StopReason: llm.AgentStopReasonToolCalls},
		{ToolCalls: []domain.AssistantAgentToolCall{{ID: "r3", Name: "read-two", Arguments: map[string]any{}}}, StopReason: llm.AgentStopReasonToolCalls},
	}}
	opts := assistantLoopStackOptions{agentic: AssistantAgentLoopConfig{Agentic: config.AssistantAgenticConfig{MaxIterations: 2}}}
	first := newAssistantLoopStack(t, relay, signer, server, model, opts)
	if _, err := first.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-cap", TurnID: "t", Prompt: "loop forever"}, OperatorPubkey: "operator", RequestEventID: "prompt-cap", DefaultWorkflow: domain.AssistantWorkflowIterative}); err != nil {
		t.Fatal(err)
	}
	relay.waitFor(t, "async mutation submitted", func() bool { return assistantWorkState(first.snapshot("s-cap"), 0) == domain.AssistantWorkWaitingAsync })
	first.crash()

	second := newAssistantLoopStack(t, relay, signer, server, model, opts)
	second.recover(t, relay)
	publishAssistantResult(t, relay, assistantTestRequestID(server.keysFor("mutate")[0]), "completed")
	x := second.settle(t, relay, "s-cap")
	if x.Phase != domain.AssistantExecutionBlocked {
		t.Fatalf("iteration cap did not block the run: phase=%s work=%+v", x.Phase, x.Work)
	}
	if model.callCount() != 2 {
		t.Fatalf("model calls = %d, want exactly max_iterations=2 across the restart", model.callCount())
	}
	if blocked := second.status.phase("loop_guard_blocked"); blocked == nil || !strings.Contains(blocked["error"].(string), "max_iterations=2") {
		t.Fatalf("cap reason not surfaced: %#v", second.status.statuses)
	}
	if server.count("mutate") != 1 || server.count("read-one") != 1 || server.count("read-two") != 0 {
		t.Fatalf("dispatch counts mutate=%d read-one=%d read-two=%d", server.count("mutate"), server.count("read-one"), server.count("read-two"))
	}
}

func TestAssistantIterativeProposerUserPromptHookBlocksBeforeModel(t *testing.T) {
	relay := newAssistantTestRelay()
	model := &assistantLoopModel{}
	hooks := hookRunnerWith(map[AssistantHookEvent][]AssistantHookMatcher{
		AssistantHookEventUserPromptSubmit: {{Matcher: "*", Handlers: []AssistantHookHandler{{Type: AssistantHookHandlerPrompt, Prompt: "screen"}}}},
	}, &scriptedHookEvaluator{outcomes: map[string][]AssistantHookOutcome{"screen": {{Decision: AssistantHookDecisionDeny, Reason: "prompt refused"}}}})
	st := newAssistantLoopStack(t, relay, testAssistantSigner(t), &assistantRuntimeMCPServer{}, model, assistantLoopStackOptions{hooks: hooks})

	x := st.runIterative(t, relay, "s-screen", "do something")
	if x.Phase != domain.AssistantExecutionBlocked || model.callCount() != 0 {
		t.Fatalf("blocked prompt reached the model: phase=%s calls=%d", x.Phase, model.callCount())
	}
	if st.status.phase("user_prompt_blocked") == nil {
		t.Fatalf("prompt block not surfaced: %#v", st.status.statuses)
	}
}
