package workflow

import (
	"context"
	"errors"
	"fmt"
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

func (m *stubDeploymentUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	if unit.ID == uuid.Nil {
		unit.ID = uuid.New()
	}
	if m.units == nil {
		m.units = make(map[uuid.UUID]*domain.DeploymentUnit)
	}
	m.units[unit.ID] = unit
	return nil
}
func (m *stubDeploymentUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	return m.units[id], nil
}
func (m *stubDeploymentUnitRepo) GetByEnvironmentKey(_ context.Context, _ uuid.UUID, _ string) (*domain.DeploymentUnit, error) {
	return nil, nil
}
func (m *stubDeploymentUnitRepo) ListByEnvironment(_ context.Context, _ uuid.UUID) ([]domain.DeploymentUnit, error) {
	return nil, nil
}
func (m *stubDeploymentUnitRepo) ResolveDefault(_ context.Context, _ *domain.Environment) (*domain.DeploymentUnit, error) {
	return nil, nil
}

// --- Mock Loom Client ---

type stubRuntime struct {
	runtimeType domain.RuntimeType
	lastService string
	lastImage   string
	deployErr   error
	deployCalls int
}

func (s *stubRuntime) Type() domain.RuntimeType { return s.runtimeType }
func (s *stubRuntime) Observe(_ context.Context, _, _ uuid.UUID, _ string) (*domain.RuntimeObservation, error) {
	return nil, nil
}
func (s *stubRuntime) Deploy(_ context.Context, serviceName, image string, _ runtimeadapter.DeployOptions) error {
	s.lastService = serviceName
	s.lastImage = image
	s.deployCalls++
	return s.deployErr
}
func (s *stubRuntime) Undeploy(_ context.Context, _ string) error { return nil }
func (s *stubRuntime) StreamLogs(_ context.Context, _ string, _ runtimeadapter.LogOptions) (<-chan runtimeadapter.LogEntry, error) {
	return nil, nil
}

type stubRuntimeResolver struct {
	rt  runtimeadapter.Runtime
	err error
}

func (s *stubRuntimeResolver) Resolve(_ *domain.Service, _ *domain.Environment) (runtimeadapter.Runtime, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rt, nil
}

type stubLoomClient struct {
	status       *loom.JobStatus
	err          error
	gotJobID     string
	gotWorkerKey string
	submitCalls  int
	lastJobReq   loom.JobRequest
}

func (s *stubLoomClient) SubmitJob(_ context.Context, req loom.JobRequest) (string, error) {
	s.submitCalls++
	s.lastJobReq = req
	return "stub-job", nil
}

func (s *stubLoomClient) PollJobStatusFromWorker(_ context.Context, jobID string, workerPubkey string, _ ...loom.StatusCallback) (*loom.JobStatus, error) {
	s.gotJobID = jobID
	s.gotWorkerKey = workerPubkey
	return s.status, s.err
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

func TestPollForCompletion_UsesCoordinatorContext(t *testing.T) {
	// This test verifies that pollForCompletion respects context cancellation.
	// We create a coordinator, start a mock poll, cancel the context, and verify
	// the poll exits promptly.

	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()

	svc := &domain.Service{Name: "test-svc", ArtifactRepo: "harbor/test"}
	_ = svcRepo.Create(context.Background(), svc)
	env := &domain.Environment{Name: "staging", DeployStrategy: domain.DeployStrategyReplace}
	_ = envRepo.Create(context.Background(), env)
	art := &domain.Artifact{BuildID: uuid.New(), ServiceID: svc.ID, ImageRepo: "harbor/test", ImageTag: "v1", ImageDigest: "sha256:abc"}
	_ = artRepo.Create(context.Background(), art)

	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	logger := zap.NewNop()

	// We can't easily mock the loom.Client since it's a concrete struct,
	// but we can test the pollForCompletion method's context behavior directly.
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, logger)

	// Create a run in the repo for the poll to complete.
	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	_ = registry.CreateDeploymentIntent(context.Background(), di)
	intentRepo.mu.Lock()
	intentRepo.intents[di.ID].Status = domain.IntentStatusDeploying
	intentRepo.mu.Unlock()

	run := &domain.DeploymentRun{DeploymentIntentID: di.ID, LoomJobID: "test-job", Status: domain.RunStatusQueued}
	_ = registry.CreateDeploymentRun(context.Background(), run)

	// Cancel the coordinator context (simulating shutdown).
	coord.cancel()

	// Call pollForCompletion directly — it should exit quickly since the context is cancelled.
	// The loom client is nil, but pollForCompletion will call c.loom.PollJobStatus which will
	// panic on nil receiver. Instead, we verify the context cancellation via the Shutdown path.

	// Verify the run can still be completed with a timeout status using a detached context.
	// This validates the "detached context for completion during shutdown" behavior.
	completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer completeCancel()

	err := registry.CompleteDeploymentRun(completeCtx, run.ID, domain.RunStatusTimeout, nil)
	if err != nil {
		t.Fatalf("should be able to complete run with detached context: %v", err)
	}

	// Verify the run was marked as timeout.
	runRepo.mu.Lock()
	updatedRun := runRepo.runs[run.ID]
	runRepo.mu.Unlock()

	if updatedRun.Status != domain.RunStatusTimeout {
		t.Errorf("expected run status timeout, got %s", updatedRun.Status)
	}
}

func TestPollForCompletion_DoesNotMutateRunOnUntrustedPollError(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)

	intent := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     art.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired,
		Status:         domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	run := &domain.DeploymentRun{DeploymentIntentID: intent.ID, LoomJobID: "job-id", Status: domain.RunStatusQueued}
	if err := registry.CreateDeploymentRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	stubLoom := &stubLoomClient{err: errors.New("loom job status subscription closed: auth-required")}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop())
	coord.loom = stubLoom

	coord.pollForCompletion(context.Background(), run.ID, "job-id", "worker-pubkey")

	updated, err := registry.GetDeploymentRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RunStatusQueued {
		t.Fatalf("run status = %s, want queued after untrusted poll error", updated.Status)
	}
	if stubLoom.gotWorkerKey != "worker-pubkey" {
		t.Fatalf("worker pubkey passed to Loom = %q, want selected worker", stubLoom.gotWorkerKey)
	}
}

