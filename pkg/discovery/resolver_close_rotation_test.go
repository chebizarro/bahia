package discovery

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
)

// The events channel closing ends the subscription even while EOSE is still
// pending (its channel never closes here): consume returns instead of
// blocking on EndOfStoredEvents, the resolver is not reported ready, and the
// interrupted backfill is redone in full.
func TestResolverConsumeReturnsWhenEventsCloseBeforeEOSE(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	events := make(chan *nostr.Event)
	relayEOSE := make(chan nostradapter.RelayEOSE)
	closed := make(chan nostradapter.RelayClosed)
	merged := &nostradapter.MergedSubscription{Events: events, EndOfStoredEvents: make(chan struct{}), RelayEOSE: relayEOSE, Closed: closed}
	done := make(chan error, 1)
	go func() {
		retry, err := resolver.consume(context.Background(), &fakeRelayPool{}, merged, map[string]struct{}{})
		require.False(t, retry)
		done <- err
	}()

	events <- liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.10"), resolverTestBase()+10)
	close(events)
	require.ErrorContains(t, waitDone(t, done), "subscription event stream closed")

	select {
	case <-resolver.Ready():
		t.Fatal("resolver reported ready without EOSE")
	default:
	}
	require.Zero(t, resolver.subscriptionFilter().Since, "a backfill cut short by the close is redone in full")
	endpoint, ok := resolver.ResolveByFQDN("api.svc.example.com")
	require.True(t, ok, "events received before the close are kept")
	require.Equal(t, "10.0.0.10", endpoint.Address)
}

// subscribeUntilClosed surfaces the close to run (which reconnects) rather
// than hanging when a relay pool closes events before EOSE.
func TestResolverSubscribeUntilClosedEndsWhenEventsCloseBeforeEOSE(t *testing.T) {
	_, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	events := make(chan *nostr.Event)
	close(events)
	pool := &fakeRelayPool{subs: []*nostradapter.MergedSubscription{{Events: events, EndOfStoredEvents: make(chan struct{})}}}
	done := make(chan error, 1)
	go func() { done <- resolver.subscribeUntilClosed(context.Background(), pool) }()
	require.ErrorContains(t, waitDone(t, done), "subscription event stream closed")
}

// During a service key rotation both keys are trusted and requested; a d
// coordinate is shared across them, so the newest record wins whichever key
// signed it.
func TestResolverAcceptsRotatedServiceKeys(t *testing.T) {
	oldSecret, oldPubkey := generatedResolverKeyPair(t)
	newSecret, newPubkey := generatedResolverKeyPair(t)
	forgerSecret, _ := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, oldPubkey, WithServiceKeys(strings.ToUpper(newPubkey), oldPubkey, " "))

	oldAuthor, err := nostrutil.PubKeyFromHex(oldPubkey)
	require.NoError(t, err)
	newAuthor, err := nostrutil.PubKeyFromHex(newPubkey)
	require.NoError(t, err)
	require.Equal(t, []nostr.PubKey{oldAuthor, newAuthor}, resolver.subscriptionFilter().Authors, "every trusted key is requested once")

	base := resolverTestBase()
	api := apiEndpoint("10.0.0.10")
	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, oldSecret, api, base)))
	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, newSecret, apiEndpoint("10.0.0.20"), base+10)))
	endpoint, ok := resolver.ResolveByFQDN(api.FQDN)
	require.True(t, ok)
	require.Equal(t, "10.0.0.20", endpoint.Address, "the new key's newer record supersedes the old key's")
	require.Len(t, resolver.Endpoints(), 1, "one endpoint, not one per key")

	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, oldSecret, apiEndpoint("10.0.0.30"), base+5)))
	endpoint, _ = resolver.ResolveByFQDN(api.FQDN)
	require.Equal(t, "10.0.0.20", endpoint.Address, "a stale record from the old key does not win")

	require.ErrorContains(t, resolver.applyEvent(liveEndpointEvent(t, forgerSecret, apiEndpoint("10.6.6.6"), base+20)), "unexpected author")

	require.NoError(t, resolver.applyEvent(endpointTombstoneEvent(t, oldSecret, api.Coordinate, api.FQDN, base+30)))
	_, ok = resolver.ResolveByFQDN(api.FQDN)
	require.False(t, ok, "a tombstone from either trusted key removes the endpoint")
}

func TestResolverStartRejectsInvalidRotationKey(t *testing.T) {
	_, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey, WithServiceKeys("not-hex"))
	require.ErrorContains(t, resolver.Start(context.Background()), "not-hex")
	require.NoError(t, resolver.Stop())
}
