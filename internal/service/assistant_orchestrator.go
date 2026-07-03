package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

const defaultAssistantAgentID = "bahia-operator-assistant"

const (
	assistantPublishTimeout = 10 * time.Second
	assistantContextTimeout = 20 * time.Second
	assistantLLMTimeout     = 110 * time.Second
)

// AssistantChatClient is the LLM planner surface used by the orchestrator.
type AssistantChatClient interface {
	PlanFromPrompt(ctx context.Context, systemPrompt string, userPrompt string) (*domain.AssistantPlan, error)
}

type AssistantStreamingChatClient interface {
	PlanFromPromptStreaming(ctx context.Context, systemPrompt, userPrompt string, onChunk func(chunk string)) (*domain.AssistantPlan, error)
}

// AssistantContextProvider assembles bounded operational context for planning.
type AssistantContextProvider interface {
	BuildContext(ctx context.Context, routeContext map[string]string, selectedRefs []string, transcriptSummary string) (string, error)
}

// AssistantAsyncToolInvoker invokes assistant-safe event-native MCP tools.
type AssistantAsyncToolInvoker interface {
	InvokeAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error)
}

// AssistantEventPublisher publishes signed assistant events to relays.
type AssistantEventPublisher interface {
	Publish(ctx context.Context, ev nostr.Event) (int, error)
}

type AssistantRelaySubscriber interface {
	SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (AssistantMergedSubscription, error)
}

type AssistantMergedSubscription interface {
	EventChan() <-chan *nostr.Event
	ClosedChan() <-chan AssistantRelayClosed
	EOSEChan() <-chan struct{}
	Close()
}

type AssistantRelayClosed struct {
	RelayURL string
	Reason   string
}

// AssistantIdentity describes the managed assistant identity used for attribution.
type AssistantIdentity struct {
	AgentID string
	Pubkey  string
	Npub    string
}

// AssistantRequestSource describes the canonical ContextVM request that initiated
// assistant orchestration.
type AssistantRequestSource struct {
	Event          *nostr.Event
	OperatorPubkey string
	RequestID      string
	DedupKey       string
}

// AssistantOperationResult is returned as the ContextVM JSON-RPC result payload
// for prompt/approval operations.
type AssistantOperationResult map[string]any

// AssistantAgentLoopController is the agentic loop surface consumed by the
// orchestrator and startup recovery. The concrete AssistantAgentLoop implements
// it; tests can supply a deterministic loop without reaching a real model.
type AssistantAgentLoopController interface {
	StartTurn(ctx context.Context, req AssistantAgentTurnRequest) (*AssistantAgentLoopResult, error)
	ResumeAfterAsyncObservation(ctx context.Context, req AssistantAgentResumeAsyncRequest) (*AssistantAgentLoopResult, error)
	ResumeAfterActionDecision(ctx context.Context, req AssistantAgentActionDecisionRequest) (*AssistantAgentLoopResult, error)
}

// AssistantOrchestratorConfig contains dependencies and startup state.
type AssistantOrchestratorConfig struct {
	ChatClient       AssistantChatClient
	ContextBuilder   AssistantContextProvider
	ToolInvoker      AssistantAsyncToolInvoker
	Publisher        AssistantEventPublisher
	Subscriber       AssistantRelaySubscriber
	Signer           canonicalnostr.Signer
	Identity         AssistantIdentity
	AllowedToolNames []string
	InitialSessions  []domain.AssistantSession
	AgenticEnabled   bool
	StreamingEnabled bool
	AgentLoop        AssistantAgentLoopController
	Logger           *slog.Logger
}

// AssistantOrchestrator coordinates assistant prompt planning and approval dispatch.
type AssistantOrchestrator struct {
	chatClient       AssistantChatClient
	contextBuilder   AssistantContextProvider
	toolInvoker      AssistantAsyncToolInvoker
	publisher        AssistantEventPublisher
	subscriber       AssistantRelaySubscriber
	signer           canonicalnostr.Signer
	identity         AssistantIdentity
	allowedTools     map[string]struct{}
	logger           *slog.Logger
	agenticEnabled   bool
	streamingEnabled bool
	agentLoop        AssistantAgentLoopController

	mu                 sync.Mutex
	sessions           map[string]*domain.AssistantSession
	sessionLocks       map[string]*sync.Mutex
	processedTurns     map[string]AssistantOperationResult
	processedApprovals map[string]AssistantOperationResult
	submittedPlans     map[string]struct{}
	activeObservers    map[string]context.CancelFunc
}

// NewAssistantOrchestrator creates a prompt/approval orchestrator.
func NewAssistantOrchestrator(config AssistantOrchestratorConfig) *AssistantOrchestrator {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	identity := config.Identity
	if strings.TrimSpace(identity.AgentID) == "" {
		identity.AgentID = defaultAssistantAgentID
	}
	allowed := make(map[string]struct{}, len(config.AllowedToolNames))
	for _, name := range config.AllowedToolNames {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = struct{}{}
		}
	}
	o := &AssistantOrchestrator{
		chatClient:         config.ChatClient,
		contextBuilder:     config.ContextBuilder,
		toolInvoker:        config.ToolInvoker,
		publisher:          config.Publisher,
		subscriber:         config.Subscriber,
		signer:             config.Signer,
		identity:           identity,
		allowedTools:       allowed,
		logger:             logger.With("component", "assistant_orchestrator"),
		agenticEnabled:     config.AgenticEnabled,
		streamingEnabled:   config.StreamingEnabled,
		agentLoop:          config.AgentLoop,
		sessions:           make(map[string]*domain.AssistantSession),
		sessionLocks:       make(map[string]*sync.Mutex),
		processedTurns:     make(map[string]AssistantOperationResult),
		processedApprovals: make(map[string]AssistantOperationResult),
		submittedPlans:     make(map[string]struct{}),
		activeObservers:    make(map[string]context.CancelFunc),
	}
	for _, session := range config.InitialSessions {
		copySession := session
		normalizeSessionParticipants(&copySession)
		if copySession.SessionID != "" {
			o.sessions[copySession.SessionID] = &copySession
		}
	}
	return o
}

// SetAgentLoop installs the concrete agent loop after the orchestrator exists.
// The app uses this to resolve the runtime/orchestrator DI cycle: the runtime
// needs the orchestrator as its session persister and async observer, while the
// orchestrator needs the loop for agentic turns.
func (o *AssistantOrchestrator) SetAgentLoop(loop AssistantAgentLoopController) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.agentLoop = loop
	o.mu.Unlock()
}

