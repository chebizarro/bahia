package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
)

// RelayPublishResult records one relay's OK outcome for a published event.
// Accepted must correspond to the NIP-01 OK accepted flag; OK=false is not a
// successful publish even when the relay supplied a reason.
type RelayPublishResult struct {
	RelayURL string
	Accepted bool
	Reason   string
	Error    error
}

// RelayBusSubscription is the merged event stream exposed by the SoulFactory
// relay bus. EndOfStoredEvents closes after every active relay has sent EOSE for
// the initial subscription generation; Events remains open for realtime events
// until the caller's context is cancelled.
type RelayBusSubscription struct {
	Events            <-chan *nostr.Event
	EndOfStoredEvents <-chan struct{}
	cancel            context.CancelFunc
}

func (s *RelayBusSubscription) Close() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

type relayAuthSigner interface {
	Sign(context.Context, *nostr.Event) error
}

type relayBusEndpoint interface {
	URL() string
	// Publish sends one EVENT frame. admit is mandatory and is invoked after
	// the endpoint is connected and immediately before the raw frame.
	Publish(ctx context.Context, ev nostr.Event, admit func(context.Context) error) RelayPublishResult
	Subscribe(context.Context, []nostr.Filter) (relayBusRelaySubscription, error)
	Auth(context.Context, relayAuthSigner) error
	Close()
}

type relayBusRelaySubscription interface {
	Events() <-chan *nostr.Event
	EndOfStoredEvents() <-chan struct{}
	ClosedReason() <-chan string
	Close()
}

type relayBusBackoff func(context.Context, int) error

type RelayBusOption func(*SoulFactoryRelayBus)

func WithRelayBusSigner(signer relayAuthSigner) RelayBusOption {
	return func(b *SoulFactoryRelayBus) { b.signer = signer }
}

func WithRelayBusLogger(logger *slog.Logger) RelayBusOption {
	return func(b *SoulFactoryRelayBus) {
		if logger != nil {
			b.logger = logger
		}
	}
}

func WithRelayBusBackoff(backoff relayBusBackoff) RelayBusOption {
	return func(b *SoulFactoryRelayBus) {
		if backoff != nil {
			b.backoff = backoff
		}
	}
}

// defaultRelayBusAdmission resolves the controller for buses that were not
// given one. It is the process-wide controller; tests replace it to isolate
// unrelated cases from each other's budget.
var defaultRelayBusAdmission = nostrout.Default

// WithRelayBusAdmission injects the outbound admission controller. Buses
// default to the process-wide controller; nil keeps that default.
func WithRelayBusAdmission(admission *nostrout.Admission) RelayBusOption {
	return func(b *SoulFactoryRelayBus) {
		if admission != nil {
			b.admission = admission
		}
	}
}

// outbound returns the bus controller, never nil: a bus assembled without one
// is still admitted by the process-wide controller rather than left unlimited.
func (b *SoulFactoryRelayBus) outbound() *nostrout.Admission {
	if b.admission != nil {
		return b.admission
	}
	return defaultRelayBusAdmission()
}

func WithRelayBusEventValidator(validator func(*nostr.Event) bool) RelayBusOption {
	return func(b *SoulFactoryRelayBus) {
		if validator != nil {
			b.validateEvent = validator
		}
	}
}

// SoulFactoryRelayBus owns resilient publish/query/subscribe transport for
// SoulFactory relay interactions. It keeps protocol completion event-driven:
// EOSE marks backfill completion, OK decides publish acceptance, CLOSED/AUTH
// drive subscription handling, and reconnect timers are used only for backoff.
type SoulFactoryRelayBus struct {
	endpoints     []relayBusEndpoint
	admission     *nostrout.Admission
	signer        relayAuthSigner
	logger        *slog.Logger
	backoff       relayBusBackoff
	validateEvent func(*nostr.Event) bool
}

