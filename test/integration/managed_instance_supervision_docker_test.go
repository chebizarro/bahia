//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestManagedInstanceSupervisionDocker(t *testing.T) {
	if os.Getenv("INTEGRATION_TEST") != "1" {
		t.Skip("set INTEGRATION_TEST=1 to run disposable Docker supervision coverage")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI is unavailable")
	}
	if output, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker daemon is unavailable: %s", strings.TrimSpace(string(output)))
	}

	image := os.Getenv("BAHIA_INTEGRATION_DOCKER_IMAGE")
	if image == "" {
		image = "alpine:3.20"
	}
	if err := dockerEnsureImage(image); err != nil {
		t.Skipf("integration image unavailable: %v", err)
	}

	ctx := context.Background()
	observer := runtimeadapter.NewDockerObserver(os.Getenv("DOCKER_HOST"), zap.NewNop())
	serviceID, environmentID, deploymentUnitID := uuid.New(), uuid.New(), uuid.New()
	prefix := "bahia-supervision-" + strings.ToLower(uuid.NewString()[:8])

	newKey := func(target string) domain.ManagedInstanceKey {
		return domain.ManagedInstanceKey{ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: deploymentUnitID, RuntimeTargetName: target}
	}
	createArgs := func(name string) []string {
		return []string{"--name", name, "--label", "bahia.service_id=" + serviceID.String(), "--label", "bahia.environment_id=" + environmentID.String()}
	}
	cleanup := func(name string) {
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	}

	t.Run("classifies stopped unhealthy and oom-like exits", func(t *testing.T) {
		stoppedName := prefix + "-stopped"
		cleanup(stoppedName)
		dockerMust(t, append([]string{"create"}, append(createArgs(stoppedName), image, "sleep", "300")...)...)
		assertSupervisorStatus(t, observer, newKey(stoppedName), domain.InstanceHealthStatusStopped)

		unhealthyName := prefix + "-unhealthy"
		cleanup(unhealthyName)
		dockerMust(t, append([]string{"run", "-d"}, append(createArgs(unhealthyName), "--health-cmd", "false", "--health-interval", "100ms", "--health-timeout", "1s", "--health-retries", "1", image, "sleep", "300")...)...)
		waitForStatus(t, 15*time.Second, func() bool {
			observation, err := observer.ObserveInstance(ctx, newKey(unhealthyName))
			return err == nil && observation.Status == domain.InstanceHealthStatusUnhealthy
		})
		assertSupervisorStatus(t, observer, newKey(unhealthyName), domain.InstanceHealthStatusUnhealthy)

		oomName := prefix + "-oom"
		cleanup(oomName)
		dockerMust(t, append([]string{"run", "-d", "--memory", "16m"}, append(createArgs(oomName), image, "sh", "-c", "x=$(head -c 64m /dev/zero); sleep 30")...)...)
		waitForStatus(t, 20*time.Second, func() bool {
			output, err := dockerOutput("inspect", "-f", "{{.State.OOMKilled}} {{.State.Running}}", oomName)
			return err == nil && strings.TrimSpace(output) == "true false"
		})
		assertSupervisorStatus(t, observer, newKey(oomName), domain.InstanceHealthStatusOOMKilled)
	})

	t.Run("restarts only the target and respects maintenance", func(t *testing.T) {
		targetName, decoyName := prefix+"-target", prefix+"-decoy"
		cleanup(targetName)
		cleanup(decoyName)
		dockerMust(t, append([]string{"run", "-d"}, append(createArgs(targetName), image, "sleep", "300")...)...)
		dockerMust(t, "stop", targetName)
		dockerMust(t, append([]string{"run", "-d"}, append(createArgs(decoyName), image, "sleep", "300")...)...)
		targetStartedBefore, err := dockerOutput("inspect", "-f", "{{.State.StartedAt}}", targetName)
		if err != nil {
			t.Fatal(err)
		}
		decoyStartedBefore, err := dockerOutput("inspect", "-f", "{{.State.StartedAt}}", decoyName)
		if err != nil {
			t.Fatal(err)
		}

		repo := newDockerHealthRepo()
		spec := recoverySpec(newKey(targetName), observer)
		supervisor, err := service.NewManagedInstanceSupervisor(service.StaticSupervisionSpecSource{spec}, repo, dockerTryLocker{}, &events.NoopPublisher{}, time.Second, zap.NewNop())
		if err != nil {
			t.Fatal(err)
		}
		if err := supervisor.EvaluateOnce(ctx); err != nil {
			t.Fatalf("targeted recovery evaluation: %v", err)
		}
		waitForStatus(t, 10*time.Second, func() bool {
			observation, observeErr := observer.ObserveInstance(ctx, spec.Key)
			return observeErr == nil && (observation.Status == domain.InstanceHealthStatusRunning || observation.Status == domain.InstanceHealthStatusHealthy)
		})
		targetStartedAfter, err := dockerOutput("inspect", "-f", "{{.State.StartedAt}}", targetName)
		if err != nil {
			t.Fatal(err)
		}
		if targetStartedAfter == targetStartedBefore {
			t.Fatalf("target container was not restarted: started_at=%s", targetStartedAfter)
		}
		decoyStartedAfter, err := dockerOutput("inspect", "-f", "{{.State.StartedAt}}", decoyName)
		if err != nil {
			t.Fatal(err)
		}
		if decoyStartedAfter != decoyStartedBefore {
			t.Fatalf("decoy container was touched: before=%s after=%s", decoyStartedBefore, decoyStartedAfter)
		}

		dockerMust(t, "stop", targetName)
		if _, err := supervisor.SetMaintenanceOverride(ctx, spec.Key, "integration-operator", "planned maintenance", nil); err != nil {
			t.Fatal(err)
		}
		startedBefore, err := dockerOutput("inspect", "-f", "{{.State.StartedAt}}", targetName)
		if err != nil {
			t.Fatal(err)
		}
		if err := supervisor.EvaluateOnce(ctx); err != nil {
			t.Fatalf("maintenance evaluation: %v", err)
		}
		startedAfter, err := dockerOutput("inspect", "-f", "{{.State.StartedAt}}", targetName)
		if err != nil {
			t.Fatal(err)
		}
		if startedAfter != startedBefore {
			t.Fatalf("maintenance override did not suppress restart: before=%s after=%s", startedBefore, startedAfter)
		}
	})
}

