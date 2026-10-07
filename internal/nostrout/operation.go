package nostrout

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"fiatjaf.com/nostr"
)

// OperationSpec declares a bounded multi-event publication operation such as
// a Concord rotation. MaxEvents is the upper bound of logical publications the
// operation may perform; MaxWait bounds how long it may queue for admission.
type OperationSpec struct {
	MaxEvents int
	MaxWait   time.Duration
}

// Operation is an admitted multi-event publication. Admission of the whole
// operation is all-or-none before its first event: once active, its events are
// paced through the dedicated bulk lane instead of failing mid-operation
// merely because the operation is larger than a burst. The kill switch,
// caller cancellation, and the operation deadline still interrupt it; network
// failures still propagate. Nostr cannot make several events atomically
// visible across relays, so callers must keep their existing resumable
// recovery semantics for interrupted delivery.
//
// Only one operation is active per controller; others wait in a bounded FIFO
// queue. The bulk lane is separate from the priority lane, so operator
// results and tombstones keep their full reserved capacity throughout.
type Operation struct {
	a           *Admission
	maxEvents   int
	used        int
	deadline    time.Time
	busy        bool
	outstanding *Publication
	closed      bool
}

type operationWaiter struct {
	op        *Operation
	ready     chan struct{}
	activated bool
}

