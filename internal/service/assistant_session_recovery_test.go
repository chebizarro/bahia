package service

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAssistantSessionRecoveryUsesLoopForWaitingAsync(t *testing.T) {
	ctx := context.Background()
	loop := &assistantFakeAgentLoop{asyncResult: &AssistantAgentLoopResult{RunID: "run-1", TurnID: "turn-1", Iteration: 2, State: domain.AssistantAgentLoopStateCompleted, SessionState: domain.AssistantSessionStateCompleted, Completed: true}}
	orchestrator := NewAssistantOrchestrator(AssistantOrchestratorConfig{
		Publisher: &assistantTestPublisher{},
		Signer:    testAssistantSigner(t),
		Identity:  AssistantIdentity{AgentID: "assistant-test", Pubkey: "assistant-pubkey"},
	})
	runner := NewAssistantSessionRecoveryRunner(orchestrator, AssistantSessionRecoveryConfig{AgentLoop: loop})

	session := &domain.AssistantSession{
		SessionID:      "session-1",
		State:          domain.AssistantSessionStateExecuting,
		OperatorPubkey: "operator",
		Participants:   []string{"operator"},
		Metadata:       map[string]any{},
	}
	setAssistantAgentLoopMetadata(session, domain.AssistantAgentLoopMetadata{
		RunID:             "run-1",
		State:             domain.AssistantAgentLoopStateWaitingAsync,
		PendingToolCallID: "tool-call-1",
		WaitingReceipt:    &domain.AsyncToolReceipt{ToolName: "bahia_assistant_dns_zone_create", RequestEventID: "request-1", ResultKinds: []int{30315}},
	})

	if recovered := runner.recoverAgentLoop(ctx, session); !recovered {
		t.Fatal("recoverAgentLoop returned false for waiting_async session")
	}
	if loop.asyncCalls != 1 {
		t.Fatalf("ResumeAfterAsyncObservation calls = %d, want 1", loop.asyncCalls)
	}
	if loop.lastAsync.Session == nil || loop.lastAsync.Session.SessionID != "session-1" {
		t.Fatalf("loop received wrong session: %#v", loop.lastAsync.Session)
	}
}
