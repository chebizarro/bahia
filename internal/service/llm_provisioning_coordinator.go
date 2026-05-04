package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// LLMProvisioningResponder publishes optional out-of-band lifecycle replies for Nostr-originated LLM intents.
type LLMProvisioningResponder interface {
	PublishStatus(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step, message string) error
	PublishResult(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, status, message string) error
	PublishError(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step string, cause error) error
}

// LLMProvisioningCoordinator drains DB-backed LLM runs and promotes healthy backends into the gateway.
type LLMProvisioningCoordinator struct {
	registry          *LLMRegistryService
	environments      repository.EnvironmentRepository
	runs              repository.LLMDeploymentRunRepository
	placement         *LLMPlacementService
	provisioners      llmadapter.ProvisionerResolver
	gateway           llmadapter.GatewayRouteManager
	gate              *LLMPromotionGate
	defaultGatewayRef string
	responder         LLMProvisioningResponder
	pollInterval      time.Duration
	staleRunTimeout   time.Duration
	logger            *zap.Logger
}

type LLMProvisioningCoordinatorOption func(*LLMProvisioningCoordinator)

func WithLLMCoordinatorIntervals(pollInterval, staleRunTimeout time.Duration) LLMProvisioningCoordinatorOption {
	return func(c *LLMProvisioningCoordinator) {
		if pollInterval > 0 {
			c.pollInterval = pollInterval
		}
		if staleRunTimeout > 0 {
			c.staleRunTimeout = staleRunTimeout
		}
	}
}

func WithLLMProvisioningResponder(responder LLMProvisioningResponder) LLMProvisioningCoordinatorOption {
	return func(c *LLMProvisioningCoordinator) { c.responder = responder }
}

func NewLLMProvisioningCoordinator(registry *LLMRegistryService, envs repository.EnvironmentRepository, runs repository.LLMDeploymentRunRepository, placement *LLMPlacementService, provisioners llmadapter.ProvisionerResolver, gateway llmadapter.GatewayRouteManager, defaultGatewayRef string, logger *zap.Logger, opts ...LLMProvisioningCoordinatorOption) *LLMProvisioningCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	c := &LLMProvisioningCoordinator{registry: registry, environments: envs, runs: runs, placement: placement, provisioners: provisioners, gateway: gateway, gate: NewLLMPromotionGate(), defaultGatewayRef: defaultGatewayRef, pollInterval: 5 * time.Second, staleRunTimeout: 15 * time.Minute, logger: logger}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *LLMProvisioningCoordinator) Name() string { return "llm-provisioning-coordinator" }

