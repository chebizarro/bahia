package servicesigner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
)

// Binary-safe NIP-44 methods: a documented non-standard extension some
// bunkers serve. Params are [peer_pubkey_hex, base64(plaintext)] and
// [peer_pubkey_hex, ciphertext]; only the plaintext side is base64.
const (
	methodNIP44EncryptBinary = "nip44_encrypt_b64"
	methodNIP44DecryptBinary = "nip44_decrypt_b64"
)

// Untrusted payloads are bounded before base64 decoding, which otherwise
// allocates proportionally to the input.
const maxNIP44PayloadBase64 = 16 << 20

var errClosed = errors.New("NIP-46 service signer session is closed")

// Bunker replies that mean "this method does not exist here", as opposed to a
// refusal or transport failure.
var unsupportedMethodReplies = []string{"unsupported method", "unknown method", "method not found", "method not supported", "not implemented"}

var _ BinaryCipher = (*nip46Keyer)(nil)

// nip46Keyer signs and NIP-44-encrypts as the pinned service identity through
// standard NIP-46 requests. Every request crosses outbound admission in
// nostrout.Bunker. Any response whose identity or immutable fields differ
// from the request is discarded.
type nip46Keyer struct {
	expected nostr.PubKey
	rpc      func(context.Context, string, []string) (string, error)
	timeout  time.Duration
	closed   <-chan struct{}
	cancel   context.CancelFunc
}

func openNIP46(ctx context.Context, signer config.NostrSignerConfig, expected nostr.PubKey, timeout time.Duration, admission *nostrout.Admission, onAuthURL func(string)) (*nip46Keyer, error) {
	clientSecret, err := nip46ClientSecret(signer)
	if err != nil {
		return nil, err
	}
	if clientSecret.Public() == expected {
		return nil, errors.New("dedicated NIP-46 client key must differ from the service key")
	}
	lifetime, cancel := context.WithCancel(ctx)
	bunker, err := connectBunker(lifetime, timeout, func(connectCtx context.Context) (*nostrout.Bunker, error) {
		return nostrout.ConnectBunker(connectCtx, admission, clientSecret, signer.BunkerURI, nil, onAuthURL)
	})
	if err != nil {
		cancel()
		return nil, redactError(fmt.Errorf("connect NIP-46 bunker: %w", err), signer.BunkerURI)
	}
	pubkeyCtx, cancelPubkey := context.WithTimeout(lifetime, timeout)
	actual, err := bunker.GetPublicKey(pubkeyCtx)
	cancelPubkey()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("NIP-46 get_public_key: %w", err)
	}
	if actual != expected {
		cancel()
		return nil, fmt.Errorf("NIP-46 signer pubkey %s differs from configured service pubkey %s", actual.Hex(), expected.Hex())
	}
	return &nip46Keyer{expected: expected, rpc: bunker.RPC, timeout: timeout, closed: lifetime.Done(), cancel: cancel}, nil
}

func nip46ClientSecret(signer config.NostrSignerConfig) (nostr.SecretKey, error) {
	resolved, err := ResolveClientKeyFile(config.NostrConfig{Signer: signer})
	if err != nil {
		return nostr.SecretKey{}, err
	}
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(resolved.Signer.ClientSecretKey))
	if err != nil || secret == (nostr.SecretKey{}) {
		return nostr.SecretKey{}, errors.New("dedicated NIP-46 client key must be a 64-character hex secret key")
	}
	return secret, nil
}

