package controlplane

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func newWorkerHandlerTestReactor(t *testing.T, authorizedPubkey string, capture *captureNostrPublisher, repo *memoryWorkerRepo) *Reactor {
	t.Helper()
	return newWorkerHandlerTestReactorWithRegistry(t, authorizedPubkey, capture, repo, nil)
}

func newWorkerHandlerTestReactorWithRegistry(t *testing.T, authorizedPubkey string, capture *captureNostrPublisher, repo *memoryWorkerRepo, registry *service.RegistryService) *Reactor {
	t.Helper()
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	return NewReactor(Config{AuthorizedPubkeys: []string{authorizedPubkey}}, registry, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithWorkerRepository(repo))
}

type controlplaneCleanupLoomFake struct {
	releasePoll <-chan struct{}
}

func (f *controlplaneCleanupLoomFake) SubmitCleanupJob(context.Context, service.CleanupJobRequest) (string, error) {
	return "loom-cleanup-dispatch", nil
}

func (f *controlplaneCleanupLoomFake) PollCleanupJobStatusFromWorker(context.Context, string, string, ...service.CleanupStatusCallback) (*service.CleanupJobStatus, error) {
	<-f.releasePoll
	success := true
	return &service.CleanupJobStatus{Status: "completed", Success: &success}, nil
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
