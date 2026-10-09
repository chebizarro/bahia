package localstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.etcd.io/bbolt"
)

// BackupRunAdmission is the durable correlation between one operator request
// and the first service-signed run state. It remains after settled outbox
// entries are pruned; the event itself remains canonical on relays.
type BackupRunAdmission struct {
	IntentID        string `json:"intent_id"`
	Coordinate      string `json:"coordinate"`
	RequestEventID  string `json:"request_event_id"`
	StateEventID    string `json:"state_event_id"`
	Target          string `json:"target"`
	Actor           string `json:"actor"`
	ServicePubkey   string `json:"service_pubkey"`
	Delivered       bool   `json:"delivered"`
	StatusEventID   string `json:"status_event_id,omitempty"`
	StatusTarget    string `json:"status_target,omitempty"`
	StatusOutcome   string `json:"status_outcome,omitempty"`
	StatusDelivered bool   `json:"status_delivered,omitempty"`
}

// GetBackupRunAdmission reports an exact replay or a key conflict. The
// admission record and its staged outbox entry are committed together.
func (o *Outbox) GetBackupRunAdmission(intentID, coordinate, requestEventID string) (*BackupRunAdmission, error) {
	if o == nil || o.shared == nil || intentID == "" || coordinate == "" || requestEventID == "" {
		return nil, fmt.Errorf("backup run admission requires an outbox and request identity")
	}
	var result *BackupRunAdmission
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		intents := tx.Bucket(backupRunIntentsBucket)
		coords := tx.Bucket(backupRunCoordsBucket)
		if intents == nil || coords == nil {
			return fmt.Errorf("backup run admission index is unavailable")
		}
		if raw := intents.Get([]byte(intentID)); raw != nil {
			var record BackupRunAdmission
			if err := json.Unmarshal(raw, &record); err != nil {
				return fmt.Errorf("decode backup run admission: %w", err)
			}
			if record.IntentID != intentID || record.Coordinate != coordinate || record.RequestEventID != requestEventID {
				return fmt.Errorf("backup run intent id conflicts with a different signed request")
			}
			if bound := coords.Get([]byte(coordinate)); string(bound) != intentID {
				return fmt.Errorf("backup run admission coordinate index is inconsistent")
			}
			if bound := tx.Bucket(backupRunEventsBucket).Get([]byte(record.StateEventID)); string(bound) != intentID {
				return fmt.Errorf("backup run admission event index is inconsistent")
			}
			if record.Delivered {
				id, err := nostr.IDFromHex(record.StateEventID)
				if err != nil {
					return fmt.Errorf("backup run admission state id is invalid: %w", err)
				}
				var proof DeliveryProof
				raw := tx.Bucket(outboxDeliveryProofsBucket).Get(id[:])
				if raw == nil || json.Unmarshal(raw, &proof) != nil || proof.Event.ID != id ||
					proof.Event.PubKey.Hex() != record.ServicePubkey ||
					proof.Target != record.Target || !proof.ValidFor(proof.Event, record.Target) ||
					!isBackupRunStateEvent(proof.Event) || outboxTag(proof.Event.Tags, "d") != record.Coordinate {
					return fmt.Errorf("backup run admission lacks exact relay quorum proof")
				}
			}
			if record.StatusDelivered {
				if !record.Delivered || record.StatusOutcome != "accepted" {
					return fmt.Errorf("backup run accepted status lacks admitted run")
				}
				id, err := nostr.IDFromHex(record.StatusEventID)
				if err != nil {
					return fmt.Errorf("backup run accepted status id is invalid: %w", err)
				}
				var proof DeliveryProof
				raw := tx.Bucket(outboxDeliveryProofsBucket).Get(id[:])
				if raw == nil || json.Unmarshal(raw, &proof) != nil || !proof.ValidFor(proof.Event, record.StatusTarget) ||
					proof.Event.ID != id || proof.Event.PubKey.Hex() != record.ServicePubkey ||
					!isBackupRunAcceptedStatusEvent(proof.Event) ||
					outboxTag(proof.Event.Tags, "d") != "intent-status:"+record.Actor+":"+record.Coordinate ||
					string(tx.Bucket(backupRunStatusEventsBucket).Get([]byte(record.StatusEventID))) != intentID {
					return fmt.Errorf("backup run accepted status lacks exact relay quorum proof")
				}
			}
			result = &record
			return nil
		}
		if coords.Get([]byte(coordinate)) != nil {
			return fmt.Errorf("backup run coordinate already belongs to another signed request")
		}
		return nil
	})
	return result, err
}

