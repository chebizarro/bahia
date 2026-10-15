package localstore

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.etcd.io/bbolt"
)

const (
	BackupRunPendingState  = "pending"
	BackupRunAcceptedState = "accepted"
	BackupRunRefusedState  = "refused"
)

// BackupRunPending holds a signed operator request while admission or effect
// recovery still needs it, then retains its compact exact-identity proof.
// It lives with the outbox, never in the rebuildable inbound cache or
// PostgreSQL.
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

// indexBackupRunPending upgrades old outboxes once. Only live work is indexed;
// terminal identity bindings stay in the primary ledger for exact replay.
func indexBackupRunPending(tx *bbolt.Tx) error {
	due, refusals := tx.Bucket(backupRunDueBucket), tx.Bucket(backupRunRefusalsBucket)
	if due.Sequence() == 1 && refusals.Sequence() == 1 {
		return nil
	}
	if err := due.SetSequence(1); err != nil {
		return err
	}
	if err := refusals.SetSequence(1); err != nil {
		return err
	}
	cursor := tx.Bucket(backupRunPendingBucket).Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		var record BackupRunPending
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("index backup run pending %q: %w", key, err)
		}
		if record.IntentID != string(key) {
			return fmt.Errorf("index backup run pending %q: intent mismatch", key)
		}
		switch record.State {
		case BackupRunPendingState:
			if err := due.Put(backupRunDueKey(record), nil); err != nil {
				return err
			}
		case BackupRunRefusedState:
			if record.StatusEventID == "" {
				if err := refusals.Put(key, nil); err != nil {
					return err
				}
			} else {
				compactBackupRunPending(&record)
				if err := putBackupRunPending(tx, record); err != nil {
					return err
				}
			}
		case BackupRunAcceptedState:
			// Execution still needs the signed request until its terminal
			// status has exact publisher-quorum proof.
		default:
			return fmt.Errorf("index backup run pending %q: unknown state %q", key, record.State)
		}
	}
	return nil
}

func backupRunDueKey(record BackupRunPending) []byte {
	at := record.NextAttemptAt
	if at.IsZero() {
		at = record.ReceivedAt
	}
	key := make([]byte, 8, 8+len(record.IntentID))
	binary.BigEndian.PutUint64(key, uint64(at.UnixNano())^(uint64(1)<<63))
	return append(key, record.IntentID...)
}

func backupRunDueAt(key []byte) time.Time {
	return time.Unix(0, int64(binary.BigEndian.Uint64(key[:8])^(uint64(1)<<63))).UTC()
}

func compactBackupRunPending(record *BackupRunPending) {
	record.RequestEvent = nostr.Event{ID: record.RequestEvent.ID, PubKey: record.RequestEvent.PubKey, Sig: record.RequestEvent.Sig}
	record.NextAttemptAt = time.Time{}
	record.Attempts = 0
	if record.State == BackupRunAcceptedState {
		record.LastFailure = ""
	}
}

func putBackupRunPending(tx *bbolt.Tx, record BackupRunPending) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return tx.Bucket(backupRunPendingBucket).Put([]byte(record.IntentID), raw)
}

// CompactBackupRunAccepted drops the signed request body only after the
// executor's terminal status has exact publisher-quorum proof. Until then the
// executor still needs the full request to recover an interrupted effect.
func (o *Outbox) CompactBackupRunAccepted(intentID, coordinate, requestEventID string, terminal nostr.Event, target string) error {
	if o == nil || o.shared == nil || o.shared.readOnly || terminal.ID == nostr.ZeroID || target == "" {
		return ErrReadOnly
	}
	o.shared.backupRunStatusMu.Lock()
	defer o.shared.backupRunStatusMu.Unlock()
	return o.shared.db.Update(func(tx *bbolt.Tx) error {
		var record BackupRunPending
		raw := tx.Bucket(backupRunPendingBucket).Get([]byte(intentID))
		if raw == nil || json.Unmarshal(raw, &record) != nil || !validRetainedBackupRunIdentity(record) ||
			record.State != BackupRunAcceptedState || record.Coordinate != coordinate || record.RequestEvent.ID.Hex() != requestEventID ||
			string(tx.Bucket(backupRunPendingCoordsBucket).Get([]byte(coordinate))) != intentID ||
			string(tx.Bucket(backupRunPendingEventsBucket).Get([]byte(requestEventID))) != intentID {
			return fmt.Errorf("backup run accepted request is unavailable for compaction")
		}
		var admission BackupRunAdmission
		raw = tx.Bucket(backupRunIntentsBucket).Get([]byte(intentID))
		if raw == nil || json.Unmarshal(raw, &admission) != nil || admission.Coordinate != coordinate ||
			admission.RequestEventID != requestEventID || !admission.StatusDelivered || admission.StatusOutcome != "accepted" {
			return fmt.Errorf("backup run accepted status lacks durable proof")
		}
		var proof DeliveryProof
		raw = tx.Bucket(outboxDeliveryProofsBucket).Get(terminal.ID[:])
		if raw == nil || json.Unmarshal(raw, &proof) != nil || !proof.ValidFor(terminal, target) ||
			terminal.Kind != 30315 || terminal.PubKey.Hex() != record.ServicePubkey ||
			!oneBackupRunTag(terminal.Tags, "d", "intent-status:"+record.Actor+":"+coordinate) ||
			!oneBackupRunTag(terminal.Tags, "e", requestEventID) ||
			!oneBackupRunTag(terminal.Tags, "intent_id", intentID) ||
			!oneBackupRunTag(terminal.Tags, "p", record.Actor) ||
			(!oneBackupRunTag(terminal.Tags, "status", "success") && !oneBackupRunTag(terminal.Tags, "status", "error")) {
			return fmt.Errorf("backup run terminal status lacks exact relay quorum proof")
		}
		if record.RequestEvent.Kind == 0 {
			return nil
		}
		compactBackupRunPending(&record)
		return putBackupRunPending(tx, record)
	})
}

