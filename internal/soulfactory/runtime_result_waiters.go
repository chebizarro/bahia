package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"fiatjaf.com/nostr"
)

// Late runtime-control results (bahia-irsry.31).
//
// A runtime adapter bounds its wait for the kind:38386 result correlated with
// a published kind:38384 request (RuntimeAdapterConfig.ResultTimeout). When the
// wait ends first the outcome is unknown: the runtime may still apply the
// request and answer later. Callers therefore never treat a timeout as failure
// and never roll back on it. They publish an awaiting_terminal progress event
// and park the rest of the operation here. The reactor's long-lived resumable
// subscription delivers every kind:38386 addressed to the controller, and
// deliver hands the correlated one to the parked continuation, which applies it
// exactly as a timely result would have been applied: a late success finishes
// the operation, a late failure rolls it back. Rollback happens only on an
// observed terminal failure or an explicit operator action (a lifecycle
// rollback action or a newer fleet revision).
//
// A parked operation that changes a soul (a lifecycle step or a fleet apply)
// keeps that soul's soulOperationGate hold while it waits, so later lifecycle
// actions and fleet reloads for the soul wait behind it. Such entries are never
// evicted: the gate allows one per soul. The only evictable entries are late
// rollback follow-ups, which hold no soul (see parkedOperation).
//
// Parked continuations live in memory, but the state they stand for is on the
// relays: every park publishes a kind:6950 awaiting_terminal progress event
// tagged t=awaitingTerminalTopic, and its operation ends with a kind:7950
// terminal result. After a restart the reactor rebuilds the outstanding ones
// (awaiting_terminal without a matching terminal result) before it handles the
// backlog, holds their souls again and re-drives them with the same runtime
// idempotency keys (rebuildParkedOperations).

const (
	// actionStatusAwaitingTerminal is the kind:6950 progress status of an
	// operation whose runtime request was accepted but whose terminal kind:38386
	// was not observed within the wait. Its outcome is unknown, neither success
	// nor failure, until the late result is reconciled.
	actionStatusAwaitingTerminal = "awaiting_terminal"
	// rollbackStatusOutcomeUnknown reports a rollback request whose terminal
	// result was not observed within the wait.
	rollbackStatusOutcomeUnknown = "outcome_unknown"

	// actionStatusRollbackResolved is the kind:6950 follow-up progress status
	// published when the terminal kind:38386 of a rollback whose wait timed out
	// (reported as rollback_status outcome_unknown) is observed. Its
	// rollback-status tag says how the rollback ended.
	actionStatusRollbackResolved = "rollback_resolved"

	// maxParkedRuntimeResults bounds parked entries that hold no soul (late
	// rollback follow-ups). Past it the oldest of those is dropped with a
	// warning. Entries that hold a soul are never evicted.
	maxParkedRuntimeResults = 4096
	// maxUnclaimedRuntimeResults bounds results remembered for an operation
	// that has not parked yet: the result can arrive between the wait's timeout
	// and the park.
	maxUnclaimedRuntimeResults = 512
)

// runtimeResultPending is a runtime adapter's Execute error when the control
// request was accepted but the wait for its terminal kind:38386 ended first. It
// unwraps to the *NoTerminalResultError and carries what is needed to correlate
// a late result with the request.
type runtimeResultPending struct {
	noResult         *NoTerminalResultError
	requestEvent     *nostr.Event
	req              RuntimeAdapterRequest
	controllerPubkey string
}

func (e *runtimeResultPending) Error() string { return e.noResult.Error() }

func (e *runtimeResultPending) Unwrap() error { return e.noResult }

func (e *runtimeResultPending) requestID() string { return e.requestEvent.ID.Hex() }

func (e *runtimeResultPending) correlates(result *RuntimeControlResultEnvelope) bool {
	return runtimeResultCorrelates(result, e.requestEvent, e.req, e.controllerPubkey)
}

