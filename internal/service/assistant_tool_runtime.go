package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	assistantAgentLoopMetadataKey       = "agent_loop"
	assistantDeferredActionsMetadataKey = "deferred_actions"
)

// AssistantToolRuntimeToolContent is the runtime-local projection of one MCP
// content block. Keeping this package-local avoids an internal/service ->
// internal/mcp import cycle; item 8 can adapt the real MCP result directly.
type AssistantToolRuntimeToolContent struct {
	Type string
	Text string
}

// AssistantToolRuntimeToolResult is the runtime-local projection of an MCP tool
// result.
type AssistantToolRuntimeToolResult struct {
	Content []AssistantToolRuntimeToolContent
	IsError bool
}

// AssistantToolRuntimeToolDescriptor is the descriptor metadata supplied by the
// item-2 MCP assistant registry, projected into service-local types to avoid a
// package cycle.
type AssistantToolRuntimeToolDescriptor struct {
	Name          string
	ExecutionMode domain.AssistantToolExecutionMode
	Effect        domain.AssistantToolEffect
	DefaultRisk   domain.AssistantToolRisk
	ResourceTypes []string
	InputSchema   map[string]any
}

// AssistantToolRuntimeMCPServer is the MCP surface required by the agent tool
// runtime. The production adapter should call mcp.Server.CallTool for sync tools
// and mcp.Server.InvokeAssistantAsyncTool for async tools.
type AssistantToolRuntimeMCPServer interface {
	CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*AssistantToolRuntimeToolResult, error)
	InvokeAssistantAsyncTool(ctx context.Context, name string, args map[string]interface{}) (*domain.AsyncToolReceipt, error)
}

// AssistantToolRuntimeRegistry is the descriptor lookup surface supplied by the
// MCP assistant registry from item 2.
type AssistantToolRuntimeRegistry interface {
	GetAgentTool(name string) (AssistantToolRuntimeToolDescriptor, bool)
}

// AssistantToolPermissionEvaluator is implemented by AssistantPermissionEngine.
type AssistantToolPermissionEvaluator interface {
	Evaluate(req AssistantPermissionRequest) domain.AssistantPermissionResult
}

// AssistantToolRuntimeSessionPersister persists session metadata/state changes
// and emits status/audit events. AssistantOrchestrator implements this by
// publishing the existing kind 30900 and kind 30315 projections.
type AssistantToolRuntimeSessionPersister interface {
	PersistAssistantSession(ctx context.Context, session *domain.AssistantSession) error
	PublishAssistantStatus(ctx context.Context, sessionID, status string, content map[string]any) error
}

// AssistantAsyncObservationOutcome is the normalized terminal observation from
// the existing event-native downstream result observer.
type AssistantAsyncObservationOutcome struct {
	Status string
	Event  *nostr.Event
}

// AssistantAsyncResultObserver resumes an async tool call by observing the
// receipt's result kinds scoped by e=<request_event_id>. AssistantOrchestrator
// implements this by delegating to observeDownstreamResult.
type AssistantAsyncResultObserver interface {
	ObserveAssistantAsyncResult(ctx context.Context, sessionID, toolCallID, toolName string, receipt *domain.AsyncToolReceipt) (AssistantAsyncObservationOutcome, error)
}

// AssistantToolRuntimeConfig wires the item-5 bridge. The runtime intentionally
// does not own the agent loop; callers execute one tool call, inspect the
// observation, and either continue, suspend, or resume through ResumeAsync.
type AssistantToolRuntimeConfig struct {
	MCPServer   AssistantToolRuntimeMCPServer
	Registry    AssistantToolRuntimeRegistry
	Permissions AssistantToolPermissionEvaluator
	Hooks       *AssistantHookRunner
	Sessions    AssistantToolRuntimeSessionPersister
	Observer    AssistantAsyncResultObserver
	Now         func() time.Time
	NewID       func(prefix string) string
}

// AssistantToolRuntime normalizes sync MCP results, async Nostr receipts,
// deferred approvals, denials, and resumed terminal events into a single
// AssistantToolObservation contract for the future agent loop.
type AssistantToolRuntime struct {
	mcpServer   AssistantToolRuntimeMCPServer
	registry    AssistantToolRuntimeRegistry
	permissions AssistantToolPermissionEvaluator
	hooks       *AssistantHookRunner
	sessions    AssistantToolRuntimeSessionPersister
	observer    AssistantAsyncResultObserver
	now         func() time.Time
	newID       func(prefix string) string
}

// AssistantToolRuntimeRequest is one model-requested tool invocation.
type AssistantToolRuntimeRequest struct {
	Session        *domain.AssistantSession
	RunID          string
	TurnID         string
	Iteration      int
	ToolCall       domain.AssistantAgentToolCall
	PlanHash       string
	CancelScope    string
	ApprovedAction *domain.AssistantDeferredAction
	// PermissionOverride, when set, short-circuits the runtime's own permission
	// evaluation. The agent loop uses it to enforce the hard-deny -> hooks ->
	// re-evaluate-on-modified-input ordering before execution. It is only
	// consulted for fresh (non-approved) tool calls.
	PermissionOverride *domain.AssistantPermissionResult
}

// AssistantToolResumeRequest resumes a previously suspended async tool call.
type AssistantToolResumeRequest struct {
	Session  *domain.AssistantSession
	RunID    string
	Receipt  *domain.AsyncToolReceipt
	ToolCall *domain.AssistantAgentToolCall
}

// NewAssistantToolRuntime constructs the async bridge used by item 7 and by
// startup recovery.
func NewAssistantToolRuntime(config AssistantToolRuntimeConfig) *AssistantToolRuntime {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	newID := config.NewID
	if newID == nil {
		newID = randomAssistantRuntimeID
	}
	return &AssistantToolRuntime{
		mcpServer:   config.MCPServer,
		registry:    config.Registry,
		permissions: config.Permissions,
		hooks:       config.Hooks,
		sessions:    config.Sessions,
		observer:    config.Observer,
		now:         now,
		newID:       newID,
	}
}

