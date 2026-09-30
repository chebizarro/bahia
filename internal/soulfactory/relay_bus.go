package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
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
// relay bus. EndOfStoredEvents closes once every relay has answered the initial
// REQ with a terminal frame, EOSE or CLOSED, or once the subscription ends.
// Closing is not completeness: StoredEventsIncomplete reports relays that
// CLOSED or never answered. Events remains open for realtime events until the
// caller's context is cancelled.
type RelayBusSubscription struct {
	Events            <-chan *nostr.Event
	EndOfStoredEvents <-chan struct{}
	cancel            context.CancelFunc
	stored            *relayStoredEventsTracker
}

func (s *RelayBusSubscription) Close() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

// StoredEventsOutcome returns each relay's answer to the initial REQ so far. It
// is nil for subscriptions that were not built by the bus.
func (s *RelayBusSubscription) StoredEventsOutcome() []RelayStoredEventsOutcome {
	if s == nil || s.stored == nil {
		return nil
	}
	return s.stored.snapshot()
}

// StoredEventsIncomplete returns nil when every relay has sent EOSE. Otherwise
// it returns a *RelayBusIncompleteError naming the relays that CLOSED the REQ
// or have not answered yet. cause is the reason the caller stopped waiting (a
// context error), or nil when the caller saw EndOfStoredEvents.
func (s *RelayBusSubscription) StoredEventsIncomplete(cause error) error {
	if s == nil || s.stored == nil {
		if cause == nil {
			return nil
		}
		return &RelayBusIncompleteError{Cause: cause}
	}
	return s.stored.incomplete(cause)
}

// CollectStoredEvents gathers the subscription's stored events until every
// relay has answered with EOSE or CLOSED, or until ctx ends. A context without
// a deadline is bounded by relayBusStoredEventsTimeout, so a relay that never
// answers cannot hold the caller forever. The result is complete only when the
// error is nil; otherwise the error is a *RelayBusIncompleteError (matching
// ErrRelayBusIncomplete, and the context error when the wait timed out) and
// the returned events are the partial set received before the wait ended.
func (s *RelayBusSubscription) CollectStoredEvents(ctx context.Context) ([]*nostr.Event, error) {
	if s == nil {
		return nil, fmt.Errorf("relay bus subscription is nil")
	}
	waitCtx, cancel := boundRelayBusWait(ctx, relayBusStoredEventsTimeout)
	defer cancel()
	var out []*nostr.Event
	for {
		select {
		case <-waitCtx.Done():
			return out, s.StoredEventsIncomplete(context.Cause(waitCtx))
		case <-s.EndOfStoredEvents:
			// Event delivery and EOSE use separate channels. Relay workers
			// dispatch stored events before settling their REQ, but both channels
			// can be ready when this wakes up. Drain the already-dispatched events
			// so a buffered final event is not lost to select's random choice.
			for {
				select {
				case ev, ok := <-s.Events:
					if !ok {
						return out, s.StoredEventsIncomplete(nil)
					}
					out = append(out, ev)
				default:
					return out, s.StoredEventsIncomplete(nil)
				}
			}
		case ev, ok := <-s.Events:
			if !ok {
				// The bus closes Events only after every relay worker has
				// exited, i.e. once the caller cancelled the subscription.
				return out, s.StoredEventsIncomplete(ctx.Err())
			}
			out = append(out, ev)
		}
	}
}

// relayBusStoredEventsTimeout bounds a stored-event read whose caller context
// has no deadline. An earlier caller deadline always wins.
const relayBusStoredEventsTimeout = 15 * time.Second

// relayBusAuthHandshakeTimeout bounds one NIP-42 handshake (connect, probe
// answers, AUTH OK) when the caller context has no earlier deadline.
const relayBusAuthHandshakeTimeout = 10 * time.Second

// boundRelayBusWait returns ctx bounded by limit unless ctx already carries a
// deadline, which the caller owns.
func boundRelayBusWait(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, limit)
}

// RelayStoredEventsStatus is one relay's answer to a bus subscription's
// initial REQ.
type RelayStoredEventsStatus string

const (
	// RelayStoredEventsPending means the relay has sent neither EOSE nor CLOSED.
	RelayStoredEventsPending RelayStoredEventsStatus = "pending"
	// RelayStoredEventsEOSE means the relay finished sending stored events.
	RelayStoredEventsEOSE RelayStoredEventsStatus = "eose"
	// RelayStoredEventsClosed means the relay CLOSED the REQ before EOSE (and
	// NIP-42 authentication, when requested, did not recover it).
	RelayStoredEventsClosed RelayStoredEventsStatus = "closed"
)

