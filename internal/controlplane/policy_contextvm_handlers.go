package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func (r *Reactor) handlePolicyCreate(ctx context.Context, request ContextVMRequest) (any, error) {
	var req struct {
		Name          string              `json:"name"`
		EnvironmentID *uuid.UUID          `json:"environment_id,omitempty"`
		Rules         []domain.PolicyRule `json:"rules"`
		Enforcement   string              `json:"enforcement"`
		Enabled       bool                `json:"enabled"`
	}
	if err := decodeContextVMParams(request.RPC.Params, &req); err != nil {
		return nil, err
	}
	if request.ProgressToken == "" {
		return nil, fmt.Errorf("idempotency_key or _meta.progressToken is required")
	}
	policy := &domain.DeploymentPolicy{Name: req.Name, EnvironmentID: req.EnvironmentID, Rules: req.Rules, Enforcement: domain.PolicyEnforcement(req.Enforcement), Enabled: req.Enabled}
	if err := validateContextVMPolicy(policy); err != nil {
		return nil, err
	}
	if err := r.policyService.CreatePolicy(ctx, policy); err != nil {
		return nil, err
	}
	if err := r.publishPolicyRegistry(ctx, policy, false); err != nil {
		return nil, fmt.Errorf("policy created but registry publication failed: %w", err)
	}
	return policyMutationResult("policy_create", policy.ID), nil
}

func (r *Reactor) handlePolicyUpdate(ctx context.Context, request ContextVMRequest) (any, error) {
	var req struct {
		ID            uuid.UUID           `json:"id"`
		Name          *string             `json:"name,omitempty"`
		EnvironmentID *string             `json:"environment_id,omitempty"`
		Rules         []domain.PolicyRule `json:"rules,omitempty"`
		Enforcement   *string             `json:"enforcement,omitempty"`
		Enabled       *bool               `json:"enabled,omitempty"`
	}
	if err := decodeContextVMParams(request.RPC.Params, &req); err != nil {
		return nil, err
	}
	if req.ID == uuid.Nil {
		return nil, fmt.Errorf("policy id is required")
	}
	policy, err := r.policyService.GetPolicy(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		return nil, fmt.Errorf("policy not found")
	}
	// Do not mutate a repository-owned value until the patch has been validated.
	updated := *policy
	if req.Name != nil {
		updated.Name = *req.Name
	}
	if req.Rules != nil {
		updated.Rules = req.Rules
	}
	if req.Enforcement != nil {
		updated.Enforcement = domain.PolicyEnforcement(*req.Enforcement)
	}
	if req.Enabled != nil {
		updated.Enabled = *req.Enabled
	}
	if req.EnvironmentID != nil {
		updated.EnvironmentID = nil
		if strings.TrimSpace(*req.EnvironmentID) != "" {
			id, err := uuid.Parse(*req.EnvironmentID)
			if err != nil {
				return nil, fmt.Errorf("invalid environment_id: %w", err)
			}
			updated.EnvironmentID = &id
		}
	}
	if err := validateContextVMPolicy(&updated); err != nil {
		return nil, err
	}
	if err := r.policyService.UpdatePolicy(ctx, &updated); err != nil {
		return nil, err
	}
	if err := r.publishPolicyRegistry(ctx, &updated, false); err != nil {
		return nil, fmt.Errorf("policy updated but registry publication failed: %w", err)
	}
	return policyMutationResult("policy_update", updated.ID), nil
}

func (r *Reactor) handlePolicyDelete(ctx context.Context, request ContextVMRequest) (any, error) {
	var req struct {
		ID uuid.UUID `json:"id"`
	}
	if err := decodeContextVMParams(request.RPC.Params, &req); err != nil {
		return nil, err
	}
	if req.ID == uuid.Nil {
		return nil, fmt.Errorf("policy id is required")
	}
	if err := r.policyService.DeletePolicy(ctx, req.ID); err != nil {
		return nil, err
	}
	if err := r.publishPolicyRegistry(ctx, &domain.DeploymentPolicy{ID: req.ID, UpdatedAt: time.Now().UTC()}, true); err != nil {
		return nil, fmt.Errorf("policy deleted but registry publication failed: %w", err)
	}
	return policyMutationResult("policy_delete", req.ID), nil
}

func (r *Reactor) handlePolicyEvaluate(ctx context.Context, request ContextVMRequest) (any, error) {
	var req struct {
		ArtifactID    uuid.UUID `json:"artifact_id"`
		EnvironmentID uuid.UUID `json:"environment_id"`
	}
	if err := decodeContextVMParams(request.RPC.Params, &req); err != nil {
		return nil, err
	}
	if req.ArtifactID == uuid.Nil || req.EnvironmentID == uuid.Nil {
		return nil, fmt.Errorf("artifact_id and environment_id are required")
	}
	return r.policyService.Evaluate(ctx, req.ArtifactID, req.EnvironmentID)
}

func policyMutationResult(action string, id uuid.UUID) map[string]any {
	return map[string]any{"status": "success", "action": action, "policy_id": id.String()}
}

func validateContextVMPolicy(policy *domain.DeploymentPolicy) error {
	policy.Name = strings.TrimSpace(policy.Name)
	if policy.Name == "" || len(policy.Rules) == 0 {
		return fmt.Errorf("name and rules are required")
	}
	if policy.EnvironmentID != nil && *policy.EnvironmentID == uuid.Nil {
		return fmt.Errorf("environment_id must not be the zero UUID")
	}
	if policy.Enforcement == "" {
		policy.Enforcement = domain.PolicyEnforcementWarn
	}
	if policy.Enforcement != domain.PolicyEnforcementWarn && policy.Enforcement != domain.PolicyEnforcementBlock {
		return fmt.Errorf("enforcement must be warn or block")
	}
	for _, rule := range policy.Rules {
		switch rule.Type {
		case domain.RuleRequireApproval, domain.RuleRequireSignature, domain.RuleRequireSBOM,
			domain.RuleMaxCriticalVulns, domain.RuleMaxHighVulns, domain.RuleRequireScanStatus,
			domain.RuleSecurityOSVScan, domain.RuleBlockPackage, domain.RuleSBOMSubjectDigestMatch,
			domain.RuleSBOMParseability, domain.RuleSBOMNTIAMinFields, domain.RuleSBOMTrustedGenerator,
			domain.RuleSBOMFormat:
		default:
			return fmt.Errorf("unsupported deployment policy rule %q", rule.Type)
		}
	}
	return nil
}
