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

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/nbd-wtf/go-nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

const defaultAssistantAgentID = "bahia-operator-assistant"

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
	Logger           *slog.Logger
}

// AssistantOrchestrator coordinates assistant prompt planning and approval dispatch.
type AssistantOrchestrator struct {
	chatClient     AssistantChatClient
	contextBuilder AssistantContextProvider
	toolInvoker    AssistantAsyncToolInvoker
	publisher      AssistantEventPublisher
	subscriber     AssistantRelaySubscriber
	signer         canonicalnostr.Signer
	identity       AssistantIdentity
	allowedTools   map[string]struct{}
	logger         *slog.Logger

	mu                 sync.Mutex
	sessions           map[string]*domain.AssistantSession
	sessionLocks       map[string]*sync.Mutex
	processedTurns     map[string]*nostr.Event
	processedApprovals map[string]struct{}
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
		sessions:           make(map[string]*domain.AssistantSession),
		sessionLocks:       make(map[string]*sync.Mutex),
		processedTurns:     make(map[string]*nostr.Event),
		processedApprovals: make(map[string]struct{}),
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

// HandlePrompt processes a kind:38420 prompt request into an approved-or-clarifying assistant result.
func (o *AssistantOrchestrator) HandlePrompt(ctx context.Context, event *nostr.Event) error {
	if event == nil {
		return fmt.Errorf("assistant prompt event is nil")
	}
	var req domain.AssistantPromptRequest
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		return o.PublishFailure(ctx, event, sessionFromEvent(event), "parse_error", fmt.Sprintf("invalid prompt request JSON: %v", err))
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = sessionFromEvent(event)
	}
	if err := validatePromptRequest(req); err != nil {
		return o.PublishFailure(ctx, event, req.SessionID, "validation_error", err.Error())
	}

	lock := o.lockForSession(req.SessionID)
	lock.Lock()
	defer lock.Unlock()

	dTag := tagValue(event.Tags, "d")
	if dTag != "" {
		o.mu.Lock()
		processed := o.processedTurns[dTag]
		o.mu.Unlock()
		if processed != nil {
			return o.republish(ctx, processed)
		}
	}

	session := o.loadOrCreateSession(req.SessionID, event.PubKey)
	addSessionParticipant(session, event.PubKey)
	session.State = domain.AssistantSessionStatePlanning
	session.CurrentTurnID = req.TurnID
	session.CurrentRequestID = event.ID
	if err := o.publishSession(ctx, session); err != nil {
		return err
	}
	if err := o.publishStatus(ctx, event, req.SessionID, "planning", map[string]any{"message": "planning assistant response"}); err != nil {
		return err
	}

	contextBlock, err := o.contextBuilder.BuildContext(ctx, routeContextStrings(req.RouteContext), req.SelectedRefs, session.TranscriptSummary)
	if err != nil {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		return o.publishPromptResult(ctx, dTag, event, req.SessionID, "failed", "context_error", map[string]any{"summary": "failed to build assistant context", "error": err.Error()})
	}
	plan, err := o.planFromPrompt(ctx, event, req.SessionID, o.systemPrompt(), o.userPrompt(req, contextBlock))
	if err != nil {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		return o.publishPromptResult(ctx, dTag, event, req.SessionID, "failed", "llm_error", map[string]any{"summary": "assistant planning failed", "error": err.Error()})
	}
	if plan == nil {
		plan = &domain.AssistantPlan{Summary: "The assistant did not return a plan.", NeedsClarification: true, ClarifyingQuestion: "Please restate the request with explicit target resources and desired action.", RiskLevel: "low", Steps: []domain.AssistantPlanStep{}}
	}
	if err := o.validatePlan(*plan); err != nil {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		return o.publishPromptResult(ctx, dTag, event, req.SessionID, "failed", "plan_validation_error", map[string]any{"summary": "assistant plan failed validation", "error": err.Error(), "plan": plan})
	}

	if plan.NeedsClarification {
		session.State = domain.AssistantSessionStateIdle
		session.CurrentPlan = plan
		if plan.ClarifyingQuestion != "" {
			session.TranscriptSummary = appendTranscriptSummary(session.TranscriptSummary, "assistant asked: "+plan.ClarifyingQuestion)
		}
		_ = o.publishSession(ctx, session)
		return o.publishPromptResult(ctx, dTag, event, req.SessionID, "needs_clarification", "needs_clarification", map[string]any{"summary": plan.Summary, "clarifying_question": plan.ClarifyingQuestion, "plan": plan})
	}

	planHash := domain.ComputePlanHash(*plan, req.SessionID)
	if planHash == "" {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		return o.publishPromptResult(ctx, dTag, event, req.SessionID, "failed", "plan_hash_error", map[string]any{"summary": "assistant plan could not be hashed"})
	}

	session.CurrentPlan = plan
	session.LastPlanHash = planHash
	session.PendingSteps = append([]domain.AssistantPlanStep(nil), plan.Steps...)
	session.TranscriptSummary = appendTranscriptSummary(session.TranscriptSummary, "operator: "+req.Prompt+"\nassistant plan: "+plan.Summary)
	if len(plan.Steps) == 0 {
		session.State = domain.AssistantSessionStateIdle
		_ = o.publishSession(ctx, session)
		return o.publishPromptResult(ctx, dTag, event, req.SessionID, "completed", "completed", map[string]any{"summary": plan.Summary, "plan": plan})
	}

	session.State = domain.AssistantSessionStateAwaitingApproval
	if err := o.publishSession(ctx, session); err != nil {
		return err
	}
	return o.publishPromptResult(ctx, dTag, event, req.SessionID, "planned", "planned", map[string]any{"summary": plan.Summary, "plan_hash": planHash, "plan": plan})
}

