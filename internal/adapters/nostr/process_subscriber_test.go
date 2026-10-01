package nostr

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The DNS agent hands this adapter to its ContextVM transport. Gift wraps are
// backdated, so they are reconciled by id within the transport's window; the
// transport still validates, routes and expires the requests inside.
func TestStoreBackedSubscriberDeliversOnlyUnseenWrapsAcrossRestartsAndRelays(t *testing.T) {
	a := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	b := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	b.down.Store(true)
	sk := gonostr.Generate()
	recipient := gonostr.Generate().Public()
	now := gonostr.Now()
	tags := gonostr.Tags{{"p", recipient.Hex()}}
	onA := syncTestEvent(t, sk, 1059, now-3600, tags, "encrypted-a")
	onB := syncTestEvent(t, sk, 1059, now-3500, tags, "encrypted-b")
	a.add(t, onA)
	b.add(t, onB)
	path := filepath.Join(t.TempDir(), "dns-events.bolt")
	filter := gonostr.Filter{Kinds: []gonostr.Kind{1059, 21059}, Tags: gonostr.TagMap{"p": {recipient.Hex()}}, Since: now - 12*3600}

	first := openStoreBackedSubscription(t, path, filter, a, b)
	require.Equal(t, []gonostr.ID{onA.ID}, first.untilAllEOSE(t), "a down relay does not hold back the others")
	first.close(t)

	b.down.Store(false)
	a.resetRecorded()
	b.resetRecorded()
	second := openStoreBackedSubscription(t, path, filter, a, b)
	require.Equal(t, []gonostr.ID{onB.ID}, second.untilRelaysEOSE(t, a.url, b.url), "the recovered relay catches up on its own")
	second.close(t)
	require.Zero(t, fetchedEventCount(a.recorded()), "the relay already synced sends no wraps")
	require.Equal(t, 1, fetchedEventCount(b.recorded()))

	a.resetRecorded()
	b.resetRecorded()
	third := openStoreBackedSubscription(t, path, filter, a, b)
	require.Empty(t, third.untilRelaysEOSE(t, a.url, b.url), "a restart redelivers no handled wrap")
	third.close(t)
	require.Zero(t, fetchedEventCount(a.recorded())+fetchedEventCount(b.recorded()))
}

type storeBackedRun struct {
	merged *MergedSubscription
	pool   *RelayPool
	store  *localstore.Store
	cancel context.CancelFunc
}

func openStoreBackedSubscription(t *testing.T, path string, filter gonostr.Filter, relays ...*syncTestRelay) *storeBackedRun {
	t.Helper()
	for _, relay := range relays {
		seedSupportedNIPs(relay.relay)
	}
	store, err := localstore.Open(path)
	require.NoError(t, err)
	pool := newSyncTestPool(relays...)
	ctx, cancel := context.WithCancel(t.Context())
	pool.Connect(ctx)
	adapter := &StoreBackedSubscriber{Pool: pool, Store: store, Logger: zap.NewNop(), ReconcileKinds: []gonostr.Kind{1059}}
	merged, err := adapter.SubscribeAllWithEOSE(ctx, []gonostr.Filter{filter})
	require.NoError(t, err)
	return &storeBackedRun{merged: merged, pool: pool, store: store, cancel: cancel}
}

// untilAllEOSE collects events until EndOfStoredEvents.
func (r *storeBackedRun) untilAllEOSE(t *testing.T) []gonostr.ID {
	t.Helper()
	var ids []gonostr.ID
	for {
		select {
		case ev := <-r.merged.Events:
			ids = append(ids, ev.ID)
		case <-r.merged.EndOfStoredEvents:
			return ids
		case <-time.After(syncTestTimeout):
			t.Fatal("no EndOfStoredEvents")
		}
	}
}

// untilRelaysEOSE collects events until every named relay has caught up.
func (r *storeBackedRun) untilRelaysEOSE(t *testing.T, urls ...string) []gonostr.ID {
	t.Helper()
	pending := map[string]bool{}
	for _, url := range urls {
		pending[url] = true
	}
	var ids []gonostr.ID
	for len(pending) > 0 {
		select {
		case ev := <-r.merged.Events:
			ids = append(ids, ev.ID)
		case eose := <-r.merged.RelayEOSE:
			delete(pending, eose.RelayURL)
		case <-time.After(syncTestTimeout):
			t.Fatalf("relays %v did not catch up", pending)
		}
	}
	return ids
}

func (r *storeBackedRun) close(t *testing.T) {
	t.Helper()
	r.merged.Close()
	r.cancel()
	r.pool.Close()
	require.NoError(t, r.store.Close())
}
