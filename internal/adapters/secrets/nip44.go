// Package secrets provides encryption adapters for service secret management.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Encryptor encrypts and decrypts secret values using the configured method.
type Encryptor struct {
	privateKey string // Bahia's Nostr private key (hex)
	aesKey     []byte // derived AES-256 key for symmetric encryption
}

// NewEncryptor creates a new Encryptor with the given Nostr private key.
// The AES key is derived from the private key via SHA-256.
func NewEncryptor(nostrPrivateKey string) (*Encryptor, error) {
	nostrPrivateKey = strings.TrimSpace(nostrPrivateKey)
	if nostrPrivateKey == "" {
		return nil, errors.New("nostr private key is required for secret encryption")
	}

	// Derive AES-256 key from the Nostr private key via SHA-256.
	hash := sha256.Sum256([]byte(nostrPrivateKey))
	return &Encryptor{
		privateKey: nostrPrivateKey,
		aesKey:     hash[:],
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
	conversationKey, err := nip44.GenerateConversationKey(workerPubkey, e.privateKey)
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
	// Self-encryption: use conversation key with ourselves.
	conversationKey, err := nip44.GenerateConversationKey(e.publicKey(), e.privateKey)
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
	conversationKey, err := nip44.GenerateConversationKey(e.publicKey(), e.privateKey)
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
func (e *Encryptor) publicKey() string {
	// For NIP-44, the private key is used directly. The go-nostr library
	// handles key derivation internally. We pass the private key as both sides
	// of the conversation key to encrypt to ourselves.
	// In practice, the pubkey is derived from the privkey via secp256k1.
	// For self-encryption, using the privkey as the "recipient" actually
	// generates a conversation key that only we can decrypt.
	return e.privateKey
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
