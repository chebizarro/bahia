package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// nonceConfidentialEncryptor behaves like the OCK encryptor where it matters
// for dedupe: every encryption of the same plaintext yields different content.
type nonceConfidentialEncryptor struct {
	mockConfidentialEncryptor
	nonce atomic.Uint64
}

func (e *nonceConfidentialEncryptor) EncryptConfidential(_ context.Context, _ string, plaintext []byte, _ int, _, _ string, _ []byte) (string, error) {
	out, err := json.Marshal(map[string]any{
		"key_version":  "v1",
		"nonce":        e.nonce.Add(1),
		"_org_visible": json.RawMessage(plaintext),
	})
	return string(out), err
}

// memoryPaymentIndex stands in for the optional SQL payment index.
type memoryPaymentIndex struct {
	mu     sync.Mutex
	rows   map[uuid.UUID]domain.PaymentRecord
	writes int
	err    error
}

func newMemoryPaymentIndex() *memoryPaymentIndex {
	return &memoryPaymentIndex{rows: map[uuid.UUID]domain.PaymentRecord{}}
}

func (m *memoryPaymentIndex) Create(_ context.Context, rec *domain.PaymentRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if m.err != nil {
		return m.err
	}
	m.rows[rec.ID] = *rec
	return nil
}

func (m *memoryPaymentIndex) UpdateStatus(_ context.Context, id uuid.UUID, status domain.PaymentStatus, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if m.err != nil {
		return m.err
	}
	rec, ok := m.rows[id]
	if !ok {
		return repository.ErrNotFound
	}
	rec.Status, rec.ErrorMessage = status, message
	m.rows[id] = rec
	return nil
}

func (m *memoryPaymentIndex) GetByID(_ context.Context, id uuid.UUID) (*domain.PaymentRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[id]
	if !ok {
		return nil, nil // PgPaymentRepository reports a missing row as (nil, nil)
	}
	return &rec, nil
}

func (m *memoryPaymentIndex) ListByRun(context.Context, uuid.UUID) ([]domain.PaymentRecord, error) {
	return nil, errors.New("payment history must not be read from the SQL index")
}

func (m *memoryPaymentIndex) ListByWorker(context.Context, string, int) ([]domain.PaymentRecord, error) {
	return nil, errors.New("payment history must not be read from the SQL index")
}

func (m *memoryPaymentIndex) GetByTokenHash(context.Context, string) (*domain.PaymentRecord, error) {
	return nil, errors.New("payment idempotency must not be read from the SQL index")
}

func (m *memoryPaymentIndex) snapshot() (map[uuid.UUID]domain.PaymentRecord, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[uuid.UUID]domain.PaymentRecord, len(m.rows))
	for id, rec := range m.rows {
		out[id] = rec
	}
	return out, m.writes
}

func localPaymentService(d *localHistoryDaemon, index repository.PaymentRecordRepository) *service.PaymentService {
	canonical := NewPaymentCanonicalPublisher(d.projector, &nonceConfidentialEncryptor{}, zap.NewNop())
	svc := service.NewPaymentService(index, zap.NewNop())
	svc.SetCPStatePublisher(canonical)
	svc.SetCanonicalView(canonical)
	return svc
}

// storedPaymentEvents returns the daemon's retained payment cp-state events.
func storedPaymentEvents(t *testing.T, store *localstore.Store) []nostr.Event {
	t.Helper()
	servicePubkey, err := publicKeyHexFromPrivateKeyHex(projectorTestConfig().PrivateKey)
	require.NoError(t, err)
	var out []nostr.Event
	for ev := range store.QueryEvents(nostr.Filter{
		Kinds:   []nostr.Kind{KindCASControlState},
		Authors: []nostr.PubKey{nostr.MustPubKeyFromHex(servicePubkey)},
		Tags:    nostr.TagMap{"t": []string{kinds.CPStateTopicPaymentRecord}},
	}) {
		out = append(out, ev)
	}
	return out
}

