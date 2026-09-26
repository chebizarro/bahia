package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

type assistantMemoryCheckpointStore struct {
	mu           sync.Mutex
	x            domain.AssistantExecution
	id           string
	updates      chan domain.AssistantExecution
	failRevision uint64
}

func (s *assistantMemoryCheckpointStore) Append(_ context.Context, x domain.AssistantExecution, prev string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x.Revision == s.failRevision {
		return "", errors.New("checkpoint rejected")
	}
	if s.id != prev && s.id != "" {
		return "", errors.New("predecessor mismatch")
	}
	clone, err := x.Clone()
	if err != nil {
		return "", err
	}
	s.x = clone
	s.id = fmt.Sprintf("checkpoint-%d", x.Revision)
	if s.updates != nil {
		s.updates <- clone
	}
	return s.id, nil
}
func (s *assistantMemoryCheckpointStore) Load(_ context.Context, sessionID, runID string) (domain.AssistantExecution, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.x.SessionID != sessionID || s.x.RunID != runID {
		return domain.AssistantExecution{}, "", errors.New("no assistant checkpoint")
	}
	clone, err := s.x.Clone()
	return clone, s.id, err
}
func waitExecutionPhase(t *testing.T, ch <-chan domain.AssistantExecution, phase domain.AssistantExecutionPhase) domain.AssistantExecution {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case x := <-ch:
			if x.Phase == phase {
				return x
			}
		case <-deadline:
			t.Fatalf("did not reach %s", phase)
		}
	}
}

type assistantStaticBatchProposer struct{ plan domain.AssistantPlan }

func (p assistantStaticBatchProposer) ProposeBatch(context.Context, AssistantProposalRequest) (AssistantProposal, error) {
	return AssistantProposal{Kind: AssistantProposalBatch, Batch: &p.plan}, nil
}

type assistantBarrierObserver struct {
	started chan struct{}
	result  chan AssistantAsyncObservationOutcome
}

func (o *assistantBarrierObserver) ObserveAssistantAsyncResult(ctx context.Context, _, _, _ string, _ *domain.AsyncToolReceipt) (AssistantAsyncObservationOutcome, error) {
	o.started <- struct{}{}
	select {
	case v := <-o.result:
		return v, nil
	case <-ctx.Done():
		return AssistantAsyncObservationOutcome{Status: "blocked"}, ctx.Err()
	}
}

func assistantEngineRuntime(server *assistantRuntimeMCPServer) *AssistantToolRuntime {
	schema := map[string]any{"type": "object"}
	registry := assistantRuntimeRegistry{
		"read-one": {Name: "read-one", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: schema},
		"mutate":   {Name: "mutate", ExecutionMode: domain.AssistantToolExecutionModeAsync, Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: schema},
		"read-two": {Name: "read-two", ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: domain.AssistantToolEffectRead, DefaultRisk: domain.AssistantToolRiskLow, InputSchema: schema},
	}
	return NewAssistantToolRuntime(AssistantToolRuntimeConfig{MCPServer: server, Registry: registry, Permissions: NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeAudited}, nil)})
}

