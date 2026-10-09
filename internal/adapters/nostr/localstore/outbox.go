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
	"github.com/openagentsinc/bahia/internal/kinds"
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
	outboxEntriesBucket        = []byte("bahiaOutboxEntries")
	outboxPendingBucket        = []byte("bahiaOutboxPending")
	outboxPublishedBucket      = []byte("bahiaOutboxPublished")
	outboxFailedBucket         = []byte("bahiaOutboxFailed")
	outboxCoordinatesBucket    = []byte("bahiaOutboxCoordinatesV1")
	outboxCoordinatesReady     = []byte("index-ready")
	outboxDeliveryProofsBucket = []byte("bahiaOutboxDeliveryProofsV1")
)

// DeliveryPolicy is the publisher's write-relay policy at the instant the
// event reached quorum. It is not inferred from a later relay configuration.
type DeliveryPolicy struct {
	WriteRelays []string `json:"write_relays"`
	Required    int      `json:"required"`
}

// DeliveryProof retains the exact signed canonical event and the relay
// outcomes that satisfied its historical publish policy after its outbox row
// is pruned. An absent proof is not evidence of delivery.
type DeliveryProof struct {
	Event      nostr.Event     `json:"event"`
	Target     string          `json:"target"`
	Policy     DeliveryPolicy  `json:"policy"`
	RelayOK    map[string]bool `json:"relay_ok"`
	AcceptedAt time.Time       `json:"accepted_at"`
}

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
	Policy    DeliveryPolicy           `json:"policy,omitzero"`
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
	// Target binds verified publisher outcomes to the entry's relay pool.
	Target    string
	Rounds    int
	Relays    map[string]RelayDelivery
	Delivered bool
	Policy    DeliveryPolicy
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

// OpenExistingOutboxStrict takes the daemon outbox's exclusive lock without
// creating, upgrading, renaming, or replacing the file. Offline cutover tools
// use it because even a corrupt outbox may hold the only signed copy of an
// event; an automatic repair would destroy cutover evidence.
func OpenExistingOutboxStrict(path string) (*Outbox, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("strict outbox path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat existing outbox %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("existing outbox %s must be a nonempty regular file (no symlinks)", path)
	}
	openOutboxes.Lock()
	defer openOutboxes.Unlock()
	if openOutboxes.byPath[path] != nil {
		return nil, fmt.Errorf("outbox %s is already open in this process", path)
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: openTimeout})
	if err != nil {
		return nil, fmt.Errorf("open existing outbox %s without repair: %w", path, err)
	}
	shared := &sharedOutbox{path: path, db: db, refs: 1}
	openOutboxes.byPath[path] = shared
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
		for _, name := range [][]byte{outboxEntriesBucket, outboxPendingBucket, outboxPublishedBucket, outboxFailedBucket, outboxCoordinatesBucket, outboxDeliveryProofsBucket} {
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

// Enqueue stores an undelivered entry as pending and reports whether it was new.
// Caller-supplied delivery state is refused; only Publisher's verified
// pre-commit path may admit a row containing relay outcomes.
// An entry
// whose event id is already held, in any state, is left untouched: the same
// signed event is never queued twice.
func (o *Outbox) Enqueue(entry OutboxEntry) (bool, error) {
	if entry.Delivered || entry.Rounds != 0 || len(entry.Relays) != 0 || len(entry.Policy.WriteRelays) != 0 || entry.Policy.Required != 0 {
		return false, errors.New("unverified outbox enqueue cannot contain delivery state")
	}
	return o.enqueue(entry)
}

// EnqueuePublisherDelivery retains the publisher's already-verified relay
// outcomes after PublishBeforeCommit reached quorum. Enqueue itself cannot
// mint or retain those outcomes. This admission still creates no proof: the
// publisher must commit its verified round before reporting success.
func (o *Outbox) EnqueuePublisherDelivery(entry OutboxEntry) (bool, error) {
	if !entry.Delivered {
		return false, errors.New("publisher delivery enqueue requires a reached quorum")
	}
	return o.enqueue(entry)
}

func (o *Outbox) enqueue(entry OutboxEntry) (bool, error) {
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
			if err := tx.Bucket(outboxCoordinatesBucket).Put(key, nil); err != nil {
				return err
			}
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

// GetDeliveryProof reads a non-prunable quorum receipt for a canonical config
// event. Rows predating this proof format are deliberately not backfilled from
// an outbox Delivered bit: that bit lacks the target relay policy.
func (o *Outbox) GetDeliveryProof(id nostr.ID) (DeliveryProof, bool, error) {
	var proof DeliveryProof
	found := false
	err := o.shared.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(outboxDeliveryProofsBucket)
		if bucket == nil {
			return nil
		}
		raw := bucket.Get(id[:])
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &proof)
	})
	if err != nil {
		return DeliveryProof{}, false, fmt.Errorf("read outbox delivery proof %s: %w", id.Hex(), err)
	}
	return proof, found, nil
}

