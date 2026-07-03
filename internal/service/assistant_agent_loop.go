package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	defaultAssistantAgentLoopMaxIterations              = 12
	defaultAssistantAgentLoopMaxConsecutiveToolFailures = 3
)

// AssistantAgentModelHistoryBuilder supplies replay-backed model messages for
// one assistant turn. AssistantContextBuilder implements this interface.
type AssistantAgentModelHistoryBuilder interface {
	BuildModelHistory(ctx context.Context, sessionID string, routeContext map[string]string, selectedRefs []string, currentOperatorPrompt string) ([]domain.AssistantAgentMessage, error)
}

// AssistantAgentToolSchemaProvider supplies provider-neutral native tool schemas
// for the model request. Item 8 adapts MCP descriptors into this service-local
// shape without importing internal/mcp into the loop.
type AssistantAgentToolSchemaProvider interface {
	AgentToolSchemas(ctx context.Context) ([]llm.AgentToolSchema, error)
}

// AssistantAgentTranscriptAppender appends durable assistant transcript messages.
// AssistantTranscriptStore implements this interface.
type AssistantAgentTranscriptAppender interface {
	AppendMessage(ctx context.Context, appendReq AssistantTranscriptAppend) (*AssistantTranscriptRecord, error)
}

// AssistantAgentLoopConfig wires the service-layer agent loop. The loop consumes
// the model seam, replay-backed context builder, descriptor-derived schemas, and
// item-5 tool runtime; it deliberately does not import internal/mcp.
type AssistantAgentLoopConfig struct {
	ModelClient    llm.AgentModelClient
	ToolRuntime    *AssistantToolRuntime
	ContextBuilder AssistantAgentModelHistoryBuilder
	ToolSchemas    AssistantAgentToolSchemaProvider
	Transcript     AssistantAgentTranscriptAppender
	Sessions       AssistantToolRuntimeSessionPersister
	Agentic        config.AssistantAgenticConfig
	Now            func() time.Time
	NewID          func(prefix string) string
}

// AssistantAgentLoop executes one assistant turn as a resumable model/tool loop.
type AssistantAgentLoop struct {
	modelClient    llm.AgentModelClient
	toolRuntime    *AssistantToolRuntime
	contextBuilder AssistantAgentModelHistoryBuilder
	toolSchemas    AssistantAgentToolSchemaProvider
	transcript     AssistantAgentTranscriptAppender
	sessions       AssistantToolRuntimeSessionPersister
	agentic        config.AssistantAgenticConfig
	now            func() time.Time
	newID          func(prefix string) string
}

// AssistantAgentTurnRequest starts a new operator turn.
type AssistantAgentTurnRequest struct {
	Session        *domain.AssistantSession
	Prompt         string
	TurnID         string
	RouteContext   map[string]string
	SelectedRefs   []string
	OperatorPubkey string
	PlanHash       string
	CancelScope    string
}

// AssistantAgentResumeAsyncRequest resumes a turn that suspended on a
// waiting_async observation.
type AssistantAgentResumeAsyncRequest struct {
	Session      *domain.AssistantSession
	RouteContext map[string]string
	SelectedRefs []string
}

// AssistantAgentActionDecisionRequest resumes a turn that suspended on a
// deferred approval action.
type AssistantAgentActionDecisionRequest struct {
	Session        *domain.AssistantSession
	ActionID       string
	Decision       string
	Reason         string
	RouteContext   map[string]string
	SelectedRefs   []string
	OperatorPubkey string
}

// AssistantAgentLoopResult describes the externally visible outcome of one loop
// entry-point call. A suspended result is not terminal for the run; item 8 should
// persist the session emitted by the runtime/loop and call the matching resume
// method later.
type AssistantAgentLoopResult struct {
	RunID          string
	TurnID         string
	Iteration      int
	State          domain.AssistantAgentLoopState
	SessionState   domain.AssistantSessionState
	Suspended      bool
	Completed      bool
	DeferredAction *domain.AssistantDeferredAction
	Observations   []*domain.AssistantToolObservation
	StopReason     llm.AgentStopReason
	Error          string
}

