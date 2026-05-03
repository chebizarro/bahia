package soulfactory

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// BahiaIntegration bridges Soul Factory with bahia's deployment registry.
// It handles service registration, deployment intents, status sync, and lifecycle actions.
type BahiaIntegration struct {
	registry   *service.RegistryService
	agentEnvID uuid.UUID // Default environment for agents
	logger     *slog.Logger
}

// BahiaIntegrationConfig holds configuration for bahia integration.
type BahiaIntegrationConfig struct {
	// AgentEnvironmentID is the default environment UUID for deploying agents.
	// If empty, agents are registered as services but not auto-deployed.
	AgentEnvironmentID string
}

// NewBahiaIntegration creates a new bahia integration.
func NewBahiaIntegration(registry *service.RegistryService, config BahiaIntegrationConfig, logger *slog.Logger) (*BahiaIntegration, error) {
	bi := &BahiaIntegration{
		registry: registry,
		logger:   logger,
	}

	if config.AgentEnvironmentID != "" {
		envID, err := uuid.Parse(config.AgentEnvironmentID)
		if err != nil {
			return nil, fmt.Errorf("invalid agent environment ID: %w", err)
		}
		bi.agentEnvID = envID
	}

	return bi, nil
}

// RegisterSoulAsService creates a bahia Service entry for a newly provisioned soul.
// Returns the service ID on success.
func (bi *BahiaIntegration) RegisterSoulAsService(ctx context.Context, soul *domain.AgentSoul) (uuid.UUID, error) {
	logger := bi.logger.With("agent_id", soul.AgentID, "soul_id", soul.ID)
	logger.Info("registering soul as bahia service")

	// Build service entry
	svc := &domain.Service{
		ID:            uuid.New(),
		Name:          fmt.Sprintf("agent-%s", soul.AgentID),
		RepoURL:       soul.WorkspaceRepoURL,
		ArtifactRepo:  fmt.Sprintf("agents/%s", soul.AgentID),
		DefaultBranch: "main",
		RuntimeType:   bi.getRuntimeType(soul.Tier),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	if err := bi.registry.CreateService(ctx, svc); err != nil {
		return uuid.Nil, fmt.Errorf("create service: %w", err)
	}

	logger.Info("registered soul as service",
		"service_id", svc.ID,
		"service_name", svc.Name,
	)

	return svc.ID, nil
}

// CreateInitialDeployment creates a deployment intent for a newly provisioned soul.
// This is called after the soul and service are created.
func (bi *BahiaIntegration) CreateInitialDeployment(ctx context.Context, soul *domain.AgentSoul, serviceID uuid.UUID) (*domain.DeploymentIntent, error) {
	logger := bi.logger.With("agent_id", soul.AgentID, "service_id", serviceID)

	if bi.agentEnvID == uuid.Nil {
		logger.Debug("no agent environment configured, skipping initial deployment")
		return nil, nil
	}

	return nil, ErrDeployableArtifactRequired

	// First, we need to register the agent's "artifact" (container image)
	artifact := &domain.Artifact{
		ID:                uuid.New(),
		ServiceID:         serviceID,
		ImageRepo:         fmt.Sprintf("agents/%s", soul.AgentID),
		ImageTag:          "latest",
		ImageDigest:       "",
		ManifestMediaType: "application/vnd.oci.image.manifest.v1+json",
		ScanStatus:        domain.ScanStatusUnknown,
		Metadata: map[string]interface{}{
			"soul_id":   soul.ID.String(),
			"agent_id":  soul.AgentID,
			"tier":      string(soul.Tier),
			"npub":      soul.NostrNpub,
			"soul_type": "provisioned",
		},
		CreatedAt: time.Now().UTC(),
	}

	if err := bi.registry.RegisterArtifact(ctx, artifact); err != nil {
		// If artifact registration fails due to verification, log but don't fail
		// (image may not exist yet if this is a new agent)
		logger.Warn("artifact registration failed (image may not exist yet)", "error", err)
	}

	// Create deployment intent
	intent := &domain.DeploymentIntent{
		ID:             uuid.New(),
		ServiceID:      serviceID,
		EnvironmentID:  bi.agentEnvID,
		ArtifactID:     artifact.ID,
		RequestedBy:    "soul-factory",
		SourceKind:     domain.SourceKindEventTriggered,
		ApprovalStatus: domain.ApprovalStatusNotRequired, // Auto-approve for agents
		Status:         domain.IntentStatusApproved,
		Metadata: map[string]interface{}{
			"soul_id":  soul.ID.String(),
			"agent_id": soul.AgentID,
			"source":   "soul-factory-provision",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := bi.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		return nil, fmt.Errorf("create deployment intent: %w", err)
	}

	logger.Info("created initial deployment intent",
		"intent_id", intent.ID,
		"environment_id", bi.agentEnvID,
	)

	return intent, nil
}

// SyncSoulStatus synchronizes bahia deployment status back to the soul.
// Returns the updated deploy status string.
func (bi *BahiaIntegration) SyncSoulStatus(ctx context.Context, soul *domain.AgentSoul) (string, error) {
	if soul.BahiaServiceID == nil {
		return "", nil
	}

	logger := bi.logger.With("agent_id", soul.AgentID, "service_id", *soul.BahiaServiceID)

	if bi.agentEnvID == uuid.Nil {
		return "unmanaged", nil
	}

	// Get current deployment state
	state, err := bi.registry.GetEnvironmentServiceState(ctx, *soul.BahiaServiceID, bi.agentEnvID)
	if err != nil {
		return "", fmt.Errorf("get environment state: %w", err)
	}

	if state == nil {
		return "pending", nil
	}

	// Map bahia states to soul deploy status
	var deployStatus string
	switch {
	case state.DriftStatus == domain.DriftStatusInSync:
		deployStatus = "deployed"
	case state.DriftStatus == domain.DriftStatusDeploying:
		deployStatus = "deploying"
	case state.DriftStatus == domain.DriftStatusDrifted:
		deployStatus = "drifted"
	default:
		deployStatus = "unknown"
	}

	// Check health from latest observation
	if state.CurrentObservationID != nil {
		obs, err := bi.registry.GetLatestObservation(ctx, *soul.BahiaServiceID, bi.agentEnvID)
		if err != nil {
			logger.Warn("failed to get latest observation", "error", err)
		} else if obs != nil {
			switch obs.HealthStatus {
			case domain.HealthStatusHealthy:
				if deployStatus == "deployed" {
					deployStatus = "healthy"
				}
			case domain.HealthStatusUnhealthy:
				deployStatus = "unhealthy"
			case domain.HealthStatusStopped:
				deployStatus = "stopped"
			}
		}
	}

	logger.Debug("synced soul status", "deploy_status", deployStatus)
	return deployStatus, nil
}

// HandleLifecycleAction handles soul lifecycle actions by updating bahia accordingly.
func (bi *BahiaIntegration) HandleLifecycleAction(ctx context.Context, soul *domain.AgentSoul, action domain.SoulActionType) error {
	if soul.BahiaServiceID == nil {
		bi.logger.Debug("no bahia service ID for lifecycle action", "action", action)
		return ErrLifecycleUnsupported
	}

	logger := bi.logger.With("agent_id", soul.AgentID, "service_id", *soul.BahiaServiceID, "action", action)
	logger.Info("handling soul lifecycle action")

	switch action {
	case domain.SoulActionSuspend:
		return bi.suspendDeployment(ctx, soul)
	case domain.SoulActionResume:
		return bi.resumeDeployment(ctx, soul)
	case domain.SoulActionRevoke:
		return bi.revokeDeployment(ctx, soul)
	case domain.SoulActionRedeploy:
		return bi.redeployAgent(ctx, soul)
	default:
		logger.Debug("unhandled lifecycle action")
		return nil
	}
}

// suspendDeployment pauses the agent's deployment.
func (bi *BahiaIntegration) suspendDeployment(ctx context.Context, soul *domain.AgentSoul) error {
	return ErrLifecycleUnsupported

	logger := bi.logger.With("agent_id", soul.AgentID)

	// For now, we just update metadata to indicate suspended state.
	// In a full implementation, this would:
	// 1. Scale down replicas to 0
	// 2. Update runtime config
	// 3. Publish suspension event

	svc, err := bi.registry.GetService(ctx, *soul.BahiaServiceID)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	if svc == nil {
		return fmt.Errorf("service not found: %s", *soul.BahiaServiceID)
	}

	// Update service metadata to indicate suspended
	// Note: In production, this would trigger actual runtime changes
	logger.Info("suspended agent deployment", "service_name", svc.Name)
	return nil
}

// resumeDeployment resumes a suspended agent's deployment.
func (bi *BahiaIntegration) resumeDeployment(ctx context.Context, soul *domain.AgentSoul) error {
	return ErrLifecycleUnsupported

	logger := bi.logger.With("agent_id", soul.AgentID)

	svc, err := bi.registry.GetService(ctx, *soul.BahiaServiceID)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	if svc == nil {
		return fmt.Errorf("service not found: %s", *soul.BahiaServiceID)
	}

	// Resume deployment
	// Note: In production, this would trigger actual runtime changes
	logger.Info("resumed agent deployment", "service_name", svc.Name)
	return nil
}

// revokeDeployment terminates and removes an agent's deployment.
func (bi *BahiaIntegration) revokeDeployment(ctx context.Context, soul *domain.AgentSoul) error {
	return ErrLifecycleUnsupported

	logger := bi.logger.With("agent_id", soul.AgentID)

	// For revocation, we don't delete the service (audit trail)
	// but we mark it as terminated and stop all deployments.

	svc, err := bi.registry.GetService(ctx, *soul.BahiaServiceID)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	if svc == nil {
		logger.Warn("service not found for revocation")
		return nil
	}

	// In production, this would:
	// 1. Stop all running deployments
	// 2. Revoke NIP-46 access in Signet
	// 3. Clean up resources (optionally)

	logger.Info("revoked agent deployment", "service_name", svc.Name)
	return nil
}

// redeployAgent triggers a fresh deployment for the agent.
func (bi *BahiaIntegration) redeployAgent(ctx context.Context, soul *domain.AgentSoul) error {
	return ErrLifecycleUnsupported

	logger := bi.logger.With("agent_id", soul.AgentID)

	if bi.agentEnvID == uuid.Nil {
		logger.Debug("no agent environment configured, skipping redeploy")
		return nil
	}

	// Create a new deployment intent to trigger redeploy
	_, err := bi.CreateInitialDeployment(ctx, soul, *soul.BahiaServiceID)
	if err != nil {
		return fmt.Errorf("create redeploy intent: %w", err)
	}

	logger.Info("triggered agent redeployment")
	return nil
}

// getRuntimeType maps soul tier to bahia runtime type.
func (bi *BahiaIntegration) getRuntimeType(tier domain.SoulTier) domain.RuntimeType {
	switch tier {
	case domain.SoulTierLightweight:
		return domain.RuntimeTypeDocker
	case domain.SoulTierStandard:
		return domain.RuntimeTypeCompose
	case domain.SoulTierHeavy:
		return domain.RuntimeTypeK8s
	default:
		return domain.RuntimeTypeDocker
	}
}

// EnsureAgentEnvironment creates the default agent environment if it doesn't exist.
// Returns the environment ID.
func (bi *BahiaIntegration) EnsureAgentEnvironment(ctx context.Context) (uuid.UUID, error) {
	const envName = "agents"

	// Try to get existing environment
	existing, err := bi.registry.GetEnvironmentByName(ctx, envName)
	if err != nil {
		return uuid.Nil, fmt.Errorf("lookup environment: %w", err)
	}
	if existing != nil {
		bi.agentEnvID = existing.ID
		return existing.ID, nil
	}

	// Create the agents environment
	env := &domain.Environment{
		ID:             uuid.New(),
		Name:           envName,
		DeployStrategy: domain.DeployStrategyReplace,
		Protected:      false, // Auto-approve agent deployments
		RuntimeConfig: map[string]interface{}{
			"type":        "agents",
			"description": "Default environment for Soul Factory agents",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := bi.registry.CreateEnvironment(ctx, env); err != nil {
		return uuid.Nil, fmt.Errorf("create environment: %w", err)
	}

	bi.agentEnvID = env.ID
	bi.logger.Info("created agents environment", "environment_id", env.ID)
	return env.ID, nil
}
