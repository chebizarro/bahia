package reconcile

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// RemediationAction defines what happens during auto-remediation.
type RemediationAction string

const (
	ActionRedeploy RemediationAction = "redeploy"
	ActionRollback RemediationAction = "rollback"
	ActionNone     RemediationAction = "none"
)

// RemediationConfig configures automatic remediation for an environment.
// Stored in Environment.RuntimeConfig["auto_remediation"].
type RemediationConfig struct {
	Enabled         bool              `json:"enabled"`
	MaxRetries      int               `json:"max_retries"`       // max consecutive attempts (default: 3)
	Cooldown        time.Duration     `json:"cooldown"`          // wait between attempts (default: 5m)
	OnDrift         RemediationAction `json:"on_drift"`          // action on drift (default: redeploy)
	OnHealthFailure RemediationAction `json:"on_health_failure"` // action on health failure (default: rollback)
}

// DefaultRemediationConfig returns sensible defaults.
func DefaultRemediationConfig() RemediationConfig {
	return RemediationConfig{
		Enabled:         false,
		MaxRetries:      3,
		Cooldown:        5 * time.Minute,
		OnDrift:         ActionRedeploy,
		OnHealthFailure: ActionRollback,
	}
}

// remediationState tracks per-service remediation attempts.
type remediationState struct {
	attempts   int
	lastAction time.Time
	inProgress bool
}

// Remediator handles automatic remediation of drift and health failures.
type Remediator struct {
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	artifacts    repository.ArtifactRepository
	state        repository.EnvironmentServiceStateRepository
	rt           runtime.Runtime
	publisher    events.Publisher
	logger       *zap.Logger

	mu     sync.Mutex
	states map[string]*remediationState // keyed by "serviceID:envID"
}

// NewRemediator creates a new Remediator.
func NewRemediator(
	services repository.ServiceRepository,
	environments repository.EnvironmentRepository,
	artifacts repository.ArtifactRepository,
	state repository.EnvironmentServiceStateRepository,
	rt runtime.Runtime,
	publisher events.Publisher,
	logger *zap.Logger,
) *Remediator {
	return &Remediator{
		services:     services,
		environments: environments,
		artifacts:    artifacts,
		state:        state,
		rt:           rt,
		publisher:    publisher,
		logger:       logger,
		states:       make(map[string]*remediationState),
	}
}

// OnDriftDetected is called when drift is detected.
// It checks if auto-remediation is enabled and triggers the configured action.
func (r *Remediator) OnDriftDetected(ctx context.Context, serviceID, envID uuid.UUID) error {
	cfg, err := r.getConfig(ctx, envID)
	if err != nil || !cfg.Enabled {
		return err
	}

	if cfg.OnDrift == ActionNone {
		return nil
	}

	key := stateKey(serviceID, envID)
	if !r.canRemediate(key, cfg) {
		r.logger.Info("skipping drift remediation (cooldown or max retries)",
			zap.String("service_id", serviceID.String()),
			zap.String("environment_id", envID.String()),
		)
		return nil
	}

	return r.remediate(ctx, serviceID, envID, cfg.OnDrift, "drift")
}

// OnHealthFailure is called when a health check fails.
// It checks if auto-remediation is enabled and triggers the configured action.
func (r *Remediator) OnHealthFailure(ctx context.Context, serviceID, envID uuid.UUID) error {
	cfg, err := r.getConfig(ctx, envID)
	if err != nil || !cfg.Enabled {
		return err
	}

	if cfg.OnHealthFailure == ActionNone {
		return nil
	}

	key := stateKey(serviceID, envID)
	if !r.canRemediate(key, cfg) {
		r.logger.Info("skipping health remediation (cooldown or max retries)",
			zap.String("service_id", serviceID.String()),
			zap.String("environment_id", envID.String()),
		)
		return nil
	}

	return r.remediate(ctx, serviceID, envID, cfg.OnHealthFailure, "health_failure")
}

// ResetState clears the remediation state for a service/environment pair.
// Call this after a successful deployment to reset the retry counter.
func (r *Remediator) ResetState(serviceID, envID uuid.UUID) {
	key := stateKey(serviceID, envID)
	r.mu.Lock()
	delete(r.states, key)
	r.mu.Unlock()
}

func (r *Remediator) getConfig(ctx context.Context, envID uuid.UUID) (RemediationConfig, error) {
	cfg := DefaultRemediationConfig()

	env, err := r.environments.GetByID(ctx, envID)
	if err != nil || env == nil {
		return cfg, err
	}

	raw, ok := env.RuntimeConfig["auto_remediation"]
	if !ok {
		return cfg, nil
	}

	cfgMap, ok := raw.(map[string]any)
	if !ok {
		return cfg, nil
	}

	if enabled, ok := cfgMap["enabled"].(bool); ok {
		cfg.Enabled = enabled
	}
	if maxRetries, ok := cfgMap["max_retries"].(float64); ok {
		cfg.MaxRetries = int(maxRetries)
	}
	if cooldownStr, ok := cfgMap["cooldown"].(string); ok {
		if d, err := time.ParseDuration(cooldownStr); err == nil {
			cfg.Cooldown = d
		}
	}
	if cooldownSec, ok := cfgMap["cooldown_seconds"].(float64); ok {
		cfg.Cooldown = time.Duration(cooldownSec) * time.Second
	}
	if onDrift, ok := cfgMap["on_drift"].(string); ok {
		cfg.OnDrift = RemediationAction(onDrift)
	}
	if onHealth, ok := cfgMap["on_health_failure"].(string); ok {
		cfg.OnHealthFailure = RemediationAction(onHealth)
	}

	return cfg, nil
}

