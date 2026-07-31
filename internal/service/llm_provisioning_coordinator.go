package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
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
	registry             *LLMRegistryService
	environments         repository.EnvironmentRepository
	runs                 repository.LLMDeploymentRunRepository
	placement            *LLMPlacementService
	provisioners         llmadapter.ProvisionerResolver
	gateway              llmadapter.GatewayRouteManager
	secrets              llmadapter.SecretResolver
	gate                 *LLMPromotionGate
	promotionLock        LLMPromotionLocker
	defaultGatewayRef    string
	responder            LLMProvisioningResponder
	recoveryPollInterval time.Duration
	staleRunTimeout      time.Duration
	wake                 chan struct{}
	logger               *zap.Logger
}

type LLMProvisioningCoordinatorOption func(*LLMProvisioningCoordinator)

func WithLLMCoordinatorRecoveryIntervals(recoveryPollInterval, staleRunTimeout time.Duration) LLMProvisioningCoordinatorOption {
	return func(c *LLMProvisioningCoordinator) {
		if recoveryPollInterval > 0 {
			c.recoveryPollInterval = recoveryPollInterval
		}
		if staleRunTimeout > 0 {
			c.staleRunTimeout = staleRunTimeout
		}
	}
}

func WithLLMProvisioningResponder(responder LLMProvisioningResponder) LLMProvisioningCoordinatorOption {
	return func(c *LLMProvisioningCoordinator) { c.responder = responder }
}

func WithLLMPromotionLock(lock LLMPromotionLocker) LLMProvisioningCoordinatorOption {
	return func(c *LLMProvisioningCoordinator) { c.promotionLock = lock }
}

func WithLLMSecretResolver(resolver llmadapter.SecretResolver) LLMProvisioningCoordinatorOption {
	return func(c *LLMProvisioningCoordinator) { c.secrets = resolver }
}

func NewLLMProvisioningCoordinator(registry *LLMRegistryService, envs repository.EnvironmentRepository, runs repository.LLMDeploymentRunRepository, placement *LLMPlacementService, provisioners llmadapter.ProvisionerResolver, gateway llmadapter.GatewayRouteManager, defaultGatewayRef string, logger *zap.Logger, opts ...LLMProvisioningCoordinatorOption) *LLMProvisioningCoordinator {
	if logger == nil {
		logger = zap.NewNop()
	}
	c := &LLMProvisioningCoordinator{registry: registry, environments: envs, runs: runs, placement: placement, provisioners: provisioners, gateway: gateway, gate: NewLLMPromotionGate(), defaultGatewayRef: defaultGatewayRef, recoveryPollInterval: 30 * time.Second, staleRunTimeout: 15 * time.Minute, wake: make(chan struct{}, 1), logger: logger}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *LLMProvisioningCoordinator) validateDependencies() error {
	if c == nil {
		return fmt.Errorf("LLMProvisioningCoordinator is nil")
	}
	if c.registry == nil {
		return fmt.Errorf("LLMProvisioningCoordinator registry is not configured")
	}
	if c.environments == nil {
		return fmt.Errorf("LLMProvisioningCoordinator environment repository is not configured")
	}
	if c.runs == nil {
		return fmt.Errorf("LLMProvisioningCoordinator run repository is not configured")
	}
	if c.placement == nil {
		return fmt.Errorf("LLMProvisioningCoordinator placement is not configured")
	}
	if c.provisioners == nil {
		return fmt.Errorf("LLMProvisioningCoordinator provisioner resolver is not configured")
	}
	if c.gateway == nil {
		return fmt.Errorf("LLMProvisioningCoordinator gateway is not configured")
	}
	if c.gate == nil {
		return fmt.Errorf("LLMProvisioningCoordinator promotion gate is not configured")
	}
	if c.promotionLock == nil {
		return fmt.Errorf("LLMProvisioningCoordinator promotion lock is not configured")
	}
	return nil
}

func (c *LLMProvisioningCoordinator) Name() string { return "llm-provisioning-recovery" }

// SetupSubscriptions makes persisted intent events the primary provisioning intake path.
func (c *LLMProvisioningCoordinator) SetupSubscriptions(publisher events.Publisher) {
	if c == nil || publisher == nil {
		return
	}
	trigger := func(context.Context, events.Event) { c.trigger() }
	publisher.Subscribe(events.EventLLMDeploymentIntentCreated, trigger)
	publisher.Subscribe(events.EventLLMDeploymentIntentApproved, trigger)
}

func (c *LLMProvisioningCoordinator) trigger() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Run processes event-triggered intent work and periodically scans durable state for crash recovery.
func (c *LLMProvisioningCoordinator) Run(ctx context.Context) error {
	if err := c.validateDependencies(); err != nil {
		return err
	}
	c.runUntilIdle(ctx)
	ticker := time.NewTicker(c.recoveryPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.wake:
			c.runUntilIdle(ctx)
		case <-ticker.C:
			c.runUntilIdle(ctx)
		}
	}
}

func (c *LLMProvisioningCoordinator) runUntilIdle(ctx context.Context) {
	for {
		processed, err := c.processOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				c.logger.Warn("LLM provisioning recovery scan failed", zap.Error(err))
			}
			return
		}
		if !processed {
			return
		}
	}
}