// ValidFor checks the exact signed event, target, historical relay policy and
// per-relay OK set. It never treats the current relay configuration as proof
// that an earlier publication met quorum.
func (p DeliveryProof) ValidFor(event nostr.Event, target string) bool {
	if p.Target != target || p.Event.ID != event.ID || p.Event.PubKey != event.PubKey ||
		p.Event.Sig != event.Sig || !event.CheckID() || !event.VerifySignature() ||
		!p.Event.CheckID() || !p.Event.VerifySignature() ||
		p.AcceptedAt.IsZero() || p.Policy.Required < 1 ||
		p.Policy.Required > len(p.Policy.WriteRelays) || len(p.RelayOK) != len(p.Policy.WriteRelays) {
		return false
	}
	seen := make(map[string]struct{}, len(p.Policy.WriteRelays))
	accepted := 0
	for _, relay := range p.Policy.WriteRelays {
		if relay == "" {
			return false
		}
		if _, duplicate := seen[relay]; duplicate {
			return false
		}
		seen[relay] = struct{}{}
		ok, exists := p.RelayOK[relay]
		if !exists {
			return false
		}
		if ok {
			accepted++
		}
	}
	return accepted >= p.Policy.Required
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

// CommitRound records an untrusted delivery round for id and returns the stored
// entry. It cannot mint a backup-config quorum proof from caller-supplied flags.
// Several deliveries of one entry may overlap (an inline publish and a
// runner, or the outgoing and incoming App during a reload), so the commit
// merges instead of overwriting: a relay that accepted or rejected stays so,
// the round count never decreases, and a settled entry is never reopened.
func (o *Outbox) CommitRound(id nostr.ID, round OutboxRound) (OutboxEntry, error) {
	return o.commitRound(id, round, false)
}

// CommitPublisherRound records a round observed by Publisher's relay pool.
// Only that verified OK path may request backup-config proof creation. NIP-01
// OK frames are unsigned, so the trusted boundary is the publisher's relay
// transport and its verified per-relay PublishResult, not arbitrary callers
// of Enqueue or CommitRound.
func (o *Outbox) CommitPublisherRound(id nostr.ID, round OutboxRound) (OutboxEntry, error) {
	return o.commitRound(id, round, true)
}

func (o *Outbox) commitRound(id nostr.ID, round OutboxRound, recordProof bool) (OutboxEntry, error) {
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
		if !recordProof && isBackupConfigEvent(stored.Event) {
			return errors.New("backup config delivery rounds require the publisher path")
		}
		if recordProof && round.Target != stored.Target {
			return fmt.Errorf("publisher round target %q differs from outbox target %q", round.Target, stored.Target)
		}
		at := round.At.UTC()
		if at.IsZero() {
			at = time.Now().UTC()
		}
		if stored.State != OutboxPending {
			return recordPublisherProof(tx, stored, round, at, recordProof)
		}
		stored.Relays = mergeRelayDeliveries(stored.Relays, round.Relays)
		stored.Rounds = max(stored.Rounds, round.Rounds)
		stored.Delivered = stored.Delivered || round.Delivered
		if round.Delivered && len(round.Policy.WriteRelays) > 0 {
			stored.Policy = round.Policy
		}
		stored.LastError = round.Detail
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
		if err := entries.Put(id[:], encoded); err != nil {
			return err
		}
		return recordPublisherProof(tx, stored, round, at, recordProof)
	})
	if err != nil {
		return OutboxEntry{}, fmt.Errorf("commit outbox round for %s: %w", id.Hex(), err)
	}
	return stored, nil
}

func recordPublisherProof(tx *bbolt.Tx, entry OutboxEntry, round OutboxRound, at time.Time, publisherRound bool) error {
	if !publisherRound || !round.Delivered || !isProofEligibleEvent(entry.Event) {
		return nil
	}
	proofs := tx.Bucket(outboxDeliveryProofsBucket)
	if proofs.Get(entry.Event.ID[:]) != nil {
		return nil
	}
	proofEntry := entry
	proofEntry.Relays = round.Relays
	proof, ok := canonicalDeliveryProof(proofEntry, round.Policy, at)
	if !ok {
		return fmt.Errorf("canonical config %s publisher round lacks verified target quorum", entry.Event.ID.Hex())
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return fmt.Errorf("encode outbox delivery proof %s: %w", entry.Event.ID.Hex(), err)
	}
	return proofs.Put(entry.Event.ID[:], raw)
}

func canonicalDeliveryProof(entry OutboxEntry, policy DeliveryPolicy, at time.Time) (DeliveryProof, bool) {
	ev := entry.Event
	if !isProofEligibleEvent(ev) || !ev.CheckID() || !ev.VerifySignature() || outboxTag(ev.Tags, "d") == "" {
		return DeliveryProof{}, false
	}
	relayOK := make(map[string]bool, len(policy.WriteRelays))
	for _, relay := range policy.WriteRelays {
		relayOK[relay] = entry.Relays[relay].Accepted
	}
	proof := DeliveryProof{Event: ev, Target: entry.Target, Policy: policy, RelayOK: relayOK, AcceptedAt: at}
	return proof, proof.ValidFor(ev, entry.Target)
}

func isProofEligibleEvent(ev nostr.Event) bool {
	return isBackupConfigEvent(ev) || isDeploymentPolicyEvent(ev)
}

func isDeploymentPolicyEvent(ev nostr.Event) bool {
	return ev.Kind == nostr.Kind(kinds.CASControlState) &&
		outboxTag(ev.Tags, "domain") == "policy" &&
		outboxTag(ev.Tags, "t") == kinds.CPStateTopicPolicyRegistry &&
		outboxTag(ev.Tags, "d") != ""
}

func isBackupConfigEvent(ev nostr.Event) bool {
	if ev.Kind != nostr.Kind(kinds.CASControlState) || outboxTag(ev.Tags, "domain") != "backup" {
		return false
	}
	switch outboxTag(ev.Tags, "t") {
	case kinds.CPStateTopicBackupRecipe, kinds.CPStateTopicBackupRepository, kinds.CPStateTopicBackupPolicy:
		return true
	default:
		return false
	}
}

func outboxTag(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
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