// RelayStoredEventsOutcome is one relay's answer to the initial REQ.
type RelayStoredEventsOutcome struct {
	RelayURL string
	Status   RelayStoredEventsStatus
	Reason   string
}

// ErrRelayBusIncomplete matches every *RelayBusIncompleteError.
var ErrRelayBusIncomplete = errors.New("relay bus stored events are incomplete")

// RelayBusIncompleteError reports a stored-event read that ended without EOSE
// from every relay. It is never a complete result. Cause is the context error
// when a deadline or cancellation ended the wait, and nil when every relay
// answered but at least one CLOSED the REQ instead of sending EOSE.
type RelayBusIncompleteError struct {
	// Relays lists every relay that did not send EOSE.
	Relays []RelayStoredEventsOutcome
	// Total is the number of relays the read covered, so Total-len(Relays)
	// relays sent EOSE. It is zero when the relay set is unknown.
	Total int
	Cause error
}

func (e *RelayBusIncompleteError) Error() string {
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
		return fmt.Sprintf("relay bus stored events are incomplete (%v): %s", e.Cause, detail)
	}
	return "relay bus stored events are incomplete: " + detail
}

func (e *RelayBusIncompleteError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrRelayBusIncomplete}
	}
	return []error{ErrRelayBusIncomplete, e.Cause}
}

// relayStoredEventsTracker records each relay's terminal answer to the initial
// REQ and closes done once every relay has one, or when the subscription ends.
type relayStoredEventsTracker struct {
	mu        sync.Mutex
	outcomes  []RelayStoredEventsOutcome
	pending   int
	done      chan struct{}
	closeDone sync.Once
}

func newRelayStoredEventsTracker(relays []string) *relayStoredEventsTracker {
	t := &relayStoredEventsTracker{
		outcomes: make([]RelayStoredEventsOutcome, len(relays)),
		pending:  len(relays),
		done:     make(chan struct{}),
	}
	for i, relay := range relays {
		t.outcomes[i] = RelayStoredEventsOutcome{RelayURL: relay, Status: RelayStoredEventsPending}
	}
	return t
}

// settle records relay i's first terminal answer; later answers are ignored.
func (t *relayStoredEventsTracker) settle(i int, status RelayStoredEventsStatus, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.outcomes[i].Status != RelayStoredEventsPending {
		return
	}
	t.outcomes[i].Status = status
	t.outcomes[i].Reason = reason
	t.pending--
	if t.pending == 0 {
		t.finish()
	}
}

func (t *relayStoredEventsTracker) finish() {
	t.closeDone.Do(func() { close(t.done) })
}

func (t *relayStoredEventsTracker) snapshot() []RelayStoredEventsOutcome {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]RelayStoredEventsOutcome(nil), t.outcomes...)
}

