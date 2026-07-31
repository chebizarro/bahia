package app

import (
	"context"
	"testing"
	"time"

	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNostrTransportMetricsRunnerRefresh(t *testing.T) {
	ctx := context.Background()
	pool := nostrAdapter.NewRelayPool([]string{"wss://relay.example"}, zap.NewNop())
	pool.RecordRelayClosed("wss://relay.example", "auth-required: sign in")
	pool.RecordRelayReREQ()

	outbox := repository.NewInMemoryNostrEventRepository()
	_, err := outbox.Record(ctx, &repository.NostrEventRecord{
		ID:           "pending-event",
		PublishState: repository.NostrPublishStatePending,
		CreatedAt:    time.Now().UTC(),
	})
	require.NoError(t, err)

	metrics := telemetry.NewMetrics()
	runner := newNostrTransportMetricsRunner(metrics, outbox, time.Second, zap.NewNop(), pool)
	runner.refresh(ctx)

	require.Equal(t, int64(1), metrics.NostrRelayClosedReasons["wss://relay.example"]["auth-required"])
	require.Equal(t, int64(1), metrics.NostrRelayReREQAttempts["wss://relay.example"])
	require.Equal(t, int64(1), metrics.NostrOutboxDepth)
}
