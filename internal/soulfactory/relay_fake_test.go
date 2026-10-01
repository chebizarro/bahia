package soulfactory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/coder/websocket"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

// Test names for the pool's stored-event vocabulary.
type RelayStoredEventsStatus = nostradapter.RelayStoredStatus

const (
	RelayStoredEventsPending = nostradapter.RelayStoredPending
	RelayStoredEventsEOSE    = nostradapter.RelayStoredEOSE
	RelayStoredEventsClosed  = nostradapter.RelayStoredClosed
)

var ErrRelayReadIncomplete = nostradapter.ErrStoredEventsIncomplete

// fakeRelayEndpoint is a scripted in-process NIP-01 relay. SoulFactory talks
// to it through the real relay pool over a websocket, so tests exercise the
// same stack as production. The script mirrors the endpoint fake the retired
// relay bus used:
//
//   - publishResults / publishFn answer each EVENT in order with OK true,
//     OK false + Reason, or, for Error, a dropped connection. Every EVENT is
//     recorded in published.
//   - Each REQ (the pool sends one filter per REQ) is recorded on
//     subscribeCalls and answered by the next fakeRelaySubscription from
//     subscribeQueue: its events become EVENT frames, eose (sent or closed)
//     becomes EOSE, a closed reason becomes CLOSED, and err, or closing
//     events, drops the connection. With autoEOSE every REQ gets EOSE at once.
//   - The relay sends an AUTH challenge when a connection opens; each AUTH
//     frame is recorded on authCalls and answered OK true.
type fakeRelayEndpoint struct {
	t      *testing.T
	url    string
	server *httptest.Server

	publishResults []RelayPublishResult
	publishCalls   int
	published      []nostr.Event
	publishFn      func(context.Context, nostr.Event) RelayPublishResult

	subscribeQueue chan *fakeRelaySubscription
	subscribeCalls chan []nostr.Filter
	authCalls      chan struct{}
	autoEOSE       bool
	noChallenge    bool
	// scriptFor, when set, picks the answer to each REQ by its filters
	// instead of taking the next one from subscribeQueue.
	scriptFor func([]nostr.Filter) *fakeRelaySubscription

	mu sync.Mutex
}

type fakeRelaySubscription struct {
	events chan *nostr.Event
	eose   chan struct{}
	closed chan string
	err    error
}

func newFakeRelaySubscription() *fakeRelaySubscription {
	return &fakeRelaySubscription{
		events: make(chan *nostr.Event, 8),
		eose:   make(chan struct{}, 1),
		closed: make(chan string, 2),
	}
}

func newFakeRelayEndpoint(t *testing.T) *fakeRelayEndpoint {
	t.Helper()
	e := &fakeRelayEndpoint{
		t:              t,
		subscribeQueue: make(chan *fakeRelaySubscription, 32),
		subscribeCalls: make(chan []nostr.Filter, 64),
		authCalls:      make(chan struct{}, 64),
	}
	e.server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.server.Close)
	e.url = "ws" + strings.TrimPrefix(e.server.URL, "http")
	return e
}

func (e *fakeRelayEndpoint) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Accept") == "application/nostr+json" {
		http.NotFound(w, r)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 24)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &fakeRelayConn{endpoint: e, conn: conn, ctx: ctx, drop: cancel, subs: map[string]context.CancelFunc{}}
	if !e.noChallenge {
		c.write(fmt.Sprintf(`["AUTH",%q]`, "fake-challenge"))
	}
	reqs := make(chan fakeREQ, 64)
	go c.answerREQs(reqs)
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var frame []json.RawMessage
		if json.Unmarshal(msg, &frame) != nil || len(frame) < 2 {
			continue
		}
		var verb string
		_ = json.Unmarshal(frame[0], &verb)
		switch verb {
		case "EVENT":
			var event nostr.Event
			if json.Unmarshal(frame[1], &event) != nil {
				continue
			}
			c.handlePublish(event)
		case "AUTH":
			var event nostr.Event
			if json.Unmarshal(frame[1], &event) != nil {
				continue
			}
			e.authCalls <- struct{}{}
			c.write(fmt.Sprintf(`["OK",%q,true,""]`, event.ID.Hex()))
		case "REQ":
			var subID string
			_ = json.Unmarshal(frame[1], &subID)
			filters := make([]nostr.Filter, 0, len(frame)-2)
			for _, raw := range frame[2:] {
				var filter nostr.Filter
				_ = json.Unmarshal(raw, &filter)
				filters = append(filters, filter)
			}
			if isAuthBarrier(filters) {
				c.write(fmt.Sprintf(`["EOSE",%q]`, subID))
				continue
			}
			subCtx, stop := context.WithCancel(ctx)
			c.mu.Lock()
			c.subs[subID] = stop
			c.mu.Unlock()
			select {
			case reqs <- fakeREQ{id: subID, filters: filters, ctx: subCtx}:
			case <-ctx.Done():
				return
			}
		case "CLOSE":
			var subID string
			_ = json.Unmarshal(frame[1], &subID)
			c.mu.Lock()
			if stop := c.subs[subID]; stop != nil {
				stop()
				delete(c.subs, subID)
			}
			c.mu.Unlock()
		}
	}
}

