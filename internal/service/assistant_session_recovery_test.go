package service

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

func publishAssistantSessionEvent(t *testing.T, relay *assistantTestRelay, signer nostr.Signer, schema, sessionID string, content any, created nostr.Timestamp) {
	t.Helper()
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	ev := nostr.Event{Kind: nostr.Kind(domain.KindAssistantSessionState), CreatedAt: created, Tags: nostr.Tags{{"d", schema + ":" + sessionID}, {domain.AssistantSessionTagSchema, schema}, {"session", sessionID}}, Content: string(raw)}
	if err := signer.SignEvent(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Publish(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
}

func rootCheckpoints(relay *assistantTestRelay, runID string) int {
	n := 0
	for _, rev := range assistantCheckpointRevisions(relay, runID) {
		if rev == 1 {
			n++
		}
	}
	return n
}

// A v1 iterative session waiting on a correlated receipt converts to an
// accounting-only v2 run: the receipt is observed, nothing is redispatched and
// reasoning does not resume without a reconstructable call sequence.
func TestAssistantRecoveryConvertsV1IterativeWaitingAsyncForAccountingOnly(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	proposer := &assistantScriptedProposer{}
	session := &domain.AssistantSession{SessionID: "s-v1", State: domain.AssistantSessionStateExecuting, OperatorPubkey: "operator", CurrentTurnID: "turn-1", CurrentRequestID: "request-1", Metadata: map[string]any{}}
	setAssistantAgentLoopMetadata(session, domain.AssistantAgentLoopMetadata{RunID: "run-legacy", State: domain.AssistantAgentLoopStateWaitingAsync, PendingToolCallID: "call-1", WaitingReceipt: &domain.AsyncToolReceipt{ToolName: "mutate", RequestEventID: "legacy-request", RequestKind: 25910, ResultKinds: []int{7961}, IdempotencyKey: "assistant-agent:s-v1:run-legacy:call-1"}})
	publishAssistantSessionEvent(t, relay, signer, domain.AssistantSessionSchema, "s-v1", session, nostr.Timestamp(assistantTestClock().Unix()-60))

	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: proposer})
	first.recover(t, relay)
	relay.waitFor(t, "legacy receipt observed", func() bool { return relay.liveSubs(7961) == 1 })
	x := first.snapshot("s-v1")
	if x.RunID != "run-legacy" || x.Phase != domain.AssistantExecutionBlocked || x.Migration == nil || x.Migration.Classification != string(AssistantLegacyIterativeAccounting) {
		t.Fatalf("conversion=%+v", x)
	}
	publishAssistantResult(t, relay, "legacy-request", "completed")
	relay.waitFor(t, "accounting recorded", func() bool { return assistantWorkState(first.snapshot("s-v1"), 0) == domain.AssistantWorkSucceeded })
	x = first.snapshot("s-v1")
	if x.Phase != domain.AssistantExecutionBlocked || server.total() != 0 || proposer.calls() != 0 {
		t.Fatalf("accounting-only run acted: phase=%s calls=%d proposals=%d", x.Phase, server.total(), proposer.calls())
	}
	first.crash()

	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: proposer})
	second.recover(t, relay)
	second.crash()
	if n := rootCheckpoints(relay, "run-legacy"); n != 1 {
		t.Fatalf("conversion re-rooted: %d roots", n)
	}
	if server.total() != 0 || proposer.calls() != 0 {
		t.Fatal("second recovery acted on accounting-only run")
	}
}

// A v1 batch draft converts to a v2 draft that is approved and executed only
// through the common executor.
func TestAssistantRecoveryConvertsV1BatchDraftForV2Approval(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	plan := domain.AssistantPlan{Summary: "legacy", Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}}}
	session := domain.AssistantSession{SessionID: "s-draft", State: domain.AssistantSessionStateAwaitingApproval, OperatorPubkey: "operator", CurrentTurnID: "turn-1", CurrentRequestID: "request-1", CurrentPlan: &plan, LastPlanHash: domain.ComputePlanHash(plan, "s-draft")}
	publishAssistantSessionEvent(t, relay, signer, domain.AssistantSessionSchema, "s-draft", session, nostr.Timestamp(assistantTestClock().Unix()-60))

	st := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	st.recover(t, relay)
	x := st.snapshot("s-draft")
	if x.Phase != domain.AssistantExecutionAwaitingApproval || x.Proposal == nil || x.Proposal.Hash == session.LastPlanHash || server.total() != 0 {
		t.Fatalf("draft conversion=%+v", x)
	}
	p := x.Proposal
	if _, err := st.engine.Decide(context.Background(), AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s-draft", RunID: x.RunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: "approve"}, OperatorPubkey: "operator", RequestEventID: "approval-v2"}); err != nil {
		t.Fatal(err)
	}
	relay.waitFor(t, "migrated draft completed", func() bool { return st.snapshot("s-draft").Phase == domain.AssistantExecutionCompleted })
	if server.count("read-one") != 1 {
		t.Fatalf("read-one=%d", server.count("read-one"))
	}
}

// Contradictory v1 state and a v2 projection whose checkpoint is missing from
// the journal both park: nothing is loaded and nothing executes.
func TestAssistantRecoveryParksAmbiguousHistory(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}}}
	mixed := &domain.AssistantSession{SessionID: "s-mixed", State: domain.AssistantSessionStateExecuting, OperatorPubkey: "operator", CurrentTurnID: "turn", CurrentRequestID: "request", CurrentPlan: &plan, LastPlanHash: domain.ComputePlanHash(plan, "s-mixed"), PendingSteps: plan.Steps, Metadata: map[string]any{}}
	setAssistantAgentLoopMetadata(mixed, domain.AssistantAgentLoopMetadata{RunID: "run", State: domain.AssistantAgentLoopStateRunning})
	publishAssistantSessionEvent(t, relay, signer, domain.AssistantSessionSchema, "s-mixed", mixed, nostr.Timestamp(assistantTestClock().Unix()-60))
	publishAssistantSessionEvent(t, relay, signer, domain.AssistantSessionSchemaV2, "s-gap", domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: "s-gap", OperatorPubkey: "operator", CurrentRunID: "run-gap", Phase: domain.AssistantExecutionExecuting, Workflow: domain.AssistantWorkflowBatch, ExecutionVersion: 2, CheckpointEventID: "0000000000000000000000000000000000000000000000000000000000000001"}, nostr.Timestamp(assistantTestClock().Unix()-60))

	st := newAssistantStack(t, relay, signer, server, assistantStackOptions{})
	st.recover(t, relay)
	st.crash()
	if _, ok := st.engine.Snapshot("s-mixed"); ok {
		t.Fatal("mixed v1 history was loaded")
	}
	if _, ok := st.engine.Snapshot("s-gap"); ok {
		t.Fatal("projection with a missing checkpoint was loaded")
	}
	if server.total() != 0 || len(relay.eventsOfKind(nostr.Kind(domain.AssistantExecutionCheckpointKind))) != 0 {
		t.Fatal("parked history produced effects")
	}
}
