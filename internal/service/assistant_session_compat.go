package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AssistantLegacyClassification is a read policy, not permission to dispatch.
type AssistantLegacyClassification string

const (
	AssistantLegacyReadOnly            AssistantLegacyClassification = "terminal_read_only"
	AssistantLegacyBatchDraft          AssistantLegacyClassification = "batch_draft"
	AssistantLegacyBatchAccounting     AssistantLegacyClassification = "batch_accounting"
	AssistantLegacyIterativeAction     AssistantLegacyClassification = "iterative_action"
	AssistantLegacyIterativeAccounting AssistantLegacyClassification = "iterative_accounting"
	AssistantLegacyParked              AssistantLegacyClassification = "parked"
	AssistantLegacyTerminalAccounting  AssistantLegacyClassification = "terminal_accounting"
)

type AssistantLegacyTranscriptEvidence struct {
	RunID                string
	TurnID               string
	ModelResponseID      string
	Calls                []domain.AssistantAgentToolCall
	Observations         map[string]domain.AssistantToolObservation // keyed by call ID
	CompleteCallSequence bool
}

type AssistantLegacySessionSource struct {
	EventID    string
	Schema     string
	JSON       []byte
	Transcript AssistantLegacyTranscriptEvidence
}

type AssistantLegacyConversion struct {
	Classification       AssistantLegacyClassification
	Reason               string
	Execution            *domain.AssistantExecution
	CanAcceptApproval    bool
	CanObserveReceipts   bool
	CanContinueReasoning bool
}

// ClassifyAssistantLegacySession is deterministic and side-effect-free. It
// never treats an uncorrelated receipt, a single field, or a tool-name match as
// authority. Callers must still checkpoint conversion before any side effect.
func ClassifyAssistantLegacySession(source AssistantLegacySessionSource) AssistantLegacyConversion {
	park := func(reason string) AssistantLegacyConversion {
		return AssistantLegacyConversion{Classification: AssistantLegacyParked, Reason: reason}
	}
	if source.Schema != domain.AssistantSessionSchema || source.EventID == "" {
		return park("missing v1 schema or source event identity")
	}
	var s domain.AssistantSession
	if len(source.JSON) == 0 || domain.ValidateAssistantIJSON(source.JSON) != nil || json.Unmarshal(source.JSON, &s) != nil {
		return park("malformed v1 session JSON")
	}
	if s.SessionID == "" || s.OperatorPubkey == "" {
		return park("missing session/operator identity")
	}
	if s.CurrentPlan != nil && (s.LastPlanHash == "" || domain.ComputePlanHash(*s.CurrentPlan, s.SessionID) != s.LastPlanHash) {
		return park("plan/hash mismatch")
	}
	loop, loopPresent, loopErr := legacyLoop(s.Metadata)
	if loopErr != nil {
		return park("malformed iterative metadata")
	}
	deferred, deferredErr := legacyDeferred(s.Metadata)
	if deferredErr != nil {
		return park("malformed deferred actions")
	}
	receipts, receiptsErr := legacyReceipts(s.Metadata)
	if receiptsErr != nil {
		return park("malformed pending receipts")
	}
	batchEvidence := s.CurrentPlan != nil || s.LastPlanHash != "" || len(s.PendingSteps) > 0 || len(receipts) > 0
	iterativeEvidence := loopPresent || len(deferred) > 0
	unresolved := len(s.PendingSteps) > 0 || len(receipts) > 0 ||
		(loopPresent && (loop.WaitingReceipt != nil || loop.PendingActionID != "")) || len(deferred) > 0
	if s.State == domain.AssistantSessionStateCompleted || s.State == domain.AssistantSessionStateFailed {
		if !unresolved {
			return AssistantLegacyConversion{Classification: AssistantLegacyReadOnly, Reason: "terminal v1 history is read-only"}
		}
		if batchEvidence && !iterativeEvidence && s.CurrentTurnID != "" && s.CurrentRequestID != "" {
			accounting := s
			accounting.State = domain.AssistantSessionStateBlocked
			converted := classifyLegacyBatch(source, accounting, receipts, park)
			if converted.Classification == AssistantLegacyBatchAccounting {
				converted.Classification = AssistantLegacyTerminalAccounting
				converted.Reason = "terminal outcome preserved; correlated effects remain accounting-only"
				converted.Execution.Migration.Classification = string(AssistantLegacyTerminalAccounting)
				converted.Execution.Migration.Reason = converted.Reason
				return converted
			}
		}
		if iterativeEvidence && !batchEvidence && s.CurrentTurnID != "" && s.CurrentRequestID != "" {
			accounting := s
			accounting.State = domain.AssistantSessionStateBlocked
			converted := classifyLegacyIterative(source, accounting, loop, deferred, park)
			if converted.Classification == AssistantLegacyIterativeAccounting {
				converted.Classification = AssistantLegacyTerminalAccounting
				converted.CanContinueReasoning = false
				converted.Reason = "terminal outcome preserved; correlated effects remain accounting-only"
				converted.Execution.Migration.Classification = string(AssistantLegacyTerminalAccounting)
				converted.Execution.Migration.Reason = converted.Reason
				return converted
			}
		}
		return AssistantLegacyConversion{Classification: AssistantLegacyTerminalAccounting, Reason: "terminal history has unresolved, uncorrelated effects/actions"}
	}
	if s.State == domain.AssistantSessionStateBlocked && !unresolved {
		return AssistantLegacyConversion{Classification: AssistantLegacyReadOnly, Reason: "blocked v1 history has no unresolved effects"}
	}
	if batchEvidence && iterativeEvidence {
		return park("active mixed batch and iterative state")
	}
	if !batchEvidence && !iterativeEvidence {
		return park("no reconstructable workflow identity")
	}
	if s.CurrentTurnID == "" || s.CurrentRequestID == "" {
		return park("missing turn or request identity")
	}
	if batchEvidence {
		return classifyLegacyBatch(source, s, receipts, park)
	}
	return classifyLegacyIterative(source, s, loop, deferred, park)
}

