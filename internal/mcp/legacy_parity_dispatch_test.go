package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

// legacyCallTool is a test-only oracle for parity fixtures. Production
// dispatch has no repository-backed path for these canonical families.
func (s *Server) legacyCallTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	switch name {
	case "bahia_list_services":
		return s.handleListServices(ctx, args)
	case "bahia_get_service":
		return s.handleGetService(ctx, args)
	case "bahia_list_environments":
		return s.handleListEnvironments(ctx, args)
	case "bahia_get_environment":
		return s.handleGetEnvironment(ctx, args)
	case "bahia_get_deployment_status":
		return s.handleGetDeploymentStatus(ctx, args)
	case "bahia_llm_list_routes":
		return s.handleLLMListRoutes(ctx, args)
	case "bahia_list_artifacts":
		return s.handleListArtifacts(ctx, args)
	case "bahia_get_artifact":
		return s.handleGetArtifact(ctx, args)
	case "bahia_list_builds":
		return s.handleListBuilds(ctx, args)
	case "bahia_get_build":
		return s.handleGetBuild(ctx, args)
	case "bahia_list_states":
		return s.handleListStates(ctx, args)
	case "bahia_list_drifted":
		return s.handleListDrifted(ctx, args)
	case "bahia_list_intents":
		return s.handleListIntents(ctx, args)
	case "bahia_list_runs":
		return s.handleListRuns(ctx, args)
	case "bahia_get_run":
		return s.handleGetRun(ctx, args)
	case "bahia_list_secrets":
		return s.handleListSecrets(ctx, args)
	case "bahia_list_workers":
		return s.handleListWorkers(ctx, args)
	case "bahia_get_worker":
		return s.handleGetWorker(ctx, args)
	case "bahia_get_worker_pricing":
		return s.handleGetWorkerPricing(ctx, args)
	case "bahia_estimate_cost":
		return s.handleEstimateCost(ctx, args)
	case "bahia_get_run_cost":
		return s.handleGetRunCost(ctx, args)
	case "bahia_get_payment_history":
		return s.handleGetPaymentHistory(ctx, args)
	case "bahia_get_intent":
		return s.handleGetIntent(ctx, args)
	case "bahia_list_policies":
		return s.handleListPolicies(ctx, args)
	case "bahia_get_policy":
		return s.handleGetPolicy(ctx, args)
	case "bahia_list_notification_channels":
		return s.handleListNotificationChannels(ctx, args)
	case "bahia_get_notification_channel":
		return s.handleGetNotificationChannel(ctx, args)
	case "bahia_dns_list_endpoints", "bahia_assistant_dns_list_endpoints":
		return s.handleDNSListEndpoints(ctx, args)
	case "bahia_dns_list_drift", "bahia_assistant_dns_list_drift":
		return s.handleDNSListDrift(ctx, args)
	case "bahia_fips_list_mesh_nodes":
		return jsonResult(map[string]any{"nodes": []fipsMeshNode{}, "total": 0})
	case "bahia_fips_mesh_status":
		return jsonResult(map[string]any{"total_nodes": 0, "healthy_projected_nodes": 0, "health_counts": map[string]int{}, "projection_counts": map[string]int{}, "nodes": []fipsMeshNode{}})
	case "bahia_ml_list_state":
		return s.handleMLListState(ctx, args)
	case "bahia_ml_get_state":
		return s.handleMLGetState(ctx, args)
	case "bahia_ml_get_provenance":
		return s.handleMLGetProvenance(ctx, args)
	case "bahia_worker_get_assignments":
		return s.handleWorkerGetAssignments(ctx, args)
	case "bahia_worker_list_assignments":
		return s.handleWorkerListAssignments(ctx, args)
	case "bahia_worker_get_drain_status":
		return s.handleWorkerGetDrainStatus(ctx, args)
	case "bahia_worker_list_drain_status":
		return s.handleWorkerListDrainStatus(ctx, args)
	case "bahia_worker_preview_eligibility":
		return s.legacyWorkerPreviewEligibility(ctx, args)
	case "bahia_package_list":
		return s.handlePackageList(ctx, args)
	case "bahia_package_get":
		return s.handlePackageGet(ctx, args)
	case "bahia_get_run_logs":
		return s.legacyGetRunLogs(ctx, args)
	default:
		if isBackupToolName(name) {
			return s.legacyBackupReadTool(ctx, name, args)
		}
		return errorResult("no legacy parity oracle for " + name), nil
	}
}

