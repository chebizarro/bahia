package nostr

import (
	"errors"
	"strings"
)

// closedRetryBudget applies the pool's CLOSED policy (ClassifyClosedReason,
// bounded by WithRetryableClosedBudget) to the successive REQs one caller
// issues for one filter on one relay. The pool's subscription workers use it,
// and so does the inbound sync, which drives its own single-relay REQs, so a
// relay that keeps closing a REQ is given up on the same way everywhere
// .
type closedRetryBudget struct {
	max         int
	retryable   int
	authRetried bool
}

func (p *RelayPool) newClosedRetryBudget() *closedRetryBudget {
	return &closedRetryBudget{max: p.maxRetryableClosedRetries}
}

// served records an EOSE: the relay serves the filter, so the count of
// CLOSEDs in a row starts over.
func (b *closedRetryBudget) served() {
	b.retryable = 0
	b.authRetried = false
}

// closedVerdict is what to do with one CLOSED.
type closedVerdict struct {
	// Action is ClosedAuthenticate (authenticate, then reissue at once; if
	// AUTH fails, give up), ClosedRetry (reissue after a backoff) or
	// ClosedTerminal (give up on the REQ for good).
	Action ClosedAction
	// Exhausted is set when a retryable CLOSED exceeded the budget, which
	// makes it terminal.
	Exhausted bool
}

// closed classifies one CLOSED reason against the REQ's history. An
// "auth-required:" is answered once in a row: if the relay still refuses
// after a successful AUTH, retrying cannot help. A retryable reason is
// reissued at most max times in a row.
func (b *closedRetryBudget) closed(reason string) closedVerdict {
	switch action := ClassifyClosedReason(reason); action {
	case ClosedAuthenticate:
		if b.authRetried {
			return closedVerdict{Action: ClosedTerminal}
		}
		b.authRetried = true
		return closedVerdict{Action: ClosedAuthenticate}
	case ClosedRetry:
		b.retryable++
		if b.retryable > b.max {
			return closedVerdict{Action: ClosedTerminal, Exhausted: true}
		}
		return closedVerdict{Action: ClosedRetry}
	default:
		return closedVerdict{Action: ClosedTerminal}
	}
}

// recordClosedRetryExhausted counts a give-up on a relay that kept closing a
// REQ with retryable reasons (bahia_nostr_relay_closed_retry_exhausted_total).
func (p *RelayPool) recordClosedRetryExhausted(relayURL string) {
	if p.health == nil {
		return
	}
	p.health.GetOrCreate(relayURL).RecordClosedRetryExhausted()
}

// ErrSubscriptionGaveUp matches every *SubscriptionGaveUpError.
var ErrSubscriptionGaveUp = errors.New("relays refused the subscription for good")

// SubscriptionGaveUpError reports a subscription the pool stopped reissuing
// for good on at least one relay: a policy refusal, a failed NIP-42 AUTH or an
// exhausted CLOSED retry budget (see ClassifyClosedReason). Opening the same
// subscription again would only repeat those refusals with a fresh budget.
type SubscriptionGaveUpError struct {
	// Closed holds each such relay's terminal CLOSED, in the order received.
	Closed []RelayClosed
}

func (e *SubscriptionGaveUpError) Error() string {
	parts := make([]string, 0, len(e.Closed))
	for _, closed := range e.Closed {
		parts = append(parts, closed.RelayURL+": "+closed.Reason)
	}
	return ErrSubscriptionGaveUp.Error() + ": " + strings.Join(parts, "; ")
}

func (e *SubscriptionGaveUpError) Unwrap() error { return ErrSubscriptionGaveUp }

// GaveUp returns a *SubscriptionGaveUpError when the pool stopped reissuing
// one of the subscription's REQs for good on some relay, and nil otherwise.
// The record is made before EndOfStoredEvents can close on that relay's
// answer and before Events closes, so a caller that sees either can rely on
// it. A caller that opens the subscription again once Events closes must check
// GaveUp first: resubscribing would sidestep the pool's give-up.
func (m *MergedSubscription) GaveUp() error {
	if m == nil {
		return nil
	}
	if m.active != nil {
		return m.active.gaveUpError()
	}
	if m.gaveUp != nil {
		return m.gaveUp()
	}
	return nil
}

// recordTerminal notes that the pool will not reissue one of relayURL's REQs
// again. It runs before that REQ's answer is settled, so EndOfStoredEvents
// never closes ahead of the record.
func (s *activeMergedSubscription) recordTerminal(info RelayClosed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if outcome := s.outcomes[info.RelayURL]; outcome != nil {
		outcome.Terminal = true
	}
	for _, existing := range s.terminal {
		if existing.RelayURL == info.RelayURL {
			return
		}
	}
	s.terminal = append(s.terminal, info)
}

func (s *activeMergedSubscription) gaveUpError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.terminal) == 0 {
		return nil
	}
	return &SubscriptionGaveUpError{Closed: append([]RelayClosed(nil), s.terminal...)}
}
