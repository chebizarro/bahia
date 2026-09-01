package nostr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"go.uber.org/zap"
)

type BootstrapPhase string

const (
	BootstrapPhaseInit        BootstrapPhase = "init"
	BootstrapPhaseSnapshot    BootstrapPhase = "snapshot"
	BootstrapPhaseLiveCatchup BootstrapPhase = "live_catchup"
	BootstrapPhaseReady       BootstrapPhase = "ready"
	BootstrapPhaseFailed      BootstrapPhase = "failed"
)

const maxBootstrapRetryInterval = 5 * time.Minute

type BootstrapProgress struct {
	Phase          BootstrapPhase
	RequestedTier  int
	ReadyTier      int
	GroupsTotal    int
	GroupsComplete int
	StartedAt      time.Time
}

type BootstrapConfig struct {
	RequestedTier       int
	SnapshotTimeout     time.Duration
	CatchupTimeout      time.Duration
	RetryInterval       time.Duration
	ProjectionAuthors   []string
	ControlPlaneAuthors []string
}

type BootstrapCacheApplier interface {
	Apply(ctx context.Context, event *DecodedProjectionEvent) error
}

type BootstrapStatusPublisher interface {
	PublishCheckpoint(ctx context.Context, payload interface{}) error
	PublishReadiness(ctx context.Context, payload interface{}) error
}

type Bootstrapper struct {
	pool          *RelayPool
	catalog       *KindCatalog
	cursorPlanner *ReplayCursorPlanner
	cache         BootstrapCacheApplier
	logger        *zap.Logger
	mu            sync.RWMutex
	progress      BootstrapProgress
	config        BootstrapConfig
}

type bootstrapEventDecodeError struct {
	err error
}

func (e *bootstrapEventDecodeError) Error() string {
	if e == nil || e.err == nil {
		return "bootstrap event decode error"
	}
	return e.err.Error()
}

func (e *bootstrapEventDecodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

var bootstrapSubscribeAllWithEOSE = func(pool *RelayPool, ctx context.Context, filters []gonostr.Filter) (*MergedSubscription, error) {
	if pool == nil {
		return nil, errors.New("relay pool is required")
	}
	return pool.SubscribeAllWithEOSE(ctx, filters)
}

func NewBootstrapper(pool *RelayPool, catalog *KindCatalog, cursorPlanner *ReplayCursorPlanner, cache BootstrapCacheApplier, logger *zap.Logger, config BootstrapConfig) *Bootstrapper {
	if catalog == nil {
		catalog = NewKindCatalog()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if config.SnapshotTimeout <= 0 {
		config.SnapshotTimeout = 30 * time.Second
	}
	if config.CatchupTimeout <= 0 {
		config.CatchupTimeout = 15 * time.Second
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = 30 * time.Second
	}
	if config.RequestedTier < 0 {
		config.RequestedTier = 0
	}
	if config.RequestedTier > 3 {
		config.RequestedTier = 3
	}

	return &Bootstrapper{
		pool:          pool,
		catalog:       catalog,
		cursorPlanner: cursorPlanner,
		cache:         cache,
		logger:        logger.Named("bootstrapper"),
		config:        config,
		progress: BootstrapProgress{
			Phase:         BootstrapPhaseInit,
			RequestedTier: config.RequestedTier,
			ReadyTier:     -1,
		},
	}
}

func (b *Bootstrapper) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	retryInterval := b.config.RetryInterval
	if retryInterval <= 0 {
		retryInterval = 30 * time.Second
	}
	for {
		err := b.attemptBootstrap(ctx)
		if err == nil {
			return nil
		}

		b.setPhase(BootstrapPhaseFailed)
		b.logger.Warn("bootstrap attempt failed, retrying",
			zap.Error(err),
			zap.Duration("retry_interval", retryInterval))

		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}

		retryInterval *= 2
		if retryInterval > maxBootstrapRetryInterval {
			retryInterval = maxBootstrapRetryInterval
		}
	}
}

