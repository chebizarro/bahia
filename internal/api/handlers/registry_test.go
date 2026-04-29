package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type mockOCIReadRepo struct {
	manifestPutCalled bool
	manifestPutArg    domain.OCIManifest
	manifestPutTag    string
	blobsInRepo       map[string]bool
	manifest          *domain.OCIManifest
	blob              *domain.OCIBlob
	tags              []string
	referrers         []domain.OCIReferrerDescriptor

	mu    sync.Mutex
	blobs map[string]*domain.OCIBlob
}

func (m *mockOCIReadRepo) EnsureRepository(_ context.Context, _ string) (*domain.OCIRepository, error) {
	return &domain.OCIRepository{}, nil
}
func (m *mockOCIReadRepo) GetRepository(_ context.Context, _ string) (*domain.OCIRepository, error) {
	return nil, nil
}
func (m *mockOCIReadRepo) GetManifestByDigest(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return m.manifest, nil
}
func (m *mockOCIReadRepo) GetManifestByTag(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return m.manifest, nil
}
func (m *mockOCIReadRepo) PutManifest(_ context.Context, manifest domain.OCIManifest, tag string) error {
	m.manifestPutCalled = true
	m.manifestPutArg = manifest
	m.manifestPutTag = tag
	return nil
}
func (m *mockOCIReadRepo) GetBlob(_ context.Context, digest string) (*domain.OCIBlob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.blobs[digest]; ok {
		return b, nil
	}
	if m.blob != nil && digest == m.blob.Digest {
		return m.blob, nil
	}
	return nil, nil
}
func (m *mockOCIReadRepo) BlobExistsInRepo(_ context.Context, _, digest string) (bool, error) {
	return m.blobsInRepo[digest], nil
}
func (m *mockOCIReadRepo) FinalizeBlob(_ context.Context, _ domain.OCIBlobUpload) error {
	return nil
}
func (m *mockOCIReadRepo) LinkBlobToRepo(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockOCIReadRepo) UpsertBlob(_ context.Context, repoName, digest, mediaType, storageRef string, sizeBytes int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blobs == nil {
		m.blobs = map[string]*domain.OCIBlob{}
	}
	m.blobs[digest] = &domain.OCIBlob{Digest: digest, MediaType: mediaType, StorageRef: storageRef, SizeBytes: sizeBytes}
	return nil
}
func (m *mockOCIReadRepo) ListTags(_ context.Context, _, _ string, _ int) ([]string, error) {
	return m.tags, nil
}
func (m *mockOCIReadRepo) ListReferrers(_ context.Context, _, _, _ string) ([]domain.OCIReferrerDescriptor, error) {
	return m.referrers, nil
}
func (m *mockOCIReadRepo) GetManifest(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return m.manifest, nil
}

type mockUploadRepo struct {
	mu       sync.Mutex
	sessions map[string]uploadSession
}

type uploadSession struct {
	repoName string
	spool    string
	state    string
	offset   int64
	expires  time.Time
}

func (m *mockUploadRepo) Create(_ context.Context, uploadID, repoName, spoolPath string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = map[string]uploadSession{}
	}
	m.sessions[uploadID] = uploadSession{repoName: repoName, spool: spoolPath, state: string(domain.OCIBlobUploadStatePending), expires: expiresAt}
	return nil
}
func (m *mockUploadRepo) Get(_ context.Context, uploadID string) (string, string, string, int64, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[uploadID]
	if !ok {
		return "", "", "", 0, time.Time{}, nil
	}
	return s.repoName, s.spool, s.state, s.offset, s.expires, nil
}
func (m *mockUploadRepo) UpdateOffset(_ context.Context, uploadID string, offset int64, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[uploadID]
	s.offset = offset
	s.expires = expires
	s.state = string(domain.OCIBlobUploadStateUploading)
	m.sessions[uploadID] = s
	return nil
}
func (m *mockUploadRepo) UpdateState(_ context.Context, uploadID, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[uploadID]
	s.state = state
	m.sessions[uploadID] = s
	return nil
}
func (m *mockUploadRepo) Delete(_ context.Context, uploadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, uploadID)
	return nil
}

