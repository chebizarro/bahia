// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip11"
	"go.uber.org/zap"
)

// RelayPool manages persistent connections to a set of Nostr relays.
// It provides automatic reconnection and shared access across publishers and clients.
type RelayPool struct {
	mu             sync.RWMutex
	relays         map[string]*managedRelay
	relayInfoCache map[string]*nip11.RelayInformationDocument // NIP-11 info cache
	health         *RelayHealthTracker
	urls           []string
	logger         *zap.Logger
	ctx            context.Context
	cancel         context.CancelFunc
	privateKey     string // hex-encoded private key for NIP-42 AUTH (optional)
}

type managedRelay struct {
	url       string
	relay     *nostr.Relay
	connected bool
	lastErr   error
	mu        sync.Mutex
}

// RelayPoolOption configures a RelayPool.
type RelayPoolOption func(*RelayPool)

// WithPrivateKey sets the private key for NIP-42 AUTH.
// The key should be hex-encoded. When set, AuthenticateRelay() can be called
// to respond to auth-required errors detected via PublishResult.IsAuthRequired().
func WithPrivateKey(privateKeyHex string) RelayPoolOption {
	return func(p *RelayPool) { p.privateKey = privateKeyHex }
}

// RelayPoolReconfigureResult describes an in-place relay topology update.
type RelayPoolReconfigureResult struct {
	Changed      bool
	PreviousURLs []string
	CurrentURLs  []string
	AddedURLs    []string
	RemovedURLs  []string
}

