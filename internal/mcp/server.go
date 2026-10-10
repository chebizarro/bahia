// Package mcp provides an MCP (Model Context Protocol) server for Bahia operations.
// This allows AI agents to interact with the deployment registry programmatically.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/pkg/client"
	"go.uber.org/zap"
)

// Server provides an MCP-compatible interface for Bahia operations.
// It exposes deployment registry functionality as MCP tools.
type Server struct {
	stateStore         StateEventStore
	intentProc         *controlplane.IntentProcessor
	servicePubkey      string
	confidentialReader ConfidentialStateReader
	registry           *service.RegistryService
	llmRegistry        *service.LLMRegistryService
	logger             *zap.Logger
	secretsRepo        repository.SecretRepository       // optional: for secret management tools
	encryptor          *secrets.Encryptor                // optional: for secret encryption/decryption
	notificationRepo   repository.NotificationRepository // optional: for notification tools
	notificationDisp   *notifications.Dispatcher         // optional: for notification testing
	logService         *adapterruntime.LogService        // optional: for deployment run log tools
	toolProvisioning   repository.ToolProvisioningRepository
	authorizedPubkeys  []string
	rbac               *auth.RBAC
	outbox             OutboxReader // optional: for outbox inspection tool
}

// Config holds MCP server configuration.
type Config struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// ServerDeps holds MCP dependencies. StateStore and ServicePubkey are required.
type ServerDeps struct {
	StateStore             StateEventStore
	IntentProcessor        *controlplane.IntentProcessor
	ServicePubkey          string
	ConfidentialReader     ConfidentialStateReader
	SecretsRepo            repository.SecretRepository
	Encryptor              *secrets.Encryptor
	NotificationRepo       repository.NotificationRepository
	NotificationDispatcher *notifications.Dispatcher
	LogService             *adapterruntime.LogService
	ToolProvisioning       repository.ToolProvisioningRepository
	LLMRegistry            *service.LLMRegistryService
	// AuthorizedPubkeys is the explicit operator allowlist for external MCP callers.
	// An empty allowlist denies all non-system callers.
	AuthorizedPubkeys []string
	// RBAC is required for tenant-scoped authorization such as secret access.
	RBAC *auth.RBAC
	// Outbox is optional: exposes daemon outbox counts and failed entries to MCP callers.
	Outbox OutboxReader
}

// NewServerWithOptionsChecked requires the local event store used by MCP reads.
func NewServerWithOptionsChecked(registry *service.RegistryService, logger *zap.Logger, deps ServerDeps) (*Server, error) {
	if deps.StateStore == nil || nilStateStore(deps.StateStore) {
		return nil, fmt.Errorf("MCP state event store is required")
	}
	if strings.TrimSpace(deps.ServicePubkey) == "" {
		return nil, fmt.Errorf("MCP service pubkey is required for state reads")
	}
	return &Server{
		stateStore:         deps.StateStore,
		intentProc:         deps.IntentProcessor,
		servicePubkey:      deps.ServicePubkey,
		confidentialReader: deps.ConfidentialReader,
		registry:           registry,
		llmRegistry:        deps.LLMRegistry,
		logger:             logger,
		secretsRepo:        deps.SecretsRepo,
		encryptor:          deps.Encryptor,
		notificationRepo:   deps.NotificationRepo,
		notificationDisp:   deps.NotificationDispatcher,
		logService:         deps.LogService,
		toolProvisioning:   deps.ToolProvisioning,
		authorizedPubkeys:  normalizePubkeys(deps.AuthorizedPubkeys),
		rbac:               deps.RBAC,
		outbox:             deps.Outbox,
	}, nil
}

