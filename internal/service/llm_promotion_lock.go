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

// LLMPromotionLocker serializes the desired-state check and gateway mutation.
type LLMPromotionLocker interface {
	Lock(ctx context.Context, routeID, environmentID uuid.UUID) (func(), error)
}

// PGLLMPromotionLock is a cross-process route/environment advisory lock.
type PGLLMPromotionLock struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
}

func NewPGLLMPromotionLock(pool *pgxpool.Pool, logger *zap.Logger) *PGLLMPromotionLock {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PGLLMPromotionLock{pool: pool, logger: logger.Named("llm-promotion-lock")}
}

func (l *PGLLMPromotionLock) Lock(ctx context.Context, routeID, environmentID uuid.UUID) (func(), error) {
	if l == nil || l.pool == nil {
		return nil, fmt.Errorf("LLM promotion lock database is not configured")
	}
	key := llmPromotionAdvisoryKey(routeID, environmentID)
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquiring connection for LLM promotion lock: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		conn.Release()
		return nil, fmt.Errorf("locking LLM route %s environment %s: %w", routeID, environmentID, err)
	}
	unlocked := false
	return func() {
		if unlocked {
			return
		}
		unlocked = true
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", key); err != nil {
			l.logger.Error("failed to release LLM promotion lock", zap.String("route_id", routeID.String()), zap.String("environment_id", environmentID.String()), zap.Error(err))
		}
		conn.Release()
	}, nil
}

func llmPromotionAdvisoryKey(routeID, environmentID uuid.UUID) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("bahia:llm-promotion:v1"))
	_, _ = h.Write(routeID[:])
	_, _ = h.Write(environmentID[:])
	return int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]))
}
