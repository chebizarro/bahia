package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// These tests cover a deployment whose batch proposer is not constructed
// (assistant.llm_model unset with an iterative default). Batch requests that
// would start new batch work are refused with workflow_unavailable, never run
// as iterative; history stays readable; already-approved work still finishes.

func assistantReadOnlyPlan() domain.AssistantPlan {
	return domain.AssistantPlan{Summary: "read", Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}}}
}

func (f *assistantRouterFixture) decideDraft(t *testing.T, sessionID string, draft domain.AssistantSessionV2, decision string) AssistantOperationResult {
	t.Helper()
	p := draft.Proposal
	res, err := f.router.HandleApprovalRequest(context.Background(), f.source(decision), domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: sessionID, RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: decision})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// restart replaces every service of the fixture, as a process restart with a
// new configuration would, and runs startup recovery.
func (f *assistantRouterFixture) restart(t *testing.T, defaultWorkflow domain.AssistantWorkflow, opts assistantStackOptions) {
	t.Helper()
	f.stack.crash()
	f.stack = newAssistantStack(t, f.relay, f.signer, f.server, opts)
	f.stack.recover(t, f.relay)
	f.router = f.newRouter(defaultWorkflow)
}

func requireWorkflowUnavailable(t *testing.T, res AssistantOperationResult) {
	t.Helper()
	requireRefusal(t, res, AssistantRefusalWorkflowUnavailable)
	msg, _ := res["error"].(string)
	if !strings.Contains(msg, "batch workflow is not available") || !strings.Contains(msg, "assistant.llm_model") || res["summary"] != msg {
		t.Fatalf("refusal message = %#v, want batch + assistant.llm_model explanation", res)
	}
}

func assistantCheckpointCount(relay *assistantTestRelay) int {
	return len(relay.eventsOfKind(nostr.Kind(domain.AssistantExecutionCheckpointKind)))
}

// An explicit batch request is refused before any checkpoint and is never
// silently run through the iterative proposer; iterative requests still work.
func TestAssistantBatchUnavailableExplicitRequestRefusedNeverDowngraded(t *testing.T) {
	iterative := &assistantScriptedProposer{}
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowIterative, assistantStackOptions{iterative: iterative})

	requireWorkflowUnavailable(t, f.prompt(t, "s-batch", domain.AssistantWorkflowBatch))
	if iterative.calls() != 0 || f.server.total() != 0 || assistantCheckpointCount(f.relay) != 0 {
		t.Fatalf("refused batch request acted: proposals=%d calls=%d checkpoints=%d", iterative.calls(), f.server.total(), assistantCheckpointCount(f.relay))
	}
	if _, ok := f.stack.engine.Snapshot("s-batch"); ok {
		t.Fatal("refused batch request created a run")
	}
	if _, ok := f.stack.engine.Projection("s-batch"); ok {
		t.Fatal("refused batch request created a session projection")
	}

	fresh := requireAccepted(t, f.prompt(t, "s-iter", ""))
	if fresh.Workflow != domain.AssistantWorkflowIterative || fresh.Phase != domain.AssistantExecutionCompleted || iterative.calls() != 1 {
		t.Fatalf("iterative default = %+v proposals=%d", fresh, iterative.calls())
	}

	// A batch default without a batch proposer (unreachable with validated
	// config) is refused the same way rather than run as iterative.
	_, err := f.stack.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-default-batch", TurnID: "t", Prompt: "go"}, OperatorPubkey: "operator", RequestEventID: "prompt-default-batch", DefaultWorkflow: domain.AssistantWorkflowBatch})
	if !errors.Is(err, ErrAssistantWorkflowUnavailable) || iterative.calls() != 1 {
		t.Fatalf("batch default err=%v proposals=%d", err, iterative.calls())
	}
}

