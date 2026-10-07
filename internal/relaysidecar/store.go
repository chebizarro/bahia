package relaysidecar

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
	"fiatjaf.com/nostr/eventstore/codec/betterbinary"
	"fiatjaf.com/nostr/nip40"
	"github.com/openagentsinc/bahia/internal/boltcoord"
	"go.etcd.io/bbolt"
	"go.uber.org/zap"
)

// eventStoreFile is the bbolt database under nostr.sidecar.data_dir:
// fiatjaf's eventstore indexes kinds, authors and tags (#e/#p/#d/#a…), so
// ContextVM, addressable and FIPS lookups are index reads, not table scans.
const eventStoreFile = "events.bolt"

// Buckets Bahia keeps next to the eventstore's own in the same bbolt file.
var (
	sidecarMetaBucket   = []byte("bahiaSidecarMeta")
	sidecarExpiryBucket = []byte("bahiaSidecarExpiry")
)

const (
	// unboundedQueryLimit stands in for "no cap" on queries.
	unboundedQueryLimit = math.MaxInt32
	// queryPageSize bounds one bbolt query. bbolt preallocates a result buffer
	// proportional to the limit it is given, so larger reads (negentropy sets,
	// uncapped internal reads) are paged by scan.
	queryPageSize = 1000
	// sweepBatchSize bounds how many events one sweep page reads and deletes.
	sweepBatchSize = 1000
)

var errEventDeleted = errors.New("blocked: this event was deleted by its author (NIP-09)")

// eventStore is the durable source of relay history. Nostr subscribers may
// disconnect and replay at any time, so accepted events must survive relay
// process and container restarts.
//
// Every handle to one data_dir shares a single bbolt database. bbolt holds an
// exclusive file lock, and cmd/relay prepares a SIGHUP replacement runtime
// before it stops the active one, so a second open of the same file must reuse
// the first instead of waiting for a lock its own process holds.
type eventStore struct {
	shared    *sharedEventStore
	closeOnce sync.Once
	closeErr  error
}

type sharedEventStore struct {
	path    string
	backend *boltdb.BoltBackend
	refs    int
	// replaceMu serialises Replace so that its read of the current version
	// and the write that supersedes it are not interleaved with another
	// replace of the same coordinate.
	replaceMu sync.Mutex
}

var openEventStores = struct {
	sync.Mutex
	byPath map[string]*sharedEventStore
}{byPath: make(map[string]*sharedEventStore)}

