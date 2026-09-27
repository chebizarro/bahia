package nostrout

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
	"github.com/stretchr/testify/require"
)

// fakeSigner answers NIP-46 requests the way a remote signer would, from the
// signer side of the NIP-44 conversation.
type fakeSigner struct {
	t        *testing.T
	sk       nostr.SecretKey
	client   nostr.PubKey
	bunker   *Bunker
	mu       sync.Mutex
	frames   map[string]int
	outcome  func(relay string) error
	tamper   bool
	authOnce bool
}

func newFakeSignerBunker(t *testing.T, admission *Admission, relays ...string) *fakeSigner {
	t.Helper()
	signer := &fakeSigner{t: t, sk: nostr.Generate(), frames: map[string]int{}}
	clientSK := nostr.Generate()
	signer.client = nostr.GetPublicKey(clientSK)
	bunker, err := newBunker(admission, clientSK, nostr.GetPublicKey(signer.sk), relays, nil)
	require.NoError(t, err)
	bunker.send = signer.send
	signer.bunker = bunker
	return signer
}

func (s *fakeSigner) frameCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, n := range s.frames {
		total += n
	}
	return total
}

func (s *fakeSigner) send(ctx context.Context, relay string, ev nostr.Event, admit func(context.Context) error) error {
	if err := admit(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.frames[relay]++
	s.mu.Unlock()
	if s.outcome != nil {
		if err := s.outcome(relay); err != nil {
			return err
		}
	}
	s.respond(ev)
	return nil
}

func (s *fakeSigner) respond(request nostr.Event) {
	key, err := nip44.GenerateConversationKey(s.client, s.sk)
	require.NoError(s.t, err)
	plaintext, err := nip44.Decrypt(request.Content, key)
	require.NoError(s.t, err)
	var req nip46.Request
	require.NoError(s.t, json.Unmarshal([]byte(plaintext), &req))

	resp := nip46.Response{ID: req.ID}
	switch req.Method {
	case "connect", "ping":
		resp.Result = "ack"
	case "get_public_key":
		resp.Result = nostr.GetPublicKey(s.sk).Hex()
	case "sign_event":
		var ev nostr.Event
		require.NoError(s.t, json.Unmarshal([]byte(req.Params[0]), &ev))
		require.NoError(s.t, ev.Sign(s.sk))
		if s.tamper {
			ev.Content += "tampered"
		}
		resp.Result = ev.String()
	default:
		resp.Error = "unsupported"
	}
	if s.authOnce {
		s.authOnce = false
		s.deliver(key, nip46.Response{ID: req.ID, Result: "auth_url", Error: "https://signer.example/approve"})
	}
	s.deliver(key, resp)
}

func (s *fakeSigner) deliver(key [32]byte, resp nip46.Response) {
	raw, err := json.Marshal(resp)
	require.NoError(s.t, err)
	content, err := nip44.Encrypt(string(raw), key)
	require.NoError(s.t, err)
	ev := nostr.Event{Kind: nostr.KindNostrConnect, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", s.client.Hex()}}, Content: content}
	require.NoError(s.t, ev.Sign(s.sk))
	s.bunker.handleResponseEvent(ev)
}

func TestBunkerRoundTripIsAdmittedAndCachesPublicKey(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	signer := newFakeSignerBunker(t, a, relayA)
	pk, err := signer.bunker.GetPublicKey(t.Context())
	require.NoError(t, err)
	require.Equal(t, nostr.GetPublicKey(signer.sk), pk)
	_, err = signer.bunker.GetPublicKey(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, signer.frameCount(), "a known public key needs no request")
	require.NoError(t, signer.bunker.Ping(t.Context()))
	metrics := a.Metrics()
	require.Equal(t, uint64(2), metrics.Admitted)
	require.Equal(t, uint64(2), metrics.WireAttempts)
}

func TestBunkerSignEventVerifiesTheSignerResult(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	signer := newFakeSignerBunker(t, a, relayA)
	ev := &nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "hello"}
	require.NoError(t, signer.bunker.SignEvent(t.Context(), ev))
	require.Equal(t, nostr.GetPublicKey(signer.sk), ev.PubKey)
	require.True(t, ev.VerifySignature())

	signer.tamper = true
	err := signer.bunker.SignEvent(t.Context(), &nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "again"})
	require.ErrorContains(t, err, "invalid id")
}

func TestBunkerRelayRateLimitOpensTheSharedBreaker(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	signer := newFakeSignerBunker(t, a, relayA, relayB)
	// B's frame goes out before A's rate limit is returned: otherwise the
	// breaker A opens would (correctly) make B wait on the frozen fake clock.
	bSent := make(chan struct{})
	signer.outcome = func(relay string) error {
		if relay == relayA {
			<-bSent
			return errors.New("msg: rate-limited: too many signer requests")
		}
		close(bSent)
		return nil
	}
	released := make(chan struct{})
	signer.bunker.released = func() { close(released) }
	require.NoError(t, signer.bunker.Ping(t.Context()), "one accepting relay completes the request")
	<-released
	require.True(t, a.State().CircuitOpen,
		"a signer relay's rate limit must reach the shared breaker even after the request completed")
	require.Equal(t, uint64(1), a.Metrics().RelayRateLimited)
	_, err := a.Begin(t.Context(), nostr.Event{Kind: 1}, []string{relayB})
	require.ErrorIs(t, err, ErrCircuitOpen, "every gateway sees the signer-triggered circuit")
}

func TestBunkerSignerOnlyRateLimitFailsTheRequest(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	signer := newFakeSignerBunker(t, a, relayA)
	signer.outcome = func(string) error { return errors.New("msg: rate-limited: slow down") }
	err := signer.bunker.Ping(t.Context())
	require.ErrorContains(t, err, "no bunker relay accepted")
	require.True(t, a.State().CircuitOpen)
}

func TestBunkerKillSwitchSendsNoFrame(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	path := filepath.Join(t.TempDir(), "stop")
	require.NoError(t, os.WriteFile(path, []byte("stop"), 0o600))
	a.killSwitchFile = path
	signer := newFakeSignerBunker(t, a, relayA)
	err := signer.bunker.Ping(t.Context())
	require.ErrorIs(t, err, ErrKillSwitch)
	require.Zero(t, signer.frameCount())
}

func TestBunkerAuthURLIsReportedAndRequestStillCompletes(t *testing.T) {
	a, _ := newTestAdmission(testConfig())
	signer := newFakeSignerBunker(t, a, relayA)
	var authURL string
	signer.bunker.onAuth = func(url string) { authURL = url }
	signer.authOnce = true
	require.NoError(t, signer.bunker.Ping(t.Context()))
	require.Equal(t, "https://signer.example/approve", authURL)
}
