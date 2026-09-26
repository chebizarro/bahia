package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

// AssistantExecutionScopeResolver derives trusted command scope before a model
// sees tools. A command-aware installation must supply one; browser metadata is
// not accepted as an authorization source.
type AssistantExecutionScopeResolver interface {
	ResolveAssistantScope(context.Context, AssistantTurnStartRequest) (domain.AssistantCommandScope, error)
}

// AssistantRequestEvidenceResolver returns a receipt only after verifying and
// decrypting the exact downstream request event and comparing it to work.
type AssistantRequestEvidenceResolver interface {
	ResolveAssistantRequestEvidence(context.Context, string, domain.AssistantExecution, domain.AssistantWorkItem) (*domain.AsyncToolReceipt, error)
}

type AssistantExecutionEngineConfig struct {
	Store         AssistantCheckpointStore
	Runtime       *AssistantToolRuntime
	Observer      AssistantAsyncResultObserver
	Batch         AssistantBatchProposer
	Iterative     AssistantIterativeProposer
	Transcript    AssistantAgentTranscriptAppender
	ScopeResolver AssistantExecutionScopeResolver
	Evidence      AssistantRequestEvidenceResolver
	Publisher     AssistantEventPublisher
	Signer        nostr.Signer
	Identity      AssistantIdentity
	Lifecycle     context.Context
	Now           func() time.Time
	NewID         func(string) string
	MaxIterations int
}

type assistantEngineSession struct {
	mu           sync.Mutex
	execution    domain.AssistantExecution
	checkpoint   string
	projection   domain.AssistantSessionV2
	activeCancel context.CancelFunc
	observing    map[string]context.CancelFunc
}

// AssistantExecutionEngine is the single authoritative per-session v2 writer.
// All external I/O is outside the session lock except a checkpoint commit,
// which is the dispatch/cancellation reservation boundary.
type AssistantExecutionEngine struct {
	cfg      AssistantExecutionEngineConfig
	mu       sync.Mutex
	sessions map[string]*assistantEngineSession
}

var _ AssistantTurnEngine = (*AssistantExecutionEngine)(nil)

func NewAssistantExecutionEngine(cfg AssistantExecutionEngineConfig) *AssistantExecutionEngine {
	if cfg.Lifecycle == nil {
		cfg.Lifecycle = context.Background()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = randomAssistantRuntimeID
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = defaultAssistantAgentLoopMaxIterations
	}
	return &AssistantExecutionEngine{cfg: cfg, sessions: make(map[string]*assistantEngineSession)}
}

func (e *AssistantExecutionEngine) session(id string) *assistantEngineSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.sessions[id]
	if s == nil {
		s = &assistantEngineSession{observing: make(map[string]context.CancelFunc)}
		e.sessions[id] = s
	}
	return s
}

func (e *AssistantExecutionEngine) ready() error {
	if e == nil || e.cfg.Store == nil || e.cfg.Runtime == nil || e.cfg.Observer == nil {
		return errors.New("assistant executor store/runtime/observer not configured")
	}
	return nil
}

