package localstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/boltcoord"
	"go.etcd.io/bbolt"
)

// undeliveredBucket holds one Undelivered record per coordinate whose latest
// event produced by this daemon was abandoned by the publish outbox. It is
// the store-side half of the abandoned-delivery contract (design
// docs/architecture/outbox-delivery.md): the event itself stays in the store so canonical reads keep
// answering with the state the daemon committed to, and the marker tells
// readers and the readiness endpoint that relays do not hold it.
var undeliveredBucket = []byte("bahiaLocalUndelivered")

// Undelivered records that the publish outbox abandoned the latest event this
// daemon produced on a coordinate: the event is held locally and is the
// daemon's committed state, but no publish quorum accepted it. The marker is
// cleared when a publish quorum accepts the event (an operator retry of the
// outbox entry) or when a newer version is saved on the coordinate, whether
// the daemon produced it (the next publish of the coordinate, whose own
// delivery then decides) or a relay served it (the coordinate has moved on
// without this daemon).
type Undelivered struct {
	// Coordinate is "<kind>:<pubkey>:<d>" for replaceable and addressable
	// events and the event id for regular ones (see undeliveredKey).
	Coordinate string          `json:"coordinate"`
	EventID    nostr.ID        `json:"event_id"`
	Kind       nostr.Kind      `json:"kind"`
	CreatedAt  nostr.Timestamp `json:"created_at"`
	FailedAt   time.Time       `json:"failed_at"`
	// Detail is the outbox's abandonment reason.
	Detail string `json:"detail,omitempty"`
}

// undeliveredKey is the marker key of ev: its NIP-01 coordinate for
// replaceable and addressable kinds, since a later version on the coordinate
// supersedes it, and its id for regular kinds, which stand alone.
func undeliveredKey(ev nostr.Event) string {
	if ev.Kind.IsReplaceable() || ev.Kind.IsAddressable() {
		return boltcoord.CoordinateOf(ev).String()
	}
	return ev.ID.Hex()
}

// MarkUndelivered records that the outbox abandoned ev. It reports whether a
// marker was written: nothing is recorded when the store already holds a newer
// version of ev's coordinate (a later publish superseded ev and will report
// its own outcome) or a marker for a newer event, so an abandonment reported
// late cannot flag state that has moved on.
func (s *Store) MarkUndelivered(ev nostr.Event, detail string, at time.Time) (bool, error) {
	s.shared.saveMu.Lock()
	defer s.shared.saveMu.Unlock()
	if ev.Kind.IsReplaceable() || ev.Kind.IsAddressable() {
		if latest, held := boltcoord.LatestVersion(s.scan, boltcoord.CoordinateOf(ev)); held && newerThan(latest, ev) {
			return false, nil
		}
	}
	key := []byte(undeliveredKey(ev))
	marker := Undelivered{Coordinate: string(key), EventID: ev.ID, Kind: ev.Kind, CreatedAt: ev.CreatedAt, FailedAt: at.UTC(), Detail: detail}
	written := false
	err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(undeliveredBucket)
		if raw := bucket.Get(key); raw != nil {
			var existing Undelivered
			if err := json.Unmarshal(raw, &existing); err == nil && existing.EventID != ev.ID && !olderThan(existing, ev) {
				return nil
			}
		}
		encoded, err := json.Marshal(marker)
		if err != nil {
			return err
		}
		written = true
		return bucket.Put(key, encoded)
	})
	if err != nil {
		return false, fmt.Errorf("mark local event %s undelivered: %w", ev.ID.Hex(), err)
	}
	return written, nil
}

// ClearUndelivered removes the marker on ev's coordinate because a publish
// quorum accepted ev. It reports whether a marker was removed. A marker for an
// event newer than ev is kept: the acceptance of an older version does not
// put the latest state on the relays.
func (s *Store) ClearUndelivered(ev nostr.Event) (bool, error) {
	removed, err := s.clearUndeliveredUnless(ev, func(existing Undelivered) bool {
		return existing.EventID != ev.ID && !olderThan(existing, ev)
	})
	if err != nil {
		return false, fmt.Errorf("clear undelivered marker for local event %s: %w", ev.ID.Hex(), err)
	}
	return removed, nil
}

// clearSupersededUndelivered removes the marker on ev's coordinate when ev is
// a newer version than the marked event. SaveEvent calls it under saveMu.
func (s *Store) clearSupersededUndelivered(ev nostr.Event) (bool, error) {
	removed, err := s.clearUndeliveredUnless(ev, func(existing Undelivered) bool {
		return !olderThan(existing, ev)
	})
	if err != nil {
		return false, fmt.Errorf("clear superseded undelivered marker for local event %s: %w", ev.ID.Hex(), err)
	}
	return removed, nil
}

// clearUndeliveredUnless deletes the marker on ev's coordinate unless keep
// says the stored marker must stay.
func (s *Store) clearUndeliveredUnless(ev nostr.Event, keep func(Undelivered) bool) (bool, error) {
	key := []byte(undeliveredKey(ev))
	removed := false
	err := s.backend().DB.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(undeliveredBucket)
		raw := bucket.Get(key)
		if raw == nil {
			return nil
		}
		var existing Undelivered
		if err := json.Unmarshal(raw, &existing); err == nil && keep(existing) {
			return nil
		}
		removed = true
		return bucket.Delete(key)
	})
	return removed, err
}

// Undelivered returns the marker on ev's coordinate, if any.
func (s *Store) Undelivered(ev nostr.Event) (Undelivered, bool, error) {
	key := []byte(undeliveredKey(ev))
	var marker Undelivered
	found := false
	err := s.backend().DB.View(func(tx *bbolt.Tx) error {
		raw := tx.Bucket(undeliveredBucket).Get(key)
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &marker)
	})
	if err != nil {
		return Undelivered{}, false, fmt.Errorf("read undelivered marker for local event %s: %w", ev.ID.Hex(), err)
	}
	return marker, found, nil
}

// ListUndelivered returns every marker, ordered by coordinate.
func (s *Store) ListUndelivered() ([]Undelivered, error) {
	var out []Undelivered
	err := s.backend().DB.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(undeliveredBucket).ForEach(func(key, raw []byte) error {
			var marker Undelivered
			if err := json.Unmarshal(raw, &marker); err != nil {
				return fmt.Errorf("decode undelivered marker %q: %w", bytes.Clone(key), err)
			}
			out = append(out, marker)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("list undelivered markers: %w", err)
	}
	return out, nil
}

// UndeliveredEventIDs returns the ids of the events the markers point at, so a
// reader can flag those records without a lookup per event.
func (s *Store) UndeliveredEventIDs() (map[nostr.ID]struct{}, error) {
	markers, err := s.ListUndelivered()
	if err != nil {
		return nil, err
	}
	ids := make(map[nostr.ID]struct{}, len(markers))
	for _, marker := range markers {
		ids[marker.EventID] = struct{}{}
	}
	return ids, nil
}

// newerThan reports whether a wins over b under NIP-01 replacement: a higher
// created_at, then a lower id.
func newerThan(a, b nostr.Event) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID != b.ID && bytes.Compare(a.ID[:], b.ID[:]) < 0
}

// olderThan reports whether marker's event is older than ev (ev would replace
// it under NIP-01).
func olderThan(marker Undelivered, ev nostr.Event) bool {
	if marker.CreatedAt != ev.CreatedAt {
		return marker.CreatedAt < ev.CreatedAt
	}
	return bytes.Compare(ev.ID[:], marker.EventID[:]) < 0
}
