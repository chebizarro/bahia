package relaysidecar

import (
	"sync"
	"sync/atomic"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"go.uber.org/zap"
)

// defaultSubscriberQueueSize bounds the live events buffered for one client
// connection. It absorbs publisher bursts. A connection that falls further
// behind than this has the affected subscription CLOSED instead of silently
// missing events.
const defaultSubscriberQueueSize = 1024

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

// liveDelivery holds a pointer so that a queue slot costs two words, not a
// copy of the event. The event is shared, read-only, by every matching queue.
type liveDelivery struct {
	sub   *liveSubscription
	event *nostr.Event
}

// liveConnection owns the bounded delivery queue and the single writer for one
// websocket, so that a slow connection only ever delays itself.
type liveConnection struct {
	ws    *khatru.WebSocket
	queue chan liveDelivery
	subs  map[string]*liveSubscription // guarded by liveFanout.mu

	closeMu sync.Mutex
	closing []*liveSubscription
	wake    chan struct{}
}

func (c *liveConnection) takeClosing() []*liveSubscription {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	out := c.closing
	c.closing = nil
	return out
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
// re-REQs from its cursor. Other subscribers, and other subscriptions on the
// same connection that still fit, keep receiving every event.
type liveFanout struct {
	logger    *zap.Logger
	queueSize int
	write     func(*khatru.WebSocket, any) error

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
// and ephemeral events to dispatch.
func (f *liveFanout) install(relay *khatru.Relay) {
	relay.PreventBroadcast = func(_ *khatru.WebSocket, _ nostr.Filter, _ nostr.Event) bool {
		return true
	}
	relay.OnListenerAdded = f.listenerAdded
	relay.OnListenerRemoved = f.listenerRemoved
}

func (f *liveFanout) listenerAdded(ws *khatru.WebSocket, ssid int, id string, filter nostr.Filter) {
	if ws == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	conn := f.conns[ws]
	if conn == nil {
		conn = &liveConnection{
			ws:    ws,
			queue: make(chan liveDelivery, f.queueSize),
			subs:  make(map[string]*liveSubscription),
			wake:  make(chan struct{}, 1),
		}
		f.conns[ws] = conn
		go f.deliver(conn)
	}
	sub := conn.subs[id]
	if sub == nil {
		sub = &liveSubscription{id: id, filters: make(map[int]nostr.Filter, 1)}
		conn.subs[id] = sub
	}
	sub.filters[ssid] = filter
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
	delete(sub.filters, ssid)
	if len(sub.filters) == 0 {
		// Queued deliveries for this subscription are discarded by the writer.
		// A later REQ that reuses the id gets a fresh entry.
		sub.state.Store(subscriptionRemoved)
		delete(conn.subs, id)
	}
}

// dispatch queues event for every live subscription it matches. It never
// blocks, so the publisher's OK is not held up by any subscriber.
func (f *liveFanout) dispatch(event nostr.Event) {
	shared := &event
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, conn := range f.conns {
		for _, sub := range conn.subs {
			if sub.state.Load() != subscriptionLive || !sub.matches(event) {
				continue
			}
			select {
			case conn.queue <- liveDelivery{sub: sub, event: shared}:
			default:
				f.overflow(conn, sub, event)
			}
		}
	}
}

func (f *liveFanout) overflow(conn *liveConnection, sub *liveSubscription, event nostr.Event) {
	if !sub.state.CompareAndSwap(subscriptionLive, subscriptionOverflowed) {
		return
	}
	conn.closeMu.Lock()
	conn.closing = append(conn.closing, sub)
	conn.closeMu.Unlock()
	select {
	case conn.wake <- struct{}{}:
	default:
	}
	fields := []zap.Field{
		zap.String("subscription_id", sub.id),
		zap.String("event_id", event.ID.Hex()),
		zap.Uint16("kind", uint16(event.Kind)),
		zap.Int("queue_size", cap(conn.queue)),
	}
	if conn.ws.Request != nil {
		fields = append(fields, zap.String("remote_ip", khatru.GetIPFromRequest(conn.ws.Request)))
	}
	f.logger.Warn("relay-sidecar subscriber fell behind live delivery; closing subscription so it re-subscribes from its cursor", fields...)
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
			for _, sub := range conn.takeClosing() {
				if sub.state.Load() != subscriptionOverflowed {
					continue // the client already closed or replaced it
				}
				send(nostr.ClosedEnvelope{SubscriptionID: sub.id, Reason: subscriberOverflowReason})
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
}