// Execute evaluates permission and either calls a sync tool inline, dispatches
// an async mutation and persists waiting_async, creates a deferred action, or
// returns a deny observation. Async execution never waits for terminal results.
func (r *AssistantToolRuntime) Execute(ctx context.Context, req AssistantToolRuntimeRequest) (*domain.AssistantToolObservation, error) {
	if r == nil {
		return nil, fmt.Errorf("assistant tool runtime is not configured")
	}
	if req.Session == nil {
		return nil, fmt.Errorf("assistant session is required")
	}
	call := normalizeAssistantToolCall(req.ToolCall)
	if call.ID == "" || call.Name == "" {
		return nil, fmt.Errorf("assistant tool call requires id and name")
	}
	descriptor, ok := r.lookupDescriptor(call.Name)
	if !ok {
		return r.deniedObservation(req, call, domain.AssistantPermissionResult{Decision: domain.AssistantPermissionDecisionDeny, Reason: "assistant tool is not registered for agent use"}), nil
	}
	if req.ApprovedAction != nil {
		permission, err := r.permissionFromApprovedAction(req, call, descriptor)
		if err != nil {
			return r.deniedObservation(req, call, domain.AssistantPermissionResult{Decision: domain.AssistantPermissionDecisionDeny, Effect: descriptor.Effect, Risk: descriptor.DefaultRisk, ExecutionMode: descriptor.ExecutionMode, Reason: err.Error()}), nil
		}
		if descriptor.ExecutionMode == domain.AssistantToolExecutionModeAsync {
			return r.executeAsync(ctx, req, call, descriptor, permission)
		}
		if descriptor.ExecutionMode != domain.AssistantToolExecutionModeSync {
			return r.failedObservation(req, call, descriptor, permission, "assistant tool execution mode is unsupported", nil), nil
		}
		return r.executeSync(ctx, req, call, descriptor, permission)
	}
	permission := r.resolvePermission(req, descriptor, call.Arguments)
	switch permission.Decision {
	case domain.AssistantPermissionDecisionDeny:
		return r.deniedObservation(req, call, permission), nil
	case domain.AssistantPermissionDecisionAsk:
		return r.deferObservation(ctx, req, call, descriptor, permission)
	case domain.AssistantPermissionDecisionAllow:
		// continue below
	default:
		permission.Decision = domain.AssistantPermissionDecisionDeny
		permission.Reason = firstNonEmptyString(permission.Reason, "assistant permission evaluator returned an unsupported decision")
		return r.deniedObservation(req, call, permission), nil
	}

	if descriptor.ExecutionMode == domain.AssistantToolExecutionModeAsync {
		return r.executeAsync(ctx, req, call, descriptor, permission)
	}
	if descriptor.ExecutionMode != domain.AssistantToolExecutionModeSync {
		return r.failedObservation(req, call, descriptor, permission, "assistant tool execution mode is unsupported", nil), nil
	}
	return r.executeSync(ctx, req, call, descriptor, permission)
}

// ResumeAsync observes the terminal Nostr result for a waiting async receipt and
// converts it back into one tool observation. Relay closure, missing receipt
// metadata, and caller cancellation fail closed by moving the loop/session to
// blocked rather than dropping the pending work.
func (r *AssistantToolRuntime) ResumeAsync(ctx context.Context, req AssistantToolResumeRequest) (*domain.AssistantToolObservation, error) {
	if r == nil {
		return nil, fmt.Errorf("assistant tool runtime is not configured")
	}
	if req.Session == nil {
		return nil, fmt.Errorf("assistant session is required")
	}
	metadata := assistantAgentLoopMetadata(req.Session)
	if req.RunID != "" && metadata.RunID == "" {
		metadata.RunID = req.RunID
	}
	receipt := req.Receipt
	if receipt == nil && metadata.WaitingReceipt != nil {
		copyReceipt := *metadata.WaitingReceipt
		receipt = &copyReceipt
	}
	if receipt == nil || receipt.RequestEventID == "" || len(receipt.ResultKinds) == 0 {
		obs := r.blockedObservationFromMetadata(req.Session, metadata, "downstream receipt is missing observable result metadata")
		_ = r.persistBlocked(ctx, req.Session, metadata, obs)
		return obs, nil
	}
	toolName := strings.TrimSpace(receipt.ToolName)
	toolCallID := strings.TrimSpace(metadata.PendingToolCallID)
	if req.ToolCall != nil {
		if req.ToolCall.Name != "" {
			toolName = req.ToolCall.Name
		}
		if req.ToolCall.ID != "" {
			toolCallID = req.ToolCall.ID
		}
	}
	if toolName == "" {
		toolName = "unknown_async_tool"
	}

	if r.observer == nil {
		obs := r.blockedObservation(req.Session, metadata, toolCallID, toolName, receipt, "assistant async result observer is not configured")
		_ = r.persistBlocked(ctx, req.Session, metadata, obs)
		return obs, nil
	}
	outcome, err := r.observer.ObserveAssistantAsyncResult(ctx, req.Session.SessionID, toolCallID, toolName, receipt)
	if err != nil || outcome.Status == "blocked" {
		reason := "downstream observation blocked before terminal result"
		if err != nil {
			reason = err.Error()
		}
		obs := r.blockedObservation(req.Session, metadata, toolCallID, toolName, receipt, reason)
		_ = r.persistBlocked(ctx, req.Session, metadata, obs)
		return obs, nil
	}

	status := domain.AssistantToolObservationSucceeded
	summary := "async tool completed from downstream result"
	if outcome.Status == "failed" {
		status = domain.AssistantToolObservationFailed
		summary = "async tool failed from downstream result"
	}
	result, content := assistantObservationFromEvent(outcome.Event)
	obs := &domain.AssistantToolObservation{
		ObservationID: r.newID("obs"),
		ToolCallID:    toolCallID,
		ToolName:      toolName,
		Status:        status,
		Effect:        domain.AssistantToolEffectMutation,
		ExecutionMode: domain.AssistantToolExecutionModeAsync,
		Summary:       summary,
		Content:       content,
		Result:        result,
		Receipt:       cloneAsyncToolReceipt(receipt),
		ObservedAt:    r.now().UTC(),
		Metadata:      map[string]any{"downstream_status": outcome.Status},
	}
	if outcome.Event != nil {
		obs.EventID = outcome.Event.ID.Hex()
	}
	metadata.State = domain.AssistantAgentLoopStateRunning
	metadata.WaitingReceipt = nil
	metadata.PendingToolCallID = ""
	metadata.LastObservationID = obs.ObservationID
	metadata.UpdatedAt = r.now().UTC()
	setAssistantAgentLoopMetadata(req.Session, metadata)
	if req.Session.State == domain.AssistantSessionStateBlocked {
		req.Session.State = domain.AssistantSessionStateExecuting
	}
	if err := r.persistSession(ctx, req.Session); err != nil {
		return obs, err
	}
	_ = r.publishStatus(ctx, req.Session.SessionID, string(req.Session.State), map[string]any{
		"phase":              "tool_observed",
		"summary":            obs.Summary,
		"tool_call_id":       obs.ToolCallID,
		"tool_name":          obs.ToolName,
		"observation_id":     obs.ObservationID,
		"downstream_request": receipt.RequestEventID,
		"downstream_result":  obs.EventID,
		"observation_status": string(obs.Status),
	})
	return obs, nil
}

