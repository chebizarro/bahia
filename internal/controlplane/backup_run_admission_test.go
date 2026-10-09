package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type testBackupRunAdmission struct {
	events *localstore.Store
	outbox *localstore.Outbox
	key    nostr.SecretKey
}

func (s testBackupRunAdmission) LookupRunAdmission(_ context.Context, intentID, coordinate, requestID string) (string, bool, bool, error) {
	entry, err := s.outbox.GetBackupRunAdmission(intentID, coordinate, requestID)
	if err != nil || entry == nil {
		return "", false, false, err
	}
	return entry.StateEventID, true, entry.Delivered && entry.StatusEventID != "", nil
}

func (s testBackupRunAdmission) StageRunAdmission(_ context.Context, intentID, requestID string, run *domain.BackupRun) (string, error) {
	event, err := backupReceiptEventForAdmission(s.key, run)
	if err != nil {
		return "", err
	}
	entry, inserted, err := s.outbox.EnqueueBackupRun(localstore.OutboxEntry{Event: event, Target: "control-plane"}, intentID, run.RequestDTag, requestID, run.RequestedBy)
	if err != nil {
		return "", err
	}
	if inserted {
		_, err = s.events.SaveEvent(event)
	}
	return entry.StateEventID, err
}

func backupReceiptEventForAdmission(key nostr.SecretKey, run *domain.BackupRun) (nostr.Event, error) {
	tags, content := nostradapter.BackupRunStateRecord(run, nil)
	wireKind, envelope := nostradapter.ControlStateEnvelope(kinds.BackupRunState, nostradapter.BackupRunDTag(run.ID), false)
	event := nostr.Event{Kind: nostr.Kind(wireKind), CreatedAt: nostr.Now(), Tags: append(envelope, tags...), Content: content}
	return event, event.Sign(key)
}