func TestPaymentCanonicalDBLessLocalStoreSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	runID := uuid.New()

	first := startLocalHistoryDaemon(t, dir, script)
	firstService := localPaymentService(first, nil)
	record, err := firstService.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.NoError(t, err)
	change, err := firstService.RecordChange(ctx, runID, "worker", "https://mint", 11, "change-proof")
	require.NoError(t, err)
	publishCalls := script.totalCalls()
	require.Equal(t, 4, publishCalls, "two records, each published to both control-plane relays")

	replayed, err := firstService.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.NoError(t, err)
	require.Equal(t, record.ID, replayed.ID)
	require.Equal(t, publishCalls, script.totalCalls(), "same token must not sign another record")
	first.close()

	// The restarted daemon has no SQL repository and a cold service: payment
	// history and idempotency come from the reopened local event store.
	restarted := startLocalHistoryDaemon(t, dir, script)
	restartedService := localPaymentService(restarted, nil)
	history, err := restartedService.GetPaymentHistory(ctx, "worker", 10)
	require.NoError(t, err)
	require.Len(t, history, 2)
	summary, err := restartedService.GetRunCostSummary(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, service.CostSummary{TotalPaid: 41, TotalChange: 11, NetCost: 30, PaymentCount: 1, ChangeCount: 1}, *summary)

	replayed, err = restartedService.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.NoError(t, err)
	require.Equal(t, record.ID, replayed.ID)
	require.Equal(t, publishCalls, script.totalCalls(), "a retry after restart must not sign a duplicate")

	require.NoError(t, restartedService.MarkPaymentSent(ctx, record.ID))
	require.Equal(t, publishCalls+2, script.totalCalls(), "the transition is one new event")
	require.NoError(t, restartedService.MarkPaymentSent(ctx, record.ID))
	require.Equal(t, publishCalls+2, script.totalCalls(), "repeating the transition signs nothing")

	payments, err := restartedService.GetRunPayments(ctx, runID)
	require.NoError(t, err)
	statuses := map[uuid.UUID]domain.PaymentStatus{}
	for _, rec := range payments {
		statuses[rec.ID] = rec.Status
	}
	require.Equal(t, map[uuid.UUID]domain.PaymentStatus{record.ID: domain.PaymentStatusSent, change.ID: domain.PaymentStatusRedeemed}, statuses)
	require.Len(t, storedPaymentEvents(t, restarted.store), 2, "state transition must replace the same local coordinate")
}

// Randomized ciphertext must not defeat the dedupe: re-publishing unchanged
// payment state signs nothing, and changed state signs exactly one event.
func TestPaymentCanonicalPublisherDedupesRandomizedCiphertext(t *testing.T) {
	ctx := context.Background()
	script := newRelayScript()
	daemon := startLocalHistoryDaemon(t, t.TempDir(), script)
	publisher := NewPaymentCanonicalPublisher(daemon.projector, &nonceConfidentialEncryptor{}, zap.NewNop())
	rec := &domain.PaymentRecord{ID: uuid.New(), DeploymentRunID: uuid.New(), WorkerPubkey: "worker", MintURL: "https://mint", AmountSats: 9, TokenHash: "hash", Direction: domain.PaymentDirectionPayment, Status: domain.PaymentStatusPending}

	require.NoError(t, publisher.PublishPaymentRecord(ctx, rec))
	require.NoError(t, publisher.PublishPaymentRecord(ctx, rec))
	require.Equal(t, 2, script.totalCalls(), "unchanged plaintext is one event on two relays")

	rec.Status = domain.PaymentStatusSent
	require.NoError(t, publisher.PublishPaymentRecord(ctx, rec))
	require.Equal(t, 4, script.totalCalls())
	events := storedPaymentEvents(t, daemon.store)
	require.Len(t, events, 1)
	require.Contains(t, events[0].Content, `"nonce":3`, "the retained event is the encryptor's envelope of the latest state")
	require.NotEmpty(t, tagValue(events[0].Tags, confidentialStateHashTag))
	for _, tag := range events[0].Tags {
		require.NotContains(t, tag, "hash", "the token hash stays inside the encrypted content")
	}
}

