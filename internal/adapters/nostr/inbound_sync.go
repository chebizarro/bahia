package nostr

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"go.uber.org/zap"
)

// The Subscriber's sync engine (bahia-irsry.10.1): one worker per relay
// catches that relay up and follows it live; one consumer stores, dispatches
// and keeps cursors. See the Subscriber doc comment for the algorithm and
// replay_cursor.go for the cursor rules.

// regularRetentionPruneInterval spaces local store pruning.
const regularRetentionPruneInterval = time.Hour

// Run implements app.BackgroundRunner. It blocks until ctx is cancelled.
func (s *Subscriber) Run(ctx context.Context) error {
	if s.pool == nil {
		return errors.New("nostr subscriber: a relay pool is required")
	}
	if s.store == nil {
		return errors.New("nostr subscriber: a local event store is required")
	}
	filters, err := s.buildSubscriptionFilters()
	if err != nil {
		return err
	}
	s.caughtUp.Store(false)
	for _, observer := range s.ingestionObservers {
		observer.ObserveSubscriptionStart()
	}
	defer func() {
		for _, observer := range s.ingestionObservers {
			observer.ObserveSubscriptionEnd()
		}
	}()
	s.pruneStore()
	lastPrune := s.now()

	runCtx, cancel := context.WithCancel(ctx)
	inbound := make(chan inboundItem, 256)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()
	connected, stopNotify := s.pool.NotifyRelayConnected()
	defer stopNotify()

	tracker := newCursorTracker(s.store, s.self, s.now, s.logger)
	progress := make(map[string]relayProgress)
	startRelays := func() {
		urls := s.pool.URLs()
		// A relay given up on stays given up while it is configured; one
		// that has left the pool since is synced afresh if it comes back.
		configured := make(map[string]struct{}, len(urls))
		for _, relayURL := range urls {
			configured[relayURL] = struct{}{}
		}
		for relayURL, state := range progress {
			if _, ok := configured[relayURL]; !ok && state.gaveUp {
				delete(progress, relayURL)
			}
		}
		for _, relayURL := range urls {
			if _, running := progress[relayURL]; running {
				continue
			}
			progress[relayURL] = relayProgress{}
			workers.Add(1)
			go func() {
				defer workers.Done()
				s.runRelay(runCtx, relayURL, filters, inbound)
			}()
		}
	}
	startRelays()
	s.logger.Info("inbound sync started",
		zap.Ints("kinds", s.kinds),
		zap.Strings("relays", s.pool.URLs()),
		zap.Int("filters", len(filters)))

	for {
		select {
		case <-runCtx.Done():
			return nil
		case <-connected:
			// A relay (re)connected, possibly one added by a reconfigure.
			startRelays()
		case item := <-inbound:
			s.consume(runCtx, item, tracker, progress)
			if item.op == opRelayGaveUp && everyRelayGaveUp(progress) {
				s.logger.Error("every relay refused the inbound subscription for good; inbound sync is idle until the relay set changes",
					zap.Strings("relays", s.pool.URLs()))
			}
			if item.op == opCaughtUp && s.now().Sub(lastPrune) >= regularRetentionPruneInterval {
				s.pruneStore()
				lastPrune = s.now()
			}
			if s.trace != nil {
				s.trace(item)
			}
		}
	}
}

// relayProgress tracks a relay's first catch-up, for IsCaughtUp.
type relayProgress struct {
	caughtUp bool
	failed   bool
	// gaveUp is set once the relay refused every filter for good; its
	// worker has stopped.
	gaveUp bool
}

type inboundOp int

const (
	// opEvent: an event the relay delivered for key.
	opEvent inboundOp = iota
	// opBegin: a new REQ (or NIP-77 session) generation starts for key.
	opBegin
	// opCommit: the relay proved key's stored history complete from floor.
	opCommit
	// opCaughtUp: the relay finished catching up on every filter.
	opCaughtUp
	// opRelayFailed: a relay session failed before catching up.
	opRelayFailed
	// opRelayGone: the relay left the pool; its worker stopped.
	opRelayGone
	// opRelayGaveUp: the relay refused every filter for good (see runRelay);
	// its worker stopped. reason is the last refusal.
	opRelayGaveUp
)