func classifyLegacyBatch(source AssistantLegacySessionSource, s domain.AssistantSession, receipts map[string]domain.AsyncToolReceipt, park func(string) AssistantLegacyConversion) AssistantLegacyConversion {
	if s.CurrentPlan == nil || s.LastPlanHash == "" {
		return park("batch plan or hash missing")
	}
	plan, err := domain.NormalizeAssistantExecutablePlan(*s.CurrentPlan)
	if err != nil {
		return park("invalid batch plan: " + err.Error())
	}
	runID := legacyRunID(source.EventID, s.SessionID, s.CurrentTurnID)
	e := domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: s.SessionID, RunID: runID, TurnID: s.CurrentTurnID, RequestID: s.CurrentRequestID, Workflow: domain.AssistantWorkflowBatch, Revision: 1,
		Migration: &domain.AssistantExecutionMigration{SourceSchema: source.Schema, SourceEventID: source.EventID, LegacyPlanHash: s.LastPlanHash}}
	if s.State == domain.AssistantSessionStateAwaitingApproval {
		if len(s.PendingSteps) != 0 || len(receipts) != 0 {
			return park("awaiting batch draft has dispatch evidence")
		}
		for _, step := range s.CurrentPlan.Steps {
			if step.IdempotencyKey != "" {
				return park("awaiting batch draft contains dispatch key")
			}
		}
		e.Phase = domain.AssistantExecutionAwaitingApproval
		proposalID := runID + ":draft"
		approvalScope, err := e.Scope.ApprovalScope()
		if err != nil {
			return park("invalid migrated batch scope")
		}
		v2Hash, err := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: domain.AssistantExecutionVersion, SessionID: s.SessionID, RunID: runID, Workflow: domain.AssistantWorkflowBatch, ProposalID: proposalID, Revision: 1, Scope: approvalScope, Plan: plan})
		if err != nil {
			return park("invalid migrated batch hash")
		}
		e.Proposal = &domain.AssistantProposalRevision{ProposalID: proposalID, Revision: 1, Plan: plan, Hash: v2Hash, RequestID: s.CurrentRequestID}
		e.Migration.Classification = string(AssistantLegacyBatchDraft)
		return AssistantLegacyConversion{Classification: AssistantLegacyBatchDraft, Execution: &e, CanAcceptApproval: true}
	}
	if s.State != domain.AssistantSessionStateExecuting && s.State != domain.AssistantSessionStateBlocked {
		return park("batch state is not resumable")
	}
	if len(s.PendingSteps) == 0 {
		return park("executing batch has no pending work evidence")
	}
	planIndex := map[string]int{}
	for i, step := range plan.Steps {
		planIndex[step.StepID] = i
	}
	used := map[string]bool{}
	lastOrdinal := -1
	e.Phase = domain.AssistantExecutionBlocked
	for _, step := range s.PendingSteps {
		ordinal, ok := planIndex[step.StepID]
		if !ok || ordinal <= lastOrdinal || step.ToolName != plan.Steps[ordinal].ToolName {
			return park("pending batch steps do not match ordered plan")
		}
		lastOrdinal = ordinal
		if step.IdempotencyKey != "" && step.IdempotencyKey != fmt.Sprintf("assistant:%s:%s:%s", s.SessionID, s.LastPlanHash, step.StepID) {
			return park("pending step dispatch key does not match legacy identity")
		}
		args, err := domain.DeepCopyAssistantJSONMap(plan.Steps[ordinal].ToolArgs)
		if err != nil {
			return park("invalid pending step arguments")
		}
		digest, err := domain.ComputeAssistantArgumentsDigest(args)
		if err != nil {
			return park("invalid pending step digest")
		}
		item := domain.AssistantWorkItem{WorkID: runID + ":" + step.StepID, OriginID: step.StepID, Ordinal: ordinal, ToolName: step.ToolName, Arguments: args, ArgumentsDigest: digest, IdempotencyKey: step.IdempotencyKey, State: domain.AssistantWorkUncertain}
		if step.IdempotencyKey != "" {
			if receipt, ok := receipts[step.IdempotencyKey]; ok {
				if !validLegacyReceipt(receipt, step.ToolName, step.IdempotencyKey) {
					return park("pending receipt identity or provenance mismatch")
				}
				copyReceipt := receipt
				item.Receipt = &copyReceipt
				item.State = domain.AssistantWorkWaitingAsync
				used[step.IdempotencyKey] = true
			}
		}
		e.Work = append(e.Work, item)
	}
	if len(used) != len(receipts) {
		return park("orphan or uncorrelated batch receipt")
	}
	e.Migration.Classification = string(AssistantLegacyBatchAccounting)
	e.Migration.Reason = "observe correlated receipts; park unreceipted and undispatched remainder for renewed review"
	return AssistantLegacyConversion{Classification: AssistantLegacyBatchAccounting, Reason: e.Migration.Reason, Execution: &e, CanObserveReceipts: len(used) > 0}
}

