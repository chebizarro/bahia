// Package nostr provides Nostr relay integration for publishing and subscribing to events.
package nostr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
	"go.uber.org/zap"
)

// RelayPool manages persistent connections to a set of Nostr relays.
// It provides automatic reconnection and shared access across publishers and clients.
type RelayPool struct {
	mu             sync.RWMutex
	relays         map[string]*managedRelay
	relayInfoCache map[string]*nip11.RelayInformationDocument // NIP-11 info cache
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

// NewRelayPool creates a relay pool for the given URLs.
// Call Connect() to establish connections and Close() when done.
func NewRelayPool(urls []string, logger *zap.Logger, opts ...RelayPoolOption) *RelayPool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &RelayPool{
		relays:         make(map[string]*managedRelay),
		relayInfoCache: make(map[string]*nip11.RelayInformationDocument),
		urls:           urls,
		logger:         logger,
		ctx:            ctx,
		cancel:         cancel,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Connect establishes connections to all configured relays.
// Failed connections are logged but not fatal; they will be retried on use.
func (p *RelayPool) Connect(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, url := range p.urls {
		mr := &managedRelay{url: url}
		p.relays[url] = mr
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
		p.logger.Warn("failed to connect to relay", zap.String("relay", mr.url), zap.Error(err))
		return
	}

	mr.relay = relay
	mr.connected = true
	mr.lastErr = nil
	p.logger.Debug("connected to relay", zap.String("relay", mr.url))
}

// Publish publishes an event to all connected relays.
// Returns the number of successful publications and any errors.
func (p *RelayPool) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	published := 0
	var lastErr error

	for _, mr := range p.relays {
		if err := p.publishToRelay(ctx, mr, ev); err != nil {
			lastErr = err
			continue
		}
		published++
	}

	if published == 0 && lastErr != nil {
		return 0, fmt.Errorf("failed to publish to any relay: %w", lastErr)
	}
	return published, nil
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
	return strings.HasPrefix(reason, "auth-required:")
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

func (p *RelayPool) publishToRelay(ctx context.Context, mr *managedRelay, ev nostr.Event) error {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	// Reconnect if needed.
	if !mr.connected || mr.relay == nil {
		connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		opts := p.buildRelayOptions(mr.url)
		relay, err := nostr.RelayConnect(connectCtx, mr.url, opts)
		if err != nil {
			mr.connected = false
			mr.lastErr = err
			return fmt.Errorf("reconnecting to %s: %w", mr.url, err)
		}
		mr.relay = relay
		mr.connected = true
		mr.lastErr = nil
	}

	err := mr.relay.Publish(ctx, ev)
	if err != nil {
		errStr := err.Error()

		// Check if this is an OK=false response (rejection) vs transport error.
		// go-nostr returns errors starting with "msg:" for OK=false.
		if strings.HasPrefix(errStr, "msg:") {
			reason := strings.TrimPrefix(errStr, "msg: ")

			// Log different rejection types at appropriate levels.
			if strings.HasPrefix(reason, "auth-required:") {
				p.logger.Warn("relay requires authentication",
					zap.String("relay", mr.url),
					zap.String("event_id", ev.ID),
					zap.String("reason", reason),
				)
				// Don't mark as disconnected - auth issues are expected.
				return fmt.Errorf("relay %s rejected event (auth-required): %s", mr.url, reason)
			} else if strings.HasPrefix(reason, "rate-limited:") {
				p.logger.Warn("relay rate-limited publish",
					zap.String("relay", mr.url),
					zap.String("event_id", ev.ID),
					zap.String("reason", reason),
				)
				// Don't mark as disconnected - rate limiting is temporary.
				return fmt.Errorf("relay %s rejected event (rate-limited): %s", mr.url, reason)
			} else if strings.HasPrefix(reason, "blocked:") {
				p.logger.Warn("relay blocked event",
					zap.String("relay", mr.url),
					zap.String("event_id", ev.ID),
					zap.String("reason", reason),
				)
				// Don't mark as disconnected - policy blocks are expected.
				return fmt.Errorf("relay %s rejected event (blocked): %s", mr.url, reason)
			} else if strings.HasPrefix(reason, "duplicate:") {
				// Duplicate is not an error - relay already has the event.
				p.logger.Debug("relay already has event",
					zap.String("relay", mr.url),
					zap.String("event_id", ev.ID),
				)
				return nil // Consider duplicate as success.
			} else {
				// Unknown rejection reason.
				p.logger.Warn("relay rejected event",
					zap.String("relay", mr.url),
					zap.String("event_id", ev.ID),
					zap.String("reason", reason),
				)
				return fmt.Errorf("relay %s rejected event: %s", mr.url, reason)
			}
		}

		// Transport/connection error - mark as disconnected.
		mr.connected = false
		mr.lastErr = err
		p.logger.Warn("publish failed (transport error), marking relay disconnected",
			zap.String("relay", mr.url),
			zap.String("event_id", ev.ID),
			zap.Error(err),
		)
		return fmt.Errorf("publishing to %s: %w", mr.url, err)
	}

	// Success - relay accepted the event.
	p.logger.Debug("event accepted by relay",
		zap.String("relay", mr.url),
		zap.String("event_id", ev.ID),
	)
	return nil
}

// Subscribe creates a subscription on the first available relay.
// It attempts each relay in order and returns the first successful subscription.
func (p *RelayPool) Subscribe(ctx context.Context, filters []nostr.Filter) (*nostr.Subscription, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, mr := range p.relays {
		mr.mu.Lock()
		if !mr.connected || mr.relay == nil {
			connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			opts := p.buildRelayOptions(mr.url)
			relay, err := nostr.RelayConnect(connectCtx, mr.url, opts)
			cancel()
			if err != nil {
				mr.connected = false
				mr.lastErr = err
				mr.mu.Unlock()
				continue
			}
			mr.relay = relay
			mr.connected = true
			mr.lastErr = nil
		}

		sub, err := mr.relay.Subscribe(ctx, filters)
		mr.mu.Unlock()
		if err != nil {
			p.logger.Warn("subscription failed", zap.String("relay", mr.url), zap.Error(err))
			continue
		}
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
	p.mu.RLock()
	defer p.mu.RUnlock()

	subCtx, cancel := context.WithCancel(ctx)
	subs := make([]relaySubscription, 0, len(p.relays))

	for _, mr := range p.relays {
		mr.mu.Lock()
		if !mr.connected || mr.relay == nil {
			connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			opts := p.buildRelayOptions(mr.url)
			relay, err := nostr.RelayConnect(connectCtx, mr.url, opts)
			cancel()
			if err != nil {
				mr.mu.Unlock()
				continue
			}
			mr.relay = relay
			mr.connected = true
		}

		sub, err := mr.relay.Subscribe(subCtx, filters)
		mr.mu.Unlock()
		if err != nil {
			continue
		}

		subs = append(subs, relaySubscription{relayURL: mr.url, sub: sub})
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
			var eventsCh <-chan *nostr.Event
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
						// available. If go-nostr has marked the subscription context as relay CLOSED,
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
					select {
					case merged <- ev:
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

// URLs returns the list of configured relay URLs.
func (p *RelayPool) URLs() []string {
	return p.urls
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

	for _, url := range p.urls {
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
func (p *RelayPool) buildRelayOptions(relayURL string) nostr.RelayOption {
	return nostr.WithNoticeHandler(func(notice string) {
		p.logger.Info("relay notice",
			zap.String("relay", relayURL),
			zap.String("notice", notice),
		)
	})
}

// AuthenticateRelay sends a NIP-42 AUTH response to a specific relay.
// Call this after receiving an auth-required error (PublishResult.IsAuthRequired()).
// Returns an error if no private key is configured or auth fails.
func (p *RelayPool) AuthenticateRelay(ctx context.Context, relayURL string) error {
	if p.privateKey == "" {
		return fmt.Errorf("no private key configured for NIP-42 AUTH")
	}

	p.mu.RLock()
	mr, exists := p.relays[relayURL]
	p.mu.RUnlock()

	if !exists {
		return fmt.Errorf("relay not found: %s", relayURL)
	}

	mr.mu.Lock()
	relay := mr.relay
	mr.mu.Unlock()

	if relay == nil {
		return fmt.Errorf("relay not connected: %s", relayURL)
	}

	p.logger.Info("sending NIP-42 AUTH", zap.String("relay", relayURL))

	err := relay.Auth(ctx, func(event *nostr.Event) error {
		return event.Sign(p.privateKey)
	})

	if err != nil {
		p.logger.Error("NIP-42 AUTH failed",
			zap.String("relay", relayURL),
			zap.Error(err),
		)
		return err
	}

	p.logger.Info("NIP-42 AUTH completed", zap.String("relay", relayURL))
	return nil
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
		mr.mu.Unlock()
	}
}
