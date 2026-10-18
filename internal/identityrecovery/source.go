package identityrecovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/relayguard"
)

type RelaySource struct {
	Pool   *adapter.RelayPool
	Relays []string
}

func (s RelaySource) Read(ctx context.Context, filter nostr.Filter) ([]nostr.Event, error) {
	seen := map[nostr.ID]nostr.Event{}
	filters := []nostr.Filter{filter}
	if len(filter.IDs) > 64 {
		filters = nil
		for start := 0; start < len(filter.IDs); start += 64 {
			batch := filter
			batch.IDs = filter.IDs[start:min(start+64, len(filter.IDs))]
			filters = append(filters, batch)
		}
	}
	for _, url := range s.Relays {
		for _, batch := range filters {
			events, err := scanRecoveryRelay(ctx, s.Pool, url, batch)
			if err != nil {
				return nil, fmt.Errorf("audited relay history incomplete: %w", err)
			}
			for _, ev := range events {
				seen[ev.ID] = ev
			}
		}
	}
	out := make([]nostr.Event, 0, len(seen))
	for _, ev := range seen {
		out = append(out, ev)
	}
	slices.SortFunc(out, func(a, b nostr.Event) int { return slices.Compare(a.ID[:], b.ID[:]) })
	return out, nil
}

// An IDs filter can be subdivided when a relay cannot page within one
// created_at second; every requested ID remains directly addressable.
func scanRecoveryRelay(ctx context.Context, pool *adapter.RelayPool, relay string, filter nostr.Filter) ([]nostr.Event, error) {
	events, _, err := pool.ScanStoredPages(ctx, relay, filter)
	if !errors.Is(err, adapter.ErrStoredHistoryBoundary) || len(filter.IDs) < 2 {
		return events, err
	}
	left, right := filter, filter
	left.IDs = filter.IDs[:len(filter.IDs)/2]
	right.IDs = filter.IDs[len(filter.IDs)/2:]
	leftEvents, leftErr := scanRecoveryRelay(ctx, pool, relay, left)
	rightEvents, rightErr := scanRecoveryRelay(ctx, pool, relay, right)
	return append(leftEvents, rightEvents...), errors.Join(leftErr, rightErr)
}
func (s RelaySource) Publish(ctx context.Context, ev nostr.Event) error {
	results, err := s.Pool.PublishToRelaysWithResults(ctx, ev, s.Relays)
	if err != nil {
		if len(results) > 0 {
			return fmt.Errorf("recovery publish failed: %s", classifyRelayReason(results[0].Reason, "transport"))
		}
		return errors.New("recovery publish failed")
	}
	if len(results) != len(s.Relays) {
		return errors.New("recovery publish omitted controlled relay")
	}
	want := make(map[string]struct{}, len(s.Relays))
	for _, relay := range s.Relays {
		want[relayguard.Normalize(relay)] = struct{}{}
	}
	for _, r := range results {
		if _, ok := want[relayguard.Normalize(r.RelayURL)]; !ok {
			return errors.New("recovery publish result outside controlled relays")
		}
		delete(want, relayguard.Normalize(r.RelayURL))
		if !r.Succeeded() {
			return fmt.Errorf("controlled relay %s did not acknowledge recovery record", r.RelayURL)
		}
	}
	if len(want) != 0 {
		return errors.New("recovery publish omitted controlled relay")
	}
	return nil
}

type LocalStoreSource struct{ Path string }

func (s LocalStoreSource) Events(ctx context.Context, pub nostr.PubKey) ([]nostr.Event, error) {
	if _, err := os.Stat(s.Path); err != nil {
		return nil, errors.New("daemon local store file missing")
	}
	store, err := localstore.Open(s.Path)
	if err != nil {
		return nil, errors.New("daemon local store unavailable (run operator recovery with daemon stopped)")
	}
	defer func() { _ = store.Close() }()
	var out []nostr.Event
	for ev := range store.QueryEvents(nostr.Filter{Authors: []nostr.PubKey{pub}}) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func (s LocalStoreSource) StoreEvent(ctx context.Context, ev nostr.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ev.CheckID() || !ev.VerifySignature() {
		return errors.New("invalid recovery event for local store")
	}
	if _, err := os.Stat(s.Path); err != nil {
		return errors.New("daemon local store file missing")
	}
	store, err := localstore.Open(s.Path)
	if err != nil {
		return errors.New("daemon local store unavailable (run operator recovery with daemon stopped)")
	}
	defer func() { _ = store.Close() }()
	_, err = store.SaveEvent(ev)
	return err
}
