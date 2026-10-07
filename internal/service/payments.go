// Package service implements the core business logic for the Bahia Deployment Registry.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// PaymentCPStatePublisher admits a signed, replaceable payment record to the
// durable publish outbox. A nil error means the record is accepted or queued
// for per-relay retry; any error means it was not admitted and nothing derived
// from it may be written.
type PaymentCPStatePublisher interface {
	PublishPaymentRecord(context.Context, *domain.PaymentRecord) error
}

// PaymentCanonicalView reads the daemon's own retained payment records from the
// local event store: one record per payment, in its latest state.
type PaymentCanonicalView interface {
	ListPaymentRecords(context.Context) ([]domain.PaymentRecord, error)
}

// PaymentService manages Cashu payment lifecycle for deployment runs.
//
// Payment state is canonical on relays: every mutation mints its
// identity up front, publishes the signed cp-state record first, and only then
// updates the SQL repository, which is an optional index that RebuildIndex can
// recreate. Reads come from the local event store, so the service works with no
// SQL repository at all.
type PaymentService struct {
	// mu serializes mutations so the read-check-publish of one payment is not
	// interleaved with another writer of the same coordinate.
	mu          sync.Mutex
	payments    repository.PaymentRecordRepository // optional, rebuildable SQL index
	cpPublisher PaymentCPStatePublisher
	canonical   PaymentCanonicalView
	logger      *zap.Logger
}

// NewPaymentService creates a payment service. payments may be nil.
func NewPaymentService(payments repository.PaymentRecordRepository, logger *zap.Logger) *PaymentService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PaymentService{payments: payments, logger: logger}
}

// SetCPStatePublisher configures the canonical publisher. It must be called
// before any mutation; without it mutations fail instead of writing SQL only.
func (s *PaymentService) SetCPStatePublisher(pub PaymentCPStatePublisher) { s.cpPublisher = pub }

// SetCanonicalView configures the local event store view every read uses.
func (s *PaymentService) SetCanonicalView(view PaymentCanonicalView) { s.canonical = view }

// Ready reports whether the canonical publisher and view are configured.
func (s *PaymentService) Ready() error {
	switch {
	case s == nil:
		return errors.New("payment service is not configured")
	case s.cpPublisher == nil:
		return errors.New("payment canonical publisher is not configured")
	case s.canonical == nil:
		return errors.New("payment canonical local view is not configured")
	}
	return nil
}

func (s *PaymentService) records(ctx context.Context) ([]domain.PaymentRecord, error) {
	if s.canonical == nil {
		return nil, errors.New("payment canonical local view is not configured")
	}
	return s.canonical.ListPaymentRecords(ctx)
}

// recordToken records one token once. The payment id is derived from the token
// hash, so a retry after a failed or unconfirmed publish addresses the same
// coordinate instead of minting a second record.
func (s *PaymentService) recordToken(ctx context.Context, runID uuid.UUID, workerPubkey, mintURL string, amountSats int64, tokenData string, direction domain.PaymentDirection, status domain.PaymentStatus) (*domain.PaymentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.records(ctx)
	if err != nil {
		return nil, err
	}
	tokenHash := hashToken(tokenData)
	for i := range records {
		if records[i].TokenHash != tokenHash {
			continue
		}
		existing := records[i]
		if existing.Direction != direction {
			return nil, fmt.Errorf("token already recorded as %s %s: %w", existing.Direction, existing.ID, repository.ErrAlreadyExists)
		}
		s.logger.Info("payment token already recorded (idempotent)", zap.String("payment_id", existing.ID.String()), zap.String("run_id", runID.String()))
		s.indexRecord(ctx, existing)
		return &existing, nil
	}
	now := time.Now().UTC()
	rec := &domain.PaymentRecord{ID: paymentRecordID(tokenHash), DeploymentRunID: runID, WorkerPubkey: workerPubkey, MintURL: mintURL, AmountSats: amountSats, TokenHash: tokenHash, Direction: direction, Status: status, CreatedAt: now, UpdatedAt: now}
	if err := s.publishCPState(ctx, rec); err != nil {
		return nil, err
	}
	s.indexRecord(ctx, *rec)
	s.logger.Info("payment token recorded", zap.String("payment_id", rec.ID.String()), zap.String("run_id", runID.String()), zap.String("direction", string(direction)), zap.Int64("amount_sats", amountSats))
	return rec, nil
}

