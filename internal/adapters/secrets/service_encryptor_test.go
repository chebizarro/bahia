package secrets

import (
	"errors"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

func TestServiceEncryptorFailsClosedWithoutKeyMaterial(t *testing.T) {
	remote := keyer.NewPlainKeySigner(nostr.Generate())
	encryptor, err := NewServiceEncryptor(remote)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(encryptor.Unavailable(), nostrutil.ErrServiceKeyMaterialRequired) {
		t.Fatalf("Unavailable() = %v, want ErrServiceKeyMaterialRequired", encryptor.Unavailable())
	}
	for _, method := range []domain.EncryptionMethod{domain.EncryptionAES256, domain.EncryptionNIP44} {
		if _, err := encryptor.Encrypt("v", method); !errors.Is(err, nostrutil.ErrServiceKeyMaterialRequired) {
			t.Fatalf("Encrypt(%s) error = %v", method, err)
		}
		if _, err := encryptor.Decrypt([]byte("c"), method); !errors.Is(err, nostrutil.ErrServiceKeyMaterialRequired) {
			t.Fatalf("Decrypt(%s) error = %v", method, err)
		}
		if _, err := encryptor.ReEncryptForWorker([]byte("c"), method, nostr.Generate().Public().Hex()); !errors.Is(err, nostrutil.ErrServiceKeyMaterialRequired) {
			t.Fatalf("ReEncryptForWorker(%s) error = %v", method, err)
		}
	}
}

// TestServiceEncryptorLocalModeReadsDeployedCiphertext pins local mode to the
// deployed HKDF key: ciphertext written by NewEncryptor(hex) still decrypts.
func TestServiceEncryptorLocalModeReadsDeployedCiphertext(t *testing.T) {
	key := nostr.Generate().Hex()
	deployed, err := NewEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	local, err := nostrutil.NewLocalKeyer(key)
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := NewServiceEncryptor(local)
	if err != nil || encryptor.Unavailable() != nil {
		t.Fatalf("NewServiceEncryptor(local) = %v, %v", err, encryptor.Unavailable())
	}
	for _, method := range []domain.EncryptionMethod{domain.EncryptionAES256, domain.EncryptionNIP44} {
		ciphertext, err := deployed.Encrypt("secret-value", method)
		if err != nil {
			t.Fatal(err)
		}
		plaintext, err := encryptor.Decrypt(ciphertext, method)
		if err != nil || plaintext != "secret-value" {
			t.Fatalf("Decrypt(%s) = %q, %v", method, plaintext, err)
		}
	}
}