// connectBunker bounds the NIP-46 handshake. The library keeps the context it
// connects with as the response subscription's lifetime, so the deadline
// cannot be a child context; on expiry the caller cancels lifetime instead.
func connectBunker(lifetime context.Context, timeout time.Duration, connect func(context.Context) (*nostrout.Bunker, error)) (*nostrout.Bunker, error) {
	type result struct {
		bunker *nostrout.Bunker
		err    error
	}
	done := make(chan result, 1)
	go func() {
		bunker, err := connect(lifetime)
		done <- result{bunker, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.bunker, r.err
	case <-timer.C:
		return nil, fmt.Errorf("no NIP-46 connect acknowledgement within %s: the bunker relays or the bunker did not answer "+
			"(NIP-46 bunkers may ignore a connect secret that was already used: if this client key was paired before, "+
			"remove the secret from nostr.signer.bunker_uri or pair it again with a fresh one)", timeout)
	case <-lifetime.Done():
		return nil, lifetime.Err()
	}
}

// redactError keeps a pairing URI (which may carry a connect secret) out of
// error text.
func redactError(err error, secrets ...string) error {
	message := err.Error()
	redacted := message
	for _, secret := range secrets {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "[redacted]")
		}
	}
	if redacted == message {
		return err
	}
	return errors.New(redacted)
}

func (k *nip46Keyer) live(ctx context.Context) error {
	select {
	case <-k.closed:
		return errClosed
	default:
		return ctx.Err()
	}
}

// call sends one request and discards a result that arrives after the
// session or caller context ended.
func (k *nip46Keyer) call(ctx context.Context, method string, params []string) (string, error) {
	if err := k.live(ctx); err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	result, err := k.rpc(callCtx, method, params)
	if err != nil {
		if unsupportedMethod(err) {
			return "", fmt.Errorf("NIP-46 %s: %w: %w", method, errors.ErrUnsupported, err)
		}
		return "", fmt.Errorf("NIP-46 %s: %w", method, err)
	}
	if err := k.live(ctx); err != nil {
		return "", err
	}
	if result == "" {
		return "", fmt.Errorf("NIP-46 %s returned no result", method)
	}
	return result, nil
}

func unsupportedMethod(err error) bool {
	message := strings.ToLower(err.Error())
	for _, reply := range unsupportedMethodReplies {
		if strings.Contains(message, reply) {
			return true
		}
	}
	return false
}

// GetPublicKey returns the service pubkey verified against the bunker at Open.
func (k *nip46Keyer) GetPublicKey(ctx context.Context) (nostr.PubKey, error) {
	if err := k.live(ctx); err != nil {
		return nostr.ZeroPK, err
	}
	return k.expected, nil
}

// SignEvent sends a standard sign_event request and accepts only a validly
// signed event by the service pubkey with the requested kind, timestamp,
// content and tags. event is unchanged on any failure.
func (k *nip46Keyer) SignEvent(ctx context.Context, event *nostr.Event) error {
	if event == nil {
		return errors.New("event is required")
	}
	if event.PubKey != nostr.ZeroPK && event.PubKey != k.expected {
		return errors.New("event author differs from the service pubkey")
	}
	if event.ID != (nostr.ID{}) || event.Sig != ([64]byte{}) {
		return errors.New("event must be unsigned before remote signing")
	}
	request := *event
	request.Tags = cloneTags(event.Tags)
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode event for NIP-46: %w", err)
	}
	response, err := k.call(ctx, "sign_event", []string{string(requestJSON)})
	if err != nil {
		return err
	}
	var signed nostr.Event
	if err := json.Unmarshal([]byte(response), &signed); err != nil {
		return fmt.Errorf("decode NIP-46 signed event: %w", err)
	}
	if signed.PubKey != k.expected || signed.Kind != request.Kind || signed.CreatedAt != request.CreatedAt || signed.Content != request.Content || !sameTags(signed.Tags, request.Tags) {
		return errors.New("NIP-46 signer changed the event identity or immutable fields")
	}
	if !signed.CheckID() || !signed.VerifySignature() {
		return errors.New("NIP-46 signer returned an invalid event id or signature")
	}
	*event = signed
	return nil
}

// Nip04Encrypt is refused: the service identity uses NIP-44 only.
func (k *nip46Keyer) Nip04Encrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", fmt.Errorf("NIP-04 for the service identity: %w", errors.ErrUnsupported)
}

