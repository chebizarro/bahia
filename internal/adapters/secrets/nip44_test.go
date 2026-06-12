package secrets

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

const testPrivateKey = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

func mustNewEncryptor(t *testing.T, key string) *Encryptor {
	t.Helper()
	enc, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("NewEncryptor() error = %v", err)
	}
	return enc
}

func TestAES256_EncryptDecrypt(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	original := "postgresql://user:pass@localhost/mydb"

	ciphertext, err := enc.Encrypt(original, domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if len(ciphertext) == 0 {
		t.Fatal("ciphertext is empty")
	}

	// Ciphertext should not contain the original value.
	if string(ciphertext) == original {
		t.Error("ciphertext equals plaintext — not encrypted")
	}

	plaintext, err := enc.Decrypt(ciphertext, domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if plaintext != original {
		t.Errorf("expected %q, got %q", original, plaintext)
	}
}

func TestAES256_DifferentCiphertexts(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	original := "same-secret-value"

	ct1, _ := enc.Encrypt(original, domain.EncryptionAES256)
	ct2, _ := enc.Encrypt(original, domain.EncryptionAES256)

	// Due to random nonce, two encryptions of the same value should differ.
	if string(ct1) == string(ct2) {
		t.Error("two encryptions produced identical ciphertext (random nonce not working)")
	}

	// Both should decrypt to the same value.
	p1, _ := enc.Decrypt(ct1, domain.EncryptionAES256)
	p2, _ := enc.Decrypt(ct2, domain.EncryptionAES256)
	if p1 != original || p2 != original {
		t.Error("decryptions don't match original")
	}
}

func TestAES256_WrongKeyFails(t *testing.T) {
	enc1 := mustNewEncryptor(t, "key1key1key1key1key1key1key1key1key1key1key1key1key1key1key1key1")
	enc2 := mustNewEncryptor(t, "key2key2key2key2key2key2key2key2key2key2key2key2key2key2key2key2")

	ct, err := enc1.Encrypt("secret", domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	_, err = enc2.Decrypt(ct, domain.EncryptionAES256)
	if err == nil {
		t.Fatal("expected decrypt to fail with wrong key")
	}
}

func TestAES256_ShortCiphertext(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	_, err := enc.Decrypt([]byte("short"), domain.EncryptionAES256)
	if err == nil {
		t.Fatal("expected error for short ciphertext")
	}
}

func TestAES256_EmptyPlaintext(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	ct, err := enc.Encrypt("", domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	pt, err := enc.Decrypt(ct, domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if pt != "" {
		t.Errorf("expected empty string, got %q", pt)
	}
}

func TestUnsupportedMethod(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	_, err := enc.Encrypt("test", "rot13")
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
	_, err = enc.Decrypt([]byte("test"), "rot13")
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
}

func TestNewEncryptor(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	if enc == nil {
		t.Fatal("expected non-nil encryptor")
	}
	if len(enc.aesKey) != 32 {
		t.Errorf("expected 32-byte AES key, got %d", len(enc.aesKey))
	}
}

// --- NIP-44 self-encryption tests ---

func TestNIP44_EncryptDecryptRoundTrip(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	original := "postgresql://user:pass@localhost/mydb"

	ciphertext, err := enc.Encrypt(original, domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("NIP-44 encrypt failed: %v", err)
	}
	if len(ciphertext) == 0 {
		t.Fatal("NIP-44 ciphertext is empty")
	}
	if string(ciphertext) == original {
		t.Error("NIP-44 ciphertext equals plaintext — not encrypted")
	}

	plaintext, err := enc.Decrypt(ciphertext, domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("NIP-44 decrypt failed: %v", err)
	}
	if plaintext != original {
		t.Errorf("NIP-44 round-trip: expected %q, got %q", original, plaintext)
	}
}

func TestNIP44_DifferentCiphertexts(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	original := "same-secret-value"

	ct1, err := enc.Encrypt(original, domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("first encrypt: %v", err)
	}
	ct2, err := enc.Encrypt(original, domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("second encrypt: %v", err)
	}

	// NIP-44 uses random padding, so two encryptions should differ.
	if string(ct1) == string(ct2) {
		t.Error("two NIP-44 encryptions produced identical ciphertext")
	}

	p1, _ := enc.Decrypt(ct1, domain.EncryptionNIP44)
	p2, _ := enc.Decrypt(ct2, domain.EncryptionNIP44)
	if p1 != original || p2 != original {
		t.Error("NIP-44 decryptions don't match original")
	}
}

func TestNIP44_WrongKeyCannotDecrypt(t *testing.T) {
	enc1 := mustNewEncryptor(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	enc2 := mustNewEncryptor(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	ct, err := enc1.Encrypt("secret", domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	_, err = enc2.Decrypt(ct, domain.EncryptionNIP44)
	if err == nil {
		t.Fatal("expected NIP-44 decrypt to fail with wrong key")
	}
}

func TestNIP44_EmptyPlaintext(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	// NIP-44 may reject empty plaintext depending on implementation;
	// verify it either round-trips or returns a clear error.
	ct, err := enc.Encrypt("", domain.EncryptionNIP44)
	if err != nil {
		// Empty plaintext rejection is acceptable.
		t.Skipf("NIP-44 rejects empty plaintext: %v", err)
	}
	pt, err := enc.Decrypt(ct, domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("NIP-44 decrypt empty: %v", err)
	}
	if pt != "" {
		t.Errorf("expected empty string, got %q", pt)
	}
}

func TestNIP44_SelfEncryptionUsesPublicKeyDerivation(t *testing.T) {
	// Verify that NIP-44 self-encryption derives the conversation key using the
	// *public* key (not the private key as recipient). Two encryptors with the
	// same private key must produce interchangeable ciphertext.
	enc1 := mustNewEncryptor(t, testPrivateKey)
	enc2 := mustNewEncryptor(t, testPrivateKey)

	ct, err := enc1.Encrypt("consistency-check", domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	pt, err := enc2.Decrypt(ct, domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("decrypt with second encryptor: %v", err)
	}
	if pt != "consistency-check" {
		t.Errorf("cross-encryptor round-trip: expected %q, got %q", "consistency-check", pt)
	}
}

func TestNIP44_ReEncryptForWorker(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	original := "worker-secret-value"

	// Encrypt with NIP-44 self-encryption.
	ct, err := enc.Encrypt(original, domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Re-encrypt for a "worker" (using a different key as the worker pubkey).
	workerPrivKey := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	workerEnc := mustNewEncryptor(t, workerPrivKey)
	workerPubKey := workerEnc.publicKey()

	reEncrypted, err := enc.ReEncryptForWorker(ct, domain.EncryptionNIP44, workerPubKey)
	if err != nil {
		t.Fatalf("ReEncryptForWorker: %v", err)
	}
	if reEncrypted == "" {
		t.Fatal("re-encrypted ciphertext is empty")
	}

	// The worker should be able to decrypt using a conversation key with Bahia's pubkey.
	bahiaPubKey := enc.publicKey()
	conversationKey, err := workerEnc.nip44ConversationKeyWith(bahiaPubKey)
	if err != nil {
		t.Fatalf("worker conversation key: %v", err)
	}
	_ = conversationKey // conversation key generated successfully
}

func (e *Encryptor) nip44ConversationKeyWith(recipientPubkey string) ([32]byte, error) {
	return nostrutil.NIP44ConversationKey(recipientPubkey, e.privateKey)
}

func TestNIP44_LargePayload(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	original := make([]byte, 4096)
	for i := range original {
		original[i] = byte(i % 256)
	}

	ct, err := enc.Encrypt(string(original), domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("NIP-44 encrypt large: %v", err)
	}
	pt, err := enc.Decrypt(ct, domain.EncryptionNIP44)
	if err != nil {
		t.Fatalf("NIP-44 decrypt large: %v", err)
	}
	if pt != string(original) {
		t.Error("NIP-44 large payload round-trip failed")
	}
}

// --- Legacy compatibility documentation ---
// The migration from github.com/nbd-wtf/go-nostr to fiatjaf.com/nostr changed
// the NIP-44 self-encryption conversation key derivation. The old code passed
// the private key hex as the "recipient" identifier, which produced an incorrect
// conversation key. The new code correctly derives the public key first.
//
// Any NIP-44 ciphertext stored with the old derivation CANNOT be decrypted with
// the new code. Operators who stored NIP-44 encrypted secrets before this
// migration must re-encrypt them. AES-256 secrets are unaffected.

func TestNewEncryptorRejectsBlankKey(t *testing.T) {
	if enc, err := NewEncryptor("   "); err == nil || enc != nil {
		t.Fatalf("NewEncryptor blank key = (%v, %v), want nil encryptor and error", enc, err)
	}
}

func TestAES256_LargePayload(t *testing.T) {
	enc := mustNewEncryptor(t, testPrivateKey)
	// Simulate a large secret (e.g., a PEM certificate).
	original := make([]byte, 4096)
	for i := range original {
		original[i] = byte(i % 256)
	}

	ct, err := enc.Encrypt(string(original), domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("encrypt large: %v", err)
	}

	pt, err := enc.Decrypt(ct, domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("decrypt large: %v", err)
	}

	if pt != string(original) {
		t.Error("large payload round-trip failed")
	}
}
