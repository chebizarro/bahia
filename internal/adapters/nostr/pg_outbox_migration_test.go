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
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

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
