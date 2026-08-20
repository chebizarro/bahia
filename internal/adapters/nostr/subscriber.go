package nostr

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Inbound event kinds the subscriber listens for.
//
// Protocol boundary:
//   - Control-plane request/response kinds are owned and audited by the reactor.
//   - The subscriber tracks non-reactor operational streams: worker catalog updates,
//     Hive-CI/Loom events, and assistant relay status/result events.
//   - Legacy NIP-90 kind 5900 belongs to the old upstream dvm-cicd-runner path and is
//     not part of this subscriber contract.
var DefaultInboundKinds = []int{
	// Canonical Bahia observables.
	KindCASControlState,
	KindCASAudit,
	KindNIP38Status,
	kinds.AssistantTranscript,
	kinds.SoulFactoryRuntimeCapability,
	kinds.ContextVMToolsList,
	kinds.ContextVMResourcesList,
	kinds.ContextVMResourceTemplatesList,
	kinds.ContextVMPromptsList,
	KindRelaySetDiscovery,
	KindNIP65RelayList,

	// Hive-CI protocol kinds.
	KindHiveCIWorkflowRun,
	KindHiveCIWorkflowResult,

	// Loom protocol kinds.
	KindLoomWorkerAdvertisement,
	KindLoomJobStatusUpdate,
	KindLoomJobResult,
	KindLoomJobCancellation,
}

// EventHandler is called for each inbound event after persistence.
// Implementations should be non-blocking; heavy processing should be
// dispatched asynchronously.
type EventHandler func(ctx context.Context, ev *nostr.Event)

// IngestionObserver receives subscription lifecycle signals after transport
// handling. It lets projections distinguish relay/projector availability from
// the health of subjects represented by events.
type IngestionObserver interface {
	ObserveSubscriptionStart()
	ObserveSubscriptionEnd()
	ObserveEOSE()
	ObserveRelayClosed(relayURL, reason string)
}

// Subscriber connects to Nostr relays and persists inbound events
// to the nostr_events audit table. It implements app.BackgroundRunner.
type Subscriber struct {
	pool                   *RelayPool
	eventRepo              repository.NostrEventRepository
	kinds                  []int
	handlers               []EventHandler
	logger                 *zap.Logger
	dedup                  *EventDeduplicator
	backfillLimit          int // max events to fetch on catch-up (0 = no limit)
	authorizedAuthorScopes AuthorizedAuthorScopes
	now                    func() time.Time
	ingestionObservers     []IngestionObserver

	// lastSeenByKind tracks newest created_at values processed in this process.
	lastSeenMu     sync.Mutex
	lastSeenByKind map[int]int64

	// caughtUp indicates whether EOSE has been received (caught up with stored events).
	caughtUp atomic.Bool
}

// AuthorizedAuthorScopes configures operator pubkeys by control-plane scope.
type AuthorizedAuthorScopes struct {
	Default       []string
	Adoption      []string
	DirectRuntime []string
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

// WithIngestionObserver registers a subscription lifecycle observer.
func WithIngestionObserver(observer IngestionObserver) SubscriberOption {
	return func(s *Subscriber) {
		if observer != nil {
			s.ingestionObservers = append(s.ingestionObservers, observer)
		}
	}
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

// WithAuthorizedAuthors scopes default Bahia command subscriptions to known operator pubkeys.
func WithAuthorizedAuthors(pubkeys []string) SubscriberOption {
	return WithAuthorizedAuthorScopes(AuthorizedAuthorScopes{Default: pubkeys})
}

// WithAuthorizedAuthorScopes scopes Bahia command subscriptions by operator capability.
func WithAuthorizedAuthorScopes(scopes AuthorizedAuthorScopes) SubscriberOption {
	return func(s *Subscriber) {
		s.authorizedAuthorScopes = AuthorizedAuthorScopes{
			Default:       cloneStrings(scopes.Default),
			Adoption:      cloneStrings(scopes.Adoption),
			DirectRuntime: cloneStrings(scopes.DirectRuntime),
		}
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
		err := s.subscribe(ctx, backoff)
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
		s.pool.RecordRelayReREQ()
	}
}

// IsCaughtUp returns true if EOSE has been received from all relays.
func (s *Subscriber) IsCaughtUp() bool {
	return s.caughtUp.Load()
}

// subscribe opens a subscription to all relays and processes events.
func (s *Subscriber) subscribe(ctx context.Context, backoff *Backoff) error {
	// Reset caught-up state on new subscription.
	s.caughtUp.Store(false)
	for _, observer := range s.ingestionObservers {
		observer.ObserveSubscriptionStart()
	}
	defer func() {
		for _, observer := range s.ingestionObservers {
			observer.ObserveSubscriptionEnd()
		}
	}()

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

	authAttempted := make(map[string]struct{})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case eose, ok := <-merged.RelayEOSE:
			if ok {
				s.handleRelayEOSE(eose)
			} else {
				merged.RelayEOSE = nil
			}
		case closed, ok := <-merged.Closed:
			if ok {
				if s.handleRelayClosed(ctx, closed, authAttempted) {
					merged.Close()
					return nil
				}
			} else {
				merged.Closed = nil
			}
		case <-merged.EndOfStoredEvents:
			s.handleEOSE(backoff)
			merged.EndOfStoredEvents = nil
		case ev, ok := <-merged.Events:
			if !ok {
				return nil // channel closed
			}
			s.handleEvent(ctx, ev)
		}
	}
}