func isAuthBarrier(filters []nostr.Filter) bool {
	return len(filters) == 1 && len(filters[0].Kinds) == 1 && filters[0].Kinds[0] == nostr.KindClientAuthentication
}

type fakeREQ struct {
	id      string
	filters []nostr.Filter
	ctx     context.Context
}

type fakeRelayConn struct {
	endpoint *fakeRelayEndpoint
	conn     *websocket.Conn
	ctx      context.Context
	drop     context.CancelFunc

	writeMu sync.Mutex
	mu      sync.Mutex
	subs    map[string]context.CancelFunc
}

func (c *fakeRelayConn) write(frame string) bool {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(c.ctx, websocket.MessageText, []byte(frame)) == nil
}

func (c *fakeRelayConn) handlePublish(event nostr.Event) {
	e := c.endpoint
	e.mu.Lock()
	e.published = append(e.published, event)
	var result RelayPublishResult
	publishFn := e.publishFn
	if publishFn == nil {
		result = RelayPublishResult{Error: errors.New("unexpected publish")}
		if e.publishCalls < len(e.publishResults) {
			result = e.publishResults[e.publishCalls]
		}
	}
	e.publishCalls++
	e.mu.Unlock()
	if publishFn != nil {
		result = publishFn(c.ctx, event)
	}
	switch {
	case result.Error != nil:
		c.drop()
	case result.Accepted:
		c.write(fmt.Sprintf(`["OK",%q,true,""]`, event.ID.Hex()))
	default:
		reason, _ := json.Marshal(result.Reason)
		c.write(fmt.Sprintf(`["OK",%q,false,%s]`, event.ID.Hex(), reason))
	}
}

// answerREQs answers REQs in arrival order, each from the next scripted
// subscription.
func (c *fakeRelayConn) answerREQs(reqs <-chan fakeREQ) {
	e := c.endpoint
	for {
		var req fakeREQ
		select {
		case req = <-reqs:
		case <-c.ctx.Done():
			return
		}
		var script *fakeRelaySubscription
		switch {
		case e.scriptFor != nil:
			recordNonBlocking(e.subscribeCalls, req.filters)
			script = e.scriptFor(req.filters)
		case e.autoEOSE:
			recordNonBlocking(e.subscribeCalls, req.filters)
			script = eoseScript()
		default:
			select {
			case e.subscribeCalls <- req.filters:
			case <-c.ctx.Done():
				return
			}
			select {
			case script = <-e.subscribeQueue:
			case <-c.ctx.Done():
				return
			}
		}
		if script.err != nil {
			c.drop()
			return
		}
		go c.play(req, script)
	}
}

