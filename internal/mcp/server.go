// Package mcp provides an MCP (Model Context Protocol) server for Bahia operations.
// This allows AI agents to interact with the deployment registry programmatically.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	adapterSBOM "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Server provides an MCP-compatible interface for Bahia operations.
// It exposes deployment registry functionality as MCP tools.
type Server struct {
	registry             *service.RegistryService
	mlRegistry           *service.MLRegistryService
	llmRegistry          *service.LLMRegistryService
	mlCommands           MLCommandPublisher
	llmCommands          LLMCommandPublisher
	serviceCommands      ServiceCommandPublisher
	artifactCommands     ArtifactCommandPublisher
	packageCommands      PackageCommandPublisher
	policyCommands       PolicyCommandPublisher
	toolApprovalCommands ToolApprovalCommandPublisher
	workerCommands       WorkerCommandPublisher
	backupCommands       BackupCommandPublisher
	dnsCommands          DNSCommandPublisher
	packageProjection    repository.PackageControlPlaneRepository
	workerReadModels     *service.WorkerReadModelService
	backupReadModels     BackupReadModelRepository
	logger               *zap.Logger
	secretsRepo          repository.SecretRepository       // optional: for secret management tools
	encryptor            *secrets.Encryptor                // optional: for secret encryption/decryption
	policies             *service.PolicyService            // optional: for policy management tools
	notificationRepo     repository.NotificationRepository // optional: for notification tools
	notificationDisp     *notifications.Dispatcher         // optional: for notification testing
	workers              repository.WorkerRepository       // optional: for worker management tools
	logService           *adapterruntime.LogService        // optional: for deployment run log tools
	payments             *service.PaymentService           // optional: for payment tools
	sboms                repository.SBOMRepository         // optional: for SBOM tools
	signatures           repository.ArtifactSignatureRepository
	signVerifier         SignatureVerifier
	toolProvisioning     repository.ToolProvisioningRepository
	dnsEndpoints         DNSEndpointLister
	authorizedPubkeys    []string
	rbac                 *auth.RBAC
}

// Config holds MCP server configuration.
type Config struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// ServerDeps holds optional dependencies for the MCP server.
type ServerDeps struct {
	SecretsRepo                  repository.SecretRepository
	Encryptor                    *secrets.Encryptor
	Policies                     *service.PolicyService
	NotificationRepo             repository.NotificationRepository
	NotificationDispatcher       *notifications.Dispatcher
	Workers                      repository.WorkerRepository
	LogService                   *adapterruntime.LogService
	Payments                     *service.PaymentService
	SBOMs                        repository.SBOMRepository
	Signatures                   repository.ArtifactSignatureRepository
	SignVerifier                 SignatureVerifier
	ToolProvisioning             repository.ToolProvisioningRepository
	MLRegistry                   *service.MLRegistryService
	MLCommandPublisher           MLCommandPublisher
	LLMRegistry                  *service.LLMRegistryService
	LLMCommandPublisher          LLMCommandPublisher
	ServiceCommandPublisher      ServiceCommandPublisher
	ArtifactCommandPublisher     ArtifactCommandPublisher
	PackageCommandPublisher      PackageCommandPublisher
	PolicyCommandPublisher       PolicyCommandPublisher
	ToolApprovalCommandPublisher ToolApprovalCommandPublisher
	WorkerCommandPublisher       WorkerCommandPublisher
	BackupCommandPublisher       BackupCommandPublisher
	DNSCommandPublisher          DNSCommandPublisher
	PackageProjection            repository.PackageControlPlaneRepository
	WorkerReadModels             *service.WorkerReadModelService
	BackupReadModels             BackupReadModelRepository
	DNSEndpoints                 DNSEndpointLister
	// AuthorizedPubkeys is the explicit operator allowlist for external MCP callers.
	// An empty allowlist denies all non-system callers.
	AuthorizedPubkeys []string
	// RBAC is required for tenant-scoped authorization such as secret access.
	RBAC *auth.RBAC
}

// SignatureVerifier verifies signatures for an artifact.
type SignatureVerifier interface {
	VerifySignatures(ctx context.Context, artifact *domain.Artifact) ([]domain.ArtifactSignature, error)
}

// MLCommandPublisher emits canonical Nostr request events for signer-first ML MCP tools.
type MLCommandPublisher interface {
	PublishMLModelImportRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
	PublishMLRecipeRunRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
	PublishMLInferenceDeployRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
	PublishMLInferenceRollbackRequest(ctx context.Context, cmd controlplane.MLCommandPayload) (*controlplane.MLCommandReceipt, error)
}

// ServiceCommandPublisher emits canonical Nostr request events for assistant-safe service tools.
type ServiceCommandPublisher interface {
	PublishDeployRequest(ctx context.Context, cmd controlplane.ServiceDeployCommand) (*controlplane.ServiceCommandReceipt, error)
	PublishRollbackRequest(ctx context.Context, cmd controlplane.ServiceRollbackCommand) (*controlplane.ServiceCommandReceipt, error)
	PublishDeploymentApprovalRequest(ctx context.Context, cmd controlplane.ServiceApprovalCommand) (*controlplane.ServiceCommandReceipt, error)
}

// ArtifactCommandPublisher emits signer-first artifact registration events.
type ArtifactCommandPublisher interface {
	PublishArtifactRegisterRequest(ctx context.Context, cmd controlplane.ArtifactRegisterCommand) (*controlplane.ArtifactCommandReceipt, error)
}

// LLMCommandPublisher emits canonical Nostr request events for signer-first LLM MCP tools.
type LLMCommandPublisher interface {
	PublishLLMRouteCreateRequest(ctx context.Context, cmd controlplane.LLMRouteCreateCommand) (*controlplane.LLMCommandReceipt, error)
	PublishLLMReleaseRegisterRequest(ctx context.Context, cmd controlplane.LLMReleaseRegisterCommand) (*controlplane.LLMCommandReceipt, error)
	PublishLLMDeployRequest(ctx context.Context, cmd controlplane.LLMDeployCommand) (*controlplane.LLMCommandReceipt, error)
	PublishLLMApprovalRequest(ctx context.Context, cmd controlplane.LLMApprovalCommand) (*controlplane.LLMCommandReceipt, error)
	PublishLLMRollbackRequest(ctx context.Context, cmd controlplane.LLMRollbackCommand) (*controlplane.LLMCommandReceipt, error)
}

// PackageCommandPublisher emits canonical Nostr request events for signer-first package MCP tools.
type PackageCommandPublisher interface {
	PublishPackageRepositoryApplyRequest(ctx context.Context, cmd controlplane.PackageRepositoryApplyCommand) (*controlplane.PackageCommandReceipt, error)
	PublishPackageRepositoryDeleteRequest(ctx context.Context, cmd controlplane.PackageRepositoryDeleteCommand) (*controlplane.PackageCommandReceipt, error)
	PublishPackagePublishRequest(ctx context.Context, cmd controlplane.PackagePublishCommand) (*controlplane.PackageCommandReceipt, error)
	PublishPackagePromotionRequest(ctx context.Context, cmd controlplane.PackagePromotionCommand) (*controlplane.PackageCommandReceipt, error)
	PublishPackageYankRequest(ctx context.Context, cmd controlplane.PackageYankCommand) (*controlplane.PackageCommandReceipt, error)
	PublishPackageDriftDetectRequest(ctx context.Context, cmd controlplane.PackageDriftDetectCommand) (*controlplane.PackageCommandReceipt, error)
}

// PolicyCommandPublisher emits canonical public Nostr policy request events for signer-first MCP tools.
type PolicyCommandPublisher interface {
	PublishPolicyCreateRequest(ctx context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error)
	PublishPolicyUpdateRequest(ctx context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error)
	PublishPolicyDeleteRequest(ctx context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error)
	PublishPolicyEvaluateRequest(ctx context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error)
}

// ToolApprovalCommandPublisher emits canonical public Nostr tool approval response events.
type ToolApprovalCommandPublisher interface {
	PublishToolApprovalResponse(ctx context.Context, cmd controlplane.ToolApprovalCommand) (*controlplane.ToolApprovalCommandReceipt, error)
}

// NewServer creates a new MCP server for Bahia.
func NewServer(registry *service.RegistryService, logger *zap.Logger) *Server {
	return NewServerWithOptions(registry, logger, ServerDeps{})
}

// NewServerWithDeps creates a new MCP server with optional dependencies.
// secretsRepo and encryptor are optional; if nil, secret management tools will return errors.
func NewServerWithDeps(registry *service.RegistryService, logger *zap.Logger, secretsRepo repository.SecretRepository, encryptor *secrets.Encryptor) *Server {
	return NewServerWithOptions(registry, logger, ServerDeps{
		SecretsRepo: secretsRepo,
		Encryptor:   encryptor,
	})
}

// NewServerWithOptions creates a new MCP server with optional dependencies.
// This is the canonical constructor; other constructors delegate to this.
func NewServerWithOptions(registry *service.RegistryService, logger *zap.Logger, deps ServerDeps) *Server {
	return &Server{
		registry:             registry,
		mlRegistry:           deps.MLRegistry,
		llmRegistry:          deps.LLMRegistry,
		mlCommands:           deps.MLCommandPublisher,
		llmCommands:          deps.LLMCommandPublisher,
		serviceCommands:      deps.ServiceCommandPublisher,
		artifactCommands:     deps.ArtifactCommandPublisher,
		packageCommands:      deps.PackageCommandPublisher,
		policyCommands:       deps.PolicyCommandPublisher,
		toolApprovalCommands: deps.ToolApprovalCommandPublisher,
		workerCommands:       deps.WorkerCommandPublisher,
		backupCommands:       deps.BackupCommandPublisher,
		dnsCommands:          deps.DNSCommandPublisher,
		packageProjection:    deps.PackageProjection,
		workerReadModels:     deps.WorkerReadModels,
		backupReadModels:     deps.BackupReadModels,
		logger:               logger,
		secretsRepo:          deps.SecretsRepo,
		encryptor:            deps.Encryptor,
		policies:             deps.Policies,
		notificationRepo:     deps.NotificationRepo,
		notificationDisp:     deps.NotificationDispatcher,
		workers:              deps.Workers,
		logService:           deps.LogService,
		payments:             deps.Payments,
		sboms:                deps.SBOMs,
		signatures:           deps.Signatures,
		signVerifier:         deps.SignVerifier,
		toolProvisioning:     deps.ToolProvisioning,
		dnsEndpoints:         deps.DNSEndpoints,
		authorizedPubkeys:    normalizePubkeys(deps.AuthorizedPubkeys),
		rbac:                 deps.RBAC,
	}
}

// --- MCP Tool Definitions ---