// NewAssistantAgentLoop constructs the resumable assistant loop.
func NewAssistantAgentLoop(config AssistantAgentLoopConfig) *AssistantAgentLoop {
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newID := config.NewID
	if newID == nil {
		newID = randomAssistantAgentLoopID
	}
	return &AssistantAgentLoop{modelClient: config.ModelClient, toolRuntime: config.ToolRuntime, contextBuilder: config.ContextBuilder, toolSchemas: config.ToolSchemas, transcript: config.Transcript, sessions: config.Sessions, agentic: config.Agentic, now: now, newID: newID}
}

// StartTurn builds replay-backed model input, appends the current user message to
// the transcript, and iterates until the model stops or the turn suspends.
func (l *AssistantAgentLoop) StartTurn(ctx context.Context, req AssistantAgentTurnRequest) (*AssistantAgentLoopResult, error) {
	if err := l.validateReady(req.Session); err != nil {
		return nil, err
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("assistant agent turn prompt is required")
	}
	turnID := firstNonEmptyString(strings.TrimSpace(req.TurnID), strings.TrimSpace(req.Session.CurrentTurnID), l.newID("turn"))
	runID := l.newID("run")
	req.Session.CurrentTurnID = turnID
	if req.OperatorPubkey != "" {
		req.Session.OperatorPubkey = strings.TrimSpace(req.OperatorPubkey)
	}
	metadata := domain.AssistantAgentLoopMetadata{RunID: runID, Iteration: 0, State: domain.AssistantAgentLoopStateRunning, MaxIterations: l.maxIterations(), MaxConsecutiveToolFailures: l.maxConsecutiveToolFailures(), UpdatedAt: l.now().UTC()}
	setAssistantAgentLoopMetadata(req.Session, metadata)
	req.Session.State = domain.AssistantSessionStatePlanning
	if err := l.persistSession(ctx, req.Session); err != nil {
		return nil, err
	}
	_ = l.publishStatus(ctx, req.Session.SessionID, "planning", map[string]any{"phase": "loop_started", "run_id": runID, "turn_id": turnID})

	messages, err := l.contextBuilder.BuildModelHistory(ctx, req.Session.SessionID, cloneAssistantLoopStringMap(req.RouteContext), append([]string(nil), req.SelectedRefs...), prompt)
	if err != nil {
		return l.failLoop(ctx, req.Session, "context_error", err), err
	}
	messages = ensureAssistantCurrentPrompt(messages, prompt)
	seq := countAssistantTranscriptMessages(messages)
	if err := l.appendTranscript(ctx, req.Session, turnID, runID, seq, lastAssistantMessage(messages), map[string]any{"phase": "user_prompt"}); err != nil {
		return l.failLoop(ctx, req.Session, "transcript_append_failed", err), err
	}
	req.Session.State = domain.AssistantSessionStateExecuting
	if err := l.persistSession(ctx, req.Session); err != nil {
		return nil, err
	}
	return l.continueLoop(ctx, assistantAgentLoopRun{session: req.Session, runID: runID, turnID: turnID, routeContext: req.RouteContext, selectedRefs: req.SelectedRefs, messages: messages, nextSequence: seq + 1, planHash: req.PlanHash, cancelScope: req.CancelScope})
}