func NewSoulFactoryRelayBus(relays []string, opts ...RelayBusOption) (*SoulFactoryRelayBus, error) {
	relays = normalizeSoulRelays(relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("at least one SoulFactory relay is required")
	}
	endpoints := make([]relayBusEndpoint, 0, len(relays))
	for _, relay := range relays {
		endpoints = append(endpoints, newGoNostrRelayEndpoint(relay))
	}
	return newSoulFactoryRelayBusFromEndpoints(endpoints, opts...)
}

func newSoulFactoryRelayBusFromEndpoints(endpoints []relayBusEndpoint, opts ...RelayBusOption) (*SoulFactoryRelayBus, error) {
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("at least one SoulFactory relay endpoint is required")
	}
	b := &SoulFactoryRelayBus{
		endpoints:     endpoints,
		admission:     defaultRelayBusAdmission(),
		logger:        slog.Default().With("component", "soulfactory-relay-bus"),
		backoff:       defaultRelayBusBackoff,
		validateEvent: validSignedEvent,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	return b, nil
}

// Publish fans ev out to every bus relay concurrently and succeeds on the
// first acceptance. The publication is admitted as one logical event; each
// relay frame is admitted immediately before it is sent, and late relay
// outcomes are still observed after Publish returns.
func (b *SoulFactoryRelayBus) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	if b == nil || len(b.endpoints) == 0 {
		return 0, fmt.Errorf("soul factory relay bus is not configured")
	}
	pub, err := b.outbound().Begin(ctx, ev, relayBusEndpointURLs(b.endpoints))
	if err != nil {
		return 0, fmt.Errorf("admit SoulFactory relay publication: %w", err)
	}
	if len(pub.CachedResults()) > 0 {
		pub.Close()
		return 1, nil
	}
	publishCtx, cancel := context.WithCancel(ctx)

	pending := make([]relayBusEndpoint, 0, len(b.endpoints))
	for _, endpoint := range b.endpoints {
		if pub.NeedsRelay(endpoint.URL()) {
			pending = append(pending, endpoint)
		}
	}
	results := make(chan RelayPublishResult, len(pending))
	var workers sync.WaitGroup
	for _, endpoint := range pending {
		endpoint := endpoint
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- b.sendAdmitted(publishCtx, pub, endpoint, ev, false)
		}()
	}
	// The permit is released only after every worker has reported, so a relay
	// that answers after the first acceptance still feeds the breaker.
	go func() {
		workers.Wait()
		cancel()
		pub.Close()
	}()

	var failures []string
	for range pending {
		result := <-results
		if result.Accepted {
			cancel()
			return 1, nil
		}
		failures = append(failures, relayBusFailure(result))
	}
	if len(failures) == 0 {
		return 0, fmt.Errorf("event was not accepted by any relay")
	}
	return 0, fmt.Errorf("event was not accepted by any relay: %s", strings.Join(failures, "; "))
}

// relayBusCompletion is the delivery contract of a sequential publication.
type relayBusCompletion int

const (
	// relayBusAllRequired needs acceptance from every endpoint (Concord
	// community relays are operator-controlled and must all carry the wrap).
	relayBusAllRequired relayBusCompletion = iota
	// relayBusAnySufficient needs one acceptance (a recipient's inbox list is
	// not operator-controlled and may name dead relays).
	relayBusAnySufficient
)

// relayBusFailures reports per-relay delivery failures in order. It keeps each
// cause reachable through errors.Is/As while rendering the historical
// "relay: reason; relay: reason" message.
type relayBusFailures []error

func (f relayBusFailures) Error() string {
	messages := make([]string, 0, len(f))
	for _, err := range f {
		messages = append(messages, err.Error())
	}
	return strings.Join(messages, "; ")
}

func (f relayBusFailures) Unwrap() []error { return f }

