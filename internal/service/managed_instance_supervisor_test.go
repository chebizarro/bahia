package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type supervisorRepoFake struct {
	health   *domain.ManagedInstanceHealth
	events   []domain.ManagedInstanceHealthEvent
	attempts []domain.RecoveryAttempt
	override *domain.MaintenanceOverride
}

func (r *supervisorRepoFake) UpsertHealth(_ context.Context, h *domain.ManagedInstanceHealth) error {
	copy := *h
	r.health = &copy
	return nil
}
func (r *supervisorRepoFake) GetHealth(context.Context, domain.ManagedInstanceKey) (*domain.ManagedInstanceHealth, error) {
	if r.health == nil {
		return nil, nil
	}
	copy := *r.health
	return &copy, nil
}
func (r *supervisorRepoFake) ListAllHealth(context.Context) ([]domain.ManagedInstanceHealth, error) {
	return nil, nil
}
func (r *supervisorRepoFake) ListHealthByEnvironment(context.Context, uuid.UUID) ([]domain.ManagedInstanceHealth, error) {
	return nil, nil
}
func (r *supervisorRepoFake) ListHealthByService(context.Context, uuid.UUID) ([]domain.ManagedInstanceHealth, error) {
	return nil, nil
}
func (r *supervisorRepoFake) ListUnhealthy(context.Context) ([]domain.ManagedInstanceHealth, error) {
	return nil, nil
}
func (r *supervisorRepoFake) AppendHealthEvent(_ context.Context, e *domain.ManagedInstanceHealthEvent) error {
	r.events = append(r.events, *e)
	return nil
}
func (r *supervisorRepoFake) ListRecentHealthEvents(context.Context, domain.ManagedInstanceKey, int) ([]domain.ManagedInstanceHealthEvent, error) {
	return r.events, nil
}
func (r *supervisorRepoFake) RecordRecoveryAttempt(_ context.Context, a *domain.RecoveryAttempt) (bool, error) {
	for _, x := range r.attempts {
		if x.CorrelationID == a.CorrelationID {
			return false, nil
		}
	}
	r.attempts = append(r.attempts, *a)
	return true, nil
}
func (r *supervisorRepoFake) CompleteRecoveryAttempt(_ context.Context, correlationID string, result domain.RecoveryAttemptResult, evidence string) (bool, error) {
	for i := range r.attempts {
		if r.attempts[i].CorrelationID == correlationID && r.attempts[i].Result == domain.RecoveryAttemptPending {
			r.attempts[i].Result = result
			r.attempts[i].Evidence = evidence
			return true, nil
		}
	}
	return false, nil
}
func (r *supervisorRepoFake) ListRecentRecoveryAttempts(context.Context, domain.ManagedInstanceKey, int) ([]domain.RecoveryAttempt, error) {
	return append([]domain.RecoveryAttempt(nil), r.attempts...), nil
}
func (r *supervisorRepoFake) CreateMaintenanceOverride(_ context.Context, o *domain.MaintenanceOverride) error {
	copy := *o
	r.override = &copy
	return nil
}
func (r *supervisorRepoFake) ClearMaintenanceOverride(context.Context, domain.ManagedInstanceKey) error {
	r.override = nil
	return nil
}
func (r *supervisorRepoFake) GetActiveMaintenanceOverride(_ context.Context, _ domain.ManagedInstanceKey, at time.Time) (*domain.MaintenanceOverride, error) {
	if r.override != nil && r.override.ActiveAt(at) {
		copy := *r.override
		return &copy, nil
	}
	return nil, nil
}

type sequenceRuntime struct {
	observations []*runtime.InstanceObservation
	observeErr   error
	restarts     int
	restartErr   error
	keys         []domain.ManagedInstanceKey
}

func (r *sequenceRuntime) ObserveInstance(context.Context, domain.ManagedInstanceKey) (*runtime.InstanceObservation, error) {
	if r.observeErr != nil {
		return nil, r.observeErr
	}
	if len(r.observations) == 0 {
		return nil, errors.New("no observation")
	}
	v := r.observations[0]
	if len(r.observations) > 1 {
		r.observations = r.observations[1:]
	}
	copy := *v
	return &copy, nil
}
func (r *sequenceRuntime) RestartInstance(_ context.Context, k domain.ManagedInstanceKey) error {
	r.restarts++
	r.keys = append(r.keys, k)
	return r.restartErr
}
func (r *sequenceRuntime) StopInstance(context.Context, domain.ManagedInstanceKey) error { return nil }

