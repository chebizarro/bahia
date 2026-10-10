package servicesigner

import (
	"context"
	"errors"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
)

var _ BinaryCipher = localKeyer{}

// localKeyer signs with nostr.private_key held in process memory.
type localKeyer struct{ keyer.KeySigner }

func newLocalKeyer(privateKey string) (localKeyer, error) {
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(privateKey))
	if err != nil || secret == (nostr.SecretKey{}) {
		return localKeyer{}, errors.New("nostr.private_key must be a 64-character hex secret key")
	}
	return localKeyer{keyer.NewPlainKeySigner(secret)}, nil
}

// EncryptBytes NIP-44-encrypts the exact bytes: a Go string carries them
// verbatim, so only remote transports need an encoding.
func (k localKeyer) EncryptBytes(ctx context.Context, plaintext []byte, recipient nostr.PubKey) (string, error) {
	if len(plaintext) == 0 {
		return "", errors.New("NIP-44 plaintext must not be empty")
	}
	return k.Encrypt(ctx, string(plaintext), recipient)
}

func (k localKeyer) DecryptBytes(ctx context.Context, ciphertext string, sender nostr.PubKey) ([]byte, error) {
	plaintext, err := k.Decrypt(ctx, ciphertext, sender)
	if err != nil {
		return nil, err
	}
	return []byte(plaintext), nil
}
