// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip11"
	"go.uber.org/zap"
)

// RelayPool manages persistent connections to a set of Nostr relays.
// It provides automatic reconnection and shared access across publishers and clients.
//
// Lock order: p.mu (topology) is only held to read or change the relay maps,
// never across network I/O; publishes and subscriptions snapshot the relays
// they need and release it. A managedRelay's mu guards its connection state and
// is not held while dialing (see ensureRelayConnected).
type RelayPool struct {
	mu sync.RWMutex
	// subscriptionsMu is deliberately separate from mu so subscription
	// bookkeeping never waits on topology changes.
	subscriptionsMu     sync.Mutex
	reconfigureMu       sync.Mutex
	relays              map[string]*managedRelay
	retiredRelays       map[string]*managedRelay
	activeSubscriptions map[uint64]*activeMergedSubscription
	nextSubscriptionID  uint64
	relayInfoCache      map[string]*nip11.RelayInformationDocument // NIP-11 info cache
	health              *RelayHealthTracker
	urls                []string
	logger              *zap.Logger
	ctx                 context.Context
	cancel              context.CancelFunc
	privateKey          string // hex-encoded private key for NIP-42 AUTH (optional)
	authSigner          nostr.Signer
	authSignFunc        func(context.Context, *nostr.Event) error
	connectRelay        func(context.Context, string, nostr.RelayOptions) (*nostr.Relay, error)
	// now and newReconnectBackoff pace reconnects to failing relays; both are
	// replaceable in tests.
	now                 func() time.Time
	newReconnectBackoff func() *Backoff
	// connectTimeout bounds an explicit connect (Connect, reconfigure,
	// subscription setup); reconnectTimeout bounds the on-demand reconnect a
	// publish or single-relay subscribe performs.
	connectTimeout   time.Duration
	reconnectTimeout time.Duration
	// newResubscribeBackoff paces the reissue of one relay's REQ after a drop
	// or a retryable CLOSED; replaceable in tests.
	newResubscribeBackoff func() *Backoff
	// fetchRelayLimits reads a relay's NIP-11 document after each connect,
	// bounded by relayInfoTimeout; replaceable in tests.
	fetchRelayLimits func(context.Context, string) (relayLimits, *nip11.RelayInformationDocument, error)
	relayInfoTimeout time.Duration

	// connectedMu guards relay (re)connection listeners. It is never held
	// while calling out, and notification never blocks the pool.
	connectedMu        sync.Mutex
	connectedListeners map[uint64]chan struct{}
	nextListenerID     uint64
}

type managedRelay struct {
	url       string
	relay     *nostr.Relay
	connected bool
	lastErr   error
	mu        sync.Mutex

	// dialing is non-nil while a connect is in flight and is closed when it
	// finishes; concurrent callers wait for that outcome instead of dialing
	// again. Guarded by mu.
	dialing chan struct{}
	// reconnectBackoff and retryAt pace on-demand reconnects to a relay that
	// keeps failing: until retryAt, publishes fail fast instead of each waiting
	// out another connect timeout. A successful connect resets both. Guarded
	// by mu.
	reconnectBackoff *Backoff
	retryAt          time.Time
	// failedAt is when the most recent dial failed. Guarded by mu.
	failedAt time.Time
	// closed is set once the pool has closed this relay for good (retired and
	// pruned, or pool Close); it is never reconnected afterwards. Guarded by mu.
	closed bool

	// limits are the NIP-11 limitations of the current connection. After a
	// connect they are fetched once; limitsReady is closed when that fetch
	// ends (nil when none is running). Guarded by mu.
	limits      relayLimits
	limitsReady chan struct{}
	// openREQs counts the pool's live subscription REQs on this relay, capped
	// at limits.MaxSubscriptions; slotFreed is closed (and replaced) whenever
	// one ends. Guarded by mu.
	openREQs  int
	slotFreed chan struct{}
}

// relayLimits are the NIP-11 limitations the pool enforces. Zero means the
// relay did not state one.
type relayLimits struct {
	MaxLimit         int
	MaxSubscriptions int
	MaxFilters       int
}

// RelayReconnectBackoffError is returned for a relay whose recent connect
// attempts failed while its reconnect backoff is running. It is retryable: the
// caller should try again after RetryAt. No dial was made for this call;
// FailedAt identifies the dial failure the caller is being told about, so a
// caller can tell a fresh failure from one it has already seen.
type RelayReconnectBackoffError struct {
	RelayURL string
	RetryAt  time.Time
	FailedAt time.Time
	LastErr  error
}

func (e *RelayReconnectBackoffError) Error() string {
	msg := fmt.Sprintf("relay %s reconnect backing off until %s", e.RelayURL, e.RetryAt.UTC().Format(time.RFC3339))
	if e.LastErr != nil {
		msg += ": last connect error: " + e.LastErr.Error()
	}
	return msg
}

func (e *RelayReconnectBackoffError) Unwrap() error { return e.LastErr }

const (
	defaultRelayConnectTimeout   = 10 * time.Second
	defaultRelayReconnectTimeout = 5 * time.Second
	defaultRelayInfoTimeout      = 3 * time.Second
)

// defaultResubscribeBackoff paces the reissue of one relay's REQ: 1s doubling
// to a 1 minute cap, with jitter.
func defaultResubscribeBackoff() *Backoff {
	return &Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2, Jitter: 0.2}
}

// defaultReconnectBackoff paces reconnects to a failing relay: 1s doubling to
// a 1 minute cap, with jitter.
func defaultReconnectBackoff() *Backoff {
	return &Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2, Jitter: 0.2}
}

// RelayPoolOption configures a RelayPool.
type RelayPoolOption func(*RelayPool)

// WithPrivateKey sets the private key for NIP-42 AUTH.
// The key should be hex-encoded. When set, AuthenticateRelay() can be called
// to respond to auth-required errors detected via PublishResult.IsAuthRequired().
func WithPrivateKey(privateKeyHex string) RelayPoolOption {
	return func(p *RelayPool) { p.privateKey = privateKeyHex }
}

// WithAuthSigner sets a signer for NIP-42 AUTH without requiring local
// identity key material. It is intended for remote signers such as NIP-46.
func WithAuthSigner(signer nostr.Signer) RelayPoolOption {
	return func(p *RelayPool) { p.authSigner = signer }
}

// WithResubscribeBackoff sets how the pool paces the reissue of one relay's
// REQ after a dropped connection or a retryable CLOSED (default: 1s doubling
// to 1 minute, with jitter).
func WithResubscribeBackoff(newBackoff func() *Backoff) RelayPoolOption {
	return func(p *RelayPool) {
		if newBackoff != nil {
			p.newResubscribeBackoff = newBackoff
		}
	}
}

// WithAuthSignFunc sets the function that signs NIP-42 AUTH events, for
// callers whose signer is not a nostr.Signer.
func WithAuthSignFunc(sign func(context.Context, *nostr.Event) error) RelayPoolOption {
	return func(p *RelayPool) { p.authSignFunc = sign }
}

// RelayPoolReconfigureResult describes an in-place relay topology update.
type RelayPoolReconfigureResult struct {
	Changed               bool
	PreviousURLs          []string
	CurrentURLs           []string
	AddedURLs             []string
	RemovedURLs           []string
	ActiveSubscriptions   int
	MigratedSubscriptions int
	MigrationErrors       []string
}

// NewRelayPool creates a relay pool for the given URLs.
// Call Connect() to establish connections and Close() when done.
func NewRelayPool(urls []string, logger *zap.Logger, opts ...RelayPoolOption) *RelayPool {
	normalizedURLs := normalizeRelayURLs(urls)
	ctx, cancel := context.WithCancel(context.Background())
	p := &RelayPool{
		relays:                make(map[string]*managedRelay),
		retiredRelays:         make(map[string]*managedRelay),
		activeSubscriptions:   make(map[uint64]*activeMergedSubscription),
		relayInfoCache:        make(map[string]*nip11.RelayInformationDocument),
		health:                NewRelayHealthTracker(),
		urls:                  normalizedURLs,
		logger:                logger,
		ctx:                   ctx,
		cancel:                cancel,
		connectRelay:          nostr.RelayConnect,
		now:                   time.Now,
		newReconnectBackoff:   defaultReconnectBackoff,
		connectTimeout:        defaultRelayConnectTimeout,
		reconnectTimeout:      defaultRelayReconnectTimeout,
		newResubscribeBackoff: defaultResubscribeBackoff,
		fetchRelayLimits:      fetchRelayLimits,
		relayInfoTimeout:      defaultRelayInfoTimeout,
	}
	for _, url := range normalizedURLs {
		p.health.GetOrCreate(url)
		// Relays are held from the start and dialed on first use (or by
		// Connect), so a publish or subscription before Connect still
		// reaches them.
		p.relays[url] = &managedRelay{url: url}
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ReconfigureRelayURLs replaces the configured relay topology and migrates active
// logical subscriptions. Added relays are connected and subscribed before removed relay
// subscriptions are retired, so callers keep one continuous event stream during handoff.
func (p *RelayPool) ReconfigureRelayURLs(urls []string) RelayPoolReconfigureResult {
	return p.ReconfigureRelayURLsContext(p.ctx, urls)
}

// ReconfigureRelayURLsContext is ReconfigureRelayURLs with a caller-controlled context
// for the connect and subscription work performed during the topology transition.
func (p *RelayPool) ReconfigureRelayURLsContext(ctx context.Context, urls []string) RelayPoolReconfigureResult {
	if ctx == nil {
		ctx = p.ctx
	}
	p.reconfigureMu.Lock()
	defer p.reconfigureMu.Unlock()

	nextURLs := normalizeRelayURLs(urls)
	p.mu.Lock()
	previousURLs := cloneRelayURLs(p.urls)
	if sameRelayURLOrder(previousURLs, nextURLs) {
		result := RelayPoolReconfigureResult{
			Changed:      false,
			PreviousURLs: previousURLs,
			CurrentURLs:  cloneRelayURLs(p.urls),
		}
		p.mu.Unlock()
		return result
	}

	previousSet := relayURLSet(previousURLs)
	nextSet := relayURLSet(nextURLs)
	removedURLs := make([]string, 0)
	for _, url := range previousURLs {
		if _, keep := nextSet[url]; !keep {
			removedURLs = append(removedURLs, url)
		}
	}
	addedURLs := make([]string, 0)
	for _, url := range nextURLs {
		if _, alreadyConfigured := previousSet[url]; !alreadyConfigured {
			addedURLs = append(addedURLs, url)
		}
	}

	for _, url := range removedURLs {
		if mr, exists := p.relays[url]; exists {
			p.retiredRelays[url] = mr
			delete(p.relays, url)
		}
	}
	for _, url := range nextURLs {
		p.health.GetOrCreate(url)
		if _, exists := p.relays[url]; exists {
			continue
		}
		if retired, exists := p.retiredRelays[url]; exists {
			p.relays[url] = retired
			delete(p.retiredRelays, url)
			continue
		}
		p.relays[url] = &managedRelay{url: url}
	}
	p.urls = nextURLs

	p.subscriptionsMu.Lock()
	active := make([]*activeMergedSubscription, 0, len(p.activeSubscriptions))
	for _, subscription := range p.activeSubscriptions {
		active = append(active, subscription)
	}
	p.subscriptionsMu.Unlock()
	addedRelays := make(map[string]*managedRelay, len(addedURLs))
	for _, url := range addedURLs {
		addedRelays[url] = p.relays[url]
	}
	p.mu.Unlock()

	result := RelayPoolReconfigureResult{
		Changed:             true,
		PreviousURLs:        previousURLs,
		CurrentURLs:         cloneRelayURLs(nextURLs),
		AddedURLs:           addedURLs,
		RemovedURLs:         removedURLs,
		ActiveSubscriptions: len(active),
	}

	ready := make(map[string]bool, len(addedRelays))
	for _, url := range addedURLs {
		mr := addedRelays[url]
		p.connectOne(ctx, mr)
		ready[url] = managedRelayConnected(mr)
		if !ready[url] {
			result.MigrationErrors = append(result.MigrationErrors, fmt.Sprintf("connect %s", url))
		}
	}

	for _, subscription := range active {
		migrated := true
		for _, url := range addedURLs {
			if !ready[url] {
				migrated = false
				continue
			}
			if err := subscription.addRelay(ctx, addedRelays[url]); err != nil {
				migrated = false
				result.MigrationErrors = append(result.MigrationErrors, fmt.Sprintf("subscribe %s: %v", url, err))
			}
		}
		if !migrated {
			continue
		}
		for _, url := range removedURLs {
			subscription.removeRelay(url)
		}
		result.MigratedSubscriptions++
	}

	p.pruneRetiredRelays()
	return result
}

func managedRelayConnected(mr *managedRelay) bool {
	if mr == nil {
		return false
	}
	mr.mu.Lock()
	defer mr.mu.Unlock()
	return mr.connected && mr.relay != nil
}

func normalizeRelayURLs(urls []string) []string {
	if len(urls) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(urls))
	seen := make(map[string]struct{}, len(urls))
	for _, url := range urls {
		normalizedURL := nostr.NormalizeURL(url)
		if normalizedURL == "" {
			continue
		}
		if _, exists := seen[normalizedURL]; exists {
			continue
		}
		seen[normalizedURL] = struct{}{}
		normalized = append(normalized, normalizedURL)
	}
	return normalized
}

func cloneRelayURLs(urls []string) []string {
	if len(urls) == 0 {
		return nil
	}
	return append([]string(nil), urls...)
}

func relayURLSet(urls []string) map[string]struct{} {
	set := make(map[string]struct{}, len(urls))
	for _, url := range urls {
		set[url] = struct{}{}
	}
	return set
}

func sameRelayURLOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Connect establishes connections to all configured relays concurrently.
// Failed connections are logged but not fatal; they will be retried on use.
func (p *RelayPool) Connect(ctx context.Context) {
	p.mu.Lock()
	relays := make([]*managedRelay, 0, len(p.urls))
	for _, url := range p.urls {
		mr, exists := p.relays[url]
		if !exists {
			mr = &managedRelay{url: url}
			p.relays[url] = mr
		}
		relays = append(relays, mr)
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	for _, mr := range relays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.connectOne(ctx, mr)
		}()
	}
	wg.Wait()
}

