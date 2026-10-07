package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
)

// A publish below the quorum is durably queued in the control-plane outbox:
// the caller gets a queued receipt, not an error, so it has no reason to
// re-sign and republish the same desired version.
func TestConfigFabricPublishBelowQuorumIsQueuedForControlPlaneRunner(t *testing.T) {
	ctx := context.Background()
	repo := repositorytest.NewInMemoryNostrEventRepository()
	publisher := &configTestPublisher{err: fmt.Errorf("relay down: %w", nostrutil.ErrPublishIncomplete)}
	svc := NewConfigFabricService(repo, publisher, newConfigTestSigner(t))
	svc.now = func() time.Time { return time.Unix(1787625660, 0) }

	receipt, err := svc.Publish(ctx, validPolicyRequest(1))
	if err != nil {
		t.Fatalf("Publish() error = %v, want queued receipt", err)
	}
	if receipt.Delivery != ConfigDeliveryQueued {
		t.Fatalf("receipt delivery = %q, want %q", receipt.Delivery, ConfigDeliveryQueued)
	}
	if len(publisher.events) != 1 || publisher.events[0].ID.Hex() != receipt.EventID {
		t.Fatalf("publisher saw %d events, want the receipt event once", len(publisher.events))
	}
	if publisher.entityTypes[0] != configEntityType {
		t.Fatalf("entity type = %q, want %q", publisher.entityTypes[0], configEntityType)
	}

	rec, err := repo.GetByID(ctx, receipt.EventID)
	if err != nil || rec == nil {
		t.Fatalf("desired row missing: rec=%v err=%v", rec, err)
	}
	if rec.PublishState != repository.NostrPublishStatePending || rec.PublishTarget != repository.LocalOutboxArchiveTarget(repository.NostrPublishTargetControlPlane) {
		t.Fatalf("desired row state=%q target=%q, want the pending archive row of the control-plane local outbox", rec.PublishState, rec.PublishTarget)
	}
	// The local outbox delivers the event: no PostgreSQL runner adopts the
	// archive row, so it is not delivered twice.
	for _, target := range []string{repository.NostrPublishTargetDefault, repository.NostrPublishTargetControlPlane} {
		rows, err := repo.ListUnpublishedAfter(ctx, target, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("the %q PostgreSQL runner would adopt %d config-fabric rows", target, len(rows))
		}
	}

	// Retrying the same version is refused, so a caller cannot queue a
	// second signed copy of the queued desired state.
	if _, err := svc.Publish(ctx, validPolicyRequest(1)); err == nil {
		t.Fatal("republishing a queued version must be refused")
	}
	if len(publisher.events) != 1 {
		t.Fatalf("publisher saw %d events after a refused republish, want 1", len(publisher.events))
	}
}

func TestConfigFabricPublishHardFailureIsReturned(t *testing.T) {
	repo := repositorytest.NewInMemoryNostrEventRepository()
	publisher := &configTestPublisher{err: errors.New("persist signed nostr event before publish: disk full")}
	svc := NewConfigFabricService(repo, publisher, newConfigTestSigner(t))

	if _, err := svc.Publish(context.Background(), validPolicyRequest(1)); err == nil {
		t.Fatal("a non-queue publish failure must be returned")
	}
}

func TestConfigFabricPublishAcceptedReceipt(t *testing.T) {
	repo := repositorytest.NewInMemoryNostrEventRepository()
	svc := NewConfigFabricService(repo, &configTestPublisher{}, newConfigTestSigner(t))

	receipt, err := svc.Publish(context.Background(), validPolicyRequest(1))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if receipt.Delivery != ConfigDeliveryAccepted {
		t.Fatalf("receipt delivery = %q, want %q", receipt.Delivery, ConfigDeliveryAccepted)
	}
}