func (s *Subscriber) handleRelayEOSE(eose RelayEOSE) {
	s.logger.Debug("relay sent EOSE",
		zap.String("relay", eose.RelayURL),
		zap.String("subscription_id", eose.SubscriptionID),
		zap.Ints("kinds", s.kinds),
	)
}

func (s *Subscriber) handleEOSE(backoff *Backoff) {
	if backoff != nil {
		backoff.Reset()
	}
	if !s.caughtUp.Load() {
		s.caughtUp.Store(true)
		for _, observer := range s.ingestionObservers {
			observer.ObserveEOSE()
		}
		s.logger.Info("EOSE received: caught up with stored events",
			zap.Ints("kinds", s.kinds),
		)
	}
}

func (s *Subscriber) handleRelayClosed(ctx context.Context, closed RelayClosed, authAttempted map[string]struct{}) bool {
	if s.pool != nil {
		s.pool.RecordRelayClosed(closed.RelayURL, closed.Reason)
	}
	s.logger.Warn("relay closed subscription",
		zap.String("relay", closed.RelayURL),
		zap.String("subscription_id", closed.SubscriptionID),
		zap.String("reason", closed.Reason),
	)
	for _, observer := range s.ingestionObservers {
		observer.ObserveRelayClosed(closed.RelayURL, closed.Reason)
	}
	if !IsAuthRequiredReason(closed.Reason) || closed.RelayURL == "" || s.pool == nil {
		return false
	}
	if _, ok := authAttempted[closed.RelayURL]; ok {
		return false
	}
	authAttempted[closed.RelayURL] = struct{}{}
	if err := s.pool.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
		s.pool.RecordRelayError(closed.RelayURL, "auth-unavailable: "+closed.Reason+": "+err.Error())
		s.logger.Warn("relay subscription auth failed",
			zap.String("relay", closed.RelayURL),
			zap.String("reason", closed.Reason),
			zap.Error(err),
		)
		return false
	}
	return true
}

// handleEvent persists the event and invokes registered handlers.
// Repository insert state gates handlers so overlap backfill and multi-relay
// duplicates cannot re-run side effects across process restarts.
func (s *Subscriber) handleEvent(ctx context.Context, ev *nostr.Event) {
	if err := ValidateInboundEvent(ev, s.now(), InboundEventMaxFutureSkew); err != nil {
		eventID := ""
		if ev != nil {
			eventID = eventIDHex(ev)
		}
		s.logger.Warn("dropping invalid inbound event before persistence",
			zap.String("event_id", eventID),
			zap.Error(err),
		)
		return
	}
	if isLegacyProductionRuntimeKind(eventKindInt(ev)) {
		s.logger.Warn("dropping legacy inbound event after migration boundary",
			zap.String("event_id", eventIDHex(ev)),
			zap.Int("kind", eventKindInt(ev)),
		)
		return
	}

	// Serialize tags.
	tagsJSON, err := json.Marshal(ev.Tags)
	if err != nil {
		s.logger.Warn("failed to marshal event tags",
			zap.String("event_id", eventIDHex(ev)),
			zap.Error(err),
		)
		tagsJSON = []byte("[]")
	}

	rec := &repository.NostrEventRecord{
		ID:         eventIDHex(ev),
		Kind:       eventKindInt(ev),
		PubKey:     eventPubKeyHex(ev),
		Content:    ev.Content,
		Tags:       tagsJSON,
		Sig:        eventSignatureHex(ev),
		CreatedAt:  ev.CreatedAt.Time(),
		ReceivedAt: time.Now().UTC(),
	}

	inserted, err := s.eventRepo.Record(ctx, rec)
	if err != nil {
		s.logger.Warn("failed to persist inbound event",
			zap.String("event_id", eventIDHex(ev)),
			zap.Int("kind", eventKindInt(ev)),
			zap.Error(err),
		)
		return
	}
	if !inserted {
		s.logger.Debug("skipping already-persisted event",
			zap.String("event_id", eventIDHex(ev)),
			zap.Int("kind", eventKindInt(ev)),
		)
		return
	}

	s.recordLastSeen(eventKindInt(ev), ev.CreatedAt.Time())
	s.dedup.MarkSeen(eventIDHex(ev))
	s.logger.Debug("inbound event persisted",
		zap.String("event_id", eventIDHex(ev)),
		zap.Int("kind", eventKindInt(ev)),
		zap.String("pubkey", eventPubKeyHex(ev)),
	)

	// Invoke handlers - only for non-duplicate events.
	for _, h := range s.handlers {
		h(ctx, ev)
	}
}

