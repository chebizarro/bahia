package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// AssistantHookEvent is a lifecycle point at which operator-configured hooks run.
type AssistantHookEvent string

const (
	AssistantHookEventUserPromptSubmit AssistantHookEvent = "UserPromptSubmit"
	AssistantHookEventSessionStart     AssistantHookEvent = "SessionStart"
	AssistantHookEventSessionEnd       AssistantHookEvent = "SessionEnd"
	AssistantHookEventPreToolUse       AssistantHookEvent = "PreToolUse"
	AssistantHookEventPostToolUse      AssistantHookEvent = "PostToolUse"
	AssistantHookEventStop             AssistantHookEvent = "Stop"
	AssistantHookEventSubagentStop     AssistantHookEvent = "SubagentStop"
)

func assistantHookEvents() []AssistantHookEvent {
	return []AssistantHookEvent{
		AssistantHookEventUserPromptSubmit,
		AssistantHookEventSessionStart,
		AssistantHookEventSessionEnd,
		AssistantHookEventPreToolUse,
		AssistantHookEventPostToolUse,
		AssistantHookEventStop,
		AssistantHookEventSubagentStop,
	}
}

// AssistantHookHandlerType selects how a hook handler is evaluated. Only the
// read-only prompt and mcp-tool handler types are supported; arbitrary shell is
// intentionally excluded until a sandboxed runner exists.
type AssistantHookHandlerType string

const (
	AssistantHookHandlerPrompt  AssistantHookHandlerType = "prompt"
	AssistantHookHandlerMCPTool AssistantHookHandlerType = "mcp-tool"
)

// AssistantHookDecision is the normalized decision a hook can return. allow/ask/
// deny gate tool use; block halts a Stop/SubagentStop from terminating.
type AssistantHookDecision string

const (
	AssistantHookDecisionNone  AssistantHookDecision = ""
	AssistantHookDecisionAllow AssistantHookDecision = "allow"
	AssistantHookDecisionAsk   AssistantHookDecision = "ask"
	AssistantHookDecisionDeny  AssistantHookDecision = "deny"
	AssistantHookDecisionBlock AssistantHookDecision = "block"
)

// AssistantHookHandler is one evaluatable hook step.
type AssistantHookHandler struct {
	Type   AssistantHookHandlerType
	Prompt string
	Tool   string
	Args   map[string]any
}

// AssistantHookMatcher scopes a set of handlers to matching subjects (tool names
// for tool events, free text otherwise). An empty or "*" matcher matches all.
type AssistantHookMatcher struct {
	Matcher  string
	Handlers []AssistantHookHandler
}

// AssistantHookSet is the parsed, event-indexed hook configuration.
type AssistantHookSet struct {
	byEvent map[AssistantHookEvent][]AssistantHookMatcher
}

// Len returns the total number of matcher groups across all events.
func (s AssistantHookSet) Len() int {
	total := 0
	for _, matchers := range s.byEvent {
		total += len(matchers)
	}
	return total
}

// AssistantHookInput carries the subject and payload a hook evaluates.
type AssistantHookInput struct {
	SessionID string
	ToolName  string
	ToolArgs  map[string]any
	Text      string
	Extra     map[string]any
}

// AssistantHookOutcome is the normalized, aggregated result of running hooks for
// one event.
type AssistantHookOutcome struct {
	Decision          AssistantHookDecision
	Reason            string
	UpdatedInput      map[string]any
	AdditionalContext string
	SystemMessage     string
	Blocked           bool
}

// AssistantHookPromptRequest is passed to a prompt-hook evaluator.
type AssistantHookPromptRequest struct {
	Event  AssistantHookEvent
	Prompt string
	Input  AssistantHookInput
}

// AssistantHookPromptEvaluator evaluates a prompt-type hook. Implementations use
// the model to judge the hook prompt against the input. When unset, prompt hooks
// are skipped (they can never upgrade a decision to allow).
type AssistantHookPromptEvaluator interface {
	EvaluateHookPrompt(ctx context.Context, req AssistantHookPromptRequest) (AssistantHookOutcome, error)
}

