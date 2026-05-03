// Package controlplane implements the Nostr-based control plane for Bahia.
// It provides a reactive event-driven interface for deployment operations,
// allowing agents and external systems to manage deployments via Nostr events.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"go.uber.org/zap"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// Event kinds for Bahia control plane (596x/696x/796x series).
// This is the CANONICAL event kind series for Bahia operations.
// The 311xx series in internal/adapters/nostr/publisher.go is deprecated.
const (
	// Request kinds (5961-5975)
	KindDeployRequest         = 5961 // Request to deploy a service
	KindRollbackRequest       = 5962 // Request to rollback a service
	KindServiceAction         = 5963 // Lifecycle action (scale, restart, stop)
	KindServiceCreate         = 5964 // Create a new service
	KindEnvironmentCreate     = 5965 // Create a new environment
	KindDeploymentApproval    = 5966 // Approve or reject a deployment
	KindObservationSubmit     = 5967 // Submit runtime observation
	KindDriftRemediate        = 5968 // Request drift remediation
	KindLLMRouteCreate        = 5971 // Create an LLM route
	KindLLMReleaseRegister    = 5972 // Register an LLM release
	KindLLMDeployRequest      = 5973 // Request LLM route deployment
	KindLLMDeploymentApproval = 5974 // Approve or reject an LLM deployment
	KindLLMRollbackRequest    = 5975 // Request LLM route rollback
	KindToolProvisionRequest  = 5976 // Agent → Bahia
	KindToolApprovalRequest   = 5977 // Bahia → Operator

	// Status kinds (6961-6973)
	KindDeploymentStatus    = 6961 // Deployment progress updates
	KindServiceStatus       = 6962 // Service health/state updates
	KindLLMDeploymentStatus = 6973 // LLM deployment/rollback progress updates
	KindToolProvisionStatus = 6976 // Bahia → Agent (progress)

	// Result kinds (7961-7973)
	KindDeploymentResult         = 7961 // Final deployment result
	KindActionResult             = 7962 // Result of a service action
	KindServiceCreateResult      = 7963 // Service creation result
	KindEnvCreateResult          = 7964 // Environment creation result
	KindObservationResult        = 7965 // Observation submission result
	KindRemediationResult        = 7966 // Drift remediation result
	KindLLMRouteCreateResult     = 7971 // LLM route creation result
	KindLLMReleaseRegisterResult = 7972 // LLM release registration result
	KindLLMDeploymentResult      = 7973 // LLM deployment/approval/rollback result
	KindToolProvisionResult      = 7976 // Bahia → Agent (final)
	KindToolApprovalResponse     = 7977 // Operator → Bahia

	// Replaceable registry kinds (3196x series, d-tag indexed)
	KindServiceState        = 31961 // Replaceable service state (d=service:env)
	KindServiceRegistry     = 31962 // Replaceable service registry entry (d=service_id)
	KindEnvironmentRegistry = 31963 // Replaceable environment registry entry (d=env_id)
	KindLLMRouteRegistry    = 31964 // Replaceable LLM route registry entry (d=route_id)
	KindLLMRouteState       = 31965 // Replaceable LLM route state (d=route:env)
)

// Config holds reactor configuration.
type Config struct {
	// Relays is the list of public relay URLs for subscriptions and results.
	Relays []string
	// PrivateRelays is the list of relay URLs for private/draft events.
	PrivateRelays []string
	// PrivateKey is the hex-encoded key used only when this reactor must create
	// its own relay pool with NIP-42 AUTH support. Event signing uses Signer.
	PrivateKey string
	// AuthorizedPubkeys is the list of pubkeys allowed to submit requests.
	AuthorizedPubkeys []string
}

// Reactor subscribes to Nostr control plane events and dispatches handlers.
type Reactor struct {
	config      Config
	pool        *nostrpool.RelayPool
	registry    *service.RegistryService
	llmRegistry *service.LLMRegistryService
	signer      canonicalnostr.Signer
	logger      *slog.Logger
	zapLog      *zap.Logger
	dedup       *nostrpool.EventDeduplicator
	backoff     *nostrpool.Backoff

	toolProvisioning repository.ToolProvisioningRepository
	toolResponder    *ToolResponder
	toolCoordinator  *service.ToolProvisioningCoordinator

	mu   sync.Mutex
	runs map[string]*DeploymentRun // requestEventID -> run
}

// DeploymentRun tracks an in-progress deployment initiated via Nostr.
type DeploymentRun struct {
	ID              uuid.UUID
	RequestEventID  string
	ServiceID       uuid.UUID
	EnvironmentID   uuid.UUID
	ArtifactID      uuid.UUID
	IntentID        *uuid.UUID
	RequesterPubkey string
	Status          string
	CurrentStep     string
	Error           string
	StartedAt       time.Time
	CompletedAt     *time.Time
}

// ReactorOption configures optional reactor dependencies without breaking the legacy constructor shape.
type ReactorOption func(*Reactor)

// WithLLMRegistry enables LLM Nostr lifecycle request handling.
func WithLLMRegistry(registry *service.LLMRegistryService) ReactorOption {
	return func(r *Reactor) { r.llmRegistry = registry }
}

func WithToolProvisioningRepository(repo repository.ToolProvisioningRepository) ReactorOption {
	return func(r *Reactor) { r.toolProvisioning = repo }
}

func WithToolResponder(responder *ToolResponder) ReactorOption {
	return func(r *Reactor) { r.toolResponder = responder }
}

func WithToolProvisioningCoordinator(coordinator *service.ToolProvisioningCoordinator) ReactorOption {
	return func(r *Reactor) { r.toolCoordinator = coordinator }
}

