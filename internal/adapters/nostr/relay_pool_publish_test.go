package nostr

import (
	"context"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestRelayPoolPublishContactsRelaysConcurrently(t *testing.T) {
	pool := newRelayPoolWithManagedRelays(relayA, relayB)
	markRelayConnectedForSubscribeTest(pool, relayA)
	markRelayConnectedForSubscribeTest(pool, relayB)

	// Relay A does not answer until relay B has been contacted. A sequential
	// publish loop would never reach relay B.
	bStarted := make(chan struct{})
	setPublishOnRelayForTest(t, func(relay *gonostr.Relay, ctx context.Context, _ gonostr.Event) error {
		if relay.URL == relayB {
			close(bStarted)
			return nil
		}
		select {
		case <-bStarted:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results, err := pool.PublishWithResults(ctx, gonostr.Event{})
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, relayA, results[0].RelayURL, "results keep configured relay order")
	require.Equal(t, relayB, results[1].RelayURL)
	require.True(t, results[0].Accepted, "relay A was blocked behind relay B: publishes are not concurrent")
	require.True(t, results[1].Accepted)
}

func TestRelayPoolPublishToRelaysOnlyContactsNamedRelays(t *testing.T) {
	pool := newRelayPoolWithManagedRelays(relayA, relayB, relayC)
	for _, url := range []string{relayA, relayB, relayC} {
		markRelayConnectedForSubscribeTest(pool, url)
	}
	var mu sync.Mutex
	var contacted []string
	setPublishOnRelayForTest(t, func(relay *gonostr.Relay, _ context.Context, _ gonostr.Event) error {
		mu.Lock()
		contacted = append(contacted, relay.URL)
		mu.Unlock()
		return nil
	})

	results, err := pool.PublishToRelaysWithResults(context.Background(), gonostr.Event{}, []string{relayB, "wss://not-configured.example"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, relayB, results[0].RelayURL)
	require.Equal(t, []string{relayB}, contacted)
}
