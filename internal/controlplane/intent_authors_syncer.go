package controlplane

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

var errIntentAuthorsChanged = errors.New("intent authors changed during push")

// IntentAuthorsSyncer keeps Bahia sidecar relays informed of which pubkeys may
// write intent events (kind 30900 + t=bahia-intent). On startup and whenever
// the TrustSet's principal set changes or Postgres membership mutates, the
// syncer pushes the current set to each configured sidecar target via the
// NIP-86 setintentauthors method.
//
// The push is idempotent: if the set has not changed since the last successful
// push to a target, it is skipped. On failure, retries use capped exponential
// backoff (1s → 2s → 4s → … capped at 60s) indefinitely until the context is
// cancelled. Warn-level logs and the SyncStatus() health detail expose stale
// state.
//
// Only Bahia-owned sidecar targets receive the push. The syncer never pushes to
// third-party relays.
//
// See docs/architecture/intents-and-authority.md.
type IntentAuthorsSyncer struct {
	trustSet   *TrustSet
	admin      IntentAuthorsAdmin
	targetRefs []string // only bahia-owned sidecar refs
	logger     *zap.Logger

	notifyCh chan struct{}

	mu        sync.Mutex
	lastPush  map[string]string // targetRef → sorted pubkeys fingerprint
	outOfSync bool              // true when at least one target failed

	// pgPubkeys tracks pubkeys discovered through Postgres org membership
	// mutations. These are merged with TrustSet.AuthorPubkeys() when pushing
	// to sidecars, closing the gap that Postgres members are not enumerable
	// through the TrustSet alone.
	pgMu      sync.RWMutex
	pgPubkeys map[string]bool
}

// IntentAuthorsAdmin is the narrow interface the syncer needs from the NIP-86
// relay administration client.
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
		pgPubkeys:  make(map[string]bool),
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

// TrackPubkey adds a pubkey discovered through a Postgres org membership
// mutation and triggers a re-sync.
func (s *IntentAuthorsSyncer) TrackPubkey(pubkey string) {
	if pubkey == "" {
		return
	}
	s.pgMu.Lock()
	s.pgPubkeys[strings.ToLower(pubkey)] = true
	s.pgMu.Unlock()
	s.Notify()
}

// UntrackPubkey removes a pubkey after a Postgres org membership removal and
// triggers a re-sync.
func (s *IntentAuthorsSyncer) UntrackPubkey(pubkey string) {
	if pubkey == "" {
		return
	}
	s.pgMu.Lock()
	delete(s.pgPubkeys, strings.ToLower(pubkey))
	s.pgMu.Unlock()
	s.Notify()
}

// SyncStatus returns a health detail snapshot. OutOfSync is true when at least
// one target has not received the latest push.
func (s *IntentAuthorsSyncer) SyncStatus() IntentAuthorsSyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return IntentAuthorsSyncStatus{
		OutOfSync:  s.outOfSync,
		TargetRefs: append([]string(nil), s.targetRefs...),
	}
}

// IntentAuthorsSyncStatus is exposed via the health provider.
type IntentAuthorsSyncStatus struct {
	OutOfSync  bool
	TargetRefs []string
}

// Run performs the initial push, then blocks listening for change notifications
// until ctx is cancelled.
func (s *IntentAuthorsSyncer) Run(ctx context.Context) error {
	for {
		if s.push(ctx) {
			// A change superseded an in-flight retry. Rebuild the author set
			// immediately; the notification was consumed by pushWithRetry.
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.notifyCh:
			// Re-snapshot the current set on the next iteration.
		}
	}
}

// push returns true when a newer author set superseded this snapshot.
func (s *IntentAuthorsSyncer) push(ctx context.Context) bool {
	pubkeys := s.trustSet.AuthorPubkeys()

	// Merge in Postgres-sourced pubkeys.
	s.pgMu.RLock()
	for pk := range s.pgPubkeys {
		pubkeys = append(pubkeys, pk)
	}
	s.pgMu.RUnlock()

	// Deduplicate and sort.
	sort.Strings(pubkeys)
	pubkeys = compactStrings(pubkeys)
	fingerprint := strings.Join(pubkeys, ",")

	anyFailed := false
	for _, ref := range s.targetRefs {
		if ctx.Err() != nil {
			return false
		}
		s.mu.Lock()
		last, previouslyPushed := s.lastPush[ref]
		s.mu.Unlock()

		if previouslyPushed && last == fingerprint {
			continue
		}

		if err := s.pushWithRetry(ctx, ref, pubkeys); err != nil {
			if errors.Is(err, errIntentAuthorsChanged) {
				return true
			}
			s.logger.Warn("failed to push intent authors to sidecar",
				zap.String("target", ref),
				zap.Error(err),
			)
			anyFailed = true
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

	s.mu.Lock()
	s.outOfSync = anyFailed
	s.mu.Unlock()
	return false
}

func (s *IntentAuthorsSyncer) pushWithRetry(ctx context.Context, targetRef string, pubkeys []string) error {
	const maxBackoff = 60 * time.Second
	backoff := 1 * time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := s.admin.SetIntentAuthors(ctx, targetRef, pubkeys)
		if err == nil {
			return nil
		}
		s.logger.Warn("setintentauthors attempt failed, retrying",
			zap.String("target", targetRef),
			zap.Duration("backoff", backoff),
			zap.Error(err),
		)

		s.mu.Lock()
		s.outOfSync = true
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.notifyCh:
			// Do not retry an obsolete set or swallow the only notification
			// carrying a revocation. Run will snapshot and push the new set.
			return errIntentAuthorsChanged
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// compactStrings removes adjacent duplicates from a sorted slice.
func compactStrings(s []string) []string {
	if len(s) < 2 {
		return s
	}
	j := 0
	for i := 1; i < len(s); i++ {
		if s[i] != s[j] {
			j++
			s[j] = s[i]
		}
	}
	return s[:j+1]
}
