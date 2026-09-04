package controlplane

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

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
	PublicRoutes     *service.PublicRoutePlanner
	Services         repository.ServiceRepository
	DeploymentUnits  repository.DeploymentUnitRepository
	RBAC             *auth.RBAC
	Logger           *zap.Logger
}

type encryptedServiceHandlers struct {
	registry         *service.RegistryService
	runtimeLifecycle *service.RuntimeLifecycleService
	policy           *service.PolicyService
	publicRoutes     *service.PublicRoutePlanner
	deploymentUnits  repository.DeploymentUnitRepository
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
		publicRoutes:     cfg.PublicRoutes,
		deploymentUnits:  cfg.DeploymentUnits,
		authorizer:       encryptedTenantAuthorizer{services: cfg.Services, environments: cfg.Registry, rbac: cfg.RBAC},
		logger:           cfg.Logger,
	}
	if h.logger == nil {
		h.logger = zap.NewNop()
	}
	transport.RegisterContextVMHandler(ContextVMMethodServiceDeployPreview, h.previewDeploy)
	transport.RegisterContextVMHandler(ContextVMMethodServiceDeploy, h.deploy)
	transport.RegisterContextVMHandler(ContextVMMethodServiceRouteAttach, h.routeAttach)
	transport.RegisterContextVMHandler(ContextVMMethodServiceRollback, h.rollback)
	transport.RegisterContextVMHandler(ContextVMMethodApprovalApprove, h.approve)
	transport.RegisterContextVMHandler(ContextVMMethodApprovalReject, h.reject)
}

func (h *encryptedServiceHandlers) previewDeploy(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil || h.runtimeLifecycle == nil || h.policy == nil {
		return nil, fmt.Errorf("service deployment control plane is not configured")
	}
	var params dto.ServiceDeployPreviewRequest
	if err := decodeStrictContextVMParams(request.RPC.Params, &params); err != nil {
		return nil, fmt.Errorf("decode service/deploy-preview params: %w", err)
	}
	if params.ServiceID == uuid.Nil || params.EnvironmentID == uuid.Nil || params.ArtifactID == uuid.Nil || params.ManagedRuntimeConfig == nil {
		return nil, fmt.Errorf("service_id, environment_id, artifact_id, and managed_runtime_config are required")
	}
	if params.DeploymentUnitID != nil && *params.DeploymentUnitID == uuid.Nil {
		return nil, fmt.Errorf("deployment_unit_id must not be nil")
	}
	svc, env, err := h.authorizer.authorizeServiceEnvironment(
		ctx,
		request.Event,
		params.ServiceID,
		params.EnvironmentID,
		domain.PermWriteDeployments,
		domain.PermWriteDeployments,
	)
	if err != nil {
		return nil, err
	}
	if !supportsManagedRuntimeConfig(svc.RuntimeType) {
		return nil, fmt.Errorf("desired-state wizard requires a managed runtime service")
	}
	managed := domain.NormalizeManagedRuntimeConfig(params.ManagedRuntimeConfig)
	if err := domain.ValidateManagedRuntimeConfig(managed); err != nil {
		return nil, fmt.Errorf("invalid managed runtime configuration: %w", err)
	}
	desiredState, err := h.runtimeLifecycle.PreviewDesiredStateSnapshot(ctx, params.ServiceID, params.EnvironmentID, params.ArtifactID, params.DeploymentUnitID, managed)
	if err != nil {
		return nil, fmt.Errorf("build desired-state preview: %w", err)
	}
	routeApprovalRequired := false
	if params.PublicRoute != nil {
		if h.publicRoutes == nil {
			return nil, fmt.Errorf("public route provisioning is not configured")
		}
		plan, protected, err := h.publicRoutes.Plan(ctx, svc, env, desiredState, *params.PublicRoute)
		if err != nil {
			return nil, fmt.Errorf("plan public route: %w", err)
		}
		desiredState.PublicRoute = plan
		desiredState.ComputeDesiredHash()
		routeApprovalRequired = protected
	}
	currentDesiredState, err := h.latestDeployedDesiredState(ctx, params.ServiceID, params.EnvironmentID, desiredState.DeploymentUnitID)
	if err != nil {
		return nil, fmt.Errorf("load current desired state: %w", err)
	}
	evaluation, err := h.policy.Evaluate(ctx, params.ArtifactID, params.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("evaluate deployment policy: %w", err)
	}
	return map[string]any{
		"service_id":              params.ServiceID.String(),
		"environment_id":          params.EnvironmentID.String(),
		"artifact_id":             params.ArtifactID.String(),
		"deployment_unit_id":      desiredState.DeploymentUnitID,
		"desired_state":           desiredState,
		"current_desired_state":   currentDesiredState,
		"desired_state_hash":      desiredState.DesiredHash,
		"policy":                  evaluation,
		"route_preview":           desiredState.PublicRoute,
		"route_approval_required": routeApprovalRequired,
		"idempotency_key":         effectiveIdempotencyKey(request, params.IdempotencyKey),
	}, nil
}

