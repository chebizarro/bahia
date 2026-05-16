package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/backends/filesystem_mock"
	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestPackageRegistryServiceEnsureRepositoryValidatesBackendAndImmutability(t *testing.T) {
	svc := newTestPackageService(t)
	repo := testServiceRepo("dev")
	created, err := svc.EnsureRepository(context.Background(), &repo, nil)
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if created.Status != domain.PackageRepositoryStatusReady || created.PublicURL == "" || created.BackendType != domain.PackageBackendFilesystemMock {
		t.Fatalf("unexpected repository: %#v", created)
	}
	updated := *created
	updated.ExternalRepositoryName = "other"
	if err := svc.ValidateRepositorySpec(&updated, created); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected immutability error, got %v", err)
	}
	badFormat := *created
	badFormat.Format = domain.PackageRepositoryFormat("generic")
	if err := svc.ValidateRepositorySpec(&badFormat, nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported format error, got %v", err)
	}
}

func TestPackageRegistryServicePublishEnforcesPolicyAndVerifiesSource(t *testing.T) {
	svc := newTestPackageService(t)
	repo := testServiceRepo("dev")
	repo.Policy = domain.PackageRepositoryPolicy{RequireSHA256: true, AllowOverwrite: false, MaxFileSizeBytes: 128, AllowedMediaTypes: []string{"application/octet-stream"}, AllowedPackageNamePrefixes: []string{"@scope/", "pkg"}}
	created, err := svc.EnsureRepository(context.Background(), &repo, nil)
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	sourceURL, digest, size := writeSource(t, []byte("package bytes"))
	artifact, err := svc.PublishPackage(context.Background(), created, nil, PackagePublishRequest{PackageName: "pkg-one", Version: "1.0.0", Filename: "pkg.tgz", SourceURL: sourceURL, SHA256: digest, SizeBytes: size, ContentType: "application/octet-stream"})
	if err != nil {
		t.Fatalf("PublishPackage: %v", err)
	}
	if artifact.Status != domain.PackageArtifactStatusAvailable || artifact.SHA256 != digest || artifact.BackendPath != "pkg-one/1.0.0/pkg.tgz" {
		t.Fatalf("unexpected artifact: %#v", artifact)
	}
	idempotent, err := svc.PublishPackage(context.Background(), created, artifact, PackagePublishRequest{PackageName: "pkg-one", Version: "1.0.0", Filename: "pkg.tgz", SourceURL: sourceURL, SHA256: digest, SizeBytes: size, ContentType: "application/octet-stream"})
	if err != nil {
		t.Fatalf("idempotent PublishPackage: %v", err)
	}
	if idempotent.ID != artifact.ID {
		t.Fatalf("expected existing artifact on idempotent publish")
	}
	otherURL, otherDigest, otherSize := writeSource(t, []byte("different"))
	if _, err := svc.PublishPackage(context.Background(), created, artifact, PackagePublishRequest{PackageName: "pkg-one", Version: "1.0.0", Filename: "pkg.tgz", SourceURL: otherURL, SHA256: otherDigest, SizeBytes: otherSize, ContentType: "application/octet-stream"}); err == nil || !strings.Contains(err.Error(), "overwrites are disabled") {
		t.Fatalf("expected overwrite policy denial, got %v", err)
	}
	if _, err := svc.PublishPackage(context.Background(), created, nil, PackagePublishRequest{PackageName: "pkg-bad", Version: "1", Filename: "bad.tgz", SourceURL: sourceURL, SHA256: strings.Repeat("0", 64), SizeBytes: size, ContentType: "application/octet-stream"}); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected source digest mismatch, got %v", err)
	}
	oversizeURL, oversizeDigest, _ := writeSource(t, []byte("actual bytes longer than declared"))
	if _, err := svc.PublishPackage(context.Background(), created, nil, PackagePublishRequest{PackageName: "pkg-large", Version: "1", Filename: "large.tgz", SourceURL: oversizeURL, SHA256: oversizeDigest, SizeBytes: 4, ContentType: "application/octet-stream"}); err == nil || !strings.Contains(err.Error(), "exceeds declared") {
		t.Fatalf("expected declared size overflow, got %v", err)
	}
	if _, err := svc.PublishPackage(context.Background(), created, nil, PackagePublishRequest{PackageName: "pkg-two", Version: "1", Filename: "pkg.tgz", SourceURL: sourceURL, SHA256: digest, SizeBytes: size, ContentType: "text/plain"}); err == nil || !strings.Contains(err.Error(), "media type policy") {
		t.Fatalf("expected media type policy error, got %v", err)
	}
}

