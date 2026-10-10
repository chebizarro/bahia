package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
)

const dataKeyCipherVersion byte = 2
const maxServiceSecretBytes = 1 << 20

// WrappedDataKey is a random AES key wrapped to the existing service pubkey.
// WrappedHex is NIP-44 ciphertext over exactly 64 lowercase hex characters.
type WrappedDataKey struct {
	ID         uuid.UUID
	ServiceKey nostr.PubKey
	WrappedHex string
}

// DataKey is an in-memory v2 secret key; the caller controls its lifetime.
type DataKey struct {
	id  uuid.UUID
	key [32]byte
}

// This keyer seam is package-private until a production Signet-fenced caller
// can be wired without creating an adapter import cycle.
func newWrappedDataKey(ctx context.Context, keyer nostr.Keyer, service nostr.PubKey) (WrappedDataKey, *DataKey, error) {
	if keyer == nil || service == nostr.ZeroPK {
		return WrappedDataKey{}, nil, errors.New("fenced service keyer and existing service pubkey are required")
	}
	actual, err := keyer.GetPublicKey(ctx)
	if err != nil || actual != service {
		return WrappedDataKey{}, nil, errors.New("service keyer pubkey does not match existing service pubkey")
	}
	key := &DataKey{id: uuid.New()}
	if _, err := io.ReadFull(rand.Reader, key.key[:]); err != nil {
		return WrappedDataKey{}, nil, errors.New("generate random service-secret data key")
	}
	wrapped, err := keyer.Encrypt(ctx, hex.EncodeToString(key.key[:]), service)
	if err != nil || wrapped == "" {
		return WrappedDataKey{}, nil, errors.New("fenced service-secret data-key wrap failed")
	}
	return WrappedDataKey{ID: key.id, ServiceKey: service, WrappedHex: wrapped}, key, nil
}

func openWrappedDataKey(ctx context.Context, keyer nostr.Keyer, wrapped WrappedDataKey, expected nostr.PubKey) (*DataKey, error) {
	if keyer == nil || expected == nostr.ZeroPK || wrapped.ServiceKey != expected || wrapped.ID == uuid.Nil || wrapped.WrappedHex == "" {
		return nil, errors.New("invalid wrapped service-secret data key")
	}
	actual, err := keyer.GetPublicKey(ctx)
	if err != nil || actual != expected {
		return nil, errors.New("service keyer pubkey does not match existing service pubkey")
	}
	plainHex, err := keyer.Decrypt(ctx, wrapped.WrappedHex, expected)
	if err != nil {
		return nil, errors.New("fenced service-secret data-key unwrap failed")
	}
	plain, err := hex.DecodeString(plainHex)
	if err != nil || len(plain) != 32 || hex.EncodeToString(plain) != plainHex {
		return nil, errors.New("malformed unwrapped service-secret data key")
	}
	key := &DataKey{id: wrapped.ID}
	copy(key.key[:], plain)
	return key, nil
}

func (k *DataKey) Seal(secretID uuid.UUID, version int, plaintext []byte) ([]byte, error) {
	if k == nil || k.id == uuid.Nil || secretID == uuid.Nil || version <= 0 || len(plaintext) > maxServiceSecretBytes {
		return nil, errors.New("invalid service-secret v2 seal input")
	}
	aead, err := dataKeyAEAD(k.key[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.New("generate service-secret nonce")
	}
	header := make([]byte, 1+16+len(nonce))
	header[0] = dataKeyCipherVersion
	copy(header[1:17], k.id[:])
	copy(header[17:], nonce)
	return aead.Seal(header, nonce, plaintext, secretAAD(secretID, version, k.id)), nil
}

func (k *DataKey) Open(secretID uuid.UUID, version int, ciphertext []byte) ([]byte, error) {
	if k == nil || k.id == uuid.Nil || secretID == uuid.Nil || version <= 0 || len(ciphertext) < 1+16+12+16 || len(ciphertext) > maxServiceSecretBytes+1+16+12+16 ||
		ciphertext[0] != dataKeyCipherVersion || uuid.UUID(ciphertext[1:17]) != k.id {
		return nil, errors.New("invalid service-secret v2 ciphertext")
	}
	aead, err := dataKeyAEAD(k.key[:])
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, ciphertext[17:29], ciphertext[29:], secretAAD(secretID, version, k.id))
	if err != nil {
		return nil, errors.New("service-secret v2 authentication failed")
	}
	return plain, nil
}

func (k *DataKey) ID() uuid.UUID {
	if k == nil {
		return uuid.Nil
	}
	return k.id
}

func dataKeyAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize service-secret v2 cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func secretAAD(secretID uuid.UUID, version int, keyID uuid.UUID) []byte {
	aad := make([]byte, 16+8+16)
	copy(aad[:16], secretID[:])
	binary.BigEndian.PutUint64(aad[16:24], uint64(version))
	copy(aad[24:], keyID[:])
	return aad
}
