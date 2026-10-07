package domain

// AssistantPermissionMode selects the global assistant permission posture.
type AssistantPermissionMode string

const (
	// AssistantPermissionModeReview preserves the legacy human-review posture:
	// reads may run, but mutation-capable tools require an approval decision.
	AssistantPermissionModeReview AssistantPermissionMode = "review"

	// AssistantPermissionModeAudited allows policy-approved autonomous execution
	// with audit events: reads and low/medium-risk scoped mutations may run while
	// high-risk or destructive actions require approval.
	AssistantPermissionModeAudited AssistantPermissionMode = "audited"

	// AssistantPermissionModeReadonly allows read-only assistant tools and denies
	// mutations regardless of their default risk.
	AssistantPermissionModeReadonly AssistantPermissionMode = "readonly"

	// AssistantPermissionModeEmergency denies assistant tool execution by default
	// during incident lockdown.
	AssistantPermissionModeEmergency AssistantPermissionMode = "emergency"
)

// AssistantPermissionDecision is the canonical allow/ask/deny result emitted by
// the assistant permission engine.
type AssistantPermissionDecision string

const (
	AssistantPermissionDecisionAllow AssistantPermissionDecision = "allow"
	AssistantPermissionDecisionAsk   AssistantPermissionDecision = "ask"
	AssistantPermissionDecisionDeny  AssistantPermissionDecision = "deny"
)

// AssistantToolEffect declares whether a tool only reads state or can mutate
// control-plane resources.
type AssistantToolEffect string

const (
	AssistantToolEffectRead     AssistantToolEffect = "read"
	AssistantToolEffectMutation AssistantToolEffect = "mutation"
)

// AssistantToolRisk is the default or evaluated risk tier for an assistant tool
// action.
type AssistantToolRisk string

const (
	AssistantToolRiskLow         AssistantToolRisk = "low"
	AssistantToolRiskMedium      AssistantToolRisk = "medium"
	AssistantToolRiskHigh        AssistantToolRisk = "high"
	AssistantToolRiskDestructive AssistantToolRisk = "destructive"
)

// AssistantToolExecutionMode tells the agent runtime whether a tool returns a
// terminal result inline or submits an event-native action whose result must be
// observed later through scoped Nostr subscriptions.
type AssistantToolExecutionMode string

const (
	AssistantToolExecutionModeSync  AssistantToolExecutionMode = "sync"
	AssistantToolExecutionModeAsync AssistantToolExecutionMode = "async"
)

// AssistantPermissionResult captures a permission decision with the normalized
// metadata that downstream runtime/status code should audit.
type AssistantPermissionResult struct {
	Decision      AssistantPermissionDecision `json:"decision"`
	Mode          AssistantPermissionMode     `json:"mode,omitempty"`
	Effect        AssistantToolEffect         `json:"effect,omitempty"`
	Risk          AssistantToolRisk           `json:"risk,omitempty"`
	ExecutionMode AssistantToolExecutionMode  `json:"execution_mode,omitempty"`
	Reason        string                      `json:"reason,omitempty"`
	RuleID        string                      `json:"rule_id,omitempty"`
	Metadata      map[string]any              `json:"metadata,omitempty"`
}
