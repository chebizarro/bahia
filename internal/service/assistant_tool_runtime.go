package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Historical v1 session metadata keys. They are read only by the v1
// compatibility classifier; nothing writes them any more.
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

// AssistantAsyncObservationOutcome is the normalized terminal observation the
// executor's work observer reports for one submitted async request.
type AssistantAsyncObservationOutcome struct {
	Status string
	Event  *nostr.Event
}

// AssistantInternalToolCall is one invocation of a service-owned internal tool
// (subagent delegation, skill loading). It is only ever produced by the
// executor's dispatch of an already-checkpointed, authorized work item.
type AssistantInternalToolCall struct {
	SessionID string
	RunID     string
	TurnID    string
	WorkID    string
	Scope     domain.AssistantCommandScope
	ToolCall  domain.AssistantAgentToolCall
	// OperatorPubkey is the operator the parent work acts as; subagent
	// children act as the same operator.
	OperatorPubkey string
}

// AssistantInternalTool is a service-owned tool registration. Internal tools are
// synchronous and read-only toward the external control plane; they keep this
// separate registration (they are not MCP registry tools and are not part of the
// batch catalog) but pass the same scope, schema, permission and hook gate as
// every other work item before the executor dispatches them.
type AssistantInternalTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Effect      domain.AssistantToolEffect
	Risk        domain.AssistantToolRisk
	Handler     func(ctx context.Context, call AssistantInternalToolCall) (*domain.AssistantToolObservation, error)
}

// AssistantToolRuntimeConfig wires the common authorization/dispatch gateway.
// The runtime never owns execution state: the executor checkpoints every
// transition and calls PrepareWork/DispatchPreparedWork.
type AssistantToolRuntimeConfig struct {
	MCPServer   AssistantToolRuntimeMCPServer
	Registry    AssistantToolRuntimeRegistry
	Permissions AssistantToolPermissionEvaluator
	Hooks       *AssistantHookRunner
	Now         func() time.Time
	NewID       func(prefix string) string
}

// AssistantToolRuntime is the single authorization and dispatch gateway for
// assistant work. It normalizes sync MCP results, internal-tool results and
// async receipts into AssistantToolObservation/receipt values for the executor.
type AssistantToolRuntime struct {
	mcpServer   AssistantToolRuntimeMCPServer
	registry    AssistantToolRuntimeRegistry
	permissions AssistantToolPermissionEvaluator
	hooks       *AssistantHookRunner
	now         func() time.Time
	newID       func(prefix string) string

	internalMu sync.RWMutex
	internal   map[string]AssistantInternalTool
}

// NewAssistantToolRuntime constructs the common gateway used by the executor.
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
		now:         now,
		newID:       newID,
		internal:    map[string]AssistantInternalTool{},
	}
}

// RegisterInternalTools adds service-owned internal tools. A name that collides
// with a registered MCP tool, or registers twice, is refused so an internal
// handler can never shadow an external tool's policy.
func (r *AssistantToolRuntime) RegisterInternalTools(tools ...AssistantInternalTool) error {
	if r == nil {
		return fmt.Errorf("assistant tool runtime is not configured")
	}
	r.internalMu.Lock()
	defer r.internalMu.Unlock()
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" || tool.Handler == nil || tool.InputSchema == nil {
			return fmt.Errorf("assistant internal tool registration requires name, schema and handler")
		}
		if _, exists := r.internal[name]; exists {
			return fmt.Errorf("assistant internal tool %q registered twice", name)
		}
		if r.registry != nil {
			if _, exists := r.registry.GetAgentTool(name); exists {
				return fmt.Errorf("assistant internal tool %q collides with a registered MCP tool", name)
			}
		}
		tool.Name = name
		r.internal[name] = tool
	}
	return nil
}

func (r *AssistantToolRuntime) internalTool(name string) (AssistantInternalTool, bool) {
	r.internalMu.RLock()
	defer r.internalMu.RUnlock()
	tool, ok := r.internal[name]
	return tool, ok
}

func assistantInternalDescriptor(tool AssistantInternalTool) AssistantToolRuntimeToolDescriptor {
	return AssistantToolRuntimeToolDescriptor{Name: tool.Name, ExecutionMode: domain.AssistantToolExecutionModeSync, Effect: tool.Effect, DefaultRisk: tool.Risk, InputSchema: tool.InputSchema}
}

