package boltcoord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/eventstore/boltdb"
)

// Store writes events to a bolt eventstore and keeps the tag index of values
// the eventstore does not index (see tagIndex) in step with it. Every write
// and delete of a store that serves tag filters through Query must go through
// Store; ReplaceEvent's own removals included, which Replace accounts for.
type Store struct {
	backend *boltdb.BoltBackend
	tags    tagIndex
}

// NewStore returns a Store over backend whose tag index is kept in tagBucket,
// which the caller creates when it opens the backend.
func NewStore(backend *boltdb.BoltBackend, tagBucket []byte) Store {
	return Store{backend: backend, tags: tagIndex{db: backend.DB, bucket: tagBucket}}
}

// Save stores an event with the eventstore's SaveEvent. It returns
// eventstore.ErrDupEvent itself, unwrapped, when the event is already held.
func (st Store) Save(event nostr.Event) error {
	if err := st.tags.index(event); err != nil {
		return fmt.Errorf("index tag values: %w", err)
	}
	err := st.backend.SaveEvent(event)
	if err == nil || errors.Is(err, eventstore.ErrDupEvent) {
		return err
	}
	return errors.Join(err, st.unindexUnstored(event))
}

// unindexUnstored drops the tag entries of an event whose write failed, unless
// a concurrent write of the same event stored it (the entries are its too).
func (st Store) unindexUnstored(event nostr.Event) error {
	if st.held(event.ID) {
		return nil
	}
	return st.tags.unindex(event)
}

func (st Store) held(id nostr.ID) bool {
	for event := range st.backend.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1) {
		return event.ID == id
	}
	return false
}

// Delete removes a stored event and its tag entries. event is the stored
// event: its tags name the entries. Removing an absent event is not an error.
func (st Store) Delete(event nostr.Event) error {
	if err := st.backend.DeleteEvent(event.ID); err != nil {
		return err
	}
	return st.tags.unindex(event)
}

// deleteID removes the stored event with id, if any.
func (st Store) deleteID(id nostr.ID) error {
	for _, event := range slices.Collect(st.backend.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1)) {
		if event.ID == id {
			return st.Delete(event)
		}
	}
	return nil
}

// Replace stores a replaceable or addressable event if it is newer than every
// version held for its coordinate (NIP-01: higher created_at, then lower id)
// and removes the versions it supersedes. It reports false, storing nothing,
// when a version at least as new is held (the event itself included). Callers
// serialise Replace with other writes to the coordinate.
func (st Store) Replace(scan Scan, event nostr.Event) (bool, error) {
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
	if err := st.tags.index(event); err != nil {
		return false, fmt.Errorf("index tag values: %w", err)
	}
	replaced, err := st.backend.ReplaceEvent(event)
	if err != nil {
		return false, errors.Join(err, st.unindexUnstored(event))
	}
	for _, older := range replaced {
		if err := st.tags.unindex(older); err != nil {
			return false, fmt.Errorf("unindex replaced version %s: %w", older.ID.Hex(), err)
		}
	}
	if !c.dIndexed() {
		for _, older := range current {
			if err := st.Delete(older); err != nil {
				return false, fmt.Errorf("delete replaced version %s: %w", older.ID.Hex(), err)
			}
		}
	}
	return true, nil
}

// ApplyDeletion removes the stored events a kind-5 request deletes (NIP-09)
// and returns how many it removed: those its `e` references name when the
// requester is their author, and every version of its `a` coordinates (the
// requester's own; see DeletedCoordinates) up to its created_at. References to
// other authors' events and to deletion requests are ignored. Applying a
// request twice is harmless.
func (st Store) ApplyDeletion(ctx context.Context, scan Scan, request nostr.Event) (int, error) {
	var targets []nostr.Event
	add := func(target nostr.Event) {
		if !slices.ContainsFunc(targets, func(t nostr.Event) bool { return t.ID == target.ID }) {
			targets = append(targets, target)
		}
	}
	for _, tag := range request.Tags {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if len(tag) < 2 || tag[0] != "e" {
			continue
		}
		id, err := nostr.IDFromHex(tag[1])
		if err != nil {
			continue
		}
		for target := range st.backend.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1) {
			if target.ID == id && target.PubKey == request.PubKey && target.Kind != nostr.KindDeletion {
				add(target)
			}
		}
	}
	for _, c := range DeletedCoordinates(request) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		for target := range Versions(scan, c, request.CreatedAt, unbounded) {
			add(target)
		}
	}
	// Collected first: no bbolt write may run inside a read.
	for i, target := range targets {
		if err := st.Delete(target); err != nil {
			return i, fmt.Errorf("delete %s: %w", target.ID.Hex(), err)
		}
	}
	return len(targets), nil
}

