package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const defaultRestartLoopThreshold = 3

// ManagedInstanceTryLocker is the non-blocking subset of the runtime apply lock.
type ManagedInstanceTryLocker interface {
	TryLock(context.Context, uuid.UUID) (func(), bool, error)
}

// InstanceProber performs an optional readiness probe.
type InstanceProber interface {
	Probe(context.Context, runtime.HTTPProbeConfig) runtime.HTTPProbeResult
}

// SupervisionSpec binds one exact runtime target to its observer, controller, and policy.
// Specs may be loaded from configuration or derived from Bahia-managed deployment units.
type SupervisionSpec struct {
	Key                    domain.ManagedInstanceKey
	Host                   string
	SupervisorType         domain.InstanceSupervisorType
	ProbeConfig            *runtime.HTTPProbeConfig
	RecoveryPolicy         domain.RecoveryPolicy
	DesiredRunning         bool
	Observer               runtime.HealthObserver
	Controller             runtime.ManagedInstanceController
	Prober                 InstanceProber
	MemoryThresholdRatio   float64
	HighMemorySustainCount int
	RestartLoopThreshold   int
}

// SupervisionSpecSource enumerates the current supervised target set.
type SupervisionSpecSource interface {
	SupervisionSpecs(context.Context) ([]SupervisionSpec, error)
}

// StaticSupervisionSpecSource is useful for configuration-driven targets.
type StaticSupervisionSpecSource []SupervisionSpec

func (s StaticSupervisionSpecSource) SupervisionSpecs(context.Context) ([]SupervisionSpec, error) {
	return append([]SupervisionSpec(nil), s...), nil
}

// ManagedInstanceHealthChanged is the internal health transition/read-model payload.
type ManagedInstanceHealthChanged struct {
	EventID        string                       `json:"event_id"`
	Health         domain.ManagedInstanceHealth `json:"health"`
	PreviousStatus domain.InstanceHealthStatus  `json:"previous_status,omitempty"`
	Severity       domain.AlertSeverity         `json:"severity"`
	Alert          bool                         `json:"alert"`
	Reason         string                       `json:"reason,omitempty"`
	OccurredAt     time.Time                    `json:"occurred_at"`
}

// ShouldNotify lets the notification dispatcher honor supervisor alert policy.
func (e ManagedInstanceHealthChanged) ShouldNotify() bool { return e.Alert }

// ManagedInstanceRecoveryEvent is the internal recovery audit payload.
type ManagedInstanceRecoveryEvent struct {
	EventID    string                       `json:"event_id"`
	Health     domain.ManagedInstanceHealth `json:"health"`
	Decision   domain.RecoveryDecision      `json:"decision"`
	Attempt    domain.RecoveryAttempt       `json:"attempt"`
	Severity   domain.AlertSeverity         `json:"severity"`
	OccurredAt time.Time                    `json:"occurred_at"`
}

func (ManagedInstanceRecoveryEvent) ShouldNotify() bool { return true }

// ManagedInstanceMaintenanceEvent is the internal maintenance audit payload.
type ManagedInstanceMaintenanceEvent struct {
	EventID    string                        `json:"event_id"`
	Health     *domain.ManagedInstanceHealth `json:"health,omitempty"`
	Override   domain.MaintenanceOverride    `json:"override"`
	Active     bool                          `json:"active"`
	OccurredAt time.Time                     `json:"occurred_at"`
}

func (ManagedInstanceMaintenanceEvent) ShouldNotify() bool { return false }

// ManagedInstanceSupervisor observes local managed instances and performs narrowly scoped recovery.
type ManagedInstanceSupervisor struct {
	source    SupervisionSpecSource
	repo      repository.ManagedInstanceHealthRepository
	lock      ManagedInstanceTryLocker
	publisher events.Publisher
	interval  time.Duration
	logger    *zap.Logger
	now       func() time.Time

	mu             sync.Mutex
	highMemoryRuns map[string]int
	lastAlerts     map[string]time.Time
}

