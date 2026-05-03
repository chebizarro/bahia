package nostr

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Inbound event kinds the subscriber listens for.
var DefaultInboundKinds = []int{
	// Hive-CI protocol kinds.
	5401, // Hive-CI workflow run request
	5402, // Hive-CI workflow result

	// Loom protocol kinds.
	10100, // Worker Advertisement
	30100, // Job Status Update
	5101,  // Job Result
	5102,  // Job Cancellation

	// Bahia command kinds (31100-31105) — registered by Phase 1 command definitions.
	31100, 31101, 31102, 31103, 31104, 31105,

	// Canonical Bahia control-plane request kinds. These are audited here only;
	// the controlplane.Reactor remains the handler of record.
	5961, 5962, 5963, 5964, 5965, 5966, 5967, 5968,
	5976, // Tool provisioning request
	7977, // Tool approval response
}

// EventHandler is called for each inbound event after persistence.
// Implementations should be non-blocking; heavy processing should be
// dispatched asynchronously.
type EventHandler func(ctx context.Context, ev *nostr.Event)

// Subscriber connects to Nostr relays and persists inbound events
// to the nostr_events audit table. It implements app.BackgroundRunner.
type Subscriber struct {
	pool              *RelayPool
	eventRepo         repository.NostrEventRepository
	kinds             []int
	handlers          []EventHandler
	logger            *zap.Logger
	dedup             *EventDeduplicator
	backfillLimit     int // max events to fetch on catch-up (0 = no limit)
	authorizedAuthors []string
	now               func() time.Time

	// lastSeenByKind tracks newest created_at values processed in this process.
	lastSeenMu     sync.Mutex
	lastSeenByKind map[int]int64

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

// WithAuthorizedAuthors scopes Bahia command subscriptions to known operator pubkeys.
func WithAuthorizedAuthors(pubkeys []string) SubscriberOption {
	return func(s *Subscriber) {
		s.authorizedAuthors = append([]string(nil), pubkeys...)
	}
}

func withClock(now func() time.Time) SubscriberOption {
	return func(s *Subscriber) {
		if now != nil {
			s.now = now
		}
	}
}

// NewSubscriber creates a new inbound event subscriber.
func NewSubscriber(
	pool *RelayPool,
	eventRepo repository.NostrEventRepository,
	logger *zap.Logger,
	opts ...SubscriberOption,
) *Subscriber {
	s := &Subscriber{
		pool:           pool,
		eventRepo:      eventRepo,
		kinds:          DefaultInboundKinds,
		logger:         logger.Named("nostr-subscriber"),
		dedup:          NewEventDeduplicator(10000), // Default: track last 10k events
		backfillLimit:  1000,                        // Default: limit catch-up to 1000 events
		now:            func() time.Time { return time.Now().UTC() },
		lastSeenByKind: make(map[int]int64),
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

	filters, err := s.buildSubscriptionFilters(ctx)
	if err != nil {
		return err
	}

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
			s.handleEOSE()
			merged.EndOfStoredEvents = nil
		case ev, ok := <-merged.Events:
			if !ok {
				return nil // channel closed
			}
			s.handleEvent(ctx, ev)
		}
	}
}

func (s *Subscriber) handleEOSE() {
	if !s.caughtUp.Load() {
		s.caughtUp.Store(true)
		s.logger.Info("EOSE received: caught up with stored events",
			zap.Ints("kinds", s.kinds),
		)
	}
}