// openEventStore opens (or shares) the bbolt event store under dataDir. On the
// first open of a data_dir that still holds the pre-eventstore events.sqlite,
// it imports that history once (see migrateLegacySQLite).
func openEventStore(ctx context.Context, dataDir string, logger *zap.Logger) (*eventStore, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("relay sidecar data_dir is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create relay sidecar data directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dataDir, eventStoreFile))
	if err != nil {
		return nil, fmt.Errorf("resolve relay sidecar event store path: %w", err)
	}

	openEventStores.Lock()
	defer openEventStores.Unlock()
	if shared := openEventStores.byPath[path]; shared != nil {
		shared.refs++
		return &eventStore{shared: shared}, nil
	}

	backend := &boltdb.BoltBackend{Path: path}
	if err := backend.Init(); err != nil {
		if backend.DB != nil {
			_ = backend.DB.Close()
		}
		return nil, fmt.Errorf("open relay sidecar event store %s (is another relay process using this data_dir?): %w", path, err)
	}
	if err := backend.DB.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{sidecarMetaBucket, sidecarExpiryBucket, sidecarDeletionBucket, sidecarTagBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = backend.DB.Close()
		return nil, fmt.Errorf("initialize relay sidecar event store buckets: %w", err)
	}
	shared := &sharedEventStore{path: path, backend: backend, refs: 1}
	store := &eventStore{shared: shared}
	if _, err := migrateLegacySQLite(ctx, store, dataDir, logger); err != nil {
		_ = backend.DB.Close()
		return nil, err
	}
	for _, step := range []func(context.Context) error{store.buildDeletionIndex, store.buildTagIndex, store.repairCoordinates} {
		if err := step(ctx); err != nil {
			_ = backend.DB.Close()
			return nil, err
		}
	}
	openEventStores.byPath[path] = shared
	return store, nil
}

func (s *eventStore) backend() *boltdb.BoltBackend { return s.shared.backend }

// coords is the write path for every event the store saves or deletes: it
// keeps the tag index of values the eventstore does not index (see
// sidecarTagBucket) in step with the eventstore.
func (s *eventStore) coords() boltcoord.Store {
	return boltcoord.NewStore(s.backend(), sidecarTagBucket)
}

// Close releases this handle. The database closes with its last handle.
// Closing a handle twice is a no-op.
func (s *eventStore) Close() error {
	s.closeOnce.Do(func() {
		openEventStores.Lock()
		defer openEventStores.Unlock()
		s.shared.refs--
		if s.shared.refs > 0 {
			return
		}
		delete(openEventStores.byPath, s.shared.path)
		s.closeErr = s.shared.backend.DB.Close()
	})
	return s.closeErr
}

// ping reports whether the database is still open and readable.
func (s *eventStore) ping() error {
	return s.backend().DB.View(func(*bbolt.Tx) error { return nil })
}

// Save stores a regular event. Saving a kind-5 deletion request also applies
// it (NIP-09).
func (s *eventStore) Save(ctx context.Context, event nostr.Event) error {
	if err := storableEvent(event); err != nil {
		return err
	}
	if err := s.checkNotDeleted(event); err != nil {
		return err
	}
	if event.Kind == nostr.KindDeletion {
		// Index before the write: a concurrent write of a target either sees
		// the entry or lands before applyDeletion reads the coordinate.
		if err := s.indexDeletion(event); err != nil {
			return err
		}
	}
	if err := s.coords().Save(event); err != nil {
		if errors.Is(err, eventstore.ErrDupEvent) {
			return eventstore.ErrDupEvent // khatru compares the sentinel with ==
		}
		err = fmt.Errorf("store relay event: %w", err)
		if event.Kind == nostr.KindDeletion && !s.stored(event.ID) {
			err = errors.Join(err, s.unindexDeletion(event))
		}
		return err
	}
	return s.afterWrite(ctx, event)
}

// Replace stores a replaceable or addressable event if it is newer than the
// version held for its coordinate (NIP-01: higher created_at, then lower id).
// It returns eventstore.ErrDupEvent when the event is not stored, so khatru
// neither acknowledges it as new nor dispatches it.
func (s *eventStore) Replace(ctx context.Context, event nostr.Event) error {
	if !event.Kind.IsReplaceable() && !event.Kind.IsAddressable() {
		return s.Save(ctx, event)
	}
	if err := storableEvent(event); err != nil {
		return err
	}
	if err := s.checkNotDeleted(event); err != nil {
		return err
	}
	if err := s.replace(event); err != nil {
		return err
	}
	return s.afterWrite(ctx, event)
}

func (s *eventStore) replace(event nostr.Event) error {
	s.shared.replaceMu.Lock()
	defer s.shared.replaceMu.Unlock()
	stored, err := s.coords().Replace(s.scan, event)
	if err != nil {
		return fmt.Errorf("replace relay event: %w", err)
	}
	if !stored {
		return eventstore.ErrDupEvent
	}
	return nil
}

// afterWrite indexes the event's NIP-40 expiration and closes the race with a
// concurrent deletion: a kind 5 saved after checkNotDeleted but before this
// write was applied before the write landed, so recheck and undo.
func (s *eventStore) afterWrite(ctx context.Context, event nostr.Event) error {
	if err := s.indexExpiration(event); err != nil {
		return err
	}
	if event.Kind == nostr.KindDeletion {
		if _, err := s.applyDeletion(ctx, event); err != nil {
			return fmt.Errorf("apply deletion request: %w", err)
		}
		return nil
	}
	if err := s.checkNotDeleted(event); err != nil {
		if deleteErr := s.coords().Delete(event); deleteErr != nil {
			return errors.Join(err, deleteErr)
		}
		return err
	}
	return nil
}

// checkNotDeleted rejects an event its author already asked to delete: by id
// (an `e` reference), or, for replaceable and addressable events, by
// coordinate (an `a` reference) at or after the event's created_at. Stored
// kind-5 requests are the tombstones, so a deleted event is never re-accepted.
// Event ids are always 64 hex characters, which the eventstore's tag index
// holds; coordinates can be longer than it indexes, so they are looked up in
// the sidecar's own deletion index.
func (s *eventStore) checkNotDeleted(event nostr.Event) error {
	if event.Kind == nostr.KindDeletion {
		return nil // deleting a deletion request has no effect (NIP-09)
	}
	byID := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.KindDeletion},
		Authors: []nostr.PubKey{event.PubKey},
		Tags:    nostr.TagMap{"e": []string{event.ID.Hex()}},
	}
	if _, found := s.latest(byID); found {
		return errEventDeleted
	}
	if event.Kind.IsReplaceable() || event.Kind.IsAddressable() {
		deleted, err := s.coordinateDeleted(boltcoord.CoordinateOf(event), event.CreatedAt)
		if err != nil {
			return err
		}
		if deleted {
			return errEventDeleted
		}
	}
	return nil
}

