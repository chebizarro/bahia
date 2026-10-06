// Package localstore is the daemon's per-process Nostr event store: a
// rebuildable cache of the events its inbound subscriptions received, plus the
// per-(relay, filter) resume cursors those subscriptions keep (bahia-irsry.10.1,
// audit C-2, C-14, B-12, B-15).
//
// It is a cache, not a source of truth. Relays are canonical: deleting the file
// is always safe, and the daemon rebuilds it by syncing from its relays. A file
// that cannot be opened because it is corrupt is moved aside and recreated for
// the same reason. The one thing relays cannot rebuild is the daemon's own
// output whose delivery the outbox abandoned (see Undelivered): that event and
// its marker are restored from the outbox's retained failed entries when the
// publisher starts, so they outlive a deleted store file as long as the outbox
// retains the entry.
//
// Events live in a fiatjaf.com/nostr/eventstore bbolt backend (the same pure-Go
// store the relay sidecar uses). The store keeps what a NIP-01/NIP-09 relay
// would serve: replaceable and addressable events collapse to their latest
// version per coordinate, and a kind-5 deletion request removes the
// requester's events it names and keeps them from coming back. Coordinates
// the eventstore's tag index cannot hold (an empty d, a d or `a` coordinate
// over 100 bytes) are resolved through package boltcoord, as in the relay
// sidecar (bahia-irsry.51), and so are filters on such tag values, through
// boltcoord's tag index (bahia-irsry.52). Cursors live in a separate bucket of
// the same file, so the events and the cursors that describe them are deleted
// together.
package localstore

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/eventstore/boltdb"
	"github.com/openagentsinc/bahia/internal/boltcoord"
	"go.etcd.io/bbolt"
)

// Buckets Bahia keeps next to the eventstore's own in the same bbolt file.
var (
	// cursorBucket holds one record per (relay, filter hash).
	cursorBucket = []byte("bahiaInboundCursors")
	// deletionBucket indexes the coordinates that stored kind-5 requests
	// delete (boltcoord.DeletionIndex). An entry is written before its
	// request is stored and removed with it.
	deletionBucket = []byte("bahiaLocalDeletions")
	// tagBucket indexes the tag values the eventstore does not (empty, or
	// over 100 bytes; see boltcoord.Store), so QueryEvents answers filters on
	// them. Every write and delete goes through Store.coords.
	tagBucket  = []byte("bahiaLocalTags")
	metaBucket = []byte("bahiaLocalMeta")
)

// coordinateRepairMarker records that the store has been brought up to what
// SaveEvent maintains (see repairCoordinates): stores written before
// deletionBucket existed applied no deletion requests.
var coordinateRepairMarker = boltcoord.Marker{Bucket: metaBucket, Key: []byte("coordinateRepairVersion"), Version: "1"}

// tagIndexMarker records that every event stored before tagBucket existed has
// been indexed (see Open).
var tagIndexMarker = boltcoord.Marker{Bucket: metaBucket, Key: []byte("tagIndexVersion"), Version: "1"}

// queryPageSize bounds one bbolt query. The backend preallocates a buffer
// proportional to the limit it is given, so unbounded reads are paged by
// created_at instead of asking for math.MaxInt events at once. Tests shrink it.
var queryPageSize = 1000

// openTimeout bounds the wait for bbolt's file lock. Handles in this process
// share one database (see Open), so the lock is only ever contended by another
// process using the same path, which is a configuration error.
const openTimeout = 2 * time.Second

// Store is one handle to a local event store. Handles opened on the same path
// in one process share the underlying database; the database closes with the
// last handle.
type Store struct {
	shared    *sharedStore
	closeOnce sync.Once
	closeErr  error
}

type sharedStore struct {
	path    string
	backend *boltdb.BoltBackend
	refs    int
	// saveMu serialises SaveEvent: its "already held?" check and the write
	// must not interleave with another save of the same id or coordinate.
	saveMu sync.Mutex
}

var openStores = struct {
	sync.Mutex
	byPath map[string]*sharedStore
}{byPath: make(map[string]*sharedStore)}

