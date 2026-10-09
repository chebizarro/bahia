package localstore

import (
	"encoding/json"
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
	IntentID       string `json:"intent_id"`
	Coordinate     string `json:"coordinate"`
	RequestEventID string `json:"request_event_id"`
	StateEventID   string `json:"state_event_id"`
	Delivered      bool   `json:"delivered"`
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

// EnqueueBackupRun atomically stages the signed state and its immutable
// request keys. A crash cannot leave a processed request without the event to
// deliver, or a queued event without the admission keys needed for replay.
func (o *Outbox) EnqueueBackupRun(entry OutboxEntry, intentID, coordinate, requestEventID string) (BackupRunAdmission, bool, error) {
	if o == nil || o.shared == nil || o.shared.readOnly {
		return BackupRunAdmission{}, false, ErrReadOnly
	}
	if intentID == "" || coordinate == "" || requestEventID == "" || strings.ContainsRune(intentID, 0) || strings.ContainsRune(coordinate, 0) {
		return BackupRunAdmission{}, false, fmt.Errorf("backup run admission requires valid request keys")
	}
	if _, err := nostr.IDFromHex(requestEventID); err != nil {
		return BackupRunAdmission{}, false, fmt.Errorf("invalid backup run request event id: %w", err)
	}
	if entry.Target == "" || !entry.Event.CheckID() || !entry.Event.VerifySignature() ||
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
	record := BackupRunAdmission{IntentID: intentID, Coordinate: coordinate, RequestEventID: requestEventID, StateEventID: entry.Event.ID.Hex()}
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
			if prior.IntentID != intentID || prior.Coordinate != coordinate || prior.RequestEventID != requestEventID ||
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
	if !entry.Delivered {
		return nil
	}
	accepted := false
	for _, relay := range entry.Relays {
		accepted = accepted || relay.Accepted
	}
	if !accepted {
		return nil
	}
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
	record.Delivered = true
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
