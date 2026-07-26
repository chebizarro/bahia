package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type EncryptedServiceHandlersConfig struct {
	Registry         *service.RegistryService
	RuntimeLifecycle *service.RuntimeLifecycleService
	Policy           *service.PolicyService
	Logger           *zap.Logger
}

type encryptedServiceHandlers struct {
	registry         *service.RegistryService
	runtimeLifecycle *service.RuntimeLifecycleService
	policy           *service.PolicyService
	logger           *zap.Logger
}

// RegisterServiceContextVMHandlers wires the signer-first service deployment
// method emitted by the operator CLI into the encrypted ContextVM runtime.
func RegisterServiceContextVMHandlers(transport *EncryptedRequestTransport, cfg EncryptedServiceHandlersConfig) {
	if transport == nil {
		return
	}
	h := &encryptedServiceHandlers{
		registry:         cfg.Registry,
		runtimeLifecycle: cfg.RuntimeLifecycle,
		policy:           cfg.Policy,
		logger:           cfg.Logger,
	}
	if h.logger == nil {
		h.logger = zap.NewNop()
	}
	transport.RegisterContextVMHandler(ContextVMMethodServiceDeploy, h.deploy)
}

func (h *encryptedServiceHandlers) deploy(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil || h.runtimeLifecycle == nil || h.policy == nil {
		return nil, fmt.Errorf("service deployment control plane is not configured")
	}
	var params struct {
		ServiceID     string `json:"service_id"`
		EnvironmentID string `json:"environment_id"`
		ArtifactID    string `json:"artifact_id"`
	}
	if err := json.Unmarshal(request.RPC.Params, &params); err != nil {
		return nil, fmt.Errorf("decode service/deploy params: %w", err)
	}
	serviceID, err := uuid.Parse(strings.TrimSpace(params.ServiceID))
	if err != nil {
		return nil, fmt.Errorf("invalid service_id: %w", err)
	}
	environmentID, err := uuid.Parse(strings.TrimSpace(params.EnvironmentID))
	if err != nil {
		return nil, fmt.Errorf("invalid environment_id: %w", err)
	}
	artifactID, err := uuid.Parse(strings.TrimSpace(params.ArtifactID))
	if err != nil {
		return nil, fmt.Errorf("invalid artifact_id: %w", err)
	}

	evaluation, err := h.policy.Evaluate(ctx, artifactID, environmentID)
	if err != nil {
		return nil, fmt.Errorf("evaluate deployment policy: %w", err)
	}
	if evaluation != nil && (!evaluation.Allowed || evaluation.Blockers > 0) {
		return nil, fmt.Errorf("deployment blocked by policy: %s", summarizePolicyBlockReason(evaluation))
	}

	requestedBy := ""
	metadata := map[string]any{"contextvm_method": ContextVMMethodServiceDeploy}
	if request.Event != nil {
		requestedBy = strings.TrimSpace(request.Event.PubKey.Hex())
		if request.Event.ID.Hex() != "" {
			metadata["nostr_event_id"] = request.Event.ID.Hex()
		}
	}
	if requestedBy == "" {
		return nil, fmt.Errorf("requester pubkey is required")
	}

	desiredState, err := h.runtimeLifecycle.BuildDesiredStateSnapshot(ctx, serviceID, environmentID, artifactID)
	if err != nil {
		return nil, fmt.Errorf("build desired state: %w", err)
	}
	intent := &domain.DeploymentIntent{
		ID:            uuid.New(),
		ServiceID:     serviceID,
		EnvironmentID: environmentID,
		ArtifactID:    artifactID,
		RequestedBy:   requestedBy,
		SourceKind:    domain.SourceKindEventTriggered,
		Metadata:      metadata,
		DesiredState:  desiredState,
		DesiredHash:   desiredState.DesiredHash,
	}
	if err := h.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		return nil, fmt.Errorf("create deployment intent: %w", err)
	}
	result := map[string]any{
		"status":         string(intent.Status),
		"intent_id":      intent.ID.String(),
		"service_id":     serviceID.String(),
		"environment_id": environmentID.String(),
		"artifact_id":    artifactID.String(),
	}
	if intent.Status != domain.IntentStatusApproved {
		result["message"] = "deployment intent created; approval required"
		return result, nil
	}

	startedAt := time.Now().UTC()
	run := &domain.DeploymentRun{
		ID:                 uuid.New(),
		DeploymentIntentID: intent.ID,
		Status:             domain.RunStatusRunning,
		StartedAt:          &startedAt,
		Metadata:           metadata,
		ApplyMetadata: map[string]any{
			"desired_hash":                 desiredState.DesiredHash,
			"desired_state_schema_version": desiredState.SchemaVersion,
		},
	}
	if err := h.registry.CreateDeploymentRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create deployment run: %w", err)
	}
	_, deployErr := h.runtimeLifecycle.Deploy(ctx, serviceID, environmentID, &artifactID)
	exitCode := 0
	runStatus := domain.RunStatusSucceeded
	if deployErr != nil {
		exitCode = 1
		runStatus = domain.RunStatusFailed
	}
	if err := h.registry.CompleteDeploymentRun(ctx, run.ID, runStatus, &exitCode); err != nil {
		if deployErr != nil {
			h.logger.Error("failed to complete deployment run after deploy failure", zap.Error(err), zap.Error(deployErr))
			return nil, fmt.Errorf("deploy service: %v; complete failed deployment run: %w", deployErr, err)
		}
		return nil, fmt.Errorf("complete deployment run: %w", err)
	}
	if deployErr != nil {
		return nil, fmt.Errorf("deploy service: %w", deployErr)
	}
	result["status"] = string(domain.IntentStatusDeployed)
	result["run_id"] = run.ID.String()
	result["message"] = "desired state applied"
	return result, nil
}
