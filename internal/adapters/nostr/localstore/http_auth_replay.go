package localstore

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"go.etcd.io/bbolt"
)

var httpAuthReplayBucket = []byte("bahiaHTTPAuthReplayV1")
var httpAuthReplayExpiryBucket = []byte("bahiaHTTPAuthReplayExpiryV1")

// ClaimHTTPAuth atomically consumes a NIP-98 event ID in the durable outbox
// ledger. Expiry pruning is indexed and bounded per claim, not a history scan.
// This coordinates validators sharing this ledger, not independent hosts.
func (o *Outbox) ClaimHTTPAuth(ctx context.Context, eventID string, expiresAt time.Time) (bool, error) {
	if o == nil || o.shared == nil {
		return false, fmt.Errorf("HTTP authentication replay ledger is unavailable")
	}
	if o.shared.readOnly {
		return false, ErrReadOnly
	}
	if len(eventID) != 64 || strings.ToLower(eventID) != eventID {
		return false, fmt.Errorf("invalid HTTP authentication event ID")
	}
	if _, err := hex.DecodeString(eventID); err != nil {
		return false, fmt.Errorf("invalid HTTP authentication event ID")
	}
	now := time.Now()
	if !expiresAt.After(now) {
		return false, fmt.Errorf("HTTP authentication replay claim is expired")
	}
	var expiry [8]byte
	binary.BigEndian.PutUint64(expiry[:], uint64(expiresAt.UnixNano()))
	var claimed bool
	err := o.shared.db.Update(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		claims, err := tx.CreateBucketIfNotExists(httpAuthReplayBucket)
		if err != nil {
			return err
		}
		expirations, err := tx.CreateBucketIfNotExists(httpAuthReplayExpiryBucket)
		if err != nil {
			return err
		}
		cursor := expirations.Cursor()
		// One valid HTTP claim makes progress through at most 256 expired rows.
		pruned := 0
		for key, _ := cursor.First(); key != nil && pruned < 256; key, _ = cursor.Next() {
			if len(key) != 72 {
				return fmt.Errorf("corrupt HTTP authentication replay expiry")
			}
			if binary.BigEndian.Uint64(key[:8]) > uint64(now.UnixNano()) {
				break
			}
			if prior := claims.Get(key[8:]); bytes.Equal(prior, key[:8]) {
				if err := claims.Delete(key[8:]); err != nil {
					return err
				}
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			pruned++
		}
		key := []byte(eventID)
		if prior := claims.Get(key); prior != nil {
			if len(prior) != 8 {
				return fmt.Errorf("corrupt HTTP authentication replay claim")
			}
			if binary.BigEndian.Uint64(prior) > uint64(now.UnixNano()) {
				return nil
			}
			oldIndex := append(bytes.Clone(prior), key...)
			if err := expirations.Delete(oldIndex); err != nil {
				return err
			}
		}
		if err := claims.Put(key, expiry[:]); err != nil {
			return err
		}
		if err := expirations.Put(append(bytes.Clone(expiry[:]), key...), []byte{1}); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return claimed, nil
}
