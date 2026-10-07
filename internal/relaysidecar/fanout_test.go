package relaysidecar

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fanoutTestTimeout only bounds a hung test. Every wait below is on a channel
// event, not a sleep.
const fanoutTestTimeout = 30 * time.Second

// setFanoutForTest swaps the queue size and writer under the fanout lock, so
// connections created afterwards observe them without a data race.
func setFanoutForTest(f *liveFanout, queueSize int, write func(*khatru.WebSocket, any) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if queueSize > 0 {
		f.queueSize = queueSize
	}
	if write != nil {
		f.write = write
	}
}

func fanoutTestEvent(kind nostr.Kind, i int) nostr.Event {
	event := nostr.Event{Kind: kind, CreatedAt: nostr.Timestamp(1790000000 + i), Content: strconv.Itoa(i)}
	event.ID = event.GetID()
	return event
}

func nextFrame(t *testing.T, ctx context.Context, frames <-chan any) any {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-ctx.Done():
		t.Fatal("timed out waiting for a delivered frame")
		return nil
	}
}

func requireEventFrame(t *testing.T, frame any, subID string, want nostr.Event) {
	t.Helper()
	env, ok := frame.(nostr.EventEnvelope)
	require.Truef(t, ok, "frame %T, want EVENT for %q", frame, subID)
	require.Equal(t, subID, *env.SubscriptionID)
	require.Equal(t, want.ID, env.Event.ID)
}

func TestLiveFanoutClosesOnlyOverflowingSubscriptionAndNeverWritesAfterClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()

	stalled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var stallOnce sync.Once
	frames := make(chan any, 64)

	f := newLiveFanout(zap.NewNop(), 2)
	f.write = func(_ *khatru.WebSocket, v any) error {
		if env, ok := v.(nostr.EventEnvelope); ok && *env.SubscriptionID == "a" {
			stallOnce.Do(func() {
				close(stalled)
				<-release
			})
		}
		frames <- v
		return nil
	}
	ws := &khatru.WebSocket{Context: ctx}
	kindA := nostr.Filter{Kinds: []nostr.Kind{1}}
	f.listenerAdded(ws, 1, "a", kindA)
	f.listenerAdded(ws, 2, "b", nostr.Filter{Kinds: []nostr.Kind{7}})

	a := func(i int) nostr.Event { return fanoutTestEvent(1, i) }
	b := func(i int) nostr.Event { return fanoutTestEvent(7, 1000+i) }

	f.dispatch(a(0))
	select {
	case <-stalled: // the writer now holds a0; the queue (cap 2) is empty
	case <-ctx.Done():
		t.Fatal("writer never picked up the first delivery")
	}
	f.dispatch(b(0)) // queue: b0
	f.dispatch(a(1)) // queue: b0 a1 (full)
	f.dispatch(a(2)) // overflow -> "a" is CLOSED
	f.dispatch(a(3)) // "a" is no longer live: not queued, no second CLOSED
	releaseOnce.Do(func() { close(release) })

	requireEventFrame(t, nextFrame(t, ctx, frames), "a", a(0))
	var sawB0, sawClosed bool
	for range 2 {
		switch frame := nextFrame(t, ctx, frames).(type) {
		case nostr.EventEnvelope:
			requireEventFrame(t, frame, "b", b(0))
			sawB0 = true
		case nostr.ClosedEnvelope:
			require.Equal(t, "a", frame.SubscriptionID)
			require.True(t, strings.HasPrefix(frame.Reason, "error:"), "CLOSED reason %q lacks machine-readable prefix", frame.Reason)
			sawClosed = true
		default:
			t.Fatalf("unexpected frame %T", frame)
		}
	}
	require.True(t, sawB0 && sawClosed, "want b0 delivered and a CLOSED")

	// "a" stays closed and "b", on the same connection, keeps receiving. FIFO
	// order means anything written for "a" would arrive before b1.
	f.dispatch(a(4))
	f.dispatch(b(1))
	requireEventFrame(t, nextFrame(t, ctx, frames), "b", b(1))

	// The client re-REQs "a" (khatru removes the old listener, then adds the
	// new one), and live delivery resumes on the fresh subscription.
	f.listenerRemoved(ws, 1, "a", kindA)
	f.listenerAdded(ws, 3, "a", kindA)
	f.dispatch(a(5))
	requireEventFrame(t, nextFrame(t, ctx, frames), "a", a(5))

	// A REQ with two filters that both match is delivered once.
	f.listenerAdded(ws, 4, "m", nostr.Filter{Kinds: []nostr.Kind{9}})
	f.listenerAdded(ws, 5, "m", nostr.Filter{Tags: nostr.TagMap{"t": {"x"}}})
	both := nostr.Event{Kind: 9, CreatedAt: 1790000500, Tags: nostr.Tags{{"t", "x"}}}
	both.ID = both.GetID()
	f.dispatch(both)
	f.dispatch(b(2))
	requireEventFrame(t, nextFrame(t, ctx, frames), "m", both)
	requireEventFrame(t, nextFrame(t, ctx, frames), "b", b(2))
}

