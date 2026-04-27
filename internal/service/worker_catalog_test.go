package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// mockCashuWallet implements CashuWallet for testing.
type mockCashuWallet struct {
	balances map[string]int64
	proofs   map[string][]mockProof
}

type mockProof struct {
	ID     string
	Amount int64
}

func newMockCashuWallet() *mockCashuWallet {
	return &mockCashuWallet{
		balances: make(map[string]int64),
		proofs:   make(map[string][]mockProof),
	}
}

func (w *mockCashuWallet) Initialize(ctx context.Context) error { return nil }

func (w *mockCashuWallet) GetBalance(mintURL string) int64 { return w.balances[mintURL] }

func (w *mockCashuWallet) GetAllBalances() map[string]int64 {
	result := make(map[string]int64)
	for k, v := range w.balances {
		result[k] = v
	}
	return result
}

func (w *mockCashuWallet) HasMint(mintURL string) bool {
	_, ok := w.balances[mintURL]
	return ok
}

func (w *mockCashuWallet) EstimateCost(pricePerSecond int, estimatedDurationSecs int, minDuration int) int64 {
	duration := estimatedDurationSecs
	if duration < minDuration {
		duration = minDuration
	}
	return int64(pricePerSecond * duration)
}

func (w *mockCashuWallet) CreatePaymentToken(ctx context.Context, mintURL string, amount int64, recipientPubkey string) (string, error) {
	if w.balances[mintURL] < amount {
		return "", context.DeadlineExceeded // just for error
	}
	w.balances[mintURL] -= amount
	return "cashuA_test_token", nil
}

func (w *mockCashuWallet) AddBalance(mintURL string, amount int64) {
	w.balances[mintURL] += amount
}

// catalogMockWorkerRepo implements repository.WorkerRepository for testing.
type catalogMockWorkerRepo struct {
	workers map[string]*domain.Worker
}

func newCatalogMockWorkerRepo() *catalogMockWorkerRepo {
	return &catalogMockWorkerRepo{workers: make(map[string]*domain.Worker)}
}

func (r *catalogMockWorkerRepo) Upsert(ctx context.Context, w *domain.Worker) error {
	r.workers[w.PubKey] = w
	return nil
}

func (r *catalogMockWorkerRepo) GetByPubKey(ctx context.Context, pubkey string) (*domain.Worker, error) {
	return r.workers[pubkey], nil
}

func (r *catalogMockWorkerRepo) List(ctx context.Context, status string, limit int) ([]domain.Worker, error) {
	var result []domain.Worker
	for _, w := range r.workers {
		if status == "" || string(w.Status) == status {
			result = append(result, *w)
		}
	}
	return result, nil
}

func (r *catalogMockWorkerRepo) UpdateStatus(ctx context.Context, pubkey string, status domain.WorkerStatus) error {
	if w, ok := r.workers[pubkey]; ok {
		w.Status = status
	}
	return nil
}

// catalogMockPaymentRepo implements PaymentRepo for testing.
type catalogMockPaymentRepo struct {
	payments map[uuid.UUID]*domain.PaymentRecord
}

func newCatalogMockPaymentRepo() *catalogMockPaymentRepo {
	return &catalogMockPaymentRepo{payments: make(map[uuid.UUID]*domain.PaymentRecord)}
}

func (r *catalogMockPaymentRepo) Create(ctx context.Context, p *domain.PaymentRecord) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	r.payments[p.ID] = p
	return nil
}

func (r *catalogMockPaymentRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.PaymentStatus, errorMsg string) error {
	if p, ok := r.payments[id]; ok {
		p.Status = status
		p.ErrorMessage = errorMsg
	}
	return nil
}

func (r *catalogMockPaymentRepo) GetTotalPaidToWorker(ctx context.Context, workerPubkey string) (int64, error) {
	var total int64
	for _, p := range r.payments {
		if p.WorkerPubkey == workerPubkey && p.Direction == domain.PaymentDirectionPayment {
			total += p.AmountSats
		}
	}
	return total, nil
}

func TestWorkerCatalogService_GetOnlineWorkers(t *testing.T) {
	workerRepo := newCatalogMockWorkerRepo()
	paymentRepo := newCatalogMockPaymentRepo()
	logger := zap.NewNop()

	// Add workers
	workerRepo.Upsert(context.Background(), &domain.Worker{
		PubKey: "worker1",
		Name:   "Worker 1",
		Status: domain.WorkerStatusOnline,
	})
	workerRepo.Upsert(context.Background(), &domain.Worker{
		PubKey: "worker2",
		Name:   "Worker 2",
		Status: domain.WorkerStatusOnline,
	})
	workerRepo.Upsert(context.Background(), &domain.Worker{
		PubKey: "worker3",
		Name:   "Worker 3",
		Status: domain.WorkerStatusOffline,
	})

	policySvc := NewWorkerPolicyService(workerRepo, logger)
	svc := NewWorkerCatalogService(nil, workerRepo, paymentRepo, policySvc, nil, logger)

	// Refresh cache manually
	svc.refreshCache(context.Background())

	workers := svc.GetOnlineWorkers()
	if len(workers) != 2 {
		t.Errorf("GetOnlineWorkers() returned %d workers, want 2", len(workers))
	}
}

