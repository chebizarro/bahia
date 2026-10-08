package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func startCleanupTestRelay(t *testing.T) (*khatru.Relay, string) {
	t.Helper()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 100)
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return relay, "ws" + strings.TrimPrefix(server.URL, "http")
}

func cleanupTestEvent(t *testing.T, kind nostr.Kind) nostr.Event {
	t.Helper()
	ev := nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Content: t.Name()}
	require.NoError(t, ev.Sign(nostr.Generate()))
	return ev
}

// TestPoolOrphanRelayGoesThroughAdmissionGateway proves the worker-orphan
// cleanup no longer touches a raw relay: reads run through the pool's
// subscription API and deletions take an admission permit, with the kill
// switch refusing them before any frame.
func TestPoolOrphanRelayGoesThroughAdmissionGateway(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	relay, url := startCleanupTestRelay(t)
	seed := cleanupTestEvent(t, 30900)
	_, err := relay.AddEvent(ctx, seed)
	require.NoError(t, err)

	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	admission := nostrout.New(nostrout.Config{
		Aggregate:         generous,
		PurposeBudgets:    map[nostrout.Purpose]nostrout.PurposeBudget{},
		RelayWire:         generous,
		RelayWirePriority: generous,
	})
	pool := nostrpool.NewRelayPool([]string{url}, zap.NewNop(), nostrpool.WithOutboundAdmission(admission))
	defer pool.Close()
	pool.Connect(ctx)
	adapter := &poolOrphanRelay{pool: pool, timeout: 15 * time.Second}

	var found bool
	for ev := range adapter.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{30900}}) {
		found = found || ev.ID == seed.ID
	}
	require.True(t, found, "stored-event read must return the seeded record")

	deletion := cleanupTestEvent(t, 5)
	require.NoError(t, adapter.Publish(ctx, deletion))
	require.Equal(t, uint64(1), admission.Metrics().Admitted, "the deletion crossed admission")

	// The kill switch refuses the next deletion before any frame.
	killPath := filepath.Join(t.TempDir(), "stop")
	require.NoError(t, os.WriteFile(killPath, []byte("stop"), 0o600))
	killed := nostrout.New(nostrout.Config{KillSwitchFile: killPath})
	killedPool := nostrpool.NewRelayPool([]string{url}, zap.NewNop(), nostrpool.WithOutboundAdmission(killed))
	defer killedPool.Close()
	killedPool.Connect(ctx)
	killedAdapter := &poolOrphanRelay{pool: killedPool, timeout: 15 * time.Second}
	require.ErrorIs(t, killedAdapter.Publish(ctx, cleanupTestEvent(t, 5)), nostrout.ErrKillSwitch)
}

// TestCleanupRelayFactoryBuildsGatedAdapter proves the command's default
// factory hands the cleanup the pool adapter — never a raw library relay.
func TestCleanupRelayFactoryBuildsGatedAdapter(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, url := startCleanupTestRelay(t)
	root := newRootCommand()
	handle, closeRelay, err := cleanupRelayFactory(ctx, root, url)
	require.NoError(t, err)
	defer closeRelay()
	_, ok := handle.(*poolOrphanRelay)
	require.True(t, ok, "cleanup must run on the admission-gated pool adapter")
}