func (t *relayStoredEventsTracker) incomplete(cause error) error {
	var missing []RelayStoredEventsOutcome
	for _, outcome := range t.snapshot() {
		if outcome.Status != RelayStoredEventsEOSE {
			missing = append(missing, outcome)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &RelayBusIncompleteError{Relays: missing, Total: len(t.outcomes), Cause: cause}
}

type relayAuthSigner interface {
	Sign(context.Context, *nostr.Event) error
}

type relayBusEndpoint interface {
	URL() string
	Publish(context.Context, nostr.Event) RelayPublishResult
	Subscribe(context.Context, []nostr.Filter) (relayBusRelaySubscription, error)
	// Authenticate completes NIP-42 for later frames on this endpoint. probe is
	// the frame the relay rejected with "auth-required:", or nil for a proactive
	// handshake before a write. It returns nil when the endpoint is already
	// authenticated, so callers retry the rejected frame exactly once.
	Authenticate(ctx context.Context, signer relayAuthSigner, probe *relayAuthProbe) error
	Close()
}

// relayAuthProbe is a frame a relay rejected with "auth-required:". The
// endpoint replays it on a private connection so the relay's AUTH challenge is
// provably received before the answer the handshake waits for.
type relayAuthProbe struct {
	Filters []nostr.Filter
	Event   *nostr.Event
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

// Relay bus publish quorum values for WithRelayBusPublishQuorum.
const (
	// RelayBusPublishQuorumDefault succeeds once one relay has accepted.
	RelayBusPublishQuorumDefault = 1
	// RelayBusPublishQuorumAll succeeds only once every relay has accepted.
	RelayBusPublishQuorumAll = -1
)

// WithRelayBusPublishQuorum sets how many relays must accept (OK true or
// duplicate) a published event for Publish to succeed: N > 0 relays (capped at
// the relay count) or RelayBusPublishQuorumAll. The default is
// RelayBusPublishQuorumDefault (one relay). Every relay's result is collected
// regardless of the quorum.
func WithRelayBusPublishQuorum(quorum int) RelayBusOption {
	return func(b *SoulFactoryRelayBus) {
		if quorum > 0 || quorum == RelayBusPublishQuorumAll {
			b.publishQuorum = quorum
		}
	}
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
	signer        relayAuthSigner
	logger        *slog.Logger
	backoff       relayBusBackoff
	validateEvent func(*nostr.Event) bool
	publishQuorum int
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
		logger:        slog.Default().With("component", "soulfactory-relay-bus"),
		backoff:       defaultRelayBusBackoff,
		validateEvent: validSignedEvent,
		publishQuorum: RelayBusPublishQuorumDefault,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	return b, nil
}

// Publish sends ev to every relay and waits for each relay's OK. It returns the
// number of relays that accepted (OK true or duplicate) and an error only when
// fewer than the publish quorum accepted (default one relay; see
// WithRelayBusPublishQuorum). One relay's OK never cancels the publishes still
// in flight to others. The bus has no durable retry: relays that did not accept
// are logged, and PublishWithResults returns every relay's outcome.
func (b *SoulFactoryRelayBus) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	results, err := b.PublishWithResults(ctx, ev)
	return countRelayBusAccepted(results), err
}

// PublishWithResults sends ev to every relay concurrently and returns every
// relay's outcome, in endpoint order, once all relays have answered (or failed).
// The error is non-nil only when fewer than the publish quorum accepted.
func (b *SoulFactoryRelayBus) PublishWithResults(ctx context.Context, ev nostr.Event) ([]RelayPublishResult, error) {
	if b == nil || len(b.endpoints) == 0 {
		return nil, fmt.Errorf("soul factory relay bus is not configured")
	}

	results := make([]RelayPublishResult, len(b.endpoints))
	var wg sync.WaitGroup
	for i, endpoint := range b.endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := publishRelayEndpoint(ctx, endpoint, b.signer, ev)
			if result.RelayURL == "" {
				result.RelayURL = endpoint.URL()
			}
			results[i] = result
		}()
	}
	wg.Wait()

	accepted := countRelayBusAccepted(results)
	required := b.requiredAcceptances()

	var failures []string
	for _, result := range results {
		if relayBusAccepted(result) {
			continue
		}
		if result.Error != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", result.RelayURL, result.Error))
			continue
		}
		reason := strings.TrimSpace(result.Reason)
		if reason == "" {
			reason = "OK false"
		}
		failures = append(failures, fmt.Sprintf("%s: %s", result.RelayURL, reason))
	}
	if accepted >= required {
		if len(failures) > 0 {
			b.log().Warn("event accepted by publish quorum; some relays did not accept and will not be retried by the bus",
				"event_id", ev.ID.Hex(), "accepted", accepted, "relays", len(b.endpoints), "failures", strings.Join(failures, "; "))
		}
		return results, nil
	}
	if accepted == 0 {
		return results, fmt.Errorf("event was not accepted by any relay: %s", strings.Join(failures, "; "))
	}
	return results, fmt.Errorf("event accepted by %d of %d required relays: %s", accepted, required, strings.Join(failures, "; "))
}

// requiredAcceptances resolves the publish quorum against the relay count. A
// zero value (bus built without the constructor) means the default of one.
func (b *SoulFactoryRelayBus) requiredAcceptances() int {
	required := b.publishQuorum
	switch {
	case required == RelayBusPublishQuorumAll:
		required = len(b.endpoints)
	case required <= 0:
		required = RelayBusPublishQuorumDefault
	}
	return min(required, len(b.endpoints))
}

func (b *SoulFactoryRelayBus) log() *slog.Logger {
	if b.logger != nil {
		return b.logger
	}
	return slog.Default()
}

// publishRelayEndpoint publishes ev to one relay. When the relay answers
// "auth-required:" and a signer is configured, it completes NIP-42 on the
// endpoint and republishes exactly once.
func publishRelayEndpoint(ctx context.Context, endpoint relayBusEndpoint, signer relayAuthSigner, ev nostr.Event) RelayPublishResult {
	result := endpoint.Publish(ctx, ev)
	if relayBusAccepted(result) || signer == nil || !relayPublishAuthRequired(result) {
		return result
	}
	if err := endpoint.Authenticate(ctx, signer, &relayAuthProbe{Event: &ev}); err != nil {
		return RelayPublishResult{RelayURL: endpoint.URL(), Error: fmt.Errorf("authenticate after %q: %w", strings.TrimSpace(result.Reason), err)}
	}
	return endpoint.Publish(ctx, ev)
}

