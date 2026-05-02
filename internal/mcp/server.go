// Package mcp provides an MCP (Model Context Protocol) server for Bahia operations.
// This allows AI agents to interact with the deployment registry programmatically.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Server provides an MCP-compatible interface for Bahia operations.
// It exposes deployment registry functionality as MCP tools.
type Server struct {
	registry           *service.RegistryService
	logger             *zap.Logger
	secretsRepo        repository.SecretRepository   // optional: for secret management tools
	encryptor          *secrets.Encryptor            // optional: for secret encryption/decryption
	policies           *service.PolicyService        // optional: for policy management tools
	notificationRepo   repository.NotificationRepository // optional: for notification tools
	notificationDisp   *notifications.Dispatcher     // optional: for notification testing
	workers            repository.WorkerRepository   // optional: for worker management tools
}

// Config holds MCP server configuration.
type Config struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// ServerDeps holds optional dependencies for the MCP server.
type ServerDeps struct {
	SecretsRepo          repository.SecretRepository
	Encryptor            *secrets.Encryptor
	Policies             *service.PolicyService
	NotificationRepo     repository.NotificationRepository
	NotificationDispatcher *notifications.Dispatcher
	Workers              repository.WorkerRepository
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
		registry:         registry,
		logger:           logger,
		secretsRepo:      deps.SecretsRepo,
		encryptor:        deps.Encryptor,
		policies:         deps.Policies,
		notificationRepo: deps.NotificationRepo,
		notificationDisp: deps.NotificationDispatcher,
		workers:          deps.Workers,
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
	return []Tool{
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
			Description: "Register a new service in the deployment registry",
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
						"enum":        []string{"docker", "compose", "kubernetes", "podman"},
						"default":     "docker",
					},
				},
				"required": []string{"name", "artifact_repo"},
			},
		},
		{
			Name:        "bahia_update_service",
			Description: "Update an existing service's configuration",
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
						"enum":        []string{"docker", "compose", "kubernetes", "podman"},
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
			Description: "Create a new deployment environment",
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
			Description: "Update an existing environment's configuration",
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
		{
			Name:        "bahia_delete_service",
			Description: "Delete a service from the registry",
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
			Description: "Delete an environment from the registry",
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
			Description: "Create a new deployment policy",
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
				},
				"required": []string{"name", "rules", "enforcement"},
			},
		},
		{
			Name:        "bahia_update_policy",
			Description: "Update an existing deployment policy",
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
				},
				"required": []string{"policy_id"},
			},
		},
		{
			Name:        "bahia_delete_policy",
			Description: "Delete a deployment policy",
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
			Name:        "bahia_evaluate_policy",
			Description: "Evaluate all applicable policies against an artifact for a given environment",
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
		// Notification operations
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
}