// AssistantHookMCPCaller runs a read-only MCP tool for an mcp-tool hook. When
// unset, mcp-tool hooks are skipped.
type AssistantHookMCPCaller interface {
	CallReadOnlyTool(ctx context.Context, name string, args map[string]any) (map[string]any, error)
}

// AssistantHookRunner evaluates configured hooks. Security model: the runner is
// additive-and-restrictive only. It can deny, ask, or block, and can inject
// context, but it can never turn a denial into an allow.
type AssistantHookRunner struct {
	set    AssistantHookSet
	prompt AssistantHookPromptEvaluator
	mcp    AssistantHookMCPCaller
	now    func() time.Time
}

// AssistantHookRunnerConfig wires the hook runner's evaluators.
type AssistantHookRunnerConfig struct {
	Set    AssistantHookSet
	Prompt AssistantHookPromptEvaluator
	MCP    AssistantHookMCPCaller
	Now    func() time.Time
}

// NewAssistantHookRunner builds a hook runner from a parsed hook set.
func NewAssistantHookRunner(cfg AssistantHookRunnerConfig) *AssistantHookRunner {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &AssistantHookRunner{set: cfg.Set, prompt: cfg.Prompt, mcp: cfg.MCP, now: now}
}

// Run evaluates every matching handler for the event and folds their outcomes
// into one aggregate decision (most restrictive wins).
func (r *AssistantHookRunner) Run(ctx context.Context, event AssistantHookEvent, input AssistantHookInput) AssistantHookOutcome {
	aggregate := AssistantHookOutcome{}
	if r == nil {
		return aggregate
	}
	subject := input.ToolName
	if subject == "" {
		subject = input.Text
	}
	for _, matcher := range r.set.byEvent[event] {
		if !assistantHookMatcherMatches(matcher.Matcher, subject) {
			continue
		}
		for _, handler := range matcher.Handlers {
			outcome, ok := r.evaluate(ctx, event, handler, input)
			if !ok {
				continue
			}
			aggregate = foldAssistantHookOutcome(aggregate, outcome)
		}
	}
	return aggregate
}

func (r *AssistantHookRunner) evaluate(ctx context.Context, event AssistantHookEvent, handler AssistantHookHandler, input AssistantHookInput) (AssistantHookOutcome, bool) {
	switch handler.Type {
	case AssistantHookHandlerPrompt:
		if r.prompt == nil {
			return AssistantHookOutcome{}, false
		}
		outcome, err := r.prompt.EvaluateHookPrompt(ctx, AssistantHookPromptRequest{Event: event, Prompt: handler.Prompt, Input: input})
		if err != nil {
			return AssistantHookOutcome{}, false
		}
		return outcome, true
	case AssistantHookHandlerMCPTool:
		if r.mcp == nil || strings.TrimSpace(handler.Tool) == "" {
			return AssistantHookOutcome{}, false
		}
		args := mergeAssistantHookArgs(handler.Args, input)
		result, err := r.mcp.CallReadOnlyTool(ctx, handler.Tool, args)
		if err != nil {
			return AssistantHookOutcome{}, false
		}
		return assistantHookOutcomeFromMap(result), true
	default:
		return AssistantHookOutcome{}, false
	}
}

func mergeAssistantHookArgs(base map[string]any, input AssistantHookInput) map[string]any {
	args := map[string]any{}
	for k, v := range base {
		args[k] = v
	}
	if _, ok := args["tool_name"]; !ok && input.ToolName != "" {
		args["tool_name"] = input.ToolName
	}
	if _, ok := args["tool_args"]; !ok && input.ToolArgs != nil {
		args["tool_args"] = input.ToolArgs
	}
	if _, ok := args["session_id"]; !ok && input.SessionID != "" {
		args["session_id"] = input.SessionID
	}
	return args
}

