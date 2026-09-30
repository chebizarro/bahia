package relaysidecar

import (
	"context"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// configRetryDelay is how long the config worker waits before re-reading a
// coordinate after the store failed to return it.
const configRetryDelay = time.Second

// configDirtySet records desired-config coordinates whose stored event changed
// and has not been handled yet. Config events are already persisted when they
// are marked, so the set only holds addressable coordinates. The worker
// re-reads the current event for each coordinate from the store. The set
// therefore never drops a change, and a burst of versions for one coordinate
// collapses to the one the store holds, so the latest config wins.
type configDirtySet struct {
	mu      sync.Mutex
	order   []string
	pending map[string]struct{}
	wake    chan struct{}
}

func newConfigDirtySet() *configDirtySet {
	return &configDirtySet{pending: make(map[string]struct{}), wake: make(chan struct{}, 1)}
}

func (d *configDirtySet) mark(key string) {
	d.mu.Lock()
	if _, ok := d.pending[key]; !ok {
		d.pending[key] = struct{}{}
		d.order = append(d.order, key)
	}
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *configDirtySet) take() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.order
	d.order = nil
	clear(d.pending)
	return out
}

func (s *Server) startConfigWorker(ctx context.Context) {
	if s.consumer == nil || s.configDirty == nil {
		return
	}
	// Reconcile persisted desired state with the store once: a desired event
	// deleted or expired while the sidecar was down reads back absent and is
	// withdrawn, an unchanged one is skipped as already handled, and pending
	// expirations are re-armed.
	handled := make(map[string]nostr.ID)
	for key, eventID := range s.consumer.desiredEvents() {
		if id, err := nostr.IDFromHex(eventID); err == nil {
			handled[key] = id
		}
		s.configDirty.mark(key)
	}
	for key, expiresAt := range s.consumer.desiredExpiries() {
		s.scheduleConfigExpiry(ctx, key, expiresAt)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.configDirty.wake:
			}
			for _, key := range s.configDirty.take() {
				s.handleLatestConfig(ctx, key, handled)
			}
		}
	}()
}

// handleLatestConfig hands the consumer the event the store currently holds for
// key. Handling reads the store, not the notification, so an out-of-order
// OnEventSaved can't make an older version win.
func (s *Server) handleLatestConfig(ctx context.Context, key string, handled map[string]nostr.ID) {
	event, ok, err := s.store.latestByReplaceableKey(ctx, key)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.logger.Error("relay-sidecar could not read latest desired config; retrying", zap.String("coordinate", key), zap.Error(err))
		time.AfterFunc(configRetryDelay, func() {
			if ctx.Err() == nil {
				s.configDirty.mark(key)
			}
		})
		return
	}
	// The store's replaceable read does not filter NIP-40 expiration (only
	// Query does, and the sweep runs periodically), so check it here.
	if ok && nostrutil.Expired(&event, s.consumer.now()) {
		ok = false
	}
	if !ok {
		// Deleted (NIP-09) or expired (NIP-40): the desired event is gone.
		delete(handled, key)
		if err := s.consumer.withdraw(context.WithoutCancel(ctx), key, "desired event was deleted or has expired"); err != nil {
			s.logger.Warn("relay-sidecar could not withdraw desired config", zap.String("coordinate", key), zap.Error(err))
		}
		return
	}
	if handled[key] == event.ID {
		return
	}
	handled[key] = event.ID
	if err := s.consumer.Handle(context.WithoutCancel(ctx), event); err != nil {
		s.logger.Warn("relay-sidecar desired config rejected", zap.String("event_id", event.ID.Hex()), zap.Error(err))
	}
	if expiresAt := nostrutil.ExpiresAt(&event); expiresAt > 0 {
		s.scheduleConfigExpiry(ctx, key, expiresAt.Time())
	}
}

// scheduleConfigExpiry re-reads key when its desired event expires, so the
// expiry is handled at that moment rather than at the next retention sweep.
func (s *Server) scheduleConfigExpiry(ctx context.Context, key string, expiresAt time.Time) {
	s.configAfter(expiresAt.Sub(s.consumer.now()), func() {
		if ctx.Err() == nil {
			s.configDirty.mark(key)
		}
	})
}