// connectOne is an explicit connect: it ignores the reconnect backoff (but
// still shares an in-flight dial and records its outcome in the backoff).
func (p *RelayPool) connectOne(ctx context.Context, mr *managedRelay) {
	if _, err := p.ensureRelayConnected(ctx, mr, p.connectTimeout, false); err != nil {
		p.logger.Warn("failed to connect to relay", zap.String("relay", mr.url), zap.Error(err))
		return
	}
	p.logger.Debug("connected to relay", zap.String("relay", mr.url))
}

// reconnectRelay returns mr's live connection, reconnecting on demand. While
// the relay's reconnect backoff is running it fails fast with a
// *RelayReconnectBackoffError instead of dialing, so a relay that keeps failing
// costs at most one connect timeout per backoff window rather than one per
// publish.
func (p *RelayPool) reconnectRelay(ctx context.Context, mr *managedRelay) (*nostr.Relay, error) {
	return p.ensureRelayConnected(ctx, mr, p.reconnectTimeout, true)
}

// ensureRelayConnected returns mr's connection, dialing if needed. At most one
// dial per relay is in flight: concurrent callers wait for its outcome (bounded
// by their own ctx) instead of dialing again. mr.mu is not held while dialing,
// so a slow or unreachable relay never blocks callers that only need its
// state. reconnect selects the on-demand path: it honours the reconnect
// backoff and counts the dial as a reconnect.
func (p *RelayPool) ensureRelayConnected(ctx context.Context, mr *managedRelay, timeout time.Duration, reconnect bool) (*nostr.Relay, error) {
	for {
		mr.mu.Lock()
		if mr.connected && mr.relay != nil {
			relay := mr.relay
			if relay.Context().Err() == nil {
				mr.mu.Unlock()
				return relay, nil
			}
			// The websocket died under us; redial below.
			mr.connected = false
			mr.lastErr = context.Cause(relay.Context())
			mr.mu.Unlock()
			p.recordRelayConnectionState(mr.url, false)
			continue
		}
		if mr.closed {
			mr.mu.Unlock()
			return nil, fmt.Errorf("relay %s was removed from the pool", mr.url)
		}
		if wait := mr.dialing; wait != nil {
			mr.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if reconnect && p.now().Before(mr.retryAt) {
			err := &RelayReconnectBackoffError{RelayURL: mr.url, RetryAt: mr.retryAt, FailedAt: mr.failedAt, LastErr: mr.lastErr}
			mr.mu.Unlock()
			return nil, err
		}
		done := make(chan struct{})
		mr.dialing = done
		mr.mu.Unlock()

		if reconnect {
			p.recordRelayReconnect(mr.url)
		}
		connectCtx, cancel := context.WithTimeout(ctx, timeout)
		relay, err := p.connectRelay(connectCtx, mr.url, p.buildRelayOptions(mr.url))
		cancel()
		return p.finishDial(ctx, mr, done, relay, err)
	}
}

// finishDial records the outcome of the dial that owns done and wakes waiters.
func (p *RelayPool) finishDial(ctx context.Context, mr *managedRelay, done chan struct{}, relay *nostr.Relay, err error) (*nostr.Relay, error) {
	mr.mu.Lock()
	mr.dialing = nil
	close(done)
	if err == nil && mr.closed {
		mr.mu.Unlock()
		_ = relay.Close()
		return nil, fmt.Errorf("relay %s was removed from the pool", mr.url)
	}
	if err != nil {
		mr.connected = false
		mr.lastErr = err
		// A dial cut short by the caller's own cancellation says nothing
		// about the relay, so it does not extend the backoff.
		if ctx.Err() == nil {
			if mr.reconnectBackoff == nil {
				mr.reconnectBackoff = p.newReconnectBackoff()
			}
			mr.failedAt = p.now()
			mr.retryAt = mr.failedAt.Add(mr.reconnectBackoff.Next())
		}
		mr.mu.Unlock()
		p.recordRelayConnectionState(mr.url, false)
		p.recordRelayError(mr.url, err.Error())
		return nil, err
	}
	mr.relay = relay
	mr.connected = true
	mr.lastErr = nil
	mr.retryAt = time.Time{}
	if mr.reconnectBackoff != nil {
		mr.reconnectBackoff.Reset()
	}
	mr.limits = relayLimits{}
	mr.limitsReady = nil
	if relay.IsConnected() {
		ready := make(chan struct{})
		mr.limitsReady = ready
		go p.loadRelayLimits(mr, ready)
	}
	mr.mu.Unlock()
	p.recordRelayConnectionState(mr.url, true)
	return relay, nil
}

// Publish publishes an event to all connected relays.
// Returns the number of successful publications and any errors.
func (p *RelayPool) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	results, err := p.PublishWithResults(ctx, ev)
	return countSuccessfulPublishResults(results), err
}

// PublishWithResults publishes an event to all connected relays and returns
// one result for each attempted relay publication. Protocol OK=false rejections
// preserve the relay-provided reason with Error unset; transport and connection
// failures preserve Error with Reason unset. A duplicate rejection is treated as
// aggregate success because the relay already has the event.
//
// The aggregate error only reports "no relay accepted"; callers that need a
// delivery guarantee must inspect the per-relay results (see Publisher, which
// tracks acceptance per relay and retries the relays that have not accepted).
func (p *RelayPool) PublishWithResults(ctx context.Context, ev nostr.Event) ([]PublishResult, error) {
	return p.PublishToRelaysWithResults(ctx, ev, nil)
}

// PublishToRelaysWithResults publishes ev to the configured relays named in
// relayURLs (every configured relay when relayURLs is nil). Relays are contacted
// concurrently so one slow or half-open relay does not delay the others; results
// are returned in configured relay order. URLs that are not configured in the
// pool are ignored and produce no result. The topology lock is only held to
// snapshot the target relays, never during the sends, so a slow relay cannot
// hold up reconfiguration; a relay retired mid-send fails that send.
func (p *RelayPool) PublishToRelaysWithResults(ctx context.Context, ev nostr.Event, relayURLs []string) ([]PublishResult, error) {
	p.mu.RLock()
	relays := p.orderedRelaysLocked()
	p.mu.RUnlock()
	if relayURLs != nil {
		wanted := relayURLSet(normalizeRelayURLs(relayURLs))
		selected := relays[:0]
		for _, mr := range relays {
			if _, ok := wanted[mr.url]; ok {
				selected = append(selected, mr)
			}
		}
		relays = selected
	}

	results := make([]PublishResult, len(relays))
	var wg sync.WaitGroup
	for i, mr := range relays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = p.publishToRelayWithResult(ctx, mr, ev)
		}()
	}
	wg.Wait()

	return results, aggregatePublishResultsError(results)
}

// PublishResult contains the outcome of a publish attempt.
type PublishResult struct {
	RelayURL string
	Accepted bool
	Reason   string // rejection reason if not accepted
	Error    error  // transport/connection error (nil if relay responded)
}

// IsAuthRequiredReason returns true if a relay protocol reason requires authentication.
func IsAuthRequiredReason(reason string) bool {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	return normalized == "auth-required" || strings.HasPrefix(normalized, "auth-required:")
}

func authUnavailableMetadata(relayReason string, authErr error) string {
	reason := strings.TrimSpace(relayReason)
	if reason == "" {
		reason = "auth-required"
	}
	if authErr == nil {
		return "auth-unavailable: " + reason
	}
	return fmt.Sprintf("auth-unavailable: %s: %s", reason, authErr.Error())
}

var subscribeOnRelay = func(relay *nostr.Relay, ctx context.Context, filter nostr.Filter) (*nostr.Subscription, error) {
	return relay.Subscribe(ctx, filter, nostr.SubscriptionOptions{MaxWaitForEOSE: time.Duration(math.MaxInt64)})
}

var publishOnRelay = func(relay *nostr.Relay, ctx context.Context, event nostr.Event) error {
	return relay.Publish(ctx, event)
}

// IsRateLimitedReason returns true if a relay protocol reason indicates rate limiting.
func IsRateLimitedReason(reason string) bool {
	return strings.HasPrefix(reason, "rate-limited:")
}

// IsBlockedReason returns true if a relay protocol reason indicates a policy block.
func IsBlockedReason(reason string) bool {
	return strings.HasPrefix(reason, "blocked:")
}

// IsDuplicateReason returns true if a relay already has the event.
func IsDuplicateReason(reason string) bool {
	return strings.HasPrefix(reason, "duplicate:")
}

// IsAuthRequired returns true if the relay requires authentication.
func (r PublishResult) IsAuthRequired() bool {
	return IsAuthRequiredReason(r.Reason)
}