// Open opens (or shares) the store at path, creating its directory. A second
// Open of the same path in this process shares the first handle's database:
// the daemon builds a replacement App on SIGHUP before stopping the running
// one, and bbolt's exclusive file lock would otherwise make that wait fail.
//
// If the file exists but is not a readable store, it is renamed to
// "<path>.corrupt-<unix>" and a fresh store is created: the contents are a
// cache that the relays rebuild.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("local event store path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve local event store path: %w", err)
	}
	openStores.Lock()
	defer openStores.Unlock()
	if shared := openStores.byPath[abs]; shared != nil {
		shared.refs++
		return &Store{shared: shared}, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, fmt.Errorf("create local event store directory: %w", err)
	}
	backend, err := openBackend(abs)
	if err != nil && isCorruptStoreError(err) {
		aside := fmt.Sprintf("%s.corrupt-%d", abs, time.Now().Unix())
		if renameErr := os.Rename(abs, aside); renameErr != nil {
			return nil, fmt.Errorf("open local event store %s: %w (moving it aside failed: %v)", abs, err, renameErr)
		}
		backend, err = openBackend(abs)
	}
	if err != nil {
		return nil, fmt.Errorf("open local event store %s (is another process using it?): %w", abs, err)
	}
	shared := &sharedStore{path: abs, backend: backend, refs: 1}
	store := &Store{shared: shared}
	if err := store.coords().BuildTagIndex(context.Background(), store.pagedQuery, tagIndexMarker, queryPageSize); err != nil {
		_ = backend.DB.Close()
		return nil, fmt.Errorf("open local event store %s: %w", abs, err)
	}
	if err := store.repairCoordinates(); err != nil {
		_ = backend.DB.Close()
		return nil, fmt.Errorf("open local event store %s: %w", abs, err)
	}
	openStores.byPath[abs] = shared
	return store, nil
}

func openBackend(path string) (*boltdb.BoltBackend, error) {
	backend := &boltdb.BoltBackend{Path: path}
	if err := backend.Init(); err != nil {
		if backend.DB != nil {
			_ = backend.DB.Close()
		}
		return nil, err
	}
	if err := backend.DB.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{cursorBucket, deletionBucket, tagBucket, metaBucket, undeliveredBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = backend.DB.Close()
		return nil, err
	}
	return backend, nil
}

// isCorruptStoreError reports whether bbolt refused the file because of its
// contents rather than because another process holds it.
func isCorruptStoreError(err error) bool {
	return errors.Is(err, bbolt.ErrInvalid) ||
		errors.Is(err, bbolt.ErrVersionMismatch) ||
		errors.Is(err, bbolt.ErrChecksum) ||
		errors.Is(err, bbolt.ErrInvalidMapping)
}

// Close releases this handle. Closing a handle twice is a no-op.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		openStores.Lock()
		defer openStores.Unlock()
		s.shared.refs--
		if s.shared.refs > 0 {
			return
		}
		delete(openStores.byPath, s.shared.path)
		s.closeErr = s.shared.backend.DB.Close()
	})
	return s.closeErr
}

func (s *Store) backend() *boltdb.BoltBackend { return s.shared.backend }

// SaveEvent stores ev and reports whether it is new to this store: false for
// an id the store already holds, for a replaceable or addressable event that
// is not newer than the version held for its coordinate (NIP-01: higher
// created_at, then lower id), and for an event a stored kind-5 request of its
// author deletes (NIP-09). Saving a kind-5 request removes the requester's
// events it names: `e` references, and every version of an `a` coordinate up
// to the request's created_at. Ephemeral events are never stored and always
// count as new. The caller is expected to have verified the event.
func (s *Store) SaveEvent(ev nostr.Event) (bool, error) {
	if ev.Kind.IsEphemeral() {
		return true, nil
	}
	s.shared.saveMu.Lock()
	defer s.shared.saveMu.Unlock()
	if s.hasLocked(ev.ID) {
		return false, nil
	}
	deleted, err := s.deletedLocked(ev)
	if err != nil {
		return false, fmt.Errorf("check local event %s against deletion requests: %w", ev.ID.Hex(), err)
	}
	if deleted {
		return false, nil
	}
	switch {
	case ev.Kind == nostr.KindDeletion:
		if err := s.saveDeletionLocked(ev); err != nil {
			return false, fmt.Errorf("save local deletion request %s: %w", ev.ID.Hex(), err)
		}
		return true, nil
	case ev.Kind.IsReplaceable() || ev.Kind.IsAddressable():
		stored, err := s.coords().Replace(s.scan, ev)
		if err != nil {
			return false, fmt.Errorf("replace local event %s: %w", ev.ID.Hex(), err)
		}
		if stored {
			// A newer version supersedes an abandoned one: its own delivery
			// now decides whether the coordinate is on the relays.
			if _, err := s.clearSupersededUndelivered(ev); err != nil {
				return false, err
			}
		}
		return stored, nil
	}
	if err := s.coords().Save(ev); err != nil {
		if errors.Is(err, eventstore.ErrDupEvent) {
			return false, nil
		}
		return false, fmt.Errorf("save local event %s: %w", ev.ID.Hex(), err)
	}
	return true, nil
}

