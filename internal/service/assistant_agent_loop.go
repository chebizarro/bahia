package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
// for the model request from the MCP registry.
type AssistantAgentToolSchemaProvider interface {
	AgentToolSchemas(ctx context.Context) ([]llm.AgentToolSchema, error)
}

// AssistantAgentLoopConfig wires the iterative proposal producer.
type AssistantAgentLoopConfig struct {
	ModelClient llm.AgentModelClient
	// ToolRuntime receives the loop's internal-tool registrations and runs
	// subagent child calls through its gate. The loop never dispatches work.
	ToolRuntime    *AssistantToolRuntime
	ContextBuilder AssistantAgentModelHistoryBuilder
	ToolSchemas    AssistantAgentToolSchemaProvider
	// Transcript is the model history of record. Every prompt and model
	// response is appended once under a run-scoped logical identity; the
	// executor appends tool observations the same way.
	Transcript AssistantExecutionTranscript
	Context    *AssistantProposalContext
	Status     AssistantStatusPublisher
	Agentic    config.AssistantAgenticConfig
	Subagents  *AssistantSubagentLibrary
	Skills     *AssistantSkillLibrary
	Hooks      *AssistantHookRunner
	Logger     *slog.Logger
	Now        func() time.Time
	NewID      func(prefix string) string
}

// AssistantAgentLoop is the iterative proposal producer. Each ProposeIterative
// call asks the model for the next step of a run and returns every tool call
// of that model response for the executor to checkpoint and run; it never
// dispatches a tool itself. It owns the per-run model iteration cap, derived
// from the durable transcript so a restart cannot reset it.
type AssistantAgentLoop struct {
	modelClient    llm.AgentModelClient
	toolRuntime    *AssistantToolRuntime
	contextBuilder AssistantAgentModelHistoryBuilder
	toolSchemas    AssistantAgentToolSchemaProvider
	transcript     AssistantExecutionTranscript
	proposal       *AssistantProposalContext
	status         AssistantStatusPublisher
	agentic        config.AssistantAgenticConfig
	subagents      *AssistantSubagentLibrary
	skills         *AssistantSkillLibrary
	hooks          *AssistantHookRunner
	internalTools  []AssistantInternalTool
	logger         *slog.Logger
	now            func() time.Time
	newID          func(prefix string) string

	mu         sync.Mutex
	iterations map[string]int
}

var _ AssistantIterativeProposer = (*AssistantAgentLoop)(nil)

