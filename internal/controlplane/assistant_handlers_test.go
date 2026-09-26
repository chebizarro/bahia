package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// ---------------------------------------------------------------------------
// Migrated v1 fixtures produced by the real compatibility classifier.

const legacyOperatorLabel = "legacy-operator"

func legacyOperatorPubkey() nostr.PubKey {
	return nostr.PubKey(sha256.Sum256([]byte(legacyOperatorLabel)))
}

func legacyPlan() domain.AssistantPlan {
	return domain.AssistantPlan{Summary: "Read then verify", RiskLevel: "low", Steps: []domain.AssistantPlanStep{{StepID: "one", Title: "Read", ToolName: "read-one", ToolArgs: map[string]any{"q": "a"}}}}
}

func classifyLegacy(t *testing.T, eventID string, session domain.AssistantSession) domain.AssistantExecution {
	t.Helper()
	raw, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	conversion := service.ClassifyAssistantLegacySession(service.AssistantLegacySessionSource{EventID: eventID, Schema: domain.AssistantSessionSchema, JSON: raw})
	if conversion.Execution == nil {
		t.Fatalf("classifier parked fixture: %s %s", conversion.Classification, conversion.Reason)
	}
	return *conversion.Execution
}

func legacyBatchDraft(t *testing.T, sessionID string) (domain.AssistantExecution, string) {
	t.Helper()
	plan := legacyPlan()
	hash := domain.ComputePlanHash(plan, sessionID)
	x := classifyLegacy(t, "event-draft-"+sessionID, domain.AssistantSession{SessionID: sessionID, State: domain.AssistantSessionStateAwaitingApproval, OperatorPubkey: legacyOperatorPubkey().Hex(), CurrentTurnID: "turn", CurrentRequestID: "request", LastPlanHash: hash, CurrentPlan: &plan})
	return x, hash
}

func legacyIterativeAction(t *testing.T, sessionID string) domain.AssistantExecution {
	t.Helper()
	action := domain.AssistantDeferredAction{ActionID: "action-1", SessionID: sessionID, RunID: "run-legacy", TurnID: "turn", ToolCallID: "call-1", ToolName: "read-one", ToolArgs: map[string]any{"q": "b"}}
	return classifyLegacy(t, "event-action-"+sessionID, domain.AssistantSession{SessionID: sessionID, State: domain.AssistantSessionStateAwaitingApproval, OperatorPubkey: legacyOperatorPubkey().Hex(), CurrentTurnID: "turn", CurrentRequestID: "request", Metadata: map[string]any{
		"agent_loop":       domain.AssistantAgentLoopMetadata{RunID: "run-legacy", State: domain.AssistantAgentLoopStateAwaitingApproval, PendingActionID: "action-1", PendingToolCallID: "call-1"},
		"deferred_actions": map[string]any{"action-1": action},
	}})
}

func legacyBatchAccounting(t *testing.T, sessionID string) (domain.AssistantExecution, string) {
	t.Helper()
	plan := domain.AssistantPlan{Summary: "Mutate", RiskLevel: "low", Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "mutate", ToolArgs: map[string]any{"zone": "a"}}}}
	hash := domain.ComputePlanHash(plan, sessionID)
	key := fmt.Sprintf("assistant:%s:%s:one", sessionID, hash)
	pending := plan.Steps[0]
	pending.IdempotencyKey = key
	receipt := domain.AsyncToolReceipt{ToolName: "mutate", RequestEventID: "legacy-request", RequestKind: 25910, ResultKinds: []int{7961}, IdempotencyKey: key}
	x := classifyLegacy(t, "event-accounting-"+sessionID, domain.AssistantSession{SessionID: sessionID, State: domain.AssistantSessionStateExecuting, OperatorPubkey: legacyOperatorPubkey().Hex(), CurrentTurnID: "turn", CurrentRequestID: "request", LastPlanHash: hash, CurrentPlan: &plan, PendingSteps: []domain.AssistantPlanStep{pending}, Metadata: map[string]any{"pending_receipts": map[string]any{key: receipt}}})
	return x, hash
}