func startSidecarForFanoutTest(t *testing.T) (*Server, string) {
	t.Helper()
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	server, err := New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return server, httpServer.URL
}

// rawRelayClient is a minimal RFC 6455 client. It lets the tests assert the
// exact NIP-01 frames the relay sends (EVENT, EOSE, CLOSED, OK) in order,
// without the buffering and goroutine dispatch of a full Nostr client.
type rawRelayClient struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

type relayFrame struct {
	label  string
	subID  string
	event  nostr.Event
	reason string
	ok     bool
	count  uint32 // NIP-45 COUNT
}

func dialRawRelay(t *testing.T, ctx context.Context, httpURL string) *rawRelayClient {
	t.Helper()
	u, err := url.Parse(httpURL)
	require.NoError(t, err)
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", u.Host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	if deadline, ok := ctx.Deadline(); ok {
		require.NoError(t, conn.SetDeadline(deadline))
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		u.Host, base64.StdEncoding.EncodeToString(nonce))
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	return &rawRelayClient{t: t, conn: conn, br: br}
}

func (c *rawRelayClient) send(message ...any) {
	c.t.Helper()
	payload, err := json.Marshal(message)
	require.NoError(c.t, err)
	header := []byte{0x81} // FIN + text
	switch n := len(payload); {
	case n < 126:
		header = append(header, 0x80|byte(n))
	case n <= 0xffff:
		header = append(header, 0x80|126, byte(n>>8), byte(n))
	default:
		header = binary.BigEndian.AppendUint64(append(header, 0x80|127), uint64(n))
	}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err = c.conn.Write(append(append(header, mask...), masked...))
	require.NoError(c.t, err)
}

