package soulfactory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// BahiaIntegration bridges Soul Factory with bahia's deployment registry.
// It handles service registration, deployment intents, status sync, and lifecycle actions.
type BahiaIntegration struct {
	registry               *service.RegistryService
	agentEnvID             uuid.UUID // Default environment for agents
	deployRuntimeArtifacts bool
	logger                 *slog.Logger
}

// BahiaIntegrationConfig holds configuration for bahia integration.
type BahiaIntegrationConfig struct {
	// AgentEnvironmentID is the default environment UUID for deploying agents.
	// If empty, agents are registered as services but not auto-deployed.
	AgentEnvironmentID string
	// DeployRuntimeArtifacts is an explicit opt-in for turning runtime-supplied
	// deployable artifact metadata into Bahia build/artifact/deployment intents.
	DeployRuntimeArtifacts bool
}

// NewBahiaIntegration creates a new bahia integration.
func NewBahiaIntegration(registry *service.RegistryService, config BahiaIntegrationConfig, logger *slog.Logger) (*BahiaIntegration, error) {
	bi := &BahiaIntegration{
		registry:               registry,
		deployRuntimeArtifacts: config.DeployRuntimeArtifacts,
		logger:                 logger,
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

// CreateInitialDeployment creates a deployment intent for a newly provisioned soul
// only when explicit Bahia config opts in and the runtime returned a real
// deployable artifact reference containing an image digest.
func (bi *BahiaIntegration) CreateInitialDeployment(ctx context.Context, soul *domain.AgentSoul, serviceID uuid.UUID, runtimeResult *RuntimeControlResultEnvelope) (*domain.DeploymentIntent, error) {
	logger := bi.logger.With("agent_id", soul.AgentID, "service_id", serviceID)

	if bi.agentEnvID == uuid.Nil {
		logger.Debug("no agent environment configured, skipping initial deployment")
		return nil, nil
	}
	if !bi.deployRuntimeArtifacts {
		logger.Debug("runtime artifact deployment opt-in disabled, skipping initial deployment")
		return nil, nil
	}
	deployable := runtimeDeployableArtifactFromResult(runtimeResult)
	if deployable == nil {
		return nil, ErrDeployableArtifactRequired
	}
	if err := domain.ValidateImageDigest(deployable.ImageDigest); err != nil {
		return nil, fmt.Errorf("%w: invalid runtime artifact digest: %v", ErrDeployableArtifactRequired, err)
	}
	svc, err := bi.registry.GetService(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("get service: %w", err)
	}
	if svc == nil {
		return nil, fmt.Errorf("service not found: %s", serviceID)
	}

	artifact, err := bi.registry.GetArtifactByDigest(ctx, deployable.ImageRepo, deployable.ImageDigest)
	if err != nil {
		return nil, fmt.Errorf("lookup runtime artifact: %w", err)
	}
	if artifact != nil && artifact.ServiceID != serviceID {
		return nil, fmt.Errorf("runtime artifact %s belongs to service %s, not %s", artifact.ID, artifact.ServiceID, serviceID)
	}
	if artifact == nil {
		build := &domain.Build{
			ID:            uuid.New(),
			ServiceID:     serviceID,
			GitSHA:        deployable.GitSHA,
			GitRef:        deployable.GitRef,
			CISystem:      firstNonEmpty(deployable.CISystem, "soul-factory-runtime"),
			CIRunID:       firstNonEmpty(deployable.CIRunID, runtimeResult.RequestEvent, runtimeResult.IdempotencyKey),
			Status:        domain.BuildStatusSucceeded,
			SourceEventID: firstNonEmpty(deployable.SourceEventID, runtimeEventID(runtimeResult), runtimeResult.RequestEvent),
			Metadata:      runtimeProjectionMetadata(soul, runtimeResult, deployable.Metadata),
			CreatedAt:     time.Now().UTC(),
		}
		if err := bi.registry.RegisterBuild(ctx, build); err != nil {
			return nil, fmt.Errorf("register runtime build projection: %w", err)
		}

		artifact = &domain.Artifact{
			ID:                uuid.New(),
			BuildID:           build.ID,
			ServiceID:         serviceID,
			ImageRepo:         deployable.ImageRepo,
			ImageTag:          deployable.ImageTag,
			ImageDigest:       deployable.ImageDigest,
			ManifestMediaType: firstNonEmpty(deployable.ManifestMediaType, "application/vnd.oci.image.manifest.v1+json"),
			SizeBytes:         deployable.SizeBytes,
			SBOMURL:           deployable.SBOMURL,
			SignatureRef:      deployable.SignatureRef,
			ScanStatus:        domain.ScanStatusUnknown,
			Metadata:          runtimeProjectionMetadata(soul, runtimeResult, deployable.Metadata),
			CreatedAt:         time.Now().UTC(),
		}
		if err := bi.registry.RegisterArtifact(ctx, artifact); err != nil {
			return nil, fmt.Errorf("register runtime artifact projection: %w", err)
		}
	}

	intent := &domain.DeploymentIntent{
		ID:             uuid.New(),
		ServiceID:      serviceID,
		EnvironmentID:  bi.agentEnvID,
		ArtifactID:     artifact.ID,
		RequestedBy:    "soul-factory",
		SourceKind:     domain.SourceKindEventTriggered,
		ApprovalStatus: domain.ApprovalStatusNotRequired,
		Status:         domain.IntentStatusApproved,
		Metadata:       runtimeProjectionMetadata(soul, runtimeResult, deployable.Metadata),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	if err := bi.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		return nil, fmt.Errorf("create deployment intent: %w", err)
	}

	logger.Info("created initial deployment intent from runtime artifact",
		"intent_id", intent.ID,
		"environment_id", bi.agentEnvID,
		"image_repo", deployable.ImageRepo,
		"image_digest", deployable.ImageDigest,
	)

	return intent, nil
}

type runtimeDeployableArtifact struct {
	ImageRepo         string
	ImageTag          string
	ImageDigest       string
	ManifestMediaType string
	SizeBytes         *int64
	SBOMURL           string
	SignatureRef      string
	GitSHA            string
	GitRef            string
	CISystem          string
	CIRunID           string
	SourceEventID     string
	Metadata          map[string]any
}

func runtimeDeployableArtifactFromResult(result *RuntimeControlResultEnvelope) *runtimeDeployableArtifact {
	if result == nil || len(result.Result) == 0 {
		return nil
	}
	artifactFields := nestedRuntimeResultMap(result.Result, "artifact", "deployable_artifact", "runtime_artifact")
	if artifactFields == nil {
		return nil
	}
	buildFields := nestedRuntimeResultMap(result.Result, "build")
	if buildFields == nil {
		buildFields = result.Result
	}
	imageRepo := runtimeStringField(artifactFields, "image_repo", "repo", "repository")
	imageDigest := runtimeStringField(artifactFields, "image_digest", "digest")
	if imageRepo == "" || imageDigest == "" {
		return nil
	}
	metadata := map[string]any{}
	for key, value := range artifactFields {
		metadata[key] = value
	}
	return &runtimeDeployableArtifact{
		ImageRepo:         imageRepo,
		ImageTag:          runtimeStringField(artifactFields, "image_tag", "tag"),
		ImageDigest:       imageDigest,
		ManifestMediaType: runtimeStringField(artifactFields, "manifest_media_type", "media_type"),
		SizeBytes:         runtimeInt64Pointer(artifactFields, "size_bytes", "size"),
		SBOMURL:           runtimeStringField(artifactFields, "sbom_url"),
		SignatureRef:      runtimeStringField(artifactFields, "signature_ref"),
		GitSHA:            runtimeStringField(buildFields, "git_sha", "revision"),
		GitRef:            runtimeStringField(buildFields, "git_ref", "ref"),
		CISystem:          runtimeStringField(buildFields, "ci_system", "system"),
		CIRunID:           runtimeStringField(buildFields, "ci_run_id", "run_id"),
		SourceEventID:     runtimeStringField(buildFields, "source_event_id", "event_id"),
		Metadata:          metadata,
	}
}

func nestedRuntimeResultMap(fields map[string]interface{}, keys ...string) map[string]interface{} {
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || value == nil {
			continue
		}
		if nested, ok := value.(map[string]interface{}); ok {
			return nested
		}
	}
	return nil
}

func runtimeStringField(fields map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || value == nil {
			continue
		}
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			return text
		}
	}
	return ""
}

