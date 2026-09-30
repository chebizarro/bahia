//go:build !js

package nostr

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

func TestPublish(t *testing.T) {
	// test note to be sent over websocket
	priv, pub := makeKeyPair(t)
	textNote := Event{
		Kind:      KindTextNote,
		Content:   "hello",
		CreatedAt: Timestamp(1672068534), // random fixed timestamp
		Tags:      Tags{[]string{"foo", "bar"}},
		PubKey:    pub,
	}
	err := textNote.Sign(priv)
	require.NoError(t, err)

	// fake relay server
	var mu sync.Mutex // guards published to satisfy go test -race
	var published bool
	ws := newWebsocketServer(func(conn *websocket.Conn) {
		mu.Lock()
		published = true
		mu.Unlock()
		// verify the client sent exactly the textNote
		var raw []stdjson.RawMessage
		err := websocket.JSON.Receive(conn, &raw)
		require.NoError(t, err)

		event := parseEventMessage(t, raw)
		require.True(t, bytes.Equal(event.Serialize(), textNote.Serialize()))

		// send back an ok nip-20 command result
		res := []any{"OK", textNote.ID.Hex(), true, ""}
		err = websocket.JSON.Send(conn, res)
		require.NoError(t, err)
	})
	defer ws.Close()

	// connect a client and send the text note
	rl := mustRelayConnect(t, ws.URL)
	err = rl.Publish(t.Context(), textNote)
	require.NoError(t, err)

	require.True(t, published, "fake relay server saw no event")
}

func TestPublishBlocked(t *testing.T) {
	// test note to be sent over websocket
	textNote := Event{Kind: KindTextNote, Content: "hello"}
	textNote.ID = textNote.GetID()

	// fake relay server
	ws := newWebsocketServer(func(conn *websocket.Conn) {
		// discard received message; not interested
		var raw []stdjson.RawMessage
		err := websocket.JSON.Receive(conn, &raw)
		require.NoError(t, err)

		// send back a not ok nip-20 command result
		res := []any{"OK", textNote.ID.Hex(), false, "blocked"}
		websocket.JSON.Send(conn, res)
	})
	defer ws.Close()

	// connect a client and send a text note
	rl := mustRelayConnect(t, ws.URL)
	err := rl.Publish(t.Context(), textNote)
	require.Error(t, err)
	require.EqualError(t, err, "msg: blocked")
}

func TestPublishWriteFailed(t *testing.T) {
	// test note to be sent over websocket
	textNote := Event{Kind: KindTextNote, Content: "hello"}
	textNote.ID = textNote.GetID()

	// fake relay server
	ws := newWebsocketServer(func(conn *websocket.Conn) {
		// reject receive - force send error
		conn.Close()
	})
	defer ws.Close()

	// connect a client and send a text note
	rl := mustRelayConnect(t, ws.URL)
	// Force brief period of time so that publish always fails on closed socket.
	time.Sleep(1 * time.Millisecond)
	err := rl.Publish(t.Context(), textNote)
	require.Error(t, err)
}

func TestConnectContext(t *testing.T) {
	// fake relay server
	var mu sync.Mutex // guards connected to satisfy go test -race
	var connected bool
	ws := newWebsocketServer(func(conn *websocket.Conn) {
		mu.Lock()
		connected = true
		mu.Unlock()
		io.ReadAll(conn) // discard all input
	})
	defer ws.Close()

	// relay client
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	r, err := RelayConnect(ctx, ws.URL, RelayOptions{})
	require.NoError(t, err)

	defer r.Close()

	mu.Lock()
	defer mu.Unlock()
	require.True(t, connected, "fake relay server saw no client connect")
}

func TestConnectContextCanceled(t *testing.T) {
	// fake relay server
	ws := newWebsocketServer(func(conn *websocket.Conn) {
		io.ReadAll(conn) // discard all input
	})
	defer ws.Close()

	// relay client
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // make ctx expired
	_, err := RelayConnect(ctx, ws.URL, RelayOptions{})
	require.ErrorIs(t, err, context.Canceled)
}

func newWebsocketServer(handler func(*websocket.Conn)) *httptest.Server {
	return httptest.NewServer(&websocket.Server{
		Handshake: anyOriginHandshake,
		Handler:   handler,
	})
}

// anyOriginHandshake is an alternative to default in golang.org/x/net/websocket
// which checks for origin. nostr client sends no origin and it makes no difference
// for the tests here anyway.
var anyOriginHandshake = func(conf *websocket.Config, r *http.Request) error {
	return nil
}

func makeKeyPair(t *testing.T) (priv, pub [32]byte) {
	t.Helper()

	privkey := Generate()
	pubkey := GetPublicKey(privkey)

	return privkey, pubkey
}

func mustRelayConnect(t *testing.T, url string) *Relay {
	t.Helper()

	rl, err := RelayConnect(t.Context(), url, RelayOptions{})
	require.NoError(t, err)

	return rl
}

func parseEventMessage(t *testing.T, raw []stdjson.RawMessage) Event {
	t.Helper()

	require.Condition(t, func() (success bool) {
		return len(raw) >= 2
	})

	var typ string
	err := json.Unmarshal(raw[0], &typ)
	require.NoError(t, err)
	require.Equal(t, "EVENT", typ)

	var event Event
	err = json.Unmarshal(raw[1], &event)
	require.NoError(t, err)

	return event
}

func parseSubscriptionMessage(t *testing.T, raw []stdjson.RawMessage) (subid string, filters []Filter) {
	t.Helper()

	require.Greater(t, len(raw), 3)

	var typ string
	err := json.Unmarshal(raw[0], &typ)

	require.NoError(t, err)
	require.Equal(t, "REQ", typ)

	var id string
	err = json.Unmarshal(raw[1], &id)
	require.NoError(t, err)

	var ff []Filter
	for _, b := range raw[2:] {
		var f Filter
		err := json.Unmarshal(b, &f)
		require.NoError(t, err)
		ff = append(ff, f)
	}
	return id, ff
}
