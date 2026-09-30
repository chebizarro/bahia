package nostr

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// defaultBootstrapPageLimit bounds every replay REQ explicitly. It sits at or
// below common relay query caps (strfry's default maxFilterLimit is 500, the
// Bahia relay sidecar's MaxQueryLimit is 2000) so a full page is detectable
// and the next page is requested with `until` instead of silently truncating.
const defaultBootstrapPageLimit = 500

type BootstrapProgress struct {
	Phase          BootstrapPhase
	RequestedTier  int
	ReadyTier      int
	GroupsTotal    int
	GroupsComplete int
	StartedAt      time.Time
	CurrentGroup   string
	BlockingRelays []string
	LastError      string
}

type BootstrapConfig struct {
	RequestedTier   int
	SnapshotTimeout time.Duration
	CatchupTimeout  time.Duration
	RetryInterval   time.Duration
	// PageLimit is the explicit `limit` on every replay REQ page. A page
	// that returns PageLimit events is followed by an `until`-bounded page.
	PageLimit           int
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
	if config.PageLimit <= 0 {
		config.PageLimit = defaultBootstrapPageLimit
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
		progress.CurrentGroup = ""
		progress.BlockingRelays = nil
		progress.LastError = ""
	})

	// A group is completed only when every page reached a terminal state on
	// every relay with at least one real EOSE (see runPage). Readiness is
	// "synced", not "non-empty": a brand-new fleet whose relays all answer
	// EOSE with no stored events is ready, while a group whose relays all
	// CLOSED or dropped is not completed no matter what they sent.
	completed := make(map[string]bool, len(groups))
	decodedEvents := 0

	b.setPhase(BootstrapPhaseSnapshot)
	for _, group := range groups {
		if !group.Snapshot {
			continue
		}
		filter, filterErr := b.snapshotFilter(group, startedAt)
		if filterErr != nil {
			return b.failAttempt(group.Name, filterErr)
		}
		ok, applied, err := b.runGroup(ctx, group, filter, b.config.SnapshotTimeout)
		decodedEvents += applied
		if err != nil {
			b.recordGroupFailure(group.Name, err)
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
		filter, filterErr := b.liveFilter(ctx, group, startedAt)
		if filterErr != nil {
			return b.failAttempt(group.Name, filterErr)
		}
		ok, applied, err := b.runGroup(ctx, group, filter, b.config.CatchupTimeout)
		decodedEvents += applied
		if err != nil {
			b.recordGroupFailure(group.Name, err)
			b.logger.Warn("bootstrap live catch-up group failed", zap.String("group", group.Name), zap.Error(err))
		}
		if ok {
			completed[group.Name] = true
			b.incrementGroupsComplete()
		}
	}

	readyTier := b.computeReadyTier(completed)
	if readyTier < 0 {
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
	b.logger.Info("bootstrap ready",
		zap.Int("ready_tier", readyTier),
		zap.Int("groups_synced", len(completed)),
		zap.Int("events_applied", decodedEvents))
	return nil
}

// failAttempt ends a bootstrap attempt on a configuration error that no relay
// response can fix, such as a replay group with no usable author scope.
func (b *Bootstrapper) failAttempt(group string, err error) error {
	err = fmt.Errorf("bootstrap group %q: %w", group, err)
	b.setProgress(func(progress *BootstrapProgress) {
		progress.Phase = BootstrapPhaseFailed
		progress.ReadyTier = -1
		progress.CurrentGroup = group
		progress.LastError = err.Error()
	})
	return err
}

func (b *Bootstrapper) Progress() BootstrapProgress {
	b.mu.RLock()
	defer b.mu.RUnlock()
	progress := b.progress
	progress.BlockingRelays = append([]string(nil), b.progress.BlockingRelays...)
	return progress
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

// runGroup replays one group to completion, paging backwards with `until`
// whenever a page comes back full. The returned count is events applied.
func (b *Bootstrapper) runGroup(ctx context.Context, group ReplayGroup, base gonostr.Filter, timeout time.Duration) (bool, int, error) {
	limit := b.config.PageLimit
	if limit <= 0 {
		limit = defaultBootstrapPageLimit
	}
	applied := make(map[string]struct{})
	until := base.Until
	for page := 1; ; page++ {
		filter := base
		filter.Limit = limit
		filter.Until = until
		result, err := b.runPage(ctx, group, filter, timeout, applied)
		if err != nil {
			return false, len(applied), err
		}
		if len(result.createdAt) < limit {
			b.clearGroupProgress()
			return true, len(applied), nil
		}
		// Each relay returns at most `limit` of its newest matching events,
		// so any relay that filled its page has its oldest returned event at
		// or before the limit-th newest event of the merged page. Paging from
		// there (inclusive) cannot skip an older event on any relay; events
		// already applied are deduplicated by ID.
		next := result.nthNewest(limit)
		if until != 0 && next >= until {
			return false, len(applied), fmt.Errorf("bootstrap group %q page %d: at least %d events share created_at %d; cannot page past them with until", group.Name, page, limit, next)
		}
		b.logger.Debug("bootstrap group page full; requesting older page",
			zap.String("group", group.Name),
			zap.Int("page", page),
			zap.Int("limit", limit),
			zap.Int64("until", int64(next)))
		until = next
	}
}

type bootstrapPageResult struct {
	// createdAt holds one entry per distinct event ID the page delivered,
	// including events that were later rejected, because every delivered
	// event counts against the relay's limit.
	createdAt []gonostr.Timestamp
}

// nthNewest returns the created_at of the n-th newest delivered event.
// Callers guarantee len(r.createdAt) >= n.
func (r bootstrapPageResult) nthNewest(n int) gonostr.Timestamp {
	sorted := append([]gonostr.Timestamp(nil), r.createdAt...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] > sorted[j] })
	return sorted[n-1]
}

