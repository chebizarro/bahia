package nostrout

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
)

// bunkerRelay is a loopback relay with a minimal NIP-46 bunker behind it. It
// reports every websocket connect and disconnect so tests can prove which
// connections a bunker session releases.
type bunkerRelay struct {
	url          string
	uri          string
	connected    chan struct{}
	disconnected chan struct{}
}

func newBunkerRelay(t *testing.T, refuseConnect bool) *bunkerRelay {
	t.Helper()
	bunkerSecret := nostr.Generate()
	relay := khatru.NewRelay()
	r := &bunkerRelay{connected: make(chan struct{}, 16), disconnected: make(chan struct{}, 16)}
	relay.OnConnect = func(context.Context) { r.connected <- struct{}{} }
	relay.OnDisconnect = func(context.Context) { r.disconnected <- struct{}{} }
	relay.OnEphemeralEvent = func(_ context.Context, evt nostr.Event) {
		if evt.Kind != nostr.KindNostrConnect || evt.Tags.Find("p")[1] != bunkerSecret.Public().Hex() {
			return
		}
		key, err := nip44.GenerateConversationKey(evt.PubKey, bunkerSecret)
		if err != nil {
			return
		}
		plaintext, err := nip44.Decrypt(evt.Content, key)
		if err != nil {
			return
		}
		var req nip46.Request
		if json.Unmarshal([]byte(plaintext), &req) != nil {
			return
		}
		resp := nip46.Response{ID: req.ID, Result: "ack"}
		if refuseConnect {
			resp = nip46.Response{ID: req.ID, Error: "client not authorized"}
		}
		body, _ := json.Marshal(resp)
		content, _ := nip44.Encrypt(string(body), key)
		reply := nostr.Event{Kind: nostr.KindNostrConnect, CreatedAt: nostr.Now(), Content: content, Tags: nostr.Tags{{"p", evt.PubKey.Hex()}}}
		if reply.Sign(bunkerSecret) == nil {
			relay.BroadcastEvent(reply)
		}
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	r.url = "ws" + strings.TrimPrefix(server.URL, "http")
	r.uri = "bunker://" + bunkerSecret.Public().Hex() + "?relay=" + r.url
	return r
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestConnectBunkerOwnedPoolClosesWithSession(t *testing.T) {
	relay := newBunkerRelay(t, false)
	session, endSession := context.WithCancel(context.Background())
	defer endSession()

	if _, err := ConnectBunker(session, nil, nostr.Generate(), relay.uri, nil, nil); err != nil {
		t.Fatalf("ConnectBunker: %v", err)
	}
	waitSignal(t, relay.connected, "bunker relay connection")
	select {
	case <-relay.disconnected:
		t.Fatal("live session dropped its bunker relay connection")
	default:
	}

	endSession()
	waitSignal(t, relay.disconnected, "bunker relay connection to close with the session")
}

func TestConnectBunkerOwnedPoolClosesOnConnectFailure(t *testing.T) {
	relay := newBunkerRelay(t, true)
	session, endSession := context.WithCancel(context.Background())
	defer endSession()

	if _, err := ConnectBunker(session, nil, nostr.Generate(), relay.uri, nil, nil); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("ConnectBunker error = %v, want the bunker refusal", err)
	}
	waitSignal(t, relay.connected, "bunker relay connection")
	// The session context is still live: only the failed connect closes the pool.
	waitSignal(t, relay.disconnected, "bunker relay connection to close after the refused connect")
}
