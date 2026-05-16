// Package loom provides a client for interacting with Loom workers via Nostr.
package loom

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/nbd-wtf/go-nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// WorkerDiscovery subscribes to Kind 10100 events and ingests worker advertisements.
type WorkerDiscovery struct {
	pool       *nostrAdapter.RelayPool
	workerRepo repository.WorkerRepository
	logger     *zap.Logger

	ctx        context.Context
	cancel     context.CancelFunc
	refreshInt time.Duration
}

// NewWorkerDiscovery creates a new WorkerDiscovery.
func NewWorkerDiscovery(
	pool *nostrAdapter.RelayPool,
	workerRepo repository.WorkerRepository,
	logger *zap.Logger,
) *WorkerDiscovery {
	return &WorkerDiscovery{
		pool:       pool,
		workerRepo: workerRepo,
		logger:     logger,
		refreshInt: 5 * time.Minute,
	}
}

// DiscoveryOption configures WorkerDiscovery.
type DiscoveryOption func(*WorkerDiscovery)

// WithRefreshInterval sets the worker status refresh interval.
func WithRefreshInterval(d time.Duration) DiscoveryOption {
	return func(wd *WorkerDiscovery) { wd.refreshInt = d }
}

// Start begins subscribing to worker advertisements and refreshing status.
func (d *WorkerDiscovery) Start(ctx context.Context, opts ...DiscoveryOption) error {
	for _, opt := range opts {
		opt(d)
	}

	d.ctx, d.cancel = context.WithCancel(ctx)

	// Initial fetch of recent worker advertisements
	if err := d.fetchRecentWorkers(d.ctx); err != nil {
		d.logger.Warn("initial worker fetch failed", zap.Error(err))
	}

	// Start background subscription
	go d.subscribeLoop()

	// Start status refresh loop
	go d.statusRefreshLoop()

	d.logger.Info("worker discovery started",
		zap.Duration("refresh_interval", d.refreshInt),
	)
	return nil
}

// Stop halts the discovery subscription.
func (d *WorkerDiscovery) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
}

// fetchRecentWorkers queries for Kind 10100 events from the last 30 minutes.
// Uses EOSE (End of Stored Events) to detect when backfill is complete.
func (d *WorkerDiscovery) fetchRecentWorkers(ctx context.Context) error {
	since := nostr.Timestamp(time.Now().Add(-30 * time.Minute).Unix())
	filters := []nostr.Filter{{
		Kinds: []int{KindWorkerAd},
		Since: &since,
		Limit: 500, // Limit backfill to prevent memory pressure
	}}

	// Subscribe with a timeout for the overall fetch operation.
	// EOSE will signal completion before this timeout in normal conditions.
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	sub, err := d.pool.Subscribe(fetchCtx, filters)
	if err != nil {
		return err
	}

	var events []*nostr.Event

collectLoop:
	for {
		select {
		case <-fetchCtx.Done():
			d.logger.Warn("fetch context expired before EOSE received",
				zap.Int("events_collected", len(events)),
			)
			break collectLoop
		case <-sub.EndOfStoredEvents:
			// EOSE received - all stored events have been delivered.
			d.logger.Debug("EOSE received for worker fetch",
				zap.Int("events_collected", len(events)),
			)
			break collectLoop
		case ev, ok := <-sub.Events:
			if !ok {
				break collectLoop
			}
			events = append(events, ev)
		}
	}

	d.logger.Info("fetched recent worker advertisements",
		zap.Int("count", len(events)),
	)

	for _, ev := range events {
		if err := d.processWorkerEvent(ctx, ev); err != nil {
			d.logger.Warn("failed to process worker event",
				zap.String("pubkey", ev.PubKey),
				zap.Error(err),
			)
		}
	}
	return nil
}

// subscribeLoop maintains a subscription to Kind 10100 events.
func (d *WorkerDiscovery) subscribeLoop() {
	backoff := nostrAdapter.DefaultBackoff()

	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}

		filters := []nostr.Filter{{
			Kinds: []int{KindWorkerAd},
		}}

		sub, err := d.pool.Subscribe(d.ctx, filters)
		if err != nil {
			delay := backoff.Next()
			d.logger.Error("failed to subscribe to worker events",
				zap.Error(err),
				zap.Duration("retry_in", delay),
				zap.Int("attempt", backoff.Attempt()),
			)
			select {
			case <-d.ctx.Done():
				return
			case <-time.After(delay):
			}
			continue
		}

		// Reset backoff on successful subscription.
		backoff.Reset()
		d.logger.Info("subscribed to worker advertisements")

		for {
			select {
			case <-d.ctx.Done():
				return
			case ev, ok := <-sub.Events:
				if !ok {
					delay := backoff.Next()
					d.logger.Warn("worker subscription closed, reconnecting with backoff",
						zap.Duration("delay", delay),
						zap.Int("attempt", backoff.Attempt()),
					)
					select {
					case <-d.ctx.Done():
						return
					case <-time.After(delay):
					}
					break
				}
				if err := d.processWorkerEvent(d.ctx, ev); err != nil {
					d.logger.Warn("failed to process worker event",
						zap.String("pubkey", ev.PubKey),
						zap.Error(err),
					)
				}
			}
		}
	}
}

// statusRefreshLoop periodically updates worker status based on last_advertisement_at.
func (d *WorkerDiscovery) statusRefreshLoop() {
	ticker := time.NewTicker(d.refreshInt)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.refreshWorkerStatuses()
		}
	}
}

