package controlplane

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func stageStatusTestRun(t *testing.T, outbox *localstore.Outbox, service nostr.SecretKey) (localstore.BackupRunAdmission, nostr.Event) {
	return stageStatusTestRunWithID(t, outbox, service, "intent-"+uuid.NewString())
}

func stageStatusTestRunWithID(t *testing.T, outbox *localstore.Outbox, service nostr.SecretKey, intentID string) (localstore.BackupRunAdmission, nostr.Event) {
	t.Helper()
	actor := nostr.Generate().Public().Hex()
	coordinate := "backup-run:" + uuid.NewString()
	requestID := nostr.Generate().Public().Hex()
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", coordinate}, {"t", kinds.CPStateTopicBackupRun}, {"domain", "backup"},
		{"schema", kinds.CASControlStateSchema}, {"legacy_kind", "31996"}, {"deleted", "false"},
	}, Content: `{"deleted":false}`}
	require.NoError(t, event.Sign(service))
	record, inserted, err := outbox.EnqueueBackupRun(localstore.OutboxEntry{Event: event, Target: "control-plane"}, intentID, coordinate, requestID, actor)
	require.NoError(t, err)
	require.True(t, inserted)
	return record, event
}

func testBackupStatusReconciler(t *testing.T, outbox *localstore.Outbox, service nostr.SecretKey, wakes *int) *BackupRunStatusReconciler {
	t.Helper()
	signer, err := NewPrivateKeySigner(service.Hex())
	require.NoError(t, err)
	status := NewIntentStatusPublisher(func(_ context.Context, _ nostr.Event) error { return nil }, signer, zap.NewNop())
	reconciler, err := NewBackupRunStatusReconciler(outbox, status, "", func() { *wakes++ }, nil, zap.NewNop())
	require.NoError(t, err)
	return reconciler
}

