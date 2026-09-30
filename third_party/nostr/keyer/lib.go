package keyer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip05"
	"fiatjaf.com/nostr/nip19"
	"fiatjaf.com/nostr/nip46"
	"fiatjaf.com/nostr/nip49"
	"github.com/puzpuzpuz/xsync/v3"
)

var (
	_ nostr.Keyer = (*BunkerSigner)(nil)
	_ nostr.Keyer = (*EncryptedKeySigner)(nil)
	_ nostr.Keyer = (*KeySigner)(nil)
	_ nostr.Keyer = (*ManualSigner)(nil)
)

// SignerOptions contains configuration options for creating a new signer.
type SignerOptions struct {
	// BunkerClientSecretKey is the secret key used for the bunker client
	BunkerClientSecretKey nostr.SecretKey

	// BunkerSignTimeout is the timeout duration for bunker signing operations
	BunkerSignTimeout time.Duration

	// BunkerAuthHandler is called when authentication is needed for bunker operations
	BunkerAuthHandler func(string)

	// PasswordHandler is called when an operation needs access to the encrypted key.
	// If provided, the key will be stored encrypted and this function will be called
	// every time an operation needs access to the key so the user can be prompted.
	PasswordHandler func(context.Context) string

	// Password is used along with ncryptsec to decrypt the key.
	// If provided, the key will be decrypted and stored in plaintext.
	Password string
}

// New creates a new Keyer implementation based on the input string format.
// It supports various input formats:
// - ncryptsec: Creates an EncryptedKeySigner or KeySigner depending on options
// - NIP-46 bunker URL or NIP-05 identifier: Creates a BunkerSigner
// - nsec: Creates a KeySigner
// - hex private key: Creates a KeySigner
//
// The context is used for operations that may require network access.
// The pool is used for relay connections when needed.
// Options are used for additional pieces required for EncryptedKeySigner and BunkerSigner.
func New(ctx context.Context, pool *nostr.Pool, input string, opts *SignerOptions) (nostr.Keyer, error) {
	if opts == nil {
		opts = &SignerOptions{}
	}

	if strings.HasPrefix(input, "ncryptsec") {
		if opts.PasswordHandler != nil {
			return &EncryptedKeySigner{input, nostr.ZeroPK, opts.PasswordHandler}, nil
		}
		sec, err := nip49.Decrypt(input, opts.Password)
		if err != nil {
			if opts.Password == "" {
				return nil, fmt.Errorf("failed to decrypt with blank password: %w", err)
			}
			return nil, fmt.Errorf("failed to decrypt with given password: %w", err)
		}
		pk := nostr.GetPublicKey(sec)
		return KeySigner{sec, pk, xsync.NewMapOf[nostr.PubKey, [32]byte]()}, nil
	} else if nip46.IsValidBunkerURL(input) || nip05.IsValidIdentifier(input) {
		bcsk := nostr.Generate()
		oa := func(url string) { println("auth_url received but not handled") }

		if opts.BunkerClientSecretKey != [32]byte{} {
			bcsk = opts.BunkerClientSecretKey
		}
		if opts.BunkerAuthHandler != nil {
			oa = opts.BunkerAuthHandler
		}

		bunker, err := nip46.ConnectBunker(ctx, bcsk, input, pool, oa)
		if err != nil {
			return nil, err
		}
		return BunkerSigner{bunker}, nil
	} else if prefix, parsed, err := nip19.Decode(input); err == nil && prefix == "nsec" {
		sec := parsed.(nostr.SecretKey)
		pk := nostr.GetPublicKey(sec)
		return KeySigner{sec, pk, xsync.NewMapOf[nostr.PubKey, [32]byte]()}, nil
	} else if sk, err := nostr.SecretKeyFromHex(input); err == nil {
		pk := nostr.GetPublicKey(sk)
		return KeySigner{sk, pk, xsync.NewMapOf[nostr.PubKey, [32]byte]()}, nil
	}

	return nil, fmt.Errorf("unsupported input '%s'", input)
}
