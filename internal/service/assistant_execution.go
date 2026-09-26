package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Engine outcomes surfaced to transport handlers. Each is a stable token so the
// ContextVM layer can map it without string matching on free-form errors.
var (
	ErrAssistantRunInProgress                 = errors.New("run_in_progress")
	ErrAssistantSessionClosed                 = errors.New("session_closed")
	ErrAssistantCheckpointUnconfirmed         = errors.New("checkpoint_unconfirmed")
	ErrAssistantProposalChangedRequiresReview = errors.New("proposal_changed_requires_review")
	ErrAssistantStaleTarget                   = errors.New("stale_target")
	ErrAssistantNotAwaitingApproval           = errors.New("not_awaiting_approval")
	ErrAssistantOperatorMismatch              = errors.New("operator_mismatch")
	ErrAssistantReconciliationRejected        = errors.New("reconciliation_rejected")
)

const defaultAssistantExecutionMaxWorkItems = 48

// AssistantExecutionScopeResolver derives trusted command scope before a model
// sees tools. A command-aware installation must supply one; browser metadata is
// not accepted as an authorization source.
type AssistantExecutionScopeResolver interface {
	ResolveAssistantScope(context.Context, AssistantTurnStartRequest) (domain.AssistantCommandScope, error)
}

// AssistantRequestEvidenceResolver returns a receipt only after fetching the
// exact downstream request event through verified/decrypting transport and
// comparing its tool, effective arguments, attribution and idempotency key to
// the work item. Absence of the event is an error, never "not submitted".
type AssistantRequestEvidenceResolver interface {
	ResolveAssistantRequestEvidence(ctx context.Context, requestEventID string, execution domain.AssistantExecution, work domain.AssistantWorkItem) (*domain.AsyncToolReceipt, error)
}

// AssistantWorkRuntime is the common authorization/dispatch gateway. The
// concrete implementation is AssistantToolRuntime.
type AssistantWorkRuntime interface {
	PrepareWork(context.Context, domain.AssistantExecution, domain.AssistantWorkItem) (AssistantPreparedWork, error)
	DispatchPreparedWork(context.Context, AssistantPreparedWork) (*domain.AssistantToolObservation, *domain.AsyncToolReceipt, error)
}

// AssistantExecutionTranscript appends a logical transcript message at most
// once. AssistantTranscriptStore implements it.
type AssistantExecutionTranscript interface {
	AppendMessageOnce(context.Context, AssistantTranscriptAppend) (*AssistantTranscriptRecord, error)
}

// AssistantExecutionProjectionHydrator seeds public identity fields that the
// encrypted execution record intentionally does not duplicate, together with
// the created_at of the latest published projection (0 when unknown) so the
// per-session projection clock stays monotonic across restarts.
type AssistantExecutionProjectionHydrator interface {
	HydrateProjection(p domain.AssistantSessionV2, publishedAt nostr.Timestamp)
}

type AssistantExecutionEngineConfig struct {
	Store         AssistantCheckpointStore
	Runtime       AssistantWorkRuntime
	Observer      AssistantWorkObserver
	Batch         AssistantBatchProposer
	Iterative     AssistantIterativeProposer
	Transcript    AssistantExecutionTranscript
	ScopeResolver AssistantExecutionScopeResolver
	Evidence      AssistantRequestEvidenceResolver
	// Publisher and Signer publish the public v2 session projection. The
	// projection is a derived read model; its publication is best-effort.
	Publisher AssistantEventPublisher
	Signer    nostr.Signer
	// Subscriber recovers the latest published projection created_at for a
	// session before this process first publishes it (see projection clock).
	Subscriber AssistantRelaySubscriber
	Identity   AssistantIdentity
	// Lifecycle bounds all execution and observation. It is the application
	// context, never a request deadline. Cancelling it is shutdown: nothing is
	// recorded as cancelled or failed.
	Lifecycle context.Context
	Now       func() time.Time
	NewID     func(string) string
	// MaxWorkItems is a per-run backstop. The per-model-call iteration bound
	// belongs to the iterative proposer, which owns model history.
	MaxWorkItems               int
	MaxConsecutiveToolFailures int
	Logger                     *slog.Logger
}

// assistantCheckpointFault fences a session after a checkpoint commit failed.
// The unconfirmed checkpoint may already be on a relay, so no different logical
// checkpoint may follow it; only a retry of the identical signed event heals.
type assistantCheckpointFault struct {
	err      error
	pending  domain.AssistantExecution
	previous string
}

// assistantVolatileDispatch is in-process knowledge about a dispatching item
// whose outcome is not yet checkpointed. It lets an in-process heal avoid
// declaring an item uncertain when this process knows exactly what happened.
type assistantVolatileDispatch struct {
	invoked     bool
	inFlight    bool
	receipt     *domain.AsyncToolReceipt
	observation *domain.AssistantToolObservation
}

type assistantEngineSession struct {
	id                 string
	mu                 sync.Mutex
	execution          domain.AssistantExecution
	checkpoint         string
	projection         domain.AssistantSessionV2
	projectionAt       nostr.Timestamp
	projectionClock    bool
	fault              *assistantCheckpointFault
	volatile           map[string]assistantVolatileDispatch
	observationBlocked map[string]string
	activeCancel       context.CancelFunc
	observing          map[string]context.CancelFunc
	driving            bool
	redrive            bool
}

// AssistantExecutionEngine is the single authoritative per-session v2 writer
// and the only component that dispatches assistant work. Every side effect is
// preceded by an accepted checkpoint; a failed checkpoint fences the session so
// no later side effect can occur until the identical checkpoint is confirmed.
// Locks are never held across model, tool or subscription I/O; checkpoint
// commits happen under the session lock because they are the reservation
// boundary shared with cancellation.
type AssistantExecutionEngine struct {
	cfg      AssistantExecutionEngineConfig
	logger   *slog.Logger
	mu       sync.Mutex
	sessions map[string]*assistantEngineSession
	wg       sync.WaitGroup
}

var (
	_ AssistantTurnEngine                  = (*AssistantExecutionEngine)(nil)
	_ AssistantExecutionProjectionHydrator = (*AssistantExecutionEngine)(nil)
	_ AssistantWorkRuntime                 = (*AssistantToolRuntime)(nil)
	_ AssistantExecutionTranscript         = (*AssistantTranscriptStore)(nil)
)

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
	if cfg.MaxWorkItems <= 0 {
		cfg.MaxWorkItems = defaultAssistantExecutionMaxWorkItems
	}
	if cfg.MaxConsecutiveToolFailures <= 0 {
		cfg.MaxConsecutiveToolFailures = defaultAssistantAgentLoopMaxConsecutiveToolFailures
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &AssistantExecutionEngine{cfg: cfg, logger: logger.With("component", "assistant_execution"), sessions: make(map[string]*assistantEngineSession)}
}

// Wait blocks until every execution and observation goroutine has exited. Call
// it after cancelling the lifecycle context for an orderly shutdown.
func (e *AssistantExecutionEngine) Wait() { e.wg.Wait() }

