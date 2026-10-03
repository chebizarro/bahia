package client

import (
	"context"
	"fmt"
	"sort"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// SyncAndQueryWorkerFamilies catches up the four canonical worker read-model
// families as one service-author subscription and reuses its persisted cursor.
func (c *NostrClient) SyncAndQueryWorkerFamilies(ctx context.Context) ([]nostr.Event, *SyncResult, error) {
	topics := []string{kinds.WorkerStateTopic, kinds.WorkerAssignmentTopic, kinds.WorkerDrainTopic, kinds.WorkerEligibilityTopic}
	result, err := c.syncTopics(ctx, kinds.WorkerDomain, topics)
	if err != nil {
		return nil, nil, err
	}
	events, err := c.queryTopics(topics)
	return events, result, err
}

// SyncAndQueryWorkerAdvertisements subscribes only to the supplied worker
// authors, not the Bahia service author. These keys come from canonical worker
// state or an explicit worker-get pubkey; no unscoped relay scan is performed.
func (c *NostrClient) SyncAndQueryWorkerAdvertisements(ctx context.Context, authors []string) ([]nostr.Event, *SyncResult, error) {
	if len(authors) == 0 {
		return []nostr.Event{}, &SyncResult{Fresh: true}, nil
	}
	seen := make(map[string]struct{}, len(authors))
	parsed := make([]nostr.PubKey, 0, len(authors))
	for _, raw := range authors {
		pub, err := nostr.PubKeyFromHex(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid worker pubkey %q: %w", raw, err)
		}
		if _, ok := seen[pub.Hex()]; ok {
			continue
		}
		seen[pub.Hex()] = struct{}{}
		parsed = append(parsed, pub)
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].Hex() < parsed[j].Hex() })
	filter := buildAuthorFilter(kinds.LoomWorkerAdvertisement, parsed, nil)
	valid := func(ev nostr.Event) bool {
		if ev.Kind != nostr.Kind(kinds.LoomWorkerAdvertisement) || !ev.CheckID() || !ev.VerifySignature() {
			return false
		}
		_, ok := seen[ev.PubKey.Hex()]
		return ok
	}
	result, err := c.syncFilter(ctx, "worker-advertisement", filter, valid)
	if err != nil {
		return nil, nil, err
	}
	events := make([]nostr.Event, 0)
	for ev := range c.store.QueryEvents(filter) {
		if valid(ev) {
			events = append(events, ev)
		}
	}
	return events, result, nil
}

// buildAuthorFilter scopes a family to its actual authors. Control state uses
// the Bahia service key; worker advertisements use worker keys instead.
func buildAuthorFilter(kind int, authors []nostr.PubKey, topics []string) nostr.Filter {
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kind)}, Authors: authors}
	if len(topics) > 0 {
		filter.Tags = nostr.TagMap{"t": topics}
	}
	return filter
}