// Repair brings a store written before its owner applied NIP-09 and
// latest-wins for every coordinate up to what its writes now maintain, once
// (marker): it applies every stored kind-5 request and collapses each
// coordinate ReplaceEvent cannot resolve (an addressable d that is empty or
// longer than TagIndexMaxValue) to its latest version. ReplaceEvent always
// collapsed the other coordinates. The caller indexes the stored requests in
// its DeletionIndex first. Each step is safe to repeat, so a repair
// interrupted before the marker is written runs again in full.
func (st Store) Repair(ctx context.Context, scan Scan, marker Marker) error {
	done, err := marker.Done(st.backend.DB)
	if err != nil {
		return fmt.Errorf("read coordinate repair marker: %w", err)
	}
	if done {
		return nil
	}
	// scan copies each page out of its read transaction, so deleting between
	// requests is safe; deletion requests are never deleted.
	for request := range scan(nostr.Filter{Kinds: []nostr.Kind{nostr.KindDeletion}}, unbounded) {
		if _, err := st.ApplyDeletion(ctx, scan, request); err != nil {
			return fmt.Errorf("apply stored deletion request %s: %w", request.ID.Hex(), err)
		}
	}
	if err := st.collapseVersions(ctx, scan); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := st.backend.DB.Update(marker.Set); err != nil {
		return fmt.Errorf("write coordinate repair marker: %w", err)
	}
	return nil
}

// collapseVersions deletes every stored addressable event whose d the
// eventstore does not index and that is not the latest version of its
// coordinate.
func (st Store) collapseVersions(ctx context.Context, scan Scan) error {
	latest := map[Coordinate]nostr.Event{} // only ID and CreatedAt, for nostr.IsOlder
	var superseded []nostr.ID
	for event := range scan(nostr.Filter{}, unbounded) {
		if err := ctx.Err(); err != nil {
			return err
		}
		c := CoordinateOf(event)
		if !event.Kind.IsAddressable() || c.dIndexed() {
			continue
		}
		version := nostr.Event{ID: event.ID, CreatedAt: event.CreatedAt}
		current, held := latest[c]
		switch {
		case !held:
			latest[c] = version
		case nostr.IsOlder(current, version):
			superseded = append(superseded, current.ID)
			latest[c] = version
		default:
			superseded = append(superseded, version.ID)
		}
	}
	for _, id := range superseded {
		if err := st.deleteID(id); err != nil {
			return fmt.Errorf("delete superseded version %s: %w", id.Hex(), err)
		}
	}
	return nil
}

// BuildTagIndex indexes the tag values of every stored event once per store
// (marker), for stores written before the tag index existed, in transactions
// of at most batch entries; the marker commits with the last of them. Events
// saved through Store later are indexed as they are written. Indexing an
// event twice is harmless, so an interrupted build simply runs again. This is
// the only full read of the store the tag index needs.
func (st Store) BuildTagIndex(ctx context.Context, scan Scan, marker Marker, batch int) error {
	done, err := marker.Done(st.backend.DB)
	if err != nil {
		return fmt.Errorf("read tag index marker: %w", err)
	}
	if done {
		return nil
	}
	keys := func(yield func([]byte) bool) {
		for event := range scan(nostr.Filter{}, unbounded) {
			for _, key := range tagKeys(event) {
				if !yield(key) {
					return
				}
			}
		}
	}
	if err := putBatched(ctx, st.backend.DB, st.tags.bucket, keys, batch, marker.Set); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("build tag index: %w", err)
	}
	return nil
}

