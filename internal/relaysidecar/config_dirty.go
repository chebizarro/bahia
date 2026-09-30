package relaysidecar

import (
	"context"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"
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
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		handled := make(map[string]nostr.ID)
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
	if !ok || handled[key] == event.ID {
		return
	}
	handled[key] = event.ID
	if err := s.consumer.Handle(context.WithoutCancel(ctx), event); err != nil {
		s.logger.Warn("relay-sidecar desired config rejected", zap.String("event_id", event.ID.Hex()), zap.Error(err))
	}
}
