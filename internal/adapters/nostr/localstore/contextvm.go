package localstore

// The ContextVM request ledger is the daemon's
// at-most-once guard for ContextVM requests. It sits in this file next to the
// relay cursors that decide which requests are replayed, so the two are kept
// or lost together.
//
// Unlike the event cache, the ledger is not rebuildable from relays: it records
// which requests this daemon already executed. Deleting the file is still safe
// in the sense the package comment promises. A fresh ledger records a new
// epoch, and the ContextVM transport refuses requests created before that epoch
// (less a short grace), so a lost ledger can drop old requests but never runs
// them a second time.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"go.etcd.io/bbolt"
)

var (
	// contextVMRequestsBucket maps a request event id to its contextVMRecord.
	contextVMRequestsBucket = []byte("bahiaContextVMRequests")
	// contextVMKeysBucket maps an idempotency key to the request event id that
	// first claimed it.
	contextVMKeysBucket = []byte("bahiaContextVMKeys")
	// contextVMDeliveriesBucket maps the id of every event a relay delivered on
	// the request subscription (the outer gift wrap for wrapped requests) to
	// the unix second it was first processed.
	contextVMDeliveriesBucket = []byte("bahiaContextVMDeliveries")
	contextVMMetaBucket       = []byte("bahiaContextVMMeta")
	contextVMEpochKey         = []byte("epoch")
)

// contextVMCursorFilter gives a service's request subscription its own cursor
// identity, so no other subscription in this store can move its frontier.
const contextVMCursorFilter = "contextvm-requests-v1:"

// ContextVMRequest identifies one delivery of a ContextVM request.
type ContextVMRequest struct {
	// DeliveryID is the id of the event the relay delivered: the outer gift
	// wrap, or the request itself when it was sent as plaintext.
	DeliveryID string
	// RequestID is the id of the request event (the inner event of a wrap).
	RequestID string
	// Key is the requester-scoped idempotency key, empty when there is none.
	Key string
	// Fingerprint binds Key to the request parameters it was first used with.
	Fingerprint string
	// CreatedAt is the request event's own created_at.
	CreatedAt nostr.Timestamp
}

// ContextVMClaimState is the outcome of ClaimContextVMRequest.
type ContextVMClaimState uint8

const (
	// ContextVMClaimed means the request is new and now recorded as pending:
	// the caller executes it, then calls CompleteContextVMRequest.
	ContextVMClaimed ContextVMClaimState = iota + 1
	// ContextVMRedelivered means this exact delivery was processed before
	// (relay replay after a restart, or the same event from another relay).
	ContextVMRedelivered
	// ContextVMCompleted means the request, or an earlier request with the
	// same idempotency key, has finished. Response holds its stored terminal
	// response when one was kept (keyed requests only).
	ContextVMCompleted
	// ContextVMPending means the request, or an earlier request with the same
	// idempotency key, was claimed but never completed: another process is
	// running it, or a crash interrupted it. Its outcome is unknown.
	ContextVMPending
	// ContextVMKeyConflict means the idempotency key is bound to different
	// request parameters.
	ContextVMKeyConflict
)

// ContextVMClaim is the result of ClaimContextVMRequest.
type ContextVMClaim struct {
	State    ContextVMClaimState
	Response json.RawMessage
}