// HandleApproval processes a kind:38421 approval/rejection/cancel request.
func (o *AssistantOrchestrator) HandleApproval(ctx context.Context, event *nostr.Event) error {
	if event == nil {
		return fmt.Errorf("assistant approval event is nil")
	}
	sessionID := sessionFromEvent(event)
	planHash := tagValue(event.Tags, "plan-hash")
	decision := strings.ToLower(strings.TrimSpace(tagValue(event.Tags, "decision")))
	var content struct {
		Reason       string                `json:"reason,omitempty"`
		ModifiedPlan *domain.AssistantPlan `json:"modified_plan,omitempty"`
	}
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
			return o.PublishFailure(ctx, event, sessionID, "parse_error", fmt.Sprintf("invalid approval request JSON: %v", err))
		}
	}
	if sessionID == "" || planHash == "" || decision == "" {
		return o.PublishFailure(ctx, event, sessionID, "validation_error", "session, plan-hash, and decision tags are required")
	}
	if decision != "approve" && decision != "reject" && decision != "cancel" {
		return o.PublishFailure(ctx, event, sessionID, "validation_error", "decision must be approve, reject, or cancel")
	}

	lock := o.lockForSession(sessionID)
	lock.Lock()

	dTag := tagValue(event.Tags, "d")
	if dTag != "" {
		o.mu.Lock()
		_, duplicate := o.processedApprovals[dTag]
		o.mu.Unlock()
		if duplicate {
			lock.Unlock()
			return nil
		}
	}

	session := o.session(sessionID)
	if session == nil {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "unknown_session", map[string]any{"summary": "assistant session is not known"})
	}
	if !sessionHasParticipant(session, event.PubKey) {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "unauthorized_participant", map[string]any{"summary": "operator is not a participant in this assistant session"})
	}
	if decision == "cancel" {
		if session.State != domain.AssistantSessionStateExecuting && session.State != domain.AssistantSessionStateBlocked && session.State != domain.AssistantSessionStateAwaitingApproval {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "invalid_cancel", map[string]any{"summary": "cancel is only valid while executing, blocked, or awaiting approval"})
		}
		o.cancelObserver(sessionID)
		session.State = domain.AssistantSessionStateFailed
		session.PendingSteps = nil
		_ = o.publishSession(ctx, session)
		lock.Unlock()
		return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "cancelled", map[string]any{"summary": "assistant session cancelled by operator", "message": "operator cancel; no downstream rollback attempted", "reason": content.Reason, "plan_hash": planHash})
	}
	if content.ModifiedPlan != nil && decision != "approve" {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "validation_error", map[string]any{"summary": "modified_plan is only valid for approve decisions", "plan_hash": planHash})
	}
	if content.ModifiedPlan != nil {
		if session.CurrentPlan == nil || session.LastPlanHash == "" {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "stale_approval", map[string]any{"summary": "approval does not match the latest assistant plan", "plan_hash": planHash, "latest_plan_hash": session.LastPlanHash})
		}
		if err := o.validatePlan(*content.ModifiedPlan); err != nil {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "plan_validation_error", map[string]any{"summary": "modified assistant plan failed validation", "plan_hash": planHash, "error": err.Error()})
		}
		modifiedHash := domain.ComputePlanHash(*content.ModifiedPlan, sessionID)
		if modifiedHash == "" {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "plan_hash_error", map[string]any{"summary": "modified assistant plan could not be hashed"})
		}
		if modifiedHash != planHash {
			lock.Unlock()
			return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "plan_hash_mismatch", map[string]any{"summary": "modified plan hash does not match approval tag", "plan_hash": planHash, "computed_plan_hash": modifiedHash})
		}
		session.CurrentPlan = content.ModifiedPlan
		session.LastPlanHash = modifiedHash
		session.PendingSteps = append([]domain.AssistantPlanStep(nil), content.ModifiedPlan.Steps...)
		_ = o.publishSession(ctx, session)
	} else if session.LastPlanHash != planHash || session.CurrentPlan == nil {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "stale_approval", map[string]any{"summary": "approval does not match the latest assistant plan", "plan_hash": planHash, "latest_plan_hash": session.LastPlanHash})
	}
	if decision == "reject" {
		session.State = domain.AssistantSessionStateIdle
		session.PendingSteps = nil
		_ = o.publishSession(ctx, session)
		lock.Unlock()
		return o.publishApprovalResult(ctx, dTag, event, sessionID, "rejected", "rejected", map[string]any{"summary": "assistant plan rejected by operator", "reason": content.Reason, "plan_hash": planHash})
	}
	if session.State != domain.AssistantSessionStateAwaitingApproval && session.State != domain.AssistantSessionStateExecuting {
		lock.Unlock()
		return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "invalid_state", map[string]any{"summary": "assistant session is not awaiting approval", "state": session.State})
	}

	o.mu.Lock()
	_, alreadySubmitted := o.submittedPlans[sessionID+":"+planHash]
	o.mu.Unlock()
	if alreadySubmitted {
		lock.Unlock()
		return o.markApprovalProcessed(dTag, nil, nil)
	}
	session.State = domain.AssistantSessionStateExecuting
	_ = o.publishSession(ctx, session)
	if err := o.publishStatus(ctx, event, sessionID, "executing", map[string]any{"message": "operator approved plan; submitting event-native commands", "plan_hash": planHash}); err != nil {
		lock.Unlock()
		return err
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
				return err
			}
			var err error
			receipt, err = o.toolInvoker.InvokeAssistantAsyncTool(ctx, step.ToolName, args)
			if err != nil {
				session.State = domain.AssistantSessionStateFailed
				_ = o.publishSession(ctx, session)
				lock.Unlock()
				return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "tool_dispatch_error", map[string]any{"summary": "failed to dispatch assistant plan step", "plan_hash": planHash, "step_id": step.StepID, "tool_name": step.ToolName, "error": err.Error()})
			}
			o.storePendingReceipt(session, step, receipt)
			_ = o.publishSession(ctx, session)
		}
		if receipt != nil {
			if err := o.publishStatus(ctx, event, sessionID, "executing", map[string]any{"phase": "executing", "message": "downstream command submitted; awaiting event-native terminal result", "plan_hash": planHash, "step_id": step.StepID, "tool_name": step.ToolName, "downstream_request": receipt.RequestEventID, "receipt": receipt}); err != nil {
				lock.Unlock()
				return err
			}
		}
		lock.Unlock()
		outcome, err := o.observeDownstreamResult(ctx, sessionID, step, receipt)
		lock.Lock()
		if session.State == domain.AssistantSessionStateFailed {
			lock.Unlock()
			return o.markApprovalProcessed(dTag, nil, nil)
		}
		if err != nil || outcome.Status == "blocked" {
			session.State = domain.AssistantSessionStateBlocked
			_ = o.publishSession(ctx, session)
			lock.Unlock()
			msg := "downstream observation blocked before terminal result"
			if err != nil {
				msg = err.Error()
			}
			return o.publishApprovalResult(ctx, dTag, event, sessionID, "blocked", "blocked", map[string]any{"summary": msg, "plan_hash": planHash, "step_id": step.StepID, "tool_name": step.ToolName})
		}
		if outcome.Status == "failed" {
			session.State = domain.AssistantSessionStateFailed
			_ = o.publishSession(ctx, session)
			lock.Unlock()
			return o.publishApprovalResult(ctx, dTag, event, sessionID, "failed", "downstream_failed", map[string]any{"summary": "downstream step failed", "plan_hash": planHash, "step_id": step.StepID, "tool_name": step.ToolName, "downstream_result": outcome.Event})
		}
		o.clearPendingReceipt(session, step.IdempotencyKey)
		if len(session.PendingSteps) > 0 {
			session.PendingSteps = session.PendingSteps[1:]
		}
		_ = o.publishSession(ctx, session)
	}
	o.mu.Lock()
	o.submittedPlans[sessionID+":"+planHash] = struct{}{}
	o.mu.Unlock()
	session.State = domain.AssistantSessionStateCompleted
	session.PendingSteps = nil
	_ = o.publishSession(ctx, session)
	lock.Unlock()
	return o.publishApprovalResult(ctx, dTag, event, sessionID, "completed", "completed", map[string]any{"summary": "assistant plan completed", "plan_hash": planHash})
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
	merged, err := o.subscriber.SubscribeAllWithEOSE(observeCtx, []nostr.Filter{{Kinds: append([]int(nil), receipt.ResultKinds...), Tags: nostr.TagMap{"e": []string{receipt.RequestEventID}}}})
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
			if _, dup := seen[ev.ID]; dup {
				continue
			}
			seen[ev.ID] = struct{}{}
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