func refusalCode(tr legacyAssistantTranslation) string {
	if tr.refusal == nil {
		return ""
	}
	code, _ := tr.refusal["step"].(string)
	return code
}

// ---------------------------------------------------------------------------
// A. The v1 compatibility rules as a pure decoder over classified snapshots.

func TestTranslateLegacyAssistantApprovalRules(t *testing.T) {
	draft, draftHash := legacyBatchDraft(t, "s-draft")
	action := legacyIterativeAction(t, "s-action")
	accounting, accountingHash := legacyBatchAccounting(t, "s-accounting")
	native := domain.AssistantExecution{Version: 2, SessionID: "s-native", RunID: "run-native", Workflow: domain.AssistantWorkflowBatch, Phase: domain.AssistantExecutionAwaitingApproval, Proposal: &domain.AssistantProposalRevision{ProposalID: "p", Revision: 1, Hash: "native-hash"}}
	edited := legacyPlan()

	t.Run("unedited batch approval binds the migrated draft revision", func(t *testing.T) {
		for _, decision := range []string{"approve", "reject"} {
			tr := translateLegacyAssistantApproval(draft, true, domain.AssistantApprovalRequest{SessionID: "s-draft", PlanHash: draftHash, Decision: decision, Message: "ok"}, "legacy-request")
			a := tr.approval
			if tr.refusal != nil || a == nil || a.ContractVersion != 2 || a.RunID != draft.RunID || a.Workflow != domain.AssistantWorkflowBatch || a.ProposalID != draft.Proposal.ProposalID ||
				a.BaseRevision != 1 || a.ApprovedRevision != 1 || a.BasePlanHash != draft.Proposal.Hash || a.ApprovedPlanHash != draft.Proposal.Hash || a.Decision != decision || a.ModifiedPlan != nil || a.Reason != "ok" {
				t.Fatalf("%s translation = %+v refusal=%v", decision, a, tr.refusal)
			}
			if a.BasePlanHash == draftHash {
				t.Fatal("legacy v1 hash reused as the v2 proposal hash")
			}
		}
	})
	t.Run("legacy hash must match the migrated draft", func(t *testing.T) {
		tr := translateLegacyAssistantApproval(draft, true, domain.AssistantApprovalRequest{SessionID: "s-draft", PlanHash: "0" + draftHash[1:], Decision: "approve"}, "legacy-request")
		if refusalCode(tr) != service.AssistantRefusalStaleApproval {
			t.Fatalf("mismatched legacy hash = %+v", tr)
		}
		moved := draft
		proposal := *draft.Proposal
		proposal.Revision, proposal.PreviousRevision, proposal.PreviousHash = 2, 1, draft.Proposal.Hash
		moved.Proposal = &proposal
		if refusalCode(translateLegacyAssistantApproval(moved, true, domain.AssistantApprovalRequest{SessionID: "s-draft", PlanHash: draftHash, Decision: "approve"}, "legacy-request")) != service.AssistantRefusalStaleApproval {
			t.Fatal("legacy hash accepted for a draft that is no longer the migrated revision")
		}
	})
	t.Run("edited plan from an old client requires the upgraded contract", func(t *testing.T) {
		for _, x := range []domain.AssistantExecution{draft, native, {}} {
			tr := translateLegacyAssistantApproval(x, x.RunID != "", domain.AssistantApprovalRequest{SessionID: "s-draft", PlanHash: draftHash, Decision: "approve", ModifiedPlan: &edited}, "legacy-request")
			if refusalCode(tr) != service.AssistantRefusalContractUpgradeRequired || tr.approval != nil || tr.cancellation != nil {
				t.Fatalf("edited old-client approval = %+v", tr)
			}
		}
	})
	t.Run("action approval only for the unique migrated pending action", func(t *testing.T) {
		tr := translateLegacyAssistantApproval(action, true, domain.AssistantApprovalRequest{SessionID: "s-action", ActionID: "action-1", Decision: "approve"}, "legacy-request")
		if tr.approval == nil || tr.approval.ContractVersion != 2 || tr.approval.RunID != "run-legacy" || tr.approval.Workflow != domain.AssistantWorkflowIterative || tr.approval.ActionID != "action-1" {
			t.Fatalf("action translation = %+v", tr)
		}
		if tr := translateLegacyAssistantApproval(action, true, domain.AssistantApprovalRequest{SessionID: "s-action", ActionID: "action-1", Decision: "cancel"}, "legacy-request"); tr.approval == nil || tr.approval.Decision != "reject" {
			t.Fatalf("legacy action cancel must stay a rejection: %+v", tr)
		}
		for label, req := range map[string]domain.AssistantApprovalRequest{
			"other action":           {SessionID: "s-action", ActionID: "action-2", Decision: "approve"},
			"action on batch":        {SessionID: "s-draft", ActionID: "action-1", Decision: "approve"},
			"native iterative":       {SessionID: "s-native", ActionID: "run-native:c", Decision: "approve"},
			"plan hash on iterative": {SessionID: "s-action", PlanHash: draftHash, Decision: "approve"},
		} {
			x := action
			switch label {
			case "action on batch":
				x = draft
			case "native iterative":
				x = native
				x.Workflow = domain.AssistantWorkflowIterative
			}
			if code := refusalCode(translateLegacyAssistantApproval(x, true, req, "legacy-request")); code != service.AssistantRefusalStaleApproval && code != service.AssistantRefusalContractUpgradeRequired {
				t.Fatalf("%s accepted: %q", label, code)
			}
		}
	})
	t.Run("plan-hash cancel only for the migrated current batch run", func(t *testing.T) {
		tr := translateLegacyAssistantApproval(accounting, true, domain.AssistantApprovalRequest{SessionID: "s-accounting", PlanHash: accountingHash, Decision: "cancel", Reason: "stop"}, "legacy-request")
		c := tr.cancellation
		if c == nil || c.ContractVersion != 2 || c.RunID != accounting.RunID || c.Scope != "run" || c.Reason != "stop" {
			t.Fatalf("cancel translation = %+v", tr)
		}
		if refusalCode(translateLegacyAssistantApproval(accounting, true, domain.AssistantApprovalRequest{SessionID: "s-accounting", PlanHash: draftHash, Decision: "cancel"}, "legacy-request")) != service.AssistantRefusalStaleApproval {
			t.Fatal("cancel with another run's legacy hash accepted")
		}
		finished := accounting
		finished.Phase = domain.AssistantExecutionCancelled
		if refusalCode(translateLegacyAssistantApproval(finished, true, domain.AssistantApprovalRequest{SessionID: "s-accounting", PlanHash: accountingHash, Decision: "cancel"}, "legacy-request")) != service.AssistantRefusalInvalidState {
			t.Fatal("cancel of a finished migrated run accepted")
		}
		if refusalCode(translateLegacyAssistantApproval(accounting, true, domain.AssistantApprovalRequest{SessionID: "s-accounting", PlanHash: accountingHash, Decision: "approve"}, "legacy-request")) != service.AssistantRefusalStaleApproval {
			t.Fatal("approval accepted for a migrated accounting-only run")
		}
	})
	t.Run("v2-native runs require the v2 contracts", func(t *testing.T) {
		for _, decision := range []string{"approve", "reject", "cancel"} {
			if code := refusalCode(translateLegacyAssistantApproval(native, true, domain.AssistantApprovalRequest{SessionID: "s-native", PlanHash: "native-hash", Decision: decision}, "legacy-request")); code != service.AssistantRefusalContractUpgradeRequired {
				t.Fatalf("%s on v2-native run = %q", decision, code)
			}
		}
		if code := refusalCode(translateLegacyAssistantApproval(domain.AssistantExecution{}, false, domain.AssistantApprovalRequest{SessionID: "s-none", PlanHash: "h", Decision: "approve"}, "legacy-request")); code != service.AssistantRefusalUnknownSession {
			t.Fatalf("unknown session = %q", code)
		}
	})
}

