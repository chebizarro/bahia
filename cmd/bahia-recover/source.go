package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
)

type relaySource struct {
	pool   *adapter.RelayPool
	relays []string
}

func (s relaySource) Read(ctx context.Context, filter nostr.Filter) ([]nostr.Event, error) {
	seen := map[nostr.ID]nostr.Event{}
	for _, url := range s.relays {
		sub, err := s.pool.SubscribeWithOptions(ctx, []nostr.Filter{filter}, adapter.SubscribeOptions{Relays: []string{url}, AwaitUnavailableRelays: true, ValidateEvent: func(ev *nostr.Event) bool { return ev.CheckID() && ev.VerifySignature() }})
		if err != nil {
			return nil, errors.New("audited relay subscription failed")
		}
		complete := false
		for !complete {
			select {
			case ev, ok := <-sub.Events:
				if !ok {
					sub.Close()
					return nil, errors.New("relay stream closed before EOSE")
				}
				seen[ev.ID] = *ev
			case <-sub.EndOfStoredEvents:
				if err := sub.StoredEventsIncomplete(nil); err != nil {
					sub.Close()
					return nil, errors.New("audited relay history incomplete")
				}
				for {
					select {
					case ev, ok := <-sub.Events:
						if ok {
							seen[ev.ID] = *ev
						}
					default:
						complete = true
					}
				}
			case <-ctx.Done():
				sub.Close()
				return nil, ctx.Err()
			}
		}
		if !sub.AllRelaysReachedEOSE() {
			sub.Close()
			return nil, errors.New("audited relay did not reach EOSE")
		}
		sub.Close()
	}
	out := make([]nostr.Event, 0, len(seen))
	for _, ev := range seen {
		out = append(out, ev)
	}
	slices.SortFunc(out, func(a, b nostr.Event) int { return slices.Compare(a.ID[:], b.ID[:]) })
	return out, nil
}
func (s relaySource) Publish(ctx context.Context, ev nostr.Event) error {
	results, err := s.pool.PublishToRelaysWithResults(ctx, ev, s.relays)
	if err != nil {
		return errors.New("recovery publish failed")
	}
	if len(results) != len(s.relays) {
		return errors.New("recovery publish omitted controlled relay")
	}
	for _, r := range results {
		if !r.Succeeded() {
			return fmt.Errorf("controlled relay %s did not acknowledge recovery record", r.RelayURL)
		}
	}
	return nil
}

type localSource struct{ path string }

func (s localSource) Events(ctx context.Context, pub nostr.PubKey) ([]nostr.Event, error) {
	if _, err := os.Stat(s.path); err != nil {
		return nil, errors.New("daemon local store file missing")
	}
	store, err := localstore.Open(s.path)
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