// runtimeOutcomeUnknown reports whether err says a runtime request's terminal
// result was not observed. pending is nil when the adapter did not supply the
// request correlation (an adapter outside this package); such an operation
// stays awaiting_terminal until it is re-driven.
func runtimeOutcomeUnknown(err error) (pending *runtimeResultPending, unknown bool) {
	if !errors.Is(err, ErrNoTerminalResult) {
		return nil, false
	}
	errors.As(err, &pending)
	return pending, true
}

// runtimeResultFailure is the error a terminal runtime result stands for: nil
// for success, otherwise the runtime's status and error.
func runtimeResultFailure(result *RuntimeControlResultEnvelope) error {
	if result == nil {
		return fmt.Errorf("runtime returned no result")
	}
	if result.Status == "success" {
		return nil
	}
	if result.Error != nil {
		return fmt.Errorf("runtime %s response: %s: %s", result.Status, result.Error.Code, result.Error.Message)
	}
	return fmt.Errorf("runtime %s response", result.Status)
}

// parkedOperation is the continuation of an operation waiting for a late
// runtime terminal result.
type parkedOperation struct {
	// shardKey is the reactor handler shard of the parked operation, so the
	// continuation runs serialized with the work it continues.
	shardKey string
	// holdsSoul marks an operation that keeps its soul's soulOperationGate hold
	// while parked. It is never evicted: dropping it would leave the soul held
	// with nothing left to release it.
	holdsSoul bool
	resume    func(context.Context, *RuntimeControlResultEnvelope)
}

type parkedRuntimeResult struct {
	pending *runtimeResultPending
	op      parkedOperation
	seq     uint64
}

type unclaimedRuntimeResult struct {
	result *RuntimeControlResultEnvelope
	seq    uint64
}

// runtimeResultWaiters matches late kind:38386 results with parked operations.
// It is event-driven: nothing expires on a timer.
type runtimeResultWaiters struct {
	logger *slog.Logger
	// limit bounds parked entries that hold no soul (maxParkedRuntimeResults).
	limit int

	mu             sync.Mutex
	seq            uint64
	parked         map[string]*parkedRuntimeResult     // by kind:38384 request event id
	unclaimed      map[string][]unclaimedRuntimeResult // by kind:38384 request event id
	unclaimedCount int
}

func newRuntimeResultWaiters(logger *slog.Logger) *runtimeResultWaiters {
	if logger == nil {
		logger = slog.Default()
	}
	return &runtimeResultWaiters{
		logger:    logger,
		limit:     maxParkedRuntimeResults,
		parked:    make(map[string]*parkedRuntimeResult),
		unclaimed: make(map[string][]unclaimedRuntimeResult),
	}
}

// park registers op for pending's terminal result. When a correlated result
// has already been delivered, park returns it instead and registers nothing;
// the caller continues inline.
func (w *runtimeResultWaiters) park(pending *runtimeResultPending, op parkedOperation) (*RuntimeControlResultEnvelope, bool) {
	id := pending.requestID()
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, candidate := range w.unclaimed[id] {
		if pending.correlates(candidate.result) {
			w.dropUnclaimedLocked(id, i)
			return candidate.result, true
		}
	}
	w.seq++
	w.parked[id] = &parkedRuntimeResult{pending: pending, op: op, seq: w.seq}
	w.evictFollowUpsLocked()
	return nil, false
}

// evictFollowUpsLocked drops the oldest parked entries that hold no soul while
// more than limit of them are parked.
func (w *runtimeResultWaiters) evictFollowUpsLocked() {
	for {
		followUps, oldestID, oldest := 0, "", uint64(0)
		for id, candidate := range w.parked {
			if candidate.op.holdsSoul {
				continue
			}
			followUps++
			if oldestID == "" || candidate.seq < oldest {
				oldestID, oldest = id, candidate.seq
			}
		}
		if followUps <= w.limit {
			return
		}
		delete(w.parked, oldestID)
		w.logger.Warn("dropping oldest late rollback follow-up awaiting a runtime terminal result; its action already reported rollback_status outcome_unknown",
			"request_event", oldestID, "limit", w.limit)
	}
}

