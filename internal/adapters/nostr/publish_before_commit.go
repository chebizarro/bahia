package nostr

import (
	"context"
	"errors"
	"fmt"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
)

// PublishBeforeCommit delivers a signed event for a producer that commits its
// own state only once the publish quorum has accepted the event, such as the
// relay-first registry. Such a producer abandons its write
// when the quorum is not met, so nothing may remain queued to be delivered
// later. The order is therefore reversed from PublishProjection, which makes
// the event durable before the first round:
//
//  1. One round goes to every write relay of this publisher's pool.
//  2. Below the publish quorum it returns an error and nothing is enqueued.
//     Relays that did accept keep the event, as with any partial publish.
//  3. At the quorum the event is admitted to the outbox with that round's
//     per-relay results: accepted and permanently rejected relays are
//     recorded as such, and the round is counted. The runner then retries
//     only the relays that have not accepted, with the usual backoff and
//     attempt budget, and the entry settles like any other.
//
// A nil error means the quorum accepted and the event is durably tracked. An
// error after the quorum accepted means the outbox could not record it; the
// caller must treat the publish as failed.
func (p *Publisher) PublishBeforeCommit(ctx context.Context, ev nostr.Event, entityType string, entityID *uuid.UUID) error {
	if p == nil {
		return errors.New("nostr publisher not configured")
	}
	if !ev.CheckID() || !ev.VerifySignature() {
		return fmt.Errorf("nostr event %s has an invalid id or signature", ev.ID.Hex())
	}
	if p.publishFn == nil {
		return errors.New("relay publisher is not configured")
	}
	eventID := ev.ID.Hex()
	configured := normalizeRelayURLs(p.relayURLs())
	if len(configured) == 0 {
		return fmt.Errorf("publish nostr event %s: no write relays configured", eventID)
	}
	required := p.requiredAcceptances(len(configured))

	d := p.newDelivery(ev, 0)
	d.syncRelays(configured)
	results, callErr := p.publishFn(ctx, ev, configured)
	if countable, _, _ := d.applyRound(configured, results, callErr); countable {
		d.rounds = 1
	}
	accepted := d.acceptedCount(configured)
	detail := d.failureDetail(configured)
	if accepted < required {
		if callErr != nil && len(results) == 0 {
			detail = joinDetail(callErr.Error(), detail)
		}
		return fmt.Errorf("nostr event %s accepted by %d of %d required relays; not queued: %s", eventID, accepted, required, detail)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	// Tracked before it is durable, as in enqueueAndDeliver, so a concurrent
	// runner discovery pass skips the entry instead of starting a second
	// delivery.
	p.deliveriesMu.Lock()
	if _, tracked := p.deliveries[eventID]; tracked {
		p.deliveriesMu.Unlock()
		if p.localOutbox != nil {
			proof, found, err := p.localOutbox.GetDeliveryProof(ev.ID)
			if err != nil {
				return fmt.Errorf("nostr event %s duplicate delivery proof: %w", eventID, err)
			}
			if found && proof.ValidFor(ev, p.target) {
				return nil
			}
		}
		return fmt.Errorf("nostr event %s is already publishing without durable quorum proof", eventID)
	}
	p.deliveries[eventID] = d
	p.deliveriesMu.Unlock()

	d.delivered = true
	d.policy = localstore.DeliveryPolicy{WriteRelays: configured, Required: required}
	d.quorumReached.Store(true)
	if err := p.admit(ctx, ev, entityType, entityID, d); err != nil {
		p.forgetDelivery(d)
		return fmt.Errorf("nostr event %s accepted by %d relays but not recorded for redelivery: %w", eventID, accepted, err)
	}
	settled := len(d.retryableRelays(configured)) == 0
	if err := p.persistRound(ctx, d, true, settled, false, detail, d.policy); err != nil {
		// The enqueued event remains retryable, but the caller must not commit
		// producer state until the quorum proof is durable.
		p.forgetDelivery(d)
		return fmt.Errorf("nostr event %s reached quorum but delivery proof was not recorded: %w", eventID, err)
	}
	d.settled = settled
	d.reportedDelivered = true
	p.notifyDelivered(ev)
	if settled || !p.running.Load() {
		// Without an active runner the pending entry, with its per-relay
		// state, is resumed by this target's runner discovery.
		p.forgetDelivery(d)
	} else {
		p.scheduleDelivery(d, p.now().Add(d.backoff.Next()))
		p.nudge()
	}
	p.logRound(d, deliveryReport{accepted: accepted, required: required, delivered: true, settled: settled}, detail)
	return nil
}