func TestBackupRunAdmissionPendingUntilACKThenAcceptedOnReplay(t *testing.T) {
	dir := t.TempDir()
	eventPath, outboxPath := filepath.Join(dir, "events.db"), filepath.Join(dir, "outbox.db")
	events, err := localstore.Open(eventPath)
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = events.Close(); _ = outbox.Close() })
	serviceKey, operatorKey := nostr.Generate(), nostr.Generate()
	request := signedBackupRunFixture(t, operatorKey, nil)
	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(request.Content), &content))
	var snapshot domain.BackupExecutionSnapshot
	raw, err := json.Marshal(content["execution_snapshot"])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	recipeTags, recipeContent := nostradapter.BackupRecipeRegistryRecord(&snapshot.Recipe, false)
	recipeEvent := configSnapshotEvent(t, serviceKey, kinds.BackupRecipeRegistry, kinds.CPStateTopicBackupRecipe,
		nostradapter.BackupRecipeDTag(snapshot.Recipe.ID), recipeTags, recipeContent)
	repoTags, repoContent := nostradapter.BackupRepositoryRegistryRecord(&snapshot.Repository, false)
	repoEvent := configSnapshotEvent(t, serviceKey, kinds.BackupRepositoryRegistry, kinds.CPStateTopicBackupRepository,
		nostradapter.BackupRepositoryDTag(snapshot.Repository.ID), repoTags, repoContent)
	policyTags, policyContent := nostradapter.BackupPolicyRegistryRecord(snapshot.Policy, false)
	policyEvent := configSnapshotEvent(t, serviceKey, kinds.BackupPolicyRegistry, kinds.CPStateTopicBackupPolicy,
		nostradapter.BackupPolicyDTag(snapshot.Policy.ID), policyTags, policyContent)
	snapshot.RecipeEventID, snapshot.RepositoryEventID, snapshot.PolicyEventID = recipeEvent.ID.Hex(), repoEvent.ID.Hex(), policyEvent.ID.Hex()
	content["execution_snapshot"] = snapshot
	encoded, err := json.Marshal(content)
	require.NoError(t, err)
	request.Content = string(encoded)
	require.NoError(t, request.Sign(operatorKey))
	for _, ev := range []nostr.Event{recipeEvent, repoEvent, policyEvent} {
		_, err = events.SaveEvent(ev)
		require.NoError(t, err)
		_, err = outbox.Enqueue(localstore.OutboxEntry{Event: ev, Target: "control-plane"})
		require.NoError(t, err)
		_, err = outbox.CommitPublisherRound(ev.ID, localstore.OutboxRound{Target: "control-plane", Rounds: 1, Delivered: true, State: localstore.OutboxPublished,
			Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://relay.example"}, Required: 1}, Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}})
		require.NoError(t, err)
	}
	statuses := &statusCollector{}
	newProcessor := func() *IntentProcessor {
		reader, err := NewLocalBackupRunReceipts(events, outbox, serviceKey.Public().Hex())
		require.NoError(t, err)
		processor := NewIntentProcessor(NewTrustSet([]string{operatorKey.Public().Hex()}, zap.NewNop()), openTestStore(t),
			NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
			IntentProcessorConfig{EnabledDomains: map[string]bool{"backup": true}}, zap.NewNop())
		processor.RegisterHandler("backup", NewBackupIntentHandler(BackupIntentHandlerConfig{
			RunReceipts: reader, RunAdmission: testBackupRunAdmission{events: events, outbox: outbox, key: serviceKey}, Logger: zap.NewNop(),
		}))
		return processor
	}
	processor := newProcessor()
	process := func() *Intent {
		intent, err := ParseIntent(&request)
		require.NoError(t, err)
		intent.Actor = operatorKey.Public().Hex()
		require.NoError(t, processor.ProcessInProcess(t.Context(), intent))
		return intent
	}
	first := process()
	require.Empty(t, statuses.events, "provisional status must not race the final ACK result")
	require.False(t, processor.IsProcessed(first.IntentID))
	stateID, ok := first.Result["state_event_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, stateID)
	require.NoError(t, events.Close())
	require.NoError(t, outbox.Close())
	require.NoError(t, os.Remove(eventPath), "the inbound cache is rebuildable, unlike the admission outbox")
	events, err = localstore.Open(eventPath)
	require.NoError(t, err)
	outbox, err = localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	processor = newProcessor()
	second := process()
	require.Equal(t, stateID, second.Result["state_event_id"])
	require.Empty(t, statuses.events)
	id, err := nostr.IDFromHex(stateID)
	require.NoError(t, err)
	_, err = outbox.CommitPublisherRound(id, localstore.OutboxRound{Target: "control-plane", Rounds: 1, Delivered: false, State: localstore.OutboxFailed,
		Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Rejected: "blocked"}}})
	require.NoError(t, err)
	process()
	require.False(t, processor.IsProcessed(first.IntentID), "a refused relay cannot accept a run")
	require.Empty(t, statuses.events, "provisional status must not race the final ACK result")
	_, err = outbox.Retry(id)
	require.NoError(t, err)
	_, err = outbox.CommitPublisherRound(id, localstore.OutboxRound{Target: "control-plane", Rounds: 1, Delivered: true, State: localstore.OutboxPending,
		Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://second.example"}, Required: 1}, Relays: map[string]localstore.RelayDelivery{"wss://second.example": {Accepted: true}}})
	require.NoError(t, err)
	process()
	require.False(t, processor.IsProcessed(first.IntentID), "run ACK without a durable accepted status is still pending")
	signer, err := NewPrivateKeySigner(serviceKey.Hex())
	require.NoError(t, err)
	statusReconciler, err := NewBackupRunStatusReconciler(outbox,
		NewIntentStatusPublisher(statuses.publish, signer, zap.NewNop()), "", func() {}, nil, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, statusReconciler.ReconcileOnce(t.Context()))
	accepted := process()
	require.True(t, processor.IsProcessed(first.IntentID))
	require.Empty(t, statuses.events, "replay must not mint a duplicate accepted status")
	require.Equal(t, stateID, accepted.Result["state_event_id"])
	conflicting := request
	conflicting.Tags = make(nostr.Tags, len(request.Tags))
	for i, tag := range request.Tags {
		conflicting.Tags[i] = append(nostr.Tag(nil), tag...)
	}
	for i := range conflicting.Tags {
		if conflicting.Tags[i][0] == "intent_id" {
			conflicting.Tags[i][1] = "different-intent"
		}
	}
	require.NoError(t, conflicting.Sign(operatorKey))
	conflictIntent, err := ParseIntent(&conflicting)
	require.NoError(t, err)
	conflictIntent.Actor = operatorKey.Public().Hex()
	require.ErrorContains(t, processor.ProcessInProcess(t.Context(), conflictIntent), "coordinate already belongs")
	require.False(t, processor.IsProcessed(conflictIntent.IntentID))
	conflicting = request
	conflicting.Content += " "
	require.NoError(t, conflicting.Sign(operatorKey))
	conflictIntent, err = ParseIntent(&conflicting)
	require.NoError(t, err)
	conflictIntent.Actor = operatorKey.Public().Hex()
	require.ErrorContains(t, processor.ProcessInProcess(t.Context(), conflictIntent), "intent id conflicts")
	_, err = outbox.CommitPublisherRound(id, localstore.OutboxRound{Target: "control-plane", Rounds: 3, Delivered: true, State: localstore.OutboxPublished})
	require.NoError(t, err)
	process()
	require.Empty(t, statuses.events, "replay must not mint a duplicate accepted status")
	// The immutable admission is still authoritative after delivery-row pruning.
	_, err = outbox.Prune(time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	process()
	require.Empty(t, statuses.events, "replay must not mint a duplicate accepted status")
}