func (h *encryptedServiceHandlers) latestDeployedDesiredState(ctx context.Context, serviceID, environmentID uuid.UUID, unitID *uuid.UUID) (*domain.DesiredServiceSpec, error) {
	intent, err := h.latestDeployedIntent(ctx, serviceID, environmentID, unitID)
	if err != nil || intent == nil {
		return nil, err
	}
	return intent.DesiredState, nil
}

func (h *encryptedServiceHandlers) latestDeployedIntent(ctx context.Context, serviceID, environmentID uuid.UUID, unitID *uuid.UUID) (*domain.DeploymentIntent, error) {
	intents, err := h.registry.ListDeploymentIntents(ctx, serviceID, environmentID, 50, 0)
	if err != nil {
		return nil, err
	}
	for i := range intents {
		intent := &intents[i]
		if intent.Status != domain.IntentStatusDeployed || intent.DesiredState == nil {
			continue
		}
		if !optionalUUIDsEqual(intent.DeploymentUnitID, unitID) {
			continue
		}
		return intent, nil
	}
	return nil, nil
}

func optionalUUIDsEqual(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
	svc, env, err := h.authorizer.authorizeServiceEnvironment(
		ctx,
		request.Event,
		params.ServiceID,
		params.EnvironmentID,
		domain.PermWriteDeployments,
		domain.PermWriteDeployments,
	)
	if err != nil {
		return nil, err
	}

	if err := validateManagedDeployReviewHash(svc, params.ExpectedDesiredStateHash); err != nil {
		return nil, err
	}
	serviceID := params.ServiceID
	environmentID := params.EnvironmentID
	artifactID := params.ArtifactID
	requestedUnitID := params.DeploymentUnitID
	desiredState, err := h.runtimeLifecycle.BuildDesiredStateSnapshot(ctx, serviceID, environmentID, artifactID, requestedUnitID)
	if err != nil {
		return nil, fmt.Errorf("build desired state: %w", err)
	}
	if params.PublicRoute != nil {
		if h.publicRoutes == nil {
			return nil, fmt.Errorf("public route provisioning is not configured")
		}
		plan, _, err := h.publicRoutes.Plan(ctx, svc, env, desiredState, *params.PublicRoute)
		if err != nil {
			return nil, fmt.Errorf("plan public route: %w", err)
		}
		desiredState.PublicRoute = plan
		desiredState.ComputeDesiredHash()
	}
	if params.ExpectedDesiredStateHash != "" && !desiredStateHashesEqual(params.ExpectedDesiredStateHash, desiredState.DesiredHash) {
		return nil, fmt.Errorf("desired state changed after review; refresh the preview before submitting")
	}
	evaluation, err := h.policy.Evaluate(ctx, artifactID, environmentID)
	if err != nil {
		return nil, fmt.Errorf("evaluate deployment policy: %w", err)
	}
	if evaluation != nil && (!evaluation.Allowed || evaluation.Blockers > 0) {
		return nil, fmt.Errorf("deployment blocked by policy: %s", summarizePolicyBlockReason(evaluation))
	}

	requestedBy, metadata, approvalMetadata, artifactDigest, target, err := h.deploymentIntentMetadata(
		ctx,
		request,
		ContextVMMethodServiceDeploy,
		serviceID,
		environmentID,
		artifactID,
		desiredState.DeploymentUnitID,
		evaluation,
	)
	if err != nil {
		return nil, err
	}

	intent := &domain.DeploymentIntent{
		ID:               uuid.New(),
		ServiceID:        serviceID,
		EnvironmentID:    environmentID,
		DeploymentUnitID: desiredState.DeploymentUnitID,
		ArtifactID:       artifactID,
		RequestedBy:      requestedBy,
		SourceKind:       domain.SourceKindEventTriggered,
		ApprovalMetadata: approvalMetadata,
		Metadata:         metadata,
		DesiredState:     desiredState,
		DesiredHash:      desiredState.DesiredHash,
	}
	if err := h.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		return nil, fmt.Errorf("create deployment intent: %w", err)
	}

	message := "deployment approved and queued"
	if intent.Status != domain.IntentStatusApproved {
		message = "deployment intent created; approval required"
	}
	return map[string]any{
		"status":             string(intent.Status),
		"intent_id":          intent.ID.String(),
		"service_id":         serviceID.String(),
		"environment_id":     environmentID.String(),
		"artifact_id":        artifactID.String(),
		"artifact_digest":    artifactDigest,
		"deployment_unit_id": desiredState.DeploymentUnitID,
		"deployment_target":  target,
		"desired_state_hash": desiredState.DesiredHash,
		"idempotency_key":    effectiveIdempotencyKey(request, params.IdempotencyKey),
		"message":            message,
	}, nil
}

