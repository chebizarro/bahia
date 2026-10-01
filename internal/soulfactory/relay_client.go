package soulfactory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

// SoulFactory relay I/O runs on the shared relay pool
// (internal/adapters/nostr.RelayPool, bahia-irsry.10.2). The pool owns the
// protocol: connections, NIP-42 through the library AuthHandler, per-relay
// EOSE/CLOSED accounting, CLOSED classification, per-relay re-REQ with a
// resume cursor, NIP-11 limits and per-relay publish results. RelayClient
// adds only SoulFactory's caller policy on top: the publish quorum, the
// per-caller read policies (RelayReadPolicy) and slog logging.

// RelayPublishResult is one relay's OK outcome for a published event.
// Accepted corresponds to the NIP-01 OK accepted flag; OK false is not a
// successful publish even when the relay supplied a reason.
type RelayPublishResult = nostradapter.PublishResult

// RelayStoredEventsOutcome is one relay's answer to a subscription's initial
// REQs.
type RelayStoredEventsOutcome = nostradapter.RelayStoredOutcome

// RelayReadIncompleteError reports a stored-event read that ended without
// EOSE from every relay; it is never a complete result, and it matches
// nostradapter.ErrStoredEventsIncomplete.
type RelayReadIncompleteError = nostradapter.StoredEventsIncompleteError

// relayStoredEventsTimeout bounds a stored-event read whose caller context
// has no deadline. An earlier caller deadline always wins.
const relayStoredEventsTimeout = nostradapter.DefaultStoredEventsTimeout

// boundRelayWait returns ctx bounded by limit unless ctx already carries a
// deadline, which the caller owns.
func boundRelayWait(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	return nostradapter.BoundStoredEventsWait(ctx, limit)
}

type relayAuthSigner interface {
	Sign(context.Context, *nostr.Event) error
}

// Relay publish quorum values for WithRelayPublishQuorum.
const (
	// RelayPublishQuorumDefault succeeds once one relay has accepted.
	RelayPublishQuorumDefault = 1
	// RelayPublishQuorumAll succeeds only once every relay has accepted.
	RelayPublishQuorumAll = -1
)

// RelayClientOption configures a RelayClient.
type RelayClientOption func(*RelayClient)

// WithRelaySigner signs NIP-42 AUTH events for the client's pool.
func WithRelaySigner(signer relayAuthSigner) RelayClientOption {
	return func(c *RelayClient) { c.signer = signer }
}

// WithRelayLogger sets the logger for the client and its pool.
func WithRelayLogger(logger *slog.Logger) RelayClientOption {
	return func(c *RelayClient) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// withRelayPublishQuorum sets how many relays must accept (OK true or
// duplicate) a published event for Publish to succeed: N > 0 relays (capped
// at the relay count) or RelayPublishQuorumAll. Every relay's result is
// collected regardless of the quorum.
func withRelayPublishQuorum(quorum int) RelayClientOption {
	return func(c *RelayClient) {
		if quorum > 0 || quorum == RelayPublishQuorumAll {
			c.publishQuorum = quorum
		}
	}
}

// withRelayEventValidator replaces the signed-event check applied to every
// delivered event.
func withRelayEventValidator(validator func(*nostr.Event) bool) RelayClientOption {
	return func(c *RelayClient) {
		if validator != nil {
			c.validateEvent = validator
		}
	}
}

// withRelayResubscribeBackoff paces the pool's per-relay REQ reissues.
func withRelayResubscribeBackoff(newBackoff func() *nostradapter.Backoff) RelayClientOption {
	return func(c *RelayClient) { c.resubscribeBackoff = newBackoff }
}

// RelayClient is SoulFactory's handle on a relay pool for one relay set.
type RelayClient struct {
	pool               *nostradapter.RelayPool
	relays             []string
	signer             relayAuthSigner
	logger             *slog.Logger
	validateEvent      func(*nostr.Event) bool
	publishQuorum      int
	resubscribeBackoff func() *nostradapter.Backoff
}

// NewRelayClient returns a client over a new relay pool for relays. The pool
// connects lazily, on the first publish, read or AUTH.
func NewRelayClient(relays []string, opts ...RelayClientOption) (*RelayClient, error) {
	relays = normalizeSoulRelays(relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("at least one SoulFactory relay is required")
	}
	c := &RelayClient{
		logger:        slog.Default().With("component", "soulfactory-relays"),
		validateEvent: validSignedEvent,
		publishQuorum: RelayPublishQuorumDefault,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	poolOpts := []nostradapter.RelayPoolOption{}
	if c.signer != nil {
		poolOpts = append(poolOpts, nostradapter.WithAuthSignFunc(c.signer.Sign))
	}
	if c.resubscribeBackoff != nil {
		poolOpts = append(poolOpts, nostradapter.WithResubscribeBackoff(c.resubscribeBackoff))
	}
	c.pool = nostradapter.NewRelayPool(relays, newSlogZapLogger(c.logger), poolOpts...)
	c.relays = c.pool.URLs()
	return c, nil
}

// Relays returns the client's normalized relay URLs.
func (c *RelayClient) Relays() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.relays...)
}

