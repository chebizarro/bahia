package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type failingPaymentIndex struct {
	*mockPaymentRepo
	creates int
	updates int
}

func (r *failingPaymentIndex) Create(context.Context, *domain.PaymentRecord) error {
	r.creates++
	return errors.New("SQL down")
}
func (r *failingPaymentIndex) UpdateStatus(context.Context, uuid.UUID, domain.PaymentStatus, string) error {
	r.updates++
	return errors.New("SQL down")
}

func TestPaymentCanonicalFirstAndDBLessHistory(t *testing.T) {
	ctx := context.Background()
	runID := uuid.New()
	canonical := &mockPaymentCanonical{records: map[uuid.UUID]domain.PaymentRecord{}}
	index := &failingPaymentIndex{mockPaymentRepo: newMockPaymentRepo()}
	svc := NewPaymentService(index, zap.NewNop())
	svc.SetCPStatePublisher(canonical)
	svc.SetCanonicalView(canonical)
	rec, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 17, "token")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == uuid.Nil || index.creates != 1 {
		t.Fatalf("identity/index = %s/%d", rec.ID, index.creates)
	}
	history, err := svc.GetPaymentHistory(ctx, "worker", 10)
	if err != nil || len(history) != 1 || history[0].ID != rec.ID {
		t.Fatalf("canonical history = %v, %v", history, err)
	}
	if err := svc.MarkPaymentSent(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	history, err = svc.GetPaymentHistory(ctx, "worker", 10)
	if err != nil || len(history) != 1 || history[0].Status != domain.PaymentStatusSent {
		t.Fatalf("sent history = %v, %v", history, err)
	}
	if index.updates != 1 {
		t.Fatalf("SQL status writes = %d", index.updates)
	}
	restarted := NewPaymentService(nil, zap.NewNop())
	restarted.SetCPStatePublisher(canonical)
	restarted.SetCanonicalView(canonical)
	replay, err := restarted.RecordPayment(ctx, runID, "worker", "https://mint", 17, "token")
	if err != nil || replay.ID != rec.ID {
		t.Fatalf("replay = %v, %v", replay, err)
	}
	summary, err := restarted.GetRunCostSummary(ctx, runID)
	if err != nil || summary.TotalPaid != 17 {
		t.Fatalf("summary = %v, %v", summary, err)
	}
	canonical.mu.Lock()
	n := len(canonical.records)
	canonical.mu.Unlock()
	if n != 1 {
		t.Fatalf("canonical coordinates = %d", n)
	}
}
func TestPaymentCanonicalRejectionLeavesSQLUntouched(t *testing.T) {
	ctx := context.Background()
	canonical := &mockPaymentCanonical{records: map[uuid.UUID]domain.PaymentRecord{}, err: errors.New("relay rejected")}
	index := &failingPaymentIndex{mockPaymentRepo: newMockPaymentRepo()}
	svc := NewPaymentService(index, zap.NewNop())
	svc.SetCPStatePublisher(canonical)
	svc.SetCanonicalView(canonical)
	if _, err := svc.RecordPayment(ctx, uuid.New(), "worker", "https://mint", 17, "token"); err == nil {
		t.Fatal("expected publish rejection")
	}
	if index.creates != 0 {
		t.Fatalf("SQL touched after rejection: %d", index.creates)
	}
	canonical.mu.Lock()
	firstID := canonical.attempts[0]
	canonical.err = nil
	canonical.mu.Unlock()
	retried, err := svc.RecordPayment(ctx, uuid.New(), "worker", "https://mint", 17, "token")
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID != firstID {
		t.Fatalf("retry changed canonical identity: %s != %s", retried.ID, firstID)
	}
	canonical.mu.Lock()
	n := len(canonical.records)
	canonical.mu.Unlock()
	if n != 1 {
		t.Fatalf("retry created %d canonical coordinates, want 1", n)
	}
}

func TestPaymentStatusRejectionLeavesSQLUntouched(t *testing.T) {
	ctx := context.Background()
	canonical := &mockPaymentCanonical{records: map[uuid.UUID]domain.PaymentRecord{}}
	index := &failingPaymentIndex{mockPaymentRepo: newMockPaymentRepo()}
	svc := NewPaymentService(index, zap.NewNop())
	svc.SetCPStatePublisher(canonical)
	svc.SetCanonicalView(canonical)
	rec, err := svc.RecordPayment(ctx, uuid.New(), "worker", "https://mint", 17, "token")
	if err != nil {
		t.Fatal(err)
	}
	canonical.mu.Lock()
	canonical.err = errors.New("relay rejected")
	canonical.mu.Unlock()
	if err := svc.MarkPaymentSent(ctx, rec.ID); err == nil {
		t.Fatal("expected status publish rejection")
	}
	if index.updates != 0 {
		t.Fatalf("SQL status touched after rejection: %d", index.updates)
	}
	canonical.mu.Lock()
	status := canonical.records[rec.ID].Status
	canonical.mu.Unlock()
	if status != domain.PaymentStatusPending {
		t.Fatalf("rejected transition changed canonical status to %q", status)
	}
}