// Tool represents an MCP tool definition.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// ToolResult represents the result of an MCP tool call.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Content represents content in an MCP response.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// GetTools returns the list of available MCP tools.
func (s *Server) GetTools() []Tool {
	tools := []Tool{
		// Service operations
		{
			Name:        "bahia_list_services",
			Description: "List all registered services in the deployment registry",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "bahia_get_service",
			Description: "Get details for a specific service",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "The service's unique identifier (UUID)",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "The service name (alternative to service_id)",
					},
				},
			},
		},
		{
			Name:        "bahia_create_service",
			Description: "Deprecated: direct registry writes are removed; publish signer-first ContextVM/Nostr method service/create instead",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Unique name for the service",
					},
					"artifact_repo": map[string]interface{}{
						"type":        "string",
						"description": "Container image repository path",
					},
					"repo_url": map[string]interface{}{
						"type":        "string",
						"description": "Source code repository URL (optional)",
					},
					"runtime_type": map[string]interface{}{
						"type":        "string",
						"description": "Target runtime type",
						"enum":        []string{"docker", "compose", "kubernetes", "podman", "vm-firecracker", "vm-qemu"},
						"default":     "docker",
					},
				},
				"required": []string{"name", "artifact_repo"},
			},
		},
		{
			Name:        "bahia_update_service",
			Description: "Deprecated: direct registry writes are removed; publish signer-first ContextVM/Nostr method service/update instead",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID to update",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "New service name (optional)",
					},
					"repo_url": map[string]interface{}{
						"type":        "string",
						"description": "New source code repository URL (optional)",
					},
					"artifact_repo": map[string]interface{}{
						"type":        "string",
						"description": "New container image repository path (optional)",
					},
					"default_branch": map[string]interface{}{
						"type":        "string",
						"description": "New default branch (optional)",
					},
					"runtime_type": map[string]interface{}{
						"type":        "string",
						"description": "New runtime type (optional)",
						"enum":        []string{"docker", "compose", "kubernetes", "podman", "vm-firecracker", "vm-qemu"},
					},
				},
				"required": []string{"service_id"},
			},
		},
		// Environment operations
		{
			Name:        "bahia_list_environments",
			Description: "List all deployment environments",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "bahia_get_environment",
			Description: "Get details for a specific environment",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "The environment's unique identifier (UUID)",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "The environment name (alternative to environment_id)",
					},
				},
			},
		},
		{
			Name:        "bahia_create_environment",
			Description: "Deprecated: direct registry writes are removed; publish signer-first ContextVM/Nostr method environment/create instead",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Unique name for the environment",
					},
					"protected": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether deployments require approval",
						"default":     false,
					},
					"deploy_strategy": map[string]interface{}{
						"type":        "string",
						"description": "Deployment strategy",
						"enum":        []string{"replace", "blue_green", "canary"},
						"default":     "replace",
					},
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "bahia_update_environment",
			Description: "Deprecated: direct registry writes are removed; publish signer-first ContextVM/Nostr method environment/update instead",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Environment UUID to update",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "New environment name (optional)",
					},
					"loom_worker_selector": map[string]interface{}{
						"type":        "object",
						"description": "New Loom worker selector criteria (optional)",
					},
					"runtime_config": map[string]interface{}{
						"type":        "object",
						"description": "New runtime configuration (optional)",
					},
					"deploy_strategy": map[string]interface{}{
						"type":        "string",
						"description": "New deployment strategy (optional)",
						"enum":        []string{"replace", "blue_green", "canary"},
					},
					"protected": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether deployments require approval (optional)",
					},
				},
				"required": []string{"environment_id"},
			},
		},
		// Deployment operations
		{
			Name:        "bahia_deploy",
			Description: "Create a deployment intent to deploy a service to an environment",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID to deploy",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Target environment UUID",
					},
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID to deploy",
					},
					"requested_by": map[string]interface{}{
						"type":        "string",
						"description": "Identity of the requester",
					},
				},
				"required": []string{"service_id", "environment_id", "artifact_id"},
			},
		},
		{
			Name:        "bahia_rollback",
			Description: "Rollback a service to its previous successful deployment",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID to rollback",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Target environment UUID",
					},
					"requested_by": map[string]interface{}{
						"type":        "string",
						"description": "Identity of the requester",
					},
				},
				"required": []string{"service_id", "environment_id"},
			},
		},
		{
			Name:        "bahia_get_deployment_status",
			Description: "Get the current deployment status for a service in an environment",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Environment UUID",
					},
				},
				"required": []string{"service_id", "environment_id"},
			},
		},
		{
			Name:        "bahia_approve_deployment",
			Description: "Approve a pending deployment intent",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"intent_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment intent UUID to approve",
					},
				},
				"required": []string{"intent_id"},
			},
		},
		{
			Name:        "bahia_reject_deployment",
			Description: "Reject a pending deployment intent",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"intent_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment intent UUID to reject",
					},
				},
				"required": []string{"intent_id"},
			},
		},
		// LLM route/release registry operations
		{
			Name:        "bahia_llm_create_route",
			Description: "Publish a canonical LLM route-create request event and return correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name":                     map[string]interface{}{"type": "string", "description": "Unique LLM route name"},
					"description":              map[string]interface{}{"type": "string", "description": "Optional route description"},
					"gateway_config":           map[string]interface{}{"type": "object", "description": "Gateway route configuration"},
					"default_placement_policy": map[string]interface{}{"type": "object", "description": "Default placement policy"},
					"default_promotion_gate":   map[string]interface{}{"type": "object", "description": "Default promotion gate"},
					"metadata":                 map[string]interface{}{"type": "object", "description": "Additional metadata"},
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "bahia_llm_update_route",
			Description: "Update an LLM route registry entry",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"route_id":                 map[string]interface{}{"type": "string", "description": "LLM route UUID"},
					"description":              map[string]interface{}{"type": "string", "description": "Replacement route description"},
					"gateway_config":           map[string]interface{}{"type": "object", "description": "Replacement gateway route configuration"},
					"default_placement_policy": map[string]interface{}{"type": "object", "description": "Replacement default placement policy"},
					"default_promotion_gate":   map[string]interface{}{"type": "object", "description": "Replacement default promotion gate"},
					"metadata":                 map[string]interface{}{"type": "object", "description": "Replacement metadata"},
				},
				"required": []string{"route_id"},
			},
		},
		{
			Name:        "bahia_llm_register_release",
			Description: "Publish a canonical LLM release-register request event and return correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"route_id":            map[string]interface{}{"type": "string", "description": "LLM route UUID"},
					"version":             map[string]interface{}{"type": "string", "description": "Release version"},
					"model_ref":           map[string]interface{}{"type": "string", "description": "Model reference"},
					"model_source":        map[string]interface{}{"type": "string", "description": "Model source"},
					"model_revision":      map[string]interface{}{"type": "string", "description": "Optional model revision"},
					"estimated_vram_gb":   map[string]interface{}{"type": "integer", "description": "Estimated VRAM in GB"},
					"backend_preferences": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Preferred backend kinds"},
					"runtime_backend":     map[string]interface{}{"type": "object", "description": "Managed runtime backend config"},
					"external_backend":    map[string]interface{}{"type": "object", "description": "External backend config"},
					"placement_policy":    map[string]interface{}{"type": "object", "description": "Release placement policy"},
					"promotion_gate":      map[string]interface{}{"type": "object", "description": "Release promotion gate"},
					"metadata":            map[string]interface{}{"type": "object", "description": "Additional metadata"},
				},
				"required": []string{"route_id", "version", "model_ref", "model_source"},
			},
		},
		{
			Name:        "bahia_llm_list_routes",
			Description: "List LLM route registry entries",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"limit": map[string]interface{}{"type": "integer"}, "offset": map[string]interface{}{"type": "integer"}}},
		},
		{
			Name:        "bahia_llm_list_releases",
			Description: "List LLM releases for a route",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"route_id": map[string]interface{}{"type": "string"}, "limit": map[string]interface{}{"type": "integer"}, "offset": map[string]interface{}{"type": "integer"}}, "required": []string{"route_id"}},
		},
		// Async LLM Nostr command operations
		{
			Name:        "bahia_llm_deploy",
			Description: "Publish a canonical LLM deploy request event and return correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"route_id":       map[string]interface{}{"type": "string", "description": "LLM route UUID"},
					"environment_id": map[string]interface{}{"type": "string", "description": "Target environment UUID"},
					"release_id":     map[string]interface{}{"type": "string", "description": "LLM release UUID"},
					"requested_by":   map[string]interface{}{"type": "string", "description": "Requester identity"},
					"metadata":       map[string]interface{}{"type": "object", "description": "Additional request metadata"},
				},
				"required": []string{"route_id", "environment_id", "release_id"},
			},
		},
		{
			Name:        "bahia_llm_approve_deployment",
			Description: "Publish a canonical LLM deployment approval request event and return correlation metadata",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"intent_id": map[string]interface{}{"type": "string"}}, "required": []string{"intent_id"}},
		},
		{
			Name:        "bahia_llm_reject_deployment",
			Description: "Publish a canonical LLM deployment rejection request event and return correlation metadata",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"intent_id": map[string]interface{}{"type": "string"}}, "required": []string{"intent_id"}},
		},
		{
			Name:        "bahia_llm_rollback",
			Description: "Publish a canonical LLM rollback request event and return correlation metadata",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"route_id": map[string]interface{}{"type": "string"}, "environment_id": map[string]interface{}{"type": "string"}, "requested_by": map[string]interface{}{"type": "string"}}, "required": []string{"route_id", "environment_id"}},
		},
		{
			Name:        "bahia_delete_service",
			Description: "Deprecated: direct registry writes are removed; publish signer-first ContextVM/Nostr method service/delete instead",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID to delete",
					},
					"force": map[string]interface{}{
						"type":        "boolean",
						"description": "Force delete even if service has deployments",
						"default":     false,
					},
				},
				"required": []string{"service_id"},
			},
		},
		{
			Name:        "bahia_delete_environment",
			Description: "Deprecated: direct registry writes are removed; publish signer-first ContextVM/Nostr method environment/delete instead",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Environment UUID to delete",
					},
					"force": map[string]interface{}{
						"type":        "boolean",
						"description": "Force delete even if environment has deployments",
						"default":     false,
					},
				},
				"required": []string{"environment_id"},
			},
		},
		// Artifact operations
		{
			Name:        "bahia_list_artifacts",
			Description: "List artifacts for a service",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID to list artifacts for",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results",
						"default":     20,
					},
				},
				"required": []string{"service_id"},
			},
		},
		{
			Name:        "bahia_get_artifact",
			Description: "Get details for a specific artifact",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID",
					},
				},
				"required": []string{"artifact_id"},
			},
		},
		{
			Name:        "bahia_register_artifact",
			Description: "Register a new artifact",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"build_id": map[string]interface{}{
						"type":        "string",
						"description": "Build UUID",
					},
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID",
					},
					"image_repo": map[string]interface{}{
						"type":        "string",
						"description": "Container image repository",
					},
					"image_tag": map[string]interface{}{
						"type":        "string",
						"description": "Container image tag",
					},
					"image_digest": map[string]interface{}{
						"type":        "string",
						"description": "Container image digest (sha256:...)",
					},
					"manifest_media_type": map[string]interface{}{
						"type":        "string",
						"description": "OCI manifest media type (optional)",
					},
					"size_bytes": map[string]interface{}{
						"type":        "integer",
						"description": "Artifact size in bytes (optional)",
					},
					"sbom_url": map[string]interface{}{
						"type":        "string",
						"description": "SBOM URL (optional)",
					},
					"signature_ref": map[string]interface{}{
						"type":        "string",
						"description": "Signature reference (optional)",
					},
					"scan_status": map[string]interface{}{
						"type":        "string",
						"description": "Scan status (optional)",
						"enum":        []string{"unknown", "pending", "clean", "warning", "failed"},
					},
					"metadata": map[string]interface{}{
						"type":        "object",
						"description": "Arbitrary artifact metadata (optional)",
					},
				},
				"required": []string{"build_id", "service_id", "image_repo", "image_tag", "image_digest"},
			},
		},
		// Signature operations
		{
			Name:        "bahia_list_signatures",
			Description: "List all signatures recorded for an artifact",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID",
					},
				},
				"required": []string{"artifact_id"},
			},
		},
		{
			Name:        "bahia_list_verified_signatures",
			Description: "List verified signatures recorded for an artifact",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID",
					},
				},
				"required": []string{"artifact_id"},
			},
		},
		{
			Name:        "bahia_has_verified_signature",
			Description: "Check whether an artifact has at least one verified signature",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID",
					},
				},
				"required": []string{"artifact_id"},
			},
		},
		{
			Name:        "bahia_get_signature",
			Description: "Get a signature record by ID",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"signature_id": map[string]interface{}{
						"type":        "string",
						"description": "Signature UUID",
					},
				},
				"required": []string{"signature_id"},
			},
		},
		{
			Name:        "bahia_verify_signatures",
			Description: "Verify signatures for an artifact and store any discovered signature records",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID",
					},
				},
				"required": []string{"artifact_id"},
			},
		},
		// SBOM operations
		{
			Name:        "bahia_get_sbom",
			Description: "Get the parsed SBOM metadata for an artifact",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID",
					},
				},
				"required": []string{"artifact_id"},
			},
		},
		{
			Name:        "bahia_get_sbom_packages",
			Description: "List packages parsed from an artifact's SBOM",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID",
					},
				},
				"required": []string{"artifact_id"},
			},
		},
		{
			Name:        "bahia_search_sbom_packages",
			Description: "Search packages across ingested SBOMs by package name",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Package name search query",
					},
					"package": map[string]interface{}{
						"type":        "string",
						"description": "Package name search query (REST API alias)",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results",
						"default":     100,
					},
				},
			},
		},
		{
			Name:        "bahia_ingest_sbom",
			Description: "Parse and store an SPDX or CycloneDX JSON SBOM document for an artifact",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID",
					},
					"sbom_data": map[string]interface{}{
						"type":        "string",
						"description": "Raw SPDX or CycloneDX JSON SBOM document",
					},
					"source_url": map[string]interface{}{
						"type":        "string",
						"description": "SBOM source URL or OCI referrer (optional)",
					},
				},
				"required": []string{"artifact_id", "sbom_data"},
			},
		},
		// Build operations
		{
			Name:        "bahia_list_builds",
			Description: "List builds for a service",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID to list builds for",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results",
						"default":     20,
					},
				},
				"required": []string{"service_id"},
			},
		},
		{
			Name:        "bahia_get_build",
			Description: "Get details for a specific build",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"build_id": map[string]interface{}{
						"type":        "string",
						"description": "Build UUID",
					},
				},
				"required": []string{"build_id"},
			},
		},
		{
			Name:        "bahia_register_build",
			Description: "Register a new build",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID",
					},
					"git_sha": map[string]interface{}{
						"type":        "string",
						"description": "Git commit SHA",
					},
					"git_ref": map[string]interface{}{
						"type":        "string",
						"description": "Git reference (branch/tag)",
					},
					"ci_system": map[string]interface{}{
						"type":        "string",
						"description": "CI system name (optional, default hive-ci)",
					},
					"ci_run_id": map[string]interface{}{
						"type":        "string",
						"description": "CI run identifier",
					},
					"loom_job_id": map[string]interface{}{
						"type":        "string",
						"description": "Associated Loom job identifier (optional)",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "Build status (optional)",
						"enum":        []string{"queued", "running", "succeeded", "failed", "cancelled"},
					},
					"source_event_id": map[string]interface{}{
						"type":        "string",
						"description": "Source event identifier (optional)",
					},
					"metadata": map[string]interface{}{
						"type":        "object",
						"description": "Arbitrary build metadata (optional)",
					},
				},
				"required": []string{"service_id", "git_sha", "git_ref", "ci_run_id"},
			},
		},
		{
			Name:        "bahia_update_build_status",
			Description: "Update the status of an existing build",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"build_id": map[string]interface{}{
						"type":        "string",
						"description": "Build UUID",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "Build status",
						"enum":        []string{"queued", "running", "succeeded", "failed", "cancelled"},
					},
				},
				"required": []string{"build_id", "status"},
			},
		},
		// Observability operations
		{
			Name:        "bahia_list_states",
			Description: "List environment service states (current desired vs observed state)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Filter by environment UUID (optional)",
					},
				},
			},
		},
		{
			Name:        "bahia_list_drifted",
			Description: "List services that have drifted from desired state",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "bahia_get_observation",
			Description: "Get the latest runtime observation for a service in an environment",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Environment UUID",
					},
				},
				"required": []string{"service_id", "environment_id"},
			},
		},
		{
			Name:        "bahia_list_intents",
			Description: "List deployment intents for a service in an environment",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Environment UUID",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results",
						"default":     20,
					},
				},
				"required": []string{"service_id", "environment_id"},
			},
		},
		{
			Name:        "bahia_list_runs",
			Description: "List deployment runs for an intent",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"intent_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment intent UUID",
					},
				},
				"required": []string{"intent_id"},
			},
		},
		{
			Name:        "bahia_create_run",
			Description: "Create a new deployment run for an approved intent",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"intent_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment intent UUID",
					},
					"worker_pubkey": map[string]interface{}{
						"type":        "string",
						"description": "Worker public key (optional)",
					},
				},
				"required": []string{"intent_id"},
			},
		},
		{
			Name:        "bahia_get_run",
			Description: "Get details for a specific deployment run",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"run_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment run UUID",
					},
				},
				"required": []string{"run_id"},
			},
		},
		{
			Name:        "bahia_get_run_logs",
			Description: "Retrieve stored stdout/stderr logs for a completed deployment run",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"run_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment run UUID",
					},
					"tail": map[string]interface{}{
						"type":        "integer",
						"description": "Optional number of lines to return from the end of each stream",
					},
					"stream": map[string]interface{}{
						"type":        "string",
						"description": "Log stream to return",
						"enum":        []string{"stdout", "stderr", "merged"},
						"default":     "merged",
					},
				},
				"required": []string{"run_id"},
			},
		},
		{
			Name:        "bahia_complete_run",
			Description: "Mark a deployment run as complete with status and exit code",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"run_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment run UUID",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "Terminal status (succeeded, failed, cancelled)",
						"enum":        []string{"succeeded", "failed", "cancelled"},
					},
					"exit_code": map[string]interface{}{
						"type":        "integer",
						"description": "Process exit code (optional)",
					},
				},
				"required": []string{"run_id", "status"},
			},
		},
		// Secret management operations
		{
			Name:        "bahia_list_secrets",
			Description: "List secrets for a service (returns metadata only, not plaintext values)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID",
					},
				},
				"required": []string{"service_id"},
			},
		},
		{
			Name:        "bahia_create_secret",
			Description: "Create a new encrypted secret for a service",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Secret name (e.g., DATABASE_URL)",
					},
					"value": map[string]interface{}{
						"type":        "string",
						"description": "Plaintext secret value to encrypt",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Environment UUID (optional, omit for service-wide secret)",
					},
				},
				"required": []string{"service_id", "name", "value"},
			},
		},
		{
			Name:        "bahia_update_secret",
			Description: "Update an existing secret's value",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"secret_id": map[string]interface{}{
						"type":        "string",
						"description": "Secret UUID",
					},
					"value": map[string]interface{}{
						"type":        "string",
						"description": "New plaintext secret value to encrypt",
					},
				},
				"required": []string{"secret_id", "value"},
			},
		},
		{
			Name:        "bahia_delete_secret",
			Description: "Delete a secret",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"secret_id": map[string]interface{}{
						"type":        "string",
						"description": "Secret UUID",
					},
				},
				"required": []string{"secret_id"},
			},
		},
		// Policy operations
		{
			Name:        "bahia_list_policies",
			Description: "List deployment policies",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Filter by environment ID (UUID). Omit to list all policies.",
					},
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, only return enabled policies",
					},
				},
			},
		},
		{
			Name:        "bahia_get_policy",
			Description: "Get details for a specific deployment policy",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"policy_id": map[string]interface{}{
						"type":        "string",
						"description": "Policy UUID",
					},
				},
				"required": []string{"policy_id"},
			},
		},
		{
			Name:        "bahia_create_policy",
			Description: "Publish a signed PolicyCreate (kind 5986) request and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Policy name",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Environment ID (UUID). Omit for global policy.",
					},
					"rules": map[string]interface{}{
						"type":        "array",
						"description": "Array of policy rules",
					},
					"enforcement": map[string]interface{}{
						"type":        "string",
						"description": "Enforcement mode: 'warn' or 'block'",
					},
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether the policy is enabled",
					},
					"idempotency_key": map[string]interface{}{
						"type":        "string",
						"description": "Optional Nostr d tag for idempotency/correlation",
					},
				},
				"required": []string{"name", "rules", "enforcement"},
			},
		},
		{
			Name:        "bahia_update_policy",
			Description: "Publish a signed PolicyUpdate (kind 5987) request and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"policy_id": map[string]interface{}{
						"type":        "string",
						"description": "Policy UUID",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Policy name",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Environment ID (UUID). Omit for global policy.",
					},
					"rules": map[string]interface{}{
						"type":        "array",
						"description": "Array of policy rules",
					},
					"enforcement": map[string]interface{}{
						"type":        "string",
						"description": "Enforcement mode: 'warn' or 'block'",
					},
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether the policy is enabled",
					},
					"idempotency_key": map[string]interface{}{
						"type":        "string",
						"description": "Optional Nostr d tag for idempotency/correlation",
					},
				},
				"required": []string{"policy_id"},
			},
		},
		{
			Name:        "bahia_delete_policy",
			Description: "Publish a signed PolicyDelete (kind 5988) request and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"policy_id": map[string]interface{}{
						"type":        "string",
						"description": "Policy UUID",
					},
					"idempotency_key": map[string]interface{}{
						"type":        "string",
						"description": "Optional Nostr d tag for idempotency/correlation",
					},
				},
				"required": []string{"policy_id"},
			},
		},
		{
			Name:        "bahia_evaluate_policy",
			Description: "Publish a signed PolicyEvaluate (kind 5989) request and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Environment UUID",
					},
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID (optional, for context)",
					},
					"idempotency_key": map[string]interface{}{
						"type":        "string",
						"description": "Optional Nostr d tag for idempotency/correlation",
					},
				},
				"required": []string{"artifact_id", "environment_id"},
			},
		},
		// Worker operations
		{
			Name:        "bahia_list_workers",
			Description: "List Loom workers with optional filters",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"capability": map[string]interface{}{
						"type":        "string",
						"description": "Filter by software capability/name (optional)",
					},
					"available": map[string]interface{}{
						"type":        "boolean",
						"description": "Filter by availability status (optional)",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results (default: 50)",
						"default":     50,
					},
				},
			},
		},
		{
			Name:        "bahia_get_worker",
			Description: "Get details for a specific Loom worker by public key",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pubkey": map[string]interface{}{
						"type":        "string",
						"description": "Worker's Nostr public key (hex format)",
					},
				},
				"required": []string{"pubkey"},
			},
		},
		{
			Name:        "bahia_get_worker_pricing",
			Description: "Get pricing information for a specific Loom worker",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pubkey": map[string]interface{}{
						"type":        "string",
						"description": "Worker's Nostr public key (hex format)",
					},
				},
				"required": []string{"pubkey"},
			},
		},
		// Payment operations
		{
			Name:        "bahia_estimate_cost",
			Description: "Estimate the Cashu payment cost for a deployment run based on the assigned worker pricing",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"run_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment run UUID",
					},
					"estimated_duration_secs": map[string]interface{}{
						"type":        "integer",
						"description": "Optional estimated run duration in seconds. If omitted, worker max duration or service default is used.",
					},
				},
				"required": []string{"run_id"},
			},
		},
		{
			Name:        "bahia_get_run_cost",
			Description: "Get Cashu payment records and aggregate cost summary for a deployment run",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"run_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment run UUID",
					},
				},
				"required": []string{"run_id"},
			},
		},
		{
			Name:        "bahia_get_payment_history",
			Description: "List Cashu payment history for a worker",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"worker_pubkey": map[string]interface{}{
						"type":        "string",
						"description": "Worker public key to list payments for",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of payment records to return (default: 50)",
						"default":     50,
					},
				},
				"required": []string{"worker_pubkey"},
			},
		},
		// Intent alias operations (REST-aligned naming)
		{
			Name:        "bahia_create_intent",
			Description: "Create a deployment intent (alias for bahia_deploy)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID to deploy",
					},
					"environment_id": map[string]interface{}{
						"type":        "string",
						"description": "Target environment UUID",
					},
					"artifact_id": map[string]interface{}{
						"type":        "string",
						"description": "Artifact UUID to deploy",
					},
					"requested_by": map[string]interface{}{
						"type":        "string",
						"description": "Identity of the requester",
					},
				},
				"required": []string{"service_id", "environment_id", "artifact_id"},
			},
		},
		{
			Name:        "bahia_get_intent",
			Description: "Get details for a specific deployment intent",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"intent_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment intent UUID",
					},
				},
				"required": []string{"intent_id"},
			},
		},
		{
			Name:        "bahia_approve_intent",
			Description: "Approve a pending deployment intent (alias for bahia_approve_deployment)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"intent_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment intent UUID to approve",
					},
				},
				"required": []string{"intent_id"},
			},
		},
		{
			Name:        "bahia_reject_intent",
			Description: "Reject a pending deployment intent (alias for bahia_reject_deployment)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"intent_id": map[string]interface{}{
						"type":        "string",
						"description": "Deployment intent UUID to reject",
					},
				},
				"required": []string{"intent_id"},
			},
		},
		// Tool provisioning operations
		{
			Name:        "bahia_tool_provision_request",
			Description: "Request tools to be provisioned for a service",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id":     map[string]interface{}{"type": "string", "description": "Service UUID"},
					"environment_id": map[string]interface{}{"type": "string", "description": "Environment UUID"},
					"tools":          map[string]interface{}{"type": "array", "description": "Array of tool objects", "items": map[string]interface{}{"type": "object"}},
					"reason":         map[string]interface{}{"type": "string", "description": "Reason for tool request"},
				},
				"required": []string{"service_id", "environment_id", "tools", "reason"},
			},
		},
		{
			Name:        "bahia_tool_provision_status",
			Description: "Get status of a tool provisioning intent",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"intent_id": map[string]interface{}{"type": "string", "description": "Intent UUID"}}, "required": []string{"intent_id"}},
		},
		{
			Name:        "bahia_tool_provision_approve",
			Description: "Publish a signed ToolApprovalResponse (kind 7977) approval and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"intent_id": map[string]interface{}{"type": "string", "description": "Intent UUID"}, "reason": map[string]interface{}{"type": "string", "description": "Approval reason"}, "idempotency_key": map[string]interface{}{"type": "string", "description": "Optional Nostr d tag for idempotency/correlation"}}, "required": []string{"intent_id", "reason"}},
		},
		{
			Name:        "bahia_tool_provision_reject",
			Description: "Publish a signed ToolApprovalResponse (kind 7977) rejection and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"intent_id": map[string]interface{}{"type": "string", "description": "Intent UUID"}, "reason": map[string]interface{}{"type": "string", "description": "Rejection reason"}, "idempotency_key": map[string]interface{}{"type": "string", "description": "Optional Nostr d tag for idempotency/correlation"}}, "required": []string{"intent_id", "reason"}},
		},
		{
			Name:        "bahia_tool_denylist_add",
			Description: "Add a package to the tool denylist",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"package": map[string]interface{}{"type": "string"}, "manager": map[string]interface{}{"type": "string"}, "reason": map[string]interface{}{"type": "string"}}, "required": []string{"package", "manager", "reason"}},
		},
		{
			Name:        "bahia_tool_denylist_remove",
			Description: "Remove a package from the tool denylist",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"package": map[string]interface{}{"type": "string"}, "manager": map[string]interface{}{"type": "string"}}, "required": []string{"package", "manager"}},
		},
		{
			Name:        "bahia_tool_denylist_list",
			Description: "List all packages on the tool denylist",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "bahia_tool_profile_get",
			Description: "Get the current tool profile for a service/environment",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"service_id": map[string]interface{}{"type": "string"}, "environment_id": map[string]interface{}{"type": "string"}}, "required": []string{"service_id", "environment_id"}},
		},
		// Notification channel operations
		{
			Name:        "bahia_list_notification_channels",
			Description: "List notification delivery channels",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, only return enabled channels",
					},
				},
			},
		},
		{
			Name:        "bahia_get_notification_channel",
			Description: "Get a notification channel by ID",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"channel_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification channel UUID",
					},
				},
				"required": []string{"channel_id"},
			},
		},
		{
			Name:        "bahia_create_notification_channel",
			Description: "Create a notification delivery channel",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Unique channel name",
					},
					"channel_type": map[string]interface{}{
						"type":        "string",
						"description": "Delivery type",
						"enum":        []string{"webhook", "nostr_dm"},
					},
					"config": map[string]interface{}{
						"type":        "object",
						"description": "Type-specific delivery config (for example webhook url or nostr pubkey)",
					},
					"event_filter": map[string]interface{}{
						"type":        "object",
						"description": "Optional filter such as type=* or types=[drift.detected]",
					},
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether the channel is active (default true)",
					},
				},
				"required": []string{"name", "channel_type", "config"},
			},
		},
		{
			Name:        "bahia_update_notification_channel",
			Description: "Update a notification delivery channel",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"channel_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification channel UUID",
					},
					"name": map[string]interface{}{"type": "string", "description": "New channel name"},
					"channel_type": map[string]interface{}{
						"type":        "string",
						"description": "Delivery type",
						"enum":        []string{"webhook", "nostr_dm"},
					},
					"config":       map[string]interface{}{"type": "object", "description": "Replacement type-specific delivery config"},
					"event_filter": map[string]interface{}{"type": "object", "description": "Replacement event filter"},
					"enabled":      map[string]interface{}{"type": "boolean", "description": "Whether the channel is active"},
				},
				"required": []string{"channel_id"},
			},
		},
		{
			Name:        "bahia_delete_notification_channel",
			Description: "Delete a notification delivery channel",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"channel_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification channel UUID",
					},
				},
				"required": []string{"channel_id"},
			},
		},
		{
			Name:        "bahia_test_notification_channel",
			Description: "Send a test notification through the configured dispatcher",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"channel_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification channel UUID",
					},
				},
				"required": []string{"channel_id"},
			},
		},
		// Notification log operations
		{
			Name:        "bahia_list_notifications",
			Description: "List recent notifications with optional filters",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"status": map[string]interface{}{
						"type":        "string",
						"description": "Filter by status: 'read' (sent), 'unread' (pending/retrying), or omit for all",
						"enum":        []string{"read", "unread"},
					},
					"event_type": map[string]interface{}{
						"type":        "string",
						"description": "Filter by event type (optional)",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of notifications to return (default: 50)",
						"default":     50,
					},
				},
			},
		},
		{
			Name:        "bahia_get_notification",
			Description: "Get a single notification by ID (not currently supported)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"notification_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification UUID",
					},
				},
				"required": []string{"notification_id"},
			},
		},
		{
			Name:        "bahia_mark_notification_read",
			Description: "Mark a notification as read",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"notification_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification UUID",
					},
				},
				"required": []string{"notification_id"},
			},
		},
		{
			Name:        "bahia_dismiss_notification",
			Description: "Dismiss/delete a notification (not supported - notification logs are immutable)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"notification_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification UUID",
					},
				},
				"required": []string{"notification_id"},
			},
		},
	}
	tools = append(tools, mlToolDefinitions()...)
	tools = append(tools, assistantAsyncToolDefinitions()...)
	tools = append(tools, dnsToolDefinitions()...)
	tools = append(tools, dnsAssistantToolDefinitions()...)
	tools = append(tools, fipsToolDefinitions()...)
	tools = append(tools, workerToolDefinitions()...)
	tools = append(tools, packageToolDefinitions()...)
	tools = append(tools, backupToolDefinitions()...)
	return append(tools, docsToolDefinitions()...)
}

