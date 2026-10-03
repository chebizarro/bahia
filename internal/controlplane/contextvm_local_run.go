package controlplane

// ContextVM requests over the daemon's local store (bahia-irsry.10.6, audit
// C-14, B-25).
//
// Each configured relay gets its own request subscription and its own cursor
// in the local store. Every request is claimed in a local ledger before its
// handler runs, so neither restart idempotency nor downtime recovery depends
// on Postgres.
//
// Cursor. The committed cursor for a relay is a wall-clock time T such that
// every event that relay received before T has been processed. It is written
// only when this transport's own REQ to that relay sends EOSE, and the value
// written is the time taken just before that REQ was opened. EOSE proves the
// relay has answered with everything it stored when the REQ arrived; anything
// later is delivered live. Live events never write "now". The pool reissues a
// REQ transparently after a dropped connection, and the transport cannot tell
// that reissue's backfill from live delivery, so "now" could skip a backfill
// that a crash interrupted. Instead, a live event that finds the cursor older
// than a configurable age. When the pool reissues the REQ after a reconnect,
// the reissued REQ's EOSE carries a ReissuedAt timestamp that is committed as
// a cursor, keeping it fresh without a separate re-anchor loop.
//
// Backdating. NIP-59 lets a sender randomize a wrap's outer created_at up to
// two days into the past, so the outer timestamp says almost nothing about
// when the wrap was published. A wrap published at or after T has an outer
// created_at of at least T - 48h, less the sender's clock skew. Resuming with
// since = T - contextVMWrapBackdateOverlap (49h: 48h plus an hour for skew and
// rounding) therefore never skips one. For the same reason the cursor is never
// derived from outer created_at values, and the 2-minute inner-event window of
// a store-less transport does not apply here.
//
// Age floor. A request whose own created_at is before the floor is dropped
// unexecuted. The floor is the later of now - contextVMRequestMaxAge and the
// ledger epoch less contextVMColdLedgerGrace. The epoch rule matters on first
// start after an upgrade, or after the store was wiped: the ledger has no
// record of what the previous process executed, so requests created before it
// existed are not replayed (as before, apart from the grace). Ledger entries
// are pruned once they are older than both the floor and the resume window,
// so a pruned entry can never be needed again. Since never goes earlier than
// floor - 49h, which bounds the replay after long downtime.
//
// Dedup. Three keys, all in one bbolt transaction with the claim:
//   - The delivered event id (the outer wrap). A relay replay or a second
//     relay's copy is skipped silently. Every event on the subscription is
//     marked, including responses and rejected requests, so a restart
//     neither re-dispatches nor re-answers them.
//   - The request event id (the inner id). A re-wrapped copy of a handled
//     request is never executed again.
//   - The idempotency key (requester, method, progressToken), bound to a
//     params fingerprint. A new request reusing a key is never executed
//     again either; reusing it with other params is a conflict.
//
// A completed keyed request's terminal response is kept, saved before it is
// published, and replayed with the retry's own JSON-RPC id. An unkeyed request
// keeps no response (responses can carry revealed secrets). Its retry, and a
// retry of a request whose claim never completed (crash, or another process
// still running it), gets ContextVMDuplicateRequestErrorCode. At most once
// beats a blind re-run of a side effect. Processing is serialized across
// relays, as the single merged subscription was.
//
// Stored vs ephemeral. Only stored 1059 wraps can be recovered after downtime.
// Plaintext 25910 and the oversized-request 21059 fallback are ephemeral:
// relays forward them live and never store them, so a request sent while the
// daemon is down is lost and the client must retry. Durable desired-state
// intents are Phase 3 (bahia-irsry.11). This transport does not pretend to
// make ephemeral RPC durable.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"go.uber.org/zap"
)

