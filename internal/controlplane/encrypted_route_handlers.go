package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const (
	EncryptedOperationServiceSecretsList   = "services.secrets.list"
	EncryptedOperationServiceSecretsCreate = "services.secrets.create"
	EncryptedOperationServiceSecretsUpdate = "services.secrets.update"
	EncryptedOperationServiceSecretsDelete = "services.secrets.delete"
	EncryptedOperationServiceSecretsReveal = "services.secrets.reveal"

	ContextVMMethodServiceSecretsList   = "services/secrets-list"
	ContextVMMethodServiceSecretsCreate = "services/secrets-create"
	ContextVMMethodServiceSecretsUpdate = "services/secrets-update"
	ContextVMMethodServiceSecretsDelete = "services/secrets-delete"
	ContextVMMethodServiceSecretsReveal = "services/secrets-reveal"

	EncryptedOperationDeploymentRunLogsGet = "deployments.run_logs.get"
	ContextVMMethodDeploymentRunLogsGet    = "deployments/run-logs-get"
	ContextVMMethodServiceUpdate           = "service/update"
	ContextVMMethodServiceDelete           = "service/delete"
	ContextVMMethodEnvironmentCreate       = "environment/create"
	ContextVMMethodEnvironmentUpdate       = "environment/update"
	ContextVMMethodEnvironmentDelete       = "environment/delete"

	EncryptedOperationArtifactSignaturesVerify = "artifacts.signatures.verify"
)

// RunLogFetcher is the encrypted request contract for stored deployment run logs.
type RunLogFetcher interface {
	FetchRunLogs(ctx context.Context, run *domain.DeploymentRun) (*adapterruntime.RunLogs, error)
}

// SignatureVerifier is the encrypted request contract for artifact signature verification.
type SignatureVerifier interface {
	VerifySignatures(ctx context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error)
}

// RegistryMutationBackend is the ContextVM contract for dashboard registry
// mutations. service.RegistryService and service.RelayFirstRegistry satisfy this
// interface; in relay-first mode the latter preserves publish-OK-before-DB-write
// semantics for service/environment writes.
type RegistryMutationBackend interface {
	CreateService(ctx context.Context, svc *domain.Service) error
	UpdateService(ctx context.Context, svc *domain.Service) error
	DeleteService(ctx context.Context, id uuid.UUID, force bool) error
	CreateEnvironment(ctx context.Context, env *domain.Environment) error
	CreateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit) error
	GetEnvironment(ctx context.Context, id uuid.UUID) (*domain.Environment, error)
	UpdateEnvironment(ctx context.Context, env *domain.Environment) error
	UpdateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit, expectedUpdatedAt time.Time) error
	DeleteEnvironment(ctx context.Context, id uuid.UUID, force bool) error
}

type EncryptedRouteHandlersConfig struct {
	Secrets      repository.SecretRepository
	Encryptor    *secrets.Encryptor
	Runs         repository.DeploymentRunRepository
	RunLogs      RunLogFetcher
	Artifacts    repository.ArtifactRepository
	Signatures   repository.ArtifactSignatureRepository
	SignVerifier SignatureVerifier
	Services     repository.ServiceRepository
	Intents      repository.DeploymentIntentRepository
	Registry     RegistryMutationBackend
	RBAC         *auth.RBAC
	Logger       *zap.Logger
}

type EncryptedRouteHandlers struct {
	secrets      repository.SecretRepository
	encryptor    *secrets.Encryptor
	runs         repository.DeploymentRunRepository
	runLogs      RunLogFetcher
	artifacts    repository.ArtifactRepository
	signatures   repository.ArtifactSignatureRepository
	signVerifier SignatureVerifier
	services     repository.ServiceRepository
	intents      repository.DeploymentIntentRepository
	registry     RegistryMutationBackend
	rbac         *auth.RBAC
	logger       *zap.Logger
}

// NewEncryptedRouteHandlers adapts sensitive route-only actions onto encrypted
// signer-first encrypted request/result operations. Secrets, stored run logs, and
// signature verification results are never projected to the public sidecar.
func NewEncryptedRouteHandlers(cfg EncryptedRouteHandlersConfig) *EncryptedRouteHandlers {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &EncryptedRouteHandlers{
		secrets:      cfg.Secrets,
		encryptor:    cfg.Encryptor,
		runs:         cfg.Runs,
		runLogs:      cfg.RunLogs,
		artifacts:    cfg.Artifacts,
		signatures:   cfg.Signatures,
		signVerifier: cfg.SignVerifier,
		services:     cfg.Services,
		intents:      cfg.Intents,
		registry:     cfg.Registry,
		rbac:         cfg.RBAC,
		logger:       logger.Named("encrypted-route-handlers"),
	}
}

