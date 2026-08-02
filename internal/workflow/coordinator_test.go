package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// --- Minimal mock repos for coordinator tests ---

type stubServiceRepo struct{ svc *domain.Service }

func (m *stubServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	if svc.ID == uuid.Nil {
		svc.ID = uuid.New()
	}
	m.svc = svc
	return nil
}
func (m *stubServiceRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Service, error) {
	return m.svc, nil
}
func (m *stubServiceRepo) GetByName(_ context.Context, _ string) (*domain.Service, error) {
	return m.svc, nil
}
func (m *stubServiceRepo) List(_ context.Context) ([]domain.Service, error) { return nil, nil }
func (m *stubServiceRepo) ListByOrg(_ context.Context, _ uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (m *stubServiceRepo) Update(_ context.Context, _ *domain.Service) error { return nil }
func (m *stubServiceRepo) Delete(_ context.Context, _ uuid.UUID) error       { return nil }

type stubEnvRepo struct{ env *domain.Environment }

func (m *stubEnvRepo) Create(_ context.Context, env *domain.Environment) error {
	if env.ID == uuid.Nil {
		env.ID = uuid.New()
	}
	m.env = env
	return nil
}
func (m *stubEnvRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Environment, error) {
	return m.env, nil
}
func (m *stubEnvRepo) GetByName(_ context.Context, _ string) (*domain.Environment, error) {
	return m.env, nil
}
func (m *stubEnvRepo) List(_ context.Context) ([]domain.Environment, error) { return nil, nil }
func (m *stubEnvRepo) ListByOrg(_ context.Context, _ uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (m *stubEnvRepo) Update(_ context.Context, _ *domain.Environment) error { return nil }
func (m *stubEnvRepo) Delete(_ context.Context, _ uuid.UUID) error           { return nil }

type stubBuildRepo struct{}

func (m *stubBuildRepo) Create(_ context.Context, b *domain.Build) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}
func (m *stubBuildRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Build, error) {
	return nil, nil
}
func (m *stubBuildRepo) ListByService(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.Build, error) {
	return nil, nil
}
func (m *stubBuildRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ domain.BuildStatus) error {
	return nil
}
func (m *stubBuildRepo) GetByCISystemRunID(_ context.Context, _, _ string) (*domain.Build, error) {
	return nil, nil
}

type stubArtifactRepo struct{ art *domain.Artifact }

func (m *stubArtifactRepo) Create(_ context.Context, a *domain.Artifact) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	m.art = a
	return nil
}
func (m *stubArtifactRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Artifact, error) {
	return m.art, nil
}
func (m *stubArtifactRepo) GetByDigest(_ context.Context, _, _ string) (*domain.Artifact, error) {
	return nil, nil
}
func (m *stubArtifactRepo) ListByService(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.Artifact, error) {
	return nil, nil
}
func (m *stubArtifactRepo) ListByBuild(_ context.Context, _ uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}
func (m *stubArtifactRepo) GetByImageRepoDigest(_ context.Context, _, _ string) (*domain.Artifact, error) {
	return nil, nil
}

type stubIntentRepo struct {
	mu      sync.Mutex
	intents map[uuid.UUID]*domain.DeploymentIntent
}

func newStubIntentRepo() *stubIntentRepo {
	return &stubIntentRepo{intents: make(map[uuid.UUID]*domain.DeploymentIntent)}
}

func (m *stubIntentRepo) Create(_ context.Context, di *domain.DeploymentIntent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if di.ID == uuid.Nil {
		di.ID = uuid.New()
	}
	now := time.Now().UTC()
	di.CreatedAt = now
	di.UpdatedAt = now
	m.intents[di.ID] = di
	return nil
}
func (m *stubIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.intents[id], nil
}
func (m *stubIntentRepo) ListByServiceEnv(_ context.Context, _, _ uuid.UUID, _, _ int) ([]domain.DeploymentIntent, error) {
	return nil, nil
}
func (m *stubIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if di, ok := m.intents[id]; ok {
		di.Status = status
	}
	return nil
}
func (m *stubIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if di, ok := m.intents[id]; ok {
		di.ApprovalStatus = status
	}
	return nil
}
func (m *stubIntentRepo) UpdateDesiredState(_ context.Context, id uuid.UUID, desiredState *domain.DesiredServiceSpec, desiredHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if di, ok := m.intents[id]; ok {
		di.DesiredState = desiredState
		di.DesiredHash = desiredHash
	}
	return nil
}
func (m *stubIntentRepo) GetByHiveResultEventID(_ context.Context, _ string) (*domain.DeploymentIntent, error) {
	return nil, nil
}

type stubRunRepo struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*domain.DeploymentRun
}

func newStubRunRepo() *stubRunRepo {
	return &stubRunRepo{runs: make(map[uuid.UUID]*domain.DeploymentRun)}
}

