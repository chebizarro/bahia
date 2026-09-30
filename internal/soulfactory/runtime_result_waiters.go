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
// Parked operations live in memory. After a restart the reactor's backfill
// re-drives them: the kind:1950 action has no terminal result yet, and a soul
// whose applied fleet revision is not the latest is reconciled again, with the
// same runtime idempotency keys.

const (
	// actionStatusAwaitingTerminal is the kind:6950 progress status of an
	// operation whose runtime request was accepted but whose terminal kind:38386
	// was not observed within the wait. Its outcome is unknown, neither success
	// nor failure, until the late result is reconciled.
	actionStatusAwaitingTerminal = "awaiting_terminal"
	// rollbackStatusOutcomeUnknown reports a rollback request whose terminal
	// result was not observed within the wait.
	rollbackStatusOutcomeUnknown = "outcome_unknown"

	// maxParkedRuntimeResults bounds parked operations. Past it the oldest is
	// dropped with a warning and stays awaiting_terminal until re-driven.
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

type parkedRuntimeResult struct {
	pending *runtimeResultPending
	// shardKey is the reactor handler shard of the parked operation, so the
	// continuation runs serialized with the work it continues.
	shardKey string
	resume   func(context.Context, *RuntimeControlResultEnvelope)
	seq      uint64
}

type unclaimedRuntimeResult struct {
	result *RuntimeControlResultEnvelope
	seq    uint64
}

// runtimeResultWaiters matches late kind:38386 results with parked operations.
// It is event-driven: nothing expires on a timer.
type runtimeResultWaiters struct {
	logger *slog.Logger

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
		parked:    make(map[string]*parkedRuntimeResult),
		unclaimed: make(map[string][]unclaimedRuntimeResult),
	}
}

// park registers resume for pending's terminal result. When a correlated result
// has already been delivered, park returns it instead and registers nothing;
// the caller continues inline.
func (w *runtimeResultWaiters) park(pending *runtimeResultPending, shardKey string, resume func(context.Context, *RuntimeControlResultEnvelope)) (*RuntimeControlResultEnvelope, bool) {
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
	w.parked[id] = &parkedRuntimeResult{pending: pending, shardKey: shardKey, resume: resume, seq: w.seq}
	if len(w.parked) > maxParkedRuntimeResults {
		oldestID, oldest := "", uint64(0)
		for candidateID, candidate := range w.parked {
			if oldestID == "" || candidate.seq < oldest {
				oldestID, oldest = candidateID, candidate.seq
			}
		}
		delete(w.parked, oldestID)
		w.logger.Warn("dropping oldest operation awaiting a runtime terminal result; it stays awaiting_terminal until re-driven",
			"request_event", oldestID, "limit", maxParkedRuntimeResults)
	}
	return nil, false
}

// shardKey returns the handler shard of the operation parked on requestID.
func (w *runtimeResultWaiters) shardKey(requestID string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	parked := w.parked[requestID]
	if parked == nil {
		return "", false
	}
	return parked.shardKey, true
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
	parked.resume(ctx, result)
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
