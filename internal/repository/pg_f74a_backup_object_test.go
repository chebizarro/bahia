package repository_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestF74aLocalBackupObjectHashIsReadOnlyAndFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f74a-backup")
	content := []byte("independent backup bytes")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	digest := sha256.Sum256(content)
	proof := repository.F74aReceiptVerification{
		ReceiptID: uuid.New(), BackupObjectRef: (&url.URL{Scheme: "file", Path: path}).String(),
		BackupObjectSHA256: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().Add(time.Hour),
	}
	size, err := repository.VerifyF74aLocalBackupObject(context.Background(), proof)
	require.NoError(t, err)
	require.Equal(t, int64(len(content)), size)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, got)

	bad := proof
	bad.BackupObjectRef = "https://example.invalid/backup"
	_, err = repository.VerifyF74aLocalBackupObject(context.Background(), bad)
	require.ErrorContains(t, err, "not a local file URI")
	bad.BackupObjectRef = (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "sensitive-missing-object")}).String()
	_, err = repository.VerifyF74aLocalBackupObject(context.Background(), bad)
	require.ErrorContains(t, err, "cannot be opened")
	require.NotContains(t, err.Error(), "sensitive-missing-object")
	bad.BackupObjectRef = proof.BackupObjectRef
	bad.ExpiresAt = time.Now().Add(-time.Second)
	_, err = repository.VerifyF74aLocalBackupObject(context.Background(), bad)
	require.ErrorContains(t, err, "expired")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = repository.VerifyF74aLocalBackupObject(ctx, proof)
	require.ErrorIs(t, err, context.Canceled)
}
