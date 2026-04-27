// Package service provides domain services.
package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// WorkerDiscovery defines the interface for worker discovery.
// Implemented by loom.WorkerDiscovery to avoid import cycles.
type WorkerDiscovery interface {
	Start(ctx context.Context) error
	Stop()
}

// CashuWallet defines the interface for a Cashu wallet.
// Implemented by cashu.Wallet to avoid import cycles.
type CashuWallet interface {
	Initialize(ctx context.Context) error
	GetBalance(mintURL string) int64
	GetAllBalances() map[string]int64
	HasMint(mintURL string) bool
	EstimateCost(pricePerSecond int, estimatedDurationSecs int, minDuration int) int64
	CreatePaymentToken(ctx context.Context, mintURL string, amount int64, recipientPubkey string) (string, error)
}

// PaymentRepo defines the payment repository interface used by WorkerCatalogService.
type PaymentRepo interface {
	Create(ctx context.Context, rec *domain.PaymentRecord) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.PaymentStatus, errMsg string) error
	GetTotalPaidToWorker(ctx context.Context, workerPubkey string) (int64, error)
}

// WorkerCatalogService manages the worker catalog with discovery, selection, and payments.
type WorkerCatalogService struct {
	discovery    WorkerDiscovery
	workerRepo   repository.WorkerRepository
	paymentRepo  PaymentRepo
	policyService *WorkerPolicyService
	wallet       CashuWallet
	jobStats     *JobStatsTracker
	logger       *zap.Logger

	mu            sync.RWMutex
	onlineWorkers map[string]*domain.Worker // pubkey -> worker cache
}

// NewWorkerCatalogService creates a new WorkerCatalogService.
func NewWorkerCatalogService(
	discovery WorkerDiscovery,
	workerRepo repository.WorkerRepository,
	paymentRepo PaymentRepo,
	policyService *WorkerPolicyService,
	wallet CashuWallet,
	logger *zap.Logger,
) *WorkerCatalogService {
	return &WorkerCatalogService{
		discovery:     discovery,
		workerRepo:    workerRepo,
		paymentRepo:   paymentRepo,
		policyService: policyService,
		wallet:        wallet,
		jobStats:      NewJobStatsTracker(100), // Track last 100 jobs per worker
		logger:        logger,
		onlineWorkers: make(map[string]*domain.Worker),
	}
}

// Start begins worker discovery and catalog management.
func (s *WorkerCatalogService) Start(ctx context.Context) error {
	// Initialize wallet
	if s.wallet != nil {
		if err := s.wallet.Initialize(ctx); err != nil {
			s.logger.Warn("cashu wallet initialization failed", zap.Error(err))
		}
	}

	// Start discovery
	if s.discovery != nil {
		if err := s.discovery.Start(ctx); err != nil {
			return fmt.Errorf("starting worker discovery: %w", err)
		}
	}

	// Initial cache load
	s.refreshCache(ctx)

	// Start cache refresh loop
	go s.cacheRefreshLoop(ctx)

	s.logger.Info("worker catalog service started")
	return nil
}

// Stop halts the worker catalog service.
func (s *WorkerCatalogService) Stop() {
	if s.discovery != nil {
		s.discovery.Stop()
	}
}

// cacheRefreshLoop periodically refreshes the online workers cache.
func (s *WorkerCatalogService) cacheRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshCache(ctx)
		}
	}
}