// CallTool handles an MCP tool call.
// InvokeTool exposes the in-process tool path used by the assistant orchestrator.
func (s *Server) InvokeTool(ctx context.Context, name string, arguments map[string]interface{}) (*ToolResult, error) {
	return s.CallTool(ctx, name, arguments)
}

func signerFirstMCPMutationUnavailable(toolName, method string) *ToolResult {
	return errorResult(fmt.Sprintf("%s is no longer available as a direct registry mutation; publish a signed ContextVM/Nostr %s command with an operator signer instead", toolName, method))
}

func normalizePubkeys(pubkeys []string) []string {
	normalized := make([]string, 0, len(pubkeys))
	for _, pubkey := range pubkeys {
		pubkey = strings.ToLower(strings.TrimSpace(pubkey))
		if pubkey != "" && !slices.Contains(normalized, pubkey) {
			normalized = append(normalized, pubkey)
		}
	}
	return normalized
}

func (s *Server) authorizeToolCall(ctx context.Context, name string) *ToolResult {
	principal := auth.GetPrincipal(ctx)
	if principal == nil || !principal.IsAuthenticated() {
		s.logger.Warn("unauthenticated MCP tool call rejected", zap.String("tool", name))
		return errorResult("authentication required")
	}

	// System callers are trusted only when they explicitly carry the admin role.
	// External callers must match the same fail-closed operator allowlist model used
	// by the control-plane reactor.
	if principal.Method == auth.MethodSystem && principal.HasRole(string(domain.RoleAdmin)) {
		return nil
	}
	pubkey := strings.ToLower(strings.TrimSpace(principal.PubKey))
	if pubkey == "" || !slices.Contains(s.authorizedPubkeys, pubkey) {
		s.logger.Warn("unauthorized MCP tool call rejected",
			zap.String("tool", name),
			zap.String("subject", principal.Subject),
			zap.String("pubkey", principal.PubKey),
		)
		return errorResult("access denied")
	}
	return nil
}

func (s *Server) authorizeSecretPermission(ctx context.Context, serviceID uuid.UUID, permission domain.Permission) *ToolResult {
	principal := auth.GetPrincipal(ctx)
	if principal != nil && principal.Method == auth.MethodSystem && principal.HasRole(string(domain.RoleAdmin)) {
		return nil
	}
	if principal == nil || !principal.IsAuthenticated() {
		return errorResult("authentication required")
	}
	if s.registry == nil || s.rbac == nil {
		return errorResult("secret authorization is not configured")
	}

	svc, err := s.registry.GetService(ctx, serviceID)
	if err != nil || svc == nil || svc.OrgID == uuid.Nil {
		return errorResult("secret owner not found")
	}
	if err := s.rbac.CheckPermission(ctx, principal, svc.OrgID, permission); err != nil {
		s.logger.Warn("tenant secret access rejected",
			zap.String("service_id", serviceID.String()),
			zap.String("org_id", svc.OrgID.String()),
			zap.String("subject", principal.Subject),
			zap.String("permission", string(permission)),
		)
		return errorResult("access denied")
	}
	return nil
}

func (s *Server) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*ToolResult, error) {
	s.logger.Info("tool call", zap.String("tool", name))
	if denied := s.authorizeToolCall(ctx, name); denied != nil {
		return denied, nil
	}

	if isBackupToolName(name) {
		return s.handleBackupTool(ctx, name, arguments)
	}

	switch name {
	// Service operations
	case "bahia_list_services":
		return s.handleListServices(ctx, arguments)
	case "bahia_get_service":
		return s.handleGetService(ctx, arguments)
	case "bahia_create_service":
		return s.handleCreateService(ctx, arguments)
	case "bahia_update_service":
		return s.handleUpdateService(ctx, arguments)
	// Environment operations
	case "bahia_list_environments":
		return s.handleListEnvironments(ctx, arguments)
	case "bahia_get_environment":
		return s.handleGetEnvironment(ctx, arguments)
	case "bahia_create_environment":
		return s.handleCreateEnvironment(ctx, arguments)
	case "bahia_update_environment":
		return s.handleUpdateEnvironment(ctx, arguments)
	// Deployment operations
	case "bahia_deploy":
		return s.handleDeploy(ctx, arguments)
	case "bahia_rollback":
		return s.handleRollback(ctx, arguments)
	case "bahia_get_deployment_status":
		return s.handleGetDeploymentStatus(ctx, arguments)
	case "bahia_approve_deployment":
		return s.handleApproveDeployment(ctx, arguments)
	case "bahia_reject_deployment":
		return s.handleRejectDeployment(ctx, arguments)
	// ML compatibility operations
	case "bahia_ml_import_model":
		return s.handleMLModelImport(ctx, arguments)
	case "bahia_ml_run_recipe":
		return s.handleMLRecipeRun(ctx, arguments)
	case "bahia_ml_deploy":
		return s.handleMLDeploy(ctx, arguments)
	case "bahia_ml_rollback":
		return s.handleMLRollback(ctx, arguments)
	case "bahia_assistant_service_deploy", "bahia_assistant_service_rollback", "bahia_assistant_llm_deploy", "bahia_assistant_llm_approve_deployment", "bahia_assistant_llm_rollback", "bahia_assistant_ml_deploy", "bahia_assistant_ml_approve_deployment", "bahia_assistant_ml_rollback":
		return s.handleAssistantAsyncTool(ctx, name, arguments)
	case "bahia_dns_list_endpoints", "bahia_assistant_dns_list_endpoints":
		return s.handleDNSListEndpoints(ctx, arguments)
	case "bahia_dns_list_drift", "bahia_assistant_dns_list_drift":
		return s.handleDNSListDrift(ctx, arguments)
	case "bahia_fips_list_mesh_nodes":
		return s.handleFIPSListMeshNodes(ctx, arguments)
	case "bahia_fips_mesh_status":
		return s.handleFIPSMeshStatus(ctx, arguments)
	case "bahia_assistant_dns_zone_create", "bahia_assistant_dns_policy_apply", "bahia_assistant_dns_record_override", "bahia_assistant_dns_drift_remediate":
		return s.handleDNSAssistantAsyncTool(ctx, name, arguments)
	case "bahia_ml_list_state":
		return s.handleMLListState(ctx, arguments)
	case "bahia_ml_get_state":
		return s.handleMLGetState(ctx, arguments)
	case "bahia_ml_get_provenance":
		return s.handleMLGetProvenance(ctx, arguments)
	// LLM registry operations
	case "bahia_llm_create_route":
		return s.handleLLMCreateRoute(ctx, arguments)
	case "bahia_llm_update_route":
		return s.handleLLMUpdateRoute(ctx, arguments)
	case "bahia_llm_register_release":
		return s.handleLLMRegisterRelease(ctx, arguments)
	case "bahia_llm_list_routes":
		return s.handleLLMListRoutes(ctx, arguments)
	case "bahia_llm_list_releases":
		return s.handleLLMListReleases(ctx, arguments)
	// Async LLM Nostr command operations
	case "bahia_llm_deploy":
		return s.handleLLMDeploy(ctx, arguments)
	case "bahia_llm_approve_deployment":
		return s.handleLLMApproveDeployment(ctx, arguments)
	case "bahia_llm_reject_deployment":
		return s.handleLLMRejectDeployment(ctx, arguments)
	case "bahia_llm_rollback":
		return s.handleLLMRollback(ctx, arguments)
	case "bahia_delete_service":
		return s.handleDeleteService(ctx, arguments)
	case "bahia_delete_environment":
		return s.handleDeleteEnvironment(ctx, arguments)
	// Artifact operations
	case "bahia_list_artifacts":
		return s.handleListArtifacts(ctx, arguments)
	case "bahia_get_artifact":
		return s.handleGetArtifact(ctx, arguments)
	case "bahia_register_artifact":
		return s.handleRegisterArtifact(ctx, arguments)
	// Signature operations
	case "bahia_list_signatures":
		return s.handleListSignatures(ctx, arguments)
	case "bahia_list_verified_signatures":
		return s.handleListVerifiedSignatures(ctx, arguments)
	case "bahia_has_verified_signature":
		return s.handleHasVerifiedSignature(ctx, arguments)
	case "bahia_get_signature":
		return s.handleGetSignature(ctx, arguments)
	case "bahia_verify_signatures":
		return s.handleVerifySignatures(ctx, arguments)
	// SBOM operations
	case "bahia_get_sbom":
		return s.handleGetSBOM(ctx, arguments)
	case "bahia_get_sbom_packages":
		return s.handleGetSBOMPackages(ctx, arguments)
	case "bahia_search_sbom_packages":
		return s.handleSearchSBOMPackages(ctx, arguments)
	case "bahia_ingest_sbom":
		return s.handleIngestSBOM(ctx, arguments)
	// Build operations
	case "bahia_list_builds":
		return s.handleListBuilds(ctx, arguments)
	case "bahia_get_build":
		return s.handleGetBuild(ctx, arguments)
	case "bahia_register_build":
		return s.handleRegisterBuild(ctx, arguments)
	case "bahia_update_build_status":
		return s.handleUpdateBuildStatus(ctx, arguments)
	// Observability operations
	case "bahia_list_states":
		return s.handleListStates(ctx, arguments)
	case "bahia_list_drifted":
		return s.handleListDrifted(ctx, arguments)
	case "bahia_get_observation":
		return s.handleGetObservation(ctx, arguments)
	case "bahia_list_intents":
		return s.handleListIntents(ctx, arguments)
	case "bahia_list_runs":
		return s.handleListRuns(ctx, arguments)
	case "bahia_create_run":
		return s.handleCreateRun(ctx, arguments)
	case "bahia_get_run":
		return s.handleGetRun(ctx, arguments)
	case "bahia_get_run_logs":
		return s.handleGetRunLogs(ctx, arguments)
	case "bahia_complete_run":
		return s.handleCompleteRun(ctx, arguments)
	// Secret operations
	case "bahia_list_secrets":
		return s.handleListSecrets(ctx, arguments)
	case "bahia_create_secret":
		return s.handleCreateSecret(ctx, arguments)
	case "bahia_update_secret":
		return s.handleUpdateSecret(ctx, arguments)
	case "bahia_delete_secret":
		return s.handleDeleteSecret(ctx, arguments)
	// Policy operations
	case "bahia_list_policies":
		return s.handleListPolicies(ctx, arguments)
	case "bahia_get_policy":
		return s.handleGetPolicy(ctx, arguments)
	case "bahia_create_policy":
		return s.handleCreatePolicy(ctx, arguments)
	case "bahia_update_policy":
		return s.handleUpdatePolicy(ctx, arguments)
	case "bahia_delete_policy":
		return s.handleDeletePolicy(ctx, arguments)
	case "bahia_evaluate_policy":
		return s.handleEvaluatePolicy(ctx, arguments)
	// Worker operations
	case "bahia_list_workers":
		return s.handleListWorkers(ctx, arguments)
	case "bahia_get_worker":
		return s.handleGetWorker(ctx, arguments)
	case "bahia_get_worker_pricing":
		return s.handleGetWorkerPricing(ctx, arguments)
	case "bahia_worker_cordon", "bahia_worker_uncordon", "bahia_worker_drain", "bahia_worker_undrain", "bahia_worker_maintenance_enter", "bahia_worker_maintenance_exit":
		return s.handleWorkerLifecycleCommand(ctx, name, arguments)
	case "bahia_worker_labels_update":
		return s.handleWorkerLabelsUpdate(ctx, arguments)
	case "bahia_worker_get_assignments":
		return s.handleWorkerGetAssignments(ctx, arguments)
	case "bahia_worker_list_assignments":
		return s.handleWorkerListAssignments(ctx, arguments)
	case "bahia_worker_get_drain_status":
		return s.handleWorkerGetDrainStatus(ctx, arguments)
	case "bahia_worker_list_drain_status":
		return s.handleWorkerListDrainStatus(ctx, arguments)
	case "bahia_worker_preview_eligibility":
		return s.handleWorkerPreviewEligibility(ctx, arguments)
	// Payment operations
	case "bahia_estimate_cost":
		return s.handleEstimateCost(ctx, arguments)
	case "bahia_get_run_cost":
		return s.handleGetRunCost(ctx, arguments)
	case "bahia_get_payment_history":
		return s.handleGetPaymentHistory(ctx, arguments)
	// Intent alias operations
	case "bahia_create_intent":
		return s.handleDeploy(ctx, arguments) // alias
	case "bahia_get_intent":
		return s.handleGetIntent(ctx, arguments)
	case "bahia_approve_intent":
		return s.handleApproveDeployment(ctx, arguments) // alias
	case "bahia_reject_intent":
		return s.handleRejectDeployment(ctx, arguments) // alias
	// Tool provisioning operations
	case "bahia_tool_provision_request":
		return s.handleToolProvisionRequest(ctx, arguments)
	case "bahia_tool_provision_status":
		return s.handleToolProvisionStatus(ctx, arguments)
	case "bahia_tool_provision_approve":
		return s.handleToolProvisionApprove(ctx, arguments)
	case "bahia_tool_provision_reject":
		return s.handleToolProvisionReject(ctx, arguments)
	case "bahia_tool_denylist_add":
		return s.handleToolDenylistAdd(ctx, arguments)
	case "bahia_tool_denylist_remove":
		return s.handleToolDenylistRemove(ctx, arguments)
	case "bahia_tool_denylist_list":
		return s.handleToolDenylistList(ctx, arguments)
	case "bahia_tool_profile_get":
		return s.handleToolProfileGet(ctx, arguments)
	// Package control-plane operations
	case "bahia_package_repository_apply":
		return s.handlePackageRepositoryApply(ctx, arguments)
	case "bahia_package_repository_delete":
		return s.handlePackageRepositoryDelete(ctx, arguments)
	case "bahia_package_upload":
		return s.handlePackageUpload(ctx, arguments)
	case "bahia_package_promote":
		return s.handlePackagePromote(ctx, arguments)
	case "bahia_package_yank":
		return s.handlePackageYank(ctx, arguments)
	case "bahia_package_drift_detect":
		return s.handlePackageDriftDetect(ctx, arguments)
	case "bahia_package_list":
		return s.handlePackageList(ctx, arguments)
	case "bahia_package_get":
		return s.handlePackageGet(ctx, arguments)
	case "bahia_package_status":
		return s.handlePackageStatus(ctx, arguments)
	// Notification channel operations
	case "bahia_list_notification_channels":
		return s.handleListNotificationChannels(ctx, arguments)
	case "bahia_get_notification_channel":
		return s.handleGetNotificationChannel(ctx, arguments)
	case "bahia_create_notification_channel":
		return s.handleCreateNotificationChannel(ctx, arguments)
	case "bahia_update_notification_channel":
		return s.handleUpdateNotificationChannel(ctx, arguments)
	case "bahia_delete_notification_channel":
		return s.handleDeleteNotificationChannel(ctx, arguments)
	case "bahia_test_notification_channel":
		return s.handleTestNotificationChannel(ctx, arguments)
	// Notification log operations
	case "bahia_list_notifications":
		return s.handleListNotifications(ctx, arguments)
	case "bahia_get_notification":
		return s.handleGetNotification(ctx, arguments)
	case "bahia_mark_notification_read":
		return s.handleMarkNotificationRead(ctx, arguments)
	case "bahia_dismiss_notification":
		return s.handleDismissNotification(ctx, arguments)
	case "bahia_docs_read":
		return s.handleDocsRead(ctx, arguments)
	case "bahia_docs_list":
		return s.handleDocsList(ctx, arguments)
	default:
		return errorResult(fmt.Sprintf("unknown tool: %s", name)), nil
	}
}

// --- Tool Handlers ---

func (s *Server) handleListServices(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	services, err := s.registry.ListServices(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list services: %v", err)), nil
	}

	result := map[string]interface{}{
		"services": servicesToMaps(services),
		"total":    len(services),
	}
	return jsonResult(result)
}

