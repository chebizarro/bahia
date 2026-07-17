package rollout

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

type executorTestRuntime struct {
	undeployErr error
}

func (r *executorTestRuntime) Type() domain.RuntimeType { return domain.RuntimeTypeDocker }
func (r *executorTestRuntime) Observe(context.Context, uuid.UUID, uuid.UUID, string) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{HealthStatus: domain.HealthStatusHealthy, ObservedImageRepo: "registry.example/api", ObservedImageDigest: "sha256:old"}, nil
}
func (r *executorTestRuntime) Deploy(context.Context, string, string, runtime.DeployOptions) error {
	return nil
}
func (r *executorTestRuntime) Undeploy(context.Context, string) error { return r.undeployErr }
func (r *executorTestRuntime) StreamLogs(context.Context, string, runtime.LogOptions) (<-chan runtime.LogEntry, error) {
	ch := make(chan runtime.LogEntry)
	close(ch)
	return ch, nil
}

type executorTrafficRuntime struct {
	executorTestRuntime
	trafficErr error
	state      TrafficState
	shifts     int
	switches   int
}

func (r *executorTrafficRuntime) ShiftTraffic(context.Context, string, string, int) (TrafficState, error) {
	r.shifts++
	return r.state, r.trafficErr
}
func (r *executorTrafficRuntime) SwitchTraffic(context.Context, string, string, string) (TrafficState, error) {
	r.switches++
	return r.state, r.trafficErr
}

type executorRestoreRuntime struct {
	executorTestRuntime
	observation *domain.RuntimeObservation
	deployed    []string
}

func (r *executorRestoreRuntime) Observe(context.Context, uuid.UUID, uuid.UUID, string) (*domain.RuntimeObservation, error) {
	return r.observation, nil
}
func (r *executorRestoreRuntime) Deploy(_ context.Context, serviceName, image string, _ runtime.DeployOptions) error {
	r.deployed = append(r.deployed, serviceName+":"+image)
	repo, digest, _ := strings.Cut(image, "@")
	r.observation = &domain.RuntimeObservation{HealthStatus: domain.HealthStatusHealthy, ObservedImageRepo: repo, ObservedImageDigest: digest}
	return nil
}

type executorTestRepo struct{}

func (*executorTestRepo) CreatePlan(context.Context, *domain.RolloutPlan) error { return nil }
func (*executorTestRepo) GetPlanByID(context.Context, uuid.UUID) (*domain.RolloutPlan, error) {
	return nil, nil
}
func (*executorTestRepo) GetPlanByIntent(context.Context, uuid.UUID) (*domain.RolloutPlan, error) {
	return nil, nil
}
func (*executorTestRepo) UpdatePlan(context.Context, *domain.RolloutPlan) error { return nil }
func (*executorTestRepo) ListActivePlans(context.Context) ([]domain.RolloutPlan, error) {
	return nil, nil
}
func (*executorTestRepo) CreateStep(context.Context, *domain.RolloutStep) error   { return nil }
func (*executorTestRepo) CreateSteps(context.Context, []domain.RolloutStep) error { return nil }
func (*executorTestRepo) GetStepByID(context.Context, uuid.UUID) (*domain.RolloutStep, error) {
	return nil, nil
}
func (*executorTestRepo) ListStepsByPlan(context.Context, uuid.UUID) ([]domain.RolloutStep, error) {
	return nil, nil
}
func (*executorTestRepo) UpdateStep(context.Context, *domain.RolloutStep) error { return nil }

type executorTestPublisher struct{ events []events.Event }

func (p *executorTestPublisher) Publish(_ context.Context, event events.Event) {
	p.events = append(p.events, event)
}
func (*executorTestPublisher) Subscribe(events.EventType, events.Handler) {}

func TestExecuteStepTrafficActionsFailWhenRuntimeCannotApplyTraffic(t *testing.T) {
	rt := &executorTestRuntime{}
	executor := NewExecutor(&executorTestRepo{}, rt, rt, &executorTestPublisher{}, zap.NewNop())

	for _, step := range []domain.RolloutStep{
		{Action: domain.StepActionShiftTraffic, Config: map[string]any{"weight": float64(50)}},
		{Action: domain.StepActionSwitch, Config: map[string]any{"from": "blue", "to": "green"}},
	} {
		if err := executor.executeStep(t.Context(), &step, "api", "registry.example/api:new"); err == nil {
			t.Fatalf("executeStep(%s) returned nil without applying traffic", step.Action)
		}
	}
}

func TestExecuteStepTrafficShiftPropagatesRuntimeFailure(t *testing.T) {
	rt := &executorTrafficRuntime{trafficErr: errors.New("load balancer rejected update")}
	executor := NewExecutor(&executorTestRepo{}, rt, rt, &executorTestPublisher{}, zap.NewNop())
	step := &domain.RolloutStep{Action: domain.StepActionShiftTraffic, Config: map[string]any{"weight": float64(50)}}

	err := executor.executeStep(t.Context(), step, "api", "registry.example/api:new")
	if err == nil || !strings.Contains(err.Error(), "load balancer rejected update") {
		t.Fatalf("executeStep() error = %v, want traffic controller failure", err)
	}
	if rt.shifts != 1 {
		t.Fatalf("traffic shift calls = %d, want 1", rt.shifts)
	}
}

func TestAutoRollbackRestoresAndVerifiesPreviousArtifact(t *testing.T) {
	rt := &executorRestoreRuntime{observation: &domain.RuntimeObservation{
		HealthStatus: domain.HealthStatusUnhealthy, ObservedImageRepo: "registry.example/api", ObservedImageDigest: "sha256:broken",
	}}
	publisher := &executorTestPublisher{}
	executor := NewExecutor(&executorTestRepo{}, rt, rt, publisher, zap.NewNop())
	plan := &domain.RolloutPlan{ID: uuid.New(), Strategy: domain.DeployStrategyReplace, Status: domain.RolloutStatusRunning}

	if err := executor.autoRollback(t.Context(), plan, "api", "registry.example/api@sha256:old"); err != nil {
		t.Fatalf("autoRollback() error = %v", err)
	}
	if len(rt.deployed) != 1 || rt.deployed[0] != "api:registry.example/api@sha256:old" {
		t.Fatalf("restored deployments = %#v", rt.deployed)
	}
	if plan.Status != domain.RolloutStatusRolledBack {
		t.Fatalf("plan status = %s, want rolled_back", plan.Status)
	}
}

func TestAutoRollbackDoesNotReportSuccessWhenCleanupFails(t *testing.T) {
	rt := &executorTestRuntime{undeployErr: errors.New("runtime cleanup failed")}
	publisher := &executorTestPublisher{}
	executor := NewExecutor(&executorTestRepo{}, rt, rt, publisher, zap.NewNop())
	plan := &domain.RolloutPlan{ID: uuid.New(), Strategy: domain.DeployStrategyReplace, Status: domain.RolloutStatusRunning}

	_ = executor.autoRollback(t.Context(), plan, "api", "registry.example/api@sha256:old")

	if plan.Status == domain.RolloutStatusRolledBack {
		t.Fatal("rollback was reported successful despite runtime cleanup failure")
	}
	for _, event := range publisher.events {
		if event.Type == "rollout.rolled_back" {
			t.Fatal("rollout.rolled_back was published despite runtime cleanup failure")
		}
	}
	if plan.CompletedAt == nil || time.Since(*plan.CompletedAt) > time.Second {
		t.Fatalf("rollback failure did not produce a terminal timestamp: %#v", plan.CompletedAt)
	}
}