func (h *EncryptedRouteHandlers) Register(transport *EncryptedRequestTransport) {
	if h == nil || transport == nil {
		return
	}
	h.registerRouteHandler(transport, EncryptedOperationServiceSecretsList, h.ListSecrets, ContextVMMethodServiceSecretsList)
	h.registerRouteHandler(transport, EncryptedOperationServiceSecretsCreate, h.CreateSecret, ContextVMMethodServiceSecretsCreate)
	h.registerRouteHandler(transport, EncryptedOperationServiceSecretsUpdate, h.UpdateSecret, ContextVMMethodServiceSecretsUpdate)
	h.registerRouteHandler(transport, EncryptedOperationServiceSecretsDelete, h.DeleteSecret, ContextVMMethodServiceSecretsDelete)
	h.registerRouteHandler(transport, EncryptedOperationServiceSecretsReveal, h.RevealSecret, ContextVMMethodServiceSecretsReveal)
	h.registerRouteHandler(transport, EncryptedOperationDeploymentRunLogsGet, h.GetRunLogs, ContextVMMethodDeploymentRunLogsGet)
	h.registerRouteHandler(transport, EncryptedOperationArtifactSignaturesVerify, h.VerifyArtifactSignatures)
	transport.RegisterContextVMHandler(ContextVMMethodServiceCreate, h.CreateService)
	transport.RegisterContextVMHandler(ContextVMMethodServiceUpdate, h.UpdateService)
	transport.RegisterContextVMHandler(ContextVMMethodServiceDelete, h.DeleteService)
	transport.RegisterContextVMHandler(ContextVMMethodEnvironmentCreate, h.CreateEnvironment)
	transport.RegisterContextVMHandler(ContextVMMethodEnvironmentUpdate, h.UpdateEnvironment)
	transport.RegisterContextVMHandler(ContextVMMethodEnvironmentDelete, h.DeleteEnvironment)
}

type encryptedRouteHandler = EncryptedRequestHandler

func (h *EncryptedRouteHandlers) registerRouteHandler(transport *EncryptedRequestTransport, operation string, handler encryptedRouteHandler, contextVMAliases ...string) {
	transport.RegisterHandler(operation, handler)
	register := func(method string) {
		transport.RegisterContextVMHandler(method, func(ctx context.Context, request ContextVMRequest) (any, error) {
			return handler(ctx, EncryptedRequest{Event: request.Event, Envelope: EncryptedRequestEnvelope{Version: ContextVMWireVersion, Operation: operation, RequesterPubkey: request.Event.PubKey.Hex(), Payload: request.RPC.Params}})
		})
	}
	register(operation)
	for _, alias := range contextVMAliases {
		register(alias)
	}
}

type encryptedEnvironmentCreatePayload struct {
	OrgID              uuid.UUID                        `json:"org_id,omitempty"`
	Name               string                           `json:"name"`
	LoomWorkerSelector json.RawMessage                  `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      map[string]any                   `json:"runtime_config,omitempty"`
	Targeting          *dto.EnvironmentTargetingRequest `json:"targeting,omitempty"`
	DeploymentUnits    []dto.DeploymentUnitRequest      `json:"deployment_units,omitempty"`
	ReconcileMode      string                           `json:"reconcile_mode,omitempty"`
	DeployStrategy     string                           `json:"deploy_strategy,omitempty"`
	Protected          bool                             `json:"protected,omitempty"`
}

type encryptedEnvironmentUpdatePayload struct {
	ID                 string                           `json:"id"`
	OrgID              *uuid.UUID                       `json:"org_id,omitempty"`
	ExpectedUpdatedAt  *time.Time                       `json:"expected_updated_at,omitempty"`
	Name               string                           `json:"name,omitempty"`
	LoomWorkerSelector json.RawMessage                  `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      map[string]any                   `json:"runtime_config,omitempty"`
	Targeting          *dto.EnvironmentTargetingRequest `json:"targeting,omitempty"`
	DeploymentUnits    []dto.DeploymentUnitRequest      `json:"deployment_units,omitempty"`
	ReconcileMode      string                           `json:"reconcile_mode,omitempty"`
	DeployStrategy     string                           `json:"deploy_strategy,omitempty"`
	Protected          *bool                            `json:"protected,omitempty"`
}

type encryptedEnvironmentDeletePayload struct {
	ID    string `json:"id"`
	Force bool   `json:"force,omitempty"`
}

