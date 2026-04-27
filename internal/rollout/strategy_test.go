package rollout

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestBuildPlan_Canary(t *testing.T) {
	intentID := uuid.New()
	plan, steps := BuildPlan(intentID, domain.DeployStrategyCanary)

	if plan.DeploymentIntentID != intentID {
		t.Error("intent ID mismatch")
	}
	if plan.Strategy != domain.DeployStrategyCanary {
		t.Errorf("expected canary, got %s", plan.Strategy)
	}
	if plan.Status != domain.RolloutStatusPending {
		t.Errorf("expected pending, got %s", plan.Status)
	}

	if len(steps) != 5 {
		t.Fatalf("expected 5 canary steps, got %d", len(steps))
	}

	expectedActions := []domain.StepAction{
		domain.StepActionDeployCanary,
		domain.StepActionObserve,
		domain.StepActionShiftTraffic,
		domain.StepActionObserve,
		domain.StepActionPromote,
	}
	for i, step := range steps {
		if step.Action != expectedActions[i] {
			t.Errorf("step %d: expected %s, got %s", i, expectedActions[i], step.Action)
		}
		if step.StepOrder != i {
			t.Errorf("step %d: expected order %d, got %d", i, i, step.StepOrder)
		}
		if step.Status != domain.StepStatusPending {
			t.Errorf("step %d: expected pending, got %s", i, step.Status)
		}
		if step.RolloutPlanID != plan.ID {
			t.Errorf("step %d: plan ID mismatch", i)
		}
	}

	// Verify traffic weights in canary steps.
	if w, ok := steps[0].Config["weight"].(int); !ok || w != 10 {
		t.Errorf("canary deploy: expected weight=10, got %v", steps[0].Config["weight"])
	}
	if w, ok := steps[2].Config["weight"].(int); !ok || w != 50 {
		t.Errorf("shift traffic: expected weight=50, got %v", steps[2].Config["weight"])
	}
	if w, ok := steps[4].Config["weight"].(int); !ok || w != 100 {
		t.Errorf("promote: expected weight=100, got %v", steps[4].Config["weight"])
	}
}

func TestBuildPlan_BlueGreen(t *testing.T) {
	intentID := uuid.New()
	plan, steps := BuildPlan(intentID, domain.DeployStrategyBlueGreen)

	if plan.Strategy != domain.DeployStrategyBlueGreen {
		t.Errorf("expected blue_green, got %s", plan.Strategy)
	}

	if len(steps) != 5 {
		t.Fatalf("expected 5 blue-green steps, got %d", len(steps))
	}

	expectedActions := []domain.StepAction{
		domain.StepActionDeployGreen,
		domain.StepActionObserve,
		domain.StepActionSwitch,
		domain.StepActionObserve,
		domain.StepActionPromote,
	}
	for i, step := range steps {
		if step.Action != expectedActions[i] {
			t.Errorf("step %d: expected %s, got %s", i, expectedActions[i], step.Action)
		}
	}
}

func TestBuildPlan_Replace(t *testing.T) {
	intentID := uuid.New()
	plan, steps := BuildPlan(intentID, domain.DeployStrategyReplace)

	if plan.Strategy != domain.DeployStrategyReplace {
		t.Errorf("expected replace, got %s", plan.Strategy)
	}

	if len(steps) != 1 {
		t.Fatalf("expected 1 replace step, got %d", len(steps))
	}
	if steps[0].Action != domain.StepActionPromote {
		t.Errorf("expected promote, got %s", steps[0].Action)
	}
}

func TestBuildPlan_UniqueIDs(t *testing.T) {
	_, steps1 := BuildPlan(uuid.New(), domain.DeployStrategyCanary)
	_, steps2 := BuildPlan(uuid.New(), domain.DeployStrategyCanary)

	ids := make(map[uuid.UUID]bool)
	for _, s := range steps1 {
		if ids[s.ID] {
			t.Errorf("duplicate step ID: %s", s.ID)
		}
		ids[s.ID] = true
	}
	for _, s := range steps2 {
		if ids[s.ID] {
			t.Errorf("duplicate step ID across plans: %s", s.ID)
		}
		ids[s.ID] = true
	}
}

func TestBuildPlan_ObserveStepsHaveConfig(t *testing.T) {
	_, steps := BuildPlan(uuid.New(), domain.DeployStrategyCanary)

	for _, step := range steps {
		if step.Action == domain.StepActionObserve {
			if step.Config == nil {
				t.Errorf("observe step %d has nil config", step.StepOrder)
				continue
			}
			if _, ok := step.Config["duration_seconds"]; !ok {
				t.Errorf("observe step %d missing duration_seconds", step.StepOrder)
			}
			if _, ok := step.Config["success_threshold"]; !ok {
				t.Errorf("observe step %d missing success_threshold", step.StepOrder)
			}
		}
	}
}