// BeginOperation admits a bounded operation, waiting up to spec.MaxWait (and at
// most 30 seconds) in a bounded FIFO queue. A full queue returns ErrQueueFull
// immediately; a timeout returns ErrQueueTimeout. Nothing is published before
// the operation is active. The returned Operation must be closed.
func (a *Admission) BeginOperation(ctx context.Context, spec OperationSpec) (*Operation, error) {
	if a == nil {
		return nil, ErrNotConfigured
	}
	if spec.MaxEvents < 1 || spec.MaxEvents > a.maxOperationEvents {
		return nil, fmt.Errorf("%w: operation declares %d events; allowed range is 1..%d", ErrOperation, spec.MaxEvents, a.maxOperationEvents)
	}
	maxWait := spec.MaxWait
	if maxWait <= 0 || maxWait > a.maxQueueWait {
		maxWait = a.maxQueueWait
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	a.mu.Lock()
	if err := a.killSwitchLocked(); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	op := &Operation{a: a, maxEvents: spec.MaxEvents}
	if a.operation == nil && len(a.operationQueue) == 0 {
		a.activateLocked(op)
		a.mu.Unlock()
		return op, nil
	}
	if len(a.operationQueue) >= a.maxQueuedOperations {
		a.metrics.QueueRejected++
		a.mu.Unlock()
		return nil, fmt.Errorf("%w: %d operations already queued", ErrQueueFull, len(a.operationQueue))
	}
	waiter := &operationWaiter{op: op, ready: make(chan struct{})}
	a.operationQueue = append(a.operationQueue, waiter)
	fired, stop := a.clock.NewTimer(maxWait)
	a.mu.Unlock()

	select {
	case <-waiter.ready:
		stop()
		return op, nil
	case <-ctx.Done():
		stop()
		return nil, a.abandonOperationWaiter(waiter, ctx.Err())
	case <-fired:
		return nil, a.abandonOperationWaiter(waiter, ErrQueueTimeout)
	}
}

func (a *Admission) abandonOperationWaiter(waiter *operationWaiter, cause error) error {
	a.mu.Lock()
	if waiter.activated {
		// Activation raced the timeout/cancellation: release the slot so the
		// next queued operation proceeds.
		a.mu.Unlock()
		waiter.op.Close()
		return cause
	}
	for i, queued := range a.operationQueue {
		if queued == waiter {
			a.operationQueue = append(a.operationQueue[:i], a.operationQueue[i+1:]...)
			break
		}
	}
	if errors.Is(cause, ErrQueueTimeout) {
		a.metrics.QueueRejected++
	}
	a.mu.Unlock()
	return cause
}

func (a *Admission) activateLocked(op *Operation) {
	a.operation = op
	a.metrics.OperationsStarted++
	// The operation lifetime is exactly what the bulk lane needs to carry its
	// declared events, plus a fixed slack for relay round trips. At defaults a
	// 2048-event operation is bounded at roughly 6h51m; it is never silently
	// truncated to an arbitrary short timeout.
	seconds := math.Ceil(float64(op.maxEvents) / a.bulkRatePerSecond)
	op.deadline = a.clock.Now().Add(time.Duration(seconds)*time.Second + operationDeadlineSlack)
}

// Deadline returns when the active operation stops being admitted.
func (op *Operation) Deadline() time.Time {
	if op == nil {
		return time.Time{}
	}
	op.a.mu.Lock()
	defer op.a.mu.Unlock()
	return op.deadline
}

// remaining returns how many more logical publications the operation may start.
func (op *Operation) remaining() int {
	if op == nil {
		return 0
	}
	op.a.mu.Lock()
	defer op.a.mu.Unlock()
	return op.maxEvents - op.used
}

// Begin admits the next logical publication of the operation, waiting for bulk
// capacity and for an open circuit to close instead of failing. Only one
// publication may be outstanding at a time; the previous one must be closed.
func (op *Operation) Begin(ctx context.Context, ev nostr.Event, relayURLs []string) (*Publication, error) {
	if op == nil {
		return nil, ErrNotConfigured
	}
	return op.a.begin(ctx, op, ev, relayURLs)
}

func (op *Operation) claimLocked() error {
	switch {
	case op.closed:
		return fmt.Errorf("%w: operation already closed", ErrOperation)
	case op.a.operation != op:
		return fmt.Errorf("%w: operation is not active", ErrOperation)
	case op.busy || op.outstanding != nil:
		return fmt.Errorf("%w: previous operation publication is still outstanding", ErrOperation)
	case op.used >= op.maxEvents:
		return fmt.Errorf("%w: operation exceeded its declared %d events", ErrOperation, op.maxEvents)
	}
	op.busy = true
	return nil
}

func (op *Operation) finishClaimLocked(pub *Publication, err error) {
	op.busy = false
	if err != nil {
		return
	}
	op.used++
	if !pub.closed {
		op.outstanding = pub
	}
}

// Close releases the operation and activates the next queued one. Unused
// declared capacity is simply unused: nothing was reserved, so nothing is
// refunded. Close is idempotent.
func (op *Operation) Close() {
	if op == nil {
		return
	}
	a := op.a
	a.mu.Lock()
	defer a.mu.Unlock()
	if op.closed {
		return
	}
	op.closed = true
	if a.operation != op {
		return
	}
	a.operation = nil
	if len(a.operationQueue) == 0 {
		return
	}
	next := a.operationQueue[0]
	a.operationQueue = a.operationQueue[1:]
	next.activated = true
	a.activateLocked(next.op)
	close(next.ready)
}

func operationDeadlineError(err error) error {
	if errors.Is(err, ErrQueueTimeout) {
		return fmt.Errorf("%w: operation deadline exceeded: %w", ErrOperation, err)
	}
	return err
}

type operationContextKey struct{}

// WithOperation carries an active operation through helper layers that
// publish on its behalf, so nested helpers never acquire nested operations.
func WithOperation(ctx context.Context, op *Operation) context.Context {
	return context.WithValue(ctx, operationContextKey{}, op)
}

// OperationFromContext returns the operation carried by ctx, if any.
func OperationFromContext(ctx context.Context) *Operation {
	op, _ := ctx.Value(operationContextKey{}).(*Operation)
	return op
}

// AdmitOpaque admits one publication whose event is built and sent inside a
// library that exposes no per-frame hook (NIP-46 remote-signer RPC): the
// library sends the frame to every relay in relays. It waits — bounded by the
// controller's maximum queue wait and a bounded waiter count — for the lane,
// the aggregate budget, and every relay's wire budget, then consumes all of
// them together. The kill switch rejects immediately.
func (a *Admission) AdmitOpaque(ctx context.Context, purpose Purpose, relayURLs []string) error {
	if a == nil {
		return ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	relays := normalizeRelayList(relayURLs)
	if len(relays) == 0 {
		return ErrNoDestinations
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.metrics.Attempted++
	if err := a.killSwitchLocked(); err != nil {
		return err
	}
	if _, ok := a.lanes[purpose]; !ok {
		return fmt.Errorf("%w: unknown purpose %q", ErrOperation, purpose)
	}
	if a.opaqueWaiters >= a.maxOpaqueWaiters {
		a.metrics.QueueRejected++
		return fmt.Errorf("%w: %d opaque publications waiting", ErrQueueFull, a.opaqueWaiters)
	}
	now := a.clock.Now()
	buckets := make([]*relayBucket, 0, len(relays))
	for _, relay := range relays {
		rb, err := a.relayBucketLocked(now, relay)
		if err != nil {
			for _, previous := range buckets {
				previous.refs--
			}
			return err
		}
		rb.refs++
		buckets = append(buckets, rb)
	}
	a.opaqueWaiters++
	defer func() {
		a.opaqueWaiters--
		for _, rb := range buckets {
			rb.refs--
		}
	}()

	err := a.waitLocked(ctx, now.Add(a.maxQueueWait), func(now time.Time) (time.Duration, error) {
		if err := a.killSwitchLocked(); err != nil {
			return 0, err
		}
		wait := a.breakerWaitLocked(now)
		wait = max(wait, a.aggregate.wait(now), a.lanes[purpose].wait(now))
		for _, rb := range buckets {
			wait = max(wait, rb.forPurpose(purpose).wait(now))
		}
		if wait > 0 {
			return wait, nil
		}
		a.aggregate.tokens--
		a.lanes[purpose].tokens--
		for _, rb := range buckets {
			rb.forPurpose(purpose).tokens--
		}
		return 0, nil
	})
	if err != nil {
		if errors.Is(err, ErrQueueTimeout) {
			a.metrics.QueueRejected++
		}
		return err
	}
	a.metrics.Admitted++
	a.metrics.OpaqueAdmitted++
	a.metrics.WireAttempts += uint64(len(buckets))
	return nil
}
