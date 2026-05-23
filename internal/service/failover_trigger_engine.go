package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

const (
	// EventFailoverRequested is emitted once when heartbeat loss crosses from suspect to firing.
	EventFailoverRequested events.EventType = "continuity.failover_requested"
	// EventFailoverRequestFailed is emitted once when heartbeat loss fires but no standby can take over.
	EventFailoverRequestFailed events.EventType = "continuity.failover_failed"
)

// TriggerPhase is the automatic failover trigger state for one service.
type TriggerPhase string

const (
	TriggerPhaseHealthy TriggerPhase = "healthy"
	TriggerPhaseSuspect TriggerPhase = "suspect"
	TriggerPhaseFiring  TriggerPhase = "firing"
	TriggerPhaseActive  TriggerPhase = "active"
)

// TriggerState records automatic failover state for one managed service.
type TriggerState struct {
	ServiceKey   string
	Phase        TriggerPhase
	LastChangeAt time.Time
	ActiveRunID  string
}

// FailoverRequested is the in-process command emitted by the trigger engine.
type FailoverRequested struct {
	RunID               string
	ServiceKey          string
	RecipeName          string
	PrimaryWorkerPubKey string
	StandbyWorkerPubKey string
	TriggerType         string
	TriggerTarget       string
	RequestedAt         time.Time
	LastHeartbeatAt     time.Time
	Reason              string
}

// FailoverRequestFailed records that an automatic failover trigger could not be converted into a run.
type FailoverRequestFailed struct {
	RunID               string
	ServiceKey          string
	RecipeName          string
	PrimaryWorkerPubKey string
	TriggerType         string
	TriggerTarget       string
	FailedAt            time.Time
	LastHeartbeatAt     time.Time
	Reason              string
}

// FailoverTriggerEngine evaluates heartbeat-loss triggers for managed continuity services.
type FailoverTriggerEngine struct {
	heartbeats  HeartbeatMonitor
	definitions ContinuityDefinitionStore
	publisher   events.Publisher
	interval    time.Duration
	logger      *zap.Logger
	clock       func() time.Time

	mu     sync.RWMutex
	states map[string]TriggerState
}

// NewFailoverTriggerEngine constructs a ticker-driven automatic failover evaluator.
func NewFailoverTriggerEngine(
	heartbeats HeartbeatMonitor,
	definitions ContinuityDefinitionStore,
	publisher events.Publisher,
	interval time.Duration,
	logger *zap.Logger,
) (*FailoverTriggerEngine, error) {
	if heartbeats == nil {
		return nil, fmt.Errorf("heartbeat monitor is required")
	}
	if definitions == nil {
		return nil, fmt.Errorf("continuity definition store is required")
	}
	if publisher == nil {
		return nil, fmt.Errorf("event publisher is required")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("failover trigger interval must be positive")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FailoverTriggerEngine{
		heartbeats:  heartbeats,
		definitions: definitions,
		publisher:   publisher,
		interval:    interval,
		logger:      logger,
		clock:       func() time.Time { return time.Now().UTC() },
		states:      make(map[string]TriggerState),
	}, nil
}

// Run starts the failover trigger loop. It blocks until ctx is cancelled.
func (e *FailoverTriggerEngine) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.logger.Info("failover trigger engine started", zap.Duration("interval", e.interval))
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	e.EvaluateOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			e.logger.Info("failover trigger engine stopped")
			return
		case <-ticker.C:
			e.EvaluateOnce(ctx)
		}
	}
}

// EvaluateOnce evaluates every managed service once using the engine clock.
func (e *FailoverTriggerEngine) EvaluateOnce(ctx context.Context) {
	if e == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		e.logger.Debug("skipping failover trigger evaluation", zap.Error(err))
		return
	}
	now := e.now()
	profiles := e.definitions.ListProfiles()
	for i := range profiles {
		if err := ctx.Err(); err != nil {
			e.logger.Debug("stopping failover trigger evaluation", zap.Error(err))
			return
		}
		e.evaluateService(ctx, profiles[i], now)
	}
}

