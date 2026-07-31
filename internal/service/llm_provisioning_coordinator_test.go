package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

type coordinatorRunRepo struct {
	byID   map[uuid.UUID]*domain.LLMDeploymentRun
	queue  []uuid.UUID
	order  []uuid.UUID
	stales int
}

func newCoordinatorRunRepo() *coordinatorRunRepo {
	return &coordinatorRunRepo{byID: map[uuid.UUID]*domain.LLMDeploymentRun{}}
}

func (r *coordinatorRunRepo) seedQueued(intentID, routeID, releaseID, envID uuid.UUID) *domain.LLMDeploymentRun {
	now := time.Now().UTC()
	run := &domain.LLMDeploymentRun{
		ID:                 uuid.New(),
		DeploymentIntentID: intentID,
		Status:             domain.RunStatusQueued,
		Metadata: map[string]any{
			"route_id":       routeID.String(),
			"release_id":     releaseID.String(),
			"environment_id": envID.String(),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	r.byID[run.ID] = cloneRun(run)
	r.queue = append(r.queue, run.ID)
	r.order = append(r.order, run.ID)
	return cloneRun(run)
}

func (r *coordinatorRunRepo) Create(_ context.Context, run *domain.LLMDeploymentRun) error {
	if run == nil {
		return nil
	}
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	run.UpdatedAt = run.CreatedAt
	r.byID[run.ID] = cloneRun(run)
	r.order = append(r.order, run.ID)
	return nil
}

func (r *coordinatorRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMDeploymentRun, error) {
	return cloneRun(r.byID[id]), nil
}

func (r *coordinatorRunRepo) ListByIntent(_ context.Context, intentID uuid.UUID) ([]domain.LLMDeploymentRun, error) {
	out := make([]domain.LLMDeploymentRun, 0)
	for _, id := range r.order {
		if run := r.byID[id]; run != nil && run.DeploymentIntentID == intentID {
			out = append(out, *cloneRun(run))
		}
	}
	return out, nil
}

func (r *coordinatorRunRepo) EnsureQueuedRunForNextReadyIntent(context.Context) (*domain.LLMDeploymentRun, error) {
	for _, id := range r.queue {
		if run := r.byID[id]; run != nil && run.Status == domain.RunStatusQueued {
			return cloneRun(run), nil
		}
	}
	return nil, nil
}

func (r *coordinatorRunRepo) ClaimNextQueuedRun(context.Context) (*domain.LLMDeploymentRun, error) {
	now := time.Now().UTC()
	for i, id := range r.queue {
		run := r.byID[id]
		if run == nil || run.Status != domain.RunStatusQueued {
			continue
		}
		r.queue = append(r.queue[:i], r.queue[i+1:]...)
		run.Status = domain.RunStatusRunning
		run.StartedAt = &now
		run.UpdatedAt = now
		return cloneRun(run), nil
	}
	return nil, nil
}

func (r *coordinatorRunRepo) RequeueStaleRunning(context.Context, time.Duration) (int, error) {
	return r.stales, nil
}

func (r *coordinatorRunRepo) Update(_ context.Context, run *domain.LLMDeploymentRun) error {
	if run == nil {
		return nil
	}
	cp := cloneRun(run)
	cp.UpdatedAt = time.Now().UTC()
	r.byID[cp.ID] = cp
	return nil
}

func (r *coordinatorRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	run := r.byID[id]
	if run == nil {
		return nil
	}
	now := time.Now().UTC()
	run.Status = status
	run.ExitCode = exitCode
	run.UpdatedAt = now
	if status == domain.RunStatusRunning {
		run.StartedAt = &now
	}
	if status == domain.RunStatusSucceeded || status == domain.RunStatusFailed || status == domain.RunStatusCancelled {
		run.FinishedAt = &now
	}
	return nil
}

type captureProvisioningResponder struct {
	statuses []captureProvisioningStatus
	results  []captureProvisioningResult
	errors   []captureProvisioningError
}

type captureProvisioningStatus struct {
	intentID uuid.UUID
	runID    uuid.UUID
	step     string
	message  string
}

type captureProvisioningResult struct {
	intentID uuid.UUID
	runID    uuid.UUID
	status   string
	message  string
}

type captureProvisioningError struct {
	intentID uuid.UUID
	runID    uuid.UUID
	step     string
	cause    string
}

func (r *captureProvisioningResponder) PublishStatus(_ context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step, message string) error {
	r.statuses = append(r.statuses, captureProvisioningStatus{intentID: intent.ID, runID: run.ID, step: step, message: message})
	return nil
}

func (r *captureProvisioningResponder) PublishResult(_ context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, status, message string) error {
	r.results = append(r.results, captureProvisioningResult{intentID: intent.ID, runID: run.ID, status: status, message: message})
	return nil
}

func (r *captureProvisioningResponder) PublishError(_ context.Context, intent *domain.LLMDeploymentIntent, run *domain.LLMDeploymentRun, step string, cause error) error {
	r.errors = append(r.errors, captureProvisioningError{intentID: intent.ID, runID: run.ID, step: step, cause: cause.Error()})
	return nil
}

type fakeCoordinatorProvisioner struct {
	provisionResult   *llmadapter.ProvisionCandidateResult
	provisionErr      error
	observeResults    []*llmadapter.BackendObservation
	observeErr        error
	provisionRequests []llmadapter.ProvisionCandidateRequest
	observeRequests   []llmadapter.ProvisionCandidateRequest
	deprovisionCalls  []llmadapter.ProvisionCandidateRequest
	deprovisionErr    error
}

func (p *fakeCoordinatorProvisioner) Provision(_ context.Context, req llmadapter.ProvisionCandidateRequest) (*llmadapter.ProvisionCandidateResult, error) {
	p.provisionRequests = append(p.provisionRequests, req)
	if p.provisionErr != nil {
		return nil, p.provisionErr
	}
	return p.provisionResult, nil
}

func (p *fakeCoordinatorProvisioner) Observe(_ context.Context, req llmadapter.ProvisionCandidateRequest) (*llmadapter.BackendObservation, error) {
	p.observeRequests = append(p.observeRequests, req)
	if p.observeErr != nil {
		return nil, p.observeErr
	}
	if len(p.observeResults) == 0 {
		return nil, nil
	}
	obs := p.observeResults[0]
	if len(p.observeResults) > 1 {
		p.observeResults = p.observeResults[1:]
	}
	return obs, nil
}

func (p *fakeCoordinatorProvisioner) Deprovision(_ context.Context, req llmadapter.ProvisionCandidateRequest) error {
	p.deprovisionCalls = append(p.deprovisionCalls, req)
	return p.deprovisionErr
}

type fakeCoordinatorGateway struct {
	upsertObs *llmadapter.GatewayRouteObservation
	upsertErr error
	calls     []llmadapter.GatewayRouteSpec
}

type fakeLLMPromotionLock struct {
	calls  int
	onLock func()
}

func (l *fakeLLMPromotionLock) Lock(context.Context, uuid.UUID, uuid.UUID) (func(), error) {
	l.calls++
	if l.onLock != nil {
		l.onLock()
	}
	return func() {}, nil
}

func (g *fakeCoordinatorGateway) UpsertRoute(_ context.Context, _ string, spec llmadapter.GatewayRouteSpec) (*llmadapter.GatewayRouteObservation, error) {
	g.calls = append(g.calls, spec)
	if g.upsertErr != nil {
		return nil, g.upsertErr
	}
	return g.upsertObs, nil
}

func (g *fakeCoordinatorGateway) GetRoute(context.Context, string, string) (*llmadapter.GatewayRouteObservation, error) {
	return nil, nil
}

func (g *fakeCoordinatorGateway) DeleteRoute(context.Context, string, string) error { return nil }

func TestLLMProvisioningCoordinatorIntentEventsTriggerWork(t *testing.T) {
	publisher := events.NewInProcessPublisher(zap.NewNop())
	coordinator := &LLMProvisioningCoordinator{wake: make(chan struct{}, 1)}
	coordinator.SetupSubscriptions(publisher)

	for _, eventType := range []events.EventType{
		events.EventLLMDeploymentIntentCreated,
		events.EventLLMDeploymentIntentApproved,
	} {
		publisher.Publish(context.Background(), events.Event{Type: eventType})
		select {
		case <-coordinator.wake:
		case <-time.After(time.Second):
			t.Fatalf("event %s did not trigger coordinator work", eventType)
		}
	}
}

func TestLLMProvisioningCoordinatorProcessOnceSuccessPublishesProgressAndCompletion(t *testing.T) {
	ctx := context.Background()
	routeID, releaseID, envID := uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, releaseID, envID)
	runs := newCoordinatorRunRepo()
	registry := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, runs, repos.obs, repos.states, nil, zap.NewNop())
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: releaseID, EnvironmentID: envID, RequestedBy: "tester", Metadata: map[string]any{"nostr_event_id": "req-1", "nostr_request_pubkey": "pub-1"}}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	run := runs.seedQueued(intent.ID, routeID, releaseID, envID)
	provisioner := &fakeCoordinatorProvisioner{
		provisionResult: &llmadapter.ProvisionCandidateResult{
			BackendKind:     domain.LLMBackendKindVLLM,
			BackendEndpoint: "http://worker.example.com:8000",
			EndpointRef:     "prod-target",
			TargetName:      "llm-drydock-review",
			WorkerPubkey:    "pk-l40s",
			WorkerName:      "T7920 L40S",
			Metadata:        map[string]any{"allocation_id": "alloc-1"},
		},
		observeResults: []*llmadapter.BackendObservation{
			{BackendKind: domain.LLMBackendKindVLLM, BackendEndpoint: "http://worker.example.com:8000", HealthStatus: domain.HealthStatusHealthy, Source: "test", Metadata: map[string]any{"phase": "gate"}},
			{BackendKind: domain.LLMBackendKindVLLM, BackendEndpoint: "http://worker.example.com:8000", HealthStatus: domain.HealthStatusHealthy, Source: "test", Metadata: map[string]any{"phase": "record"}},
		},
	}
	gateway := &fakeCoordinatorGateway{upsertObs: &llmadapter.GatewayRouteObservation{RouteName: "drydock-review", PublicModel: "drydock.review", TargetURL: "http://worker.example.com:8000", Status: domain.GatewayRouteStatusSynced, GatewayConfigHash: "cfg-hash", Metadata: map[string]any{"gateway": "test"}}}
	responder := &captureProvisioningResponder{}
	placement := NewLLMPlacementService(&mockWorkerRepo{workers: []domain.Worker{llmWorker("pk-l40s", "T7920 L40S", 0, []domain.WorkerAccelerator{{Vendor: "nvidia", Model: "L40S", Count: 1, MemoryGB: 48}})}}, zap.NewNop())
	promotionLock := &fakeLLMPromotionLock{}
	coordinator := NewLLMProvisioningCoordinator(registry, repos.envs, runs, placement, llmadapter.StaticProvisionerResolver{domain.LLMBackendKindVLLM: provisioner}, gateway, "", zap.NewNop(), WithLLMProvisioningResponder(responder), WithLLMPromotionLock(promotionLock))

	if err := coordinator.ProcessOnce(ctx); err != nil {
		t.Fatalf("process once: %v", err)
	}
	if got, want := responderStatusSteps(responder), []string{"placing_backend", "provisioning_backend", "evaluating_gate", "syncing_gateway", "recording_observation"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected status steps: got %v want %v", got, want)
	}
	if len(responder.results) != 1 || responder.results[0].status != "completed" || responder.results[0].runID != run.ID {
		t.Fatalf("unexpected terminal result callbacks: %#v", responder.results)
	}
	if len(responder.errors) != 0 {
		t.Fatalf("expected no error callbacks, got %#v", responder.errors)
	}
	stored := runs.byID[run.ID]
	if stored == nil || stored.Status != domain.RunStatusSucceeded {
		t.Fatalf("expected succeeded run, got %#v", stored)
	}
	if repos.obs.latest == nil || repos.obs.latest.BackendEndpoint != "http://worker.example.com:8000" || repos.obs.latest.GatewayStatus != domain.GatewayRouteStatusSynced {
		t.Fatalf("unexpected latest observation: %#v", repos.obs.latest)
	}
	state, err := registry.GetRouteState(ctx, routeID, envID)
	if err != nil {
		t.Fatalf("get route state: %v", err)
	}
	if state == nil || state.ActiveRunID == nil || *state.ActiveRunID != run.ID || state.CurrentObservationID == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID {
		t.Fatalf("unexpected route state: %#v", state)
	}
	if len(gateway.calls) != 1 || gateway.calls[0].RouteName != "drydock-review" || gateway.calls[0].TargetURL != "http://worker.example.com:8000" {
		t.Fatalf("unexpected gateway calls: %#v", gateway.calls)
	}
	if len(provisioner.deprovisionCalls) != 0 {
		t.Fatalf("expected no deprovision calls on success, got %d", len(provisioner.deprovisionCalls))
	}
	if promotionLock.calls != 1 {
		t.Fatalf("promotion lock calls = %d, want 1", promotionLock.calls)
	}
}