func (e *AssistantExecutionEngine) StartTurn(_ context.Context, req AssistantTurnStartRequest) (AssistantTurnResult, error) {
	if err := e.ready(); err != nil {
		return AssistantTurnResult{}, err
	}
	if req.Prompt.SessionID == "" || req.Prompt.TurnID == "" || strings.TrimSpace(req.Prompt.Prompt) == "" || req.OperatorPubkey == "" || req.RequestEventID == "" {
		return AssistantTurnResult{}, errors.New("assistant turn identity or prompt missing")
	}
	workflow := req.Prompt.Workflow
	if workflow == "" && req.ExistingSession != nil {
		workflow = req.ExistingSession.Workflow
	}
	if workflow == "" {
		workflow = req.DefaultWorkflow
	}
	if !workflow.Valid() {
		return AssistantTurnResult{}, errors.New("assistant workflow missing or invalid")
	}
	scope := domain.AssistantCommandScope{SelectedRefs: append([]string(nil), req.Prompt.SelectedRefs...)}
	if e.cfg.ScopeResolver != nil {
		var err error
		scope, err = e.cfg.ScopeResolver.ResolveAssistantScope(e.cfg.Lifecycle, req)
		if err != nil {
			return AssistantTurnResult{}, err
		}
	}
	scope, err := scope.Clone()
	if err != nil {
		return AssistantTurnResult{}, err
	}
	s := e.session(req.Prompt.SessionID)
	s.mu.Lock()
	if s.execution.RunID != "" && !assistantPhaseFinished(s.execution.Phase) {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("run_in_progress")
	}
	if req.ExistingSession != nil && req.ExistingSession.CurrentRunID != "" && !assistantPhaseFinished(req.ExistingSession.Phase) {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("run_in_progress")
	}
	runID := e.cfg.NewID("run")
	x := domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: req.Prompt.SessionID, RunID: runID, TurnID: req.Prompt.TurnID, RequestID: req.RequestEventID, Workflow: workflow, Phase: domain.AssistantExecutionProposing, Scope: scope, Work: []domain.AssistantWorkItem{}}
	p := domain.AssistantSessionV2{Schema: domain.AssistantSessionSchemaV2, SessionID: x.SessionID, State: domain.AssistantSessionStatePlanning, OperatorPubkey: req.OperatorPubkey, AssistantID: e.cfg.Identity.AgentID, AssistantPubkey: e.cfg.Identity.Pubkey, CurrentTurnID: x.TurnID, CurrentRequestID: x.RequestID, ExecutionVersion: domain.AssistantExecutionVersion, Workflow: workflow, CurrentRunID: runID}
	if req.ExistingSession != nil {
		p.Participants = append([]string(nil), req.ExistingSession.Participants...)
		p.TranscriptSummary = req.ExistingSession.TranscriptSummary
	}
	if err = e.commitLocked(e.cfg.Lifecycle, s, x, p); err != nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, err
	}
	token := s.execution.Revision
	modelCtx, cancel := context.WithCancel(e.cfg.Lifecycle)
	s.activeCancel = cancel
	s.mu.Unlock()
	proposalReq := AssistantProposalRequest{SessionID: x.SessionID, RunID: x.RunID, TurnID: x.TurnID, Prompt: req.Prompt.Prompt, Scope: scope}
	var proposal AssistantProposal
	if workflow == domain.AssistantWorkflowBatch {
		if e.cfg.Batch == nil {
			cancel()
			return AssistantTurnResult{}, errors.New("assistant batch proposer missing")
		}
		proposal, err = e.cfg.Batch.ProposeBatch(modelCtx, proposalReq)
	} else {
		if e.cfg.Iterative == nil {
			cancel()
			return AssistantTurnResult{}, errors.New("assistant iterative proposer missing")
		}
		proposal, err = e.cfg.Iterative.ProposeIterative(modelCtx, proposalReq)
	}
	cancel()
	s.mu.Lock()
	s.activeCancel = nil
	if s.execution.RunID != runID || s.execution.Revision != token || s.execution.Cancellation != nil {
		result := e.resultLocked(s, "turn_superseded")
		s.mu.Unlock()
		return result, nil
	}
	if err != nil {
		blocked := s.execution
		blocked.Phase = domain.AssistantExecutionBlocked
		commitErr := e.commitLocked(e.cfg.Lifecycle, s, blocked, s.projection)
		result := e.resultLocked(s, "proposal_blocked")
		s.mu.Unlock()
		if commitErr != nil {
			return result, commitErr
		}
		return result, err
	}
	next := s.execution
	switch proposal.Kind {
	case AssistantProposalBatch:
		if workflow != domain.AssistantWorkflowBatch || proposal.Batch == nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, errors.New("batch proposer returned invalid proposal")
		}
		plan, planErr := domain.NormalizeAssistantExecutablePlan(*proposal.Batch)
		if planErr != nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, planErr
		}
		scopePublic, scopeErr := next.Scope.ApprovalScope()
		if scopeErr != nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, scopeErr
		}
		proposalID := e.cfg.NewID("proposal")
		hash, hashErr := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: domain.AssistantExecutionVersion, SessionID: next.SessionID, RunID: next.RunID, Workflow: next.Workflow, ProposalID: proposalID, Revision: 1, Scope: scopePublic, Plan: plan})
		if hashErr != nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, hashErr
		}
		next.Proposal = &domain.AssistantProposalRevision{ProposalID: proposalID, Revision: 1, Plan: plan, Hash: hash, RequestID: next.RequestID}
		next.Phase = domain.AssistantExecutionAwaitingApproval
	case AssistantProposalCalls:
		if workflow != domain.AssistantWorkflowIterative {
			s.mu.Unlock()
			return AssistantTurnResult{}, errors.New("iterative proposer returned calls for batch")
		}
		if err = appendAssistantCalls(&next, proposal.Calls); err != nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, err
		}
		next.Phase = domain.AssistantExecutionExecuting
	case AssistantProposalFinal, AssistantProposalClarification:
		next.Phase = domain.AssistantExecutionCompleted
	case AssistantProposalBlocked:
		next.Phase = domain.AssistantExecutionBlocked
	default:
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("assistant proposer returned unknown outcome")
	}
	err = e.commitLocked(e.cfg.Lifecycle, s, next, s.projection)
	result := e.resultLocked(s, string(next.Phase))
	s.mu.Unlock()
	if err != nil {
		return result, err
	}
	if next.Phase == domain.AssistantExecutionExecuting {
		e.drive(x.SessionID, x.RunID)
	}
	return result, nil
}

func appendAssistantCalls(x *domain.AssistantExecution, calls []domain.AssistantAgentToolCall) error {
	seen := map[string]bool{}
	for _, w := range x.Work {
		seen[w.OriginID] = true
	}
	for _, call := range calls {
		if call.ID == "" || call.Name == "" || call.Arguments == nil || seen[call.ID] {
			return fmt.Errorf("iterative call missing or duplicate identity %q", call.ID)
		}
		seen[call.ID] = true
		args, err := domain.DeepCopyAssistantJSONMap(call.Arguments)
		if err != nil {
			return err
		}
		delete(args, "idempotency_key")
		digest, err := domain.ComputeAssistantArgumentsDigest(args)
		if err != nil {
			return err
		}
		x.Work = append(x.Work, domain.AssistantWorkItem{WorkID: x.RunID + ":" + call.ID, OriginID: call.ID, Ordinal: len(x.Work), ToolName: call.Name, Arguments: args, ArgumentsDigest: digest, State: domain.AssistantWorkReady})
	}
	return nil
}