type lockFake struct{ acquired bool }

func (l lockFake) TryLock(context.Context, uuid.UUID) (func(), bool, error) {
	if !l.acquired {
		return nil, false, nil
	}
	return func() {}, true, nil
}

type eventBusFake struct {
	published []events.Event
	handlers  map[events.EventType][]events.Handler
}

func (b *eventBusFake) Publish(ctx context.Context, e events.Event) {
	b.published = append(b.published, e)
	for _, h := range b.handlers[e.Type] {
		h(ctx, e)
	}
}
func (b *eventBusFake) Subscribe(t events.EventType, h events.Handler) {
	if b.handlers == nil {
		b.handlers = map[events.EventType][]events.Handler{}
	}
	b.handlers[t] = append(b.handlers[t], h)
}

func testKey() domain.ManagedInstanceKey {
	return domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "api"}
}
func testPolicy(observeOnly bool) domain.RecoveryPolicy {
	return domain.RecoveryPolicy{Enabled: true, ObserveOnly: observeOnly, RestartBudget: domain.RestartBudget{MaxAttempts: 3, Window: time.Hour}, BackoffBase: time.Minute, BackoffCap: 5 * time.Minute, AlertPolicy: domain.RecoveryAlertPolicy{WarningMinInterval: time.Hour}}
}

func TestManagedInstanceClassification(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	key := testKey()
	cases := []struct {
		name     string
		obs      runtime.InstanceObservation
		previous *domain.ManagedInstanceHealth
		want     domain.InstanceHealthStatus
	}{
		{"stopped", runtime.InstanceObservation{Status: domain.InstanceHealthStatusStopped, ObservedAt: now}, nil, domain.InstanceHealthStatusStopped},
		{"unhealthy", runtime.InstanceObservation{Status: domain.InstanceHealthStatusUnhealthy, ObservedAt: now}, nil, domain.InstanceHealthStatusUnhealthy},
		{"oom", runtime.InstanceObservation{Status: domain.InstanceHealthStatusStopped, OOMKilled: true, ObservedAt: now}, nil, domain.InstanceHealthStatusOOMKilled},
		{"initial lifetime restarts are baseline only", runtime.InstanceObservation{Status: domain.InstanceHealthStatusHealthy, RestartCount: 5, ObservedAt: now}, nil, domain.InstanceHealthStatusHealthy},
		{"restart count increases across observations", runtime.InstanceObservation{Status: domain.InstanceHealthStatusRunning, RestartCount: 8, ObservedAt: now}, &domain.ManagedInstanceHealth{ManagedInstanceKey: key, Status: domain.InstanceHealthStatusHealthy, RestartCount: 5}, domain.InstanceHealthStatusRestartLoop},
		{"restart loop", runtime.InstanceObservation{Status: domain.InstanceHealthStatusRunning, RestartCount: 4, ObservedAt: now}, &domain.ManagedInstanceHealth{ManagedInstanceKey: key, Status: domain.InstanceHealthStatusRunning, RestartCount: 1, ConsecutiveRestartCount: 1}, domain.InstanceHealthStatusRestartLoop},
		{"degraded probe", runtime.InstanceObservation{Status: domain.InstanceHealthStatusRunning, ProbeResult: &runtime.ProbeResult{Successful: false, Error: "not ready"}, ObservedAt: now}, nil, domain.InstanceHealthStatusDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &supervisorRepoFake{}
			s, _ := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{}, repo, lockFake{}, nil, time.Second, zap.NewNop())
			got := s.classify(context.Background(), &SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, RestartLoopThreshold: 3}, tc.previous, &tc.obs, nil)
			require.Equal(t, tc.want, got.Status)
		})
	}
}

