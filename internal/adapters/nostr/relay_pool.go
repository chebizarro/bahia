// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"context"
	"fmt"
	"math"
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
)

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
		relays:              make(map[string]*managedRelay),
		retiredRelays:       make(map[string]*managedRelay),
		activeSubscriptions: make(map[uint64]*activeMergedSubscription),
		relayInfoCache:      make(map[string]*nip11.RelayInformationDocument),
		health:              NewRelayHealthTracker(),
		urls:                normalizedURLs,
		logger:              logger,
		ctx:                 ctx,
		cancel:              cancel,
		connectRelay:        nostr.RelayConnect,
		now:                 time.Now,
		newReconnectBackoff: defaultReconnectBackoff,
		connectTimeout:      defaultRelayConnectTimeout,
		reconnectTimeout:    defaultRelayReconnectTimeout,
	}
	for _, url := range normalizedURLs {
		p.health.GetOrCreate(url)
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
			mr.mu.Unlock()
			return relay, nil
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

func subscribeAuthRequiredReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "", false
	}
	lower := strings.ToLower(message)
	idx := strings.Index(lower, "auth-required")
	if idx < 0 {
		return "", false
	}
	reason := strings.TrimSpace(message[idx:])
	if !IsAuthRequiredReason(reason) {
		return "", false
	}
	return reason, true
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
				mr.mu.Lock()
				sameRelay := mr.connected && mr.relay == relay
				authErr := fmt.Errorf("relay connection changed before AUTH retry")
				if sameRelay {
					authErr = p.authenticateManagedRelayLocked(ctx, mr)
				}
				mr.mu.Unlock()
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

type relaySubscription struct {
	relayURL string
	sub      *nostr.Subscription
	cancel   context.CancelFunc
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
	if len(filters) == 0 {
		return nil, fmt.Errorf("at least one subscription filter is required")
	}

	p.mu.RLock()
	relays := p.orderedRelaysLocked()
	p.mu.RUnlock()

	subCtx, cancel := context.WithCancel(ctx)
	subs := make([]relaySubscription, 0, len(relays))

	for _, mr := range relays {
		relaySubs, err := p.subscribeInitialManagedRelay(ctx, subCtx, mr, filters)
		if err != nil {
			p.logger.Warn("subscription failed", zap.String("relay", mr.url), zap.Error(err))
			continue
		}
		subs = append(subs, relaySubs...)
	}

	if len(subs) == 0 {
		cancel()
		return nil, fmt.Errorf("no relays available for subscription")
	}

	return p.newActiveMergedSubscription(subCtx, cancel, filters, subs), nil
}

type activeRelayGroup struct {
	cancel    context.CancelFunc
	remaining int
}

type activeMergedSubscription struct {
	pool         *RelayPool
	id           uint64
	ctx          context.Context
	cancel       context.CancelFunc
	filters      []nostr.Filter
	events       chan *nostr.Event
	eose         chan struct{}
	relayEOSE    chan RelayEOSE
	closed       chan RelayClosed
	eventSources *sync.Map
	dedup        *EventDeduplicator

	mu                 sync.Mutex
	groups             map[string]*activeRelayGroup
	initialRemaining   int
	initialWithoutEOSE bool
	initialPending     map[string]int
	realEOSECount      int
	eoseOnce           sync.Once
	workers            sync.WaitGroup
	closeOnce          sync.Once
}

func (p *RelayPool) newActiveMergedSubscription(ctx context.Context, cancel context.CancelFunc, filters []nostr.Filter, subs []relaySubscription) *MergedSubscription {
	state := &activeMergedSubscription{
		pool:             p,
		ctx:              ctx,
		cancel:           cancel,
		filters:          append([]nostr.Filter(nil), filters...),
		events:           make(chan *nostr.Event, 64),
		eose:             make(chan struct{}),
		relayEOSE:        make(chan RelayEOSE, 64),
		closed:           make(chan RelayClosed, 64),
		eventSources:     &sync.Map{},
		dedup:            NewEventDeduplicator(10000),
		groups:           make(map[string]*activeRelayGroup),
		initialRemaining: len(subs),
		initialPending:   make(map[string]int),
	}

	p.subscriptionsMu.Lock()
	p.nextSubscriptionID++
	state.id = p.nextSubscriptionID
	p.activeSubscriptions[state.id] = state
	p.subscriptionsMu.Unlock()

	state.mu.Lock()
	for _, relaySub := range subs {
		state.initialPending[relaySub.relayURL]++
		group := state.groups[relaySub.relayURL]
		if group == nil {
			group = &activeRelayGroup{cancel: relaySub.cancel}
			state.groups[relaySub.relayURL] = group
		}
		group.remaining++
		state.startWorkerLocked(relaySub, group, true)
	}
	state.mu.Unlock()

	merged := &MergedSubscription{
		Events:            state.events,
		EndOfStoredEvents: state.eose,
		RelayEOSE:         state.relayEOSE,
		Closed:            state.closed,
		closeFn:           state.close,
		eventSources:      state.eventSources,
		active:            state,
	}
	go state.finish()
	return merged
}