func nilStateStore(store StateEventStore) bool {
	v := reflect.ValueOf(store)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
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
			Description: "Publish a signer-first ContextVM/Nostr service/create request and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": mcpCreateEntityIDSchema("service"),
					"org_id": map[string]interface{}{
						"type":        "string",
						"description": "Organization UUID (optional; defaults by policy)",
					},
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
					"repository": map[string]interface{}{
						"type":        "object",
						"description": "Structured repository metadata including repo_coordinate, clone_url, and ci workflow",
					},
					"default_branch": map[string]interface{}{
						"type":        "string",
						"description": "Default source branch",
					},
					"runtime_type": map[string]interface{}{
						"type":        "string",
						"description": "Target runtime type",
						"enum":        []string{"docker", "compose", "kubernetes", "podman", "vm-firecracker", "vm-qemu"},
						"default":     "docker",
					},
					"managed_runtime_config": map[string]interface{}{
						"type":        "object",
						"description": "Managed runtime configuration for Bahia-owned deployment",
					},
					"idempotency_key": map[string]interface{}{
						"type":        "string",
						"description": "Optional idempotency key",
					},
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "Calling agent identifier for audit tags",
					},
				},
				"required": []string{"name", "artifact_repo"},
			},
		},
		{
			Name:        "bahia_update_service",
			Description: "Publish a signer-first ContextVM/Nostr service/update request and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"service_id": map[string]interface{}{
						"type":        "string",
						"description": "Service UUID to update",
					},
					"org_id": map[string]interface{}{
						"type":        "string",
						"description": "Organization UUID to assign to an unresolved service",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "New service name (optional)",
					},
					"repo_url": map[string]interface{}{
						"type":        "string",
						"description": "New source code repository URL (optional)",
					},
					"repository": map[string]interface{}{
						"type":        "object",
						"description": "Structured repository metadata including repo_coordinate, clone_url, and ci workflow",
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
					"managed_runtime_config": map[string]interface{}{
						"type":        "object",
						"description": "Managed runtime configuration for Bahia-owned deployment",
					},
					"idempotency_key": map[string]interface{}{
						"type":        "string",
						"description": "Optional idempotency key",
					},
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "Calling agent identifier for audit tags",
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
			Description: "Publish a signer-first ContextVM/Nostr environment/create request and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": mcpCreateEntityIDSchema("environment"),
					"org_id": map[string]interface{}{
						"type":        "string",
						"description": "Organization UUID that owns the environment",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Unique name for the environment",
					},
					"loom_worker_selector": map[string]interface{}{
						"type":        "object",
						"description": "Loom worker selector criteria (optional)",
					},
					"runtime_config": map[string]interface{}{
						"type":        "object",
						"description": "Environment runtime configuration (optional)",
					},
					"reconcile_mode": map[string]interface{}{
						"type":        "string",
						"description": "Default reconcile mode (optional)",
						"enum":        []string{"observe_only", "auto_apply", "approval_required", "disabled"},
					},
					"idempotency_key": map[string]interface{}{
						"type":        "string",
						"description": "Optional idempotency key",
					},
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "Calling agent identifier for audit tags",
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
				"required": []string{"org_id", "name"},
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
					"id":                       mcpCreateEntityIDSchema("LLM route"),
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
			Description: "Register an artifact through the in-process artifact/register intent pipeline",
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
					"idempotency_key": map[string]interface{}{"type": "string", "description": "Stable retry key; defaults to _meta.progressToken"},
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
			Description: "Request signature verification for an artifact through a kind-30900 intent",
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
			Description: "Queue an SPDX or CycloneDX SBOM import for an artifact through a kind-30900 intent (inline limit 360 KiB)",
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
			Description: "Publish a signed ContextVM policy/create (kind 25910) request and return relay/follow correlation metadata",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": mcpCreateEntityIDSchema("policy"),
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
			Description: "Publish a signed ContextVM policy/update (kind 25910) request and return relay/follow correlation metadata",
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
			Description: "Publish a signed ContextVM policy/delete (kind 25910) request and return relay/follow correlation metadata",
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
			Description: "Evaluate deployment policies via an intent; follow the bounded kind-30315 status for the decision",
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
			Description: "Apply a tool approval through a kind-30900 intent",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"intent_id": map[string]interface{}{"type": "string", "description": "Intent UUID"}, "reason": map[string]interface{}{"type": "string", "description": "Approval reason"}, "idempotency_key": map[string]interface{}{"type": "string", "description": "Optional Nostr d tag for idempotency/correlation"}}, "required": []string{"intent_id", "reason"}},
		},
		{
			Name:        "bahia_tool_provision_reject",
			Description: "Apply a tool rejection through a kind-30900 intent",
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
			Description: "Request a channel test through a kind-30900 intent",
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
			Description: "Get a recent notification by ID from canonical state",
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
	tools = append(tools, registryIntentToolDefinitions()...)
	tools = append(tools, assistantAsyncToolDefinitions()...)
	tools = append(tools, dnsToolDefinitions()...)
	tools = append(tools, fipsToolDefinitions()...)
	tools = append(tools, workerToolDefinitions()...)
	tools = append(tools, packageToolDefinitions()...)
	tools = append(tools, backupToolDefinitions()...)
	tools = append(tools, docsToolDefinitions()...)
	return describeIntentWriteTools(append(tools, outboxToolDefinitions()...))
}

