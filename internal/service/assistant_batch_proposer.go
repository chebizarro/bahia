package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	assistantContextTimeout = 20 * time.Second
	assistantLLMTimeout     = 110 * time.Second
)

// AssistantChatClient is the batch planner model surface.
type AssistantChatClient interface {
	PlanFromPrompt(ctx context.Context, systemPrompt string, userPrompt string) (*domain.AssistantPlan, error)
}

// AssistantStreamingChatClient streams planner output chunks.
type AssistantStreamingChatClient interface {
	PlanFromPromptStreaming(ctx context.Context, systemPrompt, userPrompt string, onChunk func(chunk string)) (*domain.AssistantPlan, error)
}

// AssistantContextProvider assembles bounded operational context for planning.
type AssistantContextProvider interface {
	BuildContext(ctx context.Context, routeContext map[string]string, selectedRefs []string, transcriptSummary string) (string, error)
}

// AssistantBatchPlannerConfig wires the batch proposal producer.
type AssistantBatchPlannerConfig struct {
	ChatClient     AssistantChatClient
	ContextBuilder AssistantContextProvider
	Context        *AssistantProposalContext
	// History detects the first turn of a session for SessionStart hooks.
	History AssistantTranscriptHistoryProvider
	// Transcript records the operator prompt and plan summary so later turns
	// of either workflow see the conversation.
	Transcript       AssistantExecutionTranscript
	Status           AssistantStatusPublisher
	AllowedToolNames []string
	StreamingEnabled bool
	Logger           *slog.Logger
}

// AssistantBatchPlanner produces one editable batch plan per operator prompt.
// It never dispatches: approved steps run only through the executor, and the
// executor never calls the planner again after approval, so batch continuation
// makes no model request.
type AssistantBatchPlanner struct {
	chat         AssistantChatClient
	context      AssistantContextProvider
	proposal     *AssistantProposalContext
	history      AssistantTranscriptHistoryProvider
	transcript   AssistantExecutionTranscript
	status       AssistantStatusPublisher
	allowedTools map[string]struct{}
	streaming    bool
	logger       *slog.Logger
}

var _ AssistantBatchProposer = (*AssistantBatchPlanner)(nil)

func NewAssistantBatchPlanner(cfg AssistantBatchPlannerConfig) *AssistantBatchPlanner {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	allowed := make(map[string]struct{}, len(cfg.AllowedToolNames))
	for _, name := range cfg.AllowedToolNames {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = struct{}{}
		}
	}
	return &AssistantBatchPlanner{chat: cfg.ChatClient, context: cfg.ContextBuilder, proposal: cfg.Context, history: cfg.History, transcript: cfg.Transcript, status: cfg.Status, allowedTools: allowed, streaming: cfg.StreamingEnabled, logger: logger.With("component", "assistant_batch_planner")}
}

// ProposeBatch builds planning context, asks the planner model for an
// AssistantPlan and returns it as a batch proposal, a clarification or, when
// the plan has no steps, a final answer.
func (p *AssistantBatchPlanner) ProposeBatch(ctx context.Context, req AssistantProposalRequest) (AssistantProposal, error) {
	if p == nil || p.chat == nil || p.context == nil {
		return AssistantProposal{}, errors.New("assistant batch planner is not configured")
	}
	firstTurn := false
	if p.history != nil {
		if history, err := p.history.BuildModelHistory(ctx, req.SessionID, 1); err == nil {
			firstTurn = len(history) == 0
		}
	}
	prepared, err := p.proposal.PrepareTurn(ctx, req, firstTurn)
	if err != nil {
		return AssistantProposal{}, err
	}
	if prepared.Blocked != "" {
		p.publish(ctx, req, "failed", map[string]any{"phase": "user_prompt_blocked", "summary": prepared.Blocked, "error": prepared.Blocked})
		return AssistantProposal{Kind: AssistantProposalBlocked, Reason: prepared.Blocked}, nil
	}
	routeContext := cloneAssistantLoopStringMap(prepared.RouteContext)
	if routeContext == nil {
		routeContext = map[string]string{}
	}
	// The session id lets the context builder replay transcript history.
	routeContext["session_id"] = req.SessionID
	contextCtx, cancelContext := context.WithTimeout(ctx, assistantContextTimeout)
	contextBlock, err := p.context.BuildContext(contextCtx, routeContext, prepared.SelectedRefs, "")
	cancelContext()
	if err != nil {
		p.publish(ctx, req, "failed", map[string]any{"phase": "context_error", "summary": "failed to build assistant context", "error": err.Error()})
		return AssistantProposal{}, fmt.Errorf("assistant context: %w", err)
	}
	if len(prepared.SystemContext) > 0 {
		contextBlock = strings.Join(prepared.SystemContext, "\n\n") + "\n\n" + contextBlock
	}
	llmCtx, cancelLLM := context.WithTimeout(ctx, assistantLLMTimeout)
	plan, err := p.plan(llmCtx, req, p.systemPrompt(req.Scope), assistantBatchUserPrompt(prepared, contextBlock))
	cancelLLM()
	if err != nil {
		p.publish(ctx, req, "failed", map[string]any{"phase": "llm_error", "summary": "assistant planning failed", "error": err.Error()})
		return AssistantProposal{}, fmt.Errorf("assistant planning: %w", err)
	}
	if plan == nil {
		plan = &domain.AssistantPlan{Summary: "The assistant did not return a plan.", NeedsClarification: true, ClarifyingQuestion: "Please restate the request with explicit target resources and desired action.", RiskLevel: "low", Steps: []domain.AssistantPlanStep{}}
	}
	if err := p.validatePlan(*plan); err != nil {
		p.publish(ctx, req, "failed", map[string]any{"phase": "plan_validation_error", "summary": "assistant plan failed validation", "error": err.Error()})
		return AssistantProposal{Kind: AssistantProposalBlocked, Reason: "plan_validation_error: " + err.Error()}, nil
	}
	p.record(ctx, req, prepared.Prompt, *plan)
	switch {
	case plan.NeedsClarification:
		p.publish(ctx, req, "needs_clarification", map[string]any{"phase": "needs_clarification", "summary": plan.Summary, "clarifying_question": plan.ClarifyingQuestion})
		return AssistantProposal{Kind: AssistantProposalClarification, Text: firstNonEmptyString(plan.ClarifyingQuestion, plan.Summary)}, nil
	case len(plan.Steps) == 0:
		p.publish(ctx, req, "completed", map[string]any{"phase": "completed", "summary": plan.Summary, "message": plan.Summary})
		return AssistantProposal{Kind: AssistantProposalFinal, Text: plan.Summary}, nil
	}
	return AssistantProposal{Kind: AssistantProposalBatch, Batch: plan}, nil
}