// publishSequential delivers one signed event to endpoints in order under a
// single admission permit. An operation carried by ctx paces the publication
// through the bulk lane instead of failing it mid-operation. It returns one
// error per failed endpoint.
func (b *SoulFactoryRelayBus) publishSequential(ctx context.Context, ev nostr.Event, endpoints []relayBusEndpoint, completion relayBusCompletion) (bool, relayBusFailures, error) {
	if b == nil {
		return false, nil, fmt.Errorf("soul factory relay bus is not configured")
	}
	urls := relayBusEndpointURLs(endpoints)
	var (
		pub *nostrout.Publication
		err error
	)
	if op := nostrout.OperationFromContext(ctx); op != nil {
		pub, err = op.Begin(ctx, ev, urls)
	} else {
		pub, err = b.outbound().Begin(ctx, ev, urls)
	}
	if err != nil {
		return false, nil, fmt.Errorf("admit SoulFactory relay publication: %w", err)
	}
	defer pub.Close()

	if completion == relayBusAnySufficient && len(pub.CachedResults()) > 0 {
		return true, nil, nil
	}
	var failures relayBusFailures
	for _, endpoint := range endpoints {
		if !pub.NeedsRelay(endpoint.URL()) {
			continue // already accepted this exact event recently
		}
		result := b.sendAdmitted(ctx, pub, endpoint, ev, true)
		if result.Accepted {
			if completion == relayBusAnySufficient {
				return true, nil, nil
			}
			continue
		}
		failures = append(failures, relayBusFailureError(result))
		if completion == relayBusAllRequired {
			return false, failures, nil
		}
	}
	return completion == relayBusAllRequired, failures, nil
}

// sendAdmitted is the only caller of relayBusEndpoint.Publish. It admits every
// EVENT frame (including the optional single NIP-42 AUTH retry) and observes
// every outcome. A relay duplicate acknowledgement counts as acceptance.
func (b *SoulFactoryRelayBus) sendAdmitted(ctx context.Context, pub *nostrout.Publication, endpoint relayBusEndpoint, ev nostr.Event, authRetry bool) RelayPublishResult {
	admit := func(ctx context.Context) error { return pub.BeforeAttempt(ctx, endpoint.URL()) }
	attempt := func() RelayPublishResult {
		result := endpoint.Publish(ctx, ev, admit)
		if result.RelayURL == "" {
			result.RelayURL = endpoint.URL()
		}
		pub.Observe(nostrout.Result{RelayURL: result.RelayURL, Accepted: result.Accepted, Reason: result.Reason, Error: result.Error})
		if !result.Accepted && nostrout.IsDuplicateReason(result.Reason) {
			result.Accepted = true
		}
		return result
	}
	result := attempt()
	if !authRetry || result.Accepted || !relayBusAuthRequired(result) {
		return result
	}
	if b.signer == nil {
		result.Error = fmt.Errorf("requested auth but no relay auth signer is configured")
		return result
	}
	if err := endpoint.Auth(ctx, b.signer); err != nil {
		result.Error = fmt.Errorf("authenticate after relay challenge: %w", err)
		return result
	}
	return attempt()
}

func relayBusAuthRequired(result RelayPublishResult) bool {
	return isRelayAuthRequired(result.Reason) || (result.Error != nil && strings.Contains(result.Error.Error(), "auth-required:"))
}

func relayBusFailureError(result RelayPublishResult) error {
	if result.Error != nil {
		return fmt.Errorf("%s: %w", result.RelayURL, result.Error)
	}
	return errors.New(relayBusFailure(result))
}

func relayBusFailure(result RelayPublishResult) string {
	if result.Error != nil {
		return fmt.Sprintf("%s: %v", result.RelayURL, result.Error)
	}
	reason := strings.TrimSpace(result.Reason)
	if reason == "" {
		reason = "OK false"
	}
	return fmt.Sprintf("%s: %s", result.RelayURL, reason)
}

func relayBusEndpointURLs(endpoints []relayBusEndpoint) []string {
	urls := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		urls = append(urls, endpoint.URL())
	}
	return urls
}