// runPage runs one REQ across every relay and returns only after each relay
// the REQ reached has hit a terminal state: EOSE, CLOSED, or a dropped
// connection. A relay that closes or fails is terminal for itself only; it
// never completes the page on behalf of a relay that is still sending
// stored events. At least one relay must send a real EOSE.
func (b *Bootstrapper) runPage(ctx context.Context, group ReplayGroup, filter gonostr.Filter, timeout time.Duration, applied map[string]struct{}) (bootstrapPageResult, error) {
	var result bootstrapPageResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	subscription, err := bootstrapSubscribeAllWithEOSE(b.pool, ctx, []gonostr.Filter{filter})
	if err != nil {
		return result, err
	}
	defer subscription.Close()
	b.setProgress(func(progress *BootstrapProgress) {
		progress.CurrentGroup = group.Name
		progress.BlockingRelays = subscription.RelayURLs()
		progress.LastError = ""
	})

	allowedAuthors := make(map[gonostr.PubKey]struct{}, len(filter.Authors))
	for _, author := range filter.Authors {
		allowedAuthors[author] = struct{}{}
	}
	delivered := make(map[string]struct{})
	eoseRelays := make(map[string]struct{})

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	eventsCh := subscription.Events
	aggregateEOSECh := subscription.EndOfStoredEvents
	relayEOSECh := subscription.RelayEOSE
	closedCh := subscription.Closed

	handleEvent := func(event *gonostr.Event) error {
		if event == nil {
			return nil
		}
		id := eventIDHex(event)
		if _, dup := delivered[id]; dup {
			return nil
		}
		delivered[id] = struct{}{}
		result.createdAt = append(result.createdAt, event.CreatedAt)
		if _, done := applied[id]; done {
			return nil
		}
		if len(allowedAuthors) > 0 {
			if _, ok := allowedAuthors[event.PubKey]; !ok {
				b.logger.Warn("bootstrap event rejected: author outside replay group scope",
					zap.String("group", group.Name),
					zap.String("scope", string(group.Authors)),
					zap.Int("kind", eventKindInt(event)),
					zap.String("event_id", id),
					zap.String("pubkey", event.PubKey.Hex()))
				return nil
			}
		}
		if err := b.decodeAndApply(ctx, group, event); err != nil {
			var decodeErr *bootstrapEventDecodeError
			if errors.As(err, &decodeErr) {
				b.logger.Warn("bootstrap event skipped",
					zap.String("group", group.Name),
					zap.Int("kind", eventKindInt(event)),
					zap.String("event_id", id),
					zap.Error(decodeErr))
				return nil
			}
			return err
		}
		applied[id] = struct{}{}
		return nil
	}
	drainEvents := func() error {
		for eventsCh != nil {
			select {
			case event, ok := <-eventsCh:
				if !ok {
					eventsCh = nil
					return nil
				}
				if err := handleEvent(event); err != nil {
					return err
				}
			default:
				return nil
			}
		}
		return nil
	}
	for {
		if eventsCh == nil && aggregateEOSECh == nil && relayEOSECh == nil && closedCh == nil {
			return result, fmt.Errorf("bootstrap group %q ended before any relay EOSE", group.Name)
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-timer.C:
			blocking := subscription.PendingEOSE()
			if len(blocking) == 0 {
				blocking = subscription.RelayURLs()
			}
			b.setBlockingRelays(blocking)
			return result, fmt.Errorf("bootstrap group %q timed out waiting for EOSE from relays %v", group.Name, blocking)
		case <-aggregateEOSECh:
			// Every relay the REQ reached is now terminal.
			if !subscription.HasRealEOSE() {
				return result, fmt.Errorf("bootstrap group %q ended before any relay EOSE", group.Name)
			}
			if err := drainEvents(); err != nil {
				return result, err
			}
			for relayEOSECh != nil {
				select {
				case relayEOSE, ok := <-relayEOSECh:
					if !ok {
						relayEOSECh = nil
						continue
					}
					eoseRelays[relayEOSE.RelayURL] = struct{}{}
					continue
				default:
				}
				break
			}
			b.warnRelaysWithoutEOSE(group, subscription, eoseRelays)
			return result, nil
		case relayEOSE, ok := <-relayEOSECh:
			if !ok {
				relayEOSECh = nil
				continue
			}
			eoseRelays[relayEOSE.RelayURL] = struct{}{}
			b.removeBlockingRelay(relayEOSE.RelayURL)
		case closed, ok := <-closedCh:
			if !ok {
				closedCh = nil
				continue
			}
			b.removeBlockingRelay(closed.RelayURL)
			b.logger.Warn("relay closed bootstrap subscription; still waiting for the other relays",
				zap.String("group", group.Name),
				zap.String("relay", closed.RelayURL),
				zap.String("subscription_id", closed.SubscriptionID),
				zap.String("reason", closed.Reason))
		case event, ok := <-eventsCh:
			if !ok {
				eventsCh = nil
				continue
			}
			if err := handleEvent(event); err != nil {
				return result, err
			}
		}
	}
}

