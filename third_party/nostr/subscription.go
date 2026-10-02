package nostr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNotConnected = errors.New("not connected")
	ErrFireFailed   = errors.New("failed to fire")
)

// subscriptionInboxCap is the maximum number of events buffered in a
// subscription's ordered inbox before the subscription is closed with
// an overflow error. Consumers (e.g. Bahia's RelayPool) resubscribe
// from their resume cursor. The cap is generous enough for normal relay
// bursts but bounded to prevent unbounded memory growth from a stuck
// consumer.
const subscriptionInboxCap = 4096

// dispatchItem is an event queued in the per-subscription inbox for FIFO delivery.
type dispatchItem struct {
	event    Event
	isStored bool
}

// subscriptionInbox is a non-blocking ordered queue for per-subscription event
// delivery (bahia-irsry.58). dispatchEvent (which runs on the relay's main-loop
// goroutine) appends items without blocking; a dispatcher goroutine drains the
// queue and delivers to Events in FIFO order.
//
// The read loop must never block: it also delivers OK, EOSE, CLOSED, AUTH and
// NOTICE for every subscription and publish on the connection. A blocking inbox
// would stall all of them (head-of-line blocking) and deadlock a consumer that
// publishes to the same relay and waits for OK inside an event handler.
type subscriptionInbox struct {
	mu     sync.Mutex
	items  []dispatchItem
	signal chan struct{} // 1-buffered; wakes the dispatcher
	closed bool
	cap    int
}

func newSubscriptionInbox(cap int) *subscriptionInbox {
	return &subscriptionInbox{
		signal: make(chan struct{}, 1),
		cap:    cap,
	}
}

const (
	pushOK       = iota // item queued successfully
	pushOverflow        // item NOT queued; inbox at capacity
	pushClosed          // item NOT queued; inbox already closed
)

// push appends an item to the queue without blocking the caller. Returns
// pushOK on success, pushOverflow when the queue has reached its capacity
// (the item is not queued and the inbox is marked closed), or pushClosed
// when the inbox was already closed or overflowed.
func (q *subscriptionInbox) push(item dispatchItem) int {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return pushClosed
	}
	if len(q.items) >= q.cap {
		q.closed = true
		q.mu.Unlock()
		// Wake the dispatcher so it can finish draining before the
		// subscription is closed.
		select {
		case q.signal <- struct{}{}:
		default:
		}
		return pushOverflow
	}
	q.items = append(q.items, item)
	q.mu.Unlock()
	// Non-blocking wake: the 1-slot buffer ensures the dispatcher sees
	// at least one signal per batch of pushes.
	select {
	case q.signal <- struct{}{}:
	default:
	}
	return pushOK
}

// drain moves all queued items out of the inbox. The caller owns the returned
// slice.
func (q *subscriptionInbox) drain() []dispatchItem {
	q.mu.Lock()
	items := q.items
	q.items = nil
	q.mu.Unlock()
	return items
}

// close marks the inbox as closed so no more items can be pushed, and wakes the
// dispatcher so it can exit.
func (q *subscriptionInbox) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// Subscription represents a subscription to a relay.
type Subscription struct {
	counter int64
	id      string

	Relay  *Relay
	Filter Filter

	// for this to be treated as a COUNT and not a REQ this must be set
	countResult chan CountEnvelope

	// the Events channel emits all EVENTs that come in a Subscription
	// will be closed when the subscription ends
	Events chan Event

	// mu guards the closing of Events and countResult. Senders hold the
	// read lock for the whole send and check channelsClosed first; the teardown
	// goroutine takes the write lock, sets channelsClosed and closes the
	// channels. Because teardown only runs after Context is done, and every
	// sender also selects on Context.Done(), the write lock is always granted
	// promptly. (bahia-irsry.17: without this, a CLOSED from the relay could
	// close Events while a dispatch goroutine was sending on it.)
	mu             sync.RWMutex
	channelsClosed bool

	// inbox is the per-subscription non-blocking ordered delivery queue.
	// Events are enqueued by dispatchEvent (called on the relay's main-loop
	// goroutine) without blocking, and delivered to Events in FIFO order by
	// the dispatcher goroutine started in PrepareSubscription. nil for
	// subscriptions created without PrepareSubscription (test helpers).
	// (bahia-irsry.58)
	inbox          *subscriptionInbox
	dispatcherDone chan struct{} // closed when the dispatcher goroutine exits

	// the EndOfStoredEvents channel receives a value when an EOSE comes for that subscription
	EndOfStoredEvents chan EndOfStoredEvent

	// the ClosedReason channel emits the reason when a CLOSED message is received
	ClosedReason chan string

	// Context will be .Done() when the subscription ends
	Context context.Context

	// if it is not nil, checkDuplicate will be called for every event received
	// if it returns true that event will not be processed further.
	checkDuplicate func(id ID, relay string) bool

	// if it is not nil, checkDuplicateReplaceable will be called for every event received
	// if it returns true that event will not be processed further.
	checkDuplicateReplaceable func(rk ReplaceableKey, ts Timestamp) bool

	match        func(Event) bool // this will be either Filters.Match or Filters.MatchIgnoringTimestampConstraints
	live         atomic.Bool
	eosed        atomic.Bool
	eoseTimedOut chan struct{}
	cancel       context.CancelCauseFunc

	// closedHandled guards handleClosed so that a second CLOSED from the relay
	// does not leak a goroutine. (bahia-irsry.26)
	closedHandled atomic.Bool

	// this keeps track of the events we've received before the EOSE that we must dispatch before
	// closing the EndOfStoredEvents channel
	storedwg sync.WaitGroup
}

