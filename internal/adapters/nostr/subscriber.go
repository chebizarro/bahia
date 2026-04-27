package nostr

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Inbound event kinds the subscriber listens for.
var DefaultInboundKinds = []int{
	// Hive-CI protocol kinds.
	5401,  // Hive-CI workflow run request
	5402,  // Hive-CI workflow result

	// Loom protocol kinds.
	10100, // Worker Advertisement
	30100, // Job Status Update
	5101,  // Job Result
	5102,  // Job Cancellation

	// Bahia command kinds (31100-31105) — registered by Phase 1 command definitions.
	31100, 31101, 31102, 31103, 31104, 31105,
}

// EventHandler is called for each inbound event after persistence.
// Implementations should be non-blocking; heavy processing should be
// dispatched asynchronously.
type EventHandler func(ctx context.Context, ev *nostr.Event)

// Subscriber connects to Nostr relays and persists inbound events
// to the nostr_events audit table. It implements app.BackgroundRunner.
type Subscriber struct {
	pool         *RelayPool
	eventRepo    repository.NostrEventRepository
	kinds        []int
	handlers     []EventHandler
	logger       *zap.Logger
	dedup        *EventDeduplicator
	backfillLimit int // max events to fetch on catch-up (0 = no limit)
	
	// caughtUp indicates whether EOSE has been received (caught up with stored events).
	caughtUp atomic.Bool
}

// SubscriberOption configures a Subscriber.
type SubscriberOption func(*Subscriber)

// WithKinds overrides the default set of inbound event kinds.
func WithKinds(kinds []int) SubscriberOption {
	return func(s *Subscriber) { s.kinds = kinds }
}

// WithHandler adds a callback invoked for each received event.
func WithHandler(h EventHandler) SubscriberOption {
	return func(s *Subscriber) { s.handlers = append(s.handlers, h) }
}

// WithDeduplicator sets a custom deduplicator. If not set, a default one is created.
func WithDeduplicator(d *EventDeduplicator) SubscriberOption {
	return func(s *Subscriber) { s.dedup = d }
}

// WithBackfillLimit sets the maximum number of events to fetch on catch-up.
// This prevents memory pressure after long disconnections.
// Default is 1000. Set to 0 for no limit (not recommended).
func WithBackfillLimit(limit int) SubscriberOption {
	return func(s *Subscriber) { s.backfillLimit = limit }
}

// NewSubscriber creates a new inbound event subscriber.
func NewSubscriber(
	pool *RelayPool,
	eventRepo repository.NostrEventRepository,
	logger *zap.Logger,
	opts ...SubscriberOption,
) *Subscriber {
	s := &Subscriber{
		pool:          pool,
		eventRepo:     eventRepo,
		kinds:         DefaultInboundKinds,
		logger:        logger.Named("nostr-subscriber"),
		dedup:         NewEventDeduplicator(10000), // Default: track last 10k events
		backfillLimit: 1000, // Default: limit catch-up to 1000 events
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Name implements app.BackgroundRunner.
func (s *Subscriber) Name() string { return "nostr-subscriber" }

// Run implements app.BackgroundRunner. It blocks until ctx is cancelled.
func (s *Subscriber) Run(ctx context.Context) error {
	backoff := DefaultBackoff()
	
	for {
		err := s.subscribe(ctx)
		if ctx.Err() != nil {
			return nil // clean shutdown
		}
		
		delay := backoff.Next()
		s.logger.Warn("subscription ended, reconnecting with backoff",
			zap.Error(err),
			zap.Duration("delay", delay),
			zap.Int("attempt", backoff.Attempt()),
		)
		
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// IsCaughtUp returns true if EOSE has been received from all relays.
func (s *Subscriber) IsCaughtUp() bool {
	return s.caughtUp.Load()
}

// subscribe opens a subscription to all relays and processes events.
func (s *Subscriber) subscribe(ctx context.Context) error {
	// Reset caught-up state on new subscription.
	s.caughtUp.Store(false)
	
	filter := nostr.Filter{
		Kinds: s.kinds,
		Since: nowTimestamp(),
	}
	
	// Apply backfill limit to prevent memory pressure on reconnect.
	if s.backfillLimit > 0 {
		filter.Limit = s.backfillLimit
	}
	
	filters := []nostr.Filter{filter}

	merged, err := s.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return err
	}

	s.logger.Info("subscribed to relays",
		zap.Ints("kinds", s.kinds),
		zap.Strings("relays", s.pool.URLs()),
	)
	
	// Note: Backoff should be reset externally after successful subscription.
	// The caller (Run) doesn't have visibility into this, but the subscription
	// will run until error, at which point backoff continues from where it was.

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-merged.EndOfStoredEvents:
			if !s.caughtUp.Load() {
				s.caughtUp.Store(true)
				s.logger.Info("EOSE received: caught up with stored events",
					zap.Ints("kinds", s.kinds),
				)
			}
		case ev, ok := <-merged.Events:
			if !ok {
				return nil // channel closed
			}
			s.handleEvent(ctx, ev)
		}
	}
}

// handleEvent persists the event and invokes registered handlers.
// Uses deduplication to prevent handlers from being invoked multiple times
// for the same event (which can happen with multi-relay delivery or reconnects).
func (s *Subscriber) handleEvent(ctx context.Context, ev *nostr.Event) {
	if ev == nil {
		return
	}

	// Check for duplicate - if we've already processed this event, skip handlers.
	// This prevents side effects from being triggered multiple times.
	if s.dedup.IsDuplicate(ev.ID) {
		s.logger.Debug("skipping duplicate event",
			zap.String("event_id", ev.ID),
			zap.Int("kind", ev.Kind),
		)
		return
	}

	// Serialize tags.
	tagsJSON, err := json.Marshal(ev.Tags)
	if err != nil {
		s.logger.Warn("failed to marshal event tags",
			zap.String("event_id", ev.ID),
			zap.Error(err),
		)
		tagsJSON = []byte("[]")
	}

	rec := &repository.NostrEventRecord{
		ID:         ev.ID,
		Kind:       ev.Kind,
		PubKey:     ev.PubKey,
		Content:    ev.Content,
		Tags:       tagsJSON,
		Sig:        ev.Sig,
		CreatedAt:  ev.CreatedAt.Time(),
		ReceivedAt: time.Now().UTC(),
	}

	// Persist (idempotent — duplicate IDs silently ignored).
	if err := s.eventRepo.Record(ctx, rec); err != nil {
		s.logger.Warn("failed to persist inbound event",
			zap.String("event_id", ev.ID),
			zap.Int("kind", ev.Kind),
			zap.Error(err),
		)
	} else {
		s.logger.Debug("inbound event persisted",
			zap.String("event_id", ev.ID),
			zap.Int("kind", ev.Kind),
			zap.String("pubkey", ev.PubKey),
		)
	}

	// Invoke handlers - only for non-duplicate events.
	for _, h := range s.handlers {
		h(ctx, ev)
	}
}

func nowTimestamp() *nostr.Timestamp {
	ts := nostr.Timestamp(time.Now().Unix())
	return &ts
}