func NewManagedInstanceSupervisor(source SupervisionSpecSource, repo repository.ManagedInstanceHealthRepository, lock ManagedInstanceTryLocker, publisher events.Publisher, interval time.Duration, logger *zap.Logger) (*ManagedInstanceSupervisor, error) {
	if source == nil || repo == nil {
		return nil, fmt.Errorf("managed instance supervisor requires spec source and health repository")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("managed instance supervisor interval must be positive")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ManagedInstanceSupervisor{source: source, repo: repo, lock: lock, publisher: publisher, interval: interval, logger: logger.Named("managed-instance-supervisor"), now: func() time.Time { return time.Now().UTC() }, highMemoryRuns: map[string]int{}, lastAlerts: map[string]time.Time{}}, nil
}

func (s *ManagedInstanceSupervisor) Name() string { return "managed-instance-supervisor" }

func (s *ManagedInstanceSupervisor) Run(ctx context.Context) error {
	if err := s.EvaluateOnce(ctx); err != nil {
		s.logger.Warn("managed instance evaluation failed", zap.Error(err))
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.EvaluateOnce(ctx); err != nil {
				s.logger.Warn("managed instance evaluation failed", zap.Error(err))
			}
		}
	}
}

func (s *ManagedInstanceSupervisor) EvaluateOnce(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	specs, err := s.source.SupervisionSpecs(ctx)
	if err != nil {
		return fmt.Errorf("enumerate supervision specs: %w", err)
	}
	sort.Slice(specs, func(i, j int) bool { return instanceKeyString(specs[i].Key) < instanceKeyString(specs[j].Key) })
	var errs []error
	for i := range specs {
		if ctx.Err() != nil {
			return errors.Join(append(errs, ctx.Err())...)
		}
		if err := s.evaluateSpec(ctx, &specs[i]); err != nil {
			errs = append(errs, fmt.Errorf("supervise %s: %w", instanceKeyString(specs[i].Key), err))
		}
	}
	return errors.Join(errs...)
}

func (s *ManagedInstanceSupervisor) evaluateSpec(ctx context.Context, spec *SupervisionSpec) error {
	if spec.Observer == nil {
		return fmt.Errorf("health observer is required")
	}
	previous, err := s.repo.GetHealth(ctx, spec.Key)
	if err != nil {
		return err
	}
	obs, observeErr := spec.Observer.ObserveInstance(ctx, spec.Key)
	health := s.classify(ctx, spec, previous, obs, observeErr)
	if previous != nil && health.LastObservedAt.Before(previous.LastObservedAt) {
		return nil
	}
	material := previous == nil || health.Status != previous.Status || health.FailureReason != previous.FailureReason || health.RestartCount != previous.RestartCount || health.MemoryCurrentBytes != previous.MemoryCurrentBytes || health.MemoryPeakBytes != previous.MemoryPeakBytes || health.MemoryLimitBytes != previous.MemoryLimitBytes
	if material {
		if err := s.repo.UpsertHealth(ctx, &health); err != nil {
			return err
		}
		e := domain.ManagedInstanceHealthEvent{ID: uuid.New(), ManagedInstanceKey: spec.Key, Status: health.Status, Reason: health.FailureReason, Evidence: health.FailureReason, ObservedAt: health.LastObservedAt}
		if previous != nil {
			e.PreviousStatus = previous.Status
		}
		if err := s.repo.AppendHealthEvent(ctx, &e); err != nil {
			return err
		}
		severity := healthSeverity(health.Status, s.highMemoryRuns[instanceKeyString(spec.Key)] >= sustainCount(spec))
		alert := s.shouldAlert(spec, severity, health.LastObservedAt)
		s.publish(ctx, events.EventRuntimeInstanceHealthChanged, instanceKeyString(spec.Key), ManagedInstanceHealthChanged{EventID: e.ID.String(), Health: health, PreviousStatus: e.PreviousStatus, Severity: severity, Alert: alert, Reason: health.FailureReason, OccurredAt: health.LastObservedAt})
	}
	return s.recover(ctx, spec, &health)
}