func relayPublishAuthRequired(result RelayPublishResult) bool {
	if isRelayAuthRequired(result.Reason) {
		return true
	}
	return result.Error != nil && strings.Contains(result.Error.Error(), "auth-required:")
}

// relayBusAccepted treats a duplicate OK as acceptance: the relay has the event.
func relayBusAccepted(result RelayPublishResult) bool {
	return result.Accepted || (result.Error == nil && strings.HasPrefix(strings.TrimSpace(result.Reason), "duplicate:"))
}

func countRelayBusAccepted(results []RelayPublishResult) int {
	accepted := 0
	for _, result := range results {
		if relayBusAccepted(result) {
			accepted++
		}
	}
	return accepted
}

// Authenticate completes NIP-42 on every relay that challenges, before a write
// that requires authenticated relay state. A relay that does not challenge is
// left unauthenticated; its later "auth-required:" answers are handled per
// frame by Publish and subscriptions.
func (b *SoulFactoryRelayBus) Authenticate(ctx context.Context) error {
	if b == nil || len(b.endpoints) == 0 {
		return fmt.Errorf("soul factory relay bus is not configured")
	}
	if b.signer == nil {
		return fmt.Errorf("soul factory relay auth signer is not configured")
	}
	for _, endpoint := range b.endpoints {
		if err := endpoint.Authenticate(ctx, b.signer, nil); err != nil {
			return fmt.Errorf("authenticate to %s: %w", endpoint.URL(), err)
		}
	}
	return nil
}

func (b *SoulFactoryRelayBus) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*RelayBusSubscription, error) {
	return b.subscribe(ctx, filters, 0)
}

// subscribeResumable is SubscribeAllWithEOSE for a long-lived subscription
// that must not lose events across reconnects. Each relay keeps a resume cursor
// (see relayResumeCursor): once that relay has sent EOSE, a REQ reissued after
// a dropped connection or a CLOSED asks only for events since the newest one
// it delivered, less overlap. Events published while the relay was unreachable
// therefore arrive in the reissued REQ's backfill, without replaying the whole
// original backfill. The bus deduplication window and idempotent handlers
// absorb the overlap. Until a relay's first EOSE its reissues repeat filters.
func (b *SoulFactoryRelayBus) subscribeResumable(ctx context.Context, filters []nostr.Filter, overlap time.Duration) (*RelayBusSubscription, error) {
	if overlap <= 0 {
		return nil, fmt.Errorf("resumable subscription overlap must be positive")
	}
	return b.subscribe(ctx, filters, overlap)
}

