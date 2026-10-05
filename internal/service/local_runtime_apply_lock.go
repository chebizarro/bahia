package service

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// LocalRuntimeApplyLock serializes supervised recovery by environment when
// PostgreSQL advisory locks are unavailable. Recovery claims are canonical
// records, so restart idempotency does not depend on this process-local lock.
type LocalRuntimeApplyLock struct {
	mu    sync.Mutex
	locks map[uuid.UUID]*sync.Mutex
}

func (l *LocalRuntimeApplyLock) TryLock(ctx context.Context, environmentID uuid.UUID) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[uuid.UUID]*sync.Mutex)
	}
	lock := l.locks[environmentID]
	if lock == nil {
		lock = &sync.Mutex{}
		l.locks[environmentID] = lock
	}
	l.mu.Unlock()
	if !lock.TryLock() {
		return nil, false, nil
	}
	return lock.Unlock, true, nil
}

var _ ManagedInstanceTryLocker = (*LocalRuntimeApplyLock)(nil)