// ---------------------------------------------------------------------------
// B. The same rules end to end: handler -> orchestrator -> unified executor.

type memoryCheckpointStore struct {
	mu      sync.Mutex
	nextID  int
	heads   map[string]service.AssistantCheckpointHead
	changed chan struct{}
}

func newMemoryCheckpointStore() *memoryCheckpointStore {
	return &memoryCheckpointStore{heads: map[string]service.AssistantCheckpointHead{}, changed: make(chan struct{})}
}

func (s *memoryCheckpointStore) Append(_ context.Context, x domain.AssistantExecution, previous string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := x.SessionID + "\x00" + x.RunID
	head := s.heads[key]
	if head.EventID != previous {
		return "", errors.New("checkpoint predecessor mismatch")
	}
	s.nextID++
	id := fmt.Sprintf("%064x", s.nextID)
	s.heads[key] = service.AssistantCheckpointHead{Execution: x, EventID: id, Chain: append(append([]string(nil), head.Chain...), id)}
	close(s.changed)
	s.changed = make(chan struct{})
	return id, nil
}

func (s *memoryCheckpointStore) Load(_ context.Context, sessionID, runID string) (service.AssistantCheckpointHead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head, ok := s.heads[sessionID+"\x00"+runID]
	if !ok {
		return service.AssistantCheckpointHead{}, service.ErrAssistantCheckpointNotFound
	}
	return head, nil
}