// NewReactor creates a new Bahia control plane reactor.
// If pool is nil, a new pool will be created from the config relays.
// signer is required for event signing. If pool is nil, config.PrivateKey is
// only used to configure relay AUTH on the created pool.
func NewReactor(config Config, registry *service.RegistryService, pool *nostrpool.RelayPool, signer canonicalnostr.Signer, zapLog *zap.Logger, opts ...ReactorOption) *Reactor {
	if zapLog == nil {
		zapLog = zap.NewNop()
	}

	// Use provided pool or create a new one
	if pool == nil {
		poolOpts := []nostrpool.RelayPoolOption{}
		if config.PrivateKey != "" {
			poolOpts = append(poolOpts, nostrpool.WithPrivateKey(config.PrivateKey))
		}

		// Copy slices to avoid mutating config's backing array
		allRelays := make([]string, 0, len(config.Relays)+len(config.PrivateRelays))
		allRelays = append(allRelays, config.Relays...)
		allRelays = append(allRelays, config.PrivateRelays...)
		pool = nostrpool.NewRelayPool(allRelays, zapLog, poolOpts...)
	}

	r := &Reactor{
		config:   config,
		pool:     pool,
		registry: registry,
		signer:   signer,
		logger:   slog.Default().With("component", "controlplane"),
		zapLog:   zapLog,
		dedup:    nostrpool.NewEventDeduplicator(10000),
		backoff:  nostrpool.DefaultBackoff(),
		runs:     make(map[string]*DeploymentRun),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run starts the reactor and blocks until context is cancelled.
func (r *Reactor) Run(ctx context.Context) error {
	r.logger.Info("starting bahia control plane reactor",
		"relays", r.config.Relays,
		"private_relays", r.config.PrivateRelays,
	)

	// Connect to relays
	r.pool.Connect(ctx)

	// Start periodic cleanup of completed runs
	go r.cleanupRuns(ctx)

	// Subscribe to control plane request events
	now := nostr.Now()
	filters := []nostr.Filter{
		{
			Kinds: []int{
				KindDeployRequest,
				KindRollbackRequest,
				KindServiceAction,
				KindServiceCreate,
				KindEnvironmentCreate,
				KindDeploymentApproval,
				KindObservationSubmit,
				KindDriftRemediate,
				KindLLMRouteCreate,
				KindLLMReleaseRegister,
				KindLLMDeployRequest,
				KindLLMDeploymentApproval,
				KindLLMRollbackRequest,
				KindToolProvisionRequest,
				KindToolApprovalResponse,
			},
			Since: &now,
		},
	}

	merged, err := r.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	r.logger.Info("subscribed to control plane events")

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reactor shutting down")
			r.pool.Close()
			return ctx.Err()

		case ev, ok := <-merged.Events:
			if !ok {
				delay := r.backoff.Next()
				r.logger.Warn("subscription closed, reconnecting...", "delay", delay)
				time.Sleep(delay)
				merged, err = r.pool.SubscribeAllWithEOSE(ctx, filters)
				if err != nil {
					r.logger.Error("reconnect failed", "error", err)
					continue
				}
				r.backoff.Reset()
				continue
			}

			r.handleEvent(ctx, ev)
		}
	}
}

// handleEvent dispatches events to the appropriate handler.
func (r *Reactor) handleEvent(ctx context.Context, event *nostr.Event) {
	// Verify event signature to prevent spoofing
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		r.logger.Warn("invalid event signature", "event_id", event.ID, "error", err)
		return
	}

	// Deduplicate events (relays may replay during reconnection)
	if r.dedup.IsDuplicate(event.ID) {
		return
	}
	r.dedup.MarkSeen(event.ID)

	switch event.Kind {
	case KindDeployRequest:
		go r.handleDeployRequest(ctx, event)
	case KindRollbackRequest:
		go r.handleRollbackRequest(ctx, event)
	case KindServiceAction:
		go r.handleServiceAction(ctx, event)
	case KindServiceCreate:
		go r.handleServiceCreate(ctx, event)
	case KindEnvironmentCreate:
		go r.handleEnvironmentCreate(ctx, event)
	case KindDeploymentApproval:
		go r.handleDeploymentApproval(ctx, event)
	case KindObservationSubmit:
		go r.handleObservationSubmit(ctx, event)
	case KindDriftRemediate:
		go r.handleDriftRemediate(ctx, event)
	case KindLLMRouteCreate:
		go r.handleLLMRouteCreate(ctx, event)
	case KindLLMReleaseRegister:
		go r.handleLLMReleaseRegister(ctx, event)
	case KindLLMDeployRequest:
		go r.handleLLMDeployRequest(ctx, event)
	case KindLLMDeploymentApproval:
		go r.handleLLMDeploymentApproval(ctx, event)
	case KindLLMRollbackRequest:
		go r.handleLLMRollbackRequest(ctx, event)
	case KindToolProvisionRequest:
		go r.handleToolProvisionRequest(ctx, event)
	case KindToolApprovalResponse:
		go r.handleToolApprovalResponse(ctx, event)
	default:
		r.logger.Warn("unexpected event kind", "kind", event.Kind)
	}
}

// handleDeployRequest processes a kind:5961 deployment request.
func (r *Reactor) handleDeployRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received deployment request")

	// Validate authorization
	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized deployment request")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse request
	req, err := r.parseDeployRequest(event)
	if err != nil {
		logger.Error("failed to parse request", "error", err)
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	logger = logger.With("service_id", req.ServiceID, "environment_id", req.EnvironmentID)
	logger.Info("creating deployment intent")

	// Validate that service, environment, and artifact exist
	if _, err := r.registry.GetService(ctx, req.ServiceID); err != nil {
		logger.Error("service not found", "error", err)
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("service not found: %v", err))
		return
	}
	if _, err := r.registry.GetEnvironment(ctx, req.EnvironmentID); err != nil {
		logger.Error("environment not found", "error", err)
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("environment not found: %v", err))
		return
	}
	if _, err := r.registry.GetArtifact(ctx, req.ArtifactID); err != nil {
		logger.Error("artifact not found", "error", err)
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("artifact not found: %v", err))
		return
	}

	// Create run tracker
	run := &DeploymentRun{
		ID:              uuid.New(),
		RequestEventID:  event.ID,
		ServiceID:       req.ServiceID,
		EnvironmentID:   req.EnvironmentID,
		ArtifactID:      req.ArtifactID,
		RequesterPubkey: event.PubKey,
		Status:          "running",
		CurrentStep:     "creating_intent",
		StartedAt:       time.Now(),
	}

	r.mu.Lock()
	r.runs[event.ID] = run
	r.mu.Unlock()

	// Publish status update
	r.publishStatus(ctx, event, "creating_intent", "Creating deployment intent")

	// Create deployment intent
	intent := &domain.DeploymentIntent{
		ID:            uuid.New(),
		ServiceID:     req.ServiceID,
		EnvironmentID: req.EnvironmentID,
		ArtifactID:    req.ArtifactID,
		RequestedBy:   event.PubKey,
		SourceKind:    domain.SourceKindEventTriggered,
		Metadata:      map[string]any{"nostr_event_id": event.ID},
	}

	if err := r.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		logger.Error("failed to create deployment intent", "error", err)
		run.Status = "failed"
		run.Error = err.Error()
		now := time.Now()
		run.CompletedAt = &now
		r.publishError(ctx, event, "intent_error", err.Error())
		return
	}

	run.IntentID = &intent.ID
	run.CurrentStep = "intent_created"

	// Publish success result
	run.Status = "completed"
	now := time.Now()
	run.CompletedAt = &now

	logger.Info("deployment intent created", "intent_id", intent.ID)

	r.publishDeploymentResult(ctx, event, intent)
}

