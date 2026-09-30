package relaysidecar

import (
	"context"
	"iter"
	"sync"
	"sync/atomic"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/config"
	"go.uber.org/zap"
)

// defaultSubscriberQueueSize bounds the live events buffered for one client
// connection when nostr.sidecar.subscriber_queue_size is unset. It absorbs
// publisher bursts. A connection that falls further behind than this has the
// affected subscription CLOSED instead of silently missing events.
const defaultSubscriberQueueSize = config.DefaultRelaySidecarSubscriberQueueSize

// subscriberOverflowReason is the NIP-01 CLOSED reason sent to a subscription
// whose live delivery queue overflowed. The "error:" prefix is machine-readable.
// Stored events are recoverable by re-subscribing from the client's cursor.
// Ephemeral events that did not fit are lost to this subscription only, and the
// CLOSED makes that loss explicit.
const subscriberOverflowReason = "error: live delivery queue overflowed; re-subscribe from your last received event"

const (
	subscriptionLive int32 = iota
	subscriptionOverflowed
	subscriptionRemoved
)

// liveSubscription is one client subscription id on one connection. A REQ with
// several filters registers one khatru listener per filter, and all of them
// share this entry so that an event matching several filters is delivered once.
type liveSubscription struct {
	id      string
	filters map[int]nostr.Filter // khatru ssid -> filter; guarded by liveFanout.mu
	state   atomic.Int32
}

func (s *liveSubscription) matches(event nostr.Event) bool {
	for _, filter := range s.filters {
		if filter.Matches(event) {
			return true
		}
	}
	return false
}

// pendingListener closes khatru's REQ gap. Khatru runs a filter's stored query
// first and registers its live listener afterwards, so an event saved in
// between would be neither replayed nor delivered live. The fanout registers a
// pendingListener from OnRequest, before the stored query starts, and buffers
// every live event that matches while the query runs. When khatru adds the
// listener, the buffered events the stored query did not already emit are
// queued ahead of any later live event.
type pendingListener struct {
	ctx    context.Context // the REQ context khatru passes to OnRequest and QueryStored
	filter nostr.Filter

	mu         sync.Mutex
	buffered   []*nostr.Event
	overflowed bool                  // more than queueSize events matched during the query
	stored     map[nostr.ID]struct{} // ids the stored query emitted for this filter
}

func (p *pendingListener) buffer(event *nostr.Event, limit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.overflowed {
		return
	}
	if len(p.buffered) >= limit {
		p.overflowed = true
		p.buffered = nil
		return
	}
	p.buffered = append(p.buffered, event)
}

func (p *pendingListener) recordStored(id nostr.ID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stored == nil {
		p.stored = make(map[nostr.ID]struct{})
	}
	p.stored[id] = struct{}{}
}

// unseen returns the buffered events the stored query did not emit, or
// overflowed=true if the buffer could not hold every match.
func (p *pendingListener) unseen() (events []*nostr.Event, overflowed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.overflowed {
		return nil, true
	}
	for _, event := range p.buffered {
		if _, dup := p.stored[event.ID]; !dup {
			events = append(events, event)
		}
	}
	return events, false
}

// liveDelivery holds a pointer so that a queue slot costs two words, not a
// copy of the event. The event is shared, read-only, by every matching queue.
type liveDelivery struct {
	sub   *liveSubscription
	event *nostr.Event
}

// liveConnection owns the bounded delivery queue and the single writer for one
// websocket, so that a slow connection only ever delays itself.
type liveConnection struct {
	ws      *khatru.WebSocket
	queue   chan liveDelivery
	subs    map[string]*liveSubscription // guarded by liveFanout.mu
	pending map[string]*pendingListener  // by subscription id; guarded by liveFanout.mu

	closeMu sync.Mutex
	closing []*liveSubscription // overflowed: send CLOSED, then remove their listeners
	orphans []int               // khatru listeners of already-closed REQs: remove silently
	wake    chan struct{}
}