const (
	// ContextVMDuplicateRequestErrorCode answers a retry of a request that
	// was already accepted when its response cannot be replayed: the
	// request had no idempotency key, or its outcome is unknown because its
	// execution never completed.
	ContextVMDuplicateRequestErrorCode = -32011

	// contextVMDefaultWrapBackdateOverlap: NIP-59's two-day outer backdating
	// plus an hour for clock skew (InboundEventMaxFutureSkew is 10 minutes)
	// and second rounding. Overridden by ContextVMLocalConfig.WrapBackdateOverlap.
	contextVMDefaultWrapBackdateOverlap = 49 * time.Hour
	// contextVMDefaultRequestMaxAge is how old a request may be and still
	// run, and so how long the ledger remembers one. Overridden by
	// ContextVMLocalConfig.RequestMaxAge.
	contextVMDefaultRequestMaxAge = 7 * 24 * time.Hour
	// contextVMColdLedgerGrace lets a new ledger accept requests created
	// this long before it, matching the replay window of the transport it
	// replaces.
	contextVMColdLedgerGrace = encryptedRequestReplayLookback
	// contextVMLedgerPruneInterval spaces ledger pruning, which runs at
	// start and after EOSE commits.
	contextVMLedgerPruneInterval = 6 * time.Hour
)

// errContextVMLedger marks local-store failures, which stop the transport:
// without the ledger a request can be neither safely run nor safely skipped.
var errContextVMLedger = errors.New("ContextVM request ledger")

var errContextVMRelayEnded = errors.New("relay subscription ended")

// contextVMRelaySubscriber is the per-relay subscription surface of the shared
// RelayPool, which also owns NIP-42 AUTH and REQ reissue.
type contextVMRelaySubscriber interface {
	URLs() []string
	SubscribeWithOptions(context.Context, []nostr.Filter, nostrpool.SubscribeOptions) (*nostrpool.MergedSubscription, error)
}

type contextVMLocalState struct {
	store *localstore.Store
	// epoch is the ledger's creation time, read before the followers start.
	epoch time.Time
	// processMu serializes event processing across relays: a delivery is
	// checked, claimed, handled and marked before the next one starts.
	processMu sync.Mutex
	lastPrune time.Time // guarded by processMu
	// requestMaxAge overrides contextVMDefaultRequestMaxAge.
	requestMaxAge time.Duration
	// wrapBackdateOverlap overrides contextVMDefaultWrapBackdateOverlap.
	wrapBackdateOverlap time.Duration
	// onCaughtUp, when set, observes each EOSE cursor commit (tests).
	onCaughtUp func(relayURL string, cursor nostr.Timestamp)
}

func (l *contextVMLocalState) maxAge() time.Duration {
	if l.requestMaxAge > 0 {
		return l.requestMaxAge
	}
	return contextVMDefaultRequestMaxAge
}

func (l *contextVMLocalState) backdateOverlap() time.Duration {
	if l.wrapBackdateOverlap > 0 {
		return l.wrapBackdateOverlap
	}
	return contextVMDefaultWrapBackdateOverlap
}

// innerFloor is the oldest request created_at that may still run.
func (l *contextVMLocalState) innerFloor(now time.Time) nostr.Timestamp {
	floor := now.Add(-l.maxAge())
	if cold := l.epoch.Add(-contextVMColdLedgerGrace); cold.After(floor) {
		floor = cold
	}
	return nostr.Timestamp(floor.Unix())
}

// pruneLocked drops ledger entries older than anything the floor accepts or a
// resume can fetch. The caller holds processMu.
func (l *contextVMLocalState) pruneLocked(now time.Time, logger *zap.Logger) {
	if !l.lastPrune.IsZero() && now.Sub(l.lastPrune) < contextVMLedgerPruneInterval {
		return
	}
	l.lastPrune = now
	cutoff := now.Add(-(l.maxAge() + l.backdateOverlap() + time.Hour))
	removed, err := l.store.PruneContextVMLedger(cutoff)
	if err != nil {
		logger.Warn("prune ContextVM request ledger failed", zap.Error(err))
		return
	}
	if removed > 0 {
		logger.Info("pruned ContextVM request ledger", zap.Int("removed", removed), zap.Time("cutoff", cutoff))
	}
}

func contextVMRequestFilter(servicePubkey string, since nostr.Timestamp) nostr.Filter {
	return nostr.Filter{
		Kinds: []nostr.Kind{KindContextVMMessage, KindContextVMGiftWrap, KindContextVMEphemeralWrap},
		Tags:  nostr.TagMap{tagRecipientPubkey: []string{servicePubkey}},
		Since: since,
	}
}

