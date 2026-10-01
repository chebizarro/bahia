package relaysidecar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"iter"

	"fiatjaf.com/nostr"
	"go.etcd.io/bbolt"
)

// tagIndexMaxValue is the longest tag value the bolt eventstore indexes
// (eventstore/boltdb getIndexKeysForEvent). It indexes neither longer values
// nor empty ones, so a tag filter on such a value matches nothing.
const tagIndexMaxValue = 100

func tagIndexable(value string) bool {
	return value != "" && len(value) <= tagIndexMaxValue
}

// sidecarDeletionBucket indexes the coordinates that stored kind-5 requests
// delete (NIP-09 `a` references). The eventstore's tag index cannot answer
// this: a coordinate is "<kind>:<pubkey>:<d>", already 67 bytes before d, so a
// d of 30 bytes or more takes it past tagIndexMaxValue. Relay config
// coordinates are 108 bytes.
//
// A key is sha256(coordinate) ‖ request created_at (8 bytes, big endian) ‖
// request id, with an empty value. checkNotDeleted therefore costs one cursor
// seek however long the coordinate is and however many requests the author has
// stored. The index holds exactly the stored requests' references: an entry is
// written before its request is saved and removed when the request is (only the
// NIP-40 sweep removes one; retention keeps tombstones for good).
var sidecarDeletionBucket = []byte("bahiaSidecarDeletions")

// deletionIndexVersionKey marks, in sidecarMetaBucket, that every stored kind-5
// request has been indexed (see buildDeletionIndex).
var deletionIndexVersionKey = []byte("deletionIndexVersion")

const deletionIndexVersion = "1"

// coordinate is a replaceable or addressable event's address. d is always
// empty for plain replaceable kinds, which ignore it.
type coordinate struct {
	kind   nostr.Kind
	author nostr.PubKey
	d      string
}

func newCoordinate(kind nostr.Kind, author nostr.PubKey, d string) coordinate {
	if !kind.IsAddressable() {
		d = ""
	}
	return coordinate{kind: kind, author: author, d: d}
}

func coordinateOf(event nostr.Event) coordinate {
	return newCoordinate(event.Kind, event.PubKey, event.Tags.GetD())
}

func (c coordinate) String() string { return addressOf(c.kind, c.author, c.d) }

// dIndexed reports whether the eventstore's #d index can find this
// coordinate's versions.
func (c coordinate) dIndexed() bool { return !c.kind.IsAddressable() || tagIndexable(c.d) }

// deletedCoordinates are the coordinates a kind-5 request deletes: its `a`
// references to the requester's own replaceable or addressable events.
func deletedCoordinates(request nostr.Event) []coordinate {
	var coordinates []coordinate
	for _, tag := range request.Tags {
		if len(tag) < 2 || tag[0] != "a" {
			continue
		}
		kind, author, d, ok := parseAddress(tag[1])
		if !ok || author != request.PubKey || (!kind.IsReplaceable() && !kind.IsAddressable()) {
			continue
		}
		c := newCoordinate(kind, author, d)
		if !containsCoordinate(coordinates, c) {
			coordinates = append(coordinates, c)
		}
	}
	return coordinates
}

func containsCoordinate(coordinates []coordinate, c coordinate) bool {
	for _, existing := range coordinates {
		if existing == c {
			return true
		}
	}
	return false
}

func deletionKeyPrefix(c coordinate) []byte {
	sum := sha256.Sum256([]byte(c.String()))
	return sum[:]
}

func deletionKey(c coordinate, request nostr.Event) []byte {
	key := make([]byte, 0, sha256.Size+8+len(request.ID))
	key = append(key, deletionKeyPrefix(c)...)
	key = binary.BigEndian.AppendUint64(key, uint64(request.CreatedAt))
	return append(key, request.ID[:]...)
}

// indexDeletion records the coordinates request deletes.
func (s *eventStore) indexDeletion(request nostr.Event) error {
	coordinates := deletedCoordinates(request)
	if len(coordinates) == 0 {
		return nil
	}
	if err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(sidecarDeletionBucket)
		for _, c := range coordinates {
			if err := bucket.Put(deletionKey(c, request), nil); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("index relay deletion request: %w", err)
	}
	return nil
}

