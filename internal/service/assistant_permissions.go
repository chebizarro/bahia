package service

import (
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

// AssistantToolPermissionMetadata is the permission-relevant descriptor data for
// one assistant-callable tool. The MCP registry owns discovery; this service
// only evaluates the already-discovered metadata.
type AssistantToolPermissionMetadata struct {
	Name          string
	Effect        domain.AssistantToolEffect
	DefaultRisk   domain.AssistantToolRisk
	ExecutionMode domain.AssistantToolExecutionMode
	ResourceTypes []string
}

// AssistantPermissionRequest is one tool invocation candidate presented to the
// permission engine before runtime execution.
type AssistantPermissionRequest struct {
	Tool AssistantToolPermissionMetadata
	Args map[string]any
}

// AssistantPermissionRule declares an explicit operator policy rule. Empty
// matcher fields are wildcards. Deny rules are always evaluated before ask or
// allow rules.
type AssistantPermissionRule struct {
	ID             string
	Decision       domain.AssistantPermissionDecision
	ToolNames      []string
	ToolPrefixes   []string
	Effects        []domain.AssistantToolEffect
	Risks          []domain.AssistantToolRisk
	ExecutionModes []domain.AssistantToolExecutionMode
	ResourceTypes  []string
	Reason         string
}

// AssistantPermissionEngine evaluates assistant tool calls using the global
// permission mode plus explicit rules supplied from configuration or a caller's
// policy assembly layer.
type AssistantPermissionEngine struct {
	mode  domain.AssistantPermissionMode
	rules []AssistantPermissionRule
}

// NewAssistantPermissionEngine builds a permission engine from assistant config
// and explicit rules. Config currently owns the canonical rollout mode; rule
// loading is kept outside this service so registry/runtime callers can supply
// their policy source without changing the evaluator contract.
func NewAssistantPermissionEngine(cfg config.AssistantPermissionsConfig, rules []AssistantPermissionRule) *AssistantPermissionEngine {
	mode := normalizeAssistantPermissionMode(cfg.Mode)
	copied := make([]AssistantPermissionRule, 0, len(rules))
	for _, rule := range rules {
		copied = append(copied, normalizeAssistantPermissionRule(rule))
	}
	return &AssistantPermissionEngine{mode: mode, rules: copied}
}

// Evaluate returns the allow/ask/deny decision for a candidate tool call.
func (e *AssistantPermissionEngine) Evaluate(req AssistantPermissionRequest) domain.AssistantPermissionResult {
	if e == nil {
		return denyResult(domain.AssistantPermissionModeReview, req.Tool, "assistant permission engine is not configured", "")
	}

	tool := normalizeAssistantToolPermissionMetadata(req.Tool)
	if tool.Name == "" {
		return denyResult(e.mode, tool, "tool name is required for assistant permission evaluation", "")
	}
	if tool.Effect != domain.AssistantToolEffectRead && tool.Effect != domain.AssistantToolEffectMutation {
		return denyResult(e.mode, tool, fmt.Sprintf("unsupported assistant tool effect %q", tool.Effect), "")
	}

	risk, riskMetadata := e.evaluateRisk(tool, req.Args)
	tool.DefaultRisk = risk

	if rule, ok := e.firstMatchingRule(domain.AssistantPermissionDecisionDeny, tool); ok {
		return ruleResult(e.mode, tool, rule, riskMetadata)
	}
	if rule, ok := e.firstMatchingRule(domain.AssistantPermissionDecisionAsk, tool); ok {
		return ruleResult(e.mode, tool, rule, riskMetadata)
	}

	switch e.mode {
	case domain.AssistantPermissionModeReview:
		return e.evaluateReview(tool, riskMetadata)
	case domain.AssistantPermissionModeAudited:
		if rule, ok := e.firstMatchingRule(domain.AssistantPermissionDecisionAllow, tool); ok {
			return ruleResult(e.mode, tool, rule, riskMetadata)
		}
		return e.evaluateAudited(tool, riskMetadata)
	case domain.AssistantPermissionModeReadonly:
		return e.evaluateReadonly(tool, riskMetadata)
	case domain.AssistantPermissionModeEmergency:
		return e.evaluateEmergency(tool, riskMetadata)
	default:
		return denyResult(e.mode, tool, fmt.Sprintf("unsupported assistant permission mode %q", e.mode), "")
	}
}

func (e *AssistantPermissionEngine) evaluateReview(tool AssistantToolPermissionMetadata, metadata map[string]any) domain.AssistantPermissionResult {
	if tool.Effect == domain.AssistantToolEffectRead {
		return permissionResult(domain.AssistantPermissionDecisionAllow, e.mode, tool, "review mode allows read-only assistant tools", "", metadata)
	}
	return permissionResult(domain.AssistantPermissionDecisionAsk, e.mode, tool, "review mode requires approval for assistant mutations", "", metadata)
}

func (e *AssistantPermissionEngine) evaluateAudited(tool AssistantToolPermissionMetadata, metadata map[string]any) domain.AssistantPermissionResult {
	if tool.Effect == domain.AssistantToolEffectRead {
		return permissionResult(domain.AssistantPermissionDecisionAllow, e.mode, tool, "audited mode allows read-only assistant tools", "", metadata)
	}
	switch tool.DefaultRisk {
	case domain.AssistantToolRiskLow, domain.AssistantToolRiskMedium:
		return permissionResult(domain.AssistantPermissionDecisionAllow, e.mode, tool, "audited mode allows low and medium risk scoped assistant mutations", "", metadata)
	case domain.AssistantToolRiskHigh, domain.AssistantToolRiskDestructive:
		return permissionResult(domain.AssistantPermissionDecisionAsk, e.mode, tool, "audited mode requires approval for high risk or destructive assistant mutations", "", metadata)
	default:
		return permissionResult(domain.AssistantPermissionDecisionAsk, e.mode, tool, "audited mode requires approval when assistant tool risk is unknown", "", metadata)
	}
}

func (e *AssistantPermissionEngine) evaluateReadonly(tool AssistantToolPermissionMetadata, metadata map[string]any) domain.AssistantPermissionResult {
	if tool.Effect == domain.AssistantToolEffectRead {
		return permissionResult(domain.AssistantPermissionDecisionAllow, e.mode, tool, "readonly mode allows read-only assistant tools", "", metadata)
	}
	return permissionResult(domain.AssistantPermissionDecisionDeny, e.mode, tool, "readonly mode denies assistant mutations", "", metadata)
}

func (e *AssistantPermissionEngine) evaluateEmergency(tool AssistantToolPermissionMetadata, metadata map[string]any) domain.AssistantPermissionResult {
	return permissionResult(domain.AssistantPermissionDecisionDeny, e.mode, tool, "emergency mode denies assistant tool execution", "", metadata)
}

func (e *AssistantPermissionEngine) evaluateRisk(tool AssistantToolPermissionMetadata, args map[string]any) (domain.AssistantToolRisk, map[string]any) {
	risk := normalizeAssistantToolRisk(tool.DefaultRisk, tool.Effect)
	upgraded, reason := upgradeAssistantToolRiskFromArgs(risk, args)
	if upgraded == risk {
		return risk, nil
	}
	return upgraded, map[string]any{
		"risk_upgraded_from":  string(risk),
		"risk_upgrade_reason": reason,
	}
}

func (e *AssistantPermissionEngine) firstMatchingRule(decision domain.AssistantPermissionDecision, tool AssistantToolPermissionMetadata) (AssistantPermissionRule, bool) {
	for _, rule := range e.rules {
		if rule.Decision != decision {
			continue
		}
		if rule.matches(tool) {
			return rule, true
		}
	}
	return AssistantPermissionRule{}, false
}

func (r AssistantPermissionRule) matches(tool AssistantToolPermissionMetadata) bool {
	if !matchesString(r.ToolNames, tool.Name) {
		return false
	}
	if !matchesPrefix(r.ToolPrefixes, tool.Name) {
		return false
	}
	if !matchesEffect(r.Effects, tool.Effect) {
		return false
	}
	if !matchesRisk(r.Risks, tool.DefaultRisk) {
		return false
	}
	if !matchesExecutionMode(r.ExecutionModes, tool.ExecutionMode) {
		return false
	}
	if !matchesAnyResourceType(r.ResourceTypes, tool.ResourceTypes) {
		return false
	}
	return true
}

func normalizeAssistantPermissionMode(mode domain.AssistantPermissionMode) domain.AssistantPermissionMode {
	switch domain.AssistantPermissionMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case domain.AssistantPermissionModeAudited:
		return domain.AssistantPermissionModeAudited
	case domain.AssistantPermissionModeReadonly:
		return domain.AssistantPermissionModeReadonly
	case domain.AssistantPermissionModeEmergency:
		return domain.AssistantPermissionModeEmergency
	case domain.AssistantPermissionModeReview:
		return domain.AssistantPermissionModeReview
	case "":
		return domain.AssistantPermissionModeAudited
	default:
		return mode
	}
}

