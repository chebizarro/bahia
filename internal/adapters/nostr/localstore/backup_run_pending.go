package localstore

import (
	"bytes"
	"encoding/json"
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
	IntentID      string      `json:"intent_id"`
	Coordinate    string      `json:"coordinate"`
	RequestEvent  nostr.Event `json:"request_event"`
	Actor         string      `json:"actor"`
	ServicePubkey string      `json:"service_pubkey"`
	ReceivedAt    time.Time   `json:"received_at"`
	ExpiresAt     time.Time   `json:"expires_at"`
	NextAttemptAt time.Time   `json:"next_attempt_at,omitempty"`
	Attempts      int         `json:"attempts,omitempty"`
	LastFailure   string      `json:"last_failure,omitempty"`
	State         string      `json:"state"`
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
// bound forever but are not returned to the history worker.
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

// RecordBackupRunPendingAttempt stores retry and terminal-expiration state
// without ever creating a service-signed run. The caller supplies a bounded
// next attempt, and the ledger clamps it to the signed expiration.
func (o *Outbox) RecordBackupRunPendingAttempt(intentID, requestEventID string, now, next time.Time, reason string) error {
	if o == nil || o.shared == nil || o.shared.readOnly {
		return ErrReadOnly
	}
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