func (h *encryptedServiceHandlers) routeAttach(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil || h.policy == nil || h.publicRoutes == nil {
		return nil, fmt.Errorf("service deployment control plane is not configured")
	}
	var params dto.ServiceRouteAttachRequest
	if err := decodeStrictContextVMParams(request.RPC.Params, &params); err != nil {
		return nil, fmt.Errorf("decode service/route-attach params: %w", err)
	}
	if params.ServiceID == uuid.Nil || params.EnvironmentID == uuid.Nil || params.PublicRoute == nil {
		return nil, fmt.Errorf("service_id, environment_id, and public_route are required")
	}
	if params.DeploymentUnitID != nil && *params.DeploymentUnitID == uuid.Nil {
		return nil, fmt.Errorf("deployment_unit_id must not be nil")
	}
	svc, env, err := h.authorizer.authorizeServiceEnvironment(
		ctx,
		request.Event,
		params.ServiceID,
		params.EnvironmentID,
		domain.PermWriteDeployments,
		domain.PermWriteDeployments,
	)
	if err != nil {
		return nil, err
	}
	current, err := h.latestDeployedIntent(ctx, params.ServiceID, params.EnvironmentID, params.DeploymentUnitID)
	if err != nil {
		return nil, fmt.Errorf("load current deployed service: %w", err)
	}
	if current == nil || current.DesiredState == nil {
		return nil, fmt.Errorf("no deployed desired state exists for the service, environment, and deployment unit")
	}
	desiredState, err := cloneDesiredServiceSpec(current.DesiredState)
	if err != nil {
		return nil, fmt.Errorf("clone current desired state: %w", err)
	}
	if _, err := h.resolveRouteAttachDeploymentUnit(ctx, current, env); err != nil {
		return nil, err
	}
	plan, _, err := h.publicRoutes.Plan(ctx, svc, env, desiredState, *params.PublicRoute)
	if err != nil {
		return nil, fmt.Errorf("plan public route: %w", err)
	}
	desiredState.PublicRoute = plan
	desiredState.ComputeDesiredHash()

	evaluation, err := h.policy.Evaluate(ctx, current.ArtifactID, params.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("evaluate deployment policy: %w", err)
	}
	if evaluation != nil && (!evaluation.Allowed || evaluation.Blockers > 0) {
		return nil, fmt.Errorf("route attachment blocked by policy: %s", summarizePolicyBlockReason(evaluation))
	}
	requestedBy := ""
	metadata := map[string]any{"contextvm_method": ContextVMMethodServiceRouteAttach}
	if request.Event != nil {
		requestedBy = strings.TrimSpace(request.Event.PubKey.Hex())
		if request.Event.ID.Hex() != "" {
			metadata["nostr_event_id"] = request.Event.ID.Hex()
		}
	}
	if requestedBy == "" {
		return nil, fmt.Errorf("requester pubkey is required")
	}
	artifact, err := h.registry.GetArtifact(ctx, current.ArtifactID)
	if err != nil {
		return nil, fmt.Errorf("get current deployment artifact: %w", err)
	}
	if artifact == nil || artifact.ServiceID != params.ServiceID {
		return nil, fmt.Errorf("current deployment artifact not found for service")
	}
	metadata["artifact_digest"] = artifact.ImageDigest
	if current.Metadata != nil {
		if target, ok := current.Metadata["deployment_target"]; ok {
			metadata["deployment_target"] = target
		}
	}
	policy := map[string]any{"decision": "allow", "allowed": true, "warnings": 0, "blockers": 0}
	if evaluation != nil {
		policy["allowed"] = evaluation.Allowed
		policy["warnings"] = evaluation.Warnings
		policy["blockers"] = evaluation.Blockers
	}
	metadata["policy"] = policy
	intent := &domain.DeploymentIntent{
		ID:               uuid.New(),
		ServiceID:        params.ServiceID,
		EnvironmentID:    params.EnvironmentID,
		DeploymentUnitID: desiredState.DeploymentUnitID,
		ArtifactID:       current.ArtifactID,
		RequestedBy:      requestedBy,
		SourceKind:       domain.SourceKindEventTriggered,
		ApprovalMetadata: map[string]any{"policy": policy},
		Metadata:         metadata,
		DesiredState:     desiredState,
		DesiredHash:      desiredState.DesiredHash,
	}
	if err := h.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		return nil, fmt.Errorf("create route attachment deployment intent: %w", err)
	}
	message := "route attachment approved and queued"
	if intent.Status != domain.IntentStatusApproved {
		message = "route attachment intent created; approval required"
	}
	return map[string]any{
		"status":             string(intent.Status),
		"intent_id":          intent.ID.String(),
		"service_id":         params.ServiceID.String(),
		"environment_id":     params.EnvironmentID.String(),
		"artifact_id":        current.ArtifactID.String(),
		"artifact_digest":    artifact.ImageDigest,
		"deployment_unit_id": desiredState.DeploymentUnitID,
		"deployment_target":  metadata["deployment_target"],
		"desired_state_hash": desiredState.DesiredHash,
		"public_route":       desiredState.PublicRoute,
		"idempotency_key":    effectiveIdempotencyKey(request, params.IdempotencyKey),
		"message":            message,
	}, nil
}