func TestPaymentCanonicalRejectedPublishIsSurfacedAndLeavesIndexUntouched(t *testing.T) {
	ctx := context.Background()
	runID := uuid.New()
	script := newRelayScript()
	script.reject[cpRelayA] = "blocked: maintenance"
	script.reject[cpRelayB] = "blocked: maintenance"
	daemon := startLocalHistoryDaemon(t, t.TempDir(), script)
	index := newMemoryPaymentIndex()
	svc := localPaymentService(daemon, index)

	_, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.ErrorIs(t, err, ErrPublishAbandoned, "a rejected canonical publish is returned to the caller")
	rows, writes := index.snapshot()
	require.Empty(t, rows)
	require.Zero(t, writes, "SQL must stay untouched when the canonical publish is rejected")
	require.Empty(t, storedPaymentEvents(t, daemon.store), "a rejected record is not the daemon's output")
	history, err := svc.GetPaymentHistory(ctx, "worker", 10)
	require.NoError(t, err)
	require.Empty(t, history)

	script.mu.Lock()
	script.reject = map[string]string{}
	script.mu.Unlock()
	rec, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.NoError(t, err)
	rows, _ = index.snapshot()
	require.Len(t, rows, 1)
	require.Equal(t, domain.PaymentStatusPending, rows[rec.ID].Status)
	require.Len(t, storedPaymentEvents(t, daemon.store), 1)
}

// With every relay unreachable the signed record is still durable: it is held
// by the publish outbox and the local store before the index is written.
func TestPaymentCanonicalQueuedPublishIsDurableAndIndexFailureIsNotFatal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	runID := uuid.New()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	first := startLocalHistoryDaemon(t, dir, script)
	index := newMemoryPaymentIndex()
	index.err = errors.New("SQL down")
	svc := localPaymentService(first, index)

	rec, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.NoError(t, err, "a queued publish and a failed index write are both non-fatal")
	events := storedPaymentEvents(t, first.store)
	require.Len(t, events, 1, "the canonical record exists although the SQL write failed")
	entry, held, err := first.outbox.Get(events[0].ID)
	require.NoError(t, err)
	require.True(t, held, "the signed record is durably queued for relay delivery")
	require.Equal(t, localstore.OutboxPending, entry.State)
	rows, writes := index.snapshot()
	require.Empty(t, rows)
	require.Equal(t, 1, writes)
	first.close()

	// After a restart the queued record is still the payment history, and a
	// healthy index is rebuilt from it.
	script.setDown(cpRelayA, false)
	script.setDown(cpRelayB, false)
	restarted := startLocalHistoryDaemon(t, dir, script)
	rebuilt := newMemoryPaymentIndex()
	restartedService := localPaymentService(restarted, rebuilt)
	history, err := restartedService.GetPaymentHistory(ctx, "worker", 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, rec.ID, history[0].ID)
	require.NoError(t, restartedService.RebuildIndex(ctx))
	rows, _ = rebuilt.snapshot()
	require.Len(t, rows, 1)
	require.Equal(t, int64(41), rows[rec.ID].AmountSats)
	require.Equal(t, runID, rows[rec.ID].DeploymentRunID)
}

