package relaysidecar

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/boltcoord"
)

// tagIndexMaxValue is the longest tag value the bolt eventstore indexes; see
// package boltcoord for how the sidecar handles longer and empty ones.
const tagIndexMaxValue = boltcoord.TagIndexMaxValue

// sidecarDeletionBucket indexes the coordinates that stored kind-5 requests
// delete (NIP-09 `a` references; see boltcoord.DeletionIndex). The
// eventstore's tag index cannot answer this: relay config coordinates are 108
// bytes. An entry is written before its request is saved and removed when the
// request is (only the NIP-40 sweep removes one; retention keeps tombstones for
// good).
var sidecarDeletionBucket = []byte("bahiaSidecarDeletions")

// deletionIndexVersionKey marks, in sidecarMetaBucket, that every stored kind-5
// request has been indexed (see buildDeletionIndex).
var deletionIndexVersionKey = []byte("deletionIndexVersion")

const deletionIndexVersion = "1"

var deletionIndexMarker = boltcoord.Marker{Bucket: sidecarMetaBucket, Key: deletionIndexVersionKey, Version: deletionIndexVersion}

// sidecarTagBucket indexes the tag values the eventstore does not (empty, or
// longer than tagIndexMaxValue; see boltcoord.Store), so a REQ or COUNT on
// #a with a relay config coordinate or on an empty #d matches what it should
// . Every write and delete goes through eventStore.coords,
// which keeps it in step.
var sidecarTagBucket = []byte("bahiaSidecarTags")

// tagIndexMarker marks, in sidecarMetaBucket, that every event stored before
// the tag index existed has been indexed (see buildTagIndex).
var tagIndexMarker = boltcoord.Marker{Bucket: sidecarMetaBucket, Key: []byte("tagIndexVersion"), Version: "1"}

// coordinateRepairMarker marks, in sidecarMetaBucket, that the store has been
// repaired once (see repairCoordinates).
var coordinateRepairMarker = boltcoord.Marker{Bucket: sidecarMetaBucket, Key: []byte("coordinateRepairVersion"), Version: "1"}

// coordinate is a replaceable or addressable event's address.
type coordinate = boltcoord.Coordinate

func newCoordinate(kind nostr.Kind, author nostr.PubKey, d string) coordinate {
	return boltcoord.NewCoordinate(kind, author, d)
}

func (s *eventStore) deletions() boltcoord.DeletionIndex {
	return boltcoord.NewDeletionIndex(s.backend().DB, sidecarDeletionBucket)
}

// indexDeletion records the coordinates request deletes.
func (s *eventStore) indexDeletion(request nostr.Event) error {
	if err := s.deletions().Index(request); err != nil {
		return fmt.Errorf("index relay deletion request: %w", err)
	}
	return nil
}

// unindexDeletion removes request's entries, when the request itself goes.
func (s *eventStore) unindexDeletion(request nostr.Event) error {
	if err := s.deletions().Unindex(request); err != nil {
		return fmt.Errorf("unindex relay deletion request: %w", err)
	}
	return nil
}

// coordinateDeleted reports whether a stored request of the coordinate's author
// deletes it at or after since.
func (s *eventStore) coordinateDeleted(c coordinate, since nostr.Timestamp) (bool, error) {
	deleted, err := s.deletions().Deleted(c, since)
	if err != nil {
		return false, fmt.Errorf("read relay deletion index: %w", err)
	}
	return deleted, nil
}

// buildDeletionIndex indexes every stored kind-5 request once per store, for
// stores written before the index existed. Requests saved later are indexed as
// they are saved, so the marker makes later opens free.
func (s *eventStore) buildDeletionIndex(ctx context.Context) error {
	built, err := deletionIndexMarker.Done(s.backend().DB)
	if err != nil {
		return fmt.Errorf("read relay deletion index marker: %w", err)
	}
	if built {
		return nil
	}
	// scan copies each page out of its read transaction, so writing while
	// iterating is safe.
	requests := s.scan(nostr.Filter{Kinds: []nostr.Kind{nostr.KindDeletion}}, unboundedQueryLimit)
	if err := s.deletions().Backfill(ctx, requests, sweepBatchSize, deletionIndexMarker.Set); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("build relay deletion index: %w", err)
	}
	return nil
}

// buildTagIndex indexes the tag values of every stored event once per store,
// for stores written before the tag index existed (boltcoord.Store
// BuildTagIndex). Events written later are indexed as they are written.
func (s *eventStore) buildTagIndex(ctx context.Context) error {
	if err := s.coords().BuildTagIndex(ctx, s.scanIndexed, tagIndexMarker, sweepBatchSize); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("relay tag index: %w", err)
	}
	return nil
}

// repairCoordinates brings a store written before up to what
// writes now maintain, once: such a store kept every version
// of an addressable coordinate whose d is empty or longer than
// tagIndexMaxValue, and may serve events its stored kind-5 requests delete
// (applyDeletion missed coordinates longer than the eventstore indexes, and
// kind-5s imported from SQLite were never applied). It applies every stored
// request and collapses those coordinates to their latest version
// (boltcoord.Store.Repair). buildDeletionIndex runs first, so the requests are
// already indexed.
func (s *eventStore) repairCoordinates(ctx context.Context) error {
	if err := s.coords().Repair(ctx, s.scan, coordinateRepairMarker); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("repair relay event store: %w", err)
	}
	return nil
}

// latestVersion returns the newest stored version of c.
func (s *eventStore) latestVersion(c coordinate) (nostr.Event, bool) {
	return boltcoord.LatestVersion(s.scan, c)
}
