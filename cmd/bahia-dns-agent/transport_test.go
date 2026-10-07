package main

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
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/khatru"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	dnsagent "github.com/openagentsinc/bahia/internal/dnsagent/agent"
	"github.com/openagentsinc/bahia/internal/dnsagent/engine"
	"github.com/openagentsinc/bahia/internal/dnsagent/protocol"
	pkgclient "github.com/openagentsinc/bahia/pkg/client"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// End-to-end tests of the agent's request transport on its local event store
// real encrypted ContextVM requests from pkg/client, two
// in-process khatru relays with NIP-77, and the agent's own handlers. Waits
// are on the transport's per-relay EOSE, on relay storage hooks and on handler
// calls; nothing sleeps.

const agentTestTimeout = 20 * time.Second

type agentTestRelay struct {
	relay *khatru.Relay
	url   string
	down  atomic.Bool
	// fetched counts events requested by id outside NIP-77 sessions: the
	// downloads of a reconciliation.
	fetched atomic.Int64
	wraps   chan nostr.ID
}

func newAgentTestRelay(t *testing.T) *agentTestRelay {
	t.Helper()
	store := &boltdb.BoltBackend{Path: filepath.Join(t.TempDir(), "relay.bolt")}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	r := &agentTestRelay{relay: khatru.NewRelay(), wraps: make(chan nostr.ID, 64)}
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
	r.relay.OnEventSaved = func(_ context.Context, ev nostr.Event) {
		if ev.Kind == controlplane.KindContextVMGiftWrap {
			r.wraps <- ev.ID
		}
	}
	server := httptest.NewServer(r.relay)
	t.Cleanup(server.Close)
	r.url = nostr.NormalizeURL("ws" + strings.TrimPrefix(server.URL, "http"))
	return r
}

// eoseCore reports the relays the transport logs as caught up.
type eoseCore struct{ relays chan string }

func (c eoseCore) Enabled(zapcore.Level) bool        { return true }
func (c eoseCore) With([]zapcore.Field) zapcore.Core { return c }
func (c eoseCore) Sync() error                       { return nil }
func (c eoseCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return checked.AddCore(entry, c)
}

func (c eoseCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// The local ContextVM ledger path ( item 1) logs "ContextVM
	// requests caught up" at EOSE; the non-local path logs "relay sent
	// ContextVM encrypted request EOSE". Match either so the test works
	// regardless of which path is active.
	if entry.Message != "ContextVM requests caught up" && entry.Message != "relay sent ContextVM encrypted request EOSE" {
		return nil
	}
	for _, field := range fields {
		if field.Key == "relay" {
			c.relays <- field.String
		}
	}
	return nil
}

type agentFixture struct {
	agentKey      string
	agentPubkey   string
	requesterKey  nostr.SecretKey
	requesterPub  string
	includeDir    string
	statePath     string
	handledHealth chan struct{}
}

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	agentSK := nostr.Generate()
	requesterSK := nostr.Generate()
	dir := t.TempDir()
	return &agentFixture{
		agentKey:      agentSK.Hex(),
		agentPubkey:   agentSK.Public().Hex(),
		requesterKey:  requesterSK,
		requesterPub:  requesterSK.Public().Hex(),
		includeDir:    dir,
		statePath:     filepath.Join(dir, "state", "dns-agent.json"),
		handledHealth: make(chan struct{}, 16),
	}
}

type agentRun struct {
	relays chan string
	cancel context.CancelFunc
	done   chan error
	pool   *nostradapter.RelayPool
	store  *localstore.Store
}

// start runs the agent's transport as run wires it, counting health calls.
func (f *agentFixture) start(t *testing.T, storePath string, relays ...*agentTestRelay) *agentRun {
	t.Helper()
	eng := engine.New(engine.Config{
		IncludeDir: f.includeDir,
		FilePrefix: "bahia-",
		Reload:     engine.ReloadConfig{ExplicitCommand: "reload dnsmasq"},
		Runner:     func(context.Context, []string) error { return nil },
	})
	service, err := dnsagent.New(dnsagent.Config{Engine: eng, IncludeDir: f.includeDir, FilePrefix: "bahia-", AllowedZones: []string{"example.internal"}, StateFilePath: f.statePath})
	require.NoError(t, err)
	store, err := localstore.Open(storePath)
	require.NoError(t, err)
	urls := make([]string, 0, len(relays))
	for _, relay := range relays {
		urls = append(urls, relay.url)
	}
	run := &agentRun{relays: make(chan string, 16), done: make(chan error, 1), store: store}
	logger := zap.New(eoseCore{relays: run.relays})
	run.pool = nostradapter.NewRelayPool(urls, zap.NewNop(), nostradapter.WithPrivateKey(f.agentKey))
	transport, err := newRequestTransport(run.pool, store, f.agentKey, f.requesterPub, logger)
	require.NoError(t, err)
	service.RegisterHandlers(transport)
	transport.RegisterContextVMHandler(protocol.MethodHealth, func(ctx context.Context, request controlplane.ContextVMRequest) (any, error) {
		f.handledHealth <- struct{}{}
		return service.HealthHandler(ctx, request)
	})
	ctx, cancel := context.WithCancel(t.Context())
	run.cancel = cancel
	run.pool.Connect(ctx)
	go func() { run.done <- transport.Run(ctx) }()
	return run
}