func (s *ManagedInstanceSupervisor) classify(ctx context.Context, spec *SupervisionSpec, previous *domain.ManagedInstanceHealth, obs *runtime.InstanceObservation, observeErr error) domain.ManagedInstanceHealth {
	now := s.now()
	health := domain.ManagedInstanceHealth{ManagedInstanceKey: spec.Key, Host: strings.TrimSpace(spec.Host), SupervisorType: spec.SupervisorType, Status: domain.InstanceHealthStatusUnknown, LastObservedAt: now, UpdatedAt: now}
	if previous != nil {
		health.LastRecoveryAttempt = previous.LastRecoveryAttempt
		health.FailureGenerationAt = previous.FailureGenerationAt
	}
	if observeErr != nil {
		health.FailureReason = domain.SanitizeEvidence(observeErr.Error())
		return health
	}
	if obs == nil {
		health.FailureReason = "observer returned no observation"
		return health
	}
	if !obs.ObservedAt.IsZero() {
		health.LastObservedAt = obs.ObservedAt.UTC()
	}
	health.Status, health.RestartCount = obs.Status, obs.RestartCount
	health.MemoryCurrentBytes, health.MemoryPeakBytes, health.MemoryLimitBytes = obs.MemoryCurrentBytes, obs.MemoryPeakBytes, obs.MemoryLimitBytes
	health.FailureReason = domain.SanitizeEvidence(obs.Detail)
	if obs.OOMKilled {
		health.Status = domain.InstanceHealthStatusOOMKilled
	}
	if obs.ProbeResult != nil && !obs.ProbeResult.Successful && (health.Status == domain.InstanceHealthStatusHealthy || health.Status == domain.InstanceHealthStatusRunning) {
		health.Status = domain.InstanceHealthStatusDegraded
		health.FailureReason = domain.SanitizeEvidence(firstManagedEvidence(obs.ProbeResult.Error, obs.ProbeResult.Detail, "readiness probe failed"))
	}
	if spec.ProbeConfig != nil && spec.Prober != nil {
		result := spec.Prober.Probe(ctx, *spec.ProbeConfig)
		if !result.Successful && (health.Status == domain.InstanceHealthStatusHealthy || health.Status == domain.InstanceHealthStatusRunning) {
			health.Status = domain.InstanceHealthStatusDegraded
			health.FailureReason = domain.SanitizeEvidence(firstManagedEvidence(result.Error, result.Detail, "readiness probe failed"))
		}
	}
	if previous != nil {
		delta := health.RestartCount - previous.RestartCount
		if delta > 0 {
			health.ConsecutiveRestartCount = previous.ConsecutiveRestartCount + delta
		} else if delta < 0 {
			health.ConsecutiveRestartCount = max(0, health.RestartCount)
		} else if health.Status == domain.InstanceHealthStatusHealthy || health.Status == domain.InstanceHealthStatusRunning {
			health.ConsecutiveRestartCount = 0
		} else {
			health.ConsecutiveRestartCount = previous.ConsecutiveRestartCount
		}
	} else {
		health.ConsecutiveRestartCount = 0
	}
	threshold := spec.RestartLoopThreshold
	if threshold <= 0 {
		threshold = defaultRestartLoopThreshold
	}
	if health.ConsecutiveRestartCount >= threshold {
		health.Status = domain.InstanceHealthStatusRestartLoop
		health.FailureReason = "restart count increased repeatedly"
	}
	key := instanceKeyString(spec.Key)
	if spec.MemoryThresholdRatio > 0 && health.MemoryLimitBytes > 0 && float64(health.MemoryCurrentBytes)/float64(health.MemoryLimitBytes) >= spec.MemoryThresholdRatio {
		s.highMemoryRuns[key]++
	} else {
		s.highMemoryRuns[key] = 0
	}
	if s.highMemoryRuns[key] >= sustainCount(spec) {
		if health.Status == domain.InstanceHealthStatusHealthy || health.Status == domain.InstanceHealthStatusRunning {
			health.Status = domain.InstanceHealthStatusDegraded
		}
		health.FailureReason = domain.SanitizeEvidence(firstManagedEvidence(health.FailureReason, "sustained high memory usage"))
	}
	if recoveryFailureStatus(health.Status) {
		switch {
		case previous == nil || !recoveryFailureStatus(previous.Status):
			health.FailureGenerationAt = health.LastObservedAt
		case !previous.FailureGenerationAt.IsZero():
			health.FailureGenerationAt = previous.FailureGenerationAt
		default:
			health.FailureGenerationAt = previous.LastObservedAt
		}
	} else {
		health.FailureGenerationAt = time.Time{}
	}
	return health
}

func sustainCount(spec *SupervisionSpec) int {
	if spec.HighMemorySustainCount > 0 {
		return spec.HighMemorySustainCount
	}
	return 2
}

