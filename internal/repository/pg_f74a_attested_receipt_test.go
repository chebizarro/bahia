package repository_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestF74aReceiptRejectsSignedUnknownPayloadBeforeDatabaseAccess(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	payload := json.RawMessage(`{"version":"bahia-f74a-backup-restore-v1","unknown_authorization":true}`)
	signature := ed25519.Sign(priv, append([]byte("bahia-f74a-backup-restore-v1\x00"), payload...))
	receipt, err := json.Marshal(struct {
		Payload   json.RawMessage `json:"payload"`
		Signature string          `json:"signature"`
	}{payload, hex.EncodeToString(signature)})
	require.NoError(t, err)
	_, err = repository.VerifyF74aAttestedReceipt(context.Background(), nil, hex.EncodeToString(pub), receipt)
	require.ErrorContains(t, err, "unknown field")
}

func TestF74aReceiptRejectsSignedDuplicatePayloadKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	payload := json.RawMessage(`{"version":"bahia-f74a-backup-restore-v1","version":"bahia-f74a-backup-restore-v1"}`)
	signature := ed25519.Sign(priv, append([]byte("bahia-f74a-backup-restore-v1\x00"), payload...))
	receipt, err := json.Marshal(struct {
		Payload   json.RawMessage `json:"payload"`
		Signature string          `json:"signature"`
	}{payload, hex.EncodeToString(signature)})
	require.NoError(t, err)
	_, err = repository.VerifyF74aAttestedReceipt(context.Background(), nil, hex.EncodeToString(pub), receipt)
	require.ErrorContains(t, err, "duplicate object key")
}
