package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestWorkerCordonHandlerUpdatesWorkerAndPublishesLifecycle(t *testing.T) {
	ctx := context.Background()
	operatorKey := nostr.GeneratePrivateKey()
	operatorPubkey, _ := nostr.GetPublicKey(operatorKey)
	workerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	capture := &captureNostrPublisher{published: 1}
	repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker-a", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive, LastAdvertisementAt: time.Now().UTC(), Labels: map[string]string{"role": "ci"}})
	reactor := newWorkerHandlerTestReactor(t, operatorPubkey, capture, repo)
	event := &nostr.Event{ID: "cordon-request", PubKey: operatorPubkey, Kind: KindWorkerCordonRequest, Tags: nostr.Tags{{"d", "cordon-1"}, {"worker", workerPubkey}}, Content: mustJSON(WorkerLifecycleCommand{WorkerPubKey: workerPubkey, Reason: "maintenance window", IdempotencyKey: "cordon-1", OperatorMetadata: map[string]any{"ticket": "ops-1"}})}

	reactor.handleWorkerCordonRequest(ctx, event)

	updated, err := repo.GetByPubKey(ctx, workerPubkey)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if updated.SchedulingState != domain.WorkerSchedulingCordoned || updated.SchedulingNote != "maintenance window" {
		t.Fatalf("unexpected worker state: %#v", updated)
	}
	assertPublishedKind(t, capture.events, KindWorkerStatus)
	assertPublishedKind(t, capture.events, KindWorkerState)
	assertPublishedKind(t, capture.events, KindWorkerResult)
	state := lastPublishedKind(t, capture.events, KindWorkerState)
	if tagValueNostr(state.Tags, "d") != workerPubkey || tagValueNostr(state.Tags, "scheduling_state") != string(domain.WorkerSchedulingCordoned) {
		t.Fatalf("unexpected worker state tags: %#v", state.Tags)
	}
	result := lastPublishedKind(t, capture.events, KindWorkerResult)
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("result content: %v", err)
	}
	if payload["status"] != "succeeded" || payload["worker_pubkey"] != workerPubkey || payload["idempotency_key"] != "cordon-1" || payload["scheduling_state"] != string(domain.WorkerSchedulingCordoned) {
		t.Fatalf("unexpected result payload: %#v", payload)
	}
}

func TestWorkerLabelsUpdateHandlerUpdatesLabelsAndPublishesReadModel(t *testing.T) {
	ctx := context.Background()
	operatorKey := nostr.GeneratePrivateKey()
	operatorPubkey, _ := nostr.GetPublicKey(operatorKey)
	workerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	capture := &captureNostrPublisher{published: 1}
	repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker-a", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive, LastAdvertisementAt: time.Now().UTC(), Telemetry: &domain.WorkerTelemetry{SampledAt: time.Unix(100, 0).UTC(), Memory: &domain.WorkerMemoryTelemetry{TotalBytes: 16 << 30, AvailableBytes: 8 << 30}}, Pressure: &domain.WorkerPressureAssessment{OverallLevel: domain.WorkerPressureWarning, CapacityClass: domain.WorkerCapacityReduced, RecommendedAction: domain.WorkerPressureActionOperatorIntervention, AssessedAt: time.Unix(101, 0).UTC()}})
	reactor := newWorkerHandlerTestReactor(t, operatorPubkey, capture, repo)
	event := &nostr.Event{ID: "labels-request", PubKey: operatorPubkey, Kind: KindWorkerLabelsUpdate, Tags: nostr.Tags{{"d", "labels-1"}, {"worker", workerPubkey}}, Content: mustJSON(WorkerLabelsUpdateCommand{WorkerPubKey: workerPubkey, Reason: "pool update", IdempotencyKey: "labels-1", Labels: map[string]string{" role ": " inference ", "track": "canary"}})}

	reactor.handleWorkerLabelsUpdateRequest(ctx, event)

	updated, err := repo.GetByPubKey(ctx, workerPubkey)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if updated.Labels["role"] != "inference" || updated.Labels["track"] != "canary" {
		t.Fatalf("unexpected labels: %#v", updated.Labels)
	}
	state := lastPublishedKind(t, capture.events, KindWorkerState)
	if !hasFullTag(state.Tags, nostr.Tag{"label", "role", "inference"}) || !hasFullTag(state.Tags, nostr.Tag{"label", "track", "canary"}) {
		t.Fatalf("state event missing label tags: %#v", state.Tags)
	}
	if tagValueNostr(state.Tags, "capacity_class") != string(domain.WorkerCapacityReduced) || tagValueNostr(state.Tags, "pressure_state") != string(domain.WorkerPressureWarning) || tagValueNostr(state.Tags, "recommended_action") != string(domain.WorkerPressureActionOperatorIntervention) {
		t.Fatalf("state event missing pressure tags: %#v", state.Tags)
	}
	var statePayload map[string]any
	if err := json.Unmarshal([]byte(state.Content), &statePayload); err != nil {
		t.Fatalf("state content: %v", err)
	}
	if statePayload["telemetry"] == nil || statePayload["pressure"] == nil {
		t.Fatalf("state content missing telemetry/pressure: %#v", statePayload)
	}
	assertPublishedKind(t, capture.events, KindWorkerResult)
}

