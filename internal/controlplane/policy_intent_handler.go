package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// PolicyCRUD is the read/write contract the policy intent handler uses for
// level-triggered reconciliation. service.PolicyService satisfies it.
type PolicyCRUD interface {
	CreatePolicy(ctx context.Context, p *domain.DeploymentPolicy) (replayed bool, err error)
	GetPolicy(ctx context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error)
	UpdatePolicy(ctx context.Context, p *domain.DeploymentPolicy) error
	DeletePolicy(ctx context.Context, id uuid.UUID) error
}

// PolicyStatePublisher signs and publishes a canonical kind-30900 cp-state
// record for a deployment policy mutation. The implementation uses
// PublishBeforeCommit for outbox durability (docs/architecture/intents-and-authority.md).
type PolicyStatePublisher func(ctx context.Context, policy *domain.DeploymentPolicy, deleted bool) error

// PolicyEvaluator preserves the daemon's full signature, SBOM, security-scan,
// and attestation checks when evaluating a deployment decision.
type PolicyEvaluator interface {
	Evaluate(ctx context.Context, artifactID, environmentID uuid.UUID) (*domain.PolicyEvaluation, error)
}

// PolicyIntentHandler processes kind-30900 intents for the "policy" domain.
// It is level-triggered: the intent's content is the full desired state, and
// the handler reconciles the entity toward it regardless of whether prior
// events for the coordinate have been seen.
//
// Registered at startup when "policy" is enabled via
// IntentProcessor.RegisterHandler("policy", handler).
//
// Policies are fleet-scoped: authorization uses FleetOperatorGate rather than
// per-org membership (docs/architecture/intents-and-authority.md). The handler implements FleetScopedHandler
// so the intent processor authorizes fleet operators correctly.
//
// Revision decision: latest-wins. DeploymentPolicy carries UpdatedAt but has
// no concurrent multi-operator editing in practice (it is fleet-operator
// authored). When expected_updated_at is present in the intent content the
// handler enforces it; otherwise the newest intent wins unconditionally.
//
// See docs/architecture/intents-and-authority.md
type PolicyIntentHandler struct {
	policies PolicyCRUD
	evaluate PolicyEvaluator
	publish  PolicyStatePublisher
	status   *IntentStatusPublisher
	logger   *zap.Logger
}

// PolicyIntentHandlerConfig configures the policy intent handler.
type PolicyIntentHandlerConfig struct {
	Policies  PolicyCRUD
	Evaluator PolicyEvaluator
	Publish   PolicyStatePublisher
	Status    *IntentStatusPublisher
	Logger    *zap.Logger
}

// NewPolicyIntentHandler constructs the handler.
func NewPolicyIntentHandler(cfg PolicyIntentHandlerConfig) *PolicyIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PolicyIntentHandler{
		policies: cfg.Policies,
		evaluate: cfg.Evaluator,
		publish:  cfg.Publish,
		status:   cfg.Status,
		logger:   logger.Named("policy-intent"),
	}
}

// HandleIntent processes a single policy intent. The processor has already
// deduplicated, validated, and authorized the intent.
func (h *PolicyIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "delete":
		return h.handleDelete(ctx, intent)
	case "evaluate":
		return h.handleEvaluate(ctx, intent)
	default:
		// Level-triggered: create and update both reconcile toward desired state.
		return h.handleCreateOrUpdate(ctx, intent)
	}
}

func (h *PolicyIntentHandler) handleEvaluate(ctx context.Context, intent *Intent) error {
	if h.evaluate == nil {
		return fmt.Errorf("policy evaluator is not configured")
	}
	artifactID, err := uuid.Parse(fmt.Sprint(intent.Content["artifact_id"]))
	if err != nil || artifactID == uuid.Nil {
		return fmt.Errorf("artifact_id must be a non-nil UUID")
	}
	environmentID, err := uuid.Parse(fmt.Sprint(intent.Content["environment_id"]))
	if err != nil || environmentID == uuid.Nil {
		return fmt.Errorf("environment_id must be a non-nil UUID")
	}
	if intent.Coordinate != "evaluation:"+artifactID.String()+":"+environmentID.String() {
		return fmt.Errorf("policy evaluation coordinate does not match artifact and environment")
	}
	evaluation, err := h.evaluate.Evaluate(ctx, artifactID, environmentID)
	if err != nil {
		return err
	}
	if evaluation == nil {
		return fmt.Errorf("policy evaluator returned no decision")
	}
	intent.Evaluation = evaluation
	return nil
}