// foldAssistantHookOutcome combines two outcomes, keeping the most restrictive
// gate decision and any block, and concatenating context.
func foldAssistantHookOutcome(acc, next AssistantHookOutcome) AssistantHookOutcome {
	if assistantHookDecisionRank(next.Decision) > assistantHookDecisionRank(acc.Decision) {
		acc.Decision = next.Decision
		if strings.TrimSpace(next.Reason) != "" {
			acc.Reason = next.Reason
		}
	}
	if next.Blocked || next.Decision == AssistantHookDecisionBlock {
		acc.Blocked = true
		if strings.TrimSpace(acc.Reason) == "" {
			acc.Reason = next.Reason
		}
	}
	if next.UpdatedInput != nil {
		if acc.UpdatedInput == nil {
			acc.UpdatedInput = map[string]any{}
		}
		for k, v := range next.UpdatedInput {
			acc.UpdatedInput[k] = v
		}
	}
	acc.AdditionalContext = joinAssistantHookText(acc.AdditionalContext, next.AdditionalContext)
	acc.SystemMessage = joinAssistantHookText(acc.SystemMessage, next.SystemMessage)
	return acc
}

func assistantHookDecisionRank(decision AssistantHookDecision) int {
	switch decision {
	case AssistantHookDecisionAllow:
		return 1
	case AssistantHookDecisionAsk:
		return 2
	case AssistantHookDecisionBlock:
		return 3
	case AssistantHookDecisionDeny:
		return 4
	default:
		return 0
	}
}

func joinAssistantHookText(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	switch {
	case existing == "":
		return next
	case next == "":
		return existing
	default:
		return existing + "\n" + next
	}
}

func assistantHookMatcherMatches(matcher, subject string) bool {
	matcher = strings.TrimSpace(matcher)
	if matcher == "" || matcher == "*" {
		return true
	}
	if re, err := regexp.Compile(matcher); err == nil {
		return re.MatchString(subject)
	}
	return strings.Contains(subject, matcher)
}

func assistantHookOutcomeFromMap(result map[string]any) AssistantHookOutcome {
	outcome := AssistantHookOutcome{}
	if result == nil {
		return outcome
	}
	specific := result
	if nested, ok := result["hookSpecificOutput"].(map[string]any); ok {
		specific = nested
	}
	decision := firstNonEmptyString(
		assistantHookStringField(specific, "permissionDecision"),
		assistantHookStringField(result, "permissionDecision"),
		assistantHookStringField(result, "decision"),
	)
	outcome.Decision = normalizeAssistantHookDecision(decision)
	outcome.Reason = firstNonEmptyString(assistantHookStringField(result, "reason"), assistantHookStringField(specific, "reason"))
	outcome.SystemMessage = assistantHookStringField(result, "systemMessage")
	outcome.AdditionalContext = firstNonEmptyString(assistantHookStringField(specific, "additionalContext"), assistantHookStringField(result, "additionalContext"))
	if updated, ok := specific["updatedInput"].(map[string]any); ok {
		outcome.UpdatedInput = updated
	} else if updated, ok := result["updatedInput"].(map[string]any); ok {
		outcome.UpdatedInput = updated
	}
	if blocked, ok := result["block"].(bool); ok && blocked {
		outcome.Blocked = true
	}
	if blocked, ok := result["blocked"].(bool); ok && blocked {
		outcome.Blocked = true
	}
	if outcome.Decision == AssistantHookDecisionBlock {
		outcome.Blocked = true
	}
	return outcome
}

func assistantHookStringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if raw, ok := m[key]; ok {
		if s, ok := raw.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func normalizeAssistantHookDecision(value string) AssistantHookDecision {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "allow", "approve", "approved":
		return AssistantHookDecisionAllow
	case "ask", "confirm":
		return AssistantHookDecisionAsk
	case "deny", "denied", "reject", "rejected":
		return AssistantHookDecisionDeny
	case "block":
		return AssistantHookDecisionBlock
	default:
		return AssistantHookDecisionNone
	}
}