func TestWorkerHandlerRejectsConflictingWorkerTagAndContent(t *testing.T) {
	ctx := context.Background()
	operatorKey := nostr.GeneratePrivateKey()
	operatorPubkey, _ := nostr.GetPublicKey(operatorKey)
	tagWorkerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	bodyWorkerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	capture := &captureNostrPublisher{published: 1}
	repo := newMemoryWorkerRepo(domain.Worker{PubKey: tagWorkerPubkey, Name: "worker-a", Status: domain.WorkerStatusOnline}, domain.Worker{PubKey: bodyWorkerPubkey, Name: "worker-b", Status: domain.WorkerStatusOnline})
	reactor := newWorkerHandlerTestReactor(t, operatorPubkey, capture, repo)
	event := &nostr.Event{ID: "conflict", PubKey: operatorPubkey, Kind: KindWorkerCordonRequest, Tags: nostr.Tags{{"d", "cordon-1"}, {"worker", tagWorkerPubkey}}, Content: mustJSON(WorkerLifecycleCommand{WorkerPubKey: bodyWorkerPubkey, IdempotencyKey: "cordon-1"})}

	reactor.handleWorkerCordonRequest(ctx, event)

	bodyWorker, _ := repo.GetByPubKey(ctx, bodyWorkerPubkey)
	if bodyWorker.SchedulingState == domain.WorkerSchedulingCordoned {
		t.Fatal("handler must reject mismatched worker tag/content instead of mutating body worker")
	}
	result := lastPublishedKind(t, capture.events, KindWorkerResult)
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("result content: %v", err)
	}
	if payload["status"] != "failed" || payload["code"] != "validation_error" {
		t.Fatalf("unexpected conflict result: %#v", payload)
	}
}

func TestWorkerHandlerRejectsDisabledWorkerTransition(t *testing.T) {
	ctx := context.Background()
	operatorKey := nostr.GeneratePrivateKey()
	operatorPubkey, _ := nostr.GetPublicKey(operatorKey)
	workerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	capture := &captureNostrPublisher{published: 1}
	repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker-a", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingDisabled})
	reactor := newWorkerHandlerTestReactor(t, operatorPubkey, capture, repo)
	event := &nostr.Event{ID: "disabled-transition", PubKey: operatorPubkey, Kind: KindWorkerMaintenanceExit, Tags: nostr.Tags{{"d", "maint-exit-1"}, {"worker", workerPubkey}}, Content: mustJSON(WorkerLifecycleCommand{WorkerPubKey: workerPubkey, IdempotencyKey: "maint-exit-1"})}

	reactor.handleWorkerMaintenanceExitRequest(ctx, event)

	updated, _ := repo.GetByPubKey(ctx, workerPubkey)
	if updated.SchedulingState != domain.WorkerSchedulingDisabled {
		t.Fatalf("disabled worker should remain disabled, got %q", updated.SchedulingState)
	}
	result := lastPublishedKind(t, capture.events, KindWorkerResult)
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("result content: %v", err)
	}
	if payload["status"] != "failed" || payload["code"] != "invalid_transition" {
		t.Fatalf("unexpected transition result: %#v", payload)
	}
}