// NewAssistantAgentLoop constructs the iterative proposer and registers its
// service-owned internal tools (subagent delegation, skill loading) with the
// common runtime, so the executor can authorize and dispatch them like any
// other work item.
func NewAssistantAgentLoop(cfg AssistantAgentLoopConfig) (*AssistantAgentLoop, error) {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newID := cfg.NewID
	if newID == nil {
		newID = randomAssistantAgentLoopID
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	loop := &AssistantAgentLoop{modelClient: cfg.ModelClient, toolRuntime: cfg.ToolRuntime, contextBuilder: cfg.ContextBuilder, toolSchemas: cfg.ToolSchemas, transcript: cfg.Transcript, proposal: cfg.Context, status: cfg.Status, agentic: cfg.Agentic, subagents: cfg.Subagents, skills: cfg.Skills, hooks: cfg.Hooks, logger: logger.With("component", "assistant_iterative_proposer"), now: now, newID: newID, iterations: map[string]int{}}
	if loop.subagents.Len() > 0 {
		loop.internalTools = append(loop.internalTools, loop.buildDelegateSubagentTool())
	}
	if loop.skills.Len() > 0 {
		loop.internalTools = append(loop.internalTools, loop.buildSkillLoadTool())
	}
	if len(loop.internalTools) > 0 {
		if cfg.ToolRuntime == nil {
			return nil, errors.New("assistant internal tools require the common tool runtime")
		}
		if err := cfg.ToolRuntime.RegisterInternalTools(loop.internalTools...); err != nil {
			return nil, err
		}
	}
	return loop, nil
}

// ProposeIterative returns the next proposal of an iterative run. With a
// prompt it starts the run (prompt hooks, user message); without one it
// continues from transcript history after the executor consumed every
// observation of the previous response.
func (l *AssistantAgentLoop) ProposeIterative(ctx context.Context, req AssistantProposalRequest) (AssistantProposal, error) {
	if err := l.validateReady(req); err != nil {
		return AssistantProposal{}, err
	}
	routeContext := l.proposal.RouteContext(req.SessionID, req.TurnID)
	selectedRefs := append([]string(nil), req.Scope.SelectedRefs...)
	history, err := l.contextBuilder.BuildModelHistory(ctx, req.SessionID, routeContext, selectedRefs, "")
	if err != nil {
		return AssistantProposal{}, fmt.Errorf("assistant model history: %w", err)
	}
	var injected []string
	if strings.TrimSpace(req.Prompt) != "" {
		prepared, err := l.proposal.PrepareTurn(ctx, req, countAssistantTranscriptMessages(history) == 0)
		if err != nil {
			return AssistantProposal{}, err
		}
		if prepared.Blocked != "" {
			l.publish(ctx, req, "failed", map[string]any{"phase": "user_prompt_blocked", "summary": prepared.Blocked, "error": prepared.Blocked})
			return AssistantProposal{Kind: AssistantProposalBlocked, Reason: prepared.Blocked}, nil
		}
		injected = prepared.SystemContext
		user := domain.AssistantAgentMessage{ID: req.RunID + ":user_prompt", Role: domain.AssistantAgentMessageRoleUser, Content: assistantTextBlocks(prepared.Prompt), Metadata: map[string]any{"run_id": req.RunID}}
		if err := l.appendOnce(ctx, req, user); err != nil {
			return AssistantProposal{}, err
		}
		history = ensureAssistantCurrentPrompt(history, prepared.Prompt)
	}
	messages := append(l.turnSystemContext(injected), history...)
	tools, err := l.toolSchemasFor(ctx, req.Scope)
	if err != nil {
		return AssistantProposal{}, err
	}
	done := l.iterationsSoFar(req.RunID, history)
	maxIterations := l.maxIterations()
	for {
		if done >= maxIterations {
			reason := fmt.Sprintf("max_iterations=%d", maxIterations)
			l.publish(ctx, req, "blocked", map[string]any{"phase": "loop_guard_blocked", "summary": "assistant iterative run exceeded " + reason, "error": reason})
			l.forget(req.RunID)
			return AssistantProposal{Kind: AssistantProposalBlocked, Reason: reason}, nil
		}
		done++
		l.setIterations(req.RunID, done)
		modelCtx, cancel := context.WithTimeout(ctx, l.requestTimeout())
		resp, err := l.modelClient.Next(modelCtx, llm.AgentModelRequest{Model: l.agentic.Model, Messages: messages, Tools: tools, ToolChoice: llm.AgentToolChoice{Mode: llm.AgentToolChoiceAuto}, Metadata: map[string]any{"session_id": req.SessionID, "run_id": req.RunID, "turn_id": req.TurnID, "iteration": done}}, nil)
		cancel()
		if err != nil {
			return AssistantProposal{}, fmt.Errorf("assistant model: %w", err)
		}
		if resp == nil {
			return AssistantProposal{}, errors.New("assistant model returned no response")
		}
		reply := domain.AssistantAgentMessage{ID: fmt.Sprintf("%s:model:%d", req.RunID, done), Role: domain.AssistantAgentMessageRoleAssistant, Content: append([]domain.AssistantAgentContentBlock(nil), resp.Content...), ToolCalls: append([]domain.AssistantAgentToolCall(nil), resp.ToolCalls...), Metadata: map[string]any{"run_id": req.RunID, "iteration": done, "stop_reason": string(resp.StopReason)}}
		if err := l.appendOnce(ctx, req, reply); err != nil {
			return AssistantProposal{}, err
		}
		messages = append(messages, reply)
		if len(resp.ToolCalls) > 0 {
			return AssistantProposal{Kind: AssistantProposalCalls, Calls: append([]domain.AssistantAgentToolCall(nil), resp.ToolCalls...)}, nil
		}
		if resp.StopReason == llm.AgentStopReasonMaxTokens || resp.StopReason == llm.AgentStopReasonContentGuard {
			reason := fmt.Sprintf("model stopped with %s before completion", resp.StopReason)
			l.publish(ctx, req, "blocked", map[string]any{"phase": "model_stopped_before_completion", "summary": reason, "error": reason})
			return AssistantProposal{Kind: AssistantProposalBlocked, Reason: reason}, nil
		}
		answer := assistantAgentBlocksText(resp.Content)
		if l.hooks != nil {
			stop := l.hooks.Run(ctx, AssistantHookEventStop, AssistantHookInput{SessionID: req.SessionID, Text: answer})
			if stop.Blocked || stop.Decision == AssistantHookDecisionBlock {
				reason := firstNonEmptyString(stop.Reason, "Stop hook requested the assistant continue working")
				nudge := domain.AssistantAgentMessage{ID: fmt.Sprintf("%s:stop_hook:%d", req.RunID, done), Role: domain.AssistantAgentMessageRoleUser, Content: assistantTextBlocks("Stop hook blocked completion: " + reason), Metadata: map[string]any{"run_id": req.RunID}}
				if err := l.appendOnce(ctx, req, nudge); err != nil {
					return AssistantProposal{}, err
				}
				messages = append(messages, nudge)
				l.publish(ctx, req, "executing", map[string]any{"phase": "stop_hook_blocked", "summary": reason, "iteration": done})
				continue
			}
		}
		content := map[string]any{"phase": "loop_completed", "iteration": done, "stop_reason": string(resp.StopReason)}
		if strings.TrimSpace(answer) != "" {
			// The browser renders a status summary/message as the reply.
			content["summary"] = answer
			content["message"] = answer
		}
		l.publish(ctx, req, "completed", content)
		if l.hooks != nil {
			_ = l.hooks.Run(ctx, AssistantHookEventSessionEnd, AssistantHookInput{SessionID: req.SessionID})
		}
		l.forget(req.RunID)
		return AssistantProposal{Kind: AssistantProposalFinal, Text: answer}, nil
	}
}

// toolSchemasFor advertises registered MCP tools plus internal tools, limited
// to the persisted command scope (nil = unrestricted, [] = none). Advertising
// is a convenience; the executor enforces the same scope on dispatch because
// a model can name a tool it was never offered.
func (l *AssistantAgentLoop) toolSchemasFor(ctx context.Context, scope domain.AssistantCommandScope) ([]llm.AgentToolSchema, error) {
	base, err := l.toolSchemas.AgentToolSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("assistant tool schemas: %w", err)
	}
	inScope := assistantScopeFilter(scope)
	out := make([]llm.AgentToolSchema, 0, len(base)+len(l.internalTools))
	for _, schema := range base {
		if inScope(schema.Name) {
			out = append(out, schema)
		}
	}
	for _, tool := range l.internalTools {
		if inScope(tool.Name) {
			out = append(out, assistantInternalToolSchema(tool))
		}
	}
	return out, nil
}

