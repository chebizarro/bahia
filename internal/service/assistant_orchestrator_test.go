package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

type assistantRouterFixture struct {
	relay  *assistantTestRelay
	signer nostr.Signer
	server *assistantTestToolServer
	stack  *assistantStack
	router *AssistantOrchestrator
	seq    int
}

func newAssistantRouterFixture(t *testing.T, defaultWorkflow domain.AssistantWorkflow, opts assistantStackOptions, legacy ...domain.AssistantSession) *assistantRouterFixture {
	t.Helper()
	f := &assistantRouterFixture{relay: newAssistantTestRelay(), signer: testAssistantSigner(t)}
	f.server = newAssistantTestToolServer(f.relay.touch)
	f.stack = newAssistantStack(t, f.relay, f.signer, f.server, opts)
	f.router = f.newRouter(defaultWorkflow, legacy...)
	return f
}

func (f *assistantRouterFixture) newRouter(defaultWorkflow domain.AssistantWorkflow, legacy ...domain.AssistantSession) *AssistantOrchestrator {
	return NewAssistantOrchestrator(AssistantOrchestratorConfig{Engine: f.stack.engine, DefaultWorkflow: defaultWorkflow, Publisher: f.relay, Subscriber: f.relay, Signer: f.signer, Identity: AssistantIdentity{AgentID: "assistant-test"}, InitialSessions: legacy})
}

func (f *assistantRouterFixture) source(label string) AssistantRequestSource {
	f.seq++
	id := assistantTestID(label + "-" + string(rune('a'+f.seq)))
	return AssistantRequestSource{Event: &nostr.Event{ID: id, PubKey: assistantTestPubKey("operator"), Kind: 25910}, OperatorPubkey: "operator", RequestID: id.Hex()}
}

func (f *assistantRouterFixture) prompt(t *testing.T, sessionID string, workflow domain.AssistantWorkflow) AssistantOperationResult {
	t.Helper()
	res, err := f.router.HandlePromptRequest(context.Background(), f.source("prompt"), domain.AssistantPromptRequest{ContractVersion: 2, Workflow: workflow, SessionID: sessionID, TurnID: "turn", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func assistantResultSession(t *testing.T, res AssistantOperationResult) domain.AssistantSessionV2 {
	t.Helper()
	p, ok := res["session"].(domain.AssistantSessionV2)
	if !ok {
		t.Fatalf("result carries no session projection: %#v", res)
	}
	return p
}

func requireAccepted(t *testing.T, res AssistantOperationResult) domain.AssistantSessionV2 {
	t.Helper()
	if res["status"] != "accepted" {
		t.Fatalf("request refused: %#v", res)
	}
	return assistantResultSession(t, res)
}

func requireRefusal(t *testing.T, res AssistantOperationResult, code string) {
	t.Helper()
	if res["status"] != "failed" || res["step"] != code || res["error"] == "" {
		t.Fatalf("result = %#v, want refusal {status:failed, step:%s}", res, code)
	}
}

func (f *assistantRouterFixture) rejectDraft(t *testing.T, sessionID string, p domain.AssistantSessionV2) {
	t.Helper()
	res, err := f.router.HandleApprovalRequest(context.Background(), f.source("reject"), domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: sessionID, RunID: p.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.Proposal.ProposalID, BaseRevision: p.Proposal.Revision, BasePlanHash: p.Proposal.Hash, ApprovedRevision: p.Proposal.Revision, ApprovedPlanHash: p.Proposal.Hash, Decision: "reject"})
	if err != nil {
		t.Fatal(err)
	}
	if requireAccepted(t, res).Phase != domain.AssistantExecutionCancelled {
		t.Fatalf("plan rejection result = %#v", res)
	}
}

// Workflow precedence: explicit request > persisted session workflow >
// configured default. Accepted requests report status "accepted" plus the
// phase, never a phase in the status field.
func TestAssistantOrchestratorWorkflowSelectionPrecedence(t *testing.T) {
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowIterative, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantSyncAsyncSyncPlan()}, iterative: &assistantScriptedProposer{}})

	explicit := requireAccepted(t, f.prompt(t, "s-pref", domain.AssistantWorkflowBatch))
	if explicit.Workflow != domain.AssistantWorkflowBatch || explicit.Phase != domain.AssistantExecutionAwaitingApproval {
		t.Fatalf("explicit workflow ignored: %+v", explicit)
	}
	f.rejectDraft(t, "s-pref", explicit)
	persisted := requireAccepted(t, f.prompt(t, "s-pref", ""))
	if persisted.Workflow != domain.AssistantWorkflowBatch || persisted.CurrentRunID == explicit.CurrentRunID {
		t.Fatalf("persisted session workflow did not win over the default: %+v", persisted)
	}
	fresh := requireAccepted(t, f.prompt(t, "s-new", ""))
	if fresh.Workflow != domain.AssistantWorkflowIterative || fresh.Phase != domain.AssistantExecutionCompleted {
		t.Fatalf("configured default not applied to a new session: %+v", fresh)
	}
	requireRefusal(t, f.prompt(t, "s-pref", domain.AssistantWorkflowIterative), AssistantRefusalRunInProgress)
}