// HandlePromptRequest processes a ContextVM assistant prompt request into an
// approved-or-clarifying assistant result payload.
func (o *AssistantOrchestrator) HandlePromptRequest(ctx context.Context, source AssistantRequestSource, req domain.AssistantPromptRequest) (AssistantOperationResult, error) {
	event := source.Event
	if event == nil {
		return nil, fmt.Errorf("assistant prompt event is nil")
	}
	if strings.TrimSpace(source.OperatorPubkey) == "" {
		source.OperatorPubkey = event.PubKey.Hex()
	}
	if strings.TrimSpace(source.RequestID) == "" {
		source.RequestID = event.ID.Hex()
	}
	if strings.TrimSpace(source.DedupKey) == "" {
		source.DedupKey = source.RequestID
	}
	if err := validatePromptRequest(req); err != nil {
		return o.failureResult(req.SessionID, "validation_error", err.Error()), nil
	}

	lock := o.lockForSession(req.SessionID)
	lock.Lock()
	defer lock.Unlock()

	dedupKey := source.DedupKey
	if dedupKey != "" {
		o.mu.Lock()
		processed := o.processedTurns[dedupKey]
		o.mu.Unlock()
		if processed != nil {
			return processed, nil
		}
	}

	session := o.loadOrCreateSession(req.SessionID, source.OperatorPubkey)
	addSessionParticipant(session, source.OperatorPubkey)
	session.State = domain.AssistantSessionStatePlanning
	session.CurrentTurnID = req.TurnID
	session.CurrentRequestID = event.ID.Hex()
	o.logger.Info("assistant prompt started", "session_id", req.SessionID, "turn_id", req.TurnID, "request_event_id", event.ID.Hex(), "agentic", o.agenticEnabled)
	if err := o.publishSession(ctx, session); err != nil {
		return nil, err
	}
	if o.agenticEnabled {
		return o.handleAgenticPromptRequest(ctx, event, dedupKey, source, req, session)
	}
	contextCtx, cancelContext := context.WithTimeout(ctx, assistantContextTimeout)
	contextBlock, err := o.contextBuilder.BuildContext(contextCtx, routeContextStrings(req.RouteContext), req.SelectedRefs, session.TranscriptSummary)
	cancelContext()
	if err != nil {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		o.logger.Warn("assistant context build failed", "session_id", req.SessionID, "turn_id", req.TurnID, "error", err)
		return o.storePromptResult(ctx, dedupKey, o.resultPayload(event, req.SessionID, "failed", "context_error", map[string]any{"summary": "failed to build assistant context", "error": err.Error()}), nil)
	}
	o.logger.Info("assistant context built", "session_id", req.SessionID, "turn_id", req.TurnID, "context_bytes", len(contextBlock))
	llmCtx, cancelLLM := context.WithTimeout(ctx, assistantLLMTimeout)
	plan, err := o.planFromPrompt(llmCtx, event, req.SessionID, o.systemPrompt(), o.userPrompt(req, contextBlock))
	cancelLLM()
	if err != nil {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		o.logger.Warn("assistant planning failed", "session_id", req.SessionID, "turn_id", req.TurnID, "error", err)
		return o.storePromptResult(ctx, dedupKey, o.resultPayload(event, req.SessionID, "failed", "llm_error", map[string]any{"summary": "assistant planning failed", "error": err.Error()}), nil)
	}
	if plan == nil {
		plan = &domain.AssistantPlan{Summary: "The assistant did not return a plan.", NeedsClarification: true, ClarifyingQuestion: "Please restate the request with explicit target resources and desired action.", RiskLevel: "low", Steps: []domain.AssistantPlanStep{}}
	}
	if err := o.validatePlan(*plan); err != nil {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		return o.storePromptResult(ctx, dedupKey, o.resultPayload(event, req.SessionID, "failed", "plan_validation_error", map[string]any{"summary": "assistant plan failed validation", "error": err.Error(), "plan": plan}), nil)
	}

	if plan.NeedsClarification {
		session.State = domain.AssistantSessionStateIdle
		session.CurrentPlan = plan
		if plan.ClarifyingQuestion != "" {
			session.TranscriptSummary = appendTranscriptSummary(session.TranscriptSummary, "assistant asked: "+plan.ClarifyingQuestion)
		}
		_ = o.publishSession(ctx, session)
		return o.storePromptResult(ctx, dedupKey, o.resultPayload(event, req.SessionID, "needs_clarification", "needs_clarification", map[string]any{"summary": plan.Summary, "clarifying_question": plan.ClarifyingQuestion, "plan": plan}), nil)
	}

	planHash := domain.ComputePlanHash(*plan, req.SessionID)
	if planHash == "" {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		return o.storePromptResult(ctx, dedupKey, o.resultPayload(event, req.SessionID, "failed", "plan_hash_error", map[string]any{"summary": "assistant plan could not be hashed"}), nil)
	}

	session.CurrentPlan = plan
	session.LastPlanHash = planHash
	session.PendingSteps = append([]domain.AssistantPlanStep(nil), plan.Steps...)
	session.TranscriptSummary = appendTranscriptSummary(session.TranscriptSummary, "operator: "+req.Prompt+"\nassistant plan: "+plan.Summary)
	if len(plan.Steps) == 0 {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		return o.storePromptResult(ctx, dedupKey, o.resultPayload(event, req.SessionID, "completed", "completed", map[string]any{"summary": plan.Summary, "plan": plan}), nil)
	}

	session.State = domain.AssistantSessionStateAwaitingApproval
	if err := o.publishSession(ctx, session); err != nil {
		return nil, err
	}
	return o.storePromptResult(ctx, dedupKey, o.resultPayload(event, req.SessionID, "planned", "planned", map[string]any{"summary": plan.Summary, "plan_hash": planHash, "plan": plan}), nil)
}

