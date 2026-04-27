package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestServiceSecret_ToRef(t *testing.T) {
	envID := uuid.New()
	secret := ServiceSecret{
		ID:               uuid.New(),
		ServiceID:        uuid.New(),
		EnvironmentID:    &envID,
		Name:             "DATABASE_URL",
		EncryptedValue:   []byte("encrypted-stuff"),
		EncryptionMethod: EncryptionNIP44,
		Version:          3,
		CreatedBy:        "npub1abc",
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}

	ref := secret.ToRef()

	if ref.ID != secret.ID {
		t.Error("ID mismatch")
	}
	if ref.ServiceID != secret.ServiceID {
		t.Error("ServiceID mismatch")
	}
	if ref.EnvironmentID == nil || *ref.EnvironmentID != envID {
		t.Error("EnvironmentID mismatch")
	}
	if ref.Name != "DATABASE_URL" {
		t.Errorf("expected DATABASE_URL, got %s", ref.Name)
	}
	if ref.EncryptionMethod != EncryptionNIP44 {
		t.Error("EncryptionMethod mismatch")
	}
	if ref.Version != 3 {
		t.Errorf("expected version 3, got %d", ref.Version)
	}
}

func TestEncryptionMethods(t *testing.T) {
	if EncryptionNIP44 != "nip44" {
		t.Errorf("expected nip44, got %s", EncryptionNIP44)
	}
	if EncryptionAES256 != "aes256gcm" {
		t.Errorf("expected aes256gcm, got %s", EncryptionAES256)
	}
}