// IsRateLimited returns true if the relay is rate-limiting.
func (r PublishResult) IsRateLimited() bool {
	return IsRateLimitedReason(r.Reason)
}

// IsBlocked returns true if the event was blocked by relay policy.
func (r PublishResult) IsBlocked() bool {
	return IsBlockedReason(r.Reason)
}

// IsDuplicate returns true if the relay already has this event.
func (r PublishResult) IsDuplicate() bool {
	return IsDuplicateReason(r.Reason)
}

func (p *RelayPool) publishToRelayWithResult(ctx context.Context, mr *managedRelay, ev nostr.Event) PublishResult {
	result := PublishResult{RelayURL: mr.url}

	// Reconnect if needed; a relay in reconnect backoff fails fast.
	relay, err := p.reconnectRelay(ctx, mr)
	if err != nil {
		result.Error = fmt.Errorf("reconnecting to %s: %w", mr.url, err)
		return result
	}

	// Do not hold the per-relay state lock across network I/O. Bootstrap and
	// live-catchup subscriptions need this lock to attach to the same relay and
	// must not be starved by a slow publish.
	startedAt := time.Now()
	err = publishOnRelay(relay, ctx, ev)
	if err != nil {
		if reason, ok := publishRejectionReason(err); ok {
			if IsAuthRequiredReason(reason) {
				// The relay sent its challenge before this OK, so the pool's
				// AuthHandler attempt is running or done; Auth joins it (or
				// answers the challenge) and reports the AUTH OK.
				authErr := p.authenticateLiveRelay(ctx, mr, relay)
				if authErr == nil {
					err = publishOnRelay(relay, ctx, ev)
					if err == nil {
						p.recordRelayPublishSuccess(mr.url, time.Since(startedAt))
						p.recordRelayConnectionState(mr.url, true)
						p.logger.Info("event accepted by relay after NIP-42 AUTH",
							zap.String("relay", mr.url),
							zap.String("event_id", ev.ID.Hex()),
						)
						result.Accepted = true
						return result
					}
					if retryReason, retryOK := publishRejectionReason(err); retryOK {
						reason = retryReason
					}
				} else {
					p.logger.Warn("publish AUTH retry failed",
						zap.String("relay", mr.url),
						zap.String("event_id", ev.ID.Hex()),
						zap.Error(authErr),
					)
				}
			}
			result.Reason = reason
			p.recordRelayPublishFailure(mr.url, reason)
			p.logPublishRejection(mr.url, ev.ID.Hex(), reason)
			return result
		}

		// Transport/connection error - mark as disconnected.
		mr.mu.Lock()
		markedDisconnected := false
		if mr.relay == relay {
			mr.connected = false
			mr.lastErr = err
			markedDisconnected = true
		}
		mr.mu.Unlock()
		if markedDisconnected {
			p.recordRelayConnectionState(mr.url, false)
		}
		p.recordRelayPublishFailure(mr.url, err.Error())
		p.logger.Warn("publish failed (transport error), marking relay disconnected",
			zap.String("relay", mr.url),
			zap.String("event_id", ev.ID.Hex()),
			zap.Error(err),
		)
		result.Error = fmt.Errorf("publishing to %s: %w", mr.url, err)
		return result
	}

	// Success - relay accepted the event.
	p.recordRelayPublishSuccess(mr.url, time.Since(startedAt))
	p.recordRelayConnectionState(mr.url, true)
	p.logger.Debug("event accepted by relay",
		zap.String("relay", mr.url),
		zap.String("event_id", ev.ID.Hex()),
	)
	result.Accepted = true
	return result
}

func publishRejectionReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	message := err.Error()
	if !strings.HasPrefix(message, "msg:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(message, "msg:")), true
}

func (p *RelayPool) logPublishRejection(relayURL, eventID, reason string) {
	if IsAuthRequiredReason(reason) {
		p.logger.Warn("relay requires authentication",
			zap.String("relay", relayURL),
			zap.String("event_id", eventID),
			zap.String("reason", reason),
		)
		return
	}
	if IsRateLimitedReason(reason) {
		p.logger.Warn("relay rate-limited publish",
			zap.String("relay", relayURL),
			zap.String("event_id", eventID),
			zap.String("reason", reason),
		)
		return
	}
	if IsBlockedReason(reason) {
		p.logger.Warn("relay blocked event",
			zap.String("relay", relayURL),
			zap.String("event_id", eventID),
			zap.String("reason", reason),
		)
		return
	}
	if IsDuplicateReason(reason) {
		p.logger.Debug("relay already has event",
			zap.String("relay", relayURL),
			zap.String("event_id", eventID),
		)
		return
	}

	p.logger.Warn("relay rejected event",
		zap.String("relay", relayURL),
		zap.String("event_id", eventID),
		zap.String("reason", reason),
	)
}

func (p *RelayPool) orderedRelaysLocked() []*managedRelay {
	relays := make([]*managedRelay, 0, len(p.relays))
	seen := make(map[string]struct{}, len(p.relays))
	for _, url := range p.urls {
		if _, alreadySeen := seen[url]; alreadySeen {
			continue
		}
		mr, ok := p.relays[url]
		if !ok {
			continue
		}
		relays = append(relays, mr)
		seen[url] = struct{}{}
	}
	for url, mr := range p.relays {
		if _, ok := seen[url]; ok {
			continue
		}
		relays = append(relays, mr)
	}
	return relays
}

func countSuccessfulPublishResults(results []PublishResult) int {
	published := 0
	for _, result := range results {
		if result.Accepted || result.IsDuplicate() {
			published++
		}
	}
	return published
}

func aggregatePublishResultsError(results []PublishResult) error {
	if len(results) == 0 || countSuccessfulPublishResults(results) > 0 {
		return nil
	}

	failures := make([]string, 0, len(results))
	causes := make([]error, 0, len(results))
	for _, result := range results {
		switch {
		case result.Error != nil:
			failures = append(failures, fmt.Sprintf("%s transport error: %v", result.RelayURL, result.Error))
			causes = append(causes, result.Error)
		case result.Reason != "":
			failures = append(failures, fmt.Sprintf("%s rejected event: %s", result.RelayURL, result.Reason))
		case !result.Accepted:
			failures = append(failures, fmt.Sprintf("%s did not accept event", result.RelayURL))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return publishAggregateError{
		message: "failed to publish to any relay: " + strings.Join(failures, "; "),
		causes:  causes,
	}
}

type publishAggregateError struct {
	message string
	causes  []error
}

func (e publishAggregateError) Error() string {
	return e.message
}

func (e publishAggregateError) Unwrap() []error {
	return e.causes
}

// RelayEOSE identifies the relay subscription that reached end-of-stored-events.
type RelayEOSE struct {
	RelayURL       string
	SubscriptionID string
}

// RelayClosed identifies a relay subscription CLOSED message and its relay-provided reason.
type RelayClosed struct {
	RelayURL       string
	SubscriptionID string
	Reason         string
	// Terminal is true when the pool will not reissue this REQ on this relay
	// (see ClassifyClosedReason); otherwise it is reissued after a backoff.
	Terminal bool
}

// MergedSubscription holds the merged event stream and protocol metadata from multiple relay subscriptions.
type MergedSubscription struct {
	// Events receives events from all subscribed relays.
	Events <-chan *nostr.Event
	// EndOfStoredEvents is closed when all relay subscriptions have sent EOSE.
	EndOfStoredEvents <-chan struct{}
	// RelayEOSE emits once for each relay subscription that sends EOSE.
	RelayEOSE <-chan RelayEOSE
	// Closed emits relay CLOSED reasons for each relay subscription that reports one.
	Closed <-chan RelayClosed

	closeFn      func()
	relayURLs    []string
	eventSources *sync.Map
	active       *activeMergedSubscription
}

// PendingEOSE exposes the initial relay URLs that have not yet reached a
// terminal state. It is intended for bounded bootstrap diagnostics; callers
// must not use it as a synchronization primitive.
func (m *MergedSubscription) PendingEOSE() []string {
	if m == nil || m.active == nil {
		return nil
	}
	return m.active.pendingEOSESnapshot()
}

// HasRealEOSE reports whether any relay explicitly sent protocol EOSE. A
// terminal subscription without EOSE is not sufficient bootstrap evidence.
func (m *MergedSubscription) HasRealEOSE() bool {
	if m == nil || m.active == nil {
		// Synthetic and legacy merged subscriptions close their aggregate EOSE
		// channel only when their caller has supplied EOSE semantics.
		return true
	}
	return m.active.hasRealEOSE()
}

// AllRelaysReachedEOSE distinguishes complete history from terminal exhaustion.
// The aggregate EndOfStoredEvents channel can also close on CLOSED/disconnect.
func (m *MergedSubscription) AllRelaysReachedEOSE() bool {
	if m == nil {
		return false
	}
	if m.active == nil {
		return m.HasRealEOSE()
	}
	m.active.mu.Lock()
	defer m.active.mu.Unlock()
	return m.active.initialRemaining == 0 && !m.active.initialWithoutEOSE && m.active.realEOSECount > 0
}

// Close cancels all relay subscriptions represented by the merged subscription.
func (m *MergedSubscription) Close() {
	if m == nil || m.closeFn == nil {
		return
	}
	m.closeFn()
}

// RelayURLs returns the normalized relays that established subscriptions.
// It excludes relays that were unavailable or could not authenticate.
func (m *MergedSubscription) RelayURLs() []string {
	if m == nil {
		return nil
	}
	if m.active != nil {
		return m.active.relayURLsSnapshot()
	}
	return append([]string(nil), m.relayURLs...)
}

// EventSource returns the relay that first delivered an event to this merged
// subscription. It is provenance only; replaceable-event ordering never depends
// on relay arrival order.
func (m *MergedSubscription) EventSource(eventID string) string {
	if m == nil || m.eventSources == nil || eventID == "" {
		return ""
	}
	source, ok := m.eventSources.Load(eventID)
	if !ok {
		return ""
	}
	relayURL, _ := source.(string)
	return relayURL
}

func (m *MergedSubscription) recordEventSource(eventID, relayURL string) {
	if m == nil || m.eventSources == nil || eventID == "" {
		return
	}
	m.eventSources.LoadOrStore(eventID, relayURL)
}

// RelayStoredStatus is one relay's answer to a subscription's initial REQs.
type RelayStoredStatus string

const (
	// RelayStoredPending means the relay has not answered every initial REQ.
	RelayStoredPending RelayStoredStatus = "pending"
	// RelayStoredEOSE means the relay sent EOSE for every initial REQ.
	RelayStoredEOSE RelayStoredStatus = "eose"
	// RelayStoredClosed means the relay CLOSED an initial REQ before its EOSE
	// (and, for "auth-required:", AUTH did not recover it).
	RelayStoredClosed RelayStoredStatus = "closed"
)

// RelayStoredOutcome is one relay's answer to a subscription's initial REQs.
// Reason is the CLOSED reason, or for a pending relay why it has no REQ yet.
type RelayStoredOutcome struct {
	RelayURL string
	Status   RelayStoredStatus
	Reason   string
}

// ErrStoredEventsIncomplete matches every *StoredEventsIncompleteError.
var ErrStoredEventsIncomplete = errors.New("relay stored events are incomplete")

// StoredEventsIncompleteError reports a stored-event read that ended without
// EOSE from every relay. It is never a complete result. Cause is the context
// error when a deadline or cancellation ended the wait, and nil when every
// relay answered but at least one CLOSED instead of sending EOSE.
type StoredEventsIncompleteError struct {
	// Relays lists every relay that did not send EOSE.
	Relays []RelayStoredOutcome
	// Total is the number of relays the read covered, so Total-len(Relays)
	// relays sent EOSE. It is zero when the relay set is unknown.
	Total int
	Cause error
}

func (e *StoredEventsIncompleteError) Error() string {
	parts := make([]string, 0, len(e.Relays))
	for _, relay := range e.Relays {
		part := relay.RelayURL + ": " + string(relay.Status)
		if relay.Reason != "" {
			part += " (" + relay.Reason + ")"
		}
		parts = append(parts, part)
	}
	detail := strings.Join(parts, "; ")
	if detail == "" {
		detail = "no relay reported EOSE"
	}
	if e.Cause != nil {
		return fmt.Sprintf("relay stored events are incomplete (%v): %s", e.Cause, detail)
	}
	return "relay stored events are incomplete: " + detail
}

func (e *StoredEventsIncompleteError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrStoredEventsIncomplete}
	}
	return []error{ErrStoredEventsIncomplete, e.Cause}
}