func (m *stubRunRepo) Create(_ context.Context, dr *domain.DeploymentRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dr.ID == uuid.Nil {
		dr.ID = uuid.New()
	}
	m.runs[dr.ID] = dr
	return nil
}
func (m *stubRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[id], nil
}
func (m *stubRunRepo) ListByIntent(_ context.Context, _ uuid.UUID) ([]domain.DeploymentRun, error) {
	return nil, nil
}
func (m *stubRunRepo) ListNonTerminal(_ context.Context) ([]domain.DeploymentRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var runs []domain.DeploymentRun
	for _, run := range m.runs {
		if run.Status == domain.RunStatusQueued || run.Status == domain.RunStatusRunning {
			runs = append(runs, *run)
		}
	}
	return runs, nil
}
func (m *stubRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dr, ok := m.runs[id]; ok {
		dr.Status = status
		dr.ExitCode = exitCode
	}
	return nil
}

type stubObsRepo struct{}

func (m *stubObsRepo) Create(_ context.Context, obs *domain.RuntimeObservation) error {
	if obs.ID == uuid.Nil {
		obs.ID = uuid.New()
	}
	return nil
}
func (m *stubObsRepo) GetLatest(_ context.Context, _, _ uuid.UUID) (*domain.RuntimeObservation, error) {
	return nil, nil
}
func (m *stubObsRepo) ListByServiceEnv(_ context.Context, _, _ uuid.UUID, _ int) ([]domain.RuntimeObservation, error) {
	return nil, nil
}

type stubStateRepo struct {
	mu     sync.Mutex
	states map[string]*domain.EnvironmentServiceState
}

func newStubStateRepo() *stubStateRepo {
	return &stubStateRepo{states: make(map[string]*domain.EnvironmentServiceState)}
}

func stateKey(svcID, envID uuid.UUID) string {
	return svcID.String() + ":" + envID.String()
}

