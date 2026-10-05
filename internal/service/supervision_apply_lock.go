package service

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SupervisionApplyLock serializes a supervised recovery with every other
// runtime apply in its environment.
//
// It always takes a process-local lock per environment. When a shared lock is
// configured (the PostgreSQL advisory lock deploys hold) it takes that too, so
// a recovery never restarts an instance in the middle of a deploy. The shared
// lock is an optimization of a healthy database, not a precondition: when it
// cannot be reached the recovery proceeds under the process-local lock, since
// a deploy that needs the same unreachable lock cannot be running either.
type SupervisionApplyLock struct {
	shared ManagedInstanceTryLocker
	logger *zap.Logger

	mu   sync.Mutex
	held map[uuid.UUID]struct{}
}

// NewSupervisionApplyLock builds the lock. shared may be nil.
func NewSupervisionApplyLock(shared ManagedInstanceTryLocker, logger *zap.Logger) *SupervisionApplyLock {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SupervisionApplyLock{shared: shared, logger: logger.Named("supervision-apply-lock"), held: map[uuid.UUID]struct{}{}}
}

// TryLock acquires the environment's lock without blocking. It reports false
// when another apply holds it.
func (l *SupervisionApplyLock) TryLock(ctx context.Context, environmentID uuid.UUID) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	l.mu.Lock()
	if _, busy := l.held[environmentID]; busy {
		l.mu.Unlock()
		return nil, false, nil
	}
	l.held[environmentID] = struct{}{}
	l.mu.Unlock()
	release := func() {
		l.mu.Lock()
		delete(l.held, environmentID)
		l.mu.Unlock()
	}
	if l.shared == nil {
		return release, true, nil
	}
	unlockShared, acquired, err := l.shared.TryLock(ctx, environmentID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			release()
			return nil, false, ctxErr
		}
		l.logger.Warn("shared runtime apply lock unavailable; recovery proceeds under the process-local lock",
			zap.String("environment_id", environmentID.String()), zap.Error(err))
		return release, true, nil
	}
	if !acquired {
		release()
		return nil, false, nil
	}
	return func() {
		unlockShared()
		release()
	}, true, nil
}

var _ ManagedInstanceTryLocker = (*SupervisionApplyLock)(nil)
