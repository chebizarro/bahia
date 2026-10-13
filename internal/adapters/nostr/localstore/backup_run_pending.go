package localstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.etcd.io/bbolt"
)

const (
	BackupRunPendingState = "pending"
	BackupRunRefusedState = "refused"
)

// BackupRunPending is the immutable, signed operator request and its bounded
// history retry state. It lives with the outbox, never in the rebuildable
// inbound cache or PostgreSQL. No service-signed state exists at this phase.
type BackupRunPending struct {
	IntentID        string      `json:"intent_id"`
	Coordinate      string      `json:"coordinate"`
	RequestEvent    nostr.Event `json:"request_event"`
	Actor           string      `json:"actor"`
	ServicePubkey   string      `json:"service_pubkey"`
	ReceivedAt      time.Time   `json:"received_at"`
	ExpiresAt       time.Time   `json:"expires_at"`
	NextAttemptAt   time.Time   `json:"next_attempt_at,omitempty"`
	Attempts        int         `json:"attempts,omitempty"`
	LastFailure     string      `json:"last_failure,omitempty"`
	State           string      `json:"state"`
	StatusEventID   string      `json:"status_event_id,omitempty"`
	StatusTarget    string      `json:"status_target,omitempty"`
	StatusDelivered bool        `json:"status_delivered,omitempty"`
}

func (o *Outbox) GetBackupRunPending(intentID, coordinate, requestEventID string) (*BackupRunPending, error) {
	if o == nil || o.shared == nil || intentID == "" || coordinate == "" || requestEventID == "" {
		return nil, fmt.Errorf("backup run pending lookup requires an outbox and exact request keys")
	}
	var result *BackupRunPending
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		pending, coords, events := tx.Bucket(backupRunPendingBucket), tx.Bucket(backupRunPendingCoordsBucket), tx.Bucket(backupRunPendingEventsBucket)
		if pending == nil || coords == nil || events == nil {
			return fmt.Errorf("backup run pending ledger is unavailable")
		}
		if raw := pending.Get([]byte(intentID)); raw != nil {
			var record BackupRunPending
			if err := json.Unmarshal(raw, &record); err != nil {
				return fmt.Errorf("decode backup run pending record: %w", err)
			}
			if !validBackupRunPendingIdentity(record) || record.IntentID != intentID || record.Coordinate != coordinate || record.RequestEvent.ID.Hex() != requestEventID ||
				string(coords.Get([]byte(coordinate))) != intentID || string(events.Get([]byte(requestEventID))) != intentID ||
				!record.RequestEvent.CheckID() || !record.RequestEvent.VerifySignature() {
				return fmt.Errorf("backup run pending request conflicts with immutable signed keys")
			}
			if record.StatusDelivered {
				id, err := nostr.IDFromHex(record.StatusEventID)
				if err != nil {
					return fmt.Errorf("backup run refusal status id: %w", err)
				}
				var proof DeliveryProof
				raw := tx.Bucket(outboxDeliveryProofsBucket).Get(id[:])
				if record.State != BackupRunRefusedState || raw == nil || json.Unmarshal(raw, &proof) != nil ||
					!proof.ValidFor(proof.Event, record.StatusTarget) || proof.Event.ID != id ||
					proof.Event.PubKey.Hex() != record.ServicePubkey || !isBackupRunRefusedStatusEvent(proof.Event) ||
					outboxTag(proof.Event.Tags, "d") != "intent-status:"+record.Actor+":"+record.Coordinate ||
					string(tx.Bucket(backupRunStatusEventsBucket).Get([]byte(record.StatusEventID))) != intentID {
					return fmt.Errorf("backup run refusal lacks exact relay quorum proof")
				}
			} else if record.StatusEventID != "" {
				id, err := nostr.IDFromHex(record.StatusEventID)
				if err != nil {
					return fmt.Errorf("backup run refusal status id: %w", err)
				}
				var entry OutboxEntry
				raw := tx.Bucket(outboxEntriesBucket).Get(id[:])
				if record.State != BackupRunRefusedState || raw == nil || json.Unmarshal(raw, &entry) != nil || entry.Event.ID != id ||
					entry.Target != record.StatusTarget || entry.Event.PubKey.Hex() != record.ServicePubkey ||
					string(tx.Bucket(backupRunStatusEventsBucket).Get([]byte(record.StatusEventID))) != intentID {
					return fmt.Errorf("backup run refusal lacks its retryable signed outbox entry")
				}
			}
			result = &record
			return nil
		}
		if coords.Get([]byte(coordinate)) != nil || events.Get([]byte(requestEventID)) != nil {
			return fmt.Errorf("backup run pending coordinate or event belongs to another request")
		}
		return nil
	})
	return result, err
}

