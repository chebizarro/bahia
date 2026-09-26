package service

import (
	"encoding/json"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestClassifyAssistantLegacySessionMatrix(t *testing.T) {
	plan := domain.AssistantPlan{Summary: "two steps", RiskLevel: "medium", Steps: []domain.AssistantPlanStep{{StepID: "first", ToolName: "tool.same", ToolArgs: map[string]any{"nested": map[string]any{"x": 1}}}, {StepID: "second", ToolName: "tool.same", ToolArgs: map[string]any{}}}}
	base := func() domain.AssistantSession {
		encoded, _ := json.Marshal(plan)
		var p domain.AssistantPlan
		_ = json.Unmarshal(encoded, &p)
		return domain.AssistantSession{SessionID: "session", OperatorPubkey: "operator", CurrentTurnID: "turn", CurrentRequestID: "request", State: domain.AssistantSessionStateAwaitingApproval, CurrentPlan: &p, LastPlanHash: domain.ComputePlanHash(p, "session")}
	}
	batchKey := func(step string) string {
		return "assistant:session:" + domain.ComputePlanHash(plan, "session") + ":" + step
	}
	receipt := func(tool, key string) domain.AsyncToolReceipt {
		return domain.AsyncToolReceipt{ToolName: tool, IdempotencyKey: key, RequestEventID: "request-event", RequestKind: 25910, ResultKinds: []int{4902}}
	}
	loop := func(state domain.AssistantAgentLoopState) domain.AssistantAgentLoopMetadata {
		return domain.AssistantAgentLoopMetadata{RunID: "run", State: state, PendingToolCallID: "call", AllowedTools: []string{}}
	}
	deferred := domain.AssistantDeferredAction{ActionID: "action", SessionID: "session", RunID: "run", TurnID: "turn", ToolCallID: "call", ToolName: "tool.same", ToolArgs: map[string]any{"nested": map[string]any{"x": 1}}}
	iterative := func(state domain.AssistantAgentLoopState) domain.AssistantSession {
		return domain.AssistantSession{SessionID: "session", OperatorPubkey: "operator", CurrentTurnID: "turn", CurrentRequestID: "request", State: domain.AssistantSessionStateAwaitingApproval, Metadata: map[string]any{"agent_loop": loop(state)}}
	}
	cases := []struct {
		name                      string
		session                   domain.AssistantSession
		transcript                AssistantLegacyTranscriptEvidence
		want                      AssistantLegacyClassification
		approval, observe, reason bool
		workState                 domain.AssistantWorkState
	}{
		{name: "terminal clean", session: domain.AssistantSession{SessionID: "session", OperatorPubkey: "operator", State: domain.AssistantSessionStateCompleted}, want: AssistantLegacyReadOnly},
		{name: "terminal unresolved", session: func() domain.AssistantSession {
			s := base()
			s.State = domain.AssistantSessionStateCompleted
			s.PendingSteps = []domain.AssistantPlanStep{{StepID: "first", ToolName: "tool.same", IdempotencyKey: batchKey("first")}}
			return s
		}(), want: AssistantLegacyTerminalAccounting},
		{name: "batch draft", session: base(), want: AssistantLegacyBatchDraft, approval: true},
		{name: "batch hash mismatch", session: func() domain.AssistantSession { s := base(); s.LastPlanHash = "wrong"; return s }(), want: AssistantLegacyParked},
		{name: "batch single field", session: domain.AssistantSession{SessionID: "session", OperatorPubkey: "operator", State: domain.AssistantSessionStateAwaitingApproval, LastPlanHash: "hash"}, want: AssistantLegacyParked},
		{name: "batch dispatch key while draft", session: func() domain.AssistantSession { s := base(); s.CurrentPlan.Steps[0].IdempotencyKey = "key"; return s }(), want: AssistantLegacyParked},
		{name: "batch correlated receipt", session: func() domain.AssistantSession {
			s := base()
			s.State = domain.AssistantSessionStateExecuting
			s.PendingSteps = []domain.AssistantPlanStep{{StepID: "first", ToolName: "tool.same", IdempotencyKey: batchKey("first")}}
			s.Metadata = map[string]any{"pending_receipts": map[string]any{batchKey("first"): receipt("tool.same", batchKey("first"))}}
			return s
		}(), want: AssistantLegacyBatchAccounting, observe: true, workState: domain.AssistantWorkWaitingAsync},
		{name: "batch unreceipted dispatch", session: func() domain.AssistantSession {
			s := base()
			s.State = domain.AssistantSessionStateExecuting
			s.PendingSteps = []domain.AssistantPlanStep{{StepID: "first", ToolName: "tool.same", IdempotencyKey: batchKey("first")}}
			return s
		}(), want: AssistantLegacyBatchAccounting, workState: domain.AssistantWorkUncertain},
		{name: "no same-tool receipt fallback", session: func() domain.AssistantSession {
			s := base()
			s.State = domain.AssistantSessionStateExecuting
			s.PendingSteps = []domain.AssistantPlanStep{{StepID: "first", ToolName: "tool.same", IdempotencyKey: batchKey("first")}}
			s.Metadata = map[string]any{"pending_receipts": map[string]any{batchKey("second"): receipt("tool.same", batchKey("second"))}}
			return s
		}(), want: AssistantLegacyParked},
		{name: "batch missing turn identity", session: func() domain.AssistantSession { s := base(); s.CurrentTurnID = ""; return s }(), want: AssistantLegacyParked},
		{name: "iterative deferred action", session: func() domain.AssistantSession {
			s := iterative(domain.AssistantAgentLoopStateAwaitingApproval)
			m := loop(domain.AssistantAgentLoopStateAwaitingApproval)
			m.PendingActionID = "action"
			s.Metadata = map[string]any{"agent_loop": m, "deferred_actions": map[string]any{"action": deferred}}
			return s
		}(), want: AssistantLegacyIterativeAction, approval: true, workState: domain.AssistantWorkAwaitingApproval},
		{name: "iterative mismatched action", session: func() domain.AssistantSession {
			s := iterative(domain.AssistantAgentLoopStateAwaitingApproval)
			m := loop(domain.AssistantAgentLoopStateAwaitingApproval)
			m.PendingActionID = "action"
			bad := deferred
			bad.ToolCallID = "other"
			s.Metadata = map[string]any{"agent_loop": m, "deferred_actions": map[string]any{"action": bad}}
			return s
		}(), want: AssistantLegacyParked},
		{name: "iterative waiting no transcript", session: func() domain.AssistantSession {
			s := iterative(domain.AssistantAgentLoopStateWaitingAsync)
			s.State = domain.AssistantSessionStateExecuting
			m := loop(domain.AssistantAgentLoopStateWaitingAsync)
			r := receipt("tool.same", "assistant-agent:session:run:call")
			m.WaitingReceipt = &r
			s.Metadata = map[string]any{"agent_loop": m}
			return s
		}(), want: AssistantLegacyIterativeAccounting, observe: true, workState: domain.AssistantWorkWaitingAsync},
		{name: "iterative waiting complete transcript", session: func() domain.AssistantSession {
			s := iterative(domain.AssistantAgentLoopStateWaitingAsync)
			s.State = domain.AssistantSessionStateExecuting
			m := loop(domain.AssistantAgentLoopStateWaitingAsync)
			r := receipt("tool.same", "assistant-agent:session:run:call")
			m.WaitingReceipt = &r
			s.Metadata = map[string]any{"agent_loop": m}
			return s
		}(), transcript: AssistantLegacyTranscriptEvidence{RunID: "run", TurnID: "turn", ModelResponseID: "response", CompleteCallSequence: true, Calls: []domain.AssistantAgentToolCall{{ID: "call", Name: "tool.same", Arguments: map[string]any{}}}, Observations: map[string]domain.AssistantToolObservation{}}, want: AssistantLegacyIterativeAccounting, observe: true, reason: true, workState: domain.AssistantWorkWaitingAsync},
		{name: "iterative running", session: func() domain.AssistantSession {
			s := iterative(domain.AssistantAgentLoopStateRunning)
			s.State = domain.AssistantSessionStateExecuting
			return s
		}(), want: AssistantLegacyParked},
		{name: "active mixed", session: func() domain.AssistantSession {
			s := base()
			s.Metadata = map[string]any{"agent_loop": loop(domain.AssistantAgentLoopStateRunning)}
			return s
		}(), want: AssistantLegacyParked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.session)
			if err != nil {
				t.Fatal(err)
			}
			source := AssistantLegacySessionSource{EventID: "source-event", Schema: domain.AssistantSessionSchema, JSON: b, Transcript: tc.transcript}
			got := ClassifyAssistantLegacySession(source)
			if got.Classification != tc.want || got.CanAcceptApproval != tc.approval || got.CanObserveReceipts != tc.observe || got.CanContinueReasoning != tc.reason {
				t.Fatalf("got %#v, want class=%s approval=%v observe=%v reason=%v", got, tc.want, tc.approval, tc.observe, tc.reason)
			}
			if tc.workState != "" {
				if got.Execution == nil || len(got.Execution.Work) != 1 || got.Execution.Work[0].State != tc.workState {
					t.Fatalf("work state: %#v", got.Execution)
				}
			}
			if got.Execution != nil {
				again := ClassifyAssistantLegacySession(source)
				a, _ := json.Marshal(got.Execution)
				b, _ := json.Marshal(again.Execution)
				if string(a) != string(b) {
					t.Fatal("conversion not deterministic")
				}
			}
		})
	}
}

