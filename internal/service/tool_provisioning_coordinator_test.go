package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type coordinatorToolRepoFake struct {
	mu            sync.Mutex
	intents       map[uuid.UUID]*domain.ToolProvisionIntent
	states        map[string]*domain.ToolProfileState
	denylist      []domain.ToolDenylistEntry
	listStatuses  [][]domain.ToolProvisionStatus
	listOrder     []uuid.UUID
	completed     chan uuid.UUID
	listStarted   chan struct{}
	listStartedMu sync.Once
}

func newCoordinatorToolRepoFake() *coordinatorToolRepoFake {
	return &coordinatorToolRepoFake{
		intents:     make(map[uuid.UUID]*domain.ToolProvisionIntent),
		states:      make(map[string]*domain.ToolProfileState),
		completed:   make(chan uuid.UUID, 8),
		listStarted: make(chan struct{}),
	}
}

func (r *coordinatorToolRepoFake) CreateIntent(_ context.Context, intent *domain.ToolProvisionIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.intents[intent.ID] = cloneToolIntent(intent)
	return nil
}

func (r *coordinatorToolRepoFake) GetIntent(_ context.Context, id uuid.UUID) (*domain.ToolProvisionIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	intent := r.intents[id]
	if intent == nil {
		return nil, nil
	}
	return cloneToolIntent(intent), nil
}

func (r *coordinatorToolRepoFake) UpdateIntent(_ context.Context, intent *domain.ToolProvisionIntent) error {
	r.mu.Lock()
	r.intents[intent.ID] = cloneToolIntent(intent)
	completed := intent.Status == domain.ToolProvisionStatusCompleted
	r.mu.Unlock()
	if completed {
		select {
		case r.completed <- intent.ID:
		default:
		}
	}
	return nil
}

func (r *coordinatorToolRepoFake) ListPendingApprovalIntents(context.Context) ([]domain.ToolProvisionIntent, error) {
	return nil, nil
}

func (r *coordinatorToolRepoFake) ListIntentsByStatus(_ context.Context, statuses ...domain.ToolProvisionStatus) ([]domain.ToolProvisionIntent, error) {
	r.listStartedMu.Do(func() { close(r.listStarted) })
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listStatuses = append(r.listStatuses, append([]domain.ToolProvisionStatus(nil), statuses...))
	wanted := make(map[domain.ToolProvisionStatus]struct{}, len(statuses))
	for _, status := range statuses {
		wanted[status] = struct{}{}
	}
	out := make([]domain.ToolProvisionIntent, 0)
	appendIfWanted := func(intent *domain.ToolProvisionIntent) {
		if intent == nil {
			return
		}
		if _, ok := wanted[intent.Status]; ok {
			out = append(out, *cloneToolIntent(intent))
		}
	}
	if len(r.listOrder) > 0 {
		seen := make(map[uuid.UUID]struct{}, len(r.listOrder))
		for _, id := range r.listOrder {
			seen[id] = struct{}{}
			appendIfWanted(r.intents[id])
		}
		for id, intent := range r.intents {
			if _, ok := seen[id]; !ok {
				appendIfWanted(intent)
			}
		}
		return out, nil
	}
	for _, intent := range r.intents {
		appendIfWanted(intent)
	}
	return out, nil
}