func (s *memoryCheckpointStore) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		ch := s.changed
		s.mu.Unlock()
		if cond() {
			return
		}
		select {
		case <-ch:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

type handlerTestRegistry struct{}

func (handlerTestRegistry) GetAgentTool(name string) (service.AssistantToolRuntimeToolDescriptor, bool) {
	switch name {
	case "read-one":
		return service.AssistantToolRuntimeToolDescriptor{Name: name, ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object"}}, true
	case "mutate":
		return service.AssistantToolRuntimeToolDescriptor{Name: name, ExecutionMode: domain.AssistantToolExecutionModeAsync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: map[string]any{"type": "object"}}, true
	}
	return service.AssistantToolRuntimeToolDescriptor{}, false
}

type handlerTestTools struct {
	mu    sync.Mutex
	calls []string
}

func (s *handlerTestTools) CallTool(_ context.Context, name string, _ map[string]interface{}) (*service.AssistantToolRuntimeToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	return &service.AssistantToolRuntimeToolResult{Content: []service.AssistantToolRuntimeToolContent{{Type: "text", Text: `{"ok":true}`}}}, nil
}

func (s *handlerTestTools) InvokeAssistantAsyncTool(_ context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	key, _ := args["idempotency_key"].(string)
	return &domain.AsyncToolReceipt{ToolName: name, RequestEventID: "request:" + key, RequestKind: 25910, ResultKinds: []int{7961}, IdempotencyKey: key}, nil
}

func (s *handlerTestTools) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// blockingObserver never reports a terminal result; accounting stays open.
type blockingObserver struct{}

func (blockingObserver) ObserveWork(ctx context.Context, _ service.AssistantWorkObservationRequest) (service.AssistantAsyncObservationOutcome, error) {
	<-ctx.Done()
	return service.AssistantAsyncObservationOutcome{}, ctx.Err()
}

// emptyRelaySubscriber completes every backfill with EOSE and no events, so
// the orchestrator's scoped lookup proves a caller-supplied session is new.
type emptyRelaySubscriber struct{}

func (emptyRelaySubscriber) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (service.AssistantMergedSubscription, error) {
	eose := make(chan struct{})
	close(eose)
	return emptyRelaySubscription{events: make(chan *nostr.Event), closed: make(chan service.AssistantRelayClosed), eose: eose}, nil
}

type emptyRelaySubscription struct {
	events chan *nostr.Event
	closed chan service.AssistantRelayClosed
	eose   chan struct{}
}

func (s emptyRelaySubscription) EventChan() <-chan *nostr.Event                  { return s.events }
func (s emptyRelaySubscription) ClosedChan() <-chan service.AssistantRelayClosed { return s.closed }
func (s emptyRelaySubscription) EOSEChan() <-chan struct{}                       { return s.eose }
func (s emptyRelaySubscription) Close()                                          {}

type nativeBatchProposer struct{}

func (nativeBatchProposer) ProposeBatch(context.Context, service.AssistantProposalRequest) (service.AssistantProposal, error) {
	plan := legacyPlan()
	return service.AssistantProposal{Kind: service.AssistantProposalBatch, Batch: &plan}, nil
}

type handlerFixture struct {
	store   *memoryCheckpointStore
	tools   *handlerTestTools
	engine  *service.AssistantExecutionEngine
	adapter assistantContextVMAdapter
	seq     int
}

func newHandlerFixture(t *testing.T, migrated ...domain.AssistantExecution) *handlerFixture {
	t.Helper()
	f := &handlerFixture{store: newMemoryCheckpointStore(), tools: &handlerTestTools{}}
	runtime := service.NewAssistantToolRuntime(service.AssistantToolRuntimeConfig{MCPServer: f.tools, Registry: handlerTestRegistry{}, Permissions: service.NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeAudited}, nil)})
	lifecycle, cancel := context.WithCancel(context.Background())
	f.engine = service.NewAssistantExecutionEngine(service.AssistantExecutionEngineConfig{Store: f.store, Runtime: runtime, Observer: blockingObserver{}, Batch: nativeBatchProposer{}, Lifecycle: lifecycle})
	t.Cleanup(func() { cancel(); f.engine.Wait() })
	for _, x := range migrated {
		if _, err := f.store.Append(context.Background(), x, ""); err != nil {
			t.Fatal(err)
		}
		f.engine.HydrateProjection(domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: x.SessionID, OperatorPubkey: legacyOperatorPubkey().Hex(), CurrentRunID: x.RunID, Workflow: x.Workflow}, 0)
		if err := f.engine.Recover(context.Background(), service.AssistantExecutionReference{SessionID: x.SessionID, RunID: x.RunID, LegacySourceEventID: x.Migration.SourceEventID}); err != nil {
			t.Fatal(err)
		}
	}
	f.adapter = assistantContextVMAdapter{orchestrator: service.NewAssistantOrchestrator(service.AssistantOrchestratorConfig{Engine: f.engine, DefaultWorkflow: domain.AssistantWorkflowBatch, Subscriber: emptyRelaySubscriber{}, Signer: keyer.NewPlainKeySigner([32]byte(nostr.Generate()))})}
	return f
}

