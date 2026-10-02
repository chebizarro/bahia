package nostr

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"go.uber.org/zap"
)

// ProcessSync runs the daemon's inbound sync engine (inbound_sync.go) for a
// small standalone process (the DNS agent, the FIPS bridge, pkg/discovery)
// with filters of its own instead of the daemon's kind and author scopes
// (bahia-irsry.10.5). It reuses the Subscriber's per-relay workers and its
// consumer unchanged:
//
//   - every relay is caught up and followed independently, so a relay that
//     is down or behind never holds back the others and catches up on its
//     own when it returns;
//   - replaceable/addressable sets (and ReconcileKinds) are reconciled with
//     NIP-77 against the local store, so a restart fetches only the events
//     the store lacks; regular kinds resume from a per-(relay, filter)
//     cursor that advances only after EOSE;
//   - each event is validated and stored before Apply runs, and Apply runs
//     only for events new to the store: never for a redelivery, another
//     relay's copy, or an event already held from before a restart.
//
// Because the store holds everything already applied, a consumer rebuilds its
// in-memory state on start by replaying Store.QueryEvents for its filters
// before Run (the store keeps the latest version per replaceable coordinate,
// tombstones included, and drops what stored NIP-09 requests delete, for
// coordinates of any length).
type ProcessSync struct {
	Pool   *RelayPool
	Store  *localstore.Store
	Logger *zap.Logger
	// Config tunes catch-up; the zero value means DefaultInboundSyncConfig.
	Config InboundSyncConfig
	// ReconcileKinds are regular kinds whose events can be backdated, such as
	// NIP-59 gift wraps (up to six hours), so a created_at cursor would miss
	// them. They are reconciled by id with NIP-77 (paged in full where NIP-77
	// is unavailable) within the window their filter's Since sets, and
	// followed by a live-only REQ ("limit":0) with that Since, because the
	// live REQ every filter gets starts minutes before the catch-up and a
	// relay applies since to live events too.
	ReconcileKinds []nostr.Kind
	// Retention bounds how long stored regular events are kept (default 7
	// days). Events inside a ReconcileKinds window are never pruned, so a
	// reconcile does not fetch them again.
	Retention time.Duration
	// Apply receives each validated event new to the store, on the sync
	// goroutine. Ephemeral events are never stored, so a relay may deliver
	// them again after a reconnect and every relay delivers its own copy.
	Apply func(context.Context, *nostr.Event)
	// RelayCaughtUp, if set, is called each time one relay finishes catching
	// up on every filter.
	RelayCaughtUp func(relayURL string)
	// CaughtUp, if set, is called once, when every relay has either caught up
	// or failed its first attempt and at least one has caught up: a relay
	// that is down does not hold the process back, it catches up later.
	CaughtUp func()
	// RelayBackoff paces a relay's resync after its session ends (default
	// DefaultBackoff).
	RelayBackoff func() *Backoff
}

// Run syncs filters with every relay in the pool until ctx ends. It returns
// nil when ctx is cancelled, and an error when every relay has left the pool
// or refused every filter for good: a *SubscriptionGaveUpError (matching
// ErrSubscriptionGaveUp) in the latter case, which callers must not answer by
// running again, since that would sidestep the pool's CLOSED policy.
func (p *ProcessSync) Run(ctx context.Context, filters []nostr.Filter) error {
	if p == nil || p.Pool == nil || p.Store == nil || p.Apply == nil {
		return errors.New("process sync requires a relay pool, a local store and an event applier")
	}
	inbound, reconcile, err := processInboundFilters(filters, p.ReconcileKinds)
	if err != nil {
		return err
	}
	urls := p.Pool.URLs()
	if len(urls) == 0 {
		return errors.New("process sync: the relay pool has no relays")
	}
	logger := p.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	config := p.Config
	if config.IsZero() {
		config = DefaultInboundSyncConfig()
	}
	ready := &processReady{caughtUp: p.CaughtUp}
	apply := p.Apply
	worker := NewSubscriber(p.Pool, nil, logger,
		WithLocalStore(p.Store),
		WithInboundSync(config),
		WithHandler(func(ctx context.Context, ev *nostr.Event) { apply(ctx, ev) }),
		WithIngestionObserver(ready),
	)
	if p.RelayBackoff != nil {
		worker.newRelayBackoff = p.RelayBackoff
	}
	retention := p.Retention
	if retention <= 0 {
		retention = defaultInboundRegularRetention
	}
	prune := func() {
		cutoff := worker.now().Add(-retention)
		if reconcile.present {
			if reconcile.floor <= 0 {
				return // a window over the whole history: nothing may go
			}
			if floor := time.Unix(int64(reconcile.floor), 0); floor.Before(cutoff) {
				cutoff = floor
			}
		}
		removed, err := p.Store.PruneRegularEvents(cutoff)
		if err != nil {
			logger.Warn("prune local event store failed", zap.Error(err))
		} else if removed > 0 {
			logger.Info("pruned expired regular events from the local event store", zap.Int("removed", removed))
		}
	}
	prune()
	lastPrune := worker.now()

	runCtx, cancel := context.WithCancel(ctx)
	items := make(chan inboundItem, 256)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()
	if len(reconcile.live) > 0 {
		// Opened before any catch-up starts, so an event is either stored
		// before it (and reconciled) or delivered here.
		live, err := p.Pool.SubscribeWithOptions(runCtx, reconcile.live, SubscribeOptions{AwaitUnavailableRelays: true})
		if err != nil {
			return fmt.Errorf("process sync: open live-only REQ for backdated kinds: %w", err)
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer live.Close()
			forwardLiveOnly(runCtx, live, items)
		}()
	}
	tracker := newCursorTracker(p.Store, nil, worker.now, worker.logger)
	progress := make(map[string]relayProgress, len(urls))
	var refusals []RelayClosed
	for _, relayURL := range urls {
		progress[relayURL] = relayProgress{}
		workers.Add(1)
		go func() {
			defer workers.Done()
			worker.runRelay(runCtx, relayURL, inbound, items)
		}()
	}
	for {
		select {
		case <-runCtx.Done():
			return nil
		case item := <-items:
			worker.consume(runCtx, item, tracker, progress)
			switch item.op {
			case opCaughtUp:
				if p.RelayCaughtUp != nil {
					p.RelayCaughtUp(item.relay)
				}
				if worker.now().Sub(lastPrune) >= regularRetentionPruneInterval {
					prune()
					lastPrune = worker.now()
				}
			case opRelayGone:
				if len(progress) == 0 {
					return errors.New("process sync: every relay left the pool")
				}
			case opRelayGaveUp:
				refusals = append(refusals, RelayClosed{RelayURL: item.relay, Reason: item.reason, Terminal: true})
				if everyRelayGaveUp(progress) {
					return fmt.Errorf("process sync: %w", &SubscriptionGaveUpError{Closed: refusals})
				}
			}
		}
	}
}