func (h *encryptedServiceHandlers) resolveRouteAttachDeploymentUnit(ctx context.Context, intent *domain.DeploymentIntent, env *domain.Environment) (*domain.DeploymentUnit, error) {
	if intent == nil || env == nil {
		return nil, fmt.Errorf("route attachment requires a deployed intent and environment")
	}
	var unitID *uuid.UUID
	if intent.DeploymentUnitID != nil && *intent.DeploymentUnitID != uuid.Nil {
		id := *intent.DeploymentUnitID
		unitID = &id
	}
	if desired := intent.DesiredState; desired != nil && desired.DeploymentUnitID != nil && *desired.DeploymentUnitID != uuid.Nil {
		if unitID != nil && *unitID != *desired.DeploymentUnitID {
			return nil, fmt.Errorf("route attachment deployment unit %s does not match desired-state unit %s", *unitID, *desired.DeploymentUnitID)
		}
		id := *desired.DeploymentUnitID
		unitID = &id
	}
	if unitID == nil {
		return nil, fmt.Errorf("route attachment requires an explicit deployment unit")
	}
	if h.deploymentUnits == nil {
		return nil, fmt.Errorf("route attachment requires the deployment unit repository")
	}
	unit, err := h.deploymentUnits.GetByID(ctx, *unitID)
	if err != nil {
		return nil, fmt.Errorf("resolve route attachment deployment unit %s: %w", *unitID, err)
	}
	if unit == nil {
		return nil, fmt.Errorf("route attachment deployment unit %s not found", *unitID)
	}
	if unit.EnvironmentID != env.ID {
		return nil, fmt.Errorf("route attachment deployment unit %q belongs to environment %s, not %s", unit.Key, unit.EnvironmentID, env.ID)
	}
	resolved := *unit
	domain.NormalizeDeploymentUnitTargeting(&resolved)
	if err := domain.ValidateDeploymentUnit(&resolved); err != nil {
		return nil, fmt.Errorf("invalid route attachment deployment unit %q: %w", resolved.Key, err)
	}
	if desired := intent.DesiredState; desired != nil {
		if desired.DeploymentUnitID == nil && resolved.ID != uuid.Nil {
			return nil, fmt.Errorf("route attachment desired-state implicit deployment unit became explicit unit %s", resolved.ID)
		}
		if desired.DeploymentUnitKey != "" && desired.DeploymentUnitKey != resolved.Key {
			return nil, fmt.Errorf("route attachment desired-state unit key %q does not match resolved unit %q", desired.DeploymentUnitKey, resolved.Key)
		}
		if desired.UnitRuntimeType != "" && desired.UnitRuntimeType != resolved.RuntimeType {
			return nil, fmt.Errorf("route attachment desired-state runtime type %q does not match resolved unit %q type %q", desired.UnitRuntimeType, resolved.Key, resolved.RuntimeType)
		}
	}
	if resolved.OwnershipMode != domain.OwnershipModeBahiaManaged {
		return nil, fmt.Errorf("route attachment requires a Bahia-managed deployment unit; unit %q ownership is %q", resolved.Key, resolved.OwnershipMode)
	}
	if resolved.RuntimeType != domain.RuntimeTypeCompose {
		return nil, fmt.Errorf("route attachment requires a Compose deployment unit; unit %q runtime is %q", resolved.Key, resolved.RuntimeType)
	}
	if deploymentUnitDispatchesViaLoom(resolved.RuntimeConfig) {
		return nil, fmt.Errorf("route attachment requires direct runtime dispatch; deployment unit %q dispatches via Loom", resolved.Key)
	}
	return &resolved, nil
}