func TestLLMProvisioningCoordinatorRechecksDesiredIntentUnderPromotionLock(t *testing.T) {
	ctx := context.Background()
	routeID, releaseID, envID := uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, releaseID, envID)
	runs := newCoordinatorRunRepo()
	registry := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, runs, repos.obs, repos.states, nil, zap.NewNop())
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: releaseID, EnvironmentID: envID, RequestedBy: "tester"}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	run := runs.seedQueued(intent.ID, routeID, releaseID, envID)
	provisioner := &fakeCoordinatorProvisioner{
		provisionResult: &llmadapter.ProvisionCandidateResult{BackendKind: domain.LLMBackendKindVLLM, BackendEndpoint: "http://worker:8000"},
		observeResults:  []*llmadapter.BackendObservation{{HealthStatus: domain.HealthStatusHealthy}},
		deprovisionErr:  errors.New("cleanup failed"),
	}
	gateway := &fakeCoordinatorGateway{}
	placement := NewLLMPlacementService(&mockWorkerRepo{workers: []domain.Worker{llmWorker("worker", "worker", 0, []domain.WorkerAccelerator{{Vendor: "nvidia", Count: 1, MemoryGB: 48}})}}, zap.NewNop())
	lock := &fakeLLMPromotionLock{onLock: func() {
		state, err := repos.states.Get(ctx, routeID, envID)
		if err != nil {
			t.Fatal(err)
		}
		newerIntent := uuid.New()
		state.DesiredIntentID = &newerIntent
		if err := repos.states.Upsert(ctx, state); err != nil {
			t.Fatal(err)
		}
	}}
	coordinator := NewLLMProvisioningCoordinator(registry, repos.envs, runs, placement, llmadapter.StaticProvisionerResolver{domain.LLMBackendKindVLLM: provisioner}, gateway, "gateway", zap.NewNop(), WithLLMPromotionLock(lock))

	err := coordinator.ProcessOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("superseded cleanup error = %v", err)
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("obsolete intent mutated gateway: %#v", gateway.calls)
	}
	if len(provisioner.deprovisionCalls) != 1 {
		t.Fatalf("deprovision calls = %d, want 1", len(provisioner.deprovisionCalls))
	}
	if stored := runs.byID[run.ID]; stored == nil || stored.Status != domain.RunStatusCancelled {
		t.Fatalf("superseded run = %#v, want cancelled", stored)
	}
}