func (r *coordinatorToolRepoFake) CreateRun(context.Context, *domain.ToolProvisionRun) error {
	return nil
}
func (r *coordinatorToolRepoFake) GetRun(context.Context, uuid.UUID) (*domain.ToolProvisionRun, error) {
	return nil, nil
}
func (r *coordinatorToolRepoFake) UpdateRun(context.Context, *domain.ToolProvisionRun) error {
	return nil
}
func (r *coordinatorToolRepoFake) GetProfileState(_ context.Context, serviceID, envID uuid.UUID) (*domain.ToolProfileState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.states[toolProvisionTargetKey(serviceID, envID)]
	if state == nil {
		return nil, nil
	}
	copy := *state
	copy.InstalledTools = append([]domain.ResolvedTool(nil), state.InstalledTools...)
	return &copy, nil
}
func (r *coordinatorToolRepoFake) UpsertProfileState(_ context.Context, state *domain.ToolProfileState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *state
	copy.InstalledTools = append([]domain.ResolvedTool(nil), state.InstalledTools...)
	r.states[toolProvisionTargetKey(state.ServiceID, state.EnvironmentID)] = &copy
	return nil
}
func (r *coordinatorToolRepoFake) AddToDenylist(context.Context, *domain.ToolDenylistEntry) error {
	return nil
}
func (r *coordinatorToolRepoFake) RemoveFromDenylist(context.Context, string, string) error {
	return nil
}
func (r *coordinatorToolRepoFake) IsDenylisted(_ context.Context, packageName, manager string) (bool, error) {
	for _, entry := range r.denylist {
		if entry.PackageName == packageName && entry.Manager == manager {
			return true, nil
		}
	}
	return false, nil
}
func (r *coordinatorToolRepoFake) ListDenylist(context.Context) ([]domain.ToolDenylistEntry, error) {
	return append([]domain.ToolDenylistEntry(nil), r.denylist...), nil
}
func (r *coordinatorToolRepoFake) LogApproval(context.Context, uuid.UUID, string, string, string) error {
	return nil
}

func (r *coordinatorToolRepoFake) intentStatus(id uuid.UUID) domain.ToolProvisionStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.intents[id].Status
}

func (r *coordinatorToolRepoFake) profileState(serviceID, envID uuid.UUID) *domain.ToolProfileState {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.states[toolProvisionTargetKey(serviceID, envID)]
	if state == nil {
		return nil
	}
	copy := *state
	return &copy
}

type coordinatorServiceRepoFake struct{ services map[uuid.UUID]*domain.Service }

func (r coordinatorServiceRepoFake) Create(context.Context, *domain.Service) error { return nil }
func (r coordinatorServiceRepoFake) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if svc := r.services[id]; svc != nil {
		copy := *svc
		return &copy, nil
	}
	return nil, nil
}
func (r coordinatorServiceRepoFake) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (r coordinatorServiceRepoFake) List(context.Context) ([]domain.Service, error) { return nil, nil }
func (r coordinatorServiceRepoFake) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (r coordinatorServiceRepoFake) Update(context.Context, *domain.Service) error { return nil }
func (r coordinatorServiceRepoFake) Delete(context.Context, uuid.UUID) error       { return nil }

type coordinatorEnvRepoFake struct {
	envs map[uuid.UUID]*domain.Environment
}

func (r coordinatorEnvRepoFake) Create(context.Context, *domain.Environment) error { return nil }
func (r coordinatorEnvRepoFake) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	if env := r.envs[id]; env != nil {
		copy := *env
		return &copy, nil
	}
	return nil, nil
}
func (r coordinatorEnvRepoFake) GetByName(context.Context, string) (*domain.Environment, error) {
	return nil, nil
}
func (r coordinatorEnvRepoFake) List(context.Context) ([]domain.Environment, error) { return nil, nil }
func (r coordinatorEnvRepoFake) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (r coordinatorEnvRepoFake) Update(context.Context, *domain.Environment) error { return nil }
func (r coordinatorEnvRepoFake) Delete(context.Context, uuid.UUID) error           { return nil }

type coordinatorRuntimeFake struct {
	mu          sync.Mutex
	entered     chan coordinatorDeployCall
	releases    []chan struct{}
	calls       []coordinatorDeployCall
	active      map[string]int
	maxByTarget map[string]int
	maxTotal    int
}

type coordinatorDeployCall struct {
	Index       int
	ServiceName string
	Image       string
}

func newCoordinatorRuntimeFake() *coordinatorRuntimeFake {
	return &coordinatorRuntimeFake{
		entered:     make(chan coordinatorDeployCall, 8),
		active:      make(map[string]int),
		maxByTarget: make(map[string]int),
	}
}

