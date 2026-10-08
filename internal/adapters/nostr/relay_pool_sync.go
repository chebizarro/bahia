package nostr

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip77"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"go.uber.org/zap"
)

// Per-relay pool capabilities for inbound sync.
//
// SubscribeAllWithEOSE sends the same filters to every relay, which forces one
// `since` on all of them. Inbound sync keeps a cursor per (relay,
// filter) instead, so it needs a REQ on one relay at a time, and a NIP-77
// session against one relay at a time.

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
// connection. The caller owns the REQ's lifecycle (paging, cursors, resync),
// so unlike SubscribeWithOptions the pool does not supervise or reissue it.
// It uses the same primitives as the merged subscriptions:
//
//   - reconnectRelay dials the relay if needed, honouring its reconnect
//     backoff, and redials a websocket that died since the last use (the
//     pool now notices a dead socket itself);
//   - the REQ takes one of the relay's NIP-11 max_subscriptions slots, held
//     until ctx ends or the subscription does;
//   - NIP-42 is answered by the connection's AuthHandler; an "auth-required:"
//     CLOSED still ends the REQ, and the caller re-REQs after
//     AuthenticateRelay, which joins that AUTH attempt.
//
// filter.Limit is sent as given: the caller pages against relayPageLimit, and
// capping it here as well could make a full page look short. The subscription
// ends when ctx ends, the relay CLOSEs it, or the connection drops.
func (p *RelayPool) subscribeRelay(ctx context.Context, relayURL string, filter nostr.Filter) (*nostr.Subscription, error) {
	mr, err := p.managedRelayFor(relayURL)
	if err != nil {
		return nil, err
	}
	relay, err := p.reconnectRelay(ctx, mr)
	if err != nil {
		return nil, err
	}
	p.awaitRelayLimits(ctx, mr)
	release, err := p.acquireSubscriptionSlot(ctx, mr, func(limit int) {
		p.logger.Warn("relay subscription waits for a free NIP-11 max_subscriptions slot",
			zap.String("relay", mr.url), zap.Int("max_subscriptions", limit))
	})
	if err != nil {
		return nil, err
	}
	sub, err := subscribeOnRelay(relay, ctx, filter)
	if err != nil {
		release()
		p.markRelayDisconnectedIfDead(mr, relay)
		p.recordRelayError(mr.url, err.Error())
		return nil, err
	}
	p.recordRelayConnectionState(mr.url, true)
	var subDone <-chan struct{}
	if sub.Context != nil {
		subDone = sub.Context.Done()
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-subDone:
		}
		release()
	}()
	return sub, nil
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
// NIP-77 (fiatjaf.com/nostr/nip77.NegentropySyncWithOptions). Events the relay
// has and local lacks are fetched and published to local; with upload set,
// events local has and the relay lacks are published to the relay. It fails
// with the relay's NEG-ERR reason when the relay refuses the session (for
// example a set larger than it reconciles), with errNegentropyUnsupported when
// the relay's NIP-11 document omits NIP-77, and when timeout passes first (a
// relay that ignores NEG-OPEN never answers). Callers fall back to paged REQs.
//
// The session runs on its own short-lived connection, which nip77 dials and
// closes, with this pool's connection options: the pool's signer answers the
// relay's NIP-42 challenge, and an "auth-required:" NEG-ERR authenticates and
// re-opens the session once (third_party/nostr/BAHIA_PATCHES.md). The id
// fetches and uploads of a session use that authenticated connection too.
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
	err := nip77.NegentropySyncWithOptions(syncCtx, relayURL, filter, source, local, p.moveNegentropyItems, p.buildRelayOptions(relayURL))
	if err != nil && syncCtx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("negentropy session with %s timed out after %s: %w", relayURL, timeout, err)
	}
	return err
}

// maxNegentropyUploadChunk is how many events one paced admission operation
// declares for a NIP-77 upload. It matches the controller's per-operation
// ceiling; a larger reconcile is uploaded as consecutive operations, and a
// session whose context ends first simply resumes at the next one — NIP-77
// reconciliation is convergent.
const maxNegentropyUploadChunk = 2048