func (r *AssistantToolRuntime) executeSync(ctx context.Context, req AssistantToolRuntimeRequest, call domain.AssistantAgentToolCall, descriptor AssistantToolRuntimeToolDescriptor, permission domain.AssistantPermissionResult) (*domain.AssistantToolObservation, error) {
	if r.mcpServer == nil {
		return r.failedObservation(req, call, descriptor, permission, "assistant MCP server is not configured", nil), nil
	}
	result, err := r.mcpServer.CallTool(ctx, call.Name, cloneInterfaceArgs(call.Arguments))
	if err != nil {
		return r.failedObservation(req, call, descriptor, permission, err.Error(), nil), nil
	}
	obs := r.observationFromMCPResult(req, call, descriptor, permission, result)
	metadata := assistantAgentLoopMetadata(req.Session)
	metadata.RunID = firstNonEmptyString(req.RunID, metadata.RunID)
	metadata.Iteration = req.Iteration
	metadata.State = domain.AssistantAgentLoopStateRunning
	metadata.PendingActionID = ""
	metadata.PendingToolCallID = ""
	metadata.WaitingReceipt = nil
	metadata.LastObservationID = obs.ObservationID
	metadata.UpdatedAt = r.now().UTC()
	setAssistantAgentLoopMetadata(req.Session, metadata)
	if err := r.persistSession(ctx, req.Session); err != nil {
		return obs, err
	}
	_ = r.publishStatus(ctx, req.Session.SessionID, string(req.Session.State), map[string]any{"phase": "tool_observed", "tool_call_id": call.ID, "tool_name": call.Name, "observation_id": obs.ObservationID, "observation_status": string(obs.Status), "summary": obs.Summary})
	return obs, nil
}

func (r *AssistantToolRuntime) executeAsync(ctx context.Context, req AssistantToolRuntimeRequest, call domain.AssistantAgentToolCall, descriptor AssistantToolRuntimeToolDescriptor, permission domain.AssistantPermissionResult) (*domain.AssistantToolObservation, error) {
	metadata := assistantAgentLoopMetadata(req.Session)
	if metadata.State == domain.AssistantAgentLoopStateWaitingAsync && metadata.WaitingReceipt != nil && metadata.PendingToolCallID == call.ID {
		return r.waitingObservation(req, call, descriptor, permission, metadata.WaitingReceipt, "async tool already submitted; waiting for downstream result"), nil
	}
	if r.mcpServer == nil {
		return r.failedObservation(req, call, descriptor, permission, "assistant MCP server is not configured", nil), nil
	}
	args := cloneInterfaceArgs(call.Arguments)
	if strings.TrimSpace(stringFromAnyMap(args, "idempotency_key")) == "" {
		args["idempotency_key"] = assistantToolIdempotencyKey(req.Session.SessionID, firstNonEmptyString(req.RunID, metadata.RunID), call.ID)
	}
	receipt, err := r.mcpServer.InvokeAssistantAsyncTool(ctx, call.Name, args)
	if err != nil {
		return r.failedObservation(req, call, descriptor, permission, err.Error(), nil), nil
	}
	if receipt == nil || receipt.RequestEventID == "" || len(receipt.ResultKinds) == 0 {
		return r.failedObservation(req, call, descriptor, permission, "async tool receipt is missing observable result metadata", receipt), nil
	}
	metadata.RunID = firstNonEmptyString(req.RunID, metadata.RunID)
	metadata.Iteration = req.Iteration
	metadata.State = domain.AssistantAgentLoopStateWaitingAsync
	metadata.PendingActionID = ""
	metadata.PendingToolCallID = call.ID
	metadata.WaitingReceipt = cloneAsyncToolReceipt(receipt)
	metadata.UpdatedAt = r.now().UTC()
	setAssistantAgentLoopMetadata(req.Session, metadata)
	req.Session.State = domain.AssistantSessionStateExecuting
	obs := r.waitingObservation(req, call, descriptor, permission, receipt, "async tool submitted; waiting for downstream result")
	metadata.LastObservationID = obs.ObservationID
	setAssistantAgentLoopMetadata(req.Session, metadata)
	if err := r.persistSession(ctx, req.Session); err != nil {
		return obs, err
	}
	_ = r.publishStatus(ctx, req.Session.SessionID, "executing", map[string]any{
		"phase":              "tool_submitted",
		"summary":            obs.Summary,
		"tool_call_id":       call.ID,
		"tool_name":          call.Name,
		"observation_id":     obs.ObservationID,
		"downstream_request": receipt.RequestEventID,
		"receipt":            receipt,
	})
	return obs, nil
}

func (r *AssistantToolRuntime) deferObservation(ctx context.Context, req AssistantToolRuntimeRequest, call domain.AssistantAgentToolCall, descriptor AssistantToolRuntimeToolDescriptor, permission domain.AssistantPermissionResult) (*domain.AssistantToolObservation, error) {
	actionID := r.newID("action")
	deferred := &domain.AssistantDeferredAction{
		ActionID:       actionID,
		SessionID:      req.Session.SessionID,
		RunID:          req.RunID,
		TurnID:         req.TurnID,
		ToolCallID:     call.ID,
		ToolName:       call.Name,
		ToolArgs:       cloneAnyArgs(call.Arguments),
		PlanHash:       req.PlanHash,
		CancelScope:    req.CancelScope,
		Permission:     permission,
		ApprovalPrompt: firstNonEmptyString(permission.Reason, "assistant tool requires operator approval"),
		CreatedAt:      r.now().UTC(),
		Metadata:       map[string]any{"effect": string(descriptor.Effect), "execution_mode": string(descriptor.ExecutionMode), "risk": string(permission.Risk)},
	}
	obs := &domain.AssistantToolObservation{
		ObservationID: r.newID("obs"),
		ToolCallID:    call.ID,
		ToolName:      call.Name,
		Status:        domain.AssistantToolObservationDeferred,
		Effect:        descriptor.Effect,
		Risk:          permission.Risk,
		ExecutionMode: descriptor.ExecutionMode,
		Summary:       "assistant tool requires operator approval",
		Deferred:      deferred,
		ObservedAt:    r.now().UTC(),
		Metadata:      map[string]any{"permission": permission},
	}
	metadata := assistantAgentLoopMetadata(req.Session)
	metadata.RunID = firstNonEmptyString(req.RunID, metadata.RunID)
	metadata.Iteration = req.Iteration
	metadata.State = domain.AssistantAgentLoopStateAwaitingApproval
	metadata.PendingActionID = actionID
	metadata.PendingToolCallID = call.ID
	metadata.LastObservationID = obs.ObservationID
	metadata.UpdatedAt = r.now().UTC()
	setAssistantAgentLoopMetadata(req.Session, metadata)
	storeAssistantDeferredAction(req.Session, deferred)
	req.Session.State = domain.AssistantSessionStateAwaitingApproval
	if err := r.persistSession(ctx, req.Session); err != nil {
		return obs, err
	}
	_ = r.publishStatus(ctx, req.Session.SessionID, "awaiting_approval", map[string]any{"phase": "approval_required", "summary": obs.Summary, "tool_call_id": call.ID, "tool_name": call.Name, "action_id": actionID, "permission": permission})
	return obs, nil
}