// handleEvent persists the event and invokes registered handlers.
// Repository insert state gates handlers so overlap backfill and multi-relay
// duplicates cannot re-run side effects across process restarts.
func (s *Subscriber) handleEvent(ctx context.Context, ev *nostr.Event) {
	if ev == nil {
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

	inserted, err := s.eventRepo.Record(ctx, rec)
	if err != nil {
		s.logger.Warn("failed to persist inbound event",
			zap.String("event_id", ev.ID),
			zap.Int("kind", ev.Kind),
			zap.Error(err),
		)
		return
	}
	if !inserted {
		s.logger.Debug("skipping already-persisted event",
			zap.String("event_id", ev.ID),
			zap.Int("kind", ev.Kind),
		)
		return
	}

	s.recordLastSeen(ev.Kind, ev.CreatedAt.Time())
	s.dedup.MarkSeen(ev.ID)
	s.logger.Debug("inbound event persisted",
		zap.String("event_id", ev.ID),
		zap.Int("kind", ev.Kind),
		zap.String("pubkey", ev.PubKey),
	)

	if isCanonicalControlPlaneRequest(ev.Kind) {
		return
	}

	// Invoke handlers - only for non-duplicate events.
	for _, h := range s.handlers {
		h(ctx, ev)
	}
}

func (s *Subscriber) buildSubscriptionFilters(ctx context.Context) ([]nostr.Filter, error) {
	openKinds := make([]int, 0, len(s.kinds))
	authorScopedKinds := make([]int, 0, len(s.kinds))
	for _, kind := range s.kinds {
		if len(s.authorizedAuthors) > 0 && isAuthorScopedInboundKind(kind) {
			authorScopedKinds = append(authorScopedKinds, kind)
			continue
		}
		openKinds = append(openKinds, kind)
	}

	filters := make([]nostr.Filter, 0, 2)
	if len(openKinds) > 0 {
		since, err := s.subscriptionSince(ctx, openKinds, nil)
		if err != nil {
			return nil, err
		}
		filters = append(filters, s.filterForKinds(openKinds, since, nil))
	}
	if len(authorScopedKinds) > 0 {
		since, err := s.subscriptionSince(ctx, authorScopedKinds, s.authorizedAuthors)
		if err != nil {
			return nil, err
		}
		filters = append(filters, s.filterForKinds(authorScopedKinds, since, s.authorizedAuthors))
	}
	return filters, nil
}

func (s *Subscriber) filterForKinds(kinds []int, since *nostr.Timestamp, authors []string) nostr.Filter {
	filter := nostr.Filter{Kinds: append([]int(nil), kinds...), Since: since}
	if len(authors) > 0 {
		filter.Authors = append([]string(nil), authors...)
	}
	if s.backfillLimit > 0 {
		filter.Limit = s.backfillLimit
	}
	return filter
}

func (s *Subscriber) subscriptionSince(ctx context.Context, kinds []int, authors []string) (*nostr.Timestamp, error) {
	cursorUnix := s.latestSeenForKinds(kinds)
	if s.eventRepo != nil {
		var latest *time.Time
		var err error
		if len(authors) > 0 {
			latest, err = s.eventRepo.LatestCreatedAtForKindsAndAuthors(ctx, kinds, authors)
		} else {
			latest, err = s.eventRepo.LatestCreatedAtForKinds(ctx, kinds)
		}
		if err != nil {
			return nil, err
		}
		if latest != nil && latest.Unix() > cursorUnix {
			cursorUnix = latest.Unix()
		}
	}

	if cursorUnix == 0 {
		return timestampFromTime(s.now()), nil
	}

	// Nostr timestamps are second-resolution. Overlap by one second so reconnects
	// replay the disconnect boundary, then suppress duplicates via repository insert state.
	return timestampFromUnix(cursorUnix - 1), nil
}

func (s *Subscriber) latestSeenForKinds(kinds []int) int64 {
	s.lastSeenMu.Lock()
	defer s.lastSeenMu.Unlock()

	var latest int64
	for _, kind := range kinds {
		if seen := s.lastSeenByKind[kind]; seen > latest {
			latest = seen
		}
	}
	return latest
}

func (s *Subscriber) recordLastSeen(kind int, createdAt time.Time) {
	unix := createdAt.Unix()
	s.lastSeenMu.Lock()
	defer s.lastSeenMu.Unlock()
	if unix > s.lastSeenByKind[kind] {
		s.lastSeenByKind[kind] = unix
	}
}

func isCanonicalControlPlaneRequest(kind int) bool {
	return (kind >= 5961 && kind <= 5968) || kind == 5976 || kind == 7977
}

func isAuthorScopedInboundKind(kind int) bool {
	return (kind >= 5961 && kind <= 5968) || kind == 5976 || kind == 7977 || (kind >= 31100 && kind <= 31105)
}

func timestampFromTime(t time.Time) *nostr.Timestamp {
	return timestampFromUnix(t.Unix())
}

func timestampFromUnix(unix int64) *nostr.Timestamp {
	ts := nostr.Timestamp(unix)
	return &ts
}
