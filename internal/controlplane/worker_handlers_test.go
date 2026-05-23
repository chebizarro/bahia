package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
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
	repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker-a", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive, LastAdvertisementAt: time.Now().UTC()})
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
	)
}

func newWorkerHandlerTestReactor(t *testing.T, authorizedPubkey string, capture *captureNostrPublisher, repo *memoryWorkerRepo) *Reactor {
	t.Helper()
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	return NewReactor(Config{AuthorizedPubkeys: []string{authorizedPubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithWorkerRepository(repo))
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
