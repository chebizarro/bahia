package service

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type coordinatorMLRepoFake struct {
	mu           sync.Mutex
	models       map[uuid.UUID]*domain.MLModel
	modelBySlug  map[string]uuid.UUID
	versions     map[uuid.UUID]*domain.MLModelVersion
	artifacts    map[uuid.UUID]*domain.MLArtifactRef
	provenance   map[uuid.UUID]*domain.MLProvenanceEdge
	recipes      map[uuid.UUID]*domain.MLRecipe
	recipeRuns   map[uuid.UUID]*domain.MLRecipeRun
	endpoints    map[uuid.UUID]*domain.MLInferenceEndpoint
	intents      map[uuid.UUID]*domain.MLDeploymentIntent
	runs         map[uuid.UUID]*domain.MLDeploymentRun
	observations map[uuid.UUID]*domain.MLInferenceObservation
	states       map[string]*domain.MLInferenceState
	requeued     int
	completed    chan uuid.UUID
}

func newCoordinatorMLRepoFake() *coordinatorMLRepoFake {
	return &coordinatorMLRepoFake{
		models:       map[uuid.UUID]*domain.MLModel{},
		modelBySlug:  map[string]uuid.UUID{},
		versions:     map[uuid.UUID]*domain.MLModelVersion{},
		artifacts:    map[uuid.UUID]*domain.MLArtifactRef{},
		provenance:   map[uuid.UUID]*domain.MLProvenanceEdge{},
		recipes:      map[uuid.UUID]*domain.MLRecipe{},
		recipeRuns:   map[uuid.UUID]*domain.MLRecipeRun{},
		endpoints:    map[uuid.UUID]*domain.MLInferenceEndpoint{},
		intents:      map[uuid.UUID]*domain.MLDeploymentIntent{},
		runs:         map[uuid.UUID]*domain.MLDeploymentRun{},
		observations: map[uuid.UUID]*domain.MLInferenceObservation{},
		states:       map[string]*domain.MLInferenceState{},
		completed:    make(chan uuid.UUID, 8),
	}
}

