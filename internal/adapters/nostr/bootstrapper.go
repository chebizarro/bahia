package nostr

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
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
	GroupsTotal    int
	GroupsComplete int
	StartedAt      time.Time
	CurrentGroup   string
	BlockingRelays []string
	LastError      string
}

type BootstrapConfig struct {
	SnapshotTimeout time.Duration
	CatchupTimeout  time.Duration
	RetryInterval   time.Duration
	// PageLimit is the explicit `limit` on every replay REQ page. A page
	// that returns PageLimit events is followed by an `until`-bounded page.
	PageLimit           int
	ProjectionAuthors   []string
	ControlPlaneAuthors []string
	// SelfAuthors are the daemon's own pubkeys: their events never advance
	// a live group's resume cursor (B-15).
	SelfAuthors []string
	// Resume sets the overlap and fresh-cursor lookback of live groups.
	Resume InboundSyncConfig
}

type BootstrapCacheApplier interface {
	Apply(ctx context.Context, event *DecodedProjectionEvent) error
}

type BootstrapStatusPublisher interface {
	PublishCheckpoint(ctx context.Context, payload interface{}) error
	PublishReadiness(ctx context.Context, payload interface{}) error
}

type Bootstrapper struct {
	pool    *RelayPool
	catalog *KindCatalog
	// cursors holds the per-(relay, filter) resume cursors of live groups; nil
	// replays every live group from its lookback window.
	cursors  *localstore.Store
	self     map[gonostr.PubKey]struct{}
	cache    BootstrapCacheApplier
	logger   *zap.Logger
	mu       sync.RWMutex
	progress BootstrapProgress
	config   BootstrapConfig
	readyCh  chan struct{}
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

// bootstrapPageTimer starts the EOSE deadline of one replay page and returns
// the channel that fires when it elapses plus a stop function. Tests replace
// it with a timer they control, so scripted relays never race the wall clock.
var bootstrapPageTimer = func(timeout time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(timeout)
	return timer.C, func() { timer.Stop() }
}

// NewBootstrapper creates a bootstrapper. cursors (optional) is the local event
// store keeping live groups' per-relay resume cursors.
func NewBootstrapper(pool *RelayPool, catalog *KindCatalog, cursors *localstore.Store, cache BootstrapCacheApplier, logger *zap.Logger, config BootstrapConfig) *Bootstrapper {
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
	config.Resume = config.Resume.normalized()
	self := make(map[gonostr.PubKey]struct{}, len(config.SelfAuthors))
	if converted, err := filterAuthorsFromHex(config.SelfAuthors); err == nil {
		for _, pubkey := range converted {
			self[pubkey] = struct{}{}
		}
	}

	return &Bootstrapper{
		pool:    pool,
		catalog: catalog,
		cursors: cursors,
		self:    self,
		cache:   cache,
		logger:  logger.Named("bootstrapper"),
		config:  config,
		progress: BootstrapProgress{
			Phase: BootstrapPhaseInit,
		},
		readyCh: make(chan struct{}),
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
	groups := b.requiredGroups()
	if len(groups) == 0 {
		b.setProgress(func(progress *BootstrapProgress) {
			progress.Phase = BootstrapPhaseFailed
			progress.LastError = "catalog has no required groups: cannot verify relay sync before any relay EOSE"
		})
		return fmt.Errorf("bootstrap failed: catalog has no required groups")
	}
	b.setProgress(func(progress *BootstrapProgress) {
		progress.Phase = BootstrapPhaseInit
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
		synced, err := b.runGroup(ctx, group, filter, b.config.SnapshotTimeout)
		decodedEvents += synced.applied
		if err != nil {
			b.recordGroupFailure(group.Name, err)
			b.logger.Warn("bootstrap snapshot group failed", zap.String("group", group.Name), zap.Error(err))
		}
		if synced.complete {
			completed[group.Name] = true
			b.incrementGroupsComplete()
		}
	}

	b.setPhase(BootstrapPhaseLiveCatchup)
	for _, group := range groups {
		if group.Snapshot {
			continue
		}
		filters, filterErr := b.liveFilters(group, startedAt)
		if filterErr != nil {
			return b.failAttempt(group.Name, filterErr)
		}
		groupComplete := true
		for _, live := range filters {
			synced, err := b.runGroup(ctx, group, live.req, b.config.CatchupTimeout)
			decodedEvents += synced.applied
			if err != nil {
				b.recordGroupFailure(group.Name, err)
				b.logger.Warn("bootstrap live catch-up group failed", zap.String("group", group.Name), zap.Error(err))
			}
			if !synced.complete {
				groupComplete = false
				continue
			}
			b.commitLiveCursors(live, synced)
		}
		if groupComplete {
			completed[group.Name] = true
			b.incrementGroupsComplete()
		}
	}

	// Verify all required groups completed.
	allRequired := true
	for _, group := range groups {
		if !completed[group.Name] {
			allRequired = false
			break
		}
	}
	if !allRequired {
		b.setProgress(func(progress *BootstrapProgress) {
			progress.Phase = BootstrapPhaseFailed
		})
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("bootstrap failed: not all required groups synced (%d/%d)", len(completed), len(groups))
	}

	b.setProgress(func(progress *BootstrapProgress) {
		progress.Phase = BootstrapPhaseReady
	})
	b.markReady()
	b.logger.Info("bootstrap ready",
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

// ReadySignal closes after the first complete relay catch-up. Consumers use it
// to gate actions on authoritative local state without polling.
func (b *Bootstrapper) ReadySignal() <-chan struct{} {
	return b.readyCh
}

func (b *Bootstrapper) markReady() {
	select {
	case <-b.readyCh:
	default:
		close(b.readyCh)
	}
}

func (b *Bootstrapper) requiredGroups() []ReplayGroup {
	if b == nil || b.catalog == nil {
		return nil
	}
	return b.catalog.RequiredGroups()
}

// groupSync is the outcome of replaying one filter of a group.
type groupSync struct {
	// complete: every page reached a terminal state with at least one EOSE.
	complete bool
	// applied counts the events applied to the cache.
	applied int
	// eoseRelays are the relays that sent EOSE for every page.
	eoseRelays map[string]struct{}
	// newest is the newest created_at, clamped to the local clock, among the
	// delivered events not authored by the daemon itself.
	newest gonostr.Timestamp
}

// runGroup replays one group to completion, paging backwards with `until`
// whenever a page comes back full.
func (b *Bootstrapper) runGroup(ctx context.Context, group ReplayGroup, base gonostr.Filter, timeout time.Duration) (groupSync, error) {
	limit := b.config.PageLimit
	if limit <= 0 {
		limit = defaultBootstrapPageLimit
	}
	applied := make(map[string]struct{})
	var synced groupSync
	until := base.Until
	for page := 1; ; page++ {
		filter := base
		filter.Limit = limit
		filter.Until = until
		result, err := b.runPage(ctx, group, filter, timeout, applied)
		synced.applied = len(applied)
		if err != nil {
			return synced, err
		}
		if page == 1 {
			synced.eoseRelays = result.eoseRelays
		} else {
			for relayURL := range synced.eoseRelays {
				if _, ok := result.eoseRelays[relayURL]; !ok {
					delete(synced.eoseRelays, relayURL)
				}
			}
		}
		synced.newest = max(synced.newest, result.newest)
		if len(result.createdAt) < limit {
			b.clearGroupProgress()
			synced.complete = true
			return synced, nil
		}
		// Each relay returns at most `limit` of its newest matching events,
		// so any relay that filled its page has its oldest returned event at
		// or before the limit-th newest event of the merged page. Paging from
		// there (inclusive) cannot skip an older event on any relay; events
		// already applied are deduplicated by ID.
		next := result.nthNewest(limit)
		if until != 0 && next >= until {
			return synced, fmt.Errorf("bootstrap group %q page %d: at least %d events share created_at %d; cannot page past them with until", group.Name, page, limit, next)
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
	// eoseRelays are the relays that sent a real EOSE for the page.
	eoseRelays map[string]struct{}
	// newest is the newest clamped created_at among delivered events that
	// the daemon did not author.
	newest gonostr.Timestamp
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
	result := bootstrapPageResult{eoseRelays: make(map[string]struct{})}
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
	eoseRelays := result.eoseRelays

	timedOut, stopTimer := bootstrapPageTimer(timeout)
	defer stopTimer()
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
		if _, own := b.self[event.PubKey]; !own {
			result.newest = max(result.newest, clampToClock(event.CreatedAt, time.Now()))
		}
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
		case <-timedOut:
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

// liveReplayFilter is one REQ filter of a live group together with the
// cursor it resumes.
type liveReplayFilter struct {
	inboundFilter
	req gonostr.Filter
}

// liveFilters returns a live group's REQ filters. Replaceable and addressable
// kinds are replayed in full: they are the state, and the relay keeps only the
// latest version per coordinate (C-3). Regular kinds resume from the oldest
// per-relay cursor less the overlap, or from the lookback window when a relay
// has no cursor yet; never from "now".
func (b *Bootstrapper) liveFilters(group ReplayGroup, startedAt time.Time) ([]liveReplayFilter, error) {
	scoped, err := b.scopedFilter(group, gonostr.Filter{Kinds: filterKindsFromInts(group.Kinds)})
	if err != nil {
		return nil, err
	}
	var out []liveReplayFilter
	for _, filter := range splitInboundFilter(scoped) {
		req := filter.filter
		if !filter.persistent {
			req.Since = b.resumeSince(filter.hash, startedAt)
		}
		out = append(out, liveReplayFilter{inboundFilter: filter, req: req})
	}
	return out, nil
}

// resumeSince is the oldest resume point any relay in the pool needs for the
// filter: one REQ goes to every relay, so it must cover the relay that is
// furthest behind.
func (b *Bootstrapper) resumeSince(hash string, startedAt time.Time) gonostr.Timestamp {
	var relays []string
	if b.pool != nil {
		relays = b.pool.URLs()
	}
	if b.cursors == nil || len(relays) == 0 {
		return b.config.Resume.resumeSince(0, startedAt)
	}
	since := gonostr.Timestamp(-1)
	for _, relayURL := range relays {
		cursor, err := b.cursors.Cursor(relayURL, hash)
		if err != nil {
			b.logger.Warn("read live group cursor failed", zap.String("relay", relayURL), zap.Error(err))
			cursor = 0
		}
		relaySince := b.config.Resume.resumeSince(cursor, startedAt)
		if since < 0 || relaySince < since {
			since = relaySince
		}
	}
	return since
}

// commitLiveCursors advances the cursor of every relay that sent EOSE for
// every page of a regular-kind live filter. A relay that stayed down or
// CLOSED keeps its old cursor, so the next attempt catches it up.
func (b *Bootstrapper) commitLiveCursors(live liveReplayFilter, synced groupSync) {
	if b.cursors == nil || live.persistent {
		return
	}
	to := clampToClock(max(synced.newest, live.req.Since), time.Now())
	for relayURL := range synced.eoseRelays {
		if err := b.cursors.AdvanceCursor(relayURL, live.hash, to); err != nil {
			b.logger.Warn("persist live group cursor failed", zap.String("relay", relayURL), zap.Error(err))
		}
	}
}

// ApplyDeletion applies a NIP-09 deletion request received after bootstrap
// (the subscriber's live and reconnect sync) to the projection cache, exactly
// as the deletion replay group does during bootstrap. Requests of another
// kind, or from authors outside that group's trusted scope, are ignored. It is
// idempotent, so it can observe redeliveries.
func (b *Bootstrapper) ApplyDeletion(ctx context.Context, ev *gonostr.Event) {
	if b == nil || b.cache == nil || ev == nil || ev.Kind != gonostr.KindDeletion {
		return
	}
	for _, group := range b.catalog.Groups {
		if !slices.Contains(group.Kinds, int(gonostr.KindDeletion)) {
			continue
		}
		scope, err := b.scopedFilter(group, gonostr.Filter{})
		if err != nil || (len(scope.Authors) > 0 && !slices.Contains(scope.Authors, ev.PubKey)) {
			b.logger.Debug("deletion request outside the trusted scope ignored",
				zap.String("event_id", eventIDHex(ev)), zap.String("pubkey", ev.PubKey.Hex()))
			return
		}
		if err := b.decodeAndApply(ctx, group, ev); err != nil {
			b.logger.Warn("apply live deletion request failed", zap.String("event_id", eventIDHex(ev)), zap.Error(err))
		}
		return
	}
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