func TestWorkerCatalogService_GetWorker(t *testing.T) {
	workerRepo := newCatalogMockWorkerRepo()
	paymentRepo := newCatalogMockPaymentRepo()
	logger := zap.NewNop()

	workerRepo.Upsert(context.Background(), &domain.Worker{
		PubKey: "worker1",
		Name:   "Worker 1",
		Status: domain.WorkerStatusOnline,
	})

	policySvc := NewWorkerPolicyService(workerRepo, logger)
	svc := NewWorkerCatalogService(nil, workerRepo, paymentRepo, policySvc, nil, logger)
	svc.refreshCache(context.Background())

	ctx := context.Background()

	// Get cached worker
	w, err := svc.GetWorker(ctx, "worker1")
	if err != nil {
		t.Fatalf("GetWorker() error = %v", err)
	}
	if w == nil || w.Name != "Worker 1" {
		t.Error("GetWorker() did not return expected worker")
	}

	// Get uncached worker (falls back to db)
	w2, err := svc.GetWorker(ctx, "worker1")
	if err != nil {
		t.Fatalf("GetWorker() error = %v", err)
	}
	if w2 == nil {
		t.Error("GetWorker() should fall back to database")
	}
}

func TestWorkerCatalogService_PreparePayment(t *testing.T) {
	workerRepo := newCatalogMockWorkerRepo()
	paymentRepo := newCatalogMockPaymentRepo()
	logger := zap.NewNop()

	wallet := newMockCashuWallet()
	wallet.AddBalance("https://mint.example.com", 1000)

	policySvc := NewWorkerPolicyService(workerRepo, logger)
	svc := NewWorkerCatalogService(nil, workerRepo, paymentRepo, policySvc, wallet, logger)

	worker := &domain.Worker{
		PubKey:          "worker123",
		Name:            "Test Worker",
		MinDurationSecs: 10,
		Pricing: []domain.WorkerPricing{
			{MintURL: "https://mint.example.com", PricePerSecond: 10, Unit: "sat"},
		},
	}

	ctx := context.Background()
	result, err := svc.PreparePayment(ctx, worker, 60)
	if err != nil {
		t.Fatalf("PreparePayment() error = %v", err)
	}

	if result.WorkerPubkey != "worker123" {
		t.Errorf("WorkerPubkey = %s, want worker123", result.WorkerPubkey)
	}
	if result.AmountSats != 600 { // 10 sat/sec * 60 sec
		t.Errorf("AmountSats = %d, want 600", result.AmountSats)
	}
	if result.Token == "" {
		t.Error("Token should not be empty")
	}
}

func TestWorkerCatalogService_PreparePayment_NoCompatibleMint(t *testing.T) {
	workerRepo := newCatalogMockWorkerRepo()
	paymentRepo := newCatalogMockPaymentRepo()
	logger := zap.NewNop()

	wallet := newMockCashuWallet()
	wallet.AddBalance("https://our-mint.example.com", 1000)

	policySvc := NewWorkerPolicyService(workerRepo, logger)
	svc := NewWorkerCatalogService(nil, workerRepo, paymentRepo, policySvc, wallet, logger)

	worker := &domain.Worker{
		PubKey: "worker123",
		Pricing: []domain.WorkerPricing{
			{MintURL: "https://different-mint.example.com", PricePerSecond: 10, Unit: "sat"},
		},
	}

	ctx := context.Background()
	_, err := svc.PreparePayment(ctx, worker, 60)
	if err == nil {
		t.Error("PreparePayment() should error with no compatible mint")
	}
}

func TestWorkerCatalogService_PreparePayment_NoPricing(t *testing.T) {
	workerRepo := newCatalogMockWorkerRepo()
	paymentRepo := newCatalogMockPaymentRepo()
	logger := zap.NewNop()

	wallet := newMockCashuWallet()

	policySvc := NewWorkerPolicyService(workerRepo, logger)
	svc := NewWorkerCatalogService(nil, workerRepo, paymentRepo, policySvc, wallet, logger)

	worker := &domain.Worker{
		PubKey:  "worker123",
		Pricing: nil, // no pricing
	}

	ctx := context.Background()
	_, err := svc.PreparePayment(ctx, worker, 60)
	if err == nil {
		t.Error("PreparePayment() should error with no pricing")
	}
}