func (c *LLMProvisioningCoordinator) Run(ctx context.Context) error {
	if c == nil || c.registry == nil || c.runs == nil {
		return nil
	}
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		if err := c.ProcessOnce(ctx); err != nil && ctx.Err() == nil {
			c.logger.Warn("LLM provisioning coordinator tick failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ProcessOnce performs one queue/create/claim/process cycle. It is exposed for focused tests.
func (c *LLMProvisioningCoordinator) ProcessOnce(ctx context.Context) error {
	if c.staleRunTimeout > 0 {
		if n, err := c.runs.RequeueStaleRunning(ctx, c.staleRunTimeout); err != nil {
			return err
		} else if n > 0 {
			c.logger.Warn("requeued stale LLM runs", zap.Int("count", n))
		}
	}
	if _, err := c.runs.EnsureQueuedRunForNextReadyIntent(ctx); err != nil {
		return err
	}
	run, err := c.runs.ClaimNextQueuedRun(ctx)
	if err != nil || run == nil {
		return err
	}
	return c.processRun(ctx, run)
}

func (c *LLMProvisioningCoordinator) processRun(ctx context.Context, run *domain.LLMDeploymentRun) error {
	_ = c.registry.MarkDeploymentRunCreated(ctx, run)
	intent, err := c.registry.GetDeploymentIntent(ctx, run.DeploymentIntentID)
	if err != nil {
		return c.failRun(ctx, run, nil, fmt.Errorf("load intent: %w", err), nil, llmadapter.ProvisionCandidateRequest{})
	}
	if intent == nil {
		return c.failRun(ctx, run, nil, fmt.Errorf("intent %s not found", run.DeploymentIntentID), nil, llmadapter.ProvisionCandidateRequest{})
	}
	route, release, env, err := c.registry.loadRouteReleaseEnvironment(ctx, intent.RouteID, intent.ReleaseID, intent.EnvironmentID)
	if err != nil {
		return c.failRun(ctx, run, intent, err, nil, llmadapter.ProvisionCandidateRequest{})
	}
	candidate, err := c.placement.SelectCandidate(ctx, route, release, env)
	if err != nil {
		return c.failRun(ctx, run, intent, err, nil, llmadapter.ProvisionCandidateRequest{})
	}
	provisioner, err := c.provisioners.Resolve(candidate.BackendKind)
	if err != nil {
		return c.failRun(ctx, run, intent, err, nil, llmadapter.ProvisionCandidateRequest{})
	}

	req := llmadapter.ProvisionCandidateRequest{Route: route, Release: release, Environment: env, Run: run, BackendKind: candidate.BackendKind, Worker: candidate.Worker}
	mergeRunMetadata(run, map[string]any{"placement_reason": candidate.Reason, "gateway_ref": c.gatewayRef(env), "target_name": targetNameFromRun(route, run)})
	run.BackendKind = candidate.BackendKind
	if candidate.Worker != nil {
		run.WorkerPubkey = candidate.Worker.PubKey
		run.WorkerName = candidate.Worker.Name
		if candidate.Worker.RuntimeTarget != nil {
			run.EndpointRef = candidate.Worker.RuntimeTarget.EndpointRef
			mergeRunMetadata(run, map[string]any{"runtime_target": candidate.Worker.RuntimeTarget})
		}
	}
	_ = c.runs.Update(ctx, run)
	c.publishStatus(ctx, intent, run, "placing_backend", "selected LLM backend placement")

	c.publishStatus(ctx, intent, run, "provisioning_backend", "provisioning LLM backend")
	result, err := provisioner.Provision(ctx, req)
	if err != nil {
		return c.failRun(ctx, run, intent, err, provisioner, req)
	}
	run.BackendKind = result.BackendKind
	run.BackendEndpoint = result.BackendEndpoint
	run.EndpointRef = result.EndpointRef
	run.WorkerPubkey = result.WorkerPubkey
	run.WorkerName = result.WorkerName
	mergeRunMetadata(run, result.Metadata)
	mergeRunMetadata(run, map[string]any{"target_name": result.TargetName})
	if err := c.runs.Update(ctx, run); err != nil {
		return err
	}

	state, err := c.registry.GetRouteState(ctx, intent.RouteID, intent.EnvironmentID)
	if err != nil {
		return c.failRun(ctx, run, intent, err, provisioner, req)
	}
	if state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID {
		_ = provisioner.Deprovision(ctx, req)
		return c.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusCancelled, nil)
	}

	c.publishStatus(ctx, intent, run, "evaluating_gate", "evaluating LLM backend promotion gate")
	gateResult, err := c.gate.Evaluate(ctx, provisioner, req, effectiveLLMGate(route, release))
	mergeRunMetadata(run, map[string]any{"promotion_gate": gateResult})
	_ = c.runs.Update(ctx, run)
	if err != nil || gateResult == nil || !gateResult.Passed {
		if err == nil {
			err = fmt.Errorf("promotion gate failed")
		}
		return c.failRun(ctx, run, intent, err, provisioner, req)
	}

	gatewayRef := c.gatewayRef(env)
	if strings.TrimSpace(gatewayRef) == "" {
		return c.failRun(ctx, run, intent, fmt.Errorf("no llm gateway configured for environment %s", env.ID), provisioner, req)
	}
	c.publishStatus(ctx, intent, run, "syncing_gateway", "syncing LLM gateway route")
	spec := BuildLLMGatewayRouteSpec(route, result.BackendEndpoint)
	gatewayObs, err := c.gateway.UpsertRoute(ctx, gatewayRef, spec)
	if err != nil {
		return c.failRun(ctx, run, intent, err, provisioner, req)
	}
	backendObs, err := provisioner.Observe(ctx, req)
	if err != nil {
		backendObs = &llmadapter.BackendObservation{BackendKind: result.BackendKind, BackendEndpoint: result.BackendEndpoint, HealthStatus: domain.HealthStatusUnhealthy, Source: "coordinator", Metadata: map[string]any{"observe_error": err.Error()}}
	}
	c.publishStatus(ctx, intent, run, "recording_observation", "recording LLM route observation")
	obs := &domain.LLMRouteObservation{RouteID: route.ID, EnvironmentID: env.ID, ObservedReleaseID: &release.ID, ObservedRunID: &run.ID, BackendKind: backendObs.BackendKind, BackendEndpoint: backendObs.BackendEndpoint, BackendHealth: backendObs.HealthStatus, GatewayStatus: gatewayObs.Status, GatewayTarget: gatewayObs.TargetURL, GatewayConfigHash: gatewayObs.GatewayConfigHash, Source: "coordinator", Metadata: map[string]any{"backend": backendObs.Metadata, "gateway": gatewayObs.Metadata}}
	if obs.GatewayTarget == "" {
		obs.GatewayTarget = result.BackendEndpoint
	}
	if obs.GatewayConfigHash == "" {
		obs.GatewayConfigHash = spec.ManagedConfigHash()
	}
	if err := c.registry.RecordObservation(ctx, obs); err != nil {
		return err
	}
	if err := c.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusSucceeded, nil); err != nil {
		return err
	}
	c.publishResult(ctx, intent, run, "completed", "LLM deployment completed")
	return nil
}

func (c *LLMProvisioningCoordinator) failRun(ctx context.Context, run *domain.LLMDeploymentRun, intent *domain.LLMDeploymentIntent, cause error, provisioner llmadapter.Provisioner, req llmadapter.ProvisionCandidateRequest) error {
	if provisioner != nil {
		_ = provisioner.Deprovision(ctx, req)
	}
	correlationIntent := c.failureIntent(intent, run, req)
	mergeRunMetadata(run, map[string]any{"error": cause.Error()})
	_ = c.runs.Update(ctx, run)
	if correlationIntent != nil {
		c.publishError(ctx, correlationIntent, run, "failed", cause)
	}
	if err := c.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusFailed, nil); err != nil {
		return err
	}
	return cause
}

