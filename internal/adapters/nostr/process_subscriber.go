package nostr

import (
	"context"
	"errors"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"go.uber.org/zap"
)

// StoreBackedSubscriber serves SubscribeAllWithEOSE from a ProcessSync over a
// local store, for consumers written against MergedSubscription such as the
// DNS agent's ContextVM request transport (bahia-irsry.10.5). The consumer
// keeps its own validation, routing, expiry and response handling; what
// changes is that a restart or reconnect delivers only events the store has
// not seen, each relay catching up independently.
//
// Delivery is at most once per stored event: an event is stored before it is
// handed to the consumer, so one in flight when the process stops is not
// delivered again. Ephemeral events, which no store holds, are deduplicated
// across relays in memory as the pool's merged subscriptions do.
type StoreBackedSubscriber struct {
	Pool   *RelayPool
	Store  *localstore.Store
	Logger *zap.Logger
	Config InboundSyncConfig
	// ReconcileKinds: see ProcessSync.ReconcileKinds.
	ReconcileKinds []nostr.Kind
	// Retention: see ProcessSync.Retention.
	Retention time.Duration
}

// AuthenticateRelay answers a consumer's NIP-42 retry; the sync workers
// already re-REQ after AUTH themselves.
func (s *StoreBackedSubscriber) AuthenticateRelay(ctx context.Context, relayURL string) error {
	return s.Pool.AuthenticateRelay(ctx, relayURL)
}

// SubscribeAllWithEOSE starts a ProcessSync for filters. RelayEOSE fires each
// time a relay catches up, EndOfStoredEvents closes once (see
// ProcessSync.CaughtUp), and Events closes when the sync stops. CLOSED frames
// are handled by the sync workers, so Closed never fires.
func (s *StoreBackedSubscriber) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*MergedSubscription, error) {
	if s.Pool == nil || s.Store == nil {
		return nil, errors.New("store-backed subscription requires a relay pool and a local store")
	}
	if _, _, err := processInboundFilters(filters, s.ReconcileKinds); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	events := make(chan *nostr.Event)
	relayEOSE := make(chan RelayEOSE, len(s.Pool.URLs()))
	allEOSE := make(chan struct{})
	closed := make(chan RelayClosed)
	done := make(chan struct{})
	dedup := NewEventDeduplicator(10000)
	syncer := &ProcessSync{
		Pool: s.Pool, Store: s.Store, Logger: s.Logger, Config: s.Config,
		ReconcileKinds: s.ReconcileKinds,
		Retention:      s.Retention,
		Apply: func(ctx context.Context, ev *nostr.Event) {
			if dedup.IsDuplicate(ev.ID.Hex()) {
				return
			}
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		},
		RelayCaughtUp: func(relay string) {
			select {
			case relayEOSE <- RelayEOSE{RelayURL: relay}:
			case <-runCtx.Done():
			}
		},
		CaughtUp: func() { close(allEOSE) },
	}
	go func() {
		defer close(done)
		if err := syncer.Run(runCtx, filters); err != nil && s.Logger != nil {
			s.Logger.Error("store-backed subscription stopped", zap.Error(err))
		}
		close(events)
		close(relayEOSE)
		close(closed)
	}()
	var closeOnce sync.Once
	return &MergedSubscription{
		Events: events, RelayEOSE: relayEOSE, EndOfStoredEvents: allEOSE, Closed: closed,
		closeFn: func() { closeOnce.Do(func() { cancel(); <-done }) },
	}, nil
}
