// Package service implements the core business logic for the Bahia Deployment Registry.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// PaymentCPStatePublisher admits signed, replaceable payment state to the durable outbox.
type PaymentCPStatePublisher interface {
	PublishPaymentRecord(context.Context, *domain.PaymentRecord) error
}

// PaymentCanonicalView reads the daemon's authored, retained local event store.
type PaymentCanonicalView interface {
	ListPaymentRecords(context.Context) ([]domain.PaymentRecord, error)
}

// PaymentService manages Cashu payment lifecycle for deployment runs.
type PaymentService struct {
	mu          sync.Mutex
	payments    repository.PaymentRecordRepository // optional, rebuildable SQL index
	cpPublisher PaymentCPStatePublisher
	canonical   PaymentCanonicalView
	logger      *zap.Logger
}

func NewPaymentService(payments repository.PaymentRecordRepository, logger *zap.Logger) *PaymentService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PaymentService{payments: payments, logger: logger}
}

// SetCPStatePublisher and SetCanonicalView must be called before mutations.
func (s *PaymentService) SetCPStatePublisher(pub PaymentCPStatePublisher) { s.cpPublisher = pub }
func (s *PaymentService) SetCanonicalView(view PaymentCanonicalView)      { s.canonical = view }

func (s *PaymentService) records(ctx context.Context) ([]domain.PaymentRecord, error) {
	if s.canonical == nil {
		return nil, fmt.Errorf("payment canonical local view is not configured")
	}
	return s.canonical.ListPaymentRecords(ctx)
}

func (s *PaymentService) recordToken(ctx context.Context, runID uuid.UUID, workerPubkey, mintURL string, amountSats int64, tokenData string, direction domain.PaymentDirection, status domain.PaymentStatus) (*domain.PaymentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.records(ctx)
	if err != nil {
		return nil, err
	}
	tokenHash := hashToken(tokenData)
	for i := range records {
		if records[i].TokenHash == tokenHash {
			return &records[i], nil
		}
	}
	now := time.Now().UTC()
	rec := &domain.PaymentRecord{ID: paymentRecordID(tokenHash), DeploymentRunID: runID, WorkerPubkey: workerPubkey, MintURL: mintURL, AmountSats: amountSats, TokenHash: tokenHash, Direction: direction, Status: status, CreatedAt: now, UpdatedAt: now}
	if err := s.publishCPState(ctx, rec); err != nil {
		return nil, err
	}
	if s.payments != nil {
		if err := s.payments.Create(ctx, rec); err != nil {
			s.logger.Warn("payment SQL index write failed; canonical state retained", zap.String("payment_id", rec.ID.String()), zap.Error(err))
		}
	}
	return rec, nil
}

// RecordPayment first publishes the signed canonical pending record.
func (s *PaymentService) RecordPayment(ctx context.Context, runID uuid.UUID, workerPubkey, mintURL string, amountSats int64, tokenData string) (*domain.PaymentRecord, error) {
	return s.recordToken(ctx, runID, workerPubkey, mintURL, amountSats, tokenData, domain.PaymentDirectionPayment, domain.PaymentStatusPending)
}

// MarkPaymentSent replaces the same canonical coordinate before updating SQL.
func (s *PaymentService) MarkPaymentSent(ctx context.Context, paymentID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.records(ctx)
	if err != nil {
		return err
	}
	for i := range records {
		if records[i].ID != paymentID {
			continue
		}
		if records[i].Status == domain.PaymentStatusSent {
			return nil
		}
		rec := records[i]
		rec.Status = domain.PaymentStatusSent
		rec.UpdatedAt = time.Now().UTC()
		if err := s.publishCPState(ctx, &rec); err != nil {
			return err
		}
		if s.payments != nil {
			if err := s.payments.UpdateStatus(ctx, paymentID, rec.Status, ""); err != nil {
				s.logger.Warn("payment SQL index status write failed; canonical state retained", zap.String("payment_id", paymentID.String()), zap.Error(err))
			}
		}
		return nil
	}
	return fmt.Errorf("payment %s: %w", paymentID, repository.ErrNotFound)
}

// RecordChange publishes the canonical redeemed change before indexing it.
func (s *PaymentService) RecordChange(ctx context.Context, runID uuid.UUID, workerPubkey, mintURL string, amountSats int64, tokenData string) (*domain.PaymentRecord, error) {
	return s.recordToken(ctx, runID, workerPubkey, mintURL, amountSats, tokenData, domain.PaymentDirectionChange, domain.PaymentStatusRedeemed)
}

func (s *PaymentService) GetRunPayments(ctx context.Context, runID uuid.UUID) ([]domain.PaymentRecord, error) {
	records, err := s.records(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PaymentRecord, 0)
	for _, rec := range records {
		if rec.DeploymentRunID == runID {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *PaymentService) GetRunCostSummary(ctx context.Context, runID uuid.UUID) (*CostSummary, error) {
	records, err := s.GetRunPayments(ctx, runID)
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

func (s *PaymentService) GetPaymentHistory(ctx context.Context, workerPubkey string, limit int) ([]domain.PaymentRecord, error) {
	records, err := s.records(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PaymentRecord, 0)
	for _, rec := range records {
		if rec.WorkerPubkey == workerPubkey {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// RebuildIndex replays retained canonical state into the optional SQL query index.
func (s *PaymentService) RebuildIndex(ctx context.Context) error {
	if s.payments == nil {
		return nil
	}
	records, err := s.records(ctx)
	if err != nil {
		return err
	}
	for i := range records {
		existing, err := s.payments.GetByID(ctx, records[i].ID)
		if err != nil && err != repository.ErrNotFound {
			return err
		}
		if existing == nil {
			rec := records[i]
			if err := s.payments.Create(ctx, &rec); err != nil {
				return err
			}
		} else if existing.Status != records[i].Status || existing.ErrorMessage != records[i].ErrorMessage {
			if err := s.payments.UpdateStatus(ctx, records[i].ID, records[i].Status, records[i].ErrorMessage); err != nil {
				return err
			}
		}
	}
	return nil
}

type CostSummary struct {
	TotalPaid    int64 `json:"total_paid_sats"`
	TotalChange  int64 `json:"total_change_sats"`
	NetCost      int64 `json:"net_cost_sats"`
	PaymentCount int   `json:"payment_count"`
	ChangeCount  int   `json:"change_count"`
}

func (s *PaymentService) publishCPState(ctx context.Context, rec *domain.PaymentRecord) error {
	if s.cpPublisher == nil {
		return fmt.Errorf("payment canonical publisher is not configured")
	}
	if err := s.cpPublisher.PublishPaymentRecord(ctx, rec); err != nil {
		return fmt.Errorf("publish payment canonical state: %w", err)
	}
	return nil
}

func hashToken(tokenData string) string {
	h := sha256.Sum256([]byte(tokenData))
	return hex.EncodeToString(h[:])
}

// paymentRecordID keeps the canonical coordinate stable when a caller retries
// after an unconfirmed publish. The token hash is already the payment's global
// idempotency key (and is unique in the SQL index), so deriving the UUID from it
// cannot merge two records that the legacy store would have accepted.
func paymentRecordID(tokenHash string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("bahia:payment:"+tokenHash))
}
