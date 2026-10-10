package secrets

import (
	"errors"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// StoredSecretDecryptor binds a stored payload to its database identity and
// version. A raw-key legacy reader refuses v2; a data-key reader refuses
// legacy. Callers must choose a mode explicitly rather than fall back.
type StoredSecretDecryptor interface {
	DecryptStored(uuid.UUID, int, []byte, domain.EncryptionMethod) (string, error)
}

func (e *Encryptor) DecryptStored(_ uuid.UUID, _ int, ciphertext []byte, method domain.EncryptionMethod) (string, error) {
	if e == nil || method == domain.EncryptionAES256V2 {
		return "", errors.New("legacy service-secret reader cannot read v2 ciphertext")
	}
	return e.Decrypt(ciphertext, method)
}

func (k *DataKey) DecryptStored(secretID uuid.UUID, version int, ciphertext []byte, method domain.EncryptionMethod) (string, error) {
	if method != domain.EncryptionAES256V2 {
		return "", errors.New("versioned service-secret reader refuses legacy ciphertext")
	}
	plain, err := k.Open(secretID, version, ciphertext)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
