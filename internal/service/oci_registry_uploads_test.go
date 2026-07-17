package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type uploadRow struct {
	repoName  string
	spoolPath string
	state     string
	offset    int64
	expiresAt time.Time
}

type uploadRepoMock struct {
	mu      sync.Mutex
	uploads map[string]*uploadRow
}

func newUploadRepoMock() *uploadRepoMock { return &uploadRepoMock{uploads: map[string]*uploadRow{}} }

func (m *uploadRepoMock) Create(_ context.Context, uploadID, repoName, spoolPath string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uploads[uploadID] = &uploadRow{repoName: repoName, spoolPath: spoolPath, state: string(domain.OCIBlobUploadStatePending), expiresAt: expiresAt}
	return nil
}
func (m *uploadRepoMock) Get(_ context.Context, uploadID string) (string, string, string, int64, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.uploads[uploadID]
	if u == nil {
		return "", "", "", 0, time.Time{}, nil
	}
	return u.repoName, u.spoolPath, u.state, u.offset, u.expiresAt, nil
}
func (m *uploadRepoMock) UpdateOffset(_ context.Context, uploadID string, offset int64, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.uploads[uploadID]
	if u == nil {
		return errors.New("missing")
	}
	u.offset = offset
	u.expiresAt = expiresAt
	return nil
}
func (m *uploadRepoMock) UpdateState(_ context.Context, uploadID, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.uploads[uploadID]
	if u == nil {
		return errors.New("missing")
	}
	u.state = state
	return nil
}
func (m *uploadRepoMock) Delete(_ context.Context, uploadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uploads, uploadID)
	return nil
}
func (m *uploadRepoMock) ListExpiredUploads(_ context.Context, olderThan time.Time) ([]domain.OCIBlobUpload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.OCIBlobUpload{}
	for id, u := range m.uploads {
		if u.expiresAt.Before(olderThan) {
			out = append(out, domain.OCIBlobUpload{UploadID: id, SpoolPath: u.spoolPath})
		}
	}
	return out, nil
}

type blobRepoMock struct{ upserts int }

func (m *blobRepoMock) EnsureRepository(_ context.Context, _ string) (*domain.OCIRepository, error) {
	return &domain.OCIRepository{}, nil
}
func (m *blobRepoMock) GetRepository(_ context.Context, _ string) (*domain.OCIRepository, error) {
	return nil, nil
}
func (m *blobRepoMock) GetManifestByDigest(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return nil, nil
}
func (m *blobRepoMock) GetManifestByTag(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return nil, nil
}
func (m *blobRepoMock) PutManifest(_ context.Context, _ domain.OCIManifest, _ string) error {
	return nil
}
func (m *blobRepoMock) GetBlob(_ context.Context, _ string) (*domain.OCIBlob, error) {
	return nil, nil
}
func (m *blobRepoMock) BlobExistsInRepo(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
func (m *blobRepoMock) FinalizeBlob(_ context.Context, _ domain.OCIBlobUpload) error {
	return nil
}
func (m *blobRepoMock) LinkBlobToRepo(_ context.Context, _, _ string) error {
	return nil
}
func (m *blobRepoMock) UpsertBlob(_ context.Context, _, _, _, _ string, _ int64) error {
	m.upserts++
	return nil
}
func (m *blobRepoMock) ListTags(_ context.Context, _, _ string, _ int) ([]string, error) {
	return nil, nil
}
func (m *blobRepoMock) ListReferrers(_ context.Context, _, _, _ string) ([]domain.OCIReferrerDescriptor, error) {
	return nil, nil
}
func (m *blobRepoMock) GetManifest(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return nil, nil
}

func newUploadService(t *testing.T) (*OCIRegistryService, *uploadRepoMock, *blobRepoMock) {
	t.Helper()
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"` + serverURL + `/ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","size":3}`))
	}))
	serverURL = ts.URL
	t.Cleanup(ts.Close)

	uploads := newUploadRepoMock()
	repo := &blobRepoMock{}
	svc, err := NewOCIRegistryService(
		config.OCIServerConfig{SpoolDir: filepath.Join(t.TempDir(), "spool"), UploadExpiry: time.Hour},
		repo,
		uploads,
		blossom.NewClient(blossom.Config{Servers: []string{ts.URL}}, slog.Default()),
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, uploads, repo
}

func TestOCIUploadStateMachine_AppendAndFinalize(t *testing.T) {
	svc, uploads, repo := newUploadService(t)
	u, err := svc.BeginUpload(context.Background(), "repo/a")
	if err != nil {
		t.Fatal(err)
	}

	u, err = svc.AppendUpload(context.Background(), u.UploadID, bytes.NewBufferString("abc"), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.OffsetBytes != 3 {
		t.Fatalf("offset=%d", u.OffsetBytes)
	}

	blob, err := svc.FinalizeUpload(context.Background(), u.UploadID, bytes.NewBuffer(nil), 0, "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if err != nil {
		t.Fatal(err)
	}
	if blob.Digest == "" || repo.upserts != 1 {
		t.Fatalf("unexpected finalize result")
	}
	if len(uploads.uploads) != 0 {
		t.Fatalf("upload session should be deleted")
	}
	if _, err := os.Stat(u.SpoolPath); !os.IsNotExist(err) {
		t.Fatalf("spool should be removed")
	}
}

func TestOCIUploadStateMachine_OffsetMismatch(t *testing.T) {
	svc, _, _ := newUploadService(t)
	u, _ := svc.BeginUpload(context.Background(), "repo/a")
	off := int64(10)
	_, err := svc.AppendUpload(context.Background(), u.UploadID, bytes.NewBufferString("abc"), 3, &off)
	if !errors.Is(err, ErrUploadOffsetMismatch) {
		t.Fatalf("got %v", err)
	}
}

func TestOCIUploadStateMachine_CleanupExpired(t *testing.T) {
	svc, uploads, _ := newUploadService(t)
	u, _ := svc.BeginUpload(context.Background(), "repo/a")
	uploads.mu.Lock()
	uploads.uploads[u.UploadID].expiresAt = time.Now().Add(-time.Minute)
	uploads.mu.Unlock()
	if err := os.WriteFile(u.SpoolPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := svc.CleanupExpiredUploads(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("cleaned=%d", n)
	}
}