func normalizeAssistantPermissionRule(rule AssistantPermissionRule) AssistantPermissionRule {
	rule.ID = strings.TrimSpace(rule.ID)
	rule.Decision = domain.AssistantPermissionDecision(strings.ToLower(strings.TrimSpace(string(rule.Decision))))
	rule.ToolNames = normalizeAssistantStringSet(rule.ToolNames)
	rule.ToolPrefixes = normalizeAssistantStringSet(rule.ToolPrefixes)
	for i := range rule.Effects {
		rule.Effects[i] = domain.AssistantToolEffect(strings.ToLower(strings.TrimSpace(string(rule.Effects[i]))))
	}
	for i := range rule.Risks {
		rule.Risks[i] = domain.AssistantToolRisk(strings.ToLower(strings.TrimSpace(string(rule.Risks[i]))))
	}
	for i := range rule.ExecutionModes {
		rule.ExecutionModes[i] = domain.AssistantToolExecutionMode(strings.ToLower(strings.TrimSpace(string(rule.ExecutionModes[i]))))
	}
	rule.ResourceTypes = normalizeAssistantStringSet(rule.ResourceTypes)
	rule.Reason = strings.TrimSpace(rule.Reason)
	return rule
}

func normalizeAssistantToolPermissionMetadata(tool AssistantToolPermissionMetadata) AssistantToolPermissionMetadata {
	tool.Name = strings.TrimSpace(tool.Name)
	tool.Effect = domain.AssistantToolEffect(strings.ToLower(strings.TrimSpace(string(tool.Effect))))
	tool.ExecutionMode = domain.AssistantToolExecutionMode(strings.ToLower(strings.TrimSpace(string(tool.ExecutionMode))))
	if tool.ExecutionMode == "" {
		tool.ExecutionMode = domain.AssistantToolExecutionModeSync
	}
	tool.DefaultRisk = normalizeAssistantToolRisk(tool.DefaultRisk, tool.Effect)
	tool.ResourceTypes = normalizeAssistantStringSet(tool.ResourceTypes)
	return tool
}

