package nostrout

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
)

// testBunkerOptions shapes the bunker's answer to connect.
type testBunkerOptions struct {
	// RefuseConnect answers every connect with an error.
	RefuseConnect bool
	// AuthURL, when set, is sent as an auth_url challenge before each connect
	// answer.
	AuthURL string
}

// testBunker is a loopback NIP-46 bunker that signs as one service key,
// counts connect requests and reports every websocket connect and disconnect,
// so tests can prove which relay connections a bunker session releases.
type testBunker struct {
	// uri is the bunker:// pairing URI.
	uri string
	// Connected and Disconnected receive one value per websocket connect and
	// disconnect.
	Connected    chan struct{}
	Disconnected chan struct{}

	mu       sync.Mutex
	connects int
	open     int
}

// newTestBunker starts a bunker signing as service. It stops at test cleanup.
func newTestBunker(t testing.TB, service nostr.SecretKey, opts testBunkerOptions) *testBunker {
	t.Helper()
	signer := nip46.NewStaticKeySigner(service)
	var signerMu sync.Mutex
	relay := khatru.NewRelay()
	b := &testBunker{Connected: make(chan struct{}, 64), Disconnected: make(chan struct{}, 64)}
	relay.OnConnect = func(context.Context) {
		b.mu.Lock()
		b.open++
		b.mu.Unlock()
		b.Connected <- struct{}{}
	}
	relay.OnDisconnect = func(context.Context) {
		b.mu.Lock()
		b.open--
		b.mu.Unlock()
		b.Disconnected <- struct{}{}
	}
	relay.OnEphemeralEvent = func(ctx context.Context, evt nostr.Event) {
		if evt.Kind != nostr.KindNostrConnect || evt.Tags.Find("p")[1] != service.Public().Hex() {
			return
		}
		key, err := nip44.GenerateConversationKey(evt.PubKey, service)
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
		reply := func(resp nip46.Response) {
			body, _ := json.Marshal(resp)
			content, _ := nip44.Encrypt(string(body), key)
			out := nostr.Event{Kind: nostr.KindNostrConnect, CreatedAt: nostr.Now(), Content: content, Tags: nostr.Tags{{"p", evt.PubKey.Hex()}}}
			if out.Sign(service) == nil {
				relay.BroadcastEvent(out)
			}
		}
		if req.Method != "connect" {
			signerMu.Lock()
			_, _, out, err := signer.HandleRequest(ctx, evt)
			signerMu.Unlock()
			if err == nil {
				relay.BroadcastEvent(out)
			}
			return
		}
		b.mu.Lock()
		b.connects++
		b.mu.Unlock()
		if opts.AuthURL != "" {
			reply(nip46.Response{ID: req.ID, Result: "auth_url", Error: opts.AuthURL})
		}
		if opts.RefuseConnect {
			reply(nip46.Response{ID: req.ID, Error: "client not authorized"})
			return
		}
		reply(nip46.Response{ID: req.ID, Result: "ack"})
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	b.uri = "bunker://" + service.Public().Hex() + "?relay=ws" + strings.TrimPrefix(server.URL, "http")
	return b
}

// Connects is the number of connect requests received.
func (b *testBunker) Connects() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connects
}

// OpenConnections is the number of websockets currently connected.
func (b *testBunker) OpenConnections() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

// waitSignal fails the test unless ch delivers within five seconds.
func waitSignal(t testing.TB, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestConnectBunkerOwnedPoolClosesWithSession(t *testing.T) {
	bunker := newTestBunker(t, nostr.Generate(), testBunkerOptions{})
	session, endSession := context.WithCancel(context.Background())
	defer endSession()

	if _, err := ConnectBunker(session, nil, nostr.Generate(), bunker.uri, nil, nil); err != nil {
		t.Fatalf("ConnectBunker: %v", err)
	}
	waitSignal(t, bunker.Connected, "bunker relay connection")
	if bunker.Connects() != 1 || bunker.OpenConnections() != 1 {
		t.Fatalf("connects = %d, open = %d; want one session on one connection", bunker.Connects(), bunker.OpenConnections())
	}

	endSession()
	waitSignal(t, bunker.Disconnected, "bunker relay connection to close with the session")
	if open := bunker.OpenConnections(); open != 0 {
		t.Fatalf("open connections after session end = %d", open)
	}
}

func TestConnectBunkerOwnedPoolClosesOnConnectFailure(t *testing.T) {
	bunker := newTestBunker(t, nostr.Generate(), testBunkerOptions{RefuseConnect: true})
	session, endSession := context.WithCancel(context.Background())
	defer endSession()

	if _, err := ConnectBunker(session, nil, nostr.Generate(), bunker.uri, nil, nil); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("ConnectBunker error = %v, want the bunker refusal", err)
	}
	waitSignal(t, bunker.Connected, "bunker relay connection")
	// The session context is still live: only the failed connect closes the pool.
	waitSignal(t, bunker.Disconnected, "bunker relay connection to close after the refused connect")
}

func TestConnectBunkerReportsAuthURL(t *testing.T) {
	const authURL = "https://bunker.example/authorize/abc"
	bunker := newTestBunker(t, nostr.Generate(), testBunkerOptions{AuthURL: authURL})
	session, endSession := context.WithCancel(context.Background())
	defer endSession()

	var got []string
	if _, err := ConnectBunker(session, nil, nostr.Generate(), bunker.uri, nil, func(url string) { got = append(got, url) }); err != nil {
		t.Fatalf("ConnectBunker: %v", err)
	}
	// The challenge precedes the ack on the same subscription, so it has
	// been delivered by the time connect returns.
	if len(got) != 1 || got[0] != authURL {
		t.Fatalf("auth URLs = %q, want [%q]", got, authURL)
	}
}