func (c *fakeRelayConn) play(req fakeREQ, script *fakeRelaySubscription) {
	events, eose, closed := script.events, script.eose, script.closed
	sendEvent := func(event *nostr.Event) bool {
		raw, err := json.Marshal(event)
		if err != nil {
			return true
		}
		return c.write(fmt.Sprintf(`["EVENT",%q,%s]`, req.id, raw))
	}
	for {
		select {
		case <-req.ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				c.drop()
				return
			}
			if event != nil && !sendEvent(event) {
				return
			}
		case <-eose:
			// Events queued before EOSE belong to the backfill.
		drain:
			for {
				select {
				case event, ok := <-events:
					if !ok {
						events = nil
						break drain
					}
					if event != nil && !sendEvent(event) {
						return
					}
				default:
					break drain
				}
			}
			eose = nil
			if !c.write(fmt.Sprintf(`["EOSE",%q]`, req.id)) {
				return
			}
			if events == nil {
				c.drop()
				return
			}
		case reason := <-closed:
			reasonJSON, _ := json.Marshal(reason)
			c.write(fmt.Sprintf(`["CLOSED",%q,%s]`, req.id, reasonJSON))
			return
		}
	}
}

func recordNonBlocking(calls chan []nostr.Filter, filters []nostr.Filter) {
	select {
	case calls <- filters:
	default:
	}
}

// eoseScript answers a REQ with EOSE and nothing else.
func eoseScript() *fakeRelaySubscription {
	script := newFakeRelaySubscription()
	close(script.eose)
	return script
}

// receiveREQFilters reads the next n REQs the relay received and returns their
// filters (the pool sends one filter per REQ).
func receiveREQFilters(t *testing.T, endpoint *fakeRelayEndpoint, n int) []nostr.Filter {
	t.Helper()
	var filters []nostr.Filter
	for range n {
		filters = append(filters, mustReceiveFilters(t, endpoint.subscribeCalls)...)
	}
	return filters
}

// receiveREQFor reads REQs until one has a filter for kind and returns it.
func receiveREQFor(t *testing.T, endpoint *fakeRelayEndpoint, kind nostr.Kind) nostr.Filter {
	t.Helper()
	for {
		for _, filter := range mustReceiveFilters(t, endpoint.subscribeCalls) {
			for _, k := range filter.Kinds {
				if k == kind {
					return filter
				}
			}
		}
	}
}

// fastRelayBackoff reissues REQs almost at once, so tests do not wait out the
// production backoff.
func fastRelayBackoff() *nostradapter.Backoff {
	return &nostradapter.Backoff{Initial: time.Millisecond, Max: 5 * time.Millisecond, Multiplier: 2}
}

// newRelayClientFromEndpoints returns a RelayClient over the fake relays.
func newRelayClientFromEndpoints(endpoints []*fakeRelayEndpoint, opts ...RelayClientOption) (*RelayClient, error) {
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("at least one SoulFactory relay endpoint is required")
	}
	urls := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		urls = append(urls, endpoint.url)
	}
	client, err := NewRelayClient(urls, append([]RelayClientOption{withRelayResubscribeBackoff(fastRelayBackoff)}, opts...)...)
	if err != nil {
		return nil, err
	}
	endpoints[0].t.Cleanup(client.Close)
	return client, nil
}

// newEOSEOnlyRelayClient returns a client whose one relay answers every REQ
// with EOSE and holds no events.
func newEOSEOnlyRelayClient(t *testing.T) *RelayClient {
	t.Helper()
	endpoint := newFakeRelayEndpoint(t)
	endpoint.autoEOSE = true
	client, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint})
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	return client
}

func signedRelayBusEvent(t *testing.T, signer fakeSigner, kind int, content string) *nostr.Event {
	t.Helper()
	// Tagged with its own pubkey, so the #p filters these tests subscribe
	// with match (the relay library drops events outside the REQ's filter).
	event := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Content: content, Tags: nostr.Tags{{"p", signer.pubkey}}}
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return event
}

func mustReceiveRelayEvent(t *testing.T, ch <-chan *nostr.Event) *nostr.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("event channel closed before expected event")
		}
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("no event arrived")
		return nil
	}
}

func mustReceiveFilters(t *testing.T, ch <-chan []nostr.Filter) []nostr.Filter {
	t.Helper()
	select {
	case filters, ok := <-ch:
		if !ok {
			t.Fatal("subscribe call channel closed before expected call")
		}
		return filters
	case <-time.After(10 * time.Second):
		t.Fatal("no REQ arrived")
		return nil
	}
}

func mustReceiveSignal[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func containsAll(value string, wants ...string) bool {
	for _, want := range wants {
		if !strings.Contains(value, want) {
			return false
		}
	}
	return true
}