// holds reports whether relay is one of the client's relays.
func (c *RelayClient) holds(relay string) bool {
	normalized := nostr.NormalizeURL(relay)
	for _, url := range c.relays {
		if url == normalized {
			return true
		}
	}
	return false
}

func (c *RelayClient) log() *slog.Logger {
	if c != nil && c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// Publish sends ev to every relay and waits for each relay's OK. It returns
// the number of relays that accepted (OK true or duplicate) and an error only
// when fewer than the publish quorum accepted (default one relay). One
// relay's OK never cancels the publishes still in flight to others; a relay
// that answers "auth-required:" is authenticated and asked once more.
func (c *RelayClient) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	results, err := c.PublishWithResults(ctx, ev)
	return countRelayAccepted(results), err
}

// PublishWithResults sends ev to every relay concurrently and returns every
// relay's outcome, in relay order, once all relays have answered (or failed).
// The error is non-nil only when fewer than the publish quorum accepted.
func (c *RelayClient) PublishWithResults(ctx context.Context, ev nostr.Event) ([]RelayPublishResult, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("soul factory relay client is not configured")
	}
	results, _ := c.pool.PublishWithResults(ctx, ev)
	accepted := countRelayAccepted(results)
	required := c.requiredAcceptances()
	failures := relayPublishFailures(results)
	if accepted >= required {
		if len(failures) > 0 {
			c.log().Warn("event accepted by publish quorum; some relays did not accept",
				"event_id", ev.ID.Hex(), "accepted", accepted, "relays", len(c.relays), "failures", strings.Join(failures, "; "))
		}
		return results, nil
	}
	if accepted == 0 {
		return results, fmt.Errorf("event was not accepted by any relay: %s", strings.Join(failures, "; "))
	}
	return results, fmt.Errorf("event accepted by %d of %d required relays: %s", accepted, required, strings.Join(failures, "; "))
}

// publishTo sends ev to the named relays of the client, concurrently, and
// requires every one of them to accept. The error names the first relay, in
// the given order, that did not.
func (c *RelayClient) publishTo(ctx context.Context, relays []string, ev nostr.Event) error {
	results, _ := c.pool.PublishToRelaysWithResults(ctx, ev, relays)
	byRelay := make(map[string]RelayPublishResult, len(results))
	for _, result := range results {
		byRelay[result.RelayURL] = result
	}
	for _, relay := range relays {
		result, ok := byRelay[nostr.NormalizeURL(relay)]
		if !ok {
			return fmt.Errorf("%s: relay is not configured on the SoulFactory relay client", relay)
		}
		if relayAccepted(result) {
			continue
		}
		if result.Error != nil {
			return fmt.Errorf("%s: %w", relay, result.Error)
		}
		reason := strings.TrimSpace(result.Reason)
		if reason == "" {
			reason = "OK false"
		}
		return fmt.Errorf("%s: %s", relay, reason)
	}
	return nil
}

