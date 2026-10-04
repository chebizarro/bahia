package controlplane

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDefaultIntentSubscriberColdRelayBecomesReady(t *testing.T) {
	h := startSidecarTestHarness(t, nostr.Generate())
	store, err := localstore.Open(filepath.Join(t.TempDir(), "intent-local.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	pool := nostrAdapter.NewRelayPool([]string{h.wsURL}, zap.NewNop())
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool.Connect(ctx)
	actor := nostr.Generate().Public().Hex()
	trustSet := NewTrustSet([]string{actor}, zap.NewNop())
	readiness := NewReadinessTracker()
	readiness.RegisterFilter("intent-30900")
	processor := NewIntentProcessor(trustSet, store, nil,
		IntentProcessorConfig{EnabledDomains: BuildEnabledDomains(nil, nil)}, zap.NewNop())
	subscriber := NewIntentSubscriber(pool, store, trustSet, processor, readiness, "", zap.NewNop())
	filter := subscriber.buildFilter()
	require.Equal(t, []nostr.Kind{30900}, filter.Kinds)
	require.Len(t, filter.Authors, 1)
	require.Equal(t, actor, filter.Authors[0].Hex())
	require.False(t, readiness.IsReady())
	done := make(chan error, 1)
	go func() { done <- subscriber.Run(ctx) }()
	select {
	case <-readiness.Ready(): // EOSE from an empty relay completed catch-up.
	case <-ctx.Done():
		t.Fatal("intent subscriber did not become ready on cold relay", ctx.Err())
	}
	cancel()
	<-done
}

func TestIntentSubscriberWithoutKnownAuthorsScopesToSelf(t *testing.T) {
	self := nostr.Generate().Public().Hex()
	subscriber := NewIntentSubscriber(nil, nil, NewTrustSet(nil, zap.NewNop()), nil, nil, self, zap.NewNop())
	filter := subscriber.buildFilter()
	require.Len(t, filter.Authors, 1)
	require.Equal(t, self, filter.Authors[0].Hex())
}