func assertSupervisorStatus(t *testing.T, observer *runtimeadapter.DockerObserver, key domain.ManagedInstanceKey, expected domain.InstanceHealthStatus) {
	t.Helper()
	repo := newDockerHealthRepo()
	spec := recoverySpec(key, observer)
	spec.RecoveryPolicy.ObserveOnly = true
	supervisor, err := service.NewManagedInstanceSupervisor(service.StaticSupervisionSpecSource{spec}, repo, dockerTryLocker{}, &events.NoopPublisher{}, time.Second, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.EvaluateOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	health, err := repo.GetHealth(context.Background(), key)
	if err != nil || health == nil {
		t.Fatalf("health missing: health=%v err=%v", health, err)
	}
	if health.Status != expected {
		t.Fatalf("status=%s want=%s", health.Status, expected)
	}
}

func recoverySpec(key domain.ManagedInstanceKey, observer *runtimeadapter.DockerObserver) service.SupervisionSpec {
	return service.SupervisionSpec{
		Key: key, Host: "docker-integration", SupervisorType: domain.InstanceSupervisorDocker,
		DesiredRunning: true, Observer: observer, Controller: observer,
		RecoveryPolicy: domain.RecoveryPolicy{Enabled: true, RestartBudget: domain.RestartBudget{MaxAttempts: 3, Window: time.Hour}},
	}
}

type dockerTryLocker struct{}

func (dockerTryLocker) TryLock(context.Context, uuid.UUID) (func(), bool, error) {
	return func() {}, true, nil
}

type dockerHealthRepo struct {
	mu        sync.Mutex
	health    map[string]domain.ManagedInstanceHealth
	events    []domain.ManagedInstanceHealthEvent
	attempts  []domain.RecoveryAttempt
	overrides map[string]domain.MaintenanceOverride
}

func newDockerHealthRepo() *dockerHealthRepo {
	return &dockerHealthRepo{health: map[string]domain.ManagedInstanceHealth{}, overrides: map[string]domain.MaintenanceOverride{}}
}
func dockerKey(key domain.ManagedInstanceKey) string {
	return key.ServiceID.String() + "/" + key.EnvironmentID.String() + "/" + key.DeploymentUnitID.String() + "/" + key.RuntimeTargetName
}
func (r *dockerHealthRepo) UpsertHealth(_ context.Context, health *domain.ManagedInstanceHealth) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.health[dockerKey(health.ManagedInstanceKey)] = *health
	return nil
}
func (r *dockerHealthRepo) UpsertHealthWithEvent(_ context.Context, health *domain.ManagedInstanceHealth, event *domain.ManagedInstanceHealthEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.health[dockerKey(health.ManagedInstanceKey)] = *health
	r.events = append(r.events, *event)
	return nil
}
func (r *dockerHealthRepo) GetHealth(_ context.Context, key domain.ManagedInstanceKey) (*domain.ManagedInstanceHealth, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	health, ok := r.health[dockerKey(key)]
	if !ok {
		return nil, nil
	}
	return &health, nil
}
func (r *dockerHealthRepo) ListHealth(context.Context, repository.ManagedInstanceHealthListOptions) ([]repository.ManagedInstanceHealthListItem, error) {
	return nil, nil
}
func (r *dockerHealthRepo) ListAllHealth(context.Context) ([]domain.ManagedInstanceHealth, error) {
	return nil, nil
}
func (r *dockerHealthRepo) ListHealthByEnvironment(context.Context, uuid.UUID) ([]domain.ManagedInstanceHealth, error) {
	return nil, nil
}
func (r *dockerHealthRepo) ListHealthByService(context.Context, uuid.UUID) ([]domain.ManagedInstanceHealth, error) {
	return nil, nil
}
func (r *dockerHealthRepo) ListUnhealthy(context.Context) ([]domain.ManagedInstanceHealth, error) {
	return nil, nil
}
func (r *dockerHealthRepo) AppendHealthEvent(_ context.Context, event *domain.ManagedInstanceHealthEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, *event)
	return nil
}
func (r *dockerHealthRepo) ListRecentHealthEvents(context.Context, domain.ManagedInstanceKey, int) ([]domain.ManagedInstanceHealthEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.ManagedInstanceHealthEvent(nil), r.events...), nil
}
func (r *dockerHealthRepo) RecordRecoveryAttempt(_ context.Context, attempt *domain.RecoveryAttempt) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.attempts {
		if existing.CorrelationID == attempt.CorrelationID {
			return false, nil
		}
	}
	r.attempts = append(r.attempts, *attempt)
	return true, nil
}
func (r *dockerHealthRepo) CompleteRecoveryAttempt(_ context.Context, correlationID string, result domain.RecoveryAttemptResult, evidence string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.attempts {
		if r.attempts[i].CorrelationID == correlationID && r.attempts[i].Result == domain.RecoveryAttemptPending {
			r.attempts[i].Result, r.attempts[i].Evidence = result, evidence
			return true, nil
		}
	}
	return false, nil
}
func (r *dockerHealthRepo) CompleteRecoveryAttemptWithHealthEvent(_ context.Context, correlationID string, result domain.RecoveryAttemptResult, evidence string, health *domain.ManagedInstanceHealth, event *domain.ManagedInstanceHealthEvent) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.attempts {
		if r.attempts[i].CorrelationID == correlationID && r.attempts[i].Result == domain.RecoveryAttemptPending {
			r.attempts[i].Result, r.attempts[i].Evidence = result, evidence
			r.health[dockerKey(health.ManagedInstanceKey)] = *health
			if event != nil {
				r.events = append(r.events, *event)
			}
			return true, nil
		}
	}
	return false, nil
}
func (r *dockerHealthRepo) ListRecentRecoveryAttempts(context.Context, domain.ManagedInstanceKey, int) ([]domain.RecoveryAttempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.RecoveryAttempt(nil), r.attempts...), nil
}
func (r *dockerHealthRepo) CreateMaintenanceOverride(_ context.Context, override *domain.MaintenanceOverride) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrides[dockerKey(override.ManagedInstanceKey)] = *override
	return nil
}
func (r *dockerHealthRepo) ClearMaintenanceOverride(_ context.Context, key domain.ManagedInstanceKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.overrides, dockerKey(key))
	return nil
}
func (r *dockerHealthRepo) GetActiveMaintenanceOverride(_ context.Context, key domain.ManagedInstanceKey, at time.Time) (*domain.MaintenanceOverride, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	override, ok := r.overrides[dockerKey(key)]
	if !ok || !override.ActiveAt(at) {
		return nil, nil
	}
	return &override, nil
}

func dockerEnsureImage(image string) error {
	if _, err := dockerOutput("image", "inspect", image); err == nil {
		return nil
	}
	_, err := dockerOutput("pull", image)
	return err
}
func dockerMust(t *testing.T, args ...string) string {
	t.Helper()
	output, err := dockerOutput(args...)
	if err != nil {
		t.Fatalf("docker %s: %v", strings.Join(args, " "), err)
	}
	return output
}
func dockerOutput(args ...string) (string, error) {
	output, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
func waitForStatus(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out waiting for Docker state")
}