// DefaultStoredEventsTimeout bounds a stored-event read whose caller context
// has no deadline. An earlier caller deadline always wins.
const DefaultStoredEventsTimeout = 15 * time.Second

// BoundStoredEventsWait returns ctx bounded by limit unless ctx already
// carries a deadline, which the caller owns.
func BoundStoredEventsWait(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, limit)
}

// StoredOutcomes returns each relay's answer to the initial REQs so far, in
// configured relay order. Relays left out of the accounting (see
// SubscribeOptions.AwaitUnavailableRelays) are not listed.
func (m *MergedSubscription) StoredOutcomes() []RelayStoredOutcome {
	if m == nil || m.active == nil {
		return nil
	}
	return m.active.outcomesSnapshot()
}

// StoredEventsIncomplete returns nil when every relay has sent EOSE for every
// initial REQ, and otherwise a *StoredEventsIncompleteError naming the relays
// that CLOSED or have not answered. cause is why the caller stopped waiting
// (a context error), or nil when it saw EndOfStoredEvents.
func (m *MergedSubscription) StoredEventsIncomplete(cause error) error {
	outcomes := m.StoredOutcomes()
	var missing []RelayStoredOutcome
	for _, outcome := range outcomes {
		if outcome.Status != RelayStoredEOSE {
			missing = append(missing, outcome)
		}
	}
	if len(missing) == 0 && (len(outcomes) > 0 || cause == nil) {
		return nil
	}
	return &StoredEventsIncompleteError{Relays: missing, Total: len(outcomes), Cause: cause}
}

// SubscribeOptions tunes SubscribeWithOptions. The zero value is
// SubscribeAllWithEOSE.
type SubscribeOptions struct {
	// Relays restricts the subscription to these configured relays (every
	// configured relay when nil). URLs the pool does not hold are ignored.
	Relays []string
	// AwaitUnavailableRelays keeps relays that cannot be subscribed when the
	// subscription opens, or whose connection drops before EOSE, pending in
	// the stored-event accounting: EndOfStoredEvents and
	// StoredEventsIncomplete wait for a reissued REQ's answer while the pool
	// keeps retrying (callers bound the wait). Without it a relay unavailable
	// at open time is left out of the accounting (and out of RelayURLs until
	// it recovers), a drop before EOSE settles the relay as incomplete, and
	// the call fails when no relay can be subscribed. Either way the pool
	// keeps reissuing the REQ for realtime delivery.
	AwaitUnavailableRelays bool
	// ResumeOverlap, when positive, gives every relay REQ a resume cursor (see
	// relayResumeCursor): once the relay has sent EOSE, a REQ reissued after a
	// dropped connection or a retryable CLOSED asks only for events since the
	// newest one that relay delivered, less the overlap. Without it reissued
	// REQs repeat the original filter and deduplication absorbs the replay.
	ResumeOverlap time.Duration
	// ValidateEvent, when set, drops events it rejects before they reach the
	// resume cursor, deduplication or the caller.
	ValidateEvent func(*nostr.Event) bool
}

// SubscribeAll creates subscriptions on all connected relays and merges events into a single channel.
// Deprecated: Use SubscribeAllWithEOSE for EOSE-aware subscriptions.
func (p *RelayPool) SubscribeAll(ctx context.Context, filters []nostr.Filter) (<-chan *nostr.Event, error) {
	merged, err := p.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	return merged.Events, nil
}

// SubscribeAllWithEOSE creates subscriptions on all connected relays and merges events.
// Returns a MergedSubscription with both the event channel and an EOSE signal.
func (p *RelayPool) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*MergedSubscription, error) {
	return p.SubscribeWithOptions(ctx, filters, SubscribeOptions{})
}

// SubscribeWithOptions opens filters on the pool's relays and merges their
// events. Each filter is its own REQ on each relay (so a relay's NIP-11
// max_filters is never exceeded), and each (relay, filter) REQ is supervised
// on its own until ctx ends or the subscription is closed:
//
//   - CLOSED "auth-required:" answers the relay's NIP-42 challenge (the
//     pool's AuthHandler, or a join of its attempt) and reissues that REQ at
//     once. It is surfaced on Closed only when AUTH is impossible or fails.
//   - CLOSED "blocked:", "restricted:", "invalid:" and other policy refusals
//     (see ClassifyClosedReason) stop that REQ for good: surfaced with
//     Terminal set, never retried.
//   - CLOSED "error:", "rate-limited:" or an unknown reason, and a dropped
//     connection, reissue that REQ on that relay only, after a backoff and a
//     reconnect. Other relays are unaffected.
//
// Events closes once every REQ has stopped for good or the subscription ends.
// NIP-11 limitations fetched on connect are applied per relay: limit is
// capped at max_limit, and REQs beyond max_subscriptions wait for a free slot.
func (p *RelayPool) SubscribeWithOptions(ctx context.Context, filters []nostr.Filter, opts SubscribeOptions) (*MergedSubscription, error) {
	if len(filters) == 0 {
		return nil, fmt.Errorf("at least one subscription filter is required")
	}
	relays := p.subscriptionRelays(opts.Relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("no relays available for subscription")
	}

	subCtx, cancel := context.WithCancel(ctx)
	state := p.newActiveMergedSubscription(subCtx, cancel, filters, opts)

	type openedRelay struct {
		mr      *managedRelay
		group   *activeRelayGroup
		workers []*relayFilterWorker
		initial bool
	}
	opened := make([]openedRelay, 0, len(relays))
	established := 0
	for _, mr := range relays {
		groupCtx, groupCancel := context.WithCancel(subCtx)
		group := &activeRelayGroup{ctx: groupCtx, cancel: groupCancel}
		workers, err := p.openRelayWorkers(ctx, groupCtx, mr, state.filters, opts)
		if err != nil {
			p.logger.Warn("subscription failed", zap.String("relay", mr.url), zap.Error(err))
		} else {
			established++
		}
		opened = append(opened, openedRelay{mr: mr, group: group, workers: workers, initial: err == nil || opts.AwaitUnavailableRelays})
	}
	if established == 0 && !opts.AwaitUnavailableRelays {
		for _, relay := range opened {
			relay.group.cancel()
		}
		cancel()
		p.unregisterActiveSubscription(state.id)
		return nil, fmt.Errorf("no relays available for subscription")
	}

	state.mu.Lock()
	for _, relay := range opened {
		if relay.initial {
			state.addInitialRelayLocked(relay.mr.url, relay.workers)
		}
	}
	for _, relay := range opened {
		state.groups[relay.mr.url] = relay.group
		for _, worker := range relay.workers {
			worker.initial = relay.initial
			state.startWorkerLocked(worker, relay.group)
		}
	}
	state.checkInitialCompleteLocked()
	state.mu.Unlock()

	go state.finish()
	return state.merged(), nil
}

// subscriptionRelays snapshots the configured relays named in urls (all when
// nil), in configured order.
func (p *RelayPool) subscriptionRelays(urls []string) []*managedRelay {
	p.mu.RLock()
	relays := p.orderedRelaysLocked()
	p.mu.RUnlock()
	if urls == nil {
		return relays
	}
	wanted := relayURLSet(normalizeRelayURLs(urls))
	selected := relays[:0]
	for _, mr := range relays {
		if _, ok := wanted[mr.url]; ok {
			selected = append(selected, mr)
		}
	}
	return selected
}

// ClosedAction is what the pool does with a relay's CLOSED for a REQ.
type ClosedAction int

const (
	// ClosedRetry reissues the REQ on that relay after a backoff.
	ClosedRetry ClosedAction = iota
	// ClosedAuthenticate answers the relay's NIP-42 challenge and reissues.
	ClosedAuthenticate
	// ClosedTerminal stops the REQ on that relay for good.
	ClosedTerminal
)

// ClassifyClosedReason maps a NIP-01 CLOSED reason to the pool's action by
// its machine-readable prefix. "auth-required:" authenticates; "blocked:",
// "restricted:", "invalid:", "unsupported:", "pow:" and "mute:" are policy
// refusals that a retry cannot change; "error:", "rate-limited:" and reasons
// without a known prefix are retried with backoff.
func ClassifyClosedReason(reason string) ClosedAction {
	prefix := strings.ToLower(strings.TrimSpace(reason))
	if i := strings.IndexByte(prefix, ':'); i >= 0 {
		prefix = prefix[:i]
	}
	switch prefix {
	case "auth-required":
		return ClosedAuthenticate
	case "blocked", "restricted", "invalid", "unsupported", "pow", "mute":
		return ClosedTerminal
	default:
		return ClosedRetry
	}
}

type activeRelayGroup struct {
	ctx       context.Context
	cancel    context.CancelFunc
	remaining int
}

// relayFilterWorker keeps one filter's REQ alive on one relay.
type relayFilterWorker struct {
	mr     *managedRelay
	filter nostr.Filter
	cursor *relayResumeCursor
	// initial is true when the worker counts toward the subscription's
	// stored-event accounting (EndOfStoredEvents, StoredOutcomes).
	initial bool
	// sub is the REQ sent while the subscription opened, with release freeing
	// its subscription slot; nil when the worker must subscribe itself.
	sub     *nostr.Subscription
	release func()
	// immediate makes that first self-subscribe skip the backoff (a REQ that
	// only waits for a subscription slot has not failed).
	immediate bool
	// pending explains why the worker has no REQ yet.
	pending string
	// clampLogged avoids repeating the max_limit log for every reissue.
	clampLogged bool
}

// reqFilter is the filter for the worker's next REQ: resumed from its cursor
// and capped at the relay's NIP-11 max_limit.
func (w *relayFilterWorker) reqFilter(pool *RelayPool, limits relayLimits) nostr.Filter {
	filter := w.cursor.resume(w.filter)
	if limits.MaxLimit > 0 && filter.Limit > limits.MaxLimit {
		if !w.clampLogged {
			w.clampLogged = true
			pool.logger.Debug("capping REQ limit at relay NIP-11 max_limit",
				zap.String("relay", w.mr.url), zap.Int("limit", filter.Limit), zap.Int("max_limit", limits.MaxLimit))
		}
		filter.Limit = limits.MaxLimit
	}
	return filter
}

