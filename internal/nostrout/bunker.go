package nostrout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
)

// Bunker is Bahia's NIP-46 remote-signer client. It speaks the same wire
// protocol as fiatjaf.com/nostr/nip46 (NIP-44 encrypted kind 24133 request and
// response events, auth_url challenges, verified sign_event results), but it
// owns request publication so every frame goes through a shared admission
// permit: the relay connection is established first, the frame is admitted
// immediately before it is sent, and each relay's OK/rate-limit outcome is
// observed, so signer-side rate limiting opens the shared circuit breaker.
//
// Unlike the upstream client it never issues unsolicited switch_relays
// requests; relays come only from the bunker URI.
type Bunker struct {
	admission       *Admission
	pool            *nostr.Pool
	relays          []string
	clientSecretKey nostr.SecretKey
	target          nostr.PubKey
	conversationKey [32]byte
	idPrefix        string
	serial          atomic.Uint64
	onAuth          func(string)

	// send puts one admitted frame on the wire; tests replace it.
	send func(ctx context.Context, relay string, ev nostr.Event, admit func(context.Context) error) error
	// released, when set, runs after a request's permit is closed, i.e. after
	// every relay outcome was observed. Tests use it to synchronize.
	released func()

	mu        sync.Mutex
	listeners map[string]chan nip46.Response
	publicKey nostr.PubKey
}

// ConnectBunker connects to a NIP-46 signer named by a bunker:// URI or NIP-05
// identifier. The response subscription lives as long as ctx. As upstream, it
// returns the client together with any connect error.
func ConnectBunker(
	ctx context.Context,
	admission *Admission,
	clientSecretKey nostr.SecretKey,
	bunkerURLOrNIP05 string,
	pool *nostr.Pool,
	onAuth func(string),
) (*Bunker, error) {
	parsed, err := nip46.ParseBunkerInput(ctx, bunkerURLOrNIP05)
	if err != nil {
		return nil, fmt.Errorf("invalid bunker: %w", err)
	}
	if pool == nil {
		pool = nostr.NewPool()
	}
	bunker, err := newBunker(admission, clientSecretKey, parsed.HostPubKey, parsed.Relays, onAuth)
	if err != nil {
		return nil, err
	}
	bunker.pool = pool
	bunker.send = bunker.sendFrame

	events, eosed := pool.SubscribeManyNotifyEOSE(ctx, parsed.Relays, nostr.Filter{
		Tags:      nostr.TagMap{"p": []string{nostr.GetPublicKey(clientSecretKey).Hex()}},
		Kinds:     []nostr.Kind{nostr.KindNostrConnect},
		Since:     nostr.Now(),
		LimitZero: true,
	}, nostr.SubscriptionOptions{Label: "bahia-nip46-client"})
	go func() {
		for relayEvent := range events {
			bunker.handleResponseEvent(relayEvent.Event)
		}
	}()
	select {
	case <-eosed:
	case <-ctx.Done():
		return bunker, ctx.Err()
	}

	_, err = bunker.RPC(ctx, "connect", []string{parsed.HostPubKey.Hex(), parsed.Secret})
	return bunker, err
}

func newBunker(admission *Admission, clientSecretKey nostr.SecretKey, target nostr.PubKey, relays []string, onAuth func(string)) (*Bunker, error) {
	conversationKey, err := nip44.GenerateConversationKey(target, clientSecretKey)
	if err != nil {
		return nil, fmt.Errorf("derive NIP-46 conversation key: %w", err)
	}
	return &Bunker{
		admission:       Or(admission),
		relays:          append([]string(nil), relays...),
		clientSecretKey: clientSecretKey,
		target:          target,
		conversationKey: conversationKey,
		idPrefix:        "bahia-" + strconv.Itoa(rand.IntN(65536)),
		onAuth:          onAuth,
		listeners:       make(map[string]chan nip46.Response),
	}, nil
}

// handleResponseEvent routes one kind 24133 response to its waiting request.
func (b *Bunker) handleResponseEvent(ev nostr.Event) {
	if ev.Kind != nostr.KindNostrConnect {
		return
	}
	plaintext, err := nip44.Decrypt(ev.Content, b.conversationKey)
	if err != nil {
		return
	}
	var resp nip46.Response
	if err := json.Unmarshal([]byte(plaintext), &resp); err != nil {
		return
	}
	if resp.Result == "auth_url" {
		if b.onAuth != nil {
			b.onAuth(resp.Error)
		}
		return
	}
	b.mu.Lock()
	listener, ok := b.listeners[resp.ID]
	delete(b.listeners, resp.ID)
	b.mu.Unlock()
	if ok {
		listener <- resp
	}
}

// sendFrame is the only raw EVENT publication of the NIP-46 client: it
// connects first, then admits the frame immediately before sending it.
func (b *Bunker) sendFrame(ctx context.Context, relayURL string, ev nostr.Event, admit func(context.Context) error) error {
	relay, err := b.pool.EnsureRelay(relayURL)
	if err != nil {
		return fmt.Errorf("connect %s: %w", relayURL, err)
	}
	if err := admit(ctx); err != nil {
		return err
	}
	return relay.Publish(ctx, ev)
}

// Relays returns the bunker relays.
func (b *Bunker) Relays() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.relays...)
}