// runLocalContextVM keeps one request follower per configured relay. A
// follower that stops (a terminal CLOSED, or its relay left the pool) is
// restarted when a relay reconnects, if its relay is still configured; only a
// ledger failure stops the transport.
func (t *EncryptedRequestTransport) runLocalContextVM(ctx context.Context) error {
	pool, ok := t.subscriber.(contextVMRelaySubscriber)
	if !ok {
		// One persisted dedup layer only: a subscriber that already filters
		// deliveries through its own store (StoreBackedSubscriber) cannot be
		// combined with this ledger, which owns the REQs and cursors itself.
		return fmt.Errorf("ContextVM local store needs the relay pool itself (URLs, SubscribeWithOptions), got %T; use one store layer, not both", t.subscriber)
	}
	servicePubkey := t.responder.ServicePubkey()
	if servicePubkey == "" {
		return errors.New("ContextVM local store needs a service pubkey")
	}
	local := &t.contextVMLocal
	now := time.Now()
	epoch, err := local.store.ContextVMLedgerEpoch(now)
	if err != nil {
		return fmt.Errorf("%w: %w", errContextVMLedger, err)
	}
	local.epoch = epoch
	local.processMu.Lock()
	local.pruneLocked(now, t.logger)
	local.processMu.Unlock()

	// Register before the first URL snapshot, so a relay that connects in
	// between still triggers a refresh.
	var connected <-chan struct{}
	if notifier, ok := t.subscriber.(interface {
		NotifyRelayConnected() (<-chan struct{}, func())
	}); ok {
		updates, unregister := notifier.NotifyRelayConnected()
		defer unregister()
		connected = updates
	}

	ctx, cancel := context.WithCancel(ctx)
	type follower struct {
		id     uint64
		cancel context.CancelFunc
	}
	type stopped struct {
		relayURL string
		id       uint64
		err      error
	}
	var (
		wg      sync.WaitGroup
		nextID  uint64
		running = make(map[string]follower)
		results = make(chan stopped)
	)
	defer func() {
		cancel()
		wg.Wait()
	}()
	refresh := func() {
		wanted := make(map[string]struct{})
		for _, relayURL := range pool.URLs() {
			wanted[relayURL] = struct{}{}
		}
		for relayURL, f := range running {
			if _, ok := wanted[relayURL]; !ok {
				f.cancel()
				delete(running, relayURL)
			}
		}
		for relayURL := range wanted {
			if _, ok := running[relayURL]; ok {
				continue
			}
			nextID++
			followCtx, followCancel := context.WithCancel(ctx)
			running[relayURL] = follower{id: nextID, cancel: followCancel}
			f := &contextVMRelayFollower{t: t, pool: pool, relayURL: relayURL, servicePubkey: servicePubkey}
			wg.Add(1)
			go func(id uint64) {
				defer wg.Done()
				defer followCancel()
				err := f.run(followCtx)
				select {
				case results <- stopped{relayURL: f.relayURL, id: id, err: err}:
				case <-ctx.Done():
				}
			}(nextID)
		}
	}
	refresh()
	if len(running) == 0 {
		return errors.New("ContextVM request pool has no relays")
	}
	t.logger.Info("following ContextVM requests per relay on the local store", zap.Int("relays", len(running)), zap.Time("ledger_epoch", epoch))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-connected:
			if !ok {
				connected = nil
				continue
			}
			refresh()
		case result := <-results:
			if f, ok := running[result.relayURL]; ok && f.id == result.id {
				delete(running, result.relayURL)
			}
			if errors.Is(result.err, errContextVMLedger) {
				return result.err
			}
			if ctx.Err() == nil && !errors.Is(result.err, context.Canceled) {
				t.logger.Warn("ContextVM request follower stopped; it restarts when a relay reconnects", zap.String("relay", result.relayURL), zap.Error(result.err))
			}
		}
	}
}