func TestWorkerPolicyApplyHandlerUpdatesEnvironmentPolicy(t *testing.T) {
	ctx := context.Background()
	operatorKey := nostr.GeneratePrivateKey()
	operatorPubkey, _ := nostr.GetPublicKey(operatorKey)
	workerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	envID := uuid.New()
	capture := &captureNostrPublisher{published: 1}
	workers := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker-a", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive})
	environments := newMemoryEnvironmentRepo(domain.Environment{ID: envID, Name: "prod", RuntimeConfig: map[string]any{"worker_policy": map[string]any{"strategy": "cheapest"}}})
	registry := service.NewRegistryService(nil, environments, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	reactor := newWorkerHandlerTestReactorWithRegistry(t, operatorPubkey, capture, workers, registry)
	event := &nostr.Event{ID: "policy-apply", PubKey: operatorPubkey, Kind: KindWorkerPolicyApplyRequest, Tags: nostr.Tags{{"d", "policy-1"}, {"environment", envID.String()}}, Content: mustJSON(WorkerPolicyApplyCommand{
		EnvironmentID:  envID.String(),
		IdempotencyKey: "policy-1",
		Policy: map[string]any{
			"pinned_worker":  workerPubkey,
			"label_selector": map[string]any{"role": "inference"},
			"rollout": map[string]any{
				"from_labels": map[string]any{"track": "canary"},
				"to_labels":   map[string]any{"track": "stable"},
			},
		},
	})}

	reactor.handleWorkerPolicyApplyRequest(ctx, event)

	updated, err := environments.GetByID(ctx, envID)
	if err != nil {
		t.Fatalf("get environment: %v", err)
	}
	policy, ok := updated.RuntimeConfig["worker_policy"].(map[string]any)
	if !ok {
		t.Fatalf("worker policy not persisted: %#v", updated.RuntimeConfig)
	}
	if policy["strategy"] != "cheapest" || policy["pinned_worker"] != workerPubkey {
		t.Fatalf("unexpected merged policy: %#v", policy)
	}
	labels := policy["label_selector"].(map[string]any)
	if labels["role"] != "inference" {
		t.Fatalf("label selector not persisted: %#v", policy)
	}
	assertPublishedKind(t, capture.events, KindEnvironmentRegistry)
	result := lastPublishedKind(t, capture.events, KindWorkerResult)
	if tagValueNostr(result.Tags, "environment") != envID.String() || tagValueNostr(result.Tags, "command") != WorkerPolicyApplyRequest {
		t.Fatalf("unexpected policy result tags: %#v", result.Tags)
	}
}

func TestWorkerPolicyApplyRejectsMismatchedWorkerTagAndPolicyPin(t *testing.T) {
	ctx := context.Background()
	operatorKey := nostr.GeneratePrivateKey()
	operatorPubkey, _ := nostr.GetPublicKey(operatorKey)
	tagWorkerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	policyWorkerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	envID := uuid.New()
	capture := &captureNostrPublisher{published: 1}
	workers := newMemoryWorkerRepo(domain.Worker{PubKey: tagWorkerPubkey, Name: "worker-a", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive}, domain.Worker{PubKey: policyWorkerPubkey, Name: "worker-b", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive})
	environments := newMemoryEnvironmentRepo(domain.Environment{ID: envID, Name: "prod", RuntimeConfig: map[string]any{}})
	registry := service.NewRegistryService(nil, environments, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	reactor := newWorkerHandlerTestReactorWithRegistry(t, operatorPubkey, capture, workers, registry)
	event := &nostr.Event{ID: "policy-mismatch", PubKey: operatorPubkey, Kind: KindWorkerPolicyApplyRequest, Tags: nostr.Tags{{"d", "policy-1"}, {"environment", envID.String()}, {"worker", tagWorkerPubkey}}, Content: mustJSON(WorkerPolicyApplyCommand{EnvironmentID: envID.String(), IdempotencyKey: "policy-1", Policy: map[string]any{"pinned_worker": policyWorkerPubkey}})}

	reactor.handleWorkerPolicyApplyRequest(ctx, event)

	updated, err := environments.GetByID(ctx, envID)
	if err != nil {
		t.Fatalf("get environment: %v", err)
	}
	if updated.RuntimeConfig["worker_policy"] != nil {
		t.Fatalf("mismatched policy pin should not persist: %#v", updated.RuntimeConfig)
	}
	result := lastPublishedKind(t, capture.events, KindWorkerResult)
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("result content: %v", err)
	}
	if payload["status"] != "failed" || payload["code"] != "validation_error" {
		t.Fatalf("unexpected mismatch result: %#v", payload)
	}
}

func TestWorkloadPinHandlerUpdatesEnvironmentPolicy(t *testing.T) {
	ctx := context.Background()
	operatorKey := nostr.GeneratePrivateKey()
	operatorPubkey, _ := nostr.GetPublicKey(operatorKey)
	workerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	envID := uuid.New()
	capture := &captureNostrPublisher{published: 1}
	workers := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker-a", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive})
	environments := newMemoryEnvironmentRepo(domain.Environment{ID: envID, Name: "prod", RuntimeConfig: map[string]any{}})
	registry := service.NewRegistryService(nil, environments, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	reactor := newWorkerHandlerTestReactorWithRegistry(t, operatorPubkey, capture, workers, registry)
	event := &nostr.Event{ID: "pin-request", PubKey: operatorPubkey, Kind: KindWorkloadPinRequest, Tags: nostr.Tags{{"d", "pin-1"}, {"environment", envID.String()}, {"worker", workerPubkey}}, Content: mustJSON(WorkloadPinCommand{EnvironmentID: envID.String(), WorkerPubKey: workerPubkey, IdempotencyKey: "pin-1"})}

	reactor.handleWorkloadPinRequest(ctx, event)

	updated, err := environments.GetByID(ctx, envID)
	if err != nil {
		t.Fatalf("get environment: %v", err)
	}
	policy, ok := updated.RuntimeConfig["worker_policy"].(map[string]any)
	if !ok || policy["pinned_worker"] != workerPubkey {
		t.Fatalf("pin not persisted: %#v", updated.RuntimeConfig)
	}
	result := lastPublishedKind(t, capture.events, KindWorkerResult)
	if tagValueNostr(result.Tags, "worker") != workerPubkey || tagValueNostr(result.Tags, "environment") != envID.String() || tagValueNostr(result.Tags, "command") != WorkloadPinRequest {
		t.Fatalf("unexpected pin result tags: %#v", result.Tags)
	}
}

func TestWorkerHandlerRejectsMissingIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	operatorKey := nostr.GeneratePrivateKey()
	operatorPubkey, _ := nostr.GetPublicKey(operatorKey)
	workerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	capture := &captureNostrPublisher{published: 1}
	repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker-a", Status: domain.WorkerStatusOnline})
	reactor := newWorkerHandlerTestReactor(t, operatorPubkey, capture, repo)
	event := &nostr.Event{ID: "missing-idempotency", PubKey: operatorPubkey, Kind: KindWorkerDrainRequest, Tags: nostr.Tags{{"worker", workerPubkey}}, Content: mustJSON(map[string]any{"worker_pubkey": workerPubkey})}

	reactor.handleWorkerDrainRequest(ctx, event)

	updated, _ := repo.GetByPubKey(ctx, workerPubkey)
	if updated.SchedulingState == domain.WorkerSchedulingDraining {
		t.Fatal("worker should not be changed without an idempotency key")
	}
	result := lastPublishedKind(t, capture.events, KindWorkerResult)
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("result content: %v", err)
	}
	if payload["status"] != "failed" || payload["code"] != "validation_error" {
		t.Fatalf("unexpected validation result: %#v", payload)
	}
}