func (h *EncryptedRouteHandlers) CreateService(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil {
		return nil, fmt.Errorf("service registry mutation handling is not configured")
	}
	var payload dto.CreateServiceRequest
	if err := decodeStrictContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(payload.Name)
	artifactRepo := strings.TrimSpace(payload.ArtifactRepo)
	if name == "" || artifactRepo == "" {
		return nil, fmt.Errorf("name and artifact_repo are required")
	}
	authorizer := encryptedTenantAuthorizer{services: h.services, environments: h.registry, rbac: h.rbac}
	if err := authorizer.authorizeOrg(ctx, request.Event, payload.OrgID, domain.PermWriteServices); err != nil {
		return nil, err
	}
	runtimeType := domain.RuntimeTypeDocker
	if strings.TrimSpace(payload.RuntimeType) != "" {
		runtimeType = domain.RuntimeType(strings.TrimSpace(payload.RuntimeType))
		if err := domain.ValidateRuntimeType(runtimeType); err != nil {
			return nil, fmt.Errorf("invalid runtime_type: %w", err)
		}
	}
	defaultBranch := strings.TrimSpace(payload.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	repositoryRef := repositoryRefFromRequest(payload.Repository)
	repoURL := strings.TrimSpace(payload.RepoURL)
	if repositoryRef != nil && strings.TrimSpace(repositoryRef.CloneURL) != "" {
		repoURL = strings.TrimSpace(repositoryRef.CloneURL)
	}
	svc := &domain.Service{
		ID:            uuid.New(),
		OrgID:         payload.OrgID,
		Name:          name,
		RepoURL:       repoURL,
		Repository:    repositoryRef,
		ArtifactRepo:  artifactRepo,
		DefaultBranch: defaultBranch,
		RuntimeType:   runtimeType,
	}
	if err := h.registry.CreateService(ctx, svc); err != nil {
		return nil, fmt.Errorf("failed to create service: %w", err)
	}
	return map[string]any{"status": "created", "service": svc, "service_id": svc.ID.String(), "idempotency_key": effectiveIdempotencyKey(request, payload.IdempotencyKey)}, nil
}

func (h *EncryptedRouteHandlers) UpdateService(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil {
		return nil, fmt.Errorf("service registry mutation handling is not configured")
	}
	var payload dto.UpdateServiceRequest
	if err := decodeStrictContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	authorizer := encryptedTenantAuthorizer{services: h.services, environments: h.registry, rbac: h.rbac}
	svc, err := authorizer.authorizeService(ctx, request.Event, payload.ID, domain.PermWriteServices)
	if err != nil {
		return nil, err
	}
	if payload.Name != nil {
		name := strings.TrimSpace(*payload.Name)
		if name == "" {
			return nil, fmt.Errorf("name must not be empty")
		}
		svc.Name = name
	}
	if payload.RepoURL != nil {
		svc.RepoURL = strings.TrimSpace(*payload.RepoURL)
		if svc.Repository != nil {
			svc.Repository.CloneURL = svc.RepoURL
		}
	}
	if payload.Repository != nil {
		svc.Repository = repositoryRefFromRequest(payload.Repository)
		if svc.Repository != nil && strings.TrimSpace(svc.Repository.CloneURL) != "" {
			svc.RepoURL = strings.TrimSpace(svc.Repository.CloneURL)
		}
	}
	if payload.ArtifactRepo != nil {
		artifactRepo := strings.TrimSpace(*payload.ArtifactRepo)
		if artifactRepo == "" {
			return nil, fmt.Errorf("artifact_repo must not be empty")
		}
		svc.ArtifactRepo = artifactRepo
	}
	if payload.DefaultBranch != nil {
		branch := strings.TrimSpace(*payload.DefaultBranch)
		if branch == "" {
			return nil, fmt.Errorf("default_branch must not be empty")
		}
		svc.DefaultBranch = branch
	}
	if payload.RuntimeType != nil {
		runtimeType := domain.RuntimeType(strings.TrimSpace(*payload.RuntimeType))
		if err := domain.ValidateRuntimeType(runtimeType); err != nil {
			return nil, fmt.Errorf("invalid runtime_type: %w", err)
		}
		svc.RuntimeType = runtimeType
	}
	if svc.RuntimeType != domain.RuntimeTypeCompose && svc.RuntimeConfig != nil && svc.RuntimeConfig.Managed != nil && payload.ManagedRuntimeConfig == nil {
		return nil, fmt.Errorf("cannot change a service with managed runtime configuration away from compose")
	}
	if payload.ManagedRuntimeConfig != nil {
		if svc.RuntimeType != domain.RuntimeTypeCompose {
			return nil, fmt.Errorf("managed runtime configuration requires runtime_type compose")
		}
		managed := domain.NormalizeManagedRuntimeConfig(payload.ManagedRuntimeConfig)
		if err := domain.ValidateManagedRuntimeConfig(managed); err != nil {
			return nil, fmt.Errorf("invalid managed runtime configuration: %w", err)
		}
		if len(managed.SecretRefs) > 0 && h.secrets == nil {
			return nil, fmt.Errorf("secret repository is required to validate managed secret references")
		}
		for _, ref := range managed.SecretRefs {
			secret, err := h.secrets.GetByID(ctx, ref.SecretID)
			if err != nil || secret == nil || secret.ServiceID != svc.ID {
				return nil, fmt.Errorf("managed runtime configuration contains an unavailable secret reference")
			}
		}
		if svc.RuntimeConfig == nil {
			svc.RuntimeConfig = &domain.ServiceRuntimeConfig{}
		}
		svc.RuntimeConfig.Managed = managed
	}
	if err := h.registry.UpdateService(ctx, svc); err != nil {
		return nil, fmt.Errorf("failed to update service: %w", err)
	}
	return map[string]any{"status": "updated", "service": svc, "service_id": svc.ID.String(), "idempotency_key": effectiveIdempotencyKey(request, payload.IdempotencyKey)}, nil
}

func (h *EncryptedRouteHandlers) DeleteService(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil {
		return nil, fmt.Errorf("service registry mutation handling is not configured")
	}
	var payload dto.DeleteServiceRequest
	if err := decodeStrictContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	authorizer := encryptedTenantAuthorizer{services: h.services, environments: h.registry, rbac: h.rbac}
	if _, err := authorizer.authorizeService(ctx, request.Event, payload.ID, domain.PermWriteServices); err != nil {
		return nil, err
	}
	if err := h.registry.DeleteService(ctx, payload.ID, payload.Force); err != nil {
		return nil, fmt.Errorf("failed to delete service: %w", err)
	}
	return map[string]any{"status": "deleted", "service_id": payload.ID.String(), "idempotency_key": effectiveIdempotencyKey(request, payload.IdempotencyKey)}, nil
}

func repositoryRefFromRequest(request *dto.RepositoryRefRequest) *domain.RepositoryRef {
	if request == nil {
		return nil
	}
	ref := &domain.RepositoryRef{
		Source:         strings.TrimSpace(request.Source),
		RepoCoordinate: strings.TrimSpace(request.RepoCoordinate),
		CloneURL:       strings.TrimSpace(request.CloneURL),
		WebURL:         strings.TrimSpace(request.WebURL),
		RelayURLs:      append([]string(nil), request.RelayURLs...),
	}
	if request.CI != nil {
		ref.CI = &domain.ServiceCIConfig{Provider: strings.TrimSpace(request.CI.Provider), WorkflowPath: strings.TrimSpace(request.CI.WorkflowPath)}
	}
	return ref
}

func effectiveIdempotencyKey(request ContextVMRequest, compatibilityKey string) string {
	if token := strings.TrimSpace(request.ProgressToken); token != "" {
		return token
	}
	return strings.TrimSpace(compatibilityKey)
}

func (h *EncryptedRouteHandlers) CreateEnvironment(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil {
		return nil, fmt.Errorf("environment registry mutation handling is not configured")
	}
	var payload encryptedEnvironmentCreatePayload
	if err := decodeStrictContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if payload.OrgID == uuid.Nil {
		return nil, fmt.Errorf("org_id is required")
	}
	deployStrategy := domain.DeployStrategyReplace
	if strings.TrimSpace(payload.DeployStrategy) != "" {
		deployStrategy = domain.DeployStrategy(strings.TrimSpace(payload.DeployStrategy))
		if err := domain.ValidateDeployStrategy(deployStrategy); err != nil {
			return nil, fmt.Errorf("invalid deploy_strategy: %w", err)
		}
	}
	var selector map[string]any
	if payload.LoomWorkerSelector != nil {
		parsed, err := parseLoomWorkerSelector(payload.LoomWorkerSelector)
		if err != nil {
			return nil, err
		}
		selector = parsed
	}
	env := &domain.Environment{
		ID:                 uuid.New(),
		OrgID:              payload.OrgID,
		Name:               name,
		LoomWorkerSelector: selector,
		RuntimeConfig:      payload.RuntimeConfig,
		Targeting:          environmentTargetingFromRequest(payload.Targeting),
		DeployStrategy:     deployStrategy,
		Protected:          payload.Protected,
	}
	if err := applyEnvironmentReconcileMode(env, payload.ReconcileMode); err != nil {
		return nil, err
	}
	units, err := deploymentUnitsFromRequests(env, payload.DeploymentUnits)
	if err != nil {
		return nil, err
	}
	if err := h.authorizeEnvironmentOrg(ctx, request, env.OrgID); err != nil {
		return nil, err
	}
	if payload.DeploymentUnits != nil {
		if err := h.registry.CreateEnvironmentWithDeploymentUnits(ctx, env, units); err != nil {
			return nil, fmt.Errorf("failed to create environment: %w", err)
		}
	} else if err := h.registry.CreateEnvironment(ctx, env); err != nil {
		return nil, fmt.Errorf("failed to create environment: %w", err)
	}
	return map[string]any{"status": "created", "environment": env, "environment_id": env.ID.String(), "deployment_units": units}, nil
}

func (h *EncryptedRouteHandlers) UpdateEnvironment(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil {
		return nil, fmt.Errorf("environment registry mutation handling is not configured")
	}
	var payload encryptedEnvironmentUpdatePayload
	if err := decodeStrictContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	id, err := parseEncryptedUUID(payload.ID, "environment ID")
	if err != nil {
		return nil, err
	}
	env, err := h.registry.GetEnvironment(ctx, id)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("environment not found")
		}
		return nil, fmt.Errorf("failed to fetch environment")
	}
	if env == nil {
		return nil, fmt.Errorf("environment not found")
	}
	if err := h.authorizeEnvironmentOrg(ctx, request, env.OrgID); err != nil {
		return nil, err
	}
	if payload.OrgID != nil {
		if *payload.OrgID == uuid.Nil {
			return nil, fmt.Errorf("org_id must not be nil")
		}
		if *payload.OrgID != env.OrgID {
			if err := h.authorizeEnvironmentOrg(ctx, request, *payload.OrgID); err != nil {
				return nil, err
			}
			env.OrgID = *payload.OrgID
		}
	}
	if strings.TrimSpace(payload.Name) != "" {
		env.Name = strings.TrimSpace(payload.Name)
	}
	if payload.LoomWorkerSelector != nil {
		selector, err := parseLoomWorkerSelector(payload.LoomWorkerSelector)
		if err != nil {
			return nil, err
		}
		env.LoomWorkerSelector = selector
	}
	if payload.RuntimeConfig != nil {
		env.RuntimeConfig = payload.RuntimeConfig
	}
	if payload.Targeting != nil {
		env.Targeting = environmentTargetingFromRequest(payload.Targeting)
	}
	if err := applyEnvironmentReconcileMode(env, payload.ReconcileMode); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.DeployStrategy) != "" {
		strategy := domain.DeployStrategy(strings.TrimSpace(payload.DeployStrategy))
		if err := domain.ValidateDeployStrategy(strategy); err != nil {
			return nil, fmt.Errorf("invalid deploy_strategy: %w", err)
		}
		env.DeployStrategy = strategy
	}
	if payload.Protected != nil {
		env.Protected = *payload.Protected
	}
	units, err := deploymentUnitsFromRequests(env, payload.DeploymentUnits)
	if err != nil {
		return nil, err
	}
	if payload.DeploymentUnits != nil {
		if payload.ExpectedUpdatedAt == nil || payload.ExpectedUpdatedAt.IsZero() {
			return nil, fmt.Errorf("expected_updated_at is required when deployment_units is supplied")
		}
		if err := h.registry.UpdateEnvironmentWithDeploymentUnits(ctx, env, units, *payload.ExpectedUpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to update environment: %w", err)
		}
	} else if err := h.registry.UpdateEnvironment(ctx, env); err != nil {
		return nil, fmt.Errorf("failed to update environment: %w", err)
	}
	return map[string]any{"status": "updated", "environment": env, "environment_id": env.ID.String(), "deployment_units": units}, nil
}