// PermissionFor returns domain.PermWritePolicies for all policy ops.
func (h *PolicyIntentHandler) PermissionFor(_ string) domain.Permission {
	return domain.PermWritePolicies
}

// IsFleetScoped implements FleetScopedHandler. Policy mutations are
// fleet-scoped — authorized via FleetOperatorGate (config pubkeys), not
// per-org membership (docs/architecture/intents-and-authority.md).
func (h *PolicyIntentHandler) IsFleetScoped() bool { return true }

// handleCreateOrUpdate reconciles a policy toward the intent's desired state.
// Level-triggered: if the entity doesn't exist, it is created regardless of
// whether the op tag says "create" or "update".
func (h *PolicyIntentHandler) handleCreateOrUpdate(ctx context.Context, intent *Intent) error {
	policy, err := policyFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse policy intent content: %w", err)
	}

	// Check expected_updated_at revision if present.
	if intent.ExpectedUpdatedAt != nil {
		return h.updateWithRevision(ctx, policy, intent)
	}

	// Level-triggered: try to load existing, create or update accordingly.
	existing, _ := h.policies.GetPolicy(ctx, policy.ID)
	if existing == nil {
		return h.createPolicy(ctx, policy, intent)
	}

	// Entity exists: merge and update.
	mergePolicyOntoExisting(existing, policy)
	return h.updatePolicy(ctx, existing, intent)
}