// runOutbox runs the daemon's publish outbox runner with a short retry
// schedule until the returned stop function is called.
func runOutbox(t *testing.T, d *localHistoryDaemon, maxAttempts int) (stop func()) {
	t.Helper()
	d.publisher.newBackoff = func() *Backoff { return &Backoff{Initial: time.Millisecond, Max: 5 * time.Millisecond, Multiplier: 2} }
	d.publisher.idleInterval = 5 * time.Millisecond
	d.publisher.maxAttempts = maxAttempts
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.publisher.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// A record that was queued (the mutation succeeded and the SQL index was
// written) and later abandoned by the outbox is not lost: it stays readable
// from the canonical view, flagged undelivered on its coordinate, the outbox
// entry stays failed, and the SQL index agrees with the canonical read. An
// operator retry of the entry that reaches the quorum clears the flag
// (docs/architecture/outbox-delivery.md).
func TestPaymentCanonicalQueuedThenAbandonedRecordStaysReadableAndFlagged(t *testing.T) {
	ctx := context.Background()
	runID := uuid.New()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	daemon := startLocalHistoryDaemon(t, t.TempDir(), script)
	index := newMemoryPaymentIndex()
	svc := localPaymentService(daemon, index)
	abandoned := make(chan nostr.Event, 1)
	daemon.publisher.OnDeliveryAbandoned(func(ev nostr.Event) { abandoned <- ev })

	rec, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.NoError(t, err, "a queued publish succeeds")
	rows, _ := index.snapshot()
	require.Len(t, rows, 1, "the SQL index is written after the queued publish")

	stop := runOutbox(t, daemon, 2)
	ev := receive(t, abandoned, "abandonment after the attempt budget")
	stop()
	entry, held, err := daemon.outbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, held)
	require.Equal(t, localstore.OutboxFailed, entry.State, "the outbox entry stays in its terminal failed state")

	events := storedPaymentEvents(t, daemon.store)
	require.Len(t, events, 1, "the abandoned record is still the daemon's committed state")
	require.Equal(t, ev.ID, events[0].ID)
	marker, flagged, err := daemon.store.Undelivered(events[0])
	require.NoError(t, err)
	require.True(t, flagged, "the coordinate is marked undelivered")
	require.Equal(t, ev.ID, marker.EventID)
	require.Contains(t, marker.Detail, "abandoned after 2 publish attempts")
	records, err := daemon.projector.history.ListByKind(ctx, KindCASControlState, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, repository.NostrPublishStateFailed, records[0].PublishState, "the record reads as delivery-failed")

	history, err := svc.GetPaymentHistory(ctx, "worker", 10)
	require.NoError(t, err)
	require.Len(t, history, 1, "the canonical read still answers with the record")
	require.Equal(t, rec.ID, history[0].ID)
	rows, _ = index.snapshot()
	require.Len(t, rows, 1)
	require.Equal(t, history[0].ID, rows[rec.ID].ID, "the SQL index agrees with the canonical read")
	require.Equal(t, history[0].Status, rows[rec.ID].Status)
	require.Equal(t, history[0].AmountSats, rows[rec.ID].AmountSats)

	// Operator action: the relays are back and the failed entry is retried.
	script.setDown(cpRelayA, false)
	script.setDown(cpRelayB, false)
	delivered := make(chan nostr.Event, 1)
	daemon.publisher.OnDelivered(func(ev nostr.Event) { delivered <- ev })
	_, err = daemon.outbox.Retry(ev.ID)
	require.NoError(t, err)
	stop = runOutbox(t, daemon, 2)
	require.Equal(t, ev.ID, receive(t, delivered, "delivery of the retried entry").ID)
	stop()
	_, flagged, err = daemon.store.Undelivered(events[0])
	require.NoError(t, err)
	require.False(t, flagged, "a quorum-accepted retry clears the marker")
	markers, err := daemon.store.ListUndelivered()
	require.NoError(t, err)
	require.Empty(t, markers)
	require.Len(t, storedPaymentEvents(t, daemon.store), 1, "the retry delivered the same event; nothing was re-signed")
}

// The next publish of an abandoned coordinate (here a status transition) is
// strictly newer than the flagged event, replaces it locally and on the relays,
// and clears the flag.
func TestPaymentCanonicalNextPublishOfAbandonedCoordinateClearsTheFlag(t *testing.T) {
	ctx := context.Background()
	runID := uuid.New()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	daemon := startLocalHistoryDaemon(t, t.TempDir(), script)
	svc := localPaymentService(daemon, newMemoryPaymentIndex())
	abandoned := make(chan nostr.Event, 1)
	daemon.publisher.OnDeliveryAbandoned(func(ev nostr.Event) { abandoned <- ev })

	rec, err := svc.RecordPayment(ctx, runID, "worker", "https://mint", 41, "proof")
	require.NoError(t, err)
	stop := runOutbox(t, daemon, 2)
	first := receive(t, abandoned, "abandonment after the attempt budget")
	stop()
	_, flagged, err := daemon.store.Undelivered(first)
	require.NoError(t, err)
	require.True(t, flagged)

	script.setDown(cpRelayA, false)
	script.setDown(cpRelayB, false)
	require.NoError(t, svc.MarkPaymentSent(ctx, rec.ID))
	events := storedPaymentEvents(t, daemon.store)
	require.Len(t, events, 1)
	require.NotEqual(t, first.ID, events[0].ID, "the transition is a new event on the same coordinate")
	require.Greater(t, events[0].CreatedAt, first.CreatedAt, "the replacement is strictly newer than the flagged event")
	_, flagged, err = daemon.store.Undelivered(events[0])
	require.NoError(t, err)
	require.False(t, flagged, "the delivered replacement clears the flag")
	history, err := svc.GetPaymentHistory(ctx, "worker", 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, domain.PaymentStatusSent, history[0].Status)
}