// subscribe opens filters on every relay. overlap > 0 gives each relay a
// resume cursor; zero reissues the original filters after every reconnect.
func (b *SoulFactoryRelayBus) subscribe(ctx context.Context, filters []nostr.Filter, overlap time.Duration) (*RelayBusSubscription, error) {
	if b == nil || len(b.endpoints) == 0 {
		return nil, fmt.Errorf("soul factory relay bus is not configured")
	}
	if len(filters) == 0 {
		return nil, fmt.Errorf("at least one Nostr filter is required")
	}

	subCtx, cancel := context.WithCancel(ctx)
	events := make(chan *nostr.Event, 64)
	relays := make([]string, len(b.endpoints))
	for i, endpoint := range b.endpoints {
		relays[i] = endpoint.URL()
	}
	stored := newRelayStoredEventsTracker(relays)
	seen := map[string]struct{}{}
	seenOrder := make([]string, 0, relayBusSeenLimit)
	var seenMu sync.Mutex
	var wg sync.WaitGroup

	dispatch := func(ev *nostr.Event, cursor *relayResumeCursor) {
		if ev == nil || !b.validateEvent(ev) {
			return
		}
		// The cursor counts every valid event its relay delivered, including
		// ones another relay delivered first.
		cursor.observe(ev)
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
	for i, endpoint := range b.endpoints {
		var cursor *relayResumeCursor
		if overlap > 0 {
			cursor = newRelayResumeCursor(overlap)
		}
		go func() {
			defer wg.Done()
			settle := func(status RelayStoredEventsStatus, reason string) { stored.settle(i, status, reason) }
			relayDispatch := func(ev *nostr.Event) { dispatch(ev, cursor) }
			b.runRelaySubscription(subCtx, endpoint, cloneRelayBusFilters(filters), cursor, relayDispatch, settle)
		}()
	}

	go func() {
		wg.Wait()
		close(events)
		stored.finish()
	}()

	return &RelayBusSubscription{Events: events, EndOfStoredEvents: stored.done, cancel: cancel, stored: stored}, nil
}

// Query returns the stored events matching filters once every relay has
// answered with EOSE or CLOSED, bounded by ctx (or relayBusStoredEventsTimeout
// when ctx has no deadline). A nil error means every relay sent EOSE. Anything
// else is a *RelayBusIncompleteError; the events returned with it are partial.
func (b *SoulFactoryRelayBus) Query(ctx context.Context, filters []nostr.Filter) ([]*nostr.Event, error) {
	sub, err := b.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	return sub.CollectStoredEvents(ctx)
}

func (b *SoulFactoryRelayBus) Close() {
	if b == nil {
		return
	}
	for _, endpoint := range b.endpoints {
		endpoint.Close()
	}
}

// runRelaySubscription keeps one relay's REQ alive until ctx ends. settle
// records the relay's terminal answer to the initial REQ: EOSE, or a CLOSED
// that authentication did not recover. Reissues after a dropped connection or
// a CLOSED keep realtime delivery going; they wait for the backoff first. A
// non-nil cursor narrows each reissue to the events the relay may not have
// delivered yet (see subscribeResumable).
func (b *SoulFactoryRelayBus) runRelaySubscription(ctx context.Context, endpoint relayBusEndpoint, filters []nostr.Filter, cursor *relayResumeCursor, dispatch func(*nostr.Event), settle func(RelayStoredEventsStatus, string)) {
	relayURL := endpoint.URL()
	attempt := 0
	// authRetried stops an authenticated relay that still answers
	// "auth-required:" from being reissued in a loop without backoff.
	authRetried := false
	for ctx.Err() == nil {
		reqFilters := cursor.resume(filters)
		cursor.begin()
		sub, err := endpoint.Subscribe(ctx, reqFilters)
		if err != nil {
			attempt++
			b.log().Warn("relay subscription failed", "relay", relayURL, "attempt", attempt, "error", err)
			if waitErr := b.backoff(ctx, attempt); waitErr != nil {
				return
			}
			continue
		}

		attempt = 0
		end := b.consumeRelaySubscription(ctx, sub, dispatch, func() {
			cursor.eose()
			settle(RelayStoredEventsEOSE, "")
		})
		sub.Close()
		if ctx.Err() != nil {
			return
		}
		if end.eosed {
			authRetried = false
		}
		if end.closed {
			if isRelayAuthRequired(end.reason) && !authRetried && b.authenticateRelaySubscription(ctx, endpoint, end.reason, reqFilters) {
				authRetried = true
				continue
			}
			b.log().Warn("relay closed subscription", "relay", relayURL, "reason", end.reason)
			settle(RelayStoredEventsClosed, end.reason)
		}
		attempt++
		if waitErr := b.backoff(ctx, attempt); waitErr != nil {
			return
		}
	}
}

// relaySubscriptionEnd describes how one relay subscription generation ended.
type relaySubscriptionEnd struct {
	eosed  bool
	closed bool
	reason string
}

func (b *SoulFactoryRelayBus) consumeRelaySubscription(ctx context.Context, sub relayBusRelaySubscription, dispatch func(*nostr.Event), markEOSE func()) relaySubscriptionEnd {
	var end relaySubscriptionEnd
	events := sub.Events()
	eose := sub.EndOfStoredEvents()
	closed := sub.ClosedReason()
	onEOSE := func() {
		drainRelayEvents(events, dispatch)
		markEOSE()
		end.eosed = true
	}
	onClosed := func(reason string) relaySubscriptionEnd {
		end.closed = true
		end.reason = strings.TrimSpace(reason)
		return end
	}
	for events != nil || eose != nil || closed != nil {
		// CLOSED and EOSE take priority over queued events so a terminal frame is
		// never starved by a busy event stream.
		select {
		case <-ctx.Done():
			return end
		default:
		}
		select {
		case reason, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return onClosed(reason)
		default:
		}
		select {
		case <-eose:
			onEOSE()
			eose = nil
			continue
		default:
		}

		select {
		case <-ctx.Done():
			return end
		case reason, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			return onClosed(reason)
		case <-eose:
			onEOSE()
			eose = nil
		case ev, ok := <-events:
			if !ok {
				if reason, ok := drainRelayClosed(closed); ok {
					return onClosed(reason)
				}
				if !end.eosed && drainRelayEOSE(eose) {
					markEOSE()
					end.eosed = true
				}
				return end
			}
			dispatch(ev)
		}
	}
	return end
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

// authenticateRelaySubscription answers an "auth-required:" CLOSED. It reports
// whether the subscription should be reissued at once on the authenticated
// endpoint.
func (b *SoulFactoryRelayBus) authenticateRelaySubscription(ctx context.Context, endpoint relayBusEndpoint, reason string, filters []nostr.Filter) bool {
	if b.signer == nil {
		b.log().Warn("relay requested auth but no signer is configured", "relay", endpoint.URL(), "reason", reason)
		return false
	}
	if err := endpoint.Authenticate(ctx, b.signer, &relayAuthProbe{Filters: filters}); err != nil {
		b.log().Warn("relay auth failed", "relay", endpoint.URL(), "reason", reason, "error", err)
		return false
	}
	return true
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
	case <-ch:
		return true
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

// relayResumeCursor is one relay's position in a resumable subscription: the
// newest created_at among the valid events that relay delivered. Stored events
// arrive in no guaranteed order, so a generation's events count only once its
// EOSE proves the backfill below them complete; after EOSE, realtime events
// advance the cursor as they arrive. Timestamps are clamped to the local clock
// so a future-dated event cannot push the cursor past events not yet seen. All
// methods are nil-safe: a nil cursor means "reissue the original filters".
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

// resume returns the filters for the next REQ: the originals until the relay
// has sent EOSE, then each filter with Since raised to the cursor less the
// overlap.
func (c *relayResumeCursor) resume(filters []nostr.Filter) []nostr.Filter {
	resumed := cloneRelayBusFilters(filters)
	if c == nil {
		return resumed
	}
	c.mu.Lock()
	since := c.since
	c.mu.Unlock()
	if since == 0 {
		return resumed
	}
	from := since - c.overlap
	if from < 1 {
		from = 1
	}
	for i := range resumed {
		if resumed[i].Since < from {
			resumed[i].Since = from
		}
	}
	return resumed
}

// goNostrRelayEndpoint is one relay behind the bus, backed by fiatjaf.com/nostr.
//
// NIP-42 and the pinned library: nostr.Relay stores the AUTH challenge, the
// sync.Once guarding AUTH, and the authed flag in plain fields. The reader
// goroutine rewrites challenge and performAuth on every AUTH frame, and
// Relay.Auth reads them from the caller's goroutine without synchronisation.
// Calling Relay.Auth while the reader might still process an AUTH frame is
// therefore a data race. Worse, resetting performAuth while another goroutine is
// inside performAuth.Do aborts the process with "sync: unlock of unlocked mutex".
// RelayOptions.AuthHandler does not avoid this: the library starts an Auth
// goroutine per AUTH frame, and relays such as khatru send a fresh challenge with
// every "auth-required:" rejection, so those goroutines overlap the next reset.
// AuthHandler also discards the AUTH OK, so nobody could learn the outcome.
//
// The endpoint therefore never sets AuthHandler and calls Relay.Auth only inside
// Authenticate, on a connection that no other goroutine has seen. Authenticate
// dials that private connection, sends only the probe frames, and waits until
// the relay has answered every one of them (OK, EOSE or CLOSED). Each answer
// reaches this goroutine through a channel fed by the library's reader. Every
// AUTH frame the relay sent before those answers (on connect, or just before an
// "auth-required:" rejection) was therefore processed before Relay.Auth reads
// challenge. The only frame in flight during Relay.Auth is our AUTH itself.
// After the swap the connection is shared and Relay.Auth is never called on it
// again. A later handshake always dials a new connection. The one assumption
// is the NIP-42 norm that a relay challenges on connect or alongside a
// rejection, not unprompted while our AUTH awaits its OK.
type goNostrRelayEndpoint struct {
	url string
	// handshake admits one NIP-42 handshake at a time.
	handshake chan struct{}

	mu   sync.Mutex
	conn *goNostrRelayConn
	// open holds every connection not yet closed, including retired ones that
	// still serve subscriptions, so Close can end them all.
	open map[*goNostrRelayConn]struct{}
}

// goNostrRelayConn is one connection generation. Fields other than relay are
// guarded by the endpoint's mu.
type goNostrRelayConn struct {
	relay *nostr.Relay
	// authenticated is true when this connection completed NIP-42 during the
	// private handshake that produced it.
	authenticated bool
	// probed is true when a handshake ran on this connection, whether or not the
	// relay challenged it.
	probed bool
	// users counts in-flight publishes and open subscriptions. A retired
	// connection closes when its last user releases it, so replacing the
	// endpoint's connection never cuts off subscriptions already served by it.
	users   int
	retired bool
}

func newGoNostrRelayEndpoint(url string) *goNostrRelayEndpoint {
	return &goNostrRelayEndpoint{
		url:       strings.TrimSpace(url),
		handshake: make(chan struct{}, 1),
		open:      make(map[*goNostrRelayConn]struct{}),
	}
}

func (e *goNostrRelayEndpoint) URL() string { return e.url }

func (e *goNostrRelayEndpoint) Publish(ctx context.Context, ev nostr.Event) RelayPublishResult {
	conn, err := e.acquire(ctx)
	if err != nil {
		return RelayPublishResult{RelayURL: e.url, Error: err}
	}
	defer e.release(conn)
	if err := conn.relay.Publish(ctx, ev); err != nil {
		if reason, ok := relayOKFalseReason(err); ok {
			return RelayPublishResult{RelayURL: e.url, Accepted: false, Reason: reason}
		}
		e.discard(conn)
		return RelayPublishResult{RelayURL: e.url, Error: err}
	}
	return RelayPublishResult{RelayURL: e.url, Accepted: true}
}

func (e *goNostrRelayEndpoint) Subscribe(ctx context.Context, filters []nostr.Filter) (relayBusRelaySubscription, error) {
	conn, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	subs, cancel, err := subscribeGoNostrRelay(ctx, conn.relay, filters)
	if err != nil {
		e.release(conn)
		e.discard(conn)
		return nil, err
	}
	var released sync.Once
	return newGoNostrRelaySubscription(ctx, cancel, subs, func() { released.Do(func() { e.release(conn) }) }), nil
}

// subscribeGoNostrRelay sends one REQ per filter. Completion is the relay's real
// EOSE: math.MaxInt64 disables the library's synthetic 7-second EOSE timer.
func subscribeGoNostrRelay(ctx context.Context, relay *nostr.Relay, filters []nostr.Filter) ([]*nostr.Subscription, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(ctx)
	subs := make([]*nostr.Subscription, 0, len(filters))
	for _, filter := range filters {
		sub, err := relay.Subscribe(ctx, filter, nostr.SubscriptionOptions{MaxWaitForEOSE: time.Duration(math.MaxInt64)})
		if err != nil {
			cancel()
			for _, existing := range subs {
				existing.Unsub()
			}
			return nil, nil, err
		}
		subs = append(subs, sub)
	}
	return subs, cancel, nil
}

// Authenticate runs the NIP-42 handshake described on goNostrRelayEndpoint. It
// returns nil without a new handshake when the current connection is already
// authenticated, or when probe is nil and the current connection was already
// probed and the relay did not challenge it.
func (e *goNostrRelayEndpoint) Authenticate(ctx context.Context, signer relayAuthSigner, probe *relayAuthProbe) error {
	if signer == nil {
		return fmt.Errorf("relay auth signer is required")
	}
	select {
	case e.handshake <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-e.handshake }()

	e.mu.Lock()
	current := e.conn
	settled := current != nil && current.relay.IsConnected() && (current.authenticated || (probe == nil && current.probed))
	e.mu.Unlock()
	if settled {
		return nil
	}

	hsCtx, cancel := boundRelayBusWait(ctx, relayBusAuthHandshakeTimeout)
	defer cancel()
	relay, err := dialGoNostrRelay(hsCtx, e.url)
	if err != nil {
		return err
	}
	rejected, err := runRelayAuthProbe(hsCtx, relay, probe)
	if err != nil {
		// Some probe frame is unanswered, so an AUTH frame may still be on its
		// way to the reader. Relay.Auth must not run on this connection.
		_ = relay.Close()
		return fmt.Errorf("NIP-42 probe: %w", err)
	}
	authenticated := false
	sign := func(authCtx context.Context, event *nostr.Event) error { return signer.Sign(authCtx, event) }
	switch err := relay.Auth(hsCtx, sign); {
	case err == nil:
		authenticated = true
	case strings.Contains(err.Error(), "no challenge") && rejected == "":
		// The relay accepted the probe and never challenged; nothing to answer.
	case strings.Contains(err.Error(), "no challenge"):
		_ = relay.Close()
		return fmt.Errorf("relay answered %q without sending a NIP-42 challenge", rejected)
	default:
		_ = relay.Close()
		return err
	}

	e.mu.Lock()
	previous := e.conn
	e.conn = e.trackLocked(&goNostrRelayConn{relay: relay, authenticated: authenticated, probed: true})
	e.retireLocked(previous)
	e.mu.Unlock()
	return nil
}

// relayAuthBarrierFilter is the probe for a proactive handshake. Any answer,
// EOSE or CLOSED, proves the relay's connect-time challenge (if any) was read.
var relayAuthBarrierFilter = nostr.Filter{Kinds: []nostr.Kind{nostr.KindClientAuthentication}, LimitZero: true}

// runRelayAuthProbe sends the probe frames on a private connection and returns
// once the relay has answered all of them. rejected is the first
// "auth-required:" answer, if any. A non-nil error means some frame is
// unanswered.
func runRelayAuthProbe(ctx context.Context, relay *nostr.Relay, probe *relayAuthProbe) (rejected string, err error) {
	if probe != nil && probe.Event != nil {
		err := relay.Publish(ctx, *probe.Event)
		if err == nil {
			return "", nil
		}
		reason, answered := relayOKFalseReason(err)
		if !answered {
			return "", err
		}
		if isRelayAuthRequired(reason) {
			return reason, nil
		}
		return "", nil
	}

	filters := []nostr.Filter{relayAuthBarrierFilter}
	if probe != nil && len(probe.Filters) > 0 {
		// Replay the rejected REQs without asking for stored events.
		filters = make([]nostr.Filter, len(probe.Filters))
		for i, filter := range probe.Filters {
			filter.Limit = 0
			filter.LimitZero = true
			filters[i] = filter
		}
	}
	subs, cancel, err := subscribeGoNostrRelay(ctx, relay, filters)
	if err != nil {
		return "", err
	}
	defer func() {
		cancel()
		for _, sub := range subs {
			sub.Unsub()
		}
	}()
	for _, sub := range subs {
		select {
		case <-sub.EndOfStoredEvents:
		case reason := <-sub.ClosedReason:
			if rejected == "" && isRelayAuthRequired(reason) {
				rejected = strings.TrimSpace(reason)
			}
		case <-sub.Context.Done():
			// The library ends a subscription itself only when the connection
			// drops; its CLOSED, if any, is still delivered first.
			select {
			case reason := <-sub.ClosedReason:
				if rejected == "" && isRelayAuthRequired(reason) {
					rejected = strings.TrimSpace(reason)
				}
			default:
				return "", fmt.Errorf("subscription ended before the relay answered: %w", context.Cause(sub.Context))
			}
		case <-ctx.Done():
			return "", context.Cause(ctx)
		}
	}
	return rejected, nil
}

// Close ends every connection, including retired ones still serving
// subscriptions.
func (e *goNostrRelayEndpoint) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for conn := range e.open {
		e.closeLocked(conn)
	}
	e.conn = nil
}

// dialGoNostrRelay connects without RelayOptions.AuthHandler (see
// goNostrRelayEndpoint) and releases the library's connection watcher when the
// dial fails.
func dialGoNostrRelay(ctx context.Context, url string) (*nostr.Relay, error) {
	relay, err := nostr.RelayConnect(ctx, url, nostr.RelayOptions{})
	if err != nil {
		if relay != nil {
			_ = relay.Close()
		}
		return nil, err
	}
	return relay, nil
}

// acquire returns the current connection, dialing a plain one when there is
// none, and counts the caller as a user until release.
func (e *goNostrRelayEndpoint) acquire(ctx context.Context) (*goNostrRelayConn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn == nil || !e.conn.relay.IsConnected() {
		relay, err := dialGoNostrRelay(ctx, e.url)
		if err != nil {
			return nil, err
		}
		e.retireLocked(e.conn)
		e.conn = e.trackLocked(&goNostrRelayConn{relay: relay})
	}
	e.conn.users++
	return e.conn, nil
}

func (e *goNostrRelayEndpoint) release(conn *goNostrRelayConn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	conn.users--
	if conn.retired && conn.users <= 0 {
		e.closeLocked(conn)
	}
}

// discard drops a connection that failed a write or REQ; it is not reused.
func (e *goNostrRelayEndpoint) discard(conn *goNostrRelayConn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn == conn {
		e.conn = nil
	}
	conn.retired = true
	e.closeLocked(conn)
}

// retireLocked stops handing conn out and closes it once unused.
func (e *goNostrRelayEndpoint) retireLocked(conn *goNostrRelayConn) {
	if conn == nil || conn.retired {
		return
	}
	conn.retired = true
	if conn.users <= 0 {
		e.closeLocked(conn)
	}
}

func (e *goNostrRelayEndpoint) trackLocked(conn *goNostrRelayConn) *goNostrRelayConn {
	e.open[conn] = struct{}{}
	return conn
}

func (e *goNostrRelayEndpoint) closeLocked(conn *goNostrRelayConn) {
	delete(e.open, conn)
	_ = conn.relay.Close()
}

type goNostrRelaySubscription struct {
	cancel  context.CancelFunc
	release func()
	subs    []*nostr.Subscription
	events  chan *nostr.Event
	eose    chan struct{}
	closed  chan string
}

func newGoNostrRelaySubscription(ctx context.Context, cancel context.CancelFunc, subs []*nostr.Subscription, release func()) *goNostrRelaySubscription {
	s := &goNostrRelaySubscription{
		cancel:  cancel,
		release: release,
		subs:    subs,
		events:  make(chan *nostr.Event, 64),
		eose:    make(chan struct{}),
		closed:  make(chan string, len(subs)),
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
	if s.release != nil {
		s.release()
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