func (c *LLMProvisioningCoordinator) failureIntent(intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, req llmadapter.ProvisionCandidateRequest) *domain.LLMDeploymentIntent {
	if intent != nil {
		return intent
	}
	if req.Route != nil && req.Release != nil && req.Environment != nil {
		return &domain.LLMDeploymentIntent{
			ID:            run.DeploymentIntentID,
			RouteID:       req.Route.ID,
			ReleaseID:     req.Release.ID,
			EnvironmentID: req.Environment.ID,
			Metadata:      metadataSubset(run.Metadata, "nostr_event_id", "nostr_request_pubkey"),
		}
	}
	if run == nil || run.Metadata == nil {
		return nil
	}
	routeID, routeOK := metadataUUID(run.Metadata, "route_id")
	releaseID, releaseOK := metadataUUID(run.Metadata, "release_id")
	envID, envOK := metadataUUID(run.Metadata, "environment_id")
	if !routeOK || !releaseOK || !envOK {
		return nil
	}
	return &domain.LLMDeploymentIntent{
		ID:            run.DeploymentIntentID,
		RouteID:       routeID,
		ReleaseID:     releaseID,
		EnvironmentID: envID,
		Metadata:      metadataSubset(run.Metadata, "nostr_event_id", "nostr_request_pubkey"),
	}
}

func metadataSubset(values map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := metadataString(values, key); ok {
			out[key] = value
		}
	}
	return out
}

func metadataString(values map[string]any, key string) (string, bool) {
	if values == nil {
		return "", false
	}
	raw, ok := values[key]
	if !ok {
		return "", false
	}
	text, ok := raw.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

func metadataUUID(values map[string]any, key string) (uuid.UUID, bool) {
	if values == nil {
		return uuid.Nil, false
	}
	raw, ok := values[key]
	if !ok {
		return uuid.Nil, false
	}
	text, ok := raw.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(text)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func (c *LLMProvisioningCoordinator) publishStatus(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step, message string) {
	if c.responder == nil || intent == nil {
		return
	}
	if err := c.responder.PublishStatus(ctx, intent, run, step, message); err != nil {
		c.logger.Warn("publish LLM provisioning status failed", zap.String("step", step), zap.Error(err))
	}
}

func (c *LLMProvisioningCoordinator) publishResult(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, status, message string) {
	if c.responder == nil || intent == nil {
		return
	}
	if err := c.responder.PublishResult(ctx, intent, run, status, message); err != nil {
		c.logger.Warn("publish LLM provisioning result failed", zap.String("status", status), zap.Error(err))
	}
}

func (c *LLMProvisioningCoordinator) publishError(ctx context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step string, cause error) {
	if c.responder == nil || intent == nil || cause == nil {
		return
	}
	if err := c.responder.PublishError(ctx, intent, run, step, cause); err != nil {
		c.logger.Warn("publish LLM provisioning error failed", zap.String("step", step), zap.Error(err))
	}
}

func (c *LLMProvisioningCoordinator) gatewayRef(env *domain.Environment) string {
	if env != nil && env.RuntimeConfig != nil {
		if ref, ok := env.RuntimeConfig["llm_gateway_ref"].(string); ok && strings.TrimSpace(ref) != "" {
			return strings.TrimSpace(ref)
		}
	}
	return strings.TrimSpace(c.defaultGatewayRef)
}

func effectiveLLMGate(route *domain.LLMRoute, release *domain.LLMRelease) *domain.LLMPromotionGateConfig {
	if release != nil && release.PromotionGate != nil {
		return release.PromotionGate
	}
	if route != nil {
		return route.DefaultPromotionGate
	}
	return nil
}

func mergeRunMetadata(run *domain.LLMDeploymentRun, values map[string]any) {
	if run.Metadata == nil {
		run.Metadata = map[string]any{}
	}
	for k, v := range values {
		if v != nil {
			run.Metadata[k] = v
		}
	}
}

func targetNameFromRun(route *domain.LLMRoute, run *domain.LLMDeploymentRun) string {
	name := "route"
	if route != nil && route.Name != "" {
		name = route.Name
	}
	id := "run"
	if run != nil {
		id = run.ID.String()
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return "llm-" + strings.Trim(strings.ToLower(name), "-") + "-" + id
}

// WorkerFromRunMetadata reconstructs the minimum worker shape needed to observe/deprovision a prior runtime-managed run.
func WorkerFromRunMetadata(run *domain.LLMDeploymentRun) *domain.Worker {
	if run == nil || run.Metadata == nil {
		return nil
	}
	raw, ok := run.Metadata["runtime_target"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var target domain.WorkerRuntimeTarget
	if err := json.Unmarshal(b, &target); err != nil {
		return nil
	}
	return &domain.Worker{PubKey: run.WorkerPubkey, Name: run.WorkerName, RuntimeTarget: &target}
}
