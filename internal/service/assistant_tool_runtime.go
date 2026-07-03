package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	sessions    AssistantToolRuntimeSessionPersister
	observer    AssistantAsyncResultObserver
	now         func() time.Time
	newID       func(prefix string) string
}

// AssistantToolRuntimeRequest is one model-requested tool invocation.
type AssistantToolRuntimeRequest struct {
	Session     *domain.AssistantSession
	RunID       string
	TurnID      string
	Iteration   int
	ToolCall    domain.AssistantAgentToolCall
	PlanHash    string
	CancelScope string
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
	permission := r.evaluatePermission(descriptor, call.Arguments)
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

func (r *AssistantToolRuntime) evaluatePermission(descriptor AssistantToolRuntimeToolDescriptor, args map[string]any) domain.AssistantPermissionResult {
	metadata := AssistantToolPermissionMetadata{Name: descriptor.Name, Effect: descriptor.Effect, DefaultRisk: descriptor.DefaultRisk, ExecutionMode: descriptor.ExecutionMode, ResourceTypes: append([]string(nil), descriptor.ResourceTypes...)}
	if r.permissions == nil {
		return domain.AssistantPermissionResult{Decision: domain.AssistantPermissionDecisionDeny, Effect: descriptor.Effect, Risk: descriptor.DefaultRisk, ExecutionMode: descriptor.ExecutionMode, Reason: "assistant permission engine is not configured"}
	}
	return r.permissions.Evaluate(AssistantPermissionRequest{Tool: metadata, Args: args})
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
	return AssistantAsyncObservationOutcome{Status: outcome.Status, Event: outcome.Event}, err
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