// processContextVMDelivery handles one event from a request subscription
// unless it was processed before, then marks it processed.
func (t *EncryptedRequestTransport) processContextVMDelivery(ctx context.Context, ev *nostr.Event) error {
	if ev == nil {
		return nil
	}
	// The pool verified the signature; the id must match too before it is
	// trusted as a ledger key, or a relay could shadow a genuine event.
	if !ev.CheckID() {
		t.logger.Warn("ContextVM event id does not match its content", zap.String("event_id", ev.ID.Hex()))
		return nil
	}
	local := &t.contextVMLocal
	local.processMu.Lock()
	defer local.processMu.Unlock()
	deliveryID := ev.ID.Hex()
	delivered, err := local.store.ContextVMDelivered(deliveryID)
	if err != nil {
		return fmt.Errorf("%w: %w", errContextVMLedger, err)
	}
	if delivered {
		t.logger.Debug("ContextVM delivery already processed", zap.String("event_id", deliveryID), zap.Int("kind", int(ev.Kind)))
		return nil
	}
	now := time.Now()
	t.handleEventSince(ctx, ev, local.innerFloor(now))
	if err := local.store.MarkContextVMDelivery(deliveryID, now); err != nil {
		return fmt.Errorf("%w: %w", errContextVMLedger, err)
	}
	return nil
}

// claimLocalContextVMRequest records this delivery of an authorized, routed
// request and reports whether its handler should run. Every other outcome is
// answered here, except a redelivery, which was answered the first time.
func (t *EncryptedRequestTransport) claimLocalContextVMRequest(ctx context.Context, outer, inner *nostr.Event, encrypted bool, rpc ContextVMJSONRPCRequest, progressToken, fingerprint string) bool {
	innerID := inner.ID.Hex()
	innerPubkey := inner.PubKey.Hex()
	key := ""
	if progressToken != "" {
		key = contextVMCacheKey(innerPubkey, rpc.Method, progressToken)
	}
	claim, err := t.contextVMLocal.store.ClaimContextVMRequest(localstore.ContextVMRequest{
		DeliveryID:  outer.ID.Hex(),
		RequestID:   innerID,
		Key:         key,
		Fingerprint: fingerprint,
		CreatedAt:   inner.CreatedAt,
	}, time.Now())
	if err != nil {
		t.logger.Error("record ContextVM request failed; not executing it", zap.String("event_id", innerID), zap.String("method", rpc.Method), zap.Error(err))
		t.publishContextVMResponse(ctx, outer, inner, encrypted, cascontextvm.NewErrorResponse(rpc.ID, cascontextvm.InternalErrorCode, "request could not be recorded and was not executed"), rpc.Method)
		return false
	}
	fields := []zap.Field{zap.String("event_id", innerID), zap.String("delivery_id", outer.ID.Hex()), zap.String("method", rpc.Method), zap.String("requester_pubkey_prefix", pubkeyPrefix(innerPubkey))}
	switch claim.State {
	case localstore.ContextVMClaimed:
	case localstore.ContextVMRedelivered:
		t.logger.Debug("ContextVM request delivery already processed", fields...)
		return false
	case localstore.ContextVMKeyConflict:
		t.logger.Warn("ContextVM idempotency key reused with different request parameters", fields...)
		t.publishContextVMResponse(ctx, outer, inner, encrypted, cascontextvm.NewErrorResponse(rpc.ID, cascontextvm.InvalidRequestCode, "idempotency key was already used with different request parameters"), rpc.Method)
		return false
	case localstore.ContextVMPending:
		t.logger.Warn("ContextVM request retried while its first execution has no recorded outcome; not executing it again", fields...)
		t.publishContextVMResponse(ctx, outer, inner, encrypted, cascontextvm.NewErrorResponse(rpc.ID, ContextVMDuplicateRequestErrorCode, "request was already accepted but its outcome is unknown (still running elsewhere, or interrupted by a restart); check the resulting state before sending a new request"), rpc.Method)
		return false
	case localstore.ContextVMCompleted:
		if len(claim.Response) == 0 {
			t.logger.Info("ContextVM request already handled; no response was kept to replay", fields...)
			t.publishContextVMResponse(ctx, outer, inner, encrypted, cascontextvm.NewErrorResponse(rpc.ID, ContextVMDuplicateRequestErrorCode, "request was already handled; send an idempotency key (_meta.progressToken) to have its response replayed"), rpc.Method)
			return false
		}
		replay, err := storedContextVMResponse(claim.Response, rpc.ID)
		if err != nil {
			t.logger.Error("decode stored ContextVM response failed", append(fields, zap.Error(err))...)
			t.publishContextVMResponse(ctx, outer, inner, encrypted, cascontextvm.NewErrorResponse(rpc.ID, ContextVMDuplicateRequestErrorCode, "request was already handled; its stored response is unreadable"), rpc.Method)
			return false
		}
		t.logger.Info("replaying stored ContextVM response", fields...)
		t.publishContextVMResponse(ctx, outer, inner, encrypted, replay, rpc.Method)
		return false
	default:
		t.logger.Error("unknown ContextVM claim state; not executing", append(fields, zap.Int("state", int(claim.State)))...)
		return false
	}
	if progressToken == "" {
		return true
	}
	// A keyed request may have completed before this ledger existed, with its
	// response in the optional Postgres store. Adopt that outcome.
	cached, ok, fingerprintMismatch := t.cachedContextVMResponse(ctx, innerPubkey, rpc.Method, progressToken, fingerprint)
	switch {
	case fingerprintMismatch:
		t.logger.Warn("ContextVM idempotency key reused with different request parameters", fields...)
		conflict := cascontextvm.NewErrorResponse(rpc.ID, cascontextvm.InvalidRequestCode, "idempotency key was already used with different request parameters")
		t.completeLocalContextVMRequest(innerID, true, conflict)
		t.publishContextVMResponse(ctx, outer, inner, encrypted, conflict, rpc.Method)
		return false
	case ok:
		cached.ID = contextVMResponseID(rpc.ID)
		t.completeLocalContextVMRequest(innerID, true, cached)
		t.publishContextVMResponse(ctx, outer, inner, encrypted, cached, rpc.Method)
		return false
	}
	return true
}