func (r *AssistantToolRuntime) failedObservation(call domain.AssistantAgentToolCall, descriptor AssistantToolRuntimeToolDescriptor, permission domain.AssistantPermissionResult, message string) *domain.AssistantToolObservation {
	return &domain.AssistantToolObservation{ObservationID: r.newID("obs"), ToolCallID: call.ID, ToolName: call.Name, Status: domain.AssistantToolObservationFailed, Effect: descriptor.Effect, Risk: permission.Risk, ExecutionMode: descriptor.ExecutionMode, Summary: "assistant tool failed", Error: message, ObservedAt: r.now().UTC(), Metadata: map[string]any{"permission": permission}}
}

func (r *AssistantToolRuntime) observationFromMCPResult(call domain.AssistantAgentToolCall, descriptor AssistantToolRuntimeToolDescriptor, permission domain.AssistantPermissionResult, result *AssistantToolRuntimeToolResult) *domain.AssistantToolObservation {
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

// lookupDescriptor resolves a registered MCP tool first, then a service-owned
// internal tool. Internal tools cannot shadow MCP tools (see registration).
func (r *AssistantToolRuntime) lookupDescriptor(name string) (AssistantToolRuntimeToolDescriptor, bool) {
	if r.registry != nil {
		if descriptor, ok := r.registry.GetAgentTool(name); ok {
			return descriptor, true
		}
	}
	if tool, ok := r.internalTool(name); ok {
		return assistantInternalDescriptor(tool), true
	}
	return AssistantToolRuntimeToolDescriptor{}, false
}

func (r *AssistantToolRuntime) evaluatePermission(descriptor AssistantToolRuntimeToolDescriptor, args map[string]any) domain.AssistantPermissionResult {
	metadata := AssistantToolPermissionMetadata{Name: descriptor.Name, Effect: descriptor.Effect, DefaultRisk: descriptor.DefaultRisk, ExecutionMode: descriptor.ExecutionMode, ResourceTypes: append([]string(nil), descriptor.ResourceTypes...)}
	if r.permissions == nil {
		return domain.AssistantPermissionResult{Decision: domain.AssistantPermissionDecisionDeny, Effect: descriptor.Effect, Risk: descriptor.DefaultRisk, ExecutionMode: descriptor.ExecutionMode, Reason: "assistant permission engine is not configured"}
	}
	return r.permissions.Evaluate(AssistantPermissionRequest{Tool: metadata, Args: args})
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
	if _, internal := r.internalTool(work.ToolName); internal && execution.Workflow != domain.AssistantWorkflowIterative {
		// Internal tools keep their separate registration and are not part of
		// the batch catalog.
		return AssistantPreparedWork{}, denyAssistantWork("internal tool %q is not available to the %s workflow", work.ToolName, execution.Workflow)
	}
	if work.IdempotencyKey == "" {
		work.IdempotencyKey = assistantExecutionIdempotencyKey(execution, work)
	}
	// PreToolUse hooks may call read-only MCP tools as this work's operator.
	ctx = assistantOperatorContext(ctx, assistantWorkOperator(execution, work))
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
	if r == nil {
		return nil, nil, fmt.Errorf("assistant tool runtime is not configured")
	}
	work := prepared.Work
	call := domain.AssistantAgentToolCall{ID: work.OriginID, Name: work.ToolName, Arguments: work.Arguments}
	descriptor := prepared.Descriptor
	// The one place tool calls get their principal: the operator this work
	// acts as, from the persisted execution (so recovery and re-dispatch use
	// the same operator). It covers the provider call, PostToolUse hooks,
	// internal tools and their subagent children.
	operator := assistantWorkOperator(prepared.Execution, work)
	ctx = assistantOperatorContext(ctx, operator)
	var obs *domain.AssistantToolObservation
	var receipt *domain.AsyncToolReceipt
	if tool, internal := r.internalTool(work.ToolName); internal {
		scope, err := prepared.Execution.Scope.Clone()
		if err != nil {
			return nil, nil, err
		}
		args, err := domain.DeepCopyAssistantJSONMap(work.Arguments)
		if err != nil {
			return nil, nil, err
		}
		call.Arguments = args
		obs, err = tool.Handler(ctx, AssistantInternalToolCall{SessionID: prepared.Execution.SessionID, RunID: prepared.Execution.RunID, TurnID: prepared.Execution.TurnID, WorkID: work.WorkID, Scope: scope, ToolCall: call, OperatorPubkey: operator})
		if err != nil {
			// Internal tools are read-only toward the control plane, so a
			// handler error is a definite failure, never an ambiguous submit.
			obs = r.failedObservation(call, descriptor, prepared.Permission, err.Error())
		}
		if obs == nil {
			obs = r.failedObservation(call, descriptor, prepared.Permission, "internal tool returned no observation")
		}
		obs.ToolCallID, obs.ToolName = call.ID, call.Name
		return r.postToolUse(ctx, prepared, obs), nil, nil
	}
	if r.mcpServer == nil {
		return nil, nil, fmt.Errorf("assistant MCP server is not configured")
	}
	switch descriptor.ExecutionMode {
	case domain.AssistantToolExecutionModeAsync:
		if work.IdempotencyKey == "" {
			return nil, nil, fmt.Errorf("assistant executor idempotency key missing")
		}
		args := cloneInterfaceArgs(work.Arguments)
		args["idempotency_key"] = work.IdempotencyKey
		var err error
		receipt, err = r.mcpServer.InvokeAssistantAsyncTool(ctx, work.ToolName, args)
		if errors.Is(err, ErrAssistantToolCallRefused) {
			// Refused before submission: a definite failure, not uncertainty.
			return r.postToolUse(ctx, prepared, r.failedObservation(call, descriptor, prepared.Permission, err.Error())), nil, nil
		}
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
			obs = r.failedObservation(call, descriptor, prepared.Permission, err.Error())
		} else {
			obs = r.observationFromMCPResult(call, descriptor, prepared.Permission, result)
		}
	default:
		return nil, nil, fmt.Errorf("assistant tool execution mode unsupported")
	}
	return r.postToolUse(ctx, prepared, obs), receipt, nil
}