func validBackupRunPendingIdentity(record BackupRunPending) bool {
	if record.IntentID == "" || !strings.HasPrefix(record.Coordinate, "backup-run:") ||
		record.RequestEvent.Kind != nostr.Kind(kinds.CASControlState) ||
		!record.RequestEvent.CheckID() || !record.RequestEvent.VerifySignature() ||
		record.RequestEvent.PubKey.Hex() != record.Actor ||
		!oneBackupRunTag(record.RequestEvent.Tags, "d", record.Coordinate) ||
		!oneBackupRunTag(record.RequestEvent.Tags, "intent_id", record.IntentID) ||
		record.ExpiresAt.IsZero() || !record.ExpiresAt.After(record.ReceivedAt) || record.ServicePubkey == "" {
		return false
	}
	var signedExpiration int64
	seen := false
	for _, tag := range record.RequestEvent.Tags {
		if len(tag) == 0 || tag[0] != "expiration" {
			continue
		}
		if seen || len(tag) != 2 {
			return false
		}
		seen = true
		var err error
		signedExpiration, err = strconv.ParseInt(tag[1], 10, 64)
		if err != nil {
			return false
		}
	}
	return seen && signedExpiration == record.ExpiresAt.Unix() &&
		!record.ExpiresAt.After(record.RequestEvent.CreatedAt.Time().Add(15*time.Minute))
}

