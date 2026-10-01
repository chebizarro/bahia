package fipsbridge

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/boltdb"
	"fiatjaf.com/nostr/khatru"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Relay-driven bridge tests (bahia-irsry.10.5, C-37) against in-process khatru
// relays with NIP-77. Every wait is on a hosts write, which the bridge makes
// only after its first catch-up (EOSE / NIP-77 completion) and then per live
// change; nothing sleeps.

type bridgeTestRelay struct {
	relay *khatru.Relay
	url   string
	down  atomic.Bool
	// fetched counts events requested by id outside NIP-77 sessions: the
	// downloads of a reconciliation.
	fetched atomic.Int64
}

func newBridgeTestRelay(t *testing.T) *bridgeTestRelay {
	t.Helper()
	store := &boltdb.BoltBackend{Path: filepath.Join(t.TempDir(), "relay.bolt")}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	r := &bridgeTestRelay{relay: khatru.NewRelay()}
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

// add stores events as if published while the bridge was not subscribed.
func (r *bridgeTestRelay) add(t *testing.T, events ...*nostr.Event) {
	t.Helper()
	for _, ev := range events {
		_, err := r.relay.AddEvent(t.Context(), *ev)
		require.NoError(t, err)
	}
}

// publishLive delivers an event to live subscribers (storing it if the relay
// takes it: a stale replaceable version is refused but still broadcast).
func (r *bridgeTestRelay) publishLive(t *testing.T, ev *nostr.Event) {
	t.Helper()
	_, _ = r.relay.AddEvent(t.Context(), *ev)
	r.relay.BroadcastEvent(*ev)
}

func bridgeTestConfig(t *testing.T, pubkey, storePath string, relays ...*bridgeTestRelay) Config {
	t.Helper()
	urls := make([]string, 0, len(relays))
	for _, relay := range relays {
		urls = append(urls, relay.url)
	}
	return Config{BahiaPubkey: pubkey, RelayURLs: urls, HostsPath: filepath.Join(t.TempDir(), "hosts"), StorePath: storePath, HealthFilter: true}
}

type bridgeRun struct {
	writer recordingWriter
	cancel context.CancelFunc
	done   chan error
}

func startBridge(t *testing.T, cfg Config) *bridgeRun {
	t.Helper()
	bridge, err := NewBridge(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	require.NoError(t, err)
	bridge.syncLogger = zap.NewNop()
	bridge.relayBackoff = func() *nostradapter.Backoff {
		return &nostradapter.Backoff{Initial: 20 * time.Millisecond, Max: 100 * time.Millisecond, Multiplier: 2}
	}
	run := &bridgeRun{writer: recordingWriter{writes: make(chan map[string]string, 32)}, done: make(chan error, 1)}
	bridge.writer = run.writer
	ctx, cancel := context.WithCancel(t.Context())
	run.cancel = cancel
	go func() { run.done <- bridge.Run(ctx) }()
	return run
}

func (r *bridgeRun) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	require.NoError(t, <-r.done)
	r.writer.requireNoPending(t)
}

func TestBridgeWritesOnceAfterCatchUpThenAppliesLiveStateAndTombstones(t *testing.T) {
	pubkey, _ := testIdentity(t)
	a, b := newBridgeTestRelay(t), newBridgeTestRelay(t)
	base := nostr.Now() - 100
	api := endpointRecord{D: "endpoint:service:api:prod", Service: "api", Environment: "prod", FQDN: "api.prod.cascadia", Health: "healthy", Worker: workerA}
	apiNewer := api
	apiNewer.Worker = workerB
	// The web endpoint's label comes from its service tag ("web"), which its
	// tombstone does not carry; removal must follow the d coordinate.
	web := endpointRecord{D: "endpoint:service:web:prod", Service: "web", Environment: "prod", FQDN: "frontend.prod.cascadia", Health: "healthy", Worker: workerC}
	db := endpointRecord{D: "endpoint:service:db:prod", Service: "db", Environment: "prod", FQDN: "db.prod.cascadia", Health: "healthy", Worker: workerC}
	// The relays disagree, as relays that missed publishes do: each holds a
	// version the other superseded.
	a.add(t, liveEndpoint(t, pubkey, apiNewer, base+10), liveEndpoint(t, pubkey, web, base), liveEndpoint(t, pubkey, db, base))
	b.add(t, liveEndpoint(t, pubkey, api, base), endpointTombstone(t, pubkey, web.D, web.FQDN, base+5))

	run := startBridge(t, bridgeTestConfig(t, pubkey, filepath.Join(t.TempDir(), "bridge.bolt"), a, b))
	require.Equal(t, map[string]string{"api": npubOf(t, workerB), "db": npubOf(t, workerC)}, run.writer.next(t),
		"one write after catch-up, newest per coordinate with tombstones applied")

	a.publishLive(t, endpointTombstone(t, pubkey, api.D, api.FQDN, base+20))
	require.Equal(t, map[string]string{"db": npubOf(t, workerC)}, run.writer.next(t))

	// A live record older than the tombstone must not resurrect the endpoint;
	// the next write (triggered by a new endpoint) proves it was ignored.
	a.publishLive(t, liveEndpoint(t, pubkey, apiNewer, base+15))
	cache := endpointRecord{D: "endpoint:service:cache:prod", Service: "cache", Environment: "prod", FQDN: "cache.prod.cascadia", Health: "healthy", Worker: workerA}
	a.publishLive(t, liveEndpoint(t, pubkey, cache, base+21))
	require.Equal(t, map[string]string{"db": npubOf(t, workerC), "cache": npubOf(t, workerA)}, run.writer.next(t))

	// A newer live record after a tombstone re-adds the endpoint.
	a.publishLive(t, liveEndpoint(t, pubkey, api, base+30))
	require.Equal(t, map[string]string{"db": npubOf(t, workerC), "cache": npubOf(t, workerA), "api": npubOf(t, workerA)}, run.writer.next(t))
	run.stop(t)
}

func TestBridgeCatchUpWithNothingStoredLeavesHostsUntouched(t *testing.T) {
	pubkey, _ := testIdentity(t)
	relay := newBridgeTestRelay(t)
	run := startBridge(t, bridgeTestConfig(t, pubkey, filepath.Join(t.TempDir(), "bridge.bolt"), relay))
	// Whether this lands in the backfill or live, it must be the first write:
	// a catch-up write of an empty section would have come before it.
	relay.publishLive(t, liveEndpoint(t, pubkey, endpointRecord{D: "endpoint:service:api:prod", Service: "api", FQDN: "api.prod.cascadia", Health: "healthy", Worker: workerA}, nostr.Now()))
	require.Equal(t, map[string]string{"api": npubOf(t, workerA)}, run.writer.next(t))
	run.stop(t)
}

func TestBridgeRestartFetchesOnlyNewEventsAndTombstonesSurvive(t *testing.T) {
	pubkey, _ := testIdentity(t)
	relay := newBridgeTestRelay(t)
	now := nostr.Now()
	api := endpointRecord{D: "endpoint:service:api:prod", Service: "api", FQDN: "api.prod.cascadia", Health: "healthy", Worker: workerA}
	web := endpointRecord{D: "endpoint:service:web:prod", Service: "web", FQDN: "web.prod.cascadia", Health: "healthy", Worker: workerB}
	db := endpointRecord{D: "endpoint:service:db:prod", Service: "db", FQDN: "db.prod.cascadia", Health: "healthy", Worker: workerC}
	for _, rec := range []endpointRecord{api, web, db} {
		relay.add(t, liveEndpoint(t, pubkey, rec, now-3600))
	}
	storePath := filepath.Join(t.TempDir(), "bridge.bolt")
	cfg := bridgeTestConfig(t, pubkey, storePath, relay)

	run := startBridge(t, cfg)
	require.Len(t, run.writer.next(t), 3)
	run.stop(t)
	require.Equal(t, int64(3), relay.fetched.Load(), "a cold start downloads every endpoint")

	relay.fetched.Store(0)
	run = startBridge(t, cfg)
	require.Len(t, run.writer.next(t), 3, "the stored endpoints are restored and written once")
	run.stop(t)
	require.Zero(t, relay.fetched.Load(), "a restart against an unchanged relay downloads nothing")

	relay.add(t, endpointTombstone(t, pubkey, api.D, api.FQDN, now-60))
	relay.fetched.Store(0)
	run = startBridge(t, cfg)
	require.Equal(t, map[string]string{"web": npubOf(t, workerB), "db": npubOf(t, workerC)}, run.writer.next(t))
	run.stop(t)
	require.Equal(t, int64(1), relay.fetched.Load(), "only the tombstone is downloaded")

	// With no relay at all, the store alone keeps the tombstone in force.
	bridge, _ := newTestBridge(t, pubkey, nil)
	store, err := localstore.Open(storePath)
	require.NoError(t, err)
	defer store.Close()
	require.Equal(t, 3, bridge.hydrate(t.Context(), store, bridge.subscriptionFilter()))
	require.Equal(t, map[string]string{"web": npubOf(t, workerB), "db": npubOf(t, workerC)}, bridge.entries)
}

func TestBridgeRelayDownDuringBackfillCatchesUpOnItsOwn(t *testing.T) {
	pubkey, _ := testIdentity(t)
	a, b := newBridgeTestRelay(t), newBridgeTestRelay(t)
	b.down.Store(true)
	now := nostr.Now()
	a.add(t, liveEndpoint(t, pubkey, endpointRecord{D: "endpoint:service:api:prod", Service: "api", FQDN: "api.prod.cascadia", Health: "healthy", Worker: workerA}, now-3600))
	b.add(t, liveEndpoint(t, pubkey, endpointRecord{D: "endpoint:service:web:prod", Service: "web", FQDN: "web.prod.cascadia", Health: "healthy", Worker: workerB}, now-3500))
	cfg := bridgeTestConfig(t, pubkey, filepath.Join(t.TempDir(), "bridge.bolt"), a, b)

	run := startBridge(t, cfg)
	require.Equal(t, map[string]string{"api": npubOf(t, workerA)}, run.writer.next(t), "a down relay does not hold back the hosts file")
	b.down.Store(false)
	require.Equal(t, map[string]string{"api": npubOf(t, workerA), "web": npubOf(t, workerB)}, run.writer.next(t), "the recovered relay catches up on its own")
	run.stop(t)

	a.fetched.Store(0)
	b.fetched.Store(0)
	run = startBridge(t, cfg)
	require.Len(t, run.writer.next(t), 2)
	run.stop(t)
	require.Zero(t, a.fetched.Load()+b.fetched.Load())
}

func TestConfigStorePathDefaultsBesideTheHostsFile(t *testing.T) {
	cfg, err := LoadConfig([]byte("bridge:\n  hosts_path: /var/lib/fips/hosts\n"))
	require.NoError(t, err)
	require.Equal(t, "/var/lib/fips/.bahia-fips-bridge.bolt", cfg.EffectiveStorePath())
	cfg.HostsPath = "/etc/other/hosts"
	require.Equal(t, "/etc/other/.bahia-fips-bridge.bolt", cfg.EffectiveStorePath(), "the default follows a hosts path set later")
	cfg, err = LoadConfig([]byte("bridge:\n  store_path: /var/cache/fips.bolt\n"))
	require.NoError(t, err)
	require.Equal(t, "/var/cache/fips.bolt", cfg.EffectiveStorePath())
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