func (s *Server) handleGetService(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceID, _ := args["service_id"].(string)
	name, _ := args["name"].(string)

	var svc *domain.Service
	var err error

	if serviceID != "" {
		id, parseErr := uuid.Parse(serviceID)
		if parseErr != nil {
			return errorResult(fmt.Sprintf("invalid service_id: %v", parseErr)), nil
		}
		svc, err = s.registry.GetService(ctx, id)
	} else if name != "" {
		svc, err = s.registry.GetServiceByName(ctx, name)
	} else {
		return errorResult("service_id or name is required"), nil
	}

	if err != nil {
		return errorResult(fmt.Sprintf("failed to get service: %v", err)), nil
	}
	if svc == nil {
		return errorResult("service not found"), nil
	}

	return jsonResult(serviceToMap(svc))
}

func (s *Server) handleCreateService(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return signerFirstMCPMutationUnavailable("bahia_create_service", "service/create"), nil
}

func (s *Server) handleListEnvironments(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	envs, err := s.registry.ListEnvironments(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list environments: %v", err)), nil
	}

	result := map[string]interface{}{
		"environments": environmentsToMaps(envs),
		"total":        len(envs),
	}
	return jsonResult(result)
}

func (s *Server) handleGetEnvironment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	envID, _ := args["environment_id"].(string)
	name, _ := args["name"].(string)

	var env *domain.Environment
	var err error

	if envID != "" {
		id, parseErr := uuid.Parse(envID)
		if parseErr != nil {
			return errorResult(fmt.Sprintf("invalid environment_id: %v", parseErr)), nil
		}
		env, err = s.registry.GetEnvironment(ctx, id)
	} else if name != "" {
		env, err = s.registry.GetEnvironmentByName(ctx, name)
	} else {
		return errorResult("environment_id or name is required"), nil
	}

	if err != nil {
		return errorResult(fmt.Sprintf("failed to get environment: %v", err)), nil
	}
	if env == nil {
		return errorResult("environment not found"), nil
	}

	return jsonResult(environmentToMap(env))
}

func (s *Server) handleCreateEnvironment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return signerFirstMCPMutationUnavailable("bahia_create_environment", "environment/create"), nil
}

func (s *Server) handleUpdateService(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return signerFirstMCPMutationUnavailable("bahia_update_service", "service/update"), nil
}

func (s *Server) handleUpdateEnvironment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return signerFirstMCPMutationUnavailable("bahia_update_environment", "environment/update"), nil
}

func (s *Server) handleDeploy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	envIDStr, _ := args["environment_id"].(string)
	artifactIDStr, _ := args["artifact_id"].(string)
	requestedBy, _ := args["requested_by"].(string)

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	envID, err := uuid.Parse(envIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
	}

	artifactID, err := uuid.Parse(artifactIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid artifact_id: %v", err)), nil
	}

	if requestedBy == "" {
		requestedBy = "mcp-agent"
	}

	if s.serviceCommands == nil {
		return signerFirstMCPMutationUnavailable("bahia_deploy", "service/deploy"), nil
	}
	receipt, err := s.serviceCommands.PublishDeployRequest(ctx, controlplane.ServiceDeployCommand{ServiceID: serviceID, EnvironmentID: envID, ArtifactID: artifactID, RequestedBy: requestedBy, IdempotencyKey: mcpIdempotencyKey(args, "service-deploy", serviceID.String(), envID.String(), artifactID.String()), AgentID: requestedBy})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish deployment request: %v", err)), nil
	}
	return jsonResult(serviceCommandReceiptToMap(receipt))
}

func (s *Server) handleRollback(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	envIDStr, _ := args["environment_id"].(string)
	requestedBy, _ := args["requested_by"].(string)

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	envID, err := uuid.Parse(envIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
	}

	if requestedBy == "" {
		requestedBy = "mcp-agent"
	}

	if s.serviceCommands == nil {
		return signerFirstMCPMutationUnavailable("bahia_rollback", "service/rollback"), nil
	}
	receipt, err := s.serviceCommands.PublishRollbackRequest(ctx, controlplane.ServiceRollbackCommand{ServiceID: serviceID, EnvironmentID: envID, IdempotencyKey: mcpIdempotencyKey(args, "service-rollback", serviceID.String(), envID.String()), AgentID: requestedBy})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish rollback request: %v", err)), nil
	}
	return jsonResult(serviceCommandReceiptToMap(receipt))
}

func (s *Server) handleGetDeploymentStatus(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	envIDStr, _ := args["environment_id"].(string)

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	envID, err := uuid.Parse(envIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
	}

	state, err := s.registry.GetEnvironmentServiceState(ctx, serviceID, envID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get state: %v", err)), nil
	}

	result := map[string]interface{}{
		"service_id":     serviceID.String(),
		"environment_id": envID.String(),
		"drift_status":   state.DriftStatus,
	}

	if state.DesiredArtifactID != nil {
		result["desired_artifact_id"] = state.DesiredArtifactID.String()
	}
	if state.DesiredIntentID != nil {
		result["desired_intent_id"] = state.DesiredIntentID.String()
	}
	if state.LastSuccessfulRunID != nil {
		result["last_successful_run_id"] = state.LastSuccessfulRunID.String()
	}
	if state.LastReconciledAt != nil {
		result["last_reconciled_at"] = state.LastReconciledAt.Format("2006-01-02T15:04:05Z")
	}

	return jsonResult(result)
}

func (s *Server) handleApproveDeployment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	intentIDStr, _ := args["intent_id"].(string)

	intentID, err := uuid.Parse(intentIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid intent_id: %v", err)), nil
	}

	return s.publishDeploymentApprovalDecision(ctx, args, intentID, "approve")
}

func (s *Server) handleRejectDeployment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	intentIDStr, _ := args["intent_id"].(string)

	intentID, err := uuid.Parse(intentIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid intent_id: %v", err)), nil
	}

	return s.publishDeploymentApprovalDecision(ctx, args, intentID, "reject")
}

func (s *Server) publishDeploymentApprovalDecision(ctx context.Context, args map[string]interface{}, intentID uuid.UUID, decision string) (*ToolResult, error) {
	if s.serviceCommands == nil {
		return signerFirstMCPMutationUnavailable("bahia_"+decision+"_deployment", "approval/"+decision), nil
	}
	agentID, _ := args["requested_by"].(string)
	if agentID == "" {
		agentID = "mcp-agent"
	}
	receipt, err := s.serviceCommands.PublishDeploymentApprovalRequest(ctx, controlplane.ServiceApprovalCommand{IntentID: intentID, Decision: decision, IdempotencyKey: mcpIdempotencyKey(args, "deployment-approval", intentID.String(), decision), AgentID: agentID})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish deployment %s request: %v", decision, err)), nil
	}
	return jsonResult(serviceCommandReceiptToMap(receipt))
}

func (s *Server) requireLLMRegistry() (*service.LLMRegistryService, *ToolResult) {
	if s.llmRegistry == nil {
		return nil, errorResult("LLM registry is not configured")
	}
	return s.llmRegistry, nil
}

func (s *Server) requireLLMCommands() (LLMCommandPublisher, *ToolResult) {
	if s.llmCommands == nil {
		return nil, errorResult("LLM command publisher is not configured")
	}
	return s.llmCommands, nil
}

func (s *Server) handleLLMCreateRoute(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireLLMCommands()
	if errResult != nil {
		return errResult, nil
	}
	var req struct {
		Name                   string                         `json:"name"`
		Description            string                         `json:"description,omitempty"`
		GatewayConfig          *domain.LLMGatewayRouteConfig  `json:"gateway_config,omitempty"`
		DefaultPlacementPolicy *domain.LLMPlacementPolicy     `json:"default_placement_policy,omitempty"`
		DefaultPromotionGate   *domain.LLMPromotionGateConfig `json:"default_promotion_gate,omitempty"`
		Metadata               map[string]any                 `json:"metadata,omitempty"`
	}
	if err := decodeToolArgs(args, &req); err != nil {
		return errorResult(fmt.Sprintf("invalid LLM route request: %v", err)), nil
	}
	receipt, err := publisher.PublishLLMRouteCreateRequest(ctx, controlplane.LLMRouteCreateCommand{
		Name:                   req.Name,
		Description:            req.Description,
		GatewayConfig:          req.GatewayConfig,
		DefaultPlacementPolicy: req.DefaultPlacementPolicy,
		DefaultPromotionGate:   req.DefaultPromotionGate,
		Metadata:               req.Metadata,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish LLM route create request: %v", err)), nil
	}
	return jsonResult(llmCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleLLMUpdateRoute(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	registry, errResult := s.requireLLMRegistry()
	if errResult != nil {
		return errResult, nil
	}
	routeID, err := parseUUIDArg(args, "route_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	route, err := registry.GetRoute(ctx, routeID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get LLM route: %v", err)), nil
	}
	if route == nil {
		return errorResult("LLM route not found"), nil
	}
	var req struct {
		Description            string                         `json:"description,omitempty"`
		GatewayConfig          *domain.LLMGatewayRouteConfig  `json:"gateway_config,omitempty"`
		DefaultPlacementPolicy *domain.LLMPlacementPolicy     `json:"default_placement_policy,omitempty"`
		DefaultPromotionGate   *domain.LLMPromotionGateConfig `json:"default_promotion_gate,omitempty"`
		Metadata               map[string]any                 `json:"metadata,omitempty"`
	}
	if err := decodeToolArgs(args, &req); err != nil {
		return errorResult(fmt.Sprintf("invalid LLM route update: %v", err)), nil
	}
	if _, ok := args["description"]; ok {
		route.Description = req.Description
	}
	if req.GatewayConfig != nil {
		route.GatewayConfig = req.GatewayConfig
	}
	if req.DefaultPlacementPolicy != nil {
		route.DefaultPlacementPolicy = req.DefaultPlacementPolicy
	}
	if req.DefaultPromotionGate != nil {
		route.DefaultPromotionGate = req.DefaultPromotionGate
	}
	if req.Metadata != nil {
		route.Metadata = req.Metadata
	}
	if err := registry.UpdateRoute(ctx, route); err != nil {
		return errorResult(fmt.Sprintf("failed to update LLM route: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{
		"status":        "updated",
		"route_id":      route.ID.String(),
		"registry_kind": controlplane.KindLLMRouteRegistry,
		"state_kind":    controlplane.KindLLMRouteState,
		"route":         llmRouteToMap(route),
	})
}

func (s *Server) handleLLMRegisterRelease(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireLLMCommands()
	if errResult != nil {
		return errResult, nil
	}
	var req struct {
		RouteID            string                                 `json:"route_id"`
		Version            string                                 `json:"version"`
		ModelRef           string                                 `json:"model_ref"`
		ModelSource        string                                 `json:"model_source"`
		ModelRevision      string                                 `json:"model_revision,omitempty"`
		EstimatedVRAMGB    int                                    `json:"estimated_vram_gb,omitempty"`
		BackendPreferences []domain.LLMBackendKind                `json:"backend_preferences,omitempty"`
		RuntimeBackend     *domain.LLMRuntimeManagedBackendConfig `json:"runtime_backend,omitempty"`
		ExternalBackend    *domain.LLMExternalBackendConfig       `json:"external_backend,omitempty"`
		PlacementPolicy    *domain.LLMPlacementPolicy             `json:"placement_policy,omitempty"`
		PromotionGate      *domain.LLMPromotionGateConfig         `json:"promotion_gate,omitempty"`
		Metadata           map[string]any                         `json:"metadata,omitempty"`
	}
	if err := decodeToolArgs(args, &req); err != nil {
		return errorResult(fmt.Sprintf("invalid LLM release request: %v", err)), nil
	}
	routeID, err := uuid.Parse(req.RouteID)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid route_id: %v", err)), nil
	}
	receipt, err := publisher.PublishLLMReleaseRegisterRequest(ctx, controlplane.LLMReleaseRegisterCommand{
		RouteID:            routeID,
		Version:            req.Version,
		ModelRef:           req.ModelRef,
		ModelSource:        req.ModelSource,
		ModelRevision:      req.ModelRevision,
		EstimatedVRAMGB:    req.EstimatedVRAMGB,
		BackendPreferences: req.BackendPreferences,
		RuntimeBackend:     req.RuntimeBackend,
		ExternalBackend:    req.ExternalBackend,
		PlacementPolicy:    req.PlacementPolicy,
		PromotionGate:      req.PromotionGate,
		Metadata:           req.Metadata,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish LLM release register request: %v", err)), nil
	}
	return jsonResult(llmCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleLLMListRoutes(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	registry, errResult := s.requireLLMRegistry()
	if errResult != nil {
		return errResult, nil
	}
	limit, offset := limitOffsetArgs(args, 100)
	routes, err := registry.ListRoutes(ctx, limit, offset)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list LLM routes: %v", err)), nil
	}
	out := make([]map[string]interface{}, 0, len(routes))
	for i := range routes {
		out = append(out, llmRouteToMap(&routes[i]))
	}
	return jsonResult(map[string]interface{}{"routes": out, "total": len(out), "registry_kind": controlplane.KindLLMRouteRegistry})
}

func (s *Server) handleLLMListReleases(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	registry, errResult := s.requireLLMRegistry()
	if errResult != nil {
		return errResult, nil
	}
	routeID, err := parseUUIDArg(args, "route_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	limit, offset := limitOffsetArgs(args, 100)
	releases, err := registry.ListReleases(ctx, routeID, limit, offset)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list LLM releases: %v", err)), nil
	}
	out := make([]map[string]interface{}, 0, len(releases))
	for i := range releases {
		out = append(out, llmReleaseToMap(&releases[i]))
	}
	return jsonResult(map[string]interface{}{"route_id": routeID.String(), "releases": out, "total": len(out), "registry_kind": controlplane.KindLLMRouteRegistry})
}

func (s *Server) handleLLMDeploy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireLLMCommands()
	if errResult != nil {
		return errResult, nil
	}
	routeID, err := parseUUIDArg(args, "route_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	envID, err := parseUUIDArg(args, "environment_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	releaseID, err := parseUUIDArg(args, "release_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	requestedBy, _ := args["requested_by"].(string)
	if requestedBy == "" {
		requestedBy = "mcp-agent"
	}
	metadata, _ := args["metadata"].(map[string]interface{})
	receipt, err := publisher.PublishLLMDeployRequest(ctx, controlplane.LLMDeployCommand{RouteID: routeID, EnvironmentID: envID, ReleaseID: releaseID, RequestedBy: requestedBy, Metadata: metadata})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish LLM deploy request: %v", err)), nil
	}
	return jsonResult(llmCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleLLMApproveDeployment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.handleLLMApprovalDecision(ctx, args, "approve")
}

func (s *Server) handleLLMRejectDeployment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.handleLLMApprovalDecision(ctx, args, "reject")
}

func (s *Server) handleLLMApprovalDecision(ctx context.Context, args map[string]interface{}, decision string) (*ToolResult, error) {
	publisher, errResult := s.requireLLMCommands()
	if errResult != nil {
		return errResult, nil
	}
	intentID, err := parseUUIDArg(args, "intent_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := publisher.PublishLLMApprovalRequest(ctx, controlplane.LLMApprovalCommand{IntentID: intentID, Decision: decision})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish LLM approval request: %v", err)), nil
	}
	return jsonResult(llmCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleLLMRollback(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	publisher, errResult := s.requireLLMCommands()
	if errResult != nil {
		return errResult, nil
	}
	routeID, err := parseUUIDArg(args, "route_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	envID, err := parseUUIDArg(args, "environment_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	requestedBy, _ := args["requested_by"].(string)
	if requestedBy == "" {
		requestedBy = "mcp-agent"
	}
	receipt, err := publisher.PublishLLMRollbackRequest(ctx, controlplane.LLMRollbackCommand{RouteID: routeID, EnvironmentID: envID, RequestedBy: requestedBy})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish LLM rollback request: %v", err)), nil
	}
	return jsonResult(llmCommandReceiptToMap("submitted", receipt))
}

func (s *Server) handleDeleteService(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return signerFirstMCPMutationUnavailable("bahia_delete_service", "service/delete"), nil
}

func (s *Server) handleDeleteEnvironment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return signerFirstMCPMutationUnavailable("bahia_delete_environment", "environment/delete"), nil
}

func (s *Server) handleListArtifacts(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	artifacts, err := s.registry.ListArtifacts(ctx, serviceID, limit, 0)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list artifacts: %v", err)), nil
	}

	result := map[string]interface{}{
		"artifacts": artifactsToMaps(artifacts),
		"total":     len(artifacts),
	}
	return jsonResult(result)
}

func (s *Server) handleGetArtifact(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	artifactIDStr, _ := args["artifact_id"].(string)

	artifactID, err := uuid.Parse(artifactIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid artifact_id: %v", err)), nil
	}

	artifact, err := s.registry.GetArtifact(ctx, artifactID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get artifact: %v", err)), nil
	}
	if artifact == nil {
		return errorResult("artifact not found"), nil
	}

	result := map[string]interface{}{
		"id":           artifact.ID.String(),
		"build_id":     artifact.BuildID.String(),
		"service_id":   artifact.ServiceID.String(),
		"image_repo":   artifact.ImageRepo,
		"image_tag":    artifact.ImageTag,
		"image_digest": artifact.ImageDigest,
		"scan_status":  artifact.ScanStatus,
		"created_at":   artifact.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	return jsonResult(result)
}

func (s *Server) handleRegisterArtifact(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	buildIDStr, _ := args["build_id"].(string)
	serviceIDStr, _ := args["service_id"].(string)
	imageRepo, _ := args["image_repo"].(string)
	imageTag, _ := args["image_tag"].(string)
	imageDigest, _ := args["image_digest"].(string)
	manifestMediaType, _ := args["manifest_media_type"].(string)
	sbomURL, _ := args["sbom_url"].(string)
	signatureRef, _ := args["signature_ref"].(string)
	scanStatus, _ := args["scan_status"].(string)
	metadata, _ := args["metadata"].(map[string]interface{})

	buildID, err := uuid.Parse(buildIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid build_id: %v", err)), nil
	}
	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}
	if err := domain.ValidateRequiredString(imageRepo, "image_repo"); err != nil {
		return errorResult(err.Error()), nil
	}
	if err := domain.ValidateRequiredString(imageTag, "image_tag"); err != nil {
		return errorResult(err.Error()), nil
	}
	if err := domain.ValidateImageDigest(imageDigest); err != nil {
		return errorResult(err.Error()), nil
	}
	if err := domain.ValidateScanStatus(domain.ScanStatus(scanStatus)); err != nil {
		return errorResult(err.Error()), nil
	}

	var sizeBytes *int64
	if size, ok := args["size_bytes"].(float64); ok {
		sizeInt := int64(size)
		sizeBytes = &sizeInt
	}

	if s.artifactCommands == nil {
		return signerFirstMCPMutationUnavailable("bahia_register_artifact", "artifact/register"), nil
	}
	receipt, err := s.artifactCommands.PublishArtifactRegisterRequest(ctx, controlplane.ArtifactRegisterCommand{BuildID: buildID, ServiceID: serviceID, ImageRepo: imageRepo, ImageTag: imageTag, ImageDigest: imageDigest, ManifestMediaType: manifestMediaType, SizeBytes: sizeBytes, SBOMURL: sbomURL, SignatureRef: signatureRef, ScanStatus: domain.ScanStatus(scanStatus), Metadata: metadata})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish artifact register request: %v", err)), nil
	}
	return jsonResult(artifactCommandReceiptToMap(receipt))
}

