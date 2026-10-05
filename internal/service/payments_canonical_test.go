package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// recordingPaymentIndex is the optional SQL index of a payment service test. It
// logs every write next to the canonical publishes and can be made to fail.
type recordingPaymentIndex struct {
	mu   sync.Mutex
	repo *mockPaymentRepo
	log  *paymentOpLog
	err  error
}

func newRecordingPaymentIndex(log *paymentOpLog) *recordingPaymentIndex {
	return &recordingPaymentIndex{repo: newMockPaymentRepo(), log: log}
}

func (r *recordingPaymentIndex) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *recordingPaymentIndex) Create(ctx context.Context, rec *domain.PaymentRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.add("index:create")
	if r.err != nil {
		return r.err
	}
	copied := *rec
	return r.repo.Create(ctx, &copied)
}

func (r *recordingPaymentIndex) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.PaymentStatus, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.add("index:update")
	if r.err != nil {
		return r.err
	}
	return r.repo.UpdateStatus(ctx, id, status, message)
}

func (r *recordingPaymentIndex) GetByID(ctx context.Context, id uuid.UUID) (*domain.PaymentRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	rec, err := r.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	copied := *rec
	return &copied, nil
}

func (r *recordingPaymentIndex) ListByRun(ctx context.Context, runID uuid.UUID) ([]domain.PaymentRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo.ListByRun(ctx, runID)
}

func (r *recordingPaymentIndex) ListByWorker(ctx context.Context, worker string, limit int) ([]domain.PaymentRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo.ListByWorker(ctx, worker, limit)
}

func (r *recordingPaymentIndex) GetByTokenHash(ctx context.Context, hash string) (*domain.PaymentRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo.GetByTokenHash(ctx, hash)
}

func (r *recordingPaymentIndex) rows() map[uuid.UUID]domain.PaymentRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[uuid.UUID]domain.PaymentRecord, len(r.repo.records))
	for id, rec := range r.repo.records {
		out[id] = *rec
	}
	return out
}

var _ repository.PaymentRecordRepository = (*recordingPaymentIndex)(nil)

func canonicalPaymentService(index repository.PaymentRecordRepository, canonical *mockPaymentCanonical) *PaymentService {
	svc := NewPaymentService(index, zap.NewNop())
	svc.SetCPStatePublisher(canonical)
	svc.SetCanonicalView(canonical)
	return svc
}

func TestPaymentPublishesCanonicalBeforeSQLIndex(t *testing.T) {
	ctx := context.Background()
	log := &paymentOpLog{}
	canonical := newMockPaymentCanonical()
	canonical.log = log
	index := newRecordingPaymentIndex(log)
	svc := canonicalPaymentService(index, canonical)

	rec, err := svc.RecordPayment(ctx, uuid.New(), "worker", "https://mint", 17, "token")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, rec.ID, "identity is minted before anything is written")
	require.Equal(t, []string{"publish:pending", "index:create"}, log.snapshot())

	require.NoError(t, svc.MarkPaymentSent(ctx, rec.ID))
	require.Equal(t, []string{"publish:pending", "index:create", "publish:sent", "index:update"}, log.snapshot())

	published, ok := canonical.record(rec.ID)
	require.True(t, ok)
	require.Equal(t, domain.PaymentStatusSent, published.Status)
	require.Equal(t, domain.PaymentStatusSent, index.rows()[rec.ID].Status)
	coordinates, _ := canonical.counts()
	require.Equal(t, 1, coordinates, "the transition replaces the payment's own coordinate")
}

func TestPaymentCanonicalRecordExistsWhenSQLIndexFails(t *testing.T) {
	ctx := context.Background()
	runID := uuid.New()
	log := &paymentOpLog{}
	canonical := newMockPaymentCanonical()
	canonical.log = log
	index := newRecordingPaymentIndex(log)
	index.setErr(errors.New("SQL down"))
	svc := canonicalPaymentService(index, canonical)

	rec, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 17, "token")
	require.NoError(t, err, "a failed index write must not fail a payment that is already canonical")
	_, ok := canonical.record(rec.ID)
	require.True(t, ok)
	require.Empty(t, index.rows())

	require.NoError(t, svc.MarkPaymentSent(ctx, rec.ID))
	history, err := svc.GetPaymentHistory(ctx, "worker", 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, domain.PaymentStatusSent, history[0].Status, "history is read from canonical state, not SQL")

	// The index recovers; the next write of the record repairs its row without
	// signing anything new.
	index.setErr(nil)
	_, attemptsBefore := canonical.counts()
	replayed, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 17, "token")
	require.NoError(t, err)
	require.Equal(t, rec.ID, replayed.ID)
	_, attemptsAfter := canonical.counts()
	require.Equal(t, attemptsBefore, attemptsAfter)
	require.Equal(t, domain.PaymentStatusSent, index.rows()[rec.ID].Status)
}