func (c *liveConnection) takeClosing() ([]*liveSubscription, []int) {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	closing, orphans := c.closing, c.orphans
	c.closing, c.orphans = nil, nil
	return closing, orphans
}

func (c *liveConnection) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// scheduleRemoval asks the connection's writer to remove a khatru listener
// that must never deliver: one registered for a REQ that was already closed.
func (c *liveConnection) scheduleRemoval(ssid int) {
	c.closeMu.Lock()
	c.orphans = append(c.orphans, ssid)
	c.closeMu.Unlock()
	c.signal()
}

// liveFanout replaces khatru's synchronous broadcast. Khatru writes to every
// matching listener inline, before it sends the publisher's OK, so one slow
// subscriber stalls every publisher. Khatru's PreventBroadcast hook doesn't
// identify the subscription, so it can't be used to route into per-subscriber
// queues. Instead, the fanout tracks listeners through khatru's
// OnListenerAdded/OnListenerRemoved hooks, matches with nostr.Filter.Matches,
// and hands each match to the subscriber connection's bounded queue without
// blocking.
//
// No event is dropped silently. When a connection's queue is full, the
// subscription that couldn't take the event gets a NIP-01 CLOSED, so the client
// re-REQs from its cursor, and its khatru listeners are removed server-side
// (khatru.Relay.RemoveListeners). Other subscribers, and other subscriptions on
// the same connection that still fit, keep receiving every event.
type liveFanout struct {
	logger    *zap.Logger
	queueSize int
	write     func(*khatru.WebSocket, any) error
	// removeListeners is khatru.Relay.RemoveListeners once installed. It is
	// only called from a connection's writer goroutine, never under mu:
	// khatru calls OnListenerRemoved, which takes mu, with its client lock held.
	removeListeners func(*khatru.WebSocket, ...int)

	overflowCloses atomic.Uint64

	mu    sync.RWMutex
	conns map[*khatru.WebSocket]*liveConnection
}

func newLiveFanout(logger *zap.Logger, queueSize int) *liveFanout {
	if logger == nil {
		logger = zap.NewNop()
	}
	if queueSize <= 0 {
		queueSize = defaultSubscriberQueueSize
	}
	return &liveFanout{
		logger:    logger,
		queueSize: queueSize,
		write:     func(ws *khatru.WebSocket, v any) error { return ws.WriteJSON(v) },
		conns:     make(map[*khatru.WebSocket]*liveConnection),
	}
}

// install takes over live delivery from khatru. Khatru still owns REQ parsing,
// stored-event replay, EOSE and listener lifecycle. The caller must route saved
// and ephemeral events to dispatch, call beginRequest from OnRequest once the
// filter is accepted, and wrap QueryStored results with trackStored.
func (f *liveFanout) install(relay *khatru.Relay) {
	relay.PreventBroadcast = func(_ *khatru.WebSocket, _ nostr.Filter, _ nostr.Event) bool {
		return true
	}
	relay.OnListenerAdded = f.listenerAdded
	relay.OnListenerRemoved = f.listenerRemoved
	f.removeListeners = relay.RemoveListeners
}

// OverflowCloses reports how many subscriptions were CLOSED because their
// connection's live delivery queue overflowed.
func (f *liveFanout) OverflowCloses() uint64 {
	return f.overflowCloses.Load()
}

func (f *liveFanout) subscriberQueueSize() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.queueSize
}

// connection returns the entry for ws, creating it and its writer. Callers
// hold f.mu for writing.
func (f *liveFanout) connection(ws *khatru.WebSocket) *liveConnection {
	conn := f.conns[ws]
	if conn == nil {
		conn = &liveConnection{
			ws:      ws,
			queue:   make(chan liveDelivery, f.queueSize),
			subs:    make(map[string]*liveSubscription),
			pending: make(map[string]*pendingListener),
			wake:    make(chan struct{}, 1),
		}
		f.conns[ws] = conn
		go f.deliver(conn)
	}
	return conn
}