func (r *AssistantToolRuntime) deniedObservation(req AssistantToolRuntimeRequest, call domain.AssistantAgentToolCall, permission domain.AssistantPermissionResult) *domain.AssistantToolObservation {
	obs := &domain.AssistantToolObservation{
		ObservationID: r.newID("obs"),
		ToolCallID:    call.ID,
		ToolName:      call.Name,
		Status:        domain.AssistantToolObservationDenied,
		Effect:        permission.Effect,
		Risk:          permission.Risk,
		ExecutionMode: permission.ExecutionMode,
		Summary:       "assistant tool denied by policy",
		Error:         firstNonEmptyString(permission.Reason, "assistant tool denied by policy"),
		ObservedAt:    r.now().UTC(),
		Metadata:      map[string]any{"permission": permission},
	}
	if req.Session != nil {
		metadata := assistantAgentLoopMetadata(req.Session)
		metadata.RunID = firstNonEmptyString(req.RunID, metadata.RunID)
		metadata.Iteration = req.Iteration
		metadata.State = domain.AssistantAgentLoopStateRunning
		metadata.LastObservationID = obs.ObservationID
		metadata.UpdatedAt = r.now().UTC()
		setAssistantAgentLoopMetadata(req.Session, metadata)
	}
	return obs
}

func (r *AssistantToolRuntime) failedObservation(req AssistantToolRuntimeRequest, call domain.AssistantAgentToolCall, descriptor AssistantToolRuntimeToolDescriptor, permission domain.AssistantPermissionResult, message string, receipt *domain.AsyncToolReceipt) *domain.AssistantToolObservation {
	return &domain.AssistantToolObservation{ObservationID: r.newID("obs"), ToolCallID: call.ID, ToolName: call.Name, Status: domain.AssistantToolObservationFailed, Effect: descriptor.Effect, Risk: permission.Risk, ExecutionMode: descriptor.ExecutionMode, Summary: "assistant tool failed", Error: message, Receipt: cloneAsyncToolReceipt(receipt), ObservedAt: r.now().UTC(), Metadata: map[string]any{"permission": permission}}
}

func (r *AssistantToolRuntime) waitingObservation(req AssistantToolRuntimeRequest, call domain.AssistantAgentToolCall, descriptor AssistantToolRuntimeToolDescriptor, permission domain.AssistantPermissionResult, receipt *domain.AsyncToolReceipt, summary string) *domain.AssistantToolObservation {
	return &domain.AssistantToolObservation{ObservationID: r.newID("obs"), ToolCallID: call.ID, ToolName: call.Name, Status: domain.AssistantToolObservationWaitingAsync, Effect: descriptor.Effect, Risk: permission.Risk, ExecutionMode: descriptor.ExecutionMode, Summary: summary, Receipt: cloneAsyncToolReceipt(receipt), ObservedAt: r.now().UTC(), Metadata: map[string]any{"permission": permission}}
}

func (r *AssistantToolRuntime) blockedObservationFromMetadata(session *domain.AssistantSession, metadata domain.AssistantAgentLoopMetadata, message string) *domain.AssistantToolObservation {
	toolName := "unknown_async_tool"
	if metadata.WaitingReceipt != nil && metadata.WaitingReceipt.ToolName != "" {
		toolName = metadata.WaitingReceipt.ToolName
	}
	return r.blockedObservation(session, metadata, metadata.PendingToolCallID, toolName, metadata.WaitingReceipt, message)
}

func (r *AssistantToolRuntime) blockedObservation(session *domain.AssistantSession, metadata domain.AssistantAgentLoopMetadata, toolCallID, toolName string, receipt *domain.AsyncToolReceipt, message string) *domain.AssistantToolObservation {
	return &domain.AssistantToolObservation{ObservationID: r.newID("obs"), ToolCallID: toolCallID, ToolName: toolName, Status: domain.AssistantToolObservationFailed, Effect: domain.AssistantToolEffectMutation, ExecutionMode: domain.AssistantToolExecutionModeAsync, Summary: "assistant async tool observation blocked", Error: message, Receipt: cloneAsyncToolReceipt(receipt), ObservedAt: r.now().UTC(), Metadata: map[string]any{"blocked": true, "loop_state": string(domain.AssistantAgentLoopStateBlocked)}}
}

func (r *AssistantToolRuntime) observationFromMCPResult(req AssistantToolRuntimeRequest, call domain.AssistantAgentToolCall, descriptor AssistantToolRuntimeToolDescriptor, permission domain.AssistantPermissionResult, result *AssistantToolRuntimeToolResult) *domain.AssistantToolObservation {
	obsStatus := domain.AssistantToolObservationSucceeded
	summary := "sync tool completed"
	errText := ""
	if result == nil {
		obsStatus = domain.AssistantToolObservationFailed
		summary = "sync tool returned no result"
		errText = summary
	}
	if result != nil && result.IsError {
		obsStatus = domain.AssistantToolObservationFailed
		summary = "sync tool returned an error"
		errText = joinedMCPText(result.Content)
	}
	content := assistantContentFromMCPResult(result)
	parsed := assistantJSONMapFromMCPResult(result)
	return &domain.AssistantToolObservation{ObservationID: r.newID("obs"), ToolCallID: call.ID, ToolName: call.Name, Status: obsStatus, Effect: descriptor.Effect, Risk: permission.Risk, ExecutionMode: descriptor.ExecutionMode, Summary: summary, Content: content, Result: parsed, Error: errText, ObservedAt: r.now().UTC(), Metadata: map[string]any{"permission": permission}}
}

func (r *AssistantToolRuntime) lookupDescriptor(name string) (AssistantToolRuntimeToolDescriptor, bool) {
	if r.registry == nil {
		return AssistantToolRuntimeToolDescriptor{}, false
	}
	return r.registry.GetAgentTool(name)
}

// AgentToolDescriptor exposes the descriptor registry lookup so the agent loop
// can reason about execution mode (e.g. to keep subagents synchronous) without
// importing internal/mcp.
func (r *AssistantToolRuntime) AgentToolDescriptor(name string) (AssistantToolRuntimeToolDescriptor, bool) {
	if r == nil {
		return AssistantToolRuntimeToolDescriptor{}, false
	}
	return r.lookupDescriptor(name)
}