// requiredAcceptances resolves the publish quorum against the relay count.
func (c *RelayClient) requiredAcceptances() int {
	required := c.publishQuorum
	switch {
	case required == RelayPublishQuorumAll:
		required = len(c.relays)
	case required <= 0:
		required = RelayPublishQuorumDefault
	}
	return min(required, len(c.relays))
}

func relayPublishFailures(results []RelayPublishResult) []string {
	var failures []string
	for _, result := range results {
		if relayAccepted(result) {
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
	return failures
}

// relayAccepted treats a duplicate OK as acceptance: the relay has the event.
func relayAccepted(result RelayPublishResult) bool {
	return result.Accepted || (result.Error == nil && result.IsDuplicate())
}

func countRelayAccepted(results []RelayPublishResult) int {
	accepted := 0
	for _, result := range results {
		if relayAccepted(result) {
			accepted++
		}
	}
	return accepted
}

// Authenticate completes NIP-42 on every relay that challenges, before
// reads or writes that need authenticated relay state. A relay that does not
// challenge is left unauthenticated; its later "auth-required:" answers are
// handled per REQ and per publish by the pool.
func (c *RelayClient) Authenticate(ctx context.Context) error {
	return c.authenticateRelays(ctx, nil)
}

func (c *RelayClient) authenticateRelays(ctx context.Context, relays []string) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("soul factory relay client is not configured")
	}
	if c.signer == nil {
		return fmt.Errorf("soul factory relay auth signer is not configured")
	}
	hsCtx, cancel := boundRelayWait(ctx, relayAuthHandshakeTimeout)
	defer cancel()
	return c.pool.AuthenticateRelays(hsCtx, relays)
}

// relayAuthHandshakeTimeout bounds one proactive NIP-42 handshake (connect,
// barrier answer, AUTH OK) when the caller context has no earlier deadline.
const relayAuthHandshakeTimeout = 10 * time.Second

// SubscribeAllWithEOSE opens filters on every relay. EndOfStoredEvents closes
// once every relay has answered with EOSE or CLOSED (relays that cannot be
// reached, or drop, stay pending while the pool retries them); Events stays
// open for realtime events until the subscription is closed.
func (c *RelayClient) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*RelaySubscription, error) {
	return c.subscribe(ctx, filters, 0)
}

// subscribeResumable is SubscribeAllWithEOSE for a long-lived subscription
// that must not lose events across reconnects: each relay REQ keeps a resume
// cursor, so a REQ reissued after a dropped connection or a retryable CLOSED
// asks only for events since the newest one that relay delivered, less
// overlap. Deduplication and idempotent handlers absorb the overlap.
func (c *RelayClient) subscribeResumable(ctx context.Context, filters []nostr.Filter, overlap time.Duration) (*RelaySubscription, error) {
	if overlap <= 0 {
		return nil, fmt.Errorf("resumable subscription overlap must be positive")
	}
	return c.subscribe(ctx, filters, overlap)
}

func (c *RelayClient) subscribe(ctx context.Context, filters []nostr.Filter, overlap time.Duration) (*RelaySubscription, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("soul factory relay client is not configured")
	}
	if len(filters) == 0 {
		return nil, fmt.Errorf("at least one Nostr filter is required")
	}
	merged, err := c.pool.SubscribeWithOptions(ctx, filters, nostradapter.SubscribeOptions{
		AwaitUnavailableRelays: true,
		ResumeOverlap:          overlap,
		ValidateEvent:          c.validateEvent,
	})
	if err != nil {
		return nil, err
	}
	go c.watchProtocolFrames(merged)
	return &RelaySubscription{Events: merged.Events, EndOfStoredEvents: merged.EndOfStoredEvents, closeFn: merged.Close, merged: merged}, nil
}

