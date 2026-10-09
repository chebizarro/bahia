package nostr

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// isAdmissionRejection reports a refusal by the process-wide outbound
// admission controller (internal/nostrout): a lane, aggregate, wire-share or
// controller-capacity budget, the shared rate-limit circuit breaker, the
// operator kill switch, or a concurrent in-flight copy of the same event.
// None of these say anything about any relay, so a refused round is skipped:
// it never counts against the attempt budget, never abandons the event, and
// never marks its coordinate undelivered (docs/architecture/outbox-delivery.md).
func isAdmissionRejection(err error) bool {
	return errors.Is(err, nostrout.ErrBudgetExceeded) ||
		errors.Is(err, nostrout.ErrCircuitOpen) ||
		errors.Is(err, nostrout.ErrKillSwitch) ||
		errors.Is(err, nostrout.ErrCapacity) ||
		errors.Is(err, nostrout.ErrInFlight)
}

// isAdmissionHalt reports an admission refusal from a process-wide gate (kill
// switch, open circuit, exhausted controller capacity) that would refuse every
// remaining entry of a redelivery pass identically, so the pass stops early.
// A lane or per-relay budget refusal is not a halt: other events may be in a
// lane that still has capacity.
func isAdmissionHalt(err error) bool {
	return errors.Is(err, nostrout.ErrKillSwitch) ||
		errors.Is(err, nostrout.ErrCircuitOpen) ||
		errors.Is(err, nostrout.ErrCapacity)
}

// admissionRetryDelay paces the retry of an admission-refused round: about one
// second, jittered so a restart's outbox hydration cannot resynchronize
// refused deliveries into a burst when capacity returns.
func admissionRetryDelay() time.Duration {
	return time.Second + time.Duration(rand.Int64N(int64(time.Second)))
}

// allAdmissionRefusals reports whether every result of a round is a
// controller-side frame refusal (a per-relay wire budget, or a kill switch or
// breaker that closed after the publication was admitted): no frame reached
// any relay, so the round learned nothing and must not count. global reports
// that at least one refusal came from a process-wide gate.
func allAdmissionRefusals(results []PublishResult) (all bool, global bool) {
	if len(results) == 0 {
		return false, false
	}
	for _, result := range results {
		if result.Error == nil || result.Reason != "" || !isAdmissionRejection(result.Error) {
			return false, false
		}
		if isAdmissionHalt(result.Error) {
			global = true
		}
	}
	return true, global
}

// defaultMaxPublishAttempts bounds how many delivery rounds an outbound event
// gets before the relays that still have not accepted it are given up on. With
// DefaultBackoff (1s doubling to 2m) this is roughly 50 minutes of retrying.
// The count is durable (the local outbox entry's rounds, or a PostgreSQL row's
// publish_attempts), so it survives restarts.
const defaultMaxPublishAttempts = 30

// maxDiscoveredDeliveries caps how many outbox rows discovery pulls into
// memory at once; rows past the cap stay pending in the outbox until tracked
// deliveries settle.
const maxDiscoveredDeliveries = 10000

// deliveryUnscheduled marks a delivery whose round is in flight (or not yet
// started), so the runner neither retries it nor treats it as overdue.
var deliveryUnscheduled = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// defaultOutboxPageSize is how many pending outbox rows the runner reads per
// discovery pass.
const defaultOutboxPageSize = 100

// ErrPublishIncomplete reports that fewer relays than the caller-facing publish
// quorum (nostr.publish_quorum, default 1) accepted an event. The event remains
// queued and relays that have not accepted are still retried; errors.As a
// *PublishIncompleteError for the counts. It aliases nostrutil's sentinel so
// callers outside this package match it without an import cycle.
var ErrPublishIncomplete = nostrutil.ErrPublishIncomplete

// ErrPublishAbandoned reports that delivery of an event was given up on: its
// outbox row is failed and nothing retries it. Unlike ErrPublishIncomplete it
// is not "kept, still retrying". Every publish entry point returns it (as a
// *PublishAbandonedError) when the first delivery round already makes the
// publish quorum unreachable; abandonment in a later runner round is reported
// through OnDeliveryAbandoned. It aliases nostrutil's sentinel.
var ErrPublishAbandoned = nostrutil.ErrPublishAbandoned

