package secrets

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
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