func (f *handlerFixture) request(t *testing.T, params any, eventLabel string) ContextVMRequest {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if eventLabel == "" {
		f.seq++
		eventLabel = fmt.Sprintf("event-%d", f.seq)
	}
	return ContextVMRequest{Event: &nostr.Event{ID: nostr.ID(sha256.Sum256([]byte(eventLabel))), PubKey: legacyOperatorPubkey(), Kind: 25910}, RPC: ContextVMJSONRPCRequest{JSONRPC: "2.0", Method: domain.AssistantContextVMMethodApproval, Params: raw}}
}

func mustResult(t *testing.T) func(any, error) service.AssistantOperationResult {
	return func(out any, err error) service.AssistantOperationResult {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		res, ok := out.(service.AssistantOperationResult)
		if !ok {
			t.Fatalf("handler result type %T", out)
		}
		return res
	}
}

func (f *handlerFixture) snapshot(t *testing.T, sessionID string) domain.AssistantExecution {
	t.Helper()
	x, ok := f.engine.Snapshot(sessionID)
	if !ok {
		t.Fatalf("no execution for %s", sessionID)
	}
	return x
}

func TestAssistantHandlerLegacyBatchApprovalRunsThroughExecutor(t *testing.T) {
	draft, legacyHash := legacyBatchDraft(t, "s-migrated")
	f := newHandlerFixture(t, draft)
	before := f.snapshot(t, "s-migrated")

	// Edited plan from an old client: refused, state untouched, nothing runs.
	edited := legacyPlan()
	edited.Steps[0].ToolArgs = map[string]any{"q": "changed"}
	res := mustResult(t)(f.adapter.handleApproval(context.Background(), f.request(t, map[string]any{"session_id": "s-migrated", "plan_hash": legacyHash, "decision": "approve", "modified_plan": edited}, "")))
	if res["status"] != "failed" || res["step"] != service.AssistantRefusalContractUpgradeRequired {
		t.Fatalf("edited old-client approval = %#v", res)
	}
	if after := f.snapshot(t, "s-migrated"); after.Revision != before.Revision || after.Phase != domain.AssistantExecutionAwaitingApproval || f.tools.count() != 0 {
		t.Fatalf("refused edit touched state: rev %d->%d phase=%s calls=%d", before.Revision, after.Revision, after.Phase, f.tools.count())
	}
	// A wrong legacy hash is stale.
	res = mustResult(t)(f.adapter.handleApproval(context.Background(), f.request(t, map[string]any{"session_id": "s-migrated", "plan_hash": strings.Repeat("0", 64), "decision": "approve"}, "")))
	if res["step"] != service.AssistantRefusalStaleApproval || f.tools.count() != 0 {
		t.Fatalf("wrong legacy hash = %#v", res)
	}

	// The unedited legacy approval is converted to a revision-bound v2
	// decision and executes exactly the migrated draft once.
	approve := f.request(t, map[string]any{"session_id": "s-migrated", "plan_hash": legacyHash, "decision": "approve"}, "legacy-approve")
	res = mustResult(t)(f.adapter.handleApproval(context.Background(), approve))
	if res["status"] != "accepted" {
		t.Fatalf("legacy approval refused: %#v", res)
	}
	f.store.waitFor(t, "migrated draft completes", func() bool { return f.snapshot(t, "s-migrated").Phase == domain.AssistantExecutionCompleted })
	res = mustResult(t)(f.adapter.handleApproval(context.Background(), approve))
	if res["status"] != "accepted" || res["step"] != "duplicate_decision" || f.tools.count() != 1 {
		t.Fatalf("redelivered legacy approval = %#v calls=%d", res, f.tools.count())
	}
	x := f.snapshot(t, "s-migrated")
	if len(x.Work) != 1 || x.Work[0].Authorization == nil || x.Work[0].Authorization.ProposalRevision != 1 || x.Work[0].State != domain.AssistantWorkSucceeded {
		t.Fatalf("migrated approval binding = %+v", x.Work)
	}
}

