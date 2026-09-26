package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// assistantOrchestrator is the request-routing surface the handlers need.
// *service.AssistantOrchestrator implements it.
type assistantOrchestrator interface {
	HandlePromptRequest(context.Context, service.AssistantRequestSource, domain.AssistantPromptRequest) (service.AssistantOperationResult, error)
	HandleApprovalRequest(context.Context, service.AssistantRequestSource, domain.AssistantApprovalRequest) (service.AssistantOperationResult, error)
	HandleCancellationRequest(context.Context, service.AssistantRequestSource, domain.AssistantCancellationRequest) (service.AssistantOperationResult, error)
	HandleReconciliationRequest(context.Context, service.AssistantRequestSource, domain.AssistantReconciliationRequest) (service.AssistantOperationResult, error)
	ExecutionSnapshot(sessionID string) (domain.AssistantExecution, bool)
	IsSessionParticipant(sessionID, operator string) bool
}

// RegisterAssistantContextVMHandlers registers the operator assistant mutation
// intents on the canonical ContextVM request transport. Every accepted request
// is routed to the unified executor; durable state is the executor's v2
// session projection and checkpoints. Refusals are successful ContextVM
// results shaped {status:"failed", step:<reason code>, summary, error}.
func RegisterAssistantContextVMHandlers(transport *EncryptedRequestTransport, orchestrator *service.AssistantOrchestrator, gate *FleetOperatorGate) {
	if transport == nil || orchestrator == nil {
		return
	}
	adapter := assistantContextVMAdapter{orchestrator: orchestrator}
	transport.RegisterOperatorContextVMHandler(domain.AssistantContextVMMethodPrompt, adapter.handlePrompt, gate)
	transport.RegisterOperatorContextVMHandler(domain.AssistantContextVMMethodApproval, adapter.handleApproval, gate)
	transport.RegisterOperatorContextVMHandler(domain.AssistantContextVMMethodCancel, adapter.handleCancel, gate)
	transport.RegisterOperatorContextVMHandler(domain.AssistantContextVMMethodReconcile, adapter.handleReconcile, gate)
}

type assistantContextVMAdapter struct {
	orchestrator assistantOrchestrator
}

func assistantRefusal(sessionID, code, message string) service.AssistantOperationResult {
	return service.AssistantOperationResult{"status": "failed", "step": code, "session_id": sessionID, "summary": message, "error": message}
}

func (a assistantContextVMAdapter) participant(sessionID string, request ContextVMRequest) bool {
	return request.Event != nil && a.orchestrator.IsSessionParticipant(sessionID, request.Event.PubKey.Hex())
}

func (a assistantContextVMAdapter) handlePrompt(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload domain.AssistantPromptRequest
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("invalid assistant prompt params: %w", err)
	}
	if strings.TrimSpace(payload.SessionID) == "" || strings.TrimSpace(payload.TurnID) == "" || strings.TrimSpace(payload.Prompt) == "" {
		return assistantRefusal(payload.SessionID, service.AssistantRefusalValidation, "prompt request requires session_id, turn_id, and prompt"), nil
	}
	if !a.participant(payload.SessionID, request) {
		return assistantRefusal(payload.SessionID, service.AssistantRefusalUnauthorized, "requester is not a participant in this assistant session"), nil
	}
	return a.orchestrator.HandlePromptRequest(ctx, assistantSourceFromContextVM(request), payload)
}

func (a assistantContextVMAdapter) handleApproval(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload domain.AssistantApprovalRequest
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("invalid assistant approval params: %w", err)
	}
	payload.Decision = strings.ToLower(strings.TrimSpace(payload.Decision))
	if strings.TrimSpace(payload.SessionID) == "" {
		return assistantRefusal(payload.SessionID, service.AssistantRefusalValidation, "approval request requires session_id"), nil
	}
	if !a.participant(payload.SessionID, request) {
		return assistantRefusal(payload.SessionID, service.AssistantRefusalUnauthorized, "requester is not a participant in this assistant session"), nil
	}
	source := assistantSourceFromContextVM(request)
	switch payload.ContractVersion {
	case domain.AssistantExecutionVersion:
		if payload.Decision != "approve" && payload.Decision != "reject" {
			return assistantRefusal(payload.SessionID, service.AssistantRefusalValidation, "v2 decision must be approve or reject; use assistant/cancel to stop a run"), nil
		}
		// The edited candidate is decoded strictly: unknown structural fields,
		// duplicate keys and non-object arguments are refused before the
		// executor computes the revision-bound hash.
		plan, err := decodeAssistantModifiedPlan(request.RPC.Params)
		if err != nil {
			return assistantRefusal(payload.SessionID, service.AssistantRefusalPlanValidation, "modified_plan is invalid: "+err.Error()), nil
		}
		payload.ModifiedPlan = plan
		return a.orchestrator.HandleApprovalRequest(ctx, source, payload)
	case 0:
		snapshot, known := a.orchestrator.ExecutionSnapshot(payload.SessionID)
		translation := translateLegacyAssistantApproval(snapshot, known, payload, source.RequestID)
		switch {
		case translation.refusal != nil:
			return translation.refusal, nil
		case translation.cancellation != nil:
			return a.orchestrator.HandleCancellationRequest(ctx, source, *translation.cancellation)
		default:
			return a.orchestrator.HandleApprovalRequest(ctx, source, *translation.approval)
		}
	default:
		return assistantRefusal(payload.SessionID, service.AssistantRefusalValidation, fmt.Sprintf("unsupported assistant contract_version %d", payload.ContractVersion)), nil
	}
}