func TestManagedInstanceHighMemorySustainedAndWarningRateLimit(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	key := testKey()
	repo := &supervisorRepoFake{}
	bus := &eventBusFake{}
	rt := &sequenceRuntime{observations: []*runtime.InstanceObservation{{Status: domain.InstanceHealthStatusRunning, MemoryCurrentBytes: 95, MemoryLimitBytes: 100, ObservedAt: now}, {Status: domain.InstanceHealthStatusRunning, MemoryCurrentBytes: 96, MemoryLimitBytes: 100, ObservedAt: now.Add(time.Minute)}}}
	spec := SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(true), MemoryThresholdRatio: .9, HighMemorySustainCount: 2}
	s, _ := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{spec}, repo, lockFake{}, bus, time.Second, zap.NewNop())
	require.NoError(t, s.EvaluateOnce(context.Background()))
	require.NoError(t, s.EvaluateOnce(context.Background()))
	last := bus.published[len(bus.published)-1].Data.(ManagedInstanceHealthChanged)
	require.True(t, last.Alert)
	require.Contains(t, last.Health.FailureReason, "high memory")
	require.False(t, s.shouldAlert(&spec, domain.AlertSeverityWarning, now.Add(2*time.Minute)))
	require.True(t, s.shouldAlert(&spec, domain.AlertSeverityError, now.Add(2*time.Minute)))
}

func TestManagedInstanceRecoverySuppressions(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	key := testKey()
	cases := []struct {
		name  string
		spec  func(*sequenceRuntime) SupervisionSpec
		setup func(*supervisorRepoFake)
	}{
		{"observe only", func(rt *sequenceRuntime) SupervisionSpec {
			return SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(true)}
		}, nil},
		{"manual stop", func(rt *sequenceRuntime) SupervisionSpec {
			return SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: false, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(false)}
		}, nil},
		{"maintenance", func(rt *sequenceRuntime) SupervisionSpec {
			return SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(false)}
		}, func(r *supervisorRepoFake) {
			r.override = &domain.MaintenanceOverride{ID: uuid.New(), ManagedInstanceKey: key, Actor: "op", Reason: "work", CreatedAt: now.Add(-time.Minute)}
		}},
		{"backoff", func(rt *sequenceRuntime) SupervisionSpec {
			return SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(false)}
		}, func(r *supervisorRepoFake) {
			r.attempts = []domain.RecoveryAttempt{{ManagedInstanceKey: key, CorrelationID: "old", RequestedAt: now.Add(-30 * time.Second), Result: domain.RecoveryAttemptFailed}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &supervisorRepoFake{}
			if tc.setup != nil {
				tc.setup(repo)
			}
			rt := &sequenceRuntime{observations: []*runtime.InstanceObservation{{Status: domain.InstanceHealthStatusUnhealthy, ObservedAt: now}}}
			s, _ := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{tc.spec(rt)}, repo, lockFake{acquired: true}, nil, time.Second, zap.NewNop())
			require.NoError(t, s.EvaluateOnce(context.Background()))
			require.Zero(t, rt.restarts)
		})
	}
}

func TestManagedInstanceBudgetLockAndIdempotency(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	key := testKey()
	t.Run("budget exhausted", func(t *testing.T) {
		repo := &supervisorRepoFake{attempts: []domain.RecoveryAttempt{{ManagedInstanceKey: key, CorrelationID: "a", RequestedAt: now.Add(-20 * time.Minute), Result: domain.RecoveryAttemptFailed}, {ManagedInstanceKey: key, CorrelationID: "b", RequestedAt: now.Add(-10 * time.Minute), Result: domain.RecoveryAttemptFailed}, {ManagedInstanceKey: key, CorrelationID: "c", RequestedAt: now.Add(-5 * time.Minute), Result: domain.RecoveryAttemptFailed}}}
		rt := &sequenceRuntime{observations: []*runtime.InstanceObservation{{Status: domain.InstanceHealthStatusUnhealthy, ObservedAt: now}}}
		bus := &eventBusFake{}
		spec := SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(false)}
		s, _ := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{spec}, repo, lockFake{acquired: true}, bus, time.Second, zap.NewNop())
		require.NoError(t, s.EvaluateOnce(context.Background()))
		require.Zero(t, rt.restarts)
		require.Equal(t, events.EventRuntimeRecoveryBudgetExhausted, bus.published[len(bus.published)-1].Type)
	})
	t.Run("busy lock", func(t *testing.T) {
		repo := &supervisorRepoFake{}
		rt := &sequenceRuntime{observations: []*runtime.InstanceObservation{{Status: domain.InstanceHealthStatusUnhealthy, ObservedAt: now}}}
		spec := SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(false)}
		s, _ := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{spec}, repo, lockFake{}, nil, time.Second, zap.NewNop())
		require.NoError(t, s.EvaluateOnce(context.Background()))
		require.Zero(t, rt.restarts)
		require.Empty(t, repo.attempts)
	})
	t.Run("same generation restarts once", func(t *testing.T) {
		repo := &supervisorRepoFake{}
		rt := &sequenceRuntime{observations: []*runtime.InstanceObservation{{Status: domain.InstanceHealthStatusUnhealthy, ObservedAt: now}, {Status: domain.InstanceHealthStatusRunning, ObservedAt: now}, {Status: domain.InstanceHealthStatusUnhealthy, ObservedAt: now}}}
		spec := SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(false)}
		s, _ := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{spec}, repo, lockFake{acquired: true}, nil, time.Second, zap.NewNop())
		require.NoError(t, s.EvaluateOnce(context.Background()))
		require.NoError(t, s.EvaluateOnce(context.Background()))
		require.Equal(t, 1, rt.restarts)
		require.Equal(t, key, rt.keys[0])
	})
}