func classifyLegacyIterative(source AssistantLegacySessionSource, s domain.AssistantSession, loop domain.AssistantAgentLoopMetadata, deferred map[string]domain.AssistantDeferredAction, park func(string) AssistantLegacyConversion) AssistantLegacyConversion {
	if loop.RunID == "" || loop.State == "" {
		return park("missing iterative run/state identity")
	}
	e := domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: s.SessionID, RunID: loop.RunID, TurnID: s.CurrentTurnID, RequestID: s.CurrentRequestID, Workflow: domain.AssistantWorkflowIterative, Revision: 1,
		Scope: domain.AssistantCommandScope{AllowedTools: loop.AllowedTools}, Migration: &domain.AssistantExecutionMigration{SourceSchema: source.Schema, SourceEventID: source.EventID}}
	var err error
	e.Scope, err = e.Scope.Clone()
	if err != nil {
		return park("invalid iterative scope")
	}
	if loop.State == domain.AssistantAgentLoopStateAwaitingApproval && s.State == domain.AssistantSessionStateAwaitingApproval {
		if loop.WaitingReceipt != nil || loop.PendingActionID == "" || loop.PendingToolCallID == "" || len(deferred) != 1 {
			return park("iterative action identities conflict")
		}
		action, ok := deferred[loop.PendingActionID]
		if !ok || action.ActionID != loop.PendingActionID || action.SessionID != s.SessionID || action.RunID != loop.RunID || action.TurnID != s.CurrentTurnID || action.ToolCallID != loop.PendingToolCallID || action.ToolName == "" || action.ToolArgs == nil {
			return park("deferred action does not match pending run/turn/call")
		}
		args, err := domain.DeepCopyAssistantJSONMap(action.ToolArgs)
		if err != nil {
			return park("invalid deferred action arguments")
		}
		e.Phase = domain.AssistantExecutionAwaitingApproval
		digest, err := domain.ComputeAssistantArgumentsDigest(args)
		if err != nil {
			return park("invalid deferred action digest")
		}
		e.Work = []domain.AssistantWorkItem{{WorkID: action.ActionID, OriginID: action.ToolCallID, ToolName: action.ToolName, Arguments: args, ArgumentsDigest: digest, State: domain.AssistantWorkAwaitingApproval}}
		e.Migration.Classification = string(AssistantLegacyIterativeAction)
		return AssistantLegacyConversion{Classification: AssistantLegacyIterativeAction, Execution: &e, CanAcceptApproval: true}
	}
	if loop.State == domain.AssistantAgentLoopStateWaitingAsync && (s.State == domain.AssistantSessionStateExecuting || s.State == domain.AssistantSessionStateBlocked) {
		if len(deferred) != 0 || loop.PendingActionID != "" || loop.PendingToolCallID == "" || loop.WaitingReceipt == nil || !validLegacyReceipt(*loop.WaitingReceipt, loop.WaitingReceipt.ToolName, loop.WaitingReceipt.IdempotencyKey) {
			return park("iterative waiting receipt identity conflict")
		}
		if loop.WaitingReceipt.IdempotencyKey != fmt.Sprintf("assistant-agent:%s:%s:%s", s.SessionID, loop.RunID, loop.PendingToolCallID) {
			return park("iterative receipt does not bind run")
		}
		e.Phase = domain.AssistantExecutionBlocked
		copyReceipt := *loop.WaitingReceipt
		e.Work = []domain.AssistantWorkItem{{WorkID: loop.RunID + ":" + loop.PendingToolCallID, OriginID: loop.PendingToolCallID, ToolName: copyReceipt.ToolName, IdempotencyKey: copyReceipt.IdempotencyKey, State: domain.AssistantWorkWaitingAsync, Receipt: &copyReceipt}}
		e.Migration.Classification = string(AssistantLegacyIterativeAccounting)
		e.Migration.Reason = "observe receipt; reasoning requires complete transcript sequence"
		canReason := attachLegacyIterativeSequence(&e, source.Transcript, loop)
		return AssistantLegacyConversion{Classification: AssistantLegacyIterativeAccounting, Reason: e.Migration.Reason, Execution: &e, CanObserveReceipts: true, CanContinueReasoning: canReason}
	}
	return park("iterative running state lacks complete checkpoint")
}

