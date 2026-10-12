package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
)

// planDeletions returns unsigned requests for every event the report's scans
// found that no recorded deletion names yet, batchSize events per request,
// ordered by kind, then newest first. Recorded deletions are never re-planned:
// resuming republishes the same signed requests.
func planDeletions(r *report, author nostr.PubKey, batchSize int, createdAt nostr.Timestamp) []deletion {
	covered := make(map[string]struct{})
	for _, del := range r.Deletions {
		for _, target := range del.Targets {
			covered[target.ID] = struct{}{}
		}
	}
	var uncovered []scannedEvent
	for _, ev := range r.targets() {
		if _, ok := covered[ev.ID]; !ok {
			uncovered = append(uncovered, ev)
		}
	}
	sort.Slice(uncovered, func(i, j int) bool {
		a, b := uncovered[i], uncovered[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt > b.CreatedAt
		}
		return a.ID < b.ID
	})
	var planned []deletion
	for batch := range slices.Chunk(uncovered, batchSize) {
		targets := slices.Clone(batch)
		planned = append(planned, deletion{Event: deletionRequest(author, targets, createdAt), Targets: targets, Relays: map[string]*relayOutcome{}})
	}
	return planned
}

// deletionRequest builds the NIP-09 request for targets: one e tag per event
// id and one k tag per kind, nothing else. There is deliberately no way to
// build an a tag here; deletionGuard refuses one anyway.
func deletionRequest(author nostr.PubKey, targets []scannedEvent, createdAt nostr.Timestamp) nostr.Event {
	tags := make(nostr.Tags, 0, len(targets)+len(leakedKinds))
	var kindValues []int
	for _, target := range targets {
		tags = append(tags, nostr.Tag{"e", target.ID})
		if !slices.Contains(kindValues, target.Kind) {
			kindValues = append(kindValues, target.Kind)
		}
	}
	sort.Ints(kindValues)
	for _, kind := range kindValues {
		tags = append(tags, nostr.Tag{"k", strconv.Itoa(kind)})
	}
	return nostr.Event{Kind: nostr.KindDeletion, PubKey: author, CreatedAt: createdAt, Tags: tags}
}

// printPlan writes each request's e and k tags and its relay states.
func printPlan(w io.Writer, recorded, planned []deletion, relays []string) {
	write := func(label string, del deletion) {
		fmt.Fprintf(w, "%s: %d events\n", label, len(del.Targets))
		for _, tag := range del.Event.Tags {
			fmt.Fprintf(w, "  [%q, %q]\n", tag[0], tag[1])
		}
		for _, relay := range relays {
			status := outcomePending
			if outcome := del.Relays[relay]; outcome != nil {
				status = outcome.Status
				if outcome.Reason != "" {
					status += " (" + outcome.Reason + ")"
				}
			}
			fmt.Fprintf(w, "  relay %s: %s\n", relay, status)
		}
	}
	for _, del := range recorded {
		write("deletion "+del.Event.ID.Hex(), del)
	}
	for i, del := range planned {
		write(fmt.Sprintf("new deletion %d (unsigned)", i+1), del)
	}
}

// publisher delivers the report's signed deletions. Each round publishes
// every unfinished request, in order, to every relay that still needs it (the
// pool contacts those relays concurrently), and records every relay's answer
// in the report before the next publish, so an interrupted run resumes where
// it stopped and republishes only what a relay has not accepted. Between
// rounds it backs off. All publications run inside one bounded bulk
// admission operation: they are paced by the bulk lane and wait out the
// shared rate-limit breaker instead of being refused by it.
type publisher struct {
	pool        *nostradapter.RelayPool
	admission   *nostrout.Admission
	guard       deletionGuard
	report      *report
	reportPath  string
	maxAttempts int
	wait        func(context.Context, time.Duration) error
	newBackoff  func() *nostradapter.Backoff
	now         func() time.Time
	out         io.Writer
}