func TestWorkerCatalogService_GetWorkerStats(t *testing.T) {
	workerRepo := newCatalogMockWorkerRepo()
	paymentRepo := newCatalogMockPaymentRepo()
	logger := zap.NewNop()

	workerRepo.Upsert(context.Background(), &domain.Worker{
		PubKey: "worker1",
		Name:   "Worker 1",
		Status: domain.WorkerStatusOnline,
	})

	// Add some payments
	paymentRepo.Create(context.Background(), &domain.PaymentRecord{
		WorkerPubkey: "worker1",
		AmountSats:   100,
		Direction:    domain.PaymentDirectionPayment,
		Status:       domain.PaymentStatusSent,
	})
	paymentRepo.Create(context.Background(), &domain.PaymentRecord{
		WorkerPubkey: "worker1",
		AmountSats:   200,
		Direction:    domain.PaymentDirectionPayment,
		Status:       domain.PaymentStatusSent,
	})

	policySvc := NewWorkerPolicyService(workerRepo, logger)
	svc := NewWorkerCatalogService(nil, workerRepo, paymentRepo, policySvc, nil, logger)

	ctx := context.Background()
	stats, err := svc.GetWorkerStats(ctx, "worker1")
	if err != nil {
		t.Fatalf("GetWorkerStats() error = %v", err)
	}

	if stats.Worker.Name != "Worker 1" {
		t.Errorf("Worker.Name = %s, want Worker 1", stats.Worker.Name)
	}
	if stats.TotalPaidSats != 300 {
		t.Errorf("TotalPaidSats = %d, want 300", stats.TotalPaidSats)
	}
}

func TestWorkerCatalogService_WalletBalances(t *testing.T) {
	workerRepo := newCatalogMockWorkerRepo()
	paymentRepo := newCatalogMockPaymentRepo()
	logger := zap.NewNop()

	wallet := newMockCashuWallet()
	wallet.AddBalance("https://mint1.example.com", 500)
	wallet.AddBalance("https://mint2.example.com", 1000)

	policySvc := NewWorkerPolicyService(workerRepo, logger)
	svc := NewWorkerCatalogService(nil, workerRepo, paymentRepo, policySvc, wallet, logger)

	if got := svc.GetWalletBalance("https://mint1.example.com"); got != 500 {
		t.Errorf("GetWalletBalance(mint1) = %d, want 500", got)
	}

	balances := svc.GetAllWalletBalances()
	if balances["https://mint1.example.com"] != 500 {
		t.Errorf("mint1 balance = %d, want 500", balances["https://mint1.example.com"])
	}
	if balances["https://mint2.example.com"] != 1000 {
		t.Errorf("mint2 balance = %d, want 1000", balances["https://mint2.example.com"])
	}
}

func TestExtractMintURLs(t *testing.T) {
	pricing := []domain.WorkerPricing{
		{MintURL: "https://mint1.example.com", PricePerSecond: 10, Unit: "sat"},
		{MintURL: "https://mint2.example.com", PricePerSecond: 5, Unit: "sat"},
	}

	urls := extractMintURLs(pricing)
	if len(urls) != 2 {
		t.Errorf("extractMintURLs() returned %d URLs, want 2", len(urls))
	}
	if urls[0] != "https://mint1.example.com" || urls[1] != "https://mint2.example.com" {
		t.Errorf("extractMintURLs() = %v, want [mint1, mint2]", urls)
	}
}

func TestWorkerCatalogService_RecordPayment(t *testing.T) {
	workerRepo := newCatalogMockWorkerRepo()
	paymentRepo := newCatalogMockPaymentRepo()
	logger := zap.NewNop()

	policySvc := NewWorkerPolicyService(workerRepo, logger)
	svc := NewWorkerCatalogService(nil, workerRepo, paymentRepo, policySvc, nil, logger)

	ctx := context.Background()
	payment := &domain.PaymentRecord{
		DeploymentRunID: uuid.New(),
		WorkerPubkey:    "worker1",
		MintURL:         "https://mint.example.com",
		AmountSats:      100,
		Direction:       domain.PaymentDirectionPayment,
		Status:          domain.PaymentStatusPending,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if err := svc.RecordPayment(ctx, payment); err != nil {
		t.Fatalf("RecordPayment() error = %v", err)
	}

	if payment.ID == uuid.Nil {
		t.Error("Payment ID should be set after recording")
	}
}