type contextVMRecord struct {
	Key         string          `json:"key,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	SeenAt      int64           `json:"seen_at"`
	Completed   bool            `json:"completed,omitempty"`
	Response    json.RawMessage `json:"response,omitempty"`
}

// ClaimContextVMRequest records the delivery and, unless the request (by id or
// by idempotency key) is already known, claims it as pending, all in one
// transaction. The claim is made before the handler runs: a pending claim is
// never handed out again, because its side effect may already have happened
// even though its response was never saved.
func (s *Store) ClaimContextVMRequest(req ContextVMRequest, now time.Time) (ContextVMClaim, error) {
	if req.DeliveryID == "" || req.RequestID == "" {
		return ContextVMClaim{}, errors.New("ContextVM delivery and request ids are required")
	}
	var claim ContextVMClaim
	err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		requests, keys, deliveries, err := contextVMBuckets(tx)
		if err != nil {
			return err
		}
		if deliveries.Get([]byte(req.DeliveryID)) != nil {
			claim.State = ContextVMRedelivered
			return nil
		}
		if err := deliveries.Put([]byte(req.DeliveryID), unixValue(now.Unix())); err != nil {
			return err
		}
		known, err := loadContextVMRecord(requests, req.RequestID)
		if err != nil {
			return err
		}
		if known == nil && req.Key != "" {
			if first := keys.Get([]byte(req.Key)); first != nil {
				if known, err = loadContextVMRecord(requests, string(first)); err != nil {
					return err
				}
				if known != nil && known.Fingerprint != req.Fingerprint {
					claim.State = ContextVMKeyConflict
					return nil
				}
			}
		}
		if known != nil {
			claim.State = ContextVMPending
			if known.Completed {
				claim.State = ContextVMCompleted
				claim.Response = append(json.RawMessage(nil), known.Response...)
			}
			return nil
		}
		record, err := json.Marshal(contextVMRecord{Key: req.Key, Fingerprint: req.Fingerprint, CreatedAt: int64(req.CreatedAt), SeenAt: now.Unix()})
		if err != nil {
			return err
		}
		if err := requests.Put([]byte(req.RequestID), record); err != nil {
			return err
		}
		if req.Key != "" {
			if err := keys.Put([]byte(req.Key), []byte(req.RequestID)); err != nil {
				return err
			}
		}
		claim.State = ContextVMClaimed
		return nil
	})
	if err != nil {
		return ContextVMClaim{}, fmt.Errorf("claim ContextVM request %s: %w", req.RequestID, err)
	}
	return claim, nil
}

// CompleteContextVMRequest marks a claimed request as finished. response is
// its terminal JSON-RPC response, kept for replay; pass nil to record only that
// the request ran. Completing a request twice keeps the first response.
func (s *Store) CompleteContextVMRequest(requestID string, response json.RawMessage) error {
	err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		requests, _, _, err := contextVMBuckets(tx)
		if err != nil {
			return err
		}
		record, err := loadContextVMRecord(requests, requestID)
		if err != nil {
			return err
		}
		if record == nil {
			return errors.New("request was never claimed")
		}
		if record.Completed {
			return nil
		}
		record.Completed = true
		record.Response = response
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return requests.Put([]byte(requestID), encoded)
	})
	if err != nil {
		return fmt.Errorf("complete ContextVM request %s: %w", requestID, err)
	}
	return nil
}

// ContextVMDelivered reports whether the event with this id was already
// processed from the request subscription.
func (s *Store) ContextVMDelivered(deliveryID string) (bool, error) {
	delivered := false
	err := s.backend().DB.View(func(tx *bbolt.Tx) error {
		if deliveries := tx.Bucket(contextVMDeliveriesBucket); deliveries != nil {
			delivered = deliveries.Get([]byte(deliveryID)) != nil
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("read ContextVM delivery %s: %w", deliveryID, err)
	}
	return delivered, nil
}

// MarkContextVMDelivery records that the event with this id was processed, so a
// relay replay of it is skipped. Requests are marked by ClaimContextVMRequest;
// this covers the other events the subscription carries (responses, rejected
// or misaddressed requests). Marking an id twice keeps the first time.
func (s *Store) MarkContextVMDelivery(deliveryID string, now time.Time) error {
	err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		_, _, deliveries, err := contextVMBuckets(tx)
		if err != nil {
			return err
		}
		if deliveries.Get([]byte(deliveryID)) != nil {
			return nil
		}
		return deliveries.Put([]byte(deliveryID), unixValue(now.Unix()))
	})
	if err != nil {
		return fmt.Errorf("mark ContextVM delivery %s: %w", deliveryID, err)
	}
	return nil
}

// ContextVMLedgerEpoch returns when this ledger was created, recording now on
// first use. The ledger knows about every request handled since its epoch and
// nothing before it.
func (s *Store) ContextVMLedgerEpoch(now time.Time) (time.Time, error) {
	var epoch int64
	err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(contextVMMetaBucket)
		if err != nil {
			return err
		}
		if value := meta.Get(contextVMEpochKey); len(value) == 8 {
			epoch = int64(binary.BigEndian.Uint64(value))
			return nil
		}
		epoch = now.Unix()
		return meta.Put(contextVMEpochKey, unixValue(epoch))
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("read ContextVM ledger epoch: %w", err)
	}
	return time.Unix(epoch, 0).UTC(), nil
}

// PruneContextVMLedger deletes request records whose request was created and
// first seen before cutoff, together with their idempotency keys, and delivery
// marks first seen before cutoff. It returns how many entries it removed. The
// caller picks a cutoff older than both the oldest request it still accepts
// and the oldest event its resume filter can fetch, so a pruned entry can
// never be needed again.
func (s *Store) PruneContextVMLedger(cutoff time.Time) (int, error) {
	limit := cutoff.Unix()
	removed := 0
	err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		requests, keys, deliveries, err := contextVMBuckets(tx)
		if err != nil {
			return err
		}
		var expiredRequests [][]byte
		var expiredKeys []string
		if err := requests.ForEach(func(id, raw []byte) error {
			var record contextVMRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return fmt.Errorf("decode ContextVM request %s: %w", id, err)
			}
			if record.CreatedAt < limit && record.SeenAt < limit {
				expiredRequests = append(expiredRequests, append([]byte(nil), id...))
				if record.Key != "" && string(keys.Get([]byte(record.Key))) == string(id) {
					expiredKeys = append(expiredKeys, record.Key)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		var expiredDeliveries [][]byte
		if err := deliveries.ForEach(func(id, value []byte) error {
			if len(value) != 8 || int64(binary.BigEndian.Uint64(value)) < limit {
				expiredDeliveries = append(expiredDeliveries, append([]byte(nil), id...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, id := range expiredRequests {
			if err := requests.Delete(id); err != nil {
				return err
			}
		}
		for _, key := range expiredKeys {
			if err := keys.Delete([]byte(key)); err != nil {
				return err
			}
		}
		for _, id := range expiredDeliveries {
			if err := deliveries.Delete(id); err != nil {
				return err
			}
		}
		removed = len(expiredRequests) + len(expiredDeliveries)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("prune ContextVM ledger: %w", err)
	}
	return removed, nil
}

// ContextVMCursor returns the committed request-subscription cursor for one
// relay and service, or 0 when there is none.
func (s *Store) ContextVMCursor(relayURL, servicePubkey string) (nostr.Timestamp, error) {
	return s.Cursor(relayURL, contextVMCursorFilter+servicePubkey)
}

// AdvanceContextVMCursor raises that cursor to to; it never moves backwards.
func (s *Store) AdvanceContextVMCursor(relayURL, servicePubkey string, to nostr.Timestamp) error {
	return s.AdvanceCursor(relayURL, contextVMCursorFilter+servicePubkey, to)
}

func contextVMBuckets(tx *bbolt.Tx) (requests, keys, deliveries *bbolt.Bucket, err error) {
	if requests, err = tx.CreateBucketIfNotExists(contextVMRequestsBucket); err != nil {
		return nil, nil, nil, err
	}
	if keys, err = tx.CreateBucketIfNotExists(contextVMKeysBucket); err != nil {
		return nil, nil, nil, err
	}
	if deliveries, err = tx.CreateBucketIfNotExists(contextVMDeliveriesBucket); err != nil {
		return nil, nil, nil, err
	}
	return requests, keys, deliveries, nil
}

func loadContextVMRecord(requests *bbolt.Bucket, requestID string) (*contextVMRecord, error) {
	raw := requests.Get([]byte(requestID))
	if raw == nil {
		return nil, nil
	}
	var record contextVMRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode ContextVM request %s: %w", requestID, err)
	}
	return &record, nil
}

func unixValue(seconds int64) []byte {
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, uint64(seconds))
	return value
}