func TestAssistantExecutionBatchResumesSyncAsyncSyncOnce(t *testing.T) {
	store := &assistantMemoryCheckpointStore{updates: make(chan domain.AssistantExecution, 100)}
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{Content: []AssistantToolRuntimeToolContent{{Type: "text", Text: `{"ok":true}`}}}, asyncReceipt: assistantRuntimeReceipt("mutate", "request-async")}
	plan := domain.AssistantPlan{Summary: "three calls", Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}, {StepID: "two", ToolName: "mutate", ToolArgs: map[string]any{"zone": "example.test"}}, {StepID: "three", ToolName: "read-two", ToolArgs: map[string]any{}}}}
	oldCtx, stopOld := context.WithCancel(context.Background())
	defer stopOld()
	oldObs := &assistantBarrierObserver{started: make(chan struct{}, 1), result: make(chan AssistantAsyncObservationOutcome)}
	engine := NewAssistantExecutionEngine(AssistantExecutionEngineConfig{Store: store, Runtime: assistantEngineRuntime(server), Observer: oldObs, Batch: assistantStaticBatchProposer{plan: plan}, Lifecycle: oldCtx, NewID: func(prefix string) string { return prefix + "-1" }})
	start, err := engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s1", TurnID: "t1", Prompt: "do it"}, OperatorPubkey: "operator", RequestEventID: "prompt-event", DefaultWorkflow: domain.AssistantWorkflowBatch})
	if err != nil {
		t.Fatal(err)
	}
	p := start.Session.Proposal
	if p == nil {
		t.Fatal("proposal missing")
	}
	_, err = engine.Decide(context.Background(), AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s1", RunID: start.Session.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: "approve"}, OperatorPubkey: "operator", RequestEventID: "approval-event"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldObs.started:
	case <-time.After(5 * time.Second):
		t.Fatal("observer did not subscribe")
	}
	if server.callCount() != 1 || server.invokeCount() != 1 {
		t.Fatalf("before restart sync=%d async=%d", server.callCount(), server.invokeCount())
	}
	stopOld()
	waitExecutionPhase(t, store.updates, domain.AssistantExecutionBlocked)
	newObs := &assistantBarrierObserver{started: make(chan struct{}, 1), result: make(chan AssistantAsyncObservationOutcome, 1)}
	fresh := NewAssistantExecutionEngine(AssistantExecutionEngineConfig{Store: store, Runtime: assistantEngineRuntime(server), Observer: newObs, Lifecycle: context.Background()})
	if err = fresh.Recover(context.Background(), AssistantExecutionReference{SessionID: "s1", RunID: start.Session.CurrentRunID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-newObs.started:
	case <-time.After(5 * time.Second):
		t.Fatal("recovered observer did not subscribe")
	}
	if server.callCount() != 1 || server.invokeCount() != 1 {
		t.Fatal("recovery redispatched existing work")
	}
	newObs.result <- AssistantAsyncObservationOutcome{Status: "completed", Event: assistantSignedResultEvent(t, "result-recovered", 7961, "request-async", "completed")}
	done := waitExecutionPhase(t, store.updates, domain.AssistantExecutionCompleted)
	if done.Cursor != 3 || server.callCount() != 2 || server.invokeCount() != 1 {
		t.Fatalf("cursor=%d sync=%d async=%d", done.Cursor, server.callCount(), server.invokeCount())
	}
	if err = fresh.Recover(context.Background(), AssistantExecutionReference{SessionID: "s1", RunID: start.Session.CurrentRunID}); err != nil {
		t.Fatal(err)
	}
	if server.callCount() != 2 || server.invokeCount() != 1 {
		t.Fatal("terminal replay duplicated dispatch")
	}
}

func TestAssistantExecutionRejectedCheckpointPreventsDispatch(t *testing.T) {
	store := &assistantMemoryCheckpointStore{updates: make(chan domain.AssistantExecution, 20), failRevision: 3}
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{}}
	plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "one", ToolName: "read-one", ToolArgs: map[string]any{}}}}
	engine := NewAssistantExecutionEngine(AssistantExecutionEngineConfig{Store: store, Runtime: assistantEngineRuntime(server), Observer: &assistantBarrierObserver{started: make(chan struct{}, 1), result: make(chan AssistantAsyncObservationOutcome)}, Batch: assistantStaticBatchProposer{plan: plan}, NewID: func(prefix string) string { return prefix + "-1" }})
	start, err := engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s1", TurnID: "t1", Prompt: "do it"}, OperatorPubkey: "operator", RequestEventID: "prompt-event", DefaultWorkflow: domain.AssistantWorkflowBatch})
	if err != nil {
		t.Fatal(err)
	}
	p := start.Session.Proposal
	_, err = engine.Decide(context.Background(), AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s1", RunID: start.Session.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: 1, BasePlanHash: p.Hash, ApprovedRevision: 1, ApprovedPlanHash: p.Hash, Decision: "approve"}, OperatorPubkey: "operator", RequestEventID: "approval-event"})
	if err == nil {
		t.Fatal("checkpoint rejection was accepted")
	}
	if server.callCount() != 0 {
		t.Fatal("tool dispatched after checkpoint rejection")
	}
}

func TestAssistantExecutionScopeNilVersusEmpty(t *testing.T) {
	runtime := assistantEngineRuntime(&assistantRuntimeMCPServer{})
	work := domain.AssistantWorkItem{WorkID: "w", ToolName: "read-one", Arguments: map[string]any{}, State: domain.AssistantWorkReady}
	x := domain.AssistantExecution{Version: 2, SessionID: "s", RunID: "r", Workflow: domain.AssistantWorkflowIterative, Scope: domain.AssistantCommandScope{AllowedTools: []string{}}}
	if _, err := runtime.PrepareWork(context.Background(), x, work); err == nil {
		t.Fatal("empty scope allowed tool")
	}
	x.Scope.AllowedTools = nil
	if _, err := runtime.PrepareWork(context.Background(), x, work); err != nil {
		t.Fatalf("nil scope denied tool: %v", err)
	}
}

type assistantSequenceIterativeProposer struct {
	mu    sync.Mutex
	calls int
}