// ResumeAfterAsyncObservation observes the terminal event for a waiting async
// tool and feeds the normalized observation back into the model loop.
func (l *AssistantAgentLoop) ResumeAfterAsyncObservation(ctx context.Context, req AssistantAgentResumeAsyncRequest) (*AssistantAgentLoopResult, error) {
	if err := l.validateReady(req.Session); err != nil {
		return nil, err
	}
	metadata := assistantAgentLoopMetadata(req.Session)
	if metadata.State != domain.AssistantAgentLoopStateWaitingAsync || metadata.WaitingReceipt == nil {
		return nil, fmt.Errorf("assistant agent loop is not waiting for async observation")
	}
	messages, seq, err := l.replayedMessages(ctx, req.Session, req.RouteContext, req.SelectedRefs)
	if err != nil {
		return l.failLoop(ctx, req.Session, "context_error", err), err
	}
	obs, err := l.toolRuntime.ResumeAsync(ctx, AssistantToolResumeRequest{Session: req.Session, RunID: metadata.RunID})
	if err != nil {
		return l.failLoop(ctx, req.Session, "async_resume_failed", err), err
	}
	if obs == nil {
		return nil, fmt.Errorf("assistant async resume returned no observation")
	}
	if blockedObservation(obs) {
		return l.result(req.Session, []*domain.AssistantToolObservation{obs}, ""), nil
	}
	messages = append(messages, assistantToolObservationMessage(obs))
	if err := l.appendTranscript(ctx, req.Session, firstNonEmptyString(req.Session.CurrentTurnID, metadataRunTurnID(metadata)), metadata.RunID, seq, lastAssistantMessage(messages), map[string]any{"phase": "tool_observed"}); err != nil {
		return l.failLoop(ctx, req.Session, "transcript_append_failed", err), err
	}
	if guarded := l.recordObservationAndMaybeBlock(ctx, req.Session, obs); guarded != nil {
		guarded.Observations = append(guarded.Observations, obs)
		return guarded, nil
	}
	return l.continueLoop(ctx, assistantAgentLoopRun{session: req.Session, runID: metadata.RunID, turnID: req.Session.CurrentTurnID, routeContext: req.RouteContext, selectedRefs: req.SelectedRefs, messages: messages, nextSequence: seq + 1, observations: []*domain.AssistantToolObservation{obs}})
}

// ResumeAfterActionDecision resumes an approval-suspended turn. Approvals execute
// the original deferred tool call through the runtime; rejections feed a denial
// observation back to the model without executing the tool.
func (l *AssistantAgentLoop) ResumeAfterActionDecision(ctx context.Context, req AssistantAgentActionDecisionRequest) (*AssistantAgentLoopResult, error) {
	if err := l.validateReady(req.Session); err != nil {
		return nil, err
	}
	metadata := assistantAgentLoopMetadata(req.Session)
	if metadata.State != domain.AssistantAgentLoopStateAwaitingApproval {
		return nil, fmt.Errorf("assistant agent loop is not awaiting approval")
	}
	action, err := assistantDeferredAction(req.Session, req.ActionID)
	if err != nil {
		return nil, err
	}
	messages, seq, err := l.replayedMessages(ctx, req.Session, req.RouteContext, req.SelectedRefs)
	if err != nil {
		return l.failLoop(ctx, req.Session, "context_error", err), err
	}
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	var obs *domain.AssistantToolObservation
	switch decision {
	case "approve", "approved", "allow", "allowed":
		clearAssistantDeferredAction(req.Session, action.ActionID)
		call := domain.AssistantAgentToolCall{ID: action.ToolCallID, Name: action.ToolName, Arguments: cloneAnyArgs(action.ToolArgs)}
		obs, err = l.toolRuntime.Execute(ctx, AssistantToolRuntimeRequest{Session: req.Session, RunID: firstNonEmptyString(action.RunID, metadata.RunID), TurnID: firstNonEmptyString(action.TurnID, req.Session.CurrentTurnID), Iteration: metadata.Iteration, ToolCall: call, PlanHash: action.PlanHash, CancelScope: action.CancelScope, ApprovedAction: action})
		if err != nil {
			return l.failLoop(ctx, req.Session, "approved_action_execute_failed", err), err
		}
	case "reject", "rejected", "deny", "denied", "cancel", "cancelled", "canceled":
		clearAssistantDeferredAction(req.Session, action.ActionID)
		obs = l.rejectedActionObservation(action, req.Reason)
		metadata.State = domain.AssistantAgentLoopStateRunning
		metadata.PendingActionID = ""
		metadata.PendingToolCallID = ""
		metadata.LastObservationID = obs.ObservationID
		metadata.UpdatedAt = l.now().UTC()
		setAssistantAgentLoopMetadata(req.Session, metadata)
		req.Session.State = domain.AssistantSessionStateExecuting
		if err := l.persistSession(ctx, req.Session); err != nil {
			return nil, err
		}
		_ = l.publishStatus(ctx, req.Session.SessionID, "executing", map[string]any{"phase": "tool_observed", "summary": obs.Summary, "tool_call_id": obs.ToolCallID, "tool_name": obs.ToolName, "observation_id": obs.ObservationID, "observation_status": string(obs.Status), "action_id": action.ActionID})
	default:
		return nil, fmt.Errorf("unsupported assistant action decision %q", req.Decision)
	}
	if obs == nil {
		return nil, fmt.Errorf("assistant action decision produced no observation")
	}
	if obs.Status == domain.AssistantToolObservationDeferred {
		return l.suspendedResult(req.Session, obs), nil
	}
	if obs.Status == domain.AssistantToolObservationWaitingAsync {
		return l.suspendedResult(req.Session, obs), nil
	}
	messages = append(messages, assistantToolObservationMessage(obs))
	if err := l.appendTranscript(ctx, req.Session, firstNonEmptyString(action.TurnID, req.Session.CurrentTurnID), firstNonEmptyString(action.RunID, metadata.RunID), seq, lastAssistantMessage(messages), map[string]any{"phase": "tool_observed", "action_id": action.ActionID, "decision": decision}); err != nil {
		return l.failLoop(ctx, req.Session, "transcript_append_failed", err), err
	}
	if guarded := l.recordObservationAndMaybeBlock(ctx, req.Session, obs); guarded != nil {
		guarded.Observations = append(guarded.Observations, obs)
		return guarded, nil
	}
	return l.continueLoop(ctx, assistantAgentLoopRun{session: req.Session, runID: firstNonEmptyString(action.RunID, metadata.RunID), turnID: firstNonEmptyString(action.TurnID, req.Session.CurrentTurnID), routeContext: req.RouteContext, selectedRefs: req.SelectedRefs, messages: messages, nextSequence: seq + 1, observations: []*domain.AssistantToolObservation{obs}})
}