// A second prompt is refused with run_in_progress while the run is awaiting
// approval, executing, waiting on a submitted effect, cancelling with
// accounting pending, or blocked.
func TestAssistantOrchestratorSecondPromptRefusedForEveryUnfinishedPhase(t *testing.T) {
	blocked := &assistantScriptedProposer{responses: []AssistantProposal{{Kind: AssistantProposalBlocked, Reason: "needs operator"}}}
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowBatch, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantAsyncThenSyncPlan()}, iterative: blocked})
	gate := f.server.gate("mutate")

	draft := requireAccepted(t, f.prompt(t, "s-busy", ""))
	requireRefusal(t, f.prompt(t, "s-busy", ""), AssistantRefusalRunInProgress) // awaiting_approval
	p := draft.Proposal
	res, err := f.router.HandleApprovalRequest(context.Background(), f.source("approve"), domain.AssistantApprovalRequest{ContractVersion: 2, RequestID: "approve-busy", SessionID: "s-busy", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	requireAccepted(t, res)
	f.relay.waitFor(t, "mutation dispatching", func() bool { return f.server.count("mutate") == 1 })
	requireRefusal(t, f.prompt(t, "s-busy", ""), AssistantRefusalRunInProgress) // executing
	close(gate)
	f.relay.waitFor(t, "waiting on receipt", func() bool { return f.stack.snapshot("s-busy").Phase == domain.AssistantExecutionWaitingAsync })
	requireRefusal(t, f.prompt(t, "s-busy", ""), AssistantRefusalRunInProgress) // waiting_async
	cancel, err := f.router.HandleCancellationRequest(context.Background(), f.source("cancel"), domain.AssistantCancellationRequest{ContractVersion: 2, SessionID: "s-busy", RunID: draft.CurrentRunID, Scope: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if requireAccepted(t, cancel).Phase != domain.AssistantExecutionCancelling {
		t.Fatalf("cancel with a submitted effect = %#v", cancel)
	}
	requireRefusal(t, f.prompt(t, "s-busy", ""), AssistantRefusalRunInProgress) // accounting pending

	if requireAccepted(t, f.prompt(t, "s-blocked", domain.AssistantWorkflowIterative)).Phase != domain.AssistantExecutionBlocked {
		t.Fatal("blocked proposal did not block the run")
	}
	requireRefusal(t, f.prompt(t, "s-blocked", ""), AssistantRefusalRunInProgress) // blocked
	requireRefusal(t, f.prompt(t, "s-blocked", "hybrid"), AssistantRefusalValidation)
}

func TestAssistantOrchestratorLegacyAndUnknownSessions(t *testing.T) {
	cached := domain.AssistantSession{SessionID: "s-v1-cached", OperatorPubkey: "operator", State: domain.AssistantSessionStateCompleted}
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowBatch, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantSyncAsyncSyncPlan()}}, cached)
	requireRefusal(t, f.prompt(t, "s-v1-cached", ""), AssistantRefusalLegacyReadOnly)
	if !f.router.IsSessionParticipant("s-v1-cached", "operator") || f.router.IsSessionParticipant("s-v1-cached", "intruder") {
		t.Fatal("v1 participant check changed")
	}

	// Sessions beyond the bounded startup cache are found by a scoped lookup.
	publishAssistantSessionEvent(t, f.relay, f.signer, domain.AssistantSessionSchema, "s-v1-old", domain.AssistantSession{SessionID: "s-v1-old", OperatorPubkey: "operator", State: domain.AssistantSessionStateCompleted}, nostr.Timestamp(assistantTestClock().Unix()-600))
	requireRefusal(t, f.prompt(t, "s-v1-old", ""), AssistantRefusalLegacyReadOnly)
	publishAssistantSessionEvent(t, f.relay, f.signer, domain.AssistantSessionSchemaV2, "s-foreign", domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: "s-foreign", OperatorPubkey: "someone-else", Workflow: domain.AssistantWorkflowBatch, ExecutionVersion: 2, CurrentRunID: "run-old", Phase: domain.AssistantExecutionCompleted}, nostr.Timestamp(assistantTestClock().Unix()-600))
	requireRefusal(t, f.prompt(t, "s-foreign", ""), AssistantRefusalUnauthorized)
	if f.router.IsSessionParticipant("s-foreign", "operator") {
		t.Fatal("hydrated foreign session admitted a non-participant")
	}

	unreachable := NewAssistantOrchestrator(AssistantOrchestratorConfig{Engine: f.stack.engine, DefaultWorkflow: domain.AssistantWorkflowBatch, Subscriber: assistantFailingSubscriber{}, Signer: f.signer})
	res, err := unreachable.HandlePromptRequest(context.Background(), f.source("prompt"), domain.AssistantPromptRequest{SessionID: "s-unproven", TurnID: "t", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	requireRefusal(t, res, AssistantRefusalSessionLookup)
	if _, ok := f.stack.engine.Snapshot("s-unproven"); ok {
		t.Fatal("an unproven session ID was created")
	}
}

type assistantFailingSubscriber struct{}

func (assistantFailingSubscriber) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (AssistantMergedSubscription, error) {
	return nil, errors.New("relay unreachable")
}

// A session-scope cancellation recorded on a finished run stays enforced after
// a restart even though the v2 projection has no "closed" field.
func TestAssistantOrchestratorSessionCloseOnFinishedRunSurvivesRestart(t *testing.T) {
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowIterative, assistantStackOptions{iterative: &assistantScriptedProposer{}})
	done := requireAccepted(t, f.prompt(t, "s-close", ""))
	if done.Phase != domain.AssistantExecutionCompleted {
		t.Fatalf("phase = %s", done.Phase)
	}
	res, err := f.router.HandleCancellationRequest(context.Background(), f.source("close"), domain.AssistantCancellationRequest{ContractVersion: 2, SessionID: "s-close", RunID: done.CurrentRunID, Scope: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if res["step"] != "session_closed" || res["status"] != "accepted" {
		t.Fatalf("session close result = %#v", res)
	}
	requireRefusal(t, f.prompt(t, "s-close", ""), AssistantRefusalSessionClosed)

	f.stack.crash()
	f.stack = newAssistantStack(t, f.relay, f.signer, f.server, assistantStackOptions{iterative: &assistantScriptedProposer{}})
	f.stack.recover(t, f.relay)
	f.router = f.newRouter(domain.AssistantWorkflowIterative)
	requireRefusal(t, f.prompt(t, "s-close", ""), AssistantRefusalSessionClosed)
}

func TestAssistantOrchestratorRequiresVersionedContracts(t *testing.T) {
	f := newAssistantRouterFixture(t, domain.AssistantWorkflowBatch, assistantStackOptions{batch: assistantTestBatchProposer{plan: assistantSyncAsyncSyncPlan()}})
	draft := requireAccepted(t, f.prompt(t, "s-v2", ""))
	res, err := f.router.HandleApprovalRequest(context.Background(), f.source("old"), domain.AssistantApprovalRequest{SessionID: "s-v2", PlanHash: draft.Proposal.Hash, Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	requireRefusal(t, res, AssistantRefusalContractUpgradeRequired)
	stale, err := f.router.HandleApprovalRequest(context.Background(), f.source("stale"), domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s-v2", RunID: draft.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: draft.Proposal.ProposalID, BaseRevision: draft.Proposal.Revision, BasePlanHash: "not-the-hash", ApprovedRevision: 1, ApprovedPlanHash: "not-the-hash", Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	requireRefusal(t, stale, AssistantRefusalStaleApproval)
	if f.server.total() != 0 || f.stack.snapshot("s-v2").Phase != domain.AssistantExecutionAwaitingApproval {
		t.Fatal("refused approvals changed state or dispatched")
	}
	encoded, err := json.Marshal(stale)
	if err != nil || !json.Valid(encoded) {
		t.Fatalf("refusal is not a JSON result: %v", err)
	}
}