// Authenticate establishes each relay connection and completes NIP-42 before
// a write that requires authenticated relay state.
func (b *SoulFactoryRelayBus) Authenticate(ctx context.Context) error {
	if b == nil || len(b.endpoints) == 0 {
		return fmt.Errorf("soul factory relay bus is not configured")
	}
	if b.signer == nil {
		return fmt.Errorf("soul factory relay auth signer is not configured")
	}
	for _, endpoint := range b.endpoints {
		if err := endpoint.Auth(ctx, b.signer); err != nil {
			return fmt.Errorf("authenticate to %s: %w", endpoint.URL(), err)
		}
	}
	return nil
}

func (b *SoulFactoryRelayBus) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*RelayBusSubscription, error) {
	if b == nil || len(b.endpoints) == 0 {
		return nil, fmt.Errorf("soul factory relay bus is not configured")
	}
	if len(filters) == 0 {
		return nil, fmt.Errorf("at least one Nostr filter is required")
	}

	subCtx, cancel := context.WithCancel(ctx)
	events := make(chan *nostr.Event, 64)
	eose := make(chan struct{})
	seen := map[string]struct{}{}
	seenOrder := make([]string, 0, relayBusSeenLimit)
	var seenMu sync.Mutex
	var wg sync.WaitGroup
	var eoseMu sync.Mutex
	eosed := make(map[string]struct{}, len(b.endpoints))
	var closeEOSE sync.Once

	markEOSE := func(relay string) {
		eoseMu.Lock()
		defer eoseMu.Unlock()
		if _, exists := eosed[relay]; exists {
			return
		}
		eosed[relay] = struct{}{}
		if len(eosed) == len(b.endpoints) {
			closeEOSE.Do(func() { close(eose) })
		}
	}

	dispatch := func(ev *nostr.Event) {
		if ev == nil || !b.validateEvent(ev) {
			return
		}
		seenMu.Lock()
		eventID := ev.ID.Hex()
		if _, duplicate := seen[eventID]; duplicate {
			seenMu.Unlock()
			return
		}
		seen[eventID] = struct{}{}
		seenOrder = append(seenOrder, eventID)
		if len(seenOrder) > relayBusSeenLimit {
			oldest := seenOrder[0]
			seenOrder = seenOrder[1:]
			delete(seen, oldest)
		}
		seenMu.Unlock()

		select {
		case events <- ev:
		case <-subCtx.Done():
		}
	}

	wg.Add(len(b.endpoints))
	for _, endpoint := range b.endpoints {
		endpoint := endpoint
		go func() {
			defer wg.Done()
			b.runRelaySubscription(subCtx, endpoint, cloneRelayBusFilters(filters), dispatch, markEOSE)
		}()
	}

	go func() {
		wg.Wait()
		close(events)
		closeEOSE.Do(func() { close(eose) })
	}()

	return &RelayBusSubscription{Events: events, EndOfStoredEvents: eose, cancel: cancel}, nil
}

func (b *SoulFactoryRelayBus) Query(ctx context.Context, filters []nostr.Filter) ([]*nostr.Event, error) {
	sub, err := b.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	var out []*nostr.Event
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-sub.EndOfStoredEvents:
			// Event delivery and EOSE use separate channels. The relay worker
			// dispatches historical events before marking EOSE, but both channels
			// can be ready when this query wakes up. Drain the already-dispatched
			// events before completing the query so a buffered final event is not
			// lost to select's random ready-case choice.
			for {
				select {
				case ev, ok := <-sub.Events:
					if !ok {
						return out, nil
					}
					out = append(out, ev)
				default:
					return out, nil
				}
			}
		case ev, ok := <-sub.Events:
			if !ok {
				return out, nil
			}
			out = append(out, ev)
		}
	}
}

func (b *SoulFactoryRelayBus) Close() {
	if b == nil {
		return
	}
	for _, endpoint := range b.endpoints {
		endpoint.Close()
	}
}