type assistantAgentLoopRun struct {
	session      *domain.AssistantSession
	runID        string
	turnID       string
	routeContext map[string]string
	selectedRefs []string
	messages     []domain.AssistantAgentMessage
	nextSequence int
	observations []*domain.AssistantToolObservation
	planHash     string
	cancelScope  string
}

func (l *AssistantAgentLoop) continueLoop(ctx context.Context, run assistantAgentLoopRun) (*AssistantAgentLoopResult, error) {
	tools, err := l.toolSchemas.AgentToolSchemas(ctx)
	if err != nil {
		return l.failLoop(ctx, run.session, "tool_schema_error", err), err
	}
	for {
		metadata := assistantAgentLoopMetadata(run.session)
		if metadata.RunID == "" {
			metadata.RunID = run.runID
		}
		if run.runID == "" {
			run.runID = metadata.RunID
		}
		if metadata.Iteration >= l.maxIterations() {
			return l.blockLoop(ctx, run.session, fmt.Sprintf("assistant agent loop exceeded max_iterations=%d", l.maxIterations()), run.observations), nil
		}
		metadata.Iteration++
		metadata.State = domain.AssistantAgentLoopStateRunning
		metadata.MaxIterations = l.maxIterations()
		metadata.MaxConsecutiveToolFailures = l.maxConsecutiveToolFailures()
		metadata.UpdatedAt = l.now().UTC()
		setAssistantAgentLoopMetadata(run.session, metadata)
		run.session.State = domain.AssistantSessionStateExecuting
		if err := l.persistSession(ctx, run.session); err != nil {
			return nil, err
		}
		_ = l.publishStatus(ctx, run.session.SessionID, "executing", map[string]any{"phase": "model_requested", "run_id": run.runID, "turn_id": run.turnID, "iteration": metadata.Iteration})

		resp, err := l.modelClient.Next(ctx, llm.AgentModelRequest{Model: l.agentic.Model, Messages: run.messages, Tools: tools, ToolChoice: llm.AgentToolChoice{Mode: llm.AgentToolChoiceAuto}, Metadata: map[string]any{"session_id": run.session.SessionID, "run_id": run.runID, "turn_id": run.turnID, "iteration": metadata.Iteration}}, nil)
		if err != nil {
			return l.failLoop(ctx, run.session, "model_error", err), err
		}
		assistantMessage := domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleAssistant, Content: append([]domain.AssistantAgentContentBlock(nil), resp.Content...), ToolCalls: append([]domain.AssistantAgentToolCall(nil), resp.ToolCalls...), Metadata: map[string]any{"stop_reason": string(resp.StopReason), "iteration": metadata.Iteration}}
		run.messages = append(run.messages, assistantMessage)
		if err := l.appendTranscript(ctx, run.session, run.turnID, run.runID, run.nextSequence, assistantMessage, map[string]any{"phase": "assistant_model_response", "stop_reason": string(resp.StopReason)}); err != nil {
			return l.failLoop(ctx, run.session, "transcript_append_failed", err), err
		}
		run.nextSequence++

		if len(resp.ToolCalls) == 0 {
			if resp.StopReason == llm.AgentStopReasonMaxTokens || resp.StopReason == llm.AgentStopReasonContentGuard {
				return l.failLoop(ctx, run.session, "model_stopped_before_completion", fmt.Errorf("assistant model stopped with %s", resp.StopReason)), nil
			}
			completed, err := l.completeLoop(ctx, run.session, resp.StopReason, run.observations)
			return completed, err
		}

		for _, toolCall := range resp.ToolCalls {
			_ = l.publishStatus(ctx, run.session.SessionID, "executing", map[string]any{"phase": "tool_call_requested", "run_id": run.runID, "turn_id": run.turnID, "iteration": metadata.Iteration, "tool_call_id": toolCall.ID, "tool_name": toolCall.Name})
			obs, err := l.toolRuntime.Execute(ctx, AssistantToolRuntimeRequest{Session: run.session, RunID: run.runID, TurnID: run.turnID, Iteration: metadata.Iteration, ToolCall: toolCall, PlanHash: run.planHash, CancelScope: run.cancelScope})
			if err != nil {
				return l.failLoop(ctx, run.session, "tool_execute_error", err), err
			}
			if obs == nil {
				return l.failLoop(ctx, run.session, "tool_execute_error", fmt.Errorf("assistant tool runtime returned no observation")), nil
			}
			if obs.Status == domain.AssistantToolObservationDeferred || obs.Status == domain.AssistantToolObservationWaitingAsync {
				return l.suspendedResult(run.session, obs), nil
			}
			run.observations = append(run.observations, obs)
			run.messages = append(run.messages, assistantToolObservationMessage(obs))
			if err := l.appendTranscript(ctx, run.session, run.turnID, run.runID, run.nextSequence, lastAssistantMessage(run.messages), map[string]any{"phase": "tool_observed"}); err != nil {
				return l.failLoop(ctx, run.session, "transcript_append_failed", err), err
			}
			run.nextSequence++
			if guarded := l.recordObservationAndMaybeBlock(ctx, run.session, obs); guarded != nil {
				guarded.Observations = append([]*domain.AssistantToolObservation(nil), run.observations...)
				return guarded, nil
			}
		}
	}
}