// turnSystemContext builds the additional system messages: the
// progressive-disclosure skill catalog and hook-provided context.
func (l *AssistantAgentLoop) turnSystemContext(extra []string) []domain.AssistantAgentMessage {
	var blocks []string
	if l.skills.Len() > 0 {
		if catalog := l.skills.Catalog(); catalog != "" {
			blocks = append(blocks, catalog)
		}
	}
	blocks = append(blocks, extra...)
	messages := make([]domain.AssistantAgentMessage, 0, len(blocks))
	for _, text := range blocks {
		if strings.TrimSpace(text) == "" {
			continue
		}
		messages = append(messages, domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleSystem, Content: assistantTextBlocks(text)})
	}
	return messages
}

// iterationsSoFar is the number of model responses already recorded for the
// run: the larger of this process's count and the transcript's durable count.
func (l *AssistantAgentLoop) iterationsSoFar(runID string, history []domain.AssistantAgentMessage) int {
	durable := 0
	for _, message := range history {
		if message.Role != domain.AssistantAgentMessageRoleAssistant || message.Metadata == nil {
			continue
		}
		if run, _ := message.Metadata["run_id"].(string); run == runID {
			durable++
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.iterations[runID] > durable {
		return l.iterations[runID]
	}
	return durable
}

// assistantIterationMemoryLimit bounds the in-process iteration floor. The
// durable transcript count remains authoritative across restarts; this floor
// only covers runs whose history outgrows the replay window.
const assistantIterationMemoryLimit = 1024

func (l *AssistantAgentLoop) setIterations(runID string, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, known := l.iterations[runID]; !known && len(l.iterations) >= assistantIterationMemoryLimit {
		l.iterations = map[string]int{}
	}
	l.iterations[runID] = n
}

func (l *AssistantAgentLoop) forget(runID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.iterations, runID)
}

func (l *AssistantAgentLoop) appendOnce(ctx context.Context, req AssistantProposalRequest, message domain.AssistantAgentMessage) error {
	if l.transcript == nil {
		return nil
	}
	if _, err := l.transcript.AppendMessageOnce(ctx, AssistantTranscriptAppend{LogicalID: message.ID, SessionID: req.SessionID, TurnID: req.TurnID, RunID: req.RunID, Message: message}); err != nil {
		return fmt.Errorf("assistant transcript append: %w", err)
	}
	return nil
}

func (l *AssistantAgentLoop) publish(ctx context.Context, req AssistantProposalRequest, status string, content map[string]any) {
	if l.status == nil {
		return
	}
	content["run_id"] = req.RunID
	content["turn_id"] = req.TurnID
	content["workflow"] = string(domain.AssistantWorkflowIterative)
	if err := l.status.PublishAssistantStatus(ctx, req.SessionID, status, content); err != nil {
		l.logger.Warn("assistant iterative status publish failed", "session_id", req.SessionID, "status", status, "error", err)
	}
}

func (l *AssistantAgentLoop) validateReady(req AssistantProposalRequest) error {
	switch {
	case l == nil:
		return errors.New("assistant iterative proposer is not configured")
	case l.modelClient == nil:
		return errors.New("assistant agent model client is not configured")
	case l.contextBuilder == nil:
		return errors.New("assistant context builder is not configured")
	case l.toolSchemas == nil:
		return errors.New("assistant tool schema provider is not configured")
	case strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.RunID) == "":
		return errors.New("assistant proposal requires session and run identity")
	}
	return nil
}