func TestBackupRunFinalStatusRequiresExactQuorumAndSurvivesPruneRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	outbox, err := localstore.OpenOutbox(path)
	require.NoError(t, err)
	service := nostr.Generate()
	record, state := stageStatusTestRun(t, outbox, service)
	wakes := 0
	reconciler := testBackupStatusReconciler(t, outbox, service, &wakes)
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Equal(t, 0, wakes)
	policy := localstore.DeliveryPolicy{WriteRelays: []string{"wss://a.example", "wss://b.example"}, Required: 2}
	_, err = outbox.CommitPublisherRound(state.ID, localstore.OutboxRound{Target: "control-plane", State: localstore.OutboxPending, Policy: policy,
		Relays: map[string]localstore.RelayDelivery{"wss://a.example": {Accepted: true}}})
	require.NoError(t, err)
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Equal(t, 0, wakes)
	_, err = outbox.CommitRound(state.ID, localstore.OutboxRound{Target: "control-plane", Delivered: true, State: localstore.OutboxPublished, Policy: policy,
		Relays: map[string]localstore.RelayDelivery{"wss://a.example": {Accepted: true}, "wss://b.example": {Accepted: true}}})
	require.ErrorContains(t, err, "publisher path")
	_, err = outbox.CommitPublisherRound(state.ID, localstore.OutboxRound{Target: "control-plane", Delivered: true, State: localstore.OutboxPublished, Policy: policy,
		Relays: map[string]localstore.RelayDelivery{"wss://a.example": {Accepted: true}, "wss://b.example": {Accepted: true}}, At: time.Now().Add(-48 * time.Hour)})
	require.NoError(t, err)
	_, err = outbox.Prune(time.Now().Add(-24*time.Hour), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	_, found, err := outbox.Get(state.ID)
	require.NoError(t, err)
	require.True(t, found, "unsettled status pins the exact signed run-state row")
	require.NoError(t, outbox.Close())
	outbox, err = localstore.OpenOutbox(path)
	require.NoError(t, err)
	defer func() { _ = outbox.Close() }()
	reconciler = testBackupStatusReconciler(t, outbox, service, &wakes)
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Equal(t, 1, wakes)
	current, err := outbox.GetBackupRunAdmission(record.IntentID, record.Coordinate, record.RequestEventID)
	require.NoError(t, err)
	require.True(t, current.Delivered)
	require.Equal(t, "accepted", current.StatusOutcome)
	require.False(t, current.StatusDelivered, "durable queueing is not status relay acceptance")
	statusID, err := nostr.IDFromHex(current.StatusEventID)
	require.NoError(t, err)
	statusEntry, found, err := outbox.Get(statusID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, localstore.OutboxPending, statusEntry.State)
	require.Equal(t, "accepted", backupReceiptTag(statusEntry.Event.Tags, "status"))
	require.Equal(t, record.RequestEventID, backupReceiptTag(statusEntry.Event.Tags, "e"))
	require.Empty(t, backupReceiptTag(statusEntry.Event.Tags, "expiration"), "the pinned same-ID retry must not expire")
	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(statusEntry.Event.Content), &content))
	require.Equal(t, "applied", content["result"])
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Equal(t, 1, wakes, "replay and startup must not sign a duplicate accepted result")
	_, err = outbox.Prune(time.Now().Add(-24*time.Hour), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	_, found, err = outbox.Get(state.ID)
	require.NoError(t, err)
	require.True(t, found, "run state stays pinned until accepted status reaches quorum")
	_, err = outbox.CommitPublisherRound(statusID, localstore.OutboxRound{Target: "", State: localstore.OutboxFailed,
		Relays: map[string]localstore.RelayDelivery{"wss://status.example": {Rejected: "blocked"}}, At: time.Now().Add(-48 * time.Hour)})
	require.NoError(t, err)
	_, err = outbox.Prune(time.Now().Add(-24*time.Hour), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	statusEntry, found, err = outbox.Get(statusID)
	require.NoError(t, err)
	require.True(t, found, "failed accepted status keeps its exact signed event for retry")
	require.NoError(t, outbox.Close())
	outbox, err = localstore.OpenOutbox(path)
	require.NoError(t, err)
	reconciler = testBackupStatusReconciler(t, outbox, service, &wakes)
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Equal(t, 1, wakes, "restart cannot sign a replacement status after refusal")
	_, err = outbox.Retry(statusID)
	require.NoError(t, err)
	_, err = outbox.CommitRound(statusID, localstore.OutboxRound{Target: "", Delivered: true, State: localstore.OutboxPublished,
		Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://status.example"}, Required: 1}, Relays: map[string]localstore.RelayDelivery{"wss://status.example": {Accepted: true}}})
	require.ErrorContains(t, err, "publisher path")
	_, err = outbox.CommitPublisherRound(statusID, localstore.OutboxRound{Target: "", Delivered: true, State: localstore.OutboxPublished,
		Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://status.example"}, Required: 1}, Relays: map[string]localstore.RelayDelivery{"wss://status.example": {Accepted: true}}, At: time.Now().Add(-48 * time.Hour)})
	require.NoError(t, err)
	current, err = outbox.GetBackupRunAdmission(record.IntentID, record.Coordinate, record.RequestEventID)
	require.NoError(t, err)
	require.True(t, current.StatusDelivered)
	proof, found, err := outbox.GetDeliveryProof(statusID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, proof.ValidFor(statusEntry.Event, ""))
	_, err = outbox.Prune(time.Now().Add(-24*time.Hour), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	_, found, err = outbox.Get(state.ID)
	require.NoError(t, err)
	require.False(t, found, "exact run delivery proof survives settled row pruning")
	_, found, err = outbox.Get(statusID)
	require.NoError(t, err)
	require.False(t, found, "exact status delivery proof survives settled row pruning")
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Equal(t, 1, wakes)
}

func TestBackupRunOutboxRefusalNeverSignsFalseStatusAndPinsSameEvent(t *testing.T) {
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	defer outbox.Close()
	service := nostr.Generate()
	record, state := stageStatusTestRun(t, outbox, service)
	wakes := 0
	reconciler := testBackupStatusReconciler(t, outbox, service, &wakes)
	_, err = outbox.CommitPublisherRound(state.ID, localstore.OutboxRound{Target: "control-plane", State: localstore.OutboxFailed,
		Relays: map[string]localstore.RelayDelivery{"wss://a.example": {Rejected: "blocked"}}, At: time.Now().Add(-48 * time.Hour)})
	require.NoError(t, err)
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Equal(t, 0, wakes, "abandonment is not proof that no relay holds a staged run")
	_, err = outbox.Prune(time.Now().Add(-24*time.Hour), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	entry, found, err := outbox.Get(state.ID)
	require.NoError(t, err)
	require.True(t, found, "failed delivery cannot discard the signed event")
	require.Equal(t, state.ID, entry.Event.ID)
	current, err := outbox.GetBackupRunAdmission(record.IntentID, record.Coordinate, record.RequestEventID)
	require.NoError(t, err)
	require.False(t, current.Delivered)
	require.Empty(t, current.StatusEventID)
	_, err = outbox.Retry(state.ID)
	require.NoError(t, err, "the same exact signed state may be retried")
	policy := localstore.DeliveryPolicy{WriteRelays: []string{"wss://a.example"}, Required: 1}
	_, err = outbox.CommitPublisherRound(state.ID, localstore.OutboxRound{Target: "control-plane", State: localstore.OutboxPublished,
		Delivered: true, Policy: policy, Relays: map[string]localstore.RelayDelivery{"wss://a.example": {Accepted: true}}})
	require.NoError(t, err)
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Equal(t, 1, wakes)
	current, err = outbox.GetBackupRunAdmission(record.IntentID, record.Coordinate, record.RequestEventID)
	require.NoError(t, err)
	require.Equal(t, "accepted", current.StatusOutcome)
}

func TestBackupAcceptedStatusUsesOperatorRelaysAndBeatsSameSecondRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	outbox, err := localstore.OpenOutbox(path)
	require.NoError(t, err)
	service := nostr.Generate()
	record, state := stageStatusTestRun(t, outbox, service)
	signer, err := NewPrivateKeySigner(service.Hex())
	require.NoError(t, err)
	status := NewIntentStatusPublisher(func(context.Context, nostr.Event) error { return nil }, signer, zap.NewNop())
	requestID, err := nostr.IDFromHex(record.RequestEventID)
	require.NoError(t, err)
	intent := &Intent{IntentID: record.IntentID, Actor: record.Actor, Coordinate: record.Coordinate, Event: &nostr.Event{ID: requestID}}
	rejectedAt := nostr.Now()
	rejected, err := status.buildStatusEventWithExpiryAt(t.Context(), intent, "rejected", "rejected", "invalid before admission", nil, time.Hour, rejectedAt)
	require.NoError(t, err)
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: rejected, Target: "operator-relays"})
	require.NoError(t, err)
	_, err = outbox.CommitRound(rejected.ID, localstore.OutboxRound{Target: "operator-relays", State: localstore.OutboxPublished, At: time.Now().Add(-48 * time.Hour)})
	require.NoError(t, err)
	_, err = outbox.Prune(time.Now().Add(-24*time.Hour), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.NoError(t, outbox.Close())
	outbox, err = localstore.OpenOutbox(path)
	require.NoError(t, err)
	defer outbox.Close()
	floor, err := outbox.BackupRunStatusTimestampFloor(record.ServicePubkey, record.Actor, record.Coordinate)
	require.NoError(t, err)
	require.Equal(t, rejectedAt, floor, "pruning and restart cannot forget a relay-visible rejection")
	policy := localstore.DeliveryPolicy{WriteRelays: []string{"wss://control.example"}, Required: 1}
	_, err = outbox.CommitPublisherRound(state.ID, localstore.OutboxRound{Target: "control-plane", State: localstore.OutboxPublished, Delivered: true,
		Policy: policy, Relays: map[string]localstore.RelayDelivery{"wss://control.example": {Accepted: true}}})
	require.NoError(t, err)
	wakes := 0
	reconciler, err := NewBackupRunStatusReconciler(outbox, status, "operator-relays", func() { wakes++ }, nil, zap.NewNop())
	require.NoError(t, err)
	stale := *intent
	stale.StatusData = map[string]any{"run_id": record.Coordinate[len("backup-run:"):], "state_event_id": record.StateEventID, "execution": "paused"}
	tied, err := status.buildStatusEventWithExpiryAt(t.Context(), &stale, "accepted", "applied", "", nil, 0, rejectedAt)
	require.NoError(t, err)
	_, _, err = outbox.StageBackupRunAcceptedStatus(record, tied, "operator-relays")
	require.ErrorContains(t, err, "not newer than the durable coordinate floor")
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Equal(t, 1, wakes)
	current, err := outbox.GetBackupRunAdmission(record.IntentID, record.Coordinate, record.RequestEventID)
	require.NoError(t, err)
	statusID, err := nostr.IDFromHex(current.StatusEventID)
	require.NoError(t, err)
	accepted, found, err := outbox.Get(statusID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "operator-relays", accepted.Target, "the CLI-observed relay topology must carry final status")
	require.Equal(t, backupReceiptTag(rejected.Tags, "d"), backupReceiptTag(accepted.Event.Tags, "d"))
	require.Greater(t, accepted.Event.CreatedAt, rejected.CreatedAt, "NIP-01 must select accepted regardless of event ID tie-break")
	_, err = outbox.CommitPublisherRound(statusID, localstore.OutboxRound{Target: "control-plane", State: localstore.OutboxPublished, Delivered: true,
		Policy: policy, Relays: map[string]localstore.RelayDelivery{"wss://control.example": {Accepted: true}}})
	require.ErrorContains(t, err, "differs from outbox target")
	operatorPolicy := localstore.DeliveryPolicy{WriteRelays: []string{"wss://operator.example"}, Required: 1}
	_, err = outbox.CommitPublisherRound(statusID, localstore.OutboxRound{Target: "operator-relays", State: localstore.OutboxPublished, Delivered: true,
		Policy: operatorPolicy, Relays: map[string]localstore.RelayDelivery{"wss://operator.example": {Accepted: true}}})
	require.NoError(t, err)
	current, err = outbox.GetBackupRunAdmission(record.IntentID, record.Coordinate, record.RequestEventID)
	require.NoError(t, err)
	require.True(t, current.StatusDelivered)
}

func TestAdmittedBackupCoordinateCannotPublishLaterGenericStatus(t *testing.T) {
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	defer outbox.Close()
	service := nostr.Generate()
	record, _ := stageStatusTestRun(t, outbox, service)
	signer, err := NewPrivateKeySigner(service.Hex())
	require.NoError(t, err)
	statuses := &statusCollector{}
	publisher := NewIntentStatusPublisher(statuses.publish, signer, zap.NewNop())
	publisher.SetBackupRunAdmissionGuard(outbox.HasBackupRunAdmissionCoordinate)
	requestID, err := nostr.IDFromHex(record.RequestEventID)
	require.NoError(t, err)
	intent := &Intent{IntentID: record.IntentID, Actor: record.Actor, Coordinate: record.Coordinate, Event: &nostr.Event{ID: requestID}}
	require.ErrorContains(t, publisher.PublishRejectionChecked(t.Context(), intent, "permission revoked"), "immutable admission outcome")
	require.ErrorContains(t, publisher.PublishAcceptedChecked(t.Context(), intent), "immutable admission outcome")
	require.Empty(t, statuses.events, "preauthorization and validation paths share the guarded publisher")
	intent.Coordinate = "backup-run:" + uuid.NewString()
	require.NoError(t, publisher.PublishRejectionChecked(t.Context(), intent, "invalid request"))
	require.Len(t, statuses.events, 1, "an unadmitted request can still receive a bounded refusal")
}

func TestBackupStatusReconciliationContinuesAfterEarlierBadAdmission(t *testing.T) {
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	defer outbox.Close()
	wrongSigner, service := nostr.Generate(), nostr.Generate()
	bad, badEvent := stageStatusTestRunWithID(t, outbox, wrongSigner, "intent-a")
	good, goodEvent := stageStatusTestRunWithID(t, outbox, service, "intent-z")
	policy := localstore.DeliveryPolicy{WriteRelays: []string{"wss://relay.example"}, Required: 1}
	for _, event := range []nostr.Event{badEvent, goodEvent} {
		_, err = outbox.CommitPublisherRound(event.ID, localstore.OutboxRound{Target: "control-plane", State: localstore.OutboxPublished,
			Delivered: true, Policy: policy, Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}})
		require.NoError(t, err)
	}
	wakes := 0
	reconciler := testBackupStatusReconciler(t, outbox, service, &wakes)
	err = reconciler.ReconcileOnce(t.Context())
	require.ErrorContains(t, err, "intent-a")
	require.NotEmpty(t, reconciler.LastError(), "the failure remains visible for health and retry")
	badCurrent, err := outbox.GetBackupRunAdmission(bad.IntentID, bad.Coordinate, bad.RequestEventID)
	require.NoError(t, err)
	require.Empty(t, badCurrent.StatusEventID)
	goodCurrent, err := outbox.GetBackupRunAdmission(good.IntentID, good.Coordinate, good.RequestEventID)
	require.NoError(t, err)
	require.NotEmpty(t, goodCurrent.StatusEventID, "later ACKed records cannot starve behind one failure")
	require.Equal(t, 1, wakes)
	signer, err := NewPrivateKeySigner(wrongSigner.Hex())
	require.NoError(t, err)
	reconciler.status = NewIntentStatusPublisher(func(context.Context, nostr.Event) error { return nil }, signer, zap.NewNop())
	require.NoError(t, reconciler.ReconcileOnce(t.Context()))
	require.Empty(t, reconciler.LastError())
	badCurrent, err = outbox.GetBackupRunAdmission(bad.IntentID, bad.Coordinate, bad.RequestEventID)
	require.NoError(t, err)
	require.NotEmpty(t, badCurrent.StatusEventID)
}
