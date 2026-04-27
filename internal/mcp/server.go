// Package mcp provides an MCP (Model Context Protocol) server for Bahia operations.
// This allows AI agents to interact with the deployment registry programmatically.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Server provides an MCP-compatible interface for Bahia operations.
// It exposes deployment registry functionality as MCP tools.
type Server struct {
	registry *service.RegistryService
	logger   *zap.Logger
}

// Config holds MCP server configuration.
type Config struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// NewServer creates a new MCP server for Bahia.
func NewServer(registry *service.RegistryService, logger *zap.Logger) *Server {
	return &Server{
		registry: registry,
		logger:   logger.With(zap.String("component", "mcp-server")),
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
	// Environment operations
	case "bahia_list_environments":
		return s.handleListEnvironments(ctx, arguments)
	case "bahia_get_environment":
		return s.handleGetEnvironment(ctx, arguments)
	case "bahia_create_environment":
		return s.handleCreateEnvironment(ctx, arguments)
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
	return map[string]interface{}{
		"id":              env.ID.String(),
		"name":            env.Name,
		"protected":       env.Protected,
		"deploy_strategy": env.DeployStrategy,
		"created_at":      env.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"updated_at":      env.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
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
