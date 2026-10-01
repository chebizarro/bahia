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
