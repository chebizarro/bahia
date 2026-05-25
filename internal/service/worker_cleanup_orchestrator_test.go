package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

type cleanupLoomOutcome struct {
	status *CleanupJobStatus
	err    error
	wait   <-chan struct{}
}

type cleanupLoomFake struct {
	mu         sync.Mutex
	submitted  []CleanupJobRequest
	submitErr  error
	outcomes   []cleanupLoomOutcome
	pollDoneCh chan struct{}
}

func newCleanupLoomFake(outcomes ...cleanupLoomOutcome) *cleanupLoomFake {
	return &cleanupLoomFake{outcomes: outcomes, pollDoneCh: make(chan struct{}, 8)}
}

func (f *cleanupLoomFake) SubmitCleanupJob(_ context.Context, job CleanupJobRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submitErr != nil {
		return "", f.submitErr
	}
	f.submitted = append(f.submitted, job)
	return "loom-job-" + string(rune('a'+len(f.submitted)-1)), nil
}

func (f *cleanupLoomFake) PollCleanupJobStatusFromWorker(_ context.Context, jobEventID string, expectedWorkerPubkey string, callbacks ...CleanupStatusCallback) (*CleanupJobStatus, error) {
	f.mu.Lock()
	var outcome cleanupLoomOutcome
	if len(f.outcomes) > 0 {
		outcome = f.outcomes[0]
		f.outcomes = f.outcomes[1:]
	}
	f.mu.Unlock()
	if outcome.wait != nil {
		<-outcome.wait
	}
	if outcome.status != nil {
		outcome.status.JobID = jobEventID
		outcome.status.WorkerPubkey = expectedWorkerPubkey
	}
	for _, cb := range callbacks {
		if cb != nil && outcome.status != nil {
			cb(outcome.status)
		}
	}
	f.pollDoneCh <- struct{}{}
	return outcome.status, outcome.err
}

func (f *cleanupLoomFake) submittedJobs() []CleanupJobRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]CleanupJobRequest, len(f.submitted))
	copy(out, f.submitted)
	return out
}

func (f *cleanupLoomFake) waitPoll(t *testing.T) {
	t.Helper()
	select {
	case <-f.pollDoneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup job poll did not finish")
	}
}

func TestWorkerCleanupReturnsDispatchEvidenceWithoutTerminalCompletion(t *testing.T) {
	worker := cleanupWorker("worker-dispatch", 0, "bash", "docker")
	repo := &mockWorkerRepo{workers: []domain.Worker{worker}}
	releasePoll := make(chan struct{})
	loom := newCleanupLoomFake(cleanupLoomOutcome{wait: releasePoll, status: cleanupSuccessStatus()})
	orchestrator := NewWorkerCleanupOrchestrator(repo, nil, loom, &events.NoopPublisher{}, WorkerCleanupConfig{RequiredSoftware: []string{"bash", "docker"}}, zap.NewNop())

	exec, err := orchestrator.RequestCleanup(context.Background(), worker.PubKey, CleanupModeReclaimableOnly, "operator requested")
	if err != nil {
		t.Fatalf("RequestCleanup: %v", err)
	}
	if exec.LoomJobID == "" || exec.Status != "dispatched" || exec.CompletedAt != nil {
		t.Fatalf("expected dispatch evidence without terminal completion, got %#v", exec)
	}
	if jobs := loom.submittedJobs(); len(jobs) != 1 || jobs[0].WorkerPubkey != worker.PubKey {
		t.Fatalf("expected one targeted Loom cleanup job, got %#v", jobs)
	}
	close(releasePoll)
	loom.waitPoll(t)
	if exec.Status != "dispatched" || exec.CompletedAt != nil {
		t.Fatalf("returned dispatch snapshot should not be mutated by async completion, got %#v", exec)
	}
}

func TestWorkerCleanupQueueFullDoesNotBurnCooldown(t *testing.T) {
	worker := cleanupWorker("worker-queue", 0, "bash", "docker")
	repo := &mockWorkerRepo{workers: []domain.Worker{worker}}
	failed := false
	queueFull := &CleanupJobStatus{Status: "failed", Success: &failed, Error: "queue is full: max_concurrent_jobs reached"}
	loom := newCleanupLoomFake(
		cleanupLoomOutcome{status: queueFull},
		cleanupLoomOutcome{status: cleanupSuccessStatus()},
	)
	orchestrator := NewWorkerCleanupOrchestrator(repo, nil, loom, &events.NoopPublisher{}, WorkerCleanupConfig{Cooldown: time.Hour, RequiredSoftware: []string{"bash", "docker"}}, zap.NewNop())

	first, err := orchestrator.RequestCleanup(context.Background(), worker.PubKey, CleanupModeReclaimableOnly, "pressure")
	if err != nil {
		t.Fatalf("first RequestCleanup: %v", err)
	}
	if first.LoomJobID == "" {
		t.Fatalf("first cleanup missing loom job id: %#v", first)
	}
	loom.waitPoll(t)

	second, err := orchestrator.RequestCleanup(context.Background(), worker.PubKey, CleanupModeReclaimableOnly, "retry after queue clears")
	if err != nil {
		t.Fatalf("second RequestCleanup should bypass cooldown after queue-full rejection: %v", err)
	}
	if second.LoomJobID == first.LoomJobID || second.LoomJobID == "" {
		t.Fatalf("expected second dispatch with a new loom job id, first=%q second=%q", first.LoomJobID, second.LoomJobID)
	}
}