// Nip04Decrypt is refused: the service identity uses NIP-44 only.
func (k *nip46Keyer) Nip04Decrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", fmt.Errorf("NIP-04 for the service identity: %w", errors.ErrUnsupported)
}

// Encrypt sends standard nip44_encrypt [peer_pubkey, plaintext]. NIP-46 JSON
// params cannot carry arbitrary bytes; binary plaintext uses EncryptBytes.
func (k *nip46Keyer) Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error) {
	if !textPlaintext(plaintext) {
		return "", errors.New("NIP-44 text plaintext must be non-empty NUL-free UTF-8; use EncryptBytes for binary")
	}
	return k.encrypt(ctx, "nip44_encrypt", recipient, plaintext)
}

// Decrypt sends standard nip44_decrypt [peer_pubkey, ciphertext].
func (k *nip46Keyer) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	plaintext, err := k.decrypt(ctx, "nip44_decrypt", sender, ciphertext)
	if err != nil {
		return "", err
	}
	if !textPlaintext(plaintext) {
		return "", errors.New("NIP-46 signer returned invalid or NUL-containing UTF-8 plaintext")
	}
	return plaintext, nil
}

// EncryptBytes sends nip44_encrypt_b64 [peer_pubkey, base64(plaintext)].
func (k *nip46Keyer) EncryptBytes(ctx context.Context, plaintext []byte, recipient nostr.PubKey) (string, error) {
	if len(plaintext) == 0 {
		return "", errors.New("NIP-44 plaintext must not be empty")
	}
	return k.encrypt(ctx, methodNIP44EncryptBinary, recipient, base64.StdEncoding.EncodeToString(plaintext))
}

// DecryptBytes sends nip44_decrypt_b64 [peer_pubkey, ciphertext] and decodes
// the base64 plaintext strictly.
func (k *nip46Keyer) DecryptBytes(ctx context.Context, ciphertext string, sender nostr.PubKey) ([]byte, error) {
	encoded, err := k.decrypt(ctx, methodNIP44DecryptBinary, sender, ciphertext)
	if err != nil {
		return nil, err
	}
	plaintext, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(plaintext) == 0 {
		return nil, errors.New("NIP-46 signer returned malformed base64 plaintext")
	}
	return plaintext, nil
}

func (k *nip46Keyer) encrypt(ctx context.Context, method string, peer nostr.PubKey, plaintext string) (string, error) {
	if peer == nostr.ZeroPK {
		return "", errors.New("NIP-44 peer pubkey is required")
	}
	ciphertext, err := k.call(ctx, method, []string{peer.Hex(), plaintext})
	if err != nil {
		return "", err
	}
	if !validNIP44Payload(ciphertext) {
		return "", fmt.Errorf("NIP-46 %s returned a malformed NIP-44 payload", method)
	}
	return ciphertext, nil
}

func (k *nip46Keyer) decrypt(ctx context.Context, method string, peer nostr.PubKey, ciphertext string) (string, error) {
	if peer == nostr.ZeroPK {
		return "", errors.New("NIP-44 peer pubkey is required")
	}
	if !validNIP44Payload(ciphertext) {
		return "", errors.New("invalid NIP-44 ciphertext")
	}
	return k.call(ctx, method, []string{peer.Hex(), ciphertext})
}

func textPlaintext(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}

func validNIP44Payload(payload string) bool {
	if len(payload) < 132 || len(payload) > maxNIP44PayloadBase64 {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(payload)
	return err == nil && len(decoded) >= 99 && decoded[0] == 2
}

func sameTags(a, b nostr.Tags) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func cloneTags(tags nostr.Tags) nostr.Tags {
	cloned := make(nostr.Tags, len(tags))
	for i, tag := range tags {
		cloned[i] = append(nostr.Tag(nil), tag...)
	}
	return cloned
}