func (p *AssistantBatchPlanner) plan(ctx context.Context, req AssistantProposalRequest, systemPrompt, userPrompt string) (*domain.AssistantPlan, error) {
	streamingClient, ok := p.chat.(AssistantStreamingChatClient)
	if !ok || !p.streaming {
		return p.chat.PlanFromPrompt(ctx, systemPrompt, userPrompt)
	}
	var pending strings.Builder
	lastPublished := time.Now()
	flush := func(force bool) {
		chunk := pending.String()
		if chunk == "" || (!force && time.Since(lastPublished) < 200*time.Millisecond && len(chunk) < 50) {
			return
		}
		pending.Reset()
		lastPublished = time.Now()
		p.publish(ctx, req, "planning", map[string]any{"phase": "planning", "streaming": true, "chunk": chunk})
	}
	plan, err := streamingClient.PlanFromPromptStreaming(ctx, systemPrompt, userPrompt, func(chunk string) {
		pending.WriteString(chunk)
		flush(false)
	})
	flush(true)
	return plan, err
}

// record appends the operator prompt and the plan summary to the transcript
// once per run. Batch history is advisory context, so an append failure is
// logged and does not block the proposal.
func (p *AssistantBatchPlanner) record(ctx context.Context, req AssistantProposalRequest, prompt string, plan domain.AssistantPlan) {
	if p.transcript == nil {
		return
	}
	entries := []domain.AssistantAgentMessage{
		{ID: req.RunID + ":user_prompt", Role: domain.AssistantAgentMessageRoleUser, Content: assistantTextBlocks(prompt), Metadata: map[string]any{"run_id": req.RunID, "workflow": string(domain.AssistantWorkflowBatch)}},
		{ID: req.RunID + ":batch_plan", Role: domain.AssistantAgentMessageRoleAssistant, Content: assistantTextBlocks(firstNonEmptyString(plan.ClarifyingQuestion, plan.Summary)), Metadata: map[string]any{"run_id": req.RunID, "workflow": string(domain.AssistantWorkflowBatch), "steps": len(plan.Steps)}},
	}
	for _, message := range entries {
		if len(message.Content) == 0 {
			continue
		}
		if _, err := p.transcript.AppendMessageOnce(ctx, AssistantTranscriptAppend{LogicalID: message.ID, SessionID: req.SessionID, TurnID: req.TurnID, RunID: req.RunID, Message: message}); err != nil {
			p.logger.Warn("assistant batch transcript append failed", "session_id", req.SessionID, "run_id", req.RunID, "error", err)
			return
		}
	}
}

func (p *AssistantBatchPlanner) publish(ctx context.Context, req AssistantProposalRequest, status string, content map[string]any) {
	if p.status == nil {
		return
	}
	content["run_id"] = req.RunID
	content["turn_id"] = req.TurnID
	content["workflow"] = string(domain.AssistantWorkflowBatch)
	if err := p.status.PublishAssistantStatus(ctx, req.SessionID, status, content); err != nil {
		p.logger.Warn("assistant batch status publish failed", "session_id", req.SessionID, "status", status, "error", err)
	}
}

func (p *AssistantBatchPlanner) validatePlan(plan domain.AssistantPlan) error {
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
		if _, ok := p.allowedTools[step.ToolName]; !ok {
			return fmt.Errorf("assistant tool %q is not allowlisted", step.ToolName)
		}
	}
	return nil
}

// systemPrompt advertises only catalog tools inside the persisted command
// scope; the executor enforces the same scope when the plan is approved.
func (p *AssistantBatchPlanner) systemPrompt(scope domain.AssistantCommandScope) string {
	inScope := assistantScopeFilter(scope)
	names := make([]string, 0, len(p.allowedTools))
	for name := range p.allowedTools {
		if inScope(name) {
			names = append(names, name)
		}
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
		"When creating zones or policies, infer zone name from existing DNS Zones context. The executor issues idempotency keys; do not invent them.\n\n" +
		"If a target resource or intended action is ambiguous, set needs_clarification=true and produce no steps. Never include secrets."
}

func assistantBatchUserPrompt(prepared AssistantPreparedTurn, contextBlock string) string {
	payload, _ := json.MarshalIndent(map[string]any{"operator_prompt": prepared.Prompt, "route_context": prepared.RouteContext, "selected_refs": prepared.SelectedRefs, "operational_context": contextBlock}, "", "  ")
	return string(payload)
}