// RPC sends one admitted NIP-46 request and waits for its correlated response.
func (b *Bunker) RPC(ctx context.Context, method string, params []string) (string, error) {
	if b == nil || b.send == nil {
		return "", ErrNotConfigured
	}
	id := b.idPrefix + "-" + strconv.FormatUint(b.serial.Add(1), 10) + "-" + method
	request, err := json.Marshal(nip46.Request{ID: id, Method: method, Params: params})
	if err != nil {
		return "", fmt.Errorf("encode NIP-46 request: %w", err)
	}
	content, err := nip44.Encrypt(string(request), b.conversationKey)
	if err != nil {
		return "", fmt.Errorf("encrypt NIP-46 request: %w", err)
	}
	ev := nostr.Event{
		Kind:      nostr.KindNostrConnect,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", b.target.Hex()}},
		Content:   content,
	}
	if err := ev.Sign(b.clientSecretKey); err != nil {
		return "", fmt.Errorf("sign NIP-46 request: %w", err)
	}

	// Register before sending: a fast signer can answer before the relay OK.
	listener := make(chan nip46.Response, 1)
	b.mu.Lock()
	b.listeners[id] = listener
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.listeners, id)
		b.mu.Unlock()
	}()

	if err := b.publish(ctx, ev); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("await NIP-46 %s response: %w", method, ctx.Err())
	case resp := <-listener:
		if resp.Error != "" {
			return "", fmt.Errorf("response error: %s", resp.Error)
		}
		return resp.Result, nil
	}
}

// publish admits the request once for all bunker relays, sends to each relay
// concurrently, and returns as soon as one relay accepts it. Every relay
// outcome — including ones that arrive after the first acceptance — is
// observed before the permit is released.
func (b *Bunker) publish(ctx context.Context, ev nostr.Event) error {
	pub, err := b.admission.BeginWaiting(ctx, ev, b.relays)
	if err != nil {
		return fmt.Errorf("admit NIP-46 request: %w", err)
	}
	pending := pub.PendingRelays()
	if len(pending) == 0 {
		pub.Close()
		return nil
	}
	outcomes := make(chan Result, len(pending))
	var workers sync.WaitGroup
	for _, relay := range pending {
		workers.Add(1)
		go func() {
			defer workers.Done()
			admit := func(ctx context.Context) error { return pub.BeforeAttempt(ctx, relay) }
			result := ResultFromPublishError(relay, b.send(ctx, relay, ev, admit))
			pub.Observe(result)
			outcomes <- result
		}()
	}
	go func() {
		workers.Wait()
		pub.Close()
		if b.released != nil {
			b.released()
		}
	}()

	var failures []error
	for range pending {
		result := <-outcomes
		if result.Succeeded() {
			return nil
		}
		switch {
		case result.Error != nil:
			failures = append(failures, fmt.Errorf("%s: %w", result.RelayURL, result.Error))
		default:
			failures = append(failures, fmt.Errorf("%s: %s", result.RelayURL, result.Reason))
		}
	}
	return fmt.Errorf("no bunker relay accepted the NIP-46 request: %w", errors.Join(failures...))
}

// Ping sends one admitted NIP-46 ping.
func (b *Bunker) Ping(ctx context.Context) error {
	_, err := b.RPC(ctx, "ping", []string{})
	return err
}

// GetPublicKey returns the signer public key, sending a request only when it is
// not already known.
func (b *Bunker) GetPublicKey(ctx context.Context) (nostr.PubKey, error) {
	if b == nil {
		return nostr.ZeroPK, ErrNotConfigured
	}
	b.mu.Lock()
	cached := b.publicKey
	b.mu.Unlock()
	if cached != nostr.ZeroPK {
		return cached, nil
	}
	resp, err := b.RPC(ctx, "get_public_key", []string{})
	if err != nil {
		return nostr.ZeroPK, err
	}
	pk, err := nostr.PubKeyFromHex(resp)
	if err != nil {
		return nostr.ZeroPK, err
	}
	b.mu.Lock()
	b.publicKey = pk
	b.mu.Unlock()
	return pk, nil
}

// SignEvent sends one sign_event request and verifies the returned event.
func (b *Bunker) SignEvent(ctx context.Context, evt *nostr.Event) error {
	resp, err := b.RPC(ctx, "sign_event", []string{evt.String()})
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(resp), evt); err != nil {
		return err
	}
	if !evt.CheckID() {
		return fmt.Errorf("sign_event response from bunker has invalid id")
	}
	if !evt.VerifySignature() {
		return fmt.Errorf("sign_event response from bunker has invalid signature")
	}
	return nil
}

// NIP44Encrypt sends one nip44_encrypt request.
func (b *Bunker) NIP44Encrypt(ctx context.Context, targetPublicKey nostr.PubKey, plaintext string) (string, error) {
	return b.RPC(ctx, "nip44_encrypt", []string{targetPublicKey.Hex(), plaintext})
}

// NIP44Decrypt sends one nip44_decrypt request.
func (b *Bunker) NIP44Decrypt(ctx context.Context, targetPublicKey nostr.PubKey, ciphertext string) (string, error) {
	return b.RPC(ctx, "nip44_decrypt", []string{targetPublicKey.Hex(), ciphertext})
}