// handleRollbackRequest processes a kind:5962 rollback request.
func (r *Reactor) handleRollbackRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received rollback request")

	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized rollback request")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse request
	var req struct {
		ServiceID     string `json:"service_id"`
		EnvironmentID string `json:"environment_id"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	serviceID, err := uuid.Parse(req.ServiceID)
	if err != nil {
		r.publishError(ctx, event, "invalid_service_id", err.Error())
		return
	}

	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.publishError(ctx, event, "invalid_environment_id", err.Error())
		return
	}

	// Execute rollback
	intent, err := r.registry.Rollback(ctx, serviceID, envID, event.PubKey)
	if err != nil {
		logger.Error("rollback failed", "error", err)
		r.publishError(ctx, event, "rollback_error", err.Error())
		return
	}

	logger.Info("rollback initiated", "intent_id", intent.ID)
	r.publishDeploymentResult(ctx, event, intent)
}

// handleServiceAction processes a kind:5963 service action.
func (r *Reactor) handleServiceAction(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received service action")

	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized service action")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse action from tags
	var serviceID, action, reason string
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "service":
			serviceID = tag[1]
		case "action":
			action = tag[1]
		case "reason":
			reason = tag[1]
		}
	}

	logger = logger.With("service_id", serviceID, "action", action)
	logger.Info("executing service action", "reason", reason)

	// For now, log and acknowledge - actual actions will be implemented
	// when service lifecycle methods are added to RegistryService
	r.publishActionResult(ctx, event, action, "acknowledged", nil)
}

// handleServiceCreate processes a kind:5964 service creation.
func (r *Reactor) handleServiceCreate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received service create request")

	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized service create")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	var req struct {
		Name         string `json:"name"`
		ArtifactRepo string `json:"artifact_repo"`
		RepoURL      string `json:"repo_url,omitempty"`
		RuntimeType  string `json:"runtime_type,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	if req.Name == "" || req.ArtifactRepo == "" {
		r.publishError(ctx, event, "validation_error", "name and artifact_repo are required")
		return
	}

	runtimeType := domain.RuntimeTypeDocker
	if req.RuntimeType != "" {
		if err := domain.ValidateRuntimeType(domain.RuntimeType(req.RuntimeType)); err != nil {
			r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid runtime_type: %v", err))
			return
		}
		runtimeType = domain.RuntimeType(req.RuntimeType)
	}

	svc := &domain.Service{
		ID:           uuid.New(),
		Name:         req.Name,
		ArtifactRepo: req.ArtifactRepo,
		RepoURL:      req.RepoURL,
		RuntimeType:  runtimeType,
	}

	if err := r.registry.CreateService(ctx, svc); err != nil {
		logger.Error("failed to create service", "error", err)
		r.publishError(ctx, event, "create_error", err.Error())
		return
	}

	logger.Info("service created", "service_id", svc.ID, "name", svc.Name)
	r.publishServiceCreated(ctx, event, svc)
}

// handleEnvironmentCreate processes a kind:5965 environment creation.
func (r *Reactor) handleEnvironmentCreate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received environment create request")

	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized environment create")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	var req struct {
		Name           string `json:"name"`
		Protected      bool   `json:"protected,omitempty"`
		DeployStrategy string `json:"deploy_strategy,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	if req.Name == "" {
		r.publishError(ctx, event, "validation_error", "name is required")
		return
	}

	deployStrategy := domain.DeployStrategyReplace
	if req.DeployStrategy != "" {
		deployStrategy = domain.DeployStrategy(req.DeployStrategy)
	}

	env := &domain.Environment{
		ID:             uuid.New(),
		Name:           req.Name,
		Protected:      req.Protected,
		DeployStrategy: deployStrategy,
	}

	if err := r.registry.CreateEnvironment(ctx, env); err != nil {
		logger.Error("failed to create environment", "error", err)
		r.publishError(ctx, event, "create_error", err.Error())
		return
	}

	logger.Info("environment created", "environment_id", env.ID, "name", env.Name)
	r.publishEnvironmentCreated(ctx, event, env)
}

// handleDeploymentApproval processes a kind:5966 deployment approval/rejection.
func (r *Reactor) handleDeploymentApproval(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "approver", event.PubKey)
	logger.Info("received deployment approval request")

	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized approval request")
		r.publishError(ctx, event, "unauthorized", "approver not in authorized list")
		return
	}

	// Parse approval from tags
	var intentID, decision string
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "intent":
			intentID = tag[1]
		case "decision":
			decision = tag[1] // "approve" or "reject"
		}
	}

	if intentID == "" {
		r.publishError(ctx, event, "validation_error", "intent tag is required")
		return
	}
	if decision != "approve" && decision != "reject" {
		r.publishError(ctx, event, "validation_error", "decision must be 'approve' or 'reject'")
		return
	}

	parsedIntentID, err := uuid.Parse(intentID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid intent id: %v", err))
		return
	}

	logger = logger.With("intent_id", intentID, "decision", decision)

	if decision == "approve" {
		if err := r.registry.ApproveDeploymentIntent(ctx, parsedIntentID); err != nil {
			logger.Error("failed to approve deployment", "error", err)
			r.publishError(ctx, event, "approval_error", err.Error())
			return
		}
		logger.Info("deployment approved")
	} else {
		if err := r.registry.RejectDeploymentIntent(ctx, parsedIntentID); err != nil {
			logger.Error("failed to reject deployment", "error", err)
			r.publishError(ctx, event, "rejection_error", err.Error())
			return
		}
		logger.Info("deployment rejected")
	}

	r.publishApprovalResult(ctx, event, intentID, decision)
}

// handleLLMRouteCreate processes a kind:5971 LLM route creation request.
func (r *Reactor) handleLLMRouteCreate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "route_create") {
		return
	}
	var req struct {
		Name                   string                         `json:"name"`
		Description            string                         `json:"description,omitempty"`
		GatewayConfig          *domain.LLMGatewayRouteConfig  `json:"gateway_config,omitempty"`
		DefaultPlacementPolicy *domain.LLMPlacementPolicy     `json:"default_placement_policy,omitempty"`
		DefaultPromotionGate   *domain.LLMPromotionGateConfig `json:"default_promotion_gate,omitempty"`
		Metadata               map[string]any                 `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishLLMError(ctx, event, "parse_error", err.Error())
		return
	}
	route := &domain.LLMRoute{Name: req.Name, Description: req.Description, GatewayConfig: req.GatewayConfig, DefaultPlacementPolicy: req.DefaultPlacementPolicy, DefaultPromotionGate: req.DefaultPromotionGate, Metadata: req.Metadata}
	if err := r.llmRegistry.CreateRoute(ctx, route); err != nil {
		logger.Error("failed to create LLM route", "error", err)
		r.publishLLMError(ctx, event, "create_error", err.Error())
		return
	}
	logger.Info("LLM route created", "route_id", route.ID.String(), "name", route.Name)
	r.publishLLMRouteCreateResult(ctx, event, route)
}

// handleLLMReleaseRegister processes a kind:5972 LLM release registration request.
func (r *Reactor) handleLLMReleaseRegister(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "release_register") {
		return
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
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishLLMError(ctx, event, "parse_error", err.Error())
		return
	}
	if req.RouteID == "" {
		req.RouteID = tagValueNostr(event.Tags, "route")
	}
	routeID, err := uuid.Parse(req.RouteID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid route_id: %v", err))
		return
	}
	release := &domain.LLMRelease{RouteID: routeID, Version: req.Version, ModelRef: req.ModelRef, ModelSource: req.ModelSource, ModelRevision: req.ModelRevision, EstimatedVRAMGB: req.EstimatedVRAMGB, BackendPreferences: req.BackendPreferences, RuntimeBackend: req.RuntimeBackend, ExternalBackend: req.ExternalBackend, PlacementPolicy: req.PlacementPolicy, PromotionGate: req.PromotionGate, Metadata: req.Metadata}
	if err := r.llmRegistry.CreateRelease(ctx, release); err != nil {
		logger.Error("failed to register LLM release", "error", err)
		r.publishLLMError(ctx, event, "register_error", err.Error())
		return
	}
	logger.Info("LLM release registered", "route_id", routeID.String(), "release_id", release.ID.String())
	r.publishLLMReleaseRegisterResult(ctx, event, release)
}

