// Package boltcoord resolves NIP-01 coordinates and NIP-09 `a` deletions over
// the bolt eventstore (fiatjaf.com/nostr/eventstore/boltdb) for coordinates of
// any length. Both Bahia stores built on it use this package: the relay
// sidecar's events.bolt and the daemon's local store (bahia-irsry.44,
// bahia-irsry.51).
//
// The eventstore indexes no tag value that is empty or longer than
// TagIndexMaxValue bytes, so its #d and #a lookups cannot find such values:
//   - ReplaceEvent finds the versions an event supersedes through #d, so for a
//     d it does not index it supersedes nothing and every version is kept.
//     Versions and Replace match such a d here instead, over the author's
//     events of that kind.
//   - A coordinate is "<kind>:<pubkey>:<d>", already 67 bytes before d, so any
//     d of 30 bytes or more takes it past the limit and an #a lookup for a
//     deletion request naming it finds nothing. DeletionIndex keeps a hashed
//     index of the coordinates stored requests delete instead.
package boltcoord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"iter"
	"math"
	"slices"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/boltdb"
	"go.etcd.io/bbolt"
)

// TagIndexMaxValue is the longest tag value the bolt eventstore indexes
// (eventstore/boltdb getIndexKeysForEvent). It indexes neither longer values
// nor empty ones, so a tag filter on such a value matches nothing.
const TagIndexMaxValue = 100

// unbounded stands in for "no cap" on scans.
const unbounded = math.MaxInt32

func tagIndexable(value string) bool {
	return value != "" && len(value) <= TagIndexMaxValue
}

// Coordinate is a replaceable or addressable event's address. D is always
// empty for plain replaceable kinds, which ignore it.
type Coordinate struct {
	Kind   nostr.Kind
	Author nostr.PubKey
	D      string
}

// NewCoordinate returns the coordinate (kind, author, d), dropping d for
// kinds that are not addressable.
func NewCoordinate(kind nostr.Kind, author nostr.PubKey, d string) Coordinate {
	if !kind.IsAddressable() {
		d = ""
	}
	return Coordinate{Kind: kind, Author: author, D: d}
}

// CoordinateOf returns the coordinate of a replaceable or addressable event.
func CoordinateOf(event nostr.Event) Coordinate {
	return NewCoordinate(event.Kind, event.PubKey, event.Tags.GetD())
}

// String renders the coordinate in the NIP-01/NIP-09 address format.
func (c Coordinate) String() string { return Address(c.Kind, c.Author, c.D) }

// dIndexed reports whether the eventstore's #d index can find this
// coordinate's versions.
func (c Coordinate) dIndexed() bool { return !c.Kind.IsAddressable() || tagIndexable(c.D) }

// Address renders "<kind>:<pubkey>:<d>".
func Address(kind nostr.Kind, author nostr.PubKey, d string) string {
	return strconv.Itoa(int(kind)) + ":" + author.Hex() + ":" + d
}

// ParseAddress reads an address written by Address. It does not check that
// the kind is replaceable or addressable.
func ParseAddress(address string) (nostr.Kind, nostr.PubKey, string, bool) {
	parts := strings.SplitN(address, ":", 3)
	if len(parts) != 3 {
		return 0, nostr.ZeroPK, "", false
	}
	kind, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return 0, nostr.ZeroPK, "", false
	}
	author, err := nostr.PubKeyFromHex(parts[1])
	if err != nil {
		return 0, nostr.ZeroPK, "", false
	}
	return nostr.Kind(kind), author, parts[2], true
}

// DeletedCoordinates are the coordinates a kind-5 request deletes: its `a`
// references to the requester's own replaceable or addressable events.
func DeletedCoordinates(request nostr.Event) []Coordinate {
	var coordinates []Coordinate
	for _, tag := range request.Tags {
		if len(tag) < 2 || tag[0] != "a" {
			continue
		}
		kind, author, d, ok := ParseAddress(tag[1])
		if !ok || author != request.PubKey || (!kind.IsReplaceable() && !kind.IsAddressable()) {
			continue
		}
		c := NewCoordinate(kind, author, d)
		if !slices.Contains(coordinates, c) {
			coordinates = append(coordinates, c)
		}
	}
	return coordinates
}

// Scan reads up to limit stored events matching filter, newest first. It must
// not hold a bbolt read transaction while it yields, so that a consumer may
// write to the store between events.
type Scan func(filter nostr.Filter, limit int) iter.Seq[nostr.Event]