// attachLegacyIterativeSequence only enables reasoning after evidence supplies
// the full ordered model response and matching consumed observations. A bool
// alone is not a reconstructed queue.
func attachLegacyIterativeSequence(e *domain.AssistantExecution, evidence AssistantLegacyTranscriptEvidence, loop domain.AssistantAgentLoopMetadata) bool {
	if evidence.RunID != e.RunID || evidence.TurnID != e.TurnID || evidence.ModelResponseID == "" || !evidence.CompleteCallSequence || len(evidence.Calls) == 0 {
		return false
	}
	seen := map[string]bool{}
	pendingAt := -1
	items := make([]domain.AssistantWorkItem, 0, len(evidence.Calls))
	for i, call := range evidence.Calls {
		if call.ID == "" || call.Name == "" || seen[call.ID] || call.Arguments == nil {
			return false
		}
		seen[call.ID] = true
		args, err := domain.DeepCopyAssistantJSONMap(call.Arguments)
		if err != nil {
			return false
		}
		digest, err := domain.ComputeAssistantArgumentsDigest(args)
		if err != nil {
			return false
		}
		item := domain.AssistantWorkItem{WorkID: e.RunID + ":" + call.ID, OriginID: call.ID, Ordinal: i, ToolName: call.Name, Arguments: args, ArgumentsDigest: digest, State: domain.AssistantWorkPending}
		if call.ID == loop.PendingToolCallID {
			if call.Name != loop.WaitingReceipt.ToolName {
				return false
			}
			pendingAt = i
			item.State = domain.AssistantWorkWaitingAsync
			copyReceipt := *loop.WaitingReceipt
			item.Receipt = &copyReceipt
			item.IdempotencyKey = copyReceipt.IdempotencyKey
		} else if observation, ok := evidence.Observations[call.ID]; ok {
			if observation.ToolCallID != call.ID || observation.ToolName != call.Name || observation.ObservationID == "" {
				return false
			}
			copyObservation := observation
			item.Observation = &copyObservation
			switch observation.Status {
			case domain.AssistantToolObservationSucceeded:
				item.State = domain.AssistantWorkSucceeded
			case domain.AssistantToolObservationFailed:
				item.State = domain.AssistantWorkFailed
			case domain.AssistantToolObservationDenied:
				item.State = domain.AssistantWorkDenied
			case domain.AssistantToolObservationCancelled:
				item.State = domain.AssistantWorkSkipped
			default:
				return false
			}
		}
		items = append(items, item)
	}
	if pendingAt < 0 || len(evidence.Observations) != pendingAt {
		return false
	}
	for i, item := range items {
		if i < pendingAt && item.Observation == nil {
			return false
		}
		if i > pendingAt && item.State != domain.AssistantWorkPending {
			return false
		}
	}
	e.Work = items
	e.Cursor = pendingAt
	cloned, err := e.Clone()
	if err != nil {
		return false
	}
	*e = cloned
	return true
}

