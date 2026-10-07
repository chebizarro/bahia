package nostr

import (
	"context"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// postgresArchive writes to the optional PostgreSQL nostr_events table without
// letting PostgreSQL gate relay traffic. Delivery state and
// inbound idempotency live in the local store; the table is an archive and
// index for the PostgreSQL-backed readers.
//
// Each write gets a short timeout, and after a failure writes are skipped for
// a cooldown, so a database outage costs one timeout per cooldown instead of
// one per event. Rows written during an outage are missing from the archive.
type postgresArchive struct {
	repo     repository.NostrEventRepository
	logger   *zap.Logger
	timeout  time.Duration
	cooldown time.Duration
	now      func() time.Time

	mu          sync.Mutex
	pausedUntil time.Time
}

const (
	postgresArchiveWriteTimeout = 2 * time.Second
	postgresArchiveCooldown     = 30 * time.Second
)

// newPostgresArchive returns nil when repo is nil; a nil archive writes
// nothing.
func newPostgresArchive(repo repository.NostrEventRepository, logger *zap.Logger) *postgresArchive {
	if repo == nil {
		return nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &postgresArchive{repo: repo, logger: logger, timeout: postgresArchiveWriteTimeout, cooldown: postgresArchiveCooldown, now: time.Now}
}

// write runs fn against the archive unless it is cooling down after a
// failure, and reports whether fn ran and succeeded. It never returns an
// error: callers carry on regardless.
func (a *postgresArchive) write(ctx context.Context, what, eventID string, fn func(context.Context, repository.NostrEventRepository) error) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	paused := a.now().Before(a.pausedUntil)
	a.mu.Unlock()
	if paused {
		return false
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.timeout)
	defer cancel()
	err := fn(writeCtx, a.repo)
	if err == nil {
		return true
	}
	a.mu.Lock()
	a.pausedUntil = a.now().Add(a.cooldown)
	a.mu.Unlock()
	a.logger.Warn("nostr_events archive write failed; skipping archive writes for a cooldown",
		zap.String("write", what),
		zap.String("event_id", eventID),
		zap.Duration("cooldown", a.cooldown),
		zap.Error(err))
	return false
}

// outbox returns the archive's publish-state extension, or nil.
func (a *postgresArchive) outbox() repository.NostrEventOutboxRepository {
	if a == nil {
		return nil
	}
	outbox, _ := a.repo.(repository.NostrEventOutboxRepository)
	return outbox
}