// PutBackupRunPending atomically records the full signed request and unique
// intent, coordinate and event bindings. No relay call or service signing is
// permitted on this ingress transaction.
func (o *Outbox) PutBackupRunPending(record BackupRunPending) (*BackupRunPending, bool, error) {
	if o == nil || o.shared == nil || o.shared.readOnly || o.shared.movedFrom != "" {
		return nil, false, fmt.Errorf("backup run pending ledger is unavailable after outbox loss")
	}
	if !validBackupRunPendingIdentity(record) {
		return nil, false, fmt.Errorf("backup run pending requires a complete signed request and bounded expiration")
	}
	if _, err := nostr.PubKeyFromHex(record.ServicePubkey); err != nil {
		return nil, false, fmt.Errorf("backup run pending service key: %w", err)
	}
	record.State = BackupRunPendingState
	record.ReceivedAt = record.ReceivedAt.UTC()
	record.ExpiresAt = record.ExpiresAt.UTC()
	record.NextAttemptAt = record.ReceivedAt
	record.LastFailure = ""
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, false, err
	}
	inserted := false
	o.shared.backupRunStatusMu.Lock()
	defer o.shared.backupRunStatusMu.Unlock()
	err = o.shared.db.Update(func(tx *bbolt.Tx) error {
		pending, coords, events := tx.Bucket(backupRunPendingBucket), tx.Bucket(backupRunPendingCoordsBucket), tx.Bucket(backupRunPendingEventsBucket)
		stagedCoords, stagedIntents := tx.Bucket(backupRunCoordsBucket), tx.Bucket(backupRunIntentsBucket)
		if pending == nil || coords == nil || events == nil || stagedCoords == nil || stagedIntents == nil {
			return fmt.Errorf("backup run pending or admission ledger is unavailable")
		}
		if raw := pending.Get([]byte(record.IntentID)); raw != nil {
			var prior BackupRunPending
			if err := json.Unmarshal(raw, &prior); err != nil {
				return err
			}
			if !validBackupRunPendingIdentity(prior) || prior.Coordinate != record.Coordinate || prior.RequestEvent.ID != record.RequestEvent.ID ||
				prior.Actor != record.Actor || prior.ServicePubkey != record.ServicePubkey ||
				string(coords.Get([]byte(record.Coordinate))) != record.IntentID ||
				string(events.Get([]byte(record.RequestEvent.ID.Hex()))) != record.IntentID {
				return fmt.Errorf("backup run pending intent conflicts with another signed request")
			}
			record = prior
			return nil
		}
		if coords.Get([]byte(record.Coordinate)) != nil || events.Get([]byte(record.RequestEvent.ID.Hex())) != nil ||
			tx.Bucket(backupRunCoordsBucket).Get([]byte(record.Coordinate)) != nil ||
			tx.Bucket(backupRunIntentsBucket).Get([]byte(record.IntentID)) != nil {
			return fmt.Errorf("backup run pending coordinate, event or intent is already owned")
		}
		if err := pending.Put([]byte(record.IntentID), encoded); err != nil {
			return err
		}
		if err := coords.Put([]byte(record.Coordinate), []byte(record.IntentID)); err != nil {
			return err
		}
		if err := events.Put([]byte(record.RequestEvent.ID.Hex()), []byte(record.IntentID)); err != nil {
			return err
		}
		inserted = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &record, inserted, nil
}

// ListBackupRunPending paginates the durable inbox. Refused records remain
// bound but are not returned to the history worker.
func (o *Outbox) ListBackupRunPending(after string, limit int) ([]BackupRunPending, string, error) {
	if o == nil || o.shared == nil {
		return nil, "", fmt.Errorf("backup run pending ledger is unavailable")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var records []BackupRunPending
	var next string
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(backupRunPendingBucket)
		if bucket == nil {
			return fmt.Errorf("backup run pending ledger is unavailable")
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
			var record BackupRunPending
			if err := json.Unmarshal(raw, &record); err != nil {
				return fmt.Errorf("decode backup run pending %q: %w", key, err)
			}
			if record.State == BackupRunPendingState {
				records = append(records, record)
			}
		}
		if key == nil {
			next = ""
		}
		return nil
	})
	return records, next, err
}