func TestDefaultHealthGate(t *testing.T) {
	cfg := domain.DefaultHealthGate()
	if cfg.Interval != 10*time.Second {
		t.Errorf("expected 10s interval, got %s", cfg.Interval)
	}
	if cfg.Timeout != 5*time.Minute {
		t.Errorf("expected 5m timeout, got %s", cfg.Timeout)
	}
	if cfg.SuccessThreshold != 3 {
		t.Errorf("expected 3 success threshold, got %d", cfg.SuccessThreshold)
	}
	if cfg.FailureThreshold != 2 {
		t.Errorf("expected 2 failure threshold, got %d", cfg.FailureThreshold)
	}
}

// mockObserver for health gate testing.
type mockObserver struct {
	observations []*domain.RuntimeObservation
	callCount    int
	err          error
}

func (m *mockObserver) Observe(_ context.Context, serviceID, envID uuid.UUID, _ string) (*domain.RuntimeObservation, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.callCount < len(m.observations) {
		obs := m.observations[m.callCount]
		m.callCount++
		return obs, nil
	}
	// Default: return healthy.
	return &domain.RuntimeObservation{
		ServiceID:     serviceID,
		EnvironmentID: envID,
		HealthStatus:  domain.HealthStatusHealthy,
		Source:        "mock",
		ObservedAt:    time.Now().UTC(),
	}, nil
}

func nopLogger() *zap.Logger { return zap.NewNop() }

func TestHealthGate_Passes(t *testing.T) {
	obs := &mockObserver{
		observations: []*domain.RuntimeObservation{
			{HealthStatus: domain.HealthStatusHealthy},
			{HealthStatus: domain.HealthStatusHealthy},
			{HealthStatus: domain.HealthStatusHealthy},
		},
	}

	gate := NewHealthGate(obs, nil)
	// Use nil logger — gate handles it.
	gate.logger = nopLogger()

	cfg := domain.HealthGateConfig{
		Interval:         10 * time.Millisecond,
		Timeout:          1 * time.Second,
		SuccessThreshold: 3,
		FailureThreshold: 2,
	}

	result, err := gate.Check(context.Background(), uuid.Nil, uuid.Nil, "test-svc", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected health gate to pass, got: %+v", result)
	}
	if result.HealthyChecks < 3 {
		t.Errorf("expected at least 3 healthy checks, got %d", result.HealthyChecks)
	}
}

func TestHealthGate_Fails(t *testing.T) {
	obs := &mockObserver{
		observations: []*domain.RuntimeObservation{
			{HealthStatus: domain.HealthStatusUnhealthy},
			{HealthStatus: domain.HealthStatusUnhealthy},
		},
	}

	gate := NewHealthGate(obs, nopLogger())

	cfg := domain.HealthGateConfig{
		Interval:         10 * time.Millisecond,
		Timeout:          1 * time.Second,
		SuccessThreshold: 3,
		FailureThreshold: 2,
	}

	result, err := gate.Check(context.Background(), uuid.Nil, uuid.Nil, "test-svc", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected health gate to fail")
	}
	if result.UnhealthyChecks < 2 {
		t.Errorf("expected at least 2 unhealthy checks, got %d", result.UnhealthyChecks)
	}
}

func TestHealthGate_ObserverError(t *testing.T) {
	obs := &mockObserver{
		err: fmt.Errorf("connection refused"),
	}

	gate := NewHealthGate(obs, nopLogger())

	cfg := domain.HealthGateConfig{
		Interval:         10 * time.Millisecond,
		Timeout:          1 * time.Second,
		SuccessThreshold: 3,
		FailureThreshold: 2,
	}

	result, err := gate.Check(context.Background(), uuid.Nil, uuid.Nil, "test-svc", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected failure when observer errors")
	}
}

func TestHealthGate_ContextCancelled(t *testing.T) {
	obs := &mockObserver{} // will return healthy

	gate := NewHealthGate(obs, nopLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	cfg := domain.HealthGateConfig{
		Interval:         10 * time.Millisecond,
		Timeout:          1 * time.Second,
		SuccessThreshold: 100, // unreachable
		FailureThreshold: 100,
	}

	_, err := gate.Check(ctx, uuid.Nil, uuid.Nil, "test-svc", cfg)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestRolloutStatuses(t *testing.T) {
	statuses := []domain.RolloutStatus{
		domain.RolloutStatusPending,
		domain.RolloutStatusRunning,
		domain.RolloutStatusCompleted,
		domain.RolloutStatusFailed,
		domain.RolloutStatusRolledBack,
	}
	for _, s := range statuses {
		if s == "" {
			t.Error("empty status")
		}
	}
}

func TestStepActions(t *testing.T) {
	actions := []domain.StepAction{
		domain.StepActionDeployCanary,
		domain.StepActionShiftTraffic,
		domain.StepActionObserve,
		domain.StepActionPromote,
		domain.StepActionRollback,
		domain.StepActionDeployGreen,
		domain.StepActionSwitch,
	}
	for _, a := range actions {
		if a == "" {
			t.Error("empty action")
		}
	}
}