// CallTool handles an MCP tool call.
func (s *Server) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*ToolResult, error) {
	s.logger.Info("tool call", zap.String("tool", name))

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
	case "bahia_delete_service":
		return s.handleDeleteService(ctx, arguments)
	case "bahia_delete_environment":
		return s.handleDeleteEnvironment(ctx, arguments)
	// Artifact operations
	case "bahia_list_artifacts":
		return s.handleListArtifacts(ctx, arguments)
	case "bahia_get_artifact":
		return s.handleGetArtifact(ctx, arguments)
	// Build operations
	case "bahia_list_builds":
		return s.handleListBuilds(ctx, arguments)
	case "bahia_get_build":
		return s.handleGetBuild(ctx, arguments)
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
	// Intent alias operations
	case "bahia_create_intent":
		return s.handleDeploy(ctx, arguments) // alias
	case "bahia_get_intent":
		return s.handleGetIntent(ctx, arguments)
	case "bahia_approve_intent":
		return s.handleApproveDeployment(ctx, arguments) // alias
	case "bahia_reject_intent":
		return s.handleRejectDeployment(ctx, arguments) // alias
	// Notification operations
	case "bahia_list_notifications":
		return s.handleListNotifications(ctx, arguments)
	case "bahia_get_notification":
		return s.handleGetNotification(ctx, arguments)
	case "bahia_mark_notification_read":
		return s.handleMarkNotificationRead(ctx, arguments)
	case "bahia_dismiss_notification":
		return s.handleDismissNotification(ctx, arguments)
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
	name, _ := args["name"].(string)
	artifactRepo, _ := args["artifact_repo"].(string)
	repoURL, _ := args["repo_url"].(string)
	runtimeType, _ := args["runtime_type"].(string)

	if name == "" {
		return errorResult("name is required"), nil
	}
	if artifactRepo == "" {
		return errorResult("artifact_repo is required"), nil
	}

	if runtimeType == "" {
		runtimeType = "docker"
	}

	// Validate runtime type
	if err := domain.ValidateRuntimeType(domain.RuntimeType(runtimeType)); err != nil {
		return errorResult(fmt.Sprintf("invalid runtime_type: %v", err)), nil
	}

	svc := &domain.Service{
		ID:           uuid.New(),
		Name:         name,
		ArtifactRepo: artifactRepo,
		RepoURL:      repoURL,
		RuntimeType:  domain.RuntimeType(runtimeType),
	}

	if err := s.registry.CreateService(ctx, svc); err != nil {
		return errorResult(fmt.Sprintf("failed to create service: %v", err)), nil
	}

	s.logger.Info("service created", zap.String("service_id", svc.ID.String()), zap.String("name", name))

	result := map[string]interface{}{
		"status":     "created",
		"service_id": svc.ID.String(),
		"name":       name,
	}
	return jsonResult(result)
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
	name, _ := args["name"].(string)
	protected, _ := args["protected"].(bool)
	deployStrategy, _ := args["deploy_strategy"].(string)

	if name == "" {
		return errorResult("name is required"), nil
	}

	if deployStrategy == "" {
		deployStrategy = "replace"
	}

	env := &domain.Environment{
		ID:             uuid.New(),
		Name:           name,
		Protected:      protected,
		DeployStrategy: domain.DeployStrategy(deployStrategy),
	}

	if err := s.registry.CreateEnvironment(ctx, env); err != nil {
		return errorResult(fmt.Sprintf("failed to create environment: %v", err)), nil
	}

	s.logger.Info("environment created", zap.String("environment_id", env.ID.String()), zap.String("name", name))

	result := map[string]interface{}{
		"status":         "created",
		"environment_id": env.ID.String(),
		"name":           name,
	}
	return jsonResult(result)
}

func (s *Server) handleUpdateService(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	if serviceIDStr == "" {
		return errorResult("service_id is required"), nil
	}

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	// Get existing service
	svc, err := s.registry.GetService(ctx, serviceID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get service: %v", err)), nil
	}
	if svc == nil {
		return errorResult("service not found"), nil
	}

	// Update fields if provided
	if name, ok := args["name"].(string); ok && name != "" {
		svc.Name = name
	}
	if repoURL, ok := args["repo_url"].(string); ok {
		svc.RepoURL = repoURL
		svc.Repository = nil
	}
	if artifactRepo, ok := args["artifact_repo"].(string); ok && artifactRepo != "" {
		svc.ArtifactRepo = artifactRepo
	}
	if defaultBranch, ok := args["default_branch"].(string); ok {
		svc.DefaultBranch = defaultBranch
	}
	if runtimeType, ok := args["runtime_type"].(string); ok && runtimeType != "" {
		rt := domain.RuntimeType(runtimeType)
		// Validate runtime type
		if rt != domain.RuntimeTypeDocker && rt != domain.RuntimeTypeCompose &&
			rt != domain.RuntimeTypeK8s && rt != domain.RuntimeTypePodman {
			return errorResult(fmt.Sprintf("invalid runtime_type: %s (must be docker, compose, kubernetes, or podman)", runtimeType)), nil
		}
		svc.RuntimeType = rt
	}

	svc.UpdatedAt = time.Now()

	if err := s.registry.UpdateService(ctx, svc); err != nil {
		return errorResult(fmt.Sprintf("failed to update service: %v", err)), nil
	}

	s.logger.Info("service updated",
		zap.String("service_id", svc.ID.String()),
		zap.String("name", svc.Name),
	)

	result := map[string]interface{}{
		"status":     "updated",
		"service_id": svc.ID.String(),
		"service":    serviceToMap(svc),
	}
	return jsonResult(result)
}

