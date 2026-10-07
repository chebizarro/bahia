package nostr

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
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
var DefaultInboundKinds = []int{
	// Canonical Bahia observables and config-fabric desired state.
	kinds.ConfigACLList,
	kinds.ConfigPolicy,
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

// Subscriber keeps the daemon's inbound subscriptions in sync with every relay
// in its pool. It implements app.BackgroundRunner.
//
// Each relay is synced independently, so a relay that is down or behind never
// holds back, or is hidden by, the others:
//   - catch-up: each replaceable/addressable filter is reconciled in full with
//     NIP-77, falling back to paged REQs when the relay refuses (NEG-ERR) or
//     does not speak NIP-77; each regular-kind filter is paged from that
//     relay's cursor less the overlap (from a lookback window on a fresh
//     node, never from "now"), backwards with `until` whenever a page
//     comes back full, so a large gap is never truncated;
//   - live: one REQ per filter from the catch-up start less the overlap.
//
// A dropped relay is caught up again the same way on reconnect. Events from all
// relays go through one consumer, which deduplicates them by id against the
// local event store so replay after a restart does not depend on Postgres,
// and which keeps the per-(relay, filter) cursors (see
// replay_cursor.go).
//
// NIP-42 is answered by the pool's connection AuthHandler. A CLOSED ends the
// session and goes through the pool's CLOSED policy for that (relay, filter),
// as for the pool's own subscriptions (see runRelay): "auth-required:"
// authenticates and resyncs at once; a policy refusal, a failed AUTH or a
// retryable reason beyond nostr.closed_retry_budget in a row gives that filter
// up on that relay. A dropped connection is always resynced with backoff.
type Subscriber struct {
	pool *RelayPool
	// archive is the optional PostgreSQL nostr_events table, written best
	// effort: it never decides whether an event is new or whether handlers
	// run.
	archive                *postgresArchive
	store                  *localstore.Store
	kinds                  []int
	handlers               []EventHandler
	observers              []EventHandler
	logger                 *zap.Logger
	authorizedAuthorScopes AuthorizedAuthorScopes
	deletionAuthors        []string
	self                   map[nostr.PubKey]struct{}
	sync                   InboundSyncConfig
	now                    func() time.Time
	ingestionObservers     []IngestionObserver
	newRelayBackoff        func() *Backoff
	// trace, when set by tests, sees every consumed item after it is applied.
	trace func(inboundItem)

	// caughtUp is set once every relay has either finished its first
	// catch-up or failed its first attempt, and at least one finished.
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

// WithObserver adds an idempotent projection callback invoked for every
// validated event that is durably persisted, whether this delivery stored it
// or it was already held.
//
// Handlers registered with WithHandler run only for events that are new to the
// local store, and never for the daemon's own events (WithSelfAuthors), so side
// effects never repeat. Observers exist for read-side projections such as
// fleet-health telemetry that must see those self-published canonical
// observables. An observer must be idempotent under redelivery, for example by
// keeping only the latest event per replaceable coordinate.
func WithObserver(h EventHandler) SubscriberOption {
	return func(s *Subscriber) {
		if h != nil {
			s.observers = append(s.observers, h)
		}
	}
}

// WithIngestionObserver registers a subscription lifecycle observer.
func WithIngestionObserver(observer IngestionObserver) SubscriberOption {
	return func(s *Subscriber) {
		if observer != nil {
			s.ingestionObservers = append(s.ingestionObservers, observer)
		}
	}
}

// WithLocalStore sets the local event store that deduplicates inbound events
// and keeps their resume cursors. Run requires one.
func WithLocalStore(store *localstore.Store) SubscriberOption {
	return func(s *Subscriber) { s.store = store }
}

// WithSelfAuthors declares the daemon's own pubkeys. Their events reach
// observers but never handlers, and never advance an inbound cursor: the
// daemon's clock and publish timing say nothing about what other authors'
// events a relay has delivered.
func WithSelfAuthors(pubkeys ...string) SubscriberOption {
	return func(s *Subscriber) {
		for _, pubkey := range pubkeys {
			if parsed, err := nostr.PubKeyFromHex(pubkey); err == nil {
				s.self[parsed] = struct{}{}
			}
		}
	}
}

// WithDeletionAuthors also subscribes to NIP-09 deletion requests (kind 5)
// from these trusted authors. Deletions are persistent: they are reconciled in
// full on every (re)connect, then followed live. Pair it with an observer that
// applies them, such as Bootstrapper.ApplyDeletion.
func WithDeletionAuthors(pubkeys []string) SubscriberOption {
	return func(s *Subscriber) { s.deletionAuthors = cloneStrings(pubkeys) }
}

// WithInboundSync tunes catch-up (overlap, fresh-cursor lookback, NIP-77).
func WithInboundSync(cfg InboundSyncConfig) SubscriberOption {
	return func(s *Subscriber) { s.sync = cfg }
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

// NewSubscriber creates a new inbound event subscriber. eventRepo is the
// optional PostgreSQL nostr_events archive.
func NewSubscriber(
	pool *RelayPool,
	eventRepo repository.NostrEventRepository,
	logger *zap.Logger,
	opts ...SubscriberOption,
) *Subscriber {
	s := &Subscriber{
		pool:            pool,
		kinds:           DefaultInboundKinds,
		logger:          logger.Named("nostr-subscriber"),
		self:            make(map[nostr.PubKey]struct{}),
		sync:            DefaultInboundSyncConfig(),
		now:             func() time.Time { return time.Now().UTC() },
		newRelayBackoff: DefaultBackoff,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.archive = newPostgresArchive(eventRepo, s.logger)
	s.sync = s.sync.normalized()
	return s
}

// Name implements app.BackgroundRunner.
func (s *Subscriber) Name() string { return "nostr-subscriber" }

// IsCaughtUp reports whether the first catch-up has finished: every relay has
// either caught up or failed its first attempt, and at least one caught up.
func (s *Subscriber) IsCaughtUp() bool {
	return s.caughtUp.Load()
}

type ingestOutcome int

const (
	// ingestRejected: invalid or out of scope; never stored.
	ingestRejected ingestOutcome = iota
	// ingestFailed: valid, but storing it failed; it will be refetched.
	ingestFailed
	// ingestDuplicate: already held (or superseded); handlers did not run.
	ingestDuplicate
	// ingestNew: stored for the first time.
	ingestNew
)

// handleEvent stores a delivered event and dispatches it. The local store is
// the idempotency gate: handlers run only for an event new to it, so neither
// overlap replay, nor another relay's copy, nor a restart re-runs side
// effects. The PostgreSQL archive is written best effort and decides nothing,
// so a database outage does not make the daemon deaf to its relays.
func (s *Subscriber) handleEvent(ctx context.Context, ev *nostr.Event) ingestOutcome {
	if err := ValidateInboundEvent(ev, s.now(), InboundEventMaxFutureSkew); err != nil {
		eventID := ""
		if ev != nil {
			eventID = eventIDHex(ev)
		}
		s.logger.Warn("dropping invalid inbound event before persistence",
			zap.String("event_id", eventID),
			zap.Error(err),
		)
		return ingestRejected
	}
	if isLegacyProductionRuntimeKind(eventKindInt(ev)) {
		s.logger.Warn("dropping legacy inbound event after migration boundary",
			zap.String("event_id", eventIDHex(ev)),
			zap.Int("kind", eventKindInt(ev)),
		)
		return ingestRejected
	}
	ctx = telemetry.ExtractTraceContext(ctx, ev.Tags)

	fresh := true
	if s.store != nil {
		stored, err := s.store.SaveEvent(*ev)
		if err != nil {
			s.logger.Warn("failed to store inbound event locally",
				zap.String("event_id", eventIDHex(ev)),
				zap.Int("kind", eventKindInt(ev)),
				zap.Error(err),
			)
			return ingestFailed
		}
		fresh = stored
	}
	if fresh {
		s.archive.write(ctx, "inbound event", eventIDHex(ev), func(ctx context.Context, repo repository.NostrEventRepository) error {
			_, err := repo.Record(ctx, s.auditRecord(ev))
			return err
		})
	}
	// Observers see validated, persisted events whether or not they are new;
	// see WithObserver for why self-published echoes must reach them.
	for _, observe := range s.observers {
		observe(ctx, ev)
	}
	if !fresh {
		s.logger.Debug("skipping already-persisted event",
			zap.String("event_id", eventIDHex(ev)),
			zap.Int("kind", eventKindInt(ev)),
		)
		return ingestDuplicate
	}
	if _, own := s.self[ev.PubKey]; own {
		return ingestNew
	}
	s.logger.Debug("inbound event persisted",
		zap.String("event_id", eventIDHex(ev)),
		zap.Int("kind", eventKindInt(ev)),
		zap.String("pubkey", eventPubKeyHex(ev)),
	)
	for _, h := range s.handlers {
		h(ctx, ev)
	}
	return ingestNew
}

func (s *Subscriber) auditRecord(ev *nostr.Event) *repository.NostrEventRecord {
	tagsJSON, err := json.Marshal(ev.Tags)
	if err != nil {
		s.logger.Warn("failed to marshal event tags",
			zap.String("event_id", eventIDHex(ev)),
			zap.Error(err),
		)
		tagsJSON = []byte("[]")
	}
	return &repository.NostrEventRecord{
		ID:         eventIDHex(ev),
		Kind:       eventKindInt(ev),
		PubKey:     eventPubKeyHex(ev),
		Content:    ev.Content,
		Tags:       tagsJSON,
		Sig:        eventSignatureHex(ev),
		CreatedAt:  ev.CreatedAt.Time(),
		ReceivedAt: time.Now().UTC(),
	}
}

// buildSubscriptionFilters groups the subscribed kinds by author scope and
// splits each group into its replaceable/addressable and regular kinds.
func (s *Subscriber) buildSubscriptionFilters() ([]inboundFilter, error) {
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

	var filters []inboundFilter
	addFilter := func(kinds []int, authors []string) error {
		if len(kinds) == 0 {
			return nil
		}
		filter := nostr.Filter{Kinds: filterKindsFromInts(kinds)}
		if len(authors) > 0 {
			converted, err := filterAuthorsFromHex(authors)
			if err != nil {
				return err
			}
			filter.Authors = converted
		}
		filters = append(filters, splitInboundFilter(filter)...)
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
	if len(s.deletionAuthors) > 0 {
		if err := addFilter([]int{int(nostr.KindDeletion)}, s.deletionAuthors); err != nil {
			return nil, err
		}
	}
	return filters, nil
}

func kindsToInts(kinds []nostr.Kind) []int {
	out := make([]int, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, int(kind))
	}
	return out
}

func isCanonicalControlPlaneRequest(kind int) bool {
	return kind == kinds.ContextVMMessage || kind == kinds.ContextVMGiftWrap || kind == kinds.ContextVMEphemeralGiftWrap
}

func isDefaultAuthorScopedInboundKind(kind int) bool {
	return kind == kinds.ConfigACLList || kind == kinds.ConfigPolicy
}

func isDirectRuntimeScopedInboundKind(kind int) bool {
	return false
}

func isAdoptionScopedInboundKind(kind int) bool {
	return false
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