// HasBackupRunAdmissionCoordinate is the status publisher's fail-closed guard
// against overwriting an admitted coordinate with a later generic rejection.
func (o *Outbox) HasBackupRunAdmissionCoordinate(coordinate string) (bool, error) {
	if o == nil || o.shared == nil {
		return false, fmt.Errorf("backup run admission outbox is unavailable")
	}
	var found bool
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(backupRunCoordsBucket)
		if bucket == nil {
			return fmt.Errorf("backup run admission coordinate index is unavailable")
		}
		found = bucket.Get([]byte(coordinate)) != nil
		return nil
	})
	return found, err
}

// BackupRunStatusTimestampFloor is the durable NIP-01 replacement floor for
// this service, requester, and run coordinate. Retained status outbox rows
// seed it on upgrade; pruning a settled row does not lower it.
func (o *Outbox) BackupRunStatusTimestampFloor(servicePubkey, actor, coordinate string) (nostr.Timestamp, error) {
	if o == nil || o.shared == nil || !strings.HasPrefix(coordinate, "backup-run:") {
		return 0, fmt.Errorf("backup run status clock requires an outbox and run coordinate")
	}
	pubkey, err := nostr.PubKeyFromHex(servicePubkey)
	if err != nil {
		return 0, fmt.Errorf("backup run status clock service pubkey: %w", err)
	}
	if _, err := nostr.PubKeyFromHex(actor); err != nil {
		return 0, fmt.Errorf("backup run status clock actor pubkey: %w", err)
	}
	var floor nostr.Timestamp
	err = o.shared.db.View(func(tx *bbolt.Tx) error {
		var err error
		floor, err = backupRunStatusTimestampFloor(tx, backupRunStatusClockKey(pubkey, actor, coordinate))
		return err
	})
	return floor, err
}