func assistantPhaseFinished(p domain.AssistantExecutionPhase) bool {
	return p == domain.AssistantExecutionCompleted || p == domain.AssistantExecutionFailed || p == domain.AssistantExecutionCancelled
}

func (e *AssistantExecutionEngine) Decide(_ context.Context, req AssistantTurnDecisionRequest) (AssistantTurnResult, error) {
	if err := e.ready(); err != nil {
		return AssistantTurnResult{}, err
	}
	a := req.Approval
	s := e.session(a.SessionID)
	s.mu.Lock()
	x := s.execution
	if x.RunID == "" || a.RunID != x.RunID || a.Workflow != x.Workflow || a.ContractVersion != domain.AssistantExecutionVersion || req.OperatorPubkey == "" || req.OperatorPubkey != s.projection.OperatorPubkey {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("assistant approval target or operator mismatch")
	}
	if x.Phase != domain.AssistantExecutionAwaitingApproval {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("assistant run is not awaiting approval")
	}
	if a.Decision == "reject" {
		if x.Workflow == domain.AssistantWorkflowBatch {
			x.Phase = domain.AssistantExecutionCancelled
		} else {
			if x.Cursor >= len(x.Work) {
				s.mu.Unlock()
				return AssistantTurnResult{}, errors.New("pending action missing")
			}
			x.Work[x.Cursor].State = domain.AssistantWorkObserved
			x.Work[x.Cursor].Observation = &domain.AssistantToolObservation{ObservationID: e.cfg.NewID("obs"), ToolCallID: x.Work[x.Cursor].OriginID, ToolName: x.Work[x.Cursor].ToolName, Status: domain.AssistantToolObservationDenied, Summary: "operator rejected action", ObservedAt: e.cfg.Now().UTC()}
			x.Phase = domain.AssistantExecutionExecuting
		}
		err := e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
		result := e.resultLocked(s, "rejected")
		s.mu.Unlock()
		if err == nil && x.Phase == domain.AssistantExecutionExecuting {
			if x.Workflow == domain.AssistantWorkflowIterative {
				e.consumeObserved(x.SessionID, x.RunID, x.Work[x.Cursor].WorkID)
			}
			e.drive(x.SessionID, x.RunID)
		}
		return result, err
	}
	if a.Decision != "approve" {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("unsupported assistant approval decision")
	}
	if x.Workflow == domain.AssistantWorkflowBatch {
		if x.Proposal == nil || a.ProposalID != x.Proposal.ProposalID || a.BaseRevision != x.Proposal.Revision || a.BasePlanHash != x.Proposal.Hash {
			s.mu.Unlock()
			return AssistantTurnResult{}, errors.New("stale assistant proposal")
		}
		plan := x.Proposal.Plan
		edited := a.ModifiedPlan != nil
		if edited {
			var err error
			plan, err = domain.NormalizeAssistantExecutablePlan(*a.ModifiedPlan)
			if err != nil {
				s.mu.Unlock()
				return AssistantTurnResult{}, err
			}
		}
		approvedRev := x.Proposal.Revision
		if edited {
			approvedRev++
		}
		if a.ApprovedRevision != approvedRev {
			s.mu.Unlock()
			return AssistantTurnResult{}, errors.New("approved revision mismatch")
		}
		scopePublic, err := x.Scope.ApprovalScope()
		if err != nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, err
		}
		hash, err := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: domain.AssistantExecutionVersion, SessionID: x.SessionID, RunID: x.RunID, Workflow: x.Workflow, ProposalID: x.Proposal.ProposalID, Revision: approvedRev, Scope: scopePublic, Plan: plan})
		if err != nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, err
		}
		if a.ApprovedPlanHash != hash {
			s.mu.Unlock()
			return AssistantTurnResult{}, errors.New("approved plan hash mismatch")
		}
		previous := *x.Proposal
		x.Proposal = &domain.AssistantProposalRevision{ProposalID: previous.ProposalID, Revision: approvedRev, Plan: plan, Hash: hash, RequestID: req.RequestEventID}
		if edited {
			x.Proposal.PreviousRevision = previous.Revision
			x.Proposal.PreviousHash = previous.Hash
		}
		x.Work = make([]domain.AssistantWorkItem, 0, len(plan.Steps))
		x.Cursor = 0
		replacement := plan
		replacementNeeded := false
		for i, step := range plan.Steps {
			args, copyErr := domain.DeepCopyAssistantJSONMap(step.ToolArgs)
			if copyErr != nil {
				s.mu.Unlock()
				return AssistantTurnResult{}, copyErr
			}
			delete(args, "idempotency_key")
			digest, digestErr := domain.ComputeAssistantArgumentsDigest(args)
			if digestErr != nil {
				s.mu.Unlock()
				return AssistantTurnResult{}, digestErr
			}
			scope, scopeErr := x.Scope.Clone()
			if scopeErr != nil {
				s.mu.Unlock()
				return AssistantTurnResult{}, scopeErr
			}
			work := domain.AssistantWorkItem{WorkID: x.RunID + ":" + step.StepID, OriginID: step.StepID, Ordinal: i, ToolName: step.ToolName, Arguments: args, ArgumentsDigest: digest, State: domain.AssistantWorkReady, Authorization: &domain.AssistantAuthorizationBinding{OperatorPubkey: req.OperatorPubkey, DecisionRequestID: req.RequestEventID, ProposalID: x.Proposal.ProposalID, ProposalRevision: approvedRev, ArgumentsDigest: digest, Scope: scope}}
			prepared, prepErr := e.cfg.Runtime.PrepareWork(e.cfg.Lifecycle, x, work)
			if errors.Is(prepErr, ErrAssistantApprovedInputChanged) {
				replacement.Steps[i].ToolArgs = prepared.Work.Arguments
				replacementNeeded = true
				continue
			}
			if prepErr != nil {
				s.mu.Unlock()
				return AssistantTurnResult{}, prepErr
			}
			work.Authorization.Permission = prepared.Permission
			x.Work = append(x.Work, work)
		}
		if replacementNeeded {
			replacementRev := approvedRev + 1
			replacementHash, hashErr := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: domain.AssistantExecutionVersion, SessionID: x.SessionID, RunID: x.RunID, Workflow: x.Workflow, ProposalID: x.Proposal.ProposalID, Revision: replacementRev, Scope: scopePublic, Plan: replacement})
			if hashErr != nil {
				s.mu.Unlock()
				return AssistantTurnResult{}, hashErr
			}
			x.Proposal = &domain.AssistantProposalRevision{ProposalID: previous.ProposalID, Revision: replacementRev, Plan: replacement, Hash: replacementHash, RequestID: req.RequestEventID, PreviousRevision: approvedRev, PreviousHash: hash}
			x.Work = nil
			x.Cursor = 0
			x.Phase = domain.AssistantExecutionAwaitingApproval
			err = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
			result := e.resultLocked(s, "proposal_changed_requires_review")
			s.mu.Unlock()
			if err != nil {
				return result, err
			}
			return result, errors.New("proposal_changed_requires_review")
		}
		if len(x.Work) == 0 {
			x.Phase = domain.AssistantExecutionCompleted
		} else {
			x.Phase = domain.AssistantExecutionExecuting
		}
	} else {
		if x.Cursor >= len(x.Work) || a.ActionID == "" || a.ActionID != x.Work[x.Cursor].WorkID {
			s.mu.Unlock()
			return AssistantTurnResult{}, errors.New("assistant action identity mismatch")
		}
		work := &x.Work[x.Cursor]
		if work.State != domain.AssistantWorkAwaitingApproval {
			s.mu.Unlock()
			return AssistantTurnResult{}, errors.New("assistant action not awaiting approval")
		}
		scope, err := x.Scope.Clone()
		if err != nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, err
		}
		work.Authorization = &domain.AssistantAuthorizationBinding{OperatorPubkey: req.OperatorPubkey, DecisionRequestID: req.RequestEventID, ActionID: work.WorkID, ArgumentsDigest: work.ArgumentsDigest, Scope: scope}
		prepared, err := e.cfg.Runtime.PrepareWork(e.cfg.Lifecycle, x, *work)
		if err != nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, err
		}
		work.Authorization.Permission = prepared.Permission
		work.State = domain.AssistantWorkReady
		x.Phase = domain.AssistantExecutionExecuting
	}
	err := e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
	result := e.resultLocked(s, "approved")
	s.mu.Unlock()
	if err == nil && x.Phase == domain.AssistantExecutionExecuting {
		e.drive(x.SessionID, x.RunID)
	}
	return result, err
}