func normalizeAssistantToolRisk(risk domain.AssistantToolRisk, effect domain.AssistantToolEffect) domain.AssistantToolRisk {
	switch domain.AssistantToolRisk(strings.ToLower(strings.TrimSpace(string(risk)))) {
	case domain.AssistantToolRiskLow:
		return domain.AssistantToolRiskLow
	case domain.AssistantToolRiskMedium:
		return domain.AssistantToolRiskMedium
	case domain.AssistantToolRiskHigh:
		return domain.AssistantToolRiskHigh
	case domain.AssistantToolRiskDestructive:
		return domain.AssistantToolRiskDestructive
	default:
		if effect == domain.AssistantToolEffectRead {
			return domain.AssistantToolRiskLow
		}
		return domain.AssistantToolRiskHigh
	}
}

func upgradeAssistantToolRiskFromArgs(current domain.AssistantToolRisk, args map[string]any) (domain.AssistantToolRisk, string) {
	if len(args) == 0 || current == domain.AssistantToolRiskDestructive {
		return current, ""
	}
	haystack := strings.ToLower(fmt.Sprintf("%v", args))
	destructiveMarkers := []string{"destroy", "purge"}
	for _, marker := range destructiveMarkers {
		if strings.Contains(haystack, marker) {
			return maxAssistantToolRisk(current, domain.AssistantToolRiskDestructive), "arguments reference destructive operation marker " + marker
		}
	}
	highMarkers := []string{"production", "prod", "rollback", "delete", "remove", "revoke"}
	for _, marker := range highMarkers {
		if strings.Contains(haystack, marker) {
			return maxAssistantToolRisk(current, domain.AssistantToolRiskHigh), "arguments reference elevated risk marker " + marker
		}
	}
	return current, ""
}

