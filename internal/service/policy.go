package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// SBOMAttestationProvider provides SBOM attestation data for policy evaluation.
type SBOMAttestationProvider interface {
	// GetAttestationForArtifact returns the SBOM attestation for an artifact.
	GetAttestationForArtifact(ctx context.Context, artifactID uuid.UUID) (*domain.SBOMAttestation, error)
	// GetSBOMDataForArtifact returns the raw SBOM data for an artifact.
	GetSBOMDataForArtifact(ctx context.Context, artifactID uuid.UUID) ([]byte, error)
}

// PolicyService evaluates deployment policies against artifacts.
type PolicyService struct {
	policies       repository.DeploymentPolicyRepository
	signatures     repository.ArtifactSignatureRepository
	sboms          repository.SBOMRepository
	attestations   SBOMAttestationProvider
	trustedGens    map[string]bool // map of trusted generator IDs
	logger         *zap.Logger
}

// PolicyServiceOption configures the PolicyService.
type PolicyServiceOption func(*PolicyService)

// WithAttestationProvider sets the SBOM attestation provider.
func WithAttestationProvider(provider SBOMAttestationProvider) PolicyServiceOption {
	return func(s *PolicyService) { s.attestations = provider }
}

// WithTrustedGenerators sets the list of trusted SBOM generator IDs.
func WithTrustedGenerators(generators []string) PolicyServiceOption {
	return func(s *PolicyService) {
		s.trustedGens = make(map[string]bool)
		for _, g := range generators {
			s.trustedGens[g] = true
		}
	}
}

// NewPolicyService creates a new policy evaluation service.
func NewPolicyService(
	policies repository.DeploymentPolicyRepository,
	signatures repository.ArtifactSignatureRepository,
	sboms repository.SBOMRepository,
	logger *zap.Logger,
	opts ...PolicyServiceOption,
) *PolicyService {
	s := &PolicyService{
		policies:    policies,
		signatures:  signatures,
		sboms:       sboms,
		trustedGens: make(map[string]bool),
		logger:      logger,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
	// --- SBOM Attestation Rules ---
	case domain.RuleSBOMSubjectDigestMatch:
		return s.checkSBOMSubjectDigestMatch(ctx, artifactID, rule.Params)
	case domain.RuleSBOMParseability:
		return s.checkSBOMParseability(ctx, artifactID)
	case domain.RuleSBOMNTIAMinFields:
		return s.checkSBOMNTIAMinFields(ctx, artifactID)
	case domain.RuleSBOMTrustedGenerator:
		return s.checkSBOMTrustedGenerator(ctx, artifactID, rule.Params)
	case domain.RuleSBOMFormat:
		return s.checkSBOMFormat(ctx, artifactID, rule.Params)
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

// --- SBOM Attestation Policy Rule Implementations ---

// checkSBOMSubjectDigestMatch verifies the SBOM attestation subject matches the artifact digest.
func (s *PolicyService) checkSBOMSubjectDigestMatch(ctx context.Context, artifactID uuid.UUID, params map[string]any) *domain.PolicyViolation {
	if s.attestations == nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMSubjectDigestMatch,
			Message: "attestation provider not configured",
		}
	}

	expectedDigest := getStringParam(params, "artifact_digest", "")
	if expectedDigest == "" {
		// Try to get digest from artifact metadata (would need artifact repo)
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMSubjectDigestMatch,
			Message: "artifact_digest parameter required",
		}
	}

	att, err := s.attestations.GetAttestationForArtifact(ctx, artifactID)
	if err != nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMSubjectDigestMatch,
			Message: fmt.Sprintf("failed to get attestation: %v", err),
		}
	}

	// Verify subject digest matches.
	if !verifySBOMSubjectDigest(att, expectedDigest) {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMSubjectDigestMatch,
			Message: fmt.Sprintf("SBOM subject digest does not match artifact digest %s", expectedDigest),
		}
	}

	return nil
}

// checkSBOMParseability verifies the SBOM can be parsed as valid SPDX or CycloneDX.
func (s *PolicyService) checkSBOMParseability(ctx context.Context, artifactID uuid.UUID) *domain.PolicyViolation {
	if s.attestations == nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMParseability,
			Message: "attestation provider not configured",
		}
	}

	sbomData, err := s.attestations.GetSBOMDataForArtifact(ctx, artifactID)
	if err != nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMParseability,
			Message: fmt.Sprintf("failed to get SBOM data: %v", err),
		}
	}

	// Try to parse the SBOM - this validates format.
	if _, err := parseSBOMForValidation(sbomData); err != nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMParseability,
			Message: fmt.Sprintf("SBOM is not parseable: %v", err),
		}
	}

	return nil
}

// checkSBOMNTIAMinFields verifies the SBOM has NTIA minimum elements.
func (s *PolicyService) checkSBOMNTIAMinFields(ctx context.Context, artifactID uuid.UUID) *domain.PolicyViolation {
	if s.attestations == nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMNTIAMinFields,
			Message: "attestation provider not configured",
		}
	}

	att, err := s.attestations.GetAttestationForArtifact(ctx, artifactID)
	if err != nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMNTIAMinFields,
			Message: fmt.Sprintf("failed to get attestation: %v", err),
		}
	}

	if att.Predicate.NTIA == nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMNTIAMinFields,
			Message: "SBOM attestation missing NTIA compliance data",
		}
	}

	if !att.Predicate.NTIA.IsCompliant {
		missing := collectMissingNTIAFields(att.Predicate.NTIA)
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMNTIAMinFields,
			Message: fmt.Sprintf("SBOM missing NTIA minimum elements: %s", missing),
		}
	}

	return nil
}