// handleLLMDeployRequest processes a kind:5973 LLM deployment request.
func (r *Reactor) handleLLMDeployRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "deploy") {
		return
	}
	var req struct {
		RouteID       string         `json:"route_id"`
		EnvironmentID string         `json:"environment_id"`
		ReleaseID     string         `json:"release_id"`
		RequestedBy   string         `json:"requested_by,omitempty"`
		Metadata      map[string]any `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishLLMError(ctx, event, "parse_error", err.Error())
		return
	}
	if req.RouteID == "" {
		req.RouteID = tagValueNostr(event.Tags, "route")
	}
	if req.EnvironmentID == "" {
		req.EnvironmentID = tagValueNostr(event.Tags, "environment")
	}
	if req.ReleaseID == "" {
		req.ReleaseID = tagValueNostr(event.Tags, "release")
	}
	routeID, err := uuid.Parse(req.RouteID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid route_id: %v", err))
		return
	}
	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err))
		return
	}
	releaseID, err := uuid.Parse(req.ReleaseID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid release_id: %v", err))
		return
	}
	if req.RequestedBy == "" {
		req.RequestedBy = event.PubKey
	}
	metadata := req.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["nostr_event_id"] = event.ID
	metadata["nostr_request_pubkey"] = event.PubKey
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, EnvironmentID: envID, ReleaseID: releaseID, RequestedBy: req.RequestedBy, SourceKind: domain.SourceKindEventTriggered, Metadata: metadata}
	if err := r.llmRegistry.CreateDeploymentIntent(ctx, intent); err != nil {
		logger.Error("failed to create LLM deployment intent", "error", err)
		r.publishLLMError(ctx, event, "intent_error", err.Error())
		return
	}
	logger.Info("LLM deployment intent created", "intent_id", intent.ID.String())
	r.publishLLMDeploymentStatus(ctx, event, intent, "accepted", "LLM deployment intent accepted")
}

// handleLLMDeploymentApproval processes a kind:5974 LLM approval/rejection request.
func (r *Reactor) handleLLMDeploymentApproval(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "approver", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "approval") {
		return
	}
	var content struct {
		IntentID string `json:"intent_id,omitempty"`
		Decision string `json:"decision,omitempty"`
	}
	_ = json.Unmarshal([]byte(event.Content), &content)
	if content.IntentID == "" {
		content.IntentID = tagValueNostr(event.Tags, "intent")
	}
	if content.Decision == "" {
		content.Decision = tagValueNostr(event.Tags, "decision")
	}
	if content.Decision != "approve" && content.Decision != "reject" {
		r.publishLLMError(ctx, event, "validation_error", "decision must be 'approve' or 'reject'")
		return
	}
	intentID, err := uuid.Parse(content.IntentID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid intent_id: %v", err))
		return
	}
	if content.Decision == "approve" {
		err = r.llmRegistry.ApproveDeploymentIntent(ctx, intentID)
	} else {
		err = r.llmRegistry.RejectDeploymentIntent(ctx, intentID)
	}
	if err != nil {
		logger.Error("failed to apply LLM deployment approval decision", "error", err)
		r.publishLLMError(ctx, event, "approval_error", err.Error())
		return
	}
	intent, _ := r.llmRegistry.GetDeploymentIntent(ctx, intentID)
	if intent == nil {
		intent = &domain.LLMDeploymentIntent{ID: intentID}
	}
	r.publishLLMDeploymentResult(ctx, event, intent, content.Decision, "LLM deployment approval decision recorded")
}

// handleLLMRollbackRequest processes a kind:5975 LLM rollback request.
func (r *Reactor) handleLLMRollbackRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	if !r.authorizeLLMRequest(ctx, event, "rollback") {
		return
	}
	var req struct {
		RouteID       string `json:"route_id,omitempty"`
		EnvironmentID string `json:"environment_id,omitempty"`
		RequestedBy   string `json:"requested_by,omitempty"`
	}
	_ = json.Unmarshal([]byte(event.Content), &req)
	if req.RouteID == "" {
		req.RouteID = tagValueNostr(event.Tags, "route")
	}
	if req.EnvironmentID == "" {
		req.EnvironmentID = tagValueNostr(event.Tags, "environment")
	}
	routeID, err := uuid.Parse(req.RouteID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid route_id: %v", err))
		return
	}
	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.publishLLMError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err))
		return
	}
	if req.RequestedBy == "" {
		req.RequestedBy = event.PubKey
	}

	metadata := map[string]any{
		"nostr_event_id":         event.ID,
		"nostr_request_pubkey":   event.PubKey,
		"nostr_request_kind":     event.Kind,
		"nostr_request_command":  "llm_rollback",
		"nostr_requested_by_raw": req.RequestedBy,
	}
	intent, err := r.llmRegistry.RollbackWithMetadata(ctx, routeID, envID, req.RequestedBy, metadata)
	if err != nil {
		logger.Error("failed to initiate LLM rollback", "error", err)
		r.publishLLMError(ctx, event, "rollback_error", err.Error())
		return
	}
	logger.Info("LLM rollback intent created", "intent_id", intent.ID.String())
	r.publishLLMDeploymentStatus(ctx, event, intent, "accepted", "LLM rollback intent accepted")
}

func (r *Reactor) handleToolProvisionRequest(ctx context.Context, event *nostr.Event) error {
	logger := r.zapLog.With(zap.String("event_id", event.ID), zap.String("requester", event.PubKey), zap.Int("kind", event.Kind))
	if !r.isAuthorized(event.PubKey) {
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return fmt.Errorf("unauthorized requester")
	}
	if r.toolProvisioning == nil {
		r.publishError(ctx, event, "tool_provisioning_unavailable", "tool provisioning repository not configured")
		return fmt.Errorf("tool provisioning repository not configured")
	}
	var req struct {
		ServiceID     string               `json:"service_id"`
		EnvironmentID string               `json:"environment_id"`
		Operation     string               `json:"operation"`
		Tools         []domain.ToolRequest `json:"tools"`
		Reason        string               `json:"reason"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return fmt.Errorf("parse tool provisioning request: %w", err)
	}
	serviceID, err := uuid.Parse(req.ServiceID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid service_id: %v", err))
		return err
	}
	envID, err := uuid.Parse(req.EnvironmentID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid environment_id: %v", err))
		return err
	}
	if len(req.Tools) == 0 {
		r.publishError(ctx, event, "validation_error", "tools are required")
		return fmt.Errorf("empty tools")
	}
	intent := &domain.ToolProvisionIntent{
		ID:              uuid.New(),
		ServiceID:       serviceID,
		EnvironmentID:   envID,
		RequestedTools:  req.Tools,
		Status:          domain.ToolProvisionStatusPending,
		NostrEventID:    event.ID,
		RequesterPubkey: event.PubKey,
		CreatedAt:       time.Now().UTC(),
	}
	if err := r.toolProvisioning.CreateIntent(ctx, intent); err != nil {
		r.publishError(ctx, event, "intent_error", err.Error())
		return fmt.Errorf("create tool provisioning intent: %w", err)
	}
	if r.toolResponder != nil {
		_ = r.toolResponder.PublishStatus(ctx, event, intent, "queued", "Tool provisioning intent accepted and queued")
	}
	logger.Info("tool provisioning request accepted", zap.String("intent_id", intent.ID.String()), zap.String("operation", req.Operation), zap.String("reason", req.Reason), zap.Int("tool_count", len(req.Tools)))
	return nil
}