// run delivers to relays for up to maxAttempts rounds. Within this run a
// relay that answers auth-required gets nothing more, and a request a relay
// rejected (blocked:, invalid:, ...) is not offered to it again; a later run
// retries both. rate-limited:, error: and transport failures are retried in
// the next round.
func (p *publisher) run(ctx context.Context, relays []string) error {
	op, err := p.admission.BeginOperation(ctx, nostrout.OperationSpec{MaxEvents: len(p.report.Deletions) * p.maxAttempts})
	if err != nil {
		return fmt.Errorf("admit %d deletion requests x %d attempts: %w (raise --batch-size or lower --max-attempts)", len(p.report.Deletions), p.maxAttempts, err)
	}
	defer op.Close()
	ctx = nostrout.WithOperation(ctx, op)

	stopped := make(map[string]bool)
	rejected := make(map[string]bool)
	backoff := p.newBackoff()
	for round := 1; ; round++ {
		retry := false
		for i := range p.report.Deletions {
			del := &p.report.Deletions[i]
			var targets []string
			for _, relay := range relays {
				if !del.Relays[relay].done() && !stopped[relay] && !rejected[del.Event.ID.Hex()+" "+relay] {
					targets = append(targets, relay)
				}
			}
			if len(targets) == 0 {
				continue
			}
			if err := p.guard.checkSigned(del.Event); err != nil {
				return fmt.Errorf("deletion %s: %w", del.Event.ID.Hex(), err)
			}
			answers, err := publishTo(ctx, p.pool, del.Event, targets)
			if err != nil {
				return err
			}
			for _, relay := range targets {
				answer := answers[relay]
				switch answer.status {
				case outcomeAuthRequired:
					stopped[relay] = true
				case outcomeRejected:
					rejected[del.Event.ID.Hex()+" "+relay] = true
				case outcomeRateLimited, outcomeError:
					retry = true
				}
				p.record(del, relay, answer)
			}
			if err := p.report.save(ctx, p.reportPath); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if !retry || round >= p.maxAttempts {
			return nil
		}
		if err := p.wait(ctx, backoff.Next()); err != nil {
			return err
		}
	}
}

func (p *publisher) record(del *deletion, relay string, answer relayAnswer) {
	outcome := del.Relays[relay]
	outcome.Status = answer.status
	outcome.Reason = p.guard.forbidden.redact(answer.reason)
	outcome.Attempts++
	outcome.UpdatedAt = p.now().UTC()
	line := fmt.Sprintf("relay %s deletion %s: %s", relay, del.Event.ID.Hex(), outcome.Status)
	if outcome.Reason != "" {
		line += " (" + outcome.Reason + ")"
	}
	fmt.Fprintln(p.out, line)
}

type relayAnswer struct {
	status string
	reason string
}

// publishTo sends ev to relays through the pool (outbound admission, NIP-42
// AUTH on auth-required) and classifies each relay's answer. Only the
// operator's kill switch is an error: nothing may be published then.
func publishTo(ctx context.Context, pool *nostradapter.RelayPool, ev nostr.Event, relays []string) (map[string]relayAnswer, error) {
	results, err := pool.PublishToRelaysWithResults(ctx, ev, relays)
	if errors.Is(err, nostrout.ErrKillSwitch) {
		return nil, err
	}
	// Admission answers cached duplicates under its own URL normalization.
	byRelay := make(map[string]relayAnswer, len(results))
	for _, result := range results {
		byRelay[nostrout.NormalizeRelayURL(result.RelayURL)] = classify(result)
	}
	answers := make(map[string]relayAnswer, len(relays))
	for _, relay := range relays {
		answer, ok := byRelay[nostrout.NormalizeRelayURL(relay)]
		if !ok {
			answer = relayAnswer{status: outcomeError, reason: "not sent"}
			if err != nil {
				answer.reason += ": " + err.Error()
			}
		}
		answers[relay] = answer
	}
	return answers, nil
}

func classify(result nostradapter.PublishResult) relayAnswer {
	switch {
	case result.Accepted:
		return relayAnswer{status: outcomeAccepted}
	case result.IsDuplicate():
		return relayAnswer{status: outcomeDuplicate, reason: result.Reason}
	case result.Error != nil:
		return relayAnswer{status: outcomeError, reason: result.Error.Error()}
	case nostradapter.IsAuthRequiredReason(result.Reason):
		return relayAnswer{status: outcomeAuthRequired, reason: result.Reason}
	case result.IsRateLimited():
		return relayAnswer{status: outcomeRateLimited, reason: result.Reason}
	case strings.HasPrefix(strings.TrimSpace(result.Reason), "error:"):
		return relayAnswer{status: outcomeError, reason: result.Reason}
	default:
		return relayAnswer{status: outcomeRejected, reason: result.Reason}
	}
}

// summarize prints each relay's tally and reports whether every deletion is
// done on every relay.
func summarize(w io.Writer, r *report, relays []string) bool {
	complete := true
	for _, relay := range relays {
		counts := map[string]int{}
		for _, del := range r.Deletions {
			status := outcomePending
			if outcome := del.Relays[relay]; outcome != nil {
				status = outcome.Status
				if !outcome.done() {
					complete = false
				}
			} else {
				complete = false
			}
			counts[status]++
		}
		var parts []string
		for _, status := range []string{outcomeAccepted, outcomeDuplicate, outcomeRejected, outcomeAuthRequired, outcomeRateLimited, outcomeError, outcomePending} {
			if counts[status] > 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", status, counts[status]))
			}
		}
		fmt.Fprintf(w, "relay %s: %d deletions: %s\n", relay, len(r.Deletions), strings.Join(parts, " "))
	}
	return complete
}

func waitTimer(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