// openRelayWorkers connects mr if needed and sends one REQ per filter under
// groupCtx. Filters that cannot be sent now (no free subscription slot, or a
// failed REQ) get a worker without a REQ that keeps trying. The error is
// non-nil when no REQ reached the relay.
func (p *RelayPool) openRelayWorkers(connectCtx, groupCtx context.Context, mr *managedRelay, filters []nostr.Filter, opts SubscribeOptions) ([]*relayFilterWorker, error) {
	workers := make([]*relayFilterWorker, len(filters))
	for i, filter := range filters {
		var cursor *relayResumeCursor
		if opts.ResumeOverlap > 0 {
			cursor = newRelayResumeCursor(opts.ResumeOverlap)
		}
		workers[i] = &relayFilterWorker{mr: mr, filter: filter, cursor: cursor}
	}

	if !managedRelayConnected(mr) {
		p.recordRelayReconnect(mr.url)
		p.connectOne(connectCtx, mr)
	}
	relay, err := p.liveRelay(mr)
	if err != nil {
		for _, worker := range workers {
			worker.pending = err.Error()
		}
		return workers, err
	}
	limits := p.awaitRelayLimits(connectCtx, mr)

	var lastErr error
	sent := 0
	for _, worker := range workers {
		release, ok := p.tryAcquireSubscriptionSlot(mr)
		if !ok {
			worker.pending = fmt.Sprintf("waiting for a subscription slot (NIP-11 max_subscriptions %d)", limits.MaxSubscriptions)
			worker.immediate = true
			continue
		}
		worker.cursor.begin()
		sub, err := subscribeOnRelay(relay, groupCtx, worker.reqFilter(p, limits))
		if err != nil {
			release()
			lastErr = err
			worker.pending = err.Error()
			p.recordRelayError(mr.url, err.Error())
			continue
		}
		worker.sub, worker.release = sub, release
		sent++
	}
	if sent == 0 && lastErr != nil {
		p.markRelayDisconnectedIfDead(mr, relay)
		return workers, lastErr
	}
	if sent > 0 {
		p.recordRelayConnectionState(mr.url, true)
	}
	return workers, nil
}

// liveRelay returns mr's connection when the pool holds one.
func (p *RelayPool) liveRelay(mr *managedRelay) (*nostr.Relay, error) {
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if !mr.connected || mr.relay == nil {
		return nil, fmt.Errorf("relay %s is not connected", mr.url)
	}
	return mr.relay, nil
}

// markRelayDisconnectedIfDead clears mr's connection when relay (the one a
// REQ was just sent on) has lost its websocket, so the next use redials.
func (p *RelayPool) markRelayDisconnectedIfDead(mr *managedRelay, relay *nostr.Relay) {
	if relay == nil || relay.Context().Err() == nil {
		return
	}
	mr.mu.Lock()
	marked := false
	if mr.relay == relay && mr.connected {
		mr.connected = false
		mr.lastErr = context.Cause(relay.Context())
		marked = true
	}
	mr.mu.Unlock()
	if marked {
		p.recordRelayConnectionState(mr.url, false)
	}
}

type activeMergedSubscription struct {
	pool          *RelayPool
	id            uint64
	ctx           context.Context
	cancel        context.CancelFunc
	filters       []nostr.Filter
	opts          SubscribeOptions
	events        chan *nostr.Event
	eose          chan struct{}
	relayEOSE     chan RelayEOSE
	closed        chan RelayClosed
	eventSources  *sync.Map
	dedup         *EventDeduplicator
	validateEvent func(*nostr.Event) bool

	mu                 sync.Mutex
	groups             map[string]*activeRelayGroup
	established        map[string]struct{}
	initialRemaining   int
	initialWithoutEOSE bool
	initialPending     map[string]int
	// outcomes holds each initial relay's answer to the initial REQs, in
	// relayOrder.
	outcomes      map[string]*RelayStoredOutcome
	relayOrder    []string
	realEOSECount int
	eoseOnce      sync.Once
	workers       sync.WaitGroup
	closeOnce     sync.Once
}

func (p *RelayPool) newActiveMergedSubscription(ctx context.Context, cancel context.CancelFunc, filters []nostr.Filter, opts SubscribeOptions) *activeMergedSubscription {
	state := &activeMergedSubscription{
		pool:           p,
		ctx:            ctx,
		cancel:         cancel,
		filters:        append([]nostr.Filter(nil), filters...),
		opts:           opts,
		events:         make(chan *nostr.Event, 64),
		eose:           make(chan struct{}),
		relayEOSE:      make(chan RelayEOSE, 64),
		closed:         make(chan RelayClosed, 64),
		eventSources:   &sync.Map{},
		dedup:          NewEventDeduplicator(10000),
		validateEvent:  opts.ValidateEvent,
		groups:         make(map[string]*activeRelayGroup),
		established:    make(map[string]struct{}),
		initialPending: make(map[string]int),
		outcomes:       make(map[string]*RelayStoredOutcome),
	}

	p.subscriptionsMu.Lock()
	p.nextSubscriptionID++
	state.id = p.nextSubscriptionID
	p.activeSubscriptions[state.id] = state
	p.subscriptionsMu.Unlock()
	return state
}

func (s *activeMergedSubscription) merged() *MergedSubscription {
	return &MergedSubscription{
		Events:            s.events,
		EndOfStoredEvents: s.eose,
		RelayEOSE:         s.relayEOSE,
		Closed:            s.closed,
		closeFn:           s.close,
		eventSources:      s.eventSources,
		active:            s,
	}
}

// addInitialRelayLocked adds relayURL's workers to the stored-event
// accounting. s.mu is held.
func (s *activeMergedSubscription) addInitialRelayLocked(relayURL string, workers []*relayFilterWorker) {
	if len(workers) == 0 {
		return
	}
	outcome := &RelayStoredOutcome{RelayURL: relayURL, Status: RelayStoredPending}
	for _, worker := range workers {
		if worker.sub == nil && worker.pending != "" {
			outcome.Reason = worker.pending
		}
		if worker.sub != nil {
			s.established[relayURL] = struct{}{}
		}
	}
	s.outcomes[relayURL] = outcome
	s.relayOrder = append(s.relayOrder, relayURL)
	s.initialRemaining += len(workers)
	s.initialPending[relayURL] += len(workers)
}

func (s *activeMergedSubscription) checkInitialCompleteLocked() {
	if s.initialRemaining == 0 {
		s.eoseOnce.Do(func() { close(s.eose) })
	}
}

func (s *activeMergedSubscription) startWorkerLocked(worker *relayFilterWorker, group *activeRelayGroup) {
	group.remaining++
	if worker.sub != nil {
		s.established[worker.mr.url] = struct{}{}
	}
	s.workers.Add(1)
	go s.runWorker(worker, group)
}

// relaySubscriptionEnd is how one REQ generation ended.
type relaySubscriptionEnd struct {
	eosed  bool
	closed bool
	reason string
}

// runWorker supervises one filter's REQ on one relay; see SubscribeWithOptions.
func (s *activeMergedSubscription) runWorker(worker *relayFilterWorker, group *activeRelayGroup) {
	defer s.workers.Done()
	defer s.workerDone(worker.mr.url, group)
	ctx := group.ctx
	relayURL := worker.mr.url

	settled := false
	settle := func(status RelayStoredStatus, reason string) {
		if !worker.initial || settled {
			return
		}
		settled = true
		s.settleInitial(relayURL, status, reason)
	}
	defer func() {
		// A REQ whose relay left the pool never answered and must not hold
		// EndOfStoredEvents open. One stopped because the subscription ended
		// stays pending: it never answered, and finish closes
		// EndOfStoredEvents anyway.
		if s.ctx.Err() == nil {
			settle(RelayStoredClosed, "relay removed from the pool before EOSE")
		}
	}()

	backoff := s.pool.newResubscribeBackoff()
	sub, release := worker.sub, worker.release
	worker.sub, worker.release = nil, nil
	immediate := worker.immediate
	if sub == nil && worker.pending != "" {
		s.setPendingReason(relayURL, worker.pending)
	}
	authRetried := false
	// Only a worker's first EOSE and first CLOSED block on the consumer.
	eoseEmitted, closedEmitted := false, false
	for {
		if sub == nil {
			sub, release = s.resubscribe(ctx, worker, backoff, immediate)
			if sub == nil {
				return
			}
			s.markEstablished(relayURL)
		}
		immediate = false
		subID := subscriptionID(sub)
		end := s.consume(ctx, worker, sub, func() {
			settle(RelayStoredEOSE, "")
			s.emitRelayEOSE(ctx, RelayEOSE{RelayURL: relayURL, SubscriptionID: subID}, !eoseEmitted)
			eoseEmitted = true
		})
		relay := sub.Relay
		release()
		sub, release = nil, nil
		if ctx.Err() != nil {
			return
		}
		if end.eosed {
			authRetried = false
			backoff.Reset()
		}
		if !end.closed {
			// The REQ ended without CLOSED: the connection dropped. Reissue
			// on this relay only, after a backoff and a reconnect. The drop
			// settles the relay's stored-event answer as incomplete unless
			// the caller waits for unavailable relays to come back.
			if !s.opts.AwaitUnavailableRelays {
				settle(RelayStoredClosed, "connection lost before EOSE")
			}
			s.pool.markRelayDisconnectedIfDead(worker.mr, relay)
			s.pool.logger.Debug("relay subscription dropped; reissuing on that relay",
				zap.String("relay", relayURL))
			continue
		}

		reason := end.reason
		action := ClassifyClosedReason(reason)
		if action == ClosedAuthenticate {
			if !authRetried {
				err := s.pool.authenticateLiveRelay(ctx, worker.mr, relay)
				if err == nil {
					authRetried = true
					immediate = true
					continue
				}
				s.pool.logger.Warn("relay requires NIP-42 AUTH for a REQ and AUTH failed",
					zap.String("relay", relayURL), zap.String("reason", reason), zap.Error(err))
				s.pool.recordRelayError(relayURL, authUnavailableMetadata(reason, err))
			}
			action = ClosedTerminal
		}
		terminal := action == ClosedTerminal
		settle(RelayStoredClosed, reason)
		s.emitClosed(ctx, RelayClosed{RelayURL: relayURL, SubscriptionID: subID, Reason: reason, Terminal: terminal}, !closedEmitted)
		closedEmitted = true
		if terminal {
			s.pool.logger.Warn("relay refused subscription; not retrying",
				zap.String("relay", relayURL), zap.String("reason", reason))
			return
		}
		s.pool.logger.Debug("relay closed subscription; reissuing after backoff",
			zap.String("relay", relayURL), zap.String("reason", reason))
	}
}

// resubscribe sends worker's next REQ on its relay, waiting out the backoff
// first unless immediate, reconnecting and waiting for a subscription slot as
// needed. It returns nil once ctx ends.
func (s *activeMergedSubscription) resubscribe(ctx context.Context, worker *relayFilterWorker, backoff *Backoff, immediate bool) (*nostr.Subscription, func()) {
	pool := s.pool
	mr := worker.mr
	for {
		if !immediate {
			timer := time.NewTimer(backoff.Next())
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, nil
			case <-timer.C:
			}
		}
		immediate = false
		relay, err := pool.reconnectRelay(ctx, mr)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil
			}
			s.setPendingReason(mr.url, err.Error())
			continue
		}
		limits := pool.awaitRelayLimits(ctx, mr)
		release, err := pool.acquireSubscriptionSlot(ctx, mr, func(max int) {
			s.setPendingReason(mr.url, fmt.Sprintf("waiting for a subscription slot (NIP-11 max_subscriptions %d)", max))
		})
		if err != nil {
			return nil, nil
		}
		worker.cursor.begin()
		sub, err := subscribeOnRelay(relay, ctx, worker.reqFilter(pool, limits))
		if err != nil {
			release()
			if ctx.Err() != nil {
				return nil, nil
			}
			pool.markRelayDisconnectedIfDead(mr, relay)
			pool.recordRelayError(mr.url, err.Error())
			s.setPendingReason(mr.url, err.Error())
			continue
		}
		pool.recordRelayReREQ(mr.url)
		pool.recordRelayConnectionState(mr.url, true)
		return sub, release
	}
}