func (s *Server) legacyBackupReadTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	switch backupToolBaseName(name) {
	case "list_backup_repositories":
		return s.handleListBackupRepositories(ctx, args)
	case "list_backup_policies":
		return s.handleListBackupPolicies(ctx, args)
	case "list_backup_recipes":
		return s.handleListBackupRecipes(ctx, args)
	case "list_backup_definitions":
		return s.handleListBackupDefinitions(ctx, args)
	case "list_backup_runs":
		return s.handleListBackupRuns(ctx, args)
	case "list_backup_restores":
		return s.handleListBackupRestores(ctx, args)
	case "list_backup_retention_runs":
		return s.handleListBackupRetentionRuns(ctx, args)
	case "inspect_backup_repository":
		return s.handleInspectBackupRepository(ctx, args)
	case "inspect_backup_policy":
		return s.handleInspectBackupPolicy(ctx, args)
	case "inspect_backup_recipe":
		return s.handleInspectBackupRecipe(ctx, args)
	case "inspect_backup_run":
		return s.handleInspectBackupRun(ctx, args)
	case "inspect_backup_restore":
		return s.handleInspectBackupRestore(ctx, args)
	case "inspect_backup_retention_run":
		return s.handleInspectBackupRetentionRun(ctx, args)
	case "inspect_backup_definition":
		return s.handleInspectBackupDefinition(ctx, args)
	default:
		return errorResult("no legacy backup parity oracle for " + name), nil
	}
}

func (s *Server) legacyGetRunLogs(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
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

func (s *Server) legacyWorkerPreviewEligibility(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	models := legacyFor(s).WorkerReadModels
	if models == nil {
		return errorResult("worker read model service is not configured"), nil
	}
	previewID := strings.TrimSpace(stringArg(args, "preview_id"))
	workloadType := strings.TrimSpace(stringArg(args, "workload_type"))
	policy := anyMapFromArg(args["policy"])
	if workloadType == "" || workloadType == "worker_policy" || workloadType == "service" || workloadType == "generic" {
		env, err := s.legacyEnvironmentForWorkerPreview(ctx, args, policy)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		preview, err := models.PreviewWorkerPolicyEligibility(ctx, previewID, env, policy)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to preview worker eligibility: %v", err)), nil
		}
		return jsonResult(map[string]interface{}{"eligibility_preview": preview, "read_model_kind": controlplane.KindCASControlState, "read_model_topic": kinds.WorkerEligibilityTopic})
	}
	req := mlPlacementRequestFromArgs(args)
	preview, err := models.PreviewMLEligibility(ctx, previewID, req, policy)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to preview ML worker eligibility: %v", err)), nil
	}
	return jsonResult(map[string]interface{}{"eligibility_preview": preview, "read_model_kind": controlplane.KindCASControlState, "read_model_topic": kinds.WorkerEligibilityTopic})
}

func (s *Server) legacyEnvironmentForWorkerPreview(ctx context.Context, args map[string]interface{}, policy map[string]any) (*domain.Environment, error) {
	if envIDRaw := strings.TrimSpace(stringArg(args, "environment_id")); envIDRaw != "" {
		if s.registry == nil {
			return nil, fmt.Errorf("registry is not configured")
		}
		envID, err := uuid.Parse(envIDRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid environment_id: %w", err)
		}
		env, err := s.registry.GetEnvironment(ctx, envID)
		if err != nil {
			return nil, fmt.Errorf("failed to get environment: %w", err)
		}
		if env == nil {
			return nil, fmt.Errorf("environment not found")
		}
		return env, nil
	}
	return &domain.Environment{ID: uuid.New(), RuntimeConfig: map[string]any{"worker_policy": policy}, LoomWorkerSelector: anyMapFromArg(args["worker_selector"])}, nil
}