// LoadAssistantHooks parses every *.json hook file under the configured roots
// and merges them into a single hook set. A malformed file or an unsupported
// handler type fails closed.
func LoadAssistantHooks(roots []string) (AssistantHookSet, error) {
	set := AssistantHookSet{byEvent: map[AssistantHookEvent][]AssistantHookMatcher{}}
	files, err := assistantJSONFiles(roots)
	if err != nil {
		return set, err
	}
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return set, fmt.Errorf("read assistant hook file %q: %w", path, err)
		}
		if err := parseAssistantHookDocument(content, path, &set); err != nil {
			return set, err
		}
	}
	return set, nil
}

// ParseAssistantHookDocument parses a single JSON hook document. Exposed for
// unit tests.
func ParseAssistantHookDocument(content []byte, sourcePath string) (AssistantHookSet, error) {
	set := AssistantHookSet{byEvent: map[AssistantHookEvent][]AssistantHookMatcher{}}
	if err := parseAssistantHookDocument(content, sourcePath, &set); err != nil {
		return AssistantHookSet{}, err
	}
	return set, nil
}

type assistantRawHookHandler struct {
	Type   string         `json:"type"`
	Prompt string         `json:"prompt"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
}

type assistantRawHookMatcher struct {
	Matcher string                    `json:"matcher"`
	Hooks   []assistantRawHookHandler `json:"hooks"`
}

func parseAssistantHookDocument(content []byte, sourcePath string, set *AssistantHookSet) error {
	var raw map[string][]assistantRawHookMatcher
	if err := json.Unmarshal(content, &raw); err != nil {
		return fmt.Errorf("parse assistant hook file %q: %w", assistantSourceLabel(sourcePath), err)
	}
	valid := map[string]AssistantHookEvent{}
	for _, event := range assistantHookEvents() {
		valid[strings.ToLower(string(event))] = event
	}
	for eventName, matchers := range raw {
		event, ok := valid[strings.ToLower(strings.TrimSpace(eventName))]
		if !ok {
			return fmt.Errorf("assistant hook file %q references unsupported event %q", assistantSourceLabel(sourcePath), eventName)
		}
		for _, matcher := range matchers {
			normalized := AssistantHookMatcher{Matcher: strings.TrimSpace(matcher.Matcher)}
			for _, handler := range matcher.Hooks {
				handlerType := AssistantHookHandlerType(strings.ToLower(strings.TrimSpace(handler.Type)))
				switch handlerType {
				case AssistantHookHandlerPrompt:
					if strings.TrimSpace(handler.Prompt) == "" {
						return fmt.Errorf("assistant hook file %q has a prompt handler with an empty prompt", assistantSourceLabel(sourcePath))
					}
				case AssistantHookHandlerMCPTool:
					if strings.TrimSpace(handler.Tool) == "" {
						return fmt.Errorf("assistant hook file %q has an mcp-tool handler with an empty tool", assistantSourceLabel(sourcePath))
					}
				default:
					return fmt.Errorf("assistant hook file %q uses unsupported handler type %q (only prompt and mcp-tool are allowed)", assistantSourceLabel(sourcePath), handler.Type)
				}
				normalized.Handlers = append(normalized.Handlers, AssistantHookHandler{
					Type:   handlerType,
					Prompt: strings.TrimSpace(handler.Prompt),
					Tool:   strings.TrimSpace(handler.Tool),
					Args:   handler.Args,
				})
			}
			if len(normalized.Handlers) > 0 {
				set.byEvent[event] = append(set.byEvent[event], normalized)
			}
		}
	}
	return nil
}

func assistantJSONFiles(roots []string) ([]string, error) {
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
			return nil, fmt.Errorf("stat assistant hook root %q: %w", root, err)
		}
		if !info.IsDir() {
			if strings.EqualFold(filepath.Ext(root), ".json") {
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
			if strings.EqualFold(filepath.Ext(path), ".json") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk assistant hook root %q: %w", root, err)
		}
	}
	sort.Strings(files)
	return files, nil
}