func TestAssistantHandlerLegacyActionAndCancelRules(t *testing.T) {
	action := legacyIterativeAction(t, "s-migrated-action")
	accounting, accountingHash := legacyBatchAccounting(t, "s-migrated-accounting")
	f := newHandlerFixture(t, action, accounting)

	res := mustResult(t)(f.adapter.handleApproval(context.Background(), f.request(t, map[string]any{"session_id": "s-migrated-action", "action_id": "not-the-action", "decision": "approve"}, "")))
	if res["step"] != service.AssistantRefusalStaleApproval || f.tools.count() != 0 {
		t.Fatalf("foreign action id = %#v", res)
	}
	res = mustResult(t)(f.adapter.handleApproval(context.Background(), f.request(t, map[string]any{"session_id": "s-migrated-action", "action_id": "action-1", "decision": "approve"}, "")))
	if res["status"] != "accepted" {
		t.Fatalf("migrated action approval refused: %#v", res)
	}
	f.store.waitFor(t, "approved action executes", func() bool {
		x := f.snapshot(t, "s-migrated-action")
		return len(x.Work) == 1 && x.Work[0].State == domain.AssistantWorkSucceeded
	})
	if x := f.snapshot(t, "s-migrated-action"); x.Work[0].IdempotencyKey != "assistant-agent:s-migrated-action:run-legacy:call-1" || f.tools.count() != 1 {
		t.Fatalf("migrated action dispatch key=%q calls=%d", x.Work[0].IdempotencyKey, f.tools.count())
	}

	// Plan-hash cancel of the migrated current batch run becomes a run
	// cancellation; accounting for its submitted effect stays open.
	res = mustResult(t)(f.adapter.handleApproval(context.Background(), f.request(t, map[string]any{"session_id": "s-migrated-accounting", "plan_hash": accountingHash, "decision": "cancel", "reason": "operator stop"}, "")))
	if res["status"] != "accepted" || res["phase"] != string(domain.AssistantExecutionCancelling) {
		t.Fatalf("legacy cancel = %#v", res)
	}
	if x := f.snapshot(t, "s-migrated-accounting"); x.Cancellation == nil || x.Cancellation.Scope != "run" || x.Cancellation.Reason != "operator stop" {
		t.Fatalf("cancellation not recorded: %+v", x.Cancellation)
	}
}

