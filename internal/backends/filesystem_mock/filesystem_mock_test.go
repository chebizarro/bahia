package filesystem_mock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestFilesystemMockRepositoryArtifactLifecycle(t *testing.T) {
	ctx := context.Background()
	backend, err := New(Config{RootDir: t.TempDir(), PublicBaseURL: "https://packages.local"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	repo := testRepo("dev")
	obs, err := backend.EnsureRepository(ctx, repo)
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if !obs.Exists || obs.PublicURL != "https://packages.local/dev" {
		t.Fatalf("unexpected repo observation: %#v", obs)
	}
	data := []byte("hello package")
	digest := sha(data)
	stored, err := backend.StoreArtifact(ctx, repo, packagebackend.StoreArtifactRequest{
		Namespace: "team", PackageName: "pkg", Version: "1.0.0", Filename: "pkg.tgz",
		SHA256: digest, SizeBytes: int64(len(data)), ContentType: "application/octet-stream", Reader: bytes.NewReader(data),
	})
	if err != nil {
		t.Fatalf("StoreArtifact: %v", err)
	}
	if stored.BackendPath != "team/pkg/1.0.0/pkg.tgz" || !stored.Exists || stored.SHA256 != digest {
		t.Fatalf("unexpected artifact observation: %#v", stored)
	}
	artifact := domain.PackageArtifact{Namespace: "team", PackageName: "pkg", Version: "1.0.0", Filename: "pkg.tgz", SHA256: digest, BackendPath: stored.BackendPath}
	stream, err := backend.GetArtifact(ctx, repo, artifact)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	got, _ := io.ReadAll(stream.ReadCloser)
	_ = stream.ReadCloser.Close()
	if string(got) != string(data) {
		t.Fatalf("unexpected artifact bytes: %q", got)
	}
	listed, err := backend.ListArtifacts(ctx, repo)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(listed) != 1 || listed[0].BackendPath != stored.BackendPath {
		t.Fatalf("unexpected list: %#v", listed)
	}
	if _, err := backend.DeleteRepository(ctx, repo, false); err == nil {
		t.Fatal("expected non-force delete to reject non-empty repo")
	}
	targetRepo := testRepo("prod")
	if _, err := backend.EnsureRepository(ctx, targetRepo); err != nil {
		t.Fatalf("EnsureRepository target: %v", err)
	}
	promoted, err := backend.PromoteArtifact(ctx, repo, targetRepo, artifact, packagebackend.PromoteArtifactRequest{Channel: "stable"})
	if err != nil {
		t.Fatalf("PromoteArtifact: %v", err)
	}
	if promoted.BackendPath != stored.BackendPath || !promoted.Exists {
		t.Fatalf("unexpected promoted observation: %#v", promoted)
	}
	yanked, err := backend.YankArtifact(ctx, repo, artifact, "bad release")
	if err != nil {
		t.Fatalf("YankArtifact: %v", err)
	}
	if !yanked.Yanked || yanked.Exists {
		t.Fatalf("unexpected yank observation: %#v", yanked)
	}
	yankedAgain, err := backend.YankArtifact(ctx, repo, artifact, "already gone")
	if err != nil {
		t.Fatalf("YankArtifact idempotent: %v", err)
	}
	if !yankedAgain.Yanked {
		t.Fatalf("expected idempotent yank marker: %#v", yankedAgain)
	}
	if _, err := backend.DeleteRepository(ctx, repo, true); err != nil {
		t.Fatalf("force DeleteRepository: %v", err)
	}
}

func TestFilesystemMockRejectsDigestMismatch(t *testing.T) {
	backend, err := New(Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = backend.StoreArtifact(context.Background(), testRepo("repo"), packagebackend.StoreArtifactRequest{PackageName: "pkg", Version: "1", Filename: "pkg.bin", SHA256: sha([]byte("expected")), SizeBytes: 3, Reader: bytes.NewReader([]byte("bad"))})
	if err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func testRepo(name string) domain.PackageRepository {
	return domain.PackageRepository{ID: uuid.New(), Name: name, ExternalRepositoryName: name, Format: domain.PackageRepositoryFormatNPM, BackendRef: "mock", BackendType: domain.PackageBackendFilesystemMock}
}

func sha(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