func (r *agentRun) waitCaughtUp(t *testing.T, urls ...string) {
	t.Helper()
	pending := map[string]bool{}
	for _, url := range urls {
		pending[url] = true
	}
	deadline := time.After(agentTestTimeout)
	for len(pending) > 0 {
		select {
		case relay := <-r.relays:
			delete(pending, relay)
		case <-deadline:
			t.Fatalf("relays %v did not catch up", pending)
		}
	}
}

func (r *agentRun) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	// Run ends with ctx's error, or nil when it sees the event stream close
	// first; supervise treats both as a stop once ctx is done.
	if err := <-r.done; err != nil {
		require.ErrorIs(t, err, context.Canceled)
	}
	r.pool.Close()
	require.NoError(t, r.store.Close())
}

// requestHealth sends one encrypted health request through relays and returns
// once a relay has stored its gift wrap; the reply is not awaited.
func (f *agentFixture) requestHealth(t *testing.T, requestID string, relays ...*agentTestRelay) {
	t.Helper()
	urls := make([]string, 0, len(relays))
	for _, relay := range relays {
		urls = append(urls, relay.url)
	}
	retries := 0
	client, err := pkgclient.NewContextVMRequestClient(pkgclient.ContextVMRequestConfig{
		Relays: urls, Signer: keyer.NewPlainKeySigner(f.requesterKey), SenderPubkey: f.requesterPub,
		RecipientPubkey: f.agentPubkey, Encrypted: true, ResultTimeout: agentTestTimeout, ResultRetries: &retries,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done; client.Close() })
	go func() {
		defer close(done)
		_, _ = client.Request(ctx, protocol.MethodHealth, protocol.HealthParams{Schema: protocol.Schema}, nostr.Tags{{"d", requestID}}, nil)
	}()
	select {
	case <-relays[0].wraps:
	case <-time.After(agentTestTimeout):
		t.Fatal("request wrap was not stored")
	}
}

func (f *agentFixture) waitHandled(t *testing.T) {
	t.Helper()
	select {
	case <-f.handledHealth:
	case <-time.After(agentTestTimeout):
		t.Fatal("request was not handled")
	}
}

func (f *agentFixture) requireNoneHandled(t *testing.T) {
	t.Helper()
	select {
	case <-f.handledHealth:
		t.Fatal("a request was handled again")
	default:
	}
}

func TestAgentTransportRestartNeitherRefetchesNorRehandlesRequests(t *testing.T) {
	f := newAgentFixture(t)
	a, b := newAgentTestRelay(t), newAgentTestRelay(t)
	storePath := filepath.Join(t.TempDir(), "events.bolt")

	run := f.start(t, storePath, a, b)
	run.waitCaughtUp(t, a.url, b.url)
	f.requestHealth(t, "health-1", a, b)
	f.waitHandled(t)
	run.stop(t)
	f.requireNoneHandled(t)

	// Without the store (a cold start), the wrap is replayed from the relay
	// and handled again because the ledger has no record of it.
	cold := f.start(t, filepath.Join(t.TempDir(), "cold.bolt"), a, b)
	cold.waitCaughtUp(t, a.url, b.url)
	f.waitHandled(t)
	cold.stop(t)

	// With the same store, the request is already claimed — not re-handled.
	restarted := f.start(t, storePath, a, b)
	restarted.waitCaughtUp(t, a.url, b.url)
	restarted.stop(t)
	f.requireNoneHandled(t)
}

func TestAgentTransportRelayDownDuringBackfillCatchesUpOnItsOwn(t *testing.T) {
	f := newAgentFixture(t)
	a, b := newAgentTestRelay(t), newAgentTestRelay(t)
	storePath := filepath.Join(t.TempDir(), "events.bolt")
	run := f.start(t, storePath, a, b)
	run.waitCaughtUp(t, a.url, b.url)
	run.stop(t)

	// A request reaches only b while the agent is stopped, and b is down when
	// the agent comes back.
	f.requestHealth(t, "health-b", b)
	b.down.Store(true)
	run = f.start(t, storePath, a, b)
	run.waitCaughtUp(t, a.url)
	f.requireNoneHandled(t)
	b.down.Store(false)
	f.waitHandled(t)
	run.waitCaughtUp(t, b.url)
	run.stop(t)
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
