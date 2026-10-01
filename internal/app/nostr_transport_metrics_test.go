package app

import (
	"context"
	"testing"
	"time"

	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/repository/repositorytest"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNostrTransportMetricsRunnerRefresh(t *testing.T) {
	ctx := context.Background()
	pool := nostrAdapter.NewRelayPool([]string{"wss://relay.example"}, zap.NewNop())
	pool.RecordRelayClosed("wss://relay.example", "auth-required: sign in")
	pool.RecordRelayReREQ()

	outbox := repositorytest.NewInMemoryNostrEventRepository()
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
	// Archive rows of the local outbox are counted by the local outbox.
	_, err = outbox.Record(ctx, &repository.NostrEventRecord{ID: "archived-pending", PublishState: repository.NostrPublishStatePending,
		PublishTarget: repository.LocalOutboxArchiveTarget(repository.NostrPublishTargetControlPlane), CreatedAt: time.Now().UTC()})
	require.NoError(t, err)

	metrics := telemetry.NewMetrics()
	local := staticLocalOutbox{counts: localstore.OutboxCounts{Pending: 3, Failed: 4}}
	runner := newNostrTransportMetricsRunner(metrics, local, outbox, time.Second, zap.NewNop(), pool)
	oldest := time.Unix(1234, 0).UTC()
	runner.setStorageSource(staticNostrStorageStats{stats: repository.NostrEventStorageStats{
		TotalBytes: 100, HeapBytes: 60, IndexBytes: 30, EstimatedLiveRows: 7, EstimatedDeadRows: 2,
		OldestHotEvent: &oldest, ArchiveBatches: map[string]int64{"exported": 3},
	}})
	runner.refresh(ctx)

	require.Equal(t, int64(1), metrics.NostrRelayClosedReasons["wss://relay.example"]["auth-required"])
	require.Equal(t, int64(1), metrics.NostrRelayReREQAttempts["wss://relay.example"])
	require.Equal(t, int64(3+1), metrics.NostrOutboxDepth, "local pending entries plus drained PostgreSQL rows")
	require.Equal(t, int64(4+2), metrics.NostrOutboxFailed)
	require.Equal(t, int64(100), metrics.NostrEventStoreTotalBytes)
	require.Equal(t, int64(1234), metrics.NostrEventStoreOldestUnix)
	require.Equal(t, int64(3), metrics.NostrArchiveBatches["exported"])
}

// Without the online failed-row index the PostgreSQL rows are not counted
// (a full-table scan would be needed): the gauge reports unknown (-1) when it
// is the only outbox, and the local outbox's count otherwise.
func TestNostrTransportMetricsRunnerFailedIndexNotReady(t *testing.T) {
	metrics := telemetry.NewMetrics()
	runner := newNostrTransportMetricsRunner(metrics, nil, failedIndexMissingOutbox{repositorytest.NewInMemoryNostrEventRepository()}, time.Second, zap.NewNop())
	runner.refresh(context.Background())
	require.Equal(t, int64(-1), metrics.NostrOutboxFailed)

	runner = newNostrTransportMetricsRunner(metrics, staticLocalOutbox{counts: localstore.OutboxCounts{Failed: 2}}, failedIndexMissingOutbox{repositorytest.NewInMemoryNostrEventRepository()}, time.Second, zap.NewNop())
	runner.refresh(context.Background())
	require.Equal(t, int64(2), metrics.NostrOutboxFailed)
}

// Without PostgreSQL the gauges come from the local outbox alone.
func TestNostrTransportMetricsRunnerLocalOutboxOnly(t *testing.T) {
	metrics := telemetry.NewMetrics()
	runner := newNostrTransportMetricsRunner(metrics, staticLocalOutbox{counts: localstore.OutboxCounts{Pending: 5, Failed: 1}}, nil, time.Second, zap.NewNop())
	runner.refresh(context.Background())
	require.Equal(t, int64(5), metrics.NostrOutboxDepth)
	require.Equal(t, int64(1), metrics.NostrOutboxFailed)
}

type staticLocalOutbox struct{ counts localstore.OutboxCounts }

func (o staticLocalOutbox) Counts() (localstore.OutboxCounts, error) { return o.counts, nil }

type failedIndexMissingOutbox struct {
	*repositorytest.InMemoryNostrEventRepository
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