// Snapshot returns a deep copy of the confirmed execution for a session.
func (e *AssistantExecutionEngine) Snapshot(sessionID string) (domain.AssistantExecution, bool) {
	s := e.session(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.execution.RunID == "" {
		return domain.AssistantExecution{}, false
	}
	x, err := s.execution.Clone()
	return x, err == nil
}

// HydrateProjection supplies public identity fields (operator, participants,
// assistant identity, summary) that are not duplicated in the encrypted
// execution record. Callers must validate the projection's author first.
func (e *AssistantExecutionEngine) HydrateProjection(p domain.AssistantSessionV2, publishedAt nostr.Timestamp) {
	if e == nil || p.SessionID == "" {
		return
	}
	s := e.session(p.SessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if publishedAt > 0 {
		if publishedAt > s.projectionAt {
			s.projectionAt = publishedAt
		}
		s.projectionClock = true
	}
	if s.execution.RunID == "" || s.execution.RunID == p.CurrentRunID {
		p.Participants = append([]string(nil), p.Participants...)
		s.projection = p
	}
}

func (e *AssistantExecutionEngine) session(id string) *assistantEngineSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.sessions[id]
	if s == nil {
		s = &assistantEngineSession{id: id, observing: map[string]context.CancelFunc{}, volatile: map[string]assistantVolatileDispatch{}, observationBlocked: map[string]string{}}
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

// ---------------------------------------------------------------------------
// StartTurn

func (e *AssistantExecutionEngine) StartTurn(ctx context.Context, req AssistantTurnStartRequest) (AssistantTurnResult, error) {
	if err := e.ready(); err != nil {
		return AssistantTurnResult{}, err
	}
	prompt := req.Prompt
	if prompt.SessionID == "" || prompt.TurnID == "" || strings.TrimSpace(prompt.Prompt) == "" || req.OperatorPubkey == "" || req.RequestEventID == "" {
		return AssistantTurnResult{}, errors.New("assistant turn identity or prompt missing")
	}
	workflow := prompt.Workflow
	if workflow == "" && req.ExistingSession != nil {
		workflow = req.ExistingSession.Workflow
	}
	if workflow == "" {
		workflow = req.DefaultWorkflow
	}
	if !workflow.Valid() {
		return AssistantTurnResult{}, errors.New("assistant workflow missing or invalid")
	}
	scope := domain.AssistantCommandScope{SelectedRefs: append([]string(nil), prompt.SelectedRefs...)}
	if e.cfg.ScopeResolver != nil {
		var err error
		if scope, err = e.cfg.ScopeResolver.ResolveAssistantScope(e.cfg.Lifecycle, req); err != nil {
			return AssistantTurnResult{}, err
		}
	}
	scope, err := scope.Clone()
	if err != nil {
		return AssistantTurnResult{}, err
	}
	s := e.session(prompt.SessionID)
	e.ensureProjectionClock(ctx, s)
	s.mu.Lock()
	if s.execution.RunID != "" && s.execution.RequestID == req.RequestEventID {
		result := e.resultLocked(s, "duplicate_turn")
		s.mu.Unlock()
		return result, nil
	}
	if err := e.healLocked(s); err != nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, err
	}
	if s.execution.RunID != "" && !assistantPhaseFinished(s.execution.Phase) {
		s.mu.Unlock()
		return AssistantTurnResult{}, ErrAssistantRunInProgress
	}
	if req.ExistingSession != nil && req.ExistingSession.CurrentRunID != "" && req.ExistingSession.CurrentRunID != s.execution.RunID && !assistantPhaseFinished(req.ExistingSession.Phase) {
		s.mu.Unlock()
		return AssistantTurnResult{}, ErrAssistantRunInProgress
	}
	if c := s.execution.Cancellation; c != nil && c.Scope == "session" {
		s.mu.Unlock()
		return AssistantTurnResult{}, ErrAssistantSessionClosed
	}
	runID := e.cfg.NewID("run")
	x := domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: prompt.SessionID, RunID: runID, TurnID: prompt.TurnID, RequestID: req.RequestEventID, Workflow: workflow, Phase: domain.AssistantExecutionProposing, Scope: scope, Work: []domain.AssistantWorkItem{}}
	if s.projection.SessionID != prompt.SessionID {
		s.projection = domain.AssistantSessionV2{SessionID: prompt.SessionID, OperatorPubkey: req.OperatorPubkey, AssistantID: e.cfg.Identity.AgentID, AssistantPubkey: e.cfg.Identity.Pubkey}
		if req.ExistingSession != nil {
			s.projection.Participants = append([]string(nil), req.ExistingSession.Participants...)
			s.projection.TranscriptSummary = req.ExistingSession.TranscriptSummary
			if req.ExistingSession.OperatorPubkey != "" {
				s.projection.OperatorPubkey = req.ExistingSession.OperatorPubkey
			}
		}
	}
	if !assistantProjectionAuthorizes(s.projection, req.OperatorPubkey) {
		s.mu.Unlock()
		return AssistantTurnResult{}, ErrAssistantOperatorMismatch
	}
	if err = e.commitLocked(s, x); err != nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, err
	}
	token := s.execution.Revision
	modelCtx, cancel := context.WithCancel(e.cfg.Lifecycle)
	s.activeCancel = cancel
	s.mu.Unlock()

	proposalReq := AssistantProposalRequest{SessionID: x.SessionID, RunID: runID, TurnID: x.TurnID, Prompt: prompt.Prompt, Scope: scope}
	var proposal AssistantProposal
	var proposeErr error
	switch {
	case workflow == domain.AssistantWorkflowBatch && e.cfg.Batch != nil:
		proposal, proposeErr = e.cfg.Batch.ProposeBatch(modelCtx, proposalReq)
	case workflow == domain.AssistantWorkflowIterative && e.cfg.Iterative != nil:
		proposal, proposeErr = e.cfg.Iterative.ProposeIterative(modelCtx, proposalReq)
	default:
		proposeErr = fmt.Errorf("assistant %s proposer is not configured", workflow)
	}
	cancel()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeCancel = nil
	if s.fault != nil || s.execution.RunID != runID || s.execution.Revision != token || s.execution.Cancellation != nil {
		// A cancellation or fault won the race; the late proposal is discarded.
		return e.resultLocked(s, "proposal_discarded"), nil
	}
	next, ack := e.applyProposal(s.execution, proposal, proposeErr)
	if err = e.commitLocked(s, next); err != nil {
		return e.resultLocked(s, ack), err
	}
	if next.Phase == domain.AssistantExecutionExecuting {
		e.kickLocked(s)
	}
	return e.resultLocked(s, ack), nil
}

// applyProposal converts a proposer outcome into the next execution. Invalid
// proposals block the run rather than being partially applied.
func (e *AssistantExecutionEngine) applyProposal(current domain.AssistantExecution, proposal AssistantProposal, proposeErr error) (domain.AssistantExecution, string) {
	next, err := current.Clone()
	if err != nil {
		return current, "clone_failed"
	}
	block := func(reason string) (domain.AssistantExecution, string) {
		next.Phase = domain.AssistantExecutionBlocked
		e.logger.Warn("assistant proposal blocked", "session_id", next.SessionID, "run_id", next.RunID, "reason", reason)
		return next, "proposal_blocked: " + reason
	}
	if proposeErr != nil {
		return block(proposeErr.Error())
	}
	switch proposal.Kind {
	case AssistantProposalBatch:
		if next.Workflow != domain.AssistantWorkflowBatch || proposal.Batch == nil || next.Proposal != nil {
			return block("batch proposal is not valid for this run")
		}
		plan, err := domain.NormalizeAssistantExecutablePlan(*proposal.Batch)
		if err != nil {
			return block("invalid batch plan: " + err.Error())
		}
		approvalScope, err := next.Scope.ApprovalScope()
		if err != nil {
			return block("invalid scope: " + err.Error())
		}
		proposalID := e.cfg.NewID("proposal")
		hash, err := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: domain.AssistantExecutionVersion, SessionID: next.SessionID, RunID: next.RunID, Workflow: next.Workflow, ProposalID: proposalID, Revision: 1, Scope: approvalScope, Plan: plan})
		if err != nil {
			return block("batch hash: " + err.Error())
		}
		next.Proposal = &domain.AssistantProposalRevision{ProposalID: proposalID, Revision: 1, Plan: plan, Hash: hash, RequestID: next.RequestID}
		next.Phase = domain.AssistantExecutionAwaitingApproval
		return next, "awaiting_approval"
	case AssistantProposalCalls:
		if next.Workflow != domain.AssistantWorkflowIterative || len(proposal.Calls) == 0 {
			return block("tool-call proposal is not valid for this run")
		}
		if err := appendAssistantCalls(&next, proposal.Calls); err != nil {
			return block(err.Error())
		}
		next.Phase = domain.AssistantExecutionExecuting
		return next, "executing"
	case AssistantProposalFinal, AssistantProposalClarification:
		next.Phase = domain.AssistantExecutionCompleted
		return next, string(proposal.Kind)
	case AssistantProposalBlocked:
		return block(firstNonEmptyString(proposal.Reason, "proposer blocked"))
	default:
		return block("unknown proposer outcome")
	}
}

// appendAssistantCalls checkpoints every call of one model response, in order,
// before the first executes. Duplicate call IDs are refused, never renamed,
// because the iterative idempotency key is derived from the call ID.
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
		// Model-supplied dispatch keys are ignored; the executor issues keys.
		delete(args, "idempotency_key")
		digest, err := domain.ComputeAssistantArgumentsDigest(args)
		if err != nil {
			return err
		}
		x.Work = append(x.Work, domain.AssistantWorkItem{WorkID: x.RunID + ":" + call.ID, OriginID: call.ID, Ordinal: len(x.Work), ToolName: call.Name, Arguments: args, ArgumentsDigest: digest, State: domain.AssistantWorkReady})
	}
	return nil
}

// ---------------------------------------------------------------------------
// Decide

func (e *AssistantExecutionEngine) Decide(ctx context.Context, req AssistantTurnDecisionRequest) (AssistantTurnResult, error) {
	if err := e.ready(); err != nil {
		return AssistantTurnResult{}, err
	}
	a := req.Approval
	if a.ContractVersion != domain.AssistantExecutionVersion || a.SessionID == "" || a.RunID == "" || req.OperatorPubkey == "" || req.RequestEventID == "" {
		return AssistantTurnResult{}, errors.New("assistant v2 decision identity missing")
	}
	if a.Decision != "approve" && a.Decision != "reject" {
		return AssistantTurnResult{}, errors.New("unsupported assistant approval decision")
	}
	s := e.session(a.SessionID)
	e.ensureProjectionClock(ctx, s)
	s.mu.Lock()
	if assistantDecisionRecorded(s.execution, req.RequestEventID) {
		result := e.resultLocked(s, "duplicate_decision")
		s.mu.Unlock()
		return result, nil
	}
	if err := e.healLocked(s); err != nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, err
	}
	x := s.execution
	switch {
	case x.RunID == "" || a.RunID != x.RunID:
		s.mu.Unlock()
		return AssistantTurnResult{}, ErrAssistantStaleTarget
	case a.Workflow != x.Workflow:
		s.mu.Unlock()
		return AssistantTurnResult{}, fmt.Errorf("%w: workflow mismatch", ErrAssistantStaleTarget)
	case !assistantProjectionAuthorizes(s.projection, req.OperatorPubkey):
		s.mu.Unlock()
		return AssistantTurnResult{}, ErrAssistantOperatorMismatch
	case x.Phase != domain.AssistantExecutionAwaitingApproval:
		s.mu.Unlock()
		return AssistantTurnResult{}, ErrAssistantNotAwaitingApproval
	}
	if x.Workflow == domain.AssistantWorkflowBatch {
		if x.Proposal == nil || a.ProposalID != x.Proposal.ProposalID || a.BaseRevision != x.Proposal.Revision || a.BasePlanHash != x.Proposal.Hash {
			s.mu.Unlock()
			return AssistantTurnResult{}, fmt.Errorf("%w: proposal revision is not current", ErrAssistantStaleTarget)
		}
		if a.Decision == "reject" {
			defer s.mu.Unlock()
			next, err := x.Clone()
			if err != nil {
				return AssistantTurnResult{}, err
			}
			// Plan rejection closes the draft without dispatch; it is not a
			// cancellation of submitted work (there is none).
			next.Phase = domain.AssistantExecutionCancelled
			if err = e.commitLocked(s, next); err != nil {
				return AssistantTurnResult{}, err
			}
			return e.resultLocked(s, "plan_rejected"), nil
		}
		return e.approveBatch(s, req)
	}
	idx := assistantWorkIndex(x, a.ActionID)
	if a.ActionID == "" || idx < 0 || x.Work[idx].State != domain.AssistantWorkAwaitingApproval {
		s.mu.Unlock()
		return AssistantTurnResult{}, fmt.Errorf("%w: action is not awaiting approval", ErrAssistantStaleTarget)
	}
	if a.Decision == "reject" {
		defer s.mu.Unlock()
		next, err := x.Clone()
		if err != nil {
			return AssistantTurnResult{}, err
		}
		w := &next.Work[idx]
		w.Observation = e.deniedObservation(*w, firstNonEmptyString(a.Reason, "operator rejected action"), req.RequestEventID)
		w.State = domain.AssistantWorkObserved
		next.Phase = assistantDerivePhase(next)
		if err = e.commitLocked(s, next); err != nil {
			return AssistantTurnResult{}, err
		}
		e.kickLocked(s)
		return e.resultLocked(s, "action_rejected"), nil
	}
	return e.approveAction(s, req, idx)
}