func (m *stubStateRepo) Upsert(_ context.Context, s *domain.EnvironmentServiceState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[stateKey(s.ServiceID, s.EnvironmentID)] = s
	return nil
}
func (m *stubStateRepo) Get(_ context.Context, svcID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[stateKey(svcID, envID)], nil
}
func (m *stubStateRepo) ListByEnvironment(_ context.Context, _ uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (m *stubStateRepo) ListByService(_ context.Context, _ uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (m *stubStateRepo) ListDrifted(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (m *stubStateRepo) ListDueForObservation(_ context.Context, _ time.Time) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (m *stubStateRepo) ListAll(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

type stubDeploymentUnitRepo struct {
	units map[uuid.UUID]*domain.DeploymentUnit
}

func (r *stubDeploymentUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	if unit.ID == uuid.Nil {
		unit.ID = uuid.New()
	}
	if r.units == nil {
		r.units = map[uuid.UUID]*domain.DeploymentUnit{}
	}
	copy := *unit
	r.units[unit.ID] = &copy
	return nil
}

func (r *stubDeploymentUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	return r.units[id], nil
}

func (r *stubDeploymentUnitRepo) GetByEnvironmentKey(_ context.Context, envID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	for _, unit := range r.units {
		if unit.EnvironmentID == envID && unit.Key == key {
			copy := *unit
			return &copy, nil
		}
	}
	return nil, nil
}

func (r *stubDeploymentUnitRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.DeploymentUnit, error) {
	var units []domain.DeploymentUnit
	for _, unit := range r.units {
		if unit.EnvironmentID == envID {
			units = append(units, *unit)
		}
	}
	return units, nil
}

func (r *stubDeploymentUnitRepo) ResolveDefault(ctx context.Context, env *domain.Environment) (*domain.DeploymentUnit, error) {
	if unit, err := r.GetByEnvironmentKey(ctx, env.ID, domain.DefaultDeploymentUnitKey); err != nil || unit != nil {
		return unit, err
	}
	return domain.NewImplicitDefaultDeploymentUnit(env)
}

type stubDeploymentRuntimeLifecycle struct {
	err  error
	unit *domain.DeploymentUnit
}

func (s *stubDeploymentRuntimeLifecycle) DeployDeploymentUnit(
	_ context.Context,
	_, _ uuid.UUID,
	_ *uuid.UUID,
	unit *domain.DeploymentUnit,
	_ *domain.DesiredServiceSpec,
) (*domain.RuntimeObservation, error) {
	s.unit = unit
	if s.err != nil {
		return nil, s.err
	}
	return &domain.RuntimeObservation{}, nil
}

type unitRuntimeResolver struct {
	rt   runtimeadapter.Runtime
	unit *domain.DeploymentUnit
}

func (r *unitRuntimeResolver) Resolve(_ *domain.Service, _ *domain.Environment) (runtimeadapter.Runtime, error) {
	return nil, fmt.Errorf("environment-only resolver must not be used for deployment-unit routing")
}

func (r *unitRuntimeResolver) ResolveDeploymentUnit(_ *domain.Service, _ *domain.Environment, unit *domain.DeploymentUnit) (runtimeadapter.Runtime, error) {
	copy := *unit
	r.unit = &copy
	return r.rt, nil
}

type renderingComposeRuntime struct {
	rendered        *runtimeadapter.RenderResult
	applyRequest    runtimeadapter.DesiredStateApplyRequest
	imperativeCalls int
}

func (r *renderingComposeRuntime) Type() domain.RuntimeType { return domain.RuntimeTypeCompose }

func (r *renderingComposeRuntime) Deploy(_ context.Context, _, _ string, _ runtimeadapter.DeployOptions) error {
	r.imperativeCalls++
	return nil
}

func (r *renderingComposeRuntime) Undeploy(_ context.Context, _ string) error { return nil }

func (r *renderingComposeRuntime) StreamLogs(_ context.Context, _ string, _ runtimeadapter.LogOptions) (<-chan runtimeadapter.LogEntry, error) {
	return nil, nil
}

func (r *renderingComposeRuntime) SupportsDesiredState() bool { return true }

func (r *renderingComposeRuntime) ApplyDesiredState(ctx context.Context, req runtimeadapter.DesiredStateApplyRequest) (*runtimeadapter.DesiredStateApplyResult, error) {
	r.applyRequest = req
	req.EnvironmentPlan.NormalizeUnitIdentity()
	req.EnvironmentPlan.GroupByDeploymentUnit()
	for i := range req.EnvironmentPlan.UnitPlans {
		unitPlan := &req.EnvironmentPlan.UnitPlans[i]
		if req.TargetService.DeploymentUnitID != nil && unitPlan.DeploymentUnitID != nil &&
			*req.TargetService.DeploymentUnitID == *unitPlan.DeploymentUnitID {
			rendered, err := runtimeadapter.NewComposeRenderer().RenderDeploymentUnitPlan(ctx, req.EnvironmentPlan.EnvironmentID.String(), unitPlan)
			if err != nil {
				return nil, err
			}
			r.rendered = rendered
			return &runtimeadapter.DesiredStateApplyResult{
				Renderer:            "compose",
				ExecutionMode:       runtimeadapter.ExecutionModeSDK,
				DesiredHash:         req.TargetService.DesiredHash,
				EnvironmentRevision: unitPlan.RevisionHash,
				ResourceNames:       []string{req.TargetService.StableServiceKey},
			}, nil
		}
	}
	return nil, fmt.Errorf("target deployment unit missing from environment plan")
}

func (r *renderingComposeRuntime) Observe(_ context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: "sha256:max",
		ObservedContainerID: serviceName + "-container",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "max-compose-fixture",
		ObservedAt:          time.Now().UTC(),
	}, nil
}

// --- Mock Loom Client ---

type stubLoomClient struct {
	status       *loom.JobStatus
	err          error
	awaitFn      func(context.Context, string, string) (*loom.JobStatus, error)
	jobTimeout   time.Duration
	gotJobID     string
	gotWorkerKey string
	submitCalls  int
	awaitCalls   int
	lastJobReq   loom.JobRequest
}

func (s *stubLoomClient) SubmitJob(_ context.Context, req loom.JobRequest) (string, error) {
	s.submitCalls++
	s.lastJobReq = req
	return "stub-job", nil
}

func (s *stubLoomClient) AwaitJobStatusFromWorker(ctx context.Context, jobID string, workerPubkey string, _ ...loom.StatusCallback) (*loom.JobStatus, error) {
	s.gotJobID = jobID
	s.gotWorkerKey = workerPubkey
	s.awaitCalls++
	if s.awaitFn != nil {
		return s.awaitFn(ctx, jobID, workerPubkey)
	}
	return s.status, s.err
}

func (s *stubLoomClient) JobTimeout() time.Duration {
	if s.jobTimeout > 0 {
		return s.jobTimeout
	}
	return time.Minute
}

// mockLoomClient is a controllable fake that replaces the real loom.Client.
// Since loom.Client is a concrete struct (not an interface), we test the coordinator
// indirectly through its public behavior. For direct unit tests, we verify the
// Coordinator's Shutdown and goroutine tracking logic.

type coordinatorWorkerRepo struct {
	workers        map[string]domain.Worker
	degradeOnFetch bool
}

func (r *coordinatorWorkerRepo) Upsert(_ context.Context, w *domain.Worker) error {
	if r.workers == nil {
		r.workers = map[string]domain.Worker{}
	}
	r.workers[w.PubKey] = *w
	return nil
}

func (r *coordinatorWorkerRepo) GetByPubKey(_ context.Context, pubkey string) (*domain.Worker, error) {
	w, ok := r.workers[pubkey]
	if !ok {
		return nil, nil
	}
	if r.degradeOnFetch {
		w.Pressure = &domain.WorkerPressureAssessment{
			OverallLevel:      domain.WorkerPressureCritical,
			CapacityClass:     domain.WorkerCapacityBlocked,
			RecommendedAction: domain.WorkerPressureActionOperatorIntervention,
			AssessedAt:        time.Now().UTC(),
		}
	}
	return &w, nil
}

func (r *coordinatorWorkerRepo) List(_ context.Context, status string, limit int) ([]domain.Worker, error) {
	workers := make([]domain.Worker, 0, len(r.workers))
	for _, w := range r.workers {
		if status != "" && string(w.Status) != status {
			continue
		}
		workers = append(workers, w)
		if len(workers) >= limit {
			break
		}
	}
	return workers, nil
}

func (r *coordinatorWorkerRepo) UpdateStatus(_ context.Context, pubkey string, status domain.WorkerStatus) error {
	w, ok := r.workers[pubkey]
	if !ok {
		return nil
	}
	w.Status = status
	r.workers[pubkey] = w
	return nil
}

// --- Test Helpers ---

func newTestCoordinatorDeps() (
	*stubServiceRepo,
	*stubEnvRepo,
	*stubArtifactRepo,
	*stubIntentRepo,
	*stubRunRepo,
	*stubStateRepo,
) {
	svcRepo := &stubServiceRepo{}
	envRepo := &stubEnvRepo{}
	artRepo := &stubArtifactRepo{}
	intentRepo := newStubIntentRepo()
	runRepo := newStubRunRepo()
	stateRepo := newStubStateRepo()
	return svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo
}

func newTestRegistry(
	svcRepo repository.ServiceRepository,
	envRepo repository.EnvironmentRepository,
	artRepo repository.ArtifactRepository,
	intentRepo repository.DeploymentIntentRepository,
	runRepo repository.DeploymentRunRepository,
	stateRepo repository.EnvironmentServiceStateRepository,
) *service.RegistryService {
	return service.NewRegistryService(
		svcRepo, envRepo, &stubBuildRepo{}, artRepo,
		intentRepo, runRepo, &stubObsRepo{}, stateRepo,
		nil, &events.NoopPublisher{}, zap.NewNop(),
	)
}

func createCoordinatorTestServiceEnvArtifact(t *testing.T, svcRepo *stubServiceRepo, envRepo *stubEnvRepo, artRepo *stubArtifactRepo) (*domain.Service, *domain.Environment, *domain.Artifact) {
	t.Helper()
	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "harbor/test"}
	if err := svcRepo.Create(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	env := &domain.Environment{Name: "staging", DeployStrategy: domain.DeployStrategyReplace}
	if err := envRepo.Create(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	art := &domain.Artifact{BuildID: uuid.New(), ServiceID: svc.ID, ImageRepo: "harbor/test", ImageTag: "v1", ImageDigest: "sha256:abc"}
	if err := artRepo.Create(context.Background(), art); err != nil {
		t.Fatal(err)
	}
	return svc, env, art
}

// --- Coordinator Lifecycle Tests ---

func TestNewCoordinator_HasCancellableContext(t *testing.T) {
	logger := zap.NewNop()
	coord := NewCoordinator(nil, nil, &events.NoopPublisher{}, logger)

	// The internal context should not be nil and should not be done yet.
	if coord.ctx == nil {
		t.Fatal("coordinator context should not be nil")
	}
	if coord.cancel == nil {
		t.Fatal("coordinator cancel func should not be nil")
	}

	select {
	case <-coord.ctx.Done():
		t.Fatal("coordinator context should not be cancelled on creation")
	default:
		// Good - not cancelled.
	}
}

func TestShutdown_CancelsContext(t *testing.T) {
	logger := zap.NewNop()
	coord := NewCoordinator(nil, nil, &events.NoopPublisher{}, logger)

	coord.Shutdown(1 * time.Second)

	select {
	case <-coord.ctx.Done():
		// Good - context was cancelled.
	default:
		t.Fatal("coordinator context should be cancelled after Shutdown")
	}
}

func TestShutdown_WaitsForGoroutines(t *testing.T) {
	logger := zap.NewNop()
	coord := NewCoordinator(nil, nil, &events.NoopPublisher{}, logger)

	var goroutineFinished atomic.Bool

	// Simulate a tracked goroutine.
	coord.wg.Add(1)
	go func() {
		defer coord.wg.Done()
		time.Sleep(50 * time.Millisecond)
		goroutineFinished.Store(true)
	}()

	coord.Shutdown(2 * time.Second)

	if !goroutineFinished.Load() {
		t.Fatal("Shutdown should have waited for the goroutine to finish")
	}
}

func TestShutdown_TimesOutOnStuckGoroutine(t *testing.T) {
	logger := zap.NewNop()
	coord := NewCoordinator(nil, nil, &events.NoopPublisher{}, logger)

	// Simulate a goroutine that blocks until context is cancelled but takes a while.
	coord.wg.Add(1)
	go func() {
		defer coord.wg.Done()
		<-coord.ctx.Done()
		// Simulate slow cleanup that exceeds the shutdown timeout.
		time.Sleep(500 * time.Millisecond)
	}()

	start := time.Now()
	coord.Shutdown(100 * time.Millisecond)
	elapsed := time.Since(start)

	// Should return around 100ms (the timeout), not 500ms.
	if elapsed > 300*time.Millisecond {
		t.Fatalf("Shutdown should have timed out after ~100ms, took %v", elapsed)
	}
}

func TestShutdown_Idempotent(t *testing.T) {
	logger := zap.NewNop()
	coord := NewCoordinator(nil, nil, &events.NoopPublisher{}, logger)

	// Multiple shutdowns should not panic.
	coord.Shutdown(1 * time.Second)
	coord.Shutdown(1 * time.Second)
}

func TestAwaitCompletion_ShutdownLeavesRunNonTerminal(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := &domain.DeploymentRun{DeploymentIntentID: intent.ID, LoomJobID: "job-id", WorkerPubkey: "worker-pubkey", Status: domain.RunStatusQueued, StartedAt: &now}
	if err := registry.CreateDeploymentRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	awaitStarted := make(chan struct{})
	stubLoom := &stubLoomClient{awaitFn: func(ctx context.Context, _, _ string) (*loom.JobStatus, error) {
		close(awaitStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop())
	coord.loom = stubLoom
	coord.startCompletionAwait(run)
	<-awaitStarted
	coord.Shutdown(time.Second)

	updated, err := registry.GetDeploymentRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RunStatusQueued {
		t.Fatalf("run status = %s, want queued after shutdown cancellation", updated.Status)
	}
}

func TestAwaitCompletion_DoesNotMutateRunOnUntrustedAwaitError(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := &domain.DeploymentRun{DeploymentIntentID: intent.ID, LoomJobID: "job-id", WorkerPubkey: "worker-pubkey", Status: domain.RunStatusQueued, StartedAt: &now}
	if err := registry.CreateDeploymentRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	stubLoom := &stubLoomClient{err: errors.New("loom job status subscription auth failed")}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop())
	coord.loom = stubLoom
	coord.awaitCompletion(context.Background(), run)

	updated, err := registry.GetDeploymentRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RunStatusQueued {
		t.Fatalf("run status = %s, want queued after untrusted await error", updated.Status)
	}
	if stubLoom.gotWorkerKey != "worker-pubkey" {
		t.Fatalf("worker pubkey passed to Loom = %q, want selected worker", stubLoom.gotWorkerKey)
	}
}

func TestAwaitCompletion_TimeoutUsesStartedAtWallClock(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now().UTC().Add(-2 * time.Minute)
	run := &domain.DeploymentRun{DeploymentIntentID: intent.ID, LoomJobID: "job-id", Status: domain.RunStatusQueued, StartedAt: &startedAt}
	if err := registry.CreateDeploymentRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	stubLoom := &stubLoomClient{jobTimeout: time.Minute, awaitFn: func(ctx context.Context, _, _ string) (*loom.JobStatus, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop())
	coord.loom = stubLoom
	coord.awaitCompletion(context.Background(), run)

	updated, err := registry.GetDeploymentRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RunStatusTimeout {
		t.Fatalf("run status = %s, want timeout", updated.Status)
	}
}

func TestRecoverNonTerminalRuns_ReattachesPersistedLoomJob(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := &domain.DeploymentRun{DeploymentIntentID: intent.ID, LoomJobID: "persisted-job-id", WorkerPubkey: "persisted-worker", Status: domain.RunStatusRunning, StartedAt: &now}
	if err := registry.CreateDeploymentRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: loom.StatusCompleted}}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop())
	coord.loom = stubLoom
	if err := coord.RecoverNonTerminalRuns(context.Background()); err != nil {
		t.Fatalf("RecoverNonTerminalRuns() error = %v", err)
	}
	coord.wg.Wait()

	updated, err := registry.GetDeploymentRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RunStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded after recovery", updated.Status)
	}
	if stubLoom.gotJobID != "persisted-job-id" || stubLoom.gotWorkerKey != "persisted-worker" {
		t.Fatalf("recovered await = (%q, %q), want persisted job and worker", stubLoom.gotJobID, stubLoom.gotWorkerKey)
	}
}

func TestMapLoomStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected domain.DeploymentRunStatus
	}{
		{"succeeded", domain.RunStatusSucceeded},
		{"failed", domain.RunStatusFailed},
		{"cancelled", domain.RunStatusCancelled},
		{"unknown", domain.RunStatusFailed},
		{"", domain.RunStatusFailed},
	}

	for _, tt := range tests {
		result := mapLoomStatus(tt.input)
		if result != tt.expected {
			t.Errorf("mapLoomStatus(%q) = %s, want %s", tt.input, result, tt.expected)
		}
	}
}