// ListBackupRunRefusalsNeedingStatus finds local terminal decisions not yet
// atomically paired with a signed status and outbox row. Startup replays this
// scan after a crash between refusal and status staging.
func (o *Outbox) ListBackupRunRefusalsNeedingStatus(after string, limit int) ([]BackupRunPending, string, error) {
	if o == nil || o.shared == nil {
		return nil, "", fmt.Errorf("backup run pending ledger is unavailable")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var records []BackupRunPending
	var next string
	var decodeErr error
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(backupRunPendingBucket)
		if bucket == nil {
			return fmt.Errorf("backup run pending ledger is unavailable")
		}
		cursor := bucket.Cursor()
		key, raw := cursor.First()
		if after != "" {
			key, raw = cursor.Seek([]byte(after))
			if bytes.Equal(key, []byte(after)) {
				key, raw = cursor.Next()
			}
		}
		for visited := 0; key != nil && visited < limit; key, raw = cursor.Next() {
			visited++
			next = string(key)
			var record BackupRunPending
			if err := json.Unmarshal(raw, &record); err != nil {
				decodeErr = errors.Join(decodeErr, fmt.Errorf("decode backup run pending %q: %w", key, err))
				continue
			}
			if record.State == BackupRunRefusedState && record.StatusEventID == "" {
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

// RecordBackupRunPendingAttempt stores retry and terminal-expiration state
// without ever creating a service-signed run. The caller supplies a bounded
// next attempt, and the ledger clamps it to the signed expiration.
func (o *Outbox) RecordBackupRunPendingAttempt(intentID, requestEventID string, now, next time.Time, reason string) error {
	if o == nil || o.shared == nil || o.shared.readOnly {
		return ErrReadOnly
	}
	o.shared.backupRunStatusMu.Lock()
	defer o.shared.backupRunStatusMu.Unlock()
	return o.shared.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(backupRunPendingBucket)
		if bucket == nil {
			return fmt.Errorf("backup run pending ledger is unavailable")
		}
		var record BackupRunPending
		if raw := bucket.Get([]byte(intentID)); raw == nil || json.Unmarshal(raw, &record) != nil || record.RequestEvent.ID.Hex() != requestEventID {
			return fmt.Errorf("backup run pending record changed before retry")
		}
		if record.State != BackupRunPendingState {
			return nil
		}
		if tx.Bucket(backupRunCoordsBucket).Get([]byte(record.Coordinate)) != nil {
			return nil
		}
		record.Attempts++
		record.LastFailure = reason
		if !now.Before(record.ExpiresAt) {
			record.State = BackupRunRefusedState
			record.NextAttemptAt = time.Time{}
		} else if next.IsZero() || next.After(record.ExpiresAt) {
			record.NextAttemptAt = record.ExpiresAt
		} else {
			record.NextAttemptAt = next.UTC()
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(intentID), encoded)
	})
}

// RefuseBackupRunPending records a definitive local history conflict. A
// failed relay subscription is not such a conflict and stays retryable.
func (o *Outbox) RefuseBackupRunPending(intentID, requestEventID, reason string) error {
	if o == nil || o.shared == nil || o.shared.readOnly {
		return ErrReadOnly
	}
	o.shared.backupRunStatusMu.Lock()
	defer o.shared.backupRunStatusMu.Unlock()
	return o.shared.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(backupRunPendingBucket)
		var record BackupRunPending
		if raw := bucket.Get([]byte(intentID)); raw == nil || json.Unmarshal(raw, &record) != nil || record.RequestEvent.ID.Hex() != requestEventID {
			return fmt.Errorf("backup run pending record changed before refusal")
		}
		if record.State != BackupRunPendingState {
			return nil
		}
		if tx.Bucket(backupRunCoordsBucket).Get([]byte(record.Coordinate)) != nil {
			return nil
		}
		record.State = BackupRunRefusedState
		record.LastFailure = reason
		record.NextAttemptAt = time.Time{}
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(intentID), raw)
	})
}

