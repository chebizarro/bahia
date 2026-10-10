package nostrout

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip46"
)

// Bunker is a NIP-46 remote-signer client whose request events are admitted by
// the shared controller. The upstream client builds and publishes each kind
// 24133 request internally and exposes no publish hook, so every RPC is
// admitted as one opaque signer-lane publication to all bunker relays before
// the library is allowed to send it. Response subscriptions (REQ) are not
// EVENT publications and are not budgeted.
type Bunker struct {
	client    *nip46.BunkerClient
	admission *Admission

	mu        sync.Mutex
	publicKey nostr.PubKey
}

// spentSecrets records, per client key and bunker, the connect secrets a
// bunker acknowledged in this process. NIP-46 connect secrets are single-use
// and bunkers should ignore a reused one, so a later connect from the same
// client (a reload that reopens the signer) omits it and relies on the
// pairing the first connect established. Entries are hashes; no secret is
// retained.
var spentSecrets sync.Map

func spentSecretKey(client, bunker nostr.PubKey, secret string) [sha256.Size]byte {
	return sha256.Sum256([]byte(client.Hex() + bunker.Hex() + secret))
}

// ConnectBunker is the admission-gated equivalent of nip46.ConnectBunker. As
// upstream, it returns the client together with any connect error, and ctx is
// the session lifetime. With a nil pool the bunker owns its relay pool: it is
// closed when ctx ends, or at once when the connect fails, so ending the
// session releases the bunker relay connections. The URI's connect secret is
// sent unless this process already paired the same client with it.
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
	closeOwned := func() {}
	if pool == nil {
		owned := nostr.NewPool()
		stop := context.AfterFunc(ctx, func() { owned.Close("NIP-46 session ended") })
		closeOwned = func() {
			stop()
			owned.Close("NIP-46 connect failed")
		}
		pool = owned
	}
	bunker := &Bunker{
		client:    nip46.NewBunker(ctx, clientSecretKey, parsed.HostPubKey, parsed.Relays, pool, onAuth),
		admission: Or(admission),
	}
	params := []string{parsed.HostPubKey.Hex()}
	spent := spentSecretKey(clientSecretKey.Public(), parsed.HostPubKey, parsed.Secret)
	if _, used := spentSecrets.Load(spent); parsed.Secret != "" && !used {
		params = append(params, parsed.Secret)
	}
	_, err = bunker.RPC(ctx, "connect", params)
	switch {
	case err == nil && len(params) == 2:
		spentSecrets.Store(spent, struct{}{})
	case err != nil:
		closeOwned()
		err = connectRefusal(err, len(params) == 2, parsed.Secret != "")
	}
	return bunker, err
}

// connectRefusal tells the operator what a bunker's refusal of connect
// means for the pairing. Other failures (admission, transport) are returned
// unchanged.
func connectRefusal(err error, sentSecret, uriHasSecret bool) error {
	if !strings.HasPrefix(err.Error(), "response error: ") {
		return err
	}
	switch {
	case sentSecret:
		return fmt.Errorf("bunker refused connect with the bunker URI's connect secret: %w (a connect secret is single-use: "+
			"if another client used it, pair this client key with a fresh one; if this client key used it and is still paired, "+
			"remove the secret from the URI)", err)
	case uriHasSecret:
		return fmt.Errorf("bunker refused a reconnect without the connect secret this process already used: %w "+
			"(this client key is no longer paired: pair it again with a fresh connect secret)", err)
	default:
		return fmt.Errorf("bunker refused connect without a connect secret: %w "+
			"(this client key is not paired: put a fresh connect secret in the bunker URI)", err)
	}
}

func (b *Bunker) admit(ctx context.Context) error {
	if b == nil || b.client == nil {
		return ErrNotConfigured
	}
	if err := b.admission.AdmitOpaque(ctx, PurposeSigner, b.client.Relays); err != nil {
		return fmt.Errorf("admit NIP-46 request: %w", err)
	}
	return nil
}

// Relays returns the bunker relays.
func (b *Bunker) Relays() []string {
	if b == nil || b.client == nil {
		return nil
	}
	return append([]string(nil), b.client.Relays...)
}

// RPC sends one admitted NIP-46 request.
func (b *Bunker) RPC(ctx context.Context, method string, params []string) (string, error) {
	if err := b.admit(ctx); err != nil {
		return "", err
	}
	return b.client.RPC(ctx, method, params)
}

// Ping sends one admitted NIP-46 ping.
func (b *Bunker) Ping(ctx context.Context) error {
	if err := b.admit(ctx); err != nil {
		return err
	}
	return b.client.Ping(ctx)
}

// GetPublicKey returns the signer public key, admitting a request only when it
// is not already known.
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
	if err := b.admit(ctx); err != nil {
		return nostr.ZeroPK, err
	}
	pk, err := b.client.GetPublicKey(ctx)
	if err != nil {
		return nostr.ZeroPK, err
	}
	b.mu.Lock()
	b.publicKey = pk
	b.mu.Unlock()
	return pk, nil
}

// SignEvent admits and sends one sign_event request.
func (b *Bunker) SignEvent(ctx context.Context, evt *nostr.Event) error {
	if err := b.admit(ctx); err != nil {
		return err
	}
	return b.client.SignEvent(ctx, evt)
}

// NIP44Encrypt admits and sends one nip44_encrypt request.
func (b *Bunker) NIP44Encrypt(ctx context.Context, targetPublicKey nostr.PubKey, plaintext string) (string, error) {
	if err := b.admit(ctx); err != nil {
		return "", err
	}
	return b.client.NIP44Encrypt(ctx, targetPublicKey, plaintext)
}

// NIP44Decrypt admits and sends one nip44_decrypt request.
func (b *Bunker) NIP44Decrypt(ctx context.Context, targetPublicKey nostr.PubKey, ciphertext string) (string, error) {
	if err := b.admit(ctx); err != nil {
		return "", err
	}
	return b.client.NIP44Decrypt(ctx, targetPublicKey, ciphertext)
}