// PublishIncompleteError describes a publish that did not reach its required
// relay acceptance.
type PublishIncompleteError struct {
	EventID  string
	Accepted int
	Required int
	Detail   string
}

func (e *PublishIncompleteError) Error() string {
	msg := fmt.Sprintf("nostr event %s accepted by %d of %d required relays", e.EventID, e.Accepted, e.Required)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

func (e *PublishIncompleteError) Unwrap() error { return ErrPublishIncomplete }

// PublishAbandonedError describes a publish whose delivery was given up on:
// the outbox row is failed and no relay is retried. It matches
// ErrPublishAbandoned and never ErrPublishIncomplete.
type PublishAbandonedError struct {
	EventID  string
	Accepted int
	Required int
	Detail   string
}

func (e *PublishAbandonedError) Error() string {
	msg := fmt.Sprintf("nostr event %s delivery abandoned: accepted by %d of %d required relays", e.EventID, e.Accepted, e.Required)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

func (e *PublishAbandonedError) Unwrap() error { return ErrPublishAbandoned }

// permanentPublishRejectionPrefixes are NIP-01 OK=false machine-readable
// prefixes that cannot change for the same signed event on the same relay, so
// retrying that relay is pointless.
var permanentPublishRejectionPrefixes = []string{"blocked:", "invalid:", "pow:"}

// IsPermanentRejectionReason reports whether an OK=false reason is terminal for
// the relay that sent it.
func IsPermanentRejectionReason(reason string) bool {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	for _, prefix := range permanentPublishRejectionPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

type relayPublishOutcome int

const (
	relayPublishRetry relayPublishOutcome = iota
	relayPublishAccepted
	relayPublishRejected
)

func classifyPublishResult(result PublishResult) relayPublishOutcome {
	switch {
	case result.Accepted || result.IsDuplicate():
		return relayPublishAccepted
	case result.Error == nil && IsPermanentRejectionReason(result.Reason):
		return relayPublishRejected
	default:
		return relayPublishRetry
	}
}

// relayDeliveryState is one relay's acceptance state for one event.
type relayDeliveryState struct {
	accepted bool
	rejected string // permanent OK=false reason; terminal for this relay
	lastErr  string // most recent retryable failure
	// seenDialFailure is the pool dial failure (RelayReconnectBackoffError
	// FailedAt) this event has already counted against its attempt budget.
	seenDialFailure time.Time
}

// deliveryLedger is where a delivery's rounds are persisted.
type deliveryLedger int

const (
	// ledgerNone: nothing durable; the delivery lives in memory only.
	ledgerNone deliveryLedger = iota
	// ledgerLocal: the local outbox, with every relay's state.
	ledgerLocal
	// ledgerAudit: a PostgreSQL audit row with no publish state.
	ledgerAudit
)

// outboxDelivery tracks per-relay acceptance of one signed event. mu serializes
// delivery rounds for the event (the runner uses TryLock to skip events that an
// inline publish is already delivering); nextAt is guarded by
// Publisher.deliveriesMu.
type outboxDelivery struct {
	mu        sync.Mutex
	event     nostr.Event
	ledger    deliveryLedger
	relays    map[string]*relayDeliveryState
	rounds    int
	backoff   *Backoff
	settled   bool
	delivered bool
	// reportedDelivered is set once OnDelivered handlers saw the event.
	reportedDelivered bool
	// callerWaiting is set while the round whose outcome the publishing
	// caller receives runs (the first round of an inline publish). An
	// abandonment in that round is returned to the caller, who treats the
	// operation as failed; one in any later round is reported only through
	// the hooks, after the caller was told the event was queued.
	callerWaiting bool
	// quorumReached mirrors delivered for readers that do not hold mu (see
	// Publisher.DeliveryOutcome).
	quorumReached atomic.Bool

	nextAt time.Time
}

// deliveryReport is the outcome of one delivery round.
type deliveryReport struct {
	detail      string // relays that have not accepted, and why
	results     []PublishResult
	accepted    int
	required    int
	delivered   bool
	settled     bool
	rateLimited bool
	// admissionRefused reports that the shared outbound controller refused
	// the round before any relay I/O; the round was skipped and does not
	// count against the attempt budget. admissionHalt additionally reports
	// that the refusal came from a process-wide gate, so the runner stops
	// the current pass instead of offering every remaining entry the same
	// refusal.
	admissionRefused bool
	admissionHalt    bool
	err              error
}

func (p *Publisher) newDelivery(ev nostr.Event, rounds int) *outboxDelivery {
	backoff := p.newBackoff()
	if backoff == nil {
		backoff = DefaultBackoff()
	}
	return &outboxDelivery{
		event:   ev,
		ledger:  p.defaultLedger(),
		relays:  make(map[string]*relayDeliveryState),
		rounds:  rounds,
		backoff: backoff,
		nextAt:  deliveryUnscheduled,
	}
}

// defaultLedger is where events this publisher admits are persisted (see
// Publisher.admit). The local outbox is required for redelivery-enabled
// publishers, so ledgerLocal is the normal case.
func (p *Publisher) defaultLedger() deliveryLedger {
	switch {
	case p.localOutbox != nil:
		return ledgerLocal
	case p.eventRepo != nil:
		return ledgerAudit
	default:
		return ledgerNone
	}
}

// trackedDelivered reports whether a delivery this publisher is tracking for
// id has reached the publish quorum.
func (p *Publisher) trackedDelivered(id string) bool {
	p.deliveriesMu.Lock()
	d := p.deliveries[id]
	p.deliveriesMu.Unlock()
	return d != nil && d.quorumReached.Load()
}

// trackDelivery returns the in-flight delivery for ev, registering a new one
// when none exists. created reports whether this call registered it.
func (p *Publisher) trackDelivery(ev nostr.Event, rounds int) (d *outboxDelivery, created bool) {
	id := ev.ID.Hex()
	p.deliveriesMu.Lock()
	defer p.deliveriesMu.Unlock()
	if existing, ok := p.deliveries[id]; ok {
		return existing, false
	}
	d = p.newDelivery(ev, rounds)
	p.deliveries[id] = d
	return d, true
}

func (p *Publisher) isTracked(id string) bool {
	p.deliveriesMu.Lock()
	defer p.deliveriesMu.Unlock()
	_, ok := p.deliveries[id]
	return ok
}

func (p *Publisher) trackedCount() int {
	p.deliveriesMu.Lock()
	defer p.deliveriesMu.Unlock()
	return len(p.deliveries)
}

func (p *Publisher) forgetDelivery(d *outboxDelivery) {
	id := d.event.ID.Hex()
	p.deliveriesMu.Lock()
	defer p.deliveriesMu.Unlock()
	if p.deliveries[id] == d {
		delete(p.deliveries, id)
	}
}

func (p *Publisher) scheduleDelivery(d *outboxDelivery, at time.Time) {
	p.deliveriesMu.Lock()
	d.nextAt = at
	p.deliveriesMu.Unlock()
}

// dueDeliveries returns tracked deliveries whose retry time has arrived, and
// the earliest scheduled retry time across all tracked deliveries (zero when
// nothing is scheduled). In-flight deliveries are unscheduled and excluded.
func (p *Publisher) dueDeliveries(now time.Time) (due []*outboxDelivery, next time.Time) {
	p.deliveriesMu.Lock()
	defer p.deliveriesMu.Unlock()
	for _, d := range p.deliveries {
		if d.nextAt.Equal(deliveryUnscheduled) {
			continue
		}
		if next.IsZero() || d.nextAt.Before(next) {
			next = d.nextAt
		}
		if !d.nextAt.After(now) {
			due = append(due, d)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].nextAt.Before(due[j].nextAt) })
	return due, next
}

// requiredAcceptances is the caller-facing publish quorum for the configured
// write relay count: nostr.publish_quorum relays (0/unset means 1, -1 means
// every relay), capped at the number of write relays. It decides whether a
// publish call succeeds and whether a settled row is published or abandoned;
// it never cuts delivery short, which always waits for every write relay to
// accept or reach a terminal state.
func (p *Publisher) requiredAcceptances(configured int) int {
	required := p.quorum
	switch {
	case required == config.PublishQuorumAllRelays:
		required = configured
	case required <= 0:
		required = config.PublishQuorumDefault
	}
	return min(required, configured)
}

// deliverRound runs one delivery round for d. The caller must hold d.mu. Only
// relays that have neither accepted nor permanently rejected the event are
// contacted. The round reports caller success once the publish quorum has
// accepted. The outbox row stays pending until every write relay has accepted
// or reached a terminal state (permanent rejection, attempt budget); it is then
// marked published if the quorum was reached, otherwise abandoned.
//
// A round the shared outbound admission controller refuses before any relay
// I/O is skipped: it does not count against the attempt budget, records no
// per-relay state, and is rescheduled about a second out (jittered). Admission
// refusal is back-pressure, never an abandonment, so a kill switch or an open
// circuit leaves entries pending for as long as it lasts.
func (p *Publisher) deliverRound(ctx context.Context, d *outboxDelivery) deliveryReport {
	eventID := d.event.ID.Hex()
	configured := normalizeRelayURLs(p.relayURLs())
	required := p.requiredAcceptances(len(configured))
	if d.settled {
		report := deliveryReport{accepted: d.acceptedCount(configured), required: required, delivered: d.delivered, settled: true}
		if !d.delivered {
			// Another round already abandoned this event (the delivery is
			// about to be forgotten); a caller that raced in must not read
			// that as success.
			report.err = &PublishAbandonedError{EventID: eventID, Accepted: report.accepted, Required: required, Detail: d.failureDetail(configured)}
		}
		return report
	}

	p.scheduleDelivery(d, deliveryUnscheduled)
	d.syncRelays(configured)
	targets := d.retryableRelays(configured)

	var (
		results []PublishResult
		callErr error
	)
	if len(targets) > 0 {
		if p.publishFn == nil {
			callErr = errors.New("relay publisher is not configured")
		} else {
			results, callErr = p.publishFn(ctx, d.event, targets)
		}
	}
	var (
		countable       bool
		backoffUntil    time.Time
		rateLimited     bool
		admissionGone   bool
		admissionGlobal bool
	)
	switch {
	case callErr != nil && len(results) == 0 && len(targets) > 0 && isAdmissionRejection(callErr):
		// The controller refused the whole publication before any relay was
		// contacted: nothing was learned about any relay, so applyRound does
		// not run, the round does not count, and the event stays pending with
		// its per-relay state untouched. Admission refusal is back-pressure,
		// never abandonment.
		admissionGone = true
		admissionGlobal = isAdmissionHalt(callErr)
		backoffUntil = p.now().Add(admissionRetryDelay())
	default:
		countable, backoffUntil, rateLimited = d.applyRound(targets, results, callErr)
		if countable {
			// Every frame of the round was refused by the controller (a
			// per-relay wire budget, or a gate that closed after the
			// publication was admitted): still nothing reached any relay.
			if all, global := allAdmissionRefusals(results); all {
				countable = false
				admissionGone = true
				admissionGlobal = global
				backoffUntil = p.now().Add(admissionRetryDelay())
			}
		}
	}
	skipped := !countable
	if !skipped {
		d.rounds++
	}

	accepted := d.acceptedCount(configured)
	retryable := len(d.retryableRelays(configured))
	delivered := len(configured) > 0 && accepted >= required
	exhausted := d.rounds >= p.maxAttempts
	quorumUnreachable := accepted+retryable < required
	settled := exhausted || (len(configured) > 0 && (retryable == 0 || quorumUnreachable))

	detail := d.failureDetail(configured)
	if len(configured) == 0 {
		detail = joinDetail("no write relays configured", detail)
	}
	if callErr != nil && len(results) == 0 && len(targets) > 0 {
		detail = joinDetail(callErr.Error(), detail)
	}

	report := deliveryReport{
		detail:           detail,
		results:          results,
		accepted:         accepted,
		required:         required,
		delivered:        delivered,
		settled:          settled,
		rateLimited:      rateLimited,
		admissionRefused: admissionGone,
		admissionHalt:    admissionGone && admissionGlobal,
	}
	if !delivered {
		report.err = &PublishIncompleteError{EventID: eventID, Accepted: accepted, Required: required, Detail: detail}
	}

	var persistErr error
	if !skipped || settled {
		// A skipped round records nothing: the durable round count is the
		// budget.
		persistErr = p.persistRound(ctx, d, delivered, settled, exhausted, detail)
	}
	if persistErr != nil {
		// Keep the delivery open so the next round retries the bookkeeping;
		// relays that already accepted are not contacted again. A delivered
		// event stays delivered: callers must not re-sign and republish it.
		p.logger.Warn("failed to persist nostr publish state", zap.String("event_id", eventID), zap.Error(persistErr))
		settled = false
		report.settled = false
		if !delivered {
			report.err = fmt.Errorf("%w; %v", report.err, persistErr)
		}
	} else if settled && !delivered {
		// The row is durably failed: report abandonment, not "queued".
		report.err = &PublishAbandonedError{EventID: eventID, Accepted: accepted, Required: required, Detail: detail}
	}
	d.settled = settled
	d.delivered = delivered
	if delivered {
		d.quorumReached.Store(true)
	}
	if persistErr == nil && delivered && !d.reportedDelivered {
		// The quorum's acceptance is durable; tell the owner of the content.
		d.reportedDelivered = true
		p.clearOwnEventUndelivered(d.event)
		p.notifyDelivered(d.event)
	}
	switch {
	case settled:
		if !delivered {
			// The entry is now durably failed; tell the owner of the content.
			if d.callerWaiting {
				// The caller learns of the abandonment from this round's
				// error and does not commit the operation: the event is not
				// the daemon's output.
				p.forgetOwnEvent(d.event)
			} else {
				// The caller was told the event was queued: keep it as the
				// daemon's committed state and flag its coordinate.
				p.markOwnEventUndelivered(d.event, abandonmentDetail(exhausted, p.maxAttempts, detail))
			}
			p.notifyAbandoned(d.event)
		}
	case skipped:
		// Retry when the relay may be dialed again, without growing this
		// event's own backoff.
		p.scheduleDelivery(d, maxTime(backoffUntil, p.now()))
	default:
		p.scheduleDelivery(d, p.now().Add(d.backoff.Next()))
	}
	p.logRound(d, report, detail)
	return report
}

// applyRound records one round's per-relay results for the targets contacted
// in d's relay state. It reports whether the round counts against the attempt
// budget: it does if it learned something, that is a relay was actually
// contacted, or a relay in reconnect backoff reported a dial failure this
// event has not counted yet. Rounds that only hit the pool's fail-fast backoff
// are skipped and rescheduled for backoffUntil, when the relay may be dialed
// again.
func (d *outboxDelivery) applyRound(targets []string, results []PublishResult, callErr error) (countable bool, backoffUntil time.Time, rateLimited bool) {
	countable = len(targets) == 0 || len(results) == 0
	answered := make(map[string]struct{}, len(results))
	for _, result := range results {
		state, ok := d.relays[result.RelayURL]
		if !ok {
			continue
		}
		answered[result.RelayURL] = struct{}{}
		var backoffErr *RelayReconnectBackoffError
		if result.Error != nil && errors.As(result.Error, &backoffErr) {
			if backoffErr.FailedAt.After(state.seenDialFailure) {
				state.seenDialFailure = backoffErr.FailedAt
				countable = true
			}
			if backoffUntil.IsZero() || backoffErr.RetryAt.Before(backoffUntil) {
				backoffUntil = backoffErr.RetryAt
			}
		} else {
			countable = true
		}
		switch classifyPublishResult(result) {
		case relayPublishAccepted:
			state.accepted = true
			state.lastErr = ""
		case relayPublishRejected:
			state.rejected = result.Reason
		default:
			rateLimited = rateLimited || result.IsRateLimited()
			state.lastErr = describePublishFailure(result)
		}
	}
	for _, url := range targets {
		if _, ok := answered[url]; ok {
			continue
		}
		missing := "no publish result"
		if callErr != nil {
			missing = callErr.Error()
		}
		d.relays[url].lastErr = missing
		countable = true
	}
	return countable, backoffUntil, rateLimited
}

// persistRound records a counted (or settling) round in d's ledger. For the
// local outbox, every relay's state is committed with it, so a restart resumes
// without resending to relays that already accepted; once the entry settles,
// its outcome is mirrored to the PostgreSQL archive, best effort.
func (p *Publisher) persistRound(ctx context.Context, d *outboxDelivery, delivered, settled, exhausted bool, detail string) error {
	eventID := d.event.ID.Hex()
	now := p.now().UTC()
	if settled && !delivered {
		detail = abandonmentDetail(exhausted, p.maxAttempts, detail)
	}
	switch d.ledger {
	case ledgerLocal:
		state := localstore.OutboxPending
		switch {
		case settled && delivered:
			state = localstore.OutboxPublished
		case settled:
			state = localstore.OutboxFailed
		}
		if _, err := p.localOutbox.CommitRound(d.event.ID, localstore.OutboxRound{
			Rounds: d.rounds, Relays: d.relayDeliveries(), Delivered: delivered, State: state, Detail: detail, At: now,
		}); err != nil {
			return err
		}
		if settled {
			p.mirrorSettled(ctx, eventID, delivered, detail, now)
		}
	}
	return nil
}

// abandonmentDetail is the recorded reason for an abandoned delivery.
func abandonmentDetail(exhausted bool, maxAttempts int, detail string) string {
	reason := "abandoned: required relay acceptance is unreachable"
	if exhausted {
		reason = fmt.Sprintf("abandoned after %d publish attempts", maxAttempts)
	}
	return joinDetail(reason, detail)
}

// mirrorSettled copies a local outbox outcome onto the event's PostgreSQL
// archive row, best effort.
func (p *Publisher) mirrorSettled(ctx context.Context, eventID string, delivered bool, detail string, at time.Time) {
	archive := p.archive.outbox()
	if archive == nil {
		return
	}
	p.archive.write(ctx, "outbound outcome", eventID, func(ctx context.Context, _ repository.NostrEventRepository) error {
		if delivered {
			return archive.MarkPublished(ctx, eventID, at)
		}
		return archive.AbandonPublish(ctx, eventID, detail)
	})
}

// relayDeliveries snapshots d's per-relay state for the local outbox.
func (d *outboxDelivery) relayDeliveries() map[string]localstore.RelayDelivery {
	out := make(map[string]localstore.RelayDelivery, len(d.relays))
	for url, state := range d.relays {
		out[url] = localstore.RelayDelivery{Accepted: state.accepted, Rejected: state.rejected, LastError: state.lastErr, SeenDialFailure: state.seenDialFailure}
	}
	return out
}

// restore resumes d from a local outbox entry left pending by an earlier
// delivery or process.
func (d *outboxDelivery) restore(entry localstore.OutboxEntry) {
	d.ledger = ledgerLocal
	d.rounds = entry.Rounds
	for url, state := range entry.Relays {
		d.relays[url] = &relayDeliveryState{accepted: state.Accepted, rejected: state.Rejected, lastErr: state.LastError, seenDialFailure: state.SeenDialFailure}
	}
}

func (p *Publisher) logRound(d *outboxDelivery, report deliveryReport, detail string) {
	fields := []zap.Field{
		zap.String("event_id", d.event.ID.Hex()),
		zap.Int("accepted", report.accepted),
		zap.Int("required", report.required),
		zap.Int("attempt", d.rounds),
	}
	switch {
	case report.settled && report.delivered && detail == "":
		p.logger.Debug("nostr event delivered to every write relay", fields...)
	case report.settled && report.delivered:
		p.logger.Warn("nostr event delivered to quorum; remaining relays gave up after retries", append(fields, zap.String("detail", detail))...)
	case report.settled:
		p.logger.Warn("nostr event abandoned; required relay acceptance not reached", append(fields, zap.String("detail", detail))...)
	}
}

// syncRelays aligns tracked relay state with the currently configured write
// relays: new relays start pending, removed relays are dropped.
func (d *outboxDelivery) syncRelays(configured []string) {
	keep := relayURLSet(configured)
	for url := range d.relays {
		if _, ok := keep[url]; !ok {
			delete(d.relays, url)
		}
	}
	for _, url := range configured {
		if _, ok := d.relays[url]; !ok {
			d.relays[url] = &relayDeliveryState{}
		}
	}
}

func (d *outboxDelivery) retryableRelays(configured []string) []string {
	out := make([]string, 0, len(configured))
	for _, url := range configured {
		state, ok := d.relays[url]
		if !ok || (!state.accepted && state.rejected == "") {
			out = append(out, url)
		}
	}
	return out
}

func (d *outboxDelivery) acceptedCount(configured []string) int {
	count := 0
	for _, url := range configured {
		if state, ok := d.relays[url]; ok && state.accepted {
			count++
		}
	}
	return count
}

func (d *outboxDelivery) failureDetail(configured []string) string {
	details := make([]string, 0, len(configured))
	for _, url := range configured {
		state, ok := d.relays[url]
		switch {
		case !ok || state.accepted:
		case state.rejected != "":
			details = append(details, fmt.Sprintf("%s rejected permanently: %s", url, state.rejected))
		case state.lastErr != "":
			details = append(details, fmt.Sprintf("%s: %s", url, state.lastErr))
		default:
			details = append(details, fmt.Sprintf("%s: not accepted", url))
		}
	}
	return strings.Join(details, "; ")
}

func describePublishFailure(result PublishResult) string {
	switch {
	case result.Error != nil:
		return result.Error.Error()
	case strings.TrimSpace(result.Reason) != "":
		return strings.TrimSpace(result.Reason)
	default:
		return "OK false"
	}
}

func joinDetail(parts ...string) string {
	nonEmpty := parts[:0:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, "; ")
}

// redeliverDue runs one round for every tracked delivery whose retry time has
// arrived. Deliveries currently being attempted elsewhere are skipped.
func (p *Publisher) redeliverDue(ctx context.Context) (rateLimited bool) {
	due, _ := p.dueDeliveries(p.now())
	for _, d := range due {
		if ctx.Err() != nil {
			return rateLimited
		}
		if !d.mu.TryLock() {
			continue
		}
		report := p.deliverRound(ctx, d)
		settled := d.settled
		d.mu.Unlock()
		rateLimited = rateLimited || report.rateLimited
		if settled {
			p.forgetDelivery(d)
		}
		if report.admissionHalt {
			// A process-wide gate refuses every remaining entry identically;
			// stop the pass. Skipped rounds are already rescheduled.
			return rateLimited
		}
	}
	return rateLimited
}

// discoverPending reads one keyset page of this publisher's target's pending
// entries from the local outbox and starts delivery for those it is not
// already tracking (left pending by a previous process or an inactive runner).
// It reports whether the page was full, meaning more follow its cursor.
//
// The local outbox is a required dependency; this is the only discovery
// path. Legacy PostgreSQL pending rows are not imported by this runner;
// bahia-migrate exposes only a read-only inventory of them.
func (p *Publisher) discoverPending(ctx context.Context) (bool, error) {
	return p.discoverLocal(ctx)
}

func (p *Publisher) discoverLocal(ctx context.Context) (bool, error) {
	if p.localOutbox == nil {
		return false, nil
	}
	entries, err := p.localOutbox.ListPending(p.target, p.localCursor, p.pageSize)
	if err != nil {
		return false, err
	}
	if len(entries) < p.pageSize {
		p.localCursor = nil // wrap to the oldest pending entry on the next pass
	} else {
		last := entries[len(entries)-1]
		p.localCursor = &localstore.OutboxCursor{EnqueuedAt: last.EnqueuedAt, ID: last.Event.ID}
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return false, nil
		}
		if p.isTracked(entry.Event.ID.Hex()) {
			continue
		}
		if p.trackedCount() >= maxDiscoveredDeliveries {
			return false, nil
		}
		d, created := p.trackDelivery(entry.Event, entry.Rounds)
		if !created || !d.mu.TryLock() {
			continue
		}
		d.restore(entry)
		report := p.deliverRound(ctx, d)
		settled := d.settled
		d.mu.Unlock()
		if settled {
			p.forgetDelivery(d)
		}
		if report.admissionHalt {
			// Restart hydration must not burst into a closed gate: the
			// remaining entries stay pending in the durable outbox and are
			// discovered again on a later pass.
			return false, nil
		}
	}
	return len(entries) == p.pageSize, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