// ProcessOnce performs one queue/create/claim/process cycle. It is exposed for focused tests.
func (c *LLMProvisioningCoordinator) ProcessOnce(ctx context.Context) error {
	_, err := c.processOnce(ctx)
	return err
}

func (c *LLMProvisioningCoordinator) processOnce(ctx context.Context) (bool, error) {
	if err := c.validateDependencies(); err != nil {
		return false, err
	}
	if c.staleRunTimeout > 0 {
		if n, err := c.runs.RequeueStaleRunning(ctx, c.staleRunTimeout); err != nil {
			return false, err
		} else if n > 0 {
			c.logger.Warn("requeued stale LLM runs", zap.Int("count", n))
		}
	}
	if _, err := c.runs.EnsureQueuedRunForNextReadyIntent(ctx); err != nil {
		return false, err
	}
	run, err := c.runs.ClaimNextQueuedRun(ctx)
	if err != nil || run == nil {
		return false, err
	}
	return true, c.processRun(ctx, run)
}

func (c *LLMProvisioningCoordinator) processRun(ctx context.Context, run *domain.LLMDeploymentRun) error {
	if err := c.registry.MarkDeploymentRunCreated(ctx, run); err != nil {
		return c.failRun(ctx, run, nil, fmt.Errorf("record LLM deployment run creation for intent %s: %w", run.DeploymentIntentID, err), nil, llmadapter.ProvisionCandidateRequest{})
	}
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
	if err := c.runs.Update(ctx, run); err != nil {
		return c.failRun(ctx, run, intent, fmt.Errorf("persist LLM placement: %w", err), nil, req)
	}
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
		return c.cancelSupersededRun(ctx, run, provisioner, req)
	}

	c.publishStatus(ctx, intent, run, "evaluating_gate", "evaluating LLM backend promotion gate")
	gateResult, err := c.gate.Evaluate(ctx, provisioner, req, effectiveLLMGate(route, release))
	mergeRunMetadata(run, map[string]any{"promotion_gate": gateResult})
	if updateErr := c.runs.Update(ctx, run); updateErr != nil {
		return c.failRun(ctx, run, intent, fmt.Errorf("persist LLM promotion gate result: %w", updateErr), provisioner, req)
	}
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
	spec, err := ResolveLLMGatewayRouteSpec(ctx, route, result.BackendEndpoint, c.secrets)
	if err != nil {
		return c.failRun(ctx, run, intent, fmt.Errorf("resolve LLM gateway route secrets: %w", err), provisioner, req)
	}
	gatewayObs, superseded, err := c.promoteGateway(ctx, intent, route.ID, env.ID, gatewayRef, spec)
	if err != nil {
		return c.failRun(ctx, run, intent, err, provisioner, req)
	}
	if superseded {
		return c.cancelSupersededRun(ctx, run, provisioner, req)
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
	if cause == nil {
		cause = fmt.Errorf("LLM deployment failed")
	}
	failure := cause
	if provisioner != nil {
		if err := provisioner.Deprovision(ctx, req); err != nil {
			failure = errors.Join(failure, fmt.Errorf("deprovision failed LLM candidate: %w", err))
		}
	}
	correlationIntent := c.failureIntent(intent, run, req)
	mergeRunMetadata(run, map[string]any{"error": failure.Error()})
	if err := c.runs.Update(ctx, run); err != nil {
		failure = errors.Join(failure, fmt.Errorf("persist failed LLM run metadata: %w", err))
	}
	if correlationIntent != nil {
		c.publishError(ctx, correlationIntent, run, "failed", failure)
	}
	if err := c.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusFailed, nil); err != nil {
		failure = errors.Join(failure, fmt.Errorf("complete failed LLM run: %w", err))
	}
	return failure
}

func (c *LLMProvisioningCoordinator) promoteGateway(ctx context.Context, intent *domain.LLMDeploymentIntent, routeID, environmentID uuid.UUID, gatewayRef string, spec llmadapter.GatewayRouteSpec) (*llmadapter.GatewayRouteObservation, bool, error) {
	unlock, err := c.promotionLock.Lock(ctx, routeID, environmentID)
	if err != nil {
		return nil, false, fmt.Errorf("acquire LLM promotion lock: %w", err)
	}
	if unlock == nil {
		return nil, false, fmt.Errorf("acquire LLM promotion lock: locker returned no unlock function")
	}
	defer unlock()

	state, err := c.registry.GetRouteState(ctx, routeID, environmentID)
	if err != nil {
		return nil, false, fmt.Errorf("recheck desired LLM intent under promotion lock: %w", err)
	}
	if state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID {
		return nil, true, nil
	}
	observation, err := c.gateway.UpsertRoute(ctx, gatewayRef, spec)
	if err != nil {
		return nil, false, err
	}
	return observation, false, nil
}

func (c *LLMProvisioningCoordinator) cancelSupersededRun(ctx context.Context, run *domain.LLMDeploymentRun, provisioner llmadapter.Provisioner, req llmadapter.ProvisionCandidateRequest) error {
	var cleanupErr error
	if provisioner != nil {
		if err := provisioner.Deprovision(ctx, req); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("deprovision superseded LLM candidate: %w", err))
		}
	}
	if err := c.registry.CompleteDeploymentRun(ctx, run.ID, domain.RunStatusCancelled, nil); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("persist superseded LLM run cancellation: %w", err))
	}
	return cleanupErr
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