func (c *rawRelayClient) next() relayFrame {
	c.t.Helper()
	var message []byte
	for {
		head := make([]byte, 2)
		_, err := io.ReadFull(c.br, head)
		require.NoError(c.t, err, "relay connection ended")
		length := uint64(head[1] & 0x7f)
		switch length {
		case 126:
			ext := make([]byte, 2)
			_, err = io.ReadFull(c.br, ext)
			length = uint64(binary.BigEndian.Uint16(ext))
		case 127:
			ext := make([]byte, 8)
			_, err = io.ReadFull(c.br, ext)
			length = binary.BigEndian.Uint64(ext)
		}
		require.NoError(c.t, err)
		payload := make([]byte, length)
		_, err = io.ReadFull(c.br, payload)
		require.NoError(c.t, err)
		switch opcode := head[0] & 0x0f; opcode {
		case 0x8:
			c.t.Fatal("relay closed the websocket")
		case 0x9, 0xa: // ping/pong: not part of the NIP-01 stream
			continue
		default:
			message = append(message, payload...)
		}
		if head[0]&0x80 != 0 {
			break
		}
	}
	var parts []json.RawMessage
	require.NoError(c.t, json.Unmarshal(message, &parts))
	var frame relayFrame
	require.NoError(c.t, json.Unmarshal(parts[0], &frame.label))
	switch frame.label {
	case "EVENT":
		require.NoError(c.t, json.Unmarshal(parts[1], &frame.subID))
		require.NoError(c.t, json.Unmarshal(parts[2], &frame.event))
	case "EOSE":
		require.NoError(c.t, json.Unmarshal(parts[1], &frame.subID))
	case "CLOSED":
		require.NoError(c.t, json.Unmarshal(parts[1], &frame.subID))
		require.NoError(c.t, json.Unmarshal(parts[2], &frame.reason))
	case "AUTH": // NIP-42 challenge: reason carries the challenge string
		require.NoError(c.t, json.Unmarshal(parts[1], &frame.reason))
	case "NOTICE":
		require.NoError(c.t, json.Unmarshal(parts[1], &frame.reason))
	case "COUNT":
		require.NoError(c.t, json.Unmarshal(parts[1], &frame.subID))
		var count struct {
			Count uint32 `json:"count"`
		}
		require.NoError(c.t, json.Unmarshal(parts[2], &count))
		frame.count = count.Count
	case "OK":
		var id string
		require.NoError(c.t, json.Unmarshal(parts[1], &id))
		frame.subID = id
		require.NoError(c.t, json.Unmarshal(parts[2], &frame.ok))
		require.NoError(c.t, json.Unmarshal(parts[3], &frame.reason))
	}
	return frame
}

// subscribe returns after EOSE. Khatru registers the live listener before it
// writes EOSE, so the subscription is live by then. Stored events that arrive
// before EOSE are returned.
func (c *rawRelayClient) subscribe(subID string, filter nostr.Filter) map[nostr.ID]struct{} {
	c.t.Helper()
	c.send("REQ", subID, filter)
	stored := map[nostr.ID]struct{}{}
	for {
		frame := c.next()
		switch {
		case frame.label == "EOSE" && frame.subID == subID:
			return stored
		case frame.label == "EVENT" && frame.subID == subID:
			stored[frame.event.ID] = struct{}{}
		case frame.label == "CLOSED" && frame.subID == subID:
			c.t.Fatalf("subscription %s closed before EOSE: %s", subID, frame.reason)
		}
	}
}

func (c *rawRelayClient) publish(event nostr.Event) {
	c.t.Helper()
	c.send("EVENT", event)
	for {
		frame := c.next()
		if frame.label == "OK" && frame.subID == event.ID.Hex() {
			require.Truef(c.t, frame.ok, "relay rejected %s: %s", event.ID.Hex(), frame.reason)
			return
		}
	}
}

func signedFanoutEvent(t *testing.T, sk nostr.SecretKey, kind nostr.Kind, i int) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: kind, CreatedAt: nostr.Timestamp(1790000000 + i), Content: strconv.Itoa(i)}
	require.NoError(t, event.Sign(sk))
	return event
}

