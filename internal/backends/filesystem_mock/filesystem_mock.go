package filesystem_mock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ErrProductionSelection is returned when the production backend factory is asked to construct filesystem_mock.
// Tests may still construct this backend directly with New.
var ErrProductionSelection = errors.New("filesystem_mock is test/development-only and cannot be selected by the production package backend factory")

// Config configures the deterministic local filesystem package backend.
type Config struct {
	RootDir       string
	PublicBaseURL string
}

// Backend stores package artifacts under a local root. It is intended for tests,
// development, and deterministic control-plane integration checks only.
type Backend struct {
	rootDir       string
	publicBaseURL string
}

func New(cfg Config) (*Backend, error) {
	root := strings.TrimSpace(cfg.RootDir)
	if root == "" {
		return nil, fmt.Errorf("filesystem_mock root dir is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create filesystem_mock root: %w", err)
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	return &Backend{rootDir: filepath.Clean(root), publicBaseURL: base}, nil
}

func (b *Backend) Type() domain.PackageBackendType { return domain.PackageBackendFilesystemMock }

func (b *Backend) Capabilities() packagebackend.Capabilities {
	return packagebackend.CommonCapabilities()
}

func (b *Backend) EnsureRepository(_ context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	dir, err := b.repositoryDir(repo)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("create package repository directory: %w", err)
	}
	return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"path": dir}}, nil
}

func (b *Backend) DeleteRepository(_ context.Context, repo domain.PackageRepository, force bool) (packagebackend.RepositoryObservation, error) {
	dir, err := b.repositoryDir(repo)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"path": dir}}, nil
	} else if err != nil {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("stat package repository directory: %w", err)
	}
	if !force {
		empty, err := directoryEmpty(dir)
		if err != nil {
			return packagebackend.RepositoryObservation{}, err
		}
		if !empty {
			return packagebackend.RepositoryObservation{}, fmt.Errorf("package repository %q is not empty", repo.ExternalRepositoryName)
		}
		if err := os.Remove(dir); err != nil {
			return packagebackend.RepositoryObservation{}, fmt.Errorf("remove package repository directory: %w", err)
		}
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"path": dir}}, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return packagebackend.RepositoryObservation{}, fmt.Errorf("remove package repository tree: %w", err)
	}
	return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"path": dir}}, nil
}

func (b *Backend) ObserveRepository(_ context.Context, repo domain.PackageRepository) (packagebackend.RepositoryObservation, error) {
	dir, err := b.repositoryDir(repo)
	if err != nil {
		return packagebackend.RepositoryObservation{}, err
	}
	_, err = os.Stat(dir)
	if err == nil {
		return packagebackend.RepositoryObservation{Exists: true, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"path": dir}}, nil
	}
	if os.IsNotExist(err) {
		return packagebackend.RepositoryObservation{Exists: false, PublicURL: b.repositoryURL(repo), Metadata: map[string]string{"path": dir}}, nil
	}
	return packagebackend.RepositoryObservation{}, fmt.Errorf("stat package repository directory: %w", err)
}

func (b *Backend) StoreArtifact(ctx context.Context, repo domain.PackageRepository, req packagebackend.StoreArtifactRequest) (packagebackend.ArtifactObservation, error) {
	select {
	case <-ctx.Done():
		return packagebackend.ArtifactObservation{}, ctx.Err()
	default:
	}
	if req.Reader == nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("artifact reader is required")
	}
	repoDir, err := b.repositoryDir(repo)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("create package repository directory: %w", err)
	}
	relPath, err := packagebackend.ArtifactPath(req.Namespace, req.PackageName, req.Version, req.Filename)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	target, err := packagebackend.SafeJoin(repoDir, relPath)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("create artifact directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".bahia-upload-*")
	if err != nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("create temp artifact: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, h), req.Reader)
	closeErr := tmp.Close()
	if copyErr != nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("write artifact temp file: %w", copyErr)
	}
	if closeErr != nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("close artifact temp file: %w", closeErr)
	}
	if req.SizeBytes > 0 && written != req.SizeBytes {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("artifact size mismatch: expected %d bytes, got %d", req.SizeBytes, written)
	}
	computed := hex.EncodeToString(h.Sum(nil))
	if strings.TrimSpace(req.SHA256) != "" && computed != strings.ToLower(strings.TrimSpace(req.SHA256)) {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("artifact sha256 mismatch: expected %s, got %s", req.SHA256, computed)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("commit artifact: %w", err)
	}
	return packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(repo, relPath), BackendPath: relPath, SHA256: computed, SizeBytes: written}, nil
}

