package nostr

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	projectioncache "github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestRepublishSnapshotUsesDBProjectorSource(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()

	repos := newProjectorSourceTestRepos()
	repos.services.items[serviceID] = domain.Service{ID: serviceID, Name: "db-api", RuntimeType: domain.RuntimeTypeDocker, CreatedAt: now, UpdatedAt: now}
	repos.environments.items[envID] = domain.Environment{ID: envID, Name: "prod", DeployStrategy: domain.DeployStrategyReplace, CreatedAt: now, UpdatedAt: now}
	repos.states.items[stateKeyForTest(serviceID, envID)] = domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync, UpdatedAt: now}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), nil, sink, nil, zap.NewNop(), WithProjectorSource(NewDBProjectorSource(repos.asSourceRepositories())))
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	assertProjectedName(t, sink, KindServiceRegistry, "db-api")
	assertProjectedName(t, sink, KindEnvironmentRegistry, "prod")
	if got := len(sink.byKind(KindServiceState)); got != 1 {
		t.Fatalf("expected one service state projection, got %d", got)
	}
}

func TestRepublishSnapshotUsesCacheProjectorSource(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()

	repos := newProjectorSourceTestRepos()
	repos.services.items[serviceID] = domain.Service{ID: serviceID, Name: "cache-api", RuntimeType: domain.RuntimeTypeDocker, CreatedAt: now, UpdatedAt: now}
	repos.environments.items[envID] = domain.Environment{ID: envID, Name: "stage", DeployStrategy: domain.DeployStrategyReplace, CreatedAt: now, UpdatedAt: now}

	cache := projectioncache.NewRelayProjectionCache(nil, zap.NewNop())
	source := NewCacheProjectorSource(cache, repos.asSourceRepositories())
	if source.RelayProjectionCache() != cache {
		t.Fatal("cache source did not retain relay projection cache")
	}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), nil, sink, nil, zap.NewNop(), WithProjectorSource(source))
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	assertProjectedName(t, sink, KindServiceRegistry, "cache-api")
	assertProjectedName(t, sink, KindEnvironmentRegistry, "stage")
}

func TestRepublishSnapshotFallsBackWhenProjectorSourceNil(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()

	legacy := newFakeProjectionSource()
	legacy.services[serviceID] = domain.Service{ID: serviceID, Name: "legacy-api", RuntimeType: domain.RuntimeTypeDocker, CreatedAt: now, UpdatedAt: now}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), legacy, sink, nil, zap.NewNop())
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	assertProjectedName(t, sink, KindServiceRegistry, "legacy-api")
}

func assertProjectedName(t *testing.T, sink *captureProjectionPublisher, kind int, want string) {
	t.Helper()
	events := sink.byKind(kind)
	if len(events) != 1 {
		t.Fatalf("expected one event for kind %d, got %d", kind, len(events))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
		t.Fatalf("decode projected content: %v", err)
	}
	if got := payload["name"]; got != want {
		t.Fatalf("projected name = %v, want %q", got, want)
	}
}

type projectorSourceTestRepos struct {
	services     *projectorSourceServiceRepo
	environments *projectorSourceEnvironmentRepo
	states       *projectorSourceStateRepo
	builds       *projectorSourceBuildRepo
	artifacts    *projectorSourceArtifactRepo
	intents      *projectorSourceIntentRepo
	runs         *projectorSourceRunRepo
}

func newProjectorSourceTestRepos() *projectorSourceTestRepos {
	return &projectorSourceTestRepos{
		services:     &projectorSourceServiceRepo{items: map[uuid.UUID]domain.Service{}},
		environments: &projectorSourceEnvironmentRepo{items: map[uuid.UUID]domain.Environment{}},
		states:       &projectorSourceStateRepo{items: map[string]domain.EnvironmentServiceState{}},
		builds:       &projectorSourceBuildRepo{items: map[uuid.UUID]domain.Build{}},
		artifacts:    &projectorSourceArtifactRepo{items: map[uuid.UUID]domain.Artifact{}},
		intents:      &projectorSourceIntentRepo{items: map[uuid.UUID]domain.DeploymentIntent{}},
		runs:         &projectorSourceRunRepo{items: map[uuid.UUID]domain.DeploymentRun{}},
	}
}

func (r *projectorSourceTestRepos) asSourceRepositories() ProjectorSourceRepositories {
	return ProjectorSourceRepositories{Services: r.services, Environments: r.environments, States: r.states, Builds: r.builds, Artifacts: r.artifacts, Intents: r.intents, Runs: r.runs}
}

type projectorSourceServiceRepo struct{ items map[uuid.UUID]domain.Service }

func (r *projectorSourceServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	r.items[svc.ID] = *svc
	return nil
}
func (r *projectorSourceServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	v, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (r *projectorSourceServiceRepo) GetByName(_ context.Context, name string) (*domain.Service, error) {
	for _, v := range r.items {
		if v.Name == name {
			return &v, nil
		}
	}
	return nil, nil
}
func (r *projectorSourceServiceRepo) List(context.Context) ([]domain.Service, error) {
	out := make([]domain.Service, 0, len(r.items))
	for _, v := range r.items {
		out = append(out, v)
	}
	return out, nil
}
func (r *projectorSourceServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return r.List(context.Background())
}
func (r *projectorSourceServiceRepo) Update(_ context.Context, svc *domain.Service) error {
	r.items[svc.ID] = *svc
	return nil
}
func (r *projectorSourceServiceRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.items, id)
	return nil
}

type projectorSourceEnvironmentRepo struct {
	items map[uuid.UUID]domain.Environment
}