func deploymentUnitDispatchesViaLoom(runtimeConfig map[string]any) bool {
	if runtimeConfig == nil {
		return false
	}
	raw, ok := runtimeConfig["dispatch_mode"]
	if !ok {
		raw, ok = runtimeConfig["execution_backend"]
	}
	if !ok {
		return false
	}
	mode, ok := raw.(string)
	return ok && strings.EqualFold(strings.TrimSpace(mode), "loom")
}

func cloneDesiredServiceSpec(source *domain.DesiredServiceSpec) (*domain.DesiredServiceSpec, error) {
	if source == nil {
		return nil, fmt.Errorf("desired service spec is required")
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var cloned domain.DesiredServiceSpec
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func (h *encryptedServiceHandlers) rollback(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil || h.runtimeLifecycle == nil || h.policy == nil {
		return nil, fmt.Errorf("service deployment control plane is not configured")
	}
	var params dto.ServiceRollbackRequest
	if err := decodeStrictContextVMParams(request.RPC.Params, &params); err != nil {
		return nil, fmt.Errorf("decode service/rollback params: %w", err)
	}
	if params.ServiceID == uuid.Nil || params.EnvironmentID == uuid.Nil || params.TargetArtifactID == uuid.Nil || params.SupersedesIntentID == uuid.Nil {
		return nil, fmt.Errorf("service_id, environment_id, target_artifact_id, and supersedes_intent_id are required")
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

	superseded, err := h.registry.GetDeploymentIntent(ctx, params.SupersedesIntentID)
	if err != nil {
		return nil, fmt.Errorf("get superseded deployment intent: %w", err)
	}
	if superseded == nil {
		return nil, fmt.Errorf("superseded deployment intent not found")
	}
	if superseded.ServiceID != params.ServiceID || superseded.EnvironmentID != params.EnvironmentID ||
		!optionalUUIDsEqual(superseded.DeploymentUnitID, params.DeploymentUnitID) {
		return nil, fmt.Errorf("rollback target must match the superseded service, environment, and deployment unit")
	}
	if superseded.ArtifactID == params.TargetArtifactID {
		return nil, fmt.Errorf("rollback target artifact must differ from the superseded artifact")
	}
	history, err := h.registry.ListDeploymentIntents(ctx, params.ServiceID, params.EnvironmentID, 100, 0)
	if err != nil {
		return nil, fmt.Errorf("load deployment history for rollback: %w", err)
	}
	previouslyDeployed := false
	for i := range history {
		candidate := &history[i]
		if candidate.ArtifactID == params.TargetArtifactID &&
			candidate.Status == domain.IntentStatusDeployed &&
			optionalUUIDsEqual(candidate.DeploymentUnitID, params.DeploymentUnitID) {
			previouslyDeployed = true
			break
		}
	}
	if !previouslyDeployed {
		return nil, fmt.Errorf("rollback target must be a previously successful artifact for this deployment unit")
	}

	desiredState, err := h.runtimeLifecycle.BuildDesiredStateSnapshot(
		ctx,
		params.ServiceID,
		params.EnvironmentID,
		params.TargetArtifactID,
		params.DeploymentUnitID,
	)
	if err != nil {
		return nil, fmt.Errorf("build rollback desired state: %w", err)
	}
	carryForwardRollbackPublicRoute(desiredState, superseded)
	evaluation, err := h.policy.Evaluate(ctx, params.TargetArtifactID, params.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("evaluate rollback policy: %w", err)
	}
	if evaluation != nil && (!evaluation.Allowed || evaluation.Blockers > 0) {
		return nil, fmt.Errorf("rollback blocked by policy: %s", summarizePolicyBlockReason(evaluation))
	}
	requestedBy, metadata, approvalMetadata, artifactDigest, target, err := h.deploymentIntentMetadata(
		ctx,
		request,
		ContextVMMethodServiceRollback,
		params.ServiceID,
		params.EnvironmentID,
		params.TargetArtifactID,
		desiredState.DeploymentUnitID,
		evaluation,
	)
	if err != nil {
		return nil, err
	}
	intent := &domain.DeploymentIntent{
		ID:                 uuid.New(),
		ServiceID:          params.ServiceID,
		EnvironmentID:      params.EnvironmentID,
		DeploymentUnitID:   desiredState.DeploymentUnitID,
		ArtifactID:         params.TargetArtifactID,
		RequestedBy:        requestedBy,
		SourceKind:         domain.SourceKindRollback,
		SupersedesIntentID: &params.SupersedesIntentID,
		ApprovalMetadata:   approvalMetadata,
		Metadata:           metadata,
		DesiredState:       desiredState,
		DesiredHash:        desiredState.DesiredHash,
	}
	if err := h.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		return nil, fmt.Errorf("create rollback deployment intent: %w", err)
	}
	message := "rollback approved and queued"
	if intent.Status != domain.IntentStatusApproved {
		message = "rollback intent created; approval required"
	}
	return map[string]any{
		"status":               string(intent.Status),
		"intent_id":            intent.ID.String(),
		"service_id":           params.ServiceID.String(),
		"environment_id":       params.EnvironmentID.String(),
		"target_artifact_id":   params.TargetArtifactID.String(),
		"artifact_digest":      artifactDigest,
		"deployment_unit_id":   desiredState.DeploymentUnitID,
		"deployment_target":    target,
		"desired_state_hash":   desiredState.DesiredHash,
		"supersedes_intent_id": params.SupersedesIntentID.String(),
		"idempotency_key":      effectiveIdempotencyKey(request, params.IdempotencyKey),
		"message":              message,
	}, nil
}

func validateManagedDeployReviewHash(svc *domain.Service, expectedHash string) error {
	expectedHash = strings.TrimSpace(expectedHash)
	if svc != nil && svc.RuntimeConfig != nil && svc.RuntimeConfig.Managed != nil && expectedHash == "" {
		return fmt.Errorf("expected_desired_state_hash is required for managed deploys")
	}
	if expectedHash != "" && (svc == nil || !supportsManagedRuntimeConfig(svc.RuntimeType)) {
		return fmt.Errorf("reviewed desired-state deploy requires a managed runtime service")
	}
	return nil
}

func carryForwardRollbackPublicRoute(desiredState *domain.DesiredServiceSpec, superseded *domain.DeploymentIntent) {
	if desiredState == nil || superseded == nil || superseded.DesiredState == nil || superseded.DesiredState.PublicRoute == nil {
		return
	}
	desiredState.PublicRoute = superseded.DesiredState.PublicRoute
	desiredState.ComputeDesiredHash()
}

func (h *encryptedServiceHandlers) deploymentIntentMetadata(
	ctx context.Context,
	request ContextVMRequest,
	method string,
	serviceID, environmentID, artifactID uuid.UUID,
	deploymentUnitID *uuid.UUID,
	evaluation *domain.PolicyEvaluation,
) (string, map[string]any, map[string]any, string, *service.DeploymentTargetSummary, error) {
	requestedBy := ""
	metadata := map[string]any{"contextvm_method": method}
	if request.Event != nil {
		requestedBy = strings.TrimSpace(request.Event.PubKey.Hex())
		if request.Event.ID.Hex() != "" {
			metadata["nostr_event_id"] = request.Event.ID.Hex()
		}
	}
	if requestedBy == "" {
		return "", nil, nil, "", nil, fmt.Errorf("requester pubkey is required")
	}
	artifact, err := h.registry.GetArtifact(ctx, artifactID)
	if err != nil {
		return "", nil, nil, "", nil, fmt.Errorf("get deployment artifact: %w", err)
	}
	if artifact == nil || artifact.ServiceID != serviceID {
		return "", nil, nil, "", nil, fmt.Errorf("deployment artifact not found for service")
	}
	target, err := h.runtimeLifecycle.ResolveDeploymentTargetSummary(ctx, serviceID, environmentID, deploymentUnitID)
	if err != nil {
		return "", nil, nil, "", nil, fmt.Errorf("resolve deployment target: %w", err)
	}
	metadata["artifact_digest"] = artifact.ImageDigest
	metadata["deployment_target"] = target
	policy := map[string]any{
		"decision": "allow",
		"allowed":  true,
		"warnings": 0,
		"blockers": 0,
	}
	if evaluation != nil {
		policy["allowed"] = evaluation.Allowed
		policy["warnings"] = evaluation.Warnings
		policy["blockers"] = evaluation.Blockers
	}
	metadata["policy"] = policy
	return requestedBy, metadata, map[string]any{"policy": policy}, artifact.ImageDigest, target, nil
}

func desiredStateHashesEqual(expected, actual string) bool {
	expected = strings.TrimPrefix(strings.TrimSpace(expected), "sha256:")
	actual = strings.TrimPrefix(strings.TrimSpace(actual), "sha256:")
	expectedBytes, expectedErr := hex.DecodeString(expected)
	actualBytes, actualErr := hex.DecodeString(actual)
	if expectedErr != nil || actualErr != nil || len(expectedBytes) != 32 || len(actualBytes) != 32 {
		return false
	}
	return subtle.ConstantTimeCompare(expectedBytes, actualBytes) == 1
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
		if h.policy == nil {
			return nil, fmt.Errorf("deployment policy service is not configured")
		}
		evaluation, policyErr := h.policy.Evaluate(ctx, intent.ArtifactID, intent.EnvironmentID)
		if policyErr != nil {
			return nil, fmt.Errorf("re-evaluate deployment policy: %w", policyErr)
		}
		if evaluation != nil && (!evaluation.Allowed || evaluation.Blockers > 0) {
			return nil, fmt.Errorf("deployment blocked by current policy: %s", summarizePolicyBlockReason(evaluation))
		}
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