func (s *Subscriber) buildSubscriptionFilters(ctx context.Context) ([]nostr.Filter, error) {
	var openKinds []int
	var defaultKinds []int
	var directRuntimeKinds []int
	var adoptionKinds []int

	for _, kind := range s.kinds {
		if isLegacyProductionRuntimeKind(kind) {
			continue
		}
		switch {
		case isDirectRuntimeScopedInboundKind(kind):
			directRuntimeKinds = append(directRuntimeKinds, kind)
		case isAdoptionScopedInboundKind(kind):
			adoptionKinds = append(adoptionKinds, kind)
		case isDefaultAuthorScopedInboundKind(kind):
			defaultKinds = append(defaultKinds, kind)
		default:
			openKinds = append(openKinds, kind)
		}
	}

	filters := make([]nostr.Filter, 0, 4)
	addFilter := func(kinds []int, authors []string) error {
		if len(kinds) == 0 {
			return nil
		}
		since, err := s.subscriptionSince(ctx, kinds, authors)
		if err != nil {
			return err
		}
		filter, err := s.filterForKinds(kinds, since, authors)
		if err != nil {
			return err
		}
		filters = append(filters, filter)
		return nil
	}

	if err := addFilter(openKinds, nil); err != nil {
		return nil, err
	}
	if err := addFilter(defaultKinds, s.authorizedAuthorScopes.Default); err != nil {
		return nil, err
	}
	if err := addFilter(directRuntimeKinds, combineAuthors(s.authorizedAuthorScopes.Default, s.authorizedAuthorScopes.DirectRuntime)); err != nil {
		return nil, err
	}
	if err := addFilter(adoptionKinds, combineAuthors(s.authorizedAuthorScopes.Default, s.authorizedAuthorScopes.Adoption)); err != nil {
		return nil, err
	}
	return filters, nil
}

func (s *Subscriber) filterForKinds(kinds []int, since nostr.Timestamp, authors []string) (nostr.Filter, error) {
	filter := nostr.Filter{Kinds: filterKindsFromInts(kinds), Since: since}
	if len(authors) > 0 {
		converted, err := filterAuthorsFromHex(authors)
		if err != nil {
			return nostr.Filter{}, err
		}
		filter.Authors = converted
	}
	if s.backfillLimit > 0 {
		filter.Limit = s.backfillLimit
	}
	return filter, nil
}

func (s *Subscriber) subscriptionSince(ctx context.Context, kinds []int, authors []string) (nostr.Timestamp, error) {
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
			return 0, err
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
	return kind == kinds.ContextVMMessage || kind == kinds.ContextVMGiftWrap || kind == kinds.ContextVMEphemeralGiftWrap
}

func isDefaultAuthorScopedInboundKind(kind int) bool {
	return false
}

func isDirectRuntimeScopedInboundKind(kind int) bool {
	return false
}

func isAdoptionScopedInboundKind(kind int) bool {
	return false
}

func isControlPlaneRequestKind(kind int) bool {
	return isCanonicalControlPlaneRequest(kind)
}

func isLegacyProductionRuntimeKind(kind int) bool {
	return (kind >= 5941 && kind <= 5999) ||
		(kind >= 6961 && kind <= 6999) ||
		(kind >= 7961 && kind <= 7999) ||
		(kind >= 31100 && kind <= 31399) ||
		(kind >= 31900 && kind <= 32099) ||
		(kind >= 38390 && kind <= 38499)
}

func combineAuthors(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var authors []string
	for _, group := range groups {
		for _, author := range group {
			if author == "" {
				continue
			}
			if _, ok := seen[author]; ok {
				continue
			}
			seen[author] = struct{}{}
			authors = append(authors, author)
		}
	}
	return authors
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func timestampFromTime(t time.Time) nostr.Timestamp {
	return timestampFromUnix(t.Unix())
}

func timestampFromUnix(unix int64) nostr.Timestamp {
	return nostr.Timestamp(unix)
}