// approveBatch is entered with s.mu held and releases it while every step is
// prepared through the common runtime. A denied or invalid step rejects the
// whole approval; a hook-changed step publishes a replacement draft instead of
// executing content the operator never saw.
func (e *AssistantExecutionEngine) approveBatch(s *assistantEngineSession, req AssistantTurnDecisionRequest) (AssistantTurnResult, error) {
	a := req.Approval
	x := s.execution
	plan := x.Proposal.Plan
	edited := a.ModifiedPlan != nil
	if edited {
		var err error
		if plan, err = domain.NormalizeAssistantExecutablePlan(*a.ModifiedPlan); err != nil {
			s.mu.Unlock()
			return AssistantTurnResult{}, fmt.Errorf("modified plan invalid: %w", err)
		}
	}
	approvedRev := x.Proposal.Revision
	if edited {
		approvedRev++
	}
	if a.ApprovedRevision != approvedRev {
		s.mu.Unlock()
		return AssistantTurnResult{}, fmt.Errorf("%w: approved revision mismatch", ErrAssistantStaleTarget)
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
		return AssistantTurnResult{}, fmt.Errorf("%w: approved plan hash mismatch", ErrAssistantStaleTarget)
	}
	approved, err := x.Clone()
	if err != nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, err
	}
	previous := *x.Proposal
	approved.Proposal = &domain.AssistantProposalRevision{ProposalID: previous.ProposalID, Revision: approvedRev, Plan: plan, Hash: hash, RequestID: req.RequestEventID}
	if edited {
		approved.Proposal.PreviousRevision = previous.Revision
		approved.Proposal.PreviousHash = previous.Hash
	}
	approved.Work = []domain.AssistantWorkItem{}
	token := x.Revision
	s.mu.Unlock()

	work := make([]domain.AssistantWorkItem, 0, len(plan.Steps))
	replacement, err := domain.NormalizeAssistantExecutablePlan(plan)
	if err != nil {
		return AssistantTurnResult{}, err
	}
	replacementNeeded := false
	for i, step := range plan.Steps {
		args, err := domain.DeepCopyAssistantJSONMap(step.ToolArgs)
		if err != nil {
			return AssistantTurnResult{}, err
		}
		delete(args, "idempotency_key")
		digest, err := domain.ComputeAssistantArgumentsDigest(args)
		if err != nil {
			return AssistantTurnResult{}, err
		}
		scope, err := approved.Scope.Clone()
		if err != nil {
			return AssistantTurnResult{}, err
		}
		item := domain.AssistantWorkItem{WorkID: x.RunID + ":" + step.StepID, OriginID: step.StepID, Ordinal: i, ToolName: step.ToolName, Arguments: args, ArgumentsDigest: digest, State: domain.AssistantWorkReady,
			Authorization: &domain.AssistantAuthorizationBinding{OperatorPubkey: req.OperatorPubkey, DecisionRequestID: req.RequestEventID, ProposalID: approved.Proposal.ProposalID, ProposalRevision: approvedRev, ArgumentsDigest: digest, Scope: scope}}
		prepared, prepErr := e.cfg.Runtime.PrepareWork(e.cfg.Lifecycle, approved, item)
		if errors.Is(prepErr, ErrAssistantApprovedInputChanged) {
			replacement.Steps[i].ToolArgs = prepared.Work.Arguments
			replacementNeeded = true
			continue
		}
		if prepErr != nil {
			return AssistantTurnResult{}, fmt.Errorf("batch approval rejected at step %s: %w", step.StepID, prepErr)
		}
		item.Authorization.Permission = prepared.Permission
		item.IdempotencyKey = prepared.Work.IdempotencyKey
		work = append(work, item)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != nil || s.execution.RunID != x.RunID || s.execution.Revision != token {
		return AssistantTurnResult{}, fmt.Errorf("%w: run changed during approval", ErrAssistantStaleTarget)
	}
	next, err := s.execution.Clone()
	if err != nil {
		return AssistantTurnResult{}, err
	}
	if replacementNeeded {
		replacementRev := approvedRev + 1
		replacementHash, err := domain.ComputeAssistantBatchApprovalHash(domain.AssistantBatchApprovalHashInput{Version: domain.AssistantExecutionVersion, SessionID: x.SessionID, RunID: x.RunID, Workflow: x.Workflow, ProposalID: previous.ProposalID, Revision: replacementRev, Scope: scopePublic, Plan: replacement})
		if err != nil {
			return AssistantTurnResult{}, err
		}
		next.Proposal = &domain.AssistantProposalRevision{ProposalID: previous.ProposalID, Revision: replacementRev, Plan: replacement, Hash: replacementHash, RequestID: req.RequestEventID, PreviousRevision: approvedRev, PreviousHash: hash}
		next.Work = []domain.AssistantWorkItem{}
		next.Phase = domain.AssistantExecutionAwaitingApproval
		if err = e.commitLocked(s, next); err != nil {
			return AssistantTurnResult{}, err
		}
		return e.resultLocked(s, ErrAssistantProposalChangedRequiresReview.Error()), ErrAssistantProposalChangedRequiresReview
	}
	next.Proposal = approved.Proposal
	next.Work = work
	next.Phase = domain.AssistantExecutionExecuting
	if len(work) == 0 {
		next.Phase = domain.AssistantExecutionCompleted
	}
	if err = e.commitLocked(s, next); err != nil {
		return AssistantTurnResult{}, err
	}
	e.kickLocked(s)
	return e.resultLocked(s, "approved"), nil
}

// approveAction binds one iterative work item to the operator decision and
// its exact argument digest. It is entered with s.mu held.
func (e *AssistantExecutionEngine) approveAction(s *assistantEngineSession, req AssistantTurnDecisionRequest, idx int) (AssistantTurnResult, error) {
	snapshot, err := s.execution.Clone()
	if err != nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, err
	}
	scope, err := snapshot.Scope.Clone()
	if err != nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, err
	}
	item := snapshot.Work[idx]
	item.Authorization = &domain.AssistantAuthorizationBinding{OperatorPubkey: req.OperatorPubkey, DecisionRequestID: req.RequestEventID, ActionID: item.WorkID, ArgumentsDigest: item.ArgumentsDigest, Scope: scope}
	token := snapshot.Revision
	s.mu.Unlock()

	prepared, prepErr := e.cfg.Runtime.PrepareWork(e.cfg.Lifecycle, snapshot, item)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fault != nil || s.execution.RunID != snapshot.RunID || s.execution.Revision != token {
		return AssistantTurnResult{}, fmt.Errorf("%w: run changed during approval", ErrAssistantStaleTarget)
	}
	next, err := s.execution.Clone()
	if err != nil {
		return AssistantTurnResult{}, err
	}
	w := &next.Work[idx]
	ack := "approved"
	var denial *AssistantWorkDenial
	switch {
	case prepErr == nil:
		item.Authorization.Permission = prepared.Permission
		w.Authorization = item.Authorization
		w.IdempotencyKey = prepared.Work.IdempotencyKey
		w.State = domain.AssistantWorkReady
	case errors.Is(prepErr, ErrAssistantApprovedInputChanged):
		w.Observation = e.deniedObservation(*w, ErrAssistantApprovedInputChanged.Error(), req.RequestEventID)
		w.State = domain.AssistantWorkObserved
		ack = ErrAssistantApprovedInputChanged.Error()
	case errors.As(prepErr, &denial):
		// Approval cannot override a hard deny, scope or current policy.
		w.Observation = e.deniedObservation(*w, denial.Reason, req.RequestEventID)
		w.State = domain.AssistantWorkObserved
		ack = "action_denied"
	default:
		return AssistantTurnResult{}, prepErr
	}
	next.Phase = assistantDerivePhase(next)
	if err = e.commitLocked(s, next); err != nil {
		return AssistantTurnResult{}, err
	}
	e.kickLocked(s)
	return e.resultLocked(s, ack), nil
}

// ---------------------------------------------------------------------------
// Cancel