// EndOfStoredEvent is emitted on Subscription.EndOfStoredEvents when an EOSE arrives.
type EndOfStoredEvent struct {
	// Hint holds the NIP-67 EOSE completeness hint(s) (e.g. "more"/"finish"), or nil.
	Hint []string
}

// All SubscriptionOptions fields are optional
type SubscriptionOptions struct {
	// Label puts a label on the subscription (it is prepended to the automatic id) that is sent to relays.
	Label string

	// CheckDuplicate is a function that, when present, is ran on events before they're parsed.
	// if it returns true the event will be discarded and not processed further.
	CheckDuplicate func(id ID, relay string) bool

	// CheckDuplicateReplaceable is like CheckDuplicate, but runs on replaceable/addressable events
	CheckDuplicateReplaceable func(rk ReplaceableKey, ts Timestamp) bool

	// a fake EndOfStoredEvents will be dispatched at this time if nothing is received before.
	// defaults to 7s (in order to disable, set it to time.Duration(math.MaxInt64))
	MaxWaitForEOSE time.Duration

	// isCount is set by countInternal so that PrepareSubscription creates
	// countResult before the subscription is stored in the map.
	// (bahia-irsry.26: fixes the race where a COUNT reply arrives before
	// countInternal can set countResult.)
	isCount bool
}

// GetID returns the subscription ID.
func (sub *Subscription) GetID() string { return sub.id }

func (sub *Subscription) dispatchEvent(evt Event) {
	isStored := false
	if !sub.eosed.Load() {
		sub.storedwg.Add(1)
		isStored = true
	}

	if q := sub.inbox; q != nil {
		// Non-blocking path (bahia-irsry.58): enqueue via the inbox FIFO.
		// The dispatcher goroutine delivers items to Events in order.
		// push() never blocks the caller (the relay's main-loop goroutine).
		switch q.push(dispatchItem{event: evt, isStored: isStored}) {
		case pushOK:
			return
		case pushOverflow:
			if isStored {
				sub.storedwg.Done()
			}
			sub.handleClosed("error: subscription inbox overflow")
			return
		case pushClosed:
			if isStored {
				sub.storedwg.Done()
			}
			return
		}
	}

	// Legacy path: per-event goroutine (used by test helpers that create
	// subscriptions without PrepareSubscription).
	go func() {
		if isStored {
			defer sub.storedwg.Done()
		}

		// hold the read lock across the send so the teardown goroutine cannot
		// close Events underneath us (see the comment on sub.mu).
		sub.mu.RLock()
		defer sub.mu.RUnlock()
		if sub.channelsClosed || !sub.live.Load() {
			return
		}

		if isStored {
			select {
			case sub.Events <- evt:
			case <-sub.Context.Done():
			case <-sub.eoseTimedOut:
			}
		} else {
			select {
			case sub.Events <- evt:
			case <-sub.Context.Done():
			}
		}
	}()
}

// dispatchCount delivers a COUNT reply, unless the subscription has already
// been torn down (in which case countResult is closed and nobody is waiting).
func (sub *Subscription) dispatchCount(env CountEnvelope) {
	sub.mu.RLock()
	defer sub.mu.RUnlock()
	if sub.channelsClosed {
		return
	}
	select {
	case sub.countResult <- env:
	case <-sub.Context.Done():
	}
}

func (sub *Subscription) dispatchEose(hint []string) {
	if sub.eosed.CompareAndSwap(false, true) {
		sub.match = sub.Filter.MatchesIgnoringTimestampConstraints
		go func() {
			sub.storedwg.Wait()
			sub.EndOfStoredEvents <- EndOfStoredEvent{Hint: hint}
		}()
	}
}

// handleClosed handles the CLOSED message from a relay.
func (sub *Subscription) handleClosed(reason string) {
	// Guard against a second CLOSED leaking a goroutine. A relay may send
	// CLOSED more than once (e.g. an overflow close followed by a
	// disconnect close), but only the first should be delivered.
	// (bahia-irsry.26)
	if !sub.closedHandled.CompareAndSwap(false, true) {
		return
	}
	go func() {
		// A relay can close a completed ID query immediately after EOSE.
		// Deliver pending history before notifying consumers or canceling it.
		sub.storedwg.Wait()
		sub.ClosedReason <- reason
		sub.live.Store(false) // set this so we don't send an unnecessary CLOSE to the relay
		sub.cancel(fmt.Errorf("CLOSED received: %s", reason))
	}()
}

// Unsub closes the subscription, sending "CLOSE" to relay as in NIP-01.
// Unsub() also closes the channel sub.Events and makes a new one.
func (sub *Subscription) Unsub() {
	sub.cancel(errors.New("Unsub() called"))
}

// Sub sets sub.Filters and then calls sub.Fire(ctx).
// The subscription will be closed if the context expires.
func (sub *Subscription) Sub(_ context.Context, filter Filter) {
	sub.Filter = filter
	sub.Fire()
}

// Fire sends the "REQ" command to the relay.
func (sub *Subscription) Fire() error {
	var reqb []byte
	if sub.countResult == nil {
		reqb, _ = ReqEnvelope{sub.id, []Filter{sub.Filter}}.MarshalJSON()
	} else {
		reqb, _ = CountEnvelope{sub.id, sub.Filter, nil, nil}.MarshalJSON()
	}

	sub.live.Store(true)
	if err := sub.Relay.WriteWithError(reqb); err != nil {
		err := fmt.Errorf("failed to write: %w", err)
		sub.cancel(err)
		return err
	}

	return nil
}