// CallTool handles an MCP tool call.
// InvokeTool exposes the in-process tool path used by the assistant orchestrator.
func (s *Server) InvokeTool(ctx context.Context, name string, arguments map[string]interface{}) (*ToolResult, error) {
	return s.CallTool(ctx, name, arguments)
}

func signerFirstMCPMutationUnavailable(toolName, method string) *ToolResult {
	return errorResult(fmt.Sprintf("%s is not available as a direct registry mutation; publish a signed ContextVM/Nostr %s command with an operator signer instead", toolName, method))
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

func (s *Server) authorizeServicePermission(ctx context.Context, serviceID uuid.UUID, permission domain.Permission, resource string) *ToolResult {
	principal := auth.GetPrincipal(ctx)
	if principal != nil && principal.Method == auth.MethodSystem && principal.HasRole(string(domain.RoleAdmin)) {
		return nil
	}
	if principal == nil || !principal.IsAuthenticated() {
		return errorResult("authentication required")
	}
	if s.rbac == nil {
		return errorResult(fmt.Sprintf("%s authorization is not configured", resource))
	}

	var svc *domain.Service
	var err error
	if permission == domain.PermReadServices {
		var record *stateRecord
		record, err = s.readStateOne(ctx, nostrAdapter.KindServiceRegistry, "id", serviceID.String())
		if err == nil && record != nil {
			svc, err = client.DecodeService(record.Event)
		}
	} else if s.registry != nil {
		// Write authorization goes through the intent path.
		svc, err = s.registry.GetService(ctx, serviceID)
	}
	if err != nil || svc == nil || svc.OrgID == uuid.Nil {
		if resource == "secret" {
			return errorResult("secret owner not found")
		}
		return errorResult("service not found")
	}
	if err := s.rbac.CheckPermission(ctx, principal, svc.OrgID, permission); err != nil {
		message := "tenant service access rejected"
		if resource == "secret" {
			message = "tenant secret access rejected"
		}
		s.logger.Warn(message,
			zap.String("resource", resource),
			zap.String("service_id", serviceID.String()),
			zap.String("org_id", svc.OrgID.String()),
			zap.String("subject", principal.Subject),
			zap.String("permission", string(permission)),
		)
		return errorResult("access denied")
	}
	return nil
}

func (s *Server) authorizeSecretPermission(ctx context.Context, serviceID uuid.UUID, permission domain.Permission) *ToolResult {
	return s.authorizeServicePermission(ctx, serviceID, permission, "secret")
}

func (s *Server) authorizeBuildPermission(ctx context.Context, buildID uuid.UUID, permission domain.Permission) (*domain.Build, *ToolResult) {
	principal := auth.GetPrincipal(ctx)
	if principal == nil || !principal.IsAuthenticated() {
		return nil, errorResult("authentication required")
	}
	systemAdmin := principal.Method == auth.MethodSystem && principal.HasRole(string(domain.RoleAdmin))
	if s.registry == nil || (!systemAdmin && s.rbac == nil) {
		return nil, errorResult("service authorization is not configured")
	}
	build, err := s.registry.GetBuild(ctx, buildID)
	if err != nil {
		return nil, errorResult(fmt.Sprintf("failed to get build: %v", err))
	}
	if build == nil {
		return nil, errorResult("build not found")
	}
	if denied := s.authorizeServicePermission(ctx, build.ServiceID, permission, "service"); denied != nil {
		return nil, denied
	}
	return build, nil
}

func (s *Server) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*ToolResult, error) {
	s.logger.Info("tool call", zap.String("tool", name))
	if isRegistryIntentTool(name) && s.intentProc == nil {
		return intentWriteError("error", "", "", "intent processor is not configured"), nil
	}
	if isFinalIntentWriteTool(name) && s.intentProc == nil {
		if denied := s.authorizeToolCall(ctx, name); denied != nil {
			return denied, nil
		}
		return intentWriteError("error", "", "", "intent processor is not configured"), nil
	}
	// Intent-backed writes authenticate their Nostr actor and authorize against
	// TrustSet inside ProcessInProcess. The MCP operator allowlist must
	// not deny an otherwise authorized org member before that check runs.
	if s.intentProc != nil && isIntentWriteTool(name) {
		result, _ := s.callIntentWrite(ctx, name, arguments)
		return result, nil
	}
	if denied := s.authorizeToolCall(ctx, name); denied != nil {
		return denied, nil
	}
	if result, handled := s.callStoreReadTool(ctx, name, arguments); handled {
		return result, nil
	}
	if isBackupToolName(name) {
		return s.handleBackupTool(ctx, name, arguments)
	}

	switch name {
	// Service operations
	case "bahia_create_service":
		return s.handleCreateService(ctx, arguments)
	case "bahia_update_service":
		return s.handleUpdateService(ctx, arguments)
	// Environment operations
	case "bahia_create_environment":
		return s.handleCreateEnvironment(ctx, arguments)
	case "bahia_update_environment":
		return s.handleUpdateEnvironment(ctx, arguments)
	// Deployment operations
	case "bahia_deploy":
		return s.handleDeploy(ctx, arguments)
	case "bahia_rollback":
		return s.handleRollback(ctx, arguments)
	case "bahia_approve_deployment":
		return s.handleApproveDeployment(ctx, arguments)
	case "bahia_reject_deployment":
		return s.handleRejectDeployment(ctx, arguments)
	case "bahia_assistant_service_deploy", "bahia_assistant_service_rollback", "bahia_assistant_llm_deploy", "bahia_assistant_llm_approve_deployment", "bahia_assistant_llm_rollback", "bahia_assistant_ml_deploy", "bahia_assistant_ml_approve_deployment", "bahia_assistant_ml_rollback":
		return s.handleAssistantAsyncTool(ctx, name, arguments)
	case "bahia_fips_list_mesh_nodes":
		return s.handleFIPSListMeshNodes(ctx, arguments)
	case "bahia_fips_mesh_status":
		return s.handleFIPSMeshStatus(ctx, arguments)

	// LLM registry operations
	case "bahia_llm_create_route":
		return s.handleLLMCreateRoute(ctx, arguments)
	case "bahia_llm_update_route":
		return s.handleLLMUpdateRoute(ctx, arguments)
	case "bahia_llm_register_release":
		return s.handleLLMRegisterRelease(ctx, arguments)
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
	case "bahia_register_artifact":
		return s.handleRegisterArtifact(ctx, arguments)
	// Observability operations
	case "bahia_create_run":
		return s.handleCreateRun(ctx, arguments)
	case "bahia_get_run_logs":
		return s.handleGetRunLogs(ctx, arguments)
	case "bahia_complete_run":
		return s.handleCompleteRun(ctx, arguments)
	// Secret operations
	case "bahia_create_secret":
		return s.handleCreateSecret(ctx, arguments)
	case "bahia_update_secret":
		return s.handleUpdateSecret(ctx, arguments)
	case "bahia_delete_secret":
		return s.handleDeleteSecret(ctx, arguments)
	// Policy operations
	case "bahia_create_policy":
		return s.handleCreatePolicy(ctx, arguments)
	case "bahia_update_policy":
		return s.handleUpdatePolicy(ctx, arguments)
	case "bahia_delete_policy":
		return s.handleDeletePolicy(ctx, arguments)
	case "bahia_evaluate_policy":
		return s.handleEvaluatePolicy(ctx, arguments)
	// Worker operations
	case "bahia_worker_cordon", "bahia_worker_uncordon", "bahia_worker_drain", "bahia_worker_undrain", "bahia_worker_maintenance_enter", "bahia_worker_maintenance_exit":
		return s.handleWorkerLifecycleCommand(ctx, name, arguments)
	case "bahia_worker_labels_update":
		return s.handleWorkerLabelsUpdate(ctx, arguments)
	case "bahia_worker_preview_eligibility":
		return s.handleWorkerPreviewEligibility(ctx, arguments)
	// Payment operations
	// Intent alias operations
	case "bahia_create_intent":
		return s.handleDeploy(ctx, arguments) // alias
	case "bahia_approve_intent":
		return s.handleApproveDeployment(ctx, arguments) // alias
	case "bahia_reject_intent":
		return s.handleRejectDeployment(ctx, arguments) // alias
	// Tool provisioning operations
	case "bahia_tool_provision_request":
		return s.handleToolProvisionRequest(ctx, arguments)
	case "bahia_tool_denylist_add":
		return s.handleToolDenylistAdd(ctx, arguments)
	case "bahia_tool_denylist_remove":
		return s.handleToolDenylistRemove(ctx, arguments)
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
	// Notification channel operations
	case "bahia_create_notification_channel":
		return s.handleCreateNotificationChannel(ctx, arguments)
	case "bahia_update_notification_channel":
		return s.handleUpdateNotificationChannel(ctx, arguments)
	case "bahia_delete_notification_channel":
		return s.handleDeleteNotificationChannel(ctx, arguments)
	// Notification log operations
	case "bahia_mark_notification_read":
		return s.handleMarkNotificationRead(ctx, arguments)
	case "bahia_dismiss_notification":
		return s.handleDismissNotification(ctx, arguments)
	case "bahia_docs_read":
		return s.handleDocsRead(ctx, arguments)
	case "bahia_docs_list":
		return s.handleDocsList(ctx, arguments)
	// Outbox inspection
	case "bahia_outbox_status":
		return s.handleOutboxStatus(ctx, arguments)
	case "bahia_outbox_retry":
		return s.handleOutboxRetry(ctx, arguments)
	default:
		return errorResult(fmt.Sprintf("unknown tool: %s", name)), nil
	}
}

