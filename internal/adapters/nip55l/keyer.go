// Package nip55l is a nostr.Keyer for the Bahia service identity backed by a
// NIP-55L desktop signer: the org.nostr.Signer D-Bus service (protocol
// nip55l 0.7.0, nostrc docs/proposals/55L.md). The secret key stays in the
// signer, and each gated call may wait for the user's approval.
//
// Every call names the configured service pubkey as the identity selector,
// never "" (the signer's active identity), so a signer that switched accounts
// fails the call instead of signing with another key. app_id is always sent
// explicitly through SignEvent or the ...ForApp method variants. Signed events
// are verified locally before they are returned. Nothing is retried, so a sign
// request is never sent twice.
package nip55l

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"github.com/godbus/dbus/v5"
)

const (
	signerBusName   = "org.nostr.Signer"
	signerPath      = dbus.ObjectPath("/org/nostr/signer")
	signerInterface = "org.nostr.Signer"

	// DefaultAppID is the app_id label sent when Config.AppID is empty.
	DefaultAppID = "bahia"
	// DefaultCallTimeout exceeds the signer's 300 s approval window, as the
	// spec requires (Groundhog uses the same value).
	DefaultCallTimeout = 330 * time.Second
	// MinCallTimeout is the parked-request lifetime. A shorter call timeout
	// would abandon requests the user can still approve.
	MinCallTimeout = 300 * time.Second

	// NIP-44 v2 bounds: plaintext 1..65535 bytes; payload base64 length for
	// the smallest and largest padded plaintexts.
	nip44MaxPlaintext  = 65535
	nip44MinPayloadB64 = 132
	nip44MaxPayloadB64 = 87472
)

var _ nostr.Keyer = (*Keyer)(nil)

// Config selects the signer identity and transport.
type Config struct {
	// ServicePubkey is the identity selector, sent as 64-hex on every call.
	ServicePubkey nostr.PubKey
	// AppID is the app_id label for SignEvent and the ...ForApp variants.
	// Signers treat it as unverified. Default DefaultAppID.
	AppID string
	// BusAddress is "" for the session bus, otherwise a D-Bus address (a
	// private bus or tests).
	BusAddress string
	// CallTimeout bounds each call, including the approval wait. Zero means
	// DefaultCallTimeout; values below MinCallTimeout are rejected.
	CallTimeout time.Duration
}

// Keyer signs and encrypts as Config.ServicePubkey through org.nostr.Signer.
// It is safe for concurrent use.
type Keyer struct {
	conn        *dbus.Conn
	obj         dbus.BusObject
	pubkey      nostr.PubKey
	identity    string
	appID       string
	callTimeout time.Duration

	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}
}

// New connects to the bus and confirms the signer holds cfg.ServicePubkey,
// with ListIdentities when the signer has it (0.6.0+, not gated) and
// otherwise by checking that GetPublicKeyForApp returns that key (gated, and
// only succeeds if the service key is the signer's active identity). Any
// failure closes the connection and returns an error.
func New(ctx context.Context, cfg Config) (*Keyer, error) {
	if cfg.ServicePubkey == nostr.ZeroPK {
		return nil, errors.New("nip55l: ServicePubkey is required")
	}
	appID := cfg.AppID
	if appID == "" {
		appID = DefaultAppID
	}
	if err := checkDBusString("AppID", appID); err != nil {
		return nil, err
	}
	timeout := cfg.CallTimeout
	if timeout == 0 {
		timeout = DefaultCallTimeout
	}
	if timeout < MinCallTimeout {
		return nil, fmt.Errorf("nip55l: CallTimeout %s is shorter than the %s approval window", timeout, MinCallTimeout)
	}

	conn, err := connect(ctx, cfg.BusAddress)
	if err != nil {
		return nil, err
	}
	k := &Keyer{
		conn:        conn,
		obj:         conn.Object(signerBusName, signerPath),
		pubkey:      cfg.ServicePubkey,
		identity:    cfg.ServicePubkey.Hex(),
		appID:       appID,
		callTimeout: timeout,
		closed:      make(chan struct{}),
	}
	if err := k.verifyIdentity(ctx); err != nil {
		_ = k.Close()
		return nil, err
	}
	return k, nil
}

