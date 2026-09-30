package nostr

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip77"
	"go.uber.org/zap"
)

// Per-relay pool capabilities for inbound sync (bahia-irsry.10.1).
//
// SubscribeAllWithEOSE sends the same filters to every relay, which forces one
// `since` on all of them (C-23). Inbound sync keeps a cursor per (relay,
// filter) instead, so it needs a REQ on one relay at a time, and a NIP-77
// session against one relay at a time. These live in their own file so the
// merged-subscription code in relay_pool.go is untouched.

// errRelayNotInPool reports a relay the pool no longer manages; a per-relay
// sync worker stops for good when it sees it.
var errRelayNotInPool = errors.New("relay is not in the pool")

// errNegentropyUnsupported reports a relay whose NIP-11 document does not list
// NIP-77, so no session is attempted.
var errNegentropyUnsupported = errors.New("relay does not advertise NIP-77")

// negentropyFetchBatch is how many ids one download (or upload) query asks
// for, matching nip77.SyncEventsFromIDs.
const negentropyFetchBatch = 50

// subscribeRelay opens one REQ for filter on one relay over the pool's managed
// connection, dialling (subject to the relay's reconnect backoff) if it is not
// connected, and answering an auth-required refusal with NIP-42 AUTH once.
// The subscription ends when ctx ends, the relay CLOSEs it, or the connection
// drops.
func (p *RelayPool) subscribeRelay(ctx context.Context, relayURL string, filter nostr.Filter) (*nostr.Subscription, error) {
	mr, err := p.managedRelayFor(relayURL)
	if err != nil {
		return nil, err
	}
	relay, err := p.ensureRelayConnected(ctx, mr, p.reconnectTimeout, true)
	if err != nil {
		return nil, err
	}
	if !relay.IsConnected() {
		// The socket dropped since the last dial; only publish errors mark a
		// relay disconnected, so notice the drop here and dial again.
		p.markRelayDropped(mr, relay)
		if _, err := p.ensureRelayConnected(ctx, mr, p.reconnectTimeout, true); err != nil {
			return nil, err
		}
	}
	subs, err := p.subscribeConnectedRelay(ctx, ctx, mr, []nostr.Filter{filter})
	if err != nil {
		return nil, err
	}
	return subs[0].sub, nil
}

// managedRelayFor returns the pool's managed relay for a configured URL,
// registering it the way Connect does if the pool has not dialled it yet.
func (p *RelayPool) managedRelayFor(relayURL string) (*managedRelay, error) {
	normalized := nostr.NormalizeURL(relayURL)
	p.mu.RLock()
	mr := p.relays[normalized]
	p.mu.RUnlock()
	if mr != nil {
		return mr, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if mr = p.relays[normalized]; mr != nil {
		return mr, nil
	}
	for _, configured := range p.urls {
		if configured == normalized {
			mr = &managedRelay{url: normalized}
			p.relays[normalized] = mr
			return mr, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", errRelayNotInPool, relayURL)
}

func (p *RelayPool) markRelayDropped(mr *managedRelay, relay *nostr.Relay) {
	mr.mu.Lock()
	dropped := mr.relay == relay && mr.connected
	if dropped {
		mr.connected = false
		mr.lastErr = errors.New("relay connection closed")
	}
	mr.mu.Unlock()
	if dropped {
		p.recordRelayConnectionState(mr.url, false)
	}
}

// relayPageLimit returns the largest `limit` worth asking relayURL for: want,
// lowered to the relay's advertised NIP-11 max_limit. Asking for more than a
// relay serves would make a truncated page look complete.
func (p *RelayPool) relayPageLimit(relayURL string, want int) int {
	if served := p.GetMaxLimit(relayURL); served > 0 && served < want {
		return served
	}
	return want
}

// negentropySyncRelay reconciles filter between local and one relay with
// NIP-77 (fiatjaf.com/nostr/nip77.NegentropySync). Events the relay has and
// local lacks are fetched and published to local; with upload set, events
// local has and the relay lacks are published to the relay. It fails with the
// relay's NEG-ERR reason when the relay refuses the session (for example a set
// larger than it reconciles), with errNegentropyUnsupported when the relay's
// NIP-11 document omits NIP-77, and when timeout passes first (a relay that
// ignores NEG-OPEN never answers). Callers fall back to paged REQs.
//
// The session runs on its own connection, which nip77 dials and (with the
// Bahia patch) closes; it does not answer NIP-42 AUTH, so an auth-required
// refusal also falls back to the pool's authenticated REQ path.
func (p *RelayPool) negentropySyncRelay(ctx context.Context, relayURL string, filter nostr.Filter, local nostr.QuerierPublisher, upload bool, timeout time.Duration) error {
	if p.GetRelayInfo(relayURL) != nil && !p.SupportsNIP(relayURL, 77) {
		return errNegentropyUnsupported
	}
	syncCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var source nostr.Querier
	if upload {
		source = local
	}
	err := nip77.NegentropySync(syncCtx, relayURL, filter, source, local, p.moveNegentropyItems)
	if err != nil && syncCtx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("negentropy session with %s timed out after %s: %w", relayURL, timeout, err)
	}
	return err
}

// moveNegentropyItems copies the events whose ids a NIP-77 session reports
// missing on one side from the other side, in batches. Unlike
// nip77.SyncEventsFromIDs it returns when ctx ends: a refused or abandoned
// session never closes dir.Items.
func (p *RelayPool) moveNegentropyItems(ctx context.Context, dir nip77.Direction) {
	batch := make([]nostr.ID, 0, negentropyFetchBatch)
	seen := make(map[nostr.ID]struct{})
	flush := func() {
		if len(batch) == 0 {
			return
		}
		for ev := range dir.From.QueryEvents(nostr.Filter{IDs: batch}) {
			if err := dir.To.Publish(ctx, ev); err != nil && ctx.Err() == nil {
				p.logger.Debug("negentropy transfer rejected",
					zap.String("event_id", ev.ID.Hex()),
					zap.Error(err))
			}
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			return
		case id, ok := <-dir.Items:
			if !ok {
				flush()
				return
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			batch = append(batch, id)
			if len(batch) == negentropyFetchBatch {
				flush()
			}
		}
	}
}