func (h *EncryptedRouteHandlers) DeleteEnvironment(ctx context.Context, request ContextVMRequest) (any, error) {
	if h.registry == nil {
		return nil, fmt.Errorf("environment registry mutation handling is not configured")
	}
	var payload encryptedEnvironmentDeletePayload
	if err := decodeStrictContextVMParams(request.RPC.Params, &payload); err != nil {
		return nil, err
	}
	id, err := parseEncryptedUUID(payload.ID, "environment ID")
	if err != nil {
		return nil, err
	}
	authorizer := encryptedTenantAuthorizer{services: h.services, environments: h.registry, rbac: h.rbac}
	if _, err := authorizer.authorizeEnvironment(ctx, request.Event, id, domain.PermWriteEnvironments); err != nil {
		return nil, err
	}
	if err := h.registry.DeleteEnvironment(ctx, id, payload.Force); err != nil {
		return nil, fmt.Errorf("failed to delete environment: %w", err)
	}
	return map[string]any{"status": "deleted", "environment_id": id.String()}, nil
}

func decodeStrictContextVMParams(params json.RawMessage, out any) error {
	if len(params) == 0 || string(params) == "null" {
		params = []byte(`{}`)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(params, &envelope); err != nil {
		return fmt.Errorf("invalid environment params: %w", err)
	}
	delete(envelope, "_meta")
	businessParams, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("invalid environment params: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(businessParams))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid environment params: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid environment params: multiple JSON values")
		}
		return fmt.Errorf("invalid environment params: %w", err)
	}
	return nil
}

