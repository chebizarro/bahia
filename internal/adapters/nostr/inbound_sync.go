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

// The Subscriber's sync engine: one worker per relay
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
	connected, stopConnectedNotify := s.pool.NotifyRelayConnected()
	defer stopConnectedNotify()
	removed, stopRemovedNotify := s.pool.NotifyRelayRemoved()
	defer stopRemovedNotify()

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
		case <-removed:
			// A relay was removed from the pool's topology. Clear its
			// progress (including gaveUp) so it syncs afresh if re-added,
			// and start any newly configured relays.
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
	// NIP-40: expired events of any kind leave the store on the same pass
	// (retired run tombstones, expired status records).
	expired, err := s.store.PruneExpiredEvents(s.now())
	if err != nil {
		s.logger.Warn("prune expired local events failed", zap.Error(err))
		return
	}
	if expired > 0 {
		s.logger.Info("pruned NIP-40 expired events from the local event store", zap.Int("removed", expired))
	}
}

// runRelay keeps one relay in sync until ctx ends, the relay leaves the pool,
// or the relay refuses every filter for good.
//
// A session that ends without a CLOSED (a dropped connection, a failed
// catch-up) is resynced with backoff, without limit. A CLOSED goes through
// the pool's CLOSED policy (closedRetryBudget) per filter, as for the pool's
// own subscriptions: "auth-required:" authenticates the
// relay and resyncs at once; a policy refusal, a failed AUTH, or a retryable
// reason more than nostr.closed_retry_budget times in a row gives that filter
// up on this relay, while its other filters keep syncing. A filter's count
// starts over once its catch-up commits. Every resync resumes each filter from
// its EOSE-anchored cursor, or reconciles it with NIP-77, as before.
//
// Each CLOSED is charged to its filter's budget the moment the REQ observes it
// (inboundClosedLedger), and every verdict pending from the session is applied
// here before the next session's REQs go out. A session can end for a reason
// other than that CLOSED (the connection dropped, another filter's REQ ended)
// with the CLOSED still in flight; it still counts, so a flapping relay cannot
// get a refused filter reissued past its budget.
func (s *Subscriber) runRelay(ctx context.Context, relayURL string, filters []inboundFilter, out chan<- inboundItem) {
	backoff := s.newRelayBackoff()
	ledger := newInboundClosedLedger(s.pool, filters)
	active := filters
	for {
		caughtUp, err := s.syncRelay(ctx, relayURL, active, out, backoff, ledger)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRelayNotInPool) {
			s.logger.Info("relay left the pool; its inbound sync stopped", zap.String("relay", relayURL))
			sendInbound(ctx, out, inboundItem{op: opRelayGone, relay: relayURL})
			return
		}
		immediate := false
		for _, closed := range ledger.take(active) {
			action := closed.Action
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
			if action != ClosedTerminal {
				continue
			}
			if closed.Exhausted {
				s.pool.recordClosedRetryExhausted(relayURL)
			}
			active = withoutInboundFilter(active, closed.hash)
			s.logger.Warn("relay refused an inbound filter for good; not retrying it",
				zap.String("relay", relayURL),
				zap.String("reason", closed.reason),
				zap.Bool("retry_budget_exhausted", closed.Exhausted),
				zap.Int("filters_left", len(active)))
			if len(active) == 0 {
				sendInbound(ctx, out, inboundItem{op: opRelayGaveUp, relay: relayURL, reason: closed.reason})
				return
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

// inboundClosedLedger applies the pool's CLOSED policy to one relay's inbound
// filters across its sync sessions: a closedRetryBudget per filter, charged
// the moment a REQ observes a CLOSED, and the verdicts not yet acted on. The
// live REQs of a session observe CLOSEDs concurrently, hence the lock.
type inboundClosedLedger struct {
	mu      sync.Mutex
	budgets map[string]*closedRetryBudget
	pending map[string]inboundClosedVerdict
}

// inboundClosedVerdict is the budget's verdict on one filter's CLOSED.
type inboundClosedVerdict struct {
	closedVerdict
	hash   string
	reason string
}

func newInboundClosedLedger(pool *RelayPool, filters []inboundFilter) *inboundClosedLedger {
	l := &inboundClosedLedger{
		budgets: make(map[string]*closedRetryBudget, len(filters)),
		pending: make(map[string]inboundClosedVerdict, len(filters)),
	}
	for _, filter := range filters {
		l.budgets[filter.hash] = pool.newClosedRetryBudget()
	}
	return l
}

// served records that hash's catch-up committed: its CLOSED count starts over.
func (l *inboundClosedLedger) served(hash string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if budget := l.budgets[hash]; budget != nil {
		budget.served()
	}
}

// closed charges one CLOSED to hash's budget and keeps the verdict for take.
// A filter's REQs run one at a time, so at most one CLOSED per filter is
// pending; should a second arrive, a terminal verdict is never downgraded.
func (l *inboundClosedLedger) closed(hash, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	budget := l.budgets[hash]
	if budget == nil {
		return
	}
	verdict := inboundClosedVerdict{closedVerdict: budget.closed(reason), hash: hash, reason: reason}
	if previous, ok := l.pending[hash]; ok && previous.Action == ClosedTerminal && verdict.Action != ClosedTerminal {
		return
	}
	l.pending[hash] = verdict
}

// take returns the pending verdicts for filters, in their order, and clears
// them. A verdict for a filter not synced is dropped.
func (l *inboundClosedLedger) take(filters []inboundFilter) []inboundClosedVerdict {
	l.mu.Lock()
	defer l.mu.Unlock()
	verdicts := make([]inboundClosedVerdict, 0, len(l.pending))
	for _, filter := range filters {
		if verdict, ok := l.pending[filter.hash]; ok {
			verdicts = append(verdicts, verdict)
		}
	}
	clear(l.pending)
	return verdicts
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
// live until the relay drops. It reports whether catch-up finished; ledger is
// told each filter whose catch-up committed and each CLOSED a REQ observed.
func (s *Subscriber) syncRelay(ctx context.Context, relayURL string, filters []inboundFilter, out chan<- inboundItem, backoff *Backoff, ledger *inboundClosedLedger) (bool, error) {
	sessionStart := s.now()
	s.refreshRelayInfo(ctx, relayURL)
	for _, filter := range filters {
		if err := s.catchUp(ctx, relayURL, filter, sessionStart, out, ledger); err != nil {
			return false, err
		}
		ledger.served(filter.hash)
	}
	if !sendInbound(ctx, out, inboundItem{op: opCaughtUp, relay: relayURL}) {
		return true, ctx.Err()
	}
	backoff.Reset()
	return true, s.follow(ctx, relayURL, filters, sessionStart, out, ledger)
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
func (s *Subscriber) catchUp(ctx context.Context, relayURL string, filter inboundFilter, sessionStart time.Time, out chan<- inboundItem, ledger *inboundClosedLedger) error {
	key := cursorKey{relay: relayURL, hash: filter.hash}
	if !sendInbound(ctx, out, inboundItem{op: opBegin, relay: relayURL, key: key}) {
		return ctx.Err()
	}
	if filter.persistent {
		target := &negentropyTarget{store: s.store, out: out, relay: relayURL, key: key}
		upload := s.sync.NegentropyUpload
		if upload && s.sync.NegentropyUploadFilter != nil {
			upload = s.sync.NegentropyUploadFilter(relayURL)
		}
		err := s.pool.negentropySyncRelay(ctx, relayURL, filter.filter, target, upload, s.sync.NegentropyTimeout)
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
		if err := s.fetchPaged(ctx, relayURL, key, filter.filter, out, ledger); err != nil {
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
	if err := s.fetchPaged(ctx, relayURL, key, req, out, ledger); err != nil {
		return err
	}
	sendInbound(ctx, out, inboundItem{op: opCommit, relay: relayURL, key: key, floor: req.Since})
	return ctx.Err()
}

// pagingBackdateOverlap is the grace window added after paging completes to
// catch events that were published during the paging period with a created_at
// backdated beyond where paging had already passed. The store deduplicates, so
// overlap-delivered events are safe. 120s covers the typical 60s NIP-01 skew
// tolerance plus one page round-trip, without a noticeable re-read cost on a
// set small enough to page.
const pagingBackdateOverlap = 120

// fetchPaged delivers every stored event matching base from one relay, newest
// page first. A page that comes back full is followed by a page bounded with
// `until` at its oldest event (inclusive; ids already delivered are dropped by
// the store), so a gap larger than one page is never truncated.
//
// After the last page, a widened-overlap REQ rechecks the paged window to catch
// events backdated beyond the relay's NIP-01 tolerance and published during
// paging (.56 item 4). The store's dedup makes this idempotent.
func (s *Subscriber) fetchPaged(ctx context.Context, relayURL string, key cursorKey, base nostr.Filter, out chan<- inboundItem, ledger *inboundClosedLedger) error {
	limit := s.pool.relayPageLimit(relayURL, s.sync.PageLimit)
	var until nostr.Timestamp
	pagingStart := nostr.Now()
	paged := false
	for {
		req := base
		req.Limit = limit
		req.Until = until
		delivered, oldest, err := s.drainStored(ctx, relayURL, key, req, out, ledger)
		if err != nil {
			return err
		}
		if delivered < limit {
			break
		}
		paged = true
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
			break
		}
		until = next
	}
	if !paged {
		return nil
	}
	// Widened-overlap pass: re-scan the paged window with a since that predates
	// the first page by pagingBackdateOverlap seconds. Events published during
	// the paging period with a backdated created_at fall into this window. The
	// store deduplicates events already delivered above.
	overlapSince := pagingStart - nostr.Timestamp(pagingBackdateOverlap)
	if base.Since != 0 && overlapSince < base.Since {
		overlapSince = base.Since
	}
	overlap := base
	overlap.Since = overlapSince
	overlap.Limit = 0
	overlap.Until = 0
	_, _, err := s.drainStored(ctx, relayURL, key, overlap, out, ledger)
	if err != nil {
		return err
	}
	return nil
}

// drainStored runs one REQ to EOSE and forwards its events. It returns how
// many distinct events the page held and the oldest created_at among them.
func (s *Subscriber) drainStored(ctx context.Context, relayURL string, key cursorKey, filter nostr.Filter, out chan<- inboundItem, ledger *inboundClosedLedger) (int, nostr.Timestamp, error) {
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
			s.closedBeforeEnd(sub, key, ledger)
			return len(seen), oldest, ctx.Err()
		case ev, ok := <-sub.Events:
			if !ok {
				if err := s.closedBeforeEnd(sub, key, ledger); err != nil {
					return len(seen), oldest, err
				}
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
			return len(seen), oldest, s.relayClosed(key, reason, ledger)
		}
	}
}

// closedBeforeEnd records the CLOSED, if any, the relay sent before the REQ
// ended, and returns its error. The library delivers a CLOSED on ClosedReason
// before it ends the subscription, so when Events closes (or the REQ's context
// ends) with a CLOSED buffered, that CLOSED must not be lost: it ended the
// REQ, and its filter's budget is charged for it.
func (s *Subscriber) closedBeforeEnd(sub *nostr.Subscription, key cursorKey, ledger *inboundClosedLedger) error {
	select {
	case reason, ok := <-sub.ClosedReason:
		if ok {
			return s.relayClosed(key, reason, ledger)
		}
	default:
	}
	return nil
}

// follow holds one live REQ per filter on the relay until any of them ends.
// Live REQs start at the catch-up start less the overlap, covering events that
// reached the relay while it was being caught up.
func (s *Subscriber) follow(ctx context.Context, relayURL string, filters []inboundFilter, sessionStart time.Time, out chan<- inboundItem, ledger *inboundClosedLedger) error {
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
			ended <- s.forwardLive(liveCtx, relayURL, key, sub, out, ledger)
		}()
	}
	return <-ended
}

// forwardLive forwards one live REQ's events. Its EOSE commits the events
// stored at the relay since the catch-up began; the REQ's since is not a floor
// for the cursor, which stays at the newest event the relay delivered so a
// quiet filter keeps resuming from before its last event.
//
// Live REQs end together: once one ends, follow cancels the rest. A CLOSED
// the relay sent to one of those is still recorded on the way out.
func (s *Subscriber) forwardLive(ctx context.Context, relayURL string, key cursorKey, sub *nostr.Subscription, out chan<- inboundItem, ledger *inboundClosedLedger) error {
	eose := sub.EndOfStoredEvents
	forward := func(ev nostr.Event) bool {
		return sendInbound(ctx, out, inboundItem{op: opEvent, relay: relayURL, key: key, ev: &ev})
	}
	for {
		select {
		case <-ctx.Done():
			s.closedBeforeEnd(sub, key, ledger)
			return ctx.Err()
		case ev, ok := <-sub.Events:
			if !ok {
				if err := s.closedBeforeEnd(sub, key, ledger); err != nil {
					return err
				}
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
			return s.relayClosed(key, reason, ledger)
		}
	}
}

// relayClosedError ends a sync session on a relay's CLOSED for one filter.
// The CLOSED was charged to the filter's budget when it was observed
// (inboundClosedLedger); runRelay acts on the verdict before the next session.
type relayClosedError struct {
	relay  string
	hash   string
	reason string
}

func (e *relayClosedError) Error() string {
	return fmt.Sprintf("relay %s CLOSED the REQ: %s", e.relay, e.reason)
}

// relayClosed records a relay CLOSED for key's filter, charges it to the
// filter's budget, and returns the error that ends the session (see runRelay
// for what follows).
func (s *Subscriber) relayClosed(key cursorKey, reason string, ledger *inboundClosedLedger) error {
	reason = strings.TrimSpace(reason)
	ledger.closed(key.hash, reason)
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