// EnqueueBackupRun atomically stages the signed state and its immutable
// request keys. A crash cannot leave a processed request without the event to
// deliver, or a queued event without the admission keys needed for replay.
func (o *Outbox) EnqueueBackupRun(entry OutboxEntry, intentID, coordinate, requestEventID, actor string) (BackupRunAdmission, bool, error) {
	if o == nil || o.shared == nil || o.shared.readOnly {
		return BackupRunAdmission{}, false, ErrReadOnly
	}
	if intentID == "" || coordinate == "" || requestEventID == "" || strings.ContainsRune(intentID, 0) || strings.ContainsRune(coordinate, 0) {
		return BackupRunAdmission{}, false, fmt.Errorf("backup run admission requires valid request keys")
	}
	if _, err := nostr.IDFromHex(requestEventID); err != nil {
		return BackupRunAdmission{}, false, fmt.Errorf("invalid backup run request event id: %w", err)
	}
	if _, err := nostr.PubKeyFromHex(actor); err != nil {
		return BackupRunAdmission{}, false, fmt.Errorf("invalid backup run requester pubkey: %w", err)
	}
	if entry.Target == "" || !entry.Event.CheckID() || !entry.Event.VerifySignature() ||
		entry.Delivered || entry.Rounds != 0 || len(entry.Relays) != 0 || len(entry.Policy.WriteRelays) != 0 || entry.Policy.Required != 0 ||
		entry.Event.Kind != nostr.Kind(kinds.CASControlState) ||
		!oneBackupRunTag(entry.Event.Tags, "d", coordinate) ||
		!oneBackupRunTag(entry.Event.Tags, "t", kinds.CPStateTopicBackupRun) ||
		!oneBackupRunTag(entry.Event.Tags, "legacy_kind", fmt.Sprint(kinds.BackupRunState)) ||
		!oneBackupRunTag(entry.Event.Tags, "deleted", "false") {
		return BackupRunAdmission{}, false, fmt.Errorf("backup run admission requires a signed canonical run-state event")
	}
	if entry.EnqueuedAt.IsZero() {
		entry.EnqueuedAt = time.Now().UTC()
	}
	entry.EnqueuedAt = entry.EnqueuedAt.UTC()
	entry.State = OutboxPending
	entry.SettledAt = time.Time{}
	encodedEntry, err := json.Marshal(entry)
	if err != nil {
		return BackupRunAdmission{}, false, err
	}
	record := BackupRunAdmission{IntentID: intentID, Coordinate: coordinate, RequestEventID: requestEventID, StateEventID: entry.Event.ID.Hex(), Target: entry.Target, Actor: actor, ServicePubkey: entry.Event.PubKey.Hex()}
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		return BackupRunAdmission{}, false, err
	}
	inserted := false
	err = o.shared.db.Update(func(tx *bbolt.Tx) error {
		intents := tx.Bucket(backupRunIntentsBucket)
		coords := tx.Bucket(backupRunCoordsBucket)
		if intents == nil || coords == nil {
			return fmt.Errorf("backup run admission index is unavailable")
		}
		if raw := intents.Get([]byte(intentID)); raw != nil {
			var prior BackupRunAdmission
			if err := json.Unmarshal(raw, &prior); err != nil {
				return err
			}
			if prior.IntentID != intentID || prior.Coordinate != coordinate || prior.RequestEventID != requestEventID || prior.Target != entry.Target || prior.Actor != actor ||
				string(coords.Get([]byte(coordinate))) != intentID ||
				string(tx.Bucket(backupRunEventsBucket).Get([]byte(prior.StateEventID))) != intentID {
				return fmt.Errorf("backup run intent id conflicts with a different signed request")
			}
			record = prior
			return nil
		}
		if coords.Get([]byte(coordinate)) != nil {
			return fmt.Errorf("backup run coordinate already belongs to another signed request")
		}
		if tx.Bucket(outboxEntriesBucket).Get(entry.Event.ID[:]) != nil {
			return fmt.Errorf("backup run state event id is already held without its admission record")
		}
		if err := tx.Bucket(outboxEntriesBucket).Put(entry.Event.ID[:], encodedEntry); err != nil {
			return err
		}
		if err := tx.Bucket(outboxPendingBucket).Put(pendingKey(entry.Target, entry.EnqueuedAt, entry.Event.ID), nil); err != nil {
			return err
		}
		if err := intents.Put([]byte(intentID), encodedRecord); err != nil {
			return err
		}
		if err := coords.Put([]byte(coordinate), []byte(intentID)); err != nil {
			return err
		}
		if err := tx.Bucket(backupRunEventsBucket).Put([]byte(entry.Event.ID.Hex()), []byte(intentID)); err != nil {
			return err
		}
		inserted = true
		return nil
	})
	if err != nil {
		return BackupRunAdmission{}, false, fmt.Errorf("stage backup run admission: %w", err)
	}
	return record, inserted, nil
}

// updateBackupRunAdmissionDelivery runs inside the same bbolt transaction as
// the outbox round. Pruning the settled event never erases its quorum proof.
func updateBackupRunAdmissionDelivery(tx *bbolt.Tx, entry OutboxEntry) error {
	events := tx.Bucket(backupRunEventsBucket)
	if events == nil {
		return nil
	}
	intentID := events.Get([]byte(entry.Event.ID.Hex()))
	if intentID == nil {
		return nil
	}
	intents := tx.Bucket(backupRunIntentsBucket)
	if intents == nil {
		return fmt.Errorf("backup run admission index is unavailable")
	}
	var record BackupRunAdmission
	if err := json.Unmarshal(intents.Get(intentID), &record); err != nil {
		return fmt.Errorf("decode backup run admission delivery: %w", err)
	}
	if record.StateEventID != entry.Event.ID.Hex() {
		return fmt.Errorf("backup run admission event index is inconsistent")
	}
	if record.Delivered {
		return nil
	}
	if entry.Delivered {
		var proof DeliveryProof
		raw := tx.Bucket(outboxDeliveryProofsBucket).Get(entry.Event.ID[:])
		if raw == nil || json.Unmarshal(raw, &proof) != nil || !proof.ValidFor(entry.Event, entry.Target) {
			return fmt.Errorf("backup run admission requires exact publisher relay quorum proof")
		}
		record.Delivered = true
	} else {
		return nil
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return intents.Put(intentID, encoded)
}

func updateBackupRunStatusDelivery(tx *bbolt.Tx, entry OutboxEntry) error {
	statusEvents := tx.Bucket(backupRunStatusEventsBucket)
	if statusEvents == nil {
		return nil
	}
	intentID := statusEvents.Get([]byte(entry.Event.ID.Hex()))
	if intentID == nil || !entry.Delivered {
		return nil
	}
	intents := tx.Bucket(backupRunIntentsBucket)
	var record BackupRunAdmission
	if raw := intents.Get(intentID); raw == nil || json.Unmarshal(raw, &record) != nil {
		return fmt.Errorf("backup run admission for accepted status is unavailable")
	}
	if record.StatusEventID != entry.Event.ID.Hex() || record.StatusTarget != entry.Target ||
		!record.Delivered || record.StatusOutcome != "accepted" {
		return fmt.Errorf("backup run accepted status index is inconsistent")
	}
	if record.StatusDelivered {
		return nil
	}
	var proof DeliveryProof
	raw := tx.Bucket(outboxDeliveryProofsBucket).Get(entry.Event.ID[:])
	if raw == nil || json.Unmarshal(raw, &proof) != nil || !proof.ValidFor(entry.Event, entry.Target) {
		return fmt.Errorf("backup run accepted status requires exact publisher relay quorum proof")
	}
	record.StatusDelivered = true
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return intents.Put(intentID, encoded)
}

func oneBackupRunTag(tags nostr.Tags, key, want string) bool {
	count := 0
	for _, tag := range tags {
		if len(tag) > 1 && tag[0] == key {
			if tag[1] != want {
				return false
			}
			count++
		}
	}
	return count == 1
}

func hasBackupRunTag(tags nostr.Tags, key string) bool {
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == key {
			return true
		}
	}
	return false
}

