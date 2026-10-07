package service

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/openagentsinc/bahia/internal/domain"
)

const defaultAssistantTurnInputLimit = 256

// AssistantStatusPublisher publishes informational kind-30315 assistant status
// events (planner stream chunks, final answers, clarifying questions). Status
// events are a read model for the operator; they never carry execution state.
type AssistantStatusPublisher interface {
	PublishAssistantStatus(ctx context.Context, sessionID, status string, content map[string]any) error
}

// AssistantProposalContextConfig wires the shared proposal preparation used by
// both workflows.
type AssistantProposalContextConfig struct {
	Commands *AssistantCommandLibrary
	Hooks    *AssistantHookRunner
	// TurnInputLimit bounds remembered request-scoped route context.
	TurnInputLimit int
}

// AssistantProposalContext is the one place command expansion and
// prompt/session hooks run, for batch and iterative proposals alike. As the
// executor's scope resolver it derives the trusted command scope from the
// operator prompt before any proposer runs; the executor persists that scope in
// the run's root checkpoint, and proposers re-derive the expanded prompt from
// the persisted scope rather than trusting browser metadata.
type AssistantProposalContext struct {
	commands *AssistantCommandLibrary
	hooks    *AssistantHookRunner
	limit    int

	mu     sync.Mutex
	inputs map[string]assistantTurnInput
	order  []string
}

type assistantTurnInput struct {
	routeContext map[string]string
}

// AssistantPreparedTurn is the expanded operator prompt plus hook-injected
// context for the first proposal of a run.
type AssistantPreparedTurn struct {
	Prompt        string
	SystemContext []string
	RouteContext  map[string]string
	SelectedRefs  []string
	// Blocked is non-empty when a UserPromptSubmit hook refused the prompt.
	Blocked string
}

var _ AssistantExecutionScopeResolver = (*AssistantProposalContext)(nil)

func NewAssistantProposalContext(cfg AssistantProposalContextConfig) *AssistantProposalContext {
	limit := cfg.TurnInputLimit
	if limit <= 0 {
		limit = defaultAssistantTurnInputLimit
	}
	return &AssistantProposalContext{commands: cfg.Commands, hooks: cfg.Hooks, limit: limit, inputs: map[string]assistantTurnInput{}}
}

// ResolveAssistantScope expands a leading slash command into its persisted
// scope: command name, allowed tools (nil = unrestricted, [] = no tools) and
// the private command arguments. It also remembers the request's route context
// for the proposers; route context is presentation input, never authority.
func (c *AssistantProposalContext) ResolveAssistantScope(_ context.Context, req AssistantTurnStartRequest) (domain.AssistantCommandScope, error) {
	scope := domain.AssistantCommandScope{SelectedRefs: append([]string(nil), req.Prompt.SelectedRefs...)}
	if c == nil {
		return scope, nil
	}
	c.remember(req.Prompt.SessionID, req.Prompt.TurnID, routeContextStrings(req.Prompt.RouteContext))
	expansion, ok := c.commands.Expand(req.Prompt.Prompt)
	if !ok {
		return scope, nil
	}
	scope.CommandName = expansion.Command.Name
	scope.AllowedTools = cloneAssistantAllowedTools(expansion.AllowedTools)
	if _, args := splitAssistantCommandInvocation(req.Prompt.Prompt); args != "" {
		scope.Arguments = map[string]any{"arguments": args}
	}
	return scope, nil
}

// PrepareTurn expands the prompt against the persisted scope and runs the
// SessionStart (first turn only) and UserPromptSubmit hooks. A prompt whose
// expansion no longer matches the persisted command scope is refused: the
// persisted scope is authoritative.
func (c *AssistantProposalContext) PrepareTurn(ctx context.Context, req AssistantProposalRequest, firstTurn bool) (AssistantPreparedTurn, error) {
	prepared := AssistantPreparedTurn{Prompt: strings.TrimSpace(req.Prompt), SelectedRefs: append([]string(nil), req.Scope.SelectedRefs...)}
	if c == nil {
		return prepared, nil
	}
	prepared.RouteContext = c.RouteContext(req.SessionID, req.TurnID)
	expansion, expanded := c.commands.Expand(prepared.Prompt)
	switch {
	case expanded && expansion.Command.Name != req.Scope.CommandName:
		return prepared, fmt.Errorf("assistant command %q does not match the persisted command scope %q", expansion.Command.Name, req.Scope.CommandName)
	case !expanded && req.Scope.CommandName != "":
		return prepared, fmt.Errorf("assistant command %q from the persisted scope is no longer available", req.Scope.CommandName)
	case expanded:
		prepared.Prompt = expansion.Prompt
	}
	if c.hooks == nil {
		return prepared, nil
	}
	if firstTurn {
		start := c.hooks.Run(ctx, AssistantHookEventSessionStart, AssistantHookInput{SessionID: req.SessionID})
		if text := strings.TrimSpace(firstNonEmptyString(start.AdditionalContext, start.SystemMessage)); text != "" {
			prepared.SystemContext = append(prepared.SystemContext, text)
		}
	}
	outcome := c.hooks.Run(ctx, AssistantHookEventUserPromptSubmit, AssistantHookInput{SessionID: req.SessionID, Text: prepared.Prompt})
	if outcome.Blocked || outcome.Decision == AssistantHookDecisionDeny {
		prepared.Blocked = firstNonEmptyString(outcome.Reason, "operator prompt blocked by UserPromptSubmit hook")
		return prepared, nil
	}
	if updated := strings.TrimSpace(stringFromAnyMapAny(outcome.UpdatedInput, "prompt")); updated != "" {
		prepared.Prompt = updated
	}
	if text := strings.TrimSpace(firstNonEmptyString(outcome.AdditionalContext, outcome.SystemMessage)); text != "" {
		prepared.SystemContext = append(prepared.SystemContext, text)
	}
	return prepared, nil
}

// RouteContext returns the remembered route context of a turn, if any.
func (c *AssistantProposalContext) RouteContext(sessionID, turnID string) map[string]string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneAssistantLoopStringMap(c.inputs[sessionID+"\x00"+turnID].routeContext)
}

func (c *AssistantProposalContext) remember(sessionID, turnID string, routeContext map[string]string) {
	if sessionID == "" || turnID == "" {
		return
	}
	key := sessionID + "\x00" + turnID
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.inputs[key]; !exists {
		c.order = append(c.order, key)
	}
	c.inputs[key] = assistantTurnInput{routeContext: cloneAssistantLoopStringMap(routeContext)}
	for len(c.order) > c.limit {
		delete(c.inputs, c.order[0])
		c.order = c.order[1:]
	}
}

// assistantScopeFilter reports whether a tool may be advertised under a
// persisted command scope. It mirrors the executor's enforcement.
func assistantScopeFilter(scope domain.AssistantCommandScope) func(string) bool {
	if scope.AllowedTools == nil {
		return func(string) bool { return true }
	}
	allowed := make(map[string]bool, len(scope.AllowedTools))
	for _, name := range scope.AllowedTools {
		allowed[strings.TrimSpace(name)] = true
	}
	return func(name string) bool { return allowed[name] }
}
