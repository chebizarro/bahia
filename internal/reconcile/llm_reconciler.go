package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// LLMRouteReconciler observes LLM backend/gateway state and repairs gateway drift.
type LLMRouteReconciler struct {
	registry          *service.LLMRegistryService
	environments      repository.EnvironmentRepository
	provisioners      llmadapter.ProvisionerResolver
	gateway           llmadapter.GatewayRouteManager
	defaultGatewayRef string
	interval          time.Duration
	logger            *zap.Logger
}

func NewLLMRouteReconciler(registry *service.LLMRegistryService, envs repository.EnvironmentRepository, provisioners llmadapter.ProvisionerResolver, gateway llmadapter.GatewayRouteManager, defaultGatewayRef string, interval time.Duration, logger *zap.Logger) *LLMRouteReconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LLMRouteReconciler{registry: registry, environments: envs, provisioners: provisioners, gateway: gateway, defaultGatewayRef: defaultGatewayRef, interval: interval, logger: logger}
}

func (r *LLMRouteReconciler) Name() string { return "llm-route-reconciler" }

func (r *LLMRouteReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if err := r.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
			r.logger.Warn("LLM route reconcile failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *LLMRouteReconciler) ReconcileOnce(ctx context.Context) error {
	states, err := r.registry.ListAllRouteStates(ctx)
	if err != nil {
		return err
	}
	for i := range states {
		if err := r.reconcileState(ctx, &states[i]); err != nil {
			r.logger.Warn("LLM route state reconcile failed", zap.String("route_id", states[i].RouteID.String()), zap.String("environment_id", states[i].EnvironmentID.String()), zap.Error(err))
		}
	}
	return nil
}

func (r *LLMRouteReconciler) reconcileState(ctx context.Context, state *domain.LLMRouteState) error {
	if state == nil || state.DesiredReleaseID == nil || state.ActiveRunID == nil {
		return nil
	}
	route, err := r.registry.GetRoute(ctx, state.RouteID)
	if err != nil || route == nil {
		return err
	}
	release, err := r.registry.GetRelease(ctx, *state.DesiredReleaseID)
	if err != nil || release == nil {
		return err
	}
	run, err := r.registry.GetDeploymentRun(ctx, *state.ActiveRunID)
	if err != nil || run == nil {
		return err
	}
	env, err := r.environments.GetByID(ctx, state.EnvironmentID)
	if err != nil || env == nil {
		return err
	}
	provisioner, err := r.provisioners.Resolve(run.BackendKind)
	if err != nil {
		return err
	}
	req := llmadapter.ProvisionCandidateRequest{Route: route, Release: release, Environment: env, Run: run, BackendKind: run.BackendKind, Worker: service.WorkerFromRunMetadata(run), TargetName: metadataString(run.Metadata, "target_name")}
	backendObs, err := provisioner.Observe(ctx, req)
	if err != nil {
		backendObs = &llmadapter.BackendObservation{BackendKind: run.BackendKind, BackendEndpoint: run.BackendEndpoint, HealthStatus: domain.HealthStatusUnhealthy, Source: "llm_reconciler", Metadata: map[string]any{"observe_error": err.Error()}}
	}
	gatewayRef := r.gatewayRef(env)
	gatewayObs, err := r.gateway.GetRoute(ctx, gatewayRef, route.Name)
	if err != nil {
		gatewayObs = &llmadapter.GatewayRouteObservation{RouteName: route.Name, Status: domain.GatewayRouteStatusError, TargetURL: run.BackendEndpoint, Metadata: map[string]any{"observe_error": err.Error()}}
	}
	spec := service.BuildLLMGatewayRouteSpec(route, backendObs.BackendEndpoint)
	obs := observationFromReconcile(route, release, run, state.EnvironmentID, backendObs, gatewayObs, spec.ManagedConfigHash())
	if err := r.registry.RecordObservation(ctx, obs); err != nil {
		return err
	}
	if backendObs.HealthStatus == domain.HealthStatusHealthy && (gatewayObs.Status == domain.GatewayRouteStatusMissing || gatewayObs.GatewayConfigHash != spec.ManagedConfigHash() || gatewayObs.TargetURL != spec.TargetURL) {
		repaired, err := r.gateway.UpsertRoute(ctx, gatewayRef, spec)
		if err != nil {
			return fmt.Errorf("repair gateway route: %w", err)
		}
		return r.registry.RecordObservation(ctx, observationFromReconcile(route, release, run, state.EnvironmentID, backendObs, repaired, spec.ManagedConfigHash()))
	}
	return nil
}

func observationFromReconcile(route *domain.LLMRoute, release *domain.LLMRelease, run *domain.LLMDeploymentRun, envID uuid.UUID, backendObs *llmadapter.BackendObservation, gatewayObs *llmadapter.GatewayRouteObservation, desiredHash string) *domain.LLMRouteObservation {
	gatewayStatus := domain.GatewayRouteStatusUnknown
	gatewayTarget := ""
	gatewayHash := desiredHash
	var gatewayMeta map[string]any
	if gatewayObs != nil {
		gatewayStatus = gatewayObs.Status
		gatewayTarget = gatewayObs.TargetURL
		if gatewayObs.GatewayConfigHash != "" {
			gatewayHash = gatewayObs.GatewayConfigHash
		}
		gatewayMeta = gatewayObs.Metadata
	}
	backendKind := run.BackendKind
	backendEndpoint := run.BackendEndpoint
	backendHealth := domain.HealthStatusUnknown
	var backendMeta map[string]any
	if backendObs != nil {
		backendKind = backendObs.BackendKind
		backendEndpoint = backendObs.BackendEndpoint
		backendHealth = backendObs.HealthStatus
		backendMeta = backendObs.Metadata
	}
	return &domain.LLMRouteObservation{RouteID: route.ID, EnvironmentID: envID, ObservedReleaseID: &release.ID, ObservedRunID: &run.ID, BackendKind: backendKind, BackendEndpoint: backendEndpoint, BackendHealth: backendHealth, GatewayStatus: gatewayStatus, GatewayTarget: gatewayTarget, GatewayConfigHash: gatewayHash, Source: "llm_reconciler", Metadata: map[string]any{"backend": backendMeta, "gateway": gatewayMeta}}
}

func (r *LLMRouteReconciler) gatewayRef(env *domain.Environment) string {
	if env != nil && env.RuntimeConfig != nil {
		if ref, ok := env.RuntimeConfig["llm_gateway_ref"].(string); ok && strings.TrimSpace(ref) != "" {
			return strings.TrimSpace(ref)
		}
	}
	return strings.TrimSpace(r.defaultGatewayRef)
}

func metadataString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if v, ok := meta[key].(string); ok {
		return v
	}
	return ""
}