func (s *Server) handleListSignatures(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.signatures == nil {
		return errorResult("signature tools are not configured"), nil
	}

	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	signatures, err := s.signatures.ListByArtifact(ctx, artifactID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list signatures: %v", err)), nil
	}

	return jsonResult(map[string]interface{}{
		"artifact_id": artifactID.String(),
		"signatures":  signaturesToMaps(signatures),
		"total":       len(signatures),
	})
}

func (s *Server) handleListVerifiedSignatures(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.signatures == nil {
		return errorResult("signature tools are not configured"), nil
	}

	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	signatures, err := s.signatures.ListVerifiedByArtifact(ctx, artifactID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list verified signatures: %v", err)), nil
	}

	return jsonResult(map[string]interface{}{
		"artifact_id": artifactID.String(),
		"signatures":  signaturesToMaps(signatures),
		"total":       len(signatures),
	})
}

func (s *Server) handleHasVerifiedSignature(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.signatures == nil {
		return errorResult("signature tools are not configured"), nil
	}

	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	hasVerified, err := s.signatures.HasVerifiedSignature(ctx, artifactID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to check signature status: %v", err)), nil
	}

	return jsonResult(map[string]interface{}{
		"artifact_id":            artifactID.String(),
		"has_verified_signature": hasVerified,
	})
}

func (s *Server) handleGetSignature(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.signatures == nil {
		return errorResult("signature tools are not configured"), nil
	}

	signatureID, err := parseRequiredUUIDArg(args, "signature_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	signature, err := s.signatures.GetByID(ctx, signatureID)
	if err != nil {
		if err == repository.ErrNotFound {
			return errorResult("signature not found"), nil
		}
		return errorResult(fmt.Sprintf("failed to get signature: %v", err)), nil
	}

	return jsonResult(signatureToMap(signature))
}

func (s *Server) handleVerifySignatures(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.signatures == nil {
		return errorResult("signature tools are not configured"), nil
	}
	if s.signVerifier == nil {
		return errorResult("signature verifier is not configured"), nil
	}
	if s.registry == nil {
		return errorResult("artifact registry is not configured"), nil
	}

	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	artifact, err := s.registry.GetArtifact(ctx, artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			return errorResult("artifact not found"), nil
		}
		return errorResult(fmt.Sprintf("failed to get artifact: %v", err)), nil
	}
	if artifact == nil {
		return errorResult("artifact not found"), nil
	}

	signatures, err := s.signVerifier.VerifySignatures(ctx, artifact)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to verify signatures: %v", err)), nil
	}

	stored := 0
	for i := range signatures {
		if err := s.signatures.Create(ctx, &signatures[i]); err != nil {
			s.logger.Warn("failed to store signature record",
				zap.String("artifact_id", artifactID.String()),
				zap.String("signature_id", signatures[i].ID.String()),
				zap.Error(err),
			)
			continue
		}
		stored++
	}

	return jsonResult(map[string]interface{}{
		"artifact_id": artifactID.String(),
		"discovered":  len(signatures),
		"stored":      stored,
		"signatures":  signaturesToMaps(signatures),
	})
}

func (s *Server) handleGetSBOM(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.sboms == nil {
		return errorResult("SBOM tools are not configured"), nil
	}

	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	sbom, err := s.sboms.GetSBOMByArtifact(ctx, artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			return errorResult("SBOM not found for artifact"), nil
		}
		return errorResult(fmt.Sprintf("failed to get SBOM: %v", err)), nil
	}

	return jsonResult(sbomToMap(sbom))
}

func (s *Server) handleGetSBOMPackages(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.sboms == nil {
		return errorResult("SBOM tools are not configured"), nil
	}

	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	sbom, err := s.sboms.GetSBOMByArtifact(ctx, artifactID)
	if err != nil {
		if err == repository.ErrNotFound {
			return errorResult("SBOM not found for artifact"), nil
		}
		return errorResult(fmt.Sprintf("failed to get SBOM: %v", err)), nil
	}

	packages, err := s.sboms.ListPackagesBySBOM(ctx, sbom.ID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list SBOM packages: %v", err)), nil
	}

	result := map[string]interface{}{
		"artifact_id": artifactID.String(),
		"sbom_id":     sbom.ID.String(),
		"packages":    sbomPackagesToMaps(packages),
		"total":       len(packages),
	}
	return jsonResult(result)
}

func (s *Server) handleSearchSBOMPackages(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.sboms == nil {
		return errorResult("SBOM tools are not configured"), nil
	}

	query, _ := args["query"].(string)
	if query == "" {
		query, _ = args["package"].(string)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return errorResult("query is required"), nil
	}
	limit := optionalIntArg(args, "limit", 100)

	packages, err := s.sboms.SearchPackagesByName(ctx, query, limit)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to search SBOM packages: %v", err)), nil
	}

	result := map[string]interface{}{
		"query":    query,
		"packages": sbomPackagesToMaps(packages),
		"total":    len(packages),
	}
	return jsonResult(result)
}

func (s *Server) handleIngestSBOM(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.sboms == nil {
		return errorResult("SBOM tools are not configured"), nil
	}
	if s.registry == nil {
		return errorResult("registry service is not configured"), nil
	}

	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if _, err := s.registry.GetArtifact(ctx, artifactID); err != nil {
		if err == repository.ErrNotFound {
			return errorResult("artifact not found"), nil
		}
		return errorResult(fmt.Sprintf("failed to get artifact: %v", err)), nil
	}

	data, err := sbomDataArg(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	hash := sha256.Sum256(data)
	rawHash := hex.EncodeToString(hash[:])
	if existing, err := s.sboms.GetSBOMByHash(ctx, rawHash); err == nil && existing != nil {
		result := map[string]interface{}{
			"status": "existing",
			"sbom":   sbomToMap(existing),
		}
		return jsonResult(result)
	}

	parsed, err := adapterSBOM.Parse(data, artifactID)
	if err != nil {
		return errorResult(fmt.Sprintf("parsing SBOM: %v", err)), nil
	}
	if sourceURL, _ := args["source_url"].(string); strings.TrimSpace(sourceURL) != "" {
		parsed.SBOM.SourceURL = strings.TrimSpace(sourceURL)
	}

	if err := s.sboms.CreateSBOM(ctx, &parsed.SBOM); err != nil {
		return errorResult(fmt.Sprintf("storing SBOM: %v", err)), nil
	}
	if len(parsed.Packages) > 0 {
		if err := s.sboms.CreatePackages(ctx, parsed.Packages); err != nil {
			return errorResult(fmt.Sprintf("storing SBOM packages: %v", err)), nil
		}
	}

	result := map[string]interface{}{
		"status":        "created",
		"sbom_id":       parsed.SBOM.ID.String(),
		"package_count": len(parsed.Packages),
		"sbom":          sbomToMap(&parsed.SBOM),
	}
	return jsonResult(result)
}

func (s *Server) handleListBuilds(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	builds, err := s.registry.ListBuilds(ctx, serviceID, limit, 0)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list builds: %v", err)), nil
	}

	result := map[string]interface{}{
		"builds": buildsToMaps(builds),
		"total":  len(builds),
	}
	return jsonResult(result)
}

func (s *Server) handleGetBuild(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	buildIDStr, _ := args["build_id"].(string)

	buildID, err := uuid.Parse(buildIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid build_id: %v", err)), nil
	}

	build, err := s.registry.GetBuild(ctx, buildID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get build: %v", err)), nil
	}
	if build == nil {
		return errorResult("build not found"), nil
	}

	result := map[string]interface{}{
		"id":         build.ID.String(),
		"service_id": build.ServiceID.String(),
		"git_sha":    build.GitSHA,
		"git_ref":    build.GitRef,
		"status":     build.Status,
		"ci_system":  build.CISystem,
		"ci_run_id":  build.CIRunID,
		"created_at": build.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	return jsonResult(result)
}

func (s *Server) handleRegisterBuild(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	gitSHA, _ := args["git_sha"].(string)
	gitRef, _ := args["git_ref"].(string)
	ciSystem, _ := args["ci_system"].(string)
	ciRunID, _ := args["ci_run_id"].(string)
	loomJobID, _ := args["loom_job_id"].(string)
	status, _ := args["status"].(string)
	sourceEventID, _ := args["source_event_id"].(string)
	metadata, _ := args["metadata"].(map[string]interface{})

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}
	if err := domain.ValidateGitSHA(gitSHA); err != nil {
		return errorResult(err.Error()), nil
	}
	if err := domain.ValidateRequiredString(gitRef, "git_ref"); err != nil {
		return errorResult(err.Error()), nil
	}
	if err := domain.ValidateRequiredString(ciRunID, "ci_run_id"); err != nil {
		return errorResult(err.Error()), nil
	}
	if err := domain.ValidateBuildStatus(domain.BuildStatus(status)); err != nil {
		return errorResult(err.Error()), nil
	}
	if ciSystem == "" {
		ciSystem = "hive-ci"
	}

	build := &domain.Build{
		ID:            uuid.New(),
		ServiceID:     serviceID,
		GitSHA:        gitSHA,
		GitRef:        gitRef,
		CISystem:      ciSystem,
		CIRunID:       ciRunID,
		LoomJobID:     loomJobID,
		Status:        domain.BuildStatus(status),
		SourceEventID: sourceEventID,
		Metadata:      metadata,
	}

	if err := s.registry.RegisterBuild(ctx, build); err != nil {
		return errorResult(fmt.Sprintf("failed to register build: %v", err)), nil
	}

	result := map[string]interface{}{
		"status":   "created",
		"build_id": build.ID.String(),
		"build": map[string]interface{}{
			"id":              build.ID.String(),
			"service_id":      build.ServiceID.String(),
			"git_sha":         build.GitSHA,
			"git_ref":         build.GitRef,
			"ci_system":       build.CISystem,
			"ci_run_id":       build.CIRunID,
			"status":          string(build.Status),
			"loom_job_id":     build.LoomJobID,
			"source_event_id": build.SourceEventID,
		},
	}
	return jsonResult(result)
}

func (s *Server) handleUpdateBuildStatus(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	buildIDStr, _ := args["build_id"].(string)
	statusStr, _ := args["status"].(string)

	buildID, err := uuid.Parse(buildIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid build_id: %v", err)), nil
	}

	status := domain.BuildStatus(statusStr)
	if err := domain.ValidateBuildStatus(status); err != nil {
		return errorResult(err.Error()), nil
	}

	if err := s.registry.UpdateBuildStatus(ctx, buildID, status); err != nil {
		return errorResult(fmt.Sprintf("failed to update build status: %v", err)), nil
	}

	result := map[string]interface{}{
		"status":   "updated",
		"build_id": buildID.String(),
		"build": map[string]interface{}{
			"id":     buildID.String(),
			"status": string(status),
		},
	}
	return jsonResult(result)
}

func (s *Server) handleListStates(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	envIDStr, _ := args["environment_id"].(string)

	var states []domain.EnvironmentServiceState
	var err error

	if envIDStr != "" {
		envID, parseErr := uuid.Parse(envIDStr)
		if parseErr != nil {
			return errorResult(fmt.Sprintf("invalid environment_id: %v", parseErr)), nil
		}
		states, err = s.registry.ListEnvironmentStates(ctx, envID)
	} else {
		states, err = s.registry.ListAllStates(ctx)
	}

	if err != nil {
		return errorResult(fmt.Sprintf("failed to list states: %v", err)), nil
	}

	result := map[string]interface{}{
		"states": statesToMaps(states),
		"total":  len(states),
	}
	return jsonResult(result)
}

func (s *Server) handleListDrifted(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	states, err := s.registry.ListDriftedStates(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list drifted states: %v", err)), nil
	}

	result := map[string]interface{}{
		"drifted": statesToMaps(states),
		"total":   len(states),
	}
	return jsonResult(result)
}

func (s *Server) handleGetObservation(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	envIDStr, _ := args["environment_id"].(string)

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	envID, err := uuid.Parse(envIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
	}

	obs, err := s.registry.GetLatestObservation(ctx, serviceID, envID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get observation: %v", err)), nil
	}
	if obs == nil {
		return errorResult("no observation found"), nil
	}

	result := map[string]interface{}{
		"id":             obs.ID.String(),
		"service_id":     obs.ServiceID.String(),
		"environment_id": obs.EnvironmentID.String(),
		"image_digest":   obs.ObservedImageDigest,
		"container_id":   obs.ObservedContainerID,
		"health_status":  obs.HealthStatus,
		"observed_at":    obs.ObservedAt.Format("2006-01-02T15:04:05Z"),
	}
	return jsonResult(result)
}

func (s *Server) handleListIntents(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	envIDStr, _ := args["environment_id"].(string)
	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	envID, err := uuid.Parse(envIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
	}

	intents, err := s.registry.ListDeploymentIntents(ctx, serviceID, envID, limit, 0)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list intents: %v", err)), nil
	}

	result := map[string]interface{}{
		"intents": intentsToMaps(intents),
		"total":   len(intents),
	}
	return jsonResult(result)
}

func (s *Server) handleListRuns(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	intentIDStr, _ := args["intent_id"].(string)

	intentID, err := uuid.Parse(intentIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid intent_id: %v", err)), nil
	}

	runs, err := s.registry.ListDeploymentRuns(ctx, intentID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list runs: %v", err)), nil
	}

	result := map[string]interface{}{
		"runs":  runsToMaps(runs),
		"total": len(runs),
	}
	return jsonResult(result)
}

func (s *Server) handleCreateRun(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	intentIDStr, _ := args["intent_id"].(string)
	workerPubkey, _ := args["worker_pubkey"].(string)

	intentID, err := uuid.Parse(intentIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid intent_id: %v", err)), nil
	}

	run := &domain.DeploymentRun{
		ID:                 uuid.New(),
		DeploymentIntentID: intentID,
		WorkerPubkey:       workerPubkey,
		Status:             domain.RunStatusQueued,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	if err := s.registry.CreateDeploymentRun(ctx, run); err != nil {
		return errorResult(fmt.Sprintf("failed to create run: %v", err)), nil
	}

	s.logger.Info("deployment run created",
		zap.String("run_id", run.ID.String()),
		zap.String("intent_id", intentID.String()),
	)

	result := map[string]interface{}{
		"status":    "created",
		"run_id":    run.ID.String(),
		"intent_id": intentID.String(),
		"message":   "Deployment run created",
	}
	return jsonResult(result)
}

func (s *Server) handleGetRun(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	runIDStr, _ := args["run_id"].(string)

	runID, err := uuid.Parse(runIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid run_id: %v", err)), nil
	}

	run, err := s.registry.GetDeploymentRun(ctx, runID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get run: %v", err)), nil
	}
	if run == nil {
		return errorResult("run not found"), nil
	}

	result := runToMap(run)
	return jsonResult(result)
}

func (s *Server) handleGetRunLogs(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	runIDStr, _ := args["run_id"].(string)

	runID, err := uuid.Parse(runIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid run_id: %v", err)), nil
	}
	if s.registry == nil {
		return errorResult("deployment registry is not configured"), nil
	}
	if s.logService == nil {
		return errorResult("run log tools are not configured"), nil
	}

	run, err := s.registry.GetDeploymentRun(ctx, runID)
	if err != nil {
		if err == repository.ErrNotFound {
			return errorResult("run not found"), nil
		}
		return errorResult(fmt.Sprintf("failed to get run: %v", err)), nil
	}
	if run == nil {
		return errorResult("run not found"), nil
	}
	if !isTerminalRunStatus(run.Status) {
		return errorResult("run is not completed; stored logs are available for terminal runs only"), nil
	}

	logs, err := s.logService.FetchRunLogs(ctx, run)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to fetch run logs: %v", err)), nil
	}

	tail := 0
	switch v := args["tail"].(type) {
	case float64:
		tail = int(v)
	case int:
		tail = v
	case int64:
		tail = int(v)
	}
	if tail > 0 {
		logs.Stdout = adapterruntime.TailLogs(logs.Stdout, tail)
		logs.Stderr = adapterruntime.TailLogs(logs.Stderr, tail)
	}

	stream, _ := args["stream"].(string)
	if stream == "" {
		stream = "merged"
	}
	switch stream {
	case "stdout":
		logs.Stderr = ""
	case "stderr":
		logs.Stdout = ""
	case "merged":
		// Return both streams plus a merged convenience field.
	default:
		return errorResult("invalid stream parameter; use stdout, stderr, or merged"), nil
	}

	result := runLogsToMap(logs, stream)
	return jsonResult(result)
}

func (s *Server) handleCompleteRun(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	runIDStr, _ := args["run_id"].(string)
	statusStr, _ := args["status"].(string)

	runID, err := uuid.Parse(runIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid run_id: %v", err)), nil
	}

	var status domain.DeploymentRunStatus
	switch statusStr {
	case "succeeded":
		status = domain.RunStatusSucceeded
	case "failed":
		status = domain.RunStatusFailed
	case "cancelled":
		status = domain.RunStatusCancelled
	default:
		return errorResult(fmt.Sprintf("invalid status: %s (must be succeeded, failed, or cancelled)", statusStr)), nil
	}

	var exitCode *int
	if code, ok := args["exit_code"].(float64); ok {
		codeInt := int(code)
		exitCode = &codeInt
	}

	if err := s.registry.CompleteDeploymentRun(ctx, runID, status, exitCode); err != nil {
		return errorResult(fmt.Sprintf("failed to complete run: %v", err)), nil
	}

	s.logger.Info("deployment run completed",
		zap.String("run_id", runID.String()),
		zap.String("status", string(status)),
	)

	result := map[string]interface{}{
		"status":  "completed",
		"run_id":  runID.String(),
		"message": fmt.Sprintf("Deployment run marked as %s", status),
	}
	return jsonResult(result)
}

func (s *Server) handleListSecrets(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.secretsRepo == nil {
		return errorResult("secret management tools are not configured"), nil
	}

	serviceIDStr, _ := args["service_id"].(string)

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	secrets, err := s.secretsRepo.ListByService(ctx, serviceID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list secrets: %v", err)), nil
	}

	result := map[string]interface{}{
		"secrets": secretsToMaps(secrets),
		"total":   len(secrets),
	}
	return jsonResult(result)
}

func (s *Server) handleCreateSecret(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.secretsRepo == nil || s.encryptor == nil {
		return errorResult("secret management tools are not configured"), nil
	}

	serviceIDStr, _ := args["service_id"].(string)
	name, _ := args["name"].(string)
	value, _ := args["value"].(string)
	envIDStr, _ := args["environment_id"].(string)

	if name == "" {
		return errorResult("name is required"), nil
	}
	if value == "" {
		return errorResult("value is required"), nil
	}

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}
	if denied := s.authorizeSecretPermission(ctx, serviceID, domain.PermWriteSecrets); denied != nil {
		return denied, nil
	}

	var envID *uuid.UUID
	if envIDStr != "" {
		parsedEnvID, err := uuid.Parse(envIDStr)
		if err != nil {
			return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
		}
		envID = &parsedEnvID
	}

	// Encrypt the value using AES256-GCM (default encryption method)
	encryptedValue, err := s.encryptor.Encrypt(value, domain.EncryptionAES256)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to encrypt secret: %v", err)), nil
	}

	secret := &domain.ServiceSecret{
		ID:               uuid.New(),
		ServiceID:        serviceID,
		EnvironmentID:    envID,
		Name:             name,
		EncryptedValue:   encryptedValue,
		EncryptionMethod: domain.EncryptionAES256,
		Version:          1,
		CreatedBy:        "mcp-agent",
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if err := s.secretsRepo.Create(ctx, secret); err != nil {
		return errorResult(fmt.Sprintf("failed to create secret: %v", err)), nil
	}

	s.logger.Info("secret created",
		zap.String("secret_id", secret.ID.String()),
		zap.String("service_id", serviceID.String()),
		zap.String("name", name),
	)

	// Return metadata only, not the encrypted value
	result := map[string]interface{}{
		"status":    "created",
		"secret_id": secret.ID.String(),
		"name":      name,
		"version":   secret.Version,
		"message":   "Secret created and encrypted successfully",
	}
	return jsonResult(result)
}