// deletedLocked reports whether a stored kind-5 request of ev's author deletes
// it: by id (an `e` reference; ids are 64 hex characters, which the
// eventstore's tag index holds) or, for replaceable and addressable events, by
// coordinate at or after its created_at (through the deletion index, since a
// coordinate can be longer than the tag index holds). Deleting a deletion
// request has no effect (NIP-09).
func (s *Store) deletedLocked(ev nostr.Event) (bool, error) {
	if ev.Kind == nostr.KindDeletion {
		return false, nil
	}
	byID := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.KindDeletion},
		Authors: []nostr.PubKey{ev.PubKey},
		Tags:    nostr.TagMap{"e": []string{ev.ID.Hex()}},
		Limit:   1,
	}
	for range s.QueryEvents(byID) {
		return true, nil
	}
	if !ev.Kind.IsReplaceable() && !ev.Kind.IsAddressable() {
		return false, nil
	}
	return s.deletions().Deleted(boltcoord.CoordinateOf(ev), ev.CreatedAt)
}

// saveDeletionLocked indexes, applies and stores a kind-5 request, in that
// order: a failure leaves the request unstored and unindexed, so a redelivery
// runs it again, and applying it twice is harmless.
func (s *Store) saveDeletionLocked(request nostr.Event) error {
	if err := s.deletions().Index(request); err != nil {
		return err
	}
	_, err := s.coords().ApplyDeletion(context.Background(), s.scan, request)
	if err == nil {
		err = s.coords().Save(request)
	}
	if err != nil {
		return errors.Join(err, s.deletions().Unindex(request))
	}
	return nil
}

// DeleteEvent removes one event. Deleting an absent id is not an error.
// Removing a kind-5 request also lifts its deletions for events stored later;
// the events it already removed stay removed.
func (s *Store) DeleteEvent(id nostr.ID) error {
	s.shared.saveMu.Lock()
	defer s.shared.saveMu.Unlock()
	held := slices.Collect(s.backend().QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1))
	if len(held) == 0 || held[0].ID != id {
		return nil
	}
	if held[0].Kind == nostr.KindDeletion {
		if err := s.deletions().Unindex(held[0]); err != nil {
			return fmt.Errorf("unindex local deletion request %s: %w", id.Hex(), err)
		}
	}
	return s.coords().Delete(held[0])
}

// hasLocked reports whether the store holds the event with this id. Callers
// that act on the answer hold saveMu.
func (s *Store) hasLocked(id nostr.ID) bool {
	for range s.backend().QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1) {
		return true
	}
	return false
}

// coords is the write path for every event the store saves or deletes: it
// keeps the tag index (tagBucket) in step with the eventstore.
func (s *Store) coords() boltcoord.Store {
	return boltcoord.NewStore(s.backend(), tagBucket)
}

func (s *Store) deletions() boltcoord.DeletionIndex {
	return boltcoord.NewDeletionIndex(s.backend().DB, deletionBucket)
}

// scan adapts QueryEvents to boltcoord.Scan.
func (s *Store) scan(filter nostr.Filter, limit int) iter.Seq[nostr.Event] {
	filter.Limit = limit
	return s.QueryEvents(filter)
}

// repairCoordinates brings a store written before the deletion index existed
// up to what SaveEvent now maintains, once (coordinateRepairMarker): it
// indexes the stored kind-5 requests, then applies them (such a store applied
// none) and collapses each coordinate whose d the eventstore does not index to
// its latest version (ReplaceEvent kept every version of those, and collapsed
// the rest itself; see boltcoord.Store.Repair). Each step is safe to repeat,
// so a repair interrupted before its marker is written runs again in full on
// the next open.
func (s *Store) repairCoordinates() error {
	done, err := coordinateRepairMarker.Done(s.backend().DB)
	if err != nil {
		return fmt.Errorf("read coordinate repair marker: %w", err)
	}
	if done {
		return nil
	}
	requests := nostr.Filter{Kinds: []nostr.Kind{nostr.KindDeletion}}
	if err := s.deletions().Backfill(context.Background(), s.QueryEvents(requests), queryPageSize, nil); err != nil {
		return fmt.Errorf("index stored deletion requests: %w", err)
	}
	return s.coords().Repair(context.Background(), s.scan, coordinateRepairMarker)
}

