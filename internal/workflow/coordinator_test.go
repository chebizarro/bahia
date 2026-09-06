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
	routingadapter "github.com/openagentsinc/bahia/internal/adapters/routing"
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
func (m *stubServiceRepo) List(_ context.Context) ([]domain.Service, error) {
	if m.svc == nil {
		return nil, nil
	}
	return []domain.Service{*m.svc}, nil
}
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
func (m *stubEnvRepo) List(_ context.Context) ([]domain.Environment, error) {
	if m.env == nil {
		return nil, nil
	}
	return []domain.Environment{*m.env}, nil
}
func (m *stubEnvRepo) ListByOrg(_ context.Context, _ uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (m *stubEnvRepo) Update(_ context.Context, _ *domain.Environment) error { return nil }
func (m *stubEnvRepo) Delete(_ context.Context, _ uuid.UUID) error           { return nil }

type stubBuildRepo struct{ build *domain.Build }

func (m *stubBuildRepo) Create(_ context.Context, b *domain.Build) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	m.build = b
	return nil
}
func (m *stubBuildRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	if m.build != nil && m.build.ID == id {
		return m.build, nil
	}
	return nil, nil
}
func (m *stubBuildRepo) ListByService(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.Build, error) {
	return nil, nil
}
func (m *stubBuildRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ domain.BuildStatus) error {
	return nil
}
func (m *stubBuildRepo) GetByCISystemRunID(_ context.Context, ciSystem, ciRunID string) (*domain.Build, error) {
	if m.build != nil && m.build.CISystem == ciSystem && m.build.CIRunID == ciRunID {
		return m.build, nil
	}
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
func (m *stubArtifactRepo) GetByDigest(_ context.Context, repo, digest string) (*domain.Artifact, error) {
	if m.art != nil && m.art.ImageRepo == repo && m.art.ImageDigest == digest {
		return m.art, nil
	}
	return nil, nil
}
func (m *stubArtifactRepo) ListByService(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.Artifact, error) {
	return nil, nil
}
func (m *stubArtifactRepo) ListByBuild(_ context.Context, _ uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}
func (m *stubArtifactRepo) GetByImageRepoDigest(_ context.Context, repo, digest string) (*domain.Artifact, error) {
	return m.GetByDigest(context.Background(), repo, digest)
}

type stubIntentRepo struct {
	mu      sync.Mutex
	intents map[uuid.UUID]*domain.DeploymentIntent
	runs    *stubRunRepo
}

func newStubIntentRepo(runRepos ...*stubRunRepo) *stubIntentRepo {
	runs := newStubRunRepo()
	if len(runRepos) > 0 {
		runs = runRepos[0]
	}
	return &stubIntentRepo{intents: make(map[uuid.UUID]*domain.DeploymentIntent), runs: runs}
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
func (m *stubIntentRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]domain.DeploymentIntent, 0, len(m.intents))
	for _, intent := range m.intents {
		if intent.ServiceID == serviceID && intent.EnvironmentID == envID {
			items = append(items, *intent)
		}
	}
	if offset >= len(items) {
		return nil, nil
	}
	items = items[offset:]
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
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
func (m *stubIntentRepo) GetByHiveResultEventID(_ context.Context, eventID string) (*domain.DeploymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, intent := range m.intents {
		if intent.Metadata != nil && intent.Metadata["hive_ci_result_event_id"] == eventID {
			return intent, nil
		}
	}
	return nil, nil
}
func (m *stubIntentRepo) ListApprovedWithoutRuns(_ context.Context) ([]domain.DeploymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs.mu.Lock()
	defer m.runs.mu.Unlock()

	var intents []domain.DeploymentIntent
	for _, intent := range m.intents {
		if intent.Status != domain.IntentStatusApproved ||
			(intent.ApprovalStatus != domain.ApprovalStatusApproved && intent.ApprovalStatus != domain.ApprovalStatusNotRequired) {
			continue
		}
		hasRun := false
		for _, run := range m.runs.runs {
			if run.DeploymentIntentID == intent.ID {
				hasRun = true
				break
			}
		}
		if !hasRun {
			intents = append(intents, *intent)
		}
	}
	return intents, nil
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
func (m *stubRunRepo) ListByIntent(_ context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var runs []domain.DeploymentRun
	for _, run := range m.runs {
		if run.DeploymentIntentID == intentID {
			runs = append(runs, *run)
		}
	}
	return runs, nil
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
func (m *stubRunRepo) UpdateApplyMetadata(_ context.Context, id uuid.UUID, metadata map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dr, ok := m.runs[id]; ok {
		dr.ApplyMetadata = metadata
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
	m.mu.Lock()
	defer m.mu.Unlock()
	states := make([]domain.EnvironmentServiceState, 0, len(m.states))
	for _, state := range m.states {
		states = append(states, *state)
	}
	return states, nil
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
	envCopy := *env
	domain.NormalizeEnvironmentTargeting(&envCopy)
	units, err := r.ListByEnvironment(ctx, envCopy.ID)
	if err != nil {
		return nil, err
	}
	for i := range units {
		if units[i].Key == envCopy.Targeting.DefaultUnitKey {
			unit := units[i]
			return &unit, nil
		}
	}
	if len(units) > 0 || envCopy.Targeting.DefaultUnitKey != domain.DefaultDeploymentUnitKey {
		return nil, repository.ErrConflict
	}
	return domain.NewImplicitDefaultDeploymentUnit(&envCopy)
}

type stubDeploymentRuntimeLifecycle struct {
	err   error
	errs  []error
	unit  *domain.DeploymentUnit
	calls int
	specs []*domain.DesiredServiceSpec
}

func (s *stubDeploymentRuntimeLifecycle) DeployDeploymentUnitWithStatus(
	_ context.Context,
	_, _ uuid.UUID,
	_ *uuid.UUID,
	unit *domain.DeploymentUnit,
	desiredState *domain.DesiredServiceSpec,
	statusFn service.DeployStatusCallback,
) (*domain.RuntimeObservation, error) {
	s.unit = unit
	s.calls++
	s.specs = append(s.specs, desiredState)
	if statusFn != nil {
		statusFn(context.Background(), service.DeployStep("applying"), "")
	}
	if s.calls <= len(s.errs) && s.errs[s.calls-1] != nil {
		return nil, s.errs[s.calls-1]
	}
	if s.err != nil {
		return nil, s.err
	}
	if statusFn != nil {
		statusFn(context.Background(), service.DeployStep("observing"), "")
	}
	return &domain.RuntimeObservation{HealthStatus: domain.HealthStatusHealthy}, nil
}

type stubPublicRouteLifecycle struct {
	err   error
	calls int
	plan  *domain.DesiredPublicRoutePlan
}

func (s *stubPublicRouteLifecycle) Apply(_ context.Context, plan *domain.DesiredPublicRoutePlan) error {
	s.calls++
	s.plan = plan
	return s.err
}

type workflowCompositePublic struct{ events *[]string }

func (s *workflowCompositePublic) Check(context.Context, *domain.DesiredPublicRoutePlan) error {
	return nil
}
func (s *workflowCompositePublic) Apply(context.Context, *domain.DesiredPublicRoutePlan) error {
	return nil
}
func (s *workflowCompositePublic) ApplyWithCompensation(context.Context, *domain.DesiredPublicRoutePlan) (routingadapter.Compensation, error) {
	*s.events = append(*s.events, "cloudflare")
	return func(context.Context) error {
		*s.events = append(*s.events, "cloudflare-rollback")
		return nil
	}, nil
}

type workflowCompositeInternal struct {
	events      *[]string
	sawInternal bool
}

func (s *workflowCompositeInternal) Check(context.Context, *domain.DesiredPublicRoutePlan) error {
	return nil
}
func (s *workflowCompositeInternal) Apply(_ context.Context, plan *domain.DesiredPublicRoutePlan) error {
	*s.events = append(*s.events, "nginx")
	s.sawInternal = plan != nil && plan.InternalHTTPS != nil
	return nil
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
	desiredHash := ""
	if r.applyRequest.TargetService != nil {
		desiredHash = r.applyRequest.TargetService.DesiredHash
	}
	return &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: "sha256:max",
		ObservedContainerID: serviceName + "-container",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "max-compose-fixture",
		NormalizedHash:      desiredHash,
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
	runRepo := newStubRunRepo()
	intentRepo := newStubIntentRepo(runRepo)
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

func TestRecoverNonTerminalRuns_ResumesDirectRuntimeAndPersistsPhases(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "arcana",
		RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "arcana-local", ComposeDir: "/srv/arcana",
		ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	desired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion,
		ServiceID:     svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
		DesiredHash: "sha256:reviewed",
	}
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
		DesiredState: desired, DesiredHash: desired.DesiredHash,
	}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	intentRepo.intents[intent.ID].Status = domain.IntentStatusApproved
	now := time.Now().UTC()
	run := &domain.DeploymentRun{
		DeploymentIntentID: intent.ID, DeploymentUnitID: &unit.ID,
		LoomJobID: "runtime:direct", Status: domain.RunStatusRunning, StartedAt: &now,
		ApplyMetadata: map[string]any{"phases": []map[string]any{}, "phase_sequence": 0},
	}
	if err := registry.CreateDeploymentRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	lifecycle := &stubDeploymentRuntimeLifecycle{}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithDeploymentUnitRouting(unitRepo, lifecycle))
	if err := coord.RecoverNonTerminalRuns(ctx); err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	coord.wg.Wait()
	updated, err := registry.GetDeploymentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RunStatusSucceeded || lifecycle.calls != 1 {
		t.Fatalf("direct recovery = status:%s calls:%d", updated.Status, lifecycle.calls)
	}
	if phases := metadataPhases(updated.ApplyMetadata["phases"]); len(phases) < 2 {
		t.Fatalf("recovered run phases were not persisted: %#v", updated.ApplyMetadata)
	}
}

func TestRecoverNonTerminalRuns_StartsApprovedIntentWithoutRun(t *testing.T) {
	for _, approvalStatus := range []domain.ApprovalStatus{domain.ApprovalStatusApproved, domain.ApprovalStatusNotRequired} {
		t.Run(string(approvalStatus), func(t *testing.T) {
			ctx := context.Background()
			svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
			registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
			svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
			unit := &domain.DeploymentUnit{
				ID: uuid.New(), EnvironmentID: env.ID, Key: "arcana",
				RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "arcana-local", ComposeDir: "/srv/arcana",
				ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
			}
			desired := &domain.DesiredServiceSpec{
				SchemaVersion: domain.DesiredStateSchemaVersion,
				ServiceID:     svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
				DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
				DesiredHash: "sha256:reviewed",
			}
			intent := &domain.DeploymentIntent{
				ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
				RequestedBy: "test", SourceKind: domain.SourceKindManual,
				ApprovalStatus: approvalStatus, Status: domain.IntentStatusApproved,
				DesiredState: desired, DesiredHash: desired.DesiredHash,
			}
			if err := intentRepo.Create(ctx, intent); err != nil {
				t.Fatal(err)
			}
			for _, excluded := range []domain.DeploymentIntent{
				{ApprovalStatus: domain.ApprovalStatusRejected, Status: domain.IntentStatusRejected},
				{ApprovalStatus: domain.ApprovalStatusPending, Status: domain.IntentStatusPending},
				{ApprovalStatus: domain.ApprovalStatusPending, Status: domain.IntentStatusApproved},
			} {
				excluded.ServiceID = svc.ID
				excluded.EnvironmentID = env.ID
				excluded.DeploymentUnitID = &unit.ID
				excluded.ArtifactID = art.ID
				excluded.RequestedBy = "test"
				excluded.SourceKind = domain.SourceKindManual
				excluded.DesiredState = desired
				excluded.DesiredHash = desired.DesiredHash
				if err := intentRepo.Create(ctx, &excluded); err != nil {
					t.Fatal(err)
				}
			}
			intentWithRun := &domain.DeploymentIntent{
				ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
				RequestedBy: "test", SourceKind: domain.SourceKindManual,
				ApprovalStatus: approvalStatus, Status: domain.IntentStatusApproved,
				DesiredState: desired, DesiredHash: desired.DesiredHash,
			}
			if err := intentRepo.Create(ctx, intentWithRun); err != nil {
				t.Fatal(err)
			}
			if err := runRepo.Create(ctx, &domain.DeploymentRun{
				DeploymentIntentID: intentWithRun.ID,
				Status:             domain.RunStatusSucceeded,
			}); err != nil {
				t.Fatal(err)
			}

			lifecycle := &stubDeploymentRuntimeLifecycle{}
			unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
			coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithDeploymentUnitRouting(unitRepo, lifecycle))
			if err := coord.RecoverNonTerminalRuns(ctx); err != nil {
				t.Fatalf("RecoverNonTerminalRuns: %v", err)
			}
			coord.wg.Wait()
			if lifecycle.calls != 1 {
				t.Fatalf("approved intent recovery runtime calls = %d, want 1", lifecycle.calls)
			}
			runs, err := registry.ListDeploymentRuns(ctx, intent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 1 || runs[0].Status != domain.RunStatusSucceeded {
				t.Fatalf("approved intent recovery runs = %#v", runs)
			}
			if err := coord.RecoverNonTerminalRuns(ctx); err != nil {
				t.Fatalf("second RecoverNonTerminalRuns: %v", err)
			}
			coord.wg.Wait()
			if lifecycle.calls != 1 {
				t.Fatalf("idempotent recovery runtime calls = %d, want 1", lifecycle.calls)
			}
			existingRuns, err := registry.ListDeploymentRuns(ctx, intentWithRun.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(existingRuns) != 1 {
				t.Fatalf("intent with existing run has %d runs, want 1", len(existingRuns))
			}
		})
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

func TestExecuteDeployment_AuthorizedReleasePromotionCreatesDigestOnlyCanary(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc := &domain.Service{Name: "release-api", ArtifactRepo: "harbor.example/team/release-api"}
	if err := svcRepo.Create(ctx, svc); err != nil {
		t.Fatal(err)
	}
	env := &domain.Environment{Name: "staging", DeployStrategy: domain.DeployStrategyCanary}
	if err := envRepo.Create(ctx, env); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	artifact := &domain.Artifact{
		BuildID: uuid.New(), ServiceID: svc.ID, ImageRepo: svc.ArtifactRepo,
		ImageDigest: digest, Metadata: map[string]any{"registration_mode": "hiveci_release_digest"},
	}
	if err := artRepo.Create(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		RequestedBy: "authorized-operator", SourceKind: domain.SourceKindEventTriggered,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
		Metadata: map[string]any{
			"release_promotion": true, "promotion_strategy": "canary",
			"artifact_digest": digest, "release_identity": domain.HiveCIReleaseIdentityPrefix + strings.Repeat("b", 64),
			"previous_artifact_digest": "sha256:" + strings.Repeat("c", 64),
			"health_contract":          map[string]any{"type": "http", "path": "/health", "timeout_seconds": 10},
			"readiness_contract":       map[string]any{"type": "http", "path": "/ready", "timeout_seconds": 15},
		},
	}
	if err := intentRepo.Create(ctx, intent); err != nil {
		t.Fatal(err)
	}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	loomClient := &stubLoomClient{status: &loom.JobStatus{Status: "succeeded"}}
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithDeploymentLoomClient(loomClient))
	defer coord.Shutdown(time.Second)
	if err := coord.ExecuteDeployment(ctx, intent.ID); err != nil {
		t.Fatal(err)
	}
	if loomClient.submitCalls != 1 {
		t.Fatalf("SubmitJob calls=%d", loomClient.submitCalls)
	}
	request := loomClient.lastJobReq
	if request.Image != artifact.ImageRepo+"@"+digest || request.Digest != digest ||
		request.Params["rollout_strategy"] != "canary" || request.Params["canary_weight"] != "10" {
		t.Fatalf("canary job did not preserve digest/strategy: %+v", request)
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
		Labels: map[string]string{
			"bahia.managed":             "true",
			"bahia.service_id":          svc.ID.String(),
			"bahia.environment_id":      env.ID.String(),
			"bahia.artifact_id":         art.ID.String(),
			"bahia.deployment_unit_key": unit.Key,
			"bahia.deployment_unit_id":  unit.ID.String(),
		},
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

func TestExecuteDeployment_DefaultChangeAfterSnapshotFailsBeforeRun(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	desired := &domain.DesiredServiceSpec{
		SchemaVersion:     domain.DesiredStateSchemaVersion,
		ServiceID:         svc.ID,
		EnvironmentID:     env.ID,
		ArtifactID:        art.ID,
		DeploymentUnitKey: domain.DefaultDeploymentUnitKey,
		UnitRuntimeType:   domain.RuntimeTypeDocker,
	}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
		DesiredState: desired,
	}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[intent.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	env.Targeting.DefaultUnitKey = "max"
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "max",
		RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "max-managed", ComposeDir: "/srv/bahia/gastown",
		ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	lifecycle := &stubDeploymentRuntimeLifecycle{}
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	coord := NewCoordinator(
		registry, nil, &events.NoopPublisher{}, zap.NewNop(),
		WithDeploymentUnitRouting(unitRepo, lifecycle),
	)
	coord.loom = stubLoom

	err := coord.ExecuteDeployment(ctx, intent.ID)
	if err == nil || !strings.Contains(err.Error(), "desired-state implicit deployment unit became explicit") {
		t.Fatalf("ExecuteDeployment() error = %v, want fail-closed unit identity mismatch", err)
	}
	if len(runRepo.runs) != 0 {
		t.Fatalf("unit identity mismatch must fail before run creation, got %d", len(runRepo.runs))
	}
	if stubLoom.submitCalls != 0 || lifecycle.unit != nil {
		t.Fatalf("unit identity mismatch executed runtime: loom=%d lifecycle=%#v", stubLoom.submitCalls, lifecycle.unit)
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

func TestExecuteDeployment_DockerUnitUsesManagedEndpointWithoutLoom(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "edge-docker",
		RuntimeType: domain.RuntimeTypeDocker, EndpointRef: "edge-01-docker",
		ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged,
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

	if err := coord.ExecuteDeployment(ctx, di.ID); err != nil {
		t.Fatalf("ExecuteDeployment() error = %v", err)
	}
	if stubLoom.submitCalls != 0 {
		t.Fatalf("Docker deployment unit must not dispatch to a compute worker, got %d Loom submissions", stubLoom.submitCalls)
	}
	if lifecycle.unit == nil || lifecycle.unit.ID != unit.ID {
		t.Fatalf("Docker deployment unit was not applied through its managed endpoint: %#v", lifecycle.unit)
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

	if err := coord.ExecuteDeployment(ctx, di.ID); err != nil {
		t.Fatalf("ExecuteDeployment() error = %v", err)
	}
	coord.Shutdown(time.Second)
	if lifecycle.unit != nil {
		t.Fatalf("Loom dispatch unit must not use direct runtime lifecycle: %#v", lifecycle.unit)
	}
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

func TestExecuteDeployment_RouteOnlySkipsArtifactConvergence(t *testing.T) {
	for _, runtimeType := range []domain.RuntimeType{domain.RuntimeTypeCompose, domain.RuntimeTypeDocker} {
		t.Run(string(runtimeType), func(t *testing.T) {
			ctx := context.Background()
			svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
			svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
			unit := &domain.DeploymentUnit{
				ID: uuid.New(), EnvironmentID: env.ID, Key: "max-" + string(runtimeType), RuntimeType: runtimeType,
				EndpointRef: "max-managed", ComposeDir: "/srv/bahia/gastown", ReconcileMode: domain.ReconcileModeAutoApply,
				OwnershipMode: domain.OwnershipModeBahiaManaged,
			}
			unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
			lifecycle := &stubDeploymentRuntimeLifecycle{}
			routes := &stubPublicRouteLifecycle{}
			registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
			plan := &domain.DesiredPublicRoutePlan{Hostname: "api.example.com"}
			desired := &domain.DesiredServiceSpec{
				SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID,
				DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
				ArtifactID: art.ID, StableServiceKey: "api", PublicRoute: plan,
			}
			desired.ComputeDesiredHash()
			intent := &domain.DeploymentIntent{
				ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
				RequestedBy: "test", SourceKind: domain.SourceKindEventTriggered, DesiredState: desired, DesiredHash: desired.DesiredHash,
				Metadata: map[string]any{"contextvm_method": "service/route-attach"},
			}
			if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
				t.Fatal(err)
			}
			if err := coordExecuteRouteOnly(registry, unitRepo, lifecycle, routes, intent.ID); err != nil {
				t.Fatalf("ExecuteDeployment() error = %v", err)
			}
			if lifecycle.calls != 0 {
				t.Fatalf("route-only execution converged the artifact %d times", lifecycle.calls)
			}
			if routes.calls != 1 || routes.plan != plan {
				t.Fatalf("route apply calls=%d plan=%#v", routes.calls, routes.plan)
			}
			for _, run := range runRepo.runs {
				if run.LoomJobID != "runtime:route-only" || run.Status != domain.RunStatusSucceeded {
					t.Fatalf("route-only run = %#v", run)
				}
				phases := metadataPhases(run.ApplyMetadata["phases"])
				if len(phases) != 1 || phases[0]["step"] != "routing" || phases[0]["status"] != "completed" {
					t.Fatalf("route-only phases = %#v", phases)
				}
			}
		})
	}
}

func TestExecuteDeploymentRouteOnlyRunsCompositeCloudflareThenInternal(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "edge-compose", RuntimeType: domain.RuntimeTypeCompose,
		EndpointRef: "edge-01", ComposeDir: "/srv/bahia/app", ReconcileMode: domain.ReconcileModeAutoApply,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	plan := &domain.DesiredPublicRoutePlan{
		Hostname: "api.example.com",
		InternalHTTPS: &domain.DesiredInternalHTTPSPlan{
			SchemaVersion: domain.InternalHTTPSPlanSchemaVersion,
			Hostname:      "api.example.com",
		},
	}
	desired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID,
		DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
		ArtifactID: art.ID, StableServiceKey: "api", PublicRoute: plan,
	}
	desired.ComputeDesiredHash()
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindEventTriggered, DesiredState: desired, DesiredHash: desired.DesiredHash,
		Metadata: map[string]any{"contextvm_method": "service/route-attach"},
	}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	var events []string
	internalBackend := &workflowCompositeInternal{events: &events}
	composite, err := routingadapter.NewCompositeBackend(&workflowCompositePublic{events: &events}, internalBackend)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &stubDeploymentRuntimeLifecycle{}
	if err := coordExecuteRouteOnly(registry, unitRepo, lifecycle, composite, intent.ID); err != nil {
		t.Fatalf("ExecuteDeployment: %v", err)
	}
	if strings.Join(events, ",") != "cloudflare,nginx" || !internalBackend.sawInternal || lifecycle.calls != 0 {
		t.Fatalf("composite events=%v saw internal=%v lifecycle calls=%d", events, internalBackend.sawInternal, lifecycle.calls)
	}
}

func TestExecuteDeployment_RouteOnlyFailurePreservesProviderCompensation(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	unit := &domain.DeploymentUnit{ID: uuid.New(), EnvironmentID: env.ID, Key: "max-compose", RuntimeType: domain.RuntimeTypeCompose, OwnershipMode: domain.OwnershipModeBahiaManaged}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	lifecycle := &stubDeploymentRuntimeLifecycle{}
	routes := &stubPublicRouteLifecycle{err: fmt.Errorf("TLS verification failed; previous public route restored")}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	previousDesired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID,
		DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
		ArtifactID: art.ID, StableServiceKey: "api",
	}
	previousDesired.ComputeDesiredHash()
	previousIntent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual, Status: domain.IntentStatusDeployed,
		DesiredState: previousDesired, DesiredHash: previousDesired.DesiredHash, CreatedAt: time.Now().Add(-time.Minute),
	}
	if err := registry.CreateDeploymentIntent(ctx, previousIntent); err != nil {
		t.Fatal(err)
	}
	desired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID,
		DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
		ArtifactID: art.ID, StableServiceKey: "api", PublicRoute: &domain.DesiredPublicRoutePlan{Hostname: "api.example.com"},
	}
	desired.ComputeDesiredHash()
	intent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindEventTriggered, DesiredState: desired, DesiredHash: desired.DesiredHash,
		Metadata: map[string]any{"contextvm_method": "service/route-attach"},
	}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	// Seed observation/health linkage recorded while the service was healthy;
	// a failed route attach must not erase it during state restoration.
	observationID := uuid.New()
	lastRunID := uuid.New()
	reconciledAt := time.Now().Add(-30 * time.Second).UTC()
	seeded, err := registry.GetEnvironmentServiceState(ctx, svc.ID, env.ID)
	if err != nil || seeded == nil {
		t.Fatalf("load seeded state: %#v err=%v", seeded, err)
	}
	seeded.CurrentObservationID = &observationID
	seeded.LastSuccessfulRunID = &lastRunID
	seeded.LastReconciledAt = &reconciledAt
	if err := stateRepo.Upsert(ctx, seeded); err != nil {
		t.Fatal(err)
	}
	err = coordExecuteRouteOnly(registry, unitRepo, lifecycle, routes, intent.ID)
	if err == nil || !strings.Contains(err.Error(), "previous public route restored") {
		t.Fatalf("compensated route error = %v", err)
	}
	if lifecycle.calls != 0 {
		t.Fatalf("failed route-only execution converged/restored the artifact %d times", lifecycle.calls)
	}
	for _, run := range runRepo.runs {
		if run.Status != domain.RunStatusFailed {
			t.Fatalf("route-only failure run = %#v", run)
		}
		phases := metadataPhases(run.ApplyMetadata["phases"])
		if len(phases) != 1 || phases[0]["step"] != "routing" || phases[0]["status"] != "failed" {
			t.Fatalf("route-only failure phases = %#v", phases)
		}
	}
	state, err := registry.GetEnvironmentServiceState(ctx, svc.ID, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != previousIntent.ID ||
		state.DesiredArtifactID == nil || *state.DesiredArtifactID != previousIntent.ArtifactID ||
		state.DesiredRuntimeState != previousDesired || state.DesiredHash != previousDesired.DesiredHash ||
		state.DriftStatus != domain.DriftStatusInSync {
		t.Fatalf("route-only failure did not restore prior in-sync state: %#v", state)
	}
	if state.CurrentObservationID == nil || *state.CurrentObservationID != observationID ||
		state.LastSuccessfulRunID == nil || *state.LastSuccessfulRunID != lastRunID ||
		state.LastReconciledAt == nil || !state.LastReconciledAt.Equal(reconciledAt) {
		t.Fatalf("route-only failure restoration erased observation/health linkage: %#v", state)
	}
}

func coordExecuteRouteOnly(registry *service.RegistryService, units repository.DeploymentUnitRepository, lifecycle deploymentRuntimeLifecycle, routes publicRouteLifecycle, intentID uuid.UUID) error {
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithDeploymentUnitRouting(units, lifecycle), WithPublicRoutes(routes))
	return coord.ExecuteDeployment(context.Background(), intentID)
}

func TestExecuteDeployment_PublicRouteFailureRestoresPreviousApplication(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "max-compose",
		RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "max-managed", ComposeDir: "/srv/bahia/gastown",
		ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	lifecycle := &stubDeploymentRuntimeLifecycle{}
	routes := &stubPublicRouteLifecycle{err: fmt.Errorf("TLS verification failed; previous public route restored")}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)

	previousDesired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID,
		DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
		ArtifactID: art.ID, StableServiceKey: "arcana-previous",
	}
	previous := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual, Status: domain.IntentStatusDeployed,
		DesiredState: previousDesired,
	}
	if err := registry.CreateDeploymentIntent(ctx, previous); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[previous.ID].Status = domain.IntentStatusDeployed
	intentRepo.mu.Unlock()

	plan := &domain.DesiredPublicRoutePlan{Hostname: "arcana.example.com"}
	currentDesired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID,
		DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
		ArtifactID: art.ID, StableServiceKey: "arcana-current", PublicRoute: plan,
	}
	current := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual, Status: domain.IntentStatusApproved,
		DesiredState: currentDesired,
	}
	if err := registry.CreateDeploymentIntent(ctx, current); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[current.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(),
		WithDeploymentUnitRouting(unitRepo, lifecycle),
		WithPublicRoutes(routes),
	)
	err := coord.ExecuteDeployment(ctx, current.ID)
	if err == nil || !strings.Contains(err.Error(), "public route apply failed") {
		t.Fatalf("expected compensated public route failure, got %v", err)
	}
	if routes.calls != 1 || routes.plan != plan {
		t.Fatalf("signed route plan was not applied exactly once: calls=%d plan=%#v", routes.calls, routes.plan)
	}
	if lifecycle.calls != 2 || lifecycle.specs[0] != currentDesired || lifecycle.specs[1] != previousDesired {
		t.Fatalf("application apply/rollback sequence = calls:%d specs:%#v", lifecycle.calls, lifecycle.specs)
	}
	for _, run := range runRepo.runs {
		if run.Status != domain.RunStatusFailed {
			t.Fatalf("route verification failure must fail the run, got %s", run.Status)
		}
	}
}

