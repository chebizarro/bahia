package nostr

import (
	"context"
	"errors"
	"sync"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func relayConnectedSignalled(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// A publish that loses the transport marks the relay disconnected without a
// wake-up; the next use that reconnects it wakes every listener exactly once.
func TestRelayPoolNotifiesListenersWhenARelayReconnects(t *testing.T) {
	const relayURL = "wss://relay.example"
	pool := newRelayPoolWithManagedRelays(relayURL)
	markRelayConnectedForSubscribeTest(pool, relayURL)
	first, cancelFirst := pool.NotifyRelayConnected()
	defer cancelFirst()
	second, cancelSecond := pool.NotifyRelayConnected()
	defer cancelSecond()

	var mu sync.Mutex
	down := true
	setPublishOnRelayForTest(t, func(*gonostr.Relay, context.Context, gonostr.Event) error {
		mu.Lock()
		defer mu.Unlock()
		if down {
			return errors.New("websocket: close 1006 (abnormal closure)")
		}
		return nil
	})
	setConnectRelayForTest(t, pool, func(_ context.Context, url string, _ gonostr.RelayOptions) (*gonostr.Relay, error) {
		return gonostr.NewRelay(context.Background(), url, gonostr.RelayOptions{}), nil
	})

	accepted, _ := pool.Publish(context.Background(), gonostr.Event{})
	require.Zero(t, accepted)
	require.False(t, relayConnectedSignalled(first), "a disconnect must not signal")

	mu.Lock()
	down = false
	mu.Unlock()
	accepted, err := pool.Publish(context.Background(), gonostr.Event{})
	require.NoError(t, err)
	require.Equal(t, 1, accepted)
	require.True(t, relayConnectedSignalled(first))
	require.True(t, relayConnectedSignalled(second))

	// Staying connected is not a reconnect.
	_, err = pool.Publish(context.Background(), gonostr.Event{})
	require.NoError(t, err)
	require.False(t, relayConnectedSignalled(first))
}

// A reconnect storm never blocks the pool on a slow listener and coalesces
// into one pending wake-up; a cancelled listener receives nothing.
func TestRelayPoolReconnectSignalsCoalesceAndNeverBlock(t *testing.T) {
	const relayURL = "wss://relay.example"
	pool := newRelayPoolWithManagedRelays(relayURL)
	slow, cancelSlow := pool.NotifyRelayConnected()
	defer cancelSlow()
	gone, cancelGone := pool.NotifyRelayConnected()
	cancelGone()
	cancelGone()

	for range 100 {
		pool.recordRelayConnectionState(relayURL, false)
		pool.recordRelayConnectionState(relayURL, true)
	}
	require.True(t, relayConnectedSignalled(slow))
	require.False(t, relayConnectedSignalled(slow), "a burst must coalesce into one wake-up")
	require.False(t, relayConnectedSignalled(gone), "a cancelled listener must not be signalled")
}