func (e *AssistantExecutionEngine) Cancel(_ context.Context, req AssistantTurnCancellationRequest) (AssistantTurnResult, error) {
	if err := e.ready(); err != nil {
		return AssistantTurnResult{}, err
	}
	c := req.Cancellation
	s := e.session(c.SessionID)
	s.mu.Lock()
	x := s.execution
	if c.ContractVersion != domain.AssistantExecutionVersion || c.RunID == "" || c.RunID != x.RunID || req.OperatorPubkey == "" || req.OperatorPubkey != s.projection.OperatorPubkey || (c.Scope != "run" && c.Scope != "session") {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("stale or invalid assistant cancellation")
	}
	if x.Cancellation != nil {
		result := e.resultLocked(s, "cancellation_already_recorded")
		s.mu.Unlock()
		return result, nil
	}
	x.Cancellation = &domain.AssistantExecutionCancellation{Scope: c.Scope, RunID: x.RunID, OperatorPubkey: req.OperatorPubkey, RequestID: req.RequestEventID, Reason: c.Reason, RecordedAt: e.cfg.Now().UTC()}
	pending := 0
	for i := range x.Work {
		switch x.Work[i].State {
		case domain.AssistantWorkPending, domain.AssistantWorkReady, domain.AssistantWorkAwaitingApproval:
			x.Work[i].State = domain.AssistantWorkSkipped
		case domain.AssistantWorkDispatching, domain.AssistantWorkWaitingAsync, domain.AssistantWorkObserved, domain.AssistantWorkUncertain:
			pending++
		}
	}
	if pending > 0 {
		x.Phase = domain.AssistantExecutionCancelling
	} else {
		x.Phase = domain.AssistantExecutionCancelled
	}
	err := e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
	if err == nil && s.activeCancel != nil {
		s.activeCancel()
	}
	result := e.resultLocked(s, "cancelled")
	s.mu.Unlock()
	return result, err
}