// refreshCache updates the in-memory cache of online workers.
func (s *WorkerCatalogService) refreshCache(ctx context.Context) {
	workers, err := s.workerRepo.List(ctx, string(domain.WorkerStatusOnline), 100)
	if err != nil {
		s.logger.Error("failed to refresh worker cache", zap.Error(err))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.onlineWorkers = make(map[string]*domain.Worker)
	for i := range workers {
		s.onlineWorkers[workers[i].PubKey] = &workers[i]
	}

	s.logger.Debug("worker cache refreshed", zap.Int("online", len(s.onlineWorkers)))
}

// GetOnlineWorkers returns a list of currently online workers.
func (s *WorkerCatalogService) GetOnlineWorkers() []*domain.Worker {
	s.mu.RLock()
	defer s.mu.RUnlock()

	workers := make([]*domain.Worker, 0, len(s.onlineWorkers))
	for _, w := range s.onlineWorkers {
		workers = append(workers, w)
	}
	return workers
}

// GetWorker retrieves a worker by pubkey.
func (s *WorkerCatalogService) GetWorker(ctx context.Context, pubkey string) (*domain.Worker, error) {
	// Check cache first
	s.mu.RLock()
	if w, ok := s.onlineWorkers[pubkey]; ok {
		s.mu.RUnlock()
		return w, nil
	}
	s.mu.RUnlock()

	// Fall back to database
	return s.workerRepo.GetByPubKey(ctx, pubkey)
}

// SelectWorkerForEnvironment selects the best worker for an environment based on policy.
func (s *WorkerCatalogService) SelectWorkerForEnvironment(ctx context.Context, env *domain.Environment) (*ScoredWorker, error) {
	return s.policyService.SelectWorker(ctx, env)
}

// RankWorkersForEnvironment ranks all workers for an environment.
func (s *WorkerCatalogService) RankWorkersForEnvironment(ctx context.Context, env *domain.Environment) ([]ScoredWorker, error) {
	return s.policyService.RankWorkers(ctx, env)
}

// PreparePayment creates a payment token for a worker.
func (s *WorkerCatalogService) PreparePayment(
	ctx context.Context,
	worker *domain.Worker,
	estimatedDurationSecs int,
) (*PaymentPrepResult, error) {
	if s.wallet == nil {
		return nil, fmt.Errorf("cashu wallet not configured")
	}

	if len(worker.Pricing) == 0 {
		return nil, fmt.Errorf("worker has no pricing information")
	}

	// Find a compatible mint
	var selectedPricing *domain.WorkerPricing
	for i, p := range worker.Pricing {
		if s.wallet.HasMint(p.MintURL) {
			selectedPricing = &worker.Pricing[i]
			break
		}
	}
	if selectedPricing == nil {
		return nil, fmt.Errorf("no compatible mint found (worker mints: %v)", extractMintURLs(worker.Pricing))
	}

	// Calculate cost
	cost := s.wallet.EstimateCost(
		selectedPricing.PricePerSecond,
		estimatedDurationSecs,
		worker.MinDurationSecs,
	)

	// Create payment token
	token, err := s.wallet.CreatePaymentToken(ctx, selectedPricing.MintURL, cost, worker.PubKey)
	if err != nil {
		return nil, fmt.Errorf("creating payment token: %w", err)
	}

	return &PaymentPrepResult{
		WorkerPubkey:  worker.PubKey,
		MintURL:       selectedPricing.MintURL,
		AmountSats:    cost,
		Token:         token,
		PricePerSec:   selectedPricing.PricePerSecond,
		EstimatedSecs: estimatedDurationSecs,
		Unit:          selectedPricing.Unit,
	}, nil
}

// PaymentPrepResult contains the result of payment preparation.
type PaymentPrepResult struct {
	WorkerPubkey  string
	MintURL       string
	AmountSats    int64
	Token         string
	PricePerSec   int
	EstimatedSecs int
	Unit          string
}

// RecordPayment records a payment in the database.
func (s *WorkerCatalogService) RecordPayment(ctx context.Context, payment *domain.PaymentRecord) error {
	return s.paymentRepo.Create(ctx, payment)
}

// UpdatePaymentStatus updates the status of a payment.
func (s *WorkerCatalogService) UpdatePaymentStatus(ctx context.Context, paymentID uuid.UUID, status domain.PaymentStatus, errorMsg string) error {
	return s.paymentRepo.UpdateStatus(ctx, paymentID, status, errorMsg)
}

// GetWorkerStats returns statistics about a worker.
func (s *WorkerCatalogService) GetWorkerStats(ctx context.Context, pubkey string) (*WorkerStats, error) {
	worker, err := s.workerRepo.GetByPubKey(ctx, pubkey)
	if err != nil {
		return nil, err
	}
	if worker == nil {
		return nil, fmt.Errorf("worker not found: %s", pubkey)
	}

	totalPaid, err := s.paymentRepo.GetTotalPaidToWorker(ctx, pubkey)
	if err != nil {
		totalPaid = 0 // Non-fatal
	}

	// Get job completion stats from tracker
	jobStats := s.jobStats.GetStats(pubkey)

	return &WorkerStats{
		Worker:        worker,
		TotalPaidSats: totalPaid,
		JobsCompleted: jobStats.TotalCompleted,
		JobsFailed:    jobStats.TotalFailed,
		AvgDurationMs: jobStats.AvgDurationMs,
		SuccessRate:   s.jobStats.SuccessRate(pubkey),
	}, nil
}

// RecordJobCompletion records a job completion for a worker.
// This should be called by the workflow coordinator when jobs complete.
func (s *WorkerCatalogService) RecordJobCompletion(workerPubkey string, durationMs int64, success bool) {
	s.jobStats.RecordJobCompletion(workerPubkey, durationMs, success)
	s.logger.Debug("recorded job completion",
		zap.String("worker", workerPubkey),
		zap.Int64("duration_ms", durationMs),
		zap.Bool("success", success),
	)
}

// GetJobStatsTracker returns the job stats tracker for direct access.
// This is useful for the worker policy service to factor in job history.
func (s *WorkerCatalogService) GetJobStatsTracker() *JobStatsTracker {
	return s.jobStats
}

// WorkerStats contains statistics about a worker.
type WorkerStats struct {
	Worker         *domain.Worker
	TotalPaidSats  int64
	JobsCompleted  int64
	JobsFailed     int64
	AvgDurationMs  int64
	SuccessRate    float64
}

// GetWalletBalance returns the current wallet balance for a mint.
func (s *WorkerCatalogService) GetWalletBalance(mintURL string) int64 {
	if s.wallet == nil {
		return 0
	}
	return s.wallet.GetBalance(mintURL)
}

// GetAllWalletBalances returns balances for all configured mints.
func (s *WorkerCatalogService) GetAllWalletBalances() map[string]int64 {
	if s.wallet == nil {
		return nil
	}
	return s.wallet.GetAllBalances()
}

// extractMintURLs extracts mint URLs from pricing info.
func extractMintURLs(pricing []domain.WorkerPricing) []string {
	urls := make([]string, len(pricing))
	for i, p := range pricing {
		urls[i] = p.MintURL
	}
	return urls
}