// NewRelayPool creates a relay pool for the given URLs.
// Call Connect() to establish connections and Close() when done.
func NewRelayPool(urls []string, logger *zap.Logger, opts ...RelayPoolOption) *RelayPool {
	normalizedURLs := normalizeRelayURLs(urls)
	ctx, cancel := context.WithCancel(context.Background())
	p := &RelayPool{
		relays:         make(map[string]*managedRelay),
		relayInfoCache: make(map[string]*nip11.RelayInformationDocument),
		health:         NewRelayHealthTracker(),
		urls:           normalizedURLs,
		logger:         logger,
		ctx:            ctx,
		cancel:         cancel,
	}
	for _, url := range normalizedURLs {
		p.health.GetOrCreate(url)
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ReconfigureRelayURLs atomically replaces the pool's configured relay topology in place.
// Existing RelayPool pointers remain valid. URLs are normalized and deduplicated with
// the same rules as NewRelayPool. If the normalized URL list is unchanged, the call is a
// no-op and existing relay connections are preserved. If the set is unchanged but order
// differs, only the configured order is updated and existing relay entries are retained.
// When the set changes, retained relay entries keep their connection state, removed relay
// entries are closed and marked disconnected, and added relay entries are created
// disconnected for the existing publish/subscribe paths to connect on use or on the next
// Connect call.
func (p *RelayPool) ReconfigureRelayURLs(urls []string) RelayPoolReconfigureResult {
	nextURLs := normalizeRelayURLs(urls)

	p.mu.Lock()
	defer p.mu.Unlock()

	previousURLs := cloneRelayURLs(p.urls)
	if sameRelayURLOrder(previousURLs, nextURLs) {
		return RelayPoolReconfigureResult{
			Changed:      false,
			PreviousURLs: previousURLs,
			CurrentURLs:  cloneRelayURLs(p.urls),
		}
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
		mr, exists := p.relays[url]
		if !exists {
			continue
		}
		mr.mu.Lock()
		if mr.relay != nil {
			mr.relay.Close()
		}
		mr.connected = false
		mr.lastErr = nil
		p.recordRelayConnectionState(mr.url, false)
		mr.mu.Unlock()
		delete(p.relays, url)
	}

	for _, url := range nextURLs {
		p.health.GetOrCreate(url)
		if _, exists := p.relays[url]; !exists {
			p.relays[url] = &managedRelay{url: url}
		}
	}
	p.urls = nextURLs

	return RelayPoolReconfigureResult{
		Changed:      true,
		PreviousURLs: previousURLs,
		CurrentURLs:  cloneRelayURLs(p.urls),
		AddedURLs:    addedURLs,
		RemovedURLs:  removedURLs,
	}
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

// Connect establishes connections to all configured relays.
// Failed connections are logged but not fatal; they will be retried on use.
func (p *RelayPool) Connect(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, url := range p.urls {
		mr, exists := p.relays[url]
		if !exists {
			mr = &managedRelay{url: url}
			p.relays[url] = mr
		}
		p.connectOne(ctx, mr)
	}
}

func (p *RelayPool) connectOne(ctx context.Context, mr *managedRelay) {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	if mr.connected && mr.relay != nil {
		return
	}

	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Build relay options with AUTH handler if private key is configured.
	opts := p.buildRelayOptions(mr.url)

	relay, err := nostr.RelayConnect(connectCtx, mr.url, opts)
	if err != nil {
		mr.connected = false
		mr.lastErr = err
		p.recordRelayConnectionState(mr.url, false)
		p.recordRelayError(mr.url, err.Error())
		p.logger.Warn("failed to connect to relay", zap.String("relay", mr.url), zap.Error(err))
		return
	}

	mr.relay = relay
	mr.connected = true
	mr.lastErr = nil
	p.recordRelayConnectionState(mr.url, true)
	p.logger.Debug("connected to relay", zap.String("relay", mr.url))
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
func (p *RelayPool) PublishWithResults(ctx context.Context, ev nostr.Event) ([]PublishResult, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	results := make([]PublishResult, 0, len(p.relays))
	for _, mr := range p.orderedRelaysLocked() {
		results = append(results, p.publishToRelayWithResult(ctx, mr, ev))
	}

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

func subscribeAuthRequired(err error) bool {
	_, ok := subscribeAuthRequiredReason(err)
	return ok
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
	mr.mu.Lock()
	defer mr.mu.Unlock()

	result := PublishResult{RelayURL: mr.url}

	// Reconnect if needed.
	if !mr.connected || mr.relay == nil {
		p.recordRelayReconnect(mr.url)
		connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		opts := p.buildRelayOptions(mr.url)
		relay, err := nostr.RelayConnect(connectCtx, mr.url, opts)
		if err != nil {
			mr.connected = false
			mr.lastErr = err
			p.recordRelayConnectionState(mr.url, false)
			p.recordRelayError(mr.url, err.Error())
			result.Error = fmt.Errorf("reconnecting to %s: %w", mr.url, err)
			return result
		}
		mr.relay = relay
		mr.connected = true
		mr.lastErr = nil
		p.recordRelayConnectionState(mr.url, true)
	}

	startedAt := time.Now()
	err := mr.relay.Publish(ctx, ev)
	if err != nil {
		if reason, ok := publishRejectionReason(err); ok {
			if IsAuthRequiredReason(reason) {
				if authErr := p.authenticateManagedRelayLocked(ctx, mr); authErr == nil {
					err = mr.relay.Publish(ctx, ev)
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
		mr.connected = false
		mr.lastErr = err
		p.recordRelayConnectionState(mr.url, false)
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

// Subscribe creates a subscription on the first available relay.
// It attempts each relay in order and returns the first successful subscription.
func (p *RelayPool) Subscribe(ctx context.Context, filters []nostr.Filter) (*nostr.Subscription, error) {
	if len(filters) != 1 {
		return nil, fmt.Errorf("single-relay Subscribe requires exactly one filter, got %d; use SubscribeAllWithEOSE for multi-filter coverage", len(filters))
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, mr := range p.relays {
		mr.mu.Lock()
		if !mr.connected || mr.relay == nil {
			p.recordRelayReconnect(mr.url)
			connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			opts := p.buildRelayOptions(mr.url)
			relay, err := nostr.RelayConnect(connectCtx, mr.url, opts)
			cancel()
			if err != nil {
				mr.connected = false
				mr.lastErr = err
				p.recordRelayConnectionState(mr.url, false)
				p.recordRelayError(mr.url, err.Error())
				mr.mu.Unlock()
				continue
			}
			mr.relay = relay
			mr.connected = true
			mr.lastErr = nil
			p.recordRelayConnectionState(mr.url, true)
		}

		filter := filters[0]
		sub, err := subscribeOnRelay(mr.relay, ctx, filter)
		recordedAuthUnavailable := false
		if reason, authRequired := subscribeAuthRequiredReason(err); authRequired {
			if authErr := p.authenticateManagedRelayLocked(ctx, mr); authErr == nil {
				sub, err = subscribeOnRelay(mr.relay, ctx, filter)
			} else {
				p.recordRelayError(mr.url, authUnavailableMetadata(reason, authErr))
				recordedAuthUnavailable = true
			}
		}
		if err != nil && !recordedAuthUnavailable {
			p.recordRelayError(mr.url, err.Error())
		}
		mr.mu.Unlock()
		if err != nil {
			p.logger.Warn("subscription failed", zap.String("relay", mr.url), zap.Error(err))
			continue
		}
		p.recordRelayConnectionState(mr.url, true)
		return sub, nil
	}

	return nil, fmt.Errorf("no relays available for subscription")
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
	// EndOfStoredEvents is closed when all relays have sent EOSE.
	EndOfStoredEvents <-chan struct{}
	// RelayEOSE emits once for each relay subscription that sends EOSE.
	RelayEOSE <-chan RelayEOSE
	// Closed emits relay CLOSED reasons for each relay subscription that reports one.
	Closed <-chan RelayClosed

	closeFn func()
}

// Close cancels all relay subscriptions represented by the merged subscription.
func (m *MergedSubscription) Close() {
	if m == nil || m.closeFn == nil {
		return
	}
	m.closeFn()
}

type relaySubscription struct {
	relayURL string
	sub      *nostr.Subscription
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
	defer p.mu.RUnlock()

	subCtx, cancel := context.WithCancel(ctx)
	subs := make([]relaySubscription, 0, len(p.relays))

	for _, mr := range p.relays {
		mr.mu.Lock()
		if !mr.connected || mr.relay == nil {
			p.recordRelayReconnect(mr.url)
			connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			opts := p.buildRelayOptions(mr.url)
			relay, err := nostr.RelayConnect(connectCtx, mr.url, opts)
			cancel()
			if err != nil {
				mr.connected = false
				mr.lastErr = err
				p.recordRelayConnectionState(mr.url, false)
				p.recordRelayError(mr.url, err.Error())
				mr.mu.Unlock()
				continue
			}
			mr.relay = relay
			mr.connected = true
			mr.lastErr = nil
			p.recordRelayConnectionState(mr.url, true)
		}

		relaySubs := make([]*nostr.Subscription, 0, len(filters))
		var lastErr error
		relayComplete := true
		for _, filter := range filters {
			sub, err := subscribeOnRelay(mr.relay, subCtx, filter)
			recordedAuthUnavailable := false
			if reason, authRequired := subscribeAuthRequiredReason(err); authRequired {
				if authErr := p.authenticateManagedRelayLocked(ctx, mr); authErr == nil {
					sub, err = subscribeOnRelay(mr.relay, subCtx, filter)
				} else {
					p.recordRelayError(mr.url, authUnavailableMetadata(reason, authErr))
					recordedAuthUnavailable = true
				}
			}
			if err != nil {
				lastErr = err
				relayComplete = false
				if !recordedAuthUnavailable {
					p.recordRelayError(mr.url, err.Error())
				}
				break
			}
			relaySubs = append(relaySubs, sub)
		}
		mr.mu.Unlock()
		if !relayComplete || len(relaySubs) != len(filters) {
			for _, sub := range relaySubs {
				if sub != nil {
					sub.Unsub()
				}
			}
			if lastErr != nil {
				p.logger.Warn("subscription failed", zap.String("relay", mr.url), zap.Error(lastErr))
			}
			continue
		}
		p.recordRelayConnectionState(mr.url, true)

		for _, sub := range relaySubs {
			subs = append(subs, relaySubscription{relayURL: mr.url, sub: sub})
		}
	}

	if len(subs) == 0 {
		cancel()
		return nil, fmt.Errorf("no relays available for subscription")
	}

	merged := mergeRelaySubscriptions(ctx, subs, 64)
	merged.closeFn = cancel
	return merged, nil
}

func mergeSubscriptions(ctx context.Context, subs []*nostr.Subscription, buffer int) *MergedSubscription {
	wrapped := make([]relaySubscription, 0, len(subs))
	for _, sub := range subs {
		wrapped = append(wrapped, relaySubscription{relayURL: relayURLForSubscription(sub), sub: sub})
	}
	return mergeRelaySubscriptions(ctx, wrapped, buffer)
}

func mergeRelaySubscriptions(ctx context.Context, subs []relaySubscription, buffer int) *MergedSubscription {
	merged := make(chan *nostr.Event, buffer)
	eoseChan := make(chan struct{})
	relayEOSE := make(chan RelayEOSE, len(subs))
	closed := make(chan RelayClosed, len(subs))
	if len(subs) == 0 {
		close(merged)
		close(eoseChan)
		close(relayEOSE)
		close(closed)
		return &MergedSubscription{Events: merged, EndOfStoredEvents: eoseChan, RelayEOSE: relayEOSE, Closed: closed}
	}

	var eventsWg sync.WaitGroup
	var eoseCount atomic.Int32
	var closeEOSE sync.Once

	eventsWg.Add(len(subs))
	for _, relaySub := range subs {
		go func(rs relaySubscription) {
			defer eventsWg.Done()
			s := rs.sub
			var eoseCh <-chan struct{}
			var eventsCh <-chan nostr.Event
			var closedCh <-chan string
			if s != nil {
				eoseCh = s.EndOfStoredEvents
				eventsCh = s.Events
				closedCh = s.ClosedReason
			}
			eoseSent := false
			markEOSE := func() {
				if eoseSent {
					return
				}
				eoseSent = true
				info := RelayEOSE{RelayURL: rs.relayURL, SubscriptionID: subscriptionID(s)}
				select {
				case relayEOSE <- info:
				case <-ctx.Done():
					return
				}
				if eoseCount.Add(1) == int32(len(subs)) {
					closeEOSE.Do(func() { close(eoseChan) })
				}
			}

			for eoseCh != nil || eventsCh != nil || closedCh != nil {
				if closedCh != nil {
					select {
					case reason, ok := <-closedCh:
						if ok {
							emitRelayClosed(ctx, closed, RelayClosed{RelayURL: rs.relayURL, SubscriptionID: subscriptionID(s), Reason: reason})
						}
						closedCh = nil
						continue
					default:
					}
				}

				select {
				case <-ctx.Done():
					return
				case _, ok := <-eoseCh:
					if ok || eoseCh != nil {
						markEOSE()
					}
					eoseCh = nil
				case reason, ok := <-closedCh:
					if ok {
						emitRelayClosed(ctx, closed, RelayClosed{RelayURL: rs.relayURL, SubscriptionID: subscriptionID(s), Reason: reason})
					}
					closedCh = nil
				case ev, ok := <-eventsCh:
					if !ok {
						// The upstream subscription is over. Drain a CLOSED reason if it is already
						// available. If the relay library has marked the subscription context as relay CLOSED,
						// keep waiting for the protocol reason instead of racing channel ordering.
						if closedCh != nil {
							select {
							case reason, ok := <-closedCh:
								if ok {
									emitRelayClosed(ctx, closed, RelayClosed{RelayURL: rs.relayURL, SubscriptionID: subscriptionID(s), Reason: reason})
								}
								return
							default:
								if subscriptionEndedByRelayClosed(s) {
									eventsCh = nil
									continue
								}
							}
						}
						return
					}
					event := ev
					select {
					case merged <- &event:
					case <-ctx.Done():
						return
					}
				}
			}
		}(relaySub)
	}

	go func() {
		eventsWg.Wait()
		close(merged)
		close(relayEOSE)
		close(closed)
	}()

	return &MergedSubscription{Events: merged, EndOfStoredEvents: eoseChan, RelayEOSE: relayEOSE, Closed: closed}
}

func emitRelayClosed(ctx context.Context, closed chan<- RelayClosed, info RelayClosed) bool {
	select {
	case closed <- info:
		return true
	case <-ctx.Done():
		return false
	}
}

func subscriptionEndedByRelayClosed(sub *nostr.Subscription) bool {
	if sub == nil || sub.Context == nil {
		return false
	}
	cause := context.Cause(sub.Context)
	return cause != nil && strings.Contains(cause.Error(), "CLOSED received")
}

func relayURLForSubscription(sub *nostr.Subscription) string {
	if sub == nil || sub.Relay == nil {
		return ""
	}
	return sub.Relay.URL
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
	URL       string
	Connected bool
	Healthy   bool
	LastSeen  time.Time
	Errors    int
	LastError string
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
			status.LastSeen = stats.LastConnected
			status.Errors = int(stats.ErrorCount)
			status.LastError = stats.LastError
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
	p.health.GetOrCreate(relayURL).SetConnected(connected)
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
	if p.privateKey == "" {
		return fmt.Errorf("no private key configured for NIP-42 AUTH")
	}
	if mr == nil || mr.relay == nil {
		return fmt.Errorf("relay not connected: %s", mr.url)
	}

	p.logger.Info("sending NIP-42 AUTH", zap.String("relay", mr.url))
	if err := mr.relay.Auth(ctx, func(_ context.Context, event *nostr.Event) error {
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
	if p.privateKey == "" {
		return fmt.Errorf("no private key configured for NIP-42 AUTH")
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

// Close disconnects all relays and stops reconnection.
func (p *RelayPool) Close() {
	p.cancel()

	p.mu.Lock()
	defer p.mu.Unlock()

	for _, mr := range p.relays {
		mr.mu.Lock()
		if mr.relay != nil {
			mr.relay.Close()
		}
		mr.connected = false
		p.recordRelayConnectionState(mr.url, false)
		mr.mu.Unlock()
	}
}
