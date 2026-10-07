package nostr

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestMigratePendingPostgresRowsToLocalOutbox verifies the one-shot startup
// migration: pending PostgreSQL outbox rows are enqueued to
// the local outbox, the PG row is re-targeted as a local archive row, and
// running the migration again is idempotent.
func TestMigratePendingPostgresRowsToLocalOutbox(t *testing.T) {
	ctx := context.Background()
	sk := gonostr.Generate()
	signer := keyer.NewPlainKeySigner(sk)
	dir := t.TempDir()

	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.Close() })

	store, err := localstore.Open(filepath.Join(dir, "daemon.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	pgRepo := &migrationTestOutbox{InMemoryNostrEventRepository: repositorytest.NewInMemoryNostrEventRepository()}

	pub := NewPublisher(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, nil, pgRepo, zap.NewNop(),
		WithLocalOutbox(outbox, store))

	// Write a pending PG row as if from a pre-upgrade daemon.
	ev := syncTestEvent(t, sk, KindCASAudit, gonostr.Now(), gonostr.Tags{{"t", "test-audit"}}, `{"test":true}`)
	require.NoError(t, signer.SignEvent(ctx, &ev))
	tagsJSON, _ := json.Marshal(ev.Tags)
	rec := &repository.NostrEventRecord{
		ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(),
		Content: ev.Content, Tags: tagsJSON, Sig: eventSignatureHex(&ev),
		CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(),
		EntityType: "artifact_registration", PublishState: repository.NostrPublishStatePending,
		PublishTarget: repository.NostrPublishTargetDefault,
	}
	_, err = pgRepo.Record(ctx, rec)
	require.NoError(t, err)

	// Run migration.
	migrated, err := pub.MigratePendingPostgresRows(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, migrated)

	// The local outbox now holds the event.
	entry, found, err := outbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found, "migrated event must be in local outbox")
	require.Equal(t, localstore.OutboxPending, entry.State)

	// The PG row was re-targeted.
	require.Len(t, pgRepo.migratedIDs, 1)
	require.Equal(t, ev.ID.Hex(), pgRepo.migratedIDs[0])

	// Second run is idempotent — the PG row is no longer pending after
	// MigrateToLocalOutbox, so ListUnpublishedAfter returns nothing.
	migrated2, err := pub.MigratePendingPostgresRows(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, migrated2, "second migration must be a no-op")
}

// TestHiveCIAuditEnqueuesToLocalOutbox verifies that hiveci audit events
// enqueue to the local outbox and publish without Postgres.
func TestHiveCIAuditEnqueuesToLocalOutbox(t *testing.T) {
	ctx := context.Background()
	sk := gonostr.Generate()
	signer := keyer.NewPlainKeySigner(sk)
	dir := t.TempDir()

	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = outbox.Close() })

	store, err := localstore.Open(filepath.Join(dir, "daemon.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// Build a LocalEventRepository with PendingAdmission that enqueues to
	// the local outbox (simulating the app.go wiring).
	var enqueuedIDs []string
	repo := NewLocalEventRepository(store, func(_ context.Context, ev gonostr.Event, target, entityType string, _ *uuid.UUID) error {
		enqueuedIDs = append(enqueuedIDs, ev.ID.Hex())
		entry := localstore.OutboxEntry{Event: ev, Target: target, EntityType: entityType, EnqueuedAt: time.Now()}
		_, err := outbox.Enqueue(entry)
		return err
	})

	// Sign and record an audit event as the hiveci audit does.
	ev := gonostr.Event{
		Kind: KindCASAudit, CreatedAt: gonostr.Now(),
		Tags:    gonostr.Tags{{"-"}, {"domain", "artifact"}, {"entity", "registration"}, {"decision", "accepted"}},
		Content: `{"schema":"bahia.audit.artifact-registration.v1","decision":"accepted"}`,
	}
	require.NoError(t, signer.SignEvent(ctx, &ev))
	tagsJSON, _ := json.Marshal(ev.Tags)
	record := &repository.NostrEventRecord{
		ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(),
		Content: ev.Content, Tags: tagsJSON, Sig: eventSignatureHex(&ev),
		CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(),
		EntityType: "artifact_registration", PublishState: repository.NostrPublishStatePending,
	}
	_, err = repo.Record(ctx, record)
	require.NoError(t, err)

	// Verify the event was admitted to the local outbox.
	require.Equal(t, []string{ev.ID.Hex()}, enqueuedIDs)
	entry, found, err := outbox.Get(ev.ID)
	require.NoError(t, err)
	require.True(t, found, "audit event must be in local outbox")
	require.Equal(t, localstore.OutboxPending, entry.State)
}

// migrationTestOutbox wraps InMemoryNostrEventRepository to implement
// MigrateToLocalOutbox and track calls.
type migrationTestOutbox struct {
	*repositorytest.InMemoryNostrEventRepository
	migratedIDs []string
}

func (m *migrationTestOutbox) MigrateToLocalOutbox(_ context.Context, id string) error {
	m.migratedIDs = append(m.migratedIDs, id)
	// Simulate re-targeting: remove from pending so second ListUnpublished
	// won't return it. We do this by marking published.
	return m.MarkPublished(context.Background(), id, time.Now())
}
