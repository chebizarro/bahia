package service

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SupervisionApplyLock serializes supervised recovery with every other
// runtime mutation in its environment. A configured shared fence is mandatory:
// an acquisition error refuses the recovery rather than falling back to a
// process-local lock that cannot exclude another daemon.
type SupervisionApplyLock struct {
	shared ManagedInstanceTryLocker
	logger *zap.Logger

	mu   sync.Mutex
	held map[uuid.UUID]struct{}
	// unavailableSince is when the shared fence last failed; zero
	// while it is reachable (or not configured).
	unavailableSince time.Time
	unavailableErr   string
	now              func() time.Time
}

// SupervisionApplyLockStatus is the lock's observable state.
type SupervisionApplyLockStatus struct {
	// Shared reports whether a filesystem-backed effect fence is configured.
	Shared bool
	// Unavailable reports that the effect fence failed on the latest attempt.
	Unavailable bool
	// UnavailableSince is when failures began; zero while healthy.
	UnavailableSince time.Time
	// LastError is the shared lock's latest error; empty while healthy.
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
	if !l.unavailableSince.IsZero() {
		status.Unavailable, status.UnavailableSince, status.LastError = true, l.unavailableSince, l.unavailableErr
	}
	return status
}

// recordShared tracks whether the shared fence can be acquired.
func (l *SupervisionApplyLock) recordShared(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		if !l.unavailableSince.IsZero() {
			l.logger.Info("runtime effect fence reachable again; supervised recoveries may resume")
		}
		l.unavailableSince, l.unavailableErr = time.Time{}, ""
		return
	}
	if l.unavailableSince.IsZero() {
		l.unavailableSince = l.now()
	}
	l.unavailableErr = err.Error()
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
		release()
		l.logger.Warn("runtime effect fence unavailable; supervised recovery refused",
			zap.String("environment_id", environmentID.String()), zap.Error(err))
		return nil, false, err
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