// PublishFailure publishes an immediate assistant failure result for rejected/malformed requests.
func (o *AssistantOrchestrator) PublishFailure(ctx context.Context, event *nostr.Event, sessionID, step, message string) error {
	if sessionID == "" && event != nil {
		sessionID = sessionFromEvent(event)
	}
	_, err := o.publishResult(ctx, event, sessionID, "failed", step, map[string]any{"summary": message, "error": message})
	return err
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
	streamingClient, ok := o.chatClient.(AssistantStreamingChatClient)
	if !ok {
		return o.chatClient.PlanFromPrompt(ctx, systemPrompt, userPrompt)
	}

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
		{"d", session.SessionID},
		{"session", session.SessionID},
	}
	for _, participant := range session.Participants {
		tags = append(tags, nostr.Tag{"p", participant, "", "operator"})
	}
	tags = append(tags,
		nostr.Tag{"agent", o.identity.AgentID},
		nostr.Tag{"status", string(session.State)},
	)
	ev := &nostr.Event{Kind: domain.KindAssistantSession, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
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
	tags := o.replyTags(requestEvent, sessionID, status)
	if planHash := stringFromMap(content, "plan_hash"); planHash != "" {
		tags = append(tags, nostr.Tag{"plan-hash", planHash})
	}
	if stepID := stringFromMap(content, "step_id"); stepID != "" {
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

func (o *AssistantOrchestrator) publishResult(ctx context.Context, requestEvent *nostr.Event, sessionID, status, step string, content map[string]any) (*nostr.Event, error) {
	if content == nil {
		content = map[string]any{}
	}
	content["status"] = status
	if step != "" {
		content["step"] = step
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal assistant result: %w", err)
	}
	tags := o.replyTags(requestEvent, sessionID, status)
	if step != "" {
		tags = append(tags, nostr.Tag{"step", step})
	}
	if planHash := stringFromMap(content, "plan_hash"); planHash != "" {
		tags = append(tags, nostr.Tag{"plan-hash", planHash})
	}
	ev := &nostr.Event{Kind: domain.KindAssistantResult, CreatedAt: nostr.Now(), Tags: tags, Content: string(contentJSON)}
	if err := o.signAndPublish(ctx, ev); err != nil {
		return ev, err
	}
	return ev, nil
}

func (o *AssistantOrchestrator) replyTags(requestEvent *nostr.Event, sessionID, status string) nostr.Tags {
	tags := nostr.Tags{{"session", sessionID}, {"agent", o.identity.AgentID}, {"status", status}}
	if requestEvent != nil {
		tags = append(tags, nostr.Tag{"e", requestEvent.ID, "", "reply"}, nostr.Tag{"p", requestEvent.PubKey})
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
	published, err := o.publisher.Publish(ctx, *ev)
	if err != nil {
		return fmt.Errorf("publish assistant event: %w", err)
	}
	if published == 0 {
		return fmt.Errorf("publish assistant event: no relay accepted event")
	}
	return nil
}

func (o *AssistantOrchestrator) republish(ctx context.Context, ev *nostr.Event) error {
	if ev == nil || o.publisher == nil {
		return nil
	}
	_, err := o.publisher.Publish(ctx, *ev)
	return err
}

func (o *AssistantOrchestrator) publishPromptResult(ctx context.Context, dTag string, requestEvent *nostr.Event, sessionID, status, step string, content map[string]any) error {
	ev, err := o.publishResult(ctx, requestEvent, sessionID, status, step, content)
	return o.storePromptResult(ctx, dTag, ev, err)
}

func (o *AssistantOrchestrator) storePromptResult(ctx context.Context, dTag string, ev *nostr.Event, err error) error {
	if err == nil && dTag != "" && ev != nil {
		o.mu.Lock()
		o.processedTurns[dTag] = ev
		o.mu.Unlock()
	}
	return err
}

func (o *AssistantOrchestrator) publishApprovalResult(ctx context.Context, dTag string, requestEvent *nostr.Event, sessionID, status, step string, content map[string]any) error {
	ev, err := o.publishResult(ctx, requestEvent, sessionID, status, step, content)
	return o.markApprovalProcessed(dTag, ev, err)
}

func (o *AssistantOrchestrator) markApprovalProcessed(dTag string, _ *nostr.Event, err error) error {
	if err == nil && dTag != "" {
		o.mu.Lock()
		o.processedApprovals[dTag] = struct{}{}
		o.mu.Unlock()
	}
	return err
}

func (o *AssistantOrchestrator) systemPrompt() string {
	names := make([]string, 0, len(o.allowedTools))
	for name := range o.allowedTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return "You are the Bahia Operator Assistant. Produce a conservative AssistantPlan JSON object. Use only these assistant-safe event-native tools: " + strings.Join(names, ", ") + ".\n\n" +
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
	canonicalEvent := canonicalnostr.Event{CreatedAt: canonicalnostr.Timestamp(ev.CreatedAt), Kind: canonicalnostr.Kind(ev.Kind), Tags: toCanonicalTags(ev.Tags), Content: ev.Content}
	if err := signer.SignEvent(ctx, &canonicalEvent); err != nil {
		return err
	}
	ev.ID = canonicalEvent.ID.Hex()
	ev.PubKey = canonicalEvent.PubKey.Hex()
	ev.CreatedAt = nostr.Timestamp(canonicalEvent.CreatedAt)
	ev.Kind = int(canonicalEvent.Kind)
	ev.Tags = toGoNostrTags(canonicalEvent.Tags)
	ev.Content = canonicalEvent.Content
	ev.Sig = canonicalnostr.HexEncodeToString(canonicalEvent.Sig[:])
	return nil
}

func toCanonicalTags(tags nostr.Tags) canonicalnostr.Tags {
	converted := make(canonicalnostr.Tags, 0, len(tags))
	for _, tag := range tags {
		converted = append(converted, canonicalnostr.Tag(append([]string(nil), tag...)))
	}
	return converted
}

func toGoNostrTags(tags canonicalnostr.Tags) nostr.Tags {
	converted := make(nostr.Tags, 0, len(tags))
	for _, tag := range tags {
		converted = append(converted, nostr.Tag(append([]string(nil), tag...)))
	}
	return converted
}
