package localstore

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func TestBackupRunPendingTerminalCompactionAndActiveIndexes(t *testing.T) {
	outbox, path := openTempOutbox(t)
	actor, service := nostr.Generate(), nostr.Generate()
	coordinate := "backup-run:" + uuid.NewString()
	intentID := "intent-" + uuid.NewString()
	createdAt := nostr.Now()
	expiresAt := createdAt.Time().Add(5 * time.Minute)
	request := signed(t, actor, nostr.Kind(kinds.CASControlState), createdAt, nostr.Tags{
		{"d", coordinate}, {"intent_id", intentID}, {"expiration", strconv.FormatInt(expiresAt.Unix(), 10)},
	}, `{"backup":"large signed request body"}`)
	original := BackupRunPending{IntentID: intentID, Coordinate: coordinate, RequestEvent: request,
		Actor: actor.Public().Hex(), ServicePubkey: service.Public().Hex(), ReceivedAt: createdAt.Time(), ExpiresAt: expiresAt}
	_, inserted, err := outbox.PutBackupRunPending(original)
	require.NoError(t, err)
	require.True(t, inserted)
	due, cursor, wake, err := outbox.ListBackupRunPendingDue("", createdAt.Time(), 1)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, request.ID, due[0].RequestEvent.ID)
	require.Empty(t, cursor)
	require.True(t, wake.IsZero())

	require.NoError(t, outbox.RefuseBackupRunPending(intentID, request.ID.Hex(), "coordinate already exists"))
	due, cursor, wake, err = outbox.ListBackupRunPendingDue("", createdAt.Time(), 1)
	require.NoError(t, err)
	require.Empty(t, due)
	require.Empty(t, cursor)
	require.True(t, wake.IsZero())
	refusals, next, err := outbox.ListBackupRunRefusalsNeedingStatus("", 1)
	require.NoError(t, err)
	require.Len(t, refusals, 1)
	require.Empty(t, next)
	status := signed(t, service, 30315, nostr.Now(), nostr.Tags{
		{"d", kinds.IntentPrivateStatusCoordinate(original.Actor, "backup", intentID)}, {"domain", "intent"}, {"t", "intent-status"},
		{"status", "rejected"}, {"intent_id", intentID}, {"p", original.Actor}, {"e", request.ID.Hex()},
	}, `{"intent_id":"`+intentID+`","result":"rejected","reason":"access_denied"}`)
	_, inserted, err = outbox.StageBackupRunRefusedStatus(refusals[0], status, "operator-relays")
	require.NoError(t, err)
	require.True(t, inserted)
	refusals, next, err = outbox.ListBackupRunRefusalsNeedingStatus("", 1)
	require.NoError(t, err)
	require.Empty(t, refusals)
	require.Empty(t, next)
	retained, err := outbox.GetBackupRunPending(intentID, coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, request.ID, retained.RequestEvent.ID)
	require.Equal(t, request.Sig, retained.RequestEvent.Sig)
	require.Empty(t, retained.RequestEvent.Content)
	require.Empty(t, retained.RequestEvent.Tags)
	require.Equal(t, "coordinate already exists", retained.LastFailure)
	_, inserted, err = outbox.PutBackupRunPending(original)
	require.NoError(t, err)
	require.False(t, inserted)

	// A legacy outbox without indexes rebuilds only live keys on open and
	// preserves exact terminal replay bindings.
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		if err := tx.DeleteBucket(backupRunRefusalsBucket); err != nil {
			return err
		}
		if err := tx.DeleteBucket(backupRunDueBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucket(backupRunRefusalsBucket)
		if err != nil {
			return err
		}
		_, err = tx.CreateBucket(backupRunDueBucket)
		return err
	}))
	require.NoError(t, outbox.Close())
	outbox, err = OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	due, _, _, err = outbox.ListBackupRunPendingDue("", expiresAt, 1)
	require.NoError(t, err)
	require.Empty(t, due)
	retained, err = outbox.GetBackupRunPending(intentID, coordinate, request.ID.Hex())
	require.NoError(t, err)
	require.Equal(t, request.Sig, retained.RequestEvent.Sig)

	// Corruption of the compacted signature is refused, not mistaken for an
	// active request or trusted as a replay proof.
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		var damaged BackupRunPending
		require.NoError(t, json.Unmarshal(tx.Bucket(backupRunPendingBucket).Get([]byte(intentID)), &damaged))
		damaged.RequestEvent.Sig[0] ^= 1
		return putBackupRunPending(tx, damaged)
	}))
	_, err = outbox.GetBackupRunPending(intentID, coordinate, request.ID.Hex())
	require.ErrorContains(t, err, "immutable signed keys")
}