func (a assistantContextVMAdapter) handleCancel(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload domain.AssistantCancellationRequest
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("invalid assistant cancel params: %w", err)
	}
	payload.Scope = strings.ToLower(strings.TrimSpace(payload.Scope))
	if payload.ContractVersion != domain.AssistantExecutionVersion || strings.TrimSpace(payload.SessionID) == "" || strings.TrimSpace(payload.RunID) == "" || (payload.Scope != "run" && payload.Scope != "session") {
		return assistantRefusal(payload.SessionID, service.AssistantRefusalValidation, "cancel requires contract_version 2, session_id, run_id and scope run or session"), nil
	}
	if !a.participant(payload.SessionID, request) {
		return assistantRefusal(payload.SessionID, service.AssistantRefusalUnauthorized, "requester is not a participant in this assistant session"), nil
	}
	return a.orchestrator.HandleCancellationRequest(ctx, assistantSourceFromContextVM(request), payload)
}

func (a assistantContextVMAdapter) handleReconcile(ctx context.Context, request ContextVMRequest) (any, error) {
	var payload domain.AssistantReconciliationRequest
	if err := decodeContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, fmt.Errorf("invalid assistant reconcile params: %w", err)
	}
	payload.Resolution = strings.ToLower(strings.TrimSpace(payload.Resolution))
	if code, message := service.ValidateAssistantReconciliationRequest(payload); code != "" {
		return assistantRefusal(payload.SessionID, code, message), nil
	}
	if !a.participant(payload.SessionID, request) {
		return assistantRefusal(payload.SessionID, service.AssistantRefusalUnauthorized, "requester is not a participant in this assistant session"), nil
	}
	return a.orchestrator.HandleReconciliationRequest(ctx, assistantSourceFromContextVM(request), payload)
}