func (b *Bootstrapper) attemptBootstrap(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	startedAt := time.Now().UTC()
	groups := b.requiredGroupsAtOrBelowRequestedTier()
	b.setProgress(func(progress *BootstrapProgress) {
		progress.Phase = BootstrapPhaseInit
		progress.RequestedTier = b.config.RequestedTier
		progress.ReadyTier = -1
		progress.GroupsTotal = len(groups)
		progress.GroupsComplete = 0
		progress.StartedAt = startedAt
	})

	completed := make(map[string]bool, len(groups))
	decodedEvents := 0

	b.setPhase(BootstrapPhaseSnapshot)
	for _, group := range groups {
		if !group.Snapshot {
			continue
		}
		filters, filterErr := b.snapshotFilters(group, startedAt)
		if filterErr != nil {
			b.logger.Warn("bootstrap snapshot filter build failed", zap.String("group", group.Name), zap.Error(filterErr))
			continue
		}
		ok, applied, err := b.runGroup(ctx, group, filters, b.config.SnapshotTimeout, true)
		decodedEvents += applied
		if err != nil {
			b.logger.Warn("bootstrap snapshot group failed", zap.String("group", group.Name), zap.Error(err))
		}
		if ok {
			completed[group.Name] = true
			b.incrementGroupsComplete()
		}
	}

	b.setPhase(BootstrapPhaseLiveCatchup)
	for _, group := range groups {
		if group.Snapshot {
			continue
		}
		filters, filterErr := b.liveFilters(ctx, group, startedAt)
		if filterErr != nil {
			b.logger.Warn("bootstrap live filter build failed", zap.String("group", group.Name), zap.Error(filterErr))
			continue
		}
		ok, applied, err := b.runGroup(ctx, group, filters, b.config.CatchupTimeout, false)
		decodedEvents += applied
		if err != nil {
			b.logger.Warn("bootstrap live catch-up group failed", zap.String("group", group.Name), zap.Error(err))
		}
		if ok {
			completed[group.Name] = true
			b.incrementGroupsComplete()
		}
	}

	readyTier := b.computeReadyTier(completed)
	if readyTier < 0 || decodedEvents == 0 {
		b.setProgress(func(progress *BootstrapProgress) {
			progress.Phase = BootstrapPhaseFailed
			progress.ReadyTier = -1
		})
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("bootstrap failed: no required tier established")
	}

	b.setProgress(func(progress *BootstrapProgress) {
		progress.Phase = BootstrapPhaseReady
		progress.ReadyTier = readyTier
	})
	return nil
}

func (b *Bootstrapper) Progress() BootstrapProgress {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.progress
}

func (b *Bootstrapper) Ready() bool {
	return b.Progress().Phase == BootstrapPhaseReady
}

func (b *Bootstrapper) ReadyTier() int {
	return b.Progress().ReadyTier
}

func (b *Bootstrapper) requiredGroupsAtOrBelowRequestedTier() []ReplayGroup {
	if b == nil || b.catalog == nil {
		return nil
	}
	return b.catalog.RequiredGroupsForTier(b.config.RequestedTier)
}

func (b *Bootstrapper) runGroup(ctx context.Context, group ReplayGroup, filters []gonostr.Filter, timeout time.Duration, waitForAllRelays bool) (bool, int, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	subscription, err := bootstrapSubscribeAllWithEOSE(b.pool, ctx, filters)
	if err != nil {
		return false, 0, err
	}
	defer subscription.Close()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	applied := 0
	for {
		select {
		case <-ctx.Done():
			return false, applied, ctx.Err()
		case <-timer.C:
			return false, applied, fmt.Errorf("bootstrap group %q timed out waiting for EOSE", group.Name)
		case <-subscription.EndOfStoredEvents:
			return true, applied, nil
		case _, ok := <-subscription.RelayEOSE:
			if ok && !waitForAllRelays {
				return true, applied, nil
			}
		case closed, ok := <-subscription.Closed:
			if ok {
				return false, applied, fmt.Errorf("relay closed bootstrap group %q subscription %q: %s", group.Name, closed.SubscriptionID, closed.Reason)
			}
		case event, ok := <-subscription.Events:
			if !ok {
				continue
			}
			if event == nil {
				continue
			}
			if err := b.decodeAndApply(ctx, group, event); err != nil {
				var decodeErr *bootstrapEventDecodeError
				if errors.As(err, &decodeErr) {
					b.logger.Warn("bootstrap event skipped",
						zap.String("group", group.Name),
						zap.Int("kind", eventKindInt(event)),
						zap.String("event_id", eventIDHex(event)),
						zap.Error(decodeErr))
					continue
				}
				return false, applied, err
			}
			applied++
		}
	}
}

