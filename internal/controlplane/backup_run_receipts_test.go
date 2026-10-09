package controlplane

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestLocalBackupRunReceiptsRequireSignedACKAndSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	eventPath, outboxPath := filepath.Join(dir, "events.db"), filepath.Join(dir, "outbox.db")
	events, err := localstore.Open(eventPath)
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = events.Close(); _ = outbox.Close() })
	secret := nostr.Generate()
	reader, err := NewLocalBackupRunReceipts(events, outbox, secret.Public().Hex())
	require.NoError(t, err)
	run := backupReceiptRun(backupReceiptRequest(t, nostr.Generate()), domain.RunStatusQueued)
	record, err := reader.GetBackupRunReceipt(t.Context(), run.ID)
	require.NoError(t, err)
	require.Nil(t, record, "absent canonical state cannot be inferred from SQL")

	event := backupReceiptEvent(t, secret, run)
	_, err = events.SaveEvent(event)
	require.NoError(t, err)
	_, err = reader.GetBackupRunReceipt(t.Context(), run.ID)
	require.ErrorContains(t, err, "no ACKed relay delivery receipt", "a signed local-only record is insufficient")
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: "control-plane"})
	require.NoError(t, err)
	_, err = reader.GetBackupRunReceipt(t.Context(), run.ID)
	require.ErrorContains(t, err, "no ACKed relay delivery receipt", "queued delivery is not accepted")
	_, err = outbox.CommitRound(event.ID, localstore.OutboxRound{Rounds: 1, Delivered: true,
		Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Rejected: "blocked"}}, State: localstore.OutboxPending})
	require.NoError(t, err)
	_, err = reader.GetBackupRunReceipt(t.Context(), run.ID)
	require.ErrorContains(t, err, "no ACKed relay delivery receipt", "a delivery flag without an accepted relay is ambiguous")
	_, err = outbox.CommitRound(event.ID, localstore.OutboxRound{Rounds: 1, Delivered: true,
		Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}, State: localstore.OutboxPublished})
	require.NoError(t, err)
	record, err = reader.GetBackupRunReceipt(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, run.ID, record.ID)
	require.Equal(t, run.RequestEventID, record.RequestEventID)

	require.NoError(t, events.Close())
	require.NoError(t, outbox.Close())
	events, err = localstore.Open(eventPath)
	require.NoError(t, err)
	outbox, err = localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	reader, err = NewLocalBackupRunReceipts(events, outbox, secret.Public().Hex())
	require.NoError(t, err)
	record, err = reader.GetBackupRunReceipt(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, run.ID, record.ID, "signed state and relay ACK must survive restart")
}

func TestLocalBackupRunReceiptsRejectTamperedAndMismatchedState(t *testing.T) {
	for _, variant := range []string{"tampered content", "wrong family", "wrong content id", "wrong author", "wrong publish target"} {
		t.Run(variant, func(t *testing.T) {
			events, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
			require.NoError(t, err)
			defer events.Close()
			outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
			require.NoError(t, err)
			defer outbox.Close()
			secret := nostr.Generate()
			run := backupReceiptRun(backupReceiptRequest(t, nostr.Generate()), domain.RunStatusSucceeded)
			event := backupReceiptEvent(t, secret, run)
			switch variant {
			case "tampered content":
				event.Content += " "
			case "wrong family":
				for i := range event.Tags {
					if event.Tags[i][0] == "legacy_kind" {
						event.Tags[i][1] = "31995"
					}
				}
				require.NoError(t, event.Sign(secret))
			case "wrong content id":
				event.Content = `{"id":"` + uuid.NewString() + `","deleted":false}`
				require.NoError(t, event.Sign(secret))
			case "wrong author":
				require.NoError(t, event.Sign(nostr.Generate()))
			}
			_, err = events.SaveEvent(event)
			require.NoError(t, err)
			target := "control-plane"
			if variant == "wrong publish target" {
				target = "default"
			}
			_, err = outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: target})
			require.NoError(t, err)
			_, err = outbox.CommitRound(event.ID, localstore.OutboxRound{Rounds: 1, Delivered: true,
				Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}, State: localstore.OutboxPublished})
			require.NoError(t, err)
			reader, err := NewLocalBackupRunReceipts(events, outbox, secret.Public().Hex())
			require.NoError(t, err)
			got, err := reader.GetBackupRunReceipt(t.Context(), run.ID)
			if variant == "wrong author" {
				require.NoError(t, err)
				require.Nil(t, got)
			} else {
				require.Error(t, err)
				require.Nil(t, got)
			}
		})
	}
}