func (r *coordinatorRuntimeFake) Type() domain.RuntimeType { return domain.RuntimeTypeDocker }
func (r *coordinatorRuntimeFake) Deploy(_ context.Context, serviceName, image string, _ runtime.DeployOptions) error {
	release := make(chan struct{})
	r.mu.Lock()
	idx := len(r.calls)
	call := coordinatorDeployCall{Index: idx, ServiceName: serviceName, Image: image}
	r.calls = append(r.calls, call)
	r.releases = append(r.releases, release)
	r.active[serviceName]++
	if r.active[serviceName] > r.maxByTarget[serviceName] {
		r.maxByTarget[serviceName] = r.active[serviceName]
	}
	total := 0
	for _, active := range r.active {
		total += active
	}
	if total > r.maxTotal {
		r.maxTotal = total
	}
	r.mu.Unlock()

	r.entered <- call
	<-release

	r.mu.Lock()
	r.active[serviceName]--
	r.mu.Unlock()
	return nil
}
func (r *coordinatorRuntimeFake) Observe(context.Context, uuid.UUID, uuid.UUID, string) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{HealthStatus: domain.HealthStatusHealthy}, nil
}
func (r *coordinatorRuntimeFake) Undeploy(context.Context, string) error { return nil }
func (r *coordinatorRuntimeFake) StreamLogs(context.Context, string, runtime.LogOptions) (<-chan runtime.LogEntry, error) {
	ch := make(chan runtime.LogEntry)
	close(ch)
	return ch, nil
}
func (r *coordinatorRuntimeFake) release(index int) {
	r.mu.Lock()
	release := r.releases[index]
	r.mu.Unlock()
	close(release)
}
func (r *coordinatorRuntimeFake) call(index int) coordinatorDeployCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[index]
}
func (r *coordinatorRuntimeFake) maxActiveFor(serviceName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxByTarget[serviceName]
}
func (r *coordinatorRuntimeFake) maxActiveTotal() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxTotal
}