func TestWorkerCommandKindsAreInDefaultSubscriptionFilter(t *testing.T) {
	filter := nostr.Filter{Kinds: defaultRequestSubscriptionKinds()}
	assertFilterHasKinds(t, filter,
		KindWorkerCordonRequest,
		KindWorkerUncordonRequest,
		KindWorkerDrainRequest,
		KindWorkerUndrainRequest,
		KindWorkerMaintenanceEnter,
		KindWorkerMaintenanceExit,
		KindWorkerLabelsUpdate,
		KindWorkerPolicyApplyRequest,
		KindWorkloadPinRequest,
	)
}

func newWorkerHandlerTestReactor(t *testing.T, authorizedPubkey string, capture *captureNostrPublisher, repo *memoryWorkerRepo) *Reactor {
	t.Helper()
	return newWorkerHandlerTestReactorWithRegistry(t, authorizedPubkey, capture, repo, nil)
}

func newWorkerHandlerTestReactorWithRegistry(t *testing.T, authorizedPubkey string, capture *captureNostrPublisher, repo *memoryWorkerRepo, registry *service.RegistryService) *Reactor {
	t.Helper()
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	return NewReactor(Config{AuthorizedPubkeys: []string{authorizedPubkey}}, registry, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithWorkerRepository(repo))
}

type memoryWorkerRepo struct {
	workers map[string]*domain.Worker
}

func newMemoryWorkerRepo(workers ...domain.Worker) *memoryWorkerRepo {
	repo := &memoryWorkerRepo{workers: map[string]*domain.Worker{}}
	for i := range workers {
		_ = repo.Upsert(context.Background(), &workers[i])
	}
	return repo
}