// --- Tool Handlers ---

func (s *Server) handleCreateService(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_create_service", args)
}

// handleCreateEnvironment publishes a signer-first environment/create request
// under a client-minted environment id.
func (s *Server) handleCreateEnvironment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_create_environment", args)
}

// mcpCreateEntityIDSchema is the input schema of a create tool's optional
// client-minted entity id.
func mcpCreateEntityIDSchema(entity string) map[string]interface{} {
	return map[string]interface{}{
		"type": "string",
		"description": "Optional client-minted " + entity + " id: a canonical lowercase UUIDv7 (or v4). Omit to mint one; the result echoes it. " +
			"To retry a create, pass the returned id with the same arguments: the same id and content replays the create, the same id with different content is rejected (JSON-RPC -32010).",
	}
}

// mcpCreateEntityID returns a create tool's entity id: the
// caller's `id` argument, which must be a canonical UUIDv7 (or v4), or a
// freshly minted UUIDv7. The id is part of the derived idempotency key, so a
// retry that passes back the returned id (and the same arguments) is replayed
// by the control plane, and one without it is a new create, never a request
// fingerprint conflict.
func mcpCreateEntityID(args map[string]interface{}) (uuid.UUID, *ToolResult) {
	raw, _ := args["id"].(string)
	id, _, err := domain.ResolveCreateEntityID(raw)
	if err != nil {
		return uuid.Nil, errorResult(fmt.Sprintf("invalid id: %v", err))
	}
	return id, nil
}