// A session whose persisted workflow is batch keeps its history readable after
// the deployment loses the batch workflow; a new turn that resolves to batch
// through the session is refused, never downgraded. An explicit iterative
// request is the operator's own choice and is honoured.
func TestAssistantBatchUnavailablePersistedBatchSessionRefused(t *testing.T) {
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowBatch, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantReadOnlyPlan()}})
	draft := requireAccepted(t, f.prompt(t, "s-hist", domain.AssistantWorkflowBatch))
	requireAccepted(t, f.decideDraft(t, "s-hist", draft, "approve"))
	f.relay.waitFor(t, "batch run completed", func() bool { return f.stack.snapshot("s-hist").Phase == domain.AssistantExecutionCompleted })
	revisions := assistantCheckpointRevisions(f.relay, draft.CurrentRunID)
	checkpoints := assistantCheckpointCount(f.relay)

	iterative := &assistantScriptedProposer{}
	f.restart(t, domain.AssistantWorkflowIterative, assistantStackOptions{iterative: iterative})

	requireWorkflowUnavailable(t, f.prompt(t, "s-hist", ""))
	if iterative.calls() != 0 || f.server.total() != 1 || assistantCheckpointCount(f.relay) != checkpoints {
		t.Fatalf("persisted batch session downgraded or acted: proposals=%d calls=%d checkpoints=%d", iterative.calls(), f.server.total(), assistantCheckpointCount(f.relay))
	}
	// History stays readable: the finished run, its journal and transcript.
	x := f.stack.snapshot("s-hist")
	if x.RunID != draft.CurrentRunID || x.Workflow != domain.AssistantWorkflowBatch || x.Phase != domain.AssistantExecutionCompleted {
		t.Fatalf("history after refusal = %+v", x)
	}
	if got := assistantCheckpointRevisions(f.relay, draft.CurrentRunID); len(got) != len(revisions) {
		t.Fatalf("refusal changed the run journal: %v -> %v", revisions, got)
	}
	if obs := f.stack.transcriptObservations(t, "s-hist"); len(obs) != 1 || obs[0].Payload.Message.Name != "read-one" {
		t.Fatalf("transcript after refusal = %+v", obs)
	}
	if p, ok := f.stack.engine.Projection("s-hist"); !ok || p.Workflow != domain.AssistantWorkflowBatch || !f.router.IsSessionParticipant("s-hist", "operator") {
		t.Fatalf("projection after refusal = %+v ok=%v", p, ok)
	}

	switched := requireAccepted(t, f.prompt(t, "s-hist", domain.AssistantWorkflowIterative))
	if switched.Workflow != domain.AssistantWorkflowIterative || iterative.calls() != 1 {
		t.Fatalf("explicit iterative turn = %+v proposals=%d", switched, iterative.calls())
	}
}

// A batch draft awaiting approval when the deployment loses the batch
// workflow cannot be approved (approval grants new batch authority), but it
// can be rejected, which closes the run without dispatch.
func TestAssistantBatchUnavailablePendingDraftApprovalRefusedRejectAllowed(t *testing.T) {
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowBatch, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantReadOnlyPlan()}})
	draft := requireAccepted(t, f.prompt(t, "s-draft", domain.AssistantWorkflowBatch))
	if draft.Phase != domain.AssistantExecutionAwaitingApproval {
		t.Fatalf("draft = %+v", draft)
	}

	iterative := &assistantScriptedProposer{}
	f.restart(t, domain.AssistantWorkflowIterative, assistantStackOptions{iterative: iterative})
	checkpoints := assistantCheckpointCount(f.relay)

	requireWorkflowUnavailable(t, f.decideDraft(t, "s-draft", draft, "approve"))
	if f.server.total() != 0 || iterative.calls() != 0 || assistantCheckpointCount(f.relay) != checkpoints || f.stack.snapshot("s-draft").Phase != domain.AssistantExecutionAwaitingApproval {
		t.Fatalf("refused approval acted: calls=%d proposals=%d phase=%s", f.server.total(), iterative.calls(), f.stack.snapshot("s-draft").Phase)
	}
	requireWorkflowUnavailable(t, f.prompt(t, "s-draft", ""))

	if requireAccepted(t, f.decideDraft(t, "s-draft", draft, "reject")).Phase != domain.AssistantExecutionCancelled || f.server.total() != 0 {
		t.Fatal("plan rejection was not accepted without dispatch")
	}
	if requireAccepted(t, f.prompt(t, "s-draft", domain.AssistantWorkflowIterative)).Workflow != domain.AssistantWorkflowIterative {
		t.Fatal("explicit iterative turn after rejection was not accepted")
	}
}

