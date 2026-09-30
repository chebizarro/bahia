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
	for _, id := range []string{"failed-1", "failed-2"} {
		_, err = outbox.Record(ctx, &repository.NostrEventRecord{ID: id, PublishState: repository.NostrPublishStateFailed, CreatedAt: time.Now().UTC()})
		require.NoError(t, err)
	}

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
	require.Equal(t, int64(2), metrics.NostrOutboxFailed)
	require.Equal(t, int64(100), metrics.NostrEventStoreTotalBytes)
	require.Equal(t, int64(1234), metrics.NostrEventStoreOldestUnix)
	require.Equal(t, int64(3), metrics.NostrArchiveBatches["exported"])
}

// Without the online failed-row index the gauge reports unknown (-1) instead
// of the count a full-table scan would produce.
func TestNostrTransportMetricsRunnerFailedIndexNotReady(t *testing.T) {
	metrics := telemetry.NewMetrics()
	runner := newNostrTransportMetricsRunner(metrics, failedIndexMissingOutbox{repository.NewInMemoryNostrEventRepository()}, time.Second, zap.NewNop())
	runner.refresh(context.Background())
	require.Equal(t, int64(-1), metrics.NostrOutboxFailed)
}

type failedIndexMissingOutbox struct {
	*repository.InMemoryNostrEventRepository
}

func (failedIndexMissingOutbox) CountPublishFailed(context.Context) (int64, error) {
	return 0, repository.ErrNostrPublishFailedIndexNotReady
}

type staticNostrStorageStats struct {
	stats repository.NostrEventStorageStats
}

func (s staticNostrStorageStats) StorageStats(context.Context) (repository.NostrEventStorageStats, error) {
	return s.stats, nil
}