func (s *activeMergedSubscription) startWorkerLocked(relaySub relaySubscription, group *activeRelayGroup, initial bool) {
	s.workers.Add(1)
	go s.runRelaySubscription(relaySub, group, initial)
}

func (s *activeMergedSubscription) runRelaySubscription(relaySub relaySubscription, group *activeRelayGroup, initial bool) {
	defer s.workers.Done()
	defer s.workerDone(relaySub.relayURL, group)

	sub := relaySub.sub
	var eoseCh <-chan nostr.EndOfStoredEvent
	var eventsCh <-chan nostr.Event
	var closedCh <-chan string
	if sub != nil {
		eoseCh = sub.EndOfStoredEvents
		eventsCh = sub.Events
		closedCh = sub.ClosedReason
	}
	terminal := false
	markTerminal := func(realEOSE bool) {
		if terminal {
			return
		}
		terminal = true
		if realEOSE {
			s.markRealEOSE()
			info := RelayEOSE{RelayURL: relaySub.relayURL, SubscriptionID: subscriptionID(sub)}
			select {
			case s.relayEOSE <- info:
			case <-s.ctx.Done():
			}
		}
		if initial {
			s.markInitialTerminal(relaySub.relayURL, realEOSE)
		}
	}
	defer markTerminal(false)
	forward := func(event nostr.Event) bool {
		eventID := event.ID.Hex()
		if eventID != "" && s.dedup.IsDuplicate(eventID) {
			return true
		}
		s.eventSources.LoadOrStore(eventID, relaySub.relayURL)
		select {
		case s.events <- &event:
			return true
		case <-s.ctx.Done():
			return false
		}
	}

	for eoseCh != nil || eventsCh != nil || closedCh != nil {
		select {
		case <-s.ctx.Done():
			return
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
							return
						}
					default:
						break drain
					}
				}
				markTerminal(true)
			}
			eoseCh = nil
		case reason, ok := <-closedCh:
			if ok {
				emitRelayClosed(s.ctx, s.closed, RelayClosed{RelayURL: relaySub.relayURL, SubscriptionID: subscriptionID(sub), Reason: reason})
			}
			closedCh = nil
		case ev, ok := <-eventsCh:
			if !ok {
				if closedCh != nil {
					select {
					case reason, ok := <-closedCh:
						if ok {
							emitRelayClosed(s.ctx, s.closed, RelayClosed{RelayURL: relaySub.relayURL, SubscriptionID: subscriptionID(sub), Reason: reason})
						}
					default:
					}
				}
				return
			}
			if !forward(ev) {
				return
			}
		}
	}
}

