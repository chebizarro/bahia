package service

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func testRuntimeApplyLock(t *testing.T) *RuntimeApplyLock {
	t.Helper()
	lock, err := NewRuntimeApplyLock(t.TempDir() + "/outbox.bolt")
	require.NoError(t, err)
	return lock
}

// TestRuntimeApplyLock_SameEnvironmentSerializes proves that two concurrent
// deploys targeting the same environment are serialized deterministically
// without sleep-based waiting — uses channels for synchronization.
func TestRuntimeApplyLock_SameEnvironmentSerializes(t *testing.T) {
	lock := testRuntimeApplyLock(t)

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
	lock := testRuntimeApplyLock(t)

	envA := uuid.New()
	envB := uuid.New()
	ctx := context.Background()

	bothAcquired := make(chan struct{}, 2)
	release := make(chan struct{})

	acquire := func(envID uuid.UUID) {
		unlock, err := lock.Lock(ctx, envID)
		require.NoError(t, err)
		bothAcquired <- struct{}{}
		<-release
		unlock()
	}

	go acquire(envA)
	go acquire(envB)

	// Both must acquire while neither has released its lock.
	defer close(release)
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
	lock := testRuntimeApplyLock(t)

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
	lock := testRuntimeApplyLock(t)

	envID := uuid.New()
	ctx := context.Background()

	unlock, err := lock.Lock(ctx, envID)
	require.NoError(t, err)

	unlock()
	// Second call should be a no-op, not panic or error.
	unlock()
}

// A second lock handle on the same outbox path must contend, even though it
// has no shared Go mutex. This exercises the filesystem effect fence.
func TestRuntimeApplyLock_IndependentHandlesContend(t *testing.T) {
	path := t.TempDir() + "/outbox.bolt"
	first, err := NewRuntimeApplyLock(path)
	require.NoError(t, err)
	second, err := NewRuntimeApplyLock(path)
	require.NoError(t, err)
	env := uuid.New()
	unlock, err := first.Lock(t.Context(), env)
	require.NoError(t, err)
	_, acquired, err := second.TryLock(t.Context(), env)
	require.NoError(t, err)
	require.False(t, acquired)
	unlock()
	unlockSecond, acquired, err := second.TryLock(t.Context(), env)
	require.NoError(t, err)
	require.True(t, acquired)
	unlockSecond()
}

func TestRuntimeApplyLock_CancelledWait(t *testing.T) {
	lock := testRuntimeApplyLock(t)
	env := uuid.New()
	unlock, err := lock.Lock(t.Context(), env)
	require.NoError(t, err)
	defer unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = lock.Lock(ctx, env)
	require.ErrorIs(t, err, context.Canceled)
}

// TestRuntimeApplyLock_SecondProcess proves exclusion is provided by the OS,
// not merely by an in-process mutex. The child exits only after parent input.
func TestRuntimeApplyLock_SecondProcess(t *testing.T) {
	path := t.TempDir() + "/outbox.bolt"
	env := uuid.New()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeApplyLockProcessHelper$")
	cmd.Env = append(os.Environ(), "BAHIA_TEST_APPLY_LOCK_PATH="+path, "BAHIA_TEST_APPLY_LOCK_ENV="+env.String())
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "locked\n", line)

	parent, err := NewRuntimeApplyLock(path)
	require.NoError(t, err)
	_, acquired, err := parent.TryLock(t.Context(), env)
	require.NoError(t, err)
	require.False(t, acquired)
	require.NoError(t, stdin.Close())
	if err := cmd.Wait(); err != nil {
		t.Fatalf("lock helper: %v: %s", err, stderr.String())
	}
	unlock, acquired, err := parent.TryLock(t.Context(), env)
	require.NoError(t, err)
	require.True(t, acquired)
	unlock()
}

func TestRuntimeApplyLockProcessHelper(t *testing.T) {
	path := os.Getenv("BAHIA_TEST_APPLY_LOCK_PATH")
	if path == "" {
		return
	}
	env, err := uuid.Parse(os.Getenv("BAHIA_TEST_APPLY_LOCK_ENV"))
	require.NoError(t, err)
	lock, err := NewRuntimeApplyLock(path)
	require.NoError(t, err)
	unlock, err := lock.Lock(t.Context(), env)
	require.NoError(t, err)
	fmt.Print("locked\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	unlock()
}
