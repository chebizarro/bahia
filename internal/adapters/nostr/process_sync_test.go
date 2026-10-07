package nostr

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ProcessSync tests against in-process khatru relays. Waits
// are on CaughtUp/RelayCaughtUp, which the consumer raises from EOSE and
// NIP-77 completion; nothing sleeps.

// processSyncRun is one ProcessSync run and what it applied.
type processSyncRun struct {
	store   *localstore.Store
	pool    *RelayPool
	cancel  context.CancelFunc
	done    chan error
	applied chan gonostr.Event
	relays  chan string
	ready   chan struct{}
}

func startProcessSync(t *testing.T, path string, filter gonostr.Filter, reconcile []gonostr.Kind, relays ...*syncTestRelay) *processSyncRun {
	t.Helper()
	for _, relay := range relays {
		seedSupportedNIPs(relay.relay)
	}
	store, err := localstore.Open(path)
	require.NoError(t, err)
	run := &processSyncRun{
		store:   store,
		pool:    newSyncTestPool(relays...),
		done:    make(chan error, 1),
		applied: make(chan gonostr.Event, 256),
		relays:  make(chan string, 16),
		ready:   make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(t.Context())
	run.cancel = cancel
	run.pool.Connect(ctx)
	syncer := &ProcessSync{
		Pool: run.pool, Store: store, Logger: zap.NewNop(),
		ReconcileKinds: reconcile,
		RelayBackoff:   fastTestBackoff,
		Apply:          func(_ context.Context, ev *gonostr.Event) { run.applied <- *ev },
		RelayCaughtUp:  func(relay string) { run.relays <- relay },
		CaughtUp:       func() { close(run.ready) },
	}
	go func() { run.done <- syncer.Run(ctx, []gonostr.Filter{filter}) }()
	return run
}

func (r *processSyncRun) waitReady(t *testing.T) {
	t.Helper()
	select {
	case <-r.ready:
	case <-time.After(syncTestTimeout):
		t.Fatal("process sync did not catch up")
	}
}

// waitRelays waits until each named relay has caught up (in any order).
func (r *processSyncRun) waitRelays(t *testing.T, urls ...string) {
	t.Helper()
	pending := map[string]bool{}
	for _, url := range urls {
		pending[url] = true
	}
	deadline := time.After(syncTestTimeout)
	for len(pending) > 0 {
		select {
		case got := <-r.relays:
			delete(pending, got)
		case <-deadline:
			t.Fatalf("relays %v did not catch up", pending)
		}
	}
}

// stop ends the run and returns the events it applied.
func (r *processSyncRun) stop(t *testing.T) []gonostr.Event {
	t.Helper()
	r.cancel()
	require.NoError(t, <-r.done)
	r.pool.Close()
	require.NoError(t, r.store.Close())
	var applied []gonostr.Event
	for {
		select {
		case ev := <-r.applied:
			applied = append(applied, ev)
		default:
			return applied
		}
	}
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

func appliedIDs(events []gonostr.Event) []gonostr.ID {
	ids := make([]gonostr.ID, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ID)
	}
	return ids
}

// fetchedEventCount counts the events a relay was asked to send by id: the
// downloads of NIP-77 reconciliation (and of nothing else in these tests).
func fetchedEventCount(reqs []recordedReq) int {
	total := 0
	for _, req := range reqs {
		if !req.negentropy && len(req.filter.IDs) > 0 {
			total += len(req.filter.IDs)
		}
	}
	return total
}

func TestProcessSyncRestartFetchesOnlyMissingEventsAndKeepsTombstone(t *testing.T) {
	relay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	sk := gonostr.Generate()
	now := gonostr.Now()
	filter := gonostr.Filter{Kinds: []gonostr.Kind{syncTestStateKind}, Authors: []gonostr.PubKey{sk.Public()}}
	keep := syncTestEvent(t, sk, syncTestStateKind, now-3600, gonostr.Tags{{"d", "keep"}}, "live")
	gone := syncTestEvent(t, sk, syncTestStateKind, now-3600, gonostr.Tags{{"d", "gone"}}, "live")
	relay.add(t, keep, gone)
	path := filepath.Join(t.TempDir(), "process.bolt")

	first := startProcessSync(t, path, filter, nil, relay)
	first.waitReady(t)
	require.Len(t, first.stop(t), 2)
	require.Equal(t, 2, fetchedEventCount(relay.recorded()))

	tombstone := syncTestEvent(t, sk, syncTestStateKind, now-60, gonostr.Tags{{"d", "gone"}, {"deleted", "true"}}, "")
	relay.add(t, tombstone)
	relay.resetRecorded()
	second := startProcessSync(t, path, filter, nil, relay)
	second.waitReady(t)
	applied := second.stop(t)
	require.Len(t, applied, 1, "only the tombstone is new")
	require.Equal(t, tombstone.ID, applied[0].ID)
	require.Equal(t, 1, fetchedEventCount(relay.recorded()), "a restart downloads only what the store lacks")

	relay.resetRecorded()
	third := startProcessSync(t, path, filter, nil, relay)
	third.waitReady(t)
	require.Empty(t, third.stop(t), "nothing already held is applied again")
	require.Zero(t, fetchedEventCount(relay.recorded()))

	store, err := localstore.Open(path)
	require.NoError(t, err)
	defer store.Close()
	held := map[gonostr.ID]bool{}
	for ev := range store.QueryEvents(filter) {
		held[ev.ID] = true
	}
	require.Equal(t, map[gonostr.ID]bool{keep.ID: true, tombstone.ID: true}, held,
		"the store replays the tombstone, not the version it replaced")
}

func TestProcessSyncCaughtUpDoesNotWaitForADownRelayWhichCatchesUpAlone(t *testing.T) {
	a := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	b := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	b.down.Store(true)
	sk := gonostr.Generate()
	now := gonostr.Now()
	filter := gonostr.Filter{Kinds: []gonostr.Kind{syncTestStateKind}, Authors: []gonostr.PubKey{sk.Public()}}
	onA := syncTestEvent(t, sk, syncTestStateKind, now-3600, gonostr.Tags{{"d", "a"}}, "a")
	onB := syncTestEvent(t, sk, syncTestStateKind, now-3500, gonostr.Tags{{"d", "b"}}, "b")
	a.add(t, onA)
	b.add(t, onB)
	path := filepath.Join(t.TempDir(), "process.bolt")

	run := startProcessSync(t, path, filter, nil, a, b)
	run.waitReady(t)
	b.down.Store(false)
	run.waitRelays(t, b.url)
	require.ElementsMatch(t, []gonostr.ID{onA.ID, onB.ID}, appliedIDs(run.stop(t)))

	a.resetRecorded()
	b.resetRecorded()
	restarted := startProcessSync(t, path, filter, nil, a, b)
	restarted.waitRelays(t, a.url, b.url)
	require.Empty(t, restarted.stop(t))
	require.Zero(t, fetchedEventCount(a.recorded())+fetchedEventCount(b.recorded()))
}

func TestProcessSyncAppliesAnEventHeldByTwoRelaysOnce(t *testing.T) {
	a := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	b := startSyncTestRelay(t, syncTestRelayOptions{})
	sk := gonostr.Generate()
	filter := gonostr.Filter{Kinds: []gonostr.Kind{syncTestStateKind}, Authors: []gonostr.PubKey{sk.Public()}}
	shared := syncTestEvent(t, sk, syncTestStateKind, gonostr.Now()-60, gonostr.Tags{{"d", "x"}}, "x")
	a.add(t, shared)
	b.add(t, shared)

	run := startProcessSync(t, filepath.Join(t.TempDir(), "process.bolt"), filter, nil, a, b)
	run.waitRelays(t, a.url, b.url)
	require.Len(t, run.stop(t), 1)
}

// Gift wraps are regular events with backdated created_at, so a cursor would
// miss them; ReconcileKinds reconciles them by id within the filter's window.
func TestProcessSyncReconcilesBackdatedRegularKindsByID(t *testing.T) {
	relay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	sk := gonostr.Generate()
	recipient := gonostr.Generate().Public()
	now := gonostr.Now()
	tags := gonostr.Tags{{"p", recipient.Hex()}}
	filter := gonostr.Filter{Kinds: []gonostr.Kind{1059}, Tags: gonostr.TagMap{"p": {recipient.Hex()}}, Since: now - 12*3600}
	first := syncTestEvent(t, sk, 1059, now-600, tags, "wrap-1")
	relay.add(t, first)
	path := filepath.Join(t.TempDir(), "process.bolt")

	run := startProcessSync(t, path, filter, []gonostr.Kind{1059}, relay)
	run.waitReady(t)
	require.Len(t, run.stop(t), 1)

	// Backdated below the newest event already held: a cursor resume would
	// start after it.
	backdated := syncTestEvent(t, sk, 1059, now-6*3600, tags, "wrap-2")
	relay.add(t, backdated)
	relay.resetRecorded()
	restarted := startProcessSync(t, path, filter, []gonostr.Kind{1059}, relay)
	restarted.waitReady(t)
	applied := restarted.stop(t)
	require.Len(t, applied, 1)
	require.Equal(t, backdated.ID, applied[0].ID)
	require.Equal(t, 1, fetchedEventCount(relay.recorded()))
}

// The live REQ every filter gets starts minutes before the catch-up; a wrap
// backdated by hours must still arrive live, through the live-only REQ.
func TestProcessSyncDeliversBackdatedReconcileKindsLive(t *testing.T) {
	relay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	sk := gonostr.Generate()
	recipient := gonostr.Generate().Public()
	now := gonostr.Now()
	tags := gonostr.Tags{{"p", recipient.Hex()}}
	filter := gonostr.Filter{Kinds: []gonostr.Kind{1059}, Tags: gonostr.TagMap{"p": {recipient.Hex()}}, Since: now - 12*3600}
	run := startProcessSync(t, filepath.Join(t.TempDir(), "process.bolt"), filter, []gonostr.Kind{1059}, relay)
	run.waitReady(t)
	relay.resetRecorded()

	backdated := syncTestEvent(t, sk, 1059, now-5*3600, tags, "wrap")
	relay.publishLive(t, backdated)
	select {
	case ev := <-run.applied:
		require.Equal(t, backdated.ID, ev.ID)
	case <-time.After(syncTestTimeout):
		t.Fatal("backdated wrap was not delivered live")
	}
	require.Empty(t, run.stop(t), "delivered once")
	require.Zero(t, fetchedEventCount(relay.recorded()))
}

func TestProcessInboundFiltersKeepTheReconcileWindow(t *testing.T) {
	filters, window, err := processInboundFilters([]gonostr.Filter{{
		Kinds: []gonostr.Kind{1059, 21059, syncTestStateKind},
		Since: 1000,
	}}, []gonostr.Kind{1059})
	require.NoError(t, err)
	require.True(t, window.present)
	require.Equal(t, gonostr.Timestamp(1000), window.floor)
	require.Equal(t, []gonostr.Filter{{Kinds: []gonostr.Kind{1059}, Since: 1000, LimitZero: true}}, window.live,
		"the live-only REQ keeps the window and asks for no stored events")
	require.Len(t, filters, 3)
	byKind := map[gonostr.Kind]inboundFilter{}
	for _, filter := range filters {
		byKind[filter.filter.Kinds[0]] = filter
	}
	require.True(t, byKind[1059].persistent)
	require.Equal(t, gonostr.Timestamp(1000), byKind[1059].filter.Since)
	require.True(t, byKind[syncTestStateKind].persistent)
	require.Zero(t, byKind[syncTestStateKind].filter.Since)
	require.False(t, byKind[21059].persistent)

	moved := gonostr.Filter{Kinds: []gonostr.Kind{1059}, Since: 2000}
	again, _, err := processInboundFilters([]gonostr.Filter{moved}, []gonostr.Kind{1059})
	require.NoError(t, err)
	require.Equal(t, byKind[1059].hash, again[0].hash, "a moving window keeps one filter identity")
}
