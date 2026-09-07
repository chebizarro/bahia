package client

import (
	"context"
	"fmt"

	nostr "fiatjaf.com/nostr"
)

// contextVMCipherSigner is the capability set the encrypted ContextVM path
// actually uses. NIP-59 sealing needs the sender's NIP-44 Encrypt plus
// SignEvent/GetPublicKey, and the correlated response needs NIP-44 Decrypt.
//
// This deliberately does NOT require the full nostr.Keyer. Keyer also mandates
// the deprecated NIP-04 pair, which Bahia's ContextVM transport never uses, and
// demanding it rejected perfectly capable remote signers — notably the
// NIP-46/Signet CLI signer, forcing operators toward a raw local nsec.
type contextVMCipherSigner interface {
	nostr.Signer

	Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error)
	Decrypt(ctx context.Context, base64ciphertext string, sender nostr.PubKey) (string, error)
}

// errNIP04Unsupported is returned by the adapter below rather than pretending a
// remote signer can perform NIP-04.
var errNIP04Unsupported = fmt.Errorf("NIP-04 is not supported by this signer; Bahia ContextVM uses NIP-44 only")

// nip59KeyerAdapter satisfies the full nostr.Keyer required by cascadia-go's
// NIP-59 helpers while only depending on the NIP-44 capabilities those helpers
// actually call. The NIP-04 methods are unreachable on this path and fail
// loudly if that ever stops being true.
type nip59KeyerAdapter struct{ contextVMCipherSigner }

func (nip59KeyerAdapter) Nip04Encrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", errNIP04Unsupported
}

func (nip59KeyerAdapter) Nip04Decrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", errNIP04Unsupported
}