// unindexDeletion removes request's entries, when the request itself goes.
func (s *eventStore) unindexDeletion(request nostr.Event) error {
	coordinates := deletedCoordinates(request)
	if len(coordinates) == 0 {
		return nil
	}
	if err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(sidecarDeletionBucket)
		for _, c := range coordinates {
			if err := bucket.Delete(deletionKey(c, request)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("unindex relay deletion request: %w", err)
	}
	return nil
}

// coordinateDeleted reports whether a stored request of the coordinate's author
// deletes it at or after since.
func (s *eventStore) coordinateDeleted(c coordinate, since nostr.Timestamp) (bool, error) {
	prefix := deletionKeyPrefix(c)
	seek := binary.BigEndian.AppendUint64(bytes.Clone(prefix), uint64(since))
	var deleted bool
	if err := s.backend().DB.View(func(tx *bbolt.Tx) error {
		key, _ := tx.Bucket(sidecarDeletionBucket).Cursor().Seek(seek)
		deleted = key != nil && bytes.HasPrefix(key, prefix)
		return nil
	}); err != nil {
		return false, fmt.Errorf("read relay deletion index: %w", err)
	}
	return deleted, nil
}

// buildDeletionIndex indexes every stored kind-5 request once per store, for
// stores written before the index existed. Requests saved later are indexed as
// they are saved, so the marker makes later opens free.
func (s *eventStore) buildDeletionIndex(ctx context.Context) error {
	var built bool
	if err := s.backend().DB.View(func(tx *bbolt.Tx) error {
		built = string(tx.Bucket(sidecarMetaBucket).Get(deletionIndexVersionKey)) == deletionIndexVersion
		return nil
	}); err != nil {
		return fmt.Errorf("read relay deletion index marker: %w", err)
	}
	if built {
		return nil
	}
	var keys [][]byte
	flush := func(done bool) error {
		if err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
			bucket := tx.Bucket(sidecarDeletionBucket)
			for _, key := range keys {
				if err := bucket.Put(key, nil); err != nil {
					return err
				}
			}
			if done {
				return tx.Bucket(sidecarMetaBucket).Put(deletionIndexVersionKey, []byte(deletionIndexVersion))
			}
			return nil
		}); err != nil {
			return fmt.Errorf("build relay deletion index: %w", err)
		}
		keys = keys[:0]
		return nil
	}
	// scan copies each page out of its read transaction, so writing while
	// iterating is safe.
	for request := range s.scan(nostr.Filter{Kinds: []nostr.Kind{nostr.KindDeletion}}, unboundedQueryLimit) {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, c := range deletedCoordinates(request) {
			keys = append(keys, deletionKey(c, request))
		}
		if len(keys) >= sweepBatchSize {
			if err := flush(false); err != nil {
				return err
			}
		}
	}
	return flush(true)
}

// versions yields the stored versions of c created at or before until (0: no
// bound), newest first, at most limit. A d the eventstore does not index (empty
// or longer than tagIndexMaxValue) is matched here, over the author's events
// of that kind: latest-wins keeps that to about one event per coordinate.
func (s *eventStore) versions(c coordinate, until nostr.Timestamp, limit int) iter.Seq[nostr.Event] {
	filter := nostr.Filter{Kinds: []nostr.Kind{c.kind}, Authors: []nostr.PubKey{c.author}, Until: until}
	if c.dIndexed() {
		if c.kind.IsAddressable() {
			filter.Tags = nostr.TagMap{"d": []string{c.d}}
		}
		return s.scan(filter, limit)
	}
	return func(yield func(nostr.Event) bool) {
		emitted := 0
		for event := range s.scan(filter, unboundedQueryLimit) {
			if event.Tags.GetD() != c.d {
				continue
			}
			if !yield(event) {
				return
			}
			emitted++
			if emitted >= limit {
				return
			}
		}
	}
}

// latestVersion returns the newest stored version of c.
func (s *eventStore) latestVersion(c coordinate) (nostr.Event, bool) {
	for event := range s.versions(c, 0, 1) {
		return event, true
	}
	return nostr.Event{}, false
}