func decodeAssistantModifiedPlan(params json.RawMessage) (*domain.AssistantPlan, error) {
	if len(params) == 0 || string(params) == "null" {
		return nil, nil
	}
	var raw struct {
		ModifiedPlan json.RawMessage `json:"modified_plan"`
	}
	if err := json.Unmarshal(params, &raw); err != nil {
		return nil, err
	}
	if len(raw.ModifiedPlan) == 0 || string(raw.ModifiedPlan) == "null" {
		return nil, nil
	}
	plan, err := domain.DecodeAssistantExecutablePlan(raw.ModifiedPlan)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

// legacyAssistantTranslation is the outcome of decoding an unversioned (v1)
// approval request: exactly one of refusal, approval or cancellation is set.
type legacyAssistantTranslation struct {
	refusal      service.AssistantOperationResult
	approval     *domain.AssistantApprovalRequest
	cancellation *domain.AssistantCancellationRequest
}

// translateLegacyAssistantApproval implements the v1 approval compatibility
// rules. Only explicitly migrated v1 targets are eligible:
//
//   - An edited ModifiedPlan is always refused with
//     approval_contract_upgrade_required before any state is read or touched:
//     the old payload does not bind an edit to a base revision.
//   - An unedited plan-hash approve/reject is translated to a revision-bound v2
//     decision only when the legacy hash equals the migrated awaiting draft's
//     recorded legacy hash and that draft is still the unmodified migrated
//     revision.
//   - An action-ID decision is translated only for the unique migrated pending
//     action of a migrated iterative run (a legacy "cancel" of an action was a
//     rejection and stays one).
//   - A plan-hash cancel is translated to a run cancellation only when it names
//     the migrated current batch run.
//   - v2-native runs require v2 contracts.
//
// The executor re-validates the translated request's run, proposal revision
// and hash under its session lock, so a draft that changes between this
// snapshot and the decision is refused as stale. A redelivered legacy
// decision that the migrated run already recorded under the same request ID
// is routed to the executor's durable decision deduplication instead.
func translateLegacyAssistantApproval(x domain.AssistantExecution, known bool, req domain.AssistantApprovalRequest, requestID string) legacyAssistantTranslation {
	sessionID := strings.TrimSpace(req.SessionID)
	refuse := func(code, message string) legacyAssistantTranslation {
		return legacyAssistantTranslation{refusal: assistantRefusal(sessionID, code, message)}
	}
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	planHash := strings.TrimSpace(req.PlanHash)
	actionID := strings.TrimSpace(req.ActionID)
	reason := firstNonEmptyTrimmed(req.Reason, req.Message)
	if (planHash == "" && actionID == "") || (decision != "approve" && decision != "reject" && decision != "cancel") {
		return refuse(service.AssistantRefusalValidation, "approval request requires plan_hash or action_id and decision approve, reject or cancel")
	}
	if req.ModifiedPlan != nil {
		return refuse(service.AssistantRefusalContractUpgradeRequired, "edited plans require the versioned approval contract; reload the proposal and approve again")
	}
	if !known || x.RunID == "" {
		return refuse(service.AssistantRefusalUnknownSession, "assistant session has no active run")
	}
	if x.Migration == nil {
		return refuse(service.AssistantRefusalContractUpgradeRequired, "this assistant run requires contract_version 2 approvals")
	}
	if decision != "cancel" && legacyDecisionRecorded(x, requestID, planHash, actionID) {
		return legacyAssistantTranslation{approval: &domain.AssistantApprovalRequest{ContractVersion: domain.AssistantExecutionVersion, SessionID: sessionID, RunID: x.RunID, Workflow: x.Workflow, ActionID: actionID, Decision: decision, Reason: reason}}
	}
	finished := x.Phase == domain.AssistantExecutionCompleted || x.Phase == domain.AssistantExecutionFailed || x.Phase == domain.AssistantExecutionCancelled
	if actionID != "" {
		if decision == "cancel" {
			decision = "reject"
		}
		pending := 0
		matched := false
		for _, w := range x.Work {
			if w.State == domain.AssistantWorkAwaitingApproval {
				pending++
				matched = matched || w.WorkID == actionID
			}
		}
		if x.Workflow != domain.AssistantWorkflowIterative || x.Migration.Classification != string(service.AssistantLegacyIterativeAction) ||
			x.Phase != domain.AssistantExecutionAwaitingApproval || pending != 1 || !matched {
			return refuse(service.AssistantRefusalStaleApproval, "action does not match the migrated pending action")
		}
		return legacyAssistantTranslation{approval: &domain.AssistantApprovalRequest{ContractVersion: domain.AssistantExecutionVersion, SessionID: sessionID, RunID: x.RunID, Workflow: domain.AssistantWorkflowIterative, ActionID: actionID, Decision: decision, Reason: reason}}
	}
	if x.Workflow != domain.AssistantWorkflowBatch || x.Migration.LegacyPlanHash == "" || x.Migration.LegacyPlanHash != planHash {
		return refuse(service.AssistantRefusalStaleApproval, "plan_hash does not identify the migrated current batch run")
	}
	if decision == "cancel" {
		if finished {
			return refuse(service.AssistantRefusalInvalidState, "the migrated batch run already finished")
		}
		return legacyAssistantTranslation{cancellation: &domain.AssistantCancellationRequest{ContractVersion: domain.AssistantExecutionVersion, SessionID: sessionID, RunID: x.RunID, Scope: "run", Reason: reason}}
	}
	p := x.Proposal
	if x.Migration.Classification != string(service.AssistantLegacyBatchDraft) || x.Phase != domain.AssistantExecutionAwaitingApproval || p == nil || p.Revision != 1 || p.PreviousHash != "" || len(x.Work) != 0 {
		return refuse(service.AssistantRefusalStaleApproval, "plan_hash does not match the migrated awaiting draft")
	}
	return legacyAssistantTranslation{approval: &domain.AssistantApprovalRequest{ContractVersion: domain.AssistantExecutionVersion, SessionID: sessionID, RunID: x.RunID, Workflow: domain.AssistantWorkflowBatch, ProposalID: p.ProposalID, BaseRevision: p.Revision, BasePlanHash: p.Hash, ApprovedRevision: p.Revision, ApprovedPlanHash: p.Hash, Decision: decision, Reason: reason}}
}

// legacyDecisionRecorded reports whether this migrated run already recorded a
// decision made by exactly this legacy request for the same target.
func legacyDecisionRecorded(x domain.AssistantExecution, requestID, planHash, actionID string) bool {
	if requestID == "" {
		return false
	}
	if planHash != "" {
		return x.Workflow == domain.AssistantWorkflowBatch && x.Migration.LegacyPlanHash == planHash && x.Proposal != nil && x.Proposal.RequestID == requestID
	}
	for _, w := range x.Work {
		if w.WorkID == actionID && w.Authorization != nil && w.Authorization.DecisionRequestID == requestID {
			return true
		}
	}
	return false
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func assistantSourceFromContextVM(request ContextVMRequest) service.AssistantRequestSource {
	dedupKey := strings.TrimSpace(request.ProgressToken)
	if dedupKey == "" && request.Event != nil {
		dedupKey = request.Event.ID.Hex()
	}
	source := service.AssistantRequestSource{Event: request.Event, DedupKey: dedupKey}
	if request.Event != nil {
		source.OperatorPubkey = request.Event.PubKey.Hex()
		source.RequestID = request.Event.ID.Hex()
	}
	return source
}

func decodeContextVMParams(params json.RawMessage, out any) error {
	if len(params) == 0 || string(params) == "null" {
		return json.Unmarshal([]byte(`{}`), out)
	}
	return json.Unmarshal(params, out)
}
