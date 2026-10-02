package controlplane

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/kinds"
)

// WorkerOrphanCleanupResult describes the outcome of a single orphaned worker
// record deletion.
type WorkerOrphanCleanupResult struct {
	// DTag is the bare-pubkey d tag of the orphaned 30900 record.
	DTag string
	// EventID is the id of the orphaned event.
	EventID string
	// DeletionID is the id of the kind-5 deletion event, empty in dry-run mode.
	DeletionID string
}

// WorkerOrphanRelay abstracts the relay operations the cleanup needs.
type WorkerOrphanRelay interface {
	QueryEvents(filter nostr.Filter) iter.Seq[nostr.Event]
	Publish(ctx context.Context, event nostr.Event) error
}

// CleanupOrphanedWorkerRecords finds 30900 worker records published by
// the signer's public key that carry a bare-pubkey d tag (no "worker:" prefix)
// and publishes NIP-09 kind-5 deletions for them on the relay.
//
// The signer must be the service identity that originally authored the records;
// NIP-09 deletions only take effect when signed by the original author.
//
// When dryRun is true, the orphans are identified but no deletions are
// published. The function returns one result per orphaned record found.
func CleanupOrphanedWorkerRecords(ctx context.Context, relay WorkerOrphanRelay, signer nostr.Signer, dryRun bool) ([]WorkerOrphanCleanupResult, error) {
	if signer == nil {
		return nil, fmt.Errorf("signer is required: NIP-09 deletions must be signed by the original author (the service identity)")
	}
	daemonPubkey, err := signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("get signer public key: %w", err)
	}

	// Fetch all 30900 events authored by the daemon. We cannot assume the
	// relay indexes multi-letter tags, so we query by kind+author and filter
	// locally for worker-domain records with bare-pubkey d tags.
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{daemonPubkey},
		Limit:   1000,
	}

	var results []WorkerOrphanCleanupResult
	for ev := range relay.QueryEvents(filter) {
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
		result := WorkerOrphanCleanupResult{
			DTag:    dTag,
			EventID: ev.ID.Hex(),
		}
		if !dryRun {
			address := fmt.Sprintf("%d:%s:%s", kinds.CASControlState, daemonPubkey.Hex(), dTag)
			deletion := nostr.Event{
				Kind: nostr.KindDeletion,
				Tags: nostr.Tags{{"a", address}},
			}
			if err := signer.SignEvent(ctx, &deletion); err != nil {
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