// consume forwards one REQ generation's events until it ends. onEOSE runs
// once the generation's stored events have all been forwarded.
func (s *activeMergedSubscription) consume(ctx context.Context, worker *relayFilterWorker, sub *nostr.Subscription, onEOSE func()) relaySubscriptionEnd {
	var end relaySubscriptionEnd
	eoseCh := sub.EndOfStoredEvents
	eventsCh := sub.Events
	closedCh := sub.ClosedReason
	relayURL := worker.mr.url
	forward := func(event nostr.Event) bool {
		ev := &event
		if s.validateEvent != nil && !s.validateEvent(ev) {
			return true
		}
		// The cursor counts every valid event its relay delivered, including
		// ones another relay delivered first.
		worker.cursor.observe(ev)
		eventID := event.ID.Hex()
		if eventID != "" && s.dedup.IsDuplicate(eventID) {
			return true
		}
		s.eventSources.LoadOrStore(eventID, relayURL)
		select {
		case s.events <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	closedWith := func(reason string) relaySubscriptionEnd {
		end.closed = true
		end.reason = strings.TrimSpace(reason)
		return end
	}

	for eventsCh != nil {
		select {
		case <-ctx.Done():
			return end
		case _, ok := <-eoseCh:
			if ok || eoseCh != nil {
				// A relay may have buffered EVENTs when EOSE becomes readable.
				// Forward those before announcing that its history is complete.
			drain:
				for {
					select {
					case event, open := <-eventsCh:
						if !open {
							break drain
						}
						if !forward(event) {
							return end
						}
					default:
						break drain
					}
				}
				worker.cursor.eose()
				end.eosed = true
				onEOSE()
			}
			eoseCh = nil
		case reason, ok := <-closedCh:
			if !ok {
				closedCh = nil
				continue
			}
			return closedWith(reason)
		case ev, ok := <-eventsCh:
			if !ok {
				// The library delivers CLOSED before it ends the subscription.
				if closedCh != nil {
					select {
					case reason, ok := <-closedCh:
						if ok {
							return closedWith(reason)
						}
					default:
					}
				}
				return end
			}
			if !forward(ev) {
				return end
			}
		}
	}
	return end
}

// emitRelayEOSE and emitClosed block for a REQ's first answer, as callers
// count them, and never block for the answers of reissued REQs: a
// long-lived subscription must not stall on a consumer that ignores them.
func (s *activeMergedSubscription) emitRelayEOSE(ctx context.Context, info RelayEOSE, block bool) {
	if block {
		select {
		case s.relayEOSE <- info:
		case <-ctx.Done():
		}
		return
	}
	select {
	case s.relayEOSE <- info:
	default:
	}
}

func (s *activeMergedSubscription) emitClosed(ctx context.Context, info RelayClosed, block bool) {
	if block {
		emitRelayClosed(ctx, s.closed, info)
		return
	}
	select {
	case s.closed <- info:
	default:
	}
}

func (s *activeMergedSubscription) settleInitial(relayURL string, status RelayStoredStatus, reason string) {
	s.mu.Lock()
	if status == RelayStoredEOSE {
		s.realEOSECount++
	} else {
		s.initialWithoutEOSE = true
	}
	if s.initialRemaining > 0 {
		s.initialRemaining--
	}
	remaining := s.initialPending[relayURL] - 1
	if remaining <= 0 {
		delete(s.initialPending, relayURL)
	} else {
		s.initialPending[relayURL] = remaining
	}
	if outcome := s.outcomes[relayURL]; outcome != nil && outcome.Status != RelayStoredClosed {
		switch {
		case status == RelayStoredClosed:
			outcome.Status, outcome.Reason = RelayStoredClosed, reason
		case remaining <= 0:
			outcome.Status, outcome.Reason = RelayStoredEOSE, ""
		}
	}
	complete := s.initialRemaining == 0
	s.mu.Unlock()
	if complete {
		s.eoseOnce.Do(func() { close(s.eose) })
	}
}

func (s *activeMergedSubscription) setPendingReason(relayURL, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if outcome := s.outcomes[relayURL]; outcome != nil && outcome.Status == RelayStoredPending {
		outcome.Reason = reason
	}
}

func (s *activeMergedSubscription) markEstablished(relayURL string) {
	s.mu.Lock()
	s.established[relayURL] = struct{}{}
	s.mu.Unlock()
}

func (s *activeMergedSubscription) pendingEOSESnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	urls := make([]string, 0, len(s.initialPending))
	for url := range s.initialPending {
		if url != "" {
			urls = append(urls, url)
		}
	}
	sort.Strings(urls)
	return urls
}

func (s *activeMergedSubscription) outcomesSnapshot() []RelayStoredOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RelayStoredOutcome, 0, len(s.relayOrder))
	for _, url := range s.relayOrder {
		out = append(out, *s.outcomes[url])
	}
	return out
}

func (s *activeMergedSubscription) hasRealEOSE() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.realEOSECount > 0
}

func (s *activeMergedSubscription) workerDone(relayURL string, group *activeRelayGroup) {
	s.mu.Lock()
	current := s.groups[relayURL]
	if current != group {
		s.mu.Unlock()
		return
	}
	group.remaining--
	if group.remaining > 0 {
		s.mu.Unlock()
		return
	}
	delete(s.groups, relayURL)
	empty := len(s.groups) == 0
	s.mu.Unlock()
	group.cancel()
	if empty {
		s.cancel()
	}
}

func (s *activeMergedSubscription) addRelay(ctx context.Context, relay *managedRelay) error {
	if relay == nil {
		return fmt.Errorf("relay is nil")
	}
	s.mu.Lock()
	if _, exists := s.groups[relay.url]; exists {
		s.mu.Unlock()
		return nil
	}
	select {
	case <-s.ctx.Done():
		s.mu.Unlock()
		return s.ctx.Err()
	default:
	}
	filters := append([]nostr.Filter(nil), s.filters...)
	s.mu.Unlock()

	groupCtx, groupCancel := context.WithCancel(s.ctx)
	group := &activeRelayGroup{ctx: groupCtx, cancel: groupCancel}
	workers, err := s.pool.openRelayWorkers(ctx, groupCtx, relay, filters, s.opts)
	if err != nil {
		groupCancel()
		return err
	}

	s.mu.Lock()
	if _, exists := s.groups[relay.url]; exists {
		s.mu.Unlock()
		groupCancel()
		return nil
	}
	select {
	case <-s.ctx.Done():
		s.mu.Unlock()
		groupCancel()
		return s.ctx.Err()
	default:
	}
	s.groups[relay.url] = group
	for _, worker := range workers {
		s.startWorkerLocked(worker, group)
	}
	s.mu.Unlock()
	return nil
}

func (s *activeMergedSubscription) removeRelay(relayURL string) {
	s.mu.Lock()
	group := s.groups[relayURL]
	if group != nil {
		delete(s.groups, relayURL)
	}
	empty := len(s.groups) == 0
	s.mu.Unlock()
	if group != nil && group.cancel != nil {
		group.cancel()
	}
	if empty {
		s.cancel()
	}
}

func (s *activeMergedSubscription) hasRelay(relayURL string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.groups[relayURL]
	return exists
}

func (s *activeMergedSubscription) relayURLsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	urls := make([]string, 0, len(s.groups))
	for url := range s.groups {
		if _, ok := s.established[url]; ok {
			urls = append(urls, url)
		}
	}
	sort.Strings(urls)
	return urls
}

func (s *activeMergedSubscription) close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.pool.unregisterActiveSubscription(s.id)
		s.cancel()
	})
}

func (s *activeMergedSubscription) finish() {
	<-s.ctx.Done()
	s.workers.Wait()
	s.pool.unregisterActiveSubscription(s.id)
	s.eoseOnce.Do(func() { close(s.eose) })
	close(s.events)
	close(s.relayEOSE)
	close(s.closed)
}

func (p *RelayPool) unregisterActiveSubscription(id uint64) {
	p.subscriptionsMu.Lock()
	delete(p.activeSubscriptions, id)
	p.subscriptionsMu.Unlock()
	p.pruneRetiredRelays()
}

func (p *RelayPool) pruneRetiredRelays() {
	p.mu.Lock()
	p.subscriptionsMu.Lock()
	toClose := make([]*managedRelay, 0)
	for url, relay := range p.retiredRelays {
		inUse := false
		for _, subscription := range p.activeSubscriptions {
			if subscription.hasRelay(url) {
				inUse = true
				break
			}
		}
		if !inUse {
			delete(p.retiredRelays, url)
			toClose = append(toClose, relay)
		}
	}
	p.subscriptionsMu.Unlock()
	p.mu.Unlock()
	for _, relay := range toClose {
		closeManagedRelay(p, relay)
	}
}

func closeManagedRelay(pool *RelayPool, mr *managedRelay) {
	if mr == nil {
		return
	}
	mr.mu.Lock()
	if mr.relay != nil {
		if err := mr.relay.Close(); err != nil {
			pool.logger.Warn("close relay connection failed", zap.String("relay", mr.url), zap.Error(err))
		}
	}
	mr.closed = true
	mr.connected = false
	mr.lastErr = nil
	pool.recordRelayConnectionState(mr.url, false)
	mr.mu.Unlock()
}

func emitRelayClosed(ctx context.Context, closed chan<- RelayClosed, info RelayClosed) bool {
	select {
	case closed <- info:
		return true
	case <-ctx.Done():
		return false
	}
}

func subscriptionID(sub *nostr.Subscription) string {
	if sub == nil {
		return ""
	}
	return sub.GetID()
}

// RelayHealthSnapshot summarizes relay connectivity and health state.
type RelayHealthSnapshot struct {
	Total     int
	Connected int
	Healthy   int
	Relays    []RelayStatus
}

// RelayStatus describes the current status of a single relay.
type RelayStatus struct {
	URL               string
	Connected         bool
	Healthy           bool
	Degraded          bool
	SuccessRate       float64
	LastSeen          time.Time
	Errors            int
	LastError         string
	ClosedReasons     map[string]int64
	ReREQAttempts     int64
	ReconnectAttempts int64
}