func TestBackupRunPendingScanIsBoundedByActiveIndex(t *testing.T) {
	outbox, _ := openTempOutbox(t)
	actor, service := nostr.Generate(), nostr.Generate()
	coordinate := "backup-run:" + uuid.NewString()
	intentID := "intent-" + uuid.NewString()
	createdAt := nostr.Now()
	expiresAt := createdAt.Time().Add(5 * time.Minute)
	request := signed(t, actor, nostr.Kind(kinds.CASControlState), createdAt, nostr.Tags{
		{"d", coordinate}, {"intent_id", intentID}, {"expiration", strconv.FormatInt(expiresAt.Unix(), 10)},
	}, `{"backup":"active"}`)
	_, _, err := outbox.PutBackupRunPending(BackupRunPending{IntentID: intentID, Coordinate: coordinate, RequestEvent: request,
		Actor: actor.Public().Hex(), ServicePubkey: service.Public().Hex(), ReceivedAt: createdAt.Time(), ExpiresAt: expiresAt})
	require.NoError(t, err)
	// These rows represent lifetime terminal history. The indexed scans must
	// neither visit nor decode them, even when there are far more than one page.
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(backupRunPendingBucket)
		for i := range 1000 {
			if err := bucket.Put([]byte(fmt.Sprintf("old-%04d", i)), []byte("{malformed terminal row")); err != nil {
				return err
			}
		}
		return nil
	}))
	due, cursor, wake, err := outbox.ListBackupRunPendingDue("", createdAt.Time(), 1)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, intentID, due[0].IntentID)
	require.Empty(t, cursor)
	require.True(t, wake.IsZero())
	refusals, next, err := outbox.ListBackupRunRefusalsNeedingStatus("", 1)
	require.NoError(t, err)
	require.Empty(t, refusals)
	require.Empty(t, next)
}

func TestBackupRunPendingDueIndexMovesWithBackoff(t *testing.T) {
	outbox, path := openTempOutbox(t)
	actor, service := nostr.Generate(), nostr.Generate()
	coordinate := "backup-run:" + uuid.NewString()
	intentID := "intent-" + uuid.NewString()
	createdAt := nostr.Now()
	expiresAt := createdAt.Time().Add(5 * time.Minute)
	request := signed(t, actor, nostr.Kind(kinds.CASControlState), createdAt, nostr.Tags{
		{"d", coordinate}, {"intent_id", intentID}, {"expiration", strconv.FormatInt(expiresAt.Unix(), 10)},
	}, `{"backup":"active"}`)
	_, _, err := outbox.PutBackupRunPending(BackupRunPending{IntentID: intentID, Coordinate: coordinate, RequestEvent: request,
		Actor: actor.Public().Hex(), ServicePubkey: service.Public().Hex(), ReceivedAt: createdAt.Time(), ExpiresAt: expiresAt})
	require.NoError(t, err)
	nextAttempt := createdAt.Time().Add(30 * time.Second)
	require.NoError(t, outbox.RecordBackupRunPendingAttempt(intentID, request.ID.Hex(), createdAt.Time(), nextAttempt, "relay unavailable"))
	require.NoError(t, outbox.shared.db.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{backupRunDueBucket, backupRunRefusalsBucket} {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
			if _, err := tx.CreateBucket(name); err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, outbox.Close())
	outbox, err = OpenOutbox(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, outbox.Close()) })
	due, cursor, wake, err := outbox.ListBackupRunPendingDue("", createdAt.Time(), 1)
	require.NoError(t, err)
	require.Empty(t, due)
	require.Empty(t, cursor)
	require.True(t, wake.Equal(nextAttempt), "next wake = %s, want %s", wake, nextAttempt)
	due, cursor, wake, err = outbox.ListBackupRunPendingDue("", nextAttempt, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Empty(t, cursor)
	require.True(t, wake.IsZero())
	require.NoError(t, outbox.RefuseBackupRunPending(intentID, request.ID.Hex(), "coordinate conflict"))
	due, _, wake, err = outbox.ListBackupRunPendingDue("", expiresAt, 1)
	require.NoError(t, err)
	require.Empty(t, due)
	require.True(t, wake.IsZero())
}
