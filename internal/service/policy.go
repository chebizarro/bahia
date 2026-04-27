package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// PolicyService evaluates deployment policies against artifacts.
type PolicyService struct {
	policies   repository.DeploymentPolicyRepository
	signatures repository.ArtifactSignatureRepository
	sboms      repository.SBOMRepository
	logger     *zap.Logger
}

// NewPolicyService creates a new policy evaluation service.
func NewPolicyService(
	policies repository.DeploymentPolicyRepository,
	signatures repository.ArtifactSignatureRepository,
	sboms repository.SBOMRepository,
	logger *zap.Logger,
) *PolicyService {
	return &PolicyService{
		policies:   policies,
		signatures: signatures,
		sboms:      sboms,
		logger:     logger,
	}
}

// Evaluate runs all applicable policies against an artifact for the given environment.
// Returns the aggregate evaluation result.
func (s *PolicyService) Evaluate(ctx context.Context, artifactID, environmentID uuid.UUID) (*domain.PolicyEvaluation, error) {
	// Collect applicable policies: global + environment-specific.
	globalPolicies, err := s.policies.ListGlobal(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing global policies: %w", err)
	}

	envPolicies, err := s.policies.ListByEnvironment(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("listing env policies: %w", err)
	}

	allPolicies := append(globalPolicies, envPolicies...)
	if len(allPolicies) == 0 {
		return &domain.PolicyEvaluation{Allowed: true}, nil
	}

	eval := &domain.PolicyEvaluation{Allowed: true}

	for _, policy := range allPolicies {
		result := s.evaluatePolicy(ctx, policy, artifactID)
		eval.Results = append(eval.Results, result)

		if !result.Passed {
			switch result.Enforcement {
			case domain.PolicyEnforcementBlock:
				eval.Blockers++
				eval.Allowed = false
			case domain.PolicyEnforcementWarn:
				eval.Warnings++
			}
		}
	}

	return eval, nil
}

// evaluatePolicy checks all rules in a single policy against an artifact.
func (s *PolicyService) evaluatePolicy(ctx context.Context, policy domain.DeploymentPolicy, artifactID uuid.UUID) domain.PolicyResult {
	result := domain.PolicyResult{
		PolicyID:    policy.ID,
		PolicyName:  policy.Name,
		Passed:      true,
		Enforcement: policy.Enforcement,
	}

	for _, rule := range policy.Rules {
		violation := s.evaluateRule(ctx, rule, artifactID)
		if violation != nil {
			result.Passed = false
			result.Violations = append(result.Violations, *violation)
		}
	}

	if !result.Passed {
		s.logger.Warn("policy violation",
			zap.String("policy", policy.Name),
			zap.String("enforcement", string(policy.Enforcement)),
			zap.Int("violations", len(result.Violations)),
		)
	}

	return result
}

// evaluateRule checks a single rule. Returns nil if the rule passes.
func (s *PolicyService) evaluateRule(ctx context.Context, rule domain.PolicyRule, artifactID uuid.UUID) *domain.PolicyViolation {
	switch rule.Type {
	case domain.RuleRequireSignature:
		return s.checkRequireSignature(ctx, artifactID)
	case domain.RuleRequireSBOM:
		return s.checkRequireSBOM(ctx, artifactID)
	case domain.RuleMaxCriticalVulns:
		return s.checkMaxVulns(ctx, artifactID, "critical", rule.Params)
	case domain.RuleMaxHighVulns:
		return s.checkMaxVulns(ctx, artifactID, "high", rule.Params)
	case domain.RuleRequireScanStatus:
		return s.checkRequireScanStatus(ctx, artifactID, rule.Params)
	case domain.RuleBlockPackage:
		return s.checkBlockPackage(ctx, artifactID, rule.Params)
	default:
		s.logger.Debug("unknown policy rule type", zap.String("type", string(rule.Type)))
		return nil
	}
}

func (s *PolicyService) checkRequireSignature(ctx context.Context, artifactID uuid.UUID) *domain.PolicyViolation {
	hasSignature, err := s.signatures.HasVerifiedSignature(ctx, artifactID)
	if err != nil {
		s.logger.Error("error checking signatures", zap.Error(err))
		return &domain.PolicyViolation{
			Rule:    domain.RuleRequireSignature,
			Message: "failed to check signatures: " + err.Error(),
		}
	}
	if !hasSignature {
		return &domain.PolicyViolation{
			Rule:    domain.RuleRequireSignature,
			Message: "artifact has no verified signature",
		}
	}
	return nil
}

