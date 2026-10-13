package relaysidecar

import (
	"context"
	"encoding/binary"
	"fmt"

	"fiatjaf.com/nostr"
	"go.etcd.io/bbolt"
)

// The queue is a local event-store index, not a second source of truth. Its
// sequence is assigned once per event id after an accepted store write. A
// startup reconciliation closes the crash gap between the eventstore write
// and the index write without republishing acknowledged history.
func (s *eventStore) ensureMirrorIndex(ctx context.Context) error {
	// A SIGHUP candidate shares the store with the active runtime. Enable
	// indexing for both handles before scanning, so writes during the scan
	// cannot fall into a gap when mirror mode is first enabled.
	s.shared.mirrorIndexEnabled.Store(true)
	for event := range s.Query(ctx, nostr.Filter{}, 0) {
		if err := s.indexMirrorEvent(event); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *eventStore) indexMirrorEvent(event nostr.Event) error {
	return s.backend().DB.Update(func(tx *bbolt.Tx) error {
		ids := tx.Bucket(sidecarMirrorIDsBucket)
		if ids.Get(event.ID[:]) != nil {
			return nil
		}
		queue := tx.Bucket(sidecarMirrorQueueBucket)
		seq, err := queue.NextSequence()
		if err != nil {
			return fmt.Errorf("allocate mirror sequence: %w", err)
		}
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], seq)
		if err := queue.Put(key[:], event.ID[:]); err != nil {
			return fmt.Errorf("index mirror event: %w", err)
		}
		return ids.Put(event.ID[:], key[:])
	})
}

func (s *eventStore) mirrorNext(after uint64) (seq uint64, id nostr.ID, found bool, err error) {
	err = s.backend().DB.View(func(tx *bbolt.Tx) error {
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], after+1)
		k, value := tx.Bucket(sidecarMirrorQueueBucket).Cursor().Seek(key[:])
		if k == nil {
			return nil
		}
		if len(k) != 8 || len(value) != len(id) {
			return fmt.Errorf("corrupt relay mirror queue entry")
		}
		seq = binary.BigEndian.Uint64(k)
		copy(id[:], value)
		found = true
		return nil
	})
	return
}

func (s *eventStore) mirrorCursor(relayURL string) (uint64, error) {
	var cursor uint64
	err := s.backend().DB.View(func(tx *bbolt.Tx) error {
		value := tx.Bucket(sidecarMirrorCursorBucket).Get([]byte(relayURL))
		if len(value) == 0 {
			return nil
		}
		if len(value) != 8 {
			return fmt.Errorf("corrupt relay mirror cursor for %s", relayURL)
		}
		cursor = binary.BigEndian.Uint64(value)
		return nil
	})
	return cursor, err
}

func (s *eventStore) advanceMirrorCursor(relayURL string, cursor uint64) error {
	return s.backend().DB.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(sidecarMirrorCursorBucket)
		if prior := bucket.Get([]byte(relayURL)); len(prior) == 8 && binary.BigEndian.Uint64(prior) >= cursor {
			return nil
		}
		var value [8]byte
		binary.BigEndian.PutUint64(value[:], cursor)
		return bucket.Put([]byte(relayURL), value[:])
	})
}

func (s *eventStore) mirrorLastSequence() (uint64, error) {
	var last uint64
	err := s.backend().DB.View(func(tx *bbolt.Tx) error {
		last = tx.Bucket(sidecarMirrorQueueBucket).Sequence()
		return nil
	})
	return last, err
}
