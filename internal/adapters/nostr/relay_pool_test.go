package nostr

import (
	"context"
	"errors"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestSubscription() *gonostr.Subscription {
	return &gonostr.Subscription{
		Events:            make(chan *gonostr.Event, 4),
		EndOfStoredEvents: make(chan struct{}),
		ClosedReason:      make(chan string, 1),
	}
}

func TestMergeSubscriptionsClosesEOSEAfterAllRelaysEOSE(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub1 := newTestSubscription()
	sub2 := newTestSubscription()
	merged := mergeSubscriptions(ctx, []*gonostr.Subscription{sub1, sub2}, 4)

	close(sub1.EndOfStoredEvents)
	select {
	case <-merged.EndOfStoredEvents:
		t.Fatal("EOSE must not close until every relay has sent EOSE")
	default:
	}

	close(sub2.EndOfStoredEvents)
	<-merged.EndOfStoredEvents

	// Closed EOSE channels are reusable by callers and must not panic or block on repeated reads.
	<-merged.EndOfStoredEvents

	close(sub1.Events)
	close(sub2.Events)
	_, ok := <-merged.Events
	require.False(t, ok)
}

func TestMergeSubscriptionsDoesNotSignalEOSEWhenRelayClosesBeforeEOSE(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := newTestSubscription()
	merged := mergeSubscriptions(ctx, []*gonostr.Subscription{sub}, 4)
	close(sub.Events)

	_, ok := <-merged.Events
	require.False(t, ok)

	select {
	case <-merged.EndOfStoredEvents:
		t.Fatal("EOSE must not close when a relay ends before sending EOSE")
	default:
	}
}

func TestMergeSubscriptionsForwardsEventsWithoutWaitingForEOSE(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := newTestSubscription()
	merged := mergeSubscriptions(ctx, []*gonostr.Subscription{sub}, 4)
	ev := &gonostr.Event{ID: "event-1", Kind: 5101}

	sub.Events <- ev
	require.Same(t, ev, <-merged.Events)

	close(sub.EndOfStoredEvents)
	<-merged.EndOfStoredEvents
	close(sub.Events)
}

func TestMergeRelaySubscriptionsEmitsPerRelayEOSE(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub1 := newTestSubscription()
	sub2 := newTestSubscription()
	merged := mergeRelaySubscriptions(ctx, []relaySubscription{
		{relayURL: "wss://relay-one.example", sub: sub1},
		{relayURL: "wss://relay-two.example", sub: sub2},
	}, 4)

	close(sub1.EndOfStoredEvents)
	require.Equal(t, RelayEOSE{RelayURL: "wss://relay-one.example"}, <-merged.RelayEOSE)
	select {
	case <-merged.EndOfStoredEvents:
		t.Fatal("aggregate EOSE must wait for every relay")
	default:
	}

	close(sub2.EndOfStoredEvents)
	require.Equal(t, RelayEOSE{RelayURL: "wss://relay-two.example"}, <-merged.RelayEOSE)
	<-merged.EndOfStoredEvents

	close(sub1.Events)
	close(sub2.Events)
}

func TestMergeRelaySubscriptionsEmitsClosedReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := newTestSubscription()
	merged := mergeRelaySubscriptions(ctx, []relaySubscription{{relayURL: "wss://relay.example", sub: sub}}, 4)

	sub.ClosedReason <- "auth-required: sign in first"
	closed := <-merged.Closed
	require.Equal(t, "wss://relay.example", closed.RelayURL)
	require.Equal(t, "auth-required: sign in first", closed.Reason)
	require.True(t, IsAuthRequiredReason(closed.Reason))
	require.True(t, IsAuthRequiredReason("auth-required"))
	require.False(t, IsAuthRequiredReason("closed: not auth-required; maintenance"))

	close(sub.Events)
}

func TestMergeRelaySubscriptionsPreservesRelayClosedReasonAfterEventsClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := newTestSubscription()
	subCtx, subCancel := context.WithCancelCause(context.Background())
	sub.Context = subCtx
	merged := mergeRelaySubscriptions(ctx, []relaySubscription{{relayURL: "wss://relay.example", sub: sub}}, 4)

	subCancel(errors.New("CLOSED received: auth-required: sign in first"))
	close(sub.Events)
	sub.ClosedReason <- "auth-required: sign in first"

	closed := <-merged.Closed
	require.Equal(t, "wss://relay.example", closed.RelayURL)
	require.Equal(t, "auth-required: sign in first", closed.Reason)
}

