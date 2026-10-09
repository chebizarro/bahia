package localstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenExistingOutboxStrictNeverCreatesOrRepairs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.bolt")
	_, err := OpenExistingOutboxStrict(path)
	require.Error(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)

	original := []byte("corrupt signed outbox must survive")
	require.NoError(t, os.WriteFile(path, original, 0o600))
	_, err = OpenExistingOutboxStrict(path)
	require.Error(t, err)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, after)
	matches, err := filepath.Glob(path + ".corrupt-*")
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestOpenExistingOutboxStrictUsesExistingFileExclusively(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.bolt")
	created, err := OpenOutbox(path)
	require.NoError(t, err)
	require.NoError(t, created.Close())
	strict, err := OpenExistingOutboxStrict(path)
	require.NoError(t, err)
	defer strict.Close()
	require.NoError(t, strict.PutControlRecord("migration", "test", []byte("ok")))
	_, err = OpenExistingOutboxStrict(path)
	require.ErrorContains(t, err, "already open")
	link := filepath.Join(filepath.Dir(path), "link.bolt")
	require.NoError(t, os.Symlink(path, link))
	_, err = OpenExistingOutboxStrict(link)
	require.ErrorContains(t, err, "no symlinks")
}