func TestWorkerCleanupEnforcesAdmissionBeforeDispatch(t *testing.T) {
	worker := cleanupWorker("worker-disabled", 0, "bash", "docker")
	worker.SchedulingState = domain.WorkerSchedulingDisabled
	repo := &mockWorkerRepo{workers: []domain.Worker{worker}}
	loom := newCleanupLoomFake()
	orchestrator := NewWorkerCleanupOrchestrator(repo, nil, loom, &events.NoopPublisher{}, WorkerCleanupConfig{RequiredSoftware: []string{"bash", "docker"}}, zap.NewNop())

	_, err := orchestrator.RequestCleanup(context.Background(), worker.PubKey, CleanupModeReclaimableOnly, "operator")
	if !errors.Is(err, ErrWorkerCleanupAdmissionRejected) || !strings.Contains(err.Error(), "worker_scheduling") {
		t.Fatalf("expected cleanup admission rejection, got %v", err)
	}
	if jobs := loom.submittedJobs(); len(jobs) != 0 {
		t.Fatalf("admission rejection must happen before dispatch, got jobs %#v", jobs)
	}
}

func TestWorkerCleanupValidatesCapabilitiesBeforeDispatch(t *testing.T) {
	worker := cleanupWorker("worker-no-docker", 0, "bash")
	repo := &mockWorkerRepo{workers: []domain.Worker{worker}}
	loom := newCleanupLoomFake()
	orchestrator := NewWorkerCleanupOrchestrator(repo, nil, loom, &events.NoopPublisher{}, WorkerCleanupConfig{RequiredSoftware: []string{"bash", "docker"}}, zap.NewNop())

	_, err := orchestrator.RequestCleanup(context.Background(), worker.PubKey, CleanupModeReclaimableOnly, "operator")
	if !errors.Is(err, ErrWorkerCleanupCapabilityMissing) || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("expected missing docker capability rejection, got %v", err)
	}
	if jobs := loom.submittedJobs(); len(jobs) != 0 {
		t.Fatalf("capability rejection must happen before dispatch, got jobs %#v", jobs)
	}
}

func TestWorkerCleanupPaymentTokenRequiredForPricedWorkersAndPropagated(t *testing.T) {
	worker := cleanupWorker("worker-paid", 7, "bash", "docker")
	repo := &mockWorkerRepo{workers: []domain.Worker{worker}}
	withoutToken := newCleanupLoomFake()
	orchestrator := NewWorkerCleanupOrchestrator(repo, nil, withoutToken, &events.NoopPublisher{}, WorkerCleanupConfig{RequiredSoftware: []string{"bash", "docker"}}, zap.NewNop())

	_, err := orchestrator.RequestCleanup(context.Background(), worker.PubKey, CleanupModeReclaimableOnly, "operator")
	if !errors.Is(err, ErrWorkerCleanupPaymentRequired) {
		t.Fatalf("expected payment-token rejection for priced worker, got %v", err)
	}
	if jobs := withoutToken.submittedJobs(); len(jobs) != 0 {
		t.Fatalf("payment rejection must happen before dispatch, got jobs %#v", jobs)
	}

	withToken := newCleanupLoomFake(cleanupLoomOutcome{status: cleanupSuccessStatus()})
	orchestrator = NewWorkerCleanupOrchestrator(repo, nil, withToken, &events.NoopPublisher{}, WorkerCleanupConfig{PaymentToken: "cashu-internal-token", RequiredSoftware: []string{"bash", "docker"}}, zap.NewNop())
	if _, err := orchestrator.RequestCleanup(context.Background(), worker.PubKey, CleanupModeReclaimableOnly, "operator"); err != nil {
		t.Fatalf("RequestCleanup with payment token: %v", err)
	}
	jobs := withToken.submittedJobs()
	if len(jobs) != 1 || jobs[0].PaymentToken != "cashu-internal-token" {
		t.Fatalf("expected cleanup payment token to be propagated, got %#v", jobs)
	}
}

func cleanupWorker(pubkey string, price int, software ...string) domain.Worker {
	worker := makeWorker(pubkey, pubkey, 0, price, "", "linux/amd64", software...)
	worker.SchedulingState = domain.WorkerSchedulingActive
	worker.Pressure = &domain.WorkerPressureAssessment{CapacityClass: domain.WorkerCapacityOpen, OverallLevel: domain.WorkerPressureNominal, RecommendedAction: domain.WorkerPressureActionNone, AssessedAt: time.Now().UTC()}
	return worker
}

func cleanupSuccessStatus() *CleanupJobStatus {
	success := true
	return &CleanupJobStatus{Status: "completed", Success: &success}
}