// shardKey returns the handler shard of the operation parked on requestID.
func (w *runtimeResultWaiters) shardKey(requestID string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	parked := w.parked[requestID]
	if parked == nil {
		return "", false
	}
	return parked.op.shardKey, true
}

// deliver hands a kind:38386 event to the operation parked on its request and
// reports whether one resumed. An uncorrelated result is remembered briefly in
// case its operation is about to park.
func (w *runtimeResultWaiters) deliver(ctx context.Context, event *nostr.Event) bool {
	result, ok := parseRuntimeControlResultEvent(event)
	if !ok || result.RequestEvent == "" {
		return false
	}
	w.mu.Lock()
	parked := w.parked[result.RequestEvent]
	if parked == nil || !parked.pending.correlates(result) {
		w.rememberLocked(result)
		w.mu.Unlock()
		return false
	}
	delete(w.parked, result.RequestEvent)
	w.mu.Unlock()
	w.logger.Info("late runtime terminal result observed; resuming operation",
		"request_event", result.RequestEvent, "result_event", event.ID.Hex(),
		"method", result.Method, "status", result.Status)
	parked.op.resume(ctx, result)
	return true
}

func (w *runtimeResultWaiters) rememberLocked(result *RuntimeControlResultEnvelope) {
	for _, existing := range w.unclaimed[result.RequestEvent] {
		if existing.result.Event.ID == result.Event.ID {
			return
		}
	}
	w.seq++
	w.unclaimed[result.RequestEvent] = append(w.unclaimed[result.RequestEvent], unclaimedRuntimeResult{result: result, seq: w.seq})
	w.unclaimedCount++
	if w.unclaimedCount <= maxUnclaimedRuntimeResults {
		return
	}
	oldestID, oldestIndex, oldest := "", 0, uint64(0)
	for id, results := range w.unclaimed {
		for i, candidate := range results {
			if oldestID == "" || candidate.seq < oldest {
				oldestID, oldestIndex, oldest = id, i, candidate.seq
			}
		}
	}
	w.dropUnclaimedLocked(oldestID, oldestIndex)
}

func (w *runtimeResultWaiters) dropUnclaimedLocked(id string, index int) {
	results := w.unclaimed[id]
	results = append(results[:index:index], results[index+1:]...)
	if len(results) == 0 {
		delete(w.unclaimed, id)
	} else {
		w.unclaimed[id] = results
	}
	w.unclaimedCount--
}

// observeLateRollback resolves a rollback request's Execute outcome. When the
// rollback's terminal result was not observed in time, its outcome stays
// unknown (the caller reports rollback_status outcome_unknown) and a follow-up
// is parked: report receives the late result once the reactor observes it, so
// the rollback's actual outcome is published rather than only logged
// (bahia-irsry.38). A result that arrived between the wait's timeout and the
// park is the rollback's outcome and is returned as if observed in time. The
// follow-up holds no soul.
func (w *runtimeResultWaiters) observeLateRollback(shardKey string, result *RuntimeControlResultEnvelope, err error, report func(context.Context, *RuntimeControlResultEnvelope)) (*RuntimeControlResultEnvelope, error) {
	pending, unknown := runtimeOutcomeUnknown(err)
	if !unknown || pending == nil {
		return result, err
	}
	late, observed := w.park(pending, parkedOperation{shardKey: shardKey, resume: report})
	if !observed {
		w.logger.Info("rollback outcome unknown; its late runtime result will be reported as progress", "request_event", pending.requestID())
		return result, err
	}
	return late, runtimeResultFailure(late)
}

// lateRollbackProgress is the rollback-status tag value and message of the
// follow-up progress for a late rollback result.
func lateRollbackProgress(step string, late *RuntimeControlResultEnvelope) (status, message string) {
	if failure := runtimeResultFailure(late); failure != nil {
		return "failed", fmt.Sprintf("%s: %s failed after its outcome was reported unknown: %v", actionStatusRollbackResolved, step, failure)
	}
	return "completed", fmt.Sprintf("%s: %s completed after its outcome was reported unknown", actionStatusRollbackResolved, step)
}