// HandleApprovalRequest processes a ContextVM assistant approval/rejection/cancel request.
func (o *AssistantOrchestrator) HandleApprovalRequest(ctx context.Context, source AssistantRequestSource, req domain.AssistantApprovalRequest) (AssistantOperationResult, error) {
	event := source.Event
	if event == nil {
		return nil, fmt.Errorf("assistant approval event is nil")
	}
	if strings.TrimSpace(source.OperatorPubkey) == "" {
		source.OperatorPubkey = event.PubKey.Hex()
	}
	if strings.TrimSpace(source.RequestID) == "" {
		source.RequestID = event.ID.Hex()
	}
	if strings.TrimSpace(source.DedupKey) == "" {
		source.DedupKey = source.RequestID
	}
	sessionID := strings.TrimSpace(req.SessionID)
	planHash := strings.TrimSpace(req.PlanHash)
	actionID := strings.TrimSpace(req.ActionID)
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	reason := firstNonEmptyString(strings.TrimSpace(req.Reason), strings.TrimSpace(req.Message))
	if sessionID == "" || (planHash == "" && actionID == "") || decision == "" {
		return o.failureResult(sessionID, "validation_error", "session_id, plan_hash or action_id, and decision are required"), nil
	}
	if decision != "approve" && decision != "reject" && decision != "cancel" {
		return o.failureResult(sessionID, "validation_error", "decision must be approve, reject, or cancel"), nil
	}
	if actionID != "" && req.ModifiedPlan != nil {
		return o.failureResult(sessionID, "validation_error", "modified_plan is only valid for legacy plan_hash approvals"), nil
	}

	lock := o.lockForSession(sessionID)
	lock.Lock()

	dedupKey := source.DedupKey
	if dedupKey != "" {
		o.mu.Lock()
		processed, duplicate := o.processedApprovals[dedupKey]
		o.mu.Unlock()
		if duplicate {
			lock.Unlock()
			return processed, nil
		}
	}

	session := o.session(sessionID)
	if session == nil {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "unknown_session", map[string]any{"summary": "assistant session is not known"})
	}
	if !sessionHasParticipant(session, source.OperatorPubkey) {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "unauthorized_participant", map[string]any{"summary": "operator is not a participant in this assistant session"})
	}
	if actionID != "" {
		result, err := o.handleAgenticActionDecision(ctx, event, dedupKey, source, session, actionID, decision, reason, req)
		lock.Unlock()
		return result, err
	}
	if decision == "cancel" {
		if session.State != domain.AssistantSessionStateExecuting && session.State != domain.AssistantSessionStateBlocked && session.State != domain.AssistantSessionStateAwaitingApproval {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "invalid_cancel", map[string]any{"summary": "cancel is only valid while executing, blocked, or awaiting approval"})
		}
		o.cancelObserver(sessionID)
		o.clearSubmittedPlan(sessionID + ":" + planHash)
		session.State = domain.AssistantSessionStateFailed
		session.PendingSteps = nil
		_ = o.publishSession(ctx, session)
		lock.Unlock()
		return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "cancelled", map[string]any{"summary": "assistant session cancelled by operator", "message": "operator cancel; no downstream rollback attempted", "reason": reason, "plan_hash": planHash})
	}
	if req.ModifiedPlan != nil && decision != "approve" {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "validation_error", map[string]any{"summary": "modified_plan is only valid for approve decisions", "plan_hash": planHash})
	}
	if req.ModifiedPlan != nil {
		if session.CurrentPlan == nil || session.LastPlanHash == "" {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "stale_approval", map[string]any{"summary": "approval does not match the latest assistant plan", "plan_hash": planHash, "latest_plan_hash": session.LastPlanHash})
		}
		if err := o.validatePlan(*req.ModifiedPlan); err != nil {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "plan_validation_error", map[string]any{"summary": "modified assistant plan failed validation", "plan_hash": planHash, "error": err.Error()})
		}
		modifiedHash := domain.ComputePlanHash(*req.ModifiedPlan, sessionID)
		if modifiedHash == "" {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "plan_hash_error", map[string]any{"summary": "modified assistant plan could not be hashed"})
		}
		if modifiedHash != planHash {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "plan_hash_mismatch", map[string]any{"summary": "modified plan hash does not match approval tag", "plan_hash": planHash, "computed_plan_hash": modifiedHash})
		}
		session.CurrentPlan = req.ModifiedPlan
		session.LastPlanHash = modifiedHash
		session.PendingSteps = append([]domain.AssistantPlanStep(nil), req.ModifiedPlan.Steps...)
		_ = o.publishSession(ctx, session)
	} else if session.LastPlanHash != planHash || session.CurrentPlan == nil {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "stale_approval", map[string]any{"summary": "approval does not match the latest assistant plan", "plan_hash": planHash, "latest_plan_hash": session.LastPlanHash})
	}
	if decision == "reject" {
		session.State = domain.AssistantSessionStateIdle
		session.PendingSteps = nil
		_ = o.publishSession(ctx, session)
		lock.Unlock()
		return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "rejected", "rejected", map[string]any{"summary": "assistant plan rejected by operator", "reason": reason, "plan_hash": planHash})
	}
	planSubmissionKey := sessionID + ":" + planHash
	o.mu.Lock()
	_, alreadySubmitted := o.submittedPlans[planSubmissionKey]
	if alreadySubmitted && session.State != domain.AssistantSessionStateExecuting {
		delete(o.submittedPlans, planSubmissionKey)
		alreadySubmitted = false
	}
	o.mu.Unlock()
	if alreadySubmitted {
		lock.Unlock()
		return o.markApprovalProcessed(dedupKey, o.resultPayload(event, sessionID, "executing", "already_submitted", map[string]any{"summary": "assistant plan is already executing", "plan_hash": planHash}), nil)
	}
	if session.State != domain.AssistantSessionStateAwaitingApproval {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "invalid_state", map[string]any{"summary": "assistant session is not awaiting approval", "state": session.State})
	}
	session.State = domain.AssistantSessionStateExecuting
	_ = o.publishSession(ctx, session)
	if err := o.publishStatus(ctx, event, sessionID, "executing", map[string]any{"message": "operator approved plan; submitting event-native commands", "plan_hash": planHash}); err != nil {
		lock.Unlock()
		return nil, err
	}

	for i := range session.CurrentPlan.Steps {
		step := session.CurrentPlan.Steps[i]
		step.IdempotencyKey = fmt.Sprintf("assistant:%s:%s:%s", sessionID, planHash, step.StepID)
		receipt := o.pendingReceipt(session, step.IdempotencyKey)
		if receipt == nil {
			args := cloneArgs(step.ToolArgs)
			args["idempotency_key"] = step.IdempotencyKey
			if err := o.publishStatus(ctx, event, sessionID, "executing", map[string]any{"phase": "executing", "plan_hash": planHash, "step_id": step.StepID, "tool_name": step.ToolName, "message": step.Title}); err != nil {
				lock.Unlock()
				return nil, err
			}
			var err error
			receipt, err = o.toolInvoker.InvokeAssistantAsyncTool(ctx, step.ToolName, args)
			if err != nil {
				session.State = domain.AssistantSessionStateFailed
				_ = o.publishSession(ctx, session)
				lock.Unlock()
				return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "tool_dispatch_error", map[string]any{"summary": "failed to dispatch assistant plan step", "plan_hash": planHash, "step_id": step.StepID, "tool_name": step.ToolName, "error": err.Error()})
			}
			o.storePendingReceipt(session, step, receipt)
			_ = o.publishSession(ctx, session)
		}
		if receipt != nil {
			if err := o.publishStatus(ctx, event, sessionID, "executing", map[string]any{"phase": "executing", "message": "downstream command submitted; awaiting event-native terminal result", "plan_hash": planHash, "step_id": step.StepID, "tool_name": step.ToolName, "downstream_request": receipt.RequestEventID, "receipt": receipt}); err != nil {
				lock.Unlock()
				return nil, err
			}
			o.markPlanSubmitted(planSubmissionKey)
		}
		lock.Unlock()
		outcome, err := o.observeDownstreamResult(ctx, sessionID, step, receipt)
		lock.Lock()
		if session.State == domain.AssistantSessionStateFailed {
			o.clearSubmittedPlan(planSubmissionKey)
			lock.Unlock()
			return o.markApprovalProcessed(dedupKey, nil, nil)
		}
		if err != nil || outcome.Status == "blocked" {
			session.State = domain.AssistantSessionStateBlocked
			o.clearSubmittedPlan(planSubmissionKey)
			_ = o.publishSession(ctx, session)
			lock.Unlock()
			msg := "downstream observation blocked before terminal result"
			if err != nil {
				msg = err.Error()
			}
			return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "blocked", "blocked", map[string]any{"summary": msg, "plan_hash": planHash, "step_id": step.StepID, "tool_name": step.ToolName})
		}
		if outcome.Status == "failed" {
			session.State = domain.AssistantSessionStateFailed
			o.clearSubmittedPlan(planSubmissionKey)
			_ = o.publishSession(ctx, session)
			lock.Unlock()
			return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "failed", "downstream_failed", map[string]any{"summary": "downstream step failed", "plan_hash": planHash, "step_id": step.StepID, "tool_name": step.ToolName, "downstream_result": outcome.Event})
		}
		o.clearPendingReceipt(session, step.IdempotencyKey)
		if len(session.PendingSteps) > 0 {
			session.PendingSteps = session.PendingSteps[1:]
		}
		_ = o.publishSession(ctx, session)
	}
	session.State = domain.AssistantSessionStateCompleted
	session.PendingSteps = nil
	o.clearSubmittedPlan(planSubmissionKey)
	_ = o.publishSession(ctx, session)
	lock.Unlock()
	return o.publishApprovalResult(ctx, dedupKey, event, sessionID, "completed", "completed", map[string]any{"summary": "assistant plan completed", "plan_hash": planHash})
}

