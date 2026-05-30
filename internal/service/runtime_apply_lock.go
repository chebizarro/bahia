package service

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// RuntimeApplyLock provides environment-scoped advisory locking for serializing
// deploy operations. It uses PostgreSQL session-level advisory locks keyed by
// a deterministic hash of the environment ID.
type RuntimeApplyLock struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
}

// NewRuntimeApplyLock creates a RuntimeApplyLock backed by the given connection pool.
func NewRuntimeApplyLock(pool *pgxpool.Pool, logger *zap.Logger) *RuntimeApplyLock {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RuntimeApplyLock{pool: pool, logger: logger}
}

// Lock acquires a PostgreSQL session-level advisory lock keyed by the environment ID.
// It blocks until the lock is available. The returned unlock function MUST be called
// (typically via defer) to release the lock. The lock is held on a dedicated connection
// so it does not interfere with short-lived transactions used for persistence.
func (l *RuntimeApplyLock) Lock(ctx context.Context, environmentID uuid.UUID) (unlock func(), err error) {
	return l.acquire(ctx, environmentID, true)
}

// TryLock attempts to acquire the same environment-scoped advisory lock without
// blocking. It returns acquired=false when another operation already holds the
// environment lock.
func (l *RuntimeApplyLock) TryLock(ctx context.Context, environmentID uuid.UUID) (unlock func(), acquired bool, err error) {
	unlock, err = l.acquire(ctx, environmentID, false)
	if err != nil {
		return nil, false, err
	}
	if unlock == nil {
		return nil, false, nil
	}
	return unlock, true, nil
}

func (l *RuntimeApplyLock) acquire(ctx context.Context, environmentID uuid.UUID, wait bool) (unlock func(), err error) {
	key := advisoryLockKey(environmentID)

	// Acquire a dedicated connection from the pool to hold the advisory lock.
	// Advisory locks are session-scoped, so we need the same connection for
	// both lock and unlock.
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquiring connection for environment lock: %w", err)
	}

	l.logger.Debug("acquiring environment apply lock",
		zap.String("environment_id", environmentID.String()),
		zap.Int64("advisory_key", key),
		zap.Bool("wait", wait),
	)

	if wait {
		_, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key)
		if err != nil {
			conn.Release()
			return nil, fmt.Errorf("acquiring advisory lock for environment %s: %w", environmentID, err)
		}
	} else {
		var acquired bool
		err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired)
		if err != nil {
			conn.Release()
			return nil, fmt.Errorf("trying advisory lock for environment %s: %w", environmentID, err)
		}
		if !acquired {
			conn.Release()
			l.logger.Debug("environment apply lock already held",
				zap.String("environment_id", environmentID.String()),
				zap.Int64("advisory_key", key),
			)
			return nil, nil
		}
	}

	l.logger.Debug("acquired environment apply lock",
		zap.String("environment_id", environmentID.String()),
		zap.Int64("advisory_key", key),
	)

	unlocked := false
	unlock = func() {
		if unlocked {
			return
		}
		unlocked = true

		// Use a background context for unlock so it succeeds even if the
		// caller's context was cancelled.
		unlockCtx := context.Background()
		_, unlockErr := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", key)
		if unlockErr != nil {
			l.logger.Error("failed to release environment apply lock",
				zap.String("environment_id", environmentID.String()),
				zap.Int64("advisory_key", key),
				zap.Error(unlockErr),
			)
		} else {
			l.logger.Debug("released environment apply lock",
				zap.String("environment_id", environmentID.String()),
				zap.Int64("advisory_key", key),
			)
		}
		conn.Release()
	}

	return unlock, nil
}

// advisoryLockKey derives a deterministic int64 key from an environment UUID.
// Uses FNV-1a hash to map the 16-byte UUID into the int64 space used by
// PostgreSQL advisory locks.
func advisoryLockKey(environmentID uuid.UUID) int64 {
	h := fnv.New64a()
	h.Write(environmentID[:])
	// Convert uint64 to int64 — PostgreSQL advisory locks use bigint (int64).
	return int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]))
}