// StageBackupRunRefusedStatus commits the sole signed refusal and its delivery
// row with the pending ledger. A restart either sees an unstaged refusal or
// retries this exact signed event; it never signs a replacement.
func (o *Outbox) StageBackupRunRefusedStatus(record BackupRunPending, event nostr.Event, target string) (string, bool, error) {
	if o == nil || o.shared == nil || o.shared.readOnly {
		return "", false, ErrReadOnly
	}
	if record.State != BackupRunRefusedState || !validBackupRunPendingIdentity(record) ||
		!event.CheckID() || !event.VerifySignature() || event.PubKey.Hex() != record.ServicePubkey ||
		!isBackupRunRefusedStatusEvent(event) ||
		!oneBackupRunTag(event.Tags, "d", "intent-status:"+record.Actor+":"+record.Coordinate) ||
		!oneBackupRunTag(event.Tags, "status", "rejected") ||
		!oneBackupRunTag(event.Tags, "domain", "intent") ||
		!oneBackupRunTag(event.Tags, "t", "intent-status") ||
		!oneBackupRunTag(event.Tags, "intent_id", record.IntentID) ||
		!oneBackupRunTag(event.Tags, "p", record.Actor) ||
		!oneBackupRunTag(event.Tags, "e", record.RequestEvent.ID.Hex()) ||
		hasBackupRunTag(event.Tags, "expiration") {
		return "", false, fmt.Errorf("backup run refusal requires exact signed request correlation")
	}
	var content struct {
		IntentID   string `json:"intent_id"`
		Coordinate string `json:"coordinate"`
		Result     string `json:"result"`
		Reason     string `json:"reason"`
	}
	if json.Unmarshal([]byte(event.Content), &content) != nil || content.IntentID != record.IntentID ||
		content.Coordinate != record.Coordinate || content.Result != "rejected" || content.Reason != record.LastFailure || content.Reason == "" {
		return "", false, fmt.Errorf("backup run refusal content does not match the ledger")
	}
	entry := OutboxEntry{Event: event, Target: target, EntityType: "backup_run.status", EnqueuedAt: time.Now().UTC(), State: OutboxPending}
	encodedEntry, err := json.Marshal(entry)
	if err != nil {
		return "", false, err
	}
	var statusID string
	inserted := false
	o.shared.backupRunStatusMu.Lock()
	defer o.shared.backupRunStatusMu.Unlock()
	err = o.shared.db.Update(func(tx *bbolt.Tx) error {
		pending := tx.Bucket(backupRunPendingBucket)
		var current BackupRunPending
		if raw := pending.Get([]byte(record.IntentID)); raw == nil || json.Unmarshal(raw, &current) != nil {
			return fmt.Errorf("backup run refusal ledger record is unavailable")
		}
		if current.State != BackupRunRefusedState || current.Coordinate != record.Coordinate ||
			current.RequestEvent.ID != record.RequestEvent.ID || current.Actor != record.Actor ||
			current.ServicePubkey != record.ServicePubkey || current.LastFailure != record.LastFailure ||
			tx.Bucket(backupRunCoordsBucket).Get([]byte(record.Coordinate)) != nil {
			return fmt.Errorf("backup run refusal changed or coordinate was admitted")
		}
		if current.StatusEventID != "" {
			statusID = current.StatusEventID
			return nil
		}
		floor, err := backupRunStatusTimestampFloor(tx, backupRunStatusClockKey(event.PubKey, current.Actor, current.Coordinate))
		if err != nil {
			return err
		}
		if event.CreatedAt <= floor {
			return fmt.Errorf("backup run refusal is not newer than the durable coordinate floor")
		}
		if tx.Bucket(outboxEntriesBucket).Get(event.ID[:]) != nil {
			return fmt.Errorf("backup run refusal status event id already exists")
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
		raw, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := pending.Put([]byte(current.IntentID), raw); err != nil {
			return err
		}
		statusID = current.StatusEventID
		inserted = true
		return nil
	})
	return statusID, inserted, err
}

func updateBackupRunRefusalDelivery(tx *bbolt.Tx, entry OutboxEntry, intentID []byte) error {
	if !entry.Delivered {
		return nil
	}
	pending := tx.Bucket(backupRunPendingBucket)
	var record BackupRunPending
	if raw := pending.Get(intentID); raw == nil || json.Unmarshal(raw, &record) != nil ||
		record.State != BackupRunRefusedState || record.StatusEventID != entry.Event.ID.Hex() || record.StatusTarget != entry.Target {
		return fmt.Errorf("backup run refusal status index is inconsistent")
	}
	if record.StatusDelivered {
		return nil
	}
	var proof DeliveryProof
	raw := tx.Bucket(outboxDeliveryProofsBucket).Get(entry.Event.ID[:])
	if raw == nil || json.Unmarshal(raw, &proof) != nil || !proof.ValidFor(entry.Event, entry.Target) {
		return fmt.Errorf("backup run refusal status requires exact publisher relay quorum proof")
	}
	record.StatusDelivered = true
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return pending.Put(intentID, encoded)
}