func (o *AssistantOrchestrator) handleAgenticPromptRequest(ctx context.Context, event *nostr.Event, dedupKey string, source AssistantRequestSource, req domain.AssistantPromptRequest, session *domain.AssistantSession) (AssistantOperationResult, error) {
	if o.agentLoop == nil {
		session.State = domain.AssistantSessionStateFailed
		_ = o.publishSession(ctx, session)
		return o.storePromptResult(ctx, dedupKey, o.resultPayload(event, req.SessionID, "failed", "agent_loop_unavailable", map[string]any{"summary": "assistant agent loop is not configured", "agentic": true}), nil)
	}
	loopCtx, cancel := context.WithTimeout(ctx, assistantLLMTimeout)
	defer cancel()
	result, err := o.agentLoop.StartTurn(loopCtx, AssistantAgentTurnRequest{
		Session:        session,
		Prompt:         req.Prompt,
		TurnID:         req.TurnID,
		RouteContext:   routeContextStrings(req.RouteContext),
		SelectedRefs:   append([]string(nil), req.SelectedRefs...),
		OperatorPubkey: source.OperatorPubkey,
	})
	if err != nil {
		session.State = domain.AssistantSessionStateFailed
		_ = o.publishSession(ctx, session)
		_ = o.publishStatus(ctx, event, session.SessionID, "failed", map[string]any{"phase": "agent_loop_error", "summary": "assistant agent loop failed", "error": err.Error()})
		content := map[string]any{"summary": "assistant agent loop failed", "error": err.Error(), "agentic": true}
		if result != nil {
			mergeAgenticLoopResult(content, result)
		}
		return o.storePromptResult(ctx, dedupKey, o.resultPayload(event, req.SessionID, "failed", "agent_loop_error", content), nil)
	}
	return o.storePromptResult(ctx, dedupKey, o.agenticResultPayload(event, req.SessionID, result), nil)
}

func (o *AssistantOrchestrator) handleAgenticActionDecision(ctx context.Context, event *nostr.Event, dedupKey string, source AssistantRequestSource, session *domain.AssistantSession, actionID, decision, reason string, req domain.AssistantApprovalRequest) (AssistantOperationResult, error) {
	if !o.agenticEnabled {
		return o.publishApprovalResult(ctx, dedupKey, event, session.SessionID, "failed", "agentic_disabled", map[string]any{"summary": "assistant action approvals require assistant.agentic.enabled=true", "action_id": actionID, "agentic": false})
	}
	if o.agentLoop == nil {
		return o.publishApprovalResult(ctx, dedupKey, event, session.SessionID, "failed", "agent_loop_unavailable", map[string]any{"summary": "assistant agent loop is not configured", "action_id": actionID, "agentic": true})
	}
	loopCtx, cancel := context.WithTimeout(ctx, assistantLLMTimeout)
	defer cancel()
	result, err := o.agentLoop.ResumeAfterActionDecision(loopCtx, AssistantAgentActionDecisionRequest{
		Session:        session,
		ActionID:       actionID,
		Decision:       decision,
		Reason:         reason,
		RouteContext:   nil,
		SelectedRefs:   nil,
		OperatorPubkey: source.OperatorPubkey,
	})
	if err != nil {
		session.State = domain.AssistantSessionStateFailed
		_ = o.publishSession(ctx, session)
		return o.publishApprovalResult(ctx, dedupKey, event, session.SessionID, "failed", "action_decision_error", map[string]any{"summary": "assistant action decision failed", "error": err.Error(), "action_id": actionID, "decision": decision, "agentic": true})
	}
	payload := o.agenticResultPayload(event, session.SessionID, result)
	payload["action_id"] = actionID
	payload["decision"] = decision
	if req.PlanHash != "" {
		payload["plan_hash"] = req.PlanHash
	}
	return o.markApprovalProcessed(dedupKey, payload, nil)
}