// completeLocalContextVMRequest records a claimed request's completion, keeping
// its response for replay when the request carried an idempotency key. A
// failure is logged, not returned: the response is still published, and the
// claim stays pending, so a retry is answered as an unknown outcome rather
// than executed again.
func (t *EncryptedRequestTransport) completeLocalContextVMRequest(requestID string, keepResponse bool, response ContextVMJSONRPCResponse) {
	store := t.contextVMLocal.store
	if store == nil {
		return
	}
	var encoded json.RawMessage
	if keepResponse {
		raw, err := json.Marshal(response)
		if err != nil {
			t.logger.Error("encode ContextVM response for the ledger failed; it will not be replayable", zap.String("event_id", requestID), zap.Error(err))
		} else {
			encoded = raw
		}
	}
	if err := store.CompleteContextVMRequest(requestID, encoded); err != nil {
		t.logger.Error("record ContextVM request completion failed", zap.String("event_id", requestID), zap.Error(err))
	}
}

func storedContextVMResponse(raw json.RawMessage, requestID json.RawMessage) (ContextVMJSONRPCResponse, error) {
	var persisted persistedContextVMJSONRPCResponse
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return ContextVMJSONRPCResponse{}, err
	}
	response := ContextVMJSONRPCResponse{JSONRPC: persisted.JSONRPC, ID: contextVMResponseID(requestID), Error: persisted.Error}
	if len(persisted.Result) > 0 {
		response.Result = persisted.Result
	}
	return response, nil
}

// contextVMRelayFollower keeps one relay's request subscription and cursor.
type contextVMRelayFollower struct {
	t             *EncryptedRequestTransport
	pool          contextVMRelaySubscriber
	relayURL      string
	servicePubkey string
}

func (f *contextVMRelayFollower) run(ctx context.Context) error {
	anchor := nostr.Now()
	sub, err := f.subscribe(ctx, anchor)
	if err != nil {
		return err
	}
	defer sub.Close()
	return f.follow(ctx, sub, anchor)
}