// inboundItem is one message from a relay worker to the consumer. A worker
// sends a key's events before that key's commit, and the channel is FIFO, so
// a cursor is only committed after the events below it are stored.
type inboundItem struct {
	op     inboundOp
	relay  string
	key    cursorKey
	ev     *nostr.Event
	floor  nostr.Timestamp
	reason string
}

func sendInbound(ctx context.Context, out chan<- inboundItem, item inboundItem) bool {
	select {
	case out <- item:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Subscriber) consume(ctx context.Context, item inboundItem, tracker *cursorTracker, progress map[string]relayProgress) {
	switch item.op {
	case opEvent:
		switch s.handleEvent(ctx, item.ev) {
		case ingestRejected:
		case ingestFailed:
			tracker.observe(item.key, item.ev, false)
		default:
			tracker.observe(item.key, item.ev, true)
		}
	case opBegin:
		tracker.begin(item.key)
	case opCommit:
		tracker.commit(item.key, item.floor)
	case opCaughtUp:
		state := progress[item.relay]
		state.caughtUp = true
		progress[item.relay] = state
		s.logger.Info("relay caught up", zap.String("relay", item.relay))
		s.updateCaughtUp(progress)
	case opRelayFailed:
		state := progress[item.relay]
		state.failed = true
		progress[item.relay] = state
		s.updateCaughtUp(progress)
	case opRelayGone:
		delete(progress, item.relay)
		s.updateCaughtUp(progress)
	case opRelayGaveUp:
		state := progress[item.relay]
		state.failed = true
		state.gaveUp = true
		progress[item.relay] = state
		s.updateCaughtUp(progress)
	}
}

func (s *Subscriber) updateCaughtUp(progress map[string]relayProgress) {
	if s.caughtUp.Load() {
		return
	}
	anyCaughtUp := false
	for _, state := range progress {
		if !state.caughtUp && !state.failed {
			return
		}
		anyCaughtUp = anyCaughtUp || state.caughtUp
	}
	if anyCaughtUp {
		s.markCaughtUp()
	}
}

func (s *Subscriber) markCaughtUp() {
	if s.caughtUp.Swap(true) {
		return
	}
	for _, observer := range s.ingestionObservers {
		observer.ObserveEOSE()
	}
	s.logger.Info("inbound sync caught up with stored events", zap.Ints("kinds", s.kinds))
}

func (s *Subscriber) pruneStore() {
	removed, err := s.store.PruneRegularEvents(s.now().Add(-defaultInboundRegularRetention))
	if err != nil {
		s.logger.Warn("prune local event store failed", zap.Error(err))
		return
	}
	if removed > 0 {
		s.logger.Info("pruned expired regular events from the local event store", zap.Int("removed", removed))
	}
}

// runRelay keeps one relay in sync until ctx ends, the relay leaves the pool,
// or the relay refuses every filter for good.
//
// A session that ends without a CLOSED (a dropped connection, a failed
// catch-up) is resynced with backoff, without limit. A CLOSED goes through
// the pool's CLOSED policy (closedRetryBudget) per filter, as for the pool's
// own subscriptions (bahia-irsry.49): "auth-required:" authenticates the
// relay and resyncs at once; a policy refusal, a failed AUTH, or a retryable
// reason more than nostr.closed_retry_budget times in a row gives that filter
// up on this relay, while its other filters keep syncing. A filter's count
// starts over once its catch-up commits. Every resync resumes each filter from
// its EOSE-anchored cursor, or reconciles it with NIP-77, as before.
func (s *Subscriber) runRelay(ctx context.Context, relayURL string, filters []inboundFilter, out chan<- inboundItem) {
	backoff := s.newRelayBackoff()
	budgets := make(map[string]*closedRetryBudget, len(filters))
	for _, filter := range filters {
		budgets[filter.hash] = s.pool.newClosedRetryBudget()
	}
	served := func(hash string) { budgets[hash].served() }
	active := filters
	for {
		caughtUp, err := s.syncRelay(ctx, relayURL, active, out, backoff, served)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRelayNotInPool) {
			s.logger.Info("relay left the pool; its inbound sync stopped", zap.String("relay", relayURL))
			sendInbound(ctx, out, inboundItem{op: opRelayGone, relay: relayURL})
			return
		}
		immediate := false
		var closed *relayClosedError
		if errors.As(err, &closed) && budgets[closed.hash] != nil {
			verdict := budgets[closed.hash].closed(closed.reason)
			action := verdict.Action
			if action == ClosedAuthenticate {
				if authErr := s.pool.AuthenticateRelay(ctx, relayURL); authErr != nil {
					s.logger.Warn("relay requires NIP-42 AUTH for an inbound REQ and AUTH failed",
						zap.String("relay", relayURL), zap.String("reason", closed.reason), zap.Error(authErr))
					s.pool.recordRelayError(relayURL, authUnavailableMetadata(closed.reason, authErr))
					action = ClosedTerminal
				} else {
					immediate = true
				}
			}
			if action == ClosedTerminal {
				if verdict.Exhausted {
					s.pool.recordClosedRetryExhausted(relayURL)
				}
				active = withoutInboundFilter(active, closed.hash)
				s.logger.Warn("relay refused an inbound filter for good; not retrying it",
					zap.String("relay", relayURL),
					zap.String("reason", closed.reason),
					zap.Bool("retry_budget_exhausted", verdict.Exhausted),
					zap.Int("filters_left", len(active)))
				if len(active) == 0 {
					sendInbound(ctx, out, inboundItem{op: opRelayGaveUp, relay: relayURL, reason: closed.reason})
					return
				}
			}
		}
		if !caughtUp {
			sendInbound(ctx, out, inboundItem{op: opRelayFailed, relay: relayURL})
		}
		if !immediate {
			delay := backoff.Next()
			s.logger.Warn("relay sync ended; resyncing with backoff",
				zap.String("relay", relayURL),
				zap.Error(err),
				zap.Duration("delay", delay),
				zap.Int("attempt", backoff.Attempt()))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		s.pool.RecordRelayReREQ()
	}
}

// withoutInboundFilter returns filters less those with hash.
func withoutInboundFilter(filters []inboundFilter, hash string) []inboundFilter {
	kept := make([]inboundFilter, 0, len(filters))
	for _, filter := range filters {
		if filter.hash != hash {
			kept = append(kept, filter)
		}
	}
	return kept
}

// syncRelay runs one session with a relay: catch up every filter, then follow
// live until the relay drops. It reports whether catch-up finished; served
// is told each filter whose catch-up committed.
func (s *Subscriber) syncRelay(ctx context.Context, relayURL string, filters []inboundFilter, out chan<- inboundItem, backoff *Backoff, served func(hash string)) (bool, error) {
	sessionStart := s.now()
	s.refreshRelayInfo(ctx, relayURL)
	for _, filter := range filters {
		if err := s.catchUp(ctx, relayURL, filter, sessionStart, out); err != nil {
			return false, err
		}
		served(filter.hash)
	}
	if !sendInbound(ctx, out, inboundItem{op: opCaughtUp, relay: relayURL}) {
		return true, ctx.Err()
	}
	backoff.Reset()
	return true, s.follow(ctx, relayURL, filters, sessionStart, out)
}

// relayInfoTimeout bounds the NIP-11 fetch that tells catch-up a relay's
// max_limit and whether it speaks NIP-77.
const relayInfoTimeout = 5 * time.Second

func (s *Subscriber) refreshRelayInfo(ctx context.Context, relayURL string) {
	if s.pool.GetRelayInfo(relayURL) != nil {
		return
	}
	infoCtx, cancel := context.WithTimeout(ctx, relayInfoTimeout)
	defer cancel()
	if _, err := s.pool.FetchRelayInfo(infoCtx, relayURL, false); err != nil {
		s.logger.Debug("relay NIP-11 unavailable; syncing without it", zap.String("relay", relayURL), zap.Error(err))
	}
}

// catchUp brings one filter up to date with one relay.
func (s *Subscriber) catchUp(ctx context.Context, relayURL string, filter inboundFilter, sessionStart time.Time, out chan<- inboundItem) error {
	key := cursorKey{relay: relayURL, hash: filter.hash}
	if !sendInbound(ctx, out, inboundItem{op: opBegin, relay: relayURL, key: key}) {
		return ctx.Err()
	}
	if filter.persistent {
		target := &negentropyTarget{store: s.store, out: out, relay: relayURL, key: key}
		err := s.pool.negentropySyncRelay(ctx, relayURL, filter.filter, target, s.sync.NegentropyUpload, s.sync.NegentropyTimeout)
		if err == nil {
			sendInbound(ctx, out, inboundItem{op: opCommit, relay: relayURL, key: key})
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.logger.Info("NIP-77 sync unavailable; paging the full set instead",
			zap.String("relay", relayURL),
			zap.Ints("kinds", kindsToInts(filter.filter.Kinds)),
			zap.Error(err))
		if err := s.fetchPaged(ctx, relayURL, key, filter.filter, out); err != nil {
			return err
		}
		sendInbound(ctx, out, inboundItem{op: opCommit, relay: relayURL, key: key})
		return ctx.Err()
	}
	cursor, err := s.store.Cursor(relayURL, filter.hash)
	if err != nil {
		return err
	}
	req := filter.filter
	req.Since = s.sync.resumeSince(cursor, sessionStart)
	if err := s.fetchPaged(ctx, relayURL, key, req, out); err != nil {
		return err
	}
	sendInbound(ctx, out, inboundItem{op: opCommit, relay: relayURL, key: key, floor: req.Since})
	return ctx.Err()
}

// fetchPaged delivers every stored event matching base from one relay, newest
// page first. A page that comes back full is followed by a page bounded with
// `until` at its oldest event (inclusive; ids already delivered are dropped by
// the store), so a gap larger than one page is never truncated (C-1).
func (s *Subscriber) fetchPaged(ctx context.Context, relayURL string, key cursorKey, base nostr.Filter, out chan<- inboundItem) error {
	limit := s.pool.relayPageLimit(relayURL, s.sync.PageLimit)
	var until nostr.Timestamp
	for {
		req := base
		req.Limit = limit
		req.Until = until
		delivered, oldest, err := s.drainStored(ctx, relayURL, key, req, out)
		if err != nil {
			return err
		}
		if delivered < limit {
			return nil
		}
		next := oldest
		if until != 0 && next >= until {
			// NIP-01 cannot page within one second: more than a page of
			// events share this created_at. Step past it, loudly.
			s.logger.Warn("more events share one created_at than a relay page holds; some are skipped",
				zap.String("relay", relayURL),
				zap.Int64("created_at", int64(until)),
				zap.Int("limit", limit))
			next = until - 1
		}
		if next <= 0 || next < base.Since {
			return nil
		}
		until = next
	}
}

// drainStored runs one REQ to EOSE and forwards its events. It returns how
// many distinct events the page held and the oldest created_at among them.
func (s *Subscriber) drainStored(ctx context.Context, relayURL string, key cursorKey, filter nostr.Filter, out chan<- inboundItem) (int, nostr.Timestamp, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sub, err := s.pool.subscribeRelay(reqCtx, relayURL, filter)
	if err != nil {
		return 0, 0, err
	}
	seen := make(map[nostr.ID]struct{})
	var oldest nostr.Timestamp
	deliver := func(ev nostr.Event) bool {
		if _, dup := seen[ev.ID]; dup {
			return true
		}
		seen[ev.ID] = struct{}{}
		if oldest == 0 || ev.CreatedAt < oldest {
			oldest = ev.CreatedAt
		}
		return sendInbound(ctx, out, inboundItem{op: opEvent, relay: relayURL, key: key, ev: &ev})
	}
	for {
		select {
		case <-ctx.Done():
			return len(seen), oldest, ctx.Err()
		case ev, ok := <-sub.Events:
			if !ok {
				return len(seen), oldest, fmt.Errorf("relay %s ended the REQ before EOSE", relayURL)
			}
			if !deliver(ev) {
				return len(seen), oldest, ctx.Err()
			}
		case <-sub.EndOfStoredEvents:
			// EOSE is only signalled once every stored event was handed to
			// Events; forward the ones still buffered.
			for {
				select {
				case ev, ok := <-sub.Events:
					if ok && deliver(ev) {
						continue
					}
				default:
				}
				break
			}
			return len(seen), oldest, ctx.Err()
		case reason := <-sub.ClosedReason:
			return len(seen), oldest, s.relayClosed(key, reason)
		}
	}
}

// follow holds one live REQ per filter on the relay until any of them ends.
// Live REQs start at the catch-up start less the overlap, covering events that
// reached the relay while it was being caught up.
func (s *Subscriber) follow(ctx context.Context, relayURL string, filters []inboundFilter, sessionStart time.Time, out chan<- inboundItem) error {
	liveCtx, cancel := context.WithCancel(ctx)
	ended := make(chan error, len(filters))
	var forwarders sync.WaitGroup
	defer func() {
		cancel()
		forwarders.Wait()
	}()
	since := s.sync.resumeSince(clampToClock(nostr.Timestamp(sessionStart.Unix()), s.now()), sessionStart)
	for _, filter := range filters {
		key := cursorKey{relay: relayURL, hash: filter.hash}
		req := filter.filter
		req.Since = since
		if !sendInbound(ctx, out, inboundItem{op: opBegin, relay: relayURL, key: key}) {
			return ctx.Err()
		}
		sub, err := s.pool.subscribeRelay(liveCtx, relayURL, req)
		if err != nil {
			return err
		}
		forwarders.Add(1)
		go func() {
			defer forwarders.Done()
			ended <- s.forwardLive(liveCtx, relayURL, key, sub, out)
		}()
	}
	return <-ended
}

// forwardLive forwards one live REQ's events. Its EOSE commits the events
// stored at the relay since the catch-up began; the REQ's since is not a floor
// for the cursor, which stays at the newest event the relay delivered so a
// quiet filter keeps resuming from before its last event.
func (s *Subscriber) forwardLive(ctx context.Context, relayURL string, key cursorKey, sub *nostr.Subscription, out chan<- inboundItem) error {
	eose := sub.EndOfStoredEvents
	forward := func(ev nostr.Event) bool {
		return sendInbound(ctx, out, inboundItem{op: opEvent, relay: relayURL, key: key, ev: &ev})
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-sub.Events:
			if !ok {
				return fmt.Errorf("relay %s ended the live REQ", relayURL)
			}
			if !forward(ev) {
				return ctx.Err()
			}
		case <-eose:
			eose = nil
			for drained := false; !drained; {
				select {
				case ev, ok := <-sub.Events:
					drained = !ok || !forward(ev)
				default:
					drained = true
				}
			}
			if !sendInbound(ctx, out, inboundItem{op: opCommit, relay: relayURL, key: key}) {
				return ctx.Err()
			}
		case reason := <-sub.ClosedReason:
			return s.relayClosed(key, reason)
		}
	}
}

// relayClosedError ends a sync session on a relay's CLOSED for one filter;
// runRelay applies the pool's CLOSED policy to it.
type relayClosedError struct {
	relay  string
	hash   string
	reason string
}

func (e *relayClosedError) Error() string {
	return fmt.Sprintf("relay %s CLOSED the REQ: %s", e.relay, e.reason)
}

// relayClosed records a relay CLOSED for key's filter and returns the error
// that ends the session (see runRelay for what follows).
func (s *Subscriber) relayClosed(key cursorKey, reason string) error {
	reason = strings.TrimSpace(reason)
	s.pool.RecordRelayClosed(key.relay, reason)
	s.logger.Warn("relay closed inbound subscription", zap.String("relay", key.relay), zap.String("reason", reason))
	for _, observer := range s.ingestionObservers {
		observer.ObserveRelayClosed(key.relay, reason)
	}
	return &relayClosedError{relay: key.relay, hash: key.hash, reason: reason}
}

// negentropyTarget is the local side of a NIP-77 session for one relay and
// filter: its set is the local store's, and events fetched from the relay go
// through the consumer like any other delivery.
type negentropyTarget struct {
	store *localstore.Store
	out   chan<- inboundItem
	relay string
	key   cursorKey
}

func (t *negentropyTarget) QueryEvents(filter nostr.Filter) iter.Seq[nostr.Event] {
	return t.store.QueryEvents(filter)
}

func (t *negentropyTarget) Publish(ctx context.Context, ev nostr.Event) error {
	if !sendInbound(ctx, t.out, inboundItem{op: opEvent, relay: t.relay, key: t.key, ev: &ev}) {
		return ctx.Err()
	}
	return nil
}
