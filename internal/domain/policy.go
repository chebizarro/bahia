package domain

import (
	"time"

	"github.com/google/uuid"
)

// PolicyEnforcement determines what happens when a policy rule fails.
type PolicyEnforcement string

const (
	PolicyEnforcementWarn  PolicyEnforcement = "warn"
	PolicyEnforcementBlock PolicyEnforcement = "block"
)

// PolicyRuleType identifies a specific policy check.
type PolicyRuleType string

const (
	RuleRequireSignature    PolicyRuleType = "require_signature"
	RuleRequireSBOM         PolicyRuleType = "require_sbom"
	RuleMaxCriticalVulns    PolicyRuleType = "max_critical_vulns"
	RuleMaxHighVulns        PolicyRuleType = "max_high_vulns"
	RuleRequireScanStatus   PolicyRuleType = "require_scan_status"
	RuleBlockPackage        PolicyRuleType = "block_package"
	RuleRequireApproval     PolicyRuleType = "require_approval"
	RulePackageMinAge       PolicyRuleType = "package_min_age"
	RulePackageMinDownloads PolicyRuleType = "package_min_downloads"
	RuleTyposquatCheck      PolicyRuleType = "typosquat_check"
)

// PolicyRule is a single check within a deployment policy.
type PolicyRule struct {
	Type   PolicyRuleType `json:"type"`
	Params map[string]any `json:"params,omitempty"` // rule-specific parameters
}

// DeploymentPolicy defines a set of rules enforced during deployment.
type DeploymentPolicy struct {
	ID            uuid.UUID         `json:"id"`
	Name          string            `json:"name"`
	EnvironmentID *uuid.UUID        `json:"environment_id,omitempty"` // nil = global
	Rules         []PolicyRule      `json:"rules"`
	Enforcement   PolicyEnforcement `json:"enforcement"`
	Enabled       bool              `json:"enabled"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// PolicyResult records the outcome of evaluating a policy against an artifact.
type PolicyResult struct {
	PolicyID   uuid.UUID         `json:"policy_id"`
	PolicyName string            `json:"policy_name"`
	Passed     bool              `json:"passed"`
	Enforcement PolicyEnforcement `json:"enforcement"`
	Violations []PolicyViolation `json:"violations,omitempty"`
}

// PolicyViolation records a single rule failure.
type PolicyViolation struct {
	Rule    PolicyRuleType `json:"rule"`
	Message string         `json:"message"`
}

// PolicyEvaluation is the aggregate result of evaluating all applicable policies.
type PolicyEvaluation struct {
	Allowed    bool           `json:"allowed"`
	Results    []PolicyResult `json:"results"`
	Warnings   int            `json:"warnings"`
	Blockers   int            `json:"blockers"`
}

// IsBlocked returns true if any blocking policy failed.
func (e *PolicyEvaluation) IsBlocked() bool {
	return e.Blockers > 0
}