func TestLLMProvisioningCoordinatorProcessOnceIntentLoadFailureUsesQueuedRunCorrelationMetadata(t *testing.T) {
	ctx := context.Background()
	routeID, releaseID, envID := uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, releaseID, envID)
	runs := newCoordinatorRunRepo()
	registry := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, runs, repos.obs, repos.states, nil, zap.NewNop())
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: releaseID, EnvironmentID: envID, RequestedBy: "tester", Metadata: map[string]any{"nostr_event_id": "req-queued", "nostr_request_pubkey": "pub-queued"}}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	run := runs.seedQueued(intent.ID, routeID, releaseID, envID)
	runs.byID[run.ID].Metadata["nostr_event_id"] = "req-queued"
	runs.byID[run.ID].Metadata["nostr_request_pubkey"] = "pub-queued"
	delete(repos.intents.byID, intent.ID)
	responder := &captureProvisioningResponder{}
	coordinator := NewLLMProvisioningCoordinator(registry, repos.envs, runs, NewLLMPlacementService(&mockWorkerRepo{}, zap.NewNop()), llmadapter.StaticProvisionerResolver{}, &fakeCoordinatorGateway{}, "", zap.NewNop(), WithLLMProvisioningResponder(responder), WithLLMPromotionLock(&fakeLLMPromotionLock{}))

	err := coordinator.ProcessOnce(ctx)
	if err == nil {
		t.Fatal("expected intent load failure")
	}
	if !strings.Contains(err.Error(), intent.ID.String()) {
		t.Fatalf("expected intent id in error, got %v", err)
	}
	if len(responder.errors) != 1 || responder.errors[0].intentID != intent.ID || responder.errors[0].runID != run.ID || responder.errors[0].step != "failed" {
		t.Fatalf("unexpected error callbacks: %#v", responder.errors)
	}
	stored := runs.byID[run.ID]
	if stored == nil || stored.Status != domain.RunStatusFailed {
		t.Fatalf("expected failed run, got %#v", stored)
	}
}