func (e *AssistantExecutionEngine) Cancel(ctx context.Context, req AssistantTurnCancellationRequest) (AssistantTurnResult, error) {
	if err := e.ready(); err != nil {
		return AssistantTurnResult{}, err
	}
	c := req.Cancellation
	if c.ContractVersion != domain.AssistantExecutionVersion || c.SessionID == "" || c.RunID == "" || req.OperatorPubkey == "" || req.RequestEventID == "" || (c.Scope != "run" && c.Scope != "session") {
		return AssistantTurnResult{}, errors.New("invalid assistant cancellation")
	}
	s := e.session(c.SessionID)
	e.ensureProjectionClock(ctx, s)
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec := s.execution.Cancellation; rec != nil && rec.RequestID == req.RequestEventID {
		return e.resultLocked(s, "duplicate_cancellation"), nil
	}
	if err := e.healLocked(s); err != nil {
		return AssistantTurnResult{}, err
	}
	x := s.execution
	if x.RunID == "" || c.RunID != x.RunID {
		// A delayed cancellation must never stop a newer run.
		return AssistantTurnResult{}, ErrAssistantStaleTarget
	}
	if !assistantProjectionAuthorizes(s.projection, req.OperatorPubkey) {
		return AssistantTurnResult{}, ErrAssistantOperatorMismatch
	}
	if x.Cancellation != nil {
		return e.resultLocked(s, "cancellation_already_recorded"), nil
	}
	if assistantPhaseFinished(x.Phase) && c.Scope != "session" {
		return e.resultLocked(s, "run_already_finished"), nil
	}
	next, err := x.Clone()
	if err != nil {
		return AssistantTurnResult{}, err
	}
	next.Cancellation = &domain.AssistantExecutionCancellation{Scope: c.Scope, RunID: x.RunID, OperatorPubkey: req.OperatorPubkey, RequestID: req.RequestEventID, Reason: c.Reason, RecordedAt: e.cfg.Now().UTC()}
	if assistantPhaseFinished(x.Phase) {
		// Closing the session to new turns keeps the finished run's outcome.
		if err = e.commitLocked(s, next); err != nil {
			return AssistantTurnResult{}, err
		}
		return e.resultLocked(s, "session_closed"), nil
	}
	for i := range next.Work {
		switch next.Work[i].State {
		case domain.AssistantWorkPending, domain.AssistantWorkReady, domain.AssistantWorkAwaitingApproval:
			next.Work[i].State = domain.AssistantWorkSkipped
		}
	}
	next.Phase = assistantDerivePhase(next)
	if err = e.commitLocked(s, next); err != nil {
		return AssistantTurnResult{}, err
	}
	if s.activeCancel != nil {
		s.activeCancel()
	}
	// Already-submitted work keeps being observed for accounting; any
	// observed-but-unconsumed item is finalized by the driver.
	e.kickLocked(s)
	return e.resultLocked(s, string(next.Phase)), nil
}

// ---------------------------------------------------------------------------
// Reconcile

func (e *AssistantExecutionEngine) Reconcile(ctx context.Context, req AssistantTurnReconciliationRequest) (AssistantTurnResult, error) {
	if err := e.ready(); err != nil {
		return AssistantTurnResult{}, err
	}
	q := req.Reconciliation
	if q.ContractVersion != domain.AssistantExecutionVersion || q.SessionID == "" || q.RunID == "" || q.WorkID == "" || q.RequestEventID == "" || req.OperatorPubkey == "" {
		return AssistantTurnResult{}, errors.New("invalid assistant reconciliation")
	}
	s := e.session(q.SessionID)
	e.ensureProjectionClock(ctx, s)
	s.mu.Lock()
	if idx := assistantWorkIndex(s.execution, q.WorkID); idx >= 0 && s.execution.RunID == q.RunID {
		if w := s.execution.Work[idx]; w.Receipt != nil && w.Receipt.RequestEventID == q.RequestEventID && w.State != domain.AssistantWorkUncertain {
			result := e.resultLocked(s, "already_reconciled")
			s.mu.Unlock()
			return result, nil
		}
	}
	if err := e.healLocked(s); err != nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, err
	}
	if e.cfg.Evidence == nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, errors.New("verified assistant request evidence resolver missing")
	}
	x := s.execution
	idx := assistantWorkIndex(x, q.WorkID)
	if x.RunID == "" || q.RunID != x.RunID || idx < 0 || x.Work[idx].State != domain.AssistantWorkUncertain {
		s.mu.Unlock()
		return AssistantTurnResult{}, fmt.Errorf("%w: work item is not uncertain", ErrAssistantStaleTarget)
	}
	if !assistantProjectionAuthorizes(s.projection, req.OperatorPubkey) {
		s.mu.Unlock()
		return AssistantTurnResult{}, ErrAssistantOperatorMismatch
	}
	snapshot, err := x.Clone()
	if err != nil {
		s.mu.Unlock()
		return AssistantTurnResult{}, err
	}
	work := snapshot.Work[idx]
	expectedKey := assistantExpectedIdempotencyKey(snapshot, work)
	s.mu.Unlock()

	receipt, err := e.cfg.Evidence.ResolveAssistantRequestEvidence(e.cfg.Lifecycle, q.RequestEventID, snapshot, work)
	if err != nil {
		// Missing evidence leaves the item uncertain; it never becomes a
		// failure or a license to redispatch.
		return AssistantTurnResult{}, fmt.Errorf("%w: %v", ErrAssistantReconciliationRejected, err)
	}
	if receipt == nil || receipt.RequestEventID != q.RequestEventID || receipt.ToolName != work.ToolName || receipt.IdempotencyKey != expectedKey || receipt.RequestKind <= 0 || len(receipt.ResultKinds) == 0 {
		return AssistantTurnResult{}, fmt.Errorf("%w: evidence does not match work identity", ErrAssistantReconciliationRejected)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	idx = assistantWorkIndex(s.execution, q.WorkID)
	if s.fault != nil || s.execution.RunID != q.RunID || idx < 0 || s.execution.Work[idx].State != domain.AssistantWorkUncertain {
		return AssistantTurnResult{}, fmt.Errorf("%w: reconciliation superseded", ErrAssistantStaleTarget)
	}
	next, err := s.execution.Clone()
	if err != nil {
		return AssistantTurnResult{}, err
	}
	w := &next.Work[idx]
	w.Receipt = cloneAsyncToolReceipt(receipt)
	w.IdempotencyKey = expectedKey
	w.State = domain.AssistantWorkWaitingAsync
	next.Phase = assistantDerivePhase(next)
	if err = e.commitLocked(s, next); err != nil {
		return AssistantTurnResult{}, err
	}
	e.startObserverLocked(s, next.RunID, w.WorkID, w.Receipt)
	return e.resultLocked(s, "reconciled"), nil
}

// ---------------------------------------------------------------------------
// Recover