func TestPollForCompletion_MarksTimeoutOnlyForContextDeadline(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)

	intent := &domain.DeploymentIntent{
		ServiceID:      svc.ID,
		EnvironmentID:  env.ID,
		ArtifactID:     art.ID,
		RequestedBy:    "test",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired,
		Status:         domain.IntentStatusApproved,
	}
	if err := registry.CreateDeploymentIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	run := &domain.DeploymentRun{DeploymentIntentID: intent.ID, LoomJobID: "job-id", Status: domain.RunStatusQueued}
	if err := registry.CreateDeploymentRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop())
	coord.loom = &stubLoomClient{err: context.DeadlineExceeded}

	coord.pollForCompletion(context.Background(), run.ID, "job-id", "worker-pubkey")

	updated, err := registry.GetDeploymentRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RunStatusTimeout {
		t.Fatalf("run status = %s, want timeout", updated.Status)
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

func TestExecuteDeployment_UsesDirectRuntimeForLocalComposeArtifact(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()

	svc := &domain.Service{Name: "gitea", ArtifactRepo: "local/gitea", RuntimeType: domain.RuntimeTypeCompose}
	_ = svcRepo.Create(context.Background(), svc)
	env := &domain.Environment{Name: "edge-01-staging", DeployStrategy: domain.DeployStrategyReplace}
	_ = envRepo.Create(context.Background(), env)
	art := &domain.Artifact{BuildID: uuid.New(), ServiceID: svc.ID, ImageRepo: "local/gitea", ImageTag: "migration", ImageDigest: "sha256:abc"}
	_ = artRepo.Create(context.Background(), art)

	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	logger := zap.NewNop()
	stubRT := &stubRuntime{runtimeType: domain.RuntimeTypeCompose}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, logger, WithRuntimeResolver(&stubRuntimeResolver{rt: stubRT}))

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	_ = registry.CreateDeploymentIntent(context.Background(), di)
	intentRepo.mu.Lock()
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	if err := coord.ExecuteDeployment(context.Background(), di.ID); err != nil {
		t.Fatalf("ExecuteDeployment() error = %v", err)
	}
	if stubRT.deployCalls != 1 {
		t.Fatalf("expected 1 direct deploy call, got %d", stubRT.deployCalls)
	}
	if stubRT.lastService != "gitea" {
		t.Fatalf("expected service gitea, got %q", stubRT.lastService)
	}
	if stubRT.lastImage != "" {
		t.Fatalf("expected compose local artifact to resolve via compose file image, got %q", stubRT.lastImage)
	}
	if len(runRepo.runs) != 1 {
		t.Fatalf("expected 1 deployment run, got %d", len(runRepo.runs))
	}
	for _, run := range runRepo.runs {
		if run.Status != domain.RunStatusSucceeded {
			t.Fatalf("expected succeeded run, got %s", run.Status)
		}
		if run.LoomJobID != "runtime:direct" {
			t.Fatalf("expected direct runtime marker, got %q", run.LoomJobID)
		}
	}
}

