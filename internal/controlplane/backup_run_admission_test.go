package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
	return entry.StateEventID, true, entry.Delivered && entry.StatusDelivered, nil
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

func (s testBackupRunAdmission) LookupPendingRun(_ context.Context, intentID, coordinate, requestID string) (*localstore.BackupRunPending, error) {
	return s.outbox.GetBackupRunPending(intentID, coordinate, requestID)
}

func (s testBackupRunAdmission) StagePendingRun(_ context.Context, intentID, coordinate string, event nostr.Event, actor string, expiresAt time.Time) (*localstore.BackupRunPending, error) {
	record, _, err := s.outbox.PutBackupRunPending(localstore.BackupRunPending{IntentID: intentID, Coordinate: coordinate, RequestEvent: event,
		Actor: actor, ServicePubkey: s.key.Public().Hex(), ReceivedAt: time.Now().UTC(), ExpiresAt: expiresAt})
	return record, err
}

func backupReceiptEventForAdmission(key nostr.SecretKey, run *domain.BackupRun) (nostr.Event, error) {
	tags, content := nostradapter.BackupRunStateRecord(run, nil)
	wireKind, envelope := nostradapter.ControlStateEnvelope(kinds.BackupRunState, nostradapter.BackupRunDTag(run.ID), false)
	event := nostr.Event{Kind: nostr.Kind(wireKind), CreatedAt: nostr.Now(), Tags: append(envelope, tags...), Content: content}
	return event, event.Sign(key)
}

func TestBackupRunAdmissionRetainsSignedPendingRequestAcrossRestart(t *testing.T) {
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
		admission := testBackupRunAdmission{events: events, outbox: outbox, key: serviceKey}
		processor.RegisterHandler("backup", NewBackupIntentHandler(BackupIntentHandlerConfig{
			RunReceipts: reader, RunAdmission: admission, RunPending: admission, Logger: zap.NewNop(),
		}))
		return processor
	}
	process := func(processor *IntentProcessor) *Intent {
		intent, err := ParseIntent(&request)
		require.NoError(t, err)
		intent.Actor = operatorKey.Public().Hex()
		require.NoError(t, processor.ProcessInProcess(t.Context(), intent))
		return intent
	}
	parsed, err := ParseIntent(&request)
	require.NoError(t, err)
	fresh := newProcessor()
	parsed.Actor = operatorKey.Public().Hex()
	require.ErrorContains(t, fresh.ProcessInProcess(t.Context(), parsed), "terminal signed status settlement is unavailable")
	unadmitted, err := outbox.GetBackupRunPending(parsed.IntentID, parsed.Coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Nil(t, unadmitted, "paused intake cannot promise an unsatisfiable pending outcome")
	statuses.mu.Lock()
	statuses.events = nil
	statuses.mu.Unlock()
	expiresAt, err := signedBackupRunExpiration(&request)
	require.NoError(t, err)
	_, inserted, err := outbox.PutBackupRunPending(localstore.BackupRunPending{
		IntentID: parsed.IntentID, Coordinate: parsed.Coordinate, RequestEvent: request,
		Actor: operatorKey.Public().Hex(), ServicePubkey: serviceKey.Public().Hex(),
		ReceivedAt: time.Now().UTC(), ExpiresAt: expiresAt,
	})
	require.NoError(t, err)
	require.True(t, inserted)
	processor := newProcessor()
	first := process(processor)
	require.Empty(t, statuses.events, "pending is not a relay status")
	require.False(t, processor.IsProcessed(first.IntentID))
	require.Equal(t, request.ID.Hex(), first.Result["request_event_id"])
	require.NotContains(t, first.Result, "state_event_id")
	pending, err := outbox.GetBackupRunPending(first.IntentID, first.Coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, localstore.BackupRunPendingState, pending.State)
	require.Equal(t, request.ID, pending.RequestEvent.ID)
	admission, err := outbox.GetBackupRunAdmission(first.IntentID, first.Coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Nil(t, admission, "no service-signed state can exist before history and fence proof")
	require.NoError(t, events.Close())
	require.NoError(t, outbox.Close())
	require.NoError(t, os.Remove(eventPath), "the inbound cache is rebuildable, unlike the pending inbox")
	events, err = localstore.Open(eventPath)
	require.NoError(t, err)
	outbox, err = localstore.OpenOutbox(outboxPath)
	require.NoError(t, err)
	processor = newProcessor()
	second := process(processor)
	require.Equal(t, first.Result, second.Result)
	require.Empty(t, statuses.events)
	require.NoError(t, outbox.RecordBackupRunPendingAttempt(first.IntentID, request.ID.Hex(), pending.ExpiresAt, time.Time{}, "missing complete proof"))
	refused, err := outbox.GetBackupRunPending(first.IntentID, first.Coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, localstore.BackupRunRefusedState, refused.State)
}

func TestBackupRunExpiredPendingReplayCannotReportPending(t *testing.T) {
	operator, service := nostr.Generate(), nostr.Generate()
	request := signedBackupRunFixture(t, operator, nil)
	now := time.Now().UTC().Truncate(time.Second)
	request.CreatedAt = nostr.Timestamp(now.Add(-12 * time.Minute).Unix())
	for i := range request.Tags {
		if len(request.Tags[i]) > 0 && request.Tags[i][0] == "expiration" {
			request.Tags[i][1] = strconv.FormatInt(now.Add(-2*time.Minute).Unix(), 10)
		}
	}
	require.NoError(t, request.Sign(operator))
	intent, err := ParseIntent(&request)
	require.NoError(t, err)
	intent.Actor = operator.Public().Hex()
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	defer outbox.Close()
	_, inserted, err := outbox.PutBackupRunPending(localstore.BackupRunPending{
		IntentID: intent.IntentID, Coordinate: intent.Coordinate, RequestEvent: request,
		Actor: intent.Actor, ServicePubkey: service.Public().Hex(),
		ReceivedAt: now.Add(-11 * time.Minute), ExpiresAt: now.Add(-2 * time.Minute),
	})
	require.NoError(t, err)
	require.True(t, inserted)
	admission := testBackupRunAdmission{outbox: outbox, key: service}
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{RunAdmission: admission, RunPending: admission, RunReceipts: &localBackupRunReceipts{}, Logger: zap.NewNop()})
	err = handler.handleRun(t.Context(), intent)
	require.ErrorIs(t, err, ErrBackupRunAdmissionConflict)
	require.ErrorContains(t, err, "expired")
	require.Nil(t, intent.Result, "expired replay cannot return pending to a one-shot client")
}