func TestExecuteDeployment_RejectsNonApprovedIntent(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()

	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "harbor/test"}
	_ = svcRepo.Create(context.Background(), svc)
	env := &domain.Environment{Name: "staging", DeployStrategy: domain.DeployStrategyReplace}
	_ = envRepo.Create(context.Background(), env)
	art := &domain.Artifact{BuildID: uuid.New(), ServiceID: svc.ID, ImageRepo: "harbor/test", ImageTag: "v1", ImageDigest: "sha256:abc"}
	_ = artRepo.Create(context.Background(), art)

	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	logger := zap.NewNop()
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, logger)

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusPending, Status: domain.IntentStatusPending,
	}
	_ = registry.CreateDeploymentIntent(context.Background(), di)
	// Keep it in pending state.
	intentRepo.mu.Lock()
	intentRepo.intents[di.ID].Status = domain.IntentStatusPending
	intentRepo.mu.Unlock()

	err := coord.ExecuteDeployment(context.Background(), di.ID)
	if err == nil {
		t.Fatal("expected error for non-approved intent")
	}
	if !containsSubstring(err.Error(), "not in approved state") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExecuteDeployment_RejectsNonExistentIntent(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()

	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	logger := zap.NewNop()
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, logger)

	err := coord.ExecuteDeployment(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error for non-existent intent")
	}
	if !containsSubstring(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExecuteDeployment_MaxComposeUnitRendersFullDesiredState(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()

	svc := &domain.Service{Name: "gastown", ArtifactRepo: "ghcr.io/openagents/gastown", RuntimeType: domain.RuntimeTypeDocker}
	if err := svcRepo.Create(ctx, svc); err != nil {
		t.Fatal(err)
	}
	env := &domain.Environment{Name: "max-production", DeployStrategy: domain.DeployStrategyReplace}
	if err := envRepo.Create(ctx, env); err != nil {
		t.Fatal(err)
	}
	art := &domain.Artifact{
		BuildID: uuid.New(), ServiceID: svc.ID,
		ImageRepo: "ghcr.io/openagents/gastown", ImageTag: "v2", ImageDigest: "sha256:max",
	}
	if err := artRepo.Create(ctx, art); err != nil {
		t.Fatal(err)
	}

	unit := &domain.DeploymentUnit{
		ID:            uuid.New(),
		EnvironmentID: env.ID,
		Key:           "max-compose",
		DisplayName:   "max",
		RuntimeType:   domain.RuntimeTypeCompose,
		EndpointRef:   "max-managed",
		ComposeDir:    "/srv/bahia/gastown",
		ReconcileMode: domain.ReconcileModeAutoApply,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
		RuntimeConfig: map[string]any{"execution_mode": "sdk"},
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}

	desired := &domain.DesiredServiceSpec{
		SchemaVersion:     domain.DesiredStateSchemaVersion,
		ServiceID:         svc.ID,
		EnvironmentID:     env.ID,
		DeploymentUnitID:  &unit.ID,
		DeploymentUnitKey: unit.Key,
		UnitRuntimeType:   domain.RuntimeTypeCompose,
		ArtifactID:        art.ID,
		StableServiceKey:  "gastown",
		ImageRef:          "ghcr.io/openagents/gastown:v2",
		Env:               map[string]string{"APP_ENV": "max"},
		SecretRefs: []domain.DesiredSecretRef{{
			EnvVar:        "NSEC",
			Name:          "nostr-signer",
			SecretID:      uuid.New(),
			RedactedValue: domain.RedactedPlaceholder("nostr-signer"),
		}},
		Volumes: []string{
			"gastown-state:/app/state",
			"/run/bahia/secrets/nsec:/run/secrets/nsec:ro",
		},
		RestartPolicy: "unless-stopped",
		Healthcheck: &domain.HealthcheckConfig{
			Test:     []string{"CMD-SHELL", "wget -qO- http://localhost:8080/health || exit 1"},
			Interval: "15s", Timeout: "3s", Retries: 5, StartPeriod: "20s",
		},
		ComposeExtension: &domain.ComposeExtension{
			EnvFile:            ".bahia/env/gastown.env",
			ProjectName:        "gastown-max",
			VolumeDeclarations: []string{"gastown-state"},
		},
	}
	desired.ComputeDesiredHash()

	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	renderingRuntime := &renderingComposeRuntime{}
	resolver := &unitRuntimeResolver{rt: renderingRuntime}
	lifecycle := service.NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artRepo, stateRepo, resolver,
		&events.NoopPublisher{}, zap.NewNop(),
		service.WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	coord := NewCoordinator(
		registry, nil, &events.NoopPublisher{}, zap.NewNop(),
		WithDeploymentUnitRouting(unitRepo, lifecycle),
	)
	coord.loom = stubLoom

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
		DesiredState: desired, DesiredHash: desired.DesiredHash,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	if err := coord.ExecuteDeployment(ctx, di.ID); err != nil {
		t.Fatalf("ExecuteDeployment() error = %v", err)
	}
	if stubLoom.submitCalls != 0 {
		t.Fatalf("Compose unit must not fall back to Loom, got %d submissions", stubLoom.submitCalls)
	}
	if renderingRuntime.imperativeCalls != 0 {
		t.Fatalf("Compose unit must use desired-state apply, got %d imperative deploys", renderingRuntime.imperativeCalls)
	}
	if resolver.unit == nil || resolver.unit.ID != unit.ID || resolver.unit.EndpointRef != "max-managed" {
		t.Fatalf("unit runtime resolution did not receive max target: %#v", resolver.unit)
	}
	if renderingRuntime.rendered == nil {
		t.Fatal("expected rendered Compose project")
	}

	yaml := string(renderingRuntime.rendered.ComposeYAML)
	for _, expected := range []string{
		"env_file:",
		".bahia/env/gastown.env",
		"gastown-state:/app/state",
		"/run/bahia/secrets/nsec:/run/secrets/nsec:ro",
		"restart: unless-stopped",
		"healthcheck:",
		"CMD-SHELL",
		"interval: 15s",
	} {
		if !strings.Contains(yaml, expected) {
			t.Errorf("rendered Compose project missing %q:\n%s", expected, yaml)
		}
	}
	envMaterial := renderingRuntime.rendered.EnvMaterial["gastown"]
	if !strings.Contains(envMaterial, "APP_ENV=max") ||
		!strings.Contains(envMaterial, "NSEC=REDACTED(nostr-signer)") {
		t.Fatalf("rendered env material missing literals or secret reference: %q", envMaterial)
	}
	if strings.Contains(yaml, "NSEC") || strings.Contains(yaml, "nostr-signer") {
		t.Fatalf("secret reference leaked into Compose YAML:\n%s", yaml)
	}

	if len(runRepo.runs) != 1 {
		t.Fatalf("expected 1 deployment run, got %d", len(runRepo.runs))
	}
	for _, run := range runRepo.runs {
		if run.Status != domain.RunStatusSucceeded || run.LoomJobID != "runtime:direct" {
			t.Fatalf("unexpected direct run: %#v", run)
		}
		if run.DeploymentUnitID == nil || *run.DeploymentUnitID != unit.ID {
			t.Fatalf("run deployment unit = %v, want %s", run.DeploymentUnitID, unit.ID)
		}
	}
}

func TestExecuteDeployment_DispatchAdmissionRejectsLatestWorkerPressure(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)

	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	worker := domain.Worker{
		PubKey:              "worker-pressure",
		Name:                "pressure worker",
		Status:              domain.WorkerStatusOnline,
		SchedulingState:     domain.WorkerSchedulingActive,
		MaxConcurrentJobs:   4,
		LastAdvertisementAt: time.Now().UTC(),
	}
	workerRepo := &coordinatorWorkerRepo{workers: map[string]domain.Worker{worker.PubKey: worker}, degradeOnFetch: true}
	workerPolicy := service.NewWorkerPolicyService(workerRepo, zap.NewNop())
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithWorkerPolicy(workerPolicy))
	coord.loom = stubLoom

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(context.Background(), di); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	if err := coord.ExecuteDeployment(context.Background(), di.ID); err != nil {
		t.Fatalf("ExecuteDeployment() error = %v", err)
	}
	if stubLoom.submitCalls != 0 {
		t.Fatalf("expected admission rejection to skip SubmitJob, got %d calls", stubLoom.submitCalls)
	}
	if len(runRepo.runs) != 1 {
		t.Fatalf("expected 1 failed admission run, got %d", len(runRepo.runs))
	}
	for _, run := range runRepo.runs {
		if run.Status != domain.RunStatusFailed {
			t.Fatalf("expected failed run, got %s", run.Status)
		}
		if run.WorkerPubkey != "worker-pressure" {
			t.Fatalf("expected selected worker on failed run, got %q", run.WorkerPubkey)
		}
		if run.LoomJobID != "admission:rejected" {
			t.Fatalf("expected admission rejection marker, got %q", run.LoomJobID)
		}
		if got := run.Metadata["failure_phase"]; got != "dispatch_admission" {
			t.Fatalf("expected dispatch admission failure phase, got %v", got)
		}
		if got := run.Metadata["admission_code"]; got != "capacity_class_rejected" {
			t.Fatalf("expected capacity_class_rejected, got %v", got)
		}
		if got := run.Metadata["capacity_class"]; got != string(domain.WorkerCapacityBlocked) {
			t.Fatalf("expected blocked capacity class, got %v", got)
		}
	}
	intentRepo.mu.Lock()
	intentStatus := intentRepo.intents[di.ID].Status
	intentRepo.mu.Unlock()
	if intentStatus != domain.IntentStatusFailed {
		t.Fatalf("expected failed intent after admission rejection, got %s", intentStatus)
	}
}