func (r *Reactor) handleToolApprovalResponse(ctx context.Context, event *nostr.Event) error {
	logger := r.zapLog.With(zap.String("event_id", event.ID), zap.String("operator", event.PubKey), zap.Int("kind", event.Kind))
	if !r.isAuthorized(event.PubKey) {
		r.publishError(ctx, event, "unauthorized", "operator not in authorized list")
		return fmt.Errorf("unauthorized operator")
	}
	if r.toolProvisioning == nil {
		r.publishError(ctx, event, "tool_provisioning_unavailable", "tool provisioning repository not configured")
		return fmt.Errorf("tool provisioning repository not configured")
	}
	var req struct {
		IntentID string `json:"intent_id"`
		Action   string `json:"action"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		r.publishError(ctx, event, "parse_error", err.Error())
		return fmt.Errorf("parse tool approval response: %w", err)
	}
	intentID, err := uuid.Parse(req.IntentID)
	if err != nil {
		r.publishError(ctx, event, "validation_error", fmt.Sprintf("invalid intent_id: %v", err))
		return err
	}
	if req.Action != "approve" && req.Action != "reject" {
		r.publishError(ctx, event, "validation_error", "action must be 'approve' or 'reject'")
		return fmt.Errorf("invalid action")
	}
	intent, err := r.toolProvisioning.GetIntent(ctx, intentID)
	if err != nil {
		r.publishError(ctx, event, "lookup_error", err.Error())
		return fmt.Errorf("get tool provisioning intent: %w", err)
	}
	if intent == nil {
		r.publishError(ctx, event, "not_found", "tool provisioning intent not found")
		return fmt.Errorf("intent not found")
	}
	now := time.Now().UTC()
	if req.Action == "approve" {
		intent.Status = domain.ToolProvisionStatusApproved
		intent.ApprovedBy = event.PubKey
		intent.ApprovedAt = &now
	} else {
		intent.Status = domain.ToolProvisionStatusRejected
	}
	if err := r.toolProvisioning.UpdateIntent(ctx, intent); err != nil {
		r.publishError(ctx, event, "update_error", err.Error())
		return fmt.Errorf("update tool provisioning intent: %w", err)
	}
	if err := r.toolProvisioning.LogApproval(ctx, intent.ID, req.Action, event.PubKey, req.Reason); err != nil {
		logger.Warn("failed to log tool approval action", zap.Error(err))
	}
	if req.Action == "approve" {
		logger.Info("tool provisioning approved and queued", zap.String("intent_id", intent.ID.String()))
		if r.toolCoordinator != nil {
			if err := r.toolCoordinator.ProcessApprovedIntent(ctx, intent.ID); err != nil {
				logger.Error("processing approved tool intent failed", zap.Error(err))
			}
		}
	} else {
		logger.Info("tool provisioning rejected", zap.String("intent_id", intent.ID.String()))
	}
	if r.toolResponder != nil {
		requestEvent := &nostr.Event{ID: intent.NostrEventID, PubKey: intent.RequesterPubkey}
		_ = r.toolResponder.PublishResult(ctx, requestEvent, intent, req.Action == "approve", req.Reason)
	}
	return nil
}

func (r *Reactor) authorizeLLMRequest(ctx context.Context, event *nostr.Event, step string) bool {
	if !r.isAuthorized(event.PubKey) {
		r.publishLLMError(ctx, event, "unauthorized", "requester not in authorized list")
		return false
	}
	if r.llmRegistry == nil {
		r.publishLLMError(ctx, event, step+"_unavailable", "LLM registry is not configured")
		return false
	}
	return true
}

func tagValueNostr(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// isAuthorized checks if a pubkey is authorized to use the control plane.
func (r *Reactor) isAuthorized(pubkey string) bool {
	if len(r.config.AuthorizedPubkeys) == 0 {
		return false
	}
	return slices.Contains(r.config.AuthorizedPubkeys, pubkey)
}

// parseDeployRequest extracts deployment request data from an event.
func (r *Reactor) parseDeployRequest(event *nostr.Event) (*deployRequest, error) {
	var req deployRequest

	// Parse from content JSON
	var content struct {
		ServiceID     string `json:"service_id"`
		EnvironmentID string `json:"environment_id"`
		ArtifactID    string `json:"artifact_id"`
	}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return nil, fmt.Errorf("invalid JSON content: %w", err)
	}

	var err error
	req.ServiceID, err = uuid.Parse(content.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service_id: %w", err)
	}

	req.EnvironmentID, err = uuid.Parse(content.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("invalid environment_id: %w", err)
	}

	req.ArtifactID, err = uuid.Parse(content.ArtifactID)
	if err != nil {
		return nil, fmt.Errorf("invalid artifact_id: %w", err)
	}

	return &req, nil
}

type deployRequest struct {
	ServiceID     uuid.UUID
	EnvironmentID uuid.UUID
	ArtifactID    uuid.UUID
}

// --- Event Publishing ---

func (r *Reactor) appendRequestResourceTags(ctx context.Context, tags nostr.Tags, requestEvent *nostr.Event) nostr.Tags {
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if len(tag) >= 2 {
			seen[tag[0]+"="+tag[1]] = struct{}{}
		}
	}
	add := func(key, value string) {
		if value == "" {
			return
		}
		dedupeKey := key + "=" + value
		if _, ok := seen[dedupeKey]; ok {
			return
		}
		seen[dedupeKey] = struct{}{}
		tags = append(tags, nostr.Tag{key, value})
	}

	r.mu.Lock()
	run := r.runs[requestEvent.ID]
	r.mu.Unlock()
	if run != nil {
		add("service", run.ServiceID.String())
		add("environment", run.EnvironmentID.String())
		if run.ArtifactID != uuid.Nil {
			add("artifact", run.ArtifactID.String())
		}
		if run.IntentID != nil {
			add("intent", run.IntentID.String())
		}
		add("run", run.ID.String())
	}

	var content struct {
		ServiceID     string `json:"service_id"`
		EnvironmentID string `json:"environment_id"`
		ArtifactID    string `json:"artifact_id"`
		IntentID      string `json:"intent_id"`
		RunID         string `json:"run_id"`
	}
	if requestEvent.Content != "" {
		_ = json.Unmarshal([]byte(requestEvent.Content), &content)
	}
	add("service", content.ServiceID)
	add("environment", content.EnvironmentID)
	add("artifact", content.ArtifactID)
	add("intent", content.IntentID)
	add("run", content.RunID)

	for _, tag := range requestEvent.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "service", "environment", "artifact", "intent", "run":
			add(tag[0], tag[1])
		}
	}

	// Approval requests normally provide only an intent tag; enrich with the
	// referenced resources when possible so consumers can filter result events.
	if content.IntentID == "" {
		for _, tag := range tags {
			if len(tag) >= 2 && tag[0] == "intent" {
				content.IntentID = tag[1]
				break
			}
		}
	}
	if r.registry != nil {
		if intentID, err := uuid.Parse(content.IntentID); err == nil {
			if intent, err := r.registry.GetDeploymentIntent(ctx, intentID); err == nil && intent != nil {
				add("service", intent.ServiceID.String())
				add("environment", intent.EnvironmentID.String())
				add("artifact", intent.ArtifactID.String())
			}
		}
	}
	return tags
}

// publishStatus publishes a kind:6961 deployment status event.
func (r *Reactor) publishStatus(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", "processing"},
		{"step", step},
	}
	tags = r.appendRequestResourceTags(ctx, tags, requestEvent)

	event := &nostr.Event{
		Kind:      KindDeploymentStatus,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   message,
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign status event: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// publishDeploymentResult publishes a kind:7961 deployment result event.
func (r *Reactor) publishDeploymentResult(ctx context.Context, requestEvent *nostr.Event, intent *domain.DeploymentIntent) error {
	content, _ := json.Marshal(map[string]interface{}{
		"intent_id":      intent.ID.String(),
		"service_id":     intent.ServiceID.String(),
		"environment_id": intent.EnvironmentID.String(),
		"artifact_id":    intent.ArtifactID.String(),
		"status":         intent.Status,
	})

	event := &nostr.Event{
		Kind:      KindDeploymentResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "success"},
			{"service", intent.ServiceID.String()},
			{"environment", intent.EnvironmentID.String()},
			{"artifact", intent.ArtifactID.String()},
			{"intent", intent.ID.String()},
		},
		Content: string(content),
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign result event: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// publishActionResult publishes a kind:7962 action result event.
func (r *Reactor) publishActionResult(ctx context.Context, requestEvent *nostr.Event, action, status string, err error) error {
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"action", action},
		{"status", status},
	}
	tags = r.appendRequestResourceTags(ctx, tags, requestEvent)

	content := map[string]interface{}{
		"action": action,
		"status": status,
	}
	if err != nil {
		tags = append(tags, nostr.Tag{"error", err.Error()})
		content["error"] = err.Error()
	}

	contentBytes, _ := json.Marshal(content)

	event := &nostr.Event{
		Kind:      KindActionResult,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   string(contentBytes),
	}

	if signErr := r.signEvent(ctx, event); signErr != nil {
		return fmt.Errorf("sign action result: %w", signErr)
	}

	_, pubErr := r.pool.Publish(ctx, *event)
	return pubErr
}

// publishApprovalResult publishes a result for deployment approval/rejection.
func (r *Reactor) publishApprovalResult(ctx context.Context, requestEvent *nostr.Event, intentID, decision string) error {
	content, _ := json.Marshal(map[string]interface{}{
		"intent_id": intentID,
		"decision":  decision,
	})
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", "success"},
		{"intent", intentID},
		{"decision", decision},
	}
	if parsedIntentID, err := uuid.Parse(intentID); err == nil {
		if intent, err := r.registry.GetDeploymentIntent(ctx, parsedIntentID); err == nil && intent != nil {
			tags = append(tags,
				nostr.Tag{"service", intent.ServiceID.String()},
				nostr.Tag{"environment", intent.EnvironmentID.String()},
				nostr.Tag{"artifact", intent.ArtifactID.String()},
			)
		}
	}

	event := &nostr.Event{
		Kind:      KindActionResult,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   string(content),
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign approval result: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

func (r *Reactor) publishLLMRouteCreateResult(ctx context.Context, requestEvent *nostr.Event, route *domain.LLMRoute) error {
	content, _ := json.Marshal(map[string]any{
		"route_id": route.ID.String(),
		"name":     route.Name,
		"status":   "success",
	})
	event := &nostr.Event{
		Kind:      KindLLMRouteCreateResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "success"},
			{"route", route.ID.String()},
		},
		Content: string(content),
	}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign LLM route create result: %w", err)
	}
	_, err := r.pool.Publish(ctx, *event)
	return err
}

func (r *Reactor) publishLLMReleaseRegisterResult(ctx context.Context, requestEvent *nostr.Event, release *domain.LLMRelease) error {
	content, _ := json.Marshal(map[string]any{
		"route_id":   release.RouteID.String(),
		"release_id": release.ID.String(),
		"version":    release.Version,
		"status":     "success",
	})
	event := &nostr.Event{
		Kind:      KindLLMReleaseRegisterResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "success"},
			{"route", release.RouteID.String()},
			{"release", release.ID.String()},
		},
		Content: string(content),
	}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign LLM release register result: %w", err)
	}
	_, err := r.pool.Publish(ctx, *event)
	return err
}

func (r *Reactor) publishLLMDeploymentStatus(ctx context.Context, requestEvent *nostr.Event, intent *domain.LLMDeploymentIntent, step, message string) error {
	content, _ := json.Marshal(map[string]any{
		"intent_id":      intent.ID.String(),
		"route_id":       intent.RouteID.String(),
		"environment_id": intent.EnvironmentID.String(),
		"release_id":     intent.ReleaseID.String(),
		"status":         "processing",
		"step":           step,
		"message":        message,
	})
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", "processing"},
		{"step", step},
		{"route", intent.RouteID.String()},
		{"environment", intent.EnvironmentID.String()},
		{"release", intent.ReleaseID.String()},
		{"intent", intent.ID.String()},
	}
	event := &nostr.Event{Kind: KindLLMDeploymentStatus, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign LLM deployment status: %w", err)
	}
	_, err := r.pool.Publish(ctx, *event)
	return err
}

func (r *Reactor) publishLLMDeploymentResult(ctx context.Context, requestEvent *nostr.Event, intent *domain.LLMDeploymentIntent, status, message string) error {
	content, _ := json.Marshal(map[string]any{
		"intent_id":      intent.ID.String(),
		"route_id":       intent.RouteID.String(),
		"environment_id": intent.EnvironmentID.String(),
		"release_id":     intent.ReleaseID.String(),
		"status":         status,
		"message":        message,
	})
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", "success"},
		{"result", status},
		{"route", intent.RouteID.String()},
		{"environment", intent.EnvironmentID.String()},
		{"release", intent.ReleaseID.String()},
		{"intent", intent.ID.String()},
	}
	event := &nostr.Event{Kind: KindLLMDeploymentResult, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign LLM deployment result: %w", err)
	}
	_, err := r.pool.Publish(ctx, *event)
	return err
}

func (r *Reactor) publishLLMError(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	kind := KindLLMDeploymentResult
	switch requestEvent.Kind {
	case KindLLMRouteCreate:
		kind = KindLLMRouteCreateResult
	case KindLLMReleaseRegister:
		kind = KindLLMReleaseRegisterResult
	}
	content, _ := json.Marshal(map[string]any{"status": "error", "step": step, "error": message})
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", "error"},
		{"step", step},
		{"error", message},
	}
	tags = appendLLMRequestTags(tags, requestEvent)
	event := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign LLM error result: %w", err)
	}
	_, err := r.pool.Publish(ctx, *event)
	return err
}

func appendLLMRequestTags(tags nostr.Tags, requestEvent *nostr.Event) nostr.Tags {
	seen := map[string]struct{}{}
	for _, tag := range tags {
		if len(tag) >= 2 {
			seen[tag[0]+"="+tag[1]] = struct{}{}
		}
	}
	add := func(key, value string) {
		if value == "" {
			return
		}
		k := key + "=" + value
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		tags = append(tags, nostr.Tag{key, value})
	}
	if requestEvent.Content != "" {
		var raw map[string]any
		if json.Unmarshal([]byte(requestEvent.Content), &raw) == nil {
			add("route", stringFromAny(raw["route_id"]))
			add("environment", stringFromAny(raw["environment_id"]))
			add("release", stringFromAny(raw["release_id"]))
			add("intent", stringFromAny(raw["intent_id"]))
			add("run", stringFromAny(raw["run_id"]))
		}
	}
	for _, tag := range requestEvent.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "route", "environment", "release", "intent", "run":
			add(tag[0], tag[1])
		}
	}
	return tags
}

// publishServiceCreated publishes a result for service creation.
func (r *Reactor) publishServiceCreated(ctx context.Context, requestEvent *nostr.Event, svc *domain.Service) error {
	content, _ := json.Marshal(map[string]interface{}{
		"service_id":    svc.ID.String(),
		"name":          svc.Name,
		"artifact_repo": svc.ArtifactRepo,
		"runtime_type":  svc.RuntimeType,
	})

	event := &nostr.Event{
		Kind:      KindServiceCreateResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "success"},
			{"type", "service_created"},
			{"service", svc.ID.String()},
		},
		Content: string(content),
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign service created: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// publishEnvironmentCreated publishes a result for environment creation.
func (r *Reactor) publishEnvironmentCreated(ctx context.Context, requestEvent *nostr.Event, env *domain.Environment) error {
	content, _ := json.Marshal(map[string]interface{}{
		"environment_id":  env.ID.String(),
		"name":            env.Name,
		"protected":       env.Protected,
		"deploy_strategy": env.DeployStrategy,
	})

	event := &nostr.Event{
		Kind:      KindEnvCreateResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "success"},
			{"type", "environment_created"},
			{"environment", env.ID.String()},
		},
		Content: string(content),
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign environment created: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// publishError publishes a kind:7961 error result event.
func (r *Reactor) publishError(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", "error"},
		{"step", step},
		{"error", message},
	}
	tags = r.appendRequestResourceTags(ctx, tags, requestEvent)
	event := &nostr.Event{
		Kind:      KindDeploymentResult,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   message,
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign error event: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// signEvent signs an event through the canonical signer compatibility boundary.
func (r *Reactor) signEvent(ctx context.Context, event *nostr.Event) error {
	return SignGoNostrEvent(ctx, r.signer, event)
}

// GetRun returns the current deployment run for a request event.
func (r *Reactor) GetRun(requestEventID string) *DeploymentRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[requestEventID]
}

// ObservationRequest represents a kind:5967 observation submission.
type ObservationRequest struct {
	ServiceID           uuid.UUID `json:"service_id"`
	EnvironmentID       uuid.UUID `json:"environment_id"`
	ObservedImageDigest string    `json:"observed_image_digest"`
	ObservedImageRepo   string    `json:"observed_image_repo,omitempty"`
	ObservedContainerID string    `json:"observed_container_id,omitempty"`
	ObservedHost        string    `json:"observed_host,omitempty"`
	ObservedVersion     string    `json:"observed_version,omitempty"`
	HealthStatus        string    `json:"health_status"`
	Source              string    `json:"source"`
}

// handleObservationSubmit processes a kind:5967 observation submission event.
func (r *Reactor) handleObservationSubmit(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received observation submission")

	// Validate authorization
	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized observation submission")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse observation request
	var req ObservationRequest
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		logger.Error("failed to parse observation request", "error", err)
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	// Validate required fields
	if req.ServiceID == uuid.Nil {
		r.publishError(ctx, event, "validation_error", "service_id is required")
		return
	}
	if req.EnvironmentID == uuid.Nil {
		r.publishError(ctx, event, "validation_error", "environment_id is required")
		return
	}
	if req.ObservedImageDigest == "" {
		r.publishError(ctx, event, "validation_error", "observed_image_digest is required")
		return
	}

	// Parse health status
	healthStatus := domain.HealthStatus(req.HealthStatus)
	if healthStatus == "" {
		healthStatus = domain.HealthStatusUnknown
	}

	// Create and record observation
	obs := &domain.RuntimeObservation{
		ID:                  uuid.New(),
		ServiceID:           req.ServiceID,
		EnvironmentID:       req.EnvironmentID,
		ObservedImageDigest: req.ObservedImageDigest,
		ObservedImageRepo:   req.ObservedImageRepo,
		ObservedContainerID: req.ObservedContainerID,
		ObservedHost:        req.ObservedHost,
		ObservedVersion:     req.ObservedVersion,
		HealthStatus:        healthStatus,
		Source:              req.Source,
		ObservedAt:          time.Now(),
	}

	if err := r.registry.RecordObservation(ctx, obs); err != nil {
		logger.Error("failed to record observation", "error", err)
		r.publishError(ctx, event, "record_error", err.Error())
		return
	}

	logger.Info("observation recorded", "observation_id", obs.ID)

	// Publish observation result
	if err := r.publishObservationResult(ctx, event, obs); err != nil {
		logger.Error("failed to publish observation result", "error", err)
	}

	// Publish updated state event
	if err := r.publishStateEvent(ctx, req.ServiceID, req.EnvironmentID); err != nil {
		logger.Error("failed to publish state event", "error", err)
	}
}

// handleDriftRemediate processes a kind:5968 drift remediation request.
func (r *Reactor) handleDriftRemediate(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received drift remediation request")

	// Validate authorization
	if !r.isAuthorized(event.PubKey) {
		logger.Warn("unauthorized drift remediation request")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized list")
		return
	}

	// Parse request - expects service_id and environment_id
	var req struct {
		ServiceID     uuid.UUID `json:"service_id"`
		EnvironmentID uuid.UUID `json:"environment_id"`
	}
	if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
		logger.Error("failed to parse remediation request", "error", err)
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	// Get current state
	state, err := r.registry.GetEnvironmentServiceState(ctx, req.ServiceID, req.EnvironmentID)
	if err != nil {
		logger.Error("failed to get service state", "error", err)
		r.publishError(ctx, event, "state_error", err.Error())
		return
	}

	// Check if drifted
	if state.DriftStatus != domain.DriftStatusDrifted {
		r.publishRemediationResult(ctx, event, state, "not_drifted", "Service is not in drifted state")
		return
	}

	// If we have a desired artifact, create a new deployment intent to remediate
	if state.DesiredArtifactID == nil {
		r.publishRemediationResult(ctx, event, state, "no_desired_artifact", "No desired artifact configured")
		return
	}

	// Create deployment intent to restore desired state
	intent := &domain.DeploymentIntent{
		ID:            uuid.New(),
		ServiceID:     req.ServiceID,
		EnvironmentID: req.EnvironmentID,
		ArtifactID:    *state.DesiredArtifactID,
		RequestedBy:   event.PubKey,
		SourceKind:    domain.SourceKindEventTriggered,
		Status:        domain.IntentStatusPending,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := r.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		logger.Error("failed to create remediation intent", "error", err)
		r.publishError(ctx, event, "intent_error", err.Error())
		return
	}

	logger.Info("remediation intent created", "intent_id", intent.ID)
	r.publishRemediationResult(ctx, event, state, "remediation_started", fmt.Sprintf("Deployment intent %s created", intent.ID))
}

// publishObservationResult publishes a kind:7965 observation result event.
func (r *Reactor) publishObservationResult(ctx context.Context, requestEvent *nostr.Event, obs *domain.RuntimeObservation) error {
	content, _ := json.Marshal(map[string]interface{}{
		"observation_id":        obs.ID.String(),
		"service_id":            obs.ServiceID.String(),
		"environment_id":        obs.EnvironmentID.String(),
		"observed_image_digest": obs.ObservedImageDigest,
		"health_status":         string(obs.HealthStatus),
		"observed_at":           obs.ObservedAt.Format(time.RFC3339),
	})

	event := &nostr.Event{
		Kind:      KindObservationResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "success"},
			{"service", obs.ServiceID.String()},
			{"environment", obs.EnvironmentID.String()},
			{"observation", obs.ID.String()},
		},
		Content: string(content),
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign observation result: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// publishRemediationResult publishes a kind:7966 remediation result event.
func (r *Reactor) publishRemediationResult(ctx context.Context, requestEvent *nostr.Event, state *domain.EnvironmentServiceState, status, message string) error {
	content, _ := json.Marshal(map[string]interface{}{
		"service_id":     state.ServiceID.String(),
		"environment_id": state.EnvironmentID.String(),
		"drift_status":   string(state.DriftStatus),
		"status":         status,
		"message":        message,
	})

	event := &nostr.Event{
		Kind:      KindRemediationResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", status},
			{"service", state.ServiceID.String()},
			{"environment", state.EnvironmentID.String()},
		},
		Content: string(content),
	}
	if state.DesiredArtifactID != nil {
		event.Tags = append(event.Tags, nostr.Tag{"artifact", state.DesiredArtifactID.String()})
	}
	if state.DesiredIntentID != nil {
		event.Tags = append(event.Tags, nostr.Tag{"intent", state.DesiredIntentID.String()})
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign remediation result: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// publishStateEvent publishes a replaceable kind:31961 service state event.
// The d-tag is formatted as "service:env" to allow per-service-environment state tracking.
func (r *Reactor) publishStateEvent(ctx context.Context, serviceID, envID uuid.UUID) error {
	state, err := r.registry.GetEnvironmentServiceState(ctx, serviceID, envID)
	if err != nil {
		return fmt.Errorf("get state: %w", err)
	}

	// Build state content
	content := map[string]interface{}{
		"service_id":     state.ServiceID.String(),
		"environment_id": state.EnvironmentID.String(),
		"drift_status":   string(state.DriftStatus),
		"deleted":        false,
		"updated_at":     state.UpdatedAt.Format(time.RFC3339),
	}
	if state.DesiredArtifactID != nil {
		content["desired_artifact_id"] = state.DesiredArtifactID.String()
	}
	if state.DesiredIntentID != nil {
		content["desired_intent_id"] = state.DesiredIntentID.String()
	}
	if state.LastSuccessfulRunID != nil {
		content["last_successful_run_id"] = state.LastSuccessfulRunID.String()
	}
	if state.CurrentObservationID != nil {
		content["current_observation_id"] = state.CurrentObservationID.String()
	}
	if state.LastReconciledAt != nil {
		content["last_reconciled_at"] = state.LastReconciledAt.Format(time.RFC3339)
	}

	contentJSON, _ := json.Marshal(content)
	dTag := fmt.Sprintf("%s:%s", serviceID.String(), envID.String())

	event := &nostr.Event{
		Kind:      KindServiceState,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", dTag},
			{"service", serviceID.String()},
			{"environment", envID.String()},
			{"deleted", "false"},
			{"drift_status", string(state.DriftStatus)},
		},
		Content: string(contentJSON),
	}
	if state.DesiredArtifactID != nil {
		event.Tags = append(event.Tags, nostr.Tag{"artifact", state.DesiredArtifactID.String()})
	}
	if state.DesiredIntentID != nil {
		event.Tags = append(event.Tags, nostr.Tag{"intent", state.DesiredIntentID.String()})
	}
	if state.LastSuccessfulRunID != nil {
		event.Tags = append(event.Tags, nostr.Tag{"run", state.LastSuccessfulRunID.String()})
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign state event: %w", err)
	}

	_, err = r.pool.Publish(ctx, *event)
	return err
}

// PublishServiceRegistry publishes a replaceable kind:31962 service registry event.
func (r *Reactor) PublishServiceRegistry(ctx context.Context, svc *domain.Service) error {
	content, _ := json.Marshal(map[string]interface{}{
		"deleted":        false,
		"id":             svc.ID.String(),
		"name":           svc.Name,
		"repo_url":       svc.RepoURL,
		"artifact_repo":  svc.ArtifactRepo,
		"default_branch": svc.DefaultBranch,
		"runtime_type":   string(svc.RuntimeType),
		"created_at":     svc.CreatedAt.Format(time.RFC3339),
		"updated_at":     svc.UpdatedAt.Format(time.RFC3339),
	})

	event := &nostr.Event{
		Kind:      KindServiceRegistry,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", svc.ID.String()},
			{"deleted", "false"},
			{"name", svc.Name},
			{"runtime", string(svc.RuntimeType)},
		},
		Content: string(content),
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign service registry event: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// PublishEnvironmentRegistry publishes a replaceable kind:31963 environment registry event.
func (r *Reactor) PublishEnvironmentRegistry(ctx context.Context, env *domain.Environment) error {
	content, _ := json.Marshal(map[string]interface{}{
		"deleted":         false,
		"id":              env.ID.String(),
		"name":            env.Name,
		"protected":       env.Protected,
		"deploy_strategy": string(env.DeployStrategy),
		"created_at":      env.CreatedAt.Format(time.RFC3339),
		"updated_at":      env.UpdatedAt.Format(time.RFC3339),
	})

	event := &nostr.Event{
		Kind:      KindEnvironmentRegistry,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", env.ID.String()},
			{"deleted", "false"},
			{"name", env.Name},
			{"protected", fmt.Sprintf("%t", env.Protected)},
		},
		Content: string(content),
	}

	if err := r.signEvent(ctx, event); err != nil {
		return fmt.Errorf("sign environment registry event: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// cleanupRuns periodically removes completed runs older than 1 hour.
func (r *Reactor) cleanupRuns(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			cutoff := time.Now().Add(-1 * time.Hour)
			deleted := 0
			for id, run := range r.runs {
				if run.CompletedAt != nil && run.CompletedAt.Before(cutoff) {
					delete(r.runs, id)
					deleted++
				}
			}
			r.mu.Unlock()
			if deleted > 0 {
				r.logger.Info("cleaned up completed runs", "deleted", deleted)
			}
		}
	}
}
