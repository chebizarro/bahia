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
	"golang.org/x/sys/unix"
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

func TestF74aLocalBackupObjectRejectsFIFOAndSymlinkWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "backup-fifo")
	require.NoError(t, unix.Mkfifo(fifo, 0o600))
	symlink := filepath.Join(dir, "backup-symlink")
	require.NoError(t, os.Symlink(fifo, symlink))
	for _, path := range []string{fifo, symlink} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			proof := repository.F74aReceiptVerification{
				ReceiptID: uuid.New(), BackupObjectRef: (&url.URL{Scheme: "file", Path: path}).String(),
				BackupObjectSHA256: hex.EncodeToString(make([]byte, 32)), ExpiresAt: time.Now().Add(time.Hour),
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := repository.VerifyF74aLocalBackupObject(ctx, proof); done <- err }()
			select {
			case err := <-done:
				require.Error(t, err)
			case <-ctx.Done():
				t.Fatal("backup object verification blocked on FIFO or symlink open")
			}
		})
	}
}
