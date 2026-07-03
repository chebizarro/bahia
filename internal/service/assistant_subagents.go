package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

// assistantDelegateSubagentToolName is the internal, service-owned tool the
// model calls to hand a scoped task to a configured subagent. It is registered
// through the distinct internal-tool path in the agent loop, never through the
// external MCP registry merge path.
const assistantDelegateSubagentToolName = "bahia_assistant_delegate_subagent"

// defaultAssistantSubagentMaxIterations bounds a delegated child loop so a
// subagent cannot run away independently of the parent turn's guards.
const defaultAssistantSubagentMaxIterations = 6

// assistantInternalTool is a service-owned tool executed directly by the agent
// loop instead of routed to the MCP server. Internal tools (subagent delegation,
// skill loading) are read-only from the perspective of the external control
// plane: they never publish async mutation intents themselves.
type assistantInternalTool struct {
	name        string
	description string
	inputSchema map[string]any
	effect      domain.AssistantToolEffect
	risk        domain.AssistantToolRisk
	handler     func(ctx context.Context, run assistantAgentLoopRun, call domain.AssistantAgentToolCall) (*domain.AssistantToolObservation, error)
}

func (t *assistantInternalTool) schema() llm.AgentToolSchema {
	return llm.AgentToolSchema{
		Name:        t.name,
		Description: t.description,
		InputSchema: t.inputSchema,
		Metadata:    map[string]any{"internal": true, "effect": string(t.effect), "risk": string(t.risk)},
	}
}

// AssistantSubagentSpec is one markdown+frontmatter subagent definition using
// the claude-code agent convention (name/description/model/tools frontmatter,
// markdown body as the child system prompt).
type AssistantSubagentSpec struct {
	Name         string
	Description  string
	Model        string
	Tools        []string
	SystemPrompt string
	SourcePath   string
}

// AssistantSubagentLibrary is the loaded, name-indexed set of subagent specs.
type AssistantSubagentLibrary struct {
	order  []string
	byName map[string]AssistantSubagentSpec
}

// Len returns the number of loaded subagents.
func (lib *AssistantSubagentLibrary) Len() int {
	if lib == nil {
		return 0
	}
	return len(lib.byName)
}

// Get returns a subagent spec by name.
func (lib *AssistantSubagentLibrary) Get(name string) (AssistantSubagentSpec, bool) {
	if lib == nil {
		return AssistantSubagentSpec{}, false
	}
	spec, ok := lib.byName[strings.TrimSpace(name)]
	return spec, ok
}

// Specs returns loaded subagents in deterministic load order.
func (lib *AssistantSubagentLibrary) Specs() []AssistantSubagentSpec {
	if lib == nil {
		return nil
	}
	out := make([]AssistantSubagentSpec, 0, len(lib.order))
	for _, name := range lib.order {
		out = append(out, lib.byName[name])
	}
	return out
}

// LoadAssistantSubagents parses every *.md subagent definition under the
// configured roots. A malformed frontmatter block, a missing required field, or
// a duplicate name is a hard error so a misconfigured extension fails closed.
func LoadAssistantSubagents(roots []string) (*AssistantSubagentLibrary, error) {
	lib := &AssistantSubagentLibrary{byName: map[string]AssistantSubagentSpec{}}
	files, err := assistantMarkdownFiles(roots)
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read assistant subagent %q: %w", path, err)
		}
		spec, err := ParseAssistantSubagent(string(content), path)
		if err != nil {
			return nil, err
		}
		if _, dup := lib.byName[spec.Name]; dup {
			return nil, fmt.Errorf("duplicate assistant subagent name %q (%s)", spec.Name, path)
		}
		lib.byName[spec.Name] = spec
		lib.order = append(lib.order, spec.Name)
	}
	return lib, nil
}