func (b *Backend) GetArtifact(_ context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (packagebackend.ArtifactStream, error) {
	pathOnDisk, relPath, err := b.artifactFile(repo, artifact)
	if err != nil {
		return packagebackend.ArtifactStream{}, err
	}
	f, err := os.Open(pathOnDisk)
	if err != nil {
		return packagebackend.ArtifactStream{}, fmt.Errorf("open artifact: %w", err)
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return packagebackend.ArtifactStream{}, fmt.Errorf("stat artifact: %w", err)
	}
	return packagebackend.ArtifactStream{ReadCloser: f, ContentType: artifact.ContentType, SHA256: artifact.SHA256, SizeBytes: stat.Size(), BackendPath: relPath}, nil
}

func (b *Backend) ListArtifacts(_ context.Context, repo domain.PackageRepository) ([]packagebackend.ArtifactObservation, error) {
	repoDir, err := b.repositoryDir(repo)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat repository directory: %w", err)
	}
	observations := make([]packagebackend.ArtifactObservation, 0)
	err = filepath.WalkDir(repoDir, func(pathOnDisk string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(repoDir, pathOnDisk)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(filepath.Base(pathOnDisk), ".bahia-upload-") || strings.HasPrefix(rel, ".index/") || strings.HasSuffix(rel, ".yanked") {
			return nil
		}
		obs, err := b.observeArtifactPath(repo, rel)
		if err != nil {
			return err
		}
		observations = append(observations, obs)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list package artifacts: %w", err)
	}
	return observations, nil
}

func (b *Backend) PromoteArtifact(ctx context.Context, sourceRepo domain.PackageRepository, targetRepo domain.PackageRepository, artifact domain.PackageArtifact, _ packagebackend.PromoteArtifactRequest) (packagebackend.ArtifactObservation, error) {
	stream, err := b.GetArtifact(ctx, sourceRepo, artifact)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	defer stream.ReadCloser.Close()
	return b.StoreArtifact(ctx, targetRepo, packagebackend.StoreArtifactRequest{
		Namespace:   artifact.Namespace,
		PackageName: artifact.PackageName,
		Version:     artifact.Version,
		Filename:    artifact.Filename,
		ContentType: artifact.ContentType,
		SHA256:      artifact.SHA256,
		SizeBytes:   stream.SizeBytes,
		Metadata:    artifact.Metadata,
		Reader:      stream.ReadCloser,
	})
}

func (b *Backend) YankArtifact(_ context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact, reason string) (packagebackend.ArtifactObservation, error) {
	pathOnDisk, relPath, err := b.artifactFile(repo, artifact)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if err := os.Remove(pathOnDisk); err != nil && !os.IsNotExist(err) {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("remove artifact: %w", err)
	}
	marker := pathOnDisk + ".yanked"
	_ = os.MkdirAll(filepath.Dir(marker), 0o755)
	_ = os.WriteFile(marker, []byte(strings.TrimSpace(reason)+"\n"+time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
	return packagebackend.ArtifactObservation{Exists: false, DownloadURL: b.artifactURL(repo, relPath), BackendPath: relPath, Yanked: true}, nil
}

func (b *Backend) ObserveArtifact(_ context.Context, repo domain.PackageRepository, artifact domain.PackageArtifact) (packagebackend.ArtifactObservation, error) {
	pathOnDisk, relPath, err := b.artifactFile(repo, artifact)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	if _, err := os.Stat(pathOnDisk); os.IsNotExist(err) {
		_, markerErr := os.Stat(pathOnDisk + ".yanked")
		return packagebackend.ArtifactObservation{Exists: false, DownloadURL: b.artifactURL(repo, relPath), BackendPath: relPath, Yanked: markerErr == nil}, nil
	} else if err != nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("stat artifact: %w", err)
	}
	return b.observeArtifactPath(repo, relPath)
}

func (b *Backend) GenerateIndex(ctx context.Context, repoID, format string) error {
	repo := domain.PackageRepository{ExternalRepositoryName: repoID, Name: repoID}
	artifacts, err := b.ListArtifacts(ctx, repo)
	if err != nil {
		return err
	}
	switch domain.PackageRepositoryFormat(format) {
	case domain.PackageRepositoryFormatNPM:
		return b.generateNPMIndex(repo, artifacts)
	case domain.PackageRepositoryFormatPyPI:
		return b.generatePyPIIndex(repo, artifacts)
	default:
		return nil
	}
}

func (b *Backend) ServeIndex(_ context.Context, repoID, requestPath string) (io.Reader, string, error) {
	repo := domain.PackageRepository{ExternalRepositoryName: repoID, Name: repoID}
	repoDir, err := b.repositoryDir(repo)
	if err != nil {
		return nil, "", err
	}
	indexPath, contentType, err := indexFileForRequest(repoDir, requestPath)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, "", fmt.Errorf("read package index: %w", err)
	}
	return bytes.NewReader(data), contentType, nil
}

func (b *Backend) repositoryDir(repo domain.PackageRepository) (string, error) {
	name := strings.TrimSpace(repo.ExternalRepositoryName)
	if name == "" {
		name = strings.TrimSpace(repo.Name)
	}
	if name == "" {
		return "", fmt.Errorf("external repository name is required")
	}
	return packagebackend.SafeJoin(b.rootDir, name)
}

func (b *Backend) artifactFile(repo domain.PackageRepository, artifact domain.PackageArtifact) (string, string, error) {
	repoDir, err := b.repositoryDir(repo)
	if err != nil {
		return "", "", err
	}
	relPath := strings.TrimSpace(artifact.BackendPath)
	if relPath == "" {
		relPath, err = packagebackend.ArtifactPath(artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
		if err != nil {
			return "", "", err
		}
	}
	pathOnDisk, err := packagebackend.SafeJoin(repoDir, relPath)
	if err != nil {
		return "", "", err
	}
	return pathOnDisk, relPath, nil
}

func (b *Backend) observeArtifactPath(repo domain.PackageRepository, relPath string) (packagebackend.ArtifactObservation, error) {
	repoDir, err := b.repositoryDir(repo)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	pathOnDisk, err := packagebackend.SafeJoin(repoDir, relPath)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	f, err := os.Open(pathOnDisk)
	if err != nil {
		return packagebackend.ArtifactObservation{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return packagebackend.ArtifactObservation{}, fmt.Errorf("hash artifact: %w", err)
	}
	return packagebackend.ArtifactObservation{Exists: true, DownloadURL: b.artifactURL(repo, relPath), BackendPath: relPath, SHA256: hex.EncodeToString(h.Sum(nil)), SizeBytes: n}, nil
}

func (b *Backend) repositoryURL(repo domain.PackageRepository) string {
	name := strings.TrimSpace(repo.ExternalRepositoryName)
	if name == "" {
		name = strings.TrimSpace(repo.Name)
	}
	if b.publicBaseURL != "" {
		return b.publicBaseURL + "/" + url.PathEscape(name)
	}
	dir, err := b.repositoryDir(repo)
	if err != nil {
		return ""
	}
	return "file://" + filepath.ToSlash(dir)
}

func (b *Backend) artifactURL(repo domain.PackageRepository, relPath string) string {
	if b.publicBaseURL != "" {
		return b.repositoryURL(repo) + "/" + escapePath(relPath)
	}
	repoDir, err := b.repositoryDir(repo)
	if err != nil {
		return ""
	}
	filePath, err := packagebackend.SafeJoin(repoDir, relPath)
	if err != nil {
		return ""
	}
	return "file://" + filepath.ToSlash(filePath)
}

type indexArtifact struct {
	PackageName string
	Version     string
	Filename    string
	BackendPath string
	DownloadURL string
	SHA256      string
}

func (b *Backend) generateNPMIndex(repo domain.PackageRepository, artifacts []packagebackend.ArtifactObservation) error {
	if err := b.clearIndexDir(repo, "npm"); err != nil {
		return err
	}
	byPackage := map[string][]indexArtifact{}
	for _, obs := range artifacts {
		item, ok := indexArtifactFromObservation(obs)
		if ok {
			byPackage[item.PackageName] = append(byPackage[item.PackageName], item)
		}
	}
	for _, pkg := range b.yankedPackageNames(repo) {
		if _, ok := byPackage[pkg]; !ok {
			byPackage[pkg] = nil
		}
	}
	for pkg, items := range byPackage {
		versions := map[string]any{}
		for _, item := range items {
			versions[item.Version] = map[string]any{"dist": map[string]any{"tarball": item.DownloadURL, "shasum": item.SHA256}}
		}
		body, err := json.MarshalIndent(map[string]any{"name": pkg, "versions": versions}, "", "  ")
		if err != nil {
			return err
		}
		if err := b.writeIndexFile(repo, filepath.Join("npm", pkg+".json"), body); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) generatePyPIIndex(repo domain.PackageRepository, artifacts []packagebackend.ArtifactObservation) error {
	if err := b.clearIndexDir(repo, "pypi"); err != nil {
		return err
	}
	byPackage := map[string][]indexArtifact{}
	for _, obs := range artifacts {
		item, ok := indexArtifactFromObservation(obs)
		if ok {
			byPackage[normalizePyPIName(item.PackageName)] = append(byPackage[normalizePyPIName(item.PackageName)], item)
		}
	}
	packages := make([]string, 0, len(byPackage))
	for pkg := range byPackage {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	var root strings.Builder
	root.WriteString("<!DOCTYPE html>\n<html><body>\n")
	for _, pkg := range packages {
		root.WriteString(fmt.Sprintf("<a href=\"%s/\">%s</a>\n", html.EscapeString(pkg), html.EscapeString(pkg)))
	}
	root.WriteString("</body></html>\n")
	if err := b.writeIndexFile(repo, filepath.Join("pypi", "simple", "index.html"), []byte(root.String())); err != nil {
		return err
	}
	for _, pkg := range packages {
		items := byPackage[pkg]
		sort.Slice(items, func(i, j int) bool { return items[i].Version < items[j].Version })
		var page strings.Builder
		page.WriteString("<!DOCTYPE html>\n<html><body>\n")
		for _, item := range items {
			page.WriteString(fmt.Sprintf("<a href=\"%s#sha256=%s\">%s</a>\n", html.EscapeString(item.DownloadURL), html.EscapeString(item.SHA256), html.EscapeString(item.Filename)))
		}
		page.WriteString("</body></html>\n")
		if err := b.writeIndexFile(repo, filepath.Join("pypi", "simple", pkg, "index.html"), []byte(page.String())); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) yankedPackageNames(repo domain.PackageRepository) []string {
	repoDir, err := b.repositoryDir(repo)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	_ = filepath.WalkDir(repoDir, func(pathOnDisk string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(pathOnDisk, ".yanked") {
			return nil
		}
		rel, err := filepath.Rel(repoDir, strings.TrimSuffix(pathOnDisk, ".yanked"))
		if err != nil {
			return nil
		}
		item, ok := indexArtifactFromObservation(packagebackend.ArtifactObservation{BackendPath: filepath.ToSlash(rel)})
		if ok {
			seen[item.PackageName] = struct{}{}
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for pkg := range seen {
		out = append(out, pkg)
	}
	sort.Strings(out)
	return out
}

func (b *Backend) clearIndexDir(repo domain.PackageRepository, formatDir string) error {
	repoDir, err := b.repositoryDir(repo)
	if err != nil {
		return err
	}
	target, err := packagebackend.SafeJoin(repoDir, ".index", formatDir)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("clear index directory: %w", err)
	}
	return nil
}

func (b *Backend) writeIndexFile(repo domain.PackageRepository, relPath string, data []byte) error {
	repoDir, err := b.repositoryDir(repo)
	if err != nil {
		return err
	}
	target, err := packagebackend.SafeJoin(repoDir, ".index", relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create index directory: %w", err)
	}
	return os.WriteFile(target, data, 0o644)
}

func indexArtifactFromObservation(obs packagebackend.ArtifactObservation) (indexArtifact, bool) {
	parts := strings.Split(strings.Trim(obs.BackendPath, "/"), "/")
	if len(parts) < 3 {
		return indexArtifact{}, false
	}
	return indexArtifact{PackageName: parts[len(parts)-3], Version: parts[len(parts)-2], Filename: parts[len(parts)-1], BackendPath: obs.BackendPath, DownloadURL: obs.DownloadURL, SHA256: obs.SHA256}, true
}

func indexFileForRequest(repoDir, requestPath string) (string, string, error) {
	requestPath = strings.Trim(requestPath, "/")
	if requestPath == "simple" || requestPath == "simple/" {
		p, err := packagebackend.SafeJoin(repoDir, ".index", "pypi", "simple", "index.html")
		return p, "text/html; charset=utf-8", err
	}
	if strings.HasPrefix(requestPath, "simple/") {
		pkg := strings.Trim(strings.TrimPrefix(requestPath, "simple/"), "/")
		p, err := packagebackend.SafeJoin(repoDir, ".index", "pypi", "simple", normalizePyPIName(pkg), "index.html")
		return p, "text/html; charset=utf-8", err
	}
	if requestPath != "" {
		p, err := packagebackend.SafeJoin(repoDir, ".index", "npm", requestPath+".json")
		return p, "application/json", err
	}
	return "", "", fmt.Errorf("index path is required")
}

func normalizePyPIName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.NewReplacer("_", "-", ".", "-").Replace(name)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func directoryEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("read repository directory: %w", err)
	}
	return len(entries) == 0, nil
}