// moveNegentropyItems copies the events whose ids a NIP-77 session reports
// missing on one side from the other side. The download direction (To is the
// local target) publishes straight into the local pipeline; the upload
// direction (To is the session's raw relay connection) crosses the outbound
// admission gateway like every other EVENT frame.
func (p *RelayPool) moveNegentropyItems(ctx context.Context, dir nip77.Direction) {
	if relay, ok := dir.To.(*nostr.Relay); ok {
		p.uploadNegentropyItems(ctx, dir, relay)
		return
	}
	p.downloadNegentropyItems(ctx, dir)
}

// downloadNegentropyItems stores events the relay has and the local store
// lacks, in batches. Unlike nip77.SyncEventsFromIDs it returns when ctx ends:
// a refused or abandoned session never closes dir.Items. dir.To is the local
// sync target — never a relay — so no outbound frame is written here.
func (p *RelayPool) downloadNegentropyItems(ctx context.Context, dir nip77.Direction) {
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

// uploadNegentropyItems publishes local-only events to the session's relay
// through the outbound admission gateway: the missing ids are collected
// first, then uploaded as consecutive bounded bulk operations, so a large
// reconcile is paced by the bulk lane and can never burst the aggregate or
// the relay's wire budget. Every frame is charged immediately before it is
// written on the session connection, and every outcome feeds the shared
// breaker and receipt cache. The kill switch, an open circuit, and the
// session context interrupt the upload; the events stay local and the next
// session resumes where this one stopped.
func (p *RelayPool) uploadNegentropyItems(ctx context.Context, dir nip77.Direction, relay *nostr.Relay) {
	ids := make([]nostr.ID, 0, negentropyFetchBatch)
	seen := make(map[nostr.ID]struct{})
	drained := false
	for !drained {
		select {
		case <-ctx.Done():
			// The session ended — timeout, NEG-ERR, or caller cancellation;
			// negentropySyncRelay cancels the session context when it
			// returns — and the library closes Items only when a reconcile
			// completes. Ranging blindly would leak this handler on every
			// aborted session; the events stay local and the convergent
			// protocol resumes at the next session.
			return
		case id, ok := <-dir.Items:
			if !ok {
				drained = true
				break
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	for start := 0; start < len(ids); start += maxNegentropyUploadChunk {
		if ctx.Err() != nil {
			return
		}
		chunk := ids[start:min(start+maxNegentropyUploadChunk, len(ids))]
		op, err := p.outboundAdmission.BeginOperation(ctx, nostrout.OperationSpec{MaxEvents: len(chunk)})
		if err != nil {
			p.logger.Warn("negentropy upload refused by outbound admission",
				zap.String("relay", relay.URL),
				zap.Int("events", len(chunk)),
				zap.Error(err))
			return
		}
		p.uploadNegentropyChunk(ctx, op, dir.From, relay, chunk)
		op.Close()
	}
}

// uploadNegentropyChunk paces one declared operation's events through the
// gateway, querying the local side in fetch batches.
func (p *RelayPool) uploadNegentropyChunk(ctx context.Context, op *nostrout.Operation, from nostr.Querier, relay *nostr.Relay, chunk []nostr.ID) {
	for start := 0; start < len(chunk); start += negentropyFetchBatch {
		if ctx.Err() != nil {
			return
		}
		batch := chunk[start:min(start+negentropyFetchBatch, len(chunk))]
		for ev := range from.QueryEvents(nostr.Filter{IDs: batch}) {
			if err := p.publishAdmittedToSessionRelay(ctx, op, relay, ev); err != nil {
				if ctx.Err() != nil {
					return
				}
				p.logger.Debug("negentropy upload rejected",
					zap.String("relay", relay.URL),
					zap.String("event_id", ev.ID.Hex()),
					zap.Error(err))
			}
		}
	}
}

// publishAdmittedToSessionRelay writes one EVENT frame on a NIP-77 session
// connection under the operation's bulk permit and the relay's wire budget,
// with the same receipt and breaker accounting as a pool publication.
func (p *RelayPool) publishAdmittedToSessionRelay(ctx context.Context, op *nostrout.Operation, relay *nostr.Relay, ev nostr.Event) error {
	pub, err := op.Begin(ctx, ev, []string{relay.URL})
	if err != nil {
		return err
	}
	defer pub.Close()
	if !pub.NeedsRelay(relay.URL) {
		return nil // this relay already accepted this exact signed event
	}
	if err := pub.BeforeAttempt(ctx, relay.URL); err != nil {
		return err
	}
	err = publishOnRelay(relay, ctx, ev)
	pub.Observe(nostrout.ResultFromPublishError(relay.URL, err))
	return err
}
