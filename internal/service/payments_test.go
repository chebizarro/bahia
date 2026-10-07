package service

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// --- Mock repositories for payment service tests ---

type mockPaymentRepo struct {
	records  map[uuid.UUID]*domain.PaymentRecord
	byHash   map[string]*domain.PaymentRecord
	byRun    map[uuid.UUID][]domain.PaymentRecord
	byWorker map[string][]domain.PaymentRecord
}

func newMockPaymentRepo() *mockPaymentRepo {
	return &mockPaymentRepo{
		records:  make(map[uuid.UUID]*domain.PaymentRecord),
		byHash:   make(map[string]*domain.PaymentRecord),
		byRun:    make(map[uuid.UUID][]domain.PaymentRecord),
		byWorker: make(map[string][]domain.PaymentRecord),
	}
}

func (m *mockPaymentRepo) Create(_ context.Context, rec *domain.PaymentRecord) error {
	if rec.ID == uuid.Nil {
		rec.ID = uuid.New()
	}
	m.records[rec.ID] = rec
	if rec.TokenHash != "" {
		m.byHash[rec.TokenHash] = rec
	}
	m.byRun[rec.DeploymentRunID] = append(m.byRun[rec.DeploymentRunID], *rec)
	m.byWorker[rec.WorkerPubkey] = append(m.byWorker[rec.WorkerPubkey], *rec)
	return nil
}

func (m *mockPaymentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.PaymentRecord, error) {
	rec, ok := m.records[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return rec, nil
}

func (m *mockPaymentRepo) ListByRun(_ context.Context, runID uuid.UUID) ([]domain.PaymentRecord, error) {
	return m.byRun[runID], nil
}

func (m *mockPaymentRepo) ListByWorker(_ context.Context, workerPubkey string, limit int) ([]domain.PaymentRecord, error) {
	recs := m.byWorker[workerPubkey]
	if limit > 0 && len(recs) > limit {
		recs = recs[:limit]
	}
	return recs, nil
}

func (m *mockPaymentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.PaymentStatus, errMsg string) error {
	rec, ok := m.records[id]
	if !ok {
		return repository.ErrNotFound
	}
	rec.Status = status
	rec.ErrorMessage = errMsg
	return nil
}

func (m *mockPaymentRepo) GetByTokenHash(_ context.Context, tokenHash string) (*domain.PaymentRecord, error) {
	rec, ok := m.byHash[tokenHash]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return rec, nil
}

type mockWorkerRepoForPayments struct {
	workers map[string]*domain.Worker
}

func (m *mockWorkerRepoForPayments) Upsert(_ context.Context, w *domain.Worker) error {
	m.workers[w.PubKey] = w
	return nil
}

func (m *mockWorkerRepoForPayments) GetByPubKey(_ context.Context, pubkey string) (*domain.Worker, error) {
	w, ok := m.workers[pubkey]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return w, nil
}

func (m *mockWorkerRepoForPayments) List(_ context.Context, _ string, _ int) ([]domain.Worker, error) {
	return nil, nil
}

func (m *mockWorkerRepoForPayments) UpdateStatus(_ context.Context, _ string, _ domain.WorkerStatus) error {
	return nil
}

// --- Tests ---

// paymentOpLog records the order in which a test's canonical publisher and
// SQL index were written, so publish-first ordering can be asserted.
type paymentOpLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *paymentOpLog) add(op string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, op)
}

func (l *paymentOpLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ops...)
}

// mockPaymentCanonical is an in-memory canonical store: one record per payment
// coordinate, replaced on every accepted publish.
type mockPaymentCanonical struct {
	mu       sync.Mutex
	records  map[uuid.UUID]domain.PaymentRecord
	attempts []uuid.UUID
	err      error
	log      *paymentOpLog
}

func newMockPaymentCanonical() *mockPaymentCanonical {
	return &mockPaymentCanonical{records: make(map[uuid.UUID]domain.PaymentRecord)}
}

func (m *mockPaymentCanonical) PublishPaymentRecord(_ context.Context, rec *domain.PaymentRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts = append(m.attempts, rec.ID)
	if m.err != nil {
		return m.err
	}
	m.records[rec.ID] = *rec
	m.log.add("publish:" + string(rec.Status))
	return nil
}

func (m *mockPaymentCanonical) ListPaymentRecords(context.Context) ([]domain.PaymentRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.PaymentRecord, 0, len(m.records))
	for _, rec := range m.records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (m *mockPaymentCanonical) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *mockPaymentCanonical) record(id uuid.UUID) (domain.PaymentRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[id]
	return rec, ok
}

