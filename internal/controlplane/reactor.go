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

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"go.uber.org/zap"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// Event kinds for Bahia control plane (596x/696x/796x series).
const (
	// Request kinds (5961-5969)
	KindDeployRequest       = 5961 // Request to deploy a service
	KindRollbackRequest     = 5962 // Request to rollback a service
	KindServiceAction       = 5963 // Lifecycle action (scale, restart, stop)
	KindServiceCreate       = 5964 // Create a new service
	KindEnvironmentCreate   = 5965 // Create a new environment
	KindDeploymentApproval  = 5966 // Approve or reject a deployment

	// Status kinds (6961-6969)
	KindDeploymentStatus   = 6961 // Deployment progress updates
	KindServiceStatus      = 6962 // Service health/state updates

	// Result kinds (7961-7969)
	KindDeploymentResult   = 7961 // Final deployment result
	KindActionResult       = 7962 // Result of a service action
)

// DefaultAuthorizedPubkeys is the list of pubkeys allowed to control Bahia via Nostr.
var DefaultAuthorizedPubkeys = []string{
	"cdee943cbb19c51ab847a66d5d774373aa9f63d287246bb59b0827fa5e637400", // Biz
	"14907326f89ebdfc9cfdabe17bd492aa48abbd59ad5d8cc25295760bdf0e5015", // Stew
}

// Config holds reactor configuration.
type Config struct {
	// Relays is the list of public relay URLs for subscriptions and results.
	Relays []string
	// PrivateRelays is the list of relay URLs for private/draft events.
	PrivateRelays []string
	// PrivateKey is the hex-encoded signing key for event publishing.
	PrivateKey string
	// AuthorizedPubkeys is the list of pubkeys allowed to submit requests.
	AuthorizedPubkeys []string
}

// Reactor subscribes to Nostr control plane events and dispatches handlers.
type Reactor struct {
	config   Config
	pool     *nostrpool.RelayPool
	registry *service.RegistryService
	logger   *slog.Logger
	zapLog   *zap.Logger

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

// NewReactor creates a new Bahia control plane reactor.
func NewReactor(config Config, registry *service.RegistryService, zapLog *zap.Logger) *Reactor {
	if zapLog == nil {
		zapLog = zap.NewNop()
	}
	
	// Create relay pool with private key for NIP-42 auth
	poolOpts := []nostrpool.RelayPoolOption{}
	if config.PrivateKey != "" {
		poolOpts = append(poolOpts, nostrpool.WithPrivateKey(config.PrivateKey))
	}
	
	allRelays := append(config.Relays, config.PrivateRelays...)
	pool := nostrpool.NewRelayPool(allRelays, zapLog, poolOpts...)

	return &Reactor{
		config:   config,
		pool:     pool,
		registry: registry,
		logger:   slog.Default().With("component", "controlplane"),
		zapLog:   zapLog,
		runs:     make(map[string]*DeploymentRun),
	}
}

// Run starts the reactor and blocks until context is cancelled.
func (r *Reactor) Run(ctx context.Context) error {
	r.logger.Info("starting bahia control plane reactor",
		"relays", r.config.Relays,
		"private_relays", r.config.PrivateRelays,
	)

	// Connect to relays
	r.pool.Connect(ctx)

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
				r.logger.Warn("subscription closed, reconnecting...")
				time.Sleep(5 * time.Second)
				merged, err = r.pool.SubscribeAllWithEOSE(ctx, filters)
				if err != nil {
					r.logger.Error("reconnect failed", "error", err)
					continue
				}
				continue
			}
			
			r.handleEvent(ctx, ev)
		}
	}
}

// handleEvent dispatches events to the appropriate handler.
func (r *Reactor) handleEvent(ctx context.Context, event *nostr.Event) {
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

// isAuthorized checks if a pubkey is authorized to use the control plane.
func (r *Reactor) isAuthorized(pubkey string) bool {
	authorized := r.config.AuthorizedPubkeys
	if len(authorized) == 0 {
		authorized = DefaultAuthorizedPubkeys
	}
	return slices.Contains(authorized, pubkey)
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

// publishStatus publishes a kind:6961 deployment status event.
func (r *Reactor) publishStatus(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	event := &nostr.Event{
		Kind:      KindDeploymentStatus,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "processing"},
			{"step", step},
		},
		Content: message,
	}

	if err := r.signEvent(event); err != nil {
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
			{"intent", intent.ID.String()},
		},
		Content: string(content),
	}

	if err := r.signEvent(event); err != nil {
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

	if signErr := r.signEvent(event); signErr != nil {
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

	event := &nostr.Event{
		Kind:      KindActionResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "success"},
			{"intent", intentID},
			{"decision", decision},
		},
		Content: string(content),
	}

	if err := r.signEvent(event); err != nil {
		return fmt.Errorf("sign approval result: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
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
		Kind:      KindActionResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "success"},
			{"service", svc.ID.String()},
		},
		Content: string(content),
	}

	if err := r.signEvent(event); err != nil {
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
		Kind:      KindActionResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "success"},
			{"environment", env.ID.String()},
		},
		Content: string(content),
	}

	if err := r.signEvent(event); err != nil {
		return fmt.Errorf("sign environment created: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// publishError publishes a kind:7961 error result event.
func (r *Reactor) publishError(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	event := &nostr.Event{
		Kind:      KindDeploymentResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "error"},
			{"step", step},
			{"error", step},
		},
		Content: message,
	}

	if err := r.signEvent(event); err != nil {
		return fmt.Errorf("sign error event: %w", err)
	}

	_, err := r.pool.Publish(ctx, *event)
	return err
}

// signEvent signs an event with the configured private key.
func (r *Reactor) signEvent(event *nostr.Event) error {
	if r.config.PrivateKey == "" {
		return fmt.Errorf("no private key configured")
	}
	return event.Sign(r.config.PrivateKey)
}

// GetRun returns the current deployment run for a request event.
func (r *Reactor) GetRun(requestEventID string) *DeploymentRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[requestEventID]
}