// HealthSnapshot returns a point-in-time summary of configured relay state.
func (p *RelayPool) HealthSnapshot() RelayHealthSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	snapshot := RelayHealthSnapshot{Relays: make([]RelayStatus, 0, len(p.urls)+len(p.relays))}
	seen := make(map[string]struct{}, len(p.urls)+len(p.relays))
	addStatus := func(url string, connected bool) {
		if _, ok := seen[url]; ok {
			return
		}
		seen[url] = struct{}{}

		status := RelayStatus{URL: url, Connected: connected}
		if p.health != nil {
			stats := p.health.GetOrCreate(url).Stats()
			status.Connected = connected || stats.Connected
			status.Healthy = status.Connected && stats.IsHealthy()
			status.Degraded = status.Connected && stats.IsDegraded()
			status.SuccessRate = stats.SuccessRate
			status.LastSeen = stats.LastConnected
			status.Errors = int(stats.ErrorCount)
			status.LastError = stats.LastError
			status.ClosedReasons = stats.ClosedReasons
			status.ReREQAttempts = stats.ReREQAttempts
			status.ReconnectAttempts = stats.Reconnects
		} else {
			status.Healthy = connected
		}

		snapshot.Total++
		if status.Connected {
			snapshot.Connected++
		}
		if status.Healthy {
			snapshot.Healthy++
		}
		snapshot.Relays = append(snapshot.Relays, status)
	}

	for _, mr := range p.orderedRelaysLocked() {
		mr.mu.Lock()
		connected := mr.connected
		mr.mu.Unlock()
		addStatus(mr.url, connected)
	}
	for _, url := range p.urls {
		addStatus(url, false)
	}
	return snapshot
}

// ConnectedCount returns the number of relays currently marked connected.
func (p *RelayPool) ConnectedCount() int {
	return p.HealthSnapshot().Connected
}

// HealthyCount returns the number of relays currently considered healthy.
func (p *RelayPool) HealthyCount() int {
	return p.HealthSnapshot().Healthy
}

func (p *RelayPool) recordRelayConnectionState(relayURL string, connected bool) {
	if p.health == nil {
		return
	}
	if p.health.GetOrCreate(relayURL).MarkConnected(connected) {
		p.signalRelayConnected()
	}
}

// NotifyRelayConnected registers a wake-up for relay (re)connection. After
// any relay of this pool moves from disconnected to connected, the returned
// channel becomes readable. It has capacity one and the pool never blocks on
// it, even though connection state is recorded under relay locks: a burst of
// reconnects while a wake-up is pending coalesces into that one wake-up, so a
// consumer re-checks the state it cares about instead of counting signals.
// cancel unregisters the listener.
func (p *RelayPool) NotifyRelayConnected() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	p.connectedMu.Lock()
	if p.connectedListeners == nil {
		p.connectedListeners = make(map[uint64]chan struct{})
	}
	p.nextListenerID++
	id := p.nextListenerID
	p.connectedListeners[id] = ch
	p.connectedMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			p.connectedMu.Lock()
			delete(p.connectedListeners, id)
			p.connectedMu.Unlock()
		})
	}
}

func (p *RelayPool) signalRelayConnected() {
	p.connectedMu.Lock()
	defer p.connectedMu.Unlock()
	for _, ch := range p.connectedListeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (p *RelayPool) recordRelayReconnect(relayURL string) {
	if p.health == nil {
		return
	}
	p.health.GetOrCreate(relayURL).RecordReconnect()
}

func (p *RelayPool) recordRelayPublishSuccess(relayURL string, latency time.Duration) {
	if p.health == nil {
		return
	}
	p.health.GetOrCreate(relayURL).RecordPublishSuccess(latency)
}

func (p *RelayPool) recordRelayPublishFailure(relayURL, reason string) {
	if p.health == nil {
		return
	}
	p.health.GetOrCreate(relayURL).RecordPublishFailure(reason)
}

func (p *RelayPool) recordRelayError(relayURL, reason string) {
	if p.health == nil {
		return
	}
	p.health.GetOrCreate(relayURL).RecordError(reason)
}

// RecordRelayClosed records a relay CLOSED frame observed by a subscription consumer.
func (p *RelayPool) RecordRelayClosed(relayURL, reason string) {
	if p == nil || p.health == nil {
		return
	}
	normalizedURL := nostr.NormalizeURL(relayURL)
	if normalizedURL == "" {
		return
	}
	p.health.GetOrCreate(normalizedURL).RecordClosed(reason)
}

// RecordRelayReREQ records one recovery REQ attempt for every configured relay.
func (p *RelayPool) RecordRelayReREQ() {
	if p == nil || p.health == nil {
		return
	}
	for _, relayURL := range p.URLs() {
		p.health.GetOrCreate(relayURL).RecordReREQ()
	}
}

// RecordRelayError records relay-level protocol or transport metadata for
// callers that observe CLOSED/AUTH failures outside the pool internals.
func (p *RelayPool) RecordRelayError(relayURL, reason string) {
	if p == nil {
		return
	}
	normalizedURL := nostr.NormalizeURL(relayURL)
	if normalizedURL == "" {
		return
	}
	p.recordRelayError(normalizedURL, strings.TrimSpace(reason))
}

// URLs returns the list of configured relay URLs.
func (p *RelayPool) URLs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneRelayURLs(p.urls)
}

// FetchRelayInfo fetches NIP-11 relay information document for the given relay URL.
// The result is cached for subsequent calls. Use force=true to refresh the cache.
func (p *RelayPool) FetchRelayInfo(ctx context.Context, relayURL string, force bool) (*nip11.RelayInformationDocument, error) {
	normalizedURL := nostr.NormalizeURL(relayURL)

	// Check cache first (unless force refresh)
	if !force {
		p.mu.RLock()
		info, exists := p.relayInfoCache[normalizedURL]
		p.mu.RUnlock()
		if exists {
			return info, nil
		}
	}

	// Fetch from relay
	p.logger.Debug("fetching NIP-11 relay info", zap.String("relay", relayURL))
	info, err := nip11.Fetch(ctx, relayURL)
	if err != nil {
		p.logger.Warn("failed to fetch NIP-11 info",
			zap.String("relay", relayURL),
			zap.Error(err),
		)
		return nil, err
	}

	// Cache the result
	p.mu.Lock()
	p.relayInfoCache[normalizedURL] = &info
	p.mu.Unlock()

	p.logger.Info("fetched NIP-11 relay info",
		zap.String("relay", relayURL),
		zap.String("name", info.Name),
		zap.Any("supported_nips", info.SupportedNIPs),
	)

	return &info, nil
}

// GetRelayInfo returns cached NIP-11 info for the relay, or nil if not cached.
// Call FetchRelayInfo first to populate the cache.
func (p *RelayPool) GetRelayInfo(relayURL string) *nip11.RelayInformationDocument {
	normalizedURL := nostr.NormalizeURL(relayURL)
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.relayInfoCache[normalizedURL]
}

// FetchAllRelayInfo fetches NIP-11 info for all configured relays concurrently.
// Returns a map of relay URL to info (nil for relays that failed to respond).
func (p *RelayPool) FetchAllRelayInfo(ctx context.Context) map[string]*nip11.RelayInformationDocument {
	results := make(map[string]*nip11.RelayInformationDocument)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, url := range p.URLs() {
		wg.Add(1)
		go func(relayURL string) {
			defer wg.Done()
			info, _ := p.FetchRelayInfo(ctx, relayURL, false)
			mu.Lock()
			results[relayURL] = info
			mu.Unlock()
		}(url)
	}

	wg.Wait()
	return results
}

// SupportsNIP checks if a relay supports a specific NIP number.
// Returns false if relay info is not cached - call FetchRelayInfo first.
func (p *RelayPool) SupportsNIP(relayURL string, nipNumber int) bool {
	info := p.GetRelayInfo(relayURL)
	if info == nil {
		return false
	}

	for _, nip := range info.SupportedNIPs {
		switch v := nip.(type) {
		case float64:
			if int(v) == nipNumber {
				return true
			}
		case int:
			if v == nipNumber {
				return true
			}
		}
	}
	return false
}

// IsAuthRequired checks if a relay requires authentication (NIP-42).
// Returns false if relay info is not cached - call FetchRelayInfo first.
func (p *RelayPool) IsAuthRequired(relayURL string) bool {
	info := p.GetRelayInfo(relayURL)
	if info == nil || info.Limitation == nil {
		return false
	}
	return info.Limitation.AuthRequired
}

// GetMaxLimit returns the max limit for query filters, or 0 if unknown.
// Returns 0 if relay info is not cached - call FetchRelayInfo first.
func (p *RelayPool) GetMaxLimit(relayURL string) int {
	info := p.GetRelayInfo(relayURL)
	if info == nil || info.Limitation == nil {
		return 0
	}
	return info.Limitation.MaxLimit
}

// GetMaxSubscriptions returns the max concurrent subscriptions, or 0 if unknown.
// Returns 0 if relay info is not cached - call FetchRelayInfo first.
func (p *RelayPool) GetMaxSubscriptions(relayURL string) int {
	info := p.GetRelayInfo(relayURL)
	if info == nil || info.Limitation == nil {
		return 0
	}
	return info.Limitation.MaxSubscriptions
}

// buildRelayOptions wires the relay's NOTICE handler and, when the pool has
// an AUTH signer, NIP-42: every connection answers the relay's AUTH challenge
// itself (AuthHandler) and reports the relay's OK (AuthResultHandler). The
// vendored library runs one AUTH attempt at a time per connection, and
// Relay.Auth joins it, so callers that meet "auth-required:" wait for that
// attempt instead of racing it (third_party/nostr/BAHIA_PATCHES.md).
func (p *RelayPool) buildRelayOptions(relayURL string) nostr.RelayOptions {
	opts := nostr.RelayOptions{NoticeHandler: func(_ *nostr.Relay, notice string) {
		p.logger.Info("relay notice",
			zap.String("relay", relayURL),
			zap.String("notice", notice),
		)
	}}
	if p.hasAuthSigner() {
		opts.AuthHandler = func(ctx context.Context, _ *nostr.Relay, event *nostr.Event) error {
			return p.signAuthEvent(ctx, event)
		}
		opts.AuthResultHandler = func(_ *nostr.Relay, err error) {
			if err != nil {
				p.logger.Warn("NIP-42 AUTH failed", zap.String("relay", relayURL), zap.Error(err))
				p.recordRelayError(relayURL, "auth-failed: "+err.Error())
				return
			}
			p.logger.Debug("NIP-42 AUTH completed", zap.String("relay", relayURL))
		}
	}
	return opts
}

func (p *RelayPool) hasAuthSigner() bool {
	return p.privateKey != "" || p.authSigner != nil || p.authSignFunc != nil
}

func (p *RelayPool) signAuthEvent(ctx context.Context, event *nostr.Event) error {
	switch {
	case p.authSignFunc != nil:
		return p.authSignFunc(ctx, event)
	case p.authSigner != nil:
		return p.authSigner.SignEvent(ctx, event)
	default:
		return signEventWithPrivateKeyHex(event, p.privateKey)
	}
}

// authenticateLiveRelay completes NIP-42 on relay, the connection that just
// answered "auth-required:" (mr's current one when nil).
func (p *RelayPool) authenticateLiveRelay(ctx context.Context, mr *managedRelay, relay *nostr.Relay) error {
	if !p.hasAuthSigner() {
		return fmt.Errorf("no signer configured for NIP-42 AUTH")
	}
	if relay == nil {
		var err error
		if relay, err = p.liveRelay(mr); err != nil {
			return err
		}
	}
	return relay.Auth(ctx, p.signAuthEvent)
}

// authBarrierFilter asks for nothing. The relay's answer to it (EOSE or
// CLOSED) proves that any AUTH challenge it sent before, on connect or with a
// rejection, has been processed by the connection.
var authBarrierFilter = nostr.Filter{Kinds: []nostr.Kind{nostr.KindClientAuthentication}, LimitZero: true}