func TestExecuteDeployment_WorkerlessLoomPathSkipsDispatchAdmission(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)

	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop())
	coord.loom = stubLoom

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(context.Background(), di); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	if err := coord.ExecuteDeployment(context.Background(), di.ID); err != nil {
		t.Fatalf("ExecuteDeployment() error = %v", err)
	}
	coord.Shutdown(time.Second)
	if stubLoom.submitCalls != 1 {
		t.Fatalf("expected workerless Loom path to submit, got %d calls", stubLoom.submitCalls)
	}
	if stubLoom.lastJobReq.WorkerPubkey != "" {
		t.Fatalf("expected workerless job request, got worker %q", stubLoom.lastJobReq.WorkerPubkey)
	}
	if len(runRepo.runs) != 1 {
		t.Fatalf("expected 1 deployment run, got %d", len(runRepo.runs))
	}
}

func TestExecuteDeployment_ComposeUnitLifecycleFailureMarksRunFailedWithoutLoomFallback(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "max-compose",
		RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "max-managed", ComposeDir: "/srv/bahia/gastown",
		ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	lifecycle := &stubDeploymentRuntimeLifecycle{err: fmt.Errorf("boom")}
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	coord := NewCoordinator(
		registry, nil, &events.NoopPublisher{}, zap.NewNop(),
		WithDeploymentUnitRouting(unitRepo, lifecycle),
	)
	coord.loom = stubLoom

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	err := coord.ExecuteDeployment(ctx, di.ID)
	if err == nil || !containsSubstring(err.Error(), "direct runtime deploy failed") {
		t.Fatalf("expected direct runtime failure, got %v", err)
	}
	if stubLoom.submitCalls != 0 {
		t.Fatalf("Compose lifecycle failure must not fall back to Loom, got %d submissions", stubLoom.submitCalls)
	}
	for _, run := range runRepo.runs {
		if run.Status != domain.RunStatusFailed {
			t.Fatalf("expected failed run, got %s", run.Status)
		}
	}
}

