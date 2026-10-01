package boltcoord

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"iter"
	"math"
	"slices"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/boltdb"
	"go.etcd.io/bbolt"
)

// tagIndex indexes, in one bucket of the eventstore's bbolt file, the tag
// values the eventstore does not: for every stored event, each single-letter
// tag (the only ones NIP-01 filters address) whose value is empty or longer
// than TagIndexMaxValue. A key is tag name (1 byte) ‖ sha256(value) ‖
// created_at (8 bytes, big endian) ‖ event id (32 bytes), with an empty value,
// so a filter value of any length is one cursor range, read newest first.
//
// Store indexes an event before saving it and unindexes it after deleting it,
// so the index can hold an entry whose event is gone (after a crash between
// the two writes), never the reverse. Readers fetch every event they yield and
// check it, so such an entry is skipped.
type tagIndex struct {
	db     *bbolt.DB
	bucket []byte
}

// tagPageSize bounds the keys one read transaction of the tag index collects.
const tagPageSize = 512

// unindexedTag reports a tag the eventstore does not index and tagIndex does.
func unindexedTag(tag nostr.Tag) bool {
	return len(tag) >= 2 && len(tag[0]) == 1 && !tagIndexable(tag[1])
}

func tagKeyPrefix(name, value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return append([]byte{name[0]}, sum[:]...)
}

// tagKeys are the tag index entries of event, one per distinct unindexed
// (name, value) pair.
func tagKeys(event nostr.Event) [][]byte {
	var keys [][]byte
	for i, tag := range event.Tags {
		if !unindexedTag(tag) {
			continue
		}
		if slices.ContainsFunc(event.Tags[:i], func(t nostr.Tag) bool {
			return len(t) >= 2 && t[0] == tag[0] && t[1] == tag[1]
		}) {
			continue
		}
		key := binary.BigEndian.AppendUint64(tagKeyPrefix(tag[0], tag[1]), uint64(event.CreatedAt))
		keys = append(keys, append(key, event.ID[:]...))
	}
	return keys
}

func (ix tagIndex) index(event nostr.Event) error {
	return ix.apply(event, func(bucket *bbolt.Bucket, key []byte) error { return bucket.Put(key, nil) })
}

func (ix tagIndex) unindex(event nostr.Event) error {
	return ix.apply(event, func(bucket *bbolt.Bucket, key []byte) error { return bucket.Delete(key) })
}

func (ix tagIndex) apply(event nostr.Event, op func(*bbolt.Bucket, []byte) error) error {
	keys := tagKeys(event)
	if len(keys) == 0 {
		return nil
	}
	return ix.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(ix.bucket)
		for _, key := range keys {
			if err := op(bucket, key); err != nil {
				return err
			}
		}
		return nil
	})
}

// events yields the stored events with a tag (name, value), newest first,
// created within [since, until] (0: unbounded). It reads at most tagPageSize
// keys per read transaction and fetches their events after it ends, so no
// transaction is held while it yields. Entries whose event is gone, or whose
// stored event does not carry the tag, are skipped. A read error ends the
// sequence early, as it does the eventstore's own queries.
func (ix tagIndex) events(backend *boltdb.BoltBackend, name, value string, since, until nostr.Timestamp) iter.Seq[nostr.Event] {
	return func(yield func(nostr.Event) bool) {
		prefix := tagKeyPrefix(name, value)
		upper := uint64(math.MaxUint64)
		if until > 0 {
			upper = uint64(until) + 1
		}
		// bound is exclusive: each page starts at the newest key below it.
		bound := binary.BigEndian.AppendUint64(bytes.Clone(prefix), upper)
		for {
			var ids []nostr.ID
			exhausted := false
			err := ix.db.View(func(tx *bbolt.Tx) error {
				cursor := tx.Bucket(ix.bucket).Cursor()
				key, _ := cursor.Seek(bound)
				if key == nil {
					key, _ = cursor.Last()
				} else {
					key, _ = cursor.Prev()
				}
				for ; len(ids) < tagPageSize; key, _ = cursor.Prev() {
					if key == nil || !bytes.HasPrefix(key, prefix) {
						exhausted = true
						return nil
					}
					createdAt := nostr.Timestamp(binary.BigEndian.Uint64(key[len(prefix):]))
					if since > 0 && createdAt < since {
						exhausted = true
						return nil
					}
					ids = append(ids, nostr.ID(key[len(prefix)+8:]))
					bound = bytes.Clone(key)
				}
				return nil
			})
			if err != nil || len(ids) == 0 {
				return
			}
			held := make(map[nostr.ID]nostr.Event, len(ids))
			for event := range backend.QueryEvents(nostr.Filter{IDs: ids}, len(ids)) {
				held[event.ID] = event
			}
			for _, id := range ids {
				event, ok := held[id]
				if !ok || !event.Tags.ContainsAny(name, []string{value}) {
					continue
				}
				if !yield(event) {
					return
				}
			}
			if exhausted {
				return
			}
		}
	}
}
