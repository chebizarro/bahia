// Package nostrarchive writes and restores immutable Nostr event archive
// artifacts. Database ownership and pruning remain in repository so the file
// and row lifecycles cannot be accidentally conflated.
package nostrarchive

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
)

type Store interface {
	GetArchiveBatch(context.Context, uuid.UUID) (*repository.NostrEventArchiveBatch, error)
	ListArchiveBatchJSON(context.Context, uuid.UUID) ([]string, error)
	MarkArchiveExported(context.Context, uuid.UUID, string, string, int64) error
	MarkArchiveProtected(context.Context, uuid.UUID, string, string, string) error
	RestoreArchiveJSON(context.Context, string) (bool, error)
}

type ArtifactManager struct {
	store Store
	dir   string
}

func NewArtifactManager(store Store, dir string) (*ArtifactManager, error) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if store == nil {
		return nil, errors.New("Nostr archive store is required")
	}
	if dir == "." || !filepath.IsAbs(dir) {
		return nil, errors.New("Nostr archive directory must be absolute")
	}
	return &ArtifactManager{store: store, dir: dir}, nil
}

// Export writes a deterministic gzip NDJSON artifact and only then records the
// exported state in PostgreSQL. An orphan file after a crash is safe to remove;
// an exported batch always points at a complete, fsynced artifact.
func (m *ArtifactManager) Export(ctx context.Context, id uuid.UUID) (*repository.NostrEventArchiveBatch, error) {
	batch, err := m.store.GetArchiveBatch(ctx, id)
	if err != nil {
		return nil, err
	}
	if batch.Status != repository.NostrArchiveStatusClaimed && batch.Status != repository.NostrArchiveStatusExported {
		return nil, fmt.Errorf("Nostr archive batch %s cannot export from status %s", id, batch.Status)
	}
	rows, err := m.store.ListArchiveBatchJSON(ctx, id)
	if err != nil {
		return nil, err
	}
	if int64(len(rows)) != batch.RowCount {
		return nil, fmt.Errorf("Nostr archive batch %s row count changed: manifest=%d selected=%d", id, batch.RowCount, len(rows))
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating Nostr archive directory: %w", err)
	}
	finalPath := filepath.Join(m.dir, id.String()+".jsonl.gz")
	tmp, err := os.OpenFile(finalPath+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating Nostr archive artifact: %w", err)
	}
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(finalPath + ".tmp")
		}
	}()

	hash := sha256.New()
	counter := &countingWriter{writer: io.MultiWriter(tmp, hash)}
	zw, err := gzip.NewWriterLevel(counter, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("creating Nostr archive compressor: %w", err)
	}
	zw.Header.ModTime = time.Unix(0, 0).UTC()
	for _, row := range rows {
		if _, err := io.WriteString(zw, row+"\n"); err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("writing Nostr archive artifact: %w", err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("closing Nostr archive compressor: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("syncing Nostr archive artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("closing Nostr archive artifact: %w", err)
	}
	if err := os.Rename(finalPath+".tmp", finalPath); err != nil {
		return nil, fmt.Errorf("publishing Nostr archive artifact: %w", err)
	}
	if err := syncDirectory(m.dir); err != nil {
		return nil, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if err := m.store.MarkArchiveExported(ctx, id, finalPath, digest, counter.count); err != nil {
		return nil, err
	}
	ok = true
	return m.store.GetArchiveBatch(ctx, id)
}

func (m *ArtifactManager) ConfirmProtected(ctx context.Context, id uuid.UUID, objectURI, objectVersion string) error {
	batch, err := m.store.GetArchiveBatch(ctx, id)
	if err != nil {
		return err
	}
	digest, _, err := fileSHA256(batch.ExportedPath)
	if err != nil {
		return err
	}
	if digest != batch.SHA256 {
		return fmt.Errorf("Nostr archive artifact digest mismatch: recorded=%s actual=%s", batch.SHA256, digest)
	}
	return m.store.MarkArchiveProtected(ctx, id, objectURI, objectVersion, digest)
}

// Restore validates the compressed artifact digest before inserting each row.
// Inserts are idempotent by Nostr event ID.
func (m *ArtifactManager) Restore(ctx context.Context, id uuid.UUID) (inserted int64, err error) {
	batch, err := m.store.GetArchiveBatch(ctx, id)
	if err != nil {
		return 0, err
	}
	if batch.Status == repository.NostrArchiveStatusClaimed {
		return 0, fmt.Errorf("Nostr archive batch %s has not been exported", id)
	}
	digest, _, err := fileSHA256(batch.ExportedPath)
	if err != nil {
		return 0, err
	}
	if digest != batch.SHA256 {
		return 0, fmt.Errorf("Nostr archive artifact digest mismatch: recorded=%s actual=%s", batch.SHA256, digest)
	}
	file, err := os.Open(batch.ExportedPath)
	if err != nil {
		return 0, fmt.Errorf("opening Nostr archive artifact: %w", err)
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
	if err != nil {
		return 0, fmt.Errorf("opening Nostr archive compressor: %w", err)
	}
	defer zr.Close()
	scanner := bufio.NewScanner(zr)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var rows int64
	for scanner.Scan() {
		rows++
		added, err := m.store.RestoreArchiveJSON(ctx, scanner.Text())
		if err != nil {
			return inserted, fmt.Errorf("restoring Nostr archive row %d: %w", rows, err)
		}
		if added {
			inserted++
		}
	}
	if err := scanner.Err(); err != nil {
		return inserted, fmt.Errorf("reading Nostr archive artifact: %w", err)
	}
	if rows != batch.RowCount {
		return inserted, fmt.Errorf("Nostr archive artifact row count mismatch: manifest=%d artifact=%d", batch.RowCount, rows)
	}
	return inserted, nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.count += int64(n)
	return n, err
}

func fileSHA256(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("opening Nostr archive artifact: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return "", n, fmt.Errorf("hashing Nostr archive artifact: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening Nostr archive directory for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("syncing Nostr archive directory: %w", err)
	}
	return nil
}
