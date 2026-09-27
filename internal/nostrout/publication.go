package nostrout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
)

// ErrNoDestinations means a publication named no relay destinations.
var ErrNoDestinations = errors.New("nostr outbound publication has no relay destinations")

// Publication is the permit for one logical EVENT publication. Begin consumes
// one logical-event token when at least one destination still needs the event;
// BeforeAttempt must be called immediately before every raw EVENT frame and
// consumes one per-relay wire token (so a NIP-42 AUTH retry is budgeted as a
// second frame but not as a second logical event); Observe feeds each relay
// outcome back into the breaker and receipt cache; Close releases in-flight
// bookkeeping (never spent tokens) and is idempotent.
type Publication struct {
	a          *Admission
	op         *Operation
	eventID    string
	purpose    Purpose
	relays     []string
	cached     []Result
	generation uint64
	registered bool
	closed     bool
	// waitDeadline is zero for fail-fast publications. Operation and waiting
	// publications pace instead of failing, but never beyond this instant.
	waitDeadline time.Time
}

// Begin admits one ordinary publication of ev to relayURLs. It fails fast: an
// exhausted budget, open circuit, active kill switch, in-flight duplicate, or
// full controller returns an error before any relay I/O so that broken
// reconciliation loops cannot queue work.
func (a *Admission) Begin(ctx context.Context, ev nostr.Event, relayURLs []string) (*Publication, error) {
	if a == nil {
		return nil, ErrNotConfigured
	}
	return a.begin(ctx, nil, false, ev, relayURLs)
}

// BeginWaiting admits one request/response publication (for example a NIP-46
// signer request) whose caller is already blocked awaiting a reply. Instead of
// failing on a momentary burst it waits for its lane, the aggregate budget,
// and an open circuit — bounded by the controller's maximum queue wait (at
// most 30 seconds) and a bounded number of concurrent waiters — and its
// BeforeAttempt calls pace within the same deadline. The kill switch and
// cancellation still end the wait immediately.
func (a *Admission) BeginWaiting(ctx context.Context, ev nostr.Event, relayURLs []string) (*Publication, error) {
	if a == nil {
		return nil, ErrNotConfigured
	}
	return a.begin(ctx, nil, true, ev, relayURLs)
}

func (a *Admission) begin(ctx context.Context, op *Operation, waiting bool, ev nostr.Event, relayURLs []string) (*Publication, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	relays := normalizeRelayList(relayURLs)
	if len(relays) == 0 {
		return nil, ErrNoDestinations
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.metrics.Attempted++
	if op != nil {
		if err := op.claimLocked(); err != nil {
			return nil, err
		}
	}
	if waiting {
		if a.waiters >= a.maxWaiters {
			a.metrics.QueueRejected++
			return nil, fmt.Errorf("%w: %d publications waiting", ErrQueueFull, a.waiters)
		}
		a.waiters++
		defer func() { a.waiters-- }()
	}
	pub, err := a.beginLocked(ctx, op, waiting, ev, relays)
	if op != nil {
		op.finishClaimLocked(pub, err)
	}
	return pub, err
}

func (a *Admission) beginLocked(ctx context.Context, op *Operation, waiting bool, ev nostr.Event, relays []string) (*Publication, error) {
	if err := a.killSwitchLocked(); err != nil {
		return nil, err
	}
	now := a.clock.Now()
	pub := &Publication{a: a, op: op, eventID: eventKeyID(ev), purpose: PurposeForEvent(ev)}
	switch {
	case op != nil:
		pub.purpose = PurposeBulk
		pub.waitDeadline = op.deadline
		if !now.Before(op.deadline) {
			return nil, operationDeadlineError(ErrQueueTimeout)
		}
	case waiting:
		pub.waitDeadline = now.Add(a.maxQueueWait)
	default:
		if a.breakerWaitLocked(now) > 0 {
			return nil, a.circuitErrorLocked()
		}
	}
	a.pruneReceiptsLocked(now)
	for _, relay := range relays {
		if pub.eventID != "" && a.hasReceiptLocked(now, receiptKey{eventID: pub.eventID, relay: relay}) {
			pub.cached = append(pub.cached, Result{RelayURL: relay, Reason: cachedDuplicateReason, Cached: true})
			continue
		}
		pub.relays = append(pub.relays, relay)
	}
	if len(pub.relays) == 0 {
		// Every destination already accepted this exact signed event: no
		// token, no frame.
		a.metrics.Duplicates++
		pub.closed = true
		return pub, nil
	}
	if pub.eventID != "" {
		for _, relay := range pub.relays {
			if _, busy := a.inflight[receiptKey{eventID: pub.eventID, relay: relay}]; busy {
				a.metrics.InFlightRejected++
				return nil, ErrInFlight
			}
		}
	}
	if a.active >= a.maxActive {
		a.metrics.CapacityRejected++
		return nil, fmt.Errorf("%w: %d active publications", ErrCapacity, a.active)
	}

	// Reserve the slot, destination keys, and relay identities before waiting
	// so a paced publication cannot race a concurrent duplicate; every failure
	// below rolls all of it back.
	if err := pub.registerLocked(now); err != nil {
		return nil, err
	}
	var err error
	if pub.waitDeadline.IsZero() {
		if a.laneWaitLocked(now, pub.purpose) > 0 {
			a.metrics.BudgetRejected++
			err = ErrBudgetExceeded
		}
	} else {
		err = a.waitLocked(ctx, pub.waitDeadline, func(now time.Time) (time.Duration, error) {
			if op != nil && op.closed {
				return 0, fmt.Errorf("%w: operation closed while waiting for admission", ErrOperation)
			}
			if err := a.killSwitchLocked(); err != nil {
				return 0, err
			}
			if wait := a.breakerWaitLocked(now); wait > 0 {
				return wait, nil
			}
			return a.laneWaitLocked(now, pub.purpose), nil
		})
		err = pub.waitError(err)
	}
	if err != nil {
		pub.unregisterLocked()
		return nil, err
	}
	pub.generation = a.breakerGeneration
	a.metrics.Admitted++
	return pub, nil
}

func (p *Publication) registerLocked(now time.Time) error {
	a := p.a
	registered := make([]*relayBucket, 0, len(p.relays))
	for _, relay := range p.relays {
		rb, err := a.relayBucketLocked(now, relay)
		if err != nil {
			for _, previous := range registered {
				previous.refs--
			}
			return err
		}
		rb.refs++
		registered = append(registered, rb)
	}
	if p.eventID != "" {
		for _, relay := range p.relays {
			a.inflight[receiptKey{eventID: p.eventID, relay: relay}] = struct{}{}
		}
	}
	a.active++
	p.registered = true
	return nil
}

func (p *Publication) unregisterLocked() {
	if !p.registered {
		return
	}
	a := p.a
	for _, relay := range p.relays {
		if rb, ok := a.relays[relay]; ok && rb.refs > 0 {
			rb.refs--
		}
		if p.eventID != "" {
			delete(a.inflight, receiptKey{eventID: p.eventID, relay: relay})
		}
	}
	a.active--
	p.registered = false
}

func (p *Publication) hasRelay(relay string) bool {
	for _, candidate := range p.relays {
		if candidate == relay {
			return true
		}
	}
	return false
}

// PendingRelays returns the normalized destinations that still need the event.
func (p *Publication) PendingRelays() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.relays...)
}