func (l *AssistantAgentLoop) validateReady(session *domain.AssistantSession) error {
	if l == nil {
		return fmt.Errorf("assistant agent loop is not configured")
	}
	if l.modelClient == nil {
		return fmt.Errorf("assistant agent model client is not configured")
	}
	if l.toolRuntime == nil {
		return fmt.Errorf("assistant tool runtime is not configured")
	}
	if l.contextBuilder == nil {
		return fmt.Errorf("assistant context builder is not configured")
	}
	if l.toolSchemas == nil {
		return fmt.Errorf("assistant tool schema provider is not configured")
	}
	if session == nil {
		return fmt.Errorf("assistant session is required")
	}
	if strings.TrimSpace(session.SessionID) == "" {
		return fmt.Errorf("assistant session_id is required")
	}
	return nil
}

func (l *AssistantAgentLoop) replayedMessages(ctx context.Context, session *domain.AssistantSession, routeContext map[string]string, selectedRefs []string) ([]domain.AssistantAgentMessage, int, error) {
	messages, err := l.contextBuilder.BuildModelHistory(ctx, session.SessionID, cloneAssistantLoopStringMap(routeContext), append([]string(nil), selectedRefs...), "")
	if err != nil {
		return nil, 0, err
	}
	return messages, countAssistantTranscriptMessages(messages) + 1, nil
}