// A v1 batch draft migrated on a deployment without the batch workflow stays
// awaiting approval; its approval is refused the same way and nothing runs.
func TestAssistantBatchUnavailableMigratedV1DraftApprovalRefused(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	plan := assistantReadOnlyPlan()
	session := domain.AssistantSession{SessionID: "s-v1-draft", State: domain.AssistantSessionStateAwaitingApproval, OperatorPubkey: "operator", CurrentTurnID: "turn-1", CurrentRequestID: "request-1", CurrentPlan: &plan, LastPlanHash: domain.ComputePlanHash(plan, "s-v1-draft")}
	publishAssistantSessionEvent(t, relay, signer, domain.AssistantSessionSchema, "s-v1-draft", session, nostr.Timestamp(assistantTestClock().Unix()-60))

	st := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: &assistantScriptedProposer{}})
	st.recover(t, relay)
	x := st.snapshot("s-v1-draft")
	if x.Phase != domain.AssistantExecutionAwaitingApproval || x.Workflow != domain.AssistantWorkflowBatch || x.Proposal == nil {
		t.Fatalf("draft conversion=%+v", x)
	}
	p := x.Proposal
	_, err := st.engine.Decide(context.Background(), AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s-v1-draft", RunID: x.RunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: "approve"}, OperatorPubkey: "operator", RequestEventID: "approval-v2"})
	if !errors.Is(err, ErrAssistantWorkflowUnavailable) {
		t.Fatalf("approve err = %v, want workflow_unavailable", err)
	}
	if server.total() != 0 || st.snapshot("s-v1-draft").Phase != domain.AssistantExecutionAwaitingApproval {
		t.Fatalf("refused migrated approval acted: calls=%d phase=%s", server.total(), st.snapshot("s-v1-draft").Phase)
	}
}

// Continuing an already-approved batch run needs no model call: the executor
// advances the persisted cursor through the runtime. After a restart without
// the batch workflow the run still finishes exactly once, and no proposer (in
// particular not the iterative one) is consulted.
func TestAssistantBatchUnavailableApprovedRunStillFinishes(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	first := newAssistantStack(t, relay, signer, server, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantSyncAsyncSyncPlan()}})
	start := first.startBatch(t, "s-approved")
	first.approve(t, "s-approved", start)
	relay.waitFor(t, "async step waiting", func() bool {
		return assistantWorkState(first.snapshot("s-approved"), 1) == domain.AssistantWorkWaitingAsync && relay.liveSubs(7961) == 1
	})
	first.crash()
	keys := server.keysFor("mutate")
	if len(keys) != 1 {
		t.Fatalf("mutate keys = %v", keys)
	}
	publishAssistantResult(t, relay, assistantTestRequestID(keys[0]), "completed")

	iterative := &assistantScriptedProposer{}
	second := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: iterative})
	second.recover(t, relay)
	relay.waitFor(t, "approved batch completed without the batch workflow", func() bool {
		return second.snapshot("s-approved").Phase == domain.AssistantExecutionCompleted
	})
	if server.count("read-one") != 1 || server.count("mutate") != 1 || server.count("read-two") != 1 || iterative.calls() != 0 {
		t.Fatalf("read-one=%d mutate=%d read-two=%d proposals=%d", server.count("read-one"), server.count("mutate"), server.count("read-two"), iterative.calls())
	}
	if x := second.snapshot("s-approved"); x.Workflow != domain.AssistantWorkflowBatch || x.RunID != start.Session.CurrentRunID || x.Cursor != 3 {
		t.Fatalf("finished run = %+v", x)
	}
}