// EvaluateToolPermission evaluates the permission engine for a candidate tool
// call without executing it. The agent loop uses it to apply hooks in the
// hard-deny -> hooks -> re-evaluate ordering. found is false when the tool is
// not registered for agent use.
func (r *AssistantToolRuntime) EvaluateToolPermission(name string, args map[string]any) (domain.AssistantPermissionResult, bool) {
	if r == nil {
		return domain.AssistantPermissionResult{Decision: domain.AssistantPermissionDecisionDeny, Reason: "assistant tool runtime is not configured"}, false
	}
	descriptor, ok := r.lookupDescriptor(name)
	if !ok {
		return domain.AssistantPermissionResult{Decision: domain.AssistantPermissionDecisionDeny, Reason: "assistant tool is not registered for agent use"}, false
	}
	return r.evaluatePermission(descriptor, args), true
}

// WithoutSessionEffects returns a shallow copy of the runtime with session
// persistence and async observation disabled. Subagent child loops use it so
// their tool calls never publish phantom session/status events or corrupt the
// parent loop's persisted metadata.
func (r *AssistantToolRuntime) WithoutSessionEffects() *AssistantToolRuntime {
	if r == nil {
		return nil
	}
	clone := *r
	clone.sessions = nil
	clone.observer = nil
	return &clone
}

func (r *AssistantToolRuntime) resolvePermission(req AssistantToolRuntimeRequest, descriptor AssistantToolRuntimeToolDescriptor, args map[string]any) domain.AssistantPermissionResult {
	if req.PermissionOverride != nil {
		permission := *req.PermissionOverride
		if permission.Effect == "" {
			permission.Effect = descriptor.Effect
		}
		if permission.Risk == "" {
			permission.Risk = descriptor.DefaultRisk
		}
		if permission.ExecutionMode == "" {
			permission.ExecutionMode = descriptor.ExecutionMode
		}
		return permission
	}
	return r.evaluatePermission(descriptor, args)
}

func (r *AssistantToolRuntime) evaluatePermission(descriptor AssistantToolRuntimeToolDescriptor, args map[string]any) domain.AssistantPermissionResult {
	metadata := AssistantToolPermissionMetadata{Name: descriptor.Name, Effect: descriptor.Effect, DefaultRisk: descriptor.DefaultRisk, ExecutionMode: descriptor.ExecutionMode, ResourceTypes: append([]string(nil), descriptor.ResourceTypes...)}
	if r.permissions == nil {
		return domain.AssistantPermissionResult{Decision: domain.AssistantPermissionDecisionDeny, Effect: descriptor.Effect, Risk: descriptor.DefaultRisk, ExecutionMode: descriptor.ExecutionMode, Reason: "assistant permission engine is not configured"}
	}
	return r.permissions.Evaluate(AssistantPermissionRequest{Tool: metadata, Args: args})
}

func (r *AssistantToolRuntime) permissionFromApprovedAction(req AssistantToolRuntimeRequest, call domain.AssistantAgentToolCall, descriptor AssistantToolRuntimeToolDescriptor) (domain.AssistantPermissionResult, error) {
	action := req.ApprovedAction
	if action == nil {
		return domain.AssistantPermissionResult{}, fmt.Errorf("approved deferred action is required")
	}
	if action.SessionID != "" && req.Session != nil && action.SessionID != req.Session.SessionID {
		return domain.AssistantPermissionResult{}, fmt.Errorf("approved action session does not match assistant session")
	}
	if action.RunID != "" && req.RunID != "" && action.RunID != req.RunID {
		return domain.AssistantPermissionResult{}, fmt.Errorf("approved action run does not match active assistant run")
	}
	if action.TurnID != "" && req.TurnID != "" && action.TurnID != req.TurnID {
		return domain.AssistantPermissionResult{}, fmt.Errorf("approved action turn does not match active assistant turn")
	}
	if action.ToolCallID != "" && action.ToolCallID != call.ID {
		return domain.AssistantPermissionResult{}, fmt.Errorf("approved action tool call does not match model tool call")
	}
	if action.ToolName != "" && action.ToolName != call.Name {
		return domain.AssistantPermissionResult{}, fmt.Errorf("approved action tool name does not match model tool call")
	}
	permission := action.Permission
	permission.Decision = domain.AssistantPermissionDecisionAllow
	if permission.Effect == "" {
		permission.Effect = descriptor.Effect
	}
	if permission.Risk == "" {
		permission.Risk = descriptor.DefaultRisk
	}
	if permission.ExecutionMode == "" {
		permission.ExecutionMode = descriptor.ExecutionMode
	}
	permission.Reason = firstNonEmptyString(permission.Reason, "operator approved deferred assistant action")
	if permission.Metadata == nil {
		permission.Metadata = map[string]any{}
	}
	permission.Metadata["approved_action_id"] = action.ActionID
	permission.Metadata["approval_decision"] = "approve"
	return permission, nil
}

func (r *AssistantToolRuntime) persistBlocked(ctx context.Context, session *domain.AssistantSession, metadata domain.AssistantAgentLoopMetadata, obs *domain.AssistantToolObservation) error {
	metadata.State = domain.AssistantAgentLoopStateBlocked
	metadata.LastObservationID = obs.ObservationID
	metadata.UpdatedAt = r.now().UTC()
	setAssistantAgentLoopMetadata(session, metadata)
	session.State = domain.AssistantSessionStateBlocked
	if err := r.persistSession(ctx, session); err != nil {
		return err
	}
	return r.publishStatus(ctx, session.SessionID, "blocked", map[string]any{"phase": "tool_observation_blocked", "summary": obs.Summary, "error": obs.Error, "tool_call_id": obs.ToolCallID, "tool_name": obs.ToolName, "observation_id": obs.ObservationID})
}

func (r *AssistantToolRuntime) persistSession(ctx context.Context, session *domain.AssistantSession) error {
	if r.sessions == nil {
		return nil
	}
	return r.sessions.PersistAssistantSession(ctx, session)
}

func (r *AssistantToolRuntime) publishStatus(ctx context.Context, sessionID, status string, content map[string]any) error {
	if r.sessions == nil {
		return nil
	}
	return r.sessions.PublishAssistantStatus(ctx, sessionID, status, content)
}

// PersistAssistantSession lets AssistantOrchestrator act as the production
// session/status persister for the runtime without changing the public event
// contracts.
func (o *AssistantOrchestrator) PersistAssistantSession(ctx context.Context, session *domain.AssistantSession) error {
	return o.publishSession(ctx, session)
}

// PublishAssistantStatus lets AssistantOrchestrator publish runtime phase events.
func (o *AssistantOrchestrator) PublishAssistantStatus(ctx context.Context, sessionID, status string, content map[string]any) error {
	return o.publishStatus(ctx, nil, sessionID, status, content)
}