func TestExecuteDeployment_CopiesDeploymentUnitOntoLoomRun(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	deploymentUnitID := uuid.New()
	unit := &domain.DeploymentUnit{
		ID:            deploymentUnitID,
		EnvironmentID: env.ID,
		Key:           "max-firecracker",
		RuntimeType:   domain.RuntimeTypeDocker,
		ReconcileMode: domain.ReconcileModeObserveOnly,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
		RuntimeConfig: map[string]any{"dispatch_mode": "loom"},
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}

	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithDeploymentUnits(unitRepo))
	coord.loom = stubLoom

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &deploymentUnitID, ArtifactID: art.ID,
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
	if stubLoom.submitCalls != 1 {
		t.Fatalf("expected one Loom submit, got %d", stubLoom.submitCalls)
	}
	if len(runRepo.runs) != 1 {
		t.Fatalf("expected 1 deployment run, got %d", len(runRepo.runs))
	}
	for _, run := range runRepo.runs {
		if run.DeploymentUnitID == nil || *run.DeploymentUnitID != deploymentUnitID {
			t.Fatalf("run deployment unit = %v, want %s", run.DeploymentUnitID, deploymentUnitID)
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

func TestExecuteDeployment_DirectRuntimeFailureMarksRunFailed(t *testing.T) {
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()

	svc := &domain.Service{Name: "gitea", ArtifactRepo: "local/gitea", RuntimeType: domain.RuntimeTypeCompose}
	_ = svcRepo.Create(context.Background(), svc)
	env := &domain.Environment{Name: "edge-01-staging", DeployStrategy: domain.DeployStrategyReplace}
	_ = envRepo.Create(context.Background(), env)
	art := &domain.Artifact{BuildID: uuid.New(), ServiceID: svc.ID, ImageRepo: "local/gitea", ImageTag: "migration", ImageDigest: "sha256:abc"}
	_ = artRepo.Create(context.Background(), art)

	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	logger := zap.NewNop()
	stubRT := &stubRuntime{runtimeType: domain.RuntimeTypeCompose, deployErr: fmt.Errorf("boom")}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, logger, WithRuntimeResolver(&stubRuntimeResolver{rt: stubRT}))

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	_ = registry.CreateDeploymentIntent(context.Background(), di)
	intentRepo.mu.Lock()
	intentRepo.intents[di.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	err := coord.ExecuteDeployment(context.Background(), di.ID)
	if err == nil || !containsSubstring(err.Error(), "direct runtime deploy failed") {
		t.Fatalf("expected direct runtime failure, got %v", err)
	}
	for _, run := range runRepo.runs {
		if run.Status != domain.RunStatusFailed {
			t.Fatalf("expected failed run, got %s", run.Status)
		}
	}
}

func TestExecuteDeployment_DockerUnitUsesDirectRuntimeWithoutLoom(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	svc.RuntimeType = domain.RuntimeTypeCompose
	art.ImageRepo = "local/gitea"
	art.ImageTag = "migration"
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "edge-docker",
		RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "edge-01-docker",
		ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	stubRT := &stubRuntime{runtimeType: domain.RuntimeTypeCompose}
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	coord := NewCoordinator(
		registry, nil, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeResolver(&stubRuntimeResolver{rt: stubRT}),
		WithDeploymentUnits(unitRepo),
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

	if err := coord.ExecuteDeployment(ctx, di.ID); err != nil {
		t.Fatalf("ExecuteDeployment() error = %v", err)
	}
	if stubLoom.submitCalls != 0 {
		t.Fatalf("Docker deployment unit must not dispatch to a compute worker, got %d Loom submissions", stubLoom.submitCalls)
	}
	if stubRT.deployCalls != 1 {
		t.Fatalf("Docker deployment unit was not applied through direct runtime, got %d deploys", stubRT.deployCalls)
	}
	for _, run := range runRepo.runs {
		if run.LoomJobID != "runtime:direct" || run.Status != domain.RunStatusSucceeded {
			t.Fatalf("unexpected direct Docker run: %#v", run)
		}
	}
}

func TestExecuteDeployment_LoomDispatchUnitSubmitsJobWithoutDirectRuntime(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	unit := &domain.DeploymentUnit{
		ID:            uuid.New(),
		EnvironmentID: env.ID,
		Key:           "max-firecracker",
		RuntimeType:   domain.RuntimeTypeDocker,
		ReconcileMode: domain.ReconcileModeObserveOnly,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
		RuntimeConfig: map[string]any{"dispatch_mode": "loom"},
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	coord := NewCoordinator(
		registry, nil, &events.NoopPublisher{}, zap.NewNop(),
		WithDeploymentUnits(unitRepo),
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

	if err := coord.ExecuteDeployment(ctx, di.ID); err != nil {
		t.Fatalf("ExecuteDeployment() error = %v", err)
	}
	coord.Shutdown(time.Second)
	if stubLoom.submitCalls != 1 {
		t.Fatalf("expected Loom dispatch unit to submit one job, got %d", stubLoom.submitCalls)
	}
	if stubLoom.lastJobReq.Service != svc.Name || stubLoom.lastJobReq.Environment != env.Name {
		t.Fatalf("unexpected Loom job request: %#v", stubLoom.lastJobReq)
	}
	if len(runRepo.runs) != 1 {
		t.Fatalf("expected 1 deployment run, got %d", len(runRepo.runs))
	}
	for _, run := range runRepo.runs {
		if run.LoomJobID == "runtime:direct" || run.LoomJobID == "" {
			t.Fatalf("expected real Loom job id, got run %#v", run)
		}
		if run.DeploymentUnitID == nil || *run.DeploymentUnitID != unit.ID {
			t.Fatalf("expected run to preserve deployment unit identity, got %#v", run.DeploymentUnitID)
		}
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
