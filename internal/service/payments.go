// Package service implements the core business logic for the Bahia Deployment Registry.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// PaymentService manages Cashu payment lifecycle for deployment runs.
type PaymentService struct {
	payments repository.PaymentRecordRepository
	workers  repository.WorkerRepository
	runs     repository.DeploymentRunRepository
	logger   *zap.Logger
}

// NewPaymentService creates a new payment service.
func NewPaymentService(
	payments repository.PaymentRecordRepository,
	workers repository.WorkerRepository,
	runs repository.DeploymentRunRepository,
	logger *zap.Logger,
) *PaymentService {
	return &PaymentService{
		payments: payments,
		workers:  workers,
		runs:     runs,
		logger:   logger,
	}
}

// EstimateCost calculates the cost estimate for a deployment run based on
// the assigned worker's pricing and estimated duration.
func (s *PaymentService) EstimateCost(ctx context.Context, runID uuid.UUID, estimatedDurationSecs int) (*domain.CostEstimate, error) {
	run, err := s.runs.GetByID(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("getting deployment run: %w", err)
	}

	if run.WorkerPubkey == "" {
		return nil, fmt.Errorf("deployment run has no assigned worker")
	}

	worker, err := s.workers.GetByPubKey(ctx, run.WorkerPubkey)
	if err != nil {
		return nil, fmt.Errorf("getting worker: %w", err)
	}

	if len(worker.Pricing) == 0 {
		return nil, fmt.Errorf("worker %s has no pricing information", worker.PubKey)
	}

	// Use the first pricing entry (most common mint).
	pricing := worker.Pricing[0]

	// Default duration estimate from worker's max if not specified.
	if estimatedDurationSecs <= 0 {
		estimatedDurationSecs = worker.MaxDurationSecs
		if estimatedDurationSecs <= 0 {
			estimatedDurationSecs = 300 // 5 minute default
		}
	}

	estimate := domain.EstimateCost(pricing, estimatedDurationSecs)
	estimate.WorkerPubkey = worker.PubKey
	estimate.WorkerName = worker.Name

	return &estimate, nil
}

// RecordPayment creates a payment record for a deployment run.
// tokenData is the Cashu token being sent (hashed for storage).
func (s *PaymentService) RecordPayment(ctx context.Context, runID uuid.UUID, workerPubkey, mintURL string, amountSats int64, tokenData string) (*domain.PaymentRecord, error) {
	tokenHash := hashToken(tokenData)

	// Idempotency check: don't record the same token twice.
	if existing, err := s.payments.GetByTokenHash(ctx, tokenHash); err == nil && existing != nil {
		s.logger.Info("payment already recorded (idempotent)",
			zap.String("token_hash", tokenHash),
			zap.String("run_id", runID.String()),
		)
		return existing, nil
	}

	rec := &domain.PaymentRecord{
		DeploymentRunID: runID,
		WorkerPubkey:    workerPubkey,
		MintURL:         mintURL,
		AmountSats:      amountSats,
		TokenHash:       tokenHash,
		Direction:       domain.PaymentDirectionPayment,
		Status:          domain.PaymentStatusPending,
	}

	if err := s.payments.Create(ctx, rec); err != nil {
		return nil, fmt.Errorf("creating payment record: %w", err)
	}

	s.logger.Info("payment recorded",
		zap.String("payment_id", rec.ID.String()),
		zap.String("run_id", runID.String()),
		zap.Int64("amount_sats", amountSats),
	)
	return rec, nil
}

// MarkPaymentSent updates a payment record to sent status.
func (s *PaymentService) MarkPaymentSent(ctx context.Context, paymentID uuid.UUID) error {
	return s.payments.UpdateStatus(ctx, paymentID, domain.PaymentStatusSent, "")
}

// RecordChange records a change (refund) token received from a worker.
func (s *PaymentService) RecordChange(ctx context.Context, runID uuid.UUID, workerPubkey, mintURL string, amountSats int64, tokenData string) (*domain.PaymentRecord, error) {
	rec := &domain.PaymentRecord{
		DeploymentRunID: runID,
		WorkerPubkey:    workerPubkey,
		MintURL:         mintURL,
		AmountSats:      amountSats,
		TokenHash:       hashToken(tokenData),
		Direction:       domain.PaymentDirectionChange,
		Status:          domain.PaymentStatusRedeemed,
	}

	if err := s.payments.Create(ctx, rec); err != nil {
		return nil, fmt.Errorf("recording change: %w", err)
	}

	s.logger.Info("change recorded",
		zap.String("run_id", runID.String()),
		zap.Int64("change_sats", amountSats),
	)
	return rec, nil
}

// GetRunPayments returns all payment records for a deployment run.
func (s *PaymentService) GetRunPayments(ctx context.Context, runID uuid.UUID) ([]domain.PaymentRecord, error) {
	return s.payments.ListByRun(ctx, runID)
}

// GetRunCostSummary returns a summary of payments for a deployment run.
func (s *PaymentService) GetRunCostSummary(ctx context.Context, runID uuid.UUID) (*CostSummary, error) {
	records, err := s.payments.ListByRun(ctx, runID)
	if err != nil {
		return nil, err
	}

	summary := &CostSummary{}
	for _, rec := range records {
		switch rec.Direction {
		case domain.PaymentDirectionPayment:
			summary.TotalPaid += rec.AmountSats
			summary.PaymentCount++
		case domain.PaymentDirectionChange:
			summary.TotalChange += rec.AmountSats
			summary.ChangeCount++
		}
	}
	summary.NetCost = summary.TotalPaid - summary.TotalChange
	return summary, nil
}

// GetPaymentHistory returns payment records for a worker.
func (s *PaymentService) GetPaymentHistory(ctx context.Context, workerPubkey string, limit int) ([]domain.PaymentRecord, error) {
	return s.payments.ListByWorker(ctx, workerPubkey, limit)
}

// CostSummary aggregates payment data for a deployment run.
type CostSummary struct {
	TotalPaid    int64 `json:"total_paid_sats"`
	TotalChange  int64 `json:"total_change_sats"`
	NetCost      int64 `json:"net_cost_sats"`
	PaymentCount int   `json:"payment_count"`
	ChangeCount  int   `json:"change_count"`
}

// hashToken creates a SHA-256 hash of a Cashu token for storage.
func hashToken(tokenData string) string {
	h := sha256.Sum256([]byte(tokenData))
	return hex.EncodeToString(h[:])
}
