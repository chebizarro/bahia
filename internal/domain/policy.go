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
	RuleSecurityOSVScan     PolicyRuleType = "security_osv_scan"
	RuleBlockPackage        PolicyRuleType = "block_package"
	RuleRequireApproval     PolicyRuleType = "require_approval"
	RulePackageMinAge       PolicyRuleType = "package_min_age"
	RulePackageMinDownloads PolicyRuleType = "package_min_downloads"
	RuleTyposquatCheck      PolicyRuleType = "typosquat_check"

	// --- SBOM Attestation Policy Rules ---

	// RuleSBOMSubjectDigestMatch requires the SBOM attestation subject to match the artifact digest.
	RuleSBOMSubjectDigestMatch PolicyRuleType = "sbom_subject_digest_match"
	// RuleSBOMParseability requires the SBOM to be parseable (valid SPDX or CycloneDX).
	RuleSBOMParseability PolicyRuleType = "sbom_parseability"
	// RuleSBOMNTIAMinFields requires the SBOM to have NTIA minimum elements.
	RuleSBOMNTIAMinFields PolicyRuleType = "sbom_ntia_min_fields"
	// RuleSBOMTrustedGenerator requires the SBOM generator to be in a trusted list.
	RuleSBOMTrustedGenerator PolicyRuleType = "sbom_trusted_generator"
	// RuleSBOMFormat requires a specific SBOM format (spdx or cyclonedx).
	RuleSBOMFormat PolicyRuleType = "sbom_format"
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
	PolicyID    uuid.UUID         `json:"policy_id"`
	PolicyName  string            `json:"policy_name"`
	Passed      bool              `json:"passed"`
	Enforcement PolicyEnforcement `json:"enforcement"`
	Violations  []PolicyViolation `json:"violations,omitempty"`
	// RequiresApproval is true when the policy contains a require_approval rule.
	RequiresApproval bool `json:"requires_approval,omitempty"`
}

// PolicyViolation records a single rule failure.
type PolicyViolation struct {
	Rule        PolicyRuleType    `json:"rule"`
	Message     string            `json:"message"`
	Enforcement PolicyEnforcement `json:"enforcement,omitempty"`
}

// PolicyEvaluation is the aggregate result of evaluating all applicable policies.
type PolicyEvaluation struct {
	Allowed  bool           `json:"allowed"`
	Results  []PolicyResult `json:"results"`
	Warnings int            `json:"warnings"`
	Blockers int            `json:"blockers"`
	// RequiresApproval is true when any applicable policy demands manual
	// approval before the deployment may proceed.
	RequiresApproval bool `json:"requires_approval,omitempty"`
}

// PolicyRequiresApproval reports whether the policy is enabled and contains a
// require_approval rule.
//
// RuleRequireApproval is an approval gate rather than an artifact check: it
// never produces a violation, but any enabled applicable policy containing it
// forces matching deployment intents into pending approval (exactly like a
// protected environment), regardless of the policy's enforcement mode.
func PolicyRequiresApproval(policy DeploymentPolicy) bool {
	if !policy.Enabled {
		return false
	}
	for _, rule := range policy.Rules {
		if rule.Type == RuleRequireApproval {
			return true
		}
	}
	return false
}

// IsBlocked returns true if any blocking policy failed.
func (e *PolicyEvaluation) IsBlocked() bool {
	return e.Blockers > 0
}
