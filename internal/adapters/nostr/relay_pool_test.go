package nostr

import (
	"context"
	"errors"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"
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