func validRetainedBackupRunIdentity(record BackupRunPending) bool {
	if record.State == BackupRunPendingState || record.StatusEventID == "" && record.State == BackupRunRefusedState {
		return validBackupRunPendingIdentity(record)
	}
	if record.State != BackupRunAcceptedState && record.State != BackupRunRefusedState {
		return false
	}
	if record.RequestEvent.Kind != 0 {
		return validBackupRunPendingIdentity(record)
	}
	if record.IntentID == "" || !strings.HasPrefix(record.Coordinate, "backup-run:") ||
		record.RequestEvent.ID == nostr.ZeroID || record.RequestEvent.PubKey.Hex() != record.Actor ||
		record.RequestEvent.CreatedAt != 0 || len(record.RequestEvent.Tags) != 0 || record.RequestEvent.Content != "" ||
		record.ServicePubkey == "" || record.ExpiresAt.IsZero() || !record.ExpiresAt.After(record.ReceivedAt) {
		return false
	}
	pubkey, err := schnorr.ParsePubKey(record.RequestEvent.PubKey[:])
	if err != nil {
		return false
	}
	sig, err := schnorr.ParseSignature(record.RequestEvent.Sig[:])
	return err == nil && sig.Verify(record.RequestEvent.ID[:], pubkey)
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
			if !validRetainedBackupRunIdentity(record) || record.IntentID != intentID || record.Coordinate != coordinate || record.RequestEvent.ID.Hex() != requestEventID ||
				string(coords.Get([]byte(coordinate))) != intentID || string(events.Get([]byte(requestEventID))) != intentID {
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
					outboxTag(proof.Event.Tags, "d") != kinds.IntentPrivateStatusCoordinate(record.Actor, "backup", record.IntentID) ||
					!oneBackupRunTag(proof.Event.Tags, "p", record.Actor) ||
					!oneBackupRunTag(proof.Event.Tags, "intent_id", record.IntentID) ||
					!oneBackupRunTag(proof.Event.Tags, "e", record.RequestEvent.ID.Hex()) ||
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
					!entry.Event.CheckID() || !entry.Event.VerifySignature() ||
					!oneBackupRunTag(entry.Event.Tags, "d", kinds.IntentPrivateStatusCoordinate(record.Actor, "backup", record.IntentID)) ||
					!oneBackupRunTag(entry.Event.Tags, "p", record.Actor) ||
					!oneBackupRunTag(entry.Event.Tags, "intent_id", record.IntentID) ||
					!oneBackupRunTag(entry.Event.Tags, "e", record.RequestEvent.ID.Hex()) ||
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
			if !validRetainedBackupRunIdentity(prior) || prior.Coordinate != record.Coordinate || prior.RequestEvent.ID != record.RequestEvent.ID ||
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
		if err := tx.Bucket(backupRunDueBucket).Put(backupRunDueKey(record), nil); err != nil {
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

// ListBackupRunPendingDue traverses only active requests whose retry or
// expiration deadline has arrived. The cursor is an opaque due-index key;
// nextWake is the first future deadline, if any.
func (o *Outbox) ListBackupRunPendingDue(after string, now time.Time, limit int) ([]BackupRunPending, string, time.Time, error) {
	if o == nil || o.shared == nil {
		return nil, "", time.Time{}, fmt.Errorf("backup run pending ledger is unavailable")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var records []BackupRunPending
	var next string
	var nextWake time.Time
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		index, pending := tx.Bucket(backupRunDueBucket), tx.Bucket(backupRunPendingBucket)
		if index == nil || pending == nil {
			return fmt.Errorf("backup run due index is unavailable")
		}
		cursor := index.Cursor()
		key, _ := cursor.First()
		if after != "" {
			key, _ = cursor.Seek([]byte(after))
			if bytes.Equal(key, []byte(after)) {
				key, _ = cursor.Next()
			}
		}
		for key != nil && len(records) < limit {
			if len(key) < 9 {
				return fmt.Errorf("backup run due index has malformed key")
			}
			at := backupRunDueAt(key)
			if at.After(now) {
				nextWake = at
				return nil
			}
			intentID := key[8:]
			var record BackupRunPending
			if raw := pending.Get(intentID); raw == nil || json.Unmarshal(raw, &record) != nil ||
				record.State != BackupRunPendingState || record.IntentID != string(intentID) ||
				!bytes.Equal(backupRunDueKey(record), key) {
				return fmt.Errorf("backup run due index %q is inconsistent", intentID)
			}
			records = append(records, record)
			next = string(key)
			key, _ = cursor.Next()
		}
		if key != nil && len(key) < 9 {
			return fmt.Errorf("backup run due index has malformed key")
		}
		if key == nil {
			next = ""
		} else if backupRunDueAt(key).After(now) {
			nextWake = backupRunDueAt(key)
			next = ""
		}
		return nil
	})
	return records, next, nextWake, err
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
		bucket, index := tx.Bucket(backupRunPendingBucket), tx.Bucket(backupRunRefusalsBucket)
		if bucket == nil || index == nil {
			return fmt.Errorf("backup run pending ledger is unavailable")
		}
		cursor := index.Cursor()
		key, _ := cursor.First()
		if after != "" {
			key, _ = cursor.Seek([]byte(after))
			if bytes.Equal(key, []byte(after)) {
				key, _ = cursor.Next()
			}
		}
		for visited := 0; key != nil && visited < limit; key, _ = cursor.Next() {
			visited++
			next = string(key)
			var record BackupRunPending
			if raw := bucket.Get(key); raw == nil || json.Unmarshal(raw, &record) != nil {
				decodeErr = errors.Join(decodeErr, fmt.Errorf("decode backup run pending %q", key))
				continue
			}
			if record.State != BackupRunRefusedState || record.StatusEventID != "" {
				decodeErr = errors.Join(decodeErr, fmt.Errorf("backup run refusal index %q is inconsistent", key))
				continue
			}
			records = append(records, record)
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
		oldDue := backupRunDueKey(record)
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
		if err := bucket.Put([]byte(intentID), encoded); err != nil {
			return err
		}
		if err := tx.Bucket(backupRunDueBucket).Delete(oldDue); err != nil {
			return err
		}
		if record.State == BackupRunRefusedState {
			return tx.Bucket(backupRunRefusalsBucket).Put([]byte(intentID), nil)
		}
		return tx.Bucket(backupRunDueBucket).Put(backupRunDueKey(record), nil)
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
		oldDue := backupRunDueKey(record)
		record.State = BackupRunRefusedState
		record.LastFailure = reason
		record.NextAttemptAt = time.Time{}
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(intentID), raw); err != nil {
			return err
		}
		if err := tx.Bucket(backupRunDueBucket).Delete(oldDue); err != nil {
			return err
		}
		return tx.Bucket(backupRunRefusalsBucket).Put([]byte(intentID), nil)
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
		!oneBackupRunTag(event.Tags, "d", kinds.IntentPrivateStatusCoordinate(record.Actor, "backup", record.IntentID)) ||
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
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(event.Content), &content) != nil || json.Unmarshal([]byte(event.Content), &fields) != nil ||
		content.IntentID != record.IntentID || content.Coordinate != "" || content.Result != "rejected" ||
		content.Reason != "access_denied" || fields["coordinate"] != nil {
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
		privateFloor, err := backupRunStatusTimestampFloor(tx, backupRunStatusEventClockKey(event))
		if err != nil {
			return err
		}
		if event.CreatedAt <= max(floor, privateFloor) {
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
		if err := recordBackupRunCoordinateStatusTimestamp(tx, event, current.Coordinate); err != nil {
			return err
		}
		current.StatusEventID = event.ID.Hex()
		current.StatusTarget = target
		compactBackupRunPending(&current)
		raw, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := pending.Put([]byte(current.IntentID), raw); err != nil {
			return err
		}
		if err := tx.Bucket(backupRunRefusalsBucket).Delete([]byte(current.IntentID)); err != nil {
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