func TestPaymentCanonicalRejectionLeavesSQLUntouched(t *testing.T) {
	ctx := context.Background()
	log := &paymentOpLog{}
	canonical := newMockPaymentCanonical()
	canonical.log = log
	canonical.setErr(errors.New("relay rejected"))
	index := newRecordingPaymentIndex(log)
	svc := canonicalPaymentService(index, canonical)

	_, err := svc.RecordPayment(ctx, uuid.New(), "worker", "https://mint", 17, "token")
	require.ErrorContains(t, err, "relay rejected", "the publish failure is returned, not swallowed")
	require.Empty(t, log.snapshot(), "SQL must not be written when the canonical publish is rejected")
	require.Empty(t, index.rows())

	canonical.mu.Lock()
	firstID := canonical.attempts[0]
	canonical.mu.Unlock()
	canonical.setErr(nil)
	retried, err := svc.RecordPayment(ctx, uuid.New(), "worker", "https://mint", 17, "token")
	require.NoError(t, err)
	require.Equal(t, firstID, retried.ID, "a retry addresses the coordinate the rejected attempt minted")
	coordinates, _ := canonical.counts()
	require.Equal(t, 1, coordinates)
	require.Equal(t, []string{"publish:pending", "index:create"}, log.snapshot())
}

func TestPaymentStatusRejectionLeavesSQLUntouched(t *testing.T) {
	ctx := context.Background()
	log := &paymentOpLog{}
	canonical := newMockPaymentCanonical()
	canonical.log = log
	index := newRecordingPaymentIndex(log)
	svc := canonicalPaymentService(index, canonical)
	rec, err := svc.RecordPayment(ctx, uuid.New(), "worker", "https://mint", 17, "token")
	require.NoError(t, err)

	canonical.setErr(errors.New("relay rejected"))
	require.ErrorContains(t, svc.MarkPaymentSent(ctx, rec.ID), "relay rejected")
	require.Equal(t, []string{"publish:pending", "index:create"}, log.snapshot())
	require.Equal(t, domain.PaymentStatusPending, index.rows()[rec.ID].Status)
	published, _ := canonical.record(rec.ID)
	require.Equal(t, domain.PaymentStatusPending, published.Status)
}

func TestPaymentServiceWorksWithoutSQLRepository(t *testing.T) {
	ctx := context.Background()
	runID, otherRun := uuid.New(), uuid.New()
	canonical := newMockPaymentCanonical()
	svc := canonicalPaymentService(nil, canonical)
	require.NoError(t, svc.Ready())

	paid, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 100, "payment-token")
	require.NoError(t, err)
	change, err := svc.RecordChange(ctx, runID, "worker", "https://mint", 30, "change-token")
	require.NoError(t, err)
	_, err = svc.RecordPayment(ctx, otherRun, "other-worker", "https://mint", 5, "other-token")
	require.NoError(t, err)
	require.NoError(t, svc.MarkPaymentSent(ctx, paid.ID))
	require.NoError(t, svc.MarkPaymentSent(ctx, paid.ID), "repeating a transition is a no-op")
	require.ErrorIs(t, svc.MarkPaymentSent(ctx, uuid.New()), repository.ErrNotFound)

	payments, err := svc.GetRunPayments(ctx, runID)
	require.NoError(t, err)
	require.Len(t, payments, 2)
	summary, err := svc.GetRunCostSummary(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, CostSummary{TotalPaid: 100, TotalChange: 30, NetCost: 70, PaymentCount: 1, ChangeCount: 1}, *summary)

	history, err := svc.GetPaymentHistory(ctx, "worker", 10)
	require.NoError(t, err)
	require.Len(t, history, 2)
	statuses := map[uuid.UUID]domain.PaymentStatus{}
	for _, rec := range history {
		statuses[rec.ID] = rec.Status
	}
	require.Equal(t, domain.PaymentStatusSent, statuses[paid.ID])
	require.Equal(t, domain.PaymentStatusRedeemed, statuses[change.ID])
	limited, err := svc.GetPaymentHistory(ctx, "worker", 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.NoError(t, svc.RebuildIndex(ctx), "rebuilding an absent index is a no-op")
}