func (b *SoulFactoryRelayBus) runRelaySubscription(ctx context.Context, endpoint relayBusEndpoint, filters []nostr.Filter, dispatch func(*nostr.Event), markEOSE func(string)) {
	relayURL := endpoint.URL()
	attempt := 0
	initialEOSEMarked := false
	markInitialEOSE := func() {
		if initialEOSEMarked {
			return
		}
		initialEOSEMarked = true
		markEOSE(relayURL)
	}
	for ctx.Err() == nil {
		sub, err := endpoint.Subscribe(ctx, filters)
		if err != nil {
			attempt++
			b.logger.Warn("relay subscription failed", "relay", relayURL, "attempt", attempt, "error", err)
			if waitErr := b.backoff(ctx, attempt); waitErr != nil {
				return
			}
			continue
		}

		attempt = 0
		reissue, authReissue, eosed := b.consumeRelaySubscription(ctx, endpoint, sub, dispatch, markInitialEOSE)
		if eosed {
			initialEOSEMarked = true
		}
		sub.Close()
		if !reissue || ctx.Err() != nil {
			return
		}
		if authReissue {
			continue
		}
		attempt++
		if waitErr := b.backoff(ctx, attempt); waitErr != nil {
			return
		}
	}
}

func (b *SoulFactoryRelayBus) consumeRelaySubscription(ctx context.Context, endpoint relayBusEndpoint, sub relayBusRelaySubscription, dispatch func(*nostr.Event), markEOSE func()) (reissue bool, authReissue bool, eosed bool) {
	events := sub.Events()
	eose := sub.EndOfStoredEvents()
	closed := sub.ClosedReason()
	for events != nil || eose != nil || closed != nil {
		select {
		case <-ctx.Done():
			return false, false, eosed
		default:
		}
		select {
		case reason, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return b.handleRelayClosed(ctx, endpoint, reason, eosed)
		default:
		}
		select {
		case _, ok := <-eose:
			if ok || eose != nil {
				drainRelayEvents(events, dispatch)
				markEOSE()
				eosed = true
			}
			eose = nil
			continue
		default:
		}

		select {
		case <-ctx.Done():
			return false, false, eosed
		case reason, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return b.handleRelayClosed(ctx, endpoint, reason, eosed)
		case _, ok := <-eose:
			if ok || eose != nil {
				drainRelayEvents(events, dispatch)
				markEOSE()
				eosed = true
			}
			eose = nil
		case ev, ok := <-events:
			if !ok {
				if reason, ok := drainRelayClosed(closed); ok {
					return b.handleRelayClosed(ctx, endpoint, reason, eosed)
				}
				if !eosed && drainRelayEOSE(eose) {
					markEOSE()
					eosed = true
				}
				return true, false, eosed
			}
			dispatch(ev)
		}
	}
	return true, false, eosed
}

// drainRelayEvents preserves the NIP-01 ordering guarantee when an endpoint
// exposes events and EOSE on separate channels. Both may be ready at once even
// though the endpoint observed the events first. Dispatch everything already
// queued before publishing merged EOSE to query callers.
func drainRelayEvents(events <-chan *nostr.Event, dispatch func(*nostr.Event)) {
	for events != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			dispatch(ev)
		default:
			return
		}
	}
}

func (b *SoulFactoryRelayBus) handleRelayClosed(ctx context.Context, endpoint relayBusEndpoint, reason string, eosed bool) (reissue bool, authReissue bool, eoseSeen bool) {
	reason = strings.TrimSpace(reason)
	if isRelayAuthRequired(reason) {
		if b.signer == nil {
			b.logger.Warn("relay requested auth but no signer is configured", "relay", endpoint.URL(), "reason", reason)
			return true, false, eosed
		}
		if err := endpoint.Auth(ctx, b.signer); err != nil {
			b.logger.Warn("relay auth failed", "relay", endpoint.URL(), "reason", reason, "error", err)
			return true, false, eosed
		}
		return true, true, eosed
	}
	b.logger.Warn("relay closed subscription", "relay", endpoint.URL(), "reason", reason)
	return true, false, eosed
}