// State returns the current trigger state for serviceKey.
func (e *FailoverTriggerEngine) State(serviceKey string) (TriggerState, bool) {
	if e == nil {
		return TriggerState{}, false
	}
	serviceKey = strings.TrimSpace(serviceKey)
	e.mu.RLock()
	state, ok := e.states[serviceKey]
	e.mu.RUnlock()
	return state, ok
}

// MarkActive records that an external status projector observed the requested run active.
func (e *FailoverTriggerEngine) MarkActive(serviceKey string, runID string, changedAt time.Time) bool {
	if e == nil {
		return false
	}
	serviceKey = strings.TrimSpace(serviceKey)
	runID = strings.TrimSpace(runID)
	if serviceKey == "" || runID == "" {
		return false
	}
	if changedAt.IsZero() {
		changedAt = e.now()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	current, ok := e.states[serviceKey]
	if !ok || current.ActiveRunID != runID || current.Phase != TriggerPhaseFiring {
		return false
	}
	e.states[serviceKey] = TriggerState{ServiceKey: serviceKey, Phase: TriggerPhaseActive, LastChangeAt: changedAt, ActiveRunID: runID}
	return true
}

func (e *FailoverTriggerEngine) evaluateService(ctx context.Context, profile domain.ServiceContinuityProfile, now time.Time) {
	serviceKey := strings.TrimSpace(profile.ServiceKey)
	if serviceKey == "" {
		e.logger.Warn("continuity profile without service key skipped")
		return
	}

	recipe, ok := e.definitions.GetRecipe(serviceKey, domain.ContinuityRecipeKindFailover)
	if !ok || recipe.Trigger == nil || strings.TrimSpace(recipe.Trigger.Type) != domain.RecipeTriggerTypeHeartbeatLoss {
		return
	}

	primary := triggerTargetWorker(profile, recipe)
	if primary == "" {
		e.logger.Warn("heartbeat-loss recipe has no primary worker target", zap.String("service_key", serviceKey), zap.String("recipe", recipe.Name))
		return
	}

	snapshot, ok := e.heartbeats.Snapshot(primary)
	if !ok {
		e.logger.Debug("failover trigger unarmed without heartbeat snapshot", zap.String("service_key", serviceKey), zap.String("primary_worker", primary))
		return
	}

	if !heartbeatLossThresholdCrossed(snapshot, recipe.Trigger.Timeout, now) {
		e.transitionIfNotActive(serviceKey, TriggerPhaseHealthy, "", now)
		return
	}

	state, hasState := e.State(serviceKey)
	if hasState && (state.Phase == TriggerPhaseFiring || state.Phase == TriggerPhaseActive) && state.ActiveRunID != "" {
		return
	}
	if !hasState || state.Phase != TriggerPhaseSuspect {
		e.setState(serviceKey, TriggerPhaseSuspect, "", now)
		return
	}

	standby, ok := e.selectEligibleStandby(serviceKey, primary)
	runID := failoverRunID(serviceKey, now)
	if !ok {
		reason := "heartbeat-loss trigger fired but no eligible standby replication target exists"
		e.setState(serviceKey, TriggerPhaseFiring, runID, now)
		e.publisher.Publish(ctx, events.Event{
			Type:     EventFailoverRequestFailed,
			EntityID: serviceKey,
			Data: FailoverRequestFailed{
				RunID:               runID,
				ServiceKey:          serviceKey,
				RecipeName:          recipe.Name,
				PrimaryWorkerPubKey: primary,
				TriggerType:         recipe.Trigger.Type,
				TriggerTarget:       recipe.Trigger.Target,
				FailedAt:            now,
				LastHeartbeatAt:     snapshot.LastObservedAt,
				Reason:              reason,
			},
		})
		return
	}

	reason := fmt.Sprintf("heartbeat for primary worker %s exceeded %s", primary, recipe.Trigger.Timeout)
	e.setState(serviceKey, TriggerPhaseFiring, runID, now)
	e.publisher.Publish(ctx, events.Event{
		Type:     EventFailoverRequested,
		EntityID: serviceKey,
		Data: FailoverRequested{
			RunID:               runID,
			ServiceKey:          serviceKey,
			RecipeName:          recipe.Name,
			PrimaryWorkerPubKey: primary,
			StandbyWorkerPubKey: standby,
			TriggerType:         recipe.Trigger.Type,
			TriggerTarget:       recipe.Trigger.Target,
			RequestedAt:         now,
			LastHeartbeatAt:     snapshot.LastObservedAt,
			Reason:              reason,
		},
	})
}

func (e *FailoverTriggerEngine) selectEligibleStandby(serviceKey string, primaryWorkerPubKey string) (string, bool) {
	policy, ok := e.definitions.GetReplicationPolicy(serviceKey)
	if !ok {
		return "", false
	}
	for _, target := range policy.Targets {
		worker := strings.TrimSpace(target.WorkerPubKey)
		if worker == "" || worker == primaryWorkerPubKey {
			continue
		}
		if replicationTargetSupportsFailover(target) {
			return worker, true
		}
	}
	return "", false
}

func (e *FailoverTriggerEngine) transitionIfNotActive(serviceKey string, phase TriggerPhase, runID string, changedAt time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	current, ok := e.states[serviceKey]
	if ok && (current.Phase == TriggerPhaseFiring || current.Phase == TriggerPhaseActive) && current.ActiveRunID != "" {
		return
	}
	if ok && current.Phase == phase && current.ActiveRunID == runID {
		return
	}
	e.states[serviceKey] = TriggerState{ServiceKey: serviceKey, Phase: phase, LastChangeAt: changedAt, ActiveRunID: runID}
}

func (e *FailoverTriggerEngine) setState(serviceKey string, phase TriggerPhase, runID string, changedAt time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	current, ok := e.states[serviceKey]
	if ok && current.Phase == phase && current.ActiveRunID == runID {
		return
	}
	e.states[serviceKey] = TriggerState{ServiceKey: serviceKey, Phase: phase, LastChangeAt: changedAt, ActiveRunID: runID}
}

func (e *FailoverTriggerEngine) now() time.Time {
	if e.clock != nil {
		return e.clock()
	}
	return time.Now().UTC()
}

func triggerTargetWorker(profile domain.ServiceContinuityProfile, recipe domain.ContinuityRecipe) string {
	primary := strings.TrimSpace(profile.PrimaryWorkerPubKey)
	if recipe.Trigger == nil {
		return primary
	}
	target := strings.TrimSpace(recipe.Trigger.Target)
	if target == "" || target == "primary" || target == primary {
		return primary
	}
	return target
}

func heartbeatLossThresholdCrossed(snapshot HeartbeatSnapshot, triggerTimeout time.Duration, now time.Time) bool {
	if snapshot.LastObservedAt.IsZero() {
		return false
	}
	threshold := triggerTimeout
	if threshold <= 0 {
		threshold = snapshot.ExpiresAfter
	}
	if threshold <= 0 {
		return snapshot.Status == domain.HeartbeatStatusExpired
	}
	return !now.Before(snapshot.LastObservedAt.Add(threshold))
}

func replicationTargetSupportsFailover(target domain.ReplicationTarget) bool {
	if len(target.RequiredForModes) == 0 {
		return true
	}
	for _, mode := range target.RequiredForModes {
		switch mode {
		case domain.ContinuityModeFull, domain.ContinuityModeDegraded, domain.ContinuityModeEmergency:
			return true
		}
	}
	return false
}

func failoverRunID(serviceKey string, at time.Time) string {
	return fmt.Sprintf("failover:%s:%d", strings.TrimSpace(serviceKey), at.UnixNano())
}