func TestAssistantHandlerNativeRunsRequireV2Contracts(t *testing.T) {
	f := newHandlerFixture(t)
	source := service.AssistantRequestSource{Event: &nostr.Event{ID: nostr.ID(sha256.Sum256([]byte("prompt"))), PubKey: legacyOperatorPubkey(), Kind: 25910}}
	started, err := f.adapter.orchestrator.HandlePromptRequest(context.Background(), source, domain.AssistantPromptRequest{ContractVersion: 2, SessionID: "s-native", TurnID: "t", Prompt: "plan"})
	if err != nil || started["status"] != "accepted" {
		t.Fatalf("native prompt = %#v err=%v", started, err)
	}
	// The proposal settles asynchronously after the accepted turn start.
	f.store.waitFor(t, "draft proposed", func() bool { return f.snapshot(t, "s-native").Phase == domain.AssistantExecutionAwaitingApproval })
	draft := f.snapshot(t, "s-native")
	legacyHash := domain.ComputePlanHash(draft.Proposal.Plan, "s-native")
	for _, params := range []map[string]any{
		{"session_id": "s-native", "plan_hash": legacyHash, "decision": "approve"},
		{"session_id": "s-native", "plan_hash": draft.Proposal.Hash, "decision": "approve"},
		{"session_id": "s-native", "plan_hash": draft.Proposal.Hash, "decision": "cancel"},
	} {
		res := mustResult(t)(f.adapter.handleApproval(context.Background(), f.request(t, params, "")))
		if res["status"] != "failed" || res["step"] != service.AssistantRefusalContractUpgradeRequired {
			t.Fatalf("unversioned request on native run = %#v", res)
		}
	}
	// v2 structural checks happen before the executor: unknown plan fields
	// and a v2 "cancel" decision are refused.
	v2 := map[string]any{"contract_version": 2, "session_id": "s-native", "run_id": draft.RunID, "workflow": "batch", "proposal_id": draft.Proposal.ProposalID, "base_revision": 1, "base_plan_hash": draft.Proposal.Hash, "approved_revision": 2, "approved_plan_hash": draft.Proposal.Hash, "decision": "approve",
		"modified_plan": map[string]any{"summary": "x", "needs_clarification": false, "risk_level": "low", "steps": []any{}, "unexpected": true}}
	if res := mustResult(t)(f.adapter.handleApproval(context.Background(), f.request(t, v2, ""))); res["step"] != service.AssistantRefusalPlanValidation {
		t.Fatalf("unknown modified_plan field = %#v", res)
	}
	v2["decision"] = "cancel"
	delete(v2, "modified_plan")
	if res := mustResult(t)(f.adapter.handleApproval(context.Background(), f.request(t, v2, ""))); res["step"] != service.AssistantRefusalValidation {
		t.Fatalf("v2 cancel decision = %#v", res)
	}
	if x := f.snapshot(t, "s-native"); x.Revision != draft.Revision || f.tools.count() != 0 {
		t.Fatal("refused requests changed state or dispatched")
	}
	cancel := ContextVMRequest{Event: &nostr.Event{ID: nostr.ID(sha256.Sum256([]byte("cancel"))), PubKey: legacyOperatorPubkey(), Kind: 25910}, RPC: ContextVMJSONRPCRequest{Params: json.RawMessage(fmt.Sprintf(`{"contract_version":2,"session_id":"s-native","run_id":%q,"scope":"run"}`, draft.RunID))}}
	res := mustResult(t)(f.adapter.handleCancel(context.Background(), cancel))
	if res["status"] != "accepted" || res["phase"] != string(domain.AssistantExecutionCancelled) {
		t.Fatalf("assistant/cancel = %#v", res)
	}
}