// TestConfigFabricListDriftWithDeliveryQuery verifies that ListDrift consults
// the delivery query when the NostrEventRecord carries no PublishState
// (non-Postgres mode). An abandoned version is excluded from
// desired state; a pending version is kept.
func TestConfigFabricListDriftWithDeliveryQuery(t *testing.T) {
	ctx := context.Background()
	repo := repositorytest.NewInMemoryNostrEventRepository()
	publisher := &configTestPublisher{}
	signer := newConfigTestSigner(t)
	delivery := &fakeDeliveryQuery{outcomes: map[string]nostrutil.DeliveryOutcome{}}

	svc := NewConfigFabricService(repo, publisher, signer, WithDeliveryQuery(delivery))
	svc.now = func() time.Time { return time.Unix(1787625660, 0) }

	// Publish version 1 (accepted) and version 2 (accepted).
	r1, err := svc.Publish(ctx, validPolicyRequest(1))
	if err != nil {
		t.Fatalf("Publish v1 error = %v", err)
	}
	r2, err := svc.Publish(ctx, validPolicyRequest(2))
	if err != nil {
		t.Fatalf("Publish v2 error = %v", err)
	}

	// Simulate non-Postgres mode: clear PublishState on the stored records.
	clearPublishState(t, repo, r1.EventID)
	clearPublishState(t, repo, r2.EventID)

	// Without delivery query answers, both versions are kept (conservative).
	drift, err := svc.ListDrift(ctx)
	if err != nil {
		t.Fatalf("ListDrift() error = %v", err)
	}
	if len(drift) != 1 {
		t.Fatalf("expected 1 drift entry, got %d", len(drift))
	}
	if drift[0].DesiredVersion != 2 {
		t.Fatalf("desired version = %d, want 2", drift[0].DesiredVersion)
	}

	// Mark version 2 as abandoned; version 1 should become the desired.
	delivery.outcomes[r2.EventID] = nostrutil.DeliveryAbandoned
	delivery.outcomes[r1.EventID] = nostrutil.DeliveryDelivered

	drift, err = svc.ListDrift(ctx)
	if err != nil {
		t.Fatalf("ListDrift() after abandonment error = %v", err)
	}
	if len(drift) != 1 {
		t.Fatalf("expected 1 drift entry after abandonment, got %d", len(drift))
	}
	if drift[0].DesiredVersion != 1 {
		t.Fatalf("desired version after v2 abandoned = %d, want 1 (fallback to delivered v1)", drift[0].DesiredVersion)
	}
	if drift[0].DesiredEventID != r1.EventID {
		t.Fatalf("desired event = %q, want v1 event %q", drift[0].DesiredEventID, r1.EventID)
	}

	// Mark both as abandoned: no drift entries.
	delivery.outcomes[r1.EventID] = nostrutil.DeliveryAbandoned
	drift, err = svc.ListDrift(ctx)
	if err != nil {
		t.Fatalf("ListDrift() both abandoned error = %v", err)
	}
	if len(drift) != 0 {
		t.Fatalf("expected 0 drift entries when all versions abandoned, got %d", len(drift))
	}
}

// TestConfigFabricPublishStateFromPgRecord verifies that when the record
// carries PublishState (Postgres mode), the delivery query is not consulted.
func TestConfigFabricPublishStateFromPgRecord(t *testing.T) {
	ctx := context.Background()
	repo := repositorytest.NewInMemoryNostrEventRepository()
	publisher := &configTestPublisher{}
	signer := newConfigTestSigner(t)
	delivery := &fakeDeliveryQuery{outcomes: map[string]nostrutil.DeliveryOutcome{}}

	svc := NewConfigFabricService(repo, publisher, signer, WithDeliveryQuery(delivery))
	svc.now = func() time.Time { return time.Unix(1787625660, 0) }

	r1, err := svc.Publish(ctx, validPolicyRequest(1))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	// Simulate Postgres mode: the record carries PublishState = failed.
	setPublishState(t, repo, r1.EventID, repository.NostrPublishStateFailed)

	// Even though the delivery query says delivered, the record's own
	// PublishState takes precedence.
	delivery.outcomes[r1.EventID] = nostrutil.DeliveryDelivered

	drift, err := svc.ListDrift(ctx)
	if err != nil {
		t.Fatalf("ListDrift() error = %v", err)
	}
	if len(drift) != 0 {
		t.Fatalf("expected 0 drift entries (PG state says failed), got %d", len(drift))
	}
}

type fakeDeliveryQuery struct {
	outcomes map[string]nostrutil.DeliveryOutcome
}

func (f *fakeDeliveryQuery) DeliveryOutcome(_ context.Context, id string) (nostrutil.DeliveryOutcome, error) {
	if outcome, ok := f.outcomes[id]; ok {
		return outcome, nil
	}
	return nostrutil.DeliveryUnknown, nil
}

func clearPublishState(t *testing.T, repo *repositorytest.InMemoryNostrEventRepository, id string) {
	t.Helper()
	rec, err := repo.GetByID(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("clearPublishState: record %q not found: %v", id, err)
	}
	rec.PublishState = ""
	repo.Replace(id, *rec)
}

func setPublishState(t *testing.T, repo *repositorytest.InMemoryNostrEventRepository, id, state string) {
	t.Helper()
	rec, err := repo.GetByID(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("setPublishState: record %q not found: %v", id, err)
	}
	rec.PublishState = state
	repo.Replace(id, *rec)
}