// Versions yields the stored versions of c created at or before until (0: no
// bound), newest first, at most limit. A d the eventstore does not index
// (empty or longer than TagIndexMaxValue) is matched here, over the author's
// events of that kind: latest-wins keeps that to about one event per
// coordinate.
func Versions(scan Scan, c Coordinate, until nostr.Timestamp, limit int) iter.Seq[nostr.Event] {
	filter := nostr.Filter{Kinds: []nostr.Kind{c.Kind}, Authors: []nostr.PubKey{c.Author}, Until: until}
	if c.dIndexed() {
		if c.Kind.IsAddressable() {
			filter.Tags = nostr.TagMap{"d": []string{c.D}}
		}
		return scan(filter, limit)
	}
	return func(yield func(nostr.Event) bool) {
		emitted := 0
		for event := range scan(filter, unbounded) {
			if event.Tags.GetD() != c.D {
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

// LatestVersion returns the newest stored version of c.
func LatestVersion(scan Scan, c Coordinate) (nostr.Event, bool) {
	for event := range Versions(scan, c, 0, 1) {
		return event, true
	}
	return nostr.Event{}, false
}

// Replace stores a replaceable or addressable event if it is newer than every
// version held for its coordinate (NIP-01: higher created_at, then lower id)
// and removes the versions it supersedes. It reports false, storing nothing,
// when a version at least as new is held (the event itself included). Callers
// serialise Replace with other writes to the coordinate.
func Replace(backend *boltdb.BoltBackend, scan Scan, event nostr.Event) (bool, error) {
	c := CoordinateOf(event)
	// ReplaceEvent finds the versions it supersedes through the #d index, so
	// for a d that index skips it supersedes nothing: read every version here
	// and delete them after the write.
	limit := 1
	if !c.dIndexed() {
		limit = unbounded
	}
	current := slices.Collect(Versions(scan, c, 0, limit))
	if len(current) > 0 && !nostr.IsOlder(current[0], event) {
		return false, nil
	}
	if _, err := backend.ReplaceEvent(event); err != nil {
		return false, err
	}
	if !c.dIndexed() {
		for _, older := range current {
			if err := backend.DeleteEvent(older.ID); err != nil {
				return false, fmt.Errorf("delete replaced version %s: %w", older.ID.Hex(), err)
			}
		}
	}
	return true, nil
}

// DeletionIndex indexes, in one bucket of the eventstore's bbolt file, the
// coordinates that stored kind-5 requests delete. A key is sha256(coordinate)
// ‖ request created_at (8 bytes, big endian) ‖ request id, with an empty
// value, so Deleted costs one cursor seek however long the coordinate is and
// however many requests the author has stored. The owner keeps it equal to the
// stored requests' references: it indexes a request before saving it and
// unindexes one when removing it.
type DeletionIndex struct {
	db     *bbolt.DB
	bucket []byte
}

// NewDeletionIndex returns the index kept in bucket, which the caller creates
// when it opens db.
func NewDeletionIndex(db *bbolt.DB, bucket []byte) DeletionIndex {
	return DeletionIndex{db: db, bucket: bucket}
}

func deletionKeyPrefix(c Coordinate) []byte {
	sum := sha256.Sum256([]byte(c.String()))
	return sum[:]
}

func deletionKey(c Coordinate, request nostr.Event) []byte {
	key := make([]byte, 0, sha256.Size+8+len(request.ID))
	key = append(key, deletionKeyPrefix(c)...)
	key = binary.BigEndian.AppendUint64(key, uint64(request.CreatedAt))
	return append(key, request.ID[:]...)
}

// Index records the coordinates request deletes.
func (ix DeletionIndex) Index(request nostr.Event) error {
	return ix.update(request, func(bucket *bbolt.Bucket, key []byte) error { return bucket.Put(key, nil) })
}

// Unindex removes request's entries, when the request itself goes.
func (ix DeletionIndex) Unindex(request nostr.Event) error {
	return ix.update(request, func(bucket *bbolt.Bucket, key []byte) error { return bucket.Delete(key) })
}

func (ix DeletionIndex) update(request nostr.Event, apply func(*bbolt.Bucket, []byte) error) error {
	coordinates := DeletedCoordinates(request)
	if len(coordinates) == 0 {
		return nil
	}
	return ix.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(ix.bucket)
		for _, c := range coordinates {
			if err := apply(bucket, deletionKey(c, request)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Deleted reports whether a stored request of the coordinate's author deletes
// it at or after since.
func (ix DeletionIndex) Deleted(c Coordinate, since nostr.Timestamp) (bool, error) {
	prefix := deletionKeyPrefix(c)
	seek := binary.BigEndian.AppendUint64(bytes.Clone(prefix), uint64(since))
	var deleted bool
	err := ix.db.View(func(tx *bbolt.Tx) error {
		key, _ := tx.Bucket(ix.bucket).Cursor().Seek(seek)
		deleted = key != nil && bytes.HasPrefix(key, prefix)
		return nil
	})
	return deleted, err
}

// Backfill indexes requests, the stored kind-5 events of a store written
// before its index existed, in transactions of at most batch entries. finish,
// if set, runs in the last transaction, so a marker it writes commits with the
// final entries. Indexing a request twice is harmless, so an interrupted
// backfill can simply run again. requests must not hold a bbolt read
// transaction while it yields (see Scan). A cancelled ctx is returned as is.
func (ix DeletionIndex) Backfill(ctx context.Context, requests iter.Seq[nostr.Event], batch int, finish func(*bbolt.Tx) error) error {
	var keys [][]byte
	flush := func(done bool) error {
		err := ix.db.Update(func(tx *bbolt.Tx) error {
			bucket := tx.Bucket(ix.bucket)
			for _, key := range keys {
				if err := bucket.Put(key, nil); err != nil {
					return err
				}
			}
			if done && finish != nil {
				return finish(tx)
			}
			return nil
		})
		keys = keys[:0]
		return err
	}
	for request := range requests {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, c := range DeletedCoordinates(request) {
			keys = append(keys, deletionKey(c, request))
		}
		if len(keys) >= batch {
			if err := flush(false); err != nil {
				return err
			}
		}
	}
	return flush(true)
}

// Marker records, under Key in Bucket, that a one-time step reached Version.
type Marker struct {
	Bucket  []byte
	Key     []byte
	Version string
}

// Done reports whether the marker holds Version.
func (m Marker) Done(db *bbolt.DB) (bool, error) {
	var done bool
	err := db.View(func(tx *bbolt.Tx) error {
		done = string(tx.Bucket(m.Bucket).Get(m.Key)) == m.Version
		return nil
	})
	return done, err
}

// Set writes the marker in tx.
func (m Marker) Set(tx *bbolt.Tx) error {
	return tx.Bucket(m.Bucket).Put(m.Key, []byte(m.Version))
}