func (s *Server) handleUpdateService(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_update_service", args)
}

func (s *Server) handleUpdateEnvironment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_update_environment", args)
}

func (s *Server) handleDeploy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_deploy", args)
}

func (s *Server) handleRollback(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_rollback", args)
}

func (s *Server) handleApproveDeployment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_approve_deployment", args)
}

func (s *Server) handleRejectDeployment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_reject_deployment", args)
}

func (s *Server) requireLLMRegistry() (*service.LLMRegistryService, *ToolResult) {
	if s.llmRegistry == nil {
		return nil, errorResult("LLM registry is not configured")
	}
	return s.llmRegistry, nil
}

func (s *Server) handleLLMCreateRoute(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_llm_create_route", args)
}

func (s *Server) handleLLMUpdateRoute(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_llm_update_route", args)
}

func (s *Server) handleLLMRegisterRelease(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_llm_register_release", args)
}

func (s *Server) handleLLMDeploy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_llm_deploy", args)
}

func (s *Server) handleLLMApproveDeployment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_llm_approve_deployment", args)
}

func (s *Server) handleLLMRejectDeployment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_llm_reject_deployment", args)
}

func (s *Server) handleLLMRollback(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_llm_rollback", args)
}

func (s *Server) handleDeleteService(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_delete_service", args)
}

