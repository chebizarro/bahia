package app

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

const hiveCIPolicyMaxRetryShift = 4

// hiveCIPolicyHydrator waits for the projector's first relay catch-up, then
// reconciles configured policies when canonical service/environment records
// change. A failed local read or publish retries with capped exponential
// backoff until it succeeds or shuts down; canonical events retry immediately.
type hiveCIPolicyHydrator struct {
	started      chan struct{}
	changed      chan struct{}
	startOnce    sync.Once
	stateMu      sync.Mutex
	ready        *atomic.Bool
	generation   atomic.Uint64
	resumeNeeded atomic.Bool
	activeCancel context.CancelFunc
	attempt      func(context.Context) error
	activate     func(context.Context, bool)
	retryAfter   func(time.Duration) <-chan time.Time
	logger       *zap.Logger
}

func newHiveCIPolicyHydrator(ready *atomic.Bool, attempt func(context.Context) error, activate func(context.Context, bool), logger *zap.Logger) *hiveCIPolicyHydrator {
	if logger == nil {
		logger = zap.NewNop()
	}
	h := &hiveCIPolicyHydrator{
		started: make(chan struct{}), changed: make(chan struct{}, 1), ready: ready,
		attempt: attempt, activate: activate, retryAfter: time.After, logger: logger.Named("hiveci-policy-hydrator"),
	}
	h.resumeNeeded.Store(true)
	return h
}

func (*hiveCIPolicyHydrator) Name() string { return "hiveci-policy-hydrator" }

func (h *hiveCIPolicyHydrator) Start() {
	h.startOnce.Do(func() { close(h.started) })
}

func (h *hiveCIPolicyHydrator) Signal() {
	if h == nil {
		return
	}
	h.stateMu.Lock()
	h.ready.Store(false)
	h.resumeNeeded.Store(true)
	h.generation.Add(1)
	if h.activeCancel != nil {
		h.activeCancel()
	}
	h.stateMu.Unlock()
	select {
	case h.changed <- struct{}{}:
	default:
	}
}

// ObserveEntity is called only after inbound event validation and persistence,
// or after local outbox delivery. It never performs repository or relay I/O.
func (h *hiveCIPolicyHydrator) ObserveEntity(ev *nostr.Event, servicePubkey string) {
	if h == nil || ev == nil || int(ev.Kind) != kinds.CASControlState || ev.PubKey.Hex() != servicePubkey {
		return
	}
	var schema, topic string
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case kinds.CASControlStateTagSchema:
			schema = tag[1]
		case "t":
			if tag[1] == kinds.CPStateTopicServiceRegistry || tag[1] == kinds.CPStateTopicEnvironmentRegistry {
				topic = tag[1]
			}
		}
	}
	if schema == kinds.CASControlStateSchema && topic != "" {
		h.Signal()
	}
}

func (h *hiveCIPolicyHydrator) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-h.started:
	}
	var retry <-chan time.Time
	attempts := 0
	pending := true
	for {
		if pending {
			generation := h.generation.Load()
			if err := h.attempt(ctx); err != nil {
				h.ready.Store(false)
				h.resumeNeeded.Store(true)
				attempts++
				h.logger.Warn("canonical Hive-CI policy hydration failed", zap.Int("attempt", attempts), zap.Error(err))
				shift := attempts - 1
				if shift > hiveCIPolicyMaxRetryShift {
					shift = hiveCIPolicyMaxRetryShift
				}
				retry = h.retryAfter(time.Second << shift)
			} else {
				attempts = 0
				retry = nil
				h.stateMu.Lock()
				if h.generation.Load() == generation {
					activationCtx, cancel := context.WithCancel(ctx)
					h.activeCancel = cancel
					resume := h.resumeNeeded.Swap(false)
					h.ready.Store(true)
					h.stateMu.Unlock()
					if h.activate != nil {
						h.activate(activationCtx, resume)
					}
					h.stateMu.Lock()
					h.activeCancel = nil
					h.stateMu.Unlock()
					cancel()
				} else {
					h.stateMu.Unlock()
				}
			}
			pending = false
		}
		select {
		case <-ctx.Done():
			return nil
		case <-h.changed:
			h.ready.Store(false)
			attempts = 0
			retry = nil
			pending = true
		case <-retry:
			retry = nil
			pending = true
		}
	}
}
