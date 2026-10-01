package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/boltdb"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Local event store tests (WithStorePath, bahia-irsry.10.5) against in-process
// khatru relays with NIP-77. Waits are on Ready and on the resolver's own
// per-relay catch-up signal; nothing sleeps.

type storeTestRelay struct {
	url  string
	down atomic.Bool
	// fetched counts events requested by id outside NIP-77 sessions: the
	// downloads of a reconciliation.
	fetched atomic.Int64
	relay   *khatru.Relay
}

func newStoreTestRelay(t *testing.T) *storeTestRelay {
	t.Helper()
	store := &boltdb.BoltBackend{Path: filepath.Join(t.TempDir(), "relay.bolt")}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	r := &storeTestRelay{relay: khatru.NewRelay()}
	r.relay.Negentropy = true
	r.relay.UseEventstore(store, 500)
	seedSupportedNIPs(r.relay)
	r.relay.RejectConnection = func(*http.Request) bool { return r.down.Load() }
	r.relay.OnRequest = func(ctx context.Context, filter nostr.Filter) (bool, string) {
		if !khatru.IsNegentropySession(ctx) {
			r.fetched.Add(int64(len(filter.IDs)))
		}
		return false, ""
	}
	server := httptest.NewServer(r.relay)
	t.Cleanup(server.Close)
	r.url = nostr.NormalizeURL("ws" + strings.TrimPrefix(server.URL, "http"))
	return r
}

func (r *storeTestRelay) add(t *testing.T, events ...*nostr.Event) {
	t.Helper()
	for _, ev := range events {
		_, err := r.relay.AddEvent(t.Context(), *ev)
		require.NoError(t, err)
	}
}

// caughtUpCore is a zap core that reports the relays the resolver logs as
// caught up, so a test can wait on that signal.
type caughtUpCore struct {
	relays chan string
}

func (c caughtUpCore) Enabled(zapcore.Level) bool        { return true }
func (c caughtUpCore) With([]zapcore.Field) zapcore.Core { return c }
func (c caughtUpCore) Sync() error                       { return nil }
func (c caughtUpCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return checked.AddCore(entry, c)
}

func (c caughtUpCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	if entry.Message != "relay caught up with stored endpoint state" {
		return nil
	}
	for _, field := range fields {
		if field.Key == "relay" {
			c.relays <- field.String
		}
	}
	return nil
}

func waitRelayCaughtUp(t *testing.T, relays <-chan string, url string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case got := <-relays:
			if got == url {
				return
			}
		case <-deadline:
			t.Fatalf("relay %s did not catch up", url)
		}
	}
}

func endpointNamed(name, address string) domain.DNSEndpoint {
	endpoint := apiEndpoint(address)
	endpoint.Name, endpoint.Coordinate, endpoint.FQDN = name, "service:"+name+":prod", name+".svc.example.com"
	return endpoint
}

func TestResolverStoreRestartFetchesOnlyNewEventsAndKeepsTombstones(t *testing.T) {
	sk, pubkey := generatedResolverKeyPair(t)
	relay := newStoreTestRelay(t)
	now := nostr.Now()
	one, two, three := endpointNamed("one", "10.0.0.10"), endpointNamed("two", "10.0.0.11"), endpointNamed("three", "10.0.0.12")
	relay.add(t, liveEndpointEvent(t, sk, one, now-3600), liveEndpointEvent(t, sk, two, now-3500), liveEndpointEvent(t, sk, three, now-3400))
	path := filepath.Join(t.TempDir(), "discovery.bolt")
	start := func() *Resolver {
		r := New([]string{relay.url}, pubkey, WithStorePath(path))
		require.NoError(t, r.Start(t.Context()))
		return r
	}

	r := start()
	waitReady(t, r)
	require.Len(t, r.Endpoints(), 3)
	require.NoError(t, r.Stop())
	require.Equal(t, int64(3), relay.fetched.Load(), "a cold start downloads every endpoint")

	relay.fetched.Store(0)
	r = start()
	require.Len(t, r.Endpoints(), 3, "stored endpoints resolve before any relay answers")
	waitReady(t, r)
	require.NoError(t, r.Stop())
	require.Zero(t, relay.fetched.Load(), "a restart against an unchanged relay downloads nothing")

	relay.add(t, endpointTombstoneEvent(t, sk, one.Coordinate, one.FQDN, now-60))
	relay.fetched.Store(0)
	r = start()
	waitReady(t, r)
	_, found := r.ResolveByFQDN(one.FQDN)
	require.False(t, found)
	require.NoError(t, r.Stop())
	require.Equal(t, int64(1), relay.fetched.Load(), "only the tombstone is downloaded")

	// The tombstone is in force from the store alone, before any relay
	// answers, and an older live version cannot resurrect the endpoint.
	relay.down.Store(true)
	r = start()
	_, found = r.ResolveByFQDN(one.FQDN)
	require.False(t, found)
	require.Len(t, r.Endpoints(), 2)
	require.NoError(t, r.applyEvent(liveEndpointEvent(t, sk, one, now-3600)))
	_, found = r.ResolveByFQDN(one.FQDN)
	require.False(t, found)
	require.NoError(t, r.Stop())
}