// refreshWorkerStatuses updates all workers' status based on their last advertisement time.
func (d *WorkerDiscovery) refreshWorkerStatuses() {
	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()

	// List all workers regardless of status
	workers, err := d.workerRepo.List(ctx, "", 1000)
	if err != nil {
		d.logger.Error("failed to list workers for status refresh", zap.Error(err))
		return
	}

	now := time.Now()
	var updated int
	for _, w := range workers {
		newStatus := w.ComputeStatus(now)
		if newStatus != w.Status {
			if err := d.workerRepo.UpdateStatus(ctx, w.PubKey, newStatus); err != nil {
				d.logger.Warn("failed to update worker status",
					zap.String("pubkey", w.PubKey),
					zap.Error(err),
				)
			} else {
				updated++
			}
		}
	}

	if updated > 0 {
		d.logger.Info("refreshed worker statuses",
			zap.Int("total", len(workers)),
			zap.Int("updated", updated),
		)
	}
}

// processWorkerEvent parses a Kind 10100 event and upserts the worker.
func (d *WorkerDiscovery) processWorkerEvent(ctx context.Context, ev *nostr.Event) error {
	worker, err := parseWorkerAdvertisement(ev)
	if err != nil {
		return err
	}

	if err := d.workerRepo.Upsert(ctx, worker); err != nil {
		return err
	}

	d.logger.Debug("processed worker advertisement",
		zap.String("pubkey", worker.PubKey),
		zap.String("name", worker.Name),
		zap.String("status", string(worker.Status)),
	)
	return nil
}

// workerAdContent is the JSON content of a Kind 10100 event.
type workerAdContent struct {
	Name              string                      `json:"name"`
	Description       string                      `json:"description"`
	MaxConcurrentJobs int                         `json:"max_concurrent_jobs"`
	CurrentQueueDepth int                         `json:"current_queue_depth"`
	Resources         *domain.WorkerResources     `json:"resources,omitempty"`
	Accelerators      []domain.WorkerAccelerator  `json:"accelerators,omitempty"`
	RuntimeTarget     *domain.WorkerRuntimeTarget `json:"runtime_target,omitempty"`
	MLCapabilities    domain.WorkerMLCapabilities `json:"ml_capabilities,omitempty"`
}

// parseWorkerAdvertisement parses a Kind 10100 event into a Worker.
func parseWorkerAdvertisement(ev *nostr.Event) (*domain.Worker, error) {
	var content workerAdContent
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		return nil, err
	}

	worker := &domain.Worker{
		PubKey:              ev.PubKey,
		Name:                content.Name,
		Description:         content.Description,
		MaxConcurrentJobs:   content.MaxConcurrentJobs,
		CurrentQueueDepth:   content.CurrentQueueDepth,
		Resources:           content.Resources,
		Accelerators:        content.Accelerators,
		RuntimeTarget:       content.RuntimeTarget,
		MLCapabilities:      content.MLCapabilities,
		LastAdvertisementAt: time.Unix(int64(ev.CreatedAt), 0),
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	// Parse tags
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "A":
			worker.Architecture = tag[1]
		case "S":
			// Software: ["S", "name", "version", "path"]
			if len(tag) >= 3 {
				sw := domain.WorkerSoftware{
					Name:    tag[1],
					Version: tag[2],
				}
				if len(tag) >= 4 {
					sw.Path = tag[3]
				}
				worker.Software = append(worker.Software, sw)
			}
		case "price":
			// Price: ["price", "mint_url", "price_per_second", "unit"]
			if len(tag) >= 4 {
				pps, _ := strconv.Atoi(tag[2])
				worker.Pricing = append(worker.Pricing, domain.WorkerPricing{
					MintURL:        tag[1],
					PricePerSecond: pps,
					Unit:           tag[3],
				})
			}
		case "g":
			worker.Geohash = tag[1]
		case "relay":
			worker.PreferredRelays = append(worker.PreferredRelays, tag[1])
		case "runtime":
			worker.MLCapabilities.Runtimes = append(worker.MLCapabilities.Runtimes, domain.MLRuntimeKind(tag[1]))
		case "artifact_format", "format":
			worker.MLCapabilities.ArtifactFormats = append(worker.MLCapabilities.ArtifactFormats, domain.MLArtifactFormat(tag[1]))
		case "task":
			worker.MLCapabilities.Tasks = append(worker.MLCapabilities.Tasks, domain.MLTaskKind(tag[1]))
		case "accelerator":
			worker.MLCapabilities.Accelerators = append(worker.MLCapabilities.Accelerators, tag[1])
		case "toolchain":
			worker.MLCapabilities.Toolchains = append(worker.MLCapabilities.Toolchains, tag[1])
		case "cached_artifact", "artifact":
			worker.MLCapabilities.CachedArtifacts = append(worker.MLCapabilities.CachedArtifacts, tag[1])
		case "min_duration":
			worker.MinDurationSecs, _ = strconv.Atoi(tag[1])
		case "max_duration":
			worker.MaxDurationSecs, _ = strconv.Atoi(tag[1])
		}
	}

	worker.MLCapabilities = domain.NormalizeWorkerMLCapabilities(*worker)

	// Compute initial status
	worker.Status = worker.ComputeStatus(time.Now())

	return worker, nil
}