// ObserveAssistantAsyncResult adapts the legacy downstream observer to the
// agentic runtime resume API.
func (o *AssistantOrchestrator) ObserveAssistantAsyncResult(ctx context.Context, sessionID, toolCallID, toolName string, receipt *domain.AsyncToolReceipt) (AssistantAsyncObservationOutcome, error) {
	outcome, err := o.observeDownstreamResult(ctx, sessionID, domain.AssistantPlanStep{StepID: toolCallID, ToolName: toolName}, receipt)
	return AssistantAsyncObservationOutcome(outcome), err
}

func assistantAgentLoopMetadata(session *domain.AssistantSession) domain.AssistantAgentLoopMetadata {
	if session == nil || session.Metadata == nil {
		return domain.AssistantAgentLoopMetadata{}
	}
	raw, ok := session.Metadata[assistantAgentLoopMetadataKey]
	if !ok || raw == nil {
		return domain.AssistantAgentLoopMetadata{}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return domain.AssistantAgentLoopMetadata{}
	}
	var metadata domain.AssistantAgentLoopMetadata
	if err := json.Unmarshal(b, &metadata); err != nil {
		return domain.AssistantAgentLoopMetadata{}
	}
	return metadata
}

func setAssistantAgentLoopMetadata(session *domain.AssistantSession, metadata domain.AssistantAgentLoopMetadata) {
	if session == nil {
		return
	}
	if session.Metadata == nil {
		session.Metadata = map[string]any{}
	}
	session.Metadata[assistantAgentLoopMetadataKey] = metadata
}

func storeAssistantDeferredAction(session *domain.AssistantSession, action *domain.AssistantDeferredAction) {
	if session == nil || action == nil || action.ActionID == "" {
		return
	}
	if session.Metadata == nil {
		session.Metadata = map[string]any{}
	}
	actions, _ := session.Metadata[assistantDeferredActionsMetadataKey].(map[string]any)
	if actions == nil {
		actions = map[string]any{}
		session.Metadata[assistantDeferredActionsMetadataKey] = actions
	}
	actions[action.ActionID] = action
}

func normalizeAssistantToolCall(call domain.AssistantAgentToolCall) domain.AssistantAgentToolCall {
	call.ID = strings.TrimSpace(call.ID)
	call.Name = strings.TrimSpace(call.Name)
	if call.Arguments == nil {
		call.Arguments = map[string]any{}
	}
	return call
}

func cloneInterfaceArgs(args map[string]any) map[string]interface{} {
	out := make(map[string]interface{}, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

func cloneAnyArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

func cloneAsyncToolReceipt(receipt *domain.AsyncToolReceipt) *domain.AsyncToolReceipt {
	if receipt == nil {
		return nil
	}
	out := *receipt
	out.StatusKinds = append([]int(nil), receipt.StatusKinds...)
	out.ResultKinds = append([]int(nil), receipt.ResultKinds...)
	out.ReadModelKinds = append([]int(nil), receipt.ReadModelKinds...)
	out.PublishedRelays = append([]string(nil), receipt.PublishedRelays...)
	if receipt.ResourceTags != nil {
		out.ResourceTags = make(map[string]string, len(receipt.ResourceTags))
		for k, v := range receipt.ResourceTags {
			out.ResourceTags[k] = v
		}
	}
	return &out
}

func assistantContentFromMCPResult(result *AssistantToolRuntimeToolResult) []domain.AssistantAgentContentBlock {
	if result == nil || len(result.Content) == 0 {
		return nil
	}
	blocks := make([]domain.AssistantAgentContentBlock, 0, len(result.Content))
	for _, item := range result.Content {
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		blocks = append(blocks, domain.AssistantAgentContentBlock{Type: domain.AssistantAgentContentText, Text: item.Text, Metadata: map[string]any{"mcp_content_type": item.Type}})
	}
	return blocks
}

func assistantJSONMapFromMCPResult(result *AssistantToolRuntimeToolResult) map[string]any {
	if result == nil || len(result.Content) != 1 || strings.TrimSpace(result.Content[0].Text) == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsed); err != nil {
		return nil
	}
	return parsed
}

func joinedMCPText(content []AssistantToolRuntimeToolContent) string {
	parts := make([]string, 0, len(content))
	for _, item := range content {
		if strings.TrimSpace(item.Text) != "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func assistantObservationFromEvent(ev *nostr.Event) (map[string]any, []domain.AssistantAgentContentBlock) {
	if ev == nil || strings.TrimSpace(ev.Content) == "" {
		return nil, nil
	}
	content := []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: ev.Content, Metadata: map[string]any{"nostr_kind": int(ev.Kind)}}}
	var result map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &result); err == nil {
		content[0].Type = domain.AssistantAgentContentJSON
		content[0].JSON = result
		content[0].Text = ""
	}
	return result, content
}

func assistantToolIdempotencyKey(sessionID, runID, toolCallID string) string {
	parts := []string{"assistant-agent", strings.TrimSpace(sessionID), strings.TrimSpace(runID), strings.TrimSpace(toolCallID)}
	return strings.Join(parts, ":")
}

func stringFromAnyMap(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	if s, ok := values[key].(string); ok {
		return s
	}
	return ""
}

func randomAssistantRuntimeID(prefix string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s_%d", strings.TrimSpace(prefix), time.Now().UnixNano())
	}
	return strings.TrimSpace(prefix) + "_" + hex.EncodeToString(buf[:])
}

// ErrAssistantApprovedInputChanged means preparation (for example a
// PreToolUse hook) produced arguments that differ from the approved digest. The
// approval never authorizes the changed content.
var ErrAssistantApprovedInputChanged = errors.New("approved_input_changed")

// ErrAssistantApprovalRequired means the work needs an exact operator approval
// binding before dispatch. The returned AssistantPreparedWork still carries
// the effective (hook-transformed) arguments the operator must approve.
var ErrAssistantApprovalRequired = errors.New("approval_required")

// AssistantWorkDenial is a policy, scope, schema or registration refusal. It is
// an authoritative answer about this work, unlike infrastructure errors.
type AssistantWorkDenial struct{ Reason string }

func (d *AssistantWorkDenial) Error() string { return "assistant work denied: " + d.Reason }

func denyAssistantWork(format string, args ...any) error {
	return &AssistantWorkDenial{Reason: fmt.Sprintf(format, args...)}
}

// AssistantPreparedWork is a checked immutable invocation. The executor must
// checkpoint dispatching before calling DispatchPreparedWork.
type AssistantPreparedWork struct {
	Execution  domain.AssistantExecution
	Work       domain.AssistantWorkItem
	Descriptor AssistantToolRuntimeToolDescriptor
	Permission domain.AssistantPermissionResult
}

