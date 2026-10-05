package relaysidecar

import (
	"context"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

// startConfigWorkersForTest runs the activation loop and the dirty-coordinate
// worker the way Run does. It stops them before the store is closed.
func startConfigWorkersForTest(t *testing.T, server *Server) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), fanoutTestTimeout)
	t.Cleanup(func() {
		cancel()
		server.wg.Wait()
		server.consumer.wait()
	})
	server.consumer.Start(ctx)
	server.startConfigWorker(ctx)
	return ctx
}

func TestSidecarConfigBurstLargerThanOldQueueAppliesLatest(t *testing.T) {
	// The removed config queue held 64 events and dropped the rest, so the
	// sidecar would have settled on version 64 and never applied the latest.
	const total = 200
	server, secret := configStatusServerForTest(t)
	applied := make(chan int, total)
	apply := server.consumer.apply
	server.consumer.apply = func(projection ConfigProjection) error {
		if err := apply(projection); err != nil {
			return err
		}
		applied <- projection.Version
		return nil
	}

	// The whole burst lands before the worker drains anything.
	for version := 1; version <= total; version++ {
		_, err := server.Relay().AddEvent(t.Context(), configStatusDesiredForTest(t, secret, version, 0))
		require.NoError(t, err)
	}

	ctx := startConfigWorkersForTest(t, server)
	for {
		select {
		case version := <-applied:
			if version != total {
				continue
			}
		case <-ctx.Done():
			t.Fatalf("latest desired version %d was never applied", total)
		}
		break
	}
	// The test's apply hook reports a version before the worker records it in
	// state.Applied under the lock, so the bookkeeping may land a moment later.
	author := secret.Public().Hex()
	require.Eventually(t, func() bool {
		return appliedVersionForTest(server.consumer, author, "membership") == total
	}, 5*time.Second, 10*time.Millisecond, "latest desired version %d was applied but not recorded", total)
}

func TestSidecarConfigWorkerReadsLatestFromStoreNotStaleNotification(t *testing.T) {
	server, secret := configStatusServerForTest(t)
	older := configStatusDesiredForTest(t, secret, 1, 0)
	newer := configStatusDesiredForTest(t, secret, 2, 0)

	var mu sync.Mutex
	var statuses []nostr.Event
	appliedNewer := make(chan struct{})
	server.consumer.publisher = configStatusPublisherFunc(func(_ context.Context, event nostr.Event) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		statuses = append(statuses, event)
		if statusNameForTest(t, event) == "applied" && event.Tags.Find("e")[1] == newer.ID.Hex() {
			close(appliedNewer)
		}
		return 1, nil
	})

	// The store holds the newer version. A late save notification for the older
	// one (concurrent publishers can report saves out of order) must not make
	// it win.
	_, err := server.Relay().AddEvent(t.Context(), newer)
	require.NoError(t, err)
	server.Relay().OnEventSaved(t.Context(), older)

	ctx := startConfigWorkersForTest(t, server)
	select {
	case <-appliedNewer:
	case <-ctx.Done():
		t.Fatal("newer desired config was never applied")
	}
	require.Equal(t, 2, appliedVersionForTest(server.consumer, secret.Public().Hex(), "membership"))
	mu.Lock()
	defer mu.Unlock()
	for _, status := range statuses {
		require.NotEqual(t, older.ID.Hex(), status.Tags.Find("e")[1], "stale notification was handled as %q", statusNameForTest(t, status))
	}
}