func (s *Server) handleUpdateSecret(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.secretsRepo == nil || s.encryptor == nil {
		return errorResult("secret management tools are not configured"), nil
	}

	secretIDStr, _ := args["secret_id"].(string)
	value, _ := args["value"].(string)

	if value == "" {
		return errorResult("value is required"), nil
	}

	secretID, err := uuid.Parse(secretIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid secret_id: %v", err)), nil
	}

	// Get existing secret
	existing, err := s.secretsRepo.GetByID(ctx, secretID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get secret: %v", err)), nil
	}
	if existing == nil {
		return errorResult("secret not found"), nil
	}
	if denied := s.authorizeSecretPermission(ctx, existing.ServiceID, domain.PermWriteSecrets); denied != nil {
		return denied, nil
	}

	// Encrypt the new value
	encryptedValue, err := s.encryptor.Encrypt(value, domain.EncryptionAES256)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to encrypt secret: %v", err)), nil
	}

	// Update the secret
	existing.EncryptedValue = encryptedValue
	existing.Version++
	existing.UpdatedAt = time.Now()

	if err := s.secretsRepo.Update(ctx, existing); err != nil {
		return errorResult(fmt.Sprintf("failed to update secret: %v", err)), nil
	}

	s.logger.Info("secret updated",
		zap.String("secret_id", secretID.String()),
		zap.Int("version", existing.Version),
	)

	result := map[string]interface{}{
		"status":    "updated",
		"secret_id": secretID.String(),
		"version":   existing.Version,
		"message":   "Secret updated successfully",
	}
	return jsonResult(result)
}

func (s *Server) handleDeleteSecret(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.secretsRepo == nil {
		return errorResult("secret management tools are not configured"), nil
	}

	secretIDStr, _ := args["secret_id"].(string)

	secretID, err := uuid.Parse(secretIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid secret_id: %v", err)), nil
	}

	existing, err := s.secretsRepo.GetByID(ctx, secretID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get secret: %v", err)), nil
	}
	if existing == nil {
		return errorResult("secret not found"), nil
	}
	if denied := s.authorizeSecretPermission(ctx, existing.ServiceID, domain.PermWriteSecrets); denied != nil {
		return denied, nil
	}

	if err := s.secretsRepo.Delete(ctx, secretID); err != nil {
		return errorResult(fmt.Sprintf("failed to delete secret: %v", err)), nil
	}

	s.logger.Info("secret deleted", zap.String("secret_id", secretID.String()))

	result := map[string]interface{}{
		"status":    "deleted",
		"secret_id": secretID.String(),
	}
	return jsonResult(result)
}

func (s *Server) handleListWorkers(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.workers == nil {
		return errorResult("worker tools are not configured"), nil
	}

	capability, _ := args["capability"].(string)
	available, hasAvailable := args["available"].(bool)
	limit := 50
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	// List all workers (status filter in repository)
	workers, err := s.workers.List(ctx, "", limit)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list workers: %v", err)), nil
	}

	// Apply filters in memory if needed
	filtered := make([]domain.Worker, 0)
	for _, w := range workers {
		// Filter by capability if specified
		if capability != "" && !w.HasSoftware(capability) {
			continue
		}
		// Filter by availability if specified
		if hasAvailable {
			isOnline := w.ComputeStatus(time.Now()) == domain.WorkerStatusOnline
			if available != isOnline {
				continue
			}
		}
		filtered = append(filtered, w)
	}

	result := map[string]interface{}{
		"workers": workersToMaps(filtered),
		"total":   len(filtered),
	}
	return jsonResult(result)
}

func (s *Server) handleGetWorker(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.workers == nil {
		return errorResult("worker tools are not configured"), nil
	}

	pubkey, _ := args["pubkey"].(string)
	if pubkey == "" {
		return errorResult("pubkey is required"), nil
	}

	worker, err := s.workers.GetByPubKey(ctx, pubkey)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get worker: %v", err)), nil
	}
	if worker == nil {
		return errorResult("worker not found"), nil
	}

	return jsonResult(workerToMap(worker))
}

func (s *Server) handleGetWorkerPricing(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.workers == nil {
		return errorResult("worker tools are not configured"), nil
	}

	pubkey, _ := args["pubkey"].(string)
	if pubkey == "" {
		return errorResult("pubkey is required"), nil
	}

	worker, err := s.workers.GetByPubKey(ctx, pubkey)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get worker: %v", err)), nil
	}
	if worker == nil {
		return errorResult("worker not found"), nil
	}

	// Return just the pricing information
	result := map[string]interface{}{
		"pubkey":  worker.PubKey,
		"name":    worker.Name,
		"pricing": worker.Pricing,
	}
	return jsonResult(result)
}

func (s *Server) handleEstimateCost(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.payments == nil {
		return errorResult("payment tools are not configured"), nil
	}

	runID, err := parseRequiredUUIDArg(args, "run_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	estimatedDurationSecs := optionalIntArg(args, "estimated_duration_secs", 0)
	estimate, err := s.payments.EstimateCost(ctx, runID, estimatedDurationSecs)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to estimate cost: %v", err)), nil
	}

	return jsonResult(costEstimateToMap(estimate))
}

func (s *Server) handleGetRunCost(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.payments == nil {
		return errorResult("payment tools are not configured"), nil
	}

	runID, err := parseRequiredUUIDArg(args, "run_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	summary, err := s.payments.GetRunCostSummary(ctx, runID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get run cost summary: %v", err)), nil
	}

	payments, err := s.payments.GetRunPayments(ctx, runID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get run payments: %v", err)), nil
	}

	result := map[string]interface{}{
		"run_id":   runID.String(),
		"summary":  costSummaryToMap(summary),
		"payments": paymentRecordsToMaps(payments),
	}
	return jsonResult(result)
}

func (s *Server) handleGetPaymentHistory(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.payments == nil {
		return errorResult("payment tools are not configured"), nil
	}

	workerPubkey, _ := args["worker_pubkey"].(string)
	if workerPubkey == "" {
		workerPubkey, _ = args["worker"].(string)
	}
	if workerPubkey == "" {
		return errorResult("worker_pubkey is required"), nil
	}

	limit := optionalIntArg(args, "limit", 50)
	if limit <= 0 {
		limit = 50
	}

	records, err := s.payments.GetPaymentHistory(ctx, workerPubkey, limit)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get payment history: %v", err)), nil
	}

	result := map[string]interface{}{
		"worker_pubkey": workerPubkey,
		"payments":      paymentRecordsToMaps(records),
		"total":         len(records),
		"limit":         limit,
	}
	return jsonResult(result)
}

func (s *Server) handleGetIntent(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	intentIDStr, _ := args["intent_id"].(string)
	if intentIDStr == "" {
		return errorResult("intent_id is required"), nil
	}

	intentID, err := uuid.Parse(intentIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid intent_id: %v", err)), nil
	}

	intent, err := s.registry.GetDeploymentIntent(ctx, intentID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get intent: %v", err)), nil
	}
	if intent == nil {
		return errorResult("intent not found"), nil
	}

	return jsonResult(intentToMap(intent))
}

func (s *Server) handleToolProvisionRequest(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.toolProvisioning == nil {
		return errorResult("tool provisioning tools are not configured"), nil
	}

	serviceID, err := parseRequiredUUIDArg(args, "service_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	environmentID, err := parseRequiredUUIDArg(args, "environment_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	reason, _ := args["reason"].(string)
	if strings.TrimSpace(reason) == "" {
		return errorResult("reason is required"), nil
	}

	toolsRaw, ok := args["tools"]
	if !ok {
		return errorResult("tools is required"), nil
	}
	toolsJSON, err := json.Marshal(toolsRaw)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid tools: %v", err)), nil
	}
	var tools []domain.ToolRequest
	if err := json.Unmarshal(toolsJSON, &tools); err != nil {
		return errorResult(fmt.Sprintf("invalid tools: %v", err)), nil
	}
	if len(tools) == 0 {
		return errorResult("at least one tool is required"), nil
	}
	for _, t := range tools {
		if strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.Manager) == "" {
			return errorResult("each tool requires name and manager"), nil
		}
	}

	requester, _ := args["requester"].(string)
	if requester == "" {
		requester = "mcp"
	}
	intent := &domain.ToolProvisionIntent{
		ID:               uuid.New(),
		ServiceID:        serviceID,
		EnvironmentID:    environmentID,
		RequestedTools:   tools,
		Status:           domain.ToolProvisionStatusAwaitingApproval,
		ApprovalRequired: true,
		ApprovalFlags:    []string{"manual_review_required", reason},
		RequesterPubkey:  requester,
		CreatedAt:        time.Now().UTC(),
	}
	if err := s.toolProvisioning.CreateIntent(ctx, intent); err != nil {
		return errorResult(fmt.Sprintf("failed to create tool provision intent: %v", err)), nil
	}

	if s.notificationDisp != nil {
		serviceName := ""
		if svc, svcErr := s.registry.GetService(ctx, serviceID); svcErr == nil && svc != nil {
			serviceName = svc.Name
		}
		baseURL := "http://localhost:7777"
		payload := map[string]any{
			"intent_id":     intent.ID,
			"service_id":    intent.ServiceID,
			"service_name":  serviceName,
			"requester":     intent.RequesterPubkey,
			"tools":         intent.RequestedTools,
			"flags":         intent.ApprovalFlags,
			"security_scan": intent.SecurityScanResults,
			"approve_url":   fmt.Sprintf("%s/tools/%s/approve", baseURL, intent.ID),
			"reject_url":    fmt.Sprintf("%s/tools/%s/reject", baseURL, intent.ID),
		}
		s.notificationDisp.Dispatch(ctx, string(events.EventToolProvisionApprovalRequired), payload)
	}

	return jsonResult(map[string]interface{}{
		"status": "created",
		"intent": toolProvisionIntentToMap(intent),
	})
}

func (s *Server) handleToolProvisionStatus(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.toolProvisioning == nil {
		return errorResult("tool provisioning tools are not configured"), nil
	}
	intentID, err := parseRequiredUUIDArg(args, "intent_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	intent, err := s.toolProvisioning.GetIntent(ctx, intentID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get tool provisioning intent: %v", err)), nil
	}
	if intent == nil {
		return errorResult("intent not found"), nil
	}
	return jsonResult(toolProvisionIntentToMap(intent))
}

func (s *Server) handleToolProvisionApprove(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.handleToolProvisionApprovalResponse(ctx, args, "approve")
}

func (s *Server) handleToolProvisionReject(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.handleToolProvisionApprovalResponse(ctx, args, "reject")
}

func (s *Server) handleToolProvisionApprovalResponse(ctx context.Context, args map[string]interface{}, action string) (*ToolResult, error) {
	if s.toolApprovalCommands == nil {
		return errorResult("tool approval command publisher is not configured"), nil
	}
	intentID, err := parseRequiredUUIDArg(args, "intent_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	reason := strings.TrimSpace(stringArg(args, "reason"))
	if reason == "" {
		return errorResult("reason is required"), nil
	}
	receipt, err := s.toolApprovalCommands.PublishToolApprovalResponse(ctx, controlplane.ToolApprovalCommand{IntentID: intentID, Action: action, Reason: reason, IdempotencyKey: stringArg(args, "idempotency_key")})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish ToolApprovalResponse request: %v", err)), nil
	}
	return jsonResult(receipt)
}