func (s *ManagedInstanceSupervisor) recover(ctx context.Context, spec *SupervisionSpec, health *domain.ManagedInstanceHealth) error {
	override, err := s.repo.GetActiveMaintenanceOverride(ctx, spec.Key, health.LastObservedAt)
	if err != nil {
		return err
	}
	attempts, err := s.repo.ListRecentRecoveryAttempts(ctx, spec.Key, 500)
	if err != nil {
		return err
	}
	budget := spec.RecoveryPolicy.RestartBudget
	budget.AttemptedAt = budget.AttemptedAt[:0]
	for _, attempt := range attempts {
		if attempt.Result != domain.RecoveryAttemptBudgetExhausted && attempt.Result != domain.RecoveryAttemptSkippedOverride {
			budget.AttemptedAt = append(budget.AttemptedAt, attempt.RequestedAt)
		}
	}
	for i := range attempts {
		if attempts[i].Result == domain.RecoveryAttemptPending {
			return s.reconcilePendingAttempt(ctx, health, attempts[i])
		}
	}
	sort.Slice(budget.AttemptedAt, func(i, j int) bool { return budget.AttemptedAt[i].Before(budget.AttemptedAt[j]) })
	decision := domain.EvaluateRecovery(spec.DesiredRunning, *health, spec.RecoveryPolicy, budget, override)
	if decision.Action == domain.RecoveryDecisionBudgetExhausted {
		attempt := s.newAttempt(spec.Key, *health, decision, domain.RecoveryAttemptBudgetExhausted)
		inserted, err := s.repo.RecordRecoveryAttempt(ctx, &attempt)
		if err != nil {
			return err
		}
		if inserted {
			health.LastRecoveryAttempt = &attempt
			_ = s.repo.UpsertHealth(ctx, health)
			s.publish(ctx, events.EventRuntimeRecoveryBudgetExhausted, attempt.CorrelationID, ManagedInstanceRecoveryEvent{EventID: attempt.ID.String(), Health: *health, Decision: decision, Attempt: attempt, Severity: domain.AlertSeverityCritical, OccurredAt: attempt.RequestedAt})
		}
		return nil
	}
	if decision.Action != domain.RecoveryDecisionRestart {
		return nil
	}
	if spec.Controller == nil {
		return fmt.Errorf("managed instance controller is required for recovery")
	}
	if s.lock == nil {
		return fmt.Errorf("runtime apply lock is required for recovery")
	}
	unlock, acquired, err := s.lock.TryLock(ctx, spec.Key.EnvironmentID)
	if err != nil || !acquired {
		return err
	}
	defer unlock()
	attempt := s.newAttempt(spec.Key, *health, decision, domain.RecoveryAttemptPending)
	inserted, err := s.repo.RecordRecoveryAttempt(ctx, &attempt)
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}
	s.publish(ctx, events.EventRuntimeRecoveryRequested, attempt.CorrelationID, ManagedInstanceRecoveryEvent{EventID: attempt.ID.String(), Health: *health, Decision: decision, Attempt: attempt, Severity: domain.AlertSeverityError, OccurredAt: attempt.RequestedAt})
	beforeRestart := *health
	restartErr := spec.Controller.RestartInstance(ctx, spec.Key)
	if restartErr == nil {
		post, obsErr := spec.Observer.ObserveInstance(ctx, spec.Key)
		postHealth := s.classify(ctx, spec, health, post, obsErr)
		switch postHealth.Status {
		case domain.InstanceHealthStatusHealthy, domain.InstanceHealthStatusRunning:
			attempt.Result = domain.RecoveryAttemptSuccess
		case domain.InstanceHealthStatusDegraded, domain.InstanceHealthStatusUnknown:
			attempt.Result = domain.RecoveryAttemptDegraded
		default:
			attempt.Result = domain.RecoveryAttemptFailed
		}
		attempt.Evidence = postHealth.FailureReason
		*health = postHealth
	} else {
		attempt.Result = domain.RecoveryAttemptFailed
		attempt.Evidence = domain.SanitizeEvidence(restartErr.Error())
	}
	completed, err := s.repo.CompleteRecoveryAttempt(ctx, attempt.CorrelationID, attempt.Result, attempt.Evidence)
	if err != nil {
		return err
	}
	if !completed {
		return nil
	}
	health.LastRecoveryAttempt = &attempt
	if err := s.repo.UpsertHealth(ctx, health); err != nil {
		return err
	}
	if beforeRestart.Status != health.Status {
		transition := domain.ManagedInstanceHealthEvent{ID: uuid.New(), ManagedInstanceKey: spec.Key, PreviousStatus: beforeRestart.Status, Status: health.Status, Reason: health.FailureReason, Evidence: health.FailureReason, ObservedAt: health.LastObservedAt}
		if err := s.repo.AppendHealthEvent(ctx, &transition); err != nil {
			return err
		}
		s.publish(ctx, events.EventRuntimeInstanceHealthChanged, instanceKeyString(spec.Key), ManagedInstanceHealthChanged{EventID: transition.ID.String(), Health: *health, PreviousStatus: beforeRestart.Status, Severity: healthSeverity(health.Status, false), Alert: false, Reason: health.FailureReason, OccurredAt: health.LastObservedAt})
	}
	eventType := events.EventRuntimeRecoveryCompleted
	severity := domain.AlertSeverityInfo
	if attempt.Result == domain.RecoveryAttemptFailed {
		eventType, severity = events.EventRuntimeRecoveryFailed, domain.AlertSeverityError
	} else if attempt.Result == domain.RecoveryAttemptDegraded {
		severity = domain.AlertSeverityWarning
	}
	s.publish(ctx, eventType, attempt.CorrelationID, ManagedInstanceRecoveryEvent{EventID: attempt.ID.String(), Health: *health, Decision: decision, Attempt: attempt, Severity: severity, OccurredAt: attempt.RequestedAt})
	return nil
}

