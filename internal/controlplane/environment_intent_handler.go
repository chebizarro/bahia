package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// EnvironmentIntentHandler processes kind-30900 intents for the "environment"
// domain family. It is level-triggered: the intent's content is the full
// desired state, and the handler reconciles the entity toward it regardless
// of whether prior events for the coordinate have been seen.
//
// Registered at startup when "environment" is enabled via
// IntentProcessor.RegisterHandler("environment", handler).
//
// See docs/architecture/intents-and-authority.md.
type EnvironmentIntentHandler struct {
	registry       service.EnvironmentIntentRegistry
	workers        repository.WorkerRepository
	statePublisher service.RelayFirstStatePublisher
	logger         *zap.Logger
}

// NewEnvironmentIntentHandler constructs the handler. registry is the
// environment CRUD backend (service.RegistryService or RelayFirstRegistry).
// statePublisher publishes the canonical 30900 record via PublishBeforeCommit.
func NewEnvironmentIntentHandler(
	registry service.EnvironmentIntentRegistry,
	statePublisher service.RelayFirstStatePublisher,
	logger *zap.Logger,
	workers ...repository.WorkerRepository,
) *EnvironmentIntentHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	h := &EnvironmentIntentHandler{
		registry:       registry,
		statePublisher: statePublisher,
		logger:         logger.Named("env-intent-handler"),
	}
	if len(workers) > 0 {
		h.workers = workers[0]
	}
	return h
}

// HandleIntent processes a single environment intent. The processor has
// already deduplicated, validated, and authorized the intent.
func (h *EnvironmentIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "create":
		return h.handleCreate(ctx, intent)
	case "update":
		return h.handleUpdate(ctx, intent)
	case "delete":
		return h.handleDelete(ctx, intent)
	case "worker-policy-apply":
		return h.handleWorkerPolicyApply(ctx, intent)
	default:
		// Level-triggered: unknown op defaults to upsert.
		return h.handleUpdate(ctx, intent)
	}
}

func (h *EnvironmentIntentHandler) handleWorkerPolicyApply(ctx context.Context, intent *Intent) error {
	var payload struct {
		EnvironmentID string         `json:"environment_id"`
		Policy        map[string]any `json:"policy"`
	}
	if err := decodeIntentContent(intent.Content, &payload); err != nil {
		return err
	}
	id, err := uuid.Parse(strings.TrimSpace(payload.EnvironmentID))
	if err != nil || id == uuid.Nil || intent.Coordinate != id.String() || payload.Policy == nil {
		return fmt.Errorf("worker-policy-apply requires matching environment_id, coordinate, and policy")
	}
	env, err := h.registry.GetEnvironment(ctx, id)
	if err != nil {
		return err
	}
	if env == nil || env.OrgID != intent.OrgID {
		return fmt.Errorf("environment must belong to the authorized organization")
	}
	if err := checkIntentRevision(intent, id.String(), true, env.UpdatedAt); err != nil {
		return err
	}
	policy := sanitizeWorkerPolicy(payload.Policy)
	if pinned := pinnedWorkerFromPolicy(policy); pinned != "" {
		if !isHexNostrPubKey(pinned) {
			return fmt.Errorf("pinned_worker must be a 32-byte lowercase hex Nostr public key")
		}
		if h.workers == nil {
			return fmt.Errorf("worker repository is not configured")
		}
		worker, err := h.workers.GetByPubKey(ctx, pinned)
		if err != nil {
			return err
		}
		if worker == nil {
			return fmt.Errorf("pinned worker not found")
		}
	}
	updated := *env
	updated.RuntimeConfig = make(map[string]any, len(env.RuntimeConfig)+1)
	for key, value := range env.RuntimeConfig {
		updated.RuntimeConfig[key] = value
	}
	updated.RuntimeConfig["worker_policy"] = policy
	if err := h.registry.UpdateEnvironment(ctx, &updated); err != nil {
		return fmt.Errorf("update environment worker policy: %w", err)
	}
	intent.Result = map[string]any{"environment_id": id.String(), "status": "applied"}
	intent.StatusData = intent.Result
	return nil
}

// PermissionFor returns the permission required for the given operation.
func (h *EnvironmentIntentHandler) PermissionFor(op string) domain.Permission {
	switch op {
	case "delete":
		return domain.PermWriteEnvironments
	default:
		return domain.PermWriteEnvironments
	}
}

func (*EnvironmentIntentHandler) IsFleetScopedOperation(op string) bool {
	return op == "worker-policy-apply"
}