func (s *Server) handleUpdateEnvironment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	envIDStr, _ := args["environment_id"].(string)
	if envIDStr == "" {
		return errorResult("environment_id is required"), nil
	}

	envID, err := uuid.Parse(envIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
	}

	// Get existing environment
	env, err := s.registry.GetEnvironment(ctx, envID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get environment: %v", err)), nil
	}
	if env == nil {
		return errorResult("environment not found"), nil
	}

	// Update fields if provided
	if name, ok := args["name"].(string); ok && name != "" {
		env.Name = name
	}
	if loomWorkerSelector, ok := args["loom_worker_selector"].(map[string]interface{}); ok {
		env.LoomWorkerSelector = loomWorkerSelector
	}
	if runtimeConfig, ok := args["runtime_config"].(map[string]interface{}); ok {
		env.RuntimeConfig = runtimeConfig
	}
	if deployStrategy, ok := args["deploy_strategy"].(string); ok && deployStrategy != "" {
		ds := domain.DeployStrategy(deployStrategy)
		// Validate deploy strategy
		if ds != domain.DeployStrategyReplace && ds != domain.DeployStrategyBlueGreen &&
		ds != domain.DeployStrategyCanary {
			return errorResult(fmt.Sprintf("invalid deploy_strategy: %s (must be replace, blue_green, or canary)", deployStrategy)), nil
		}
		env.DeployStrategy = ds
	}
	if protected, ok := args["protected"].(bool); ok {
		env.Protected = protected
	}

	env.UpdatedAt = time.Now()

	if err := s.registry.UpdateEnvironment(ctx, env); err != nil {
		return errorResult(fmt.Sprintf("failed to update environment: %v", err)), nil
	}

	s.logger.Info("environment updated",
		zap.String("environment_id", env.ID.String()),
		zap.String("name", env.Name),
	)

	result := map[string]interface{}{
		"status":         "updated",
		"environment_id": env.ID.String(),
		"environment":    environmentToMap(env),
	}
	return jsonResult(result)
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

	// Validate that service, environment, and artifact exist
	if _, err := s.registry.GetService(ctx, serviceID); err != nil {
		return errorResult(fmt.Sprintf("service not found: %v", err)), nil
	}
	if _, err := s.registry.GetEnvironment(ctx, envID); err != nil {
		return errorResult(fmt.Sprintf("environment not found: %v", err)), nil
	}
	if _, err := s.registry.GetArtifact(ctx, artifactID); err != nil {
		return errorResult(fmt.Sprintf("artifact not found: %v", err)), nil
	}

	intent := &domain.DeploymentIntent{
		ID:            uuid.New(),
		ServiceID:     serviceID,
		EnvironmentID: envID,
		ArtifactID:    artifactID,
		RequestedBy:   requestedBy,
		SourceKind:    domain.SourceKindManual,
	}

	if err := s.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		return errorResult(fmt.Sprintf("failed to create deployment intent: %v", err)), nil
	}

	s.logger.Info("deployment intent created",
		zap.String("intent_id", intent.ID.String()),
		zap.String("service_id", serviceID.String()),
		zap.String("environment_id", envID.String()),
	)

	result := map[string]interface{}{
		"status":         "submitted",
		"intent_id":      intent.ID.String(),
		"service_id":     serviceID.String(),
		"environment_id": envID.String(),
		"artifact_id":    artifactID.String(),
		"message":        "Deployment intent created. Use bahia_get_deployment_status to track progress.",
	}
	return jsonResult(result)
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

	intent, err := s.registry.Rollback(ctx, serviceID, envID, requestedBy)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to rollback: %v", err)), nil
	}

	s.logger.Info("rollback initiated",
		zap.String("intent_id", intent.ID.String()),
		zap.String("service_id", serviceID.String()),
		zap.String("environment_id", envID.String()),
	)

	result := map[string]interface{}{
		"status":         "submitted",
		"intent_id":      intent.ID.String(),
		"service_id":     serviceID.String(),
		"environment_id": envID.String(),
		"message":        "Rollback intent created",
	}
	return jsonResult(result)
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

	if err := s.registry.ApproveDeploymentIntent(ctx, intentID); err != nil {
		return errorResult(fmt.Sprintf("failed to approve deployment: %v", err)), nil
	}

	s.logger.Info("deployment approved", zap.String("intent_id", intentID.String()))

	result := map[string]interface{}{
		"status":    "approved",
		"intent_id": intentID.String(),
		"message":   "Deployment intent approved and queued for execution",
	}
	return jsonResult(result)
}