// NeedsTagIndex reports whether filter names a tag value the eventstore cannot
// find (empty or longer than TagIndexMaxValue). The eventstore matches nothing
// for such a filter, so it must be read through Store.Query.
func NeedsTagIndex(filter nostr.Filter) bool {
	_, ok := unindexedFilterTag(filter)
	return ok
}

// unindexedFilterTag picks the filter tag Query drives its read with: the
// first, by name, that has a value the eventstore does not index.
func unindexedFilterTag(filter nostr.Filter) (string, bool) {
	var names []string
	for name, values := range filter.Tags {
		if len(name) == 1 && slices.ContainsFunc(values, func(value string) bool { return !tagIndexable(value) }) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", false
	}
	slices.Sort(names)
	return names[0], true
}

// Query yields the stored events matching filter, newest first, at most limit
// (<= 0: no cap). indexed is the caller's read of the eventstore itself, which
// must not route back to Query. A filter NeedsTagIndex rejects goes straight
// to indexed. Otherwise one tag drives the read: each of its values the
// eventstore does not index is read from the tag index, the others through
// indexed with only that tag (and the filter's kinds, authors, since and
// until), and the streams are merged newest first; every event is checked
// against the whole filter before it is yielded. That costs one index range
// per value plus the eventstore's own read of the rest, as any tag filter
// does, and never a scan of the store.
func (st Store) Query(indexed Scan, filter nostr.Filter, limit int) iter.Seq[nostr.Event] {
	if limit <= 0 {
		limit = unbounded
	}
	name, ok := unindexedFilterTag(filter)
	if !ok {
		return indexed(filter, limit)
	}
	return func(yield func(nostr.Event) bool) {
		if filter.LimitZero || filter.Search != "" {
			return // the eventstore answers neither
		}
		var sources []iter.Seq[nostr.Event]
		var short []string
		for _, value := range filter.Tags[name] {
			if tagIndexable(value) {
				short = append(short, value)
				continue
			}
			sources = append(sources, st.tags.events(st.backend, name, value, filter.Since, filter.Until))
		}
		if len(short) > 0 {
			sources = append(sources, indexed(nostr.Filter{
				Kinds: filter.Kinds, Authors: filter.Authors, Since: filter.Since, Until: filter.Until,
				Tags: nostr.TagMap{name: short},
			}, unbounded))
		}
		seen := map[nostr.ID]struct{}{}
		emitted := 0
		for event := range mergeNewestFirst(sources) {
			if _, dup := seen[event.ID]; dup {
				continue
			}
			seen[event.ID] = struct{}{}
			if !filter.Matches(event) {
				continue
			}
			if !yield(event) {
				return
			}
			if emitted++; emitted >= limit {
				return
			}
		}
	}
}

// mergeNewestFirst merges event streams that are each newest first.
func mergeNewestFirst(sources []iter.Seq[nostr.Event]) iter.Seq[nostr.Event] {
	if len(sources) == 1 {
		return sources[0]
	}
	return func(yield func(nostr.Event) bool) {
		type head struct {
			next  func() (nostr.Event, bool)
			event nostr.Event
			ok    bool
		}
		heads := make([]head, len(sources))
		for i, source := range sources {
			next, stop := iter.Pull(source)
			defer stop()
			heads[i].next = next
			heads[i].event, heads[i].ok = next()
		}
		for {
			best := -1
			for i, h := range heads {
				if h.ok && (best < 0 || newer(h.event, heads[best].event)) {
					best = i
				}
			}
			if best < 0 || !yield(heads[best].event) {
				return
			}
			heads[best].event, heads[best].ok = heads[best].next()
		}
	}
}

func newer(a, b nostr.Event) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return bytes.Compare(a.ID[:], b.ID[:]) < 0
}