func TestToolProvisioningRunRecoversApprovedIntentImmediately(t *testing.T) {
	t.Parallel()
	repo, serviceRepo, envRepo, rt, svc, env := coordinatorRecoveryFixture(t)
	intent := approvedToolIntent(svc.ID, env.ID, "curl")
	repo.intents[intent.ID] = intent
	coordinator := newTestToolCoordinator(repo, serviceRepo, envRepo, rt, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()

	<-repo.listStarted
	call := waitForDeploy(t, rt)
	rt.release(call.Index)
	waitForCompletedIntent(t, repo, intent.ID)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got := repo.intentStatus(intent.ID); got != domain.ToolProvisionStatusCompleted {
		t.Fatalf("expected approved intent to recover to completed, got %q", got)
	}
}

func TestToolProvisioningRecoveryRetriesPendingIntent(t *testing.T) {
	t.Parallel()
	repo, serviceRepo, envRepo, rt, svc, env := coordinatorRecoveryFixture(t)
	repo.denylist = []domain.ToolDenylistEntry{{PackageName: "curl", Manager: "apt", Reason: "blocked by test"}}
	intent := pendingToolIntent(svc.ID, env.ID, "curl")
	repo.intents[intent.ID] = intent
	security := NewToolSecurityService(repo, nil, zap.NewNop(), ToolSecurityConfig{})
	coordinator := newTestToolCoordinator(repo, serviceRepo, envRepo, rt, security)

	if err := coordinator.ProcessPendingIntents(context.Background()); err != nil {
		t.Fatalf("recover pending intents: %v", err)
	}
	if got := repo.intentStatus(intent.ID); got != domain.ToolProvisionStatusFailed {
		t.Fatalf("expected denied pending intent to be retried and failed, got %q", got)
	}
	if len(repo.listStatuses) != 1 {
		t.Fatalf("expected one recovery scan, got %d", len(repo.listStatuses))
	}
	assertStatusesEqual(t, recoverableToolProvisionStatuses(), repo.listStatuses[0])
}

func TestToolProvisioningRecoveryOrdersSameTargetOldestFirst(t *testing.T) {
	t.Parallel()
	repo, serviceRepo, envRepo, _, svc, env := coordinatorRecoveryFixture(t)
	oldest := approvedToolIntent(svc.ID, env.ID, "curl")
	newest := approvedToolIntent(svc.ID, env.ID, "git")
	oldest.CreatedAt = time.Unix(100, 0).UTC()
	newest.CreatedAt = time.Unix(200, 0).UTC()
	repo.intents[oldest.ID] = oldest
	repo.intents[newest.ID] = newest
	repo.listOrder = []uuid.UUID{newest.ID, oldest.ID}
	coordinator := NewToolProvisioningCoordinator(repo, serviceRepo, envRepo, nil, nil, nil, nil, nil, zap.NewNop(), ToolProvisioningConfig{TargetRepo: "tools/test"})

	if err := coordinator.ProcessPendingIntents(context.Background()); err != nil {
		t.Fatalf("recover intents: %v", err)
	}
	state := repo.profileState(svc.ID, env.ID)
	if state == nil {
		t.Fatal("expected profile state")
	}
	if len(state.InstalledTools) != 1 || state.InstalledTools[0].Name != "git" {
		t.Fatalf("expected newest intent to be final state, got %#v", state.InstalledTools)
	}
	if state.PreviousImageDigest == "" {
		t.Fatal("expected oldest intent image to be retained as previous image")
	}
}

func TestToolProvisioningSerializesSameTargetAndMaintainsPreviousImage(t *testing.T) {
	t.Parallel()
	repo, serviceRepo, envRepo, rt, svc, env := coordinatorRecoveryFixture(t)
	first := approvedToolIntent(svc.ID, env.ID, "curl")
	second := approvedToolIntent(svc.ID, env.ID, "git")
	repo.intents[first.ID] = first
	repo.intents[second.ID] = second
	coordinator := newTestToolCoordinator(repo, serviceRepo, envRepo, rt, nil)

	firstDone := processApprovedAsync(coordinator, first.ID)
	firstCall := waitForDeploy(t, rt)
	if firstCall.ServiceName != svc.Name {
		t.Fatalf("expected first deploy target %q, got %q", svc.Name, firstCall.ServiceName)
	}

	secondDone := processApprovedAsync(coordinator, second.ID)
	assertNoDeployBeforeRelease(t, rt)
	rt.release(firstCall.Index)
	if err := <-firstDone; err != nil {
		t.Fatalf("first intent failed: %v", err)
	}

	secondCall := waitForDeploy(t, rt)
	rt.release(secondCall.Index)
	if err := <-secondDone; err != nil {
		t.Fatalf("second intent failed: %v", err)
	}

	if got := rt.maxActiveFor(svc.Name); got != 1 {
		t.Fatalf("expected same-target deploys to be serialized, max active=%d", got)
	}
	state := repo.profileState(svc.ID, env.ID)
	if state == nil {
		t.Fatal("expected profile state")
	}
	if state.PreviousImageDigest != firstCall.Image {
		t.Fatalf("expected previous image %q, got %q", firstCall.Image, state.PreviousImageDigest)
	}
	if state.CurrentImageDigest != secondCall.Image {
		t.Fatalf("expected current image %q, got %q", secondCall.Image, state.CurrentImageDigest)
	}
}

func TestToolProvisioningAllowsDifferentTargetsConcurrently(t *testing.T) {
	t.Parallel()
	repo := newCoordinatorToolRepoFake()
	envID := uuid.New()
	firstSvc := &domain.Service{ID: uuid.New(), Name: "svc-one"}
	secondSvc := &domain.Service{ID: uuid.New(), Name: "svc-two"}
	env := &domain.Environment{ID: envID, Name: "prod"}
	serviceRepo := coordinatorServiceRepoFake{services: map[uuid.UUID]*domain.Service{firstSvc.ID: firstSvc, secondSvc.ID: secondSvc}}
	envRepo := coordinatorEnvRepoFake{envs: map[uuid.UUID]*domain.Environment{env.ID: env}}
	rt := newCoordinatorRuntimeFake()
	first := approvedToolIntent(firstSvc.ID, env.ID, "curl")
	second := approvedToolIntent(secondSvc.ID, env.ID, "git")
	repo.intents[first.ID] = first
	repo.intents[second.ID] = second
	coordinator := newTestToolCoordinator(repo, serviceRepo, envRepo, rt, nil)

	firstDone := processApprovedAsync(coordinator, first.ID)
	secondDone := processApprovedAsync(coordinator, second.ID)
	firstCall := waitForDeploy(t, rt)
	secondCall := waitForDeploy(t, rt)
	if firstCall.ServiceName == secondCall.ServiceName {
		t.Fatalf("expected different deployment targets, got %q twice", firstCall.ServiceName)
	}
	if got := rt.maxActiveTotal(); got != 2 {
		t.Fatalf("expected different targets to deploy concurrently, max total active=%d", got)
	}
	rt.release(firstCall.Index)
	rt.release(secondCall.Index)
	if err := <-firstDone; err != nil {
		t.Fatalf("first intent failed: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second intent failed: %v", err)
	}
}

func coordinatorRecoveryFixture(t *testing.T) (*coordinatorToolRepoFake, coordinatorServiceRepoFake, coordinatorEnvRepoFake, *coordinatorRuntimeFake, *domain.Service, *domain.Environment) {
	t.Helper()
	repo := newCoordinatorToolRepoFake()
	svc := &domain.Service{ID: uuid.New(), Name: "svc"}
	env := &domain.Environment{ID: uuid.New(), Name: "prod"}
	serviceRepo := coordinatorServiceRepoFake{services: map[uuid.UUID]*domain.Service{svc.ID: svc}}
	envRepo := coordinatorEnvRepoFake{envs: map[uuid.UUID]*domain.Environment{env.ID: env}}
	return repo, serviceRepo, envRepo, newCoordinatorRuntimeFake(), svc, env
}

func newTestToolCoordinator(repo *coordinatorToolRepoFake, serviceRepo coordinatorServiceRepoFake, envRepo coordinatorEnvRepoFake, rt *coordinatorRuntimeFake, security *ToolSecurityService) *ToolProvisioningCoordinator {
	return NewToolProvisioningCoordinator(repo, serviceRepo, envRepo, security, nil, rt, nil, nil, zap.NewNop(), ToolProvisioningConfig{TargetRepo: "tools/test", RecoveryPollInterval: time.Hour})
}

func pendingToolIntent(serviceID, envID uuid.UUID, tool string) *domain.ToolProvisionIntent {
	return &domain.ToolProvisionIntent{
		ID:             uuid.New(),
		ServiceID:      serviceID,
		EnvironmentID:  envID,
		Status:         domain.ToolProvisionStatusPending,
		RequestedTools: []domain.ToolRequest{{Name: tool, Version: "latest", Manager: "apt"}},
		CreatedAt:      time.Now().UTC(),
	}
}

func approvedToolIntent(serviceID, envID uuid.UUID, tool string) *domain.ToolProvisionIntent {
	return &domain.ToolProvisionIntent{
		ID:            uuid.New(),
		ServiceID:     serviceID,
		EnvironmentID: envID,
		Status:        domain.ToolProvisionStatusApproved,
		ResolvedTools: []domain.ResolvedTool{{Name: tool, Version: "latest", Manager: "apt", Source: "debian"}},
		CreatedAt:     time.Now().UTC(),
	}
}

func cloneToolIntent(intent *domain.ToolProvisionIntent) *domain.ToolProvisionIntent {
	if intent == nil {
		return nil
	}
	copy := *intent
	copy.RequestedTools = append([]domain.ToolRequest(nil), intent.RequestedTools...)
	copy.ResolvedTools = append([]domain.ResolvedTool(nil), intent.ResolvedTools...)
	copy.ApprovalFlags = append([]string(nil), intent.ApprovalFlags...)
	return &copy
}

func processApprovedAsync(coordinator *ToolProvisioningCoordinator, id uuid.UUID) <-chan error {
	done := make(chan error, 1)
	go func() { done <- coordinator.ProcessApprovedIntent(context.Background(), id) }()
	return done
}

func waitForDeploy(t *testing.T, rt *coordinatorRuntimeFake) coordinatorDeployCall {
	t.Helper()
	select {
	case call := <-rt.entered:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for deployment")
		return coordinatorDeployCall{}
	}
}

func assertNoDeployBeforeRelease(t *testing.T, rt *coordinatorRuntimeFake) {
	t.Helper()
	select {
	case call := <-rt.entered:
		t.Fatalf("same-target intent reached deploy before prior deployment released: %#v", call)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitForCompletedIntent(t *testing.T, repo *coordinatorToolRepoFake, id uuid.UUID) {
	t.Helper()
	select {
	case got := <-repo.completed:
		if got != id {
			t.Fatalf("expected completed intent %s, got %s", id, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for completed intent")
	}
}

func assertStatusesEqual(t *testing.T, want, got []domain.ToolProvisionStatus) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("status count mismatch: want %v, got %v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("status[%d] mismatch: want %q, got %q (all: %v)", i, want[i], got[i], got)
		}
	}
}