func TestClassifyAssistantLegacySessionRejectsSourceMismatch(t *testing.T) {
	for _, source := range []AssistantLegacySessionSource{{EventID: "", Schema: domain.AssistantSessionSchema, JSON: []byte(`{}`)}, {EventID: "id", Schema: domain.AssistantSessionSchemaV2, JSON: []byte(`{}`)}, {EventID: "id", Schema: domain.AssistantSessionSchema, JSON: []byte(`broken`)}} {
		if got := ClassifyAssistantLegacySession(source); got.Classification != AssistantLegacyParked || got.Execution != nil {
			t.Fatalf("unsafe source accepted: %#v", got)
		}
	}
}

func TestClassifyAssistantLegacyIterativeSequenceKeepsTail(t *testing.T) {
	waiting := domain.AsyncToolReceipt{ToolName: "tool.b", RequestEventID: "event", RequestKind: 25910, ResultKinds: []int{4902}, IdempotencyKey: "assistant-agent:session:run:b"}
	session := domain.AssistantSession{SessionID: "session", OperatorPubkey: "operator", CurrentTurnID: "turn", CurrentRequestID: "request", State: domain.AssistantSessionStateExecuting, Metadata: map[string]any{"agent_loop": domain.AssistantAgentLoopMetadata{RunID: "run", State: domain.AssistantAgentLoopStateWaitingAsync, PendingToolCallID: "b", WaitingReceipt: &waiting, AllowedTools: []string{"tool.a", "tool.b", "tool.c"}}}}
	encoded, _ := json.Marshal(session)
	evidence := AssistantLegacyTranscriptEvidence{RunID: "run", TurnID: "turn", ModelResponseID: "response", CompleteCallSequence: true, Calls: []domain.AssistantAgentToolCall{{ID: "a", Name: "tool.a", Arguments: map[string]any{}}, {ID: "b", Name: "tool.b", Arguments: map[string]any{}}, {ID: "c", Name: "tool.c", Arguments: map[string]any{"nested": map[string]any{"value": "before"}}}}, Observations: map[string]domain.AssistantToolObservation{"a": {ObservationID: "obs-a", ToolCallID: "a", ToolName: "tool.a", Status: domain.AssistantToolObservationSucceeded}}}
	result := ClassifyAssistantLegacySession(AssistantLegacySessionSource{EventID: "event-source", Schema: domain.AssistantSessionSchema, JSON: encoded, Transcript: evidence})
	if !result.CanContinueReasoning || result.Execution == nil || len(result.Execution.Work) != 3 || result.Execution.Cursor != 1 || result.Execution.Work[0].State != domain.AssistantWorkSucceeded || result.Execution.Work[1].State != domain.AssistantWorkWaitingAsync || result.Execution.Work[2].State != domain.AssistantWorkPending {
		t.Fatalf("sequence reconstruction failed: %#v", result)
	}
	evidence.Calls[2].Arguments["nested"].(map[string]any)["value"] = "after"
	if result.Execution.Work[2].Arguments["nested"].(map[string]any)["value"] != "before" {
		t.Fatal("execution aliases transcript evidence")
	}
}

