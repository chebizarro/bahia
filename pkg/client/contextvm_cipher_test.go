package client

import (
	"context"
	"strings"
	"testing"

	nostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
)

// remoteSignerLike mirrors the CLI's NIP-46/Signet signer: it can sign and
// perform NIP-44, but deliberately implements no NIP-04, exactly like a bunker
// that exposes only the modern cipher. It also holds no local secret.
type remoteSignerLike struct{ inner nostr.Keyer }

func (s remoteSignerLike) GetPublicKey(ctx context.Context) (nostr.PubKey, error) {
	return s.inner.GetPublicKey(ctx)
}
func (s remoteSignerLike) SignEvent(ctx context.Context, evt *nostr.Event) error {
	return s.inner.SignEvent(ctx, evt)
}
func (s remoteSignerLike) Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error) {
	return s.inner.Encrypt(ctx, plaintext, recipient)
}
func (s remoteSignerLike) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	return s.inner.Decrypt(ctx, ciphertext, sender)
}

func newRemoteSignerLike(t *testing.T) (remoteSignerLike, string) {
	t.Helper()
	secret := nostr.Generate()
	return remoteSignerLike{inner: keyer.NewPlainKeySigner(secret)}, secret.Public().Hex()
}

func TestRemoteSignerWithoutNIP04SatisfiesEncryptedContextVMPath(t *testing.T) {
	signer, pubkey := newRemoteSignerLike(t)

	// Guard the regression precisely: this signer is NOT a full nostr.Keyer
	// because it has no NIP-04, which is what previously rejected NIP-46.
	if _, isKeyer := any(signer).(nostr.Keyer); isKeyer {
		t.Fatal("fixture must not satisfy nostr.Keyer, or it cannot prove the regression")
	}
	if _, ok := any(signer).(contextVMCipherSigner); !ok {
		t.Fatal("a signer with NIP-44 encrypt/decrypt must satisfy contextVMCipherSigner")
	}

	// Both encrypted clients must now construct without a raw private key.
	if _, err := NewContextVMRequestClient(ContextVMRequestConfig{
		Relays: []string{"wss://relay.example"}, Signer: signer, SenderPubkey: pubkey,
		RecipientPubkey: strings.Repeat("a", 64), Encrypted: true,
	}); err != nil {
		t.Fatalf("generic encrypted client rejected a NIP-46-shaped signer: %v", err)
	}
	if _, err := NewOperatorControlPlaneClient(OperatorControlPlaneConfig{
		Relays: []string{"wss://relay.example"}, Signer: signer, Pubkey: pubkey,
		ServicePubkey: strings.Repeat("a", 64), Encrypted: true,
	}); err != nil {
		t.Fatalf("operator encrypted client rejected a NIP-46-shaped signer: %v", err)
	}
}

func TestNIP59AdapterSupportsWrappingButRefusesNIP04(t *testing.T) {
	signer, _ := newRemoteSignerLike(t)
	adapter := nip59KeyerAdapter{signer}

	// The adapter must satisfy the full Keyer that cascadia-go's NIP-59 helper
	// demands, even though that helper only calls the NIP-44 half.
	if _, ok := any(adapter).(nostr.Keyer); !ok {
		t.Fatal("adapter must satisfy nostr.Keyer for NIP-59 wrapping")
	}

	ctx := context.Background()
	recipient := nostr.Generate().Public()
	ciphertext, err := adapter.Encrypt(ctx, "payload", recipient)
	if err != nil {
		t.Fatalf("adapter must delegate NIP-44 encryption: %v", err)
	}
	if ciphertext == "" {
		t.Fatal("adapter returned empty ciphertext")
	}

	// NIP-04 must fail loudly rather than silently pretending support.
	if _, err := adapter.Nip04Encrypt(ctx, "payload", recipient); err == nil ||
		!strings.Contains(err.Error(), "NIP-04 is not supported") {
		t.Fatalf("Nip04Encrypt error = %v, want explicit unsupported", err)
	}
	if _, err := adapter.Nip04Decrypt(ctx, "payload", recipient); err == nil ||
		!strings.Contains(err.Error(), "NIP-04 is not supported") {
		t.Fatalf("Nip04Decrypt error = %v, want explicit unsupported", err)
	}
}