func (e *AssistantExecutionEngine) Reconcile(_ context.Context, req AssistantTurnReconciliationRequest) (AssistantTurnResult, error) {
	if err := e.ready(); err != nil {
		return AssistantTurnResult{}, err
	}
	q := req.Reconciliation
	s := e.session(q.SessionID)
	s.mu.Lock()
	x := s.execution
	if q.ContractVersion != domain.AssistantExecutionVersion || q.RunID != x.RunID || q.RequestEventID == "" || req.OperatorPubkey == "" || req.OperatorPubkey != s.projection.OperatorPubkey || x.Cursor >= len(x.Work) || x.Work[x.Cursor].WorkID != q.WorkID || x.Work[x.Cursor].State != domain.AssistantWorkUncertain {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("assistant reconciliation target invalid")
	}
	if e.cfg.Evidence == nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("verified assistant request evidence resolver missing")
	}
	work := x.Work[x.Cursor]
	revision := x.Revision
	s.mu.Unlock()
	receipt, err := e.cfg.Evidence.ResolveAssistantRequestEvidence(e.cfg.Lifecycle, q.RequestEventID, x, work)
	if err != nil {
		return AssistantTurnResult{}, err
	}
	if receipt == nil || receipt.RequestEventID != q.RequestEventID || receipt.ToolName != work.ToolName || receipt.IdempotencyKey != work.IdempotencyKey || len(receipt.ResultKinds) == 0 {
		return AssistantTurnResult{}, errors.New("assistant reconciliation evidence mismatch")
	}
	s.mu.Lock()
	if s.execution.RunID != x.RunID || s.execution.Revision != revision || s.execution.Work[s.execution.Cursor].State != domain.AssistantWorkUncertain {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("assistant reconciliation superseded")
	}
	x = s.execution
	x.Work[x.Cursor].Receipt = cloneAsyncToolReceipt(receipt)
	x.Work[x.Cursor].State = domain.AssistantWorkWaitingAsync
	if x.Cancellation == nil {
		x.Phase = domain.AssistantExecutionWaitingAsync
	}
	err = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
	result := e.resultLocked(s, "reconciled")
	s.mu.Unlock()
	if err == nil {
		e.startObserver(x.SessionID, x.RunID, work.WorkID, receipt)
	}
	return result, err
}