// connect dials and authenticates without letting ctx cancellation strand
// the caller; a dial that finishes after cancellation is closed.
func connect(ctx context.Context, address string) (*dbus.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("nip55l connect: %w", err)
	}
	type result struct {
		conn *dbus.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		if address == "" {
			r.conn, r.err = dbus.ConnectSessionBus()
		} else {
			r.conn, r.err = dbus.Connect(address)
		}
		done <- r
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, &SignerError{Method: "connect", Kind: ErrSignerUnavailable, Message: r.err.Error()}
		}
		return r.conn, nil
	case <-ctx.Done():
		go func() {
			if r := <-done; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, fmt.Errorf("nip55l connect: %w", ctx.Err())
	}
}

func (k *Keyer) verifyIdentity(ctx context.Context) error {
	var ids []string
	err := k.call(ctx, "ListIdentities", false, &ids)
	switch {
	case err == nil:
		for _, id := range ids {
			if pk, ok := parseIdentity(id); ok && pk == k.pubkey {
				return nil
			}
		}
		return &SignerError{Method: "ListIdentities", Kind: ErrIdentityNotFound,
			Message: fmt.Sprintf("%d stored identities, none is %s", len(ids), nip19.EncodeNpub(k.pubkey))}
	case !isUnknownMethod(err):
		return err
	}
	// Pre-0.6.0 signer: the only probe is the active identity.
	var active string
	if err := k.call(ctx, "GetPublicKeyForApp", true, &active, k.appID); err != nil {
		return err
	}
	pk, ok := parseIdentity(active)
	if !ok {
		return badReply("GetPublicKeyForApp", "reply is not an npub")
	}
	if pk != k.pubkey {
		return &SignerError{Method: "GetPublicKeyForApp", Kind: ErrIdentityNotFound,
			Message: fmt.Sprintf("active identity is %s, want %s", nip19.EncodeNpub(pk), nip19.EncodeNpub(k.pubkey))}
	}
	return nil
}

// parseIdentity accepts an npub or a 64-hex x-only key.
func parseIdentity(s string) (nostr.PubKey, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "npub1") {
		prefix, v, err := nip19.Decode(s)
		if err != nil || prefix != "npub" {
			return nostr.ZeroPK, false
		}
		pk, ok := v.(nostr.PubKey)
		return pk, ok
	}
	if len(s) != 64 {
		return nostr.ZeroPK, false
	}
	pk, err := nostr.PubKeyFromHex(s)
	return pk, err == nil
}

// call performs one D-Bus call bounded by ctx and CallTimeout. gated calls are
// preceded by an unawaited EnableTypedApprovalErrors: the signer handles a
// connection's calls in order, so the opt-in applies to the gated call even
// if the signer restarted since the last one. A pre-0.5.0 signer's
// UnknownMethod reply to the opt-in is discarded.
func (k *Keyer) call(ctx context.Context, method string, gated bool, out any, args ...any) error {
	select {
	case <-k.closed:
		return &SignerError{Method: method, Kind: ErrClosed}
	default:
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("nip55l %s: %w", method, err)
	}
	cctx, cancel := context.WithTimeout(ctx, k.callTimeout)
	defer cancel()
	if gated {
		k.obj.CallWithContext(cctx, signerInterface+".EnableTypedApprovalErrors", dbus.FlagNoReplyExpected)
	}
	c := k.obj.CallWithContext(cctx, signerInterface+"."+method, 0, args...)
	if c.Err != nil {
		return mapCallError(ctx, method, c.Err)
	}
	if err := c.Store(out); err != nil {
		return badReply(method, "decode reply: %v", err)
	}
	return nil
}

// GetPublicKey returns the service pubkey New verified. It does not call the
// signer: GetPublicKey on the bus reports the active identity, which is not
// the selector this Keyer uses.
func (k *Keyer) GetPublicKey(context.Context) (nostr.PubKey, error) {
	select {
	case <-k.closed:
		return nostr.ZeroPK, &SignerError{Method: "GetPublicKey", Kind: ErrClosed}
	default:
	}
	return k.pubkey, nil
}

// unsignedEvent is the SignEvent input. pubkey is advisory: the signer always
// sets it to the signing key.
type unsignedEvent struct {
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      nostr.Tags `json:"tags"`
	Content   string     `json:"content"`
}