func TestClassifyAssistantLegacyBatchDraftKeepsSeparateHashes(t *testing.T) {
	plan := domain.AssistantPlan{Summary: "review", RiskLevel: "low", Steps: []domain.AssistantPlanStep{{StepID: "s", ToolName: "tool", ToolArgs: map[string]any{}, ArgsPreview: map[string]any{"presentation": true}}}}
	legacyHash := domain.ComputePlanHash(plan, "session")
	session := domain.AssistantSession{SessionID: "session", OperatorPubkey: "operator", CurrentTurnID: "turn", CurrentRequestID: "request", State: domain.AssistantSessionStateAwaitingApproval, CurrentPlan: &plan, LastPlanHash: legacyHash}
	encoded, _ := json.Marshal(session)
	result := ClassifyAssistantLegacySession(AssistantLegacySessionSource{EventID: "source", Schema: domain.AssistantSessionSchema, JSON: encoded})
	if result.Classification != AssistantLegacyBatchDraft || result.Execution == nil || result.Execution.Proposal == nil {
		t.Fatalf("draft not converted: %#v", result)
	}
	if result.Execution.Migration.LegacyPlanHash != legacyHash {
		t.Fatalf("legacy hash lost: %#v", result.Execution.Migration)
	}
	if result.Execution.Proposal.Hash == legacyHash {
		t.Fatal("v1 hash incorrectly reused as v2 approval hash")
	}
	if result.Execution.Proposal.Plan.Steps[0].ArgsPreview != nil {
		t.Fatal("preview survived executable normalization")
	}
	scope, _ := result.Execution.Scope.ApprovalScope()
	want, err := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: 2, SessionID: "session", RunID: result.Execution.RunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: result.Execution.Proposal.ProposalID, Revision: 1, Scope: scope, Plan: result.Execution.Proposal.Plan})
	if err != nil || result.Execution.Proposal.Hash != want {
		t.Fatalf("v2 hash mismatch: got %s, want %s, err %v", result.Execution.Proposal.Hash, want, err)
	}
}