func TestPackageRegistryServiceGeneratesNPMIndexAndRemovesYankedVersion(t *testing.T) {
	svc := newTestPackageService(t)
	repo := testServiceRepo("npm-dev")
	created, err := svc.EnsureRepository(context.Background(), &repo, nil)
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	sourceURL, digest, size := writeSource(t, []byte("npm package"))
	artifact, err := svc.PublishPackage(context.Background(), created, nil, PackagePublishRequest{PackageName: "pkg", Version: "1.0.0", Filename: "pkg-1.0.0.tgz", SourceURL: sourceURL, SHA256: digest, SizeBytes: size})
	if err != nil {
		t.Fatalf("PublishPackage: %v", err)
	}
	idx := readIndex(t, svc, created, "pkg")
	var payload struct {
		Name     string `json:"name"`
		Versions map[string]struct {
			Dist struct {
				Tarball string `json:"tarball"`
				Shasum  string `json:"shasum"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.Unmarshal([]byte(idx), &payload); err != nil {
		t.Fatalf("decode npm index: %v\n%s", err, idx)
	}
	version, ok := payload.Versions["1.0.0"]
	if payload.Name != "pkg" || !ok || !strings.Contains(version.Dist.Tarball, "/pkg/1.0.0/pkg-1.0.0.tgz") || version.Dist.Shasum != digest {
		t.Fatalf("unexpected npm index: %#v", payload)
	}
	if _, err := svc.YankPackage(context.Background(), created, artifact, PackageYankRequest{PackageName: "pkg", Version: "1.0.0", Filename: "pkg-1.0.0.tgz", Reason: "bad"}); err != nil {
		t.Fatalf("YankPackage: %v", err)
	}
	idx = readIndex(t, svc, created, "pkg")
	if strings.Contains(idx, "1.0.0") {
		t.Fatalf("expected yanked version removed from npm index: %s", idx)
	}
}

func TestPackageRegistryServiceGeneratesPyPISimpleIndex(t *testing.T) {
	svc := newTestPackageService(t)
	repo := testServiceRepo("pypi-dev")
	repo.Format = domain.PackageRepositoryFormatPyPI
	created, err := svc.EnsureRepository(context.Background(), &repo, nil)
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	sourceURL, digest, size := writeSource(t, []byte("pypi package"))
	if _, err := svc.PublishPackage(context.Background(), created, nil, PackagePublishRequest{PackageName: "Demo_Pkg", Version: "1.0.0", Filename: "demo_pkg-1.0.0.tar.gz", SourceURL: sourceURL, SHA256: digest, SizeBytes: size}); err != nil {
		t.Fatalf("PublishPackage: %v", err)
	}
	root := readIndex(t, svc, created, "/simple/")
	if !strings.Contains(root, `<a href="demo-pkg/">demo-pkg</a>`) {
		t.Fatalf("unexpected pypi root index: %s", root)
	}
	pkg := readIndex(t, svc, created, "/simple/demo-pkg/")
	if !strings.Contains(pkg, "demo_pkg-1.0.0.tar.gz") || !strings.Contains(pkg, "#sha256="+digest) {
		t.Fatalf("unexpected pypi package index: %s", pkg)
	}
}

func TestPackageRegistryServicePromotionYankAndDrift(t *testing.T) {
	svc := newTestPackageService(t)
	dev := testServiceRepo("dev")
	prod := testServiceRepo("prod")
	prod.Policy.PromotionRequiresApproval = true
	devRepo, err := svc.EnsureRepository(context.Background(), &dev, nil)
	if err != nil {
		t.Fatalf("EnsureRepository dev: %v", err)
	}
	prodRepo, err := svc.EnsureRepository(context.Background(), &prod, nil)
	if err != nil {
		t.Fatalf("EnsureRepository prod: %v", err)
	}
	sourceURL, digest, size := writeSource(t, []byte("release"))
	artifact, err := svc.PublishPackage(context.Background(), devRepo, nil, PackagePublishRequest{PackageName: "pkg", Version: "1.0.0", Filename: "pkg.tgz", SourceURL: sourceURL, SHA256: digest, SizeBytes: size})
	if err != nil {
		t.Fatalf("PublishPackage: %v", err)
	}
	if _, _, err := svc.PromotePackage(context.Background(), devRepo, prodRepo, artifact, nil, PackagePromotionRequest{Channel: "stable"}); err == nil || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("expected approval error, got %v", err)
	}
	promoted, publication, err := svc.PromotePackage(context.Background(), devRepo, prodRepo, artifact, nil, PackagePromotionRequest{Channel: "stable", ApprovedBy: "operator"})
	if err != nil {
		t.Fatalf("PromotePackage: %v", err)
	}
	if promoted.RepositoryID != prodRepo.ID || publication.Status != domain.PackagePublicationStatusPromoted {
		t.Fatalf("unexpected promotion artifact=%#v publication=%#v", promoted, publication)
	}
	drift, err := svc.ObserveArtifactDrift(context.Background(), prodRepo, promoted)
	if err != nil {
		t.Fatalf("ObserveArtifactDrift: %v", err)
	}
	if drift.Drifted {
		t.Fatalf("expected no drift: %#v", drift)
	}
	yanked, err := svc.YankPackage(context.Background(), prodRepo, promoted, PackageYankRequest{PackageName: promoted.PackageName, Version: promoted.Version, Filename: promoted.Filename, Reason: "bad release"})
	if err != nil {
		t.Fatalf("YankPackage: %v", err)
	}
	if !yanked.Deleted || yanked.Status != domain.PackageArtifactStatusDeleted {
		t.Fatalf("unexpected yanked artifact: %#v", yanked)
	}
}

func readIndex(t *testing.T, svc *PackageRegistryService, repo *domain.PackageRepository, path string) string {
	t.Helper()
	backend, err := svc.backend(repo.BackendRef)
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	generator, ok := backend.(packagebackend.IndexGenerator)
	if !ok {
		t.Fatalf("backend does not implement IndexGenerator")
	}
	reader, _, err := generator.ServeIndex(context.Background(), repo.ExternalRepositoryName, path)
	if err != nil {
		t.Fatalf("ServeIndex(%q): %v", path, err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	return string(data)
}

func newTestPackageService(t *testing.T) *PackageRegistryService {
	t.Helper()
	backend, err := filesystem_mock.New(filesystem_mock.Config{RootDir: t.TempDir(), PublicBaseURL: "https://packages.local"})
	if err != nil {
		t.Fatalf("filesystem mock: %v", err)
	}
	svc, err := NewPackageRegistryService(config.PackageControlplaneConfig{AllowFileSource: true}, packagebackend.Registry{"mock": backend}, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewPackageRegistryService: %v", err)
	}
	return svc
}

func testServiceRepo(name string) domain.PackageRepository {
	return domain.PackageRepository{ID: uuid.New(), Name: name, ExternalRepositoryName: name, Format: domain.PackageRepositoryFormatNPM, BackendRef: "mock", BackendType: domain.PackageBackendFilesystemMock, Status: domain.PackageRepositoryStatusReady}
}

func writeSource(t *testing.T, data []byte) (string, string, int64) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "pkg-*")
	if err != nil {
		t.Fatalf("temp source: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}
	h := sha256.Sum256(data)
	return (&url.URL{Scheme: "file", Path: f.Name()}).String(), hex.EncodeToString(h[:]), int64(len(data))
}
