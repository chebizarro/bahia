package controlplane

import (
	"context"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"go.uber.org/zap"
)

// IntentSubscriber opens a long-lived subscription for kind-30900 events
// tagged with t=bahia-intent, scoped to trusted author pubkeys from the
// TrustSet. It uses Phase 2's ProcessSync for relay-independent catch-up,
// per-relay cursors, and NIP-77 reconciliation.
//
// When the trust set changes, the subscriber restarts with an updated authors
// list. A 5-second debounce prevents churn from rapid membership changes.
//
// See design §3.1.
type IntentSubscriber struct {
	pool      *nostrAdapter.RelayPool
	store     *localstore.Store
	trustSet  *TrustSet
	processor *IntentProcessor
	readiness *ReadinessTracker
	logger    *zap.Logger
	// selfPubkey is the daemon's own pubkey, excluded from processing.
	selfPubkey string
	// syncConfig tunes catch-up behavior.
	syncConfig nostrAdapter.InboundSyncConfig

	mu        sync.Mutex
	cancelFn  context.CancelFunc
	running   bool
	caughtUp  bool
	filterKey string // for ReadinessTracker
}

// NewIntentSubscriber creates an intent subscriber. The subscriber does not
// start until Run is called.
func NewIntentSubscriber(
	pool *nostrAdapter.RelayPool,
	store *localstore.Store,
	trustSet *TrustSet,
	processor *IntentProcessor,
	readiness *ReadinessTracker,
	selfPubkey string,
	logger *zap.Logger,
) *IntentSubscriber {
	return &IntentSubscriber{
		pool:       pool,
		store:      store,
		trustSet:   trustSet,
		processor:  processor,
		readiness:  readiness,
		selfPubkey: selfPubkey,
		logger:     logger.Named("intent-subscriber"),
		syncConfig: nostrAdapter.DefaultInboundSyncConfig(),
		filterKey:  "intent-30900",
	}
}

// Name implements app.BackgroundRunner.
func (s *IntentSubscriber) Name() string { return "intent-subscriber" }

// Run starts the subscription. It blocks until ctx is cancelled. When the
// trust set changes, the subscription is restarted with updated authors.
func (s *IntentSubscriber) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	return s.runLoop(ctx)
}

func (s *IntentSubscriber) runLoop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		filter := s.buildFilter()
		if filter.Kinds == nil {
			// No kinds means nothing to subscribe to.
			s.logger.Warn("intent subscriber has no filter, waiting")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Second):
				continue
			}
		}

		subCtx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		s.cancelFn = cancel
		s.mu.Unlock()

		sync := &nostrAdapter.ProcessSync{
			Pool:   s.pool,
			Store:  s.store,
			Logger: s.logger,
			Config: s.syncConfig,
			Apply:  s.applyEvent,
			CaughtUp: func() {
				s.mu.Lock()
				s.caughtUp = true
				s.mu.Unlock()
				if s.readiness != nil {
					s.readiness.MarkFilterReady(s.filterKey)
				}
				s.logger.Info("intent subscriber caught up")
			},
		}

		err := sync.Run(subCtx, []nostr.Filter{filter})
		cancel()

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.logger.Warn("intent subscription ended, restarting",
				zap.Error(err),
			)
		}

		// Brief pause before restart.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *IntentSubscriber) buildFilter() nostr.Filter {
	filter := nostr.Filter{
		Kinds: []nostr.Kind{30900},
		Tags:  nostr.TagMap{"t": {"bahia-intent"}},
	}

	// Author-scoped subscription (§3.1, anti-amplification).
	// When Postgres is the only trust source, we cannot enumerate all
	// members, so we use an open subscription and rely on the processor
	// to check permissions.
	if !s.trustSet.HasPostgres() {
		pubkeys := s.trustSet.AuthorPubkeys()
		authors := make([]nostr.PubKey, 0, len(pubkeys))
		for _, pk := range pubkeys {
			if parsed, err := nostr.PubKeyFromHex(pk); err == nil {
				authors = append(authors, parsed)
			}
		}
		if len(authors) == 0 {
			// An empty Authors list means every author in NIP-01. Scope the
			// cold-start REQ to our own key instead: it yields EOSE (and
			// readiness) without downloading unrelated operators' intents.
			if self, err := nostr.PubKeyFromHex(s.selfPubkey); err == nil {
				authors = append(authors, self)
			}
		}
		if len(authors) == 0 {
			filter.Kinds = nil // never issue an unscoped REQ
		}
		filter.Authors = authors
	}

	return filter
}

func (s *IntentSubscriber) applyEvent(ctx context.Context, ev *nostr.Event) {
	// Skip our own events.
	if ev.PubKey.Hex() == s.selfPubkey {
		return
	}

	// Drop events from untrusted authors silently (§2.3).
	// This is a second check after the author-scoped subscription filter.
	// Events may arrive despite the filter during trust-set transitions.
	actor := ev.PubKey.Hex()
	if !s.trustSet.IsKnownPrincipal(actor) {
		// When Postgres is configured, unknown principals might still be
		// valid org members. The processor will check HasPermission.
		if !s.trustSet.HasPostgres() {
			s.logger.Debug("dropping intent from untrusted author (post-filter)",
				zap.String("pubkey", actor),
				zap.String("event_id", ev.ID.Hex()),
			)
			return
		}
	}

	if err := s.processor.ProcessRelayIntent(ctx, ev); err != nil {
		s.logger.Debug("intent processing returned error",
			zap.String("event_id", ev.ID.Hex()),
			zap.Error(err),
		)
	}
}

// IsCaughtUp reports whether the first catch-up has completed.
func (s *IntentSubscriber) IsCaughtUp() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caughtUp
}