func environmentTargetingFromRequest(request *dto.EnvironmentTargetingRequest) domain.EnvironmentTargeting {
	if request == nil {
		return domain.EnvironmentTargeting{}
	}
	return domain.EnvironmentTargeting{
		DefaultUnitKey:       strings.TrimSpace(request.DefaultUnitKey),
		FailureDomainLabels:  request.FailureDomainLabels,
		SecretScopeMode:      domain.SecretScopeMode(strings.TrimSpace(request.SecretScopeMode)),
		DefaultReconcileMode: domain.ReconcileMode(strings.TrimSpace(request.DefaultReconcileMode)),
	}
}

func applyEnvironmentReconcileMode(env *domain.Environment, value string) error {
	if env == nil {
		return fmt.Errorf("environment is nil")
	}
	mode := domain.ReconcileMode(strings.TrimSpace(value))
	if err := domain.ValidateReconcileMode(mode); err != nil {
		return fmt.Errorf("invalid reconcile_mode: %w", err)
	}
	if mode != "" {
		env.Targeting.DefaultReconcileMode = mode
	}
	domain.NormalizeEnvironmentTargeting(env)
	if err := domain.ValidateReconcileMode(env.Targeting.DefaultReconcileMode); err != nil {
		return fmt.Errorf("invalid targeting.default_reconcile_mode: %w", err)
	}
	switch env.Targeting.SecretScopeMode {
	case "", domain.SecretScopeModeService, domain.SecretScopeModeEnvironment, domain.SecretScopeModeUnit:
	default:
		return fmt.Errorf("invalid targeting.secret_scope_mode %q (allowed: service, environment, unit)", env.Targeting.SecretScopeMode)
	}
	return nil
}