// RecordPayment records the Cashu token sent for a deployment run as a pending
// payment. tokenData is hashed; the token itself is never stored or published.
func (s *PaymentService) RecordPayment(ctx context.Context, runID uuid.UUID, workerPubkey, mintURL string, amountSats int64, tokenData string) (*domain.PaymentRecord, error) {
	return s.recordToken(ctx, runID, workerPubkey, mintURL, amountSats, tokenData, domain.PaymentDirectionPayment, domain.PaymentStatusPending)
}

// MarkPaymentSent moves a payment to sent. The transition replaces the
// payment's canonical record on the same coordinate before SQL is updated.
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
		rec := records[i]
		if rec.Status != domain.PaymentStatusSent {
			rec.Status = domain.PaymentStatusSent
			rec.ErrorMessage = ""
			rec.UpdatedAt = time.Now().UTC()
			if err := s.publishCPState(ctx, &rec); err != nil {
				return err
			}
		}
		s.indexRecord(ctx, rec)
		return nil
	}
	return fmt.Errorf("payment %s: %w", paymentID, repository.ErrNotFound)
}

// RecordChange records a change (refund) token received from a worker.
func (s *PaymentService) RecordChange(ctx context.Context, runID uuid.UUID, workerPubkey, mintURL string, amountSats int64, tokenData string) (*domain.PaymentRecord, error) {
	return s.recordToken(ctx, runID, workerPubkey, mintURL, amountSats, tokenData, domain.PaymentDirectionChange, domain.PaymentStatusRedeemed)
}

// GetRunPayments returns the payment records of a deployment run, oldest first.
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
	return out, nil
}

// GetRunCostSummary returns a summary of payments for a deployment run.
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

// GetPaymentHistory returns a worker's payment records, newest first.
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
	// records is oldest first with a stable id tie-break; reverse it.
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// RebuildIndex replays the retained canonical records into the optional SQL
// index. It is safe to repeat and never removes rows.
func (s *PaymentService) RebuildIndex(ctx context.Context) error {
	if s.payments == nil {
		return nil
	}
	records, err := s.records(ctx)
	if err != nil {
		return err
	}
	var failed []error
	for _, rec := range records {
		if err := s.writeIndex(ctx, rec); err != nil {
			failed = append(failed, fmt.Errorf("payment %s: %w", rec.ID, err))
		}
	}
	return errors.Join(failed...)
}

// indexRecord mirrors a canonical record into the SQL index. The index is
// derived, so a failure is logged and repaired by the next write of the record
// or by RebuildIndex; it never fails the mutation that already published.
func (s *PaymentService) indexRecord(ctx context.Context, rec domain.PaymentRecord) {
	if err := s.writeIndex(ctx, rec); err != nil {
		s.logger.Warn("payment SQL index write failed; canonical state retained", zap.String("payment_id", rec.ID.String()), zap.Error(err))
	}
}

func (s *PaymentService) writeIndex(ctx context.Context, rec domain.PaymentRecord) error {
	if s.payments == nil {
		return nil
	}
	existing, err := s.payments.GetByID(ctx, rec.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	if existing == nil {
		return s.payments.Create(ctx, &rec)
	}
	if existing.Status != rec.Status || existing.ErrorMessage != rec.ErrorMessage {
		return s.payments.UpdateStatus(ctx, rec.ID, rec.Status, rec.ErrorMessage)
	}
	return nil
}

// CostSummary aggregates payment data for a deployment run.
type CostSummary struct {
	TotalPaid    int64 `json:"total_paid_sats"`
	TotalChange  int64 `json:"total_change_sats"`
	NetCost      int64 `json:"net_cost_sats"`
	PaymentCount int   `json:"payment_count"`
	ChangeCount  int   `json:"change_count"`
}

func (s *PaymentService) publishCPState(ctx context.Context, rec *domain.PaymentRecord) error {
	if s.cpPublisher == nil {
		return errors.New("payment canonical publisher is not configured")
	}
	if err := s.cpPublisher.PublishPaymentRecord(ctx, rec); err != nil {
		return fmt.Errorf("publish payment canonical state: %w", err)
	}
	return nil
}

// hashToken creates a SH hash of a Cashu token for storage.
func hashToken(tokenData string) string {
	h := sha256.Sum256([]byte(tokenData))
	return hex.EncodeToString(h[:])
}

// paymentRecordID keeps the canonical coordinate stable when a caller retries
// after an unconfirmed publish. The token hash is already the payment's global
// idempotency key (and is unique in the SQL index), so deriving the UUID from it
// cannot merge two records that the compatibility store would have accepted.
func paymentRecordID(tokenHash string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("bahia:payment:"+tokenHash))
}