func (o *AssistantOrchestrator) agenticResultPayload(requestEvent *nostr.Event, sessionID string, result *AssistantAgentLoopResult) AssistantOperationResult {
	status := string(domain.AssistantSessionStateFailed)
	step := "agent_loop"
	content := map[string]any{"summary": "assistant agent loop failed", "agentic": true}
	if result != nil {
		status = string(result.SessionState)
		if status == "" {
			status = agenticStatusFromLoopState(result.State)
		}
		step = string(result.State)
		if step == "" {
			step = "agent_loop"
		}
		content["summary"] = agenticSummary(result)
		mergeAgenticLoopResult(content, result)
	}
	return o.resultPayload(requestEvent, sessionID, status, step, content)
}

func mergeAgenticLoopResult(content map[string]any, result *AssistantAgentLoopResult) {
	if content == nil || result == nil {
		return
	}
	content["run_id"] = result.RunID
	content["turn_id"] = result.TurnID
	content["iteration"] = result.Iteration
	content["loop_state"] = string(result.State)
	content["session_state"] = string(result.SessionState)
	content["suspended"] = result.Suspended
	content["completed"] = result.Completed
	if result.StopReason != "" {
		content["stop_reason"] = string(result.StopReason)
	}
	if result.Error != "" {
		content["error"] = result.Error
	}
	if len(result.Observations) > 0 {
		content["observations"] = result.Observations
		last := result.Observations[len(result.Observations)-1]
		if last != nil {
			content["observation_id"] = last.ObservationID
			content["observation_status"] = string(last.Status)
			content["tool_call_id"] = last.ToolCallID
			content["tool_name"] = last.ToolName
			if last.Receipt != nil {
				content["downstream_request"] = last.Receipt.RequestEventID
			}
		}
	}
	if result.DeferredAction != nil {
		content["action_id"] = result.DeferredAction.ActionID
		content["tool_call_id"] = result.DeferredAction.ToolCallID
		content["tool_name"] = result.DeferredAction.ToolName
		content["permission"] = result.DeferredAction.Permission
		content["approval_prompt"] = result.DeferredAction.ApprovalPrompt
	}
}

func agenticStatusFromLoopState(state domain.AssistantAgentLoopState) string {
	switch state {
	case domain.AssistantAgentLoopStateCompleted:
		return string(domain.AssistantSessionStateCompleted)
	case domain.AssistantAgentLoopStateBlocked:
		return string(domain.AssistantSessionStateBlocked)
	case domain.AssistantAgentLoopStateFailed:
		return string(domain.AssistantSessionStateFailed)
	case domain.AssistantAgentLoopStateAwaitingApproval:
		return string(domain.AssistantSessionStateAwaitingApproval)
	default:
		return string(domain.AssistantSessionStateExecuting)
	}
}

func agenticSummary(result *AssistantAgentLoopResult) string {
	if result == nil {
		return "assistant agent loop failed"
	}
	if result.Error != "" {
		return result.Error
	}
	if result.DeferredAction != nil {
		return "assistant action requires operator approval"
	}
	switch result.State {
	case domain.AssistantAgentLoopStateCompleted:
		return "assistant agent loop completed"
	case domain.AssistantAgentLoopStateWaitingAsync:
		return "assistant agent loop is waiting for an async tool observation"
	case domain.AssistantAgentLoopStateAwaitingApproval:
		return "assistant agent loop is awaiting action approval"
	case domain.AssistantAgentLoopStateBlocked:
		return "assistant agent loop blocked"
	case domain.AssistantAgentLoopStateFailed:
		return "assistant agent loop failed"
	default:
		return "assistant agent loop running"
	}
}

type downstreamOutcome struct {
	Status string
	Event  *nostr.Event
}

func (o *AssistantOrchestrator) observeDownstreamResult(ctx context.Context, sessionID string, step domain.AssistantPlanStep, receipt *domain.AsyncToolReceipt) (downstreamOutcome, error) {
	if receipt == nil || receipt.RequestEventID == "" || len(receipt.ResultKinds) == 0 {
		return downstreamOutcome{Status: "blocked"}, fmt.Errorf("downstream receipt is missing observable result metadata")
	}
	if o.subscriber == nil {
		return downstreamOutcome{Status: "blocked"}, fmt.Errorf("assistant relay subscriber is not configured")
	}
	observeCtx, cancel := context.WithCancel(ctx)
	o.mu.Lock()
	o.activeObservers[sessionID] = cancel
	o.mu.Unlock()
	defer func() {
		cancel()
		o.mu.Lock()
		delete(o.activeObservers, sessionID)
		o.mu.Unlock()
	}()
	resultKinds := make([]nostr.Kind, 0, len(receipt.ResultKinds))
	for _, kind := range receipt.ResultKinds {
		resultKinds = append(resultKinds, nostr.Kind(kind))
	}
	merged, err := o.subscriber.SubscribeAllWithEOSE(observeCtx, []nostr.Filter{{Kinds: resultKinds, Tags: nostr.TagMap{"e": []string{receipt.RequestEventID}}}})
	if err != nil {
		return downstreamOutcome{Status: "blocked"}, err
	}
	defer merged.Close()
	eventsCh := merged.EventChan()
	closedCh := merged.ClosedChan()
	seen := map[string]struct{}{}
	for {
		select {
		case <-observeCtx.Done():
			return downstreamOutcome{Status: "blocked"}, observeCtx.Err()
		case closed, ok := <-closedCh:
			if ok {
				return downstreamOutcome{Status: "blocked"}, fmt.Errorf("relay subscription closed before terminal result: relay=%s reason=%s", closed.RelayURL, closed.Reason)
			}
		case ev, ok := <-eventsCh:
			if !ok {
				return downstreamOutcome{Status: "blocked"}, fmt.Errorf("downstream result subscription ended before terminal result")
			}
			if ev == nil {
				continue
			}
			eventID := ev.ID.Hex()
			if _, dup := seen[eventID]; dup {
				continue
			}
			seen[eventID] = struct{}{}
			if !downstreamResultMatchesReceipt(ev, receipt) {
				continue
			}
			status := terminalStatus(ev)
			if status == "completed" || status == "failed" {
				return downstreamOutcome{Status: status, Event: ev}, nil
			}
		}
	}
}