// Recover is the single resume entry for restart, conversion and in-process
// healing. It loads the newest valid checkpoint chain (unless this process
// already owns the run), converts receipt-less dispatching work to uncertain,
// restarts one observer per submitted receipt and resumes eligible work.
func (e *AssistantExecutionEngine) Recover(ctx context.Context, ref AssistantExecutionReference) error {
	if err := e.ready(); err != nil {
		return err
	}
	if ref.SessionID == "" || ref.RunID == "" {
		return errors.New("assistant recovery reference missing session or run")
	}
	s := e.session(ref.SessionID)
	e.ensureProjectionClock(ctx, s)
	s.mu.Lock()
	if err := e.healLocked(s); err != nil {
		s.mu.Unlock()
		return err
	}
	owned := s.execution.RunID == ref.RunID
	if !owned && s.execution.RunID != "" && !assistantPhaseFinished(s.execution.Phase) {
		s.mu.Unlock()
		return fmt.Errorf("%w: session already owns run %s", ErrAssistantRunInProgress, s.execution.RunID)
	}
	s.mu.Unlock()
	if !owned {
		head, err := e.cfg.Store.Load(ctx, ref.SessionID, ref.RunID)
		if err != nil {
			return err
		}
		if ref.CheckpointEventID != "" && !head.Contains(ref.CheckpointEventID) {
			// The projection was published only after this checkpoint was
			// accepted; its absence means incomplete relay history. Park.
			return fmt.Errorf("assistant projection references checkpoint %s missing from the validated journal", ref.CheckpointEventID)
		}
		if head.Execution.SessionID != ref.SessionID || head.Execution.RunID != ref.RunID {
			return errors.New("assistant checkpoint identity does not match recovery reference")
		}
		s.mu.Lock()
		switch {
		case s.execution.RunID == ref.RunID && s.execution.Revision >= head.Execution.Revision:
			// Adopted concurrently; the in-process record is authoritative.
		case s.execution.RunID != "" && s.execution.RunID != ref.RunID && !assistantPhaseFinished(s.execution.Phase):
			s.mu.Unlock()
			return fmt.Errorf("%w: session already owns run %s", ErrAssistantRunInProgress, s.execution.RunID)
		default:
			s.execution = head.Execution
			s.checkpoint = head.EventID
			s.fault = nil
			s.volatile = map[string]assistantVolatileDispatch{}
			s.observationBlocked = map[string]string{}
			e.refreshProjectionLocked(s)
		}
	} else {
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	if err := e.normalizeInFlightLocked(s); err != nil {
		return err
	}
	for _, w := range s.execution.Work {
		if w.State == domain.AssistantWorkWaitingAsync && w.Receipt != nil {
			e.startObserverLocked(s, s.execution.RunID, w.WorkID, w.Receipt)
		}
	}
	e.kickLocked(s)
	return nil
}

// ---------------------------------------------------------------------------
// Driver: one goroutine per session advances the cursor.

func (e *AssistantExecutionEngine) kickLocked(s *assistantEngineSession) {
	if s.driving {
		s.redrive = true
		return
	}
	if e.cfg.Lifecycle.Err() != nil {
		return
	}
	s.driving = true
	e.wg.Add(1)
	go e.drive(s)
}

func (e *AssistantExecutionEngine) drive(s *assistantEngineSession) {
	defer e.wg.Done()
	for {
		if e.step(s) {
			continue
		}
		s.mu.Lock()
		if s.redrive {
			s.redrive = false
			s.mu.Unlock()
			continue
		}
		s.driving = false
		s.mu.Unlock()
		return
	}
}

// step performs one unit of progress and reports whether to continue.
func (e *AssistantExecutionEngine) step(s *assistantEngineSession) bool {
	s.mu.Lock()
	if s.fault != nil || e.cfg.Lifecycle.Err() != nil || s.execution.RunID == "" {
		s.mu.Unlock()
		return false
	}
	x := s.execution
	// A durable observation is consumed before anything else, even while
	// cancelling or parked for accounting.
	for _, w := range x.Work {
		if w.State == domain.AssistantWorkObserved {
			s.mu.Unlock()
			return e.consume(s, x.RunID, w.WorkID)
		}
	}
	if x.Phase != domain.AssistantExecutionExecuting || x.Cancellation != nil || assistantAccountingOnly(x) {
		s.mu.Unlock()
		return false
	}
	if x.Cursor >= len(x.Work) {
		if x.Workflow == domain.AssistantWorkflowIterative {
			return e.continueIterative(s)
		}
		defer s.mu.Unlock()
		next, err := x.Clone()
		if err != nil {
			return false
		}
		next.Phase = assistantDerivePhase(next)
		if next.Phase != x.Phase {
			_ = e.commitLocked(s, next)
		}
		return false
	}
	if x.Work[x.Cursor].State != domain.AssistantWorkReady {
		s.mu.Unlock()
		return false
	}
	snapshot, err := x.Clone()
	s.mu.Unlock()
	if err != nil {
		return false
	}
	return e.dispatchReady(s, snapshot, snapshot.Cursor)
}

// dispatchReady authorizes the cursor item, commits the dispatching
// reservation and only then invokes the provider. A reservation that is not
// confirmed by a relay is never followed by a tool call.
func (e *AssistantExecutionEngine) dispatchReady(s *assistantEngineSession, snapshot domain.AssistantExecution, idx int) bool {
	prepared, prepErr := e.cfg.Runtime.PrepareWork(e.cfg.Lifecycle, snapshot, snapshot.Work[idx])
	s.mu.Lock()
	if s.fault != nil || s.execution.RunID != snapshot.RunID || s.execution.Revision != snapshot.Revision {
		s.mu.Unlock()
		return true // state moved (for example cancelled); re-evaluate
	}
	next, err := s.execution.Clone()
	if err != nil {
		s.mu.Unlock()
		return false
	}
	w := &next.Work[idx]
	var denial *AssistantWorkDenial
	switch {
	case prepErr == nil:
		w.Arguments = prepared.Work.Arguments
		w.ArgumentsDigest = prepared.Work.ArgumentsDigest
		w.IdempotencyKey = prepared.Work.IdempotencyKey
		w.State = domain.AssistantWorkDispatching
		workID := w.WorkID
		if err := e.commitLocked(s, next); err != nil {
			s.volatile[workID] = assistantVolatileDispatch{}
			s.mu.Unlock()
			return false
		}
		s.volatile[workID] = assistantVolatileDispatch{invoked: true, inFlight: true}
		prepared.Work.IdempotencyKey = w.IdempotencyKey
		runID := next.RunID
		s.mu.Unlock()
		return e.invoke(s, runID, workID, prepared)
	case errors.Is(prepErr, ErrAssistantApprovalRequired) && next.Workflow == domain.AssistantWorkflowIterative:
		// The operator approves the effective (hook-transformed) input.
		w.Arguments = prepared.Work.Arguments
		w.ArgumentsDigest = prepared.Work.ArgumentsDigest
		w.State = domain.AssistantWorkAwaitingApproval
		next.Phase = domain.AssistantExecutionAwaitingApproval
		_ = e.commitLocked(s, next)
		s.mu.Unlock()
		return false
	case errors.Is(prepErr, ErrAssistantApprovedInputChanged):
		w.Observation = e.deniedObservation(*w, ErrAssistantApprovedInputChanged.Error(), "")
		w.State = domain.AssistantWorkObserved
	case errors.As(prepErr, &denial):
		w.Observation = e.deniedObservation(*w, denial.Reason, "")
		w.State = domain.AssistantWorkObserved
	default:
		e.logger.Error("assistant work preparation failed; run parked", "session_id", next.SessionID, "run_id", next.RunID, "work_id", w.WorkID, "error", prepErr)
		next.Phase = domain.AssistantExecutionBlocked
		_ = e.commitLocked(s, next)
		s.mu.Unlock()
		return false
	}
	next.Phase = assistantDerivePhase(next)
	ok := e.commitLocked(s, next) == nil
	s.mu.Unlock()
	return ok
}

// invoke calls the provider exactly once for a confirmed reservation and
// records a receipt, an observation, or uncertainty.
func (e *AssistantExecutionEngine) invoke(s *assistantEngineSession, runID, workID string, prepared AssistantPreparedWork) bool {
	obs, receipt, dispatchErr := e.cfg.Runtime.DispatchPreparedWork(e.cfg.Lifecycle, prepared)
	if dispatchErr != nil {
		e.logger.Warn("assistant dispatch outcome ambiguous; work becomes uncertain", "session_id", s.id, "run_id", runID, "work_id", workID, "error", dispatchErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := assistantVolatileDispatch{invoked: true, receipt: receipt, observation: obs}
	s.volatile[workID] = v
	if s.fault != nil || s.execution.RunID != runID || e.cfg.Lifecycle.Err() != nil {
		// Shutdown or fence: record nothing; recovery classifies the item.
		return false
	}
	idx := assistantWorkIndex(s.execution, workID)
	if idx < 0 || s.execution.Work[idx].State != domain.AssistantWorkDispatching {
		return false
	}
	next, err := s.execution.Clone()
	if err != nil {
		return false
	}
	w := &next.Work[idx]
	applyAssistantDispatchResult(w, v)
	next.Phase = assistantDerivePhase(next)
	if err = e.commitLocked(s, next); err != nil {
		return false
	}
	delete(s.volatile, workID)
	if w.State == domain.AssistantWorkWaitingAsync {
		e.startObserverLocked(s, runID, workID, w.Receipt)
	}
	return true
}

func applyAssistantDispatchResult(w *domain.AssistantWorkItem, v assistantVolatileDispatch) {
	switch {
	case v.receipt != nil:
		w.Receipt = cloneAsyncToolReceipt(v.receipt)
		w.State = domain.AssistantWorkWaitingAsync
	case v.observation != nil:
		w.Observation = v.observation
		w.State = domain.AssistantWorkObserved
	default:
		// Submission may have happened; never replay automatically.
		w.State = domain.AssistantWorkUncertain
	}
}

// consume appends a durable observation to the transcript exactly once, then
// finalizes the item. The logical transcript identity makes a replay after a
// crash between those two steps harmless.
func (e *AssistantExecutionEngine) consume(s *assistantEngineSession, runID, workID string) bool {
	s.mu.Lock()
	idx := assistantWorkIndex(s.execution, workID)
	if s.fault != nil || s.execution.RunID != runID || idx < 0 || s.execution.Work[idx].State != domain.AssistantWorkObserved {
		s.mu.Unlock()
		return false
	}
	snapshot, err := s.execution.Clone()
	operator := s.projection.OperatorPubkey
	s.mu.Unlock()
	if err != nil {
		return false
	}
	work := snapshot.Work[idx]
	if e.cfg.Transcript != nil && work.Observation != nil {
		logical := runID + ":" + workID + ":observation"
		_, appendErr := e.cfg.Transcript.AppendMessageOnce(e.cfg.Lifecycle, AssistantTranscriptAppend{LogicalID: logical, SessionID: snapshot.SessionID, TurnID: snapshot.TurnID, RunID: runID, OperatorPubkey: operator,
			Message: domain.AssistantAgentMessage{ID: logical, Role: domain.AssistantAgentMessageRoleTool, ToolCallID: work.OriginID, Name: work.ToolName, Observation: work.Observation}})
		if appendErr != nil {
			if e.cfg.Lifecycle.Err() != nil {
				return false
			}
			e.logger.Error("assistant observation transcript append failed; run parked", "session_id", snapshot.SessionID, "run_id", runID, "work_id", workID, "error", appendErr)
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.execution.RunID == runID && s.execution.Revision == snapshot.Revision && s.execution.Phase != domain.AssistantExecutionBlocked {
				if next, err := s.execution.Clone(); err == nil {
					next.Phase = domain.AssistantExecutionBlocked
					_ = e.commitLocked(s, next)
				}
			}
			return false
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx = assistantWorkIndex(s.execution, workID)
	if s.fault != nil || s.execution.RunID != runID || idx < 0 || s.execution.Work[idx].State != domain.AssistantWorkObserved {
		return false
	}
	next, err := s.execution.Clone()
	if err != nil {
		return false
	}
	w := &next.Work[idx]
	switch w.Observation.Status {
	case domain.AssistantToolObservationSucceeded:
		w.State = domain.AssistantWorkSucceeded
	case domain.AssistantToolObservationDenied:
		w.State = domain.AssistantWorkDenied
	case domain.AssistantToolObservationCancelled:
		w.State = domain.AssistantWorkSkipped
	default:
		w.State = domain.AssistantWorkFailed
	}
	if next.Workflow == domain.AssistantWorkflowBatch && next.Cancellation == nil && (w.State == domain.AssistantWorkFailed || w.State == domain.AssistantWorkDenied) {
		// A failed or denied batch step stops the batch.
		for i := range next.Work {
			switch next.Work[i].State {
			case domain.AssistantWorkPending, domain.AssistantWorkReady, domain.AssistantWorkAwaitingApproval:
				next.Work[i].State = domain.AssistantWorkSkipped
			}
		}
	}
	next.Phase = assistantDerivePhase(next)
	return e.commitLocked(s, next) == nil
}

// continueIterative is entered with s.mu held and returns with it released.
// It requests further reasoning only after every call of the previous response
// has a consumed observation.
func (e *AssistantExecutionEngine) continueIterative(s *assistantEngineSession) bool {
	x := s.execution
	next, err := x.Clone()
	if err != nil {
		s.mu.Unlock()
		return false
	}
	reason := ""
	switch {
	case assistantTrailingFailures(x.Work) >= e.cfg.MaxConsecutiveToolFailures:
		reason = fmt.Sprintf("max_consecutive_tool_failures=%d", e.cfg.MaxConsecutiveToolFailures)
	case len(x.Work) >= e.cfg.MaxWorkItems:
		reason = fmt.Sprintf("max_work_items=%d", e.cfg.MaxWorkItems)
	case e.cfg.Iterative == nil:
		reason = "iterative proposer not configured"
	}
	if reason != "" {
		e.logger.Warn("assistant iterative run blocked", "session_id", x.SessionID, "run_id", x.RunID, "reason", reason)
		next.Phase = domain.AssistantExecutionBlocked
		_ = e.commitLocked(s, next)
		s.mu.Unlock()
		return false
	}
	next.Phase = domain.AssistantExecutionProposing
	if err = e.commitLocked(s, next); err != nil {
		s.mu.Unlock()
		return false
	}
	token := s.execution.Revision
	ctx, cancel := context.WithCancel(e.cfg.Lifecycle)
	s.activeCancel = cancel
	scope, err := x.Scope.Clone()
	s.mu.Unlock()
	var proposal AssistantProposal
	proposeErr := err
	if proposeErr == nil {
		proposal, proposeErr = e.cfg.Iterative.ProposeIterative(ctx, AssistantProposalRequest{SessionID: x.SessionID, RunID: x.RunID, TurnID: x.TurnID, Scope: scope})
	}
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeCancel = nil
	if s.fault != nil || e.cfg.Lifecycle.Err() != nil || s.execution.RunID != x.RunID || s.execution.Revision != token || s.execution.Cancellation != nil {
		return false // late model response discarded
	}
	after, _ := e.applyProposal(s.execution, proposal, proposeErr)
	if err = e.commitLocked(s, after); err != nil {
		return false
	}
	return after.Phase == domain.AssistantExecutionExecuting
}

// ---------------------------------------------------------------------------
// Observation

func (e *AssistantExecutionEngine) startObserverLocked(s *assistantEngineSession, runID, workID string, receipt *domain.AsyncToolReceipt) {
	if receipt == nil || e.cfg.Lifecycle.Err() != nil {
		return
	}
	key := runID + "\x00" + workID + "\x00" + receipt.RequestEventID
	if s.observing[key] != nil {
		return
	}
	ctx, cancel := context.WithCancel(e.cfg.Lifecycle)
	s.observing[key] = cancel
	req := AssistantWorkObservationRequest{SessionID: s.id, RunID: runID, WorkID: workID, ToolName: receipt.ToolName, Receipt: cloneAsyncToolReceipt(receipt)}
	req.OnSignal = func(sig AssistantObservationSignal) { e.observationSignal(s, runID, workID, sig) }
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		outcome, err := e.cfg.Observer.ObserveWork(ctx, req)
		s.mu.Lock()
		defer s.mu.Unlock()
		stopped := ctx.Err() != nil
		cancel()
		delete(s.observing, key)
		if stopped {
			// Shutdown closes subscriptions without recording any outcome.
			return
		}
		if err != nil || (outcome.Status != "completed" && outcome.Status != "failed") {
			reason := "observer ended without a terminal event"
			if err != nil {
				reason = err.Error()
			}
			e.setObservationBlockedLocked(s, runID, workID, reason)
			return
		}
		if e.recordObservationLocked(s, runID, workID, req.Receipt, outcome) {
			e.kickLocked(s)
		}
	}()
}

// recordObservationLocked persists a terminal downstream event once. A second
// terminal event, or a replay of the same one, finds the item no longer
// waiting and changes nothing.
func (e *AssistantExecutionEngine) recordObservationLocked(s *assistantEngineSession, runID, workID string, receipt *domain.AsyncToolReceipt, outcome AssistantAsyncObservationOutcome) bool {
	idx := assistantWorkIndex(s.execution, workID)
	if s.fault != nil || s.execution.RunID != runID || idx < 0 {
		return false
	}
	current := s.execution.Work[idx]
	if current.State != domain.AssistantWorkWaitingAsync || current.Receipt == nil || current.Receipt.RequestEventID != receipt.RequestEventID {
		return false
	}
	next, err := s.execution.Clone()
	if err != nil {
		return false
	}
	w := &next.Work[idx]
	status := domain.AssistantToolObservationSucceeded
	if outcome.Status == "failed" {
		status = domain.AssistantToolObservationFailed
	}
	result, content := assistantObservationFromEvent(outcome.Event)
	obs := &domain.AssistantToolObservation{ObservationID: e.cfg.NewID("obs"), ToolCallID: w.OriginID, ToolName: w.ToolName, Status: status, ExecutionMode: domain.AssistantToolExecutionModeAsync, Summary: "downstream result " + outcome.Status, Receipt: cloneAsyncToolReceipt(w.Receipt), Result: result, Content: content, ObservedAt: e.cfg.Now().UTC()}
	if outcome.Event != nil {
		obs.EventID = outcome.Event.ID.Hex()
	}
	w.Observation = obs
	w.State = domain.AssistantWorkObserved
	delete(s.observationBlocked, workID)
	next.Phase = assistantDerivePhase(next)
	return e.commitLocked(s, next) == nil
}

func (e *AssistantExecutionEngine) observationSignal(s *assistantEngineSession, runID, workID string, sig AssistantObservationSignal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.execution.RunID != runID {
		return
	}
	if sig.Blocked {
		e.setObservationBlockedLocked(s, runID, workID, sig.Reason)
		return
	}
	if _, blocked := s.observationBlocked[workID]; blocked {
		delete(s.observationBlocked, workID)
		e.refreshProjectionLocked(s)
	}
}

// setObservationBlockedLocked records loss of relay visibility as volatile
// projection state. It is not a checkpoint: the item is still waiting, and a
// relay outage must not fence the journal.
func (e *AssistantExecutionEngine) setObservationBlockedLocked(s *assistantEngineSession, runID, workID, reason string) {
	if s.observationBlocked[workID] == reason {
		return
	}
	s.observationBlocked[workID] = reason
	e.logger.Warn("assistant observation blocked", "session_id", s.id, "run_id", runID, "work_id", workID, "reason", reason)
	e.refreshProjectionLocked(s)
}

// ---------------------------------------------------------------------------
// Checkpoint commit, fencing and healing

// commitLocked is the only writer of s.execution. The execution is adopted
// only after a relay accepted its checkpoint; on failure the session is fenced.
func (e *AssistantExecutionEngine) commitLocked(s *assistantEngineSession, next domain.AssistantExecution) error {
	if s.fault != nil {
		return fmt.Errorf("%w: %v", ErrAssistantCheckpointUnconfirmed, s.fault.err)
	}
	next.Version = domain.AssistantExecutionVersion
	next.Cursor = assistantFirstUnfinished(next.Work)
	previous := s.checkpoint
	newRun := s.execution.RunID != next.RunID
	if newRun {
		next.Revision = 1
		previous = ""
	} else {
		next.Revision = s.execution.Revision + 1
		if err := validateAssistantExecutionTransition(s.execution, next); err != nil {
			e.logger.Error("assistant execution transition rejected", "session_id", next.SessionID, "run_id", next.RunID, "error", err)
			return err
		}
	}
	snapshot, err := next.Clone()
	if err != nil {
		return err
	}
	id, err := e.cfg.Store.Append(e.cfg.Lifecycle, snapshot, previous)
	if err != nil {
		e.logger.Error("assistant checkpoint not confirmed", "session_id", next.SessionID, "run_id", next.RunID, "revision", next.Revision, "error", err)
		if newRun && len(snapshot.Work) == 0 {
			// A new run's root has no effects; it is simply not adopted.
			return fmt.Errorf("%w: %v", ErrAssistantCheckpointUnconfirmed, err)
		}
		s.fault = &assistantCheckpointFault{err: err, pending: snapshot, previous: previous}
		if s.activeCancel != nil {
			s.activeCancel()
		}
		return fmt.Errorf("%w: %v", ErrAssistantCheckpointUnconfirmed, err)
	}
	e.adoptLocked(s, snapshot, id)
	return nil
}

func (e *AssistantExecutionEngine) adoptLocked(s *assistantEngineSession, x domain.AssistantExecution, id string) {
	if s.execution.RunID != x.RunID {
		s.volatile = map[string]assistantVolatileDispatch{}
		s.observationBlocked = map[string]string{}
	}
	s.execution = x
	s.checkpoint = id
	e.refreshProjectionLocked(s)
}

// healLocked retries the identical unconfirmed checkpoint. Only after a relay
// accepts it is the fence lifted and in-process dispatch knowledge applied.
func (e *AssistantExecutionEngine) healLocked(s *assistantEngineSession) error {
	f := s.fault
	if f == nil {
		return nil
	}
	id, err := e.cfg.Store.Append(e.cfg.Lifecycle, f.pending, f.previous)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAssistantCheckpointUnconfirmed, err)
	}
	s.fault = nil
	e.adoptLocked(s, f.pending, id)
	if err = e.normalizeInFlightLocked(s); err != nil {
		return err
	}
	for _, w := range s.execution.Work {
		if w.State == domain.AssistantWorkWaitingAsync && w.Receipt != nil {
			e.startObserverLocked(s, s.execution.RunID, w.WorkID, w.Receipt)
		}
	}
	e.kickLocked(s)
	return nil
}

// normalizeInFlightLocked resolves dispatching items that no live dispatch in
// this process owns, and interrupted proposing phases.
func (e *AssistantExecutionEngine) normalizeInFlightLocked(s *assistantEngineSession) error {
	next, err := s.execution.Clone()
	if err != nil {
		return err
	}
	changed := false
	for i := range next.Work {
		w := &next.Work[i]
		if w.State != domain.AssistantWorkDispatching {
			continue
		}
		v, known := s.volatile[w.WorkID]
		switch {
		case known && v.inFlight:
			continue
		case known && !v.invoked:
			// This process knows the reservation was never used.
			w.State = domain.AssistantWorkReady
			if next.Cancellation != nil {
				w.State = domain.AssistantWorkSkipped
			}
		case known:
			applyAssistantDispatchResult(w, v)
		default:
			// No durable receipt and no in-process knowledge: the request may
			// have been submitted. Block successors pending reconciliation.
			w.State = domain.AssistantWorkUncertain
		}
		delete(s.volatile, w.WorkID)
		changed = true
	}
	if next.Phase == domain.AssistantExecutionProposing {
		// Interrupted model work. A continuation can be re-requested because
		// every prior call is consumed; an initial proposal cannot, because the
		// prompt is not part of the execution record.
		if next.Workflow == domain.AssistantWorkflowIterative && len(next.Work) > 0 && assistantFirstUnfinished(next.Work) == len(next.Work) && next.Cancellation == nil {
			next.Phase = domain.AssistantExecutionExecuting
		} else {
			next.Phase = domain.AssistantExecutionBlocked
		}
		changed = true
	} else if changed {
		next.Phase = assistantDerivePhase(next)
	}
	if !changed {
		return nil
	}
	return e.commitLocked(s, next)
}

// ---------------------------------------------------------------------------
// Projection

func (e *AssistantExecutionEngine) refreshProjectionLocked(s *assistantEngineSession) {
	p := projectAssistantExecution(s.execution, s.checkpoint, s.projection)
	if p.AssistantID == "" {
		p.AssistantID = e.cfg.Identity.AgentID
	}
	if p.AssistantPubkey == "" {
		p.AssistantPubkey = e.cfg.Identity.Pubkey
	}
	if p.Phase == domain.AssistantExecutionWaitingAsync && len(s.observationBlocked) > 0 {
		p.Phase = domain.AssistantExecutionBlocked
		p.State = domain.AssistantSessionStateBlocked
	}
	s.projection = p
	e.publishProjectionLocked(s)
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
	p.Proposal = nil
	if x.Proposal != nil {
		// Deep copy: projections leave the engine, the execution must not.
		proposal := *x.Proposal
		if plan, err := domain.NormalizeAssistantExecutablePlan(x.Proposal.Plan); err == nil {
			proposal.Plan = plan
		}
		p.Proposal = &proposal
	}
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

// publishProjectionLocked publishes the derived read model.
//
// Projection clock: the v2 projection is a replaceable event, and NIP-01
// resolves equal created_at by the lowest event ID, not the newest. Several
// phase changes routinely happen within one second, so created_at must be
// strictly increasing per session: created_at = max(now, last + 1). The engine
// is the sole writer, so it owns this clock; after a restart the clock is
// recovered from the latest published projection (hydration by recovery, or
// ensureProjectionClock's scoped EOSE lookup at the first operation on the
// session in this process) rather than resetting to wall-clock. Under bursty transitions created_at
// deliberately runs ahead of wall-clock by at most one second per extra
// publication; that is correct for replaceable ordering and stays far inside
// relay future-timestamp tolerances for realistic bursts. Immutable 4903
// checkpoints do not use this clock; they are ordered by predecessor chain.
func (e *AssistantExecutionEngine) publishProjectionLocked(s *assistantEngineSession) {
	if e.cfg.Publisher == nil || e.cfg.Signer == nil || s.projection.SessionID == "" {
		return
	}
	p := s.projection
	content, err := json.Marshal(p)
	if err != nil {
		e.logger.Error("assistant projection encode failed", "session_id", p.SessionID, "error", err)
		return
	}
	created := nostr.Timestamp(e.cfg.Now().UTC().Unix())
	if created <= s.projectionAt {
		created = s.projectionAt + 1
	}
	s.projectionAt = created
	ev := nostr.Event{Kind: nostr.Kind(domain.KindAssistantSessionState), CreatedAt: created, Tags: assistantProjectionTags(p), Content: string(content)}
	if err = signGoNostrEvent(e.cfg.Lifecycle, e.cfg.Signer, &ev); err != nil {
		e.logger.Error("assistant projection sign failed", "session_id", p.SessionID, "error", err)
		return
	}
	accepted, err := e.cfg.Publisher.Publish(e.cfg.Lifecycle, ev)
	if err == nil && accepted == 0 {
		err = errors.New("no relay accepted projection")
	}
	if err != nil {
		e.logger.Warn("assistant projection publication failed; checkpoint remains authoritative", "session_id", p.SessionID, "error", err)
	}
}

// ensureProjectionClock seeds the per-session projection clock from the
// latest published projection before this process first publishes for the
// session. It runs before the session lock is taken so a slow relay cannot
// wedge cancellation; a failed lookup is retried at the next operation.
func (e *AssistantExecutionEngine) ensureProjectionClock(ctx context.Context, s *assistantEngineSession) {
	if e.cfg.Subscriber == nil || e.cfg.Signer == nil || e.cfg.Publisher == nil {
		return
	}
	s.mu.Lock()
	known := s.projectionClock
	s.mu.Unlock()
	if known {
		return
	}
	latest, err := e.latestProjectionAt(ctx, s.id)
	if err != nil {
		e.logger.Warn("assistant projection clock lookup failed; will retry", "session_id", s.id, "error", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if latest > s.projectionAt {
		s.projectionAt = latest
	}
	s.projectionClock = true
}

func assistantProjectionCoordinate(sessionID string) string {
	return domain.AssistantSessionSchemaV2 + ":" + sessionID
}

func assistantProjectionTags(p domain.AssistantSessionV2) nostr.Tags {
	tags := nostr.Tags{{"d", assistantProjectionCoordinate(p.SessionID)}, {domain.AssistantSessionTagSchema, domain.AssistantSessionSchemaV2}, {"session", p.SessionID}, {"agent", p.AssistantID}, {"status", string(p.State)}}
	if p.OperatorPubkey != "" {
		tags = append(tags, nostr.Tag{"p", p.OperatorPubkey, "", "operator"})
	}
	return tags
}

// latestProjectionAt performs one scoped backfill of this service's v2
// projection coordinate for the session and returns the newest created_at.
// CLOSED or a stream ending before EOSE is an error: absence is only trusted
// after a complete backfill.
func (e *AssistantExecutionEngine) latestProjectionAt(ctx context.Context, sessionID string) (nostr.Timestamp, error) {
	author, err := e.cfg.Signer.GetPublicKey(ctx)
	if err != nil {
		return 0, err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(domain.KindAssistantSessionState)}, Authors: []nostr.PubKey{author}, Tags: nostr.TagMap{"d": []string{assistantProjectionCoordinate(sessionID)}}}
	sub, err := e.cfg.Subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return 0, err
	}
	defer sub.Close()
	var latest nostr.Timestamp
	events, closed, eose := sub.EventChan(), sub.ClosedChan(), sub.EOSEChan()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return 0, fmt.Errorf("projection lookup closed by %s: %s", c.RelayURL, c.Reason)
		case ev, ok := <-events:
			if !ok {
				if assistantEOSEReached(eose) {
					return latest, nil
				}
				return 0, errors.New("projection lookup ended before EOSE")
			}
			if ev != nil && ev.PubKey == author && ev.CheckID() && ev.VerifySignature() && ev.CreatedAt > latest {
				latest = ev.CreatedAt
			}
		case <-eose:
			return latest, nil
		}
	}
}

func (e *AssistantExecutionEngine) resultLocked(s *assistantEngineSession, ack string) AssistantTurnResult {
	p := s.projection
	p.Participants = append([]string(nil), p.Participants...)
	p.PendingApprovals = append([]string(nil), p.PendingApprovals...)
	return AssistantTurnResult{Session: p, ExecutionRevision: s.execution.Revision, Acknowledgment: ack, PendingEffects: p.SubmittedEffects + p.UncertainEffects}
}

func (e *AssistantExecutionEngine) deniedObservation(w domain.AssistantWorkItem, reason, decisionRequestID string) *domain.AssistantToolObservation {
	obs := &domain.AssistantToolObservation{ObservationID: e.cfg.NewID("obs"), ToolCallID: w.OriginID, ToolName: w.ToolName, Status: domain.AssistantToolObservationDenied, Summary: "assistant tool denied", Error: reason, ObservedAt: e.cfg.Now().UTC()}
	if decisionRequestID != "" {
		obs.Metadata = map[string]any{"decision_request_id": decisionRequestID}
	}
	return obs
}

// ---------------------------------------------------------------------------
// Pure state helpers

func assistantPhaseFinished(p domain.AssistantExecutionPhase) bool {
	return p == domain.AssistantExecutionCompleted || p == domain.AssistantExecutionFailed || p == domain.AssistantExecutionCancelled
}

func assistantWorkFinished(s domain.AssistantWorkState) bool {
	switch s {
	case domain.AssistantWorkSucceeded, domain.AssistantWorkFailed, domain.AssistantWorkDenied, domain.AssistantWorkSkipped:
		return true
	}
	return false
}

// assistantFirstUnfinished is the cursor: the first item not yet finalized.
func assistantFirstUnfinished(work []domain.AssistantWorkItem) int {
	for i := range work {
		if !assistantWorkFinished(work[i].State) {
			return i
		}
	}
	return len(work)
}

func assistantWorkIndex(x domain.AssistantExecution, workID string) int {
	for i := range x.Work {
		if x.Work[i].WorkID == workID {
			return i
		}
	}
	return -1
}

// assistantAccountingOnly marks migrated runs whose only permitted activity is
// observing already-correlated submissions. They never dispatch or reason.
func assistantAccountingOnly(x domain.AssistantExecution) bool {
	if x.Migration == nil {
		return false
	}
	switch AssistantLegacyClassification(x.Migration.Classification) {
	case AssistantLegacyBatchAccounting, AssistantLegacyIterativeAccounting, AssistantLegacyTerminalAccounting:
		return true
	}
	return false
}

// assistantDerivePhase computes the phase implied by work states once a run
// is past proposal/approval. Cancellation keeps accounting open (cancelling)
// until no submitted or uncertain work remains.
func assistantDerivePhase(x domain.AssistantExecution) domain.AssistantExecutionPhase {
	inFlight, uncertain, awaiting := 0, 0, 0
	failed := false
	for _, w := range x.Work {
		switch w.State {
		case domain.AssistantWorkDispatching, domain.AssistantWorkWaitingAsync, domain.AssistantWorkObserved:
			inFlight++
		case domain.AssistantWorkUncertain:
			uncertain++
		case domain.AssistantWorkAwaitingApproval:
			awaiting++
		case domain.AssistantWorkFailed, domain.AssistantWorkDenied:
			failed = true
		}
	}
	if x.Cancellation != nil {
		if inFlight+uncertain > 0 {
			return domain.AssistantExecutionCancelling
		}
		return domain.AssistantExecutionCancelled
	}
	if assistantAccountingOnly(x) || uncertain > 0 {
		return domain.AssistantExecutionBlocked
	}
	if awaiting > 0 {
		return domain.AssistantExecutionAwaitingApproval
	}
	if cursor := assistantFirstUnfinished(x.Work); cursor < len(x.Work) {
		switch x.Work[cursor].State {
		case domain.AssistantWorkWaitingAsync:
			return domain.AssistantExecutionWaitingAsync
		case domain.AssistantWorkPending:
			return domain.AssistantExecutionBlocked
		default:
			return domain.AssistantExecutionExecuting
		}
	}
	if x.Workflow == domain.AssistantWorkflowBatch {
		if failed {
			return domain.AssistantExecutionFailed
		}
		return domain.AssistantExecutionCompleted
	}
	return domain.AssistantExecutionExecuting
}

func assistantTrailingFailures(work []domain.AssistantWorkItem) int {
	count := 0
	for i := len(work) - 1; i >= 0; i-- {
		if !toolObservationCountsAsFailure(work[i].Observation) {
			break
		}
		count++
	}
	return count
}

func assistantProjectionAuthorizes(p domain.AssistantSessionV2, pubkey string) bool {
	if pubkey == "" {
		return false
	}
	if pubkey == p.OperatorPubkey {
		return true
	}
	for _, participant := range p.Participants {
		if participant == pubkey {
			return true
		}
	}
	return false
}

// assistantDecisionRecorded makes decisions idempotent across relay
// redelivery using identities persisted in the execution itself.
func assistantDecisionRecorded(x domain.AssistantExecution, requestID string) bool {
	if requestID == "" || x.RunID == "" {
		return false
	}
	if x.Proposal != nil && x.Proposal.RequestID == requestID && requestID != x.RequestID {
		return true
	}
	for _, w := range x.Work {
		if w.Authorization != nil && w.Authorization.DecisionRequestID == requestID {
			return true
		}
		if w.Observation != nil && w.Observation.Metadata != nil && w.Observation.Metadata["decision_request_id"] == requestID {
			return true
		}
	}
	return false
}

// assistantExpectedIdempotencyKey is the key a downstream request for this
// work must carry. Migrated keys are preserved byte-for-byte.
func assistantExpectedIdempotencyKey(x domain.AssistantExecution, w domain.AssistantWorkItem) string {
	if w.IdempotencyKey != "" {
		return w.IdempotencyKey
	}
	if x.Migration != nil && x.Migration.LegacyPlanHash != "" && x.Workflow == domain.AssistantWorkflowBatch {
		return fmt.Sprintf("assistant:%s:%s:%s", x.SessionID, x.Migration.LegacyPlanHash, w.OriginID)
	}
	return assistantExecutionIdempotencyKey(x, w)
}

var assistantWorkTransitions = map[domain.AssistantWorkState][]domain.AssistantWorkState{
	domain.AssistantWorkPending:          {domain.AssistantWorkReady, domain.AssistantWorkAwaitingApproval, domain.AssistantWorkSkipped},
	domain.AssistantWorkAwaitingApproval: {domain.AssistantWorkReady, domain.AssistantWorkObserved, domain.AssistantWorkSkipped},
	domain.AssistantWorkReady:            {domain.AssistantWorkDispatching, domain.AssistantWorkAwaitingApproval, domain.AssistantWorkObserved, domain.AssistantWorkSkipped},
	// dispatching -> ready/skipped only when this process knows the
	// reservation was never used.
	domain.AssistantWorkDispatching:  {domain.AssistantWorkWaitingAsync, domain.AssistantWorkObserved, domain.AssistantWorkUncertain, domain.AssistantWorkReady, domain.AssistantWorkSkipped},
	domain.AssistantWorkWaitingAsync: {domain.AssistantWorkObserved},
	domain.AssistantWorkUncertain:    {domain.AssistantWorkWaitingAsync},
	domain.AssistantWorkObserved:     {domain.AssistantWorkSucceeded, domain.AssistantWorkFailed, domain.AssistantWorkDenied, domain.AssistantWorkSkipped},
}

func assistantWorkTransitionAllowed(from, to domain.AssistantWorkState) bool {
	if from == to {
		return true
	}
	for _, allowed := range assistantWorkTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// validateAssistantExecutionTransition is defense in depth for the journal:
// identities are immutable, work is append-only, per-item states only move
// forward, and dispatched input, keys, receipts and observations never change.
func validateAssistantExecutionTransition(prev, next domain.AssistantExecution) error {
	if prev.SessionID != next.SessionID || prev.RunID != next.RunID || prev.TurnID != next.TurnID || prev.RequestID != next.RequestID || prev.Workflow != next.Workflow {
		return errors.New("execution identity changed")
	}
	if !next.Phase.Valid() {
		return fmt.Errorf("invalid phase %q", next.Phase)
	}
	if prev.Cancellation != nil {
		if next.Cancellation == nil || next.Cancellation.RequestID != prev.Cancellation.RequestID || next.Cancellation.Scope != prev.Cancellation.Scope {
			return errors.New("recorded cancellation cannot change")
		}
	}
	if assistantPhaseFinished(prev.Phase) {
		if next.Phase != prev.Phase || prev.Cancellation != nil || next.Cancellation == nil || next.Cancellation.Scope != "session" || len(next.Work) != len(prev.Work) {
			return errors.New("finished run may only record a session close")
		}
	}
	draftReplace := prev.Workflow == domain.AssistantWorkflowBatch && prev.Phase == domain.AssistantExecutionAwaitingApproval && len(prev.Work) == 0
	if draftReplace {
		for _, w := range next.Work {
			if w.State != domain.AssistantWorkReady || w.Authorization == nil {
				return errors.New("approved batch work must be ready and bound")
			}
		}
		return nil
	}
	if len(next.Work) < len(prev.Work) {
		return errors.New("work items cannot be removed")
	}
	for i := range prev.Work {
		pw, nw := prev.Work[i], next.Work[i]
		if nw.WorkID != pw.WorkID || nw.OriginID != pw.OriginID || nw.ToolName != pw.ToolName || nw.Ordinal != pw.Ordinal {
			return fmt.Errorf("work %s identity changed", pw.WorkID)
		}
		if !assistantWorkTransitionAllowed(pw.State, nw.State) {
			return fmt.Errorf("work %s transition %s -> %s not allowed", pw.WorkID, pw.State, nw.State)
		}
		switch pw.State {
		case domain.AssistantWorkPending, domain.AssistantWorkReady, domain.AssistantWorkAwaitingApproval:
		default:
			if nw.ArgumentsDigest != pw.ArgumentsDigest || (pw.IdempotencyKey != "" && nw.IdempotencyKey != pw.IdempotencyKey) {
				return fmt.Errorf("work %s dispatched input or key changed", pw.WorkID)
			}
		}
		if pw.Receipt != nil && (nw.Receipt == nil || nw.Receipt.RequestEventID != pw.Receipt.RequestEventID) {
			return fmt.Errorf("work %s receipt changed", pw.WorkID)
		}
		if pw.Observation != nil && (nw.Observation == nil || nw.Observation.ObservationID != pw.Observation.ObservationID) {
			return fmt.Errorf("work %s observation changed", pw.WorkID)
		}
		if pw.Authorization != nil && (nw.Authorization == nil || nw.Authorization.DecisionRequestID != pw.Authorization.DecisionRequestID) {
			return fmt.Errorf("work %s approval binding changed", pw.WorkID)
		}
	}
	for _, w := range next.Work[len(prev.Work):] {
		if w.State != domain.AssistantWorkReady {
			return fmt.Errorf("appended work %s must start ready", w.WorkID)
		}
	}
	return nil
}