func runtimeInt64Pointer(fields map[string]interface{}, keys ...string) *int64 {
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case int64:
			return &typed
		case int:
			converted := int64(typed)
			return &converted
		case float64:
			converted := int64(typed)
			return &converted
		}
	}
	return nil
}

func runtimeProjectionMetadata(soul *domain.AgentSoul, result *RuntimeControlResultEnvelope, runtimeMetadata map[string]any) map[string]any {
	metadata := map[string]any{
		"agent_id": soul.AgentID,
		"soul_id":  soul.ID.String(),
		"source":   "soul-factory-runtime-result",
		"runtime":  string(soul.Runtime.Target),
	}
	if result != nil {
		metadata["runtime_method"] = result.Method
		metadata["runtime_status"] = result.Status
		metadata["runtime_request_event"] = result.RequestEvent
		metadata["runtime_idempotency_key"] = result.IdempotencyKey
		if eventID := runtimeEventID(result); eventID != "" {
			metadata["runtime_result_event"] = eventID
		}
	}
	for key, value := range runtimeMetadata {
		metadata[key] = value
	}
	return metadata
}

func runtimeEventID(result *RuntimeControlResultEnvelope) string {
	if result == nil || result.Event == nil {
		return ""
	}
	return strings.TrimSpace(result.Event.ID.Hex())
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
	return bi.recordLifecycleObservation(ctx, soul, domain.HealthStatusStopped, "suspend")
}