func (m *mockPaymentCanonical) counts() (coordinates, attempts int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records), len(m.attempts)
}

func newTestPaymentService() (*PaymentService, *mockPaymentRepo, *mockRunRepo, *mockWorkerRepoForPayments) {
	paymentRepo := newMockPaymentRepo()
	runRepo := newMockRunRepo()
	workerRepo := &mockWorkerRepoForPayments{workers: make(map[string]*domain.Worker)}
	svc := NewPaymentService(paymentRepo, zap.NewNop())
	canonical := newMockPaymentCanonical()
	svc.SetCPStatePublisher(canonical)
	svc.SetCanonicalView(canonical)
	return svc, paymentRepo, runRepo, workerRepo
}

func TestPaymentService_RecordPayment(t *testing.T) {
	svc, _, _, _ := newTestPaymentService()

	runID := uuid.New()
	rec, err := svc.RecordPayment(context.Background(), runID, "worker1", "https://mint.example.com", 1000, "cashu-token-data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.AmountSats != 1000 {
		t.Errorf("amount = %d, want 1000", rec.AmountSats)
	}
	if rec.Direction != domain.PaymentDirectionPayment {
		t.Errorf("direction = %q, want payment", rec.Direction)
	}
	if rec.Status != domain.PaymentStatusPending {
		t.Errorf("status = %q, want pending", rec.Status)
	}
	if rec.TokenHash == "" {
		t.Error("expected token hash to be set")
	}
}

func TestPaymentService_RecordPayment_Idempotent(t *testing.T) {
	svc, _, _, _ := newTestPaymentService()

	runID := uuid.New()
	rec1, err := svc.RecordPayment(context.Background(), runID, "worker1", "https://mint.example.com", 1000, "same-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec2, err := svc.RecordPayment(context.Background(), runID, "worker1", "https://mint.example.com", 1000, "same-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec1.ID != rec2.ID {
		t.Error("expected same payment record for same token (idempotent)")
	}
}

func TestPaymentService_RecordChange(t *testing.T) {
	svc, _, _, _ := newTestPaymentService()

	runID := uuid.New()
	rec, err := svc.RecordChange(context.Background(), runID, "worker1", "https://mint.example.com", 200, "change-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Direction != domain.PaymentDirectionChange {
		t.Errorf("direction = %q, want change", rec.Direction)
	}
	if rec.Status != domain.PaymentStatusRedeemed {
		t.Errorf("status = %q, want redeemed", rec.Status)
	}
}

func TestPaymentService_GetRunCostSummary(t *testing.T) {
	svc, _, _, _ := newTestPaymentService()
	runID := uuid.New()

	// Record a payment and change.
	_, err := svc.RecordPayment(context.Background(), runID, "w1", "https://mint.example.com", 1000, "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = svc.RecordChange(context.Background(), runID, "w1", "https://mint.example.com", 300, "t2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	summary, err := svc.GetRunCostSummary(context.Background(), runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.TotalPaid != 1000 {
		t.Errorf("total_paid = %d, want 1000", summary.TotalPaid)
	}
	if summary.TotalChange != 300 {
		t.Errorf("total_change = %d, want 300", summary.TotalChange)
	}
	if summary.NetCost != 700 {
		t.Errorf("net_cost = %d, want 700", summary.NetCost)
	}
	if summary.PaymentCount != 1 {
		t.Errorf("payment_count = %d, want 1", summary.PaymentCount)
	}
	if summary.ChangeCount != 1 {
		t.Errorf("change_count = %d, want 1", summary.ChangeCount)
	}
}

func TestPaymentService_MarkPaymentSent(t *testing.T) {
	svc, paymentRepo, _, _ := newTestPaymentService()

	runID := uuid.New()
	rec, _ := svc.RecordPayment(context.Background(), runID, "w1", "https://mint.example.com", 500, "tok1")

	err := svc.MarkPaymentSent(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, _ := paymentRepo.GetByID(context.Background(), rec.ID)
	if updated.Status != domain.PaymentStatusSent {
		t.Errorf("status = %q, want sent", updated.Status)
	}
}

func TestHashToken(t *testing.T) {
	h1 := hashToken("same-data")
	h2 := hashToken("same-data")
	h3 := hashToken("different-data")

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
	if len(h1) != 64 { // SH hex
		t.Errorf("hash length = %d, want 64", len(h1))
	}
}
