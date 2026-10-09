package localstore

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"go.etcd.io/bbolt"
)

// ErrReadOnly is returned when a write operation is attempted on a read-only outbox.
var ErrReadOnly = errors.New("outbox is open read-only")

// The daemon's durable publish outbox: signed
// events waiting for relay acceptance, with each relay's delivery state.
//
// Unlike the event store, the outbox is not a cache. A pending entry is the
// only copy of an event no relay may hold yet, so the outbox lives in its own
// bbolt file: deleting the event store file stays safe, deleting the outbox
// file loses the events it still had to deliver.
//
// An entry is pending until every write relay of its target has accepted it
// or reached a terminal state, then published (the caller's publish quorum
// accepted it) or failed (the quorum became unreachable). Settled entries are
// kept for a while so producers can still read an outcome they missed (see
// Get), then pruned (see Prune).

// Outbox entry states.
const (
	OutboxPending   = "pending"
	OutboxPublished = "published"
	OutboxFailed    = "failed"
)

var (
	outboxEntriesBucket     = []byte("bahiaOutboxEntries")
	outboxPendingBucket     = []byte("bahiaOutboxPending")
	outboxPublishedBucket   = []byte("bahiaOutboxPublished")
	outboxFailedBucket      = []byte("bahiaOutboxFailed")
	outboxCoordinatesBucket = []byte("bahiaOutboxCoordinatesV1")
	outboxCoordinatesReady  = []byte("index-ready")
)