func (s *Server) handleToolDenylistAdd(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.toolProvisioning == nil {
		return errorResult("tool provisioning tools are not configured"), nil
	}
	packageName, _ := args["package"].(string)
	manager, _ := args["manager"].(string)
	reason, _ := args["reason"].(string)
	if strings.TrimSpace(packageName) == "" || strings.TrimSpace(manager) == "" || strings.TrimSpace(reason) == "" {
		return errorResult("package, manager, and reason are required"), nil
	}
	entry := &domain.ToolDenylistEntry{PackageName: packageName, Manager: manager, Reason: reason, BlockedBy: "mcp"}
	if err := s.toolProvisioning.AddToDenylist(ctx, entry); err != nil {
		return errorResult(fmt.Sprintf("failed to add denylist entry: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"status": "added", "entry": toolDenylistEntryToMap(entry)})
}

func (s *Server) handleToolDenylistRemove(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.toolProvisioning == nil {
		return errorResult("tool provisioning tools are not configured"), nil
	}
	packageName, _ := args["package"].(string)
	manager, _ := args["manager"].(string)
	if strings.TrimSpace(packageName) == "" || strings.TrimSpace(manager) == "" {
		return errorResult("package and manager are required"), nil
	}
	if err := s.toolProvisioning.RemoveFromDenylist(ctx, packageName, manager); err != nil {
		return errorResult(fmt.Sprintf("failed to remove denylist entry: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"status": "removed"})
}

func (s *Server) handleToolDenylistList(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.toolProvisioning == nil {
		return errorResult("tool provisioning tools are not configured"), nil
	}
	entries, err := s.toolProvisioning.ListDenylist(ctx)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list denylist entries: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"entries": toolDenylistEntriesToMaps(entries), "total": len(entries)})
}

func (s *Server) handleToolProfileGet(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.toolProvisioning == nil {
		return errorResult("tool provisioning tools are not configured"), nil
	}
	serviceID, err := parseRequiredUUIDArg(args, "service_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	environmentID, err := parseRequiredUUIDArg(args, "environment_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	state, err := s.toolProvisioning.GetProfileState(ctx, serviceID, environmentID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get tool profile: %v", err)), nil
	}
	if state == nil {
		return jsonResult(map[string]interface{}{"state": nil})
	}
	return jsonResult(map[string]interface{}{"state": toolProfileStateToMap(state)})
}

// --- Helper Functions ---

func decodeToolArgs(args map[string]interface{}, out interface{}) error {
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func parseUUIDArg(args map[string]interface{}, key string) (uuid.UUID, error) {
	raw, _ := args[key].(string)
	if raw == "" {
		return uuid.Nil, fmt.Errorf("%s is required", key)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %v", key, err)
	}
	return id, nil
}

func limitOffsetArgs(args map[string]interface{}, defaultLimit int) (int, int) {
	limit := defaultLimit
	offset := 0
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	if l, ok := args["limit"].(int); ok && l > 0 {
		limit = l
	}
	if o, ok := args["offset"].(float64); ok && o > 0 {
		offset = int(o)
	}
	if o, ok := args["offset"].(int); ok && o > 0 {
		offset = o
	}
	return limit, offset
}

func llmRouteToMap(route *domain.LLMRoute) map[string]interface{} {
	if route == nil {
		return nil
	}
	return map[string]interface{}{
		"id":                       route.ID.String(),
		"name":                     route.Name,
		"description":              route.Description,
		"gateway_config":           route.GatewayConfig,
		"default_placement_policy": route.DefaultPlacementPolicy,
		"default_promotion_gate":   route.DefaultPromotionGate,
		"metadata":                 route.Metadata,
		"created_at":               route.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":               route.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func llmReleaseToMap(release *domain.LLMRelease) map[string]interface{} {
	if release == nil {
		return nil
	}
	return map[string]interface{}{
		"id":                  release.ID.String(),
		"route_id":            release.RouteID.String(),
		"version":             release.Version,
		"model_ref":           release.ModelRef,
		"model_source":        release.ModelSource,
		"model_revision":      release.ModelRevision,
		"estimated_vram_gb":   release.EstimatedVRAMGB,
		"backend_preferences": release.BackendPreferences,
		"runtime_backend":     release.RuntimeBackend,
		"external_backend":    release.ExternalBackend,
		"placement_policy":    release.PlacementPolicy,
		"promotion_gate":      release.PromotionGate,
		"metadata":            release.Metadata,
		"created_at":          release.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func serviceCommandReceiptToMap(receipt *controlplane.ServiceCommandReceipt) map[string]interface{} {
	result := map[string]interface{}{"status": "submitted", "timeout_seconds": 30}
	if receipt == nil {
		return result
	}
	if receipt.Status != "" {
		result["status"] = receipt.Status
	}
	result["request_event_id"] = receipt.RequestEventID
	result["request_pubkey"] = receipt.RequestPubkey
	result["request_kind"] = receipt.RequestKind
	result["status_kind"] = receipt.StatusKind
	result["result_kind"] = receipt.ResultKind
	result["idempotency_key"] = receipt.IdempotencyKey
	result["published_relays"] = receipt.PublishedRelays
	if receipt.Error != "" {
		result["error"] = receipt.Error
	}
	if receipt.RetryHint != "" {
		result["retry_hint"] = receipt.RetryHint
	}
	if receipt.ServiceID != "" {
		result["service_id"] = receipt.ServiceID
	}
	if receipt.EnvironmentID != "" {
		result["environment_id"] = receipt.EnvironmentID
	}
	if receipt.ArtifactID != "" {
		result["artifact_id"] = receipt.ArtifactID
	}
	if receipt.IntentID != "" {
		result["intent_id"] = receipt.IntentID
	}
	if receipt.Decision != "" {
		result["decision"] = receipt.Decision
	}
	return result
}

func artifactCommandReceiptToMap(receipt *controlplane.ArtifactCommandReceipt) map[string]interface{} {
	result := map[string]interface{}{"status": "submitted"}
	if receipt == nil {
		return result
	}
	result["request_event_id"] = receipt.RequestEventID
	result["request_pubkey"] = receipt.RequestPubkey
	result["request_kind"] = receipt.RequestKind
	result["result_kind"] = receipt.ResultKind
	result["registry_kind"] = receipt.RegistryKind
	result["published_relays"] = receipt.PublishedRelays
	if receipt.Status != "" {
		result["status"] = receipt.Status
	}
	if receipt.Error != "" {
		result["error"] = receipt.Error
	}
	if receipt.BuildID != "" {
		result["build_id"] = receipt.BuildID
	}
	if receipt.ServiceID != "" {
		result["service_id"] = receipt.ServiceID
	}
	if receipt.ImageDigest != "" {
		result["image_digest"] = receipt.ImageDigest
	}
	if len(receipt.RelayOutcomes) > 0 {
		result["relay_outcomes"] = receipt.RelayOutcomes
	}
	return result
}

func mcpIdempotencyKey(args map[string]interface{}, prefix string, parts ...string) string {
	if explicit, _ := args["idempotency_key"].(string); strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	h := sha256.New()
	_, _ = h.Write([]byte(prefix))
	for _, part := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(part))
	}
	return prefix + ":" + hex.EncodeToString(h.Sum(nil))[:24]
}

func llmCommandReceiptToMap(status string, receipt *controlplane.LLMCommandReceipt) map[string]interface{} {
	result := map[string]interface{}{"status": status, "timeout_seconds": 30}
	if receipt == nil {
		return result
	}
	result["request_event_id"] = receipt.RequestEventID
	result["request_pubkey"] = receipt.RequestPubkey
	result["request_kind"] = receipt.RequestKind
	if receipt.StatusKind > 0 {
		result["status_kind"] = receipt.StatusKind
	}
	result["result_kind"] = receipt.ResultKind
	result["registry_kind"] = receipt.RegistryKind
	result["state_kind"] = receipt.StateKind
	result["idempotency_key"] = receipt.IdempotencyKey
	result["published_relays"] = receipt.PublishedRelays
	if receipt.Status != "" {
		result["status"] = receipt.Status
	}
	if receipt.Error != "" {
		result["error"] = receipt.Error
	}
	if receipt.RetryHint != "" {
		result["retry_hint"] = receipt.RetryHint
	}
	if receipt.RouteID != "" {
		result["route_id"] = receipt.RouteID
	}
	if receipt.EnvironmentID != "" {
		result["environment_id"] = receipt.EnvironmentID
	}
	if receipt.ReleaseID != "" {
		result["release_id"] = receipt.ReleaseID
	}
	if receipt.IntentID != "" {
		result["intent_id"] = receipt.IntentID
	}
	if receipt.Decision != "" {
		result["decision"] = receipt.Decision
	}
	return result
}

func jsonResult(data interface{}) (*ToolResult, error) {
	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal result: %v", err)), nil
	}

	return &ToolResult{
		Content: []Content{{
			Type: "text",
			Text: string(jsonBytes),
		}},
	}, nil
}

func errorResult(message string) *ToolResult {
	return &ToolResult{
		Content: []Content{{
			Type: "text",
			Text: message,
		}},
		IsError: true,
	}
}

func toolProvisionIntentToMap(intent *domain.ToolProvisionIntent) map[string]interface{} {
	if intent == nil {
		return nil
	}
	m := map[string]interface{}{
		"id":                    intent.ID.String(),
		"service_id":            intent.ServiceID.String(),
		"environment_id":        intent.EnvironmentID.String(),
		"requested_tools":       intent.RequestedTools,
		"resolved_tools":        intent.ResolvedTools,
		"security_scan_results": intent.SecurityScanResults,
		"toolset_hash":          intent.ToolsetHash,
		"status":                string(intent.Status),
		"approval_required":     intent.ApprovalRequired,
		"approval_flags":        intent.ApprovalFlags,
		"approved_by":           intent.ApprovedBy,
		"nostr_event_id":        intent.NostrEventID,
		"requester_pubkey":      intent.RequesterPubkey,
		"created_at":            intent.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if intent.ApprovedAt != nil {
		m["approved_at"] = intent.ApprovedAt.Format("2006-01-02T15:04:05Z")
	}
	return m
}

func toolProfileStateToMap(state *domain.ToolProfileState) map[string]interface{} {
	if state == nil {
		return nil
	}
	return map[string]interface{}{
		"service_id":            state.ServiceID.String(),
		"environment_id":        state.EnvironmentID.String(),
		"current_toolset_hash":  state.CurrentToolsetHash,
		"current_image_digest":  state.CurrentImageDigest,
		"installed_tools":       state.InstalledTools,
		"previous_image_digest": state.PreviousImageDigest,
		"updated_at":            state.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func toolDenylistEntryToMap(entry *domain.ToolDenylistEntry) map[string]interface{} {
	if entry == nil {
		return nil
	}
	return map[string]interface{}{
		"package":    entry.PackageName,
		"manager":    entry.Manager,
		"reason":     entry.Reason,
		"source":     entry.Source,
		"blocked_at": entry.BlockedAt.Format("2006-01-02T15:04:05Z"),
		"blocked_by": entry.BlockedBy,
	}
}

func toolDenylistEntriesToMaps(entries []domain.ToolDenylistEntry) []map[string]interface{} {
	result := make([]map[string]interface{}, len(entries))
	for i := range entries {
		result[i] = toolDenylistEntryToMap(&entries[i])
	}
	return result
}

func serviceToMap(svc *domain.Service) map[string]interface{} {
	return map[string]interface{}{
		"id":             svc.ID.String(),
		"name":           svc.Name,
		"repo_url":       svc.RepoURL,
		"artifact_repo":  svc.ArtifactRepo,
		"default_branch": svc.DefaultBranch,
		"runtime_type":   svc.RuntimeType,
		"created_at":     svc.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":     svc.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func servicesToMaps(services []domain.Service) []map[string]interface{} {
	result := make([]map[string]interface{}, len(services))
	for i := range services {
		result[i] = serviceToMap(&services[i])
	}
	return result
}

func environmentToMap(env *domain.Environment) map[string]interface{} {
	m := map[string]interface{}{
		"id":              env.ID.String(),
		"name":            env.Name,
		"protected":       env.Protected,
		"deploy_strategy": env.DeployStrategy,
		"created_at":      env.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":      env.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if env.LoomWorkerSelector != nil {
		m["loom_worker_selector"] = env.LoomWorkerSelector
	}
	if env.RuntimeConfig != nil {
		m["runtime_config"] = env.RuntimeConfig
	}
	return m
}

func environmentsToMaps(envs []domain.Environment) []map[string]interface{} {
	result := make([]map[string]interface{}, len(envs))
	for i := range envs {
		result[i] = environmentToMap(&envs[i])
	}
	return result
}

func artifactsToMaps(artifacts []domain.Artifact) []map[string]interface{} {
	result := make([]map[string]interface{}, len(artifacts))
	for i, a := range artifacts {
		result[i] = map[string]interface{}{
			"id":           a.ID.String(),
			"build_id":     a.BuildID.String(),
			"service_id":   a.ServiceID.String(),
			"image_repo":   a.ImageRepo,
			"image_tag":    a.ImageTag,
			"image_digest": a.ImageDigest,
			"scan_status":  a.ScanStatus,
			"created_at":   a.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}
	return result
}

func signatureToMap(sig *domain.ArtifactSignature) map[string]interface{} {
	if sig == nil {
		return nil
	}
	m := map[string]interface{}{
		"id":                 sig.ID.String(),
		"artifact_id":        sig.ArtifactID.String(),
		"signer_identity":    sig.SignerIdentity,
		"signature_type":     string(sig.SignatureType),
		"signature_ref":      sig.SignatureRef,
		"verified":           sig.Verified,
		"verification_error": sig.VerificationError,
		"metadata":           sig.Metadata,
		"created_at":         sig.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if sig.VerifiedAt != nil {
		m["verified_at"] = sig.VerifiedAt.Format("2006-01-02T15:04:05Z")
	}
	return m
}

func signaturesToMaps(signatures []domain.ArtifactSignature) []map[string]interface{} {
	result := make([]map[string]interface{}, len(signatures))
	for i := range signatures {
		result[i] = signatureToMap(&signatures[i])
	}
	return result
}

func sbomToMap(s *domain.ArtifactSBOM) map[string]interface{} {
	if s == nil {
		return nil
	}
	return map[string]interface{}{
		"id":                  s.ID.String(),
		"artifact_id":         s.ArtifactID.String(),
		"format":              string(s.Format),
		"source_url":          s.SourceURL,
		"package_count":       s.PackageCount,
		"vulnerability_count": s.VulnerabilityCount,
		"critical_count":      s.CriticalCount,
		"high_count":          s.HighCount,
		"raw_hash":            s.RawHash,
		"metadata":            s.Metadata,
		"created_at":          s.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func sbomPackagesToMaps(packages []domain.SBOMPackage) []map[string]interface{} {
	result := make([]map[string]interface{}, len(packages))
	for i, p := range packages {
		result[i] = map[string]interface{}{
			"id":        p.ID.String(),
			"sbom_id":   p.SBOMID.String(),
			"name":      p.Name,
			"version":   p.Version,
			"ecosystem": p.Ecosystem,
			"license":   p.License,
			"purl":      p.PURL,
			"cpe":       p.CPE,
		}
	}
	return result
}

func buildsToMaps(builds []domain.Build) []map[string]interface{} {
	result := make([]map[string]interface{}, len(builds))
	for i, b := range builds {
		m := map[string]interface{}{
			"id":         b.ID.String(),
			"service_id": b.ServiceID.String(),
			"git_sha":    b.GitSHA,
			"git_ref":    b.GitRef,
			"ci_system":  b.CISystem,
			"ci_run_id":  b.CIRunID,
			"status":     string(b.Status),
			"created_at": b.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if b.LoomJobID != "" {
			m["loom_job_id"] = b.LoomJobID
		}
		if b.StartedAt != nil {
			m["started_at"] = b.StartedAt.Format("2006-01-02T15:04:05Z")
		}
		if b.FinishedAt != nil {
			m["finished_at"] = b.FinishedAt.Format("2006-01-02T15:04:05Z")
		}
		result[i] = m
	}
	return result
}

func statesToMaps(states []domain.EnvironmentServiceState) []map[string]interface{} {
	result := make([]map[string]interface{}, len(states))
	for i, s := range states {
		m := map[string]interface{}{
			"service_id":     s.ServiceID.String(),
			"environment_id": s.EnvironmentID.String(),
			"drift_status":   string(s.DriftStatus),
			"updated_at":     s.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if s.DesiredArtifactID != nil {
			m["desired_artifact_id"] = s.DesiredArtifactID.String()
		}
		if s.DesiredIntentID != nil {
			m["desired_intent_id"] = s.DesiredIntentID.String()
		}
		if s.LastSuccessfulRunID != nil {
			m["last_successful_run_id"] = s.LastSuccessfulRunID.String()
		}
		if s.CurrentObservationID != nil {
			m["current_observation_id"] = s.CurrentObservationID.String()
		}
		if s.LastReconciledAt != nil {
			m["last_reconciled_at"] = s.LastReconciledAt.Format("2006-01-02T15:04:05Z")
		}
		result[i] = m
	}
	return result
}

func intentsToMaps(intents []domain.DeploymentIntent) []map[string]interface{} {
	result := make([]map[string]interface{}, len(intents))
	for i, intent := range intents {
		result[i] = intentToMap(&intent)
	}
	return result
}

func intentToMap(intent *domain.DeploymentIntent) map[string]interface{} {
	m := map[string]interface{}{
		"id":              intent.ID.String(),
		"service_id":      intent.ServiceID.String(),
		"environment_id":  intent.EnvironmentID.String(),
		"artifact_id":     intent.ArtifactID.String(),
		"requested_by":    intent.RequestedBy,
		"source_kind":     string(intent.SourceKind),
		"approval_status": string(intent.ApprovalStatus),
		"status":          string(intent.Status),
		"created_at":      intent.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":      intent.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if intent.SupersedesIntentID != nil {
		m["supersedes_intent_id"] = intent.SupersedesIntentID.String()
	}
	if intent.ApprovedAt != nil {
		m["approved_at"] = intent.ApprovedAt.Format("2006-01-02T15:04:05Z")
	}
	return m
}

func workersToMaps(workers []domain.Worker) []map[string]interface{} {
	result := make([]map[string]interface{}, len(workers))
	for i := range workers {
		result[i] = workerToMap(&workers[i])
	}
	return result
}

func workerToMap(w *domain.Worker) map[string]interface{} {
	m := map[string]interface{}{
		"pubkey":                w.PubKey,
		"name":                  w.Name,
		"architecture":          w.Architecture,
		"max_concurrent_jobs":   w.MaxConcurrentJobs,
		"current_queue_depth":   w.CurrentQueueDepth,
		"software":              w.Software,
		"pricing":               w.Pricing,
		"status":                string(w.Status),
		"scheduling_state":      string(w.SchedulingState),
		"scheduling_note":       w.SchedulingNote,
		"labels":                w.Labels,
		"capabilities":          w.Capabilities,
		"ml_capabilities":       w.MLCapabilities,
		"runtime_target":        w.RuntimeTarget,
		"resources":             w.Resources,
		"accelerators":          w.Accelerators,
		"last_advertisement_at": w.LastAdvertisementAt.Format("2006-01-02T15:04:05Z"),
		"created_at":            w.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":            w.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if w.Description != "" {
		m["description"] = w.Description
	}
	if w.MinDurationSecs > 0 {
		m["min_duration_secs"] = w.MinDurationSecs
	}
	if w.MaxDurationSecs > 0 {
		m["max_duration_secs"] = w.MaxDurationSecs
	}
	if w.Geohash != "" {
		m["geohash"] = w.Geohash
	}
	if len(w.PreferredRelays) > 0 {
		m["preferred_relays"] = w.PreferredRelays
	}
	return m
}

func runsToMaps(runs []domain.DeploymentRun) []map[string]interface{} {
	result := make([]map[string]interface{}, len(runs))
	for i, r := range runs {
		m := map[string]interface{}{
			"id":                   r.ID.String(),
			"deployment_intent_id": r.DeploymentIntentID.String(),
			"status":               string(r.Status),
			"created_at":           r.CreatedAt.Format("2006-01-02T15:04:05Z"),
			"updated_at":           r.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if r.LoomJobID != "" {
			m["loom_job_id"] = r.LoomJobID
		}
		if r.WorkerPubkey != "" {
			m["worker_pubkey"] = r.WorkerPubkey
		}
		if r.WorkerName != "" {
			m["worker_name"] = r.WorkerName
		}
		if r.ExitCode != nil {
			m["exit_code"] = *r.ExitCode
		}
		if r.StartedAt != nil {
			m["started_at"] = r.StartedAt.Format("2006-01-02T15:04:05Z")
		}
		if r.FinishedAt != nil {
			m["finished_at"] = r.FinishedAt.Format("2006-01-02T15:04:05Z")
		}
		result[i] = m
	}
	return result
}

func observationToMap(obs *domain.RuntimeObservation) map[string]interface{} {
	m := map[string]interface{}{
		"id":                    obs.ID.String(),
		"service_id":            obs.ServiceID.String(),
		"environment_id":        obs.EnvironmentID.String(),
		"observed_image_digest": obs.ObservedImageDigest,
		"health_status":         string(obs.HealthStatus),
		"source":                obs.Source,
		"observed_at":           obs.ObservedAt.Format("2006-01-02T15:04:05Z"),
	}
	if obs.ObservedImageRepo != "" {
		m["observed_image_repo"] = obs.ObservedImageRepo
	}
	if obs.ObservedContainerID != "" {
		m["observed_container_id"] = obs.ObservedContainerID
	}
	if obs.ObservedHost != "" {
		m["observed_host"] = obs.ObservedHost
	}
	if obs.ObservedVersion != "" {
		m["observed_version"] = obs.ObservedVersion
	}
	return m
}

func runToMap(r *domain.DeploymentRun) map[string]interface{} {
	m := map[string]interface{}{
		"id":                   r.ID.String(),
		"deployment_intent_id": r.DeploymentIntentID.String(),
		"status":               string(r.Status),
		"created_at":           r.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":           r.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if r.LoomJobID != "" {
		m["loom_job_id"] = r.LoomJobID
	}
	if r.WorkerPubkey != "" {
		m["worker_pubkey"] = r.WorkerPubkey
	}
	if r.WorkerName != "" {
		m["worker_name"] = r.WorkerName
	}
	if r.ExitCode != nil {
		m["exit_code"] = *r.ExitCode
	}
	if r.StartedAt != nil {
		m["started_at"] = r.StartedAt.Format("2006-01-02T15:04:05Z")
	}
	if r.FinishedAt != nil {
		m["finished_at"] = r.FinishedAt.Format("2006-01-02T15:04:05Z")
	}
	return m
}

func runLogsToMap(logs *adapterruntime.RunLogs, stream string) map[string]interface{} {
	m := map[string]interface{}{
		"run_id": logs.RunID.String(),
		"stream": stream,
	}
	if logs.Stdout != "" {
		m["stdout"] = logs.Stdout
	}
	if logs.Stderr != "" {
		m["stderr"] = logs.Stderr
	}
	if stream == "merged" {
		m["logs"] = adapterruntime.MergeLogs(logs.Stdout, logs.Stderr)
	}
	if logs.ExitCode != nil {
		m["exit_code"] = *logs.ExitCode
	}
	if !logs.StartedAt.IsZero() {
		m["started_at"] = logs.StartedAt.Format("2006-01-02T15:04:05Z")
	}
	if logs.Duration != "" {
		m["duration"] = logs.Duration
	}
	return m
}

func costEstimateToMap(estimate *domain.CostEstimate) map[string]interface{} {
	if estimate == nil {
		return nil
	}
	return map[string]interface{}{
		"worker_pubkey":       estimate.WorkerPubkey,
		"worker_name":         estimate.WorkerName,
		"mint_url":            estimate.MintURL,
		"price_per_second":    estimate.PricePerSecond,
		"estimated_secs":      estimate.EstimatedSecs,
		"estimated_cost_sats": estimate.EstimatedCost,
		"unit":                estimate.Unit,
	}
}

func costSummaryToMap(summary *service.CostSummary) map[string]interface{} {
	if summary == nil {
		return nil
	}
	return map[string]interface{}{
		"total_paid_sats":   summary.TotalPaid,
		"total_change_sats": summary.TotalChange,
		"net_cost_sats":     summary.NetCost,
		"payment_count":     summary.PaymentCount,
		"change_count":      summary.ChangeCount,
	}
}

func paymentRecordsToMaps(records []domain.PaymentRecord) []map[string]interface{} {
	result := make([]map[string]interface{}, len(records))
	for i := range records {
		result[i] = paymentRecordToMap(&records[i])
	}
	return result
}

func paymentRecordToMap(rec *domain.PaymentRecord) map[string]interface{} {
	m := map[string]interface{}{
		"id":                rec.ID.String(),
		"deployment_run_id": rec.DeploymentRunID.String(),
		"worker_pubkey":     rec.WorkerPubkey,
		"mint_url":          rec.MintURL,
		"amount_sats":       rec.AmountSats,
		"direction":         string(rec.Direction),
		"status":            string(rec.Status),
		"created_at":        rec.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":        rec.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if rec.TokenHash != "" {
		m["token_hash"] = rec.TokenHash
	}
	if rec.ErrorMessage != "" {
		m["error_message"] = rec.ErrorMessage
	}
	if rec.Metadata != nil {
		m["metadata"] = rec.Metadata
	}
	return m
}

func isTerminalRunStatus(status domain.DeploymentRunStatus) bool {
	switch status {
	case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return true
	default:
		return false
	}
}

func secretsToMaps(secrets []domain.ServiceSecret) []map[string]interface{} {
	result := make([]map[string]interface{}, len(secrets))
	for i, s := range secrets {
		// Convert to SecretRef to strip encrypted value
		ref := s.ToRef()
		m := map[string]interface{}{
			"id":                ref.ID.String(),
			"service_id":        ref.ServiceID.String(),
			"name":              ref.Name,
			"encryption_method": string(ref.EncryptionMethod),
			"version":           ref.Version,
			"created_by":        ref.CreatedBy,
			"created_at":        ref.CreatedAt.Format("2006-01-02T15:04:05Z"),
			"updated_at":        ref.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if ref.EnvironmentID != nil {
			m["environment_id"] = ref.EnvironmentID.String()
		}
		result[i] = m
	}
	return result
}

// --- Policy Handlers ---

func (s *Server) handleListPolicies(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.policies == nil {
		return errorResult("policy tools are not configured"), nil
	}

	// Check for enabled filter
	enabledOnly := false
	if enabled, ok := args["enabled"].(bool); ok {
		enabledOnly = enabled
	}

	// If environment_id is provided, filter by environment
	if envIDStr, ok := args["environment_id"].(string); ok && envIDStr != "" {
		envID, err := uuid.Parse(envIDStr)
		if err != nil {
			return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
		}

		// List all policies and filter by environment
		allPolicies, err := s.policies.ListPolicies(ctx, enabledOnly)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to list policies: %v", err)), nil
		}

		var filteredPolicies []domain.DeploymentPolicy
		for _, p := range allPolicies {
			if p.EnvironmentID != nil && *p.EnvironmentID == envID {
				filteredPolicies = append(filteredPolicies, p)
			}
		}

		result := map[string]interface{}{
			"policies": policiesToMaps(filteredPolicies),
			"total":    len(filteredPolicies),
		}
		return jsonResult(result)
	}

	// List all policies
	policies, err := s.policies.ListPolicies(ctx, enabledOnly)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list policies: %v", err)), nil
	}

	result := map[string]interface{}{
		"policies": policiesToMaps(policies),
		"total":    len(policies),
	}
	return jsonResult(result)
}

func (s *Server) handleGetPolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.policies == nil {
		return errorResult("policy tools are not configured"), nil
	}

	policyIDStr, _ := args["policy_id"].(string)
	if policyIDStr == "" {
		return errorResult("policy_id is required"), nil
	}

	policyID, err := uuid.Parse(policyIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid policy_id: %v", err)), nil
	}

	policy, err := s.policies.GetPolicy(ctx, policyID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get policy: %v", err)), nil
	}
	if policy == nil {
		return errorResult("policy not found"), nil
	}

	return jsonResult(policyToMap(policy))
}

func (s *Server) handleCreatePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.policyCommands == nil {
		return errorResult("policy command publisher is not configured"), nil
	}

	name := strings.TrimSpace(stringArg(args, "name"))
	enforcementStr := strings.TrimSpace(stringArg(args, "enforcement"))
	if name == "" {
		return errorResult("name is required"), nil
	}
	if enforcementStr == "" {
		return errorResult("enforcement is required"), nil
	}
	enforcement := domain.PolicyEnforcement(enforcementStr)
	if enforcement != domain.PolicyEnforcementWarn && enforcement != domain.PolicyEnforcementBlock {
		return errorResult(fmt.Sprintf("invalid enforcement: %s (must be 'warn' or 'block')", enforcementStr)), nil
	}
	envID, errResult := optionalPolicyUUIDArg(args, "environment_id")
	if errResult != nil {
		return errResult, nil
	}
	rules, errResult := policyRulesArg(args)
	if errResult != nil {
		return errResult, nil
	}
	enabled := true
	if enabledVal, ok := args["enabled"].(bool); ok {
		enabled = enabledVal
	}
	receipt, err := s.policyCommands.PublishPolicyCreateRequest(ctx, controlplane.PolicyMutationCommand{Name: name, EnvironmentID: envID, Rules: rules, Enforcement: enforcementStr, Enabled: &enabled, IdempotencyKey: stringArg(args, "idempotency_key")})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish PolicyCreate request: %v", err)), nil
	}
	return jsonResult(receipt)
}

func (s *Server) handleUpdatePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.policyCommands == nil {
		return errorResult("policy command publisher is not configured"), nil
	}
	policyID, err := parseRequiredUUIDArg(args, "policy_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	cmd := controlplane.PolicyMutationCommand{ID: policyID, IdempotencyKey: stringArg(args, "idempotency_key")}
	if name := strings.TrimSpace(stringArg(args, "name")); name != "" {
		cmd.Name = name
	}
	if enforcementStr := strings.TrimSpace(stringArg(args, "enforcement")); enforcementStr != "" {
		enforcement := domain.PolicyEnforcement(enforcementStr)
		if enforcement != domain.PolicyEnforcementWarn && enforcement != domain.PolicyEnforcementBlock {
			return errorResult(fmt.Sprintf("invalid enforcement: %s (must be 'warn' or 'block')", enforcementStr)), nil
		}
		cmd.Enforcement = enforcementStr
	}
	if _, ok := args["environment_id"]; ok {
		envID, errResult := optionalPolicyUUIDArg(args, "environment_id")
		if errResult != nil {
			return errResult, nil
		}
		cmd.EnvironmentID = envID
	}
	if _, ok := args["rules"]; ok {
		rules, errResult := policyRulesArg(args)
		if errResult != nil {
			return errResult, nil
		}
		cmd.Rules = rules
	}
	if enabled, ok := args["enabled"].(bool); ok {
		cmd.Enabled = &enabled
	}
	receipt, err := s.policyCommands.PublishPolicyUpdateRequest(ctx, cmd)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish PolicyUpdate request: %v", err)), nil
	}
	return jsonResult(receipt)
}

func (s *Server) handleDeletePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.policyCommands == nil {
		return errorResult("policy command publisher is not configured"), nil
	}
	policyID, err := parseRequiredUUIDArg(args, "policy_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	receipt, err := s.policyCommands.PublishPolicyDeleteRequest(ctx, controlplane.PolicyMutationCommand{ID: policyID, IdempotencyKey: stringArg(args, "idempotency_key")})
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish PolicyDelete request: %v", err)), nil
	}
	return jsonResult(receipt)
}

func (s *Server) handleEvaluatePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.policyCommands == nil {
		return errorResult("policy command publisher is not configured"), nil
	}
	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	environmentID, err := parseRequiredUUIDArg(args, "environment_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	cmd := controlplane.PolicyMutationCommand{ArtifactID: artifactID, EnvironmentID: &environmentID, IdempotencyKey: stringArg(args, "idempotency_key")}
	if _, ok := args["service_id"]; ok {
		serviceID, errResult := optionalPolicyUUIDArg(args, "service_id")
		if errResult != nil {
			return errResult, nil
		}
		cmd.ServiceID = serviceID
	}
	receipt, err := s.policyCommands.PublishPolicyEvaluateRequest(ctx, cmd)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to publish PolicyEvaluate request: %v", err)), nil
	}
	return jsonResult(receipt)
}

func optionalPolicyUUIDArg(args map[string]interface{}, key string) (*uuid.UUID, *ToolResult) {
	value := strings.TrimSpace(stringArg(args, key))
	if value == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil, errorResult(fmt.Sprintf("invalid %s: %v", key, err))
	}
	return &parsed, nil
}

func policyRulesArg(args map[string]interface{}) ([]domain.PolicyRule, *ToolResult) {
	rulesRaw, ok := args["rules"]
	if !ok {
		return nil, errorResult("rules is required")
	}
	rulesJSON, err := json.Marshal(rulesRaw)
	if err != nil {
		return nil, errorResult(fmt.Sprintf("failed to marshal rules: %v", err))
	}
	var rules []domain.PolicyRule
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		return nil, errorResult(fmt.Sprintf("failed to parse rules: %v", err))
	}
	if len(rules) == 0 {
		return nil, errorResult("at least one rule is required")
	}
	return rules, nil
}

func policiesToMaps(policies []domain.DeploymentPolicy) []map[string]interface{} {
	result := make([]map[string]interface{}, len(policies))
	for i := range policies {
		result[i] = policyToMap(&policies[i])
	}
	return result
}

func policyToMap(p *domain.DeploymentPolicy) map[string]interface{} {
	m := map[string]interface{}{
		"id":          p.ID.String(),
		"name":        p.Name,
		"rules":       p.Rules,
		"enforcement": string(p.Enforcement),
		"enabled":     p.Enabled,
		"created_at":  p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":  p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if p.EnvironmentID != nil {
		m["environment_id"] = p.EnvironmentID.String()
	}
	return m
}

func policyResultsToMaps(results []domain.PolicyResult) []map[string]interface{} {
	output := make([]map[string]interface{}, len(results))
	for i, r := range results {
		m := map[string]interface{}{
			"policy_id":   r.PolicyID.String(),
			"policy_name": r.PolicyName,
			"passed":      r.Passed,
			"enforcement": string(r.Enforcement),
		}
		if len(r.Violations) > 0 {
			violations := make([]map[string]interface{}, len(r.Violations))
			for j, v := range r.Violations {
				violations[j] = map[string]interface{}{
					"rule":    string(v.Rule),
					"message": v.Message,
				}
			}
			m["violations"] = violations
		}
		output[i] = m
	}
	return output
}

// --- Notification Handlers ---

func (s *Server) handleListNotificationChannels(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification channel tools are not configured"), nil
	}

	enabledOnly := false
	if enabled, ok := args["enabled"].(bool); ok {
		enabledOnly = enabled
	}

	channels, err := s.notificationRepo.ListChannels(ctx, enabledOnly)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list notification channels: %v", err)), nil
	}

	result := map[string]interface{}{
		"channels": notificationChannelsToMaps(channels),
		"total":    len(channels),
	}
	return jsonResult(result)
}

func (s *Server) handleGetNotificationChannel(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification channel tools are not configured"), nil
	}

	channelID, err := parseRequiredUUIDArg(args, "channel_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	ch, err := s.notificationRepo.GetChannelByID(ctx, channelID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get notification channel: %v", err)), nil
	}
	if ch == nil {
		return errorResult("notification channel not found"), nil
	}

	return jsonResult(notificationChannelToMap(ch))
}

func (s *Server) handleCreateNotificationChannel(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification channel tools are not configured"), nil
	}

	name, _ := args["name"].(string)
	if name == "" {
		return errorResult("name is required"), nil
	}

	channelType, err := parseNotificationChannelType(args)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	config, err := optionalMapArg(args, "config")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if config == nil {
		return errorResult("config is required"), nil
	}

	eventFilter, err := optionalMapArg(args, "event_filter")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	enabled := true
	if enabledVal, ok := args["enabled"].(bool); ok {
		enabled = enabledVal
	}

	now := time.Now().UTC()
	ch := &domain.NotificationChannel{
		ID:          uuid.New(),
		Name:        name,
		ChannelType: channelType,
		Config:      config,
		EventFilter: eventFilter,
		Enabled:     enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.notificationRepo.CreateChannel(ctx, ch); err != nil {
		return errorResult(fmt.Sprintf("failed to create notification channel: %v", err)), nil
	}

	result := map[string]interface{}{
		"status":     "created",
		"channel_id": ch.ID.String(),
		"channel":    notificationChannelToMap(ch),
	}
	return jsonResult(result)
}

func (s *Server) handleUpdateNotificationChannel(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification channel tools are not configured"), nil
	}

	channelID, err := parseRequiredUUIDArg(args, "channel_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	ch, err := s.notificationRepo.GetChannelByID(ctx, channelID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get notification channel: %v", err)), nil
	}
	if ch == nil {
		return errorResult("notification channel not found"), nil
	}

	if name, ok := args["name"].(string); ok && name != "" {
		ch.Name = name
	}
	if _, ok := args["channel_type"]; ok {
		channelType, err := parseNotificationChannelType(args)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		ch.ChannelType = channelType
	}
	if _, ok := args["config"]; ok {
		config, err := optionalMapArg(args, "config")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		ch.Config = config
	}
	if _, ok := args["event_filter"]; ok {
		eventFilter, err := optionalMapArg(args, "event_filter")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		ch.EventFilter = eventFilter
	}
	if enabled, ok := args["enabled"].(bool); ok {
		ch.Enabled = enabled
	}
	ch.UpdatedAt = time.Now().UTC()

	if err := s.notificationRepo.UpdateChannel(ctx, ch); err != nil {
		return errorResult(fmt.Sprintf("failed to update notification channel: %v", err)), nil
	}

	result := map[string]interface{}{
		"status":     "updated",
		"channel_id": ch.ID.String(),
		"channel":    notificationChannelToMap(ch),
	}
	return jsonResult(result)
}