func (s *ManagedInstanceSupervisor) reconcilePendingAttempt(ctx context.Context, health *domain.ManagedInstanceHealth, attempt domain.RecoveryAttempt) error {
	attempt.Result = recoveryResultForObservedStatus(health.Status)
	attempt.Evidence = domain.SanitizeEvidence(health.FailureReason)
	completed, err := s.repo.CompleteRecoveryAttempt(ctx, attempt.CorrelationID, attempt.Result, attempt.Evidence)
	if err != nil || !completed {
		return err
	}
	health.LastRecoveryAttempt = &attempt
	if err := s.repo.UpsertHealth(ctx, health); err != nil {
		return err
	}
	eventType := events.EventRuntimeRecoveryCompleted
	severity := domain.AlertSeverityInfo
	if attempt.Result == domain.RecoveryAttemptFailed {
		eventType, severity = events.EventRuntimeRecoveryFailed, domain.AlertSeverityError
	} else if attempt.Result == domain.RecoveryAttemptDegraded {
		severity = domain.AlertSeverityWarning
	}
	s.publish(ctx, eventType, attempt.CorrelationID, ManagedInstanceRecoveryEvent{EventID: attempt.ID.String(), Health: *health, Attempt: attempt, Severity: severity, OccurredAt: health.LastObservedAt})
	return nil
}

func recoveryResultForObservedStatus(status domain.InstanceHealthStatus) domain.RecoveryAttemptResult {
	switch status {
	case domain.InstanceHealthStatusHealthy, domain.InstanceHealthStatusRunning:
		return domain.RecoveryAttemptSuccess
	case domain.InstanceHealthStatusDegraded, domain.InstanceHealthStatusUnknown:
		return domain.RecoveryAttemptDegraded
	default:
		return domain.RecoveryAttemptFailed
	}
}

func recoveryFailureStatus(status domain.InstanceHealthStatus) bool {
	switch status {
	case domain.InstanceHealthStatusStopped, domain.InstanceHealthStatusUnhealthy, domain.InstanceHealthStatusOOMKilled, domain.InstanceHealthStatusRestartLoop:
		return true
	default:
		return false
	}
}

func (s *ManagedInstanceSupervisor) newAttempt(key domain.ManagedInstanceKey, health domain.ManagedInstanceHealth, decision domain.RecoveryDecision, result domain.RecoveryAttemptResult) domain.RecoveryAttempt {
	generation := health.FailureGenerationAt
	if generation.IsZero() {
		generation = health.LastObservedAt
	}
	seed := fmt.Sprintf("%s|%d", instanceKeyString(key), generation.UnixNano())
	sum := sha256.Sum256([]byte(seed))
	return domain.RecoveryAttempt{ID: uuid.NewSHA1(uuid.NameSpaceOID, sum[:]), ManagedInstanceKey: key, CorrelationID: hex.EncodeToString(sum[:]), RequestedAt: generation, Result: result, Evidence: domain.SanitizeEvidence(decision.Reason)}
}