// handleDelete processes a delete intent.
func (h *PolicyIntentHandler) handleDelete(ctx context.Context, intent *Intent) error {
	idStr, _ := intent.Content["id"].(string)
	id, err := uuid.Parse(idStr)
	if err != nil || id == uuid.Nil {
		id, err = uuid.Parse(intent.Coordinate)
		if err != nil || id == uuid.Nil {
			return fmt.Errorf("delete intent must carry entity id")
		}
	}

	if err := h.policies.DeletePolicy(ctx, id); err != nil {
		return fmt.Errorf("delete policy: %w", err)
	}

	// Publish tombstone.
	tombstone := &domain.DeploymentPolicy{ID: id, UpdatedAt: time.Now().UTC()}
	if h.publish != nil {
		if err := h.publish(ctx, tombstone, true); err != nil {
			h.logger.Warn("failed to publish policy tombstone",
				zap.String("policy_id", id.String()),
				zap.Error(err),
			)
		}
	}

	h.logger.Info("policy deleted via intent",
		zap.String("policy_id", id.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// createPolicy creates a new policy and publishes canonical state.
func (h *PolicyIntentHandler) createPolicy(ctx context.Context, policy *domain.DeploymentPolicy, intent *Intent) error {
	replayed, err := h.policies.CreatePolicy(ctx, policy)
	if err != nil {
		return fmt.Errorf("create policy: %w", err)
	}

	if !replayed && h.publish != nil {
		if err := h.publish(ctx, policy, false); err != nil {
			return fmt.Errorf("policy created but publication failed: %w", err)
		}
	}

	h.logger.Info("policy created via intent",
		zap.String("policy_id", policy.ID.String()),
		zap.String("intent_id", intent.IntentID),
		zap.Bool("replayed", replayed),
	)
	return nil
}

// updatePolicy updates an existing policy and publishes canonical state.
func (h *PolicyIntentHandler) updatePolicy(ctx context.Context, policy *domain.DeploymentPolicy, intent *Intent) error {
	if err := h.policies.UpdatePolicy(ctx, policy); err != nil {
		return fmt.Errorf("update policy: %w", err)
	}

	if h.publish != nil {
		if err := h.publish(ctx, policy, false); err != nil {
			return fmt.Errorf("policy updated but publication failed: %w", err)
		}
	}

	h.logger.Info("policy updated via intent",
		zap.String("policy_id", policy.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// updateWithRevision updates a policy with expected_updated_at revision check.
func (h *PolicyIntentHandler) updateWithRevision(ctx context.Context, policy *domain.DeploymentPolicy, intent *Intent) error {
	existing, _ := h.policies.GetPolicy(ctx, policy.ID)
	if existing == nil {
		if h.status != nil {
			h.status.PublishConflict(ctx, intent)
		}
		return fmt.Errorf("policy %s not found for revisioned update", policy.ID)
	}

	// Check revision.
	expectedTime := *intent.ExpectedUpdatedAt
	if !intent.RevisionMatches(existing.UpdatedAt) {
		if h.status != nil {
			h.status.PublishConflict(ctx, intent)
		}
		return &revisionConflictError{
			entityID: policy.ID,
			expected: expectedTime,
			actual:   existing.UpdatedAt,
		}
	}

	mergePolicyOntoExisting(existing, policy)
	return h.updatePolicy(ctx, existing, intent)
}

// policyFromIntentContent parses intent content into a domain.DeploymentPolicy.
func policyFromIntentContent(intent *Intent) (*domain.DeploymentPolicy, error) {
	content := intent.Content
	if content == nil {
		return nil, fmt.Errorf("intent content is nil")
	}

	policy := &domain.DeploymentPolicy{}

	// Parse ID from content or coordinate.
	if idStr, ok := content["id"].(string); ok && idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			return nil, fmt.Errorf("invalid policy id %q: %w", idStr, err)
		}
		policy.ID = id
	}
	if policy.ID == uuid.Nil {
		id, err := uuid.Parse(intent.Coordinate)
		if err != nil {
			return nil, fmt.Errorf("cannot derive policy id from coordinate %q: %w", intent.Coordinate, err)
		}
		policy.ID = id
	}

	// Name.
	if name, ok := content["name"].(string); ok {
		policy.Name = strings.TrimSpace(name)
	}

	// Enforcement.
	if enforcement, ok := content["enforcement"].(string); ok && enforcement != "" {
		policy.Enforcement = domain.PolicyEnforcement(strings.TrimSpace(enforcement))
	}
	if policy.Enforcement == "" {
		policy.Enforcement = domain.PolicyEnforcementWarn
	}

	// Enabled.
	if enabled, ok := content["enabled"].(bool); ok {
		policy.Enabled = enabled
	}

	// Environment ID.
	if envIDStr, ok := content["environment_id"].(string); ok && envIDStr != "" {
		envID, err := uuid.Parse(envIDStr)
		if err == nil && envID != uuid.Nil {
			policy.EnvironmentID = &envID
		}
	}

	// Rules.
	if rulesRaw, ok := content["rules"]; ok && rulesRaw != nil {
		rulesJSON, err := json.Marshal(rulesRaw)
		if err != nil {
			return nil, fmt.Errorf("marshal rules: %w", err)
		}
		var rules []domain.PolicyRule
		if err := json.Unmarshal(rulesJSON, &rules); err != nil {
			return nil, fmt.Errorf("parse rules: %w", err)
		}
		policy.Rules = rules
	}

	return policy, nil
}

// mergePolicyOntoExisting applies the intent's desired state fields onto the
// loaded entity. Every non-zero field replaces the existing one (docs/architecture/intents-and-authority.md).
func mergePolicyOntoExisting(existing, intent *domain.DeploymentPolicy) {
	if intent.Name != "" {
		existing.Name = intent.Name
	}
	if intent.Enforcement != "" {
		existing.Enforcement = intent.Enforcement
	}
	if intent.Rules != nil {
		existing.Rules = intent.Rules
	}
	if intent.EnvironmentID != nil {
		existing.EnvironmentID = intent.EnvironmentID
	}
	// Enabled is a bool; always apply from intent since it could be false.
	existing.Enabled = intent.Enabled
}

// PolicyRegistryRecord builds the canonical kind-30900 cp-state tags and
// content JSON for a deployment policy mutation. This is the shared builder
// used by both the intent handler (via PolicyStatePublisher) and the
// warm-start comparison.
func PolicyRegistryRecord(policy *domain.DeploymentPolicy, deleted bool) (nostr.Tags, string) {
	content := map[string]any{
		"deleted":    deleted,
		"id":         policy.ID.String(),
		"updated_at": policy.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	tags := nostr.Tags{{"policy", policy.ID.String()}}
	if !deleted {
		content["name"] = policy.Name
		content["environment_id"] = nil
		if policy.EnvironmentID != nil {
			content["environment_id"] = policy.EnvironmentID.String()
			tags = append(tags, nostr.Tag{"environment", policy.EnvironmentID.String()})
		}
		content["rules"] = policy.Rules
		content["rule_count"] = len(policy.Rules)
		content["enforcement"] = string(policy.Enforcement)
		content["enabled"] = policy.Enabled
		content["created_at"] = policy.CreatedAt.UTC().Format(time.RFC3339Nano)
		tags = append(tags,
			nostr.Tag{"name", policy.Name},
			nostr.Tag{"enabled", fmt.Sprintf("%t", policy.Enabled)},
			nostr.Tag{"enforcement", string(policy.Enforcement)},
		)
	}
	contentJSON, _ := json.Marshal(content)
	return tags, string(contentJSON)
}