func newRegistryTestServer(t *testing.T, repo *mockOCIReadRepo, blobBody []byte) *httptest.Server {
	t.Helper()
	stored := map[string][]byte{"def": blobBody}
	var mu sync.Mutex
	blobSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash := strings.TrimPrefix(r.URL.Path, "/")
		if hash == "upload" && r.Method == http.MethodPut {
			hash = r.Header.Get("X-SHA-256")
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			stored[hash] = b
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"sha256": hash, "url": "/" + hash, "size": len(b)})
			return
		}
		mu.Lock()
		b, ok := stored[hash]
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(b)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(blobSrv.Close)

	// Preserve existing blob fields, just update StorageRef
	if repo.blob == nil {
		repo.blob = &domain.OCIBlob{}
	}
	repo.blob.StorageRef = blobSrv.URL + "/def"
	if repo.blob.Digest == "" {
		repo.blob.Digest = "sha256:def"
	}

	svc, err := service.NewOCIRegistryService(
		config.OCIServerConfig{SpoolDir: filepath.Join(t.TempDir(), "spool"), UploadExpiry: time.Hour},
		repo,
		&mockUploadRepo{},
		blossom.NewClient(blossom.Config{Servers: []string{blobSrv.URL}}, slog.Default()),
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("NewOCIRegistryService error: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}
	ociCfg := config.OCIServerConfig{
		AllowAnonymousPullCIDRs: []string{"192.168.40.0/24"},
		ServiceAccounts: []config.OCIServiceAccountConfig{{
			Username:     "svc",
			PasswordHash: string(hash),
			Permissions:  []string{"pull", "push"},
			RepoPrefixes: []string{"private/", "public/"},
		}},
	}
	return httptest.NewServer(NewOCIRegistryHandler(svc, nil, ociCfg))
}

func TestOCIRegistryHandler_PullRoutes(t *testing.T) {
	content := []byte(`{"schemaVersion":2}`)
	repo := &mockOCIReadRepo{
		manifest: &domain.OCIManifest{
			Digest:    "sha256:abc",
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Content:   content,
			SizeBytes: int64(len(content)),
		},
		blob: &domain.OCIBlob{
			Digest:     "sha256:def",
			MediaType:  "application/octet-stream",
			SizeBytes:  9,
			StorageRef: "placeholder", // Will be set by newRegistryTestServer
		},
		blobsInRepo: map[string]bool{"sha256:def": true},
		tags:        []string{"latest", "v1"},
		referrers:   []domain.OCIReferrerDescriptor{{Digest: "sha256:ref"}},
	}

	srv := newRegistryTestServer(t, repo, []byte("blob-data"))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v2/")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v2/ failed: %v status=%d", err, resp.StatusCode)
	}
	if resp.Header.Get("Docker-Distribution-API-Version") != "registry/2.0" {
		t.Fatalf("missing API version header")
	}

	resp, err = http.Get(srv.URL + "/v2/public/org/repo/manifests/latest")
	if err != nil {
		t.Fatalf("GET manifest failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status=%d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/v2/public/org/repo/blobs/sha256:def")
	if err != nil {
		t.Fatalf("GET blob failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("blob status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "blob-data" {
		t.Fatalf("blob body mismatch: %q", string(body))
	}
}

func TestOCIRegistryHandler_PrivateRepoAuth(t *testing.T) {
	content := []byte(`{"schemaVersion":2}`)
	repo := &mockOCIReadRepo{
		manifest: &domain.OCIManifest{
			Digest:    "sha256:abc",
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Content:   content,
			SizeBytes: int64(len(content)),
		},
	}

	srv := newRegistryTestServer(t, repo, []byte("blob-data"))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v2/private/team/repo/manifests/latest")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v2/private/team/repo/manifests/latest", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("svc:secret")))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("basic auth GET failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestOCIRegistryHandler_BlobUploadChunkedAndMonolithicAndMount(t *testing.T) {
	repo := &mockOCIReadRepo{
		blob: &domain.OCIBlob{
			Digest:    "sha256:def",
			MediaType: "application/octet-stream",
			SizeBytes: 9,
		},
		blobsInRepo: map[string]bool{"sha256:def": true},
	}
	srv := newRegistryTestServer(t, repo, []byte("blob-data"))
	defer srv.Close()

	client := http.DefaultClient
	authz := "Basic " + base64.StdEncoding.EncodeToString([]byte("svc:secret"))

	// Start upload
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v2/private/team/repo/blobs/uploads/", nil)
	req.Header.Set("Authorization", authz)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start upload failed: err=%v status=%d", err, resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	uuid := resp.Header.Get("Docker-Upload-UUID")
	if location == "" || uuid == "" {
		t.Fatalf("missing upload headers")
	}

	// Append chunk
	req, _ = http.NewRequest(http.MethodPatch, srv.URL+location, strings.NewReader("hello"))
	req.Header.Set("Authorization", authz)
	req.Header.Set("Content-Range", "bytes 0-4")
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("patch upload failed: err=%v status=%d", err, resp.StatusCode)
	}

	// Finalize
	req, _ = http.NewRequest(http.MethodPut, srv.URL+"/v2/private/team/repo/blobs/uploads/"+uuid+"?digest=sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", nil)
	req.Header.Set("Authorization", authz)
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("finalize failed: err=%v status=%d", err, resp.StatusCode)
	}

	// Monolithic fast-path
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v2/private/team/repo/blobs/uploads/?digest=sha256:486ea46224d1bb4fb680f34f7c9ad96a8f24ec88be73ea8e5a6c65260e9cb8a7", strings.NewReader("world"))
	req.Header.Set("Authorization", authz)
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("monolithic failed: err=%v status=%d", err, resp.StatusCode)
	}

	// Cross-repo mount
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v2/private/target/repo/blobs/uploads/?mount=sha256:def&from=public/org/repo", nil)
	req.Header.Set("Authorization", authz)
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("mount failed: err=%v status=%d", err, resp.StatusCode)
	}
}

func TestOCIRegistryHandler_BlobUploadPushAuthRequired(t *testing.T) {
	repo := &mockOCIReadRepo{}
	srv := newRegistryTestServer(t, repo, []byte("blob-data"))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v2/private/team/repo/blobs/uploads/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestOCIRegistryHandler_ManifestPush(t *testing.T) {
	repo := &mockOCIReadRepo{blobsInRepo: map[string]bool{"sha256:cfg": true, "sha256:layer": true}}
	srv := newRegistryTestServer(t, repo, []byte("blob-data"))
	defer srv.Close()

	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:cfg","size":1},"layers":[{"digest":"sha256:layer","size":1}]}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v2/private/team/repo/manifests/latest", strings.NewReader(manifest))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("svc:secret")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("manifest push failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	sum := sha256.Sum256([]byte(manifest))
	expectedDigest := "sha256:" + hex.EncodeToString(sum[:])
	if resp.Header.Get("Docker-Content-Digest") != expectedDigest {
		t.Fatalf("unexpected digest: %s", resp.Header.Get("Docker-Content-Digest"))
	}
	if !repo.manifestPutCalled {
		t.Fatalf("expected manifest write")
	}
}