func legacyLoop(metadata map[string]any) (domain.AssistantAgentLoopMetadata, bool, error) {
	raw, present := metadata[assistantAgentLoopMetadataKey]
	if !present {
		return domain.AssistantAgentLoopMetadata{}, false, nil
	}
	var loop domain.AssistantAgentLoopMetadata
	b, err := json.Marshal(raw)
	if err != nil {
		return loop, true, err
	}
	err = json.Unmarshal(b, &loop)
	return loop, true, err
}

func legacyDeferred(metadata map[string]any) (map[string]domain.AssistantDeferredAction, error) {
	out := map[string]domain.AssistantDeferredAction{}
	raw := metadata[assistantDeferredActionsMetadataKey]
	if raw == nil {
		return out, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	for key, action := range out {
		if key == "" || action.ActionID != key {
			return nil, fmt.Errorf("deferred action key mismatch")
		}
	}
	return out, nil
}

func legacyReceipts(metadata map[string]any) (map[string]domain.AsyncToolReceipt, error) {
	out := map[string]domain.AsyncToolReceipt{}
	raw := metadata["pending_receipts"]
	if raw == nil {
		return out, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	for key, receipt := range out {
		if !validLegacyReceipt(receipt, receipt.ToolName, key) {
			return nil, fmt.Errorf("receipt key mismatch")
		}
	}
	return out, nil
}

func validLegacyReceipt(r domain.AsyncToolReceipt, tool, key string) bool {
	return tool != "" && key != "" && r.ToolName == tool && r.IdempotencyKey == key && r.RequestEventID != "" && r.RequestKind > 0 && len(r.ResultKinds) > 0
}

func legacyRunID(eventID, sessionID, turnID string) string {
	sum := sha256.Sum256([]byte("assistant-v1-conversion\x00" + eventID + "\x00" + sessionID + "\x00" + turnID))
	return "migrated-" + hex.EncodeToString(sum[:16])
}
