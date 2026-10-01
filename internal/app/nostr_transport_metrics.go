package app

import (
	"context"
	"errors"
	"time"

	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type nostrTransportMetricsRunner struct {
	metrics  *telemetry.Metrics
	outbox   repository.NostrEventOutboxRepository
	storage  nostrEventStorageStatsSource
	pools    []*nostrAdapter.RelayPool
	interval time.Duration
	logger   *zap.Logger
	// failedIndexWarned limits the missing failed-row index warning to once;
	// only the Run goroutine touches it.
	failedIndexWarned bool
}

type nostrEventStorageStatsSource interface {
	StorageStats(context.Context) (repository.NostrEventStorageStats, error)
}

func newNostrTransportMetricsRunner(metrics *telemetry.Metrics, outbox repository.NostrEventOutboxRepository, interval time.Duration, logger *zap.Logger, pools ...*nostrAdapter.RelayPool) *nostrTransportMetricsRunner {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &nostrTransportMetricsRunner{metrics: metrics, outbox: outbox, pools: pools, interval: interval, logger: logger}
}

// refreshOutboxFailed samples the abandoned outbox row count. Until the online
// failed-row index exists the gauge reads -1 (unknown) and one warning names
// the maintenance command, rather than scanning nostr_events every interval.
func (r *nostrTransportMetricsRunner) refreshOutboxFailed(ctx context.Context) {
	failed, err := r.outbox.CountPublishFailed(ctx)
	switch {
	case errors.Is(err, repository.ErrNostrPublishFailedIndexNotReady):
		r.metrics.SetNostrOutboxFailed(-1)
		if !r.failedIndexWarned {
			r.failedIndexWarned = true
			r.logger.Warn("Nostr outbox failed-row metric unavailable until `bahia-event-archive --action ensure-indexes` builds its index")
		}
	case err != nil:
		r.logger.Warn("failed to refresh Nostr outbox failed-row metric", zap.Error(err))
	default:
		r.metrics.SetNostrOutboxFailed(failed)
	}
}

func (r *nostrTransportMetricsRunner) setStorageSource(source nostrEventStorageStatsSource) {
	r.storage = source
}

func (r *nostrTransportMetricsRunner) Name() string { return "nostr-transport-metrics" }

func (r *nostrTransportMetricsRunner) Run(ctx context.Context) error {
	r.refresh(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.refresh(ctx)
		}
	}
}

func (r *nostrTransportMetricsRunner) refresh(ctx context.Context) {
	if r.metrics == nil {
		return
	}
	type relayMetrics struct {
		healthy, degraded                               bool
		successRate                                     float64
		closedReasons                                   map[string]int64
		reREQAttempts, reconnects, closedRetryExhausted int64
	}
	aggregated := make(map[string]*relayMetrics)
	seen := make(map[*nostrAdapter.RelayPool]struct{}, len(r.pools))
	for _, pool := range r.pools {
		if pool == nil {
			continue
		}
		if _, ok := seen[pool]; ok {
			continue
		}
		seen[pool] = struct{}{}
		for _, relay := range pool.HealthSnapshot().Relays {
			values := aggregated[relay.URL]
			if values == nil {
				values = &relayMetrics{closedReasons: make(map[string]int64)}
				aggregated[relay.URL] = values
			}
			values.healthy = values.healthy || (relay.Healthy && !relay.Degraded)
			values.degraded = values.degraded || relay.Degraded
			if relay.SuccessRate > values.successRate {
				values.successRate = relay.SuccessRate
			}
			for reason, count := range relay.ClosedReasons {
				values.closedReasons[reason] += count
			}
			values.reREQAttempts += relay.ReREQAttempts
			values.reconnects += relay.ReconnectAttempts
			values.closedRetryExhausted += relay.ClosedRetryExhausted
		}
	}
	for relayURL, values := range aggregated {
		r.metrics.SetNostrRelayHealth(relayURL, values.healthy, values.degraded && !values.healthy, values.successRate)
		r.metrics.SetNostrRelayTransportHealth(relayURL, values.closedReasons, values.reREQAttempts, values.reconnects)
		r.metrics.SetNostrRelayClosedRetryExhausted(relayURL, values.closedRetryExhausted)
	}
	if r.outbox == nil {
		if r.storage == nil {
			return
		}
	} else {
		depth, err := r.outbox.CountUnpublished(ctx)
		if err != nil {
			r.logger.Warn("failed to refresh Nostr outbox depth metric", zap.Error(err))
		} else {
			r.metrics.SetNostrOutboxDepth(depth)
		}
		r.refreshOutboxFailed(ctx)
	}
	if r.storage == nil {
		return
	}
	stats, err := r.storage.StorageStats(ctx)
	if err != nil {
		r.logger.Warn("failed to refresh Nostr event storage metrics", zap.Error(err))
		return
	}
	oldestUnix := int64(0)
	if stats.OldestHotEvent != nil {
		oldestUnix = stats.OldestHotEvent.Unix()
	}
	r.metrics.SetNostrEventStorage(stats.TotalBytes, stats.HeapBytes, stats.IndexBytes, stats.EstimatedLiveRows, stats.EstimatedDeadRows, oldestUnix, stats.ArchiveBatches)
}