func (h *EnvironmentIntentHandler) handleCreate(ctx context.Context, intent *Intent) error {
	env, units, err := environmentFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("invalid environment intent content: %w", err)
	}

	if units != nil {
		if err := h.registry.CreateEnvironmentWithDeploymentUnits(ctx, env, units); err != nil {
			return fmt.Errorf("creating environment with deployment units: %w", err)
		}
	} else {
		if err := h.registry.CreateEnvironment(ctx, env); err != nil {
			return fmt.Errorf("creating environment: %w", err)
		}
	}

	h.logger.Info("environment created via intent",
		zap.String("environment_id", env.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

func (h *EnvironmentIntentHandler) handleUpdate(ctx context.Context, intent *Intent) error {
	env, units, err := environmentFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("invalid environment intent content: %w", err)
	}

	// Level-triggered: if the environment doesn't exist yet, create it.
	existing, existErr := h.registry.GetEnvironment(ctx, env.ID)
	if existErr != nil || existing == nil {
		// Entity not found — level-triggered create.
		return h.handleCreate(ctx, intent)
	}
	if existing.OrgID != intent.OrgID {
		return fmt.Errorf("environment %s belongs to a different organization", env.ID)
	}

	// Check expected_updated_at revision if provided.
	if intent.ExpectedUpdatedAt != nil {
		if !intent.RevisionMatches(existing.UpdatedAt) {
			// Revision conflict — publish conflict status and return error.
			return &revisionConflictError{
				entityID: env.ID,
				expected: *intent.ExpectedUpdatedAt,
				actual:   existing.UpdatedAt,
			}
		}
	}

	if units != nil {
		expectedUpdatedAt := existing.UpdatedAt
		if intent.ExpectedUpdatedAt != nil {
			expectedUpdatedAt = *intent.ExpectedUpdatedAt
		}
		if err := h.registry.UpdateEnvironmentWithDeploymentUnits(ctx, env, units, expectedUpdatedAt); err != nil {
			return fmt.Errorf("updating environment with deployment units: %w", err)
		}
	} else {
		if err := h.registry.UpdateEnvironment(ctx, env); err != nil {
			return fmt.Errorf("updating environment: %w", err)
		}
	}

	h.logger.Info("environment updated via intent",
		zap.String("environment_id", env.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

func (h *EnvironmentIntentHandler) handleDelete(ctx context.Context, intent *Intent) error {
	envID, err := environmentIDFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("invalid environment delete intent: %w", err)
	}

	force := false
	if v, ok := intent.Content["force"]; ok {
		if b, ok := v.(bool); ok {
			force = b
		}
	}

	if err := h.registry.DeleteEnvironment(ctx, envID, force); err != nil {
		return fmt.Errorf("deleting environment: %w", err)
	}

	h.logger.Info("environment deleted via intent",
		zap.String("environment_id", envID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// environmentFromIntentContent parses an intent's content JSON into a
// domain.Environment and optional deployment units. The content must be
// the complete desired state (level-triggered).
func environmentFromIntentContent(intent *Intent) (*domain.Environment, []*domain.DeploymentUnit, error) {
	if intent == nil || intent.Content == nil {
		return nil, nil, fmt.Errorf("intent content is nil")
	}

	// Re-marshal and unmarshal for type safety.
	contentJSON, err := json.Marshal(intent.Content)
	if err != nil {
		return nil, nil, fmt.Errorf("marshalling intent content: %w", err)
	}

	var payload environmentIntentPayload
	if err := json.Unmarshal(contentJSON, &payload); err != nil {
		return nil, nil, fmt.Errorf("parsing intent content: %w", err)
	}

	envID, err := uuid.Parse(payload.ID)
	if err != nil || envID == uuid.Nil {
		return nil, nil, fmt.Errorf("invalid or missing id in intent content")
	}

	name := strings.TrimSpace(payload.Name)
	if name == "" && intent.Op != "delete" {
		return nil, nil, fmt.Errorf("name is required")
	}

	deployStrategy := domain.DeployStrategyReplace
	if s := strings.TrimSpace(payload.DeployStrategy); s != "" {
		deployStrategy = domain.DeployStrategy(s)
	}

	env := &domain.Environment{
		ID:                 envID,
		OrgID:              intent.OrgID,
		Name:               name,
		LoomWorkerSelector: payload.LoomWorkerSelector,
		RuntimeConfig:      payload.RuntimeConfig,
		DeployStrategy:     deployStrategy,
		Protected:          payload.Protected,
	}

	// Apply targeting.
	if payload.Targeting != nil {
		env.Targeting = domain.EnvironmentTargeting{
			DefaultUnitKey:       strings.TrimSpace(payload.Targeting.DefaultUnitKey),
			FailureDomainLabels:  payload.Targeting.FailureDomainLabels,
			SecretScopeMode:      domain.SecretScopeMode(strings.TrimSpace(payload.Targeting.SecretScopeMode)),
			DefaultReconcileMode: domain.ReconcileMode(strings.TrimSpace(payload.Targeting.DefaultReconcileMode)),
		}
	}

	// Apply reconcile_mode (top-level override).
	if rm := strings.TrimSpace(payload.ReconcileMode); rm != "" {
		env.Targeting.DefaultReconcileMode = domain.ReconcileMode(rm)
	}

	// Parse deployment units if present.
	var units []*domain.DeploymentUnit
	if payload.DeploymentUnits != nil {
		units = make([]*domain.DeploymentUnit, 0, len(payload.DeploymentUnits))
		for _, ur := range payload.DeploymentUnits {
			unit := &domain.DeploymentUnit{
				EnvironmentID:  envID,
				Key:            strings.TrimSpace(ur.Key),
				DisplayName:    strings.TrimSpace(ur.DisplayName),
				RuntimeType:    domain.RuntimeType(strings.TrimSpace(ur.RuntimeType)),
				EndpointRef:    strings.TrimSpace(ur.EndpointRef),
				ComposeDir:     strings.TrimSpace(ur.ComposeDir),
				Namespace:      strings.TrimSpace(ur.Namespace),
				NetworkProfile: ur.NetworkProfile,
				ReconcileMode:  domain.ReconcileMode(strings.TrimSpace(ur.ReconcileMode)),
				OwnershipMode:  domain.OwnershipMode(strings.TrimSpace(ur.OwnershipMode)),
				RuntimeConfig:  ur.RuntimeConfig,
			}
			if ur.GitSource != nil {
				unit.GitSource = &domain.GitSourceBinding{
					RepositoryURL: strings.TrimSpace(ur.GitSource.RepositoryURL),
					Ref:           strings.TrimSpace(ur.GitSource.Ref),
					Branch:        strings.TrimSpace(ur.GitSource.Branch),
					CommitSHA:     strings.TrimSpace(ur.GitSource.CommitSHA),
				}
			}
			units = append(units, unit)
		}
	}

	return env, units, nil
}

// environmentIDFromIntentContent extracts just the environment ID from a
// delete intent's content.
func environmentIDFromIntentContent(intent *Intent) (uuid.UUID, error) {
	if intent == nil || intent.Content == nil {
		return uuid.Nil, fmt.Errorf("intent content is nil")
	}
	raw, ok := intent.Content["id"]
	if !ok {
		return uuid.Nil, fmt.Errorf("missing id in delete intent content")
	}
	idStr, ok := raw.(string)
	if !ok {
		return uuid.Nil, fmt.Errorf("id must be a string")
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid id: %w", err)
	}
	return id, nil
}

// environmentIntentPayload is the JSON shape of an environment intent's
// content field. It mirrors the ContextVM payload structure.
type environmentIntentPayload struct {
	ID                 string                      `json:"id"`
	Name               string                      `json:"name"`
	LoomWorkerSelector map[string]any              `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      map[string]any              `json:"runtime_config,omitempty"`
	Targeting          *environmentIntentTargeting `json:"targeting,omitempty"`
	DeploymentUnits    []environmentIntentUnit     `json:"deployment_units,omitempty"`
	ReconcileMode      string                      `json:"reconcile_mode,omitempty"`
	DeployStrategy     string                      `json:"deploy_strategy,omitempty"`
	Protected          bool                        `json:"protected,omitempty"`
}

type environmentIntentTargeting struct {
	DefaultUnitKey       string            `json:"default_unit_key,omitempty"`
	FailureDomainLabels  map[string]string `json:"failure_domain_labels,omitempty"`
	SecretScopeMode      string            `json:"secret_scope_mode,omitempty"`
	DefaultReconcileMode string            `json:"default_reconcile_mode,omitempty"`
}

type environmentIntentUnit struct {
	Key            string                      `json:"key"`
	DisplayName    string                      `json:"display_name,omitempty"`
	RuntimeType    string                      `json:"runtime_type,omitempty"`
	EndpointRef    string                      `json:"endpoint_ref,omitempty"`
	ComposeDir     string                      `json:"compose_dir,omitempty"`
	Namespace      string                      `json:"namespace,omitempty"`
	NetworkProfile map[string]string           `json:"network_profile,omitempty"`
	GitSource      *environmentIntentGitSource `json:"git_source,omitempty"`
	ReconcileMode  string                      `json:"reconcile_mode,omitempty"`
	OwnershipMode  string                      `json:"ownership_mode,omitempty"`
	RuntimeConfig  map[string]any              `json:"runtime_config,omitempty"`
}

type environmentIntentGitSource struct {
	RepositoryURL string `json:"repository_url,omitempty"`
	Ref           string `json:"ref,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
}

// revisionConflictError signals an expected_updated_at mismatch. The intent
// processor uses this to publish a bounded conflict status.
type revisionConflictError struct {
	entityType string
	entityID   uuid.UUID
	expected   time.Time
	actual     time.Time
}

func (e *revisionConflictError) Error() string {
	entityType := e.entityType
	if entityType == "" {
		entityType = "environment"
	}
	return fmt.Sprintf(
		"%s %s revision conflict (expected %s, actual %s)",
		entityType, e.entityID,
		e.expected.UTC().Format(time.RFC3339Nano),
		e.actual.UTC().Format(time.RFC3339Nano),
	)
}

// IsRevisionConflict reports whether err is a revision conflict.
func IsRevisionConflict(err error) bool {
	if _, ok := err.(*intentReplayConflictError); ok {
		return true
	}
	if _, ok := err.(*intentStateConflictError); ok {
		return true
	}
	if _, ok := err.(*relayPolicyConflictError); ok {
		return true
	}
	if _, ok := err.(*revisionConflictError); ok {
		return true
	}
	_, ok := err.(*intentRevisionConflictError)
	return ok
}
