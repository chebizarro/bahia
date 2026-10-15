package localstore

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func TestBackupRunStatusPublicationReservesBeforeRelayAndSerializesAdmission(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	service := nostr.Generate()
	actor := nostr.Generate().Public().Hex()
	coordinate := "backup-run:" + uuid.NewString()
	requestID := nostr.Generate().Public().Hex()
	status := signed(t, service, 30315, nostr.Now(), nostr.Tags{
		{"d", kinds.IntentPrivateStatusCoordinate(actor, "backup", "intent-race")}, {"domain", "intent"},
		{"t", "intent-status"}, {"status", "rejected"}, {"p", actor},
		{"intent_id", "intent-race"}, {"e", requestID},
	}, `{"intent_id":"intent-race","result":"rejected","reason":"access_denied"}`)
	relayEntered := make(chan struct{})
	releaseRelay := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseRelay)
		}
	}()
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- outbox.PublishUnadmittedBackupRunStatus(coordinate, requestID, status, func() error {
			close(relayEntered)
			<-releaseRelay
			return errors.New("relay accepted but outbox admission failed")
		})
	}()
	<-relayEntered
	floor, err := outbox.BackupRunStatusTimestampFloor(service.Public().Hex(), actor, coordinate)
	require.NoError(t, err)
	require.Equal(t, status.CreatedAt, floor, "timestamp must be durable before relay I/O")
	if outbox.shared.backupRunStatusMu.TryLock() {
		outbox.shared.backupRunStatusMu.Unlock()
		t.Fatal("run admission could pass an in-flight status publication")
	}
	state := signed(t, service, nostr.Kind(kinds.CASControlState), nostr.Now(), nostr.Tags{
		{"d", coordinate}, {"t", kinds.CPStateTopicBackupRun}, {"domain", "backup"},
		{"schema", kinds.CASControlStateSchema}, {"legacy_kind", "31996"}, {"deleted", "false"},
	}, `{"deleted":false}`)
	admissionDone := make(chan error, 1)
	go func() {
		_, _, err := outbox.EnqueueBackupRun(OutboxEntry{Event: state, Target: "control-plane"}, "intent-race", coordinate, requestID, actor)
		admissionDone <- err
	}()
	close(releaseRelay)
	released = true
	require.ErrorContains(t, <-gateDone, "outbox admission failed")
	require.NoError(t, <-admissionDone)
	floor, err = outbox.BackupRunStatusTimestampFloor(service.Public().Hex(), actor, coordinate)
	require.NoError(t, err)
	require.Equal(t, status.CreatedAt, floor, "a failed post-relay outbox write cannot erase the ordering reservation")
	published := false
	later := signed(t, service, 30315, status.CreatedAt+1, status.Tags, status.Content)
	err = outbox.PublishUnadmittedBackupRunStatus(coordinate, requestID, later, func() error { published = true; return nil })
	require.ErrorContains(t, err, "immutable admission outcome")
	require.False(t, published, "an admitted coordinate must not be published after the lock is released")
}