// resumeDeployment resumes a suspended agent's deployment.
func (bi *BahiaIntegration) resumeDeployment(ctx context.Context, soul *domain.AgentSoul) error {
	_, err := bi.createLifecycleDeploymentIntent(ctx, soul, "resume")
	return err
}

// revokeDeployment terminates and removes an agent's deployment.
func (bi *BahiaIntegration) revokeDeployment(ctx context.Context, soul *domain.AgentSoul) error {
	return bi.recordLifecycleObservation(ctx, soul, domain.HealthStatusStopped, "revoke")
}

// redeployAgent triggers a fresh deployment for the agent.
func (bi *BahiaIntegration) redeployAgent(ctx context.Context, soul *domain.AgentSoul) error {
	logger := bi.logger.With("agent_id", soul.AgentID)

	if bi.agentEnvID == uuid.Nil {
		logger.Debug("no agent environment configured, skipping redeploy")
		return nil
	}

	_, err := bi.createLifecycleDeploymentIntent(ctx, soul, "redeploy")
	if err != nil {
		return fmt.Errorf("create redeploy intent: %w", err)
	}

	logger.Info("triggered agent redeployment")
	return nil
}

func (bi *BahiaIntegration) createLifecycleDeploymentIntent(ctx context.Context, soul *domain.AgentSoul, operation string) (*domain.DeploymentIntent, error) {
	if soul.BahiaServiceID == nil {
		return nil, ErrLifecycleUnsupported
	}
	if bi.agentEnvID == uuid.Nil {
		return nil, nil
	}

	state, err := bi.registry.GetEnvironmentServiceState(ctx, *soul.BahiaServiceID, bi.agentEnvID)
	if err != nil {
		return nil, fmt.Errorf("get environment state: %w", err)
	}
	if state == nil || state.DesiredArtifactID == nil {
		return nil, nil
	}

	intent := &domain.DeploymentIntent{
		ID:                 uuid.New(),
		ServiceID:          *soul.BahiaServiceID,
		EnvironmentID:      bi.agentEnvID,
		ArtifactID:         *state.DesiredArtifactID,
		RequestedBy:        "soul-factory",
		SourceKind:         domain.SourceKindEventTriggered,
		ApprovalStatus:     domain.ApprovalStatusNotRequired,
		Status:             domain.IntentStatusApproved,
		SupersedesIntentID: state.DesiredIntentID,
		Metadata: map[string]any{
			"agent_id":  soul.AgentID,
			"soul_id":   soul.ID.String(),
			"source":    "soul-factory-lifecycle",
			"operation": operation,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := bi.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		return nil, fmt.Errorf("create %s deployment intent: %w", operation, err)
	}
	return intent, nil
}

func (bi *BahiaIntegration) recordLifecycleObservation(ctx context.Context, soul *domain.AgentSoul, health domain.HealthStatus, operation string) error {
	if soul.BahiaServiceID == nil {
		return ErrLifecycleUnsupported
	}
	if bi.agentEnvID == uuid.Nil {
		return nil
	}

	var desiredDigest string
	var desiredRepo string
	state, err := bi.registry.GetEnvironmentServiceState(ctx, *soul.BahiaServiceID, bi.agentEnvID)
	if err != nil {
		return fmt.Errorf("get environment state: %w", err)
	}
	if state != nil && state.DesiredArtifactID != nil {
		artifact, err := bi.registry.GetArtifact(ctx, *state.DesiredArtifactID)
		if err != nil {
			return fmt.Errorf("get desired artifact: %w", err)
		}
		if artifact != nil {
			desiredDigest = artifact.ImageDigest
			desiredRepo = artifact.ImageRepo
		}
	}

	obs := &domain.RuntimeObservation{
		ID:                  uuid.New(),
		ServiceID:           *soul.BahiaServiceID,
		EnvironmentID:       bi.agentEnvID,
		ObservedImageDigest: desiredDigest,
		ObservedImageRepo:   desiredRepo,
		HealthStatus:        health,
		Source:              "soul-factory",
		Metadata: map[string]any{
			"agent_id":  soul.AgentID,
			"soul_id":   soul.ID.String(),
			"operation": operation,
		},
		ObservedAt: time.Now().UTC(),
	}
	if err := bi.registry.RecordObservation(ctx, obs); err != nil {
		return fmt.Errorf("record %s observation: %w", operation, err)
	}
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