func deploymentUnitsFromRequests(env *domain.Environment, requests []dto.DeploymentUnitRequest) ([]*domain.DeploymentUnit, error) {
	if requests == nil {
		return nil, nil
	}
	defaultRuntime := domain.RuntimeTypeFromRuntimeConfig(env.RuntimeConfig)
	if defaultRuntime == "" {
		defaultRuntime = domain.RuntimeTypeDocker
	}
	units := make([]*domain.DeploymentUnit, 0, len(requests))
	keys := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		unit := &domain.DeploymentUnit{
			ID:             uuid.New(),
			EnvironmentID:  env.ID,
			Key:            strings.TrimSpace(request.Key),
			DisplayName:    strings.TrimSpace(request.DisplayName),
			RuntimeType:    domain.RuntimeType(strings.TrimSpace(request.RuntimeType)),
			EndpointRef:    strings.TrimSpace(request.EndpointRef),
			ComposeDir:     strings.TrimSpace(request.ComposeDir),
			Namespace:      strings.TrimSpace(request.Namespace),
			NetworkProfile: request.NetworkProfile,
			ReconcileMode:  domain.ReconcileMode(strings.TrimSpace(request.ReconcileMode)),
			OwnershipMode:  domain.OwnershipMode(strings.TrimSpace(request.OwnershipMode)),
			RuntimeConfig:  request.RuntimeConfig,
		}
		if unit.RuntimeType == "" {
			unit.RuntimeType = defaultRuntime
		}
		if unit.ReconcileMode == "" {
			unit.ReconcileMode = env.Targeting.DefaultReconcileMode
		}
		domain.NormalizeDeploymentUnitTargeting(unit)
		if err := domain.ValidateDeploymentUnit(unit); err != nil {
			return nil, fmt.Errorf("invalid deployment unit %q: %w", unit.Key, err)
		}
		if _, duplicate := keys[unit.Key]; duplicate {
			return nil, fmt.Errorf("duplicate deployment unit key %q", unit.Key)
		}
		keys[unit.Key] = struct{}{}
		units = append(units, unit)
	}
	if len(units) > 0 {
		if _, ok := keys[env.Targeting.DefaultUnitKey]; !ok {
			return nil, fmt.Errorf("targeting.default_unit_key %q does not identify a deployment unit", env.Targeting.DefaultUnitKey)
		}
	}
	return units, nil
}

func (h *EncryptedRouteHandlers) authorizeEnvironmentOrg(ctx context.Context, request ContextVMRequest, orgID uuid.UUID) error {
	if orgID == uuid.Nil {
		return fmt.Errorf("environment organization is required")
	}
	if h.rbac == nil {
		return fmt.Errorf("environment RBAC is not configured")
	}
	if request.Event == nil {
		return fmt.Errorf("signed environment request event is required")
	}
	return h.rbac.CheckPermission(ctx, requestPrincipal(EncryptedRequest{Event: request.Event}), orgID, domain.PermWriteEnvironments)
}

func parseLoomWorkerSelector(raw json.RawMessage) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var selector map[string]any
		if err := json.Unmarshal(raw, &selector); err != nil {
			return nil, fmt.Errorf("invalid loom_worker_selector object: %w", err)
		}
		return selector, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, fmt.Errorf("loom_worker_selector must be an object or string")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return map[string]any{}, nil
	}
	selector := map[string]any{}
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '\n' })
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			selector["pubkey"] = part
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, fmt.Errorf("loom_worker_selector contains an empty key")
		}
		selector[key] = value
	}
	return selector, nil
}

type encryptedSecretPayload struct {
	ServiceID        string `json:"service_id"`
	SecretID         string `json:"secret_id,omitempty"`
	Name             string `json:"name,omitempty"`
	Value            string `json:"value,omitempty"`
	EnvironmentID    string `json:"environment_id,omitempty"`
	EncryptionMethod string `json:"encryption_method,omitempty"`
}

func (h *EncryptedRouteHandlers) requireSecretDeps() error {
	if h.secrets == nil || h.encryptor == nil {
		return fmt.Errorf("encrypted secret request handling is not configured")
	}
	return nil
}

func (h *EncryptedRouteHandlers) ListSecrets(ctx context.Context, request EncryptedRequest) (any, error) {
	if err := h.requireSecretDeps(); err != nil {
		return nil, err
	}
	var payload encryptedSecretPayload
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	serviceID, err := parseEncryptedUUID(payload.ServiceID, "service ID")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermReadSecrets); err != nil {
		return nil, err
	}
	secrets, err := h.secrets.ListByService(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets")
	}
	refs := make([]domain.SecretRef, 0, len(secrets))
	for i := range secrets {
		refs = append(refs, secrets[i].ToRef())
	}
	return map[string]any{"secrets": refs, "total": len(refs)}, nil
}

func (h *EncryptedRouteHandlers) CreateSecret(ctx context.Context, request EncryptedRequest) (any, error) {
	if err := h.requireSecretDeps(); err != nil {
		return nil, err
	}
	var payload encryptedSecretPayload
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	serviceID, err := parseEncryptedUUID(payload.ServiceID, "service ID")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermWriteSecrets); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if payload.Value == "" {
		return nil, fmt.Errorf("value is required")
	}
	method, err := parseSecretEncryptionMethod(payload.EncryptionMethod, domain.EncryptionNIP44)
	if err != nil {
		return nil, err
	}
	encrypted, err := h.encryptor.Encrypt(payload.Value, method)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt secret")
	}
	now := time.Now().UTC()
	secret := &domain.ServiceSecret{
		ID:               uuid.New(),
		ServiceID:        serviceID,
		Name:             name,
		EncryptedValue:   encrypted,
		EncryptionMethod: method,
		Version:          1,
		CreatedBy:        normalizeEncryptedPubkey(request.Event.PubKey.Hex()),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if strings.TrimSpace(payload.EnvironmentID) != "" {
		envID, err := parseEncryptedUUID(payload.EnvironmentID, "environment ID")
		if err != nil {
			return nil, err
		}
		secret.EnvironmentID = &envID
	}
	if err := h.secrets.Create(ctx, secret); err != nil {
		return nil, fmt.Errorf("failed to create secret")
	}
	return map[string]any{"secret": secret.ToRef(), "status": "created"}, nil
}