func (r *coordinatorMLRepoFake) UpsertModel(_ context.Context, model *domain.MLModel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *model
	r.models[cp.ID] = &cp
	r.modelBySlug[cp.Slug] = cp.ID
	return nil
}
func (r *coordinatorMLRepoFake) GetModel(_ context.Context, id uuid.UUID) (*domain.MLModel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.models[id]; m != nil {
		cp := *m
		return &cp, nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) GetModelBySlug(_ context.Context, slug string) (*domain.MLModel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.modelBySlug[slug]; ok {
		cp := *r.models[id]
		return &cp, nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) ListModels(context.Context, domain.MLTaskKind, int, int) ([]domain.MLModel, error) {
	return nil, nil
}
func (r *coordinatorMLRepoFake) UpsertModelVersion(_ context.Context, version *domain.MLModelVersion) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *version
	r.versions[cp.ID] = &cp
	return nil
}
func (r *coordinatorMLRepoFake) GetModelVersion(_ context.Context, id uuid.UUID) (*domain.MLModelVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := r.versions[id]; v != nil {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) GetModelVersionByModelVersion(_ context.Context, modelID uuid.UUID, version string) (*domain.MLModelVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.versions {
		if v.ModelID == modelID && v.Version == version {
			cp := *v
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) ListModelVersions(context.Context, uuid.UUID, int, int) ([]domain.MLModelVersion, error) {
	return nil, nil
}
func (r *coordinatorMLRepoFake) UpsertArtifactRef(_ context.Context, artifact *domain.MLArtifactRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *artifact
	r.artifacts[cp.ID] = &cp
	return nil
}
func (r *coordinatorMLRepoFake) GetArtifactRef(_ context.Context, id uuid.UUID) (*domain.MLArtifactRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if artifact := r.artifacts[id]; artifact != nil {
		cp := *artifact
		return &cp, nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) ListArtifactRefsByModelVersion(_ context.Context, modelVersionID uuid.UUID) ([]domain.MLArtifactRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.MLArtifactRef{}
	for _, artifact := range r.artifacts {
		if artifact.ModelVersionID != nil && *artifact.ModelVersionID == modelVersionID {
			out = append(out, *artifact)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (r *coordinatorMLRepoFake) UpsertProvenanceEdge(_ context.Context, edge *domain.MLProvenanceEdge) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if edge.ID == uuid.Nil {
		edge.ID = uuid.New()
	}
	cp := *edge
	r.provenance[cp.ID] = &cp
	return nil
}
func (r *coordinatorMLRepoFake) ListProvenanceEdgesByArtifact(_ context.Context, artifactID uuid.UUID) ([]domain.MLProvenanceEdge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.MLProvenanceEdge{}
	for _, edge := range r.provenance {
		if (edge.FromArtifactID != nil && *edge.FromArtifactID == artifactID) || (edge.ToArtifactID != nil && *edge.ToArtifactID == artifactID) {
			out = append(out, *edge)
		}
	}
	return out, nil
}
func (r *coordinatorMLRepoFake) UpsertRecipe(_ context.Context, recipe *domain.MLRecipe) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *recipe
	r.recipes[cp.ID] = &cp
	return nil
}
func (r *coordinatorMLRepoFake) GetRecipe(_ context.Context, id uuid.UUID) (*domain.MLRecipe, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := r.recipes[id]; v != nil {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) UpsertRecipeRun(_ context.Context, run *domain.MLRecipeRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *run
	r.recipeRuns[cp.ID] = &cp
	return nil
}
func (r *coordinatorMLRepoFake) GetRecipeRun(_ context.Context, id uuid.UUID) (*domain.MLRecipeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := r.recipeRuns[id]; v != nil {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) UpsertInferenceEndpoint(_ context.Context, endpoint *domain.MLInferenceEndpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *endpoint
	r.endpoints[cp.ID] = &cp
	return nil
}
func (r *coordinatorMLRepoFake) GetInferenceEndpoint(_ context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := r.endpoints[id]; v != nil {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) GetInferenceEndpointByNameEnv(_ context.Context, name string, envID uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.endpoints {
		if v.Name == name && v.EnvironmentID == envID {
			cp := *v
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) ListInferenceEndpoints(context.Context, uuid.UUID, int, int) ([]domain.MLInferenceEndpoint, error) {
	return nil, nil
}
func (r *coordinatorMLRepoFake) UpsertDeploymentIntent(_ context.Context, intent *domain.MLDeploymentIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *intent
	r.intents[cp.ID] = &cp
	return nil
}
func (r *coordinatorMLRepoFake) GetDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := r.intents[id]; v != nil {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) ListDeploymentIntents(_ context.Context, endpointID, envID uuid.UUID, limit, offset int) ([]domain.MLDeploymentIntent, error) {
	return nil, nil
}
func (r *coordinatorMLRepoFake) UpsertDeploymentRun(_ context.Context, run *domain.MLDeploymentRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *run
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	cp.UpdatedAt = time.Now().UTC()
	if cp.Metadata != nil {
		cp.Metadata = copyAnyMap(cp.Metadata)
	}
	if cp.VerifiedDigests != nil {
		cp.VerifiedDigests = copyMLStringMap(cp.VerifiedDigests)
	}
	r.runs[cp.ID] = &cp
	if cp.Status == domain.RunStatusSucceeded || cp.Status == domain.RunStatusFailed || cp.Status == domain.RunStatusCancelled {
		select {
		case r.completed <- cp.ID:
		default:
		}
	}
	return nil
}
func (r *coordinatorMLRepoFake) GetDeploymentRun(_ context.Context, id uuid.UUID) (*domain.MLDeploymentRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneMLRun(r.runs[id]), nil
}
func (r *coordinatorMLRepoFake) ListDeploymentRuns(_ context.Context, intentID uuid.UUID) ([]domain.MLDeploymentRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.MLDeploymentRun{}
	for _, run := range r.runs {
		if run.DeploymentIntentID == intentID {
			out = append(out, *cloneMLRun(run))
		}
	}
	return out, nil
}
func (r *coordinatorMLRepoFake) UpsertInferenceObservation(_ context.Context, obs *domain.MLInferenceObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if obs.ID == uuid.Nil {
		obs.ID = uuid.New()
	}
	cp := *obs
	r.observations[cp.ID] = &cp
	return nil
}
func (r *coordinatorMLRepoFake) GetLatestInferenceObservation(_ context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest *domain.MLInferenceObservation
	for _, obs := range r.observations {
		if obs.EndpointID == endpointID && obs.EnvironmentID == envID && (latest == nil || obs.ObservedAt.After(latest.ObservedAt)) {
			cp := *obs
			latest = &cp
		}
	}
	return latest, nil
}
func (r *coordinatorMLRepoFake) UpsertInferenceState(_ context.Context, state *domain.MLInferenceState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *state
	r.states[mlInferenceTargetKey(cp.EndpointID, cp.EnvironmentID)] = &cp
	return nil
}
func (r *coordinatorMLRepoFake) GetInferenceState(_ context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := r.states[mlInferenceTargetKey(endpointID, envID)]; v != nil {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) ListInferenceStates(_ context.Context) ([]domain.MLInferenceState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.MLInferenceState{}
	for _, state := range r.states {
		out = append(out, *state)
	}
	return out, nil
}

func (r *coordinatorMLRepoFake) EnsureQueuedMLDeploymentRunForNextReadyIntent(_ context.Context) (*domain.MLDeploymentRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	intents := make([]*domain.MLDeploymentIntent, 0, len(r.intents))
	for _, intent := range r.intents {
		state := r.states[mlInferenceTargetKey(intent.EndpointID, intent.EnvironmentID)]
		if intent.Status != domain.IntentStatusApproved || (intent.ApprovalStatus != domain.ApprovalStatusNotRequired && intent.ApprovalStatus != domain.ApprovalStatusApproved) || state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID || r.hasActiveRunLocked(intent.ID) {
			continue
		}
		intents = append(intents, intent)
	}
	sort.Slice(intents, func(i, j int) bool {
		if !intents[i].CreatedAt.Equal(intents[j].CreatedAt) {
			return intents[i].CreatedAt.Before(intents[j].CreatedAt)
		}
		return intents[i].ID.String() < intents[j].ID.String()
	})
	if len(intents) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	intent := intents[0]
	run := &domain.MLDeploymentRun{ID: uuid.New(), DeploymentIntentID: intent.ID, Status: domain.RunStatusQueued, Metadata: map[string]any{"endpoint_id": intent.EndpointID.String(), "environment_id": intent.EnvironmentID.String(), "model_version_id": intent.ModelVersionID.String()}, CreatedAt: now, UpdatedAt: now}
	r.runs[run.ID] = cloneMLRun(run)
	return cloneMLRun(run), nil
}
func (r *coordinatorMLRepoFake) ClaimNextQueuedMLDeploymentRun(_ context.Context) (*domain.MLDeploymentRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := make([]*domain.MLDeploymentRun, 0)
	for _, run := range r.runs {
		if run.Status == domain.RunStatusQueued {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if !runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].CreatedAt.Before(runs[j].CreatedAt)
		}
		return runs[i].ID.String() < runs[j].ID.String()
	})
	for _, run := range runs {
		intent := r.intents[run.DeploymentIntentID]
		state := (*domain.MLInferenceState)(nil)
		if intent != nil {
			state = r.states[mlInferenceTargetKey(intent.EndpointID, intent.EnvironmentID)]
		}
		if intent == nil || state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID || r.targetHasRunningLocked(intent.EndpointID, intent.EnvironmentID) {
			continue
		}
		now := time.Now().UTC()
		run.Status = domain.RunStatusRunning
		run.StartedAt = &now
		run.UpdatedAt = now
		return cloneMLRun(run), nil
	}
	return nil, nil
}
func (r *coordinatorMLRepoFake) RequeueStaleMLDeploymentRuns(_ context.Context, olderThan time.Duration) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().UTC().Add(-olderThan)
	count := 0
	for _, run := range r.runs {
		if run.Status == domain.RunStatusRunning && run.UpdatedAt.Before(cutoff) {
			run.Status = domain.RunStatusQueued
			run.StartedAt = nil
			run.UpdatedAt = time.Now().UTC()
			count++
		}
	}
	r.requeued += count
	return count, nil
}
func (r *coordinatorMLRepoFake) hasActiveRunLocked(intentID uuid.UUID) bool {
	for _, run := range r.runs {
		if run.DeploymentIntentID == intentID && (run.Status == domain.RunStatusQueued || run.Status == domain.RunStatusRunning) {
			return true
		}
	}
	return false
}
func (r *coordinatorMLRepoFake) targetHasRunningLocked(endpointID, envID uuid.UUID) bool {
	for _, run := range r.runs {
		intent := r.intents[run.DeploymentIntentID]
		if intent != nil && intent.EndpointID == endpointID && intent.EnvironmentID == envID && run.Status == domain.RunStatusRunning {
			return true
		}
	}
	return false
}

type captureMLProvisioningResponder struct {
	statuses []string
	results  []string
	errors   []string
}

func (r *captureMLProvisioningResponder) PublishStatus(_ context.Context, _ *domain.MLDeploymentIntent, _ *domain.MLDeploymentRun, step, _ string) error {
	r.statuses = append(r.statuses, step)
	return nil
}
func (r *captureMLProvisioningResponder) PublishResult(_ context.Context, _ *domain.MLDeploymentIntent, _ *domain.MLDeploymentRun, status, _ string) error {
	r.results = append(r.results, status)
	return nil
}
func (r *captureMLProvisioningResponder) PublishError(_ context.Context, _ *domain.MLDeploymentIntent, _ *domain.MLDeploymentRun, step string, cause error) error {
	r.errors = append(r.errors, step+":"+cause.Error())
	return nil
}

type coordinatorMLProvisionerFake struct {
	mu          sync.Mutex
	entered     chan coordinatorMLProvisionCall
	releases    []chan struct{}
	calls       []coordinatorMLProvisionCall
	active      map[string]int
	maxByTarget map[string]int
	maxTotal    int
	block       bool
	digests     map[string]string
}
type coordinatorMLProvisionCall struct {
	Index   int
	Target  string
	Runtime domain.MLRuntimeKind
}

func newCoordinatorMLProvisionerFake(block bool, digests map[string]string) *coordinatorMLProvisionerFake {
	return &coordinatorMLProvisionerFake{entered: make(chan coordinatorMLProvisionCall, 8), active: map[string]int{}, maxByTarget: map[string]int{}, block: block, digests: digests}
}
func (p *coordinatorMLProvisionerFake) Provision(_ context.Context, req MLInferenceProvisionRequest) (*MLInferenceProvisionResult, error) {
	release := make(chan struct{})
	p.mu.Lock()
	idx := len(p.calls)
	call := coordinatorMLProvisionCall{Index: idx, Target: mlInferenceTargetKey(req.Endpoint.ID, req.Intent.EnvironmentID), Runtime: req.RuntimeKind}
	p.calls = append(p.calls, call)
	p.releases = append(p.releases, release)
	p.active[call.Target]++
	if p.active[call.Target] > p.maxByTarget[call.Target] {
		p.maxByTarget[call.Target] = p.active[call.Target]
	}
	total := 0
	for _, active := range p.active {
		total += active
	}
	if total > p.maxTotal {
		p.maxTotal = total
	}
	p.mu.Unlock()
	p.entered <- call
	if p.block {
		<-release
	}
	p.mu.Lock()
	p.active[call.Target]--
	p.mu.Unlock()
	return &MLInferenceProvisionResult{RuntimeKind: req.RuntimeKind, EndpointRef: "worker-target", WorkerPubkey: req.Worker.PubKey, WorkerName: req.Worker.Name, BackendEndpoint: "http://worker.example.com:8000", VerifiedDigests: copyMLStringMap(p.digests), TargetName: req.TargetName, Metadata: map[string]any{"allocation_id": "alloc-1"}}, nil
}
func (p *coordinatorMLProvisionerFake) Observe(context.Context, MLInferenceProvisionRequest) (*MLInferenceBackendObservation, error) {
	return &MLInferenceBackendObservation{RuntimeKind: domain.MLRuntimeKindVLLM, BackendEndpoint: "http://worker.example.com:8000", HealthStatus: domain.HealthStatusHealthy, Source: "test", Metadata: map[string]any{"ok": true}}, nil
}
func (p *coordinatorMLProvisionerFake) Deprovision(context.Context, MLInferenceProvisionRequest) error {
	return nil
}
func (p *coordinatorMLProvisionerFake) release(index int) {
	p.mu.Lock()
	release := p.releases[index]
	p.mu.Unlock()
	close(release)
}
func (p *coordinatorMLProvisionerFake) maxActiveFor(target string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxByTarget[target]
}
func (p *coordinatorMLProvisionerFake) maxActiveTotal() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxTotal
}

type coordinatorMLGatewayFake struct{ calls []MLInferenceGatewayRouteSpec }

func (g *coordinatorMLGatewayFake) UpsertEndpoint(_ context.Context, _ string, spec MLInferenceGatewayRouteSpec) (*MLInferenceGatewayObservation, error) {
	g.calls = append(g.calls, spec)
	return &MLInferenceGatewayObservation{Status: domain.GatewayRouteStatusSynced, TargetURL: spec.TargetURL, GatewayConfigHash: "cfg-hash", Metadata: map[string]any{"gateway": "test"}}, nil
}

func TestMLInferenceProvisioningCoordinatorProcessOnceSuccessPublishesAndObserves(t *testing.T) {
	ctx := context.Background()
	fixture := newMLCoordinatorFixture(t, "endpoint-a", time.Now().UTC())
	responder := &captureMLProvisioningResponder{}
	gateway := &coordinatorMLGatewayFake{}
	provisioner := newCoordinatorMLProvisionerFake(false, map[string]string{fixture.artifact.URI: fixture.artifact.SHA256})
	coordinator := NewMLInferenceProvisioningCoordinator(fixture.registry, fixture.placement, fixture.provenance, StaticMLInferenceProvisionerResolver{domain.MLRuntimeKindVLLM: provisioner}, zap.NewNop(), WithMLInferenceProvisioningResponder(responder), WithMLInferenceProvisioningGateway(gateway), WithMLInferenceProvisioningConfig(MLInferenceProvisioningConfig{DefaultGatewayRef: "gateway-prod"}))

	if err := coordinator.ProcessOnce(ctx); err != nil {
		t.Fatalf("process once: %v", err)
	}
	if got, want := responder.statuses, []string{"placing_runtime", "provisioning_runtime", "evaluating_gate", "observing_endpoint", "syncing_gateway"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("status steps got %v want %v", got, want)
	}
	if len(responder.results) != 1 || responder.results[0] != "succeeded" {
		t.Fatalf("unexpected results: %#v", responder.results)
	}
	run := onlyRun(t, fixture.repo)
	if run.Status != domain.RunStatusSucceeded {
		t.Fatalf("expected succeeded run, got %#v", run)
	}
	state, _ := fixture.registry.GetInferenceState(ctx, fixture.endpoint.ID, fixture.endpoint.EnvironmentID)
	if state == nil || state.ActiveRunID == nil || *state.ActiveRunID != run.ID || state.GatewayStatus != domain.GatewayRouteStatusSynced || state.BackendHealth != domain.HealthStatusHealthy {
		t.Fatalf("unexpected state: %#v", state)
	}
	if len(gateway.calls) != 1 || gateway.calls[0].TargetURL != "http://worker.example.com:8000" {
		t.Fatalf("unexpected gateway calls: %#v", gateway.calls)
	}
}

func TestMLInferenceProvisioningRecoveryProcessesSameTargetOldestFirst(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(100, 0).UTC()
	fixture := newMLCoordinatorFixture(t, "endpoint-a", base)
	fixture.seedRunAt(fixture.intent.ID, base)
	fixture.seedRunAt(fixture.intent.ID, base.Add(time.Minute))
	provisioner := newCoordinatorMLProvisionerFake(false, map[string]string{fixture.artifact.URI: fixture.artifact.SHA256})
	coordinator := NewMLInferenceProvisioningCoordinator(fixture.registry, fixture.placement, fixture.provenance, StaticMLInferenceProvisionerResolver{domain.MLRuntimeKindVLLM: provisioner}, zap.NewNop())

	if err := coordinator.ProcessOnce(ctx); err != nil {
		t.Fatalf("process oldest: %v", err)
	}
	if err := coordinator.ProcessOnce(ctx); err != nil {
		t.Fatalf("process newest: %v", err)
	}
	runs := runsForIntents(t, fixture.repo, fixture.intent.ID)
	if len(runs) != 2 {
		t.Fatalf("expected two runs, got %d", len(runs))
	}
	if !runs[0].CreatedAt.Before(runs[1].CreatedAt) {
		t.Fatalf("expected oldest run first, got %#v", runs)
	}
	if runs[0].DeploymentIntentID != fixture.intent.ID || runs[1].DeploymentIntentID != fixture.intent.ID {
		t.Fatalf("expected oldest run then newer run for same intent, got %#v", runs)
	}
}

func TestMLInferenceProvisioningRequeuesStaleRunningWithoutSleep(t *testing.T) {
	ctx := context.Background()
	fixture := newMLCoordinatorFixture(t, "endpoint-a", time.Now().UTC())
	old := time.Now().UTC().Add(-time.Hour)
	stale := &domain.MLDeploymentRun{ID: uuid.New(), DeploymentIntentID: fixture.intent.ID, Status: domain.RunStatusRunning, CreatedAt: old, UpdatedAt: old, StartedAt: &old}
	_ = fixture.repo.UpsertDeploymentRun(ctx, stale)
	fixture.repo.mu.Lock()
	fixture.repo.runs[stale.ID].UpdatedAt = old
	fixture.repo.runs[stale.ID].StartedAt = &old
	fixture.repo.mu.Unlock()
	provisioner := newCoordinatorMLProvisionerFake(false, map[string]string{fixture.artifact.URI: fixture.artifact.SHA256})
	coordinator := NewMLInferenceProvisioningCoordinator(fixture.registry, fixture.placement, fixture.provenance, StaticMLInferenceProvisionerResolver{domain.MLRuntimeKindVLLM: provisioner}, zap.NewNop(), WithMLInferenceProvisioningConfig(MLInferenceProvisioningConfig{StaleRunTimeout: time.Minute}))

	if err := coordinator.ProcessOnce(ctx); err != nil {
		t.Fatalf("process stale: %v", err)
	}
	stored, _ := fixture.repo.GetDeploymentRun(ctx, stale.ID)
	if fixture.repo.requeued != 1 {
		t.Fatalf("expected one stale requeue, got %d", fixture.repo.requeued)
	}
	if stored.Status != domain.RunStatusSucceeded {
		t.Fatalf("expected requeued stale run to complete, got %#v", stored)
	}
}

func TestMLInferenceProvisioningSkipsSupersededRunBeforeProvision(t *testing.T) {
	fixture := newMLCoordinatorFixture(t, "endpoint-a", time.Now().UTC())
	run := fixture.seedRun(fixture.intent.ID)
	fixture.addIntent(t, fixture.endpoint.ID, fixture.endpoint.EnvironmentID, fixture.version.ID, time.Now().UTC().Add(time.Second))
	provisioner := newCoordinatorMLProvisionerFake(false, map[string]string{fixture.artifact.URI: fixture.artifact.SHA256})
	coordinator := NewMLInferenceProvisioningCoordinator(fixture.registry, fixture.placement, fixture.provenance, StaticMLInferenceProvisionerResolver{domain.MLRuntimeKindVLLM: provisioner}, zap.NewNop())

	if err := coordinator.ProcessRun(context.Background(), run.ID); err != nil {
		t.Fatalf("process superseded run: %v", err)
	}
	stored, _ := fixture.repo.GetDeploymentRun(context.Background(), run.ID)
	if stored.Status != domain.RunStatusCancelled {
		t.Fatalf("expected superseded run to be cancelled, got %#v", stored)
	}
	if len(provisioner.calls) != 0 {
		t.Fatalf("expected no provision calls for superseded run, got %#v", provisioner.calls)
	}
}

func TestMLInferenceProvisioningSerializesSameEndpointEnvironment(t *testing.T) {
	fixture := newMLCoordinatorFixture(t, "endpoint-a", time.Now().UTC())
	firstRun := fixture.seedRun(fixture.intent.ID)
	secondRun := fixture.seedRun(fixture.intent.ID)
	provisioner := newCoordinatorMLProvisionerFake(true, map[string]string{fixture.artifact.URI: fixture.artifact.SHA256})
	coordinator := NewMLInferenceProvisioningCoordinator(fixture.registry, fixture.placement, fixture.provenance, StaticMLInferenceProvisionerResolver{domain.MLRuntimeKindVLLM: provisioner}, zap.NewNop())

	firstDone := processMLRunAsync(coordinator, firstRun.ID)
	secondDone := processMLRunAsync(coordinator, secondRun.ID)
	firstCall := waitForMLProvision(t, provisioner)
	provisioner.release(firstCall.Index)
	secondCall := waitForMLProvision(t, provisioner)
	provisioner.release(secondCall.Index)
	if err := <-firstDone; err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got := provisioner.maxActiveFor(mlInferenceTargetKey(fixture.endpoint.ID, fixture.endpoint.EnvironmentID)); got != 1 {
		t.Fatalf("expected same target max active 1, got %d", got)
	}
}

func TestMLInferenceProvisioningAllowsDifferentEndpointEnvironmentsConcurrently(t *testing.T) {
	fixture := newMLCoordinatorFixture(t, "endpoint-a", time.Now().UTC())
	secondEndpoint := fixture.addEndpoint(t, "endpoint-b", uuid.New())
	second := fixture.addIntent(t, secondEndpoint.ID, secondEndpoint.EnvironmentID, fixture.version.ID, time.Now().UTC().Add(time.Second))
	firstRun := fixture.seedRun(fixture.intent.ID)
	secondRun := fixture.seedRun(second.ID)
	provisioner := newCoordinatorMLProvisionerFake(true, map[string]string{fixture.artifact.URI: fixture.artifact.SHA256})
	coordinator := NewMLInferenceProvisioningCoordinator(fixture.registry, fixture.placement, fixture.provenance, StaticMLInferenceProvisionerResolver{domain.MLRuntimeKindVLLM: provisioner}, zap.NewNop())

	firstDone := processMLRunAsync(coordinator, firstRun.ID)
	secondDone := processMLRunAsync(coordinator, secondRun.ID)
	firstCall := waitForMLProvision(t, provisioner)
	secondCall := waitForMLProvision(t, provisioner)
	if firstCall.Target == secondCall.Target {
		t.Fatalf("expected different targets, got %q", firstCall.Target)
	}
	if got := provisioner.maxActiveTotal(); got != 2 {
		t.Fatalf("expected different targets concurrent, max active=%d", got)
	}
	provisioner.release(firstCall.Index)
	provisioner.release(secondCall.Index)
	if err := <-firstDone; err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second run failed: %v", err)
	}
}

type mlCoordinatorFixture struct {
	repo       *coordinatorMLRepoFake
	registry   *MLRegistryService
	provenance *MLProvenanceService
	placement  *MLPlacementService
	endpoint   *domain.MLInferenceEndpoint
	intent     *domain.MLDeploymentIntent
	version    *domain.MLModelVersion
	artifact   *domain.MLArtifactRef
}

func newMLCoordinatorFixture(t *testing.T, endpointName string, createdAt time.Time) *mlCoordinatorFixture {
	t.Helper()
	repo := newCoordinatorMLRepoFake()
	registry := NewMLRegistryService(repo, nil, zap.NewNop())
	versionID := uuid.New()
	modelID := uuid.New()
	repo.models[modelID] = &domain.MLModel{ID: modelID, Slug: "qwen", Name: "Qwen"}
	repo.versions[versionID] = &domain.MLModelVersion{ID: versionID, ModelID: modelID, Version: "v1", RuntimeRequirements: domain.MLRuntimeRequirements{PreferredRuntimes: []domain.MLRuntimeKind{domain.MLRuntimeKindVLLM}, RequiredFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors}, Accelerators: []string{"gpu_nvidia_cuda"}, MinVRAMGB: 48}}
	artifact := &domain.MLArtifactRef{ID: uuid.New(), ModelVersionID: &versionID, Kind: domain.MLArtifactKindModel, Format: domain.MLArtifactFormatSafeTensors, URI: "hf://qwen/model.safetensors", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: createdAt}
	repo.artifacts[artifact.ID] = artifact
	endpoint := &domain.MLInferenceEndpoint{ID: uuid.New(), Name: endpointName, EnvironmentID: uuid.New(), TaskKinds: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}, Protocol: "openai-compatible", Gateway: map[string]any{"gateway_ref": "gateway-prod"}, PlacementPolicy: map[string]any{"accelerator": "gpu_nvidia_cuda", "min_vram_gb": 48}}
	if err := registry.CreateOrUpdateInferenceEndpoint(context.Background(), endpoint); err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	fixture := &mlCoordinatorFixture{repo: repo, registry: registry, provenance: NewMLProvenanceService(repo, nil, zap.NewNop()), placement: NewMLPlacementService(&mockWorkerRepo{workers: []domain.Worker{mlCoordinatorWorker()}}, zap.NewNop()), endpoint: endpoint, version: repo.versions[versionID], artifact: artifact}
	fixture.intent = fixture.addIntent(t, endpoint.ID, endpoint.EnvironmentID, versionID, createdAt)
	return fixture
}
func (f *mlCoordinatorFixture) addEndpoint(t *testing.T, name string, envID uuid.UUID) *domain.MLInferenceEndpoint {
	t.Helper()
	endpoint := &domain.MLInferenceEndpoint{ID: uuid.New(), Name: name, EnvironmentID: envID, TaskKinds: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}, PlacementPolicy: map[string]any{"accelerator": "gpu_nvidia_cuda", "min_vram_gb": 48}}
	if err := f.registry.CreateOrUpdateInferenceEndpoint(context.Background(), endpoint); err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	return endpoint
}
func (f *mlCoordinatorFixture) addIntent(t *testing.T, endpointID, envID, versionID uuid.UUID, createdAt time.Time) *domain.MLDeploymentIntent {
	t.Helper()
	intent := &domain.MLDeploymentIntent{ID: uuid.New(), EndpointID: endpointID, EnvironmentID: envID, ModelVersionID: versionID, RequestedBy: "tester", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved, RuntimePreference: domain.MLRuntimeKindVLLM, CreatedAt: createdAt, UpdatedAt: createdAt}
	if err := f.registry.CreateDeploymentIntent(context.Background(), intent); err != nil {
		t.Fatalf("intent: %v", err)
	}
	f.repo.mu.Lock()
	f.repo.intents[intent.ID].CreatedAt = createdAt
	f.repo.intents[intent.ID].UpdatedAt = createdAt
	f.repo.mu.Unlock()
	return intent
}
func (f *mlCoordinatorFixture) seedRun(intentID uuid.UUID) *domain.MLDeploymentRun {
	return f.seedRunAt(intentID, time.Now().UTC())
}

func (f *mlCoordinatorFixture) seedRunAt(intentID uuid.UUID, createdAt time.Time) *domain.MLDeploymentRun {
	run := &domain.MLDeploymentRun{ID: uuid.New(), DeploymentIntentID: intentID, Status: domain.RunStatusQueued, CreatedAt: createdAt, UpdatedAt: createdAt}
	_ = f.repo.UpsertDeploymentRun(context.Background(), run)
	f.repo.mu.Lock()
	f.repo.runs[run.ID].CreatedAt = createdAt
	f.repo.runs[run.ID].UpdatedAt = createdAt
	f.repo.mu.Unlock()
	return run
}
func mlCoordinatorWorker() domain.Worker {
	w := mlWorker("pk-gpu", "gpu", 0, mlVLLMCaps())
	w.Accelerators = []domain.WorkerAccelerator{{Vendor: "nvidia", Count: 1, MemoryGB: 48}}
	return w
}
func processMLRunAsync(coordinator *MLInferenceProvisioningCoordinator, runID uuid.UUID) <-chan error {
	done := make(chan error, 1)
	go func() { done <- coordinator.ProcessRun(context.Background(), runID) }()
	return done
}
func waitForMLProvision(t *testing.T, p *coordinatorMLProvisionerFake) coordinatorMLProvisionCall {
	t.Helper()
	select {
	case call := <-p.entered:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ML provision")
		return coordinatorMLProvisionCall{}
	}
}
func onlyRun(t *testing.T, repo *coordinatorMLRepoFake) *domain.MLDeploymentRun {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.runs) != 1 {
		t.Fatalf("expected one run, got %d", len(repo.runs))
	}
	for _, run := range repo.runs {
		return cloneMLRun(run)
	}
	return nil
}
func runsForIntents(t *testing.T, repo *coordinatorMLRepoFake, intentIDs ...uuid.UUID) []domain.MLDeploymentRun {
	t.Helper()
	wanted := map[uuid.UUID]struct{}{}
	for _, id := range intentIDs {
		wanted[id] = struct{}{}
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	out := []domain.MLDeploymentRun{}
	for _, run := range repo.runs {
		if _, ok := wanted[run.DeploymentIntentID]; ok {
			out = append(out, *cloneMLRun(run))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
func cloneMLRun(run *domain.MLDeploymentRun) *domain.MLDeploymentRun {
	if run == nil {
		return nil
	}
	cp := *run
	if run.Metadata != nil {
		cp.Metadata = copyAnyMap(run.Metadata)
	}
	if run.VerifiedDigests != nil {
		cp.VerifiedDigests = copyMLStringMap(run.VerifiedDigests)
	}
	return &cp
}
func copyAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
