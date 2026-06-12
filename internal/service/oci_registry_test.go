package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type mockUploadSessionRepo struct{}

func (m *mockUploadSessionRepo) Create(_ context.Context, _, _, _ string, _ time.Time) error {
	return nil
}
func (m *mockUploadSessionRepo) Get(_ context.Context, _ string) (string, string, string, int64, time.Time, error) {
	return "", "", "", 0, time.Time{}, nil
}
func (m *mockUploadSessionRepo) UpdateOffset(_ context.Context, _ string, _ int64, _ time.Time) error {
	return nil
}
func (m *mockUploadSessionRepo) UpdateState(_ context.Context, _, _ string) error { return nil }
func (m *mockUploadSessionRepo) Delete(_ context.Context, _ string) error         { return nil }

type mockOCIRegistryRepo struct {
	manifestPutCalled bool
	manifestPutArg    domain.OCIManifest
	manifestPutTag    string
	blobsInRepo       map[string]bool
	manifest          *domain.OCIManifest
	manifestErr       error
	blob              *domain.OCIBlob
	blobErr           error
	tags              []string
	tagsErr           error
	referrers         []domain.OCIReferrerDescriptor
	refErr            error
}

func (m *mockOCIRegistryRepo) EnsureRepository(_ context.Context, _ string) (*domain.OCIRepository, error) {
	return &domain.OCIRepository{}, nil
}
func (m *mockOCIRegistryRepo) GetRepository(_ context.Context, _ string) (*domain.OCIRepository, error) {
	return nil, nil
}
func (m *mockOCIRegistryRepo) GetManifestByDigest(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return m.manifest, m.manifestErr
}
func (m *mockOCIRegistryRepo) GetManifestByTag(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return m.manifest, m.manifestErr
}
func (m *mockOCIRegistryRepo) PutManifest(_ context.Context, manifest domain.OCIManifest, tag string) error {
	m.manifestPutCalled = true
	m.manifestPutArg = manifest
	m.manifestPutTag = tag
	return nil
}
func (m *mockOCIRegistryRepo) GetBlob(_ context.Context, _ string) (*domain.OCIBlob, error) {
	return m.blob, m.blobErr
}
func (m *mockOCIRegistryRepo) BlobExistsInRepo(_ context.Context, _, digest string) (bool, error) {
	return m.blobsInRepo[digest], nil
}
func (m *mockOCIRegistryRepo) FinalizeBlob(_ context.Context, _ domain.OCIBlobUpload) error {
	return nil
}
func (m *mockOCIRegistryRepo) LinkBlobToRepo(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockOCIRegistryRepo) UpsertBlob(_ context.Context, _, _, _, _ string, _ int64) error {
	return nil
}
func (m *mockOCIRegistryRepo) ListTags(_ context.Context, _, _ string, _ int) ([]string, error) {
	return m.tags, m.tagsErr
}
func (m *mockOCIRegistryRepo) ListReferrers(_ context.Context, _, _, _ string) ([]domain.OCIReferrerDescriptor, error) {
	return m.referrers, m.refErr
}
func (m *mockOCIRegistryRepo) GetManifest(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return m.manifest, m.manifestErr
}

func newTestOCIService(t *testing.T, repo *mockOCIRegistryRepo, serverURL string) *OCIRegistryService {
	t.Helper()
	spool := filepath.Join(t.TempDir(), "spool")
	svc, err := NewOCIRegistryService(
		config.OCIServerConfig{SpoolDir: spool, UploadExpiry: time.Hour},
		repo,
		&mockUploadSessionRepo{},
		blossom.NewClient(blossom.Config{Servers: []string{serverURL}}, slog.Default()),
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("NewOCIRegistryService() error = %v", err)
	}
	return svc
}

func TestOCIRegistryService_CheckAPI(t *testing.T) {
	svc := &OCIRegistryService{}
	resp, err := svc.CheckAPI(context.Background())
	if err != nil {
		t.Fatalf("CheckAPI() error = %v", err)
	}
	if resp.DockerDistributionAPIVersion != "registry/2.0" {
		t.Fatalf("version = %q", resp.DockerDistributionAPIVersion)
	}
}