// PrepareWork applies the same ordered authorization to batch and iterative
// work: registration and schema, persisted command scope, current permission
// policy, PreToolUse hooks, re-evaluation of effective input, then approval
// binding. Hooks may only tighten the decision. It never calls a tool or
// mutates execution state.
func (r *AssistantToolRuntime) PrepareWork(ctx context.Context, execution domain.AssistantExecution, work domain.AssistantWorkItem) (AssistantPreparedWork, error) {
	if r == nil || r.registry == nil || r.permissions == nil {
		return AssistantPreparedWork{}, fmt.Errorf("assistant runtime authorization dependencies missing")
	}
	if work.ToolName == "" || work.WorkID == "" || work.Arguments == nil {
		return AssistantPreparedWork{}, denyAssistantWork("work identity or arguments missing")
	}
	args, err := domain.DeepCopyAssistantJSONMap(work.Arguments)
	if err != nil {
		return AssistantPreparedWork{}, denyAssistantWork("arguments are not valid JSON: %v", err)
	}
	// Caller-supplied dispatch keys are not executable input.
	delete(args, "idempotency_key")
	descriptor, ok := r.lookupDescriptor(work.ToolName)
	if !ok {
		return AssistantPreparedWork{}, denyAssistantWork("tool %q is not registered", work.ToolName)
	}
	if work.IdempotencyKey == "" {
		work.IdempotencyKey = assistantExecutionIdempotencyKey(execution, work)
	}
	if err := validateAssistantWorkSchema(descriptor.InputSchema, assistantSchemaArgs(descriptor, args, work.IdempotencyKey)); err != nil {
		return AssistantPreparedWork{}, denyAssistantWork("input schema: %v", err)
	}
	if !assistantScopeAllows(execution.Scope, work.ToolName) {
		return AssistantPreparedWork{}, denyAssistantWork("tool %q is outside the command's allowed-tools scope", work.ToolName)
	}
	base := r.evaluatePermission(descriptor, args)
	if base.Decision == domain.AssistantPermissionDecisionDeny {
		return AssistantPreparedWork{}, denyAssistantWork("%s", firstNonEmptyString(base.Reason, "permission policy denied tool"))
	}
	hook := AssistantHookOutcome{}
	if r.hooks != nil {
		hook = r.hooks.Run(ctx, AssistantHookEventPreToolUse, AssistantHookInput{SessionID: execution.SessionID, ToolName: work.ToolName, ToolArgs: args})
	}
	if len(hook.UpdatedInput) > 0 {
		if _, changesKey := hook.UpdatedInput["idempotency_key"]; changesKey {
			return AssistantPreparedWork{}, denyAssistantWork("hook cannot change executor idempotency key")
		}
		args = mergeAssistantToolArgs(args, hook.UpdatedInput)
		if err = validateAssistantWorkSchema(descriptor.InputSchema, assistantSchemaArgs(descriptor, args, work.IdempotencyKey)); err != nil {
			return AssistantPreparedWork{}, denyAssistantWork("transformed input schema: %v", err)
		}
	}
	current := r.evaluatePermission(descriptor, args)
	if assistantPermissionRank(current.Decision) < assistantPermissionRank(base.Decision) {
		current = base
	}
	current = applyAssistantHookDecision(current, hook)
	if hook.Blocked || current.Decision == domain.AssistantPermissionDecisionDeny {
		return AssistantPreparedWork{}, denyAssistantWork("%s", firstNonEmptyString(hook.Reason, current.Reason, "denied by current policy or hook"))
	}
	if current.Decision != domain.AssistantPermissionDecisionAllow && current.Decision != domain.AssistantPermissionDecisionAsk {
		return AssistantPreparedWork{}, denyAssistantWork("permission decision %q unsupported", current.Decision)
	}
	digest, err := domain.ComputeAssistantArgumentsDigest(args)
	if err != nil {
		return AssistantPreparedWork{}, denyAssistantWork("arguments digest: %v", err)
	}
	effective := work
	effective.Arguments = args
	effective.ArgumentsDigest = digest
	prepared := AssistantPreparedWork{Execution: execution, Work: effective, Descriptor: descriptor, Permission: current}
	if work.Authorization == nil {
		// Batch approval is mandatory even where audited mode would let an
		// iterative mutation run autonomously; ask always needs an operator.
		if execution.Workflow == domain.AssistantWorkflowBatch || current.Decision == domain.AssistantPermissionDecisionAsk {
			return prepared, ErrAssistantApprovalRequired
		}
		return prepared, nil
	}
	if work.ArgumentsDigest != digest {
		return prepared, ErrAssistantApprovedInputChanged
	}
	binding := work.Authorization
	scope, err := execution.Scope.Clone()
	if err != nil {
		return AssistantPreparedWork{}, err
	}
	if binding.ArgumentsDigest != digest || !assistantScopesEqual(binding.Scope, scope) || binding.OperatorPubkey == "" || binding.DecisionRequestID == "" {
		return AssistantPreparedWork{}, denyAssistantWork("approval binding does not match effective input or scope")
	}
	if execution.Workflow == domain.AssistantWorkflowBatch {
		if execution.Proposal == nil || binding.ProposalID != execution.Proposal.ProposalID || binding.ProposalRevision != execution.Proposal.Revision {
			return AssistantPreparedWork{}, denyAssistantWork("batch approval does not bind the approved proposal revision")
		}
	} else if binding.ActionID == "" || binding.ActionID != work.WorkID {
		// An action approval authorizes exactly one work item, never another
		// call that happens to use the same tool.
		return AssistantPreparedWork{}, denyAssistantWork("action approval does not bind this work item")
	}
	return prepared, nil
}

func assistantScopeAllows(scope domain.AssistantCommandScope, toolName string) bool {
	if scope.AllowedTools == nil {
		return true
	}
	for _, name := range scope.AllowedTools {
		if name == toolName {
			return true
		}
	}
	return false
}

func validateAssistantWorkSchema(schema map[string]any, args map[string]any) error {
	if schema == nil {
		return fmt.Errorf("registered tool schema is missing")
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var document any
	if err = json.Unmarshal(raw, &document); err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://bahia.local/assistant-tool.json", document); err != nil {
		return err
	}
	compiled, err := compiler.Compile("https://bahia.local/assistant-tool.json")
	if err != nil {
		return err
	}
	return compiled.Validate(args)
}