func (s *activeMergedSubscription) markInitialTerminal(relayURL string, realEOSE bool) {
	s.mu.Lock()
	if !realEOSE {
		s.initialWithoutEOSE = true
	}
	if s.initialRemaining > 0 {
		s.initialRemaining--
	}
	if remaining := s.initialPending[relayURL]; remaining <= 1 {
		delete(s.initialPending, relayURL)
	} else {
		s.initialPending[relayURL] = remaining - 1
	}
	complete := s.initialRemaining == 0
	s.mu.Unlock()
	if complete {
		s.eoseOnce.Do(func() { close(s.eose) })
	}
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

func (s *activeMergedSubscription) markRealEOSE() {
	s.mu.Lock()
	s.realEOSECount++
	s.mu.Unlock()
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

	relayCtx, relayCancel := context.WithCancel(s.ctx)
	subs, err := s.pool.subscribeConnectedRelay(ctx, relayCtx, relay, filters)
	if err != nil {
		relayCancel()
		return err
	}

	s.mu.Lock()
	if _, exists := s.groups[relay.url]; exists {
		s.mu.Unlock()
		relayCancel()
		return nil
	}
	select {
	case <-s.ctx.Done():
		s.mu.Unlock()
		relayCancel()
		return s.ctx.Err()
	default:
	}
	group := &activeRelayGroup{cancel: relayCancel, remaining: len(subs)}
	s.groups[relay.url] = group
	for _, sub := range subs {
		sub.cancel = relayCancel
		s.startWorkerLocked(sub, group, false)
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
		urls = append(urls, url)
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

func (p *RelayPool) subscribeInitialManagedRelay(authCtx, parentCtx context.Context, mr *managedRelay, filters []nostr.Filter) ([]relaySubscription, error) {
	relayCtx, relayCancel := context.WithCancel(parentCtx)
	keepContext := false
	defer func() {
		if !keepContext {
			relayCancel()
		}
	}()

	if !managedRelayConnected(mr) {
		p.recordRelayReconnect(mr.url)
		p.connectOne(authCtx, mr)
	}
	subs, err := p.subscribeConnectedRelay(authCtx, relayCtx, mr, filters)
	if err != nil {
		return nil, err
	}
	for i := range subs {
		subs[i].cancel = relayCancel
	}
	keepContext = true
	return subs, nil
}

func (p *RelayPool) subscribeConnectedRelay(authCtx, subscriptionCtx context.Context, mr *managedRelay, filters []nostr.Filter) ([]relaySubscription, error) {
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if !mr.connected || mr.relay == nil {
		return nil, fmt.Errorf("relay %s is not connected", mr.url)
	}

	subs := make([]relaySubscription, 0, len(filters))
	for _, filter := range filters {
		sub, err := subscribeOnRelay(mr.relay, subscriptionCtx, filter)
		recordedAuthUnavailable := false
		if reason, authRequired := subscribeAuthRequiredReason(err); authRequired {
			if authErr := p.authenticateManagedRelayLocked(authCtx, mr); authErr == nil {
				sub, err = subscribeOnRelay(mr.relay, subscriptionCtx, filter)
			} else {
				p.recordRelayError(mr.url, authUnavailableMetadata(reason, authErr))
				recordedAuthUnavailable = true
			}
		}
		if err != nil {
			if !recordedAuthUnavailable {
				p.recordRelayError(mr.url, err.Error())
			}
			return nil, err
		}
		subs = append(subs, relaySubscription{relayURL: mr.url, sub: sub})
	}
	p.recordRelayConnectionState(mr.url, true)
	return subs, nil
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

// buildRelayOptions creates RelayOption slice with notice handler.
// Note: NIP-42 AUTH requires manual handling via relay.Auth() when auth-required
// errors are detected in publish results.
func (p *RelayPool) buildRelayOptions(relayURL string) nostr.RelayOptions {
	return nostr.RelayOptions{NoticeHandler: func(_ *nostr.Relay, notice string) {
		p.logger.Info("relay notice",
			zap.String("relay", relayURL),
			zap.String("notice", notice),
		)
	}}
}

func (p *RelayPool) authenticateManagedRelayLocked(ctx context.Context, mr *managedRelay) error {
	if p.privateKey == "" && p.authSigner == nil {
		return fmt.Errorf("no signer configured for NIP-42 AUTH")
	}
	if mr == nil || mr.relay == nil {
		return fmt.Errorf("relay not connected: %s", mr.url)
	}

	p.logger.Info("sending NIP-42 AUTH", zap.String("relay", mr.url))
	if err := mr.relay.Auth(ctx, func(_ context.Context, event *nostr.Event) error {
		if p.authSigner != nil {
			return p.authSigner.SignEvent(ctx, event)
		}
		return signEventWithPrivateKeyHex(event, p.privateKey)
	}); err != nil {
		p.logger.Error("NIP-42 AUTH failed",
			zap.String("relay", mr.url),
			zap.Error(err),
		)
		return err
	}
	p.logger.Info("NIP-42 AUTH completed", zap.String("relay", mr.url))
	return nil
}

// AuthenticateRelay sends a NIP-42 AUTH response to a specific relay.
// Call this after receiving an auth-required error (PublishResult.IsAuthRequired()).
// Returns an error if no private key is configured or auth fails.
func (p *RelayPool) AuthenticateRelay(ctx context.Context, relayURL string) error {
	if p.privateKey == "" && p.authSigner == nil {
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

	return p.authenticateManagedRelayLocked(ctx, &managedRelay{url: relayURL, relay: relay, connected: true})
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