func (l *AssistantAgentLoop) appendTranscript(ctx context.Context, session *domain.AssistantSession, turnID, runID string, sequence int, message domain.AssistantAgentMessage, metadata map[string]any) error {
	if l.transcript == nil {
		return nil
	}
	if message.Role == "" {
		return fmt.Errorf("assistant transcript message role is required")
	}
	record, err := l.transcript.AppendMessage(ctx, AssistantTranscriptAppend{SessionID: session.SessionID, TurnID: turnID, RunID: runID, Sequence: sequence, Message: message, OperatorPubkey: session.OperatorPubkey, Metadata: metadata})
	if err != nil {
		return err
	}
	loopMetadata := assistantAgentLoopMetadata(session)
	if record != nil && record.EventID != "" {
		loopMetadata.TranscriptCursor = record.EventID
	} else {
		loopMetadata.TranscriptCursor = strconv.Itoa(sequence)
	}
	loopMetadata.UpdatedAt = l.now().UTC()
	setAssistantAgentLoopMetadata(session, loopMetadata)
	return nil
}

func (l *AssistantAgentLoop) recordObservationAndMaybeBlock(ctx context.Context, session *domain.AssistantSession, obs *domain.AssistantToolObservation) *AssistantAgentLoopResult {
	metadata := assistantAgentLoopMetadata(session)
	metadata.LastObservationID = obs.ObservationID
	if toolObservationCountsAsFailure(obs) {
		metadata.ConsecutiveToolFailures++
	} else if obs.Status == domain.AssistantToolObservationSucceeded {
		metadata.ConsecutiveToolFailures = 0
	}
	metadata.UpdatedAt = l.now().UTC()
	setAssistantAgentLoopMetadata(session, metadata)
	_ = l.persistSession(ctx, session)
	if metadata.ConsecutiveToolFailures >= l.maxConsecutiveToolFailures() {
		return l.blockLoop(ctx, session, fmt.Sprintf("assistant agent loop exceeded max_consecutive_tool_failures=%d", l.maxConsecutiveToolFailures()), nil)
	}
	return nil
}

func (l *AssistantAgentLoop) completeLoop(ctx context.Context, session *domain.AssistantSession, stopReason llm.AgentStopReason, observations []*domain.AssistantToolObservation) (*AssistantAgentLoopResult, error) {
	metadata := assistantAgentLoopMetadata(session)
	metadata.State = domain.AssistantAgentLoopStateCompleted
	metadata.UpdatedAt = l.now().UTC()
	setAssistantAgentLoopMetadata(session, metadata)
	session.State = domain.AssistantSessionStateCompleted
	if err := l.persistSession(ctx, session); err != nil {
		return nil, err
	}
	_ = l.publishStatus(ctx, session.SessionID, "completed", map[string]any{"phase": "loop_completed", "run_id": metadata.RunID, "iteration": metadata.Iteration, "stop_reason": string(stopReason)})
	res := l.result(session, observations, "")
	res.StopReason = stopReason
	res.Completed = true
	return res, nil
}

func (l *AssistantAgentLoop) blockLoop(ctx context.Context, session *domain.AssistantSession, reason string, observations []*domain.AssistantToolObservation) *AssistantAgentLoopResult {
	metadata := assistantAgentLoopMetadata(session)
	metadata.State = domain.AssistantAgentLoopStateBlocked
	metadata.UpdatedAt = l.now().UTC()
	setAssistantAgentLoopMetadata(session, metadata)
	session.State = domain.AssistantSessionStateBlocked
	_ = l.persistSession(ctx, session)
	_ = l.publishStatus(ctx, session.SessionID, "blocked", map[string]any{"phase": "loop_guard_blocked", "summary": reason, "error": reason, "run_id": metadata.RunID, "iteration": metadata.Iteration})
	return l.result(session, observations, reason)
}