func (s *Server) handleRejectDeployment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	intentIDStr, _ := args["intent_id"].(string)

	intentID, err := uuid.Parse(intentIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid intent_id: %v", err)), nil
	}

	if err := s.registry.RejectDeploymentIntent(ctx, intentID); err != nil {
		return errorResult(fmt.Sprintf("failed to reject deployment: %v", err)), nil
	}

	s.logger.Info("deployment rejected", zap.String("intent_id", intentID.String()))

	result := map[string]interface{}{
		"status":    "rejected",
		"intent_id": intentID.String(),
		"message":   "Deployment intent rejected",
	}
	return jsonResult(result)
}

func (s *Server) handleDeleteService(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	serviceIDStr, _ := args["service_id"].(string)
	force, _ := args["force"].(bool)

	serviceID, err := uuid.Parse(serviceIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid service_id: %v", err)), nil
	}

	if err := s.registry.DeleteService(ctx, serviceID, force); err != nil {
		return errorResult(fmt.Sprintf("failed to delete service: %v", err)), nil
	}

	s.logger.Info("service deleted", zap.String("service_id", serviceID.String()))

	result := map[string]interface{}{
		"status":     "deleted",
		"service_id": serviceID.String(),
	}
	return jsonResult(result)
}

func (s *Server) handleDeleteEnvironment(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	envIDStr, _ := args["environment_id"].(string)
	force, _ := args["force"].(bool)

	envID, err := uuid.Parse(envIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
	}

	if err := s.registry.DeleteEnvironment(ctx, envID, force); err != nil {
		return errorResult(fmt.Sprintf("failed to delete environment: %v", err)), nil
	}

	s.logger.Info("environment deleted", zap.String("environment_id", envID.String()))

	result := map[string]interface{}{
		"status":         "deleted",
		"environment_id": envID.String(),
	}
	return jsonResult(result)
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

// --- Helper Functions ---

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
		"pubkey":               w.PubKey,
		"name":                 w.Name,
		"architecture":         w.Architecture,
		"max_concurrent_jobs":  w.MaxConcurrentJobs,
		"current_queue_depth":  w.CurrentQueueDepth,
		"software":             w.Software,
		"pricing":              w.Pricing,
		"status":               string(w.Status),
		"last_advertisement_at": w.LastAdvertisementAt.Format("2006-01-02T15:04:05Z"),
		"created_at":           w.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":           w.UpdatedAt.Format("2006-01-02T15:04:05Z"),
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
	if s.policies == nil {
		return errorResult("policy tools are not configured"), nil
	}

	name, _ := args["name"].(string)
	enforcementStr, _ := args["enforcement"].(string)

	if name == "" {
		return errorResult("name is required"), nil
	}
	if enforcementStr == "" {
		return errorResult("enforcement is required"), nil
	}

	// Validate enforcement
	enforcement := domain.PolicyEnforcement(enforcementStr)
	if enforcement != domain.PolicyEnforcementWarn && enforcement != domain.PolicyEnforcementBlock {
		return errorResult(fmt.Sprintf("invalid enforcement: %s (must be 'warn' or 'block')", enforcementStr)), nil
	}

	// Parse environment_id if provided
	var envID *uuid.UUID
	if envIDStr, ok := args["environment_id"].(string); ok && envIDStr != "" {
		parsedEnvID, err := uuid.Parse(envIDStr)
		if err != nil {
			return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
		}
		envID = &parsedEnvID
	}

	// Parse rules
	rulesRaw, ok := args["rules"]
	if !ok {
		return errorResult("rules is required"), nil
	}

	// Convert rules via JSON marshal/unmarshal
	rulesJSON, err := json.Marshal(rulesRaw)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal rules: %v", err)), nil
	}

	var rules []domain.PolicyRule
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		return errorResult(fmt.Sprintf("failed to parse rules: %v", err)), nil
	}

	// Parse enabled flag (default: true)
	enabled := true
	if enabledVal, ok := args["enabled"].(bool); ok {
		enabled = enabledVal
	}

	policy := &domain.DeploymentPolicy{
		ID:            uuid.New(),
		Name:          name,
		EnvironmentID: envID,
		Rules:         rules,
		Enforcement:   enforcement,
		Enabled:       enabled,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := s.policies.CreatePolicy(ctx, policy); err != nil {
		return errorResult(fmt.Sprintf("failed to create policy: %v", err)), nil
	}

	s.logger.Info("policy created",
		zap.String("policy_id", policy.ID.String()),
		zap.String("name", name),
		zap.String("enforcement", enforcementStr),
	)

	result := map[string]interface{}{
		"status":    "created",
		"policy_id": policy.ID.String(),
		"name":      name,
		"message":   "Policy created successfully",
	}
	return jsonResult(result)
}

