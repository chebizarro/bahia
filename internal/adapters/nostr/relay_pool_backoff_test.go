package nostr

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type fakePoolClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakePoolClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakePoolClock) Set(at time.Time) {
	c.mu.Lock()
	c.now = at
	c.mu.Unlock()
}

func relayRetryAt(pool *RelayPool, url string) time.Time {
	mr := pool.relays[url]
	mr.mu.Lock()
	defer mr.mu.Unlock()
	return mr.retryAt
}

func resultFor(t *testing.T, results []PublishResult, url string) PublishResult {
	t.Helper()
	for _, result := range results {
		if result.RelayURL == url {
			return result
		}
	}
	t.Fatalf("no publish result for %s in %#v", url, results)
	return PublishResult{}
}

// A relay that keeps failing to reconnect is dialed at most once per backoff
// window: publishes in between fail fast for that relay while the healthy
// relay keeps accepting. The window grows but stays bounded by Max, and a
// successful connect resets it.
func TestRelayPoolReconnectBackoffIsBoundedAndFailsFast(t *testing.T) {
	pool := newRelayPoolWithManagedRelays(relayA, relayB)
	markRelayConnectedForSubscribeTest(pool, relayA)
	clock := &fakePoolClock{now: time.Unix(1_800_000_000, 0)}
	pool.now = clock.Now
	const maxBackoff = 8 * time.Second
	pool.newReconnectBackoff = func() *Backoff {
		return &Backoff{Initial: time.Second, Max: maxBackoff, Multiplier: 2}
	}
	var dials atomic.Int32
	var relayBUp atomic.Bool
	setConnectRelayForTest(t, pool, func(ctx context.Context, url string, opts gonostr.RelayOptions) (*gonostr.Relay, error) {
		require.Equal(t, relayB, url, "only the disconnected relay is dialed")
		dials.Add(1)
		if relayBUp.Load() {
			return gonostr.NewRelay(context.Background(), url, opts), nil
		}
		return nil, errors.New("connection refused")
	})
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error { return nil })
	ctx := context.Background()

	results, _ := pool.PublishWithResults(ctx, gonostr.Event{})
	require.True(t, resultFor(t, results, relayA).Accepted)
	require.ErrorContains(t, resultFor(t, results, relayB).Error, "connection refused")
	require.EqualValues(t, 1, dials.Load())

	// Inside the backoff window relay B fails fast without a dial; relay A is
	// unaffected.
	for range 5 {
		results, _ = pool.PublishWithResults(ctx, gonostr.Event{})
		require.True(t, resultFor(t, results, relayA).Accepted)
		var backoffErr *RelayReconnectBackoffError
		require.ErrorAs(t, resultFor(t, results, relayB).Error, &backoffErr)
		require.Equal(t, relayB, backoffErr.RelayURL)
	}
	require.EqualValues(t, 1, dials.Load(), "no dial while the reconnect backoff runs")

	// Each window that expires allows exactly one more dial; the window grows
	// and is capped at Max.
	var windows []time.Duration
	for i := range 6 {
		retryAt := relayRetryAt(pool, relayB)
		windows = append(windows, retryAt.Sub(clock.Now()))
		clock.Set(retryAt)
		_, _ = pool.PublishWithResults(ctx, gonostr.Event{})
		require.EqualValues(t, i+2, dials.Load())
	}
	for i, window := range windows {
		require.Positive(t, window)
		require.LessOrEqual(t, window, maxBackoff, "window %d", i)
		if i > 0 {
			require.GreaterOrEqual(t, window, windows[i-1], "window %d shrank", i)
		}
	}
	require.Equal(t, maxBackoff, windows[len(windows)-1])

	// A successful connect clears the backoff.
	relayBUp.Store(true)
	clock.Set(relayRetryAt(pool, relayB))
	results, _ = pool.PublishWithResults(ctx, gonostr.Event{})
	require.True(t, resultFor(t, results, relayB).Accepted)
	require.True(t, relayRetryAt(pool, relayB).IsZero())
	require.Zero(t, pool.relays[relayB].reconnectBackoff.Attempt())
}

// A slow reconnect to one relay stalls nothing else: the topology lock is not
// held during sends, the relay's state lock is not held during the dial,
// publishes to other relays complete, and concurrent publishes to the same
// relay share the in-flight dial instead of dialing again.
func TestRelayPoolSlowReconnectDoesNotStallOtherRelaysOrTopology(t *testing.T) {
	pool := newRelayPoolWithManagedRelays(relayA, relayB)
	markRelayConnectedForSubscribeTest(pool, relayA)
	clock := &fakePoolClock{now: time.Unix(1_800_000_000, 0)}
	pool.now = clock.Now
	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	var dials atomic.Int32
	setConnectRelayForTest(t, pool, func(ctx context.Context, url string, _ gonostr.RelayOptions) (*gonostr.Relay, error) {
		if dials.Add(1) == 1 {
			close(dialStarted)
		}
		select {
		case <-releaseDial:
			return nil, errors.New("connection refused")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first := make(chan []PublishResult, 1)
	go func() {
		results, _ := pool.PublishWithResults(ctx, gonostr.Event{})
		first <- results
	}()
	receive(t, dialStarted, "relay B dial in flight")

	topologyFree := make(chan struct{})
	go func() {
		pool.mu.Lock()
		pool.mu.Unlock()
		close(topologyFree)
	}()
	receive(t, topologyFree, "topology write lock while a publish is in flight")

	stateFree := make(chan bool)
	go func() { stateFree <- managedRelayConnected(pool.relays[relayB]) }()
	require.False(t, receive(t, stateFree, "relay B state readable during its dial"))

	results, err := pool.PublishToRelaysWithResults(ctx, gonostr.Event{}, []string{relayA})
	require.NoError(t, err)
	require.True(t, resultFor(t, results, relayA).Accepted, "relay A is not held up by relay B's dial")

	second := make(chan []PublishResult, 1)
	go func() {
		results, _ := pool.PublishToRelaysWithResults(ctx, gonostr.Event{}, []string{relayB})
		second <- results
	}()

	close(releaseDial)
	firstResults := receive(t, first, "first publish")
	require.True(t, resultFor(t, firstResults, relayA).Accepted)
	require.Error(t, resultFor(t, firstResults, relayB).Error)
	require.Error(t, resultFor(t, receive(t, second, "second publish"), relayB).Error)
	require.EqualValues(t, 1, dials.Load(), "a concurrent publish shares the in-flight dial")
}

// A relay closed by the pool (retired and pruned) is never reconnected, even
// if a dial was already in flight when it was closed.
func TestRelayPoolDoesNotReviveClosedRelay(t *testing.T) {
	pool := newRelayPoolWithManagedRelays(relayB)
	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	setConnectRelayForTest(t, pool, func(ctx context.Context, url string, opts gonostr.RelayOptions) (*gonostr.Relay, error) {
		close(dialStarted)
		<-releaseDial
		return gonostr.NewRelay(context.Background(), url, opts), nil
	})
	mr := pool.relays[relayB]
	done := make(chan error, 1)
	go func() {
		_, err := pool.reconnectRelay(context.Background(), mr)
		done <- err
	}()
	receive(t, dialStarted, "dial in flight")
	closeManagedRelay(pool, mr)
	close(releaseDial)
	require.ErrorContains(t, receive(t, done, "reconnect result"), "removed from the pool")
	require.False(t, managedRelayConnected(mr))
	_, err := pool.reconnectRelay(context.Background(), mr)
	require.ErrorContains(t, err, "removed from the pool")
}
