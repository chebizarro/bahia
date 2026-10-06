package service

import (
	"context"
	"sync"
	"time"

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
// a deploy driven by this daemon needs the same unreachable lock and cannot
// be running either.
//
// The fallback has one implication operators must know (bahia-as2bo): the
// process-local lock excludes only this daemon's applies. While the shared
// lock is unreachable, a deploy driven by another daemon that can still reach
// PostgreSQL is not excluded, so a recovery may restart an instance that
// daemon is deploying. Status reports the fallback so a health check can show
// it; docs/runbooks/managed-instance-supervision.md describes the response.
type SupervisionApplyLock struct {
	shared ManagedInstanceTryLocker
	logger *zap.Logger

	mu   sync.Mutex
	held map[uuid.UUID]struct{}
	// fallbackSince is when the shared lock last became unreachable; zero
	// while it is reachable (or not configured).
	fallbackSince time.Time
	fallbackErr   string
	now           func() time.Time
}

// SupervisionApplyLockStatus is the lock's observable state.
type SupervisionApplyLockStatus struct {
	// Shared reports whether a shared (PostgreSQL advisory) lock is configured.
	Shared bool
	// Fallback reports that the shared lock failed on the latest attempt, so
	// recoveries are serialized by the process-local lock only.
	Fallback bool
	// FallbackSince is when the fallback began; zero when not in fallback.
	FallbackSince time.Time
	// LastError is the shared lock's latest error; empty when not in fallback.
	LastError string
}

// NewSupervisionApplyLock builds the lock. shared may be nil.
func NewSupervisionApplyLock(shared ManagedInstanceTryLocker, logger *zap.Logger) *SupervisionApplyLock {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SupervisionApplyLock{shared: shared, logger: logger.Named("supervision-apply-lock"), held: map[uuid.UUID]struct{}{},
		now: func() time.Time { return time.Now().UTC() }}
}

// Status returns the lock's observable state.
func (l *SupervisionApplyLock) Status() SupervisionApplyLockStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	status := SupervisionApplyLockStatus{Shared: l.shared != nil}
	if !l.fallbackSince.IsZero() {
		status.Fallback, status.FallbackSince, status.LastError = true, l.fallbackSince, l.fallbackErr
	}
	return status
}

// recordShared notes the outcome of a shared lock attempt: an error opens (or
// keeps) the fallback, any answer from the lock closes it.
func (l *SupervisionApplyLock) recordShared(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		if !l.fallbackSince.IsZero() {
			l.logger.Info("shared runtime apply lock reachable again; recoveries are serialized with deploys fleet-wide")
		}
		l.fallbackSince, l.fallbackErr = time.Time{}, ""
		return
	}
	if l.fallbackSince.IsZero() {
		l.fallbackSince = l.now()
	}
	l.fallbackErr = err.Error()
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
		l.recordShared(err)
		l.logger.Warn("shared runtime apply lock unavailable; recovery proceeds under the process-local lock, which does not exclude deploys driven by other daemons",
			zap.String("environment_id", environmentID.String()), zap.Error(err))
		return release, true, nil
	}
	l.recordShared(nil)
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