func (h *EncryptedRouteHandlers) UpdateSecret(ctx context.Context, request EncryptedRequest) (any, error) {
	if err := h.requireSecretDeps(); err != nil {
		return nil, err
	}
	var payload encryptedSecretPayload
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	serviceID, err := parseEncryptedUUID(payload.ServiceID, "service ID")
	if err != nil {
		return nil, err
	}
	secretID, err := parseEncryptedUUID(payload.SecretID, "secret ID")
	if err != nil {
		return nil, err
	}
	if payload.Value == "" {
		return nil, fmt.Errorf("value is required")
	}
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermWriteSecrets); err != nil {
		return nil, err
	}
	secret, err := h.secretForService(ctx, serviceID, secretID)
	if err != nil {
		return nil, err
	}
	method, err := parseSecretEncryptionMethod(payload.EncryptionMethod, secret.EncryptionMethod)
	if err != nil {
		return nil, err
	}
	encrypted, err := h.encryptor.Encrypt(payload.Value, method)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt secret")
	}
	secret.EncryptedValue = encrypted
	secret.EncryptionMethod = method
	secret.UpdatedAt = time.Now().UTC()
	if err := h.secrets.Update(ctx, secret); err != nil {
		return nil, fmt.Errorf("failed to update secret: %w", err)
	}
	if updated, _ := h.secrets.GetByID(ctx, secretID); updated != nil {
		secret = updated
	}
	return map[string]any{"secret": secret.ToRef(), "status": "updated"}, nil
}

func (h *EncryptedRouteHandlers) DeleteSecret(ctx context.Context, request EncryptedRequest) (any, error) {
	if err := h.requireSecretDeps(); err != nil {
		return nil, err
	}
	var payload encryptedSecretPayload
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	serviceID, err := parseEncryptedUUID(payload.ServiceID, "service ID")
	if err != nil {
		return nil, err
	}
	secretID, err := parseEncryptedUUID(payload.SecretID, "secret ID")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermWriteSecrets); err != nil {
		return nil, err
	}
	if _, err := h.secretForService(ctx, serviceID, secretID); err != nil {
		return nil, err
	}
	if err := h.secrets.Delete(ctx, secretID); err != nil {
		return nil, fmt.Errorf("failed to delete secret")
	}
	return map[string]string{"status": "deleted", "secret_id": secretID.String()}, nil
}

func (h *EncryptedRouteHandlers) RevealSecret(ctx context.Context, request EncryptedRequest) (any, error) {
	if err := h.requireSecretDeps(); err != nil {
		return nil, err
	}
	var payload encryptedSecretPayload
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	serviceID, err := parseEncryptedUUID(payload.ServiceID, "service ID")
	if err != nil {
		return nil, err
	}
	secretID, err := parseEncryptedUUID(payload.SecretID, "secret ID")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeServicePermission(ctx, request, serviceID, domain.PermReadSecrets); err != nil {
		return nil, err
	}
	secret, err := h.secretForService(ctx, serviceID, secretID)
	if err != nil {
		return nil, err
	}
	value, err := h.encryptor.Decrypt(secret.EncryptedValue, secret.EncryptionMethod)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt secret")
	}
	return map[string]any{"secret": secret.ToRef(), "value": value}, nil
}

func (h *EncryptedRouteHandlers) secretForService(ctx context.Context, serviceID, secretID uuid.UUID) (*domain.ServiceSecret, error) {
	secret, err := h.secrets.GetByID(ctx, secretID)
	if err != nil {
		return nil, fmt.Errorf("failed to look up secret")
	}
	if secret == nil {
		return nil, fmt.Errorf("secret not found")
	}
	if secret.ServiceID != serviceID {
		return nil, fmt.Errorf("secret does not belong to service")
	}
	return secret, nil
}

func (h *EncryptedRouteHandlers) authorizeServicePermission(ctx context.Context, request EncryptedRequest, serviceID uuid.UUID, permission domain.Permission) error {
	if h.services == nil || h.rbac == nil {
		return fmt.Errorf("encrypted route RBAC is not configured")
	}
	service, err := h.services.GetByID(ctx, serviceID)
	if err != nil {
		if err == repository.ErrNotFound {
			return fmt.Errorf("service not found")
		}
		return fmt.Errorf("failed to fetch service")
	}
	if service == nil {
		return fmt.Errorf("service not found")
	}
	if service.OrgID == uuid.Nil {
		return fmt.Errorf("service organization is not configured")
	}
	return h.rbac.CheckPermission(ctx, requestPrincipal(request), service.OrgID, permission)
}

func parseSecretEncryptionMethod(value string, fallback domain.EncryptionMethod) (domain.EncryptionMethod, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	method := domain.EncryptionMethod(strings.TrimSpace(value))
	switch method {
	case domain.EncryptionNIP44, domain.EncryptionAES256:
		return method, nil
	default:
		return "", fmt.Errorf("encryption_method must be 'nip44' or 'aes256gcm'")
	}
}