func TestOCIRegistryService_FetchManifest_AcceptValidation(t *testing.T) {
	repo := &mockOCIRegistryRepo{}
	repo.manifest = &domain.OCIManifest{
		Digest:    "sha256:abc",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Content:   []byte("{}"),
		SizeBytes: 2,
	}

	svc := newTestOCIService(t, repo, "http://example.invalid")
	ctx := WithRegistryPrincipal(context.Background(), &domain.RegistryPrincipal{Scopes: []string{"repository:private/repo:pull"}})

	if _, err := svc.FetchManifest(ctx, "private/repo", "latest", "application/json"); !errors.Is(err, ErrManifestNotAcceptable) {
		t.Fatalf("expected ErrManifestNotAcceptable, got %v", err)
	}

	resp, err := svc.FetchManifest(ctx, "private/repo", "latest", "application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		t.Fatalf("FetchManifest() error = %v", err)
	}
	if resp.DockerContentDigest != "sha256:abc" || resp.ContentLength != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestOCIRegistryService_Authorization(t *testing.T) {
	repo := &mockOCIRegistryRepo{}
	repo.manifest = &domain.OCIManifest{
		Digest:    "sha256:abc",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Content:   []byte("{}"),
		SizeBytes: 2,
	}
	svc := newTestOCIService(t, repo, "http://example.invalid")

	if _, err := svc.FetchManifest(context.Background(), "private/repo", "latest", ""); !errors.Is(err, ErrRegistryUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}

	if _, err := svc.FetchManifest(context.Background(), "public/repo", "latest", ""); err != nil {
		t.Fatalf("public repo should allow anonymous pull: %v", err)
	}
}

func TestOCIRegistryService_ProxyBlobGETAndHEAD(t *testing.T) {
	body := []byte("blob-data")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		case http.MethodHead:
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", "9")
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer ts.Close()

	repo := &mockOCIRegistryRepo{
		blobsInRepo: map[string]bool{"sha256:def": true},
		blob: &domain.OCIBlob{
			Digest:     "sha256:def",
			MediaType:  "application/octet-stream",
			SizeBytes:  int64(len(body)),
			StorageRef: ts.URL + "/sha256:def",
		},
	}
	svc := newTestOCIService(t, repo, ts.URL)
	ctx := WithRegistryPrincipal(context.Background(), &domain.RegistryPrincipal{Scopes: []string{"repository:private/repo:pull"}})

	getResp, err := svc.ProxyBlobGET(ctx, "private/repo", "sha256:def")
	if err != nil {
		t.Fatalf("ProxyBlobGET() error = %v", err)
	}
	defer getResp.Stream.Close()
	got, _ := io.ReadAll(getResp.Stream.Body)
	if string(got) != "blob-data" {
		t.Fatalf("blob body mismatch: %q", string(got))
	}

	headResp, err := svc.ProxyBlobHEAD(ctx, "private/repo", "sha256:def")
	if err != nil {
		t.Fatalf("ProxyBlobHEAD() error = %v", err)
	}
	if !headResp.Exists || headResp.DockerContentDigest != "sha256:def" {
		t.Fatalf("unexpected HEAD response: %+v", headResp)
	}
}

func TestOCIRegistryService_ListTagsAndReferrers(t *testing.T) {
	// Mock returns paginated result directly (b, z are tags after "a" sorted)
	repo := &mockOCIRegistryRepo{tags: []string{"b", "z"}, referrers: []domain.OCIReferrerDescriptor{{Digest: "sha256:r1"}}}
	svc := newTestOCIService(t, repo, "http://example.invalid")
	ctx := WithRegistryPrincipal(context.Background(), &domain.RegistryPrincipal{Scopes: []string{"repository:private/repo:pull"}})

	tagsResp, err := svc.ListTags(ctx, "private/repo", 2, "a")
	if err != nil {
		t.Fatalf("ListTags() error = %v", err)
	}
	if len(tagsResp.Tags) != 2 || tagsResp.Tags[0] != "b" || tagsResp.Tags[1] != "z" {
		t.Fatalf("unexpected tags: %+v", tagsResp.Tags)
	}

	refResp, err := svc.ListReferrers(ctx, "private/repo", "sha256:subject", "")
	if err != nil {
		t.Fatalf("ListReferrers() error = %v", err)
	}
	if len(refResp.Manifests) != 1 {
		t.Fatalf("unexpected referrers: %+v", refResp.Manifests)
	}
}

func TestOCIRegistryService_PutManifest(t *testing.T) {
	repo := &mockOCIRegistryRepo{blobsInRepo: map[string]bool{
		"sha256:cfg":    true,
		"sha256:layer1": true,
	}}
	svc := newTestOCIService(t, repo, "http://example.invalid")
	ctx := WithRegistryPrincipal(context.Background(), &domain.RegistryPrincipal{Scopes: []string{"repository:private/repo:push"}})
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:layer1","size":20}],"annotations":{"k":"v"},"artifactType":"application/test"}`)
	digest := "sha256:abcdef"
	stored, err := svc.PutManifest(ctx, "private/repo", "latest", "application/vnd.oci.image.manifest.v1+json", digest, manifest)
	if err != nil {
		t.Fatalf("PutManifest() error = %v", err)
	}
	if stored != digest || !repo.manifestPutCalled {
		t.Fatalf("manifest not stored as expected")
	}
	if repo.manifestPutTag != "latest" || repo.manifestPutArg.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Fatalf("unexpected put args: tag=%s mediaType=%s", repo.manifestPutTag, repo.manifestPutArg.MediaType)
	}
}

func TestOCIRegistryService_PutManifest_RejectsUnknownBlob(t *testing.T) {
	repo := &mockOCIRegistryRepo{blobsInRepo: map[string]bool{}}
	svc := newTestOCIService(t, repo, "http://example.invalid")
	ctx := WithRegistryPrincipal(context.Background(), &domain.RegistryPrincipal{Scopes: []string{"repository:private/repo:push"}})
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:missing","size":1},"layers":[]}`)
	_, err := svc.PutManifest(ctx, "private/repo", "latest", "application/vnd.oci.image.manifest.v1+json", "sha256:abcd", manifest)
	if !errors.Is(err, ErrBlobUnknown) {
		t.Fatalf("expected ErrBlobUnknown, got %v", err)
	}
}

func TestNewOCIRegistryService_CreatesSpoolDir(t *testing.T) {
	repo := &mockOCIRegistryRepo{}
	spool := filepath.Join(t.TempDir(), "new-spool")
	_, err := NewOCIRegistryService(
		config.OCIServerConfig{SpoolDir: spool, UploadExpiry: time.Hour},
		repo,
		&mockUploadSessionRepo{},
		blossom.NewClient(blossom.Config{Servers: []string{"http://example.invalid"}}, slog.Default()),
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("NewOCIRegistryService() error = %v", err)
	}
	if _, err := os.Stat(spool); err != nil {
		t.Fatalf("spool dir not created: %v", err)
	}
}
