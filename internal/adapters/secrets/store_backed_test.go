package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PostgreSQL readers refuse a store-backed marker with a typed error naming
// the secret, rather than treating the empty value as a value.
func TestStoredReadersRefuseStoreBackedMarker(t *testing.T) {
	encryptor, err := NewEncryptor(testPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := NewRandomDataKey()
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	for name, reader := range map[string]StoredSecretDecryptor{"legacy reader": encryptor, "data-key reader": key} {
		value, err := reader.DecryptStored(id, 1, nil, domain.EncryptionStoreRef)
		if !errors.Is(err, domain.ErrSecretInStore) || value != "" {
			t.Fatalf("%s: DecryptStored(marker) = %q, %v", name, value, err)
		}
	}

	marker := &domain.ServiceSecret{ID: id, ServiceID: uuid.New(), Name: "DB_PASSWORD", EncryptionMethod: domain.EncryptionStoreRef, Version: 2}
	repo := &auditSecretRepo{secret: marker, version: &domain.SecretVersion{ID: uuid.New(), SecretID: id, Version: 2, EncryptionMethod: domain.EncryptionStoreRef}}
	value, _, err := NewResolver(repo, encryptor).ResolveSecretWithAudit(context.Background(), id.String(), domain.SecretResolveOptions{})
	if !errors.Is(err, domain.ErrSecretInStore) || value != "" {
		t.Fatalf("ResolveSecretWithAudit(marker) = %q, %v", value, err)
	}
	if err.Error() == domain.ErrSecretInStore.Error() {
		t.Fatalf("the error must name the secret: %v", err)
	}
}