func TestBackupIntentRunReceiptIsReadOnlyAndStillRejects(t *testing.T) {
	serviceKey := nostr.Generate()
	request := backupReceiptRequest(t, nostr.Generate())
	run := backupReceiptRun(request, domain.RunStatusQueued)
	events, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	defer events.Close()
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	defer outbox.Close()
	receipt := backupReceiptEvent(t, serviceKey, run)
	_, err = events.SaveEvent(receipt)
	require.NoError(t, err)
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: receipt, Target: "control-plane"})
	require.NoError(t, err)
	_, err = outbox.CommitRound(receipt.ID, localstore.OutboxRound{Rounds: 1, Delivered: true,
		Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}, State: localstore.OutboxPublished})
	require.NoError(t, err)
	reader, err := NewLocalBackupRunReceipts(events, outbox, serviceKey.Public().Hex())
	require.NoError(t, err)
	registry := newFakeBackupIntentRegistry()
	registry.runs[run.ID] = &domain.BackupRun{ID: run.ID, Status: domain.RunStatusSucceeded}
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{Registry: registry, RunReceipts: reader, Logger: zap.NewNop()})
	intent := &Intent{Op: "run", Actor: request.PubKey.Hex(), Coordinate: run.RequestDTag, Event: &request,
		Content: map[string]any{"id": run.ID.String(), "recipe_id": run.RecipeID.String()}}
	require.ErrorContains(t, handler.HandleIntent(t.Context(), intent), "ACKed canonical run is queued")
	statuses := &statusCollector{}
	processor := NewIntentProcessor(NewTrustSet([]string{request.PubKey.Hex()}, zap.NewNop()), openTestStore(t),
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"backup": true}}, zap.NewNop())
	processor.RegisterHandler("backup", handler)
	intent.Domain, intent.OrgID, intent.IntentID = "backup", testOrgID(), "backup-run-receipt-refusal"
	require.ErrorContains(t, processor.ProcessInProcess(t.Context(), intent), "ACKed canonical run is queued")
	require.ErrorContains(t, processor.ProcessInProcess(t.Context(), intent), "ACKed canonical run is queued")
	require.Len(t, statuses.events, 2, "duplicate requests remain rejected rather than becoming accepted markers")
	require.Equal(t, "rejected", backupReceiptTag(statuses.events[0].Tags, "status"))
	require.Equal(t, "rejected", backupReceiptTag(statuses.events[1].Tags, "status"))
	require.False(t, processor.IsProcessed(intent.IntentID), "a refusal must not become an accepted replay marker")
	intent.Content["recipe_id"] = uuid.NewString()
	require.ErrorContains(t, handler.HandleIntent(t.Context(), intent), "conflicts with an ACKed canonical request")
	require.Zero(t, registry.workflowCreates, "SQL-only or divergent rows cannot drive intake")

	missing := &Intent{Op: "run", Actor: request.PubKey.Hex(), Coordinate: run.RequestDTag, Event: &request,
		Content: map[string]any{"id": uuid.NewString(), "recipe_id": run.RecipeID.String()}}
	require.ErrorContains(t, handler.HandleIntent(t.Context(), missing), "no ACKed canonical run-state receipt")
	require.Zero(t, registry.workflowCreates)

	run.Status = domain.RunStatusSucceeded
	terminal := backupReceiptEvent(t, serviceKey, run)
	terminal.CreatedAt = receipt.CreatedAt + 1
	require.NoError(t, terminal.Sign(serviceKey))
	_, err = events.SaveEvent(terminal)
	require.NoError(t, err)
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: terminal, Target: "control-plane"})
	require.NoError(t, err)
	_, err = outbox.CommitRound(terminal.ID, localstore.OutboxRound{Rounds: 1, Delivered: true,
		Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}, State: localstore.OutboxPublished})
	require.NoError(t, err)
	intent.Content["recipe_id"] = run.RecipeID.String()
	require.ErrorContains(t, handler.HandleIntent(t.Context(), intent), "ACKed canonical terminal run exists")
	require.Zero(t, registry.workflowCreates)
}

func backupReceiptRequest(t *testing.T, key nostr.SecretKey) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "backup-run-request:test"}}, Content: `{}`}
	require.NoError(t, event.Sign(key))
	return event
}

func backupReceiptRun(request nostr.Event, status domain.DeploymentRunStatus) domain.BackupRun {
	return domain.BackupRun{ID: uuid.New(), RecipeID: uuid.New(), RepositoryID: uuid.New(), RequestedBy: request.PubKey.Hex(),
		RequestEventID: request.ID.Hex(), RequestKind: int(request.Kind), RequestDTag: backupReceiptTag(request.Tags, "d"),
		Status: status, Backend: domain.BackupBackendKopia, TargetRef: "/data", VerificationStatus: domain.BackupVerificationPending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}

func backupReceiptEvent(t *testing.T, key nostr.SecretKey, run domain.BackupRun) nostr.Event {
	t.Helper()
	tags, content := nostradapter.BackupRunStateRecord(&run, nil)
	tags = append(nostr.Tags{{"d", nostradapter.BackupRunDTag(run.ID)}, {"domain", "backup"},
		{"schema", kinds.CASControlStateSchema}, {"legacy_kind", strconv.Itoa(kinds.BackupRunState)},
		{"deleted", "false"}, {"t", kinds.CPStateTopicBackupRun}}, tags...)
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: tags, Content: content}
	require.NoError(t, event.Sign(key))
	return event
}