func TestExecuteDeployment_PublicRouteAppFailureRestoresPreviousBeforeEdgeMutation(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "max-compose",
		RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "max-managed", ComposeDir: "/srv/bahia/gastown",
		ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	lifecycle := &stubDeploymentRuntimeLifecycle{errs: []error{fmt.Errorf("partial application failure"), nil}}
	routes := &stubPublicRouteLifecycle{}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)

	previousDesired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID,
		DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
		ArtifactID: art.ID, StableServiceKey: "arcana-previous",
	}
	previous := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual, DesiredState: previousDesired,
	}
	if err := registry.CreateDeploymentIntent(ctx, previous); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[previous.ID].Status = domain.IntentStatusDeployed
	intentRepo.mu.Unlock()

	currentDesired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID,
		DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
		ArtifactID: art.ID, StableServiceKey: "arcana-current",
		PublicRoute: &domain.DesiredPublicRoutePlan{Hostname: "arcana.example.com"},
	}
	current := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "test", SourceKind: domain.SourceKindManual, DesiredState: currentDesired,
	}
	if err := registry.CreateDeploymentIntent(ctx, current); err != nil {
		t.Fatal(err)
	}
	intentRepo.mu.Lock()
	intentRepo.intents[current.ID].Status = domain.IntentStatusApproved
	intentRepo.mu.Unlock()

	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(),
		WithDeploymentUnitRouting(unitRepo, lifecycle), WithPublicRoutes(routes))
	err := coord.ExecuteDeployment(ctx, current.ID)
	if err == nil || !strings.Contains(err.Error(), "partial application failure") || !strings.Contains(err.Error(), "previous application desired state restored") {
		t.Fatalf("expected compensated application failure, got %v", err)
	}
	if routes.calls != 0 {
		t.Fatalf("edge route mutated after failed application apply: %d calls", routes.calls)
	}
	if lifecycle.calls != 2 || lifecycle.specs[0] != currentDesired || lifecycle.specs[1] != previousDesired {
		t.Fatalf("application apply/rollback sequence = calls:%d specs:%#v", lifecycle.calls, lifecycle.specs)
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

func TestExecuteDeployment_LoomDispatchUnitUsesRuntimeCommandConfig(t *testing.T) {
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
		RuntimeConfig: map[string]any{
			"dispatch_mode":         "loom",
			"command":               []any{"/bin/sh", "-c", "echo $BAHIA_DEPLOY_SERVICE"},
			"env":                   map[string]any{"CANARY": "true"},
			"required_software":     []any{"bash"},
			"required_architecture": "linux/amd64",
			"timeout":               "45s",
		},
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithDeploymentUnitRouting(unitRepo, nil))
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
	if stubLoom.lastJobReq.Cmd != "/bin/sh" {
		t.Fatalf("cmd = %q, want /bin/sh", stubLoom.lastJobReq.Cmd)
	}
	if got := fmt.Sprint(stubLoom.lastJobReq.Args); got != "[-c echo $BAHIA_DEPLOY_SERVICE]" {
		t.Fatalf("args = %s", got)
	}
	if stubLoom.lastJobReq.Env["CANARY"] != "true" {
		t.Fatalf("env = %#v", stubLoom.lastJobReq.Env)
	}
	if got := fmt.Sprint(stubLoom.lastJobReq.RequiredSoftware); got != "[bash]" {
		t.Fatalf("required software = %s", got)
	}
	if stubLoom.lastJobReq.RequiredArchitecture != "linux/amd64" {
		t.Fatalf("required architecture = %q", stubLoom.lastJobReq.RequiredArchitecture)
	}
	if stubLoom.lastJobReq.Timeout != 45*time.Second {
		t.Fatalf("timeout = %s", stubLoom.lastJobReq.Timeout)
	}
}

