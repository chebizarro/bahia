package controlplane

import (
	"context"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/kinds"
)

// workerOrphanCleanupResult describes the outcome of a single orphaned worker
// record deletion.
type workerOrphanCleanupResult struct {
	// DTag is the bare-pubkey d tag of the orphaned 30900 record.
	DTag string
	// EventID is the id of the orphaned event.
	EventID string
	// DeletionID is the id of the kind-5 deletion event, empty in dry-run mode.
	DeletionID string
}

// workerOrphanRelay abstracts the relay operations the cleanup needs.
type workerOrphanRelay interface {
	QuerySync(ctx context.Context, filter nostr.Filter) ([]*nostr.Event, error)
	Publish(ctx context.Context, event nostr.Event) error
}

// cleanupOrphanedWorkerRecords finds 30900 worker records published by
// daemonPubkey that carry a bare-pubkey d tag (no "worker:" prefix) and
// publishes NIP-09 kind-5 deletions for them on the relay.
//
// When dryRun is true, the orphans are identified but no deletions are
// published. The function returns one result per orphaned record found.
func cleanupOrphanedWorkerRecords(ctx context.Context, relay workerOrphanRelay, secretKey nostr.SecretKey, dryRun bool) ([]workerOrphanCleanupResult, error) {
	daemonPubkey := secretKey.Public()
	// Fetch all 30900 events authored by the daemon. We cannot assume the
	// relay indexes multi-letter tags, so we query by kind+author and filter
	// locally for worker-domain records.
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{daemonPubkey},
		Limit:   1000,
	}
	events, err := relay.QuerySync(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("query 30900 records: %w", err)
	}

	var results []workerOrphanCleanupResult
	for _, ev := range events {
		dTag := workerOrphanTagValue(ev.Tags, "d")
		if dTag == "" {
			continue
		}
		// Only consider worker-domain records.
		if !isWorkerRecord(ev.Tags) {
			continue
		}
		// A record is orphaned if its d tag is a bare value (no "worker:" prefix).
		if strings.HasPrefix(dTag, "worker:") {
			continue
		}
		result := workerOrphanCleanupResult{
			DTag:    dTag,
			EventID: ev.ID.Hex(),
		}
		if !dryRun {
			address := fmt.Sprintf("%d:%s:%s", kinds.CASControlState, daemonPubkey.Hex(), dTag)
			deletion := nostr.Event{
				Kind: nostr.KindDeletion,
				Tags: nostr.Tags{{"a", address}},
			}
			if err := deletion.Sign(secretKey); err != nil {
				return results, fmt.Errorf("sign deletion for d=%s: %w", dTag, err)
			}
			if err := relay.Publish(ctx, deletion); err != nil {
				return results, fmt.Errorf("publish deletion for d=%s: %w", dTag, err)
			}
			result.DeletionID = deletion.ID.Hex()
		}
		results = append(results, result)
	}
	return results, nil
}

// isWorkerRecord reports whether an event's tags indicate it is a worker-domain record.
func isWorkerRecord(tags nostr.Tags) bool {
	domain := workerOrphanTagValue(tags, "domain")
	if domain == kinds.WorkerDomain {
		return true
	}
	legacyKind := workerOrphanTagValue(tags, "legacy_kind")
	switch legacyKind {
	case "32000", "32001", "32002", "32003", "32004":
		return true
	}
	return false
}

// workerOrphanTagValue returns the first value for a named tag, or empty string.
func workerOrphanTagValue(tags nostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}