func (l *AssistantAgentLoop) maxIterations() int {
	if l != nil && l.agentic.MaxIterations > 0 {
		return l.agentic.MaxIterations
	}
	return defaultAssistantAgentLoopMaxIterations
}

func (l *AssistantAgentLoop) requestTimeout() time.Duration {
	if l != nil && l.agentic.RequestTimeout > 0 {
		return l.agentic.RequestTimeout
	}
	return assistantLLMTimeout
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

func applyAssistantHookDecision(perm domain.AssistantPermissionResult, outcome AssistantHookOutcome) domain.AssistantPermissionResult {
	var hookDecision domain.AssistantPermissionDecision
	switch outcome.Decision {
	case AssistantHookDecisionDeny:
		hookDecision = domain.AssistantPermissionDecisionDeny
	case AssistantHookDecisionAsk:
		hookDecision = domain.AssistantPermissionDecisionAsk
	case AssistantHookDecisionAllow:
		hookDecision = domain.AssistantPermissionDecisionAllow
	default:
		return perm
	}
	if assistantPermissionRank(hookDecision) > assistantPermissionRank(perm.Decision) {
		perm.Decision = hookDecision
		if strings.TrimSpace(outcome.Reason) != "" {
			perm.Reason = outcome.Reason
		}
		if perm.Metadata == nil {
			perm.Metadata = map[string]any{}
		}
		perm.Metadata["hook_decision"] = string(outcome.Decision)
	}
	return perm
}

func assistantPermissionRank(decision domain.AssistantPermissionDecision) int {
	switch decision {
	case domain.AssistantPermissionDecisionAllow:
		return 1
	case domain.AssistantPermissionDecisionAsk:
		return 2
	case domain.AssistantPermissionDecisionDeny:
		return 3
	default:
		return 0
	}
}

func mergeAssistantToolArgs(base, updated map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(updated))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range updated {
		out[k] = v
	}
	return out
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

func randomAssistantAgentLoopID(prefix string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s_%d", strings.TrimSpace(prefix), time.Now().UnixNano())
	}
	return strings.TrimSpace(prefix) + "_" + hex.EncodeToString(buf[:])
}