// OutboxEntry is one outbound event and its delivery state.
type OutboxEntry struct {
	Event nostr.Event `json:"event"`
	// Target is the publish target (relay pool) that delivers the entry.
	Target     string    `json:"target"`
	EntityType string    `json:"entity_type,omitempty"`
	EntityID   string    `json:"entity_id,omitempty"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	State      string    `json:"state"`
	// Rounds is the number of delivery rounds counted against the attempt
	// budget.
	Rounds int `json:"rounds"`
	// Delivered is set once the caller's publish quorum accepted the event,
	// which can happen long before the entry settles.
	Delivered bool                     `json:"delivered,omitempty"`
	LastError string                   `json:"last_error,omitempty"`
	SettledAt time.Time                `json:"settled_at,omitzero"`
	Relays    map[string]RelayDelivery `json:"relays,omitempty"`
}

// RelayDelivery is one relay's state for one entry.
type RelayDelivery struct {
	Accepted bool `json:"accepted,omitempty"`
	// Rejected is a permanent OK=false reason; terminal for this relay.
	Rejected string `json:"rejected,omitempty"`
	// LastError is the most recent retryable failure.
	LastError string `json:"last_error,omitempty"`
	// SeenDialFailure is the relay dial failure already counted against the
	// attempt budget.
	SeenDialFailure time.Time `json:"seen_dial_failure,omitzero"`
}

// OutboxCursor is a position in one target's pending entries, which are
// ordered by (EnqueuedAt, ID).
type OutboxCursor struct {
	EnqueuedAt time.Time
	ID         nostr.ID
}

// OutboxRound is the outcome of one delivery round, committed atomically.
type OutboxRound struct {
	Rounds    int
	Relays    map[string]RelayDelivery
	Delivered bool
	// State is OutboxPending while relays remain to be retried, else the
	// terminal state.
	State  string
	Detail string
	At     time.Time
}

// Outbox is one handle to an outbox file. Handles opened on the same path in
// one process share the database, which closes with the last handle (the
// daemon builds its replacement App on SIGHUP before stopping the old one).
type Outbox struct {
	shared    *sharedOutbox
	closeOnce sync.Once
	closeErr  error
}

type sharedOutbox struct {
	path      string
	db        *bbolt.DB
	refs      int
	readOnly  bool
	movedFrom string
}

var openOutboxes = struct {
	sync.Mutex
	byPath map[string]*sharedOutbox
}{byPath: make(map[string]*sharedOutbox)}

// OpenOutbox opens (or shares) the outbox at path, creating its directory. A
// file bbolt cannot read is renamed to "<path>.corrupt-<unix>" and a fresh
// outbox is created; MovedAside reports it so the caller can raise it, since
// the pending events in that file are not delivered.
func OpenOutbox(path string) (*Outbox, error) {
	if path == "" {
		return nil, errors.New("local outbox path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve local outbox path: %w", err)
	}
	openOutboxes.Lock()
	defer openOutboxes.Unlock()
	if shared := openOutboxes.byPath[abs]; shared != nil {
		shared.refs++
		return &Outbox{shared: shared}, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, fmt.Errorf("create local outbox directory: %w", err)
	}
	movedFrom := ""
	db, err := openOutboxDB(abs)
	if err != nil && isCorruptStoreError(err) {
		aside := fmt.Sprintf("%s.corrupt-%d", abs, time.Now().Unix())
		if renameErr := os.Rename(abs, aside); renameErr != nil {
			return nil, fmt.Errorf("open local outbox %s: %w (moving it aside failed: %v)", abs, err, renameErr)
		}
		movedFrom = aside
		db, err = openOutboxDB(abs)
	}
	if err != nil {
		return nil, fmt.Errorf("open local outbox %s (is another process using it?): %w", abs, err)
	}
	shared := &sharedOutbox{path: abs, db: db, refs: 1, movedFrom: movedFrom}
	openOutboxes.byPath[abs] = shared
	return &Outbox{shared: shared}, nil
}

// OpenOutboxReadOnly opens the outbox at path in read-only mode with a
// timeout. It never creates the file or acquires a write lock, making it safe
// for concurrent reads against another process's outbox (e.g. the CLI
// inspecting the daemon's outbox via --daemon). Write operations on a
// read-only outbox return ErrReadOnly.
func OpenOutboxReadOnly(path string) (*Outbox, error) {
	if path == "" {
		return nil, errors.New("local outbox path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve local outbox path: %w", err)
	}
	db, err := bbolt.Open(abs, 0o600, &bbolt.Options{
		ReadOnly: true,
		Timeout:  openTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("open outbox read-only at %s: %w", abs, err)
	}
	shared := &sharedOutbox{path: abs, db: db, refs: 1, readOnly: true}
	return &Outbox{shared: shared}, nil
}

func openOutboxDB(path string) (*bbolt.DB, error) {
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: openTimeout})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{outboxEntriesBucket, outboxPendingBucket, outboxPublishedBucket, outboxFailedBucket, outboxCoordinatesBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		coordinates := tx.Bucket(outboxCoordinatesBucket)
		if coordinates.Get(outboxCoordinatesReady) == nil {
			// Upgrade an existing outbox in one transaction. The cursor keeps
			// memory bounded while every retained package event, including one
			// queued before this index existed, becomes addressable before open.
			cursor := tx.Bucket(outboxEntriesBucket).Cursor()
			for _, raw := cursor.First(); raw != nil; _, raw = cursor.Next() {
				var entry OutboxEntry
				if err := json.Unmarshal(raw, &entry); err != nil {
					return fmt.Errorf("index retained outbox entry: %w", err)
				}
				if key := outboxCoordinateKey(entry.Target, entry.Event); key != nil {
					if err := coordinates.Put(key, nil); err != nil {
						return err
					}
				}
			}
			return coordinates.Put(outboxCoordinatesReady, []byte{1})
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// MovedAside returns where an unreadable outbox file was moved when this
// process opened the outbox, or "".
func (o *Outbox) MovedAside() string { return o.shared.movedFrom }

// IsReadOnly reports whether this outbox handle is read-only.
func (o *Outbox) IsReadOnly() bool { return o.shared.readOnly }

// Close releases this handle. Closing a handle twice is a no-op.
func (o *Outbox) Close() error {
	o.closeOnce.Do(func() {
		openOutboxes.Lock()
		defer openOutboxes.Unlock()
		o.shared.refs--
		if o.shared.refs > 0 {
			return
		}
		delete(openOutboxes.byPath, o.shared.path)
		o.closeErr = o.shared.db.Close()
	})
	return o.closeErr
}

// outboxCoordinatePrefix identifies one package addressable coordinate in one
// publish target. Its event suffix orders newest created_at first, then lowest
// id on ties, matching NIP-01 replacement. The index is updated atomically
// with entries and pruned with them; it never stores event payloads.
func outboxCoordinatePrefix(target string, kind nostr.Kind, author nostr.PubKey, d string) []byte {
	key := make([]byte, 0, 12+len(target)+len(d)+len(author))
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(target)))
	key = append(key, length[:]...)
	key = append(key, target...)
	binary.BigEndian.PutUint32(length[:], uint32(kind))
	key = append(key, length[:]...)
	key = append(key, author[:]...)
	binary.BigEndian.PutUint32(length[:], uint32(len(d)))
	key = append(key, length[:]...)
	return append(key, d...)
}

func outboxCoordinateKey(target string, ev nostr.Event) []byte {
	if !ev.Kind.IsAddressable() {
		return nil
	}
	var d string
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			d = tag[1]
			break
		}
	}
	if !strings.HasPrefix(d, "artifact:sbom-package:") {
		return nil
	}
	key := outboxCoordinatePrefix(target, ev.Kind, ev.PubKey, d)
	var newest [8]byte
	binary.BigEndian.PutUint64(newest[:], ^uint64(ev.CreatedAt))
	key = append(key, newest[:]...)
	return append(key, ev.ID[:]...)
}

// LatestByCoordinate reads the newest unpruned signed package event on one
// target and addressable coordinate. It includes pending entries, which are the durable
// source of truth if a crash happened after enqueue but before the event-store
// cache received the event.
func (o *Outbox) LatestByCoordinate(target string, kind nostr.Kind, author nostr.PubKey, d string) (OutboxEntry, bool, error) {
	prefix := outboxCoordinatePrefix(target, kind, author, d)
	var entry OutboxEntry
	found := false
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		index := tx.Bucket(outboxCoordinatesBucket)
		if index == nil {
			return nil
		}
		key, _ := index.Cursor().Seek(prefix)
		if !bytes.HasPrefix(key, prefix) {
			return nil
		}
		raw := tx.Bucket(outboxEntriesBucket).Get(key[len(key)-len(nostr.ID{}):])
		if raw == nil {
			return fmt.Errorf("outbox coordinate index references a missing event")
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("decode outbox coordinate event: %w", err)
		}
		if !bytes.Equal(outboxCoordinateKey(entry.Target, entry.Event), key) {
			return fmt.Errorf("outbox coordinate index does not match its event")
		}
		found = true
		return nil
	})
	if err != nil {
		return OutboxEntry{}, false, fmt.Errorf("read outbox coordinate %d:%s: %w", kind, d, err)
	}
	return entry, found, nil
}

// Enqueue stores entry as pending and reports whether it was new. An entry
// whose event id is already held, in any state, is left untouched: the same
// signed event is never queued twice.
func (o *Outbox) Enqueue(entry OutboxEntry) (bool, error) {
	if o.shared.readOnly {
		return false, ErrReadOnly
	}
	if entry.Event.ID == nostr.ZeroID {
		return false, errors.New("outbox event id is required")
	}
	if strings.ContainsRune(entry.Target, 0) {
		return false, fmt.Errorf("outbox target %q contains NUL", entry.Target)
	}
	if entry.EnqueuedAt.IsZero() {
		entry.EnqueuedAt = time.Now()
	}
	entry.EnqueuedAt = entry.EnqueuedAt.UTC()
	entry.State = OutboxPending
	entry.SettledAt = time.Time{}
	raw, err := json.Marshal(entry)
	if err != nil {
		return false, fmt.Errorf("encode outbox entry %s: %w", entry.Event.ID.Hex(), err)
	}
	inserted := false
	err = o.shared.db.Update(func(tx *bbolt.Tx) error {
		entries := tx.Bucket(outboxEntriesBucket)
		if entries.Get(entry.Event.ID[:]) != nil {
			return nil
		}
		if err := entries.Put(entry.Event.ID[:], raw); err != nil {
			return err
		}
		inserted = true
		if err := tx.Bucket(outboxPendingBucket).Put(pendingKey(entry.Target, entry.EnqueuedAt, entry.Event.ID), nil); err != nil {
			return err
		}
		if key := outboxCoordinateKey(entry.Target, entry.Event); key != nil {
			return tx.Bucket(outboxCoordinatesBucket).Put(key, nil)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("enqueue outbox entry %s: %w", entry.Event.ID.Hex(), err)
	}
	return inserted, nil
}

// Get returns the entry for id, pending or settled (until pruned).
func (o *Outbox) Get(id nostr.ID) (OutboxEntry, bool, error) {
	var entry OutboxEntry
	found := false
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		raw := tx.Bucket(outboxEntriesBucket).Get(id[:])
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &entry)
	})
	if err != nil {
		return OutboxEntry{}, false, fmt.Errorf("read outbox entry %s: %w", id.Hex(), err)
	}
	return entry, found, nil
}

// ListPending returns up to limit pending entries of target strictly after
// the cursor, oldest first; a nil cursor starts at the oldest.
func (o *Outbox) ListPending(target string, after *OutboxCursor, limit int) ([]OutboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	prefix := append([]byte(target), 0)
	var out []OutboxEntry
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		entries := tx.Bucket(outboxEntriesBucket)
		cursor := tx.Bucket(outboxPendingBucket).Cursor()
		start := prefix
		var skip []byte
		if after != nil {
			skip = pendingKey(target, after.EnqueuedAt, after.ID)
			start = skip
		}
		for key, _ := cursor.Seek(start); key != nil && bytes.HasPrefix(key, prefix) && len(out) < limit; key, _ = cursor.Next() {
			if skip != nil && bytes.Equal(key, skip) {
				continue
			}
			id := key[len(key)-len(nostr.ZeroID):]
			raw := entries.Get(id)
			if raw == nil {
				continue
			}
			var entry OutboxEntry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return fmt.Errorf("decode outbox entry %x: %w", id, err)
			}
			out = append(out, entry)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list pending outbox entries: %w", err)
	}
	return out, nil
}

// ListFailed returns up to limit entries in the failed state, oldest-settled
// first. This is a read-only view for diagnostic inspection of events that
// could not be delivered.
func (o *Outbox) ListFailed(limit int) ([]OutboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []OutboxEntry
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		entries := tx.Bucket(outboxEntriesBucket)
		cursor := tx.Bucket(outboxFailedBucket).Cursor()
		for key, _ := cursor.First(); key != nil && len(out) < limit; key, _ = cursor.Next() {
			// key = settledKey(settledAt, id); id is the last 32 bytes.
			id := key[8:]
			raw := entries.Get(id)
			if raw == nil {
				continue
			}
			var entry OutboxEntry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return fmt.Errorf("decode outbox entry %x: %w", id, err)
			}
			out = append(out, entry)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list failed outbox entries: %w", err)
	}
	return out, nil
}

// ListEntries returns up to limit entries, optionally filtered by one or more
// states. An empty states slice returns all entries. Results are sorted by
// enqueued time (oldest first).
func (o *Outbox) ListEntries(states []string, limit int) ([]OutboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	stateSet := make(map[string]bool, len(states))
	for _, s := range states {
		stateSet[s] = true
	}
	var out []OutboxEntry
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		entries := tx.Bucket(outboxEntriesBucket)
		cursor := entries.Cursor()
		for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
			var entry OutboxEntry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return fmt.Errorf("decode outbox entry %x: %w", key, err)
			}
			if len(stateSet) > 0 && !stateSet[entry.State] {
				continue
			}
			out = append(out, entry)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list outbox entries: %w", err)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].EnqueuedAt.Before(out[j].EnqueuedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CommitRound records a delivery round for id and returns the stored entry.
// Several deliveries of one entry may overlap (an inline publish and a
// runner, or the outgoing and incoming App during a reload), so the commit
// merges instead of overwriting: a relay that accepted or rejected stays so,
// the round count never decreases, and a settled entry is never reopened.
func (o *Outbox) CommitRound(id nostr.ID, round OutboxRound) (OutboxEntry, error) {
	if o.shared.readOnly {
		return OutboxEntry{}, ErrReadOnly
	}
	var stored OutboxEntry
	err := o.shared.db.Update(func(tx *bbolt.Tx) error {
		entries := tx.Bucket(outboxEntriesBucket)
		raw := entries.Get(id[:])
		if raw == nil {
			return fmt.Errorf("outbox entry %s not found", id.Hex())
		}
		if err := json.Unmarshal(raw, &stored); err != nil {
			return fmt.Errorf("decode outbox entry %s: %w", id.Hex(), err)
		}
		if stored.State != OutboxPending {
			return nil
		}
		stored.Relays = mergeRelayDeliveries(stored.Relays, round.Relays)
		stored.Rounds = max(stored.Rounds, round.Rounds)
		stored.Delivered = stored.Delivered || round.Delivered
		stored.LastError = round.Detail
		at := round.At.UTC()
		if at.IsZero() {
			at = time.Now().UTC()
		}
		switch round.State {
		case "", OutboxPending:
		case OutboxPublished, OutboxFailed:
			stored.State = round.State
			stored.SettledAt = at
			if err := tx.Bucket(outboxPendingBucket).Delete(pendingKey(stored.Target, stored.EnqueuedAt, id)); err != nil {
				return err
			}
			if err := tx.Bucket(settledBucket(round.State)).Put(settledKey(at, id), nil); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown outbox state %q", round.State)
		}
		encoded, err := json.Marshal(stored)
		if err != nil {
			return fmt.Errorf("encode outbox entry %s: %w", id.Hex(), err)
		}
		return entries.Put(id[:], encoded)
	})
	if err != nil {
		return OutboxEntry{}, fmt.Errorf("commit outbox round for %s: %w", id.Hex(), err)
	}
	return stored, nil
}

func mergeRelayDeliveries(stored, incoming map[string]RelayDelivery) map[string]RelayDelivery {
	merged := make(map[string]RelayDelivery, len(incoming)+len(stored))
	for url, state := range incoming {
		merged[url] = state
	}
	for url, previous := range stored {
		state, ok := merged[url]
		if !ok {
			merged[url] = previous
			continue
		}
		if previous.Accepted {
			state.Accepted = true
			state.Rejected = ""
			state.LastError = ""
		} else if previous.Rejected != "" && !state.Accepted {
			state.Rejected = previous.Rejected
		}
		if previous.SeenDialFailure.After(state.SeenDialFailure) {
			state.SeenDialFailure = previous.SeenDialFailure
		}
		merged[url] = state
	}
	return merged
}

// OutboxCounts is the outbox's size by state.
type OutboxCounts struct {
	Pending int64
	Failed  int64
}

// Counts returns how many entries are pending and how many failed entries
// are retained.
func (o *Outbox) Counts() (OutboxCounts, error) {
	var counts OutboxCounts
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		counts.Pending = int64(tx.Bucket(outboxPendingBucket).Stats().KeyN)
		counts.Failed = int64(tx.Bucket(outboxFailedBucket).Stats().KeyN)
		return nil
	})
	if err != nil {
		return OutboxCounts{}, fmt.Errorf("count outbox entries: %w", err)
	}
	return counts, nil
}

// Prune deletes published entries settled before publishedBefore and failed
// entries settled before failedBefore, and returns how many it removed.
// Pending entries are never pruned.
func (o *Outbox) Prune(publishedBefore, failedBefore time.Time) (int, error) {
	if o.shared.readOnly {
		return 0, ErrReadOnly
	}
	removed := 0
	err := o.shared.db.Update(func(tx *bbolt.Tx) error {
		entries := tx.Bucket(outboxEntriesBucket)
		for state, before := range map[string]time.Time{OutboxPublished: publishedBefore, OutboxFailed: failedBefore} {
			index := tx.Bucket(settledBucket(state))
			limit := settledKey(before.UTC(), nostr.ZeroID)
			var expired [][]byte
			cursor := index.Cursor()
			for key, _ := cursor.First(); key != nil && bytes.Compare(key, limit) < 0; key, _ = cursor.Next() {
				expired = append(expired, bytes.Clone(key))
			}
			for _, key := range expired {
				if err := index.Delete(key); err != nil {
					return err
				}
				raw := entries.Get(key[8:])
				if raw != nil {
					var entry OutboxEntry
					if err := json.Unmarshal(raw, &entry); err != nil {
						return fmt.Errorf("decode outbox entry for pruning: %w", err)
					}
					if coordinate := outboxCoordinateKey(entry.Target, entry.Event); coordinate != nil {
						if err := tx.Bucket(outboxCoordinatesBucket).Delete(coordinate); err != nil {
							return err
						}
					}
				}
				if err := entries.Delete(key[8:]); err != nil {
					return err
				}
				removed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("prune settled outbox entries: %w", err)
	}
	return removed, nil
}

// Retry resets a failed entry back to pending for re-delivery, clearing its
// relay state and round count so the outbox worker treats it as fresh.
// Returns the reset entry.
func (o *Outbox) Retry(id nostr.ID) (OutboxEntry, error) {
	if o.shared.readOnly {
		return OutboxEntry{}, ErrReadOnly
	}
	var entry OutboxEntry
	err := o.shared.db.Update(func(tx *bbolt.Tx) error {
		entries := tx.Bucket(outboxEntriesBucket)
		raw := entries.Get(id[:])
		if raw == nil {
			return fmt.Errorf("outbox entry %s not found", id.Hex())
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("decode outbox entry %s: %w", id.Hex(), err)
		}
		if entry.State != OutboxFailed {
			return fmt.Errorf("outbox entry %s is %s, not failed", id.Hex(), entry.State)
		}
		// Remove from failed index.
		if err := tx.Bucket(outboxFailedBucket).Delete(settledKey(entry.SettledAt, id)); err != nil {
			return err
		}
		// Reset to pending.
		entry.State = OutboxPending
		entry.SettledAt = time.Time{}
		entry.LastError = ""
		entry.Rounds = 0
		entry.Delivered = false
		entry.Relays = nil
		encoded, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode outbox entry %s: %w", id.Hex(), err)
		}
		if err := entries.Put(id[:], encoded); err != nil {
			return err
		}
		return tx.Bucket(outboxPendingBucket).Put(pendingKey(entry.Target, entry.EnqueuedAt, id), nil)
	})
	if err != nil {
		return OutboxEntry{}, fmt.Errorf("retry outbox entry %s: %w", id.Hex(), err)
	}
	return entry, nil
}

// RetryAllFailed resets every failed entry back to pending. Returns the count.
func (o *Outbox) RetryAllFailed() (int, error) {
	if o.shared.readOnly {
		return 0, ErrReadOnly
	}
	retried := 0
	err := o.shared.db.Update(func(tx *bbolt.Tx) error {
		entries := tx.Bucket(outboxEntriesBucket)
		failedIdx := tx.Bucket(outboxFailedBucket)
		pendingIdx := tx.Bucket(outboxPendingBucket)
		// Collect keys first to avoid mutating during iteration.
		var failedKeys [][]byte
		cursor := failedIdx.Cursor()
		for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
			failedKeys = append(failedKeys, bytes.Clone(key))
		}
		for _, key := range failedKeys {
			id := key[8:] // settledKey = 8-byte timestamp + id
			raw := entries.Get(id)
			if raw == nil {
				continue
			}
			var entry OutboxEntry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return fmt.Errorf("decode outbox entry %x: %w", id, err)
			}
			if err := failedIdx.Delete(key); err != nil {
				return err
			}
			entry.State = OutboxPending
			entry.SettledAt = time.Time{}
			entry.LastError = ""
			entry.Rounds = 0
			entry.Delivered = false
			entry.Relays = nil
			encoded, err := json.Marshal(entry)
			if err != nil {
				return fmt.Errorf("encode outbox entry %x: %w", id, err)
			}
			if err := entries.Put(id, encoded); err != nil {
				return err
			}
			var eid nostr.ID
			copy(eid[:], id)
			if err := pendingIdx.Put(pendingKey(entry.Target, entry.EnqueuedAt, eid), nil); err != nil {
				return err
			}
			retried++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("retry all failed outbox entries: %w", err)
	}
	return retried, nil
}

func settledBucket(state string) []byte {
	if state == OutboxFailed {
		return outboxFailedBucket
	}
	return outboxPublishedBucket
}

// pendingKey orders one target's pending entries by enqueue time, then id.
func pendingKey(target string, enqueuedAt time.Time, id nostr.ID) []byte {
	key := make([]byte, 0, len(target)+1+8+len(id))
	key = append(key, target...)
	key = append(key, 0)
	key = binary.BigEndian.AppendUint64(key, uint64(enqueuedAt.UnixNano()))
	return append(key, id[:]...)
}

// settledKey orders settled entries by settle time, then id.
func settledKey(at time.Time, id nostr.ID) []byte {
	key := binary.BigEndian.AppendUint64(make([]byte, 0, 8+len(id)), uint64(at.UnixNano()))
	return append(key, id[:]...)
}