func TestExecuteDeployment_ComposeUnitMissingManagedEndpointFailsClosed(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "compose-without-endpoint",
		RuntimeType: domain.RuntimeTypeCompose, ComposeDir: "/srv/bahia/gastown",
		ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	lifecycle := &stubDeploymentRuntimeLifecycle{}
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	coord := NewCoordinator(
		registry, nil, &events.NoopPublisher{}, zap.NewNop(),
		WithDeploymentUnitRouting(unitRepo, lifecycle),
	)
	coord.loom = stubLoom

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(ctx, di); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	err := coord.ExecuteDeployment(ctx, di.ID)
	if err == nil || !strings.Contains(err.Error(), "managed endpoint_ref") {
		t.Fatalf("expected missing endpoint failure, got %v", err)
	}
	if lifecycle.unit != nil {
		t.Fatal("runtime lifecycle must not execute without a managed endpoint")
	}
	if stubLoom.submitCalls != 0 {
		t.Fatalf("misconfigured Compose unit must not fall back to Loom, got %d submissions", stubLoom.submitCalls)
	}
	if len(runRepo.runs) != 0 {
		t.Fatalf("routing validation should fail before creating a run, got %d", len(runRepo.runs))
	}
}

func TestConcurrentShutdownAndPoll(t *testing.T) {
	// Verify that concurrent Shutdown calls and goroutine completions don't race.
	logger := zap.NewNop()
	coord := NewCoordinator(nil, nil, &events.NoopPublisher{}, logger)

	const numGoroutines = 10
	for i := 0; i < numGoroutines; i++ {
		coord.wg.Add(1)
		go func() {
			defer coord.wg.Done()
			select {
			case <-coord.ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}()
	}

	// Small delay to let goroutines start.
	time.Sleep(10 * time.Millisecond)

	// Shutdown should cancel all goroutines and return.
	coord.Shutdown(2 * time.Second)
}

// containsSubstring checks if s contains sub.
func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Verify the stubbed repos implement the interfaces at compile time.
var (
	_ repository.ServiceRepository                 = (*stubServiceRepo)(nil)
	_ repository.EnvironmentRepository             = (*stubEnvRepo)(nil)
	_ repository.BuildRepository                   = (*stubBuildRepo)(nil)
	_ repository.ArtifactRepository                = (*stubArtifactRepo)(nil)
	_ repository.DeploymentIntentRepository        = (*stubIntentRepo)(nil)
	_ repository.DeploymentRunRepository           = (*stubRunRepo)(nil)
	_ repository.RuntimeObservationRepository      = (*stubObsRepo)(nil)
	_ repository.EnvironmentServiceStateRepository = (*stubStateRepo)(nil)
)