func TestPaymentRebuildIndexReproducesCanonicalHistory(t *testing.T) {
	ctx := context.Background()
	runID := uuid.New()
	canonical := newMockPaymentCanonical()
	writer := canonicalPaymentService(nil, canonical)
	paid, err := writer.RecordPayment(ctx, runID, "worker", "https://mint", 100, "payment-token")
	require.NoError(t, err)
	change, err := writer.RecordChange(ctx, runID, "worker", "https://mint", 30, "change-token")
	require.NoError(t, err)
	require.NoError(t, writer.MarkPaymentSent(ctx, paid.ID))
	_, attemptsBefore := canonical.counts()

	// A fresh SQL index and a restarted service rebuild from canonical state.
	log := &paymentOpLog{}
	index := newRecordingPaymentIndex(log)
	restarted := canonicalPaymentService(index, canonical)
	require.NoError(t, restarted.RebuildIndex(ctx))
	rows := index.rows()
	require.Len(t, rows, 2)
	require.Equal(t, domain.PaymentStatusSent, rows[paid.ID].Status)
	require.Equal(t, domain.PaymentDirectionChange, rows[change.ID].Direction)
	require.Equal(t, int64(30), rows[change.ID].AmountSats)

	require.NoError(t, restarted.RebuildIndex(ctx))
	require.Equal(t, []string{"index:create", "index:create"}, log.snapshot(), "a second rebuild writes nothing")
	_, attemptsAfter := canonical.counts()
	require.Equal(t, attemptsBefore, attemptsAfter, "rebuilding the index never publishes")
}

func TestPaymentConcurrentRetriesPublishOneCanonicalRecord(t *testing.T) {
	ctx := context.Background()
	runID := uuid.New()
	canonical := newMockPaymentCanonical()
	svc := canonicalPaymentService(nil, canonical)

	const callers = 8
	ids := make([]uuid.UUID, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 17, "token")
			if err == nil {
				ids[i] = rec.ID
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i := range errs {
		require.NoError(t, errs[i])
		require.Equal(t, ids[0], ids[i])
	}
	coordinates, attempts := canonical.counts()
	require.Equal(t, 1, coordinates)
	require.Equal(t, 1, attempts, "retries of one token must not sign duplicate records")
}

func TestPaymentTokenCannotBeRecordedInBothDirections(t *testing.T) {
	ctx := context.Background()
	canonical := newMockPaymentCanonical()
	svc := canonicalPaymentService(nil, canonical)
	_, err := svc.RecordPayment(ctx, uuid.New(), "worker", "https://mint", 17, "token")
	require.NoError(t, err)
	_, err = svc.RecordChange(ctx, uuid.New(), "worker", "https://mint", 17, "token")
	require.ErrorIs(t, err, repository.ErrAlreadyExists)
	coordinates, attempts := canonical.counts()
	require.Equal(t, 1, coordinates)
	require.Equal(t, 1, attempts)
}

func TestPaymentServiceFailsClosedWithoutCanonicalPublisher(t *testing.T) {
	ctx := context.Background()
	log := &paymentOpLog{}
	index := newRecordingPaymentIndex(log)
	svc := NewPaymentService(index, zap.NewNop())
	require.Error(t, svc.Ready())
	_, err := svc.RecordPayment(ctx, uuid.New(), "worker", "https://mint", 17, "token")
	require.Error(t, err, "without a canonical publisher the service must not fall back to SQL-only writes")
	require.Empty(t, log.snapshot())
	_, err = svc.GetPaymentHistory(ctx, "worker", 10)
	require.Error(t, err)
}
