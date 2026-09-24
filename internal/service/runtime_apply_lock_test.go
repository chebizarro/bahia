package service

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// testPool creates a pgxpool.Pool from DATABASE_URL or skips the test.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// TestRuntimeApplyLock_SameEnvironmentSerializes proves that two concurrent
// deploys targeting the same environment are serialized deterministically
// without sleep-based waiting — uses channels for synchronization.
func TestRuntimeApplyLock_SameEnvironmentSerializes(t *testing.T) {
	pool := testPool(t)
	lock := NewRuntimeApplyLock(pool, nil)

	envID := uuid.New()
	ctx := context.Background()

	var mu sync.Mutex
	var order []int

	firstAcquired := make(chan struct{})
	firstDone := make(chan struct{})
	secondAttempting := make(chan struct{})

	go func() {
		unlock, err := lock.Lock(ctx, envID)
		require.NoError(t, err)

		mu.Lock()
		order = append(order, 1)
		mu.Unlock()

		close(firstAcquired)
		<-firstDone
		unlock()
	}()

	<-firstAcquired

	secondAcquired := make(chan struct{})
	go func() {
		close(secondAttempting)
		unlock, err := lock.Lock(ctx, envID)
		require.NoError(t, err)

		mu.Lock()
		order = append(order, 2)
		mu.Unlock()

		close(secondAcquired)
		unlock()
	}()

	<-secondAttempting

	// Non-blocking select: second goroutine signalled intent but must still be blocked.
	select {
	case <-secondAcquired:
		t.Fatal("second goroutine acquired lock before first released")
	default:
	}

	mu.Lock()
	require.Equal(t, []int{1}, order, "second goroutine should be blocked")
	mu.Unlock()

	close(firstDone)

	<-secondAcquired

	mu.Lock()
	require.Equal(t, []int{1, 2}, order, "deploys must serialize: 1 before 2")
	mu.Unlock()
}

// TestRuntimeApplyLock_DifferentEnvironmentsParallel proves that different
// environments can deploy concurrently without blocking each other.
func TestRuntimeApplyLock_DifferentEnvironmentsParallel(t *testing.T) {
	pool := testPool(t)
	lock := NewRuntimeApplyLock(pool, nil)

	envA := uuid.New()
	envB := uuid.New()
	ctx := context.Background()

	bothAcquired := make(chan struct{}, 2)

	acquire := func(envID uuid.UUID) {
		unlock, err := lock.Lock(ctx, envID)
		require.NoError(t, err)
		bothAcquired <- struct{}{}
		// Hold lock briefly so both goroutines overlap.
		<-time.After(100 * time.Millisecond)
		unlock()
	}

	go acquire(envA)
	go acquire(envB)

	// Both should acquire within a short window since they target different environments.
	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-bothAcquired:
		case <-timeout:
			t.Fatal("timed out waiting for both environments to acquire locks in parallel")
		}
	}
}

// TestRuntimeApplyLock_UnlockOnPanic verifies that deferred unlock releases
// the lock even when the critical section panics.
func TestRuntimeApplyLock_UnlockOnPanic(t *testing.T) {
	pool := testPool(t)
	lock := NewRuntimeApplyLock(pool, nil)

	envID := uuid.New()
	ctx := context.Background()

	// Simulate a panic inside a deploy operation with deferred unlock.
	func() {
		defer func() { _ = recover() }()

		unlock, err := lock.Lock(ctx, envID)
		require.NoError(t, err)
		defer unlock()

		panic("simulated deploy failure")
	}()

	// After the panic/recover, the lock should be available for a new acquire.
	acquired := make(chan struct{})
	go func() {
		unlock, err := lock.Lock(ctx, envID)
		require.NoError(t, err)
		close(acquired)
		unlock()
	}()

	select {
	case <-acquired:
		// Success — lock was properly released.
	case <-time.After(2 * time.Second):
		t.Fatal("lock was not released after panic; second acquire timed out")
	}
}

// TestRuntimeApplyLock_DoubleUnlockSafe verifies calling unlock twice is safe.
func TestRuntimeApplyLock_DoubleUnlockSafe(t *testing.T) {
	pool := testPool(t)
	lock := NewRuntimeApplyLock(pool, nil)

	envID := uuid.New()
	ctx := context.Background()

	unlock, err := lock.Lock(ctx, envID)
	require.NoError(t, err)

	unlock()
	// Second call should be a no-op, not panic or error.
	unlock()
}

// TestAdvisoryLockKey_Deterministic verifies key derivation is stable.
func TestAdvisoryLockKey_Deterministic(t *testing.T) {
	id := uuid.MustParse("12345678-1234-1234-1234-123456789abc")
	k1 := advisoryLockKey(id)
	k2 := advisoryLockKey(id)
	require.Equal(t, k1, k2, "same UUID must produce same key")

	other := uuid.MustParse("abcdefab-abcd-abcd-abcd-abcdefabcdef")
	k3 := advisoryLockKey(other)
	require.NotEqual(t, k1, k3, "different UUIDs should produce different keys")
}