// watchProtocolFrames logs and records the subscription's CLOSED frames and
// drains its per-relay EOSE notices until the subscription ends.
func (c *RelayClient) watchProtocolFrames(merged *nostradapter.MergedSubscription) {
	eose, closed := merged.RelayEOSE, merged.Closed
	for eose != nil || closed != nil {
		select {
		case _, ok := <-eose:
			if !ok {
				eose = nil
			}
		case info, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			c.pool.RecordRelayClosed(info.RelayURL, info.Reason)
			c.log().Warn("relay closed subscription", "relay", info.RelayURL, "reason", info.Reason, "terminal", info.Terminal)
		}
	}
}

// Query returns the stored events matching filters once every relay has
// answered with EOSE or CLOSED, bounded by ctx (or relayStoredEventsTimeout
// when ctx has no deadline). A nil error means every relay sent EOSE.
// Anything else is a *RelayReadIncompleteError; the events returned with it
// are partial.
func (c *RelayClient) Query(ctx context.Context, filters []nostr.Filter) ([]*nostr.Event, error) {
	sub, err := c.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	return sub.CollectStoredEvents(ctx)
}

// Close closes the client's pool and every subscription on it.
func (c *RelayClient) Close() {
	if c != nil && c.pool != nil {
		c.pool.Close()
	}
}

// RelaySubscription is a merged subscription on the client's relays.
// EndOfStoredEvents closes once every relay has answered the initial REQs
// with a terminal frame, EOSE or CLOSED, or once the subscription ends.
// Closing is not completeness: StoredEventsIncomplete reports relays that
// CLOSED or never answered. Events stays open for realtime events until the
// subscription is closed.
type RelaySubscription struct {
	Events            <-chan *nostr.Event
	EndOfStoredEvents <-chan struct{}
	closeFn           func()
	merged            *nostradapter.MergedSubscription
}

// Close ends the subscription on every relay.
func (s *RelaySubscription) Close() {
	if s != nil && s.closeFn != nil {
		s.closeFn()
	}
}

// StoredEventsOutcome returns each relay's answer to the initial REQs so far.
// It is nil for subscriptions that were not opened on a relay pool.
func (s *RelaySubscription) StoredEventsOutcome() []RelayStoredEventsOutcome {
	if s == nil || s.merged == nil {
		return nil
	}
	return s.merged.StoredOutcomes()
}

// StoredEventsIncomplete returns nil when every relay has sent EOSE and
// otherwise a *RelayReadIncompleteError naming the relays that CLOSED or have
// not answered. cause is the reason the caller stopped waiting (a context
// error), or nil when the caller saw EndOfStoredEvents.
func (s *RelaySubscription) StoredEventsIncomplete(cause error) error {
	if s == nil || s.merged == nil {
		if cause == nil {
			return nil
		}
		return &RelayReadIncompleteError{Cause: cause}
	}
	return s.merged.StoredEventsIncomplete(cause)
}

// CollectStoredEvents gathers the subscription's stored events until every
// relay has answered with EOSE or CLOSED, or until ctx ends (bounded by
// relayStoredEventsTimeout without a deadline). The result is complete only
// when the error is nil; otherwise the error is a *RelayReadIncompleteError
// (matching ErrRelayReadIncomplete, and the context error when the wait timed
// out) and the events are the partial set received before the wait ended.
func (s *RelaySubscription) CollectStoredEvents(ctx context.Context) ([]*nostr.Event, error) {
	if s == nil {
		return nil, fmt.Errorf("relay subscription is nil")
	}
	waitCtx, cancel := boundRelayWait(ctx, relayStoredEventsTimeout)
	defer cancel()
	var out []*nostr.Event
	for {
		select {
		case <-waitCtx.Done():
			return out, s.StoredEventsIncomplete(context.Cause(waitCtx))
		case <-s.EndOfStoredEvents:
			// A relay's stored events are delivered before it is settled, but
			// both channels can be ready when this wakes up. Drain what is
			// already buffered so a final stored event is not lost to
			// select's random choice.
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
				// Events closes only once every REQ has stopped: the caller
				// closed the subscription, or every relay refused it for good.
				return out, s.StoredEventsIncomplete(ctx.Err())
			}
			out = append(out, ev)
		}
	}
}