func (o *AssistantOrchestrator) cancelObserver(sessionID string) {
	o.mu.Lock()
	cancel := o.activeObservers[sessionID]
	delete(o.activeObservers, sessionID)
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func downstreamResultMatchesReceipt(ev *nostr.Event, receipt *domain.AsyncToolReceipt) bool {
	if ev == nil || receipt == nil || receipt.RequestEventID == "" {
		return false
	}
	kindAllowed := false
	for _, kind := range receipt.ResultKinds {
		if ev.Kind == nostr.Kind(kind) {
			kindAllowed = true
			break
		}
	}
	if !kindAllowed {
		return false
	}
	if !tagContainsValue(ev.Tags, "e", receipt.RequestEventID) {
		return false
	}
	if ev.CreatedAt == 0 || ev.CreatedAt > nostr.Now()+nostr.Timestamp((5*time.Minute)/time.Second) {
		return false
	}
	if !ev.CheckID() || !ev.VerifySignature() {
		return false
	}
	return true
}

func terminalStatus(ev *nostr.Event) string {
	status := strings.ToLower(strings.TrimSpace(tagValue(ev.Tags, "status")))
	if status == "" {
		var content map[string]any
		_ = json.Unmarshal([]byte(ev.Content), &content)
		status = strings.ToLower(strings.TrimSpace(stringFromMap(content, "status")))
	}
	switch status {
	case "success", "succeeded", "completed", "complete", "ok", "approved":
		return "completed"
	case "failed", "failure", "error", "rejected", "cancelled", "canceled":
		return "failed"
	default:
		return ""
	}
}

func (o *AssistantOrchestrator) pendingReceipt(session *domain.AssistantSession, key string) *domain.AsyncToolReceipt {
	if session == nil || session.Metadata == nil || key == "" {
		return nil
	}
	receipts, _ := session.Metadata["pending_receipts"].(map[string]any)
	if receipts == nil {
		return nil
	}
	raw := receipts[key]
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var receipt domain.AsyncToolReceipt
	if err := json.Unmarshal(b, &receipt); err != nil || receipt.IdempotencyKey != key {
		return nil
	}
	return &receipt
}

func (o *AssistantOrchestrator) storePendingReceipt(session *domain.AssistantSession, step domain.AssistantPlanStep, receipt *domain.AsyncToolReceipt) {
	if session == nil || receipt == nil || receipt.IdempotencyKey == "" {
		return
	}
	if session.Metadata == nil {
		session.Metadata = map[string]any{}
	}
	receipts, _ := session.Metadata["pending_receipts"].(map[string]any)
	if receipts == nil {
		receipts = map[string]any{}
		session.Metadata["pending_receipts"] = receipts
	}
	receipts[receipt.IdempotencyKey] = receipt
	for i := range session.PendingSteps {
		if session.PendingSteps[i].StepID == step.StepID {
			session.PendingSteps[i].IdempotencyKey = receipt.IdempotencyKey
			return
		}
	}
}

func (o *AssistantOrchestrator) clearPendingReceipt(session *domain.AssistantSession, key string) {
	if session == nil || session.Metadata == nil || key == "" {
		return
	}
	if receipts, _ := session.Metadata["pending_receipts"].(map[string]any); receipts != nil {
		delete(receipts, key)
		if len(receipts) == 0 {
			delete(session.Metadata, "pending_receipts")
		}
	}
}

// PublishFailure publishes a canonical failure status for rejected/malformed requests.
func (o *AssistantOrchestrator) PublishFailure(ctx context.Context, event *nostr.Event, sessionID, step, message string) error {
	if sessionID == "" && event != nil {
		sessionID = sessionFromEvent(event)
	}
	return o.publishStatus(ctx, event, sessionID, "failed", map[string]any{"step": step, "summary": message, "error": message})
}

func (o *AssistantOrchestrator) lockForSession(sessionID string) *sync.Mutex {
	o.mu.Lock()
	defer o.mu.Unlock()
	lock := o.sessionLocks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		o.sessionLocks[sessionID] = lock
	}
	return lock
}

func (o *AssistantOrchestrator) loadOrCreateSession(sessionID, operator string) *domain.AssistantSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s := o.sessions[sessionID]; s != nil {
		return s
	}
	s := &domain.AssistantSession{SessionID: sessionID, State: domain.AssistantSessionStateIdle, OperatorPubkey: operator, Participants: []string{operator}, AssistantID: o.identity.AgentID, AssistantPubkey: o.identity.Pubkey, Metadata: map[string]any{"assistant_npub": o.identity.Npub}}
	o.sessions[sessionID] = s
	return s
}

func (o *AssistantOrchestrator) session(sessionID string) *domain.AssistantSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sessions[sessionID]
}

// IsSessionParticipant reports whether operator may interact with an existing session.
// Unknown sessions return true so callers can allow new-session creation.
func (o *AssistantOrchestrator) IsSessionParticipant(sessionID, operator string) bool {
	sessionID = strings.TrimSpace(sessionID)
	operator = strings.ToLower(strings.TrimSpace(operator))
	if sessionID == "" || operator == "" {
		return false
	}
	o.mu.Lock()
	session := o.sessions[sessionID]
	o.mu.Unlock()
	if session == nil {
		return true
	}
	return sessionHasParticipant(session, operator)
}

func normalizeSessionParticipants(session *domain.AssistantSession) {
	if session == nil {
		return
	}
	if strings.TrimSpace(session.OperatorPubkey) == "" && len(session.Participants) > 0 {
		session.OperatorPubkey = session.Participants[0]
	}
	addSessionParticipant(session, session.OperatorPubkey)
	participants := make([]string, 0, len(session.Participants))
	seen := map[string]struct{}{}
	for _, participant := range session.Participants {
		clean := strings.ToLower(strings.TrimSpace(participant))
		if clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		participants = append(participants, clean)
	}
	session.Participants = participants
}