// checkSBOMTrustedGenerator verifies the SBOM generator is in the trusted list.
func (s *PolicyService) checkSBOMTrustedGenerator(ctx context.Context, artifactID uuid.UUID, params map[string]any) *domain.PolicyViolation {
	if s.attestations == nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMTrustedGenerator,
			Message: "attestation provider not configured",
		}
	}

	att, err := s.attestations.GetAttestationForArtifact(ctx, artifactID)
	if err != nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMTrustedGenerator,
			Message: fmt.Sprintf("failed to get attestation: %v", err),
		}
	}

	generatorID := att.Predicate.Generator.ID
	if generatorID == "" {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMTrustedGenerator,
			Message: "SBOM generator not identified",
		}
	}

	// Check params for inline trusted list, or fall back to service config.
	trustedList := getStringSliceParam(params, "trusted_generators")
	if len(trustedList) > 0 {
		for _, trusted := range trustedList {
			if trusted == generatorID {
				return nil // Trusted
			}
		}
	} else if s.trustedGens[generatorID] {
		return nil // Trusted via service config
	}

	return &domain.PolicyViolation{
		Rule:    domain.RuleSBOMTrustedGenerator,
		Message: fmt.Sprintf("SBOM generator %q is not in trusted list", generatorID),
	}
}

// checkSBOMFormat verifies the SBOM is in an acceptable format.
func (s *PolicyService) checkSBOMFormat(ctx context.Context, artifactID uuid.UUID, params map[string]any) *domain.PolicyViolation {
	allowedFormats := getStringSliceParam(params, "formats")
	if len(allowedFormats) == 0 {
		// No format restriction
		return nil
	}

	sbom, err := s.sboms.GetSBOMByArtifact(ctx, artifactID)
	if err != nil {
		return &domain.PolicyViolation{
			Rule:    domain.RuleSBOMFormat,
			Message: fmt.Sprintf("failed to get SBOM: %v", err),
		}
	}

	for _, allowed := range allowedFormats {
		if string(sbom.Format) == allowed {
			return nil // Allowed format
		}
	}

	return &domain.PolicyViolation{
		Rule:    domain.RuleSBOMFormat,
		Message: fmt.Sprintf("SBOM format %q not in allowed formats: %v", sbom.Format, allowedFormats),
	}
}

// verifySBOMSubjectDigest checks if any attestation subject matches the expected digest.
func verifySBOMSubjectDigest(att *domain.SBOMAttestation, expectedDigest string) bool {
	if att == nil || len(att.Subject) == 0 {
		return false
	}

	// Parse expected digest (format: "algo:hash")
	parts := splitDigest(expectedDigest)
	if len(parts) != 2 {
		return false
	}
	algo, hash := parts[0], parts[1]

	for _, subject := range att.Subject {
		if subjectHash, ok := subject.Digest[algo]; ok {
			if subjectHash == hash {
				return true
			}
		}
	}
	return false
}

// splitDigest splits "algo:hash" into [algo, hash].
func splitDigest(digest string) []string {
	for i, c := range digest {
		if c == ':' {
			return []string{digest[:i], digest[i+1:]}
		}
	}
	return nil
}

// collectMissingNTIAFields returns a comma-separated list of missing NTIA fields.
func collectMissingNTIAFields(ntia *domain.NTIACompliance) string {
	var missing []string
	if !ntia.HasSupplierName {
		missing = append(missing, "supplier_name")
	}
	if !ntia.HasComponentName {
		missing = append(missing, "component_name")
	}
	if !ntia.HasComponentVersion {
		missing = append(missing, "component_version")
	}
	if !ntia.HasUniqueID {
		missing = append(missing, "unique_id")
	}
	if !ntia.HasRelationship {
		missing = append(missing, "relationship")
	}
	if !ntia.HasAuthor {
		missing = append(missing, "author")
	}
	if !ntia.HasTimestamp {
		missing = append(missing, "timestamp")
	}
	if len(missing) == 0 {
		return "none"
	}
	return fmt.Sprintf("%v", missing)
}

// parseSBOMForValidation is a lightweight SBOM format detection and validation.
func parseSBOMForValidation(data []byte) (domain.SBOMFormat, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty SBOM data")
	}

	// Detect format by checking for format-specific keys.
	var probe struct {
		SPDXVersion string `json:"spdxVersion"`
		BOMFormat   string `json:"bomFormat"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}

	if probe.SPDXVersion != "" {
		return domain.SBOMFormatSPDX, nil
	}
	if probe.BOMFormat == "CycloneDX" {
		return domain.SBOMFormatCycloneDX, nil
	}
	return "", fmt.Errorf("unknown SBOM format")
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

func getStringSliceParam(params map[string]any, key string) []string {
	if params == nil {
		return nil
	}
	v, ok := params[key]
	if !ok {
		return nil
	}

	// Handle []string directly.
	if ss, ok := v.([]string); ok {
		return ss
	}

	// Handle []interface{} (common from JSON unmarshaling).
	if arr, ok := v.([]interface{}); ok {
		result := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}

	return nil
}