func (l *AssistantAgentLoop) failLoop(ctx context.Context, session *domain.AssistantSession, phase string, err error) *AssistantAgentLoopResult {
	if session == nil {
		return &AssistantAgentLoopResult{State: domain.AssistantAgentLoopStateFailed, SessionState: domain.AssistantSessionStateFailed, Error: errString(err)}
	}
	metadata := assistantAgentLoopMetadata(session)
	metadata.State = domain.AssistantAgentLoopStateFailed
	metadata.UpdatedAt = l.now().UTC()
	setAssistantAgentLoopMetadata(session, metadata)
	session.State = domain.AssistantSessionStateFailed
	_ = l.persistSession(ctx, session)
	_ = l.publishStatus(ctx, session.SessionID, "failed", map[string]any{"phase": phase, "summary": errString(err), "error": errString(err), "run_id": metadata.RunID, "iteration": metadata.Iteration})
	return l.result(session, nil, errString(err))
}

func (l *AssistantAgentLoop) suspendedResult(session *domain.AssistantSession, obs *domain.AssistantToolObservation) *AssistantAgentLoopResult {
	metadata := assistantAgentLoopMetadata(session)
	res := l.result(session, []*domain.AssistantToolObservation{obs}, "")
	res.Suspended = true
	if obs.Deferred != nil {
		res.DeferredAction = obs.Deferred
	}
	res.State = metadata.State
	res.SessionState = session.State
	return res
}

func (l *AssistantAgentLoop) result(session *domain.AssistantSession, observations []*domain.AssistantToolObservation, errText string) *AssistantAgentLoopResult {
	metadata := assistantAgentLoopMetadata(session)
	return &AssistantAgentLoopResult{RunID: metadata.RunID, TurnID: session.CurrentTurnID, Iteration: metadata.Iteration, State: metadata.State, SessionState: session.State, Completed: metadata.State == domain.AssistantAgentLoopStateCompleted, Suspended: metadata.State == domain.AssistantAgentLoopStateWaitingAsync || metadata.State == domain.AssistantAgentLoopStateAwaitingApproval, Observations: observations, Error: errText}
}

func (l *AssistantAgentLoop) rejectedActionObservation(action *domain.AssistantDeferredAction, reason string) *domain.AssistantToolObservation {
	summary := "operator rejected assistant tool approval"
	if strings.TrimSpace(reason) != "" {
		summary = summary + ": " + strings.TrimSpace(reason)
	}
	permission := action.Permission
	permission.Decision = domain.AssistantPermissionDecisionDeny
	if permission.Metadata == nil {
		permission.Metadata = map[string]any{}
	}
	permission.Metadata["approved_action_id"] = action.ActionID
	permission.Metadata["approval_decision"] = "reject"
	return &domain.AssistantToolObservation{ObservationID: l.newID("obs"), ToolCallID: action.ToolCallID, ToolName: action.ToolName, Status: domain.AssistantToolObservationDenied, Effect: permission.Effect, Risk: permission.Risk, ExecutionMode: permission.ExecutionMode, Summary: summary, Error: firstNonEmptyString(strings.TrimSpace(reason), "operator rejected assistant tool approval"), ObservedAt: l.now().UTC(), Metadata: map[string]any{"permission": permission, "action_id": action.ActionID}}
}

func (l *AssistantAgentLoop) persistSession(ctx context.Context, session *domain.AssistantSession) error {
	if l.sessions == nil {
		return nil
	}
	return l.sessions.PersistAssistantSession(ctx, session)
}

func (l *AssistantAgentLoop) publishStatus(ctx context.Context, sessionID, status string, content map[string]any) error {
	if l.sessions == nil {
		return nil
	}
	return l.sessions.PublishAssistantStatus(ctx, sessionID, status, content)
}

func (l *AssistantAgentLoop) maxIterations() int {
	if l != nil && l.agentic.MaxIterations > 0 {
		return l.agentic.MaxIterations
	}
	return defaultAssistantAgentLoopMaxIterations
}

