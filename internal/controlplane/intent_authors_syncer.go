package controlplane

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// IntentAuthorsSyncer keeps Bahia sidecar relays informed of which pubkeys may
// write intent events (kind 30900 + t=bahia-intent). On startup and whenever
// the TrustSet's principal set changes, the syncer pushes the current set to
// each configured sidecar target via the NIP-86 setintentauthors method.
//
// The push is idempotent: if the set has not changed since the last successful
// push to a target, it is skipped. On failure, retries use exponential backoff
// (1s → 2s → 4s → … capped at 30s) until the context is cancelled.
//
// Only Bahia-owned sidecar targets receive the push. The syncer never pushes to
// third-party relays.
//
// See design §7.1.
type IntentAuthorsSyncer struct {
	trustSet   *TrustSet
	admin      IntentAuthorsAdmin
	targetRefs []string // only bahia-owned sidecar refs
	logger     *zap.Logger

	notifyCh chan struct{}

	mu       sync.Mutex
	lastPush map[string]string // targetRef → sorted pubkeys fingerprint
}

// IntentAuthorsAdmin is the narrow interface the syncer needs from the NIP-86
// relay administration client. Both SetIntentAuthors (convenience) and the raw
// Call method are available; the syncer uses SetIntentAuthors.
type IntentAuthorsAdmin interface {
	SetIntentAuthors(ctx context.Context, targetRef string, pubkeys []string) error
}

// IntentAuthorsSyncerConfig holds construction-time configuration.
type IntentAuthorsSyncerConfig struct {
	TrustSet   *TrustSet
	Admin      IntentAuthorsAdmin
	TargetRefs []string // only bahia-owned sidecar target refs
	Logger     *zap.Logger
}

// NewIntentAuthorsSyncer creates a syncer. The syncer does not start until Run
// is called.
func NewIntentAuthorsSyncer(cfg IntentAuthorsSyncerConfig) *IntentAuthorsSyncer {
	return &IntentAuthorsSyncer{
		trustSet:   cfg.TrustSet,
		admin:      cfg.Admin,
		targetRefs: append([]string(nil), cfg.TargetRefs...),
		logger:     cfg.Logger.Named("intent-authors-syncer"),
		notifyCh:   make(chan struct{}, 1),
		lastPush:   make(map[string]string),
	}
}

func (s *IntentAuthorsSyncer) Name() string { return "intent-authors-syncer" }

// Notify triggers a re-sync. It is non-blocking: if a notification is already
// pending it is coalesced. Call this when TrustSet membership changes (e.g.
// after Postgres org membership mutations or relay membership updates).
func (s *IntentAuthorsSyncer) Notify() {
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

// Run performs the initial push, then blocks listening for change notifications
// until ctx is cancelled.
func (s *IntentAuthorsSyncer) Run(ctx context.Context) error {
	s.push(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.notifyCh:
			s.push(ctx)
		}
	}
}

func (s *IntentAuthorsSyncer) push(ctx context.Context) {
	pubkeys := s.trustSet.AuthorPubkeys()
	sort.Strings(pubkeys)
	fingerprint := strings.Join(pubkeys, ",")

	for _, ref := range s.targetRefs {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		last := s.lastPush[ref]
		s.mu.Unlock()

		if last == fingerprint {
			continue
		}

		if err := s.pushWithRetry(ctx, ref, pubkeys); err != nil {
			s.logger.Warn("failed to push intent authors to sidecar",
				zap.String("target", ref),
				zap.Error(err),
			)
			continue
		}

		s.mu.Lock()
		s.lastPush[ref] = fingerprint
		s.mu.Unlock()

		s.logger.Info("pushed intent authors to sidecar",
			zap.String("target", ref),
			zap.Int("pubkey_count", len(pubkeys)),
		)
	}
}

func (s *IntentAuthorsSyncer) pushWithRetry(ctx context.Context, targetRef string, pubkeys []string) error {
	const maxBackoff = 30 * time.Second
	backoff := 1 * time.Second
	var lastErr error

	for attempt := 0; attempt < 10; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := s.admin.SetIntentAuthors(ctx, targetRef, pubkeys)
		if err == nil {
			return nil
		}
		lastErr = err
		s.logger.Warn("setintentauthors attempt failed, will retry",
			zap.String("target", targetRef),
			zap.Int("attempt", attempt+1),
			zap.Duration("backoff", backoff),
			zap.Error(err),
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	return lastErr
}