func TestSidecarSlowSubscriberIsClosedWhileFastSubscriberReceivesEveryEvent(t *testing.T) {
	const queueSize = 4
	const total = 40
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()

	server, relayURL := startSidecarForFanoutTest(t)
	slowStalled := make(chan struct{})
	release := make(chan struct{})
	var stallOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	// The slow subscriber is modelled at the socket-write boundary: its
	// connection's writer blocks until released, as it would behind a stalled
	// TCP window.
	setFanoutForTest(server.fanout, queueSize, func(ws *khatru.WebSocket, v any) error {
		if env, ok := v.(nostr.EventEnvelope); ok && *env.SubscriptionID == "slow" {
			stallOnce.Do(func() { close(slowStalled) })
			<-release
		}
		return ws.WriteJSON(v)
	})

	sk := nostr.Generate()
	filter := nostr.Filter{Kinds: []nostr.Kind{1}, Authors: []nostr.PubKey{sk.Public()}}
	fast := dialRawRelay(t, ctx, relayURL)
	slow := dialRawRelay(t, ctx, relayURL)
	publisher := dialRawRelay(t, ctx, relayURL)
	fast.subscribe("fast", filter)
	slow.subscribe("slow", filter)

	published := make(map[nostr.ID]struct{}, total)
	for i := range total {
		event := signedFanoutEvent(t, sk, 1, i)
		// publish returns on the relay's OK, which arrives while the slow
		// subscriber's writer is stalled with a full queue.
		publisher.publish(event)
		published[event.ID] = struct{}{}
		frame := fast.next()
		require.Equal(t, "EVENT", frame.label, "fast subscriber got %s %q at event %d", frame.label, frame.reason, i)
		require.Equal(t, "fast", frame.subID)
		require.Equal(t, event.ID, frame.event.ID, "fast subscriber got the wrong event at %d", i)
	}
	select {
	case <-slowStalled:
	default:
		t.Fatal("slow subscriber's writer never stalled; the test did not exercise overflow")
	}

	releaseOnce.Do(func() { close(release) })
	slowLive := 0
	for {
		frame := slow.next()
		require.Equal(t, "slow", frame.subID)
		if frame.label == "CLOSED" {
			require.True(t, strings.HasPrefix(frame.reason, "error:"), "CLOSED reason %q lacks machine-readable prefix", frame.reason)
			break
		}
		require.Equal(t, "EVENT", frame.label)
		slowLive++
	}
	require.LessOrEqual(t, slowLive, queueSize+1, "slow subscriber received more than its queue could hold")

	// Re-subscribing from the cursor recovers every stored event, and no EVENT
	// for the closed subscription arrives after its CLOSED.
	slow.send("REQ", "resync", filter)
	recovered := map[nostr.ID]struct{}{}
	for {
		frame := slow.next()
		require.NotEqual(t, "slow", frame.subID, "frame %s for subscription after CLOSED", frame.label)
		if frame.label == "EOSE" {
			break
		}
		require.Equal(t, "EVENT", frame.label)
		recovered[frame.event.ID] = struct{}{}
	}
	require.Equal(t, published, recovered)
}

func TestSidecarLiveBurstBeyondOldGlobalQueueReachesEverySubscriber(t *testing.T) {
	// The removed global queue held 256 events and dropped the rest, stored and
	// ephemeral alike. Publish a burst well past that without waiting between
	// publishes, and require every subscriber to receive all of it.
	const total = 400
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	server, relayURL := startSidecarForFanoutTest(t)

	sk := nostr.Generate()
	filter := nostr.Filter{Authors: []nostr.PubKey{sk.Public()}}
	subscribers := []*rawRelayClient{dialRawRelay(t, ctx, relayURL), dialRawRelay(t, ctx, relayURL)}
	for _, subscriber := range subscribers {
		subscriber.subscribe("burst", filter)
	}

	want := make(map[nostr.ID]struct{}, total)
	for i := range total {
		kind := nostr.Kind(1)
		if i%2 == 1 {
			kind = 20001 // ephemeral: never stored, so live delivery is its only path
		}
		event := signedFanoutEvent(t, sk, kind, i)
		_, err := server.Relay().AddEvent(ctx, event)
		require.NoError(t, err)
		want[event.ID] = struct{}{}
	}

	for n, subscriber := range subscribers {
		got := make(map[nostr.ID]struct{}, total)
		for len(got) < total {
			frame := subscriber.next()
			require.Equalf(t, "EVENT", frame.label, "subscriber %d after %d events: %s %q", n, len(got), frame.label, frame.reason)
			got[frame.event.ID] = struct{}{}
		}
		require.Equal(t, want, got)
	}
}
