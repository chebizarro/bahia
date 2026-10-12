package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
)

// pending is an event to bring to quorum and the relays known to hold it.
type pending struct {
	ev   nostr.Event
	held map[string]bool
}

func fresh(events []nostr.Event) []pending {
	out := make([]pending, len(events))
	for i, ev := range events {
		out[i] = pending{ev: ev, held: map[string]bool{}}
	}
	return out
}

// publishInOrder brings each event, in order, to quorum relays inside bounded
// bulk admission operations, so they are paced by the bulk lane instead of
// refused by a burst. The first event that does not reach quorum after
// maxAttempts stops the run: a later checkpoint names its predecessor, and a
// re-run resumes on what quorum relays hold.
func publishInOrder(ctx context.Context, pool *nostradapter.RelayPool, admission *nostrout.Admission, work []pending, quorum, maxAttempts int, wait func(context.Context, time.Duration) error) (int, error) {
	chunk := max(1, 2048/maxAttempts)
	published := 0
	for start := 0; start < len(work); start += chunk {
		batch := work[start:min(start+chunk, len(work))]
		op, err := admission.BeginOperation(ctx, nostrout.OperationSpec{MaxEvents: len(batch) * maxAttempts})
		if err != nil {
			return published, fmt.Errorf("admit %d events: %w", len(batch), err)
		}
		opCtx := nostrout.WithOperation(ctx, op)
		for _, item := range batch {
			if err := toQuorum(opCtx, pool, item, quorum, maxAttempts, wait); err != nil {
				op.Close()
				return published, err
			}
			published++
		}
		op.Close()
	}
	return published, nil
}

// toQuorum publishes item to the relays that do not hold it until quorum
// relays hold it (accepted, or answered that they already have it).
func toQuorum(ctx context.Context, pool *nostradapter.RelayPool, item pending, quorum, maxAttempts int, wait func(context.Context, time.Duration) error) error {
	held := map[string]bool{}
	for relay := range item.held {
		held[nostrout.NormalizeRelayURL(relay)] = true
	}
	backoff := nostradapter.DefaultBackoff()
	for attempt := 1; ; attempt++ {
		if len(held) >= quorum {
			return nil
		}
		var targets []string
		for _, relay := range pool.URLs() {
			if !held[nostrout.NormalizeRelayURL(relay)] {
				targets = append(targets, relay)
			}
		}
		results, err := pool.PublishToRelaysWithResults(ctx, item.ev, targets)
		if errors.Is(err, nostrout.ErrKillSwitch) {
			return err
		}
		var reasons []string
		for _, result := range results {
			if result.Succeeded() {
				held[nostrout.NormalizeRelayURL(result.RelayURL)] = true
				continue
			}
			reason := result.Reason
			if result.Error != nil {
				reason = result.Error.Error()
			}
			reasons = append(reasons, result.RelayURL+": "+reason)
		}
		if len(held) >= quorum {
			return nil
		}
		if err != nil && len(reasons) == 0 {
			reasons = append(reasons, err.Error())
		}
		if attempt >= maxAttempts {
			return fmt.Errorf("event %s (kind %d) is held by %d relays, quorum %d, after %d attempts: %s", item.ev.ID.Hex(), item.ev.Kind, len(held), quorum, attempt, strings.Join(reasons, "; "))
		}
		if err := wait(ctx, backoff.Next()); err != nil {
			return err
		}
	}
}