func TestLLMProvisioningCoordinatorProcessOnceRouteLoadFailurePublishesTerminalErrorAndFailsRun(t *testing.T) {
	ctx := context.Background()
	routeID, releaseID, envID := uuid.New(), uuid.New(), uuid.New()
	repos := newLLMRegistryFakes(routeID, releaseID, envID)
	runs := newCoordinatorRunRepo()
	registry := NewLLMRegistryService(repos.routes, repos.releases, repos.envs, repos.intents, runs, repos.obs, repos.states, nil, zap.NewNop())
	intent := &domain.LLMDeploymentIntent{RouteID: routeID, ReleaseID: releaseID, EnvironmentID: envID, RequestedBy: "tester", Metadata: map[string]any{"nostr_event_id": "req-2", "nostr_request_pubkey": "pub-2"}}
	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	run := runs.seedQueued(intent.ID, routeID, releaseID, envID)
	delete(repos.releases.byID, releaseID)
	responder := &captureProvisioningResponder{}
	coordinator := NewLLMProvisioningCoordinator(registry, repos.envs, runs, NewLLMPlacementService(&mockWorkerRepo{}, zap.NewNop()), llmadapter.StaticProvisionerResolver{}, &fakeCoordinatorGateway{}, "", zap.NewNop(), WithLLMProvisioningResponder(responder), WithLLMPromotionLock(&fakeLLMPromotionLock{}))

	err := coordinator.ProcessOnce(ctx)
	if err == nil {
		t.Fatal("expected route/release load failure")
	}
	if !strings.Contains(err.Error(), releaseID.String()) {
		t.Fatalf("expected release id in error, got %v", err)
	}
	if len(responder.results) != 0 {
		t.Fatalf("expected no success result callbacks, got %#v", responder.results)
	}
	if len(responder.errors) != 1 || responder.errors[0].runID != run.ID || responder.errors[0].step != "failed" {
		t.Fatalf("unexpected error callbacks: %#v", responder.errors)
	}
	if !strings.Contains(responder.errors[0].cause, releaseID.String()) {
		t.Fatalf("expected release id in terminal error callback, got %#v", responder.errors[0])
	}
	stored := runs.byID[run.ID]
	if stored == nil || stored.Status != domain.RunStatusFailed {
		t.Fatalf("expected failed run, got %#v", stored)
	}
	if stored.Metadata["error"] == nil || !strings.Contains(stored.Metadata["error"].(string), releaseID.String()) {
		t.Fatalf("expected stored run error metadata, got %#v", stored.Metadata)
	}
	state, stateErr := registry.GetRouteState(ctx, routeID, envID)
	if stateErr != nil {
		t.Fatalf("get route state: %v", stateErr)
	}
	if state == nil || state.DriftStatus != domain.DriftStatusDrifted || state.GatewayStatus != domain.GatewayRouteStatusError {
		t.Fatalf("expected drifted error state after failed run, got %#v", state)
	}
}

func responderStatusSteps(r *captureProvisioningResponder) []string {
	out := make([]string, 0, len(r.statuses))
	for _, status := range r.statuses {
		out = append(out, status.step)
	}
	return out
}

func cloneRun(run *domain.LLMDeploymentRun) *domain.LLMDeploymentRun {
	if run == nil {
		return nil
	}
	cp := *run
	if run.Metadata != nil {
		cp.Metadata = make(map[string]any, len(run.Metadata))
		for k, v := range run.Metadata {
			cp.Metadata[k] = v
		}
	}
	return &cp
}