func (f *contextVMRelayFollower) subscribe(ctx context.Context, anchor nostr.Timestamp) (*nostrpool.MergedSubscription, error) {
	local := &f.t.contextVMLocal
	cursor, err := local.store.ContextVMCursor(f.relayURL, f.servicePubkey)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errContextVMLedger, err)
	}
	from := cursor
	if floor := local.innerFloor(anchor.Time()); floor > from {
		from = floor
	}
	since := from - nostr.Timestamp(local.backdateOverlap()/time.Second)
	f.t.logger.Info("subscribing to ContextVM requests",
		zap.String("relay", f.relayURL),
		zap.Int64("cursor", int64(cursor)),
		zap.Time("since", since.Time()),
	)
	sub, err := f.pool.SubscribeWithOptions(ctx, []nostr.Filter{contextVMRequestFilter(f.servicePubkey, since)}, nostrpool.SubscribeOptions{
		Relays:                 []string{f.relayURL},
		AwaitUnavailableRelays: true,
		// The pool's own resume after a reconnect uses the newest delivered
		// created_at, so it needs the same backdating overlap.
		ResumeOverlap: local.backdateOverlap(),
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe to ContextVM requests on %s: %w", f.relayURL, err)
	}
	return sub, nil
}

// follow consumes sub (the REQ opened at anchor) and commits anchor at its
// first EOSE. The pool transparently reissues the REQ after a dropped
// connection or a retryable CLOSED; those reissued REQs' EOSEs carry a
// Reissued flag and a fresh anchor that is committed as a cursor, keeping
// it up to date without a periodic re-anchor REQ (bahia-irsry.48 item 4).
func (f *contextVMRelayFollower) follow(ctx context.Context, sub *nostrpool.MergedSubscription, anchor nostr.Timestamp) error {
	events, eoses, closes := sub.Events, sub.RelayEOSE, sub.Closed
	local := &f.t.contextVMLocal
	caughtUp := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return errContextVMRelayEnded
			}
			if err := f.t.processContextVMDelivery(ctx, ev); err != nil {
				return err
			}
		case eose, ok := <-eoses:
			if !ok {
				eoses = nil
				continue
			}
			if caughtUp {
				// A REQ the pool reissued after a reconnect caught up.
				// If it carries a reissue anchor, commit it so the cursor
				// stays fresh across reconnects.
				if eose.Reissued && eose.ReissuedAt > 0 {
					if err := local.store.AdvanceContextVMCursor(f.relayURL, f.servicePubkey, eose.ReissuedAt); err != nil {
						return fmt.Errorf("%w: %w", errContextVMLedger, err)
					}
					f.t.logger.Info("reissued ContextVM subscription caught up; cursor advanced",
						zap.String("relay", f.relayURL),
						zap.Time("cursor", eose.ReissuedAt.Time()),
					)
					local.processMu.Lock()
					local.pruneLocked(time.Now(), f.t.logger)
					local.processMu.Unlock()
					if local.onCaughtUp != nil {
						local.onCaughtUp(f.relayURL, eose.ReissuedAt)
					}
				} else {
					f.t.logger.Debug("reissued ContextVM request subscription caught up", zap.String("relay", f.relayURL))
				}
				continue
			}
			// The pool queues a relay's stored events before its EOSE, on
			// a separate channel: process them before committing.
			for drained := false; !drained; {
				select {
				case ev, ok := <-events:
					if !ok {
						return errContextVMRelayEnded
					}
					if err := f.t.processContextVMDelivery(ctx, ev); err != nil {
						return err
					}
				default:
					drained = true
				}
			}
			if err := local.store.AdvanceContextVMCursor(f.relayURL, f.servicePubkey, anchor); err != nil {
				return fmt.Errorf("%w: %w", errContextVMLedger, err)
			}
			caughtUp = true
			f.t.logger.Info("ContextVM requests caught up", zap.String("relay", f.relayURL), zap.Time("cursor", anchor.Time()))
			local.processMu.Lock()
			local.pruneLocked(time.Now(), f.t.logger)
			local.processMu.Unlock()
			if local.onCaughtUp != nil {
				local.onCaughtUp(f.relayURL, anchor)
			}
		case closed, ok := <-closes:
			if !ok {
				closes = nil
				continue
			}
			f.t.logger.Warn("relay closed ContextVM request subscription",
				zap.String("relay", f.relayURL),
				zap.String("reason", closed.Reason),
				zap.Bool("terminal", closed.Terminal),
			)
		}
	}
}