func (r *Remediator) canRemediate(key string, cfg RemediationConfig) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.states[key]
	if !ok {
		return true
	}

	// Check if already in progress.
	if state.inProgress {
		return false
	}

	// Check max retries.
	if cfg.MaxRetries > 0 && state.attempts >= cfg.MaxRetries {
		return false
	}

	// Check cooldown.
	if time.Since(state.lastAction) < cfg.Cooldown {
		return false
	}

	return true
}

func (r *Remediator) markInProgress(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.states[key]
	if !ok {
		state = &remediationState{}
		r.states[key] = state
	}
	state.inProgress = true
	state.attempts++
	state.lastAction = time.Now()
}

func (r *Remediator) markComplete(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if state, ok := r.states[key]; ok {
		state.inProgress = false
	}
}

func (r *Remediator) remediate(ctx context.Context, serviceID, envID uuid.UUID, action RemediationAction, trigger string) error {
	key := stateKey(serviceID, envID)
	r.markInProgress(key)
	defer r.markComplete(key)

	svc, err := r.services.GetByID(ctx, serviceID)
	if err != nil || svc == nil {
		return err
	}

	r.logger.Info("starting auto-remediation",
		zap.String("service", svc.Name),
		zap.String("action", string(action)),
		zap.String("trigger", trigger),
	)

	r.publisher.Publish(ctx, events.Event{
		Type:     "remediation.started",
		EntityID: serviceID.String(),
		Data: map[string]string{
			"action":         string(action),
			"trigger":        trigger,
			"service":        svc.Name,
			"environment_id": envID.String(),
		},
	})

	var remediateErr error
	switch action {
	case ActionRedeploy:
		remediateErr = r.redeploy(ctx, serviceID, envID, svc.Name)
	case ActionRollback:
		remediateErr = r.rollback(ctx, serviceID, envID, svc.Name)
	default:
		r.logger.Warn("unknown remediation action", zap.String("action", string(action)))
		return nil
	}

	if remediateErr != nil {
		r.logger.Error("remediation failed",
			zap.String("service", svc.Name),
			zap.String("action", string(action)),
			zap.Error(remediateErr),
		)
		r.publisher.Publish(ctx, events.Event{
			Type:     "remediation.failed",
			EntityID: serviceID.String(),
			Data: map[string]string{
				"action": string(action),
				"error":  remediateErr.Error(),
			},
		})
		return remediateErr
	}

	r.logger.Info("remediation completed",
		zap.String("service", svc.Name),
		zap.String("action", string(action)),
	)
	r.publisher.Publish(ctx, events.Event{
		Type:     "remediation.completed",
		EntityID: serviceID.String(),
		Data: map[string]string{
			"action":  string(action),
			"trigger": trigger,
		},
	})

	return nil
}

func (r *Remediator) redeploy(ctx context.Context, serviceID, envID uuid.UUID, serviceName string) error {
	// Get the desired artifact for this service/environment.
	st, err := r.state.Get(ctx, serviceID, envID)
	if err != nil || st == nil || st.DesiredArtifactID == nil {
		return err
	}

	artifact, err := r.artifacts.GetByID(ctx, *st.DesiredArtifactID)
	if err != nil || artifact == nil {
		return err
	}

	image := artifact.ImageRepo + ":" + artifact.ImageTag
	if artifact.ImageDigest != "" {
		image = artifact.ImageRepo + "@" + artifact.ImageDigest
	}

	// Deploy using the runtime.
	return r.rt.Deploy(ctx, serviceName, image, runtime.DeployOptions{
		Labels: map[string]string{
			"bahia.service":     serviceName,
			"bahia.remediation": "redeploy",
		},
		PullAlways: true,
	})
}

func (r *Remediator) rollback(ctx context.Context, serviceID, envID uuid.UUID, serviceName string) error {
	// For rollback, we'd ideally deploy the previous known-good artifact.
	// For now, we'll just undeploy the canary/green slots and let the original run.
	_ = r.rt.Undeploy(ctx, serviceName+"-canary")
	_ = r.rt.Undeploy(ctx, serviceName+"-green")

	r.logger.Info("rollback: removed canary/green deployments",
		zap.String("service", serviceName),
	)

	return nil
}

// GetAttempts returns the current attempt count for a service/environment.
func (r *Remediator) GetAttempts(serviceID, envID uuid.UUID) int {
	key := stateKey(serviceID, envID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if state, ok := r.states[key]; ok {
		return state.attempts
	}
	return 0
}

func stateKey(serviceID, envID uuid.UUID) string {
	return serviceID.String() + ":" + envID.String()
}
