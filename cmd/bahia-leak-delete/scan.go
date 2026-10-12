package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

type scanOptions struct {
	oldPubkey nostr.PubKey
	// pageLimit is the REQ limit of each page.
	pageLimit int
	// pageTimeout bounds the wait for one page's EOSE; a page that runs out
	// of time leaves the relay's scan incomplete, never complete.
	pageTimeout time.Duration
	// concurrency bounds how many relays are scanned at once.
	concurrency int
}

// scanRelays scans every relay, at most opts.concurrency at a time, and
// returns one result per relay (in relays order) and the fingerprints of every
// d value seen. Relay-supplied reasons are redacted against those
// fingerprints before they are returned.
func scanRelays(ctx context.Context, pool *nostradapter.RelayPool, relays []string, opts scanOptions) ([]relayScan, fingerprintSet) {
	results := make([]relayScan, len(relays))
	sets := make([]fingerprintSet, len(relays))
	slots := make(chan struct{}, opts.concurrency)
	var wg sync.WaitGroup
	for i, relay := range relays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				results[i], sets[i] = scanRelay(ctx, pool, relay, opts)
			case <-ctx.Done():
				results[i] = relayScan{Relay: relay, Status: scanIncomplete, Reason: ctx.Err().Error(), ScannedAt: time.Now().UTC()}
			}
		}()
	}
	wg.Wait()
	dValues := fingerprintSet{}
	for _, set := range sets {
		for fp := range set {
			dValues[fp] = struct{}{}
		}
	}
	for i := range results {
		results[i].Reason = dValues.redact(results[i].Reason)
	}
	return results, dValues
}

// scanRelay collects every leaked event one relay holds for the old identity
// with REQ {kinds, authors:[old], limit, until} pages. Each page is complete
// only at the relay's EOSE. The next page is bounded by the oldest created_at
// seen (until is inclusive; seen ids are dropped), so a relay that silently
// caps limit below the page size is still paged to the end. The scan ends at
// an empty page, or at a page of already-seen events smaller than the largest
// page the relay returned. A page of only already-seen events as large as the
// largest page means more events share one second than the relay returns at
// once; the scan steps past that second and records Truncated.
func scanRelay(ctx context.Context, pool *nostradapter.RelayPool, relay string, opts scanOptions) (relayScan, fingerprintSet) {
	scan := relayScan{Relay: relay, ScannedAt: time.Now().UTC()}
	dValues := fingerprintSet{}
	seen := make(map[nostr.ID]scannedEvent)
	var invalid atomic.Int64
	accept := func(ev *nostr.Event) bool {
		if ev.PubKey == opts.oldPubkey && isLeakedKind(ev.Kind) && ev.CheckID() && ev.VerifySignature() {
			return true
		}
		invalid.Add(1)
		return false
	}
	var until nostr.Timestamp
	largestPage := 0
	for {
		filter := nostr.Filter{Kinds: leakedKinds, Authors: []nostr.PubKey{opts.oldPubkey}, Limit: opts.pageLimit, Until: until}
		page, err := fetchStoredPage(ctx, pool, relay, filter, opts.pageTimeout, accept)
		scan.Pages++
		if err != nil {
			scan.Status, scan.Reason = scanIncomplete, err.Error()
			break
		}
		fresh := 0
		var oldest nostr.Timestamp
		for _, ev := range page {
			if until != 0 && ev.CreatedAt > until {
				err = fmt.Errorf("relay returned an event newer than until=%d", until)
				break
			}
			for _, tag := range ev.Tags {
				if len(tag) >= 2 && tag[0] == "d" {
					dValues.add(tag[1])
				}
			}
			if oldest == 0 || ev.CreatedAt < oldest {
				oldest = ev.CreatedAt
			}
			if _, dup := seen[ev.ID]; !dup {
				seen[ev.ID] = scannedEvent{ID: ev.ID.Hex(), Kind: int(ev.Kind), CreatedAt: int64(ev.CreatedAt)}
				fresh++
			}
		}
		if err != nil {
			scan.Status, scan.Reason = scanIncomplete, err.Error()
			break
		}
		if len(page) == 0 {
			scan.Status = scanComplete
			break
		}
		if fresh == 0 {
			// Every event here was already seen, so all of them share the
			// second until.
			if len(page) < largestPage || until <= 1 {
				scan.Status = scanComplete
				break
			}
			scan.Truncated = true
			until--
			continue
		}
		largestPage = max(largestPage, len(page))
		until = oldest
	}
	scan.Invalid = int(invalid.Load())
	scan.Events = make([]scannedEvent, 0, len(seen))
	for _, ev := range seen {
		scan.Events = append(scan.Events, ev)
	}
	sort.Slice(scan.Events, func(i, j int) bool {
		a, b := scan.Events[i], scan.Events[j]
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt > b.CreatedAt
		}
		return a.ID < b.ID
	})
	return scan, dValues
}

// fetchStoredPage runs one REQ on one relay until its EOSE and returns the
// events accept admitted. It closes the REQ at EOSE: a scan reads stored
// history only, and the next page is a new REQ with a lower until. NIP-42
// AUTH challenges and retryable CLOSEDs are answered by the pool; a CLOSED
// that ends the REQ, a dropped stream or the page timeout makes the page an
// error, never a complete answer.
func fetchStoredPage(ctx context.Context, pool *nostradapter.RelayPool, relay string, filter nostr.Filter, timeout time.Duration, accept func(*nostr.Event) bool) ([]*nostr.Event, error) {
	pageCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sub, err := pool.SubscribeWithOptions(pageCtx, []nostr.Filter{filter}, nostradapter.SubscribeOptions{
		Relays:                 []string{relay},
		AwaitUnavailableRelays: true,
		ValidateEvent:          accept,
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}
	defer sub.Close()
	var events []*nostr.Event
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				if err := sub.StoredEventsIncomplete(nil); err != nil {
					return nil, err
				}
				return nil, errors.New("event stream ended before EOSE")
			}
			events = append(events, ev)
		case <-sub.EndOfStoredEvents:
			if err := sub.StoredEventsIncomplete(nil); err != nil {
				return nil, err
			}
			// The pool forwards a relay's buffered EVENTs before it records
			// that relay's EOSE; take the ones still queued.
			for {
				select {
				case ev, ok := <-sub.Events:
					if !ok {
						return events, nil
					}
					events = append(events, ev)
				default:
					return events, nil
				}
			}
		case <-pageCtx.Done():
			return nil, sub.StoredEventsIncomplete(pageCtx.Err())
		}
	}
}