func (e *AssistantExecutionEngine) Recover(ctx context.Context, ref AssistantExecutionReference) error {
	if err := e.ready(); err != nil {
		return err
	}
	x, id, err := e.cfg.Store.Load(ctx, ref.SessionID, ref.RunID)
	if err != nil {
		return err
	}
	if ref.CheckpointEventID != "" && ref.CheckpointEventID != id { /* latest valid journal wins over a stale projection */
	}
	s := e.session(x.SessionID)
	s.mu.Lock()
	if s.execution.Revision >= x.Revision && s.execution.RunID == x.RunID {
		s.mu.Unlock()
		return nil
	}
	s.execution = x
	s.checkpoint = id
	s.projection = projectAssistantExecution(x, id, s.projection)
	if x.Cursor < len(x.Work) && x.Work[x.Cursor].State == domain.AssistantWorkDispatching && x.Work[x.Cursor].Receipt == nil {
		x.Work[x.Cursor].State = domain.AssistantWorkUncertain
		x.Phase = domain.AssistantExecutionBlocked
		if err = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	phase := s.execution.Phase
	var work domain.AssistantWorkItem
	if s.execution.Cursor < len(s.execution.Work) {
		work = s.execution.Work[s.execution.Cursor]
	}
	s.mu.Unlock()
	if work.State == domain.AssistantWorkWaitingAsync && work.Receipt != nil {
		e.startObserver(x.SessionID, x.RunID, work.WorkID, work.Receipt)
	}
	if work.State == domain.AssistantWorkObserved {
		e.consumeObserved(x.SessionID, x.RunID, work.WorkID)
	}
	if phase == domain.AssistantExecutionExecuting && work.State == domain.AssistantWorkReady {
		e.drive(x.SessionID, x.RunID)
	}
	return nil
}

func (e *AssistantExecutionEngine) drive(sessionID, runID string) {
	for {
		s := e.session(sessionID)
		s.mu.Lock()
		x := s.execution
		if x.RunID != runID || x.Cancellation != nil || x.Phase != domain.AssistantExecutionExecuting {
			s.mu.Unlock()
			return
		}
		if x.Cursor >= len(x.Work) {
			if x.Workflow == domain.AssistantWorkflowIterative && e.cfg.Iterative != nil && len(x.Work) < e.cfg.MaxIterations {
				s.mu.Unlock()
				e.continueIterative(sessionID, runID)
				return
			}
			x.Phase = domain.AssistantExecutionCompleted
			_ = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
			s.mu.Unlock()
			return
		}
		work := x.Work[x.Cursor]
		if work.State != domain.AssistantWorkReady {
			s.mu.Unlock()
			return
		}
		revision := x.Revision
		s.mu.Unlock()
		prepared, err := e.cfg.Runtime.PrepareWork(e.cfg.Lifecycle, x, work)
		if err != nil {
			s.mu.Lock()
			if s.execution.RunID != runID || s.execution.Revision != revision {
				s.mu.Unlock()
				return
			}
			x = s.execution
			if x.Workflow == domain.AssistantWorkflowIterative && strings.Contains(err.Error(), "requires exact approval") {
				x.Work[x.Cursor].State = domain.AssistantWorkAwaitingApproval
				x.Phase = domain.AssistantExecutionAwaitingApproval
			} else {
				x.Work[x.Cursor].State = domain.AssistantWorkDenied
				x.Phase = domain.AssistantExecutionBlocked
				if x.Workflow == domain.AssistantWorkflowBatch {
					for i := x.Cursor + 1; i < len(x.Work); i++ {
						x.Work[i].State = domain.AssistantWorkSkipped
					}
				}
			}
			_ = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		if s.execution.RunID != runID || s.execution.Revision != revision || s.execution.Cancellation != nil {
			s.mu.Unlock()
			return
		}
		x = s.execution
		if x.Work[x.Cursor].IdempotencyKey == "" {
			x.Work[x.Cursor].IdempotencyKey = prepared.Work.IdempotencyKey
		}

		x.Work[x.Cursor].Arguments = prepared.Work.Arguments
		x.Work[x.Cursor].ArgumentsDigest = prepared.Work.ArgumentsDigest
		x.Work[x.Cursor].State = domain.AssistantWorkDispatching
		if err = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection); err != nil {
			s.mu.Unlock()
			return
		}
		prepared.Work.IdempotencyKey = x.Work[x.Cursor].IdempotencyKey
		s.mu.Unlock()
		obs, receipt, dispatchErr := e.cfg.Runtime.DispatchPreparedWork(e.cfg.Lifecycle, prepared)
		s.mu.Lock()
		if s.execution.RunID != runID || s.execution.Cursor >= len(s.execution.Work) || s.execution.Work[s.execution.Cursor].WorkID != work.WorkID {
			s.mu.Unlock()
			return
		}
		x = s.execution
		if dispatchErr != nil {
			x.Work[x.Cursor].State = domain.AssistantWorkUncertain
			x.Phase = domain.AssistantExecutionBlocked
			_ = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
			s.mu.Unlock()
			return
		}
		if receipt != nil {
			x.Work[x.Cursor].Receipt = cloneAsyncToolReceipt(receipt)
			x.Work[x.Cursor].State = domain.AssistantWorkWaitingAsync
			if x.Cancellation == nil {
				x.Phase = domain.AssistantExecutionWaitingAsync
			}
			err = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
			s.mu.Unlock()
			if err == nil {
				e.startObserver(sessionID, runID, work.WorkID, receipt)
			}
			return
		}
		if obs == nil {
			x.Work[x.Cursor].State = domain.AssistantWorkUncertain
			x.Phase = domain.AssistantExecutionBlocked
			_ = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
			s.mu.Unlock()
			return
		}
		x.Work[x.Cursor].Observation = obs
		x.Work[x.Cursor].State = domain.AssistantWorkObserved
		err = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
		s.mu.Unlock()
		if err != nil {
			return
		}
		e.consumeObserved(sessionID, runID, work.WorkID)
		s.mu.Lock()
		continueRun := s.execution.RunID == runID && s.execution.Phase == domain.AssistantExecutionExecuting
		s.mu.Unlock()
		if !continueRun {
			return
		}
	}
}

func (e *AssistantExecutionEngine) startObserver(sessionID, runID, workID string, receipt *domain.AsyncToolReceipt) {
	s := e.session(sessionID)
	key := runID + "\x00" + workID + "\x00" + receipt.RequestEventID
	s.mu.Lock()
	if s.observing[key] != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(e.cfg.Lifecycle)
	s.observing[key] = cancel
	s.mu.Unlock()
	go func() {
		defer func() { cancel(); s.mu.Lock(); delete(s.observing, key); s.mu.Unlock() }()
		outcome, err := e.cfg.Observer.ObserveAssistantAsyncResult(ctx, sessionID, workID, receipt.ToolName, receipt)
		s.mu.Lock()
		x := s.execution
		if x.RunID != runID || x.Cursor >= len(x.Work) || x.Work[x.Cursor].WorkID != workID || x.Work[x.Cursor].State != domain.AssistantWorkWaitingAsync {
			s.mu.Unlock()
			return
		}
		if err != nil || outcome.Status == "blocked" {
			x.Phase = domain.AssistantExecutionBlocked
			_ = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
			s.mu.Unlock()
			return
		}
		status := domain.AssistantToolObservationSucceeded
		if outcome.Status == "failed" {
			status = domain.AssistantToolObservationFailed
		}
		result, content := assistantObservationFromEvent(outcome.Event)
		obs := &domain.AssistantToolObservation{ObservationID: e.cfg.NewID("obs"), ToolCallID: x.Work[x.Cursor].OriginID, ToolName: x.Work[x.Cursor].ToolName, Status: status, ExecutionMode: domain.AssistantToolExecutionModeAsync, Receipt: cloneAsyncToolReceipt(receipt), Result: result, Content: content, ObservedAt: e.cfg.Now().UTC()}
		if outcome.Event != nil {
			obs.EventID = outcome.Event.ID.Hex()
		}
		x.Work[x.Cursor].Observation = obs
		x.Work[x.Cursor].State = domain.AssistantWorkObserved
		if x.Cancellation == nil {
			x.Phase = domain.AssistantExecutionExecuting
		}
		commitErr := e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
		s.mu.Unlock()
		if commitErr == nil {
			e.consumeObserved(sessionID, runID, workID)
			e.drive(sessionID, runID)
		}
	}()
}

func (e *AssistantExecutionEngine) consumeObserved(sessionID, runID, workID string) {
	s := e.session(sessionID)
	s.mu.Lock()
	x := s.execution
	if x.RunID != runID || x.Cursor >= len(x.Work) || x.Work[x.Cursor].WorkID != workID || x.Work[x.Cursor].State != domain.AssistantWorkObserved {
		s.mu.Unlock()
		return
	}
	work := x.Work[x.Cursor]
	projection := s.projection
	s.mu.Unlock()
	if e.cfg.Transcript != nil && work.Observation != nil {
		_, err := e.cfg.Transcript.AppendMessage(e.cfg.Lifecycle, AssistantTranscriptAppend{SessionID: sessionID, TurnID: x.TurnID, RunID: runID, Sequence: work.Ordinal, OperatorPubkey: projection.OperatorPubkey, LogicalID: runID + ":" + workID + ":observation", Message: domain.AssistantAgentMessage{ID: runID + ":" + workID + ":observation", Role: domain.AssistantAgentMessageRoleTool, ToolCallID: work.OriginID, Name: work.ToolName, Observation: work.Observation}})
		if err != nil {
			return
		}
	}
	s.mu.Lock()
	x = s.execution
	if x.RunID != runID || x.Cursor >= len(x.Work) || x.Work[x.Cursor].WorkID != workID || x.Work[x.Cursor].State != domain.AssistantWorkObserved {
		s.mu.Unlock()
		return
	}
	if work.Observation.Status == domain.AssistantToolObservationFailed {
		x.Work[x.Cursor].State = domain.AssistantWorkFailed
	} else if work.Observation.Status == domain.AssistantToolObservationDenied {
		x.Work[x.Cursor].State = domain.AssistantWorkDenied
	} else {
		x.Work[x.Cursor].State = domain.AssistantWorkSucceeded
	}
	x.Cursor++
	if x.Cancellation != nil {
		x.Phase = domain.AssistantExecutionCancelled
	} else if x.Workflow == domain.AssistantWorkflowBatch && work.Observation.Status != domain.AssistantToolObservationSucceeded {
		x.Phase = domain.AssistantExecutionFailed
		for i := x.Cursor; i < len(x.Work); i++ {
			x.Work[i].State = domain.AssistantWorkSkipped
		}
	} else if x.Cursor >= len(x.Work) && x.Workflow == domain.AssistantWorkflowBatch {
		x.Phase = domain.AssistantExecutionCompleted
	} else {
		x.Phase = domain.AssistantExecutionExecuting
	}
	_ = e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
	s.mu.Unlock()
}

func (e *AssistantExecutionEngine) continueIterative(sessionID, runID string) {
	s := e.session(sessionID)
	s.mu.Lock()
	x := s.execution
	if x.RunID != runID || x.Cancellation != nil || x.Phase != domain.AssistantExecutionExecuting || x.Cursor != len(x.Work) {
		s.mu.Unlock()
		return
	}
	x.Phase = domain.AssistantExecutionProposing
	if err := e.commitLocked(e.cfg.Lifecycle, s, x, s.projection); err != nil {
		s.mu.Unlock()
		return
	}
	token := s.execution.Revision
	ctx, cancel := context.WithCancel(e.cfg.Lifecycle)
	s.activeCancel = cancel
	s.mu.Unlock()
	proposal, err := e.cfg.Iterative.ProposeIterative(ctx, AssistantProposalRequest{SessionID: sessionID, RunID: runID, TurnID: x.TurnID, Scope: x.Scope})
	cancel()
	s.mu.Lock()
	s.activeCancel = nil
	x = s.execution
	if x.RunID != runID || x.Revision != token || x.Cancellation != nil {
		s.mu.Unlock()
		return
	}
	if err != nil || proposal.Kind == AssistantProposalBlocked {
		x.Phase = domain.AssistantExecutionBlocked
	} else if proposal.Kind == AssistantProposalFinal || proposal.Kind == AssistantProposalClarification {
		x.Phase = domain.AssistantExecutionCompleted
	} else if proposal.Kind == AssistantProposalCalls {
		if len(proposal.Calls) == 0 || appendAssistantCalls(&x, proposal.Calls) != nil {
			x.Phase = domain.AssistantExecutionBlocked
		} else {
			x.Phase = domain.AssistantExecutionExecuting
		}
	} else {
		x.Phase = domain.AssistantExecutionBlocked
	}
	commitErr := e.commitLocked(e.cfg.Lifecycle, s, x, s.projection)
	s.mu.Unlock()
	if commitErr == nil && x.Phase == domain.AssistantExecutionExecuting {
		e.drive(sessionID, runID)
	}
}

func (e *AssistantExecutionEngine) commitLocked(ctx context.Context, s *assistantEngineSession, x domain.AssistantExecution, p domain.AssistantSessionV2) error {
	x.Revision = s.execution.Revision + 1
	if s.execution.RunID != x.RunID {
		x.Revision = 1
		s.checkpoint = ""
	}
	id, err := e.cfg.Store.Append(ctx, x, s.checkpoint)
	if err != nil {
		return err
	}
	s.execution = x
	s.checkpoint = id
	s.projection = projectAssistantExecution(x, id, p)
	if e.cfg.Publisher != nil && e.cfg.Signer != nil {
		return e.publishProjection(ctx, s.projection)
	}
	return nil
}

func projectAssistantExecution(x domain.AssistantExecution, id string, p domain.AssistantSessionV2) domain.AssistantSessionV2 {
	p.Schema = domain.AssistantSessionSchemaV2
	p.SessionID = x.SessionID
	p.ExecutionVersion = domain.AssistantExecutionVersion
	p.Workflow = x.Workflow
	p.CurrentRunID = x.RunID
	p.CurrentTurnID = x.TurnID
	p.CurrentRequestID = x.RequestID
	p.ExecutionRevision = x.Revision
	p.Phase = x.Phase
	p.CheckpointEventID = id
	p.Scope, _ = x.Scope.ApprovalScope()
	p.Proposal = x.Proposal
	p.PendingApprovals = nil
	p.SubmittedEffects = 0
	p.UncertainEffects = 0
	for _, w := range x.Work {
		switch w.State {
		case domain.AssistantWorkWaitingAsync, domain.AssistantWorkObserved:
			p.SubmittedEffects++
		case domain.AssistantWorkUncertain, domain.AssistantWorkDispatching:
			p.UncertainEffects++
		case domain.AssistantWorkAwaitingApproval:
			p.PendingApprovals = append(p.PendingApprovals, w.WorkID)
		}
	}
	if x.Phase == domain.AssistantExecutionAwaitingApproval && x.Workflow == domain.AssistantWorkflowBatch && x.Proposal != nil {
		p.PendingApprovals = []string{x.Proposal.ProposalID}
	}
	switch x.Phase {
	case domain.AssistantExecutionProposing:
		p.State = domain.AssistantSessionStatePlanning
	case domain.AssistantExecutionAwaitingApproval:
		p.State = domain.AssistantSessionStateAwaitingApproval
	case domain.AssistantExecutionExecuting, domain.AssistantExecutionWaitingAsync, domain.AssistantExecutionCancelling:
		p.State = domain.AssistantSessionStateExecuting
	case domain.AssistantExecutionCompleted, domain.AssistantExecutionCancelled:
		p.State = domain.AssistantSessionStateCompleted
	case domain.AssistantExecutionFailed:
		p.State = domain.AssistantSessionStateFailed
	default:
		p.State = domain.AssistantSessionStateBlocked
	}
	return p
}

func (e *AssistantExecutionEngine) publishProjection(ctx context.Context, p domain.AssistantSessionV2) error {
	content, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tags := nostr.Tags{{"d", domain.AssistantSessionSchemaV2 + ":" + p.SessionID}, {domain.AssistantSessionTagSchema, domain.AssistantSessionSchemaV2}, {"session", p.SessionID}, {"agent", p.AssistantID}, {"status", string(p.State)}}
	if p.OperatorPubkey != "" {
		tags = append(tags, nostr.Tag{"p", p.OperatorPubkey, "", "operator"})
	}
	ev := nostr.Event{Kind: nostr.Kind(domain.KindAssistantSessionState), CreatedAt: nostr.Timestamp(e.cfg.Now().UTC().Unix()), Tags: tags, Content: string(content)}
	if err = signGoNostrEvent(ctx, e.cfg.Signer, &ev); err != nil {
		return err
	}
	accepted, err := e.cfg.Publisher.Publish(ctx, ev)
	if err != nil {
		return err
	}
	if accepted == 0 {
		return errors.New("no relay accepted assistant session projection")
	}
	return nil
}

func (e *AssistantExecutionEngine) resultLocked(s *assistantEngineSession, ack string) AssistantTurnResult {
	p := s.projection
	p.Participants = append([]string(nil), p.Participants...)
	return AssistantTurnResult{Session: p, ExecutionRevision: s.execution.Revision, Acknowledgment: ack, PendingEffects: p.SubmittedEffects + p.UncertainEffects}
}

// HydrateProjection supplies public identity fields that intentionally are not
// duplicated in the encrypted execution record. Recovery validates its source.
func (e *AssistantExecutionEngine) HydrateProjection(p domain.AssistantSessionV2) {
	if e == nil || p.SessionID == "" || p.CurrentRunID == "" {
		return
	}
	s := e.session(p.SessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.execution.RunID == "" || s.execution.RunID == p.CurrentRunID {
		s.projection = p
	}
}