func addSessionParticipant(session *domain.AssistantSession, operator string) {
	if session == nil {
		return
	}
	operator = strings.ToLower(strings.TrimSpace(operator))
	if operator == "" {
		return
	}
	if strings.TrimSpace(session.OperatorPubkey) == "" {
		session.OperatorPubkey = operator
	}
	for _, participant := range session.Participants {
		if strings.ToLower(strings.TrimSpace(participant)) == operator {
			return
		}
	}
	session.Participants = append(session.Participants, operator)
}

func sessionHasParticipant(session *domain.AssistantSession, operator string) bool {
	if session == nil {
		return false
	}
	operator = strings.ToLower(strings.TrimSpace(operator))
	if operator == "" {
		return false
	}
	if strings.ToLower(strings.TrimSpace(session.OperatorPubkey)) == operator {
		return true
	}
	for _, participant := range session.Participants {
		if strings.ToLower(strings.TrimSpace(participant)) == operator {
			return true
		}
	}
	return false
}

func (o *AssistantOrchestrator) planFromPrompt(ctx context.Context, requestEvent *nostr.Event, sessionID, systemPrompt, userPrompt string) (*domain.AssistantPlan, error) {
	if streamingClient, ok := o.chatClient.(AssistantStreamingChatClient); ok && o.streamingEnabled {
		var pending strings.Builder
		lastPublished := time.Now()
		flush := func(force bool) {
			chunk := pending.String()
			if chunk == "" {
				return
			}
			if !force && time.Since(lastPublished) < 200*time.Millisecond && len(chunk) < 50 {
				return
			}
			pending.Reset()
			lastPublished = time.Now()
			if err := o.publishStatus(ctx, requestEvent, sessionID, "planning", map[string]any{"phase": "planning", "streaming": true, "chunk": chunk}); err != nil {
				o.logger.Warn("failed to publish assistant planning stream chunk", "error", err)
			}
		}

		plan, err := streamingClient.PlanFromPromptStreaming(ctx, systemPrompt, userPrompt, func(chunk string) {
			pending.WriteString(chunk)
			flush(false)
		})
		flush(true)
		return plan, err
	}

	return o.chatClient.PlanFromPrompt(ctx, systemPrompt, userPrompt)
}

func (o *AssistantOrchestrator) validatePlan(plan domain.AssistantPlan) error {
	if strings.TrimSpace(plan.Summary) == "" {
		return fmt.Errorf("plan summary is required")
	}
	switch plan.RiskLevel {
	case "", "low", "medium", "high":
	default:
		return fmt.Errorf("risk_level must be low, medium, or high")
	}
	if plan.NeedsClarification {
		return nil
	}
	for _, step := range plan.Steps {
		if strings.TrimSpace(step.StepID) == "" || strings.TrimSpace(step.ToolName) == "" {
			return fmt.Errorf("each plan step requires step_id and tool_name")
		}
		if _, ok := o.allowedTools[step.ToolName]; !ok {
			return fmt.Errorf("assistant tool %q is not allowlisted", step.ToolName)
		}
	}
	return nil
}

func (o *AssistantOrchestrator) publishSession(ctx context.Context, session *domain.AssistantSession) error {
	normalizeSessionParticipants(session)
	content, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal assistant session: %w", err)
	}
	tags := nostr.Tags{
		{"d", domain.AssistantSessionSchema + ":" + session.SessionID},
		{domain.AssistantSessionTagSchema, domain.AssistantSessionSchema},
		{"domain", "assistant"},
		{"entity", "session"},
		{"session", session.SessionID},
	}
	for _, participant := range session.Participants {
		tags = append(tags, nostr.Tag{"p", participant, "", "operator"})
	}
	tags = append(tags,
		nostr.Tag{"agent", o.identity.AgentID},
		nostr.Tag{"status", string(session.State)},
	)
	ev := &nostr.Event{Kind: domain.KindAssistantSessionState, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	return o.signAndPublish(ctx, ev)
}

func (o *AssistantOrchestrator) publishStatus(ctx context.Context, requestEvent *nostr.Event, sessionID, status string, content map[string]any) error {
	if content == nil {
		content = map[string]any{}
	}
	content["status"] = status
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("marshal assistant status: %w", err)
	}
	dTag := fmt.Sprintf("%s:%s:%s:%d", domain.AssistantStatusSchema, sessionID, status, time.Now().UnixNano())
	if requestEvent != nil && requestEvent.ID != (nostr.ID{}) {
		dTag = fmt.Sprintf("%s:%s:%s:%s:%d", domain.AssistantStatusSchema, sessionID, status, requestEvent.ID.Hex(), time.Now().UnixNano())
	}
	tags := append(nostr.Tags{{"d", dTag}, {"schema", domain.AssistantStatusSchema}}, o.replyTags(requestEvent, sessionID, status)...)
	if planHash := stringFromMap(content, "plan_hash"); planHash != "" {
		tags = append(tags, nostr.Tag{"plan-hash", planHash})
	}
	if stepID := firstNonEmptyString(stringFromMap(content, "step_id"), stringFromMap(content, "step")); stepID != "" {
		tags = append(tags, nostr.Tag{"step", stepID})
	}
	if downstream := stringFromMap(content, "downstream_request"); downstream != "" {
		tags = append(tags, nostr.Tag{"downstream-request", downstream})
	}
	if streaming, _ := content["streaming"].(bool); streaming {
		tags = append(tags, nostr.Tag{"streaming", "true"})
	}
	ev := &nostr.Event{Kind: domain.KindAssistantStatus, CreatedAt: nostr.Now(), Tags: tags, Content: string(contentJSON)}
	return o.signAndPublish(ctx, ev)
}

func (o *AssistantOrchestrator) resultPayload(requestEvent *nostr.Event, sessionID, status, step string, content map[string]any) AssistantOperationResult {
	payload := AssistantOperationResult{}
	for k, v := range content {
		payload[k] = v
	}
	payload["session_id"] = sessionID
	payload["status"] = status
	payload["agent"] = o.identity.AgentID
	if step != "" {
		payload["step"] = step
	}
	if requestEvent != nil {
		payload["request_event_id"] = requestEvent.ID.Hex()
	}
	return payload
}

func (o *AssistantOrchestrator) failureResult(sessionID, step, message string) AssistantOperationResult {
	return o.resultPayload(nil, sessionID, "failed", step, map[string]any{"summary": message, "error": message})
}