func TestRelayPool_ConnectedCount(t *testing.T) {
	pool := newRelayPoolWithManagedRelays("wss://relay-one.example", "wss://relay-two.example", "wss://relay-three.example")
	pool.relays["wss://relay-one.example"].connected = true
	pool.relays["wss://relay-three.example"].connected = true
	pool.health.GetOrCreate("wss://relay-one.example").SetConnected(true)
	pool.health.GetOrCreate("wss://relay-three.example").SetConnected(true)

	require.Equal(t, 2, pool.ConnectedCount())
}

func TestRelayPool_HealthyCount(t *testing.T) {
	pool := newRelayPoolWithManagedRelays("wss://healthy.example", "wss://unhealthy.example", "wss://disconnected.example")

	healthy := pool.health.GetOrCreate("wss://healthy.example")
	healthy.SetConnected(true)
	healthy.RecordPublishSuccess(10 * time.Millisecond)
	pool.relays["wss://healthy.example"].connected = true

	unhealthy := pool.health.GetOrCreate("wss://unhealthy.example")
	unhealthy.SetConnected(true)
	for i := 0; i < 10; i++ {
		unhealthy.RecordPublishFailure("relay rejected event")
	}
	pool.relays["wss://unhealthy.example"].connected = true

	require.Equal(t, 1, pool.HealthyCount())
}

func TestRelayPool_HealthSnapshotReturnsPerRelayStatus(t *testing.T) {
	pool := newRelayPoolWithManagedRelays("wss://relay-one.example", "wss://relay-two.example")

	relayOne := pool.health.GetOrCreate("wss://relay-one.example")
	relayOne.SetConnected(true)
	relayOne.RecordPublishSuccess(25 * time.Millisecond)
	pool.relays["wss://relay-one.example"].connected = true

	relayTwo := pool.health.GetOrCreate("wss://relay-two.example")
	relayTwo.SetConnected(false)
	relayTwo.RecordError("dial tcp: connection refused")

	snapshot := pool.HealthSnapshot()
	require.Equal(t, 2, snapshot.Total)
	require.Equal(t, 1, snapshot.Connected)
	require.Equal(t, 1, snapshot.Healthy)
	require.Len(t, snapshot.Relays, 2)

	statuses := make(map[string]RelayStatus, len(snapshot.Relays))
	for _, relay := range snapshot.Relays {
		statuses[relay.URL] = relay
	}

	require.True(t, statuses["wss://relay-one.example"].Connected)
	require.True(t, statuses["wss://relay-one.example"].Healthy)
	require.False(t, statuses["wss://relay-one.example"].LastSeen.IsZero())
	require.Equal(t, 0, statuses["wss://relay-one.example"].Errors)

	require.False(t, statuses["wss://relay-two.example"].Connected)
	require.False(t, statuses["wss://relay-two.example"].Healthy)
	require.Equal(t, 1, statuses["wss://relay-two.example"].Errors)
	require.Equal(t, "dial tcp: connection refused", statuses["wss://relay-two.example"].LastError)
}

func TestRelayPoolRecordRelayErrorSurfacesAuthUnavailableMetadata(t *testing.T) {
	pool := newRelayPoolWithManagedRelays("wss://auth.example")
	pool.RecordRelayError("wss://auth.example", "auth-unavailable: auth-required: sign in: no private key configured for NIP-42 AUTH")

	snapshot := pool.HealthSnapshot()
	require.Len(t, snapshot.Relays, 1)
	require.Equal(t, 1, snapshot.Relays[0].Errors)
	require.Contains(t, snapshot.Relays[0].LastError, "auth-unavailable")
	require.False(t, snapshot.Relays[0].Healthy)
}

func TestNewPublisherConfiguresPrivateKeyForRelayAuth(t *testing.T) {
	privateKey := gonostr.GeneratePrivateKey()
	publisher := NewPublisher(config.NostrConfig{PrivateKey: privateKey, PublishEnabled: true}, nil, nil, zap.NewNop())
	require.NotNil(t, publisher)
	require.NotNil(t, publisher.pool)
	require.Equal(t, privateKey, publisher.pool.privateKey)
}

func newRelayPoolWithManagedRelays(urls ...string) *RelayPool {
	pool := NewRelayPool(urls, zap.NewNop())
	for _, url := range urls {
		pool.relays[url] = &managedRelay{url: url}
	}
	return pool
}
