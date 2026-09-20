package nostrarchive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

type memoryArchiveStore struct {
	batch            repository.NostrEventArchiveBatch
	rows             []string
	restored         map[string]struct{}
	protected        bool
	failRestoreBatch bool
}

func (s *memoryArchiveStore) GetArchiveBatch(_ context.Context, id uuid.UUID) (*repository.NostrEventArchiveBatch, error) {
	if id != s.batch.ID {
		return nil, repository.ErrNostrArchiveBatchNotFound
	}
	copy := s.batch
	return &copy, nil
}

func (s *memoryArchiveStore) ListArchiveBatchJSON(_ context.Context, id uuid.UUID) ([]string, error) {
	return append([]string(nil), s.rows...), nil
}

func (s *memoryArchiveStore) MarkArchiveExported(_ context.Context, id uuid.UUID, path, digest string, size int64) error {
	s.batch.Status = repository.NostrArchiveStatusExported
	s.batch.ExportedPath = path
	s.batch.SHA256 = digest
	s.batch.CompressedBytes = size
	return nil
}

func (s *memoryArchiveStore) MarkArchiveProtected(_ context.Context, id uuid.UUID, uri, version, digest string) error {
	s.protected = true
	s.batch.Status = repository.NostrArchiveStatusProtected
	s.batch.ObjectURI = uri
	s.batch.ObjectVersion = version
	return nil
}

func (s *memoryArchiveStore) RestoreArchiveBatchJSON(_ context.Context, rows []string) (int64, error) {
	if s.failRestoreBatch {
		return 0, fmt.Errorf("simulated restore failure")
	}
	var inserted int64
	for _, row := range rows {
		var value struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(row), &value); err != nil {
			return 0, err
		}
		if _, exists := s.restored[value.ID]; exists {
			continue
		}
		s.restored[value.ID] = struct{}{}
		inserted++
	}
	return inserted, nil
}

func TestExporterWritesAtomicDigestVerifiedArtifact(t *testing.T) {
	id := uuid.New()
	store := &memoryArchiveStore{
		batch:    repository.NostrEventArchiveBatch{ID: id, Status: repository.NostrArchiveStatusClaimed, RowCount: 2},
		rows:     []string{`{"id":"a","content":"one"}`, `{"id":"b","content":"two"}`},
		restored: make(map[string]struct{}),
	}
	dir := filepath.Join(t.TempDir(), "archive")
	manager, err := NewArtifactManager(store, dir)
	require.NoError(t, err)

	batch, err := manager.Export(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, repository.NostrArchiveStatusExported, batch.Status)
	require.Len(t, batch.SHA256, 64)
	require.Positive(t, batch.CompressedBytes)
	info, err := os.Stat(batch.ExportedPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = os.Stat(batch.ExportedPath + ".tmp")
	require.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, manager.ConfirmProtected(context.Background(), id, "kopia://bahia/nostr-events", "snapshot-1"))
	require.True(t, store.protected)
}

func TestExportIgnoresCrashOrphanedTempFile(t *testing.T) {
	id := uuid.New()
	store := &memoryArchiveStore{
		batch:    repository.NostrEventArchiveBatch{ID: id, Status: repository.NostrArchiveStatusClaimed, RowCount: 1},
		rows:     []string{`{"id":"a"}`},
		restored: make(map[string]struct{}),
	}
	dir := t.TempDir()
	orphan := filepath.Join(dir, id.String()+".jsonl.gz.tmp")
	require.NoError(t, os.WriteFile(orphan, []byte("partial export"), 0o600))
	manager, err := NewArtifactManager(store, dir)
	require.NoError(t, err)

	batch, err := manager.Export(context.Background(), id)
	require.NoError(t, err)
	require.FileExists(t, batch.ExportedPath)
	require.FileExists(t, orphan)
}

func TestRestoreRejectsDigestMismatchAndIsIdempotent(t *testing.T) {
	id := uuid.New()
	store := &memoryArchiveStore{
		batch:    repository.NostrEventArchiveBatch{ID: id, Status: repository.NostrArchiveStatusClaimed, RowCount: 2},
		rows:     []string{`{"id":"a","received_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`, `{"id":"b"}`},
		restored: make(map[string]struct{}),
	}
	manager, err := NewArtifactManager(store, filepath.Join(t.TempDir(), "archive"))
	require.NoError(t, err)
	batch, err := manager.Export(context.Background(), id)
	require.NoError(t, err)

	inserted, err := manager.Restore(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, int64(2), inserted)
	inserted, err = manager.Restore(context.Background(), id)
	require.NoError(t, err)
	require.Zero(t, inserted)

	require.NoError(t, os.WriteFile(batch.ExportedPath, []byte("corrupted"), 0o600))
	_, err = manager.Restore(context.Background(), id)
	require.ErrorContains(t, err, "digest mismatch")
}

func TestRestoreRowCountMismatchLeavesDatabaseUntouched(t *testing.T) {
	id := uuid.New()
	store := &memoryArchiveStore{
		batch:    repository.NostrEventArchiveBatch{ID: id, Status: repository.NostrArchiveStatusClaimed, RowCount: 2},
		rows:     []string{`{"id":"a"}`, `{"id":"b"}`},
		restored: make(map[string]struct{}),
	}
	manager, err := NewArtifactManager(store, filepath.Join(t.TempDir(), "archive"))
	require.NoError(t, err)
	_, err = manager.Export(context.Background(), id)
	require.NoError(t, err)

	store.batch.RowCount = 3

	_, err = manager.Restore(context.Background(), id)
	require.ErrorContains(t, err, "row count mismatch")
	require.Empty(t, store.restored, "no rows should be restored on count mismatch")

	store.batch.RowCount = 2
	inserted, err := manager.Restore(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, int64(2), inserted)
	require.Len(t, store.restored, 2)
}

func TestRestoreMidBatchFailureLeavesDatabaseUntouched(t *testing.T) {
	id := uuid.New()
	store := &memoryArchiveStore{
		batch:            repository.NostrEventArchiveBatch{ID: id, Status: repository.NostrArchiveStatusClaimed, RowCount: 2},
		rows:             []string{`{"id":"a"}`, `{"id":"b"}`},
		restored:         make(map[string]struct{}),
		failRestoreBatch: true,
	}
	manager, err := NewArtifactManager(store, filepath.Join(t.TempDir(), "archive"))
	require.NoError(t, err)
	_, err = manager.Export(context.Background(), id)
	require.NoError(t, err)

	_, err = manager.Restore(context.Background(), id)
	require.ErrorContains(t, err, "simulated restore failure")
	require.Empty(t, store.restored, "no rows should be restored on batch failure")
}