// DispatchPreparedWork performs exactly one provider invocation. A read-only
// synchronous tool error is a definite failed observation; any other provider
// error, or a missing/mismatched async receipt, is ambiguous: the request may
// have been submitted, so the caller must record it as uncertain.
func (r *AssistantToolRuntime) DispatchPreparedWork(ctx context.Context, prepared AssistantPreparedWork) (*domain.AssistantToolObservation, *domain.AsyncToolReceipt, error) {
	if r == nil || r.mcpServer == nil {
		return nil, nil, fmt.Errorf("assistant MCP server is not configured")
	}
	work := prepared.Work
	call := domain.AssistantAgentToolCall{ID: work.OriginID, Name: work.ToolName, Arguments: work.Arguments}
	req := AssistantToolRuntimeRequest{RunID: prepared.Execution.RunID, TurnID: prepared.Execution.TurnID, ToolCall: call}
	descriptor := prepared.Descriptor
	var obs *domain.AssistantToolObservation
	var receipt *domain.AsyncToolReceipt
	switch descriptor.ExecutionMode {
	case domain.AssistantToolExecutionModeAsync:
		if work.IdempotencyKey == "" {
			return nil, nil, fmt.Errorf("assistant executor idempotency key missing")
		}
		args := cloneInterfaceArgs(work.Arguments)
		args["idempotency_key"] = work.IdempotencyKey
		var err error
		receipt, err = r.mcpServer.InvokeAssistantAsyncTool(ctx, work.ToolName, args)
		if err != nil {
			return nil, nil, err
		}
		if receipt == nil || receipt.RequestEventID == "" || len(receipt.ResultKinds) == 0 || receipt.ToolName != work.ToolName || receipt.IdempotencyKey != work.IdempotencyKey {
			return nil, nil, fmt.Errorf("assistant async receipt is missing or mismatched")
		}
		return nil, cloneAsyncToolReceipt(receipt), nil
	case domain.AssistantToolExecutionModeSync:
		result, err := r.mcpServer.CallTool(ctx, work.ToolName, cloneInterfaceArgs(work.Arguments))
		if err != nil {
			if descriptor.Effect != domain.AssistantToolEffectRead {
				return nil, nil, err
			}
			obs = r.failedObservation(req, call, descriptor, prepared.Permission, err.Error(), nil)
		} else {
			obs = r.observationFromMCPResult(req, call, descriptor, prepared.Permission, result)
		}
	default:
		return nil, nil, fmt.Errorf("assistant tool execution mode unsupported")
	}
	if r.hooks != nil && obs != nil {
		post := r.hooks.Run(ctx, AssistantHookEventPostToolUse, AssistantHookInput{SessionID: prepared.Execution.SessionID, ToolName: work.ToolName, ToolArgs: work.Arguments, Text: obs.Summary})
		if text := strings.TrimSpace(firstNonEmptyString(post.AdditionalContext, post.SystemMessage)); text != "" {
			if obs.Metadata == nil {
				obs.Metadata = map[string]any{}
			}
			obs.Metadata["post_tool_use_context"] = text
		}
	}
	return obs, receipt, nil
}

func assistantScopesEqual(a, b domain.AssistantCommandScope) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(left) == string(right)
}

// ExecuteWithHooks preserves v1 caller behavior while placing its permission,
// hook and schema ordering at the common runtime boundary. The v2 executor
// uses PrepareWork and DispatchPreparedWork instead.
func (r *AssistantToolRuntime) ExecuteWithHooks(ctx context.Context, req AssistantToolRuntimeRequest, hooks *AssistantHookRunner) (*domain.AssistantToolObservation, error) {
	call := req.ToolCall
	if r == nil || req.Session == nil {
		return nil, fmt.Errorf("assistant tool runtime or session is not configured")
	}
	descriptor, found := r.lookupDescriptor(call.Name)
	if !found {
		return r.Execute(ctx, req)
	}
	if descriptor.InputSchema != nil {
		if err := validateAssistantWorkSchema(descriptor.InputSchema, assistantSchemaArgs(descriptor, call.Arguments, assistantToolIdempotencyKey(req.Session.SessionID, req.RunID, call.ID))); err != nil {
			return r.deniedObservation(req, call, domain.AssistantPermissionResult{Decision: domain.AssistantPermissionDecisionDeny, Reason: err.Error()}), nil
		}
	}
	base := r.evaluatePermission(descriptor, call.Arguments)
	if base.Decision == domain.AssistantPermissionDecisionDeny {
		req.PermissionOverride = &base
		return r.Execute(ctx, req)
	}
	hook := AssistantHookOutcome{}
	if hooks != nil {
		hook = hooks.Run(ctx, AssistantHookEventPreToolUse, AssistantHookInput{SessionID: req.Session.SessionID, ToolName: call.Name, ToolArgs: call.Arguments})
	}
	final := base
	if len(hook.UpdatedInput) > 0 {
		call.Arguments = mergeAssistantToolArgs(call.Arguments, hook.UpdatedInput)
		req.ToolCall = call
		if descriptor.InputSchema != nil {
			if err := validateAssistantWorkSchema(descriptor.InputSchema, assistantSchemaArgs(descriptor, call.Arguments, assistantToolIdempotencyKey(req.Session.SessionID, req.RunID, call.ID))); err != nil {
				return r.deniedObservation(req, call, domain.AssistantPermissionResult{Decision: domain.AssistantPermissionDecisionDeny, Reason: err.Error()}), nil
			}
		}
		current := r.evaluatePermission(descriptor, call.Arguments)
		if assistantPermissionRank(current.Decision) >= assistantPermissionRank(base.Decision) {
			final = current
		}
	}
	final = applyAssistantHookDecision(final, hook)
	req.PermissionOverride = &final
	obs, err := r.Execute(ctx, req)
	if err != nil || obs == nil {
		return obs, err
	}
	if hooks != nil {
		post := hooks.Run(ctx, AssistantHookEventPostToolUse, AssistantHookInput{SessionID: req.Session.SessionID, ToolName: call.Name, ToolArgs: call.Arguments, Text: obs.Summary})
		if text := strings.TrimSpace(firstNonEmptyString(post.AdditionalContext, post.SystemMessage)); text != "" {
			if obs.Metadata == nil {
				obs.Metadata = map[string]any{}
			}
			obs.Metadata["post_tool_use_context"] = text
		}
	}
	return obs, nil
}

func assistantExecutionIdempotencyKey(x domain.AssistantExecution, w domain.AssistantWorkItem) string {
	if x.Workflow == domain.AssistantWorkflowBatch {
		if x.Proposal == nil {
			return ""
		}
		return fmt.Sprintf("assistant:%s:%s:%s", x.SessionID, x.Proposal.Hash, w.OriginID)
	}
	return fmt.Sprintf("assistant-agent:%s:%s:%s", x.SessionID, x.RunID, w.OriginID)
}

func assistantSchemaArgs(d AssistantToolRuntimeToolDescriptor, args map[string]any, key string) map[string]any {
	if d.ExecutionMode != domain.AssistantToolExecutionModeAsync {
		return args
	}
	out := cloneAnyArgs(args)
	out["idempotency_key"] = key
	return out
}