func (b *Bootstrapper) decodeAndApply(ctx context.Context, group ReplayGroup, event *gonostr.Event) error {
	if err := ValidateInboundEvent(event, time.Now().UTC(), InboundEventMaxFutureSkew); err != nil {
		return fmt.Errorf("validate bootstrap event %s kind %d: %w", eventIDHex(event), eventKindInt(event), err)
	}
	decoder, ok := b.catalog.Decoder(eventKindInt(event))
	if !ok {
		return &bootstrapEventDecodeError{err: fmt.Errorf("no decoder registered for kind %d", eventKindInt(event))}
	}
	decoded, err := decoder(event)
	if err != nil {
		return &bootstrapEventDecodeError{err: fmt.Errorf("decode bootstrap event %s kind %d: %w", eventIDHex(event), eventKindInt(event), err)}
	}
	if decoded == nil {
		return nil
	}
	decoded.Group = group.Name
	decoded.Tier = group.Tier
	if decoded.SourceID == "" {
		decoded.SourceID = eventIDHex(event)
	}
	if decoded.Timestamp.IsZero() {
		decoded.Timestamp = event.CreatedAt.Time().UTC()
	}
	if b.cache == nil {
		return nil
	}
	if err := b.cache.Apply(ctx, decoded); err != nil {
		return fmt.Errorf("apply bootstrap event %s kind %d: %w", eventIDHex(event), eventKindInt(event), err)
	}
	return nil
}

func (b *Bootstrapper) snapshotFilters(group ReplayGroup, startedAt time.Time) ([]gonostr.Filter, error) {
	until := gonostr.Timestamp(startedAt.Unix())
	filter, err := b.scopedFilter(group, gonostr.Filter{Kinds: filterKindsFromInts(group.Kinds), Until: until})
	if err != nil {
		return nil, err
	}
	return []gonostr.Filter{filter}, nil
}

func (b *Bootstrapper) liveFilters(ctx context.Context, group ReplayGroup, startedAt time.Time) ([]gonostr.Filter, error) {
	since := b.cursorSince(ctx, group.Kinds)
	if since == nil {
		fallback := gonostr.Timestamp(startedAt.Unix())
		since = &fallback
	}
	filter, err := b.scopedFilter(group, gonostr.Filter{Kinds: filterKindsFromInts(group.Kinds), Since: *since})
	if err != nil {
		return nil, err
	}
	return []gonostr.Filter{filter}, nil
}

func (b *Bootstrapper) cursorSince(ctx context.Context, kinds []int) *gonostr.Timestamp {
	if b == nil || b.cursorPlanner == nil {
		return nil
	}
	return b.cursorPlanner.ComputeSince(ctx, kinds)
}

func (b *Bootstrapper) scopedFilter(group ReplayGroup, filter gonostr.Filter) (gonostr.Filter, error) {
	var authors []string
	switch group.Name {
	case "system_snapshot", "worker_snapshot", "core_registry_snapshot":
		authors = b.config.ProjectionAuthors
	case "continuity_snapshot", "continuity_live", "core_control_plane_live":
		authors = b.config.ControlPlaneAuthors
	}
	if len(authors) > 0 {
		converted, err := filterAuthorsFromHex(authors)
		if err != nil {
			return gonostr.Filter{}, err
		}
		filter.Authors = converted
	}
	return filter, nil
}

func (b *Bootstrapper) computeReadyTier(completed map[string]bool) int {
	if b == nil || b.catalog == nil {
		return -1
	}
	for tier := b.config.RequestedTier; tier >= 0; tier-- {
		ready := true
		for _, group := range b.catalog.RequiredGroupsForTier(tier) {
			if !completed[group.Name] {
				ready = false
				break
			}
		}
		if ready {
			return tier
		}
	}
	return -1
}

func (b *Bootstrapper) setPhase(phase BootstrapPhase) {
	b.setProgress(func(progress *BootstrapProgress) {
		progress.Phase = phase
	})
}

func (b *Bootstrapper) incrementGroupsComplete() {
	b.setProgress(func(progress *BootstrapProgress) {
		progress.GroupsComplete++
	})
}

func (b *Bootstrapper) setProgress(update func(*BootstrapProgress)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	update(&b.progress)
}