// SetMaintenanceOverride creates an active override for stage-4 API/UI callers.
func (s *ManagedInstanceSupervisor) SetMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey, actor, reason string, expiresAt *time.Time) (*domain.MaintenanceOverride, error) {
	actor, reason = domain.SanitizeEvidence(actor), domain.SanitizeEvidence(reason)
	if key.ServiceID == uuid.Nil || key.EnvironmentID == uuid.Nil || strings.TrimSpace(key.RuntimeTargetName) == "" || actor == "" || reason == "" {
		return nil, fmt.Errorf("service, environment, runtime target, actor, and reason are required")
	}
	now := s.now()
	if expiresAt != nil && !expiresAt.After(now) {
		return nil, fmt.Errorf("maintenance expiry must be in the future")
	}
	o := &domain.MaintenanceOverride{ID: uuid.New(), ManagedInstanceKey: key, Actor: actor, Reason: reason, CreatedAt: now, ExpiresAt: expiresAt}
	if err := s.repo.CreateMaintenanceOverride(ctx, o); err != nil {
		return nil, err
	}
	health, _ := s.repo.GetHealth(ctx, key)
	s.publish(ctx, events.EventRuntimeMaintenanceChanged, o.ID.String(), ManagedInstanceMaintenanceEvent{EventID: o.ID.String(), Health: health, Override: *o, Active: true, OccurredAt: now})
	return o, nil
}

// ClearMaintenanceOverride clears an override for stage-4 API/UI callers.
func (s *ManagedInstanceSupervisor) ClearMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey, actor string) error {
	now := s.now()
	existing, err := s.repo.GetActiveMaintenanceOverride(ctx, key, now)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	if err := s.repo.ClearMaintenanceOverride(ctx, key); err != nil {
		return err
	}
	health, _ := s.repo.GetHealth(ctx, key)
	existing.Actor = domain.SanitizeEvidence(actor)
	s.publish(ctx, events.EventRuntimeMaintenanceChanged, existing.ID.String(), ManagedInstanceMaintenanceEvent{EventID: existing.ID.String(), Health: health, Override: *existing, Active: false, OccurredAt: now})
	return nil
}

func (s *ManagedInstanceSupervisor) shouldAlert(spec *SupervisionSpec, severity domain.AlertSeverity, at time.Time) bool {
	immediate := severity == domain.AlertSeverityError || severity == domain.AlertSeverityCritical
	for _, allowed := range spec.RecoveryPolicy.AlertPolicy.ImmediateSeverities {
		if severity == allowed {
			immediate = true
		}
	}
	if immediate {
		return true
	}
	if severity != domain.AlertSeverityWarning {
		return false
	}
	key := instanceKeyString(spec.Key) + "|warning"
	last := s.lastAlerts[key]
	if interval := spec.RecoveryPolicy.AlertPolicy.WarningMinInterval; interval > 0 && at.Sub(last) < interval {
		return false
	}
	s.lastAlerts[key] = at
	return true
}

func healthSeverity(status domain.InstanceHealthStatus, highMemory bool) domain.AlertSeverity {
	if highMemory {
		return domain.AlertSeverityWarning
	}
	switch status {
	case domain.InstanceHealthStatusOOMKilled, domain.InstanceHealthStatusRestartLoop:
		return domain.AlertSeverityCritical
	case domain.InstanceHealthStatusStopped, domain.InstanceHealthStatusUnhealthy:
		return domain.AlertSeverityError
	case domain.InstanceHealthStatusDegraded, domain.InstanceHealthStatusUnknown:
		return domain.AlertSeverityWarning
	default:
		return domain.AlertSeverityInfo
	}
}

func (s *ManagedInstanceSupervisor) publish(ctx context.Context, typ events.EventType, entityID string, data any) {
	if s.publisher != nil {
		s.publisher.Publish(ctx, events.Event{Type: typ, EntityID: entityID, Data: data})
	}
}
func instanceKeyString(k domain.ManagedInstanceKey) string {
	return k.ServiceID.String() + ":" + k.EnvironmentID.String() + ":" + k.DeploymentUnitID.String() + ":" + strings.TrimSpace(k.RuntimeTargetName)
}
func firstManagedEvidence(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