func (r *projectorSourceEnvironmentRepo) Create(_ context.Context, env *domain.Environment) error {
	r.items[env.ID] = *env
	return nil
}
func (r *projectorSourceEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	v, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (r *projectorSourceEnvironmentRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	for _, v := range r.items {
		if v.Name == name {
			return &v, nil
		}
	}
	return nil, nil
}
func (r *projectorSourceEnvironmentRepo) List(context.Context) ([]domain.Environment, error) {
	out := make([]domain.Environment, 0, len(r.items))
	for _, v := range r.items {
		out = append(out, v)
	}
	return out, nil
}
func (r *projectorSourceEnvironmentRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return r.List(context.Background())
}
func (r *projectorSourceEnvironmentRepo) Update(_ context.Context, env *domain.Environment) error {
	r.items[env.ID] = *env
	return nil
}
func (r *projectorSourceEnvironmentRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.items, id)
	return nil
}

type projectorSourceStateRepo struct {
	items map[string]domain.EnvironmentServiceState
}

func (r *projectorSourceStateRepo) Upsert(_ context.Context, state *domain.EnvironmentServiceState) error {
	r.items[stateKeyForTest(state.ServiceID, state.EnvironmentID)] = *state
	return nil
}
func (r *projectorSourceStateRepo) Get(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	v, ok := r.items[stateKeyForTest(serviceID, envID)]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (r *projectorSourceStateRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return r.filtered(func(v domain.EnvironmentServiceState) bool { return v.EnvironmentID == envID }), nil
}
func (r *projectorSourceStateRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return r.filtered(func(v domain.EnvironmentServiceState) bool { return v.ServiceID == serviceID }), nil
}
func (r *projectorSourceStateRepo) ListDrifted(context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.filtered(func(v domain.EnvironmentServiceState) bool { return v.DriftStatus != domain.DriftStatusInSync }), nil
}
func (r *projectorSourceStateRepo) ListAll(context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.filtered(func(domain.EnvironmentServiceState) bool { return true }), nil
}
func (r *projectorSourceStateRepo) filtered(keep func(domain.EnvironmentServiceState) bool) []domain.EnvironmentServiceState {
	out := []domain.EnvironmentServiceState{}
	for _, v := range r.items {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

type projectorSourceBuildRepo struct{ items map[uuid.UUID]domain.Build }

func (r *projectorSourceBuildRepo) Create(_ context.Context, b *domain.Build) error {
	r.items[b.ID] = *b
	return nil
}
func (r *projectorSourceBuildRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	v, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (r *projectorSourceBuildRepo) GetByCISystemRunID(context.Context, string, string) (*domain.Build, error) {
	return nil, nil
}
func (r *projectorSourceBuildRepo) ListByService(_ context.Context, serviceID uuid.UUID, _, _ int) ([]domain.Build, error) {
	out := []domain.Build{}
	for _, v := range r.items {
		if v.ServiceID == serviceID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *projectorSourceBuildRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.BuildStatus) error {
	v := r.items[id]
	v.Status = status
	r.items[id] = v
	return nil
}

type projectorSourceArtifactRepo struct{ items map[uuid.UUID]domain.Artifact }

func (r *projectorSourceArtifactRepo) Create(_ context.Context, a *domain.Artifact) error {
	r.items[a.ID] = *a
	return nil
}
func (r *projectorSourceArtifactRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	v, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (r *projectorSourceArtifactRepo) GetByDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *projectorSourceArtifactRepo) GetByImageRepoDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *projectorSourceArtifactRepo) ListByService(_ context.Context, serviceID uuid.UUID, _, _ int) ([]domain.Artifact, error) {
	out := []domain.Artifact{}
	for _, v := range r.items {
		if v.ServiceID == serviceID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *projectorSourceArtifactRepo) ListByBuild(_ context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	out := []domain.Artifact{}
	for _, v := range r.items {
		if v.BuildID == buildID {
			out = append(out, v)
		}
	}
	return out, nil
}

type projectorSourceIntentRepo struct {
	items map[uuid.UUID]domain.DeploymentIntent
}

func (r *projectorSourceIntentRepo) Create(_ context.Context, i *domain.DeploymentIntent) error {
	r.items[i.ID] = *i
	return nil
}
func (r *projectorSourceIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	v, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (r *projectorSourceIntentRepo) GetByHiveResultEventID(context.Context, string) (*domain.DeploymentIntent, error) {
	return nil, nil
}
func (r *projectorSourceIntentRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, _, _ int) ([]domain.DeploymentIntent, error) {
	out := []domain.DeploymentIntent{}
	for _, v := range r.items {
		if v.ServiceID == serviceID && v.EnvironmentID == envID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *projectorSourceIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	v := r.items[id]
	v.Status = status
	r.items[id] = v
	return nil
}
func (r *projectorSourceIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	v := r.items[id]
	v.ApprovalStatus = status
	r.items[id] = v
	return nil
}

type projectorSourceRunRepo struct {
	items map[uuid.UUID]domain.DeploymentRun
}

func (r *projectorSourceRunRepo) Create(_ context.Context, run *domain.DeploymentRun) error {
	r.items[run.ID] = *run
	return nil
}
func (r *projectorSourceRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	v, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (r *projectorSourceRunRepo) ListByIntent(_ context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	out := []domain.DeploymentRun{}
	for _, v := range r.items {
		if v.DeploymentIntentID == intentID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *projectorSourceRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	v := r.items[id]
	v.Status = status
	v.ExitCode = exitCode
	r.items[id] = v
	return nil
}