func (h *EncryptedRouteHandlers) GetRunLogs(ctx context.Context, request EncryptedRequest) (any, error) {
	if h.runs == nil || h.runLogs == nil {
		return nil, fmt.Errorf("encrypted deployment run log retrieval is not configured")
	}
	var payload struct {
		RunID  string `json:"run_id"`
		Tail   int    `json:"tail,omitempty"`
		Stream string `json:"stream,omitempty"`
	}
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	runID, err := parseEncryptedUUID(payload.RunID, "run ID")
	if err != nil {
		return nil, err
	}
	run, err := h.runs.GetByID(ctx, runID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("deployment run not found")
		}
		return nil, fmt.Errorf("failed to fetch run")
	}
	if run == nil {
		return nil, fmt.Errorf("deployment run not found")
	}
	if err := h.authorizeRunPermission(ctx, request, run, domain.PermReadLogs); err != nil {
		return nil, err
	}
	if !isTerminalEncryptedRunStatus(run.Status) {
		return nil, fmt.Errorf("run is still in progress; stored logs are available after completion")
	}
	logs, err := h.runLogs.FetchRunLogs(ctx, run)
	if err != nil {
		h.logger.Error("failed to fetch encrypted request run logs", zap.String("run_id", runID.String()), zap.Error(err))
		return nil, fmt.Errorf("failed to fetch logs")
	}
	if logs == nil {
		logs = &adapterruntime.RunLogs{RunID: runID}
	}
	if payload.Tail > 0 {
		logs.Stdout = adapterruntime.TailLogs(logs.Stdout, payload.Tail)
		logs.Stderr = adapterruntime.TailLogs(logs.Stderr, payload.Tail)
	}
	stream := strings.TrimSpace(payload.Stream)
	if stream == "" {
		stream = "merged"
	}
	switch stream {
	case "stdout":
		logs.Stderr = ""
	case "stderr":
		logs.Stdout = ""
	case "merged":
		// keep both streams for tabbed UI callers
	default:
		return nil, fmt.Errorf("invalid stream parameter; use stdout, stderr, or merged")
	}
	return map[string]any{"logs": logs, "stream": stream}, nil
}

func (h *EncryptedRouteHandlers) authorizeRunPermission(ctx context.Context, request EncryptedRequest, run *domain.DeploymentRun, permission domain.Permission) error {
	if h.intents == nil {
		return fmt.Errorf("deployment intent lookup is not configured")
	}
	intent, err := h.intents.GetByID(ctx, run.DeploymentIntentID)
	if err != nil {
		if err == repository.ErrNotFound {
			return fmt.Errorf("deployment intent not found")
		}
		return fmt.Errorf("failed to fetch deployment intent")
	}
	if intent == nil {
		return fmt.Errorf("deployment intent not found")
	}
	return h.authorizeServicePermission(ctx, request, intent.ServiceID, permission)
}

func isTerminalEncryptedRunStatus(status domain.DeploymentRunStatus) bool {
	switch status {
	case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return true
	default:
		return false
	}
}

func (h *EncryptedRouteHandlers) VerifyArtifactSignatures(ctx context.Context, request EncryptedRequest) (any, error) {
	if h.signatures == nil || h.artifacts == nil || h.signVerifier == nil {
		return nil, fmt.Errorf("encrypted artifact signature request handling is not configured")
	}
	var payload struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := decodeEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	artifactID, err := parseEncryptedUUID(payload.ArtifactID, "artifact ID")
	if err != nil {
		return nil, err
	}
	artifact, err := h.artifacts.GetByID(ctx, artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("artifact not found")
		}
		return nil, fmt.Errorf("failed to fetch artifact")
	}
	if artifact == nil {
		return nil, fmt.Errorf("artifact not found")
	}
	if err := h.authorizeServicePermission(ctx, request, artifact.ServiceID, domain.PermWriteServices); err != nil {
		return nil, err
	}
	sigs, err := h.signVerifier.VerifySignatures(ctx, artifact)
	if err != nil {
		return nil, fmt.Errorf("verifying signatures: %w", err)
	}
	var stored int
	counts := map[domain.SignatureVerificationStatus]int{
		domain.SignatureStatusVerified:   0,
		domain.SignatureStatusDiscovered: 0,
		domain.SignatureStatusRejected:   0,
		domain.SignatureStatusError:      0,
	}
	for i := range sigs {
		sig := &sigs[i]
		if sig.ID == uuid.Nil {
			sig.ID = uuid.New()
		}
		if sig.ArtifactID == uuid.Nil {
			sig.ArtifactID = artifactID
		}
		sig.NormalizeVerificationStatus()
		counts[sig.VerificationStatus]++
		if err := h.signatures.Create(ctx, sig); err != nil {
			h.logger.Warn("failed to store signature record", zap.String("artifact_id", artifactID.String()), zap.String("signature_id", sig.ID.String()), zap.Error(err))
			continue
		}
		stored++
	}
	return map[string]any{
		"artifact_id": artifactID.String(),
		"found":       len(sigs),
		"stored":      stored,
		"verified":    counts[domain.SignatureStatusVerified],
		"discovered":  counts[domain.SignatureStatusDiscovered],
		"rejected":    counts[domain.SignatureStatusRejected],
		"errors":      counts[domain.SignatureStatusError],
		"signatures":  sigs,
	}, nil
}
