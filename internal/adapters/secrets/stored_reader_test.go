package secrets

import (
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestStoredSecretReadersRefuseCrossModeFallback(t *testing.T) {
	id := uuid.New()
	key, err := NewRandomDataKey()
	require.NoError(t, err)
	v2, err := key.Seal(id, 2, []byte("private-v2"))
	require.NoError(t, err)
	value, err := key.DecryptStored(id, 2, v2, domain.EncryptionAES256V2)
	require.NoError(t, err)
	require.Equal(t, "private-v2", value)
	_, err = key.DecryptStored(id, 2, v2, domain.EncryptionAES256)
	require.Error(t, err)
	_, err = key.DecryptStored(id, 1, v2, domain.EncryptionAES256V2)
	require.Error(t, err)
	_, err = key.DecryptStored(uuid.New(), 2, v2, domain.EncryptionAES256V2)
	require.Error(t, err)

	legacy, err := NewEncryptor(testPrivateKey)
	require.NoError(t, err)
	old, err := legacy.Encrypt("private-old", domain.EncryptionAES256)
	require.NoError(t, err)
	value, err = legacy.DecryptStored(id, 1, old, domain.EncryptionAES256)
	require.NoError(t, err)
	require.Equal(t, "private-old", value)
	_, err = legacy.DecryptStored(id, 2, v2, domain.EncryptionAES256V2)
	require.Error(t, err)
}