// ParseAssistantSubagent parses one subagent markdown document. The frontmatter
// must carry name and description; model and tools are optional.
func ParseAssistantSubagent(content, sourcePath string) (AssistantSubagentSpec, error) {
	frontmatter, body, ok := assistantSplitFrontmatter(content)
	if !ok {
		return AssistantSubagentSpec{}, fmt.Errorf("assistant subagent %q is missing a yaml frontmatter block", assistantSourceLabel(sourcePath))
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Model       string `yaml:"model"`
		Tools       any    `yaml:"tools"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return AssistantSubagentSpec{}, fmt.Errorf("parse assistant subagent frontmatter %q: %w", assistantSourceLabel(sourcePath), err)
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return AssistantSubagentSpec{}, fmt.Errorf("assistant subagent %q frontmatter is missing required field name", assistantSourceLabel(sourcePath))
	}
	description := strings.TrimSpace(fm.Description)
	if description == "" {
		return AssistantSubagentSpec{}, fmt.Errorf("assistant subagent %q frontmatter is missing required field description", assistantSourceLabel(sourcePath))
	}
	prompt := strings.TrimSpace(body)
	if prompt == "" {
		return AssistantSubagentSpec{}, fmt.Errorf("assistant subagent %q has an empty markdown body (system prompt)", assistantSourceLabel(sourcePath))
	}
	return AssistantSubagentSpec{
		Name:         name,
		Description:  description,
		Model:        strings.TrimSpace(fm.Model),
		Tools:        assistantNormalizeStringList(fm.Tools),
		SystemPrompt: prompt,
		SourcePath:   sourcePath,
	}, nil
}

// buildDelegateSubagentTool constructs the internal delegation tool bound to the
// loaded subagent library and this loop's model/tool-runtime dependencies.
func (l *AssistantAgentLoop) buildDelegateSubagentTool() *assistantInternalTool {
	return &assistantInternalTool{
		name:        assistantDelegateSubagentToolName,
		description: "Delegate a scoped sub-task to a configured Bahia subagent and return its synchronous result. Provide the subagent name and the task to hand off.",
		effect:      domain.AssistantToolEffectRead,
		risk:        domain.AssistantToolRiskLow,
		inputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subagent": map[string]any{"type": "string", "description": "Name of the configured subagent to delegate to."},
				"task":     map[string]any{"type": "string", "description": "The task or question to hand to the subagent."},
				"context":  map[string]any{"type": "string", "description": "Optional transcript excerpts or extra context for the subagent."},
			},
			"required": []any{"subagent", "task"},
		},
		handler: l.runDelegateSubagent,
	}
}

func (l *AssistantAgentLoop) runDelegateSubagent(ctx context.Context, run assistantAgentLoopRun, call domain.AssistantAgentToolCall) (*domain.AssistantToolObservation, error) {
	if run.depth > 0 {
		return l.internalToolObservation(call, domain.AssistantToolObservationDenied, "nested subagent delegation is not permitted", nil), nil
	}
	name := strings.TrimSpace(stringFromAnyMapAny(call.Arguments, "subagent"))
	task := strings.TrimSpace(stringFromAnyMapAny(call.Arguments, "task"))
	if name == "" || task == "" {
		return l.internalToolObservation(call, domain.AssistantToolObservationFailed, "delegate_subagent requires subagent and task arguments", nil), nil
	}
	spec, ok := l.subagents.Get(name)
	if !ok {
		return l.internalToolObservation(call, domain.AssistantToolObservationFailed, fmt.Sprintf("unknown subagent %q", name), nil), nil
	}
	extra := strings.TrimSpace(stringFromAnyMapAny(call.Arguments, "context"))
	result, err := l.runSubagentLoop(ctx, run, spec, task, extra)
	if err != nil {
		return l.internalToolObservation(call, domain.AssistantToolObservationFailed, err.Error(), nil), nil
	}

	// SubagentStop hooks gate acceptance of the child result before it is fed
	// back to the parent model as an observation.
	if l.hooks != nil {
		outcome := l.hooks.Run(ctx, AssistantHookEventSubagentStop, AssistantHookInput{
			SessionID: run.session.SessionID,
			ToolName:  assistantDelegateSubagentToolName,
			Text:      result.text,
			Extra:     map[string]any{"subagent": spec.Name, "iterations": result.iterations},
		})
		if outcome.Blocked || outcome.Decision == AssistantHookDecisionBlock || outcome.Decision == AssistantHookDecisionDeny {
			reason := firstNonEmptyString(outcome.Reason, "SubagentStop hook blocked the subagent result")
			return l.internalToolObservation(call, domain.AssistantToolObservationDenied, reason, map[string]any{"subagent": spec.Name, "subagent_stop_blocked": true}), nil
		}
	}

	obs := l.internalToolObservation(call, domain.AssistantToolObservationSucceeded, result.text, map[string]any{
		"subagent":            spec.Name,
		"subagent_iterations": result.iterations,
		"subagent_tool_calls": result.toolCalls,
	})
	obs.Result = map[string]any{"subagent": spec.Name, "result": result.text, "iterations": result.iterations}
	return obs, nil
}

type assistantSubagentResult struct {
	text       string
	iterations int
	toolCalls  int
}

func (l *AssistantAgentLoop) runSubagentLoop(ctx context.Context, parent assistantAgentLoopRun, spec AssistantSubagentSpec, task, extra string) (assistantSubagentResult, error) {
	parentTools, err := l.toolSchemas.AgentToolSchemas(ctx)
	if err != nil {
		return assistantSubagentResult{}, fmt.Errorf("load subagent tool schemas: %w", err)
	}
	childTools := filterAssistantSubagentSchemas(parentTools, spec.Tools)
	childRuntime := l.toolRuntime.WithoutSessionEffects()
	childSession := cloneAssistantSubagentSession(parent.session, spec.Name)

	userText := task
	if extra != "" {
		userText = task + "\n\nContext:\n" + extra
	}
	messages := []domain.AssistantAgentMessage{
		{Role: domain.AssistantAgentMessageRoleSystem, Content: assistantTextBlocks(spec.SystemPrompt)},
		{Role: domain.AssistantAgentMessageRoleUser, Content: assistantTextBlocks(userText)},
	}

	model := firstNonEmptyString(spec.Model, l.agentic.Model)
	allowed := assistantStringSet(spec.Tools)
	result := assistantSubagentResult{}
	for i := 0; i < defaultAssistantSubagentMaxIterations; i++ {
		result.iterations++
		resp, err := l.modelClient.Next(ctx, llm.AgentModelRequest{
			Model:      model,
			Messages:   messages,
			Tools:      childTools,
			ToolChoice: llm.AgentToolChoice{Mode: llm.AgentToolChoiceAuto},
			Metadata:   map[string]any{"session_id": childSession.SessionID, "subagent": spec.Name},
		}, nil)
		if err != nil {
			return result, fmt.Errorf("subagent %q model error: %w", spec.Name, err)
		}
		messages = append(messages, domain.AssistantAgentMessage{
			Role:      domain.AssistantAgentMessageRoleAssistant,
			Content:   append([]domain.AssistantAgentContentBlock(nil), resp.Content...),
			ToolCalls: append([]domain.AssistantAgentToolCall(nil), resp.ToolCalls...),
		})
		if len(resp.ToolCalls) == 0 {
			result.text = assistantAgentBlocksText(resp.Content)
			return result, nil
		}
		for _, toolCall := range resp.ToolCalls {
			result.toolCalls++
			obs := l.executeSubagentToolCall(ctx, childRuntime, childSession, allowed, toolCall)
			messages = append(messages, assistantToolObservationMessage(obs))
		}
	}
	result.text = firstNonEmptyString(assistantLastAssistantText(messages), fmt.Sprintf("subagent %q reached its iteration budget without a final answer", spec.Name))
	return result, nil
}

func (l *AssistantAgentLoop) executeSubagentToolCall(ctx context.Context, childRuntime *AssistantToolRuntime, childSession *domain.AssistantSession, allowed map[string]bool, toolCall domain.AssistantAgentToolCall) *domain.AssistantToolObservation {
	name := strings.TrimSpace(toolCall.Name)
	if name == assistantDelegateSubagentToolName {
		return l.internalToolObservation(toolCall, domain.AssistantToolObservationDenied, "nested subagent delegation is not permitted", nil)
	}
	if allowed != nil && !allowed[name] {
		return l.internalToolObservation(toolCall, domain.AssistantToolObservationDenied, fmt.Sprintf("tool %q is not in this subagent's allowed tool set", name), nil)
	}
	descriptor, ok := l.toolRuntime.AgentToolDescriptor(name)
	if !ok {
		return l.internalToolObservation(toolCall, domain.AssistantToolObservationDenied, fmt.Sprintf("tool %q is not registered for agent use", name), nil)
	}
	if descriptor.ExecutionMode == domain.AssistantToolExecutionModeAsync {
		return l.internalToolObservation(toolCall, domain.AssistantToolObservationDenied, fmt.Sprintf("subagents cannot execute async tool %q; it must run in the parent turn", name), nil)
	}
	obs, err := childRuntime.Execute(ctx, AssistantToolRuntimeRequest{Session: childSession, RunID: "subagent", ToolCall: toolCall})
	if err != nil {
		return l.internalToolObservation(toolCall, domain.AssistantToolObservationFailed, err.Error(), nil)
	}
	if obs == nil {
		return l.internalToolObservation(toolCall, domain.AssistantToolObservationFailed, "subagent tool runtime returned no observation", nil)
	}
	if obs.Status == domain.AssistantToolObservationDeferred || obs.Status == domain.AssistantToolObservationWaitingAsync {
		return l.internalToolObservation(toolCall, domain.AssistantToolObservationDenied, fmt.Sprintf("tool %q requires approval or async execution and is unavailable to subagents", name), nil)
	}
	return obs
}

func (l *AssistantAgentLoop) internalToolObservation(call domain.AssistantAgentToolCall, status domain.AssistantToolObservationStatus, summary string, metadata map[string]any) *domain.AssistantToolObservation {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["internal_tool"] = true
	obs := &domain.AssistantToolObservation{
		ObservationID: l.newID("obs"),
		ToolCallID:    call.ID,
		ToolName:      call.Name,
		Status:        status,
		Effect:        domain.AssistantToolEffectRead,
		Risk:          domain.AssistantToolRiskLow,
		ExecutionMode: domain.AssistantToolExecutionModeSync,
		Summary:       summary,
		ObservedAt:    l.now().UTC(),
		Metadata:      metadata,
	}
	if status == domain.AssistantToolObservationFailed || status == domain.AssistantToolObservationDenied {
		obs.Error = summary
	}
	if status == domain.AssistantToolObservationSucceeded && strings.TrimSpace(summary) != "" {
		obs.Content = assistantTextBlocks(summary)
	}
	return obs
}

func cloneAssistantSubagentSession(parent *domain.AssistantSession, name string) *domain.AssistantSession {
	child := &domain.AssistantSession{
		SessionID:      parent.SessionID + ":subagent:" + name,
		State:          domain.AssistantSessionStateExecuting,
		OperatorPubkey: parent.OperatorPubkey,
		Participants:   append([]string(nil), parent.Participants...),
		AssistantID:    parent.AssistantID,
		Metadata:       map[string]any{},
	}
	return child
}

func filterAssistantSubagentSchemas(all []llm.AgentToolSchema, allowed []string) []llm.AgentToolSchema {
	out := make([]llm.AgentToolSchema, 0, len(all))
	set := assistantStringSet(allowed)
	for _, schema := range all {
		if schema.Name == assistantDelegateSubagentToolName {
			continue
		}
		if set != nil && !set[schema.Name] {
			continue
		}
		out = append(out, schema)
	}
	return out
}

// assistantMarkdownFiles returns every *.md file under the given roots in
// deterministic order. Missing roots are skipped so an operator can configure a
// directory before populating it.
func assistantMarkdownFiles(roots []string) ([]string, error) {
	var files []string
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat assistant extension root %q: %w", root, err)
		}
		if !info.IsDir() {
			if strings.EqualFold(filepath.Ext(root), ".md") {
				files = append(files, root)
			}
			continue
		}
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".md") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk assistant extension root %q: %w", root, err)
		}
	}
	sort.Strings(files)
	return files, nil
}

// assistantSplitFrontmatter splits a document into its yaml frontmatter block and
// markdown body. ok is false when no leading `---` fenced frontmatter is present.
func assistantSplitFrontmatter(content string) (string, string, bool) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.TrimPrefix(normalized, "\uFEFF")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", normalized, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			frontmatter := strings.Join(lines[1:i], "\n")
			body := ""
			if i+1 < len(lines) {
				body = strings.Join(lines[i+1:], "\n")
			}
			return frontmatter, strings.TrimLeft(body, "\n"), true
		}
	}
	return "", normalized, false
}

func assistantNormalizeStringList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return splitAssistantCommaList(typed)
	case []string:
		return normalizeAssistantStringSet(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, strings.TrimSpace(fmt.Sprintf("%v", item)))
		}
		return normalizeAssistantStringSet(parts)
	default:
		return splitAssistantCommaList(fmt.Sprintf("%v", typed))
	}
}

func splitAssistantCommaList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return normalizeAssistantStringSet(strings.Split(value, ","))
}

func assistantStringSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			set[trimmed] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func assistantTextBlocks(text string) []domain.AssistantAgentContentBlock {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: text}}
}

func assistantAgentBlocksText(blocks []domain.AssistantAgentContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == domain.AssistantAgentContentText && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func assistantLastAssistantText(messages []domain.AssistantAgentMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == domain.AssistantAgentMessageRoleAssistant {
			if text := assistantAgentBlocksText(messages[i].Content); text != "" {
				return text
			}
		}
	}
	return ""
}

func assistantSourceLabel(sourcePath string) string {
	if strings.TrimSpace(sourcePath) == "" {
		return "<inline>"
	}
	return sourcePath
}

func stringFromAnyMapAny(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if raw, ok := values[key]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", values[key])
	}
	return ""
}
