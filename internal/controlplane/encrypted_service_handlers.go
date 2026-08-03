package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type EncryptedServiceHandlersConfig struct {
	Registry         *service.RegistryService
	RuntimeLifecycle *service.RuntimeLifecycleService
	Policy           *service.PolicyService
	Services         repository.ServiceRepository
	RBAC             *auth.RBAC
	Logger           *zap.Logger
}

type encryptedServiceHandlers struct {
	registry         *service.RegistryService
	runtimeLifecycle *service.RuntimeLifecycleService
	policy           *service.PolicyService
	authorizer       encryptedTenantAuthorizer
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
		authorizer:       encryptedTenantAuthorizer{services: cfg.Services, environments: cfg.Registry, rbac: cfg.RBAC},
		logger:           cfg.Logger,
	}
	if h.logger == nil {
		h.logger = zap.NewNop()
	}
	transport.RegisterContextVMHandler(ContextVMMethodServiceDeploy, h.deploy)
	transport.RegisterContextVMHandler(ContextVMMethodApprovalApprove, h.approve)
	transport.RegisterContextVMHandler(ContextVMMethodApprovalReject, h.reject)
}

func (h *encryptedServiceHandlers) deploy(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil || h.runtimeLifecycle == nil || h.policy == nil {
		return nil, fmt.Errorf("service deployment control plane is not configured")
	}
	var params dto.ServiceDeployRequest
	if err := decodeStrictContextVMParams(request.RPC.Params, &params); err != nil {
		return nil, fmt.Errorf("decode service/deploy params: %w", err)
	}
	if params.ServiceID == uuid.Nil || params.EnvironmentID == uuid.Nil || params.ArtifactID == uuid.Nil {
		return nil, fmt.Errorf("service_id, environment_id, and artifact_id are required")
	}
	if params.DeploymentUnitID != nil && *params.DeploymentUnitID == uuid.Nil {
		return nil, fmt.Errorf("deployment_unit_id must not be nil")
	}
	if _, _, err := h.authorizer.authorizeServiceEnvironment(
		ctx,
		request.Event,
		params.ServiceID,
		params.EnvironmentID,
		domain.PermWriteDeployments,
		domain.PermWriteDeployments,
	); err != nil {
		return nil, err
	}

	serviceID := params.ServiceID
	environmentID := params.EnvironmentID
	artifactID := params.ArtifactID
	requestedUnitID := params.DeploymentUnitID
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

	desiredState, err := h.runtimeLifecycle.BuildDesiredStateSnapshot(ctx, serviceID, environmentID, artifactID, requestedUnitID)
	if err != nil {
		return nil, fmt.Errorf("build desired state: %w", err)
	}
	intent := &domain.DeploymentIntent{
		ID:               uuid.New(),
		ServiceID:        serviceID,
		EnvironmentID:    environmentID,
		DeploymentUnitID: desiredState.DeploymentUnitID,
		ArtifactID:       artifactID,
		RequestedBy:      requestedBy,
		SourceKind:       domain.SourceKindEventTriggered,
		Metadata:         metadata,
		DesiredState:     desiredState,
		DesiredHash:      desiredState.DesiredHash,
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
		DeploymentUnitID:   desiredState.DeploymentUnitID,
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
	_, deployErr := h.runtimeLifecycle.DeployDesiredStateSnapshot(ctx, serviceID, environmentID, &artifactID, desiredState, nil)
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

func (h *encryptedServiceHandlers) approve(ctx context.Context, request ContextVMRequest) (any, error) {
	return h.decide(ctx, request, "approve")
}

func (h *encryptedServiceHandlers) reject(ctx context.Context, request ContextVMRequest) (any, error) {
	return h.decide(ctx, request, "reject")
}

func (h *encryptedServiceHandlers) decide(ctx context.Context, request ContextVMRequest, methodDecision string) (any, error) {
	if h.registry == nil {
		return nil, fmt.Errorf("service deployment control plane is not configured")
	}
	var params dto.DeploymentDecisionRequest
	if err := decodeStrictContextVMParams(request.RPC.Params, &params); err != nil {
		return nil, fmt.Errorf("decode deployment decision params: %w", err)
	}
	decision := strings.ToLower(strings.TrimSpace(params.Decision))
	if decision == "" {
		return nil, fmt.Errorf("decision is required")
	}
	if decision != methodDecision {
		return nil, fmt.Errorf("decision %q does not match ContextVM method", decision)
	}
	if params.IntentID == uuid.Nil {
		return nil, fmt.Errorf("intent_id is required")
	}
	intent, err := h.registry.GetDeploymentIntent(ctx, params.IntentID)
	if err != nil {
		return nil, fmt.Errorf("get deployment intent: %w", err)
	}
	if intent == nil {
		return nil, fmt.Errorf("deployment intent not found")
	}
	if _, _, err := h.authorizer.authorizeServiceEnvironment(
		ctx,
		request.Event,
		intent.ServiceID,
		intent.EnvironmentID,
		domain.PermApproveDeployments,
		domain.PermApproveDeployments,
	); err != nil {
		return nil, err
	}
	if decision == "approve" {
		err = h.registry.ApproveDeploymentIntent(ctx, params.IntentID)
	} else {
		err = h.registry.RejectDeploymentIntent(ctx, params.IntentID)
	}
	if err != nil {
		return nil, fmt.Errorf("%s deployment intent: %w", decision, err)
	}
	status := domain.IntentStatusApproved
	if decision == "reject" {
		status = domain.IntentStatusRejected
	}
	return map[string]any{
		"status":          string(status),
		"intent_id":       params.IntentID.String(),
		"decision":        decision,
		"idempotency_key": effectiveIdempotencyKey(request, params.IdempotencyKey),
	}, nil
}