// SignEvent signs evt as the service identity. A zero CreatedAt is set to now
// before the request so the reply can be checked field by field. The returned
// event must have a valid id and signature, pubkey == ServicePubkey, and the
// kind, created_at, tags and content that were sent; otherwise evt is left
// unchanged and the error wraps ErrBadReply.
func (k *Keyer) SignEvent(ctx context.Context, evt *nostr.Event) error {
	if evt == nil {
		return errors.New("nip55l SignEvent: nil event")
	}
	if err := checkEventText(evt); err != nil {
		return err
	}
	createdAt := evt.CreatedAt
	if createdAt == 0 {
		createdAt = nostr.Now()
	}
	tags := evt.Tags
	if tags == nil {
		tags = nostr.Tags{}
	}
	payload, err := json.Marshal(unsignedEvent{
		PubKey:    k.identity,
		CreatedAt: int64(createdAt),
		Kind:      int(evt.Kind),
		Tags:      tags,
		Content:   evt.Content,
	})
	if err != nil {
		return fmt.Errorf("nip55l SignEvent: encode event: %w", err)
	}
	var reply string
	if err := k.call(ctx, "SignEvent", true, &reply, string(payload), k.identity, k.appID); err != nil {
		return err
	}
	signed, err := k.verifySigned(reply, createdAt, evt)
	if err != nil {
		return err
	}
	evt.CreatedAt = signed.CreatedAt
	evt.PubKey = signed.PubKey
	evt.ID = signed.ID
	evt.Sig = signed.Sig
	return nil
}

func (k *Keyer) verifySigned(reply string, createdAt nostr.Timestamp, want *nostr.Event) (nostr.Event, error) {
	const method = "SignEvent"
	trimmed := strings.TrimSpace(reply)
	if len(trimmed) == 128 && isHex(trimmed) {
		return nostr.Event{}, &SignerError{Method: method, Kind: errors.ErrUnsupported,
			Message: "signer returned a bare signature (pre-0.2.0); a signed event is required"}
	}
	var got nostr.Event
	if err := json.Unmarshal([]byte(trimmed), &got); err != nil {
		return nostr.Event{}, badReply(method, "reply is not an event: %v", err)
	}
	switch {
	case got.PubKey != k.pubkey:
		return nostr.Event{}, badReply(method, "signed by %s, want %s", got.PubKey.Hex(), k.identity)
	case got.Kind != want.Kind:
		return nostr.Event{}, badReply(method, "signer changed kind %d to %d", want.Kind, got.Kind)
	case got.CreatedAt != createdAt:
		return nostr.Event{}, badReply(method, "signer changed created_at %d to %d", createdAt, got.CreatedAt)
	case got.Content != want.Content:
		return nostr.Event{}, badReply(method, "signer changed content")
	case !got.Tags.Eq(want.Tags):
		return nostr.Event{}, badReply(method, "signer changed tags")
	case !got.CheckID():
		return nostr.Event{}, badReply(method, "event id does not match its content")
	case !got.VerifySignature():
		return nostr.Event{}, badReply(method, "invalid signature")
	}
	return got, nil
}

// Encrypt NIP-44 v2 encrypts a text plaintext to recipient with
// NIP44EncryptForApp. D-Bus strings must be NUL-free UTF-8; use EncryptBytes
// for binary data.
func (k *Keyer) Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error) {
	const method = "NIP44EncryptForApp"
	if err := checkPlaintext(method, len(plaintext)); err != nil {
		return "", err
	}
	if err := checkDBusString("plaintext", plaintext); err != nil {
		return "", fmt.Errorf("%w; use EncryptBytes for binary plaintexts", err)
	}
	return k.encrypt(ctx, method, plaintext, recipient)
}

// EncryptBytes NIP-44 v2 encrypts binary plaintext with NIP44EncryptB64ForApp.
// Only the transport is base64; the payload covers the exact bytes. A signer
// without the method yields an error wrapping errors.ErrUnsupported.
func (k *Keyer) EncryptBytes(ctx context.Context, plaintext []byte, recipient nostr.PubKey) (string, error) {
	const method = "NIP44EncryptB64ForApp"
	if err := checkPlaintext(method, len(plaintext)); err != nil {
		return "", err
	}
	return k.encrypt(ctx, method, base64.StdEncoding.EncodeToString(plaintext), recipient)
}