// AuthenticateRelays completes NIP-42 ahead of reads and writes that depend
// on authenticated relay state (relays may silently omit protected events
// from an unauthenticated REQ). For each named configured relay (all when
// nil) it connects, waits for the relay's answer to a barrier REQ, and then
// joins or starts the AUTH attempt for the relay's challenge. A relay that
// never challenges is left unauthenticated: its later "auth-required:"
// answers are handled per REQ and per publish.
func (p *RelayPool) AuthenticateRelays(ctx context.Context, relayURLs []string) error {
	if !p.hasAuthSigner() {
		return fmt.Errorf("no signer configured for NIP-42 AUTH")
	}
	for _, mr := range p.subscriptionRelays(relayURLs) {
		if err := p.authenticateRelayAhead(ctx, mr); err != nil {
			return fmt.Errorf("authenticate to %s: %w", mr.url, err)
		}
	}
	return nil
}

func (p *RelayPool) authenticateRelayAhead(ctx context.Context, mr *managedRelay) error {
	relay, err := p.ensureRelayConnected(ctx, mr, p.connectTimeout, false)
	if err != nil {
		return err
	}
	barrierCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sub, err := subscribeOnRelay(relay, barrierCtx, authBarrierFilter)
	if err != nil {
		p.markRelayDisconnectedIfDead(mr, relay)
		return err
	}
	rejected := ""
	select {
	case <-sub.EndOfStoredEvents:
	case reason := <-sub.ClosedReason:
		if IsAuthRequiredReason(reason) {
			rejected = strings.TrimSpace(reason)
		}
	case <-sub.Context.Done():
		// The library ends a subscription itself only when the connection
		// drops, and delivers its CLOSED, if any, first.
		select {
		case reason := <-sub.ClosedReason:
			if IsAuthRequiredReason(reason) {
				rejected = strings.TrimSpace(reason)
			}
		default:
			return fmt.Errorf("connection ended before the relay answered: %w", context.Cause(sub.Context))
		}
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	err = relay.Auth(ctx, p.signAuthEvent)
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "no challenge") && rejected == "":
		return nil // the relay does not require NIP-42
	case strings.Contains(err.Error(), "no challenge"):
		return fmt.Errorf("relay answered %q without sending a NIP-42 challenge", rejected)
	default:
		return err
	}
}

// AuthenticateRelay sends a NIP-42 AUTH response to a specific relay.
// Call this after receiving an auth-required error (PublishResult.IsAuthRequired()).
// Returns an error if no private key is configured or auth fails.
func (p *RelayPool) AuthenticateRelay(ctx context.Context, relayURL string) error {
	if !p.hasAuthSigner() {
		return fmt.Errorf("no signer configured for NIP-42 AUTH")
	}

	normalizedURL := nostr.NormalizeURL(relayURL)
	if normalizedURL == "" {
		return fmt.Errorf("relay not found: %s", relayURL)
	}

	p.mu.RLock()
	mr, exists := p.relays[normalizedURL]
	p.mu.RUnlock()

	if !exists {
		return fmt.Errorf("relay not found: %s", normalizedURL)
	}

	relayURL = normalizedURL

	mr.mu.Lock()
	relay := mr.relay
	mr.mu.Unlock()

	if relay == nil {
		return fmt.Errorf("relay not connected: %s", relayURL)
	}

	p.logger.Info("sending NIP-42 AUTH", zap.String("relay", relayURL))
	return relay.Auth(ctx, p.signAuthEvent)
}

// Close disconnects all relays, subscriptions, and reconnection work.
func (p *RelayPool) Close() {
	p.cancel()

	p.subscriptionsMu.Lock()
	active := make([]*activeMergedSubscription, 0, len(p.activeSubscriptions))
	for _, subscription := range p.activeSubscriptions {
		active = append(active, subscription)
	}
	p.subscriptionsMu.Unlock()
	for _, subscription := range active {
		subscription.close()
	}

	p.mu.Lock()
	relays := make([]*managedRelay, 0, len(p.relays)+len(p.retiredRelays))
	for _, relay := range p.relays {
		relays = append(relays, relay)
	}
	for _, relay := range p.retiredRelays {
		relays = append(relays, relay)
	}
	p.relays = make(map[string]*managedRelay)
	p.retiredRelays = make(map[string]*managedRelay)
	p.mu.Unlock()

	for _, relay := range relays {
		closeManagedRelay(p, relay)
	}
}

// relayResumeCursor is one REQ's position on one relay in a resumable
// subscription: the newest created_at among the valid events that relay
// delivered for it. Stored events arrive in no guaranteed order, so a
// generation's events count only once its EOSE proves the backfill below
// them complete; after EOSE, realtime events advance the cursor as they
// arrive. Timestamps are clamped to the local clock so a future-dated event
// cannot push the cursor past events not yet seen. All methods are nil-safe: a
// nil cursor means "reissue the original filter".
type relayResumeCursor struct {
	overlap nostr.Timestamp

	mu sync.Mutex
	// since is the committed cursor; zero until the relay first sends EOSE.
	since nostr.Timestamp
	// generation is the newest created_at the current REQ delivered before its
	// EOSE; live reports whether the current REQ has sent EOSE.
	generation nostr.Timestamp
	live       bool
}

func newRelayResumeCursor(overlap time.Duration) *relayResumeCursor {
	seconds := nostr.Timestamp(overlap / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return &relayResumeCursor{overlap: seconds}
}

// begin starts a new REQ generation.
func (c *relayResumeCursor) begin() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation = 0
	c.live = false
}

// observe records one valid event delivered by this relay.
func (c *relayResumeCursor) observe(ev *nostr.Event) {
	if c == nil || ev == nil {
		return
	}
	createdAt := ev.CreatedAt
	if now := nostr.Now(); createdAt > now {
		createdAt = now
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		if createdAt > c.since {
			c.since = createdAt
		}
		return
	}
	if createdAt > c.generation {
		c.generation = createdAt
	}
}

// eose commits the current generation's backfill.
func (c *relayResumeCursor) eose() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation > c.since {
		c.since = c.generation
	}
	c.live = true
}

// resume returns the filter for the next REQ: the original until the relay
// has sent EOSE, then with Since raised to the cursor less the overlap.
func (c *relayResumeCursor) resume(filter nostr.Filter) nostr.Filter {
	if c == nil {
		return filter
	}
	c.mu.Lock()
	since := c.since
	c.mu.Unlock()
	if since == 0 {
		return filter
	}
	from := since - c.overlap
	if from < 1 {
		from = 1
	}
	if filter.Since < from {
		filter.Since = from
	}
	return filter
}

// fetchRelayLimits reads a relay's NIP-11 document. The library's nip11
// types do not carry max_filters, so the limitations are decoded here too.
func fetchRelayLimits(ctx context.Context, relayURL string) (relayLimits, *nip11.RelayInformationDocument, error) {
	url := nostr.NormalizeURL(relayURL)
	if len(url) < 8 {
		return relayLimits{}, nil, fmt.Errorf("invalid relay url %q", relayURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http"+url[2:], nil)
	if err != nil {
		return relayLimits{}, nil, err
	}
	req.Header.Set("Accept", "application/nostr+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return relayLimits{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return relayLimits{}, nil, fmt.Errorf("NIP-11 request answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return relayLimits{}, nil, err
	}
	var raw struct {
		Limitation struct {
			MaxLimit         int `json:"max_limit"`
			MaxSubscriptions int `json:"max_subscriptions"`
			MaxFilters       int `json:"max_filters"`
		} `json:"limitation"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return relayLimits{}, nil, fmt.Errorf("invalid NIP-11 document: %w", err)
	}
	info := nip11.RelayInformationDocument{URL: url}
	if err := json.Unmarshal(body, &info); err != nil {
		return relayLimits{}, nil, fmt.Errorf("invalid NIP-11 document: %w", err)
	}
	limits := relayLimits{
		MaxLimit:         max(raw.Limitation.MaxLimit, 0),
		MaxSubscriptions: max(raw.Limitation.MaxSubscriptions, 0),
		MaxFilters:       max(raw.Limitation.MaxFilters, 0),
	}
	return limits, &info, nil
}

// loadRelayLimits fetches mr's NIP-11 limitations once for the connection
// that started it (ready identifies it) and caches the document.
func (p *RelayPool) loadRelayLimits(mr *managedRelay, ready chan struct{}) {
	defer close(ready)
	ctx, cancel := context.WithTimeout(p.ctx, p.relayInfoTimeout)
	defer cancel()
	limits, info, err := p.fetchRelayLimits(ctx, mr.url)
	if err != nil {
		p.logger.Debug("relay NIP-11 document unavailable; enforcing no limits", zap.String("relay", mr.url), zap.Error(err))
		return
	}
	mr.mu.Lock()
	if mr.limitsReady == ready {
		mr.limits = limits
	}
	mr.mu.Unlock()
	if info != nil {
		p.mu.Lock()
		p.relayInfoCache[mr.url] = info
		p.mu.Unlock()
	}
	if limits != (relayLimits{}) {
		p.logger.Debug("relay NIP-11 limits",
			zap.String("relay", mr.url),
			zap.Int("max_limit", limits.MaxLimit),
			zap.Int("max_subscriptions", limits.MaxSubscriptions),
			zap.Int("max_filters", limits.MaxFilters))
	}
}

// awaitRelayLimits returns mr's limitations for its current connection,
// waiting for the connect-time NIP-11 fetch (itself bounded by
// relayInfoTimeout) unless ctx ends first.
func (p *RelayPool) awaitRelayLimits(ctx context.Context, mr *managedRelay) relayLimits {
	mr.mu.Lock()
	ready := mr.limitsReady
	mr.mu.Unlock()
	if ready != nil {
		select {
		case <-ready:
		case <-ctx.Done():
		}
	}
	mr.mu.Lock()
	defer mr.mu.Unlock()
	return mr.limits
}

// tryAcquireSubscriptionSlot takes one of mr's NIP-11 max_subscriptions slots
// for a REQ, without waiting.
func (p *RelayPool) tryAcquireSubscriptionSlot(mr *managedRelay) (func(), bool) {
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if limit := mr.limits.MaxSubscriptions; limit > 0 && mr.openREQs >= limit {
		return nil, false
	}
	mr.openREQs++
	return p.slotRelease(mr), true
}

// acquireSubscriptionSlot takes a slot, waiting for one to free up (onWait is
// told the limit once) until ctx ends.
func (p *RelayPool) acquireSubscriptionSlot(ctx context.Context, mr *managedRelay, onWait func(int)) (func(), error) {
	waited := false
	for {
		mr.mu.Lock()
		limit := mr.limits.MaxSubscriptions
		if limit <= 0 || mr.openREQs < limit {
			mr.openREQs++
			mr.mu.Unlock()
			return p.slotRelease(mr), nil
		}
		if mr.slotFreed == nil {
			mr.slotFreed = make(chan struct{})
		}
		freed := mr.slotFreed
		mr.mu.Unlock()
		if !waited && onWait != nil {
			waited = true
			onWait(limit)
		}
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (p *RelayPool) slotRelease(mr *managedRelay) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			mr.mu.Lock()
			defer mr.mu.Unlock()
			if mr.openREQs > 0 {
				mr.openREQs--
			}
			if mr.slotFreed != nil {
				close(mr.slotFreed)
				mr.slotFreed = nil
			}
		})
	}
}

func (p *RelayPool) recordRelayReREQ(relayURL string) {
	if p.health == nil {
		return
	}
	p.health.GetOrCreate(relayURL).RecordReREQ()
}