func (p *assistantSequenceIterativeProposer) ProposeIterative(context.Context, AssistantProposalRequest) (AssistantProposal, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return AssistantProposal{Kind: AssistantProposalCalls, Calls: []domain.AssistantAgentToolCall{{ID: "first", Name: "mutate", Arguments: map[string]any{}}, {ID: "tail", Name: "read-two", Arguments: map[string]any{}}}}, nil
	}
	return AssistantProposal{Kind: AssistantProposalFinal, Text: "done"}, nil
}
func (p *assistantSequenceIterativeProposer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestAssistantExecutionIterativeTailSurvivesAsyncSuspension(t *testing.T) {
	store := &assistantMemoryCheckpointStore{updates: make(chan domain.AssistantExecution, 100)}
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{}, asyncReceipt: assistantRuntimeReceipt("mutate", "request-tail")}
	observer := &assistantBarrierObserver{started: make(chan struct{}, 1), result: make(chan AssistantAsyncObservationOutcome, 1)}
	proposer := &assistantSequenceIterativeProposer{}
	engine := NewAssistantExecutionEngine(AssistantExecutionEngineConfig{Store: store, Runtime: assistantEngineRuntime(server), Observer: observer, Iterative: proposer, NewID: func(prefix string) string { return prefix + "-tail" }})
	start, err := engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-tail", TurnID: "turn", Prompt: "act"}, OperatorPubkey: "operator", RequestEventID: "prompt", DefaultWorkflow: domain.AssistantWorkflowIterative})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-observer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("observer not started")
	}
	if proposer.count() != 1 || server.callCount() != 0 {
		t.Fatal("tail executed before async observation")
	}
	observer.result <- AssistantAsyncObservationOutcome{Status: "completed", Event: assistantSignedResultEvent(t, "result-tail", 7961, "request-tail", "completed")}
	done := waitExecutionPhase(t, store.updates, domain.AssistantExecutionCompleted)
	if done.Cursor != 2 || len(done.Work) != 2 || server.callCount() != 1 || server.invokeCount() != 1 || proposer.count() != 2 {
		t.Fatalf("lost iterative tail: cursor=%d work=%d sync=%d async=%d proposals=%d", done.Cursor, len(done.Work), server.callCount(), server.invokeCount(), proposer.count())
	}
	_ = start
}

func TestAssistantExecutionCancellationAccountsSubmittedButSkipsSuccessor(t *testing.T) {
	store := &assistantMemoryCheckpointStore{updates: make(chan domain.AssistantExecution, 100)}
	server := &assistantRuntimeMCPServer{syncResult: &AssistantToolRuntimeToolResult{}, asyncReceipt: assistantRuntimeReceipt("mutate", "request-cancel")}
	observer := &assistantBarrierObserver{started: make(chan struct{}, 1), result: make(chan AssistantAsyncObservationOutcome, 1)}
	plan := domain.AssistantPlan{Steps: []domain.AssistantPlanStep{{StepID: "async", ToolName: "mutate", ToolArgs: map[string]any{}}, {StepID: "successor", ToolName: "read-two", ToolArgs: map[string]any{}}}}
	engine := NewAssistantExecutionEngine(AssistantExecutionEngineConfig{Store: store, Runtime: assistantEngineRuntime(server), Observer: observer, Batch: assistantStaticBatchProposer{plan: plan}, NewID: func(prefix string) string { return prefix + "-cancel" }})
	start, err := engine.StartTurn(context.Background(), AssistantTurnStartRequest{Prompt: domain.AssistantPromptRequest{SessionID: "s-cancel", TurnID: "turn", Prompt: "act"}, OperatorPubkey: "operator", RequestEventID: "prompt", DefaultWorkflow: domain.AssistantWorkflowBatch})
	if err != nil {
		t.Fatal(err)
	}
	p := start.Session.Proposal
	_, err = engine.Decide(context.Background(), AssistantTurnDecisionRequest{Approval: domain.AssistantApprovalRequest{ContractVersion: 2, SessionID: "s-cancel", RunID: start.Session.CurrentRunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: 1, BasePlanHash: p.Hash, ApprovedRevision: 1, ApprovedPlanHash: p.Hash, Decision: "approve"}, OperatorPubkey: "operator", RequestEventID: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-observer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("observer not started")
	}
	cancelled, err := engine.Cancel(context.Background(), AssistantTurnCancellationRequest{Cancellation: domain.AssistantCancellationRequest{ContractVersion: 2, SessionID: "s-cancel", RunID: start.Session.CurrentRunID, Scope: "run"}, OperatorPubkey: "operator", RequestEventID: "cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Session.Phase != domain.AssistantExecutionCancelling {
		t.Fatalf("phase=%s", cancelled.Session.Phase)
	}
	observer.result <- AssistantAsyncObservationOutcome{Status: "completed", Event: assistantSignedResultEvent(t, "result-cancel", 7961, "request-cancel", "completed")}
	done := waitExecutionPhase(t, store.updates, domain.AssistantExecutionCancelled)
	if done.Work[0].State != domain.AssistantWorkSucceeded || done.Work[1].State != domain.AssistantWorkSkipped || server.callCount() != 0 || server.invokeCount() != 1 {
		t.Fatalf("cancellation accounting wrong: %+v sync=%d async=%d", done.Work, server.callCount(), server.invokeCount())
	}
}
