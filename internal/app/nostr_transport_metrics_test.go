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
	oldest := time.Unix(1234, 0).UTC()
	runner.setStorageSource(staticNostrStorageStats{stats: repository.NostrEventStorageStats{
		TotalBytes: 100, HeapBytes: 60, IndexBytes: 30, EstimatedLiveRows: 7, EstimatedDeadRows: 2,
		OldestHotEvent: &oldest, ArchiveBatches: map[string]int64{"exported": 3},
	}})
	runner.refresh(ctx)

	require.Equal(t, int64(1), metrics.NostrRelayClosedReasons["wss://relay.example"]["auth-required"])
	require.Equal(t, int64(1), metrics.NostrRelayReREQAttempts["wss://relay.example"])
	require.Equal(t, int64(1), metrics.NostrOutboxDepth)
	require.Equal(t, int64(100), metrics.NostrEventStoreTotalBytes)
	require.Equal(t, int64(1234), metrics.NostrEventStoreOldestUnix)
	require.Equal(t, int64(3), metrics.NostrArchiveBatches["exported"])
}

type staticNostrStorageStats struct {
	stats repository.NostrEventStorageStats
}

func (s staticNostrStorageStats) StorageStats(context.Context) (repository.NostrEventStorageStats, error) {
	return s.stats, nil
}
