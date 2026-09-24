package nostr

import (
	"context"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestContinuityBackfillRequiresEOSEFromEveryInitialSubscription(t *testing.T) {
	const relayURL = "wss://relay.example"
	for _, terminate := range []bool{false, true} {
		t.Run(map[bool]string{true: "partial", false: "complete"}[terminate], func(t *testing.T) {
			pool := newRelayPoolWithManagedRelays(relayURL)
			markRelayConnectedForSubscribeTest(pool, relayURL)
			var subs []*gonostr.Subscription
			setSubscribeOnRelayForTest(t, func(_ *gonostr.Relay, _ context.Context, _ gonostr.Filter) (*gonostr.Subscription, error) {
				sub := newTestSubscription()
				subs = append(subs, sub)
				return sub, nil
			})
			merged, err := pool.SubscribeAllWithEOSE(t.Context(), []gonostr.Filter{{Kinds: []gonostr.Kind{KindFailoverPolicy}}, {Kinds: []gonostr.Kind{KindRecoveryWorkflow}}})
			require.NoError(t, err)
			defer merged.Close()
			require.False(t, merged.AllRelaysReachedEOSE())
			close(subs[0].EndOfStoredEvents)
			if terminate {
				close(subs[1].Events)
			} else {
				close(subs[1].EndOfStoredEvents)
			}
			<-merged.EndOfStoredEvents
			require.True(t, merged.HasRealEOSE())
			require.Equal(t, !terminate, merged.AllRelaysReachedEOSE(), "termination of one stream must not impersonate its EOSE")
		})
	}
}

func TestContinuityBackfillForwardsBufferedHistoryBeforeEOSE(t *testing.T) {
	const relayURL = "wss://relay.example"
	pool := newRelayPoolWithManagedRelays(relayURL)
	markRelayConnectedForSubscribeTest(pool, relayURL)
	expected := gonostr.Event{Kind: KindFailoverPolicy, ID: gonostr.ID{1}}
	setSubscribeOnRelayForTest(t, func(_ *gonostr.Relay, _ context.Context, _ gonostr.Filter) (*gonostr.Subscription, error) {
		sub := newTestSubscription()
		sub.Events <- expected
		close(sub.EndOfStoredEvents)
		return sub, nil
	})
	merged, err := pool.SubscribeAllWithEOSE(t.Context(), []gonostr.Filter{{Kinds: []gonostr.Kind{KindFailoverPolicy}}})
	require.NoError(t, err)
	defer merged.Close()
	<-merged.EndOfStoredEvents
	require.True(t, merged.AllRelaysReachedEOSE())
	select {
	case actual := <-merged.Events:
		require.Equal(t, expected.ID, actual.ID)
	default:
		t.Fatal("EOSE overtook buffered historical EVENT")
	}
}