func (s *Server) handleUpdatePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
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

	// Get existing policy
	policy, err := s.policies.GetPolicy(ctx, policyID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get policy: %v", err)), nil
	}
	if policy == nil {
		return errorResult("policy not found"), nil
	}

	// Update fields if provided
	if name, ok := args["name"].(string); ok && name != "" {
		policy.Name = name
	}

	if enforcementStr, ok := args["enforcement"].(string); ok && enforcementStr != "" {
		enforcement := domain.PolicyEnforcement(enforcementStr)
		if enforcement != domain.PolicyEnforcementWarn && enforcement != domain.PolicyEnforcementBlock {
			return errorResult(fmt.Sprintf("invalid enforcement: %s (must be 'warn' or 'block')", enforcementStr)), nil
		}
		policy.Enforcement = enforcement
	}

	if envIDStr, ok := args["environment_id"].(string); ok {
		if envIDStr == "" {
			policy.EnvironmentID = nil
		} else {
			parsedEnvID, err := uuid.Parse(envIDStr)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
			}
			policy.EnvironmentID = &parsedEnvID
		}
	}

	if rulesRaw, ok := args["rules"]; ok {
		rulesJSON, err := json.Marshal(rulesRaw)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to marshal rules: %v", err)), nil
		}

		var rules []domain.PolicyRule
		if err := json.Unmarshal(rulesJSON, &rules); err != nil {
			return errorResult(fmt.Sprintf("failed to parse rules: %v", err)), nil
		}
		policy.Rules = rules
	}

	if enabled, ok := args["enabled"].(bool); ok {
		policy.Enabled = enabled
	}

	policy.UpdatedAt = time.Now()

	if err := s.policies.UpdatePolicy(ctx, policy); err != nil {
		return errorResult(fmt.Sprintf("failed to update policy: %v", err)), nil
	}

	s.logger.Info("policy updated",
		zap.String("policy_id", policy.ID.String()),
		zap.String("name", policy.Name),
	)

	result := map[string]interface{}{
		"status":    "updated",
		"policy_id": policy.ID.String(),
		"message":   "Policy updated successfully",
	}
	return jsonResult(result)
}

func (s *Server) handleDeletePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
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

	if err := s.policies.DeletePolicy(ctx, policyID); err != nil {
		return errorResult(fmt.Sprintf("failed to delete policy: %v", err)), nil
	}

	s.logger.Info("policy deleted", zap.String("policy_id", policyID.String()))

	result := map[string]interface{}{
		"status":    "deleted",
		"policy_id": policyID.String(),
	}
	return jsonResult(result)
}

func (s *Server) handleEvaluatePolicy(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if s.policies == nil {
		return errorResult("policy tools are not configured"), nil
	}

	artifactIDStr, _ := args["artifact_id"].(string)
	environmentIDStr, _ := args["environment_id"].(string)

	if artifactIDStr == "" {
		return errorResult("artifact_id is required"), nil
	}
	if environmentIDStr == "" {
		return errorResult("environment_id is required"), nil
	}

	artifactID, err := uuid.Parse(artifactIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid artifact_id: %v", err)), nil
	}

	environmentID, err := uuid.Parse(environmentIDStr)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid environment_id: %v", err)), nil
	}

	evaluation, err := s.policies.Evaluate(ctx, artifactID, environmentID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to evaluate policies: %v", err)), nil
	}

	result := map[string]interface{}{
		"allowed":  evaluation.Allowed,
		"warnings": evaluation.Warnings,
		"blockers": evaluation.Blockers,
		"results":  policyResultsToMaps(evaluation.Results),
	}
	return jsonResult(result)
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