func (b *Bootstrapper) warnRelaysWithoutEOSE(group ReplayGroup, subscription *MergedSubscription, eoseRelays map[string]struct{}) {
	if subscription.AllRelaysReachedEOSE() {
		return
	}
	var missing []string
	for _, relayURL := range subscription.RelayURLs() {
		if _, ok := eoseRelays[relayURL]; !ok {
			missing = append(missing, relayURL)
		}
	}
	if len(missing) == 0 {
		return
	}
	b.logger.Warn("bootstrap group completed without EOSE from some relays; their stored history is not included",
		zap.String("group", group.Name),
		zap.Strings("relays", missing))
}

func (b *Bootstrapper) setBlockingRelays(relays []string) {
	b.setProgress(func(progress *BootstrapProgress) {
		progress.BlockingRelays = append([]string(nil), relays...)
	})
}

func (b *Bootstrapper) removeBlockingRelay(relayURL string) {
	b.setProgress(func(progress *BootstrapProgress) {
		filtered := progress.BlockingRelays[:0]
		for _, current := range progress.BlockingRelays {
			if current != relayURL {
				filtered = append(filtered, current)
			}
		}
		progress.BlockingRelays = filtered
	})
}

func (b *Bootstrapper) clearGroupProgress() {
	b.setProgress(func(progress *BootstrapProgress) {
		progress.CurrentGroup = ""
		progress.BlockingRelays = nil
		progress.LastError = ""
	})
}

func (b *Bootstrapper) recordGroupFailure(group string, err error) {
	b.setProgress(func(progress *BootstrapProgress) {
		progress.CurrentGroup = group
		progress.LastError = err.Error()
	})
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

func (b *Bootstrapper) snapshotFilter(group ReplayGroup, startedAt time.Time) (gonostr.Filter, error) {
	until := gonostr.Timestamp(startedAt.Unix())
	return b.scopedFilter(group, gonostr.Filter{Kinds: filterKindsFromInts(group.Kinds), Until: until})
}

func (b *Bootstrapper) liveFilter(ctx context.Context, group ReplayGroup, startedAt time.Time) (gonostr.Filter, error) {
	since := b.cursorSince(ctx, group.Kinds)
	if since == nil {
		fallback := gonostr.Timestamp(startedAt.Unix())
		since = &fallback
	}
	return b.scopedFilter(group, gonostr.Filter{Kinds: filterKindsFromInts(group.Kinds), Since: *since})
}

func (b *Bootstrapper) cursorSince(ctx context.Context, kinds []int) *gonostr.Timestamp {
	if b == nil || b.cursorPlanner == nil {
		return nil
	}
	return b.cursorPlanner.ComputeSince(ctx, kinds)
}

// scopedFilter applies the group's author scope. Scoped groups with no
// configured authors, and groups with an unknown scope, are hard errors:
// replaying them unscoped would let any pubkey's events count.
func (b *Bootstrapper) scopedFilter(group ReplayGroup, filter gonostr.Filter) (gonostr.Filter, error) {
	var authors []string
	switch group.Authors {
	case ReplayAuthorsAny:
		return filter, nil
	case ReplayAuthorsProjection:
		authors = b.config.ProjectionAuthors
	case ReplayAuthorsControlPlane:
		authors = b.config.ControlPlaneAuthors
	default:
		return gonostr.Filter{}, fmt.Errorf("replay group has unknown author scope %q", group.Authors)
	}
	if len(authors) == 0 {
		return gonostr.Filter{}, fmt.Errorf("replay group requires %s authors but none are configured", group.Authors)
	}
	converted, err := filterAuthorsFromHex(authors)
	if err != nil {
		return gonostr.Filter{}, err
	}
	filter.Authors = converted
	return filter, nil
}

// computeReadyTier returns the highest tier at or below the requested tier
// whose required groups all synced. A tier with no required groups is never
// ready: with nothing replayed there is no relay EOSE proving anything, and
// counting it would let an attempt in which every relay failed succeed.
func (b *Bootstrapper) computeReadyTier(completed map[string]bool) int {
	if b == nil || b.catalog == nil {
		return -1
	}
	for tier := b.config.RequestedTier; tier >= 0; tier-- {
		required := b.catalog.RequiredGroupsForTier(tier)
		if len(required) == 0 {
			continue
		}
		ready := true
		for _, group := range required {
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
