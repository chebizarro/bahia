package app

import (
	"context"
	"time"

	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type nostrTransportMetricsRunner struct {
	metrics  *telemetry.Metrics
	outbox   repository.NostrEventOutboxRepository
	pools    []*nostrAdapter.RelayPool
	interval time.Duration
	logger   *zap.Logger
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
		healthy, degraded         bool
		successRate               float64
		closedReasons             map[string]int64
		reREQAttempts, reconnects int64
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
		}
	}
	for relayURL, values := range aggregated {
		r.metrics.SetNostrRelayHealth(relayURL, values.healthy, values.degraded && !values.healthy, values.successRate)
		r.metrics.SetNostrRelayTransportHealth(relayURL, values.closedReasons, values.reREQAttempts, values.reconnects)
	}
	if r.outbox == nil {
		return
	}
	depth, err := r.outbox.CountUnpublished(ctx)
	if err != nil {
		r.logger.Warn("failed to refresh Nostr outbox depth metric", zap.Error(err))
		return
	}
	r.metrics.SetNostrOutboxDepth(depth)
}
