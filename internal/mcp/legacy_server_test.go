package mcp

import (
	"context"
	"fmt"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

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
	return jsonResult(map[string]interface{}{"routes": out, "total": len(out), "registry_kind": nostrpool.KindLLMRouteRegistry})
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
	if denied := s.authorizeServicePermission(ctx, serviceID, domain.PermReadServices, "service"); denied != nil {
		return denied, nil
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

	build, denied := s.authorizeBuildPermission(ctx, buildID, domain.PermReadServices)
	if denied != nil {
		return denied, nil
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

func (s *Server) handleListWorkers(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).Workers == nil {
		return errorResult("worker tools are not configured"), nil
	}

	capability, _ := args["capability"].(string)
	available, hasAvailable := args["available"].(bool)
	limit := 50
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	// List all workers (status filter in repository)
	workers, err := legacyFor(s).Workers.List(ctx, "", limit)
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
	if legacyFor(s).Workers == nil {
		return errorResult("worker tools are not configured"), nil
	}

	pubkey, _ := args["pubkey"].(string)
	if pubkey == "" {
		return errorResult("pubkey is required"), nil
	}

	worker, err := legacyFor(s).Workers.GetByPubKey(ctx, pubkey)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get worker: %v", err)), nil
	}
	if worker == nil {
		return errorResult("worker not found"), nil
	}

	return jsonResult(workerToMap(worker))
}

func (s *Server) handleGetWorkerPricing(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).Workers == nil {
		return errorResult("worker tools are not configured"), nil
	}

	pubkey, _ := args["pubkey"].(string)
	if pubkey == "" {
		return errorResult("pubkey is required"), nil
	}

	worker, err := legacyFor(s).Workers.GetByPubKey(ctx, pubkey)
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

func (s *Server) handleGetRunCost(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).Payments == nil {
		return errorResult("payment tools are not configured"), nil
	}

	runID, err := parseRequiredUUIDArg(args, "run_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	summary, err := legacyFor(s).Payments.GetRunCostSummary(ctx, runID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get run cost summary: %v", err)), nil
	}

	payments, err := legacyFor(s).Payments.GetRunPayments(ctx, runID)
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
	if legacyFor(s).Payments == nil {
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

	records, err := legacyFor(s).Payments.GetPaymentHistory(ctx, workerPubkey, limit)
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

func (s *Server) handleListPolicies(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if legacyFor(s).Policies == nil {
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
		allPolicies, err := legacyFor(s).Policies.ListPolicies(ctx, enabledOnly)
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
	policies, err := legacyFor(s).Policies.ListPolicies(ctx, enabledOnly)
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
	if legacyFor(s).Policies == nil {
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

	policy, err := legacyFor(s).Policies.GetPolicy(ctx, policyID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get policy: %v", err)), nil
	}
	if policy == nil {
		return errorResult("policy not found"), nil
	}

	return jsonResult(policyToMap(policy))
}

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