func TestManagedInstancePendingRecoveryIsReconciledWithoutSecondRestart(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		status     domain.InstanceHealthStatus
		wantResult domain.RecoveryAttemptResult
	}{
		{name: "crash before restart", status: domain.InstanceHealthStatusUnhealthy, wantResult: domain.RecoveryAttemptFailed},
		{name: "crash after restart before completion", status: domain.InstanceHealthStatusRunning, wantResult: domain.RecoveryAttemptSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := testKey()
			generation := now.Add(-time.Minute)
			pending := domain.RecoveryAttempt{ID: uuid.New(), ManagedInstanceKey: key, CorrelationID: "stable", RequestedAt: generation, Result: domain.RecoveryAttemptPending}
			repo := &supervisorRepoFake{
				health:   &domain.ManagedInstanceHealth{ManagedInstanceKey: key, SupervisorType: domain.InstanceSupervisorDocker, Status: domain.InstanceHealthStatusUnhealthy, LastObservedAt: generation, FailureGenerationAt: generation},
				attempts: []domain.RecoveryAttempt{pending},
			}
			rt := &sequenceRuntime{observations: []*runtime.InstanceObservation{{Status: tc.status, ObservedAt: now}}}
			spec := SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(false)}
			s, _ := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{spec}, repo, lockFake{acquired: true}, nil, time.Second, zap.NewNop())

			require.NoError(t, s.EvaluateOnce(context.Background()))
			require.Zero(t, rt.restarts)
			require.Len(t, repo.attempts, 1)
			require.Equal(t, tc.wantResult, repo.attempts[0].Result)
		})
	}
}

func TestManagedInstanceRecoveryCorrelationUsesFailureGeneration(t *testing.T) {
	key := testKey()
	generation := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	s := &ManagedInstanceSupervisor{}
	first := s.newAttempt(key, domain.ManagedInstanceHealth{ManagedInstanceKey: key, Status: domain.InstanceHealthStatusUnhealthy, LastObservedAt: generation, FailureGenerationAt: generation}, domain.RecoveryDecision{Reason: "recover"}, domain.RecoveryAttemptPending)
	second := s.newAttempt(key, domain.ManagedInstanceHealth{ManagedInstanceKey: key, Status: domain.InstanceHealthStatusUnhealthy, LastObservedAt: generation.Add(time.Minute), FailureGenerationAt: generation}, domain.RecoveryDecision{Reason: "recover"}, domain.RecoveryAttemptPending)
	require.Equal(t, first.CorrelationID, second.CorrelationID)
}

func TestManagedInstanceMaintenanceMethods(t *testing.T) {
	repo := &supervisorRepoFake{}
	s, _ := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{}, repo, lockFake{}, nil, time.Second, zap.NewNop())
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	key := testKey()
	o, err := s.SetMaintenanceOverride(context.Background(), key, "operator", "token=secret", nil)
	require.NoError(t, err)
	require.Equal(t, "[REDACTED]", o.Reason)
	require.NoError(t, s.ClearMaintenanceOverride(context.Background(), key, "operator"))
	require.Nil(t, repo.override)
}
