// Package secrets provides encryption adapters for service secret management.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"fiatjaf.com/nostr/nip44"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"golang.org/x/crypto/hkdf"
)

var ErrEmptyNIP44Plaintext = errors.New("NIP-44 plaintext must not be empty")

// Encryptor encrypts and decrypts secret values using the configured method.
type Encryptor struct {
	privateKey string // Bahia's Nostr private key (hex)
	aesKey     []byte // derived AES-256 key for symmetric encryption
}

// NewEncryptor creates a new Encryptor with the given Nostr private key.
func NewEncryptor(nostrPrivateKey string) (*Encryptor, error) {
	nostrPrivateKey = strings.TrimSpace(nostrPrivateKey)
	if nostrPrivateKey == "" {
		return nil, errors.New("nostr private key is required for secret encryption")
	}

	keyMaterial, err := hex.DecodeString(nostrPrivateKey)
	if err != nil || len(keyMaterial) != 32 {
		return nil, errors.New("nostr private key must be a 32-byte hex-encoded secp256k1 key")
	}
	if _, err := nostrutil.PublicKeyHexFromPrivateKeyHex(nostrPrivateKey); err != nil {
		return nil, fmt.Errorf("invalid nostr private key: %w", err)
	}

	// Derive an encryption-only key with explicit domain separation. This avoids
	// reusing SHA-256(privateKey) directly across the identity and data-key domains.
	aesKey := make([]byte, 32)
	reader := hkdf.New(
		sha256.New,
		keyMaterial,
		[]byte("bahia/secrets/aes-256-gcm/hkdf-sha256/v1"),
		[]byte("service-secret-data-key"),
	)
	if _, err := io.ReadFull(reader, aesKey); err != nil {
		return nil, fmt.Errorf("deriving AES encryption key: %w", err)
	}

	return &Encryptor{
		privateKey: nostrPrivateKey,
		aesKey:     aesKey,
	}, nil
}

// Encrypt encrypts a plaintext value using the specified method.
func (e *Encryptor) Encrypt(plaintext string, method domain.EncryptionMethod) ([]byte, error) {
	switch method {
	case domain.EncryptionNIP44:
		return e.encryptNIP44(plaintext)
	case domain.EncryptionAES256:
		return e.encryptAES256([]byte(plaintext))
	default:
		return nil, fmt.Errorf("unsupported encryption method: %s", method)
	}
}

// Decrypt decrypts an encrypted value using the specified method.
func (e *Encryptor) Decrypt(ciphertext []byte, method domain.EncryptionMethod) (string, error) {
	switch method {
	case domain.EncryptionNIP44:
		return e.decryptNIP44(ciphertext)
	case domain.EncryptionAES256:
		plaintext, err := e.decryptAES256(ciphertext)
		if err != nil {
			return "", err
		}
		return string(plaintext), nil
	default:
		return "", fmt.Errorf("unsupported encryption method: %s", method)
	}
}

// ReEncryptForWorker decrypts a secret and re-encrypts it using NIP-44 for a specific worker pubkey.
// This is used during deployment to share secrets with the target worker.
func (e *Encryptor) ReEncryptForWorker(ciphertext []byte, method domain.EncryptionMethod, workerPubkey string) (string, error) {
	// First, decrypt the secret using Bahia's key.
	plaintext, err := e.Decrypt(ciphertext, method)
	if err != nil {
		return "", fmt.Errorf("decrypting for re-encryption: %w", err)
	}

	// Re-encrypt using NIP-44 with the worker's pubkey.
	conversationKey, err := nostrutil.NIP44ConversationKey(workerPubkey, e.privateKey)
	if err != nil {
		return "", fmt.Errorf("generating conversation key for worker: %w", err)
	}

	encrypted, err := nip44.Encrypt(plaintext, conversationKey)
	if err != nil {
		return "", fmt.Errorf("encrypting for worker: %w", err)
	}

	return encrypted, nil
}

// --- NIP-44 encryption (self-encryption: Bahia encrypts to its own pubkey) ---

func (e *Encryptor) encryptNIP44(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, ErrEmptyNIP44Plaintext
	}

	// Self-encryption: use a conversation key with Bahia's derived public key.
	pubkey, err := e.publicKey()
	if err != nil {
		return nil, fmt.Errorf("deriving self public key: %w", err)
	}
	conversationKey, err := nostrutil.NIP44ConversationKey(pubkey, e.privateKey)
	if err != nil {
		return nil, fmt.Errorf("generating self conversation key: %w", err)
	}

	encrypted, err := nip44.Encrypt(plaintext, conversationKey)
	if err != nil {
		return nil, fmt.Errorf("nip44 encrypt: %w", err)
	}

	return []byte(encrypted), nil
}

func (e *Encryptor) decryptNIP44(ciphertext []byte) (string, error) {
	pubkey, err := e.publicKey()
	if err != nil {
		return "", fmt.Errorf("deriving self public key: %w", err)
	}
	conversationKey, err := nostrutil.NIP44ConversationKey(pubkey, e.privateKey)
	if err != nil {
		return "", fmt.Errorf("generating self conversation key: %w", err)
	}

	plaintext, err := nip44.Decrypt(string(ciphertext), conversationKey)
	if err != nil {
		return "", fmt.Errorf("nip44 decrypt: %w", err)
	}

	return plaintext, nil
}

// publicKey derives the public key from the private key.
func (e *Encryptor) publicKey() (string, error) {
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(e.privateKey)
	if err != nil {
		return "", fmt.Errorf("deriving nostr public key: %w", err)
	}
	return pubkey, nil
}

// --- AES-256-GCM encryption ---

func (e *Encryptor) encryptAES256(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(e.aesKey)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// Prepend nonce to ciphertext.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (e *Encryptor) decryptAES256(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(e.aesKey)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM decrypt: %w", err)
	}

	return plaintext, nil
}