func everyRelayGaveUp(progress map[string]relayProgress) bool {
	for _, state := range progress {
		if !state.gaveUp {
			return false
		}
	}
	return len(progress) > 0
}

// liveOnlyKey is the cursor key of live-only deliveries. It is never
// committed: those kinds are reconciled, not resumed from a cursor.
var liveOnlyKey = cursorKey{hash: "live-only"}

// forwardLiveOnly hands the live-only REQ's events to the consumer, which
// stores and applies them like any other delivery.
func forwardLiveOnly(ctx context.Context, live *MergedSubscription, out chan<- inboundItem) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-live.Events:
			if !ok {
				return
			}
			if !sendInbound(ctx, out, inboundItem{op: opEvent, key: liveOnlyKey, ev: ev}) {
				return
			}
		}
	}
}

// processReady turns the consumer's first catch-up into the CaughtUp callback.
type processReady struct {
	caughtUp func()
}

func (r *processReady) ObserveSubscriptionStart()      {}
func (r *processReady) ObserveSubscriptionEnd()        {}
func (r *processReady) ObserveRelayClosed(_, _ string) {}
func (r *processReady) ObserveEOSE() {
	if r.caughtUp != nil {
		r.caughtUp()
	}
}

// reconcileWindow describes the ReconcileKinds filters: floor is the oldest
// window start (stored events below it may be pruned without being fetched
// again), and live the live-only REQs that follow them.
type reconcileWindow struct {
	present bool
	floor   nostr.Timestamp
	live    []nostr.Filter
}

// processInboundFilters splits each filter by how it catches up: one group for
// ReconcileKinds (which keeps the filter's Since as its reconcile window), and
// the replaceable/addressable and regular groups splitInboundFilter makes.
func processInboundFilters(filters []nostr.Filter, reconcileKinds []nostr.Kind) ([]inboundFilter, reconcileWindow, error) {
	var out []inboundFilter
	var window reconcileWindow
	for _, base := range filters {
		var reconcile, rest []nostr.Kind
		for _, kind := range base.Kinds {
			if slices.Contains(reconcileKinds, kind) && !isPersistentKind(kind) {
				reconcile = append(reconcile, kind)
			} else {
				rest = append(rest, kind)
			}
		}
		if len(reconcile) > 0 {
			filter := base
			filter.Kinds = reconcile
			filter.Until, filter.Limit, filter.LimitZero = 0, 0, false
			out = append(out, inboundFilter{filter: filter, hash: inboundFilterHash(filter), persistent: true})
			liveOnly := filter
			liveOnly.LimitZero = true
			window.live = append(window.live, liveOnly)
			if !window.present || filter.Since < window.floor {
				window.floor = filter.Since
			}
			window.present = true
		}
		if len(rest) > 0 {
			other := base
			other.Kinds = rest
			out = append(out, splitInboundFilter(other)...)
		}
	}
	if len(out) == 0 {
		return nil, reconcileWindow{}, errors.New("process sync: no filter names a kind")
	}
	return out, window, nil
}