// backupRunAdmissionUnresolved pins the only signed copy of a staged run
// through ordinary published/failed row retention. An outbox failure is not
// proof that no relay holds the event, so it cannot be pruned or rejected.
func backupRunAdmissionUnresolved(tx *bbolt.Tx, eventID nostr.ID) (bool, error) {
	events := tx.Bucket(backupRunEventsBucket)
	statusEvents := tx.Bucket(backupRunStatusEventsBucket)
	if events == nil || statusEvents == nil {
		return true, fmt.Errorf("backup run admission indexes are unavailable")
	}
	intentID := events.Get([]byte(eventID.Hex()))
	if intentID == nil {
		intentID = statusEvents.Get([]byte(eventID.Hex()))
		if intentID == nil {
			return false, nil
		}
	}
	bucket := tx.Bucket(backupRunIntentsBucket)
	var record BackupRunAdmission
	if raw := bucket.Get(intentID); raw == nil || json.Unmarshal(raw, &record) != nil {
		return true, fmt.Errorf("backup run admission is unavailable")
	}
	return !record.StatusDelivered, nil
}

// ListBackupRunAdmissionsNeedingStatus scans durable admission outcomes. A
// caller resumes after the returned cursor so unrelated old admissions cannot
// starve later ones. A nil result means there is no ACKed outcome to sign.
func (o *Outbox) ListBackupRunAdmissionsNeedingStatus(after string, limit int) ([]BackupRunAdmission, string, error) {
	if o == nil || o.shared == nil {
		return nil, "", fmt.Errorf("backup run admission outbox is unavailable")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var records []BackupRunAdmission
	var next string
	var decodeErr error
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(backupRunIntentsBucket)
		if bucket == nil {
			return fmt.Errorf("backup run admission index is unavailable")
		}
		cursor := bucket.Cursor()
		key, raw := cursor.First()
		if after != "" {
			key, raw = cursor.Seek([]byte(after))
			if bytes.Equal(key, []byte(after)) {
				key, raw = cursor.Next()
			}
		}
		visited := 0
		for ; key != nil && visited < limit; key, raw = cursor.Next() {
			visited++
			next = string(key)
			var record BackupRunAdmission
			if err := json.Unmarshal(raw, &record); err != nil {
				decodeErr = errors.Join(decodeErr, fmt.Errorf("decode backup run admission %q: %w", key, err))
				continue
			}
			if record.Delivered && record.StatusEventID == "" {
				records = append(records, record)
			}
		}
		if key == nil {
			next = ""
		}
		return nil
	})
	return records, next, errors.Join(err, decodeErr)
}