func (l *AssistantAgentLoop) maxConsecutiveToolFailures() int {
	if l != nil && l.agentic.MaxConsecutiveToolFailures > 0 {
		return l.agentic.MaxConsecutiveToolFailures
	}
	return defaultAssistantAgentLoopMaxConsecutiveToolFailures
}

func ensureAssistantCurrentPrompt(messages []domain.AssistantAgentMessage, prompt string) []domain.AssistantAgentMessage {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return messages
	}
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if last.Role == domain.AssistantAgentMessageRoleUser && strings.TrimSpace(assistantAgentMessageText(last)) == prompt {
			return messages
		}
	}
	return append(messages, domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: prompt}}})
}

func assistantToolObservationMessage(obs *domain.AssistantToolObservation) domain.AssistantAgentMessage {
	return domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleTool, Name: obs.ToolName, ToolCallID: obs.ToolCallID, Observation: obs, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentObservation, Observation: obs}}}
}

func countAssistantTranscriptMessages(messages []domain.AssistantAgentMessage) int {
	count := 0
	for _, msg := range messages {
		if msg.Role != domain.AssistantAgentMessageRoleSystem {
			count++
		}
	}
	return count
}

func lastAssistantMessage(messages []domain.AssistantAgentMessage) domain.AssistantAgentMessage {
	if len(messages) == 0 {
		return domain.AssistantAgentMessage{}
	}
	return messages[len(messages)-1]
}

func toolObservationCountsAsFailure(obs *domain.AssistantToolObservation) bool {
	if obs == nil {
		return false
	}
	switch obs.Status {
	case domain.AssistantToolObservationFailed, domain.AssistantToolObservationDenied, domain.AssistantToolObservationCancelled:
		return true
	default:
		return false
	}
}

func blockedObservation(obs *domain.AssistantToolObservation) bool {
	return obs != nil && obs.Status == domain.AssistantToolObservationFailed && obs.Metadata != nil && obs.Metadata["blocked"] == true
}

func assistantDeferredAction(session *domain.AssistantSession, actionID string) (*domain.AssistantDeferredAction, error) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return nil, fmt.Errorf("assistant action_id is required")
	}
	if session == nil || session.Metadata == nil {
		return nil, fmt.Errorf("assistant deferred action %q not found", actionID)
	}
	actions, _ := session.Metadata[assistantDeferredActionsMetadataKey].(map[string]any)
	raw, ok := actions[actionID]
	if !ok || raw == nil {
		return nil, fmt.Errorf("assistant deferred action %q not found", actionID)
	}
	var action domain.AssistantDeferredAction
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("decode assistant deferred action %q: %w", actionID, err)
	}
	if err := json.Unmarshal(encoded, &action); err != nil {
		return nil, fmt.Errorf("decode assistant deferred action %q: %w", actionID, err)
	}
	if action.ActionID == "" {
		action.ActionID = actionID
	}
	return &action, nil
}

func clearAssistantDeferredAction(session *domain.AssistantSession, actionID string) {
	if session == nil || session.Metadata == nil {
		return
	}
	actions, _ := session.Metadata[assistantDeferredActionsMetadataKey].(map[string]any)
	delete(actions, actionID)
}

func cloneAssistantLoopStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func metadataRunTurnID(metadata domain.AssistantAgentLoopMetadata) string {
	if metadata.RunID == "" {
		return ""
	}
	return metadata.RunID + ":resume"
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func randomAssistantAgentLoopID(prefix string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s_%d", strings.TrimSpace(prefix), time.Now().UnixNano())
	}
	return strings.TrimSpace(prefix) + "_" + hex.EncodeToString(buf[:])
}

var _ interface {
	StartTurn(ctx context.Context, req AssistantAgentTurnRequest) (*AssistantAgentLoopResult, error)
	ResumeAfterAsyncObservation(ctx context.Context, req AssistantAgentResumeAsyncRequest) (*AssistantAgentLoopResult, error)
	ResumeAfterActionDecision(ctx context.Context, req AssistantAgentActionDecisionRequest) (*AssistantAgentLoopResult, error)
} = (*AssistantAgentLoop)(nil)