// NeedsRelay reports whether relayURL is one of the admitted destinations.
func (p *Publication) NeedsRelay(relayURL string) bool {
	return p != nil && p.hasRelay(NormalizeRelayURL(relayURL))
}

// CachedResults returns synthesized duplicate receipts for destinations that
// already accepted this exact signed event. Accepted is false, preserving the
// protocol meaning of that flag; Reason carries the duplicate prefix so
// duplicate-success aggregation applies.
func (p *Publication) CachedResults() []Result {
	if p == nil {
		return nil
	}
	return append([]Result(nil), p.cached...)
}

// Purpose returns the admission lane charged for this publication.
func (p *Publication) Purpose() Purpose {
	if p == nil {
		return ""
	}
	return p.purpose
}

// BeforeAttempt authorizes one raw EVENT frame to relayURL. It must be called
// immediately before every send, including NIP-42 AUTH retries. Ordinary
// publications fail fast; operation publications wait (bounded by the
// operation deadline) for per-relay capacity and for an open circuit to close.
// Tokens spent on an attempted send are never refunded.
func (p *Publication) BeforeAttempt(ctx context.Context, relayURL string) error {
	if p == nil {
		return ErrNotConfigured
	}
	a := p.a
	relay := NormalizeRelayURL(relayURL)
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.closed {
		return fmt.Errorf("%w: publication already closed", ErrOperation)
	}
	if !p.hasRelay(relay) {
		return fmt.Errorf("%w: relay %s was not admitted for this publication", ErrOperation, relay)
	}
	try := func(now time.Time) (time.Duration, error) {
		// Re-validated on every wake-up, before any token is spent: a close
		// while this attempt waited must not let it send.
		if p.closed || (p.op != nil && p.op.closed) {
			return 0, fmt.Errorf("%w: publication closed while waiting to send", ErrOperation)
		}
		if err := a.killSwitchLocked(); err != nil {
			return 0, err
		}
		if wait := a.breakerWaitLocked(now); wait > 0 {
			if p.waitDeadline.IsZero() {
				return 0, a.circuitErrorLocked()
			}
			return wait, nil
		}
		// Registered relays cannot be evicted while this publication holds a
		// reference, and the closed check above runs first.
		wire := a.relays[relay].forPurpose(p.purpose)
		if wait := wire.wait(now); wait > 0 {
			if p.waitDeadline.IsZero() {
				a.metrics.WireRejected++
				return 0, fmt.Errorf("%w: relay %s wire budget", ErrBudgetExceeded, relay)
			}
			return wait, nil
		}
		wire.tokens--
		a.metrics.WireAttempts++
		return 0, nil
	}
	if p.waitDeadline.IsZero() {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := try(a.clock.Now())
		return err
	}
	return p.waitError(a.waitLocked(ctx, p.waitDeadline, try))
}

func (p *Publication) waitError(err error) error {
	if p.op != nil {
		return operationDeadlineError(err)
	}
	if errors.Is(err, ErrQueueTimeout) {
		p.a.metrics.QueueRejected++
	}
	return err
}

// Observe records one relay outcome. It may be called after Close so that
// late fan-out workers still report rate limits and receipts. Every
// rate-limit response opens or extends the shared breaker; a success resets
// backoff only if no newer rate limit intervened.
func (p *Publication) Observe(result Result) {
	if p == nil || result.Cached {
		return
	}
	a := p.a
	relay := NormalizeRelayURL(result.RelayURL)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	if result.IsRateLimited() {
		a.openBreakerLocked(now)
		return
	}
	if !result.Succeeded() {
		return
	}
	if p.eventID != "" && p.hasRelay(relay) {
		a.storeReceiptLocked(now, receiptKey{eventID: p.eventID, relay: relay})
	}
	a.closeBreakerLocked(now, p.generation)
}

// Close releases in-flight bookkeeping. It never refunds spent tokens and is
// idempotent.
func (p *Publication) Close() {
	if p == nil {
		return
	}
	a := p.a
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.unregisterLocked()
	if p.op != nil && p.op.outstanding == p {
		p.op.outstanding = nil
	}
}
