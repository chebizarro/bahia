package nostrout

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	// Secret, when set, makes the bunker follow NIP-46 strictly: the secret
	// pairs the first client that sends it and is then spent; a connect
	// that reuses it is ignored (no answer); a paired client may reconnect
	// without a secret; anything else is refused.
	Secret string
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
	requests int
	open     int
	// connectParams holds every connect request's params, in order.
	connectParams [][]string
	paired        map[nostr.PubKey]bool
	spent         bool
}

// newTestBunker starts a bunker signing as service. It stops at test cleanup.
func newTestBunker(t testing.TB, service nostr.SecretKey, opts testBunkerOptions) *testBunker {
	t.Helper()
	signer := nip46.NewStaticKeySigner(service)
	var signerMu sync.Mutex
	relay := khatru.NewRelay()
	b := &testBunker{Connected: make(chan struct{}, 64), Disconnected: make(chan struct{}, 64), paired: map[nostr.PubKey]bool{}}
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
		b.mu.Lock()
		b.requests++
		b.mu.Unlock()
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
		b.connectParams = append(b.connectParams, req.Params)
		b.mu.Unlock()
		if opts.Secret != "" {
			b.mu.Lock()
			defer b.mu.Unlock()
			switch {
			case len(req.Params) > 1 && req.Params[1] == opts.Secret && !b.spent:
				b.spent = true
				b.paired[evt.PubKey] = true
				reply(nip46.Response{ID: req.ID, Result: "ack"})
			case len(req.Params) > 1 && req.Params[1] == opts.Secret:
				// NIP-46: ignore new attempts with an old secret.
			case len(req.Params) == 1 && b.paired[evt.PubKey]:
				reply(nip46.Response{ID: req.ID, Result: "ack"})
			default:
				reply(nip46.Response{ID: req.ID, Error: "client not paired"})
			}
			return
		}
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
	if opts.Secret != "" {
		b.uri += "&secret=" + opts.Secret
	}
	return b
}

// ConnectParams returns the params of every connect request received.
func (b *testBunker) ConnectParams() [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]string(nil), b.connectParams...)
}

// Unpair forgets client's pairing, as a bunker operator revoking it would.
func (b *testBunker) Unpair(client nostr.PubKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.paired, client)
}

// Connects is the number of connect requests received.
func (b *testBunker) Connects() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connects
}

// Requests is the number of NIP-46 requests received, of any method.
func (b *testBunker) Requests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requests
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

// No NIP-46 request leaves without a permit: under an active kill switch the
// bunker receives nothing at all, including the switch_relays request the
// upstream client used to send on its own (see third_party/nostr/BAHIA_PATCHES.md).
func TestConnectBunkerSendsNothingWithoutAdmission(t *testing.T) {
	stop := filepath.Join(t.TempDir(), "stop")
	if err := os.WriteFile(stop, []byte("stop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bunker := newTestBunker(t, nostr.Generate(), testBunkerOptions{})
	session, endSession := context.WithCancel(context.Background())
	defer endSession()

	_, err := ConnectBunker(session, New(Config{KillSwitchFile: stop}), nostr.Generate(), bunker.uri, nil, nil)
	if !errors.Is(err, ErrKillSwitch) {
		t.Fatalf("ConnectBunker error = %v, want ErrKillSwitch", err)
	}
	if n := bunker.Requests(); n != 0 {
		t.Fatalf("bunker received %d requests under the kill switch", n)
	}
}

// A reload that reopens the signer reconnects the same client key. NIP-46
// connect secrets are single-use and a strict bunker ignores a reused one, so
// the second connect in a process omits it; a different client key still
// sends it.
func TestConnectBunkerReconnectOmitsTheSecretItAlreadyUsed(t *testing.T) {
	unbounded := generousAdmission()
	bunker := newTestBunker(t, nostr.Generate(), testBunkerOptions{Secret: "pairing-secret"})
	client := nostr.Generate()
	host := strings.TrimPrefix(strings.SplitN(bunker.uri, "?", 2)[0], "bunker://")
	for range 2 {
		session, endSession := context.WithCancel(context.Background())
		if _, err := ConnectBunker(session, unbounded, client, bunker.uri, nil, nil); err != nil {
			t.Fatalf("ConnectBunker: %v", err)
		}
		endSession()
	}
	got := bunker.ConnectParams()
	want := [][]string{{host, "pairing-secret"}, {host}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("connect params = %q, want %q", got, want)
	}

	// The record is per client key: another client still offers the secret,
	// which this bunker, having spent it, ignores until the deadline.
	other, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := ConnectBunker(other, unbounded, nostr.Generate(), bunker.uri, nil, nil); err == nil {
		t.Fatal("a second client paired with a spent secret")
	}
	if got := bunker.ConnectParams(); len(got) != 3 || len(got[2]) != 2 {
		t.Fatalf("connect params = %q, want the other client to send the secret", got)
	}
}

// Refusals of connect name what the operator has to do about the pairing.
func TestConnectBunkerExplainsRefusedPairings(t *testing.T) {
	unbounded := generousAdmission()
	strict := newTestBunker(t, nostr.Generate(), testBunkerOptions{Secret: "pairing-secret"})
	client := nostr.Generate()
	session, endSession := context.WithCancel(context.Background())
	defer endSession()
	if _, err := ConnectBunker(session, unbounded, client, strict.uri, nil, nil); err != nil {
		t.Fatalf("pairing connect: %v", err)
	}
	strict.Unpair(client.Public())
	_, err := ConnectBunker(session, unbounded, client, strict.uri, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "client not paired") || !strings.Contains(err.Error(), "no longer paired: pair it again with a fresh connect secret") {
		t.Fatalf("reconnect after unpairing: %v", err)
	}

	unpaired := strings.SplitN(strict.uri, "&secret=", 2)[0]
	_, err = ConnectBunker(session, unbounded, nostr.Generate(), unpaired, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not paired: put a fresh connect secret in the bunker URI") {
		t.Fatalf("connect without a secret: %v", err)
	}

	refusing := newTestBunker(t, nostr.Generate(), testBunkerOptions{RefuseConnect: true})
	_, err = ConnectBunker(session, unbounded, nostr.Generate(), refusing.uri+"&secret=other", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "client not authorized") || !strings.Contains(err.Error(), "single-use") {
		t.Fatalf("connect with a refused secret: %v", err)
	}
}

// generousAdmission leaves the signer lane wide open, so tests that connect
// repeatedly do not wait on the shared process default's small burst.
func generousAdmission() *Admission {
	wide := PurposeBudget{RatePerMinute: 60_000, Burst: 1_000}
	return New(Config{Aggregate: wide, PurposeBudgets: map[Purpose]PurposeBudget{PurposeSigner: wide}, RelayWire: wide, RelayWirePriority: wide})
}