// QueryEvents yields the stored events matching filter, newest first, up to
// filter.Limit (all of them when the filter has no limit). Reads are paged, and
// each page is copied out of its bbolt read transaction before it is yielded,
// so a consumer that blocks or writes to the store cannot stall bbolt writers.
// A filter on a tag value the eventstore does not index (empty, or over 100
// bytes) is read through the tag index (boltcoord.Store.Query).
func (s *Store) QueryEvents(filter nostr.Filter) iter.Seq[nostr.Event] {
	if len(filter.IDs) > 0 {
		return func(yield func(nostr.Event) bool) {
			for _, ev := range slices.Collect(s.backend().QueryEvents(filter, len(filter.IDs))) {
				if !yield(ev) {
					return
				}
			}
		}
	}
	if filter.LimitZero {
		return func(func(nostr.Event) bool) {}
	}
	if boltcoord.NeedsTagIndex(filter) {
		return s.coords().Query(s.pagedQuery, filter, filter.Limit)
	}
	return s.pagedQuery(filter, filter.Limit)
}

// pagedQuery reads up to limit (<= 0: all) events matching filter, newest
// first, from the eventstore's own indexes, a page at a time.
func (s *Store) pagedQuery(filter nostr.Filter, limit int) iter.Seq[nostr.Event] {
	return func(yield func(nostr.Event) bool) {
		remaining := math.MaxInt
		if limit > 0 {
			remaining = limit
		}
		page := filter
		page.Limit = 0
		pageSize := queryPageSize
		seen := make(map[nostr.ID]struct{})
		for {
			events := slices.Collect(s.backend().QueryEvents(page, pageSize))
			fresh := 0
			for _, ev := range events {
				if _, dup := seen[ev.ID]; dup {
					continue
				}
				seen[ev.ID] = struct{}{}
				fresh++
				if !yield(ev) {
					return
				}
				if remaining--; remaining == 0 {
					return
				}
			}
			if len(events) < pageSize {
				return
			}
			if fresh == 0 {
				// A full page of events already yielded, all at the resume
				// timestamp: widen the page until it reaches past them.
				pageSize *= 2
				continue
			}
			// until is inclusive: the next page restarts at the oldest
			// timestamp returned and skips the ids already yielded there.
			page.Until = events[len(events)-1].CreatedAt
		}
	}
}

// PruneRegularEvents deletes stored regular (non-replaceable, non-addressable)
// events created before cutoff and returns how many it removed. It keeps the
// store bounded (B-12): the replay window of every cursor is far shorter than
// the retention the caller passes, so a pruned event cannot be redelivered as
// new by an ordinary resume. NIP-09 deletion requests are kept: they are
// tombstones for state that never expires, reconciled in full like it.
func (s *Store) PruneRegularEvents(cutoff time.Time) (int, error) {
	until := nostr.Timestamp(cutoff.Unix())
	if until <= 0 {
		return 0, nil
	}
	var expired []nostr.ID
	for ev := range s.QueryEvents(nostr.Filter{Until: until - 1}) {
		if ev.Kind.IsRegular() && ev.Kind != nostr.KindDeletion {
			expired = append(expired, ev.ID)
		}
	}
	for i, id := range expired {
		if err := s.DeleteEvent(id); err != nil {
			return i, fmt.Errorf("prune local event %s: %w", id.Hex(), err)
		}
	}
	return len(expired), nil
}

// Cursor returns the committed resume cursor for (relayURL, filterHash), or 0
// when there is none.
func (s *Store) Cursor(relayURL, filterHash string) (nostr.Timestamp, error) {
	var cursor nostr.Timestamp
	err := s.backend().DB.View(func(tx *bbolt.Tx) error {
		value := tx.Bucket(cursorBucket).Get(cursorKey(relayURL, filterHash))
		if len(value) == 8 {
			cursor = nostr.Timestamp(binary.BigEndian.Uint64(value))
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("read inbound cursor: %w", err)
	}
	return cursor, nil
}

// AdvanceCursor raises the cursor for (relayURL, filterHash) to to. A cursor
// never moves backwards: a smaller value is ignored.
func (s *Store) AdvanceCursor(relayURL, filterHash string, to nostr.Timestamp) error {
	if to <= 0 {
		return nil
	}
	err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(cursorBucket)
		key := cursorKey(relayURL, filterHash)
		if value := bucket.Get(key); len(value) == 8 && nostr.Timestamp(binary.BigEndian.Uint64(value)) >= to {
			return nil
		}
		value := make([]byte, 8)
		binary.BigEndian.PutUint64(value, uint64(to))
		return bucket.Put(key, value)
	})
	if err != nil {
		return fmt.Errorf("write inbound cursor: %w", err)
	}
	return nil
}

func cursorKey(relayURL, filterHash string) []byte {
	return []byte(relayURL + "\x00" + filterHash)
}