// beginRequest runs from OnRequest after the filter is accepted and before
// khatru's stored query for it, and starts buffering live matches for the
// listener khatru will add once the query ends. Khatru processes a REQ's
// filters one after another, so at most one is pending per subscription id.
func (f *liveFanout) beginRequest(ctx context.Context, filter nostr.Filter) {
	if filter.IDs != nil {
		return // khatru never adds a live listener for an ids filter
	}
	ws := khatru.GetConnection(ctx)
	id := khatru.GetSubscriptionID(ctx)
	if ws == nil || id == "" {
		return // not a REQ (e.g. NIP-77 negentropy)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connection(ws).pending[id] = &pendingListener{ctx: ctx, filter: filter}
}

// trackStored records the ids a REQ's stored query emits, so that events
// buffered by beginRequest that the query also returned aren't sent twice.
func (f *liveFanout) trackStored(ctx context.Context, events iter.Seq[nostr.Event]) iter.Seq[nostr.Event] {
	pending := f.pendingFor(ctx)
	if pending == nil {
		return events
	}
	return func(yield func(nostr.Event) bool) {
		for event := range events {
			pending.recordStored(event.ID)
			if !yield(event) {
				return
			}
		}
	}
}

func (f *liveFanout) pendingFor(ctx context.Context) *pendingListener {
	ws := khatru.GetConnection(ctx)
	id := khatru.GetSubscriptionID(ctx)
	if ws == nil || id == "" {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	conn := f.conns[ws]
	if conn == nil {
		return nil
	}
	if pending := conn.pending[id]; pending != nil && pending.ctx == ctx {
		return pending
	}
	return nil
}

func (f *liveFanout) listenerAdded(ws *khatru.WebSocket, ssid int, id string, filter nostr.Filter) {
	if ws == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	conn := f.connection(ws)
	pending := conn.pending[id]
	delete(conn.pending, id)
	if pending != nil && pending.ctx.Err() != nil {
		// The REQ was closed while its stored query ran (by our own overflow
		// CLOSED, a client CLOSE or a replacing REQ), yet khatru still registers
		// the listener. It must never deliver: remove it.
		conn.scheduleRemoval(ssid)
		return
	}
	sub := conn.subs[id]
	if sub == nil {
		sub = &liveSubscription{id: id, filters: make(map[int]nostr.Filter, 1)}
		conn.subs[id] = sub
	} else if sub.state.Load() != subscriptionLive {
		// A later filter of a REQ whose subscription was already CLOSED.
		conn.scheduleRemoval(ssid)
		return
	}
	sub.filters[ssid] = filter
	if pending == nil {
		return
	}
	// Queue what matched during the stored query. dispatch is excluded by mu,
	// so these precede every later live event for the subscription.
	events, overflowed := pending.unseen()
	if overflowed {
		f.overflow(conn, sub, nil)
		return
	}
	for _, event := range events {
		if !filter.Matches(*event) {
			continue // the pending entry belonged to a concurrent REQ reusing the id
		}
		select {
		case conn.queue <- liveDelivery{sub: sub, event: event}:
		default:
			f.overflow(conn, sub, event)
			return
		}
	}
}

func (f *liveFanout) listenerRemoved(ws *khatru.WebSocket, ssid int, id string, _ nostr.Filter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	conn := f.conns[ws]
	if conn == nil {
		return
	}
	sub := conn.subs[id]
	if sub == nil {
		return
	}
	if _, member := sub.filters[ssid]; !member {
		return // an orphan listener, or one of an earlier REQ under this id
	}
	delete(sub.filters, ssid)
	if len(sub.filters) == 0 {
		// Queued deliveries for this subscription are discarded by the writer.
		// A later REQ that reuses the id gets a fresh entry.
		sub.state.Store(subscriptionRemoved)
		delete(conn.subs, id)
	}
}

// dispatch queues event for every live subscription it matches, and buffers it
// for every REQ whose stored query is still running. It never blocks, so the
// publisher's OK is not held up by any subscriber.
func (f *liveFanout) dispatch(event nostr.Event) {
	shared := &event
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, conn := range f.conns {
		var reached map[string]struct{}
		for _, sub := range conn.subs {
			if sub.state.Load() != subscriptionLive || !sub.matches(event) {
				continue
			}
			if len(conn.pending) > 0 {
				if reached == nil {
					reached = make(map[string]struct{}, 1)
				}
				reached[sub.id] = struct{}{}
			}
			select {
			case conn.queue <- liveDelivery{sub: sub, event: shared}:
			default:
				f.overflow(conn, sub, shared)
			}
		}
		for id, pending := range conn.pending {
			if _, done := reached[id]; done {
				continue // an earlier filter of the same REQ already has it live
			}
			if pending.filter.Matches(event) {
				pending.buffer(shared, f.queueSize)
			}
		}
	}
}

// overflow marks sub closed and wakes the connection's writer to send CLOSED.
// event, when known, is the one that did not fit.
func (f *liveFanout) overflow(conn *liveConnection, sub *liveSubscription, event *nostr.Event) {
	if !sub.state.CompareAndSwap(subscriptionLive, subscriptionOverflowed) {
		return
	}
	conn.closeMu.Lock()
	conn.closing = append(conn.closing, sub)
	conn.closeMu.Unlock()
	conn.signal()
	fields := []zap.Field{
		zap.String("subscription_id", sub.id),
		zap.Int("queue_size", cap(conn.queue)),
	}
	if event != nil {
		fields = append(fields, zap.String("event_id", event.ID.Hex()), zap.Uint16("kind", uint16(event.Kind)))
	}
	if conn.ws.Request != nil {
		fields = append(fields, zap.String("remote_ip", khatru.GetIPFromRequest(conn.ws.Request)))
	}
	f.logger.Warn("relay-sidecar subscriber fell behind live delivery; closing subscription so it re-subscribes from its cursor", fields...)
}

func (f *liveFanout) listenerIDs(sub *liveSubscription) []int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ssids := make([]int, 0, len(sub.filters))
	for ssid := range sub.filters {
		ssids = append(ssids, ssid)
	}
	return ssids
}

// deliver is the only writer of live EVENT and overflow CLOSED frames on a
// connection. Because it is the only writer, no EVENT for a subscription is
// written after that subscription's CLOSED.
func (f *liveFanout) deliver(conn *liveConnection) {
	defer f.dropConnection(conn)
	broken := false
	send := func(envelope any) {
		if broken {
			return
		}
		if err := f.write(conn.ws, envelope); err != nil {
			// A failed websocket write leaves the connection unusable. Khatru's
			// ping loop then disconnects it, and the client reconnects and
			// re-subscribes. Stop writing so a dead peer doesn't hold up the
			// drain.
			broken = true
			f.logger.Warn("relay-sidecar live delivery write failed; connection will be dropped", zap.Error(err))
		}
	}
	for {
		select {
		case <-conn.ws.Context.Done():
			return
		case <-conn.wake:
			closing, remove := conn.takeClosing()
			for _, sub := range closing {
				if sub.state.Load() != subscriptionOverflowed {
					continue // the client already closed or replaced it
				}
				send(nostr.ClosedEnvelope{SubscriptionID: sub.id, Reason: subscriberOverflowReason})
				f.overflowCloses.Add(1)
				// Remove by ssid, not id: if the client re-REQs the same id
				// meanwhile, khatru has already dropped these and the new
				// listeners are untouched.
				remove = append(remove, f.listenerIDs(sub)...)
			}
			if len(remove) > 0 && f.removeListeners != nil {
				f.removeListeners(conn.ws, remove...)
			}
		case delivery := <-conn.queue:
			if delivery.sub.state.Load() != subscriptionLive {
				continue
			}
			id := delivery.sub.id
			send(nostr.EventEnvelope{SubscriptionID: &id, Event: *delivery.event})
		}
	}
}

func (f *liveFanout) dropConnection(conn *liveConnection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conns[conn.ws] == conn {
		delete(f.conns, conn.ws)
	}
	for _, sub := range conn.subs {
		sub.state.Store(subscriptionRemoved)
	}
	conn.pending = nil
}