func maxAssistantToolRisk(left, right domain.AssistantToolRisk) domain.AssistantToolRisk {
	if assistantToolRiskRank(right) > assistantToolRiskRank(left) {
		return right
	}
	return left
}

func assistantToolRiskRank(risk domain.AssistantToolRisk) int {
	switch risk {
	case domain.AssistantToolRiskLow:
		return 1
	case domain.AssistantToolRiskMedium:
		return 2
	case domain.AssistantToolRiskHigh:
		return 3
	case domain.AssistantToolRiskDestructive:
		return 4
	default:
		return 3
	}
}

func ruleResult(mode domain.AssistantPermissionMode, tool AssistantToolPermissionMetadata, rule AssistantPermissionRule, metadata map[string]any) domain.AssistantPermissionResult {
	reason := rule.Reason
	if reason == "" {
		reason = fmt.Sprintf("assistant permission rule %q returned %s", rule.ID, rule.Decision)
	}
	return permissionResult(rule.Decision, mode, tool, reason, rule.ID, metadata)
}

func denyResult(mode domain.AssistantPermissionMode, tool AssistantToolPermissionMetadata, reason string, ruleID string) domain.AssistantPermissionResult {
	return permissionResult(domain.AssistantPermissionDecisionDeny, mode, tool, reason, ruleID, nil)
}

func permissionResult(decision domain.AssistantPermissionDecision, mode domain.AssistantPermissionMode, tool AssistantToolPermissionMetadata, reason, ruleID string, metadata map[string]any) domain.AssistantPermissionResult {
	return domain.AssistantPermissionResult{
		Decision:      decision,
		Mode:          mode,
		Effect:        tool.Effect,
		Risk:          tool.DefaultRisk,
		ExecutionMode: tool.ExecutionMode,
		Reason:        reason,
		RuleID:        ruleID,
		Metadata:      cloneAssistantMetadata(metadata),
	}
}

func cloneAssistantMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func normalizeAssistantStringSet(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func matchesString(allowed []string, value string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

func matchesPrefix(prefixes []string, value string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func matchesEffect(allowed []domain.AssistantToolEffect, value domain.AssistantToolEffect) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

func matchesRisk(allowed []domain.AssistantToolRisk, value domain.AssistantToolRisk) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

func matchesExecutionMode(allowed []domain.AssistantToolExecutionMode, value domain.AssistantToolExecutionMode) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

func matchesAnyResourceType(allowed []string, values []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, allowedValue := range allowed {
		for _, value := range values {
			if allowedValue == value {
				return true
			}
		}
	}
	return false
}