func TestBackupRunStatusGateRejectsWrongEnvelopeAndPendingRequest(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	service, actor := nostr.Generate(), nostr.Generate()
	coordinate := "backup-run:" + uuid.NewString()
	intentID := "intent-" + uuid.NewString()
	createdAt := nostr.Now()
	expiresAt := createdAt.Time().Add(5 * time.Minute)
	request := signed(t, actor, nostr.Kind(kinds.CASControlState), createdAt, nostr.Tags{
		{"d", coordinate}, {"intent_id", intentID}, {"expiration", strconv.FormatInt(expiresAt.Unix(), 10)},
	}, `{}`)
	_, inserted, err := outbox.PutBackupRunPending(BackupRunPending{IntentID: intentID, Coordinate: coordinate,
		RequestEvent: request, Actor: actor.Public().Hex(), ServicePubkey: service.Public().Hex(),
		ReceivedAt: createdAt.Time(), ExpiresAt: expiresAt})
	require.NoError(t, err)
	require.True(t, inserted)
	makeStatus := func(d, status, eventID, content string) nostr.Event {
		return signed(t, service, 30315, createdAt+1, nostr.Tags{
			{"d", d}, {"domain", "intent"}, {"t", "intent-status"}, {"status", status},
			{"p", actor.Public().Hex()}, {"intent_id", intentID}, {"e", eventID},
		}, content)
	}
	private := kinds.IntentPrivateStatusCoordinate(actor.Public().Hex(), "backup", intentID)
	body := `{"intent_id":"` + intentID + `","result":"rejected","reason":"access_denied"}`
	for _, tc := range []struct{ name, d, status, eventID, content string }{
		{"ordinary-rejection", kinds.IntentStatusCoordinate(actor.Public().Hex(), coordinate), "rejected", request.ID.Hex(), body},
		{"wrong-request", private, "rejected", nostr.Generate().Public().Hex(), body},
		{"leaked-coordinate", private, "rejected", request.ID.Hex(), `{"intent_id":"` + intentID + `","coordinate":"` + coordinate + `","result":"rejected"}`},
		{"private-acceptance", private, "accepted", request.ID.Hex(), `{"intent_id":"` + intentID + `","coordinate":"` + coordinate + `","result":"applied"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := outbox.PublishUnadmittedBackupRunStatus(coordinate, request.ID.Hex(), makeStatus(tc.d, tc.status, tc.eventID, tc.content),
				func() error { called = true; return nil })
			require.Error(t, err)
			require.False(t, called)
			floor, err := outbox.BackupRunStatusTimestampFloor(service.Public().Hex(), actor.Public().Hex(), coordinate)
			require.NoError(t, err)
			require.Zero(t, floor)
		})
	}
	called := false
	require.NoError(t, outbox.PublishUnadmittedBackupRunStatus(coordinate, request.ID.Hex(),
		makeStatus(private, "rejected", request.ID.Hex(), body), func() error { called = true; return nil }))
	require.True(t, called)
	floor, err := outbox.BackupRunStatusTimestampFloor(service.Public().Hex(), actor.Public().Hex(), coordinate)
	require.NoError(t, err)
	require.Equal(t, createdAt+1, floor)
}

func TestBackupRunAdmissionScanSkipsMalformedRecordWithoutStarvingLaterACK(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	service := nostr.Generate()
	coordinate := "backup-run:" + uuid.NewString()
	requestID := nostr.Generate().Public().Hex()
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", coordinate}, {"t", kinds.CPStateTopicBackupRun}, {"domain", "backup"},
		{"schema", kinds.CASControlStateSchema}, {"legacy_kind", "31996"}, {"deleted", "false"},
	}}
	require.NoError(t, event.Sign(service))
	_, inserted, err := outbox.EnqueueBackupRun(OutboxEntry{Event: event, Target: "control-plane"}, "intent-z", coordinate, requestID, nostr.Generate().Public().Hex())
	require.NoError(t, err)
	require.True(t, inserted)
	policy := DeliveryPolicy{WriteRelays: []string{"wss://relay.example"}, Required: 1}
	_, err = outbox.CommitPublisherRound(event.ID, OutboxRound{Target: "control-plane", Delivered: true, State: OutboxPublished, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://relay.example": {Accepted: true}}})
	require.NoError(t, err)
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(backupRunIntentsBucket).Put([]byte("intent-a"), []byte("{malformed"))
	}))
	records, next, err := outbox.ListBackupRunAdmissionsNeedingStatus("", 100)
	require.ErrorContains(t, err, "intent-a")
	require.Empty(t, next)
	require.Len(t, records, 1)
	require.Equal(t, "intent-z", records[0].IntentID)
}

func TestBackupRunRefusalScanSkipsMalformedEarlierRecord(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(backupRunPendingBucket)
		if err := bucket.Put([]byte("intent-a"), []byte("{malformed")); err != nil {
			return err
		}
		if err := bucket.Put([]byte("intent-z"), []byte(`{"intent_id":"intent-z","state":"refused"}`)); err != nil {
			return err
		}
		index := tx.Bucket(backupRunRefusalsBucket)
		if err := index.Put([]byte("intent-a"), nil); err != nil {
			return err
		}
		return index.Put([]byte("intent-z"), nil)
	}))
	records, next, err := outbox.ListBackupRunRefusalsNeedingStatus("", 100)
	require.ErrorContains(t, err, "intent-a")
	require.Empty(t, next)
	require.Len(t, records, 1)
	require.Equal(t, "intent-z", records[0].IntentID)
}

func TestBackupRunStatusClockBackfillsRetainedStatusOnUpgrade(t *testing.T) {
	outbox, path := openTempOutbox(t)
	service := nostr.Generate()
	actor := nostr.Generate().Public().Hex()
	coordinate := "backup-run:" + uuid.NewString()
	createdAt := nostr.Now()
	event := signed(t, service, 30315, createdAt, nostr.Tags{
		{"d", "intent-status:" + actor + ":" + coordinate}, {"domain", "intent"},
		{"t", "intent-status"}, {"status", "rejected"}, {"p", actor},
	}, `{"result":"rejected"}`)
	_, err := outbox.Enqueue(OutboxEntry{Event: event, Target: "operator-relays"})
	require.NoError(t, err)
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		if err := tx.DeleteBucket(backupRunStatusClockBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucket(backupRunStatusClockBucket)
		return err
	}))
	require.NoError(t, outbox.Close())
	reopened, err := OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	floor, err := reopened.BackupRunStatusTimestampFloor(service.Public().Hex(), actor, coordinate)
	require.NoError(t, err)
	require.Equal(t, createdAt, floor)
}

func TestBackupRunAdmissionCommitsEventAndIdentityAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	outbox, err := OpenOutbox(path)
	require.NoError(t, err)
	runID, err := uuid.NewV7()
	require.NoError(t, err)
	coordinate := "backup-run:" + runID.String()
	request := nostr.Generate()
	requestID := request.Public().Hex()
	key := nostr.Generate()
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", coordinate}, {"t", kinds.CPStateTopicBackupRun}, {"domain", "backup"},
		{"schema", kinds.CASControlStateSchema}, {"legacy_kind", "31996"}, {"deleted", "false"},
	}, Content: `{"deleted":false}`}
	require.NoError(t, event.Sign(key))
	entry := OutboxEntry{Event: event, Target: "control-plane"}
	admission, inserted, err := outbox.EnqueueBackupRun(entry, "intent-1", coordinate, requestID, request.Public().Hex())
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, event.ID.Hex(), admission.StateEventID)
	stored, found, err := outbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, OutboxPending, stored.State)

	// A concurrent retry may sign a different event, but the same request
	// still owns exactly the original staged state.
	second := event
	second.CreatedAt++
	require.NoError(t, second.Sign(key))
	admission, inserted, err = outbox.EnqueueBackupRun(OutboxEntry{Event: second, Target: "control-plane"}, "intent-1", coordinate, requestID, request.Public().Hex())
	require.NoError(t, err)
	require.False(t, inserted)
	require.Equal(t, event.ID.Hex(), admission.StateEventID)
	_, found, err = outbox.Get(second.ID)
	require.NoError(t, err)
	require.False(t, found)
	_, _, err = outbox.EnqueueBackupRun(OutboxEntry{Event: second, Target: "control-plane"}, "intent-1", coordinate, nostr.Generate().Public().Hex(), request.Public().Hex())
	require.ErrorContains(t, err, "conflicts")
	_, _, err = outbox.EnqueueBackupRun(OutboxEntry{Event: second, Target: "control-plane"}, "intent-2", coordinate, requestID, nostr.Generate().Public().Hex())
	require.ErrorContains(t, err, "coordinate already belongs")

	_, err = outbox.CommitPublisherRound(event.ID, OutboxRound{Target: "control-plane", Rounds: 1, Delivered: false, State: OutboxFailed,
		Relays: map[string]RelayDelivery{"wss://relay.example": {Rejected: "blocked"}}})
	require.NoError(t, err)
	prior, err := outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.False(t, prior.Delivered, "relay refusal cannot accept a staged run")
	_, err = outbox.Prune(time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, found, err = outbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found, "an unaccepted staged run pins its exact signed event")
	prior, err = outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.Equal(t, event.ID.Hex(), prior.StateEventID, "delivery pruning must not free the run identity")
	require.NoError(t, outbox.Close())
	outbox, err = OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	prior, err = outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.Equal(t, event.ID.Hex(), prior.StateEventID, "restart must preserve exact request binding")
	require.False(t, prior.Delivered)
}

func TestBackupRunAdmissionRequiresQuorumAndRetainsACKAfterPrune(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	key := nostr.Generate()
	coordinate := "backup-run:" + uuid.NewString()
	requestID := nostr.Generate().Public().Hex()
	event := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", coordinate}, {"t", kinds.CPStateTopicBackupRun}, {"domain", "backup"},
		{"schema", kinds.CASControlStateSchema}, {"legacy_kind", "31996"}, {"deleted", "false"},
	}}
	require.NoError(t, event.Sign(key))
	_, inserted, err := outbox.EnqueueBackupRun(OutboxEntry{Event: event, Target: "control-plane"}, "intent-1", coordinate, requestID, nostr.Generate().Public().Hex())
	require.NoError(t, err)
	require.True(t, inserted)
	policy := DeliveryPolicy{WriteRelays: []string{"wss://a.example", "wss://b.example"}, Required: 2}
	_, err = outbox.CommitPublisherRound(event.ID, OutboxRound{Target: "control-plane", Rounds: 1, Delivered: false, State: OutboxPending, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a.example": {Accepted: true}}})
	require.NoError(t, err)
	prior, err := outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.False(t, prior.Delivered, "one relay OK below the configured quorum is not acceptance")
	_, err = outbox.CommitRound(event.ID, OutboxRound{Rounds: 2, Delivered: true, State: OutboxPending, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a.example": {Accepted: true}, "wss://b.example": {Accepted: true}}})
	require.ErrorContains(t, err, "publisher path", "caller-supplied ACK flags cannot admit a run")
	_, err = outbox.CommitPublisherRound(event.ID, OutboxRound{Target: "control-plane", Rounds: 2, Delivered: true, State: OutboxPending, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a.example": {Accepted: true}, "wss://b.example": {Accepted: true}}})
	require.NoError(t, err)
	prior, err = outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.True(t, prior.Delivered, "the persisted quorum marker and accepted relay together admit the run")
	_, err = outbox.CommitPublisherRound(event.ID, OutboxRound{Target: "control-plane", Rounds: 3, Delivered: true, State: OutboxPublished, Policy: policy,
		Relays: map[string]RelayDelivery{"wss://a.example": {Accepted: true}, "wss://b.example": {Accepted: true}}})
	require.NoError(t, err)
	_, err = outbox.Prune(time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, found, err := outbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found, "ACKed run stays pinned until final status is durable")
	prior, err = outbox.GetBackupRunAdmission("intent-1", coordinate, requestID)
	require.NoError(t, err)
	require.True(t, prior.Delivered)
}

func TestBackupRunAdmissionRejectsWrongEnvelopeWithoutMutation(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	key := nostr.Generate()
	event := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "backup-run:bad"}, {"t", "other"}, {"legacy_kind", "31996"}, {"deleted", "false"}}}
	require.NoError(t, event.Sign(key))
	_, _, err = outbox.EnqueueBackupRun(OutboxEntry{Event: event, Target: "control-plane"}, "intent-1", "backup-run:bad", nostr.Generate().Public().Hex(), nostr.Generate().Public().Hex())
	require.ErrorContains(t, err, "canonical run-state")
	prior, err := outbox.GetBackupRunAdmission("intent-1", "backup-run:bad", nostr.Generate().Public().Hex())
	require.NoError(t, err)
	require.Nil(t, prior)
	_, found, err := outbox.Get(event.ID)
	require.NoError(t, err)
	require.False(t, found)
}