func (k *Keyer) encrypt(ctx context.Context, method, input string, recipient nostr.PubKey) (string, error) {
	if recipient == nostr.ZeroPK {
		return "", fmt.Errorf("nip55l %s: recipient pubkey is required", method)
	}
	var payload string
	if err := k.call(ctx, method, true, &payload, input, recipient.Hex(), k.identity, k.appID); err != nil {
		return "", err
	}
	if !validNIP44Payload(payload) {
		return "", badReply(method, "reply is not a NIP-44 v2 payload")
	}
	return payload, nil
}

// Decrypt opens a NIP-44 v2 payload from sender with NIP44DecryptForApp.
func (k *Keyer) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	const method = "NIP44DecryptForApp"
	var plaintext string
	if err := k.decrypt(ctx, method, ciphertext, sender, &plaintext); err != nil {
		return "", err
	}
	return plaintext, nil
}

// DecryptBytes opens a NIP-44 v2 payload whose plaintext may be binary, with
// NIP44DecryptB64ForApp. A signer without the method yields an error wrapping
// errors.ErrUnsupported.
func (k *Keyer) DecryptBytes(ctx context.Context, ciphertext string, sender nostr.PubKey) ([]byte, error) {
	const method = "NIP44DecryptB64ForApp"
	var plaintextB64 string
	if err := k.decrypt(ctx, method, ciphertext, sender, &plaintextB64); err != nil {
		return nil, err
	}
	plaintext, err := base64.StdEncoding.Strict().DecodeString(plaintextB64)
	if err != nil {
		return nil, badReply(method, "reply is not standard base64")
	}
	return plaintext, nil
}

func (k *Keyer) decrypt(ctx context.Context, method, ciphertext string, sender nostr.PubKey, out *string) error {
	if sender == nostr.ZeroPK {
		return fmt.Errorf("nip55l %s: sender pubkey is required", method)
	}
	if !validNIP44Payload(ciphertext) {
		return fmt.Errorf("nip55l %s: ciphertext is not a NIP-44 v2 payload", method)
	}
	return k.call(ctx, method, true, out, ciphertext, sender.Hex(), k.identity, k.appID)
}

var errNIP04Unsupported = fmt.Errorf("nip55l: NIP-04 is not used for the Bahia service identity: %w", errors.ErrUnsupported)

// Nip04Encrypt is refused: the service identity uses NIP-44 only.
func (k *Keyer) Nip04Encrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", errNIP04Unsupported
}

// Nip04Decrypt is refused: the service identity uses NIP-44 only.
func (k *Keyer) Nip04Decrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", errNIP04Unsupported
}

// Close drops the bus connection. Calls in flight fail with
// ErrSignerUnavailable; later calls fail with ErrClosed. A request the
// signer already accepted may still complete on its side.
func (k *Keyer) Close() error {
	k.closeOnce.Do(func() {
		close(k.closed)
		k.closeErr = k.conn.Close()
	})
	return k.closeErr
}

func checkPlaintext(method string, n int) error {
	if n == 0 || n > nip44MaxPlaintext {
		return fmt.Errorf("nip55l %s: plaintext must be 1..%d bytes, got %d", method, nip44MaxPlaintext, n)
	}
	return nil
}

// checkDBusString rejects values D-Bus cannot carry as type s.
func checkDBusString(field, s string) error {
	if !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {
		return fmt.Errorf("nip55l: %s must be NUL-free UTF-8", field)
	}
	return nil
}

func checkEventText(evt *nostr.Event) error {
	if err := checkDBusString("event content", evt.Content); err != nil {
		return err
	}
	for _, tag := range evt.Tags {
		for _, v := range tag {
			if err := checkDBusString("event tag", v); err != nil {
				return err
			}
		}
	}
	return nil
}

// validNIP44Payload checks the shape of a NIP-44 v2 payload: base64 within
// the spec's length bounds whose first byte is version 2.
func validNIP44Payload(payload string) bool {
	if len(payload) < nip44MinPayloadB64 || len(payload) > nip44MaxPayloadB64 {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	return err == nil && len(raw) > 0 && raw[0] == 2
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}