func drainRelayClosed(ch <-chan string) (string, bool) {
	if ch == nil {
		return "", false
	}
	select {
	case reason, ok := <-ch:
		return reason, ok
	default:
		return "", false
	}
}

func drainRelayEOSE(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case _, ok := <-ch:
		return ok || ch != nil
	default:
		return false
	}
}

const relayBusSeenLimit = 4096

func defaultRelayBusBackoff(ctx context.Context, attempt int) error {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 5)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRelayAuthRequired(reason string) bool {
	return strings.HasPrefix(strings.TrimSpace(reason), "auth-required:")
}

func cloneRelayBusFilters(filters []nostr.Filter) []nostr.Filter {
	cloned := make([]nostr.Filter, len(filters))
	copy(cloned, filters)
	return cloned
}

type goNostrRelayEndpoint struct {
	url   string
	mu    sync.Mutex
	relay *nostr.Relay
}

func newGoNostrRelayEndpoint(url string) *goNostrRelayEndpoint {
	return &goNostrRelayEndpoint{url: strings.TrimSpace(url)}
}

func (e *goNostrRelayEndpoint) URL() string { return e.url }

func (e *goNostrRelayEndpoint) Publish(ctx context.Context, ev nostr.Event, admit func(context.Context) error) RelayPublishResult {
	if admit == nil {
		return RelayPublishResult{RelayURL: e.url, Error: fmt.Errorf("unadmitted relay publication refused")}
	}
	if err := e.ensureConnected(ctx); err != nil {
		return RelayPublishResult{RelayURL: e.url, Error: err}
	}
	e.mu.Lock()
	relay := e.relay
	e.mu.Unlock()
	if relay == nil {
		return RelayPublishResult{RelayURL: e.url, Error: fmt.Errorf("relay is not connected")}
	}
	if err := admit(ctx); err != nil {
		return RelayPublishResult{RelayURL: e.url, Error: err}
	}
	if err := relay.Publish(ctx, ev); err != nil {
		if reason, ok := relayOKFalseReason(err); ok {
			return RelayPublishResult{RelayURL: e.url, Accepted: false, Reason: reason}
		}
		e.resetRelay()
		return RelayPublishResult{RelayURL: e.url, Error: err}
	}
	return RelayPublishResult{RelayURL: e.url, Accepted: true}
}

func (e *goNostrRelayEndpoint) Subscribe(ctx context.Context, filters []nostr.Filter) (relayBusRelaySubscription, error) {
	if err := e.ensureConnected(ctx); err != nil {
		return nil, err
	}
	e.mu.Lock()
	relay := e.relay
	e.mu.Unlock()
	if relay == nil {
		return nil, fmt.Errorf("relay is not connected")
	}
	ctx, cancel := context.WithCancel(ctx)
	subs := make([]*nostr.Subscription, 0, len(filters))
	for _, filter := range filters {
		sub, err := relay.Subscribe(ctx, filter, nostr.SubscriptionOptions{})
		if err != nil {
			cancel()
			for _, existing := range subs {
				existing.Unsub()
			}
			e.resetRelay()
			return nil, err
		}
		subs = append(subs, sub)
	}
	return newGoNostrRelaySubscription(ctx, cancel, subs), nil
}

