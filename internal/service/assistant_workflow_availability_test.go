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
// (assistant.llm_model unset with an iterative default). A new turn that
// resolves to batch is refused with workflow_unavailable, never run as
// iterative. Everything that needs no proposer still works: history stays
// readable, existing drafts can be approved or rejected, approved work
// finishes.

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
// workflow can still be approved: approval needs no proposer. The approval
// safeguards are unchanged (an edit is bound to its new revision and hash; a
// mismatched hash is stale), the approved plan runs exactly once with no
// proposer call, and a new batch turn on the session is still refused. A
// draft can equally be rejected without dispatch.
func TestAssistantBatchUnavailablePendingDraftApprovalExecutes(t *testing.T) {
	plan := domain.AssistantPlan{Summary: "reads", Steps: []domain.AssistantPlanStep{
		{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}},
		{StepID: "two", ToolName: "read-two", ToolArgs: map[string]any{}},
	}}
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowBatch, assistantStackOptions{batch: assistantTestBatchProposer{plan: plan}})
	draft := requireAccepted(t, f.prompt(t, "s-draft", domain.AssistantWorkflowBatch))
	other := requireAccepted(t, f.prompt(t, "s-reject", domain.AssistantWorkflowBatch))
	if draft.Phase != domain.AssistantExecutionAwaitingApproval || other.Phase != domain.AssistantExecutionAwaitingApproval {
		t.Fatalf("drafts = %+v / %+v", draft, other)
	}

	iterative := &assistantScriptedProposer{}
	f.restart(t, domain.AssistantWorkflowIterative, assistantStackOptions{iterative: iterative})

	// A new turn resolving to batch through the session is still refused.
	requireWorkflowUnavailable(t, f.prompt(t, "s-draft", ""))

	// The operator's reviewed edit: drop step one.
	edited := draft.Proposal.Plan
	edited.Steps = []domain.AssistantPlanStep{edited.Steps[1]}
	editedHash, err := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: 2, SessionID: "s-draft", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: draft.Proposal.ProposalID, Revision: 2, Scope: draft.Scope, Plan: edited})
	if err != nil {
		t.Fatal(err)
	}
	approval := func(hash string) domain.AssistantApprovalRequest {
		return domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s-draft", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: draft.Proposal.ProposalID, BaseRevision: 1, BasePlanHash: draft.Proposal.Hash, ApprovedRevision: 2, ApprovedPlanHash: hash, Decision: "approve", ModifiedPlan: &edited}
	}
	stale, err := f.router.HandleApprovalRequest(context.Background(), f.source("stale"), approval(draft.Proposal.Hash))
	if err != nil {
		t.Fatal(err)
	}
	requireRefusal(t, stale, AssistantRefusalStaleApproval)
	if f.server.total() != 0 {
		t.Fatal("stale approval dispatched")
	}

	res, err := f.router.HandleApprovalRequest(context.Background(), f.source("approve"), approval(editedHash))
	if err != nil {
		t.Fatal(err)
	}
	requireAccepted(t, res)
	f.relay.waitFor(t, "approved draft completed without the batch proposer", func() bool {
		return f.stack.snapshot("s-draft").Phase == domain.AssistantExecutionCompleted
	})
	x := f.stack.snapshot("s-draft")
	if f.server.count("read-one") != 0 || f.server.count("read-two") != 1 || iterative.calls() != 0 {
		t.Fatalf("read-one=%d read-two=%d proposals=%d", f.server.count("read-one"), f.server.count("read-two"), iterative.calls())
	}
	if x.Workflow != domain.AssistantWorkflowBatch || x.Proposal == nil || x.Proposal.Revision != 2 || x.Proposal.Hash != editedHash {
		t.Fatalf("approved run = %+v", x)
	}
	requireWorkflowUnavailable(t, f.prompt(t, "s-draft", ""))
	if f.server.total() != 1 || iterative.calls() != 0 {
		t.Fatalf("refused turn after completion acted: calls=%d proposals=%d", f.server.total(), iterative.calls())
	}

	if requireAccepted(t, f.decideDraft(t, "s-reject", other, "reject")).Phase != domain.AssistantExecutionCancelled || f.server.total() != 1 {
		t.Fatal("plan rejection was not accepted without dispatch")
	}
}

// A v1 batch draft migrated on a deployment without the batch workflow is
// approved and executed exactly once through the common executor, with no
// proposer call; a new batch turn on the migrated session is still refused.
func TestAssistantBatchUnavailableMigratedV1DraftApprovalExecutes(t *testing.T) {
	relay := newAssistantTestRelay()
	signer := testAssistantSigner(t)
	server := newAssistantTestToolServer(relay.touch)
	plan := assistantReadOnlyPlan()
	session := domain.AssistantSession{SessionID: "s-v1-draft", State: domain.AssistantSessionStateAwaitingApproval, OperatorPubkey: "operator", CurrentTurnID: "turn-1", CurrentRequestID: "request-1", CurrentPlan: &plan, LastPlanHash: domain.ComputePlanHash(plan, "s-v1-draft")}
	publishAssistantSessionEvent(t, relay, signer, domain.AssistantSessionSchema, "s-v1-draft", session, nostr.Timestamp(assistantTestClock().Unix()-60))

	iterative := &assistantScriptedProposer{}
	st := newAssistantStack(t, relay, signer, server, assistantStackOptions{iterative: iterative})
	st.recover(t, relay)
	x := st.snapshot("s-v1-draft")
	if x.Phase != domain.AssistantExecutionAwaitingApproval || x.Workflow != domain.AssistantWorkflowBatch || x.Proposal == nil {
		t.Fatalf("draft conversion=%+v", x)
	}
	p := x.Proposal
	if _, err := st.engine.Decide(context.Background(), AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s-v1-draft", RunID: x.RunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: "approve"}, OperatorPubkey: "operator", RequestEventID: "approval-v2"}); err != nil {
		t.Fatal(err)
	}
	relay.waitFor(t, "migrated draft completed", func() bool { return st.snapshot("s-v1-draft").Phase == domain.AssistantExecutionCompleted })
	if server.count("read-one") != 1 || iterative.calls() != 0 {
		t.Fatalf("read-one=%d proposals=%d", server.count("read-one"), iterative.calls())
	}

	projection, ok := st.engine.Projection("s-v1-draft")
	if !ok || projection.Workflow != domain.AssistantWorkflowBatch {
		t.Fatalf("migrated projection = %+v ok=%v", projection, ok)
	}
	_, err := st.engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-v1-draft", TurnID: "turn-2", Prompt: "again"}, OperatorPubkey: "operator", RequestEventID: "prompt-2", ExistingSession: &projection, DefaultWorkflow: domain.AssistantWorkflowIterative})
	if !errors.Is(err, ErrAssistantWorkflowUnavailable) || server.total() != 1 || iterative.calls() != 0 {
		t.Fatalf("new turn on migrated batch session err=%v calls=%d proposals=%d", err, server.total(), iterative.calls())
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