func (s *PolicyService) checkRequireSBOM(ctx context.Context, artifactID uuid.UUID) *domain.PolicyViolation {
	_, err := s.sboms.GetSBOMByArtifact(ctx, artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			return &domain.PolicyViolation{
				Rule:    domain.RuleRequireSBOM,
				Message: "artifact has no SBOM",
			}
		}
		return &domain.PolicyViolation{
			Rule:    domain.RuleRequireSBOM,
			Message: "failed to check SBOM: " + err.Error(),
		}
	}
	return nil
}

func (s *PolicyService) checkMaxVulns(ctx context.Context, artifactID uuid.UUID, severity string, params map[string]any) *domain.PolicyViolation {
	sbom, err := s.sboms.GetSBOMByArtifact(ctx, artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil // no SBOM = no vuln data = pass (use require_sbom to enforce)
		}
		return nil
	}

	maxAllowed := getIntParam(params, "max", 0)
	var actual int
	var ruleType domain.PolicyRuleType

	switch severity {
	case "critical":
		actual = sbom.CriticalCount
		ruleType = domain.RuleMaxCriticalVulns
	case "high":
		actual = sbom.HighCount
		ruleType = domain.RuleMaxHighVulns
	}

	if actual > maxAllowed {
		return &domain.PolicyViolation{
			Rule:    ruleType,
			Message: fmt.Sprintf("%d %s vulnerabilities found (max allowed: %d)", actual, severity, maxAllowed),
		}
	}
	return nil
}

func (s *PolicyService) checkRequireScanStatus(ctx context.Context, artifactID uuid.UUID, params map[string]any) *domain.PolicyViolation {
	// This rule checks the artifact's scan_status field, but we don't have
	// direct access to the artifact here. For now, check via SBOM vulnerability counts.
	// If there's no SBOM, we can't verify scan status.
	sbom, err := s.sboms.GetSBOMByArtifact(ctx, artifactID)
	if err != nil {
		return nil // no SBOM = no data = pass
	}

	requiredStatus := getStringParam(params, "status", "clean")
	if requiredStatus == "clean" && sbom.HasVulnerabilities() {
		return &domain.PolicyViolation{
			Rule:    domain.RuleRequireScanStatus,
			Message: fmt.Sprintf("scan status not clean: %d vulnerabilities found", sbom.VulnerabilityCount),
		}
	}
	return nil
}

func (s *PolicyService) checkBlockPackage(ctx context.Context, artifactID uuid.UUID, params map[string]any) *domain.PolicyViolation {
	packageName := getStringParam(params, "package", "")
	if packageName == "" {
		return nil
	}

	sbom, err := s.sboms.GetSBOMByArtifact(ctx, artifactID)
	if err != nil {
		return nil // no SBOM = can't check = pass
	}

	pkgs, err := s.sboms.ListPackagesBySBOM(ctx, sbom.ID)
	if err != nil {
		return nil
	}

	for _, pkg := range pkgs {
		if pkg.Name == packageName {
			return &domain.PolicyViolation{
				Rule:    domain.RuleBlockPackage,
				Message: fmt.Sprintf("blocked package %q found (version: %s)", pkg.Name, pkg.Version),
			}
		}
	}
	return nil
}

// --- CRUD ---

// CreatePolicy creates a new deployment policy.
func (s *PolicyService) CreatePolicy(ctx context.Context, p *domain.DeploymentPolicy) error {
	return s.policies.Create(ctx, p)
}

// GetPolicy retrieves a policy by ID.
func (s *PolicyService) GetPolicy(ctx context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error) {
	return s.policies.GetByID(ctx, id)
}

// ListPolicies returns all policies.
func (s *PolicyService) ListPolicies(ctx context.Context, enabledOnly bool) ([]domain.DeploymentPolicy, error) {
	return s.policies.List(ctx, enabledOnly)
}

// UpdatePolicy updates an existing policy.
func (s *PolicyService) UpdatePolicy(ctx context.Context, p *domain.DeploymentPolicy) error {
	return s.policies.Update(ctx, p)
}

// DeletePolicy removes a policy.
func (s *PolicyService) DeletePolicy(ctx context.Context, id uuid.UUID) error {
	return s.policies.Delete(ctx, id)
}

// Helper functions for rule params.

func getIntParam(params map[string]any, key string, defaultVal int) int {
	if params == nil {
		return defaultVal
	}
	v, ok := params[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return defaultVal
	}
}

func getStringParam(params map[string]any, key, defaultVal string) string {
	if params == nil {
		return defaultVal
	}
	v, ok := params[key]
	if !ok {
		return defaultVal
	}
	s, ok := v.(string)
	if !ok {
		return defaultVal
	}
	return s
}