func (e *goNostrRelayEndpoint) Auth(ctx context.Context, signer relayAuthSigner) error {
	if signer == nil {
		return fmt.Errorf("relay auth signer is required")
	}
	if err := e.ensureConnected(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	relay := e.relay
	e.mu.Unlock()
	if relay == nil {
		return fmt.Errorf("relay is not connected")
	}
	sign := func(authCtx context.Context, event *nostr.Event) error {
		return signer.Sign(authCtx, event)
	}
	challengeDeadline := time.Now().Add(2 * time.Second)
	for {
		err := relay.Auth(ctx, sign)
		if err == nil || !strings.Contains(err.Error(), "no challenge") {
			return err
		}
		if time.Now().After(challengeDeadline) {
			return err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *goNostrRelayEndpoint) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.relay != nil {
		_ = e.relay.Close()
		e.relay = nil
	}
}

func (e *goNostrRelayEndpoint) ensureConnected(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.relay != nil && e.relay.IsConnected() {
		return nil
	}
	relay, err := nostr.RelayConnect(ctx, e.url, nostr.RelayOptions{})
	if err != nil {
		e.relay = nil
		return err
	}
	e.relay = relay
	return nil
}

func (e *goNostrRelayEndpoint) resetRelay() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.relay != nil {
		_ = e.relay.Close()
		e.relay = nil
	}
}

type goNostrRelaySubscription struct {
	cancel context.CancelFunc
	subs   []*nostr.Subscription
	events chan *nostr.Event
	eose   chan struct{}
	closed chan string
}

func newGoNostrRelaySubscription(ctx context.Context, cancel context.CancelFunc, subs []*nostr.Subscription) *goNostrRelaySubscription {
	s := &goNostrRelaySubscription{
		cancel: cancel,
		subs:   subs,
		events: make(chan *nostr.Event, 64),
		eose:   make(chan struct{}),
		closed: make(chan string, len(subs)),
	}
	var wg sync.WaitGroup
	var closedWG sync.WaitGroup
	eoseObserved := make(chan struct{}, len(subs))
	wg.Add(len(subs))
	closedWG.Add(len(subs))
	for _, sub := range subs {
		sub := sub
		go func() {
			defer wg.Done()
			events := sub.Events
			eose := sub.EndOfStoredEvents
			for events != nil {
				select {
				case event, ok := <-events:
					if !ok {
						return
					}
					select {
					case s.events <- &event:
					case <-ctx.Done():
						return
					}
				case <-eose:
					// The upstream library exposes events and EOSE separately.
					// Drain events already queued upstream before forwarding EOSE,
					// then keep this same goroutine for realtime delivery.
					for {
						select {
						case event, ok := <-events:
							if !ok {
								events = nil
								break
							}
							select {
							case s.events <- &event:
							case <-ctx.Done():
								return
							}
						default:
							eose = nil
						}
						if eose == nil || events == nil {
							break
						}
					}
					select {
					case eoseObserved <- struct{}{}:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
		go func() {
			defer closedWG.Done()
			select {
			case reason, ok := <-sub.ClosedReason:
				if ok {
					select {
					case s.closed <- reason:
					case <-ctx.Done():
					}
				}
			case <-ctx.Done():
			}
		}()
	}
	go func() {
		for i := 0; i < len(subs); i++ {
			select {
			case <-eoseObserved:
			case <-ctx.Done():
				return
			}
		}
		close(s.eose)
	}()
	go func() {
		wg.Wait()
		close(s.events)
	}()
	go func() {
		closedWG.Wait()
		close(s.closed)
	}()
	return s
}

func (s *goNostrRelaySubscription) Events() <-chan *nostr.Event {
	if s == nil || s.events == nil {
		return closedEventChannel()
	}
	return s.events
}

func (s *goNostrRelaySubscription) EndOfStoredEvents() <-chan struct{} {
	if s == nil || s.eose == nil {
		return closedStructChannel()
	}
	return s.eose
}

func (s *goNostrRelaySubscription) ClosedReason() <-chan string {
	if s == nil || s.closed == nil {
		return closedStringChannel()
	}
	return s.closed
}

func (s *goNostrRelaySubscription) Close() {
	if s == nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	for _, sub := range s.subs {
		if sub != nil {
			sub.Unsub()
		}
	}
}

func relayOKFalseReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	message := err.Error()
	if strings.HasPrefix(message, "msg:") {
		return strings.TrimSpace(strings.TrimPrefix(message, "msg:")), true
	}
	return "", false
}

func closedEventChannel() <-chan *nostr.Event {
	ch := make(chan *nostr.Event)
	close(ch)
	return ch
}

func closedStructChannel() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func closedStringChannel() <-chan string {
	ch := make(chan string)
	close(ch)
	return ch
}