func TestResolverStoreReadyDoesNotWaitForADownRelayWhichCatchesUpAlone(t *testing.T) {
	sk, pubkey := generatedResolverKeyPair(t)
	a, b := newStoreTestRelay(t), newStoreTestRelay(t)
	b.down.Store(true)
	now := nostr.Now()
	onA, onB := endpointNamed("alpha", "10.0.0.10"), endpointNamed("beta", "10.0.0.20")
	a.add(t, liveEndpointEvent(t, sk, onA, now-3600))
	b.add(t, liveEndpointEvent(t, sk, onB, now-3500))
	path := filepath.Join(t.TempDir(), "discovery.bolt")
	caught := make(chan string, 16)
	logger := zap.New(caughtUpCore{relays: caught})

	r := New([]string{a.url, b.url}, pubkey, WithStorePath(path), WithLogger(logger))
	require.NoError(t, r.Start(t.Context()))
	waitReady(t, r)
	_, found := r.ResolveByFQDN(onA.FQDN)
	require.True(t, found)
	_, found = r.ResolveByFQDN(onB.FQDN)
	require.False(t, found)
	b.down.Store(false)
	waitRelayCaughtUp(t, caught, b.url)
	_, found = r.ResolveByFQDN(onB.FQDN)
	require.True(t, found, "the recovered relay catches up on its own")
	require.NoError(t, r.Stop())

	a.fetched.Store(0)
	b.fetched.Store(0)
	restarted := New([]string{a.url, b.url}, pubkey, WithStorePath(path))
	require.NoError(t, restarted.Start(t.Context()))
	require.Len(t, restarted.Endpoints(), 2)
	waitReady(t, restarted)
	require.NoError(t, restarted.Stop())
	require.Zero(t, a.fetched.Load()+b.fetched.Load())
}

func TestResolverStoreAddedRelayCatchesUpWithoutRedownloadingTheOthers(t *testing.T) {
	sk, pubkey := generatedResolverKeyPair(t)
	a, b := newStoreTestRelay(t), newStoreTestRelay(t)
	now := nostr.Now()
	onA, onB := endpointNamed("alpha", "10.0.0.10"), endpointNamed("beta", "10.0.0.20")
	a.add(t, liveEndpointEvent(t, sk, onA, now-3600))
	b.add(t, liveEndpointEvent(t, sk, onB, now-3500))
	path := filepath.Join(t.TempDir(), "discovery.bolt")

	first := New([]string{a.url}, pubkey, WithStorePath(path))
	require.NoError(t, first.Start(t.Context()))
	waitReady(t, first)
	require.NoError(t, first.Stop())

	a.fetched.Store(0)
	caught := make(chan string, 16)
	second := New([]string{a.url, b.url}, pubkey, WithStorePath(path), WithLogger(zap.New(caughtUpCore{relays: caught})))
	require.NoError(t, second.Start(t.Context()))
	waitRelayCaughtUp(t, caught, b.url)
	_, found := second.ResolveByFQDN(onB.FQDN)
	require.True(t, found)
	require.NoError(t, second.Stop())
	require.Zero(t, a.fetched.Load(), "the relay already synced sends nothing")
	require.Equal(t, int64(1), b.fetched.Load())
}

func TestResolverStoreNeedsTheDefaultPool(t *testing.T) {
	_, pubkey := generatedResolverKeyPair(t)
	r := New([]string{"wss://relay.example.test"}, pubkey, WithStorePath(filepath.Join(t.TempDir(), "discovery.bolt")))
	r.poolFactory = func([]string, *zap.Logger, string) relayPool { return &fakeRelayPool{} }
	require.ErrorContains(t, r.Start(t.Context()), "default relay pool")
}

// seedSupportedNIPs lists up front the NIPs khatru's NIP-11 handler adds on
// every request. The handler appends them to a shallow copy of Info whose
// slice it shares, which races when two fetches overlap (the pool's limits
// fetch and the sync worker's run concurrently); with them already listed it
// only reads.
func seedSupportedNIPs(rl *khatru.Relay) {
	if rl.DeleteEvent != nil {
		rl.Info.AddSupportedNIP("9")
	}
	if rl.Count != nil {
		rl.Info.AddSupportedNIP("45")
	}
	if rl.Negentropy {
		rl.Info.AddSupportedNIP("77")
	}
}
