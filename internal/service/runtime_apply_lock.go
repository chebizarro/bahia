package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// RuntimeApplyLock serializes runtime mutations for an environment on the
// daemon's local filesystem. It is independent of the PostgreSQL index.
type RuntimeApplyLock struct{ directory string }

// NewRuntimeApplyLock uses a directory next to the durable outbox so every
// application instance sharing that outbox also shares the effect fence.
func NewRuntimeApplyLock(outboxPath string) (*RuntimeApplyLock, error) {
	if outboxPath == "" {
		return nil, fmt.Errorf("runtime apply fence requires a durable outbox path")
	}
	abs, err := filepath.Abs(outboxPath)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime apply fence path: %w", err)
	}
	directory := abs + ".runtime-locks"
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create runtime apply fence directory: %w", err)
	}
	return &RuntimeApplyLock{directory: directory}, nil
}

func (l *RuntimeApplyLock) Lock(ctx context.Context, environmentID uuid.UUID) (func(), error) {
	return l.acquire(ctx, environmentID, true)
}

func (l *RuntimeApplyLock) TryLock(ctx context.Context, environmentID uuid.UUID) (func(), bool, error) {
	unlock, err := l.acquire(ctx, environmentID, false)
	return unlock, unlock != nil, err
}

func (l *RuntimeApplyLock) acquire(ctx context.Context, environmentID uuid.UUID, wait bool) (func(), error) {
	if l == nil || l.directory == "" || environmentID == uuid.Nil {
		return nil, fmt.Errorf("runtime apply fence and environment are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(l.directory, environmentID.String()+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open runtime apply fence: %w", err)
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if ctx.Err() != nil {
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				_ = file.Close()
				return nil, ctx.Err()
			}
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
					_ = file.Close()
				})
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire runtime apply fence: %w", err)
		}
		if !wait {
			_ = file.Close()
			return nil, nil
		}
		// Lock contention is local coordination, never relay delivery.
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