func (m *memoryWorkerRepo) Upsert(_ context.Context, worker *domain.Worker) error {
	cp := *worker
	if cp.Labels != nil {
		cp.Labels = map[string]string{}
		for key, value := range worker.Labels {
			cp.Labels[key] = value
		}
	}
	if cp.SchedulingState == "" {
		cp.SchedulingState = domain.WorkerSchedulingActive
	}
	m.workers[cp.PubKey] = &cp
	return nil
}

func (m *memoryWorkerRepo) GetByPubKey(_ context.Context, pubkey string) (*domain.Worker, error) {
	worker := m.workers[pubkey]
	if worker == nil {
		return nil, nil
	}
	cp := *worker
	if cp.Labels != nil {
		cp.Labels = map[string]string{}
		for key, value := range worker.Labels {
			cp.Labels[key] = value
		}
	}
	return &cp, nil
}

func (m *memoryWorkerRepo) List(_ context.Context, status string, limit int) ([]domain.Worker, error) {
	out := []domain.Worker{}
	for _, worker := range m.workers {
		if status == "" || string(worker.Status) == status {
			out = append(out, *worker)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memoryWorkerRepo) UpdateStatus(_ context.Context, pubkey string, status domain.WorkerStatus) error {
	worker := m.workers[pubkey]
	if worker != nil {
		worker.Status = status
	}
	return nil
}

func (m *memoryWorkerRepo) UpdateSchedulingState(_ context.Context, pubkey string, state domain.WorkerSchedulingState, note string) error {
	worker := m.workers[pubkey]
	if worker != nil {
		worker.SchedulingState = state
		worker.SchedulingNote = note
	}
	return nil
}

func (m *memoryWorkerRepo) UpdateLabels(_ context.Context, pubkey string, labels map[string]string) error {
	worker := m.workers[pubkey]
	if worker != nil {
		worker.Labels = map[string]string{}
		for key, value := range labels {
			worker.Labels[key] = value
		}
	}
	return nil
}

type memoryEnvironmentRepo struct {
	envs map[uuid.UUID]*domain.Environment
}

func newMemoryEnvironmentRepo(environments ...domain.Environment) *memoryEnvironmentRepo {
	repo := &memoryEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{}}
	for i := range environments {
		_ = repo.Create(context.Background(), &environments[i])
	}
	return repo
}

func (m *memoryEnvironmentRepo) Create(_ context.Context, env *domain.Environment) error {
	cp := copyEnvironment(env)
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	m.envs[cp.ID] = cp
	return nil
}

func (m *memoryEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	return copyEnvironment(m.envs[id]), nil
}

func (m *memoryEnvironmentRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	for _, env := range m.envs {
		if env.Name == name {
			return copyEnvironment(env), nil
		}
	}
	return nil, nil
}

func (m *memoryEnvironmentRepo) List(_ context.Context) ([]domain.Environment, error) {
	out := make([]domain.Environment, 0, len(m.envs))
	for _, env := range m.envs {
		out = append(out, *copyEnvironment(env))
	}
	return out, nil
}

func (m *memoryEnvironmentRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	out := []domain.Environment{}
	for _, env := range m.envs {
		if env.OrgID == orgID {
			out = append(out, *copyEnvironment(env))
		}
	}
	return out, nil
}

func (m *memoryEnvironmentRepo) Update(_ context.Context, env *domain.Environment) error {
	m.envs[env.ID] = copyEnvironment(env)
	return nil
}

func (m *memoryEnvironmentRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.envs, id)
	return nil
}

func copyEnvironment(env *domain.Environment) *domain.Environment {
	if env == nil {
		return nil
	}
	cp := *env
	if env.LoomWorkerSelector != nil {
		cp.LoomWorkerSelector = map[string]any{}
		for key, value := range env.LoomWorkerSelector {
			cp.LoomWorkerSelector[key] = value
		}
	}
	if env.RuntimeConfig != nil {
		cp.RuntimeConfig = copyMapAny(env.RuntimeConfig)
	}
	return &cp
}

func copyMapAny(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		if nested, ok := value.(map[string]any); ok {
			out[key] = copyMapAny(nested)
			continue
		}
		out[key] = value
	}
	return out
}

func lastPublishedKind(t *testing.T, events []nostr.Event, kind int) nostr.Event {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == kind {
			return events[i]
		}
	}
	t.Fatalf("kind %d not published; events=%#v", kind, events)
	return nostr.Event{}
}

func hasFullTag(tags nostr.Tags, want nostr.Tag) bool {
	for _, tag := range tags {
		if len(tag) != len(want) {
			continue
		}
		matched := true
		for i := range tag {
			if tag[i] != want[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