// TestConfigFabricListDriftWithoutPostgres verifies that a daemon with no
// Postgres connection computes ListDrift correctly from the delivery query
// alone. The local event store does not preserve PublishState
// on read-back, so isDesiredAbandoned falls through to the delivery query.
// This uses a statelessPublishRepo wrapper that strips PublishState, matching
// the production LocalEventRepository behaviour.
func TestConfigFabricListDriftWithoutPostgres(t *testing.T) {
	ctx := context.Background()
	inner := repositorytest.NewInMemoryNostrEventRepository()
	repo := &statelessPublishRepo{inner}

	publisher := &configTestPublisher{}
	signer := newConfigTestSigner(t)
	delivery := &fakeDeliveryQuery{outcomes: map[string]nostrutil.DeliveryOutcome{}}

	svc := NewConfigFabricService(repo, publisher, signer, WithDeliveryQuery(delivery))
	svc.now = func() time.Time { return time.Unix(1787625660, 0) }

	// Publish version 1 (accepted inline).
	r1, err := svc.Publish(ctx, validPolicyRequest(1))
	if err != nil {
		t.Fatalf("Publish v1 error = %v", err)
	}
	// Publish version 2 (accepted inline).
	r2, err := svc.Publish(ctx, validPolicyRequest(2))
	if err != nil {
		t.Fatalf("Publish v2 error = %v", err)
	}

	// Verify the repo strips PublishState on reads (like LocalEventRepository).
	rec, err := repo.GetByID(ctx, r2.EventID)
	if err != nil || rec == nil {
		t.Fatalf("record %q not found: %v", r2.EventID, err)
	}
	if rec.PublishState != "" {
		t.Fatalf("expected empty PublishState from stateless repo, got %q", rec.PublishState)
	}

	// Without delivery query answers, both versions are kept (conservative).
	drift, err := svc.ListDrift(ctx)
	if err != nil {
		t.Fatalf("ListDrift() error = %v", err)
	}
	if len(drift) != 1 {
		t.Fatalf("expected 1 drift entry, got %d", len(drift))
	}
	if drift[0].DesiredVersion != 2 {
		t.Fatalf("desired version = %d, want 2", drift[0].DesiredVersion)
	}

	// The delivery query reports version 2 was abandoned.
	delivery.outcomes[r2.EventID] = nostrutil.DeliveryAbandoned
	delivery.outcomes[r1.EventID] = nostrutil.DeliveryDelivered

	drift, err = svc.ListDrift(ctx)
	if err != nil {
		t.Fatalf("ListDrift() after abandonment error = %v", err)
	}
	if len(drift) != 1 {
		t.Fatalf("expected 1 drift entry after abandonment, got %d", len(drift))
	}
	if drift[0].DesiredVersion != 1 {
		t.Fatalf("desired version after v2 abandoned = %d, want 1 (fallback to delivered v1)", drift[0].DesiredVersion)
	}
	if drift[0].DesiredEventID != r1.EventID {
		t.Fatalf("desired event = %q, want v1 event %q", drift[0].DesiredEventID, r1.EventID)
	}

	// Both abandoned: no drift entries (no relay holds any version).
	delivery.outcomes[r1.EventID] = nostrutil.DeliveryAbandoned
	drift, err = svc.ListDrift(ctx)
	if err != nil {
		t.Fatalf("ListDrift() both abandoned error = %v", err)
	}
	if len(drift) != 0 {
		t.Fatalf("expected 0 drift entries when all versions abandoned, got %d", len(drift))
	}
}

// statelessPublishRepo wraps InMemoryNostrEventRepository to strip PublishState
// and PublishTarget on reads, matching the behaviour of LocalEventRepository
// which stores only the signed event (no PostgreSQL publish metadata).
type statelessPublishRepo struct {
	*repositorytest.InMemoryNostrEventRepository
}

func (r *statelessPublishRepo) GetByID(ctx context.Context, id string) (*repository.NostrEventRecord, error) {
	rec, err := r.InMemoryNostrEventRepository.GetByID(ctx, id)
	if rec != nil {
		rec.PublishState = ""
		rec.PublishTarget = ""
	}
	return rec, err
}

func (r *statelessPublishRepo) ListByKind(ctx context.Context, kind int, limit int) ([]repository.NostrEventRecord, error) {
	recs, err := r.InMemoryNostrEventRepository.ListByKind(ctx, kind, limit)
	for i := range recs {
		recs[i].PublishState = ""
		recs[i].PublishTarget = ""
	}
	return recs, err
}

func (r *statelessPublishRepo) ListByKinds(ctx context.Context, kinds []int, limit int) ([]repository.NostrEventRecord, error) {
	recs, err := r.InMemoryNostrEventRepository.ListByKinds(ctx, kinds, limit)
	for i := range recs {
		recs[i].PublishState = ""
		recs[i].PublishTarget = ""
	}
	return recs, err
}