func (s *Server) handleDeleteNotificationChannel(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification channel tools are not configured"), nil
	}

	channelID, err := parseRequiredUUIDArg(args, "channel_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	ch, err := s.notificationRepo.GetChannelByID(ctx, channelID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get notification channel: %v", err)), nil
	}
	if ch == nil {
		return errorResult("notification channel not found"), nil
	}

	if err := s.notificationRepo.DeleteChannel(ctx, channelID); err != nil {
		return errorResult(fmt.Sprintf("failed to delete notification channel: %v", err)), nil
	}

	result := map[string]interface{}{
		"status":     "deleted",
		"channel_id": channelID.String(),
	}
	return jsonResult(result)
}

func (s *Server) handleTestNotificationChannel(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification channel tools are not configured"), nil
	}
	if s.notificationDisp == nil {
		return errorResult("notification dispatcher is not configured"), nil
	}

	channelID, err := parseRequiredUUIDArg(args, "channel_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	ch, err := s.notificationRepo.GetChannelByID(ctx, channelID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get notification channel: %v", err)), nil
	}
	if ch == nil {
		return errorResult("notification channel not found"), nil
	}
	if !ch.Enabled {
		return errorResult("notification channel is disabled"), nil
	}

	if err := s.notificationDisp.DispatchToChannel(ctx, ch, "test", map[string]any{
		"message":    "This is a test notification from Bahia",
		"channel_id": ch.ID.String(),
	}); err != nil {
		return errorResult(fmt.Sprintf("failed to send test notification: %v", err)), nil
	}

	result := map[string]interface{}{
		"status":     "test_sent",
		"channel_id": ch.ID.String(),
	}
	return jsonResult(result)
}

func (s *Server) handleListNotifications(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification tools are not configured"), nil
	}

	status, _ := args["status"].(string)
	eventType, _ := args["event_type"].(string)
	limit, _ := args["limit"].(float64)
	if limit == 0 {
		limit = 50
	}

	// List recent notifications
	logs, err := s.notificationRepo.ListRecentLogs(ctx, int(limit))
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list notifications: %v", err)), nil
	}

	// Apply filters
	var filtered []domain.NotificationLog
	for _, log := range logs {
		// Filter by status (read = sent, unread = pending/retrying)
		if status != "" {
			if status == "read" && log.Status != domain.NotificationStatusSent {
				continue
			}
			if status == "unread" && log.Status != domain.NotificationStatusPending && log.Status != domain.NotificationStatusRetrying {
				continue
			}
		}

		// Filter by event type
		if eventType != "" && log.EventType != eventType {
			continue
		}

		filtered = append(filtered, log)
	}

	result := map[string]interface{}{
		"notifications": notificationLogsToMaps(filtered),
		"total":         len(filtered),
	}
	return jsonResult(result)
}

func (s *Server) handleGetNotification(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification tools are not configured"), nil
	}

	return errorResult("get notification by ID is not currently supported - repository method not available. Use list_notifications instead."), nil
}

func (s *Server) handleMarkNotificationRead(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification tools are not configured"), nil
	}

	notificationIDStr, _ := args["notification_id"].(string)
	if notificationIDStr == "" {
		return errorResult("notification_id is required"), nil
	}

	notificationID, err := uuid.Parse(notificationIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid notification_id: %v", err)), nil
	}

	// Since we don't have GetLogByID, we need to search through recent logs
	// This is a workaround until the repository interface is extended
	logs, err := s.notificationRepo.ListRecentLogs(ctx, 200)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to find notification: %v", err)), nil
	}

	var notification *domain.NotificationLog
	for i, log := range logs {
		if log.ID == notificationID {
			notification = &logs[i]
			break
		}
	}

	if notification == nil {
		return errorResult("notification not found"), nil
	}

	// Mark as read by updating status to sent
	notification.Status = domain.NotificationStatusSent
	if err := s.notificationRepo.UpdateLog(ctx, notification); err != nil {
		return errorResult(fmt.Sprintf("failed to mark notification as read: %v", err)), nil
	}

	s.logger.Info("notification marked as read", zap.String("notification_id", notificationID.String()))

	result := map[string]interface{}{
		"status":          "marked_read",
		"notification_id": notificationID.String(),
	}
	return jsonResult(result)
}

func (s *Server) handleDismissNotification(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.notificationRepo == nil {
		return errorResult("notification tools are not configured"), nil
	}

	return errorResult("dismiss notification is not supported - notification logs are immutable audit records"), nil
}

func notificationChannelsToMaps(channels []domain.NotificationChannel) []map[string]interface{} {
	result := make([]map[string]interface{}, len(channels))
	for i := range channels {
		result[i] = notificationChannelToMap(&channels[i])
	}
	return result
}

func notificationChannelToMap(ch *domain.NotificationChannel) map[string]interface{} {
	return map[string]interface{}{
		"id":              ch.ID.String(),
		"name":            ch.Name,
		"channel_type":    string(ch.ChannelType),
		"config":          redactNotificationChannelConfig(ch.Config),
		"config_redacted": notificationChannelConfigHasSensitiveKeys(ch.Config),
		"event_filter":    ch.EventFilter,
		"enabled":         ch.Enabled,
		"created_at":      ch.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":      ch.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func redactNotificationChannelConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	redacted := make(map[string]any, len(config))
	for key, value := range config {
		if isSensitiveNotificationConfigKey(key) {
			redacted[key] = "[redacted]"
			continue
		}
		redacted[key] = value
	}
	return redacted
}

func notificationChannelConfigHasSensitiveKeys(config map[string]any) bool {
	for key := range config {
		if isSensitiveNotificationConfigKey(key) {
			return true
		}
	}
	return false
}

func isSensitiveNotificationConfigKey(key string) bool {
	key = strings.ToLower(key)
	if key == "url" || key == "webhook_url" {
		return true
	}
	for _, marker := range []string{"secret", "password", "token", "authorization", "bearer", "credential", "api_key", "private_key", "signing_key"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func parseRequiredUUIDArg(args map[string]interface{}, name string) (uuid.UUID, error) {
	value, _ := args[name].(string)
	if value == "" {
		return uuid.Nil, fmt.Errorf("%s is required", name)
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %v", name, err)
	}
	return id, nil
}

func optionalIntArg(args map[string]interface{}, name string, defaultValue int) int {
	switch v := args[name].(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			return int(parsed)
		}
	}
	return defaultValue
}

func sbomDataArg(args map[string]interface{}) ([]byte, error) {
	for _, name := range []string{"sbom_data", "document", "sbom"} {
		raw, ok := args[name]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case string:
			value = strings.TrimSpace(value)
			if value == "" {
				return nil, fmt.Errorf("%s is required", name)
			}
			return []byte(value), nil
		case map[string]interface{}, []interface{}:
			data, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("marshaling %s: %w", name, err)
			}
			return data, nil
		default:
			return nil, fmt.Errorf("%s must be a JSON string or object", name)
		}
	}
	return nil, fmt.Errorf("sbom_data is required")
}

func parseNotificationChannelType(args map[string]interface{}) (domain.ChannelType, error) {
	channelTypeStr, _ := args["channel_type"].(string)
	if channelTypeStr == "" {
		return "", fmt.Errorf("channel_type is required")
	}
	channelType := domain.ChannelType(channelTypeStr)
	if channelType != domain.ChannelTypeWebhook && channelType != domain.ChannelTypeNostrDM {
		return "", fmt.Errorf("channel_type must be 'webhook' or 'nostr_dm'")
	}
	return channelType, nil
}

func optionalMapArg(args map[string]interface{}, name string) (map[string]any, error) {
	value, ok := args[name]
	if !ok || value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed, nil
	default:
		return nil, fmt.Errorf("%s must be an object", name)
	}
}

func notificationLogsToMaps(logs []domain.NotificationLog) []map[string]interface{} {
	result := make([]map[string]interface{}, len(logs))
	for i, log := range logs {
		result[i] = map[string]interface{}{
			"id":         log.ID.String(),
			"channel_id": log.ChannelID.String(),
			"event_type": log.EventType,
			"payload":    log.Payload,
			"status":     string(log.Status),
			"attempts":   log.Attempts,
			"created_at": log.CreatedAt.Format("2006-01-02T15:04:05Z"),
			"updated_at": log.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if log.LastError != "" {
			result[i]["last_error"] = log.LastError
		}
	}
	return result
}