func (r *AssistantToolRuntime) postToolUse(ctx context.Context, prepared AssistantPreparedWork, obs *domain.AssistantToolObservation) *domain.AssistantToolObservation {
	if r.hooks == nil || obs == nil {
		return obs
	}
	work := prepared.Work
	post := r.hooks.Run(ctx, AssistantHookEventPostToolUse, AssistantHookInput{SessionID: prepared.Execution.SessionID, ToolName: work.ToolName, ToolArgs: work.Arguments, Text: obs.Summary})
	if text := strings.TrimSpace(firstNonEmptyString(post.AdditionalContext, post.SystemMessage)); text != "" {
		if obs.Metadata == nil {
			obs.Metadata = map[string]any{}
		}
		obs.Metadata["post_tool_use_context"] = text
	}
	return obs
}

// ExecuteSubagentTool runs one tool call of a delegated subagent child loop.
// It is reachable only from inside an internal-tool handler, which the
// executor dispatches for an already-checkpointed, authorized parent work
// item. The child call passes the same gate as parent work (registration,
// schema, the parent run's persisted command scope, current permission policy
// and hooks) and is then restricted further: only synchronous, read-effect
// MCP tools that the policy allows outright may run. Anything that mutates,
// needs approval, submits async work or is another internal tool is refused,
// so every control-plane effect stays an individually checkpointed executor
// work item and a child never touches parent execution state.
func (r *AssistantToolRuntime) ExecuteSubagentTool(ctx context.Context, parent AssistantInternalToolCall, childSessionID string, call domain.AssistantAgentToolCall) *domain.AssistantToolObservation {
	deny := func(reason string) *domain.AssistantToolObservation {
		return &domain.AssistantToolObservation{ObservationID: r.newID("obs"), ToolCallID: call.ID, ToolName: call.Name, Status: domain.AssistantToolObservationDenied, Summary: "subagent tool denied", Error: reason, ObservedAt: r.now().UTC(), Metadata: map[string]any{"subagent_child": true}}
	}
	if r == nil {
		return deny("assistant tool runtime is not configured")
	}
	name := strings.TrimSpace(call.Name)
	descriptor, class := r.classifyReadOnlySync(name)
	switch class {
	case assistantToolInternal:
		return deny(fmt.Sprintf("internal tool %q is unavailable to subagents", name))
	case assistantToolUnregistered:
		return deny(fmt.Sprintf("tool %q is not registered for agent use", name))
	case assistantToolAsync:
		return deny(fmt.Sprintf("subagents cannot execute async tool %q; it must run in the parent turn", name))
	case assistantToolMutation:
		return deny(fmt.Sprintf("subagents cannot execute mutation tool %q; it must run in the parent turn", name))
	}
	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	scope, err := parent.Scope.Clone()
	if err != nil {
		return deny("parent command scope is invalid")
	}
	child := domain.AssistantExecution{Version: domain.AssistantExecutionVersion, SessionID: childSessionID, RunID: parent.RunID, TurnID: parent.TurnID, Workflow: domain.AssistantWorkflowIterative, Scope: scope, OperatorPubkey: parent.OperatorPubkey}
	work := domain.AssistantWorkItem{WorkID: parent.WorkID + ":" + call.ID, OriginID: call.ID, ToolName: name, Arguments: args}
	prepared, err := r.PrepareWork(ctx, child, work)
	var denial *AssistantWorkDenial
	switch {
	case err == nil:
	case errors.Is(err, ErrAssistantApprovalRequired):
		return deny(fmt.Sprintf("tool %q requires operator approval and is unavailable to subagents", name))
	case errors.As(err, &denial):
		return deny(denial.Reason)
	default:
		return deny(err.Error())
	}
	obs, receipt, err := r.DispatchPreparedWork(ctx, prepared)
	if receipt != nil {
		// Unreachable for sync descriptors; refuse rather than hide a submit.
		return deny("subagent tool unexpectedly produced an async receipt")
	}
	if err != nil {
		return r.failedObservation(call, descriptor, prepared.Permission, err.Error())
	}
	return obs
}