func TestExecuteDeployment_ImplicitDefaultLoomUnitUsesEnvironmentRuntimeConfig(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	env.RuntimeConfig = map[string]any{
		"type":          string(domain.RuntimeTypeDocker),
		"dispatch_mode": "loom",
		"command":       []any{"/bin/sh", "-c", "echo implicit-default"},
	}
	if err := envRepo.Update(ctx, env); err != nil {
		t.Fatal(err)
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}}
	stubLoom := &stubLoomClient{status: &loom.JobStatus{Status: "completed"}}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	coord := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithDeploymentUnitRouting(unitRepo, nil))
	coord.loom = stubLoom

	di := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: art.ID,
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
		t.Fatalf("expected implicit Loom unit to submit one job, got %d", stubLoom.submitCalls)
	}
	if stubLoom.lastJobReq.Cmd != "/bin/sh" {
		t.Fatalf("cmd = %q, want /bin/sh", stubLoom.lastJobReq.Cmd)
	}
	if got := fmt.Sprint(stubLoom.lastJobReq.Args); got != "[-c echo implicit-default]" {
		t.Fatalf("args = %s", got)
	}
	if len(runRepo.runs) != 1 {
		t.Fatalf("expected 1 deployment run, got %d", len(runRepo.runs))
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
