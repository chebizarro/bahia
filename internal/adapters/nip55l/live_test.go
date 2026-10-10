//go:build nip55llive

package nip55l

// Opt-in test against a real org.nostr.Signer (the nostrc reference
// nostr-signer-daemon or grotto-daemon). It is not run by default.
//
// Setup:
//  1. Run the signer on the session bus (or a private bus) holding the key
//     for NIP55L_LIVE_PUBKEY in its keystore (libsecret / Keychain). The
//     reference daemon reads NOSTR_SIGNER_SECKEY_HEX only for the empty
//     ("active") selector, and this Keyer always sends the 64-hex selector,
//     so an env-only key is not enough: store it with StoreKey (daemon run
//     with NOSTR_SIGNER_ALLOW_KEY_MUTATIONS=1) or through Grotto.
//  2. Approve the prompts, or pre-grant this test's principal in
//     $XDG_CONFIG_HOME/gnostr/signer-grants.ini for the kinds event,
//     nip44_encrypt and nip44_decrypt (on macOS the principal is
//     claimed:bahia).
//  3. NIP55L_LIVE_PUBKEY=<npub or hex> [NIP55L_LIVE_BUS_ADDRESS=<addr>] \
//     go test -tags nip55llive -run TestLiveSigner -count=1 -timeout 20m \
//     ./internal/adapters/nip55l

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"fiatjaf.com/nostr"
)

func TestLiveSigner(t *testing.T) {
	sel := os.Getenv("NIP55L_LIVE_PUBKEY")
	if sel == "" {
		t.Skip("set NIP55L_LIVE_PUBKEY to run against a real NIP-55L signer")
	}
	pk, ok := parseIdentity(sel)
	if !ok {
		t.Fatalf("NIP55L_LIVE_PUBKEY %q is not an npub or 64-hex pubkey", sel)
	}
	ctx := context.Background()
	k, err := New(ctx, Config{ServicePubkey: pk, BusAddress: os.Getenv("NIP55L_LIVE_BUS_ADDRESS")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer k.Close()

	evt := &nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"t", "nip55l-live"}}, Content: "bahia nip55l live test"}
	if err := k.SignEvent(ctx, evt); err != nil {
		t.Fatalf("SignEvent: %v", err)
	}

	// NIP-44 to self: the conversation key with one's own pubkey is symmetric.
	ct, err := k.Encrypt(ctx, "live round trip", pk)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if pt, err := k.Decrypt(ctx, ct, pk); err != nil || pt != "live round trip" {
		t.Fatalf("Decrypt = %q, %v", pt, err)
	}

	bin := []byte{0, 1, 2, 0xff}
	ctb, err := k.EncryptBytes(ctx, bin, pk)
	if errors.Is(err, errors.ErrUnsupported) {
		t.Logf("signer has no NIP44EncryptB64ForApp: %v", err)
		return
	}
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}
	if got, err := k.DecryptBytes(ctx, ctb, pk); err != nil || !bytes.Equal(got, bin) {
		t.Fatalf("DecryptBytes = %x, %v", got, err)
	}
}