func (o *AssistantOrchestrator) replyTags(requestEvent *nostr.Event, sessionID, status string) nostr.Tags {
	tags := nostr.Tags{{"session", sessionID}, {"agent", o.identity.AgentID}, {"status", status}}
	if requestEvent != nil {
		tags = append(tags, nostr.Tag{"e", requestEvent.ID.Hex(), "", "reply"}, nostr.Tag{"p", requestEvent.PubKey.Hex()})
	}
	return tags
}

func (o *AssistantOrchestrator) signAndPublish(ctx context.Context, ev *nostr.Event) error {
	if err := signGoNostrEvent(ctx, o.signer, ev); err != nil {
		return fmt.Errorf("sign assistant event: %w", err)
	}
	if o.publisher == nil {
		return fmt.Errorf("assistant publisher is not configured")
	}
	publishCtx, cancel := context.WithTimeout(ctx, assistantPublishTimeout)
	defer cancel()
	published, err := o.publisher.Publish(publishCtx, *ev)
	if err != nil {
		return fmt.Errorf("publish assistant event: %w", err)
	}
	if published == 0 {
		return fmt.Errorf("publish assistant event: no relay accepted event")
	}
	return nil
}

func (o *AssistantOrchestrator) storePromptResult(_ context.Context, dedupKey string, result AssistantOperationResult, err error) (AssistantOperationResult, error) {
	if err == nil && dedupKey != "" && result != nil {
		o.mu.Lock()
		o.processedTurns[dedupKey] = result
		o.mu.Unlock()
	}
	return result, err
}

func (o *AssistantOrchestrator) publishApprovalResult(ctx context.Context, dedupKey string, requestEvent *nostr.Event, sessionID, status, step string, content map[string]any) (AssistantOperationResult, error) {
	result := o.resultPayload(requestEvent, sessionID, status, step, content)
	if err := o.publishStatus(ctx, requestEvent, sessionID, status, content); err != nil {
		return result, err
	}
	return o.markApprovalProcessed(dedupKey, result, nil)
}

func (o *AssistantOrchestrator) markApprovalProcessed(dedupKey string, result AssistantOperationResult, err error) (AssistantOperationResult, error) {
	if err == nil && dedupKey != "" && result != nil {
		o.mu.Lock()
		o.processedApprovals[dedupKey] = result
		o.mu.Unlock()
	}
	return result, err
}

func (o *AssistantOrchestrator) markPlanSubmitted(planSubmissionKey string) {
	if planSubmissionKey == "" {
		return
	}
	o.mu.Lock()
	o.submittedPlans[planSubmissionKey] = struct{}{}
	o.mu.Unlock()
}

func (o *AssistantOrchestrator) clearSubmittedPlan(planSubmissionKey string) {
	if planSubmissionKey == "" {
		return
	}
	o.mu.Lock()
	delete(o.submittedPlans, planSubmissionKey)
	o.mu.Unlock()
}

func (o *AssistantOrchestrator) systemPrompt() string {
	names := make([]string, 0, len(o.allowedTools))
	for name := range o.allowedTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return "You are the Bahia Operator Assistant. Produce a conservative AssistantPlan JSON object. Address the operator directly with second-person pronouns (you/your) in summaries and clarification questions; never describe the operator in third person. Use only these assistant-safe event-native tools: " + strings.Join(names, ", ") + ".\n\n" +
		"DNS intent mapping:\n" +
		"- \"expose X internally only\" → bahia_assistant_dns_policy_apply with split-horizon visibility=internal\n" +
		"- \"add DNS for X\" / \"create zone\" → bahia_assistant_dns_zone_create\n" +
		"- \"override X to point to Y\" → bahia_assistant_dns_record_override\n" +
		"- \"fix drift\" / \"remediate\" → bahia_assistant_dns_drift_remediate\n" +
		"- \"show endpoints\" / \"list DNS\" → bahia_assistant_dns_list_endpoints\n" +
		"- \"show drift\" → bahia_assistant_dns_list_drift\n" +
		"When creating zones or policies, infer zone name from existing DNS Zones context. Generate a UUID v4 idempotency_key for each mutation tool call.\n\n" +
		"If a target resource or intended action is ambiguous, set needs_clarification=true and produce no steps. Never include secrets."
}

func (o *AssistantOrchestrator) userPrompt(req domain.AssistantPromptRequest, contextBlock string) string {
	payload, _ := json.MarshalIndent(map[string]any{"operator_prompt": req.Prompt, "route_context": req.RouteContext, "selected_refs": req.SelectedRefs, "operational_context": contextBlock}, "", "  ")
	return string(payload)
}

func validatePromptRequest(req domain.AssistantPromptRequest) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(req.TurnID) == "" {
		return fmt.Errorf("turn_id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	return nil
}

func sessionFromEvent(event *nostr.Event) string {
	if event == nil {
		return ""
	}
	if s := tagValue(event.Tags, "session"); s != "" {
		return s
	}
	var raw struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal([]byte(event.Content), &raw)
	return strings.TrimSpace(raw.SessionID)
}

func tagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func tagContainsValue(tags nostr.Tags, key string, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}

func routeContextStrings(routeContext map[string]any) map[string]string {
	out := make(map[string]string, len(routeContext))
	for k, v := range routeContext {
		if strings.TrimSpace(k) == "" || v == nil {
			continue
		}
		switch vv := v.(type) {
		case string:
			out[k] = vv
		default:
			b, _ := json.Marshal(vv)
			out[k] = string(b)
		}
	}
	return out
}

func cloneArgs(args map[string]any) map[string]interface{} {
	out := make(map[string]interface{}, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	return out
}

func appendTranscriptSummary(existing, addition string) string {
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return existing
	}
	combined := strings.TrimSpace(existing + "\n" + addition)
	if len(combined) > 4000 {
		return combined[len(combined)-4000:]
	}
	return combined
}

func stringFromMap(m map[string]any, key string) string {
	v := m[key]
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func signGoNostrEvent(ctx context.Context, signer canonicalnostr.Signer, ev *nostr.Event) error {
	if ev == nil {
		return fmt.Errorf("nostr event is nil")
	}
	if signer == nil {
		return fmt.Errorf("assistant signer is not configured")
	}
	return signer.SignEvent(ctx, ev)
}