// stored reports whether the event with id is in the store.
func (s *eventStore) stored(id nostr.ID) bool {
	event, found := s.latest(nostr.Filter{IDs: []nostr.ID{id}})
	return found && event.ID == id
}

// applyDeletion executes a stored kind-5 request (NIP-09; see
// boltcoord.Store.ApplyDeletion). `e` references are deleted when they have
// the requester as author; `a` references delete every version of the
// requester's coordinate up to the request's created_at. References to other
// authors' events and to deletion requests are ignored. Khatru's own handler
// is not used: it cannot address plain replaceable events (whose coordinate
// has an empty d), and it fails the whole request on the first foreign
// reference.
func (s *eventStore) applyDeletion(ctx context.Context, request nostr.Event) (int, error) {
	return s.coords().ApplyDeletion(ctx, s.scan, request)
}

// Query yields events matching filter, newest first, at most maxLimit (or the
// filter's lower limit); maxLimit <= 0 means uncapped. Events past their NIP-40
// expiration are never yielded, even before the sweep removes them.
func (s *eventStore) Query(ctx context.Context, filter nostr.Filter, maxLimit int) iter.Seq[nostr.Event] {
	if filter.LimitZero {
		return func(func(nostr.Event) bool) {}
	}
	limit := maxLimit
	if limit <= 0 || limit > unboundedQueryLimit {
		limit = unboundedQueryLimit
	}
	if filter.Limit > 0 && filter.Limit < limit {
		limit = filter.Limit
	}
	return func(yield func(nostr.Event) bool) {
		now := nostr.Now()
		emitted := 0
		for event := range s.scan(filter, limit) {
			if ctx.Err() != nil {
				return
			}
			// An ids filter is answered from the raw store by id alone, so the
			// other conditions still have to be checked.
			if filter.IDs != nil && !filter.Matches(event) {
				continue
			}
			if expired(event, now) {
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

// scan reads up to limit events matching filter, newest first. A filter on a
// tag value the eventstore does not index (empty, or longer than
// tagIndexMaxValue, such as an #a relay config coordinate) is read through
// the sidecar's tag index (boltcoord.Store.Query); the rest go to scanIndexed.
func (s *eventStore) scan(filter nostr.Filter, limit int) iter.Seq[nostr.Event] {
	if matchesNothing(filter) {
		return func(func(nostr.Event) bool) {}
	}
	if filter.IDs == nil && boltcoord.NeedsTagIndex(filter) {
		return s.coords().Query(s.scanIndexed, filter, limit)
	}
	return s.scanIndexed(filter, limit)
}

// scanIndexed reads up to limit events matching filter, newest first, from the
// eventstore's own indexes. It reads in pages of at most queryPageSize and copies each page out of its bbolt read
// transaction before yielding, for two reasons: bbolt preallocates a buffer
// proportional to the limit it is given, and a read transaction held while the
// consumer blocks (a slow websocket, or a caller deleting what it reads) can
// stall or deadlock writers that need to remap the file. Each page resumes at
// the oldest created_at of the previous one (until is inclusive) and skips the
// ids already yielded at that timestamp; a page holding nothing new is retried
// larger, so ties wider than a page still make progress.
func (s *eventStore) scanIndexed(filter nostr.Filter, limit int) iter.Seq[nostr.Event] {
	return func(yield func(nostr.Event) bool) {
		if filter.IDs != nil {
			for _, event := range slices.Collect(s.backend().QueryEvents(filter, limit)) {
				if !yield(event) {
					return
				}
			}
			return
		}
		page := filter
		page.Limit = 0
		pageSize := queryPageSize
		emitted := 0
		resuming := false
		var until nostr.Timestamp
		var seen map[nostr.ID]struct{} // ids already yielded at created_at == until
		for {
			if resuming {
				page.Until = until
			}
			pageLimit := min(pageSize, limit-emitted+len(seen))
			events := slices.Collect(s.backend().QueryEvents(page, pageLimit))
			fresh := 0
			oldest, oldestIDs, haveOldest := until, seen, resuming
			for _, event := range events {
				if resuming && event.CreatedAt == until {
					if _, dup := seen[event.ID]; dup {
						continue
					}
				}
				if !haveOldest || event.CreatedAt != oldest {
					oldest, oldestIDs, haveOldest = event.CreatedAt, map[nostr.ID]struct{}{}, true
				}
				oldestIDs[event.ID] = struct{}{}
				fresh++
				if !yield(event) {
					return
				}
				emitted++
				if emitted >= limit {
					return
				}
			}
			switch {
			case len(events) < pageLimit:
				return // exhausted
			case fresh == 0:
				pageSize *= 2 // a page of ties already yielded: read further
			case oldest == 0:
				return // until=0 would mean "no bound"; nothing older exists
			default:
				resuming, until, seen = true, oldest, oldestIDs
			}
		}
	}
}

// matchesNothing reports a filter with an explicitly empty condition, which
// NIP-01 matches against no event (bbolt would treat it as absent).
func matchesNothing(filter nostr.Filter) bool {
	if (filter.IDs != nil && len(filter.IDs) == 0) ||
		(filter.Kinds != nil && len(filter.Kinds) == 0) ||
		(filter.Authors != nil && len(filter.Authors) == 0) {
		return true
	}
	for _, values := range filter.Tags {
		if len(values) == 0 {
			return true
		}
	}
	return false
}

// Count answers NIP-45 COUNT from the indexes. A filter on a tag value the
// eventstore does not index is counted over the events the sidecar's tag
// index yields for it.
func (s *eventStore) Count(ctx context.Context, filter nostr.Filter) (uint32, error) {
	if filter.LimitZero || matchesNothing(filter) {
		return 0, nil
	}
	if filter.IDs != nil || boltcoord.NeedsTagIndex(filter) {
		if err := s.ping(); err != nil {
			return 0, fmt.Errorf("count relay events: %w", err)
		}
		var count uint32
		for range s.Query(ctx, filter, 0) {
			count++
		}
		return count, nil
	}
	count, err := s.backend().CountEvents(filter)
	if err != nil {
		return 0, fmt.Errorf("count relay events: %w", err)
	}
	return count, nil
}

// latest returns the newest event matching filter.
func (s *eventStore) latest(filter nostr.Filter) (nostr.Event, bool) {
	filter.Limit = 1
	for event := range s.scan(filter, 1) {
		return event, true
	}
	return nostr.Event{}, false
}

// latestByReplaceableKey returns the event currently stored under a replaceable
// or addressable key (see replaceableKey). Unlike Query, it reports store
// failures, so callers that must not lose a change can retry.
func (s *eventStore) latestByReplaceableKey(_ context.Context, key string) (nostr.Event, bool, error) {
	kind, author, d, ok := boltcoord.ParseAddress(key)
	if !ok {
		return nostr.Event{}, false, fmt.Errorf("read replaceable relay event %s: malformed key", key)
	}
	if err := s.ping(); err != nil {
		return nostr.Event{}, false, fmt.Errorf("read replaceable relay event %s: %w", key, err)
	}
	event, found := s.latestVersion(newCoordinate(kind, author, d))
	return event, found, nil
}

// indexExpiration records a NIP-40 expiration so the sweep finds expired
// events without scanning the store.
func (s *eventStore) indexExpiration(event nostr.Event) error {
	expiresAt := nip40.GetExpiration(event.Tags)
	if expiresAt <= 0 {
		return nil
	}
	if err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(sidecarExpiryBucket).Put(expiryKey(expiresAt, event.ID), nil)
	}); err != nil {
		return fmt.Errorf("index relay event expiration: %w", err)
	}
	return nil
}

func expiryKey(expiresAt nostr.Timestamp, id nostr.ID) []byte {
	key := make([]byte, 8+len(id))
	binary.BigEndian.PutUint64(key, uint64(expiresAt))
	copy(key[8:], id[:])
	return key
}

func expired(event nostr.Event, now nostr.Timestamp) bool {
	expiresAt := nip40.GetExpiration(event.Tags)
	return expiresAt > 0 && expiresAt <= now
}

// storableEvent enforces the eventstore codec's limits (betterbinary uses
// 16-bit lengths) so an oversize event is refused with a clear OK reason
// instead of failing inside the store or, for the tag section, being encoded
// with a wrapped length. NIP-11 advertises max_content_length accordingly.
func storableEvent(event nostr.Event) error {
	if len(event.Content) > betterbinary.MaxContentSize {
		return fmt.Errorf("invalid: content is %d bytes; this relay stores at most %d", len(event.Content), betterbinary.MaxContentSize)
	}
	if len(event.Tags) > betterbinary.MaxTagCount {
		return fmt.Errorf("invalid: event has %d tags; this relay stores at most %d", len(event.Tags), betterbinary.MaxTagCount)
	}
	section := 4 + 2*len(event.Tags)
	for _, tag := range event.Tags {
		if len(tag) > betterbinary.MaxTagItemCount {
			return fmt.Errorf("invalid: a tag has %d items; this relay stores at most %d", len(tag), betterbinary.MaxTagItemCount)
		}
		section++
		for _, item := range tag {
			section += 2 + len(item)
		}
	}
	if section > math.MaxUint16 {
		return fmt.Errorf("invalid: tags encode to %d bytes; this relay stores at most %d", section, math.MaxUint16)
	}
	return nil
}

// replaceableKey is the latest-wins coordinate of a replaceable or addressable
// event: "<kind>:<pubkey>:<d>", with an empty d for replaceable kinds. It is
// the NIP-01/NIP-09 address format, so boltcoord.ParseAddress reads it back.
func replaceableKey(event nostr.Event) string {
	if !event.Kind.IsReplaceable() && !event.Kind.IsAddressable() {
		return ""
	}
	return boltcoord.Address(event.Kind, event.PubKey, event.Tags.GetD())
}

// sweepMatching deletes, page by page, the events matching filter that
// eligible accepts. filter.Until must be set: pages walk backwards from it.
func (s *eventStore) sweepMatching(ctx context.Context, filter nostr.Filter, eligible func(nostr.Event) bool) (int64, error) {
	var deleted int64
	for filter.Until > 0 {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		var swept []nostr.Event
		scanned := 0
		var oldest nostr.Timestamp
		for _, event := range slices.Collect(s.backend().QueryEvents(filter, sweepBatchSize)) {
			scanned++
			oldest = event.CreatedAt
			if eligible(event) {
				swept = append(swept, event)
			}
		}
		for _, event := range swept {
			if err := s.coords().Delete(event); err != nil {
				return deleted, fmt.Errorf("delete swept relay event: %w", err)
			}
			deleted++
		}
		if scanned < sweepBatchSize {
			return deleted, nil
		}
		// A full page: older matches may remain. Re-read from the oldest
		// timestamp seen (events sharing it may be left), or step past it when
		// the page held nothing eligible, so every round makes progress.
		if len(swept) == 0 {
			oldest--
		}
		filter.Until = oldest
	}
	return deleted, nil
}

// sweepExpired deletes events whose NIP-40 expiration is at or before now.
func (s *eventStore) sweepExpired(ctx context.Context, now nostr.Timestamp) (int64, error) {
	var deleted int64
	for {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		var keys [][]byte
		if err := s.backend().DB.View(func(tx *bbolt.Tx) error {
			cursor := tx.Bucket(sidecarExpiryBucket).Cursor()
			for key, _ := cursor.First(); key != nil && len(keys) < sweepBatchSize; key, _ = cursor.Next() {
				if nostr.Timestamp(binary.BigEndian.Uint64(key[:8])) > now {
					break
				}
				keys = append(keys, append([]byte(nil), key...))
			}
			return nil
		}); err != nil {
			return deleted, fmt.Errorf("read relay event expirations: %w", err)
		}
		if len(keys) == 0 {
			return deleted, nil
		}
		for _, key := range keys {
			id := nostr.ID(key[8:])
			if event, stored := s.latest(nostr.Filter{IDs: []nostr.ID{id}}); stored && event.ID == id {
				// Unindex first: an expired request left stored by a crash
				// here is hidden and swept on the next run anyway.
				if event.Kind == nostr.KindDeletion {
					if err := s.unindexDeletion(event); err != nil {
						return deleted, err
					}
				}
				if err := s.coords().Delete(event); err != nil {
					return deleted, fmt.Errorf("delete expired relay event: %w", err)
				}
				deleted++
			}
		}
		if err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
			bucket := tx.Bucket(sidecarExpiryBucket)
			for _, key := range keys {
				if err := bucket.Delete(key); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return deleted, fmt.Errorf("clear relay event expirations: %w", err)
		}
	}
}

// SweepRetention applies the retention policy at now (see retentionPolicy).
func (s *eventStore) SweepRetention(ctx context.Context, now time.Time, policy retentionPolicy) (sweepResult, error) {
	var result sweepResult
	var err error
	if result.Expired, err = s.sweepExpired(ctx, nostr.Timestamp(now.Unix())); err != nil {
		return result, err
	}
	if kinds := policy.storedRequestKinds(); len(kinds) > 0 {
		filter := nostr.Filter{Kinds: kinds, Until: nostr.Timestamp(now.Add(-policy.request).Unix())}
		if result.Request, err = s.sweepMatching(ctx, filter, func(nostr.Event) bool { return true }); err != nil {
			return result, err
		}
	}
	if policy.regular > 0 {
		filter := nostr.Filter{Until: nostr.Timestamp(now.Add(-policy.regular).Unix())}
		eligible := func(event nostr.Event) bool { return policy.classOf(event.Kind) == retentionRegular }
		if result.Regular, err = s.sweepMatching(ctx, filter, eligible); err != nil {
			return result, err
		}
	}
	return result, nil
}