// StageBackupRunAcceptedStatus atomically records exactly one service-signed
// accepted result and its outbox row. A crash before this transaction leaves an
// admission to reconcile; a crash after it leaves the signed status to retry.
func (o *Outbox) StageBackupRunAcceptedStatus(record BackupRunAdmission, event nostr.Event, target string) (string, bool, error) {
	if o == nil || o.shared == nil || o.shared.readOnly {
		return "", false, ErrReadOnly
	}
	if !event.CheckID() || !event.VerifySignature() || event.Kind != 30315 ||
		event.PubKey.Hex() != record.ServicePubkey ||
		!oneBackupRunTag(event.Tags, "d", "intent-status:"+record.Actor+":"+record.Coordinate) ||
		!oneBackupRunTag(event.Tags, "domain", "intent") ||
		!oneBackupRunTag(event.Tags, "t", "intent-status") ||
		!oneBackupRunTag(event.Tags, "status", "accepted") ||
		!oneBackupRunTag(event.Tags, "intent_id", record.IntentID) ||
		!oneBackupRunTag(event.Tags, "p", record.Actor) ||
		!oneBackupRunTag(event.Tags, "e", record.RequestEventID) ||
		hasBackupRunTag(event.Tags, "expiration") {
		return "", false, fmt.Errorf("backup run terminal status requires exact signed request correlation")
	}
	var content struct {
		IntentID   string `json:"intent_id"`
		Coordinate string `json:"coordinate"`
		Result     string `json:"result"`
		Data       struct {
			RunID        string `json:"run_id"`
			StateEventID string `json:"state_event_id"`
			Execution    string `json:"execution"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(event.Content), &content) != nil || content.IntentID != record.IntentID ||
		content.Coordinate != record.Coordinate ||
		content.Result != "applied" || content.Data.RunID != strings.TrimPrefix(record.Coordinate, "backup-run:") ||
		content.Data.StateEventID != record.StateEventID || content.Data.Execution != "paused" {
		return "", false, fmt.Errorf("backup run terminal status content does not match the admission")
	}
	entry := OutboxEntry{Event: event, Target: target, EntityType: "backup_run.status", EnqueuedAt: time.Now().UTC(), State: OutboxPending}
	encodedEntry, err := json.Marshal(entry)
	if err != nil {
		return "", false, err
	}
	var statusID string
	inserted := false
	err = o.shared.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(backupRunIntentsBucket)
		var current BackupRunAdmission
		if raw := bucket.Get([]byte(record.IntentID)); raw == nil || json.Unmarshal(raw, &current) != nil {
			return fmt.Errorf("backup run admission is unavailable")
		}
		if current.IntentID != record.IntentID || current.Coordinate != record.Coordinate ||
			current.RequestEventID != record.RequestEventID || current.StateEventID != record.StateEventID ||
			current.Actor != record.Actor || current.ServicePubkey != record.ServicePubkey {
			return fmt.Errorf("backup run admission changed before status staging")
		}
		if current.StatusEventID != "" {
			if current.StatusOutcome != "accepted" {
				return fmt.Errorf("backup run admission already has a conflicting terminal status")
			}
			statusID = current.StatusEventID
			return nil
		}
		if !current.Delivered {
			return fmt.Errorf("backup run admission has no matching terminal relay outcome")
		}
		floor, err := backupRunStatusTimestampFloor(tx, backupRunStatusClockKey(event.PubKey, current.Actor, current.Coordinate))
		if err != nil {
			return err
		}
		if event.CreatedAt <= floor {
			return fmt.Errorf("backup run accepted status is not newer than the durable coordinate floor")
		}
		if tx.Bucket(outboxEntriesBucket).Get(event.ID[:]) != nil {
			return fmt.Errorf("backup run terminal status event id already exists")
		}
		if err := tx.Bucket(outboxEntriesBucket).Put(event.ID[:], encodedEntry); err != nil {
			return err
		}
		if err := tx.Bucket(outboxPendingBucket).Put(pendingKey(target, entry.EnqueuedAt, event.ID), nil); err != nil {
			return err
		}
		if key := outboxCoordinateKey(target, event); key != nil {
			if err := tx.Bucket(outboxCoordinatesBucket).Put(key, nil); err != nil {
				return err
			}
		}
		if err := tx.Bucket(backupRunStatusEventsBucket).Put([]byte(event.ID.Hex()), []byte(current.IntentID)); err != nil {
			return err
		}
		if err := recordBackupRunStatusTimestamp(tx, event); err != nil {
			return err
		}
		current.StatusEventID = event.ID.Hex()
		current.StatusTarget = target
		current.StatusOutcome = "accepted"
		raw, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(current.IntentID), raw); err != nil {
			return err
		}
		statusID = current.StatusEventID
		inserted = true
		return nil
	})
	return statusID, inserted, err
}
