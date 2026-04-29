package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

var (
	ErrUploadNotFound       = errors.New("upload session not found")
	ErrUploadInvalidState   = errors.New("upload session invalid state")
	ErrUploadExpired        = errors.New("upload session expired")
	ErrUploadOffsetMismatch = errors.New("upload offset mismatch")
	ErrUploadDigestMismatch = errors.New("upload digest mismatch")
	ErrUploadLengthRequired = errors.New("upload content-length required")
)

func (s *OCIRegistryService) uploadMutex(uploadID string) *sync.Mutex {
	mu, _ := s.uploadMu.LoadOrStore(uploadID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func (s *OCIRegistryService) MountBlob(ctx context.Context, targetRepo, fromRepo, digest string) (*domain.OCIBlob, bool, error) {
	digest = strings.TrimSpace(digest)
	if digest == "" || strings.TrimSpace(targetRepo) == "" || strings.TrimSpace(fromRepo) == "" {
		return nil, false, nil
	}
	// Check if blob exists in source repo
	exists, err := s.repo.BlobExistsInRepo(ctx, fromRepo, digest)
	if err != nil {
		return nil, false, fmt.Errorf("check source blob: %w", err)
	}
	if !exists {
		return nil, false, nil
	}
	// Get the blob metadata
	blob, err := s.repo.GetBlob(ctx, digest)
	if err != nil {
		return nil, false, fmt.Errorf("get blob metadata: %w", err)
	}
	if blob == nil || strings.TrimSpace(blob.StorageRef) == "" {
		return nil, false, nil
	}
	// Link blob to target repo
	if err := s.repo.LinkBlobToRepo(ctx, targetRepo, digest); err != nil {
		return nil, false, fmt.Errorf("mount blob in target repo: %w", err)
	}
	return blob, true, nil
}

func (s *OCIRegistryService) BeginUpload(ctx context.Context, repoName string) (*domain.OCIBlobUpload, error) {
	uploadID := uuid.NewString()
	spoolPath := filepath.Join(s.cfg.SpoolDir, uploadID+".part")
	f, err := os.OpenFile(spoolPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create spool file: %w", err)
	}
	_ = f.Close()

	now := time.Now().UTC()
	expiresAt := now.Add(s.cfg.UploadExpiry)
	if err := s.uploads.Create(ctx, uploadID, repoName, spoolPath, expiresAt); err != nil {
		_ = os.Remove(spoolPath)
		return nil, fmt.Errorf("create upload session: %w", err)
	}

	return &domain.OCIBlobUpload{
		UploadID:    uploadID,
		SpoolPath:   spoolPath,
		State:       domain.OCIBlobUploadStatePending,
		OffsetBytes: 0,
		StartedAt:   now,
		UpdatedAt:   now,
		ExpiresAt:   expiresAt,
	}, nil
}

func (s *OCIRegistryService) GetUpload(ctx context.Context, uploadID string) (*domain.OCIBlobUpload, error) {
	repoName, spoolPath, state, offset, expiresAt, err := s.uploads.Get(ctx, uploadID)
	if err != nil {
		return nil, fmt.Errorf("get upload session: %w", err)
	}
	if strings.TrimSpace(spoolPath) == "" {
		return nil, ErrUploadNotFound
	}
	return &domain.OCIBlobUpload{
		UploadID:     uploadID,
		RepositoryID: repoName,
		SpoolPath:    spoolPath,
		State:        domain.OCIBlobUploadState(state),
		OffsetBytes:  offset,
		ExpiresAt:    expiresAt,
	}, nil
}

func (s *OCIRegistryService) AppendUpload(ctx context.Context, uploadID string, body io.Reader, contentLength int64, startOffset *int64) (*domain.OCIBlobUpload, error) {
	if contentLength < 0 {
		return nil, ErrUploadLengthRequired
	}
	mu := s.uploadMutex(uploadID)
	mu.Lock()
	defer mu.Unlock()

	upload, err := s.GetUpload(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	if upload.State == domain.OCIBlobUploadStateFailed || upload.State == domain.OCIBlobUploadStateExpired || upload.State == domain.OCIBlobUploadStateCompleted {
		return nil, ErrUploadInvalidState
	}
	if !upload.ExpiresAt.IsZero() && time.Now().After(upload.ExpiresAt) {
		_ = s.uploads.UpdateState(ctx, uploadID, string(domain.OCIBlobUploadStateExpired))
		return nil, ErrUploadExpired
	}
	if startOffset != nil && *startOffset != upload.OffsetBytes {
		return nil, ErrUploadOffsetMismatch
	}

	if err := s.appendUploadLocked(ctx, upload, contentLength, body); err != nil {
		return nil, err
	}
	upload.State = domain.OCIBlobUploadStateUploading
	return upload, nil
}

func (s *OCIRegistryService) FinalizeUpload(ctx context.Context, uploadID string, body io.Reader, contentLength int64, expectedDigest string) (*domain.OCIBlob, error) {
	mu := s.uploadMutex(uploadID)
	mu.Lock()
	defer mu.Unlock()

	upload, err := s.GetUpload(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	if upload.State == domain.OCIBlobUploadStateFailed || upload.State == domain.OCIBlobUploadStateExpired || upload.State == domain.OCIBlobUploadStateCompleted {
		return nil, ErrUploadInvalidState
	}
	if !upload.ExpiresAt.IsZero() && time.Now().After(upload.ExpiresAt) {
		_ = s.uploads.UpdateState(ctx, uploadID, string(domain.OCIBlobUploadStateExpired))
		return nil, ErrUploadExpired
	}

	if contentLength > 0 {
		if err := s.appendUploadLocked(ctx, upload, contentLength, body); err != nil {
			return nil, err
		}
	}

	digest, size, err := sha256File(upload.SpoolPath)
	if err != nil {
		return nil, fmt.Errorf("digest spool file: %w", err)
	}
	fullDigest := "sha256:" + digest
	if expectedDigest != "" && !strings.EqualFold(expectedDigest, fullDigest) {
		return nil, ErrUploadDigestMismatch
	}

	bd, err := s.blossom.UploadFile(ctx, upload.SpoolPath, "application/octet-stream", digest)
	if err != nil {
		_ = s.uploads.UpdateState(ctx, uploadID, string(domain.OCIBlobUploadStateFailed))
		return nil, fmt.Errorf("upload blob to blossom: %w", err)
	}

	if err := s.repo.UpsertBlob(ctx, upload.RepositoryID, fullDigest, "application/octet-stream", bd.URL, size); err != nil {
		return nil, fmt.Errorf("upsert blob metadata: %w", err)
	}
	if err := s.uploads.UpdateState(ctx, uploadID, string(domain.OCIBlobUploadStateCompleted)); err != nil {
		return nil, fmt.Errorf("mark upload completed: %w", err)
	}
	if err := s.uploads.Delete(ctx, uploadID); err != nil {
		return nil, fmt.Errorf("delete upload session: %w", err)
	}
	_ = os.Remove(upload.SpoolPath)

	return &domain.OCIBlob{Digest: fullDigest, SizeBytes: size, StorageRef: bd.URL}, nil
}

func (s *OCIRegistryService) CleanupExpiredUploads(ctx context.Context, now time.Time) (int, error) {
	type expiredLister interface {
		ListExpiredUploads(ctx context.Context, olderThan time.Time) ([]domain.OCIBlobUpload, error)
	}
	lister, ok := s.uploads.(expiredLister)
	if !ok {
		return 0, nil
	}
	expired, err := lister.ListExpiredUploads(ctx, now)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, up := range expired {
		_ = s.uploads.UpdateState(ctx, up.UploadID, string(domain.OCIBlobUploadStateExpired))
		_ = os.Remove(up.SpoolPath)
		if err := s.uploads.Delete(ctx, up.UploadID); err != nil {
			continue
		}
		count++
	}
	return count, nil
}

func (s *OCIRegistryService) appendUploadLocked(ctx context.Context, upload *domain.OCIBlobUpload, contentLength int64, body io.Reader) error {
	f, err := os.OpenFile(upload.SpoolPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open spool file: %w", err)
	}
	defer f.Close()

	written, err := io.CopyN(f, body, contentLength)
	if err != nil {
		_ = f.Truncate(upload.OffsetBytes)
		return fmt.Errorf("append chunk: %w", err)
	}
	if written != contentLength {
		_ = f.Truncate(upload.OffsetBytes)
		return io.ErrUnexpectedEOF
	}

	newOffset := upload.OffsetBytes + written
	expiresAt := time.Now().Add(s.cfg.UploadExpiry)
	if err := s.uploads.UpdateOffset(ctx, upload.UploadID, newOffset, expiresAt); err != nil {
		_ = f.Truncate(upload.OffsetBytes)
		return fmt.Errorf("update upload offset: %w", err)
	}
	upload.OffsetBytes = newOffset
	upload.ExpiresAt = expiresAt
	return nil
}

func sha256File(path string) (hexDigest string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func ParseUploadOffset(rangeHeader string) (*int64, error) {
	rangeHeader = strings.TrimSpace(rangeHeader)
	if rangeHeader == "" {
		return nil, nil
	}
	parts := strings.Split(rangeHeader, "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid upload range")
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid upload range")
	}
	return &start, nil
}