func (s *Server) handleDeleteEnvironment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_delete_environment", args)
}

func (s *Server) handleRegisterArtifact(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_register_artifact", args)
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

func (s *Server) handleGetRunLogs(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	runIDStr, _ := args["run_id"].(string)

	runID, err := uuid.Parse(runIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid run_id: %v", err)), nil
	}
	if s.logService == nil {
		return errorResult("run log tools are not configured"), nil
	}

	record, err := s.readStateOne(ctx, nostrAdapter.KindDeploymentRunRegistry, "id", runID.String())
	var run *domain.DeploymentRun
	if err == nil && record != nil {
		run = &domain.DeploymentRun{ID: runID, Status: domain.DeploymentRunStatus(fmt.Sprint(record.Fields["status"])), StdoutRef: stringFromRecord(record.Fields, "stdout_ref"), StderrRef: stringFromRecord(record.Fields, "stderr_ref")}
		if exit, ok := record.Fields["exit_code"].(float64); ok {
			code := int(exit)
			run.ExitCode = &code
		}
		if started, ok := recordTime(record.Fields, "started_at"); ok {
			run.StartedAt = &started
		}
		if finished, ok := recordTime(record.Fields, "finished_at"); ok {
			run.FinishedAt = &finished
		}
	}
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
	// This tool only has the legacy AES writer. In particular, never replace a
	// v2 payload while retaining its method: readers would treat the legacy
	// ciphertext as identity/version-bound data-key ciphertext.
	switch existing.EncryptionMethod {
	case domain.EncryptionAES256, domain.EncryptionNIP44:
		// A supported legacy row may be replaced with legacy AES ciphertext.
	case domain.EncryptionAES256V2:
		return errorResult("versioned secret updates require the fenced v2 writer"), nil
	default:
		return errorResult("unsupported stored secret encryption method"), nil
	}

	// Encrypt the new value
	encryptedValue, err := s.encryptor.Encrypt(value, domain.EncryptionAES256)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to encrypt secret: %v", err)), nil
	}

	// Update the secret
	existing.EncryptedValue = encryptedValue
	existing.EncryptionMethod = domain.EncryptionAES256
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

func (s *Server) handleToolProvisionRequest(_ context.Context, _ map[string]interface{}) (*ToolResult, error) {
	return errorResult("tool provisioning request paused: operator-signed canonical request acceptance and restart-safe effect commit are unavailable"), nil
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

// --- Helper Functions ---

func decodeToolArgs(args map[string]interface{}, out interface{}) error {
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
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

func optionalStringPointerArg(args map[string]interface{}, name string) *string {
	value, ok := args[name].(string)
	if !ok {
		return nil
	}
	return &value
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

func (s *Server) handleCreatePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_create_policy", args)
}

func (s *Server) handleUpdatePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_update_policy", args)
}

func (s *Server) handleDeletePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_delete_policy", args)
}

func (s *Server) handleEvaluatePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	return s.invokeIntentWrite(ctx, "bahia_evaluate_policy", args)
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

// --- Notification Handlers ---

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