// assistantToolClass is the outcome of the one classification that decides
// which tools may run without an individually accounted side effect.
type assistantToolClass int

const (
	assistantToolReadOnlySync assistantToolClass = iota
	assistantToolInternal
	assistantToolUnregistered
	assistantToolAsync
	assistantToolMutation
)

// classifyReadOnlySync is the single classification of read-only synchronous
// tools: registered for agent use, not a service-owned internal tool,
// synchronous and read-effect. Subagent children may call only such tools,
// and the executor may re-dispatch only such tools after a restart lost the
// outcome of their dispatch. It is a registry lookup and performs no I/O.
func (r *AssistantToolRuntime) classifyReadOnlySync(name string) (AssistantToolRuntimeToolDescriptor, assistantToolClass) {
	if _, internal := r.internalTool(name); internal {
		return AssistantToolRuntimeToolDescriptor{}, assistantToolInternal
	}
	descriptor, ok := r.lookupDescriptor(name)
	switch {
	case !ok:
		return descriptor, assistantToolUnregistered
	case descriptor.ExecutionMode != domain.AssistantToolExecutionModeSync:
		return descriptor, assistantToolAsync
	case descriptor.Effect != domain.AssistantToolEffectRead:
		return descriptor, assistantToolMutation
	}
	return descriptor, assistantToolReadOnlySync
}

// ReplaySafeTool reports whether a tool is in the read-only synchronous
// classification that restricts subagent children, and so has no side effect
// that an automatic re-dispatch could duplicate.
func (r *AssistantToolRuntime) ReplaySafeTool(name string) bool {
	if r == nil {
		return false
	}
	_, class := r.classifyReadOnlySync(strings.TrimSpace(name))
	return class == assistantToolReadOnlySync
}

// InternalToolNames lists the registered service-owned internal tools.
func (r *AssistantToolRuntime) InternalToolNames() []string {
	if r == nil {
		return nil
	}
	r.internalMu.RLock()
	defer r.internalMu.RUnlock()
	names := make([]string, 0, len(r.internal))
	for name := range r.internal {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func assistantScopesEqual(a, b domain.AssistantCommandScope) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(left) == string(right)
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
