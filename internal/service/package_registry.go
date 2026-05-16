package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

var (
	ErrPackagePolicyDenied     = errors.New("package policy denied")
	ErrPackageApprovalRequired = errors.New("package approval required")
	ErrPackageBackendMissing   = errors.New("package backend missing")
)

// PackageRegistryService owns package repository policy and backend orchestration.
// It intentionally does not publish Nostr events and does not expose package
// manager protocols; callers use it from signer-first control-plane handlers.
type PackageRegistryService struct {
	cfg        config.PackageControlplaneConfig
	backends   packagebackend.Registry
	projection repository.PackageControlPlaneRepository
	httpClient *http.Client
	logger     *zap.Logger
}

// PackagePublishRequest describes a package artifact source and metadata.
type PackagePublishRequest struct {
	Namespace   string
	PackageName string
	Version     string
	Filename    string
	SourceURL   string
	SHA256      string
	SizeBytes   int64
	ContentType string
	Metadata    map[string]any
}

// PackagePromotionRequest describes promotion into a target repository/channel.
type PackagePromotionRequest struct {
	Environment string
	Channel     string
	ApprovedBy  string
	PolicyRef   string
	Metadata    map[string]any
}

// PackageYankRequest describes a package yank/delete operation.
type PackageYankRequest struct {
	Namespace   string
	PackageName string
	Version     string
	Filename    string
	Reason      string
	Metadata    map[string]any
}

// PackageDriftObservation compares projected control-plane state with backend-observed state.
type PackageDriftObservation struct {
	ResourceKind string `json:"resource_kind"`
	ResourceID   string `json:"resource_id,omitempty"`
	Expected     bool   `json:"expected"`
	Observed     bool   `json:"observed"`
	Drifted      bool   `json:"drifted"`
	Reason       string `json:"reason,omitempty"`
}

func NewPackageRegistryService(cfg config.PackageControlplaneConfig, backends packagebackend.Registry, projection repository.PackageControlPlaneRepository, httpClient *http.Client, logger *zap.Logger) (*PackageRegistryService, error) {
	if backends == nil {
		backends = packagebackend.Registry{}
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PackageRegistryService{cfg: cfg, backends: backends, projection: projection, httpClient: httpClient, logger: logger}, nil
}

func (s *PackageRegistryService) Capabilities(ref string) (packagebackend.Capabilities, error) {
	backend, err := s.backend(ref)
	if err != nil {
		return packagebackend.Capabilities{}, err
	}
	return backend.Capabilities(), nil
}

func (s *PackageRegistryService) ValidateRepositorySpec(repo *domain.PackageRepository, existing *domain.PackageRepository) error {
	if repo == nil {
		return fmt.Errorf("package repository is required")
	}
	repo.Name = strings.TrimSpace(repo.Name)
	repo.BackendRef = strings.TrimSpace(repo.BackendRef)
	repo.ExternalRepositoryName = strings.TrimSpace(repo.ExternalRepositoryName)
	repo.Description = strings.TrimSpace(repo.Description)
	repo.NamespacePrefix = strings.Trim(repo.NamespacePrefix, "/")
	if repo.Name == "" {
		return fmt.Errorf("package repository name is required")
	}
	if repo.BackendRef == "" {
		return fmt.Errorf("package backend_ref is required")
	}
	if repo.ExternalRepositoryName == "" {
		repo.ExternalRepositoryName = repo.Name
	}
	if repo.Format == "" || !repo.Format.IsValid() {
		return fmt.Errorf("package repository format %q is unsupported", repo.Format)
	}
	backend, err := s.backend(repo.BackendRef)
	if err != nil {
		return err
	}
	if backend.Type() != repo.BackendType {
		if repo.BackendType != "" {
			return fmt.Errorf("package repository backend_type %q does not match backend ref %q type %q", repo.BackendType, repo.BackendRef, backend.Type())
		}
		repo.BackendType = backend.Type()
	}
	if !packagebackend.SupportsFormat(backend.Capabilities(), repo.Format) {
		return fmt.Errorf("backend %q does not support package format %q", repo.BackendRef, repo.Format)
	}
	if existing != nil {
		if existing.BackendRef != repo.BackendRef {
			return fmt.Errorf("package repository backend_ref is immutable")
		}
		if existing.ExternalRepositoryName != repo.ExternalRepositoryName {
			return fmt.Errorf("package repository external_repository_name is immutable")
		}
		if existing.Format != repo.Format {
			return fmt.Errorf("package repository format is immutable")
		}
		if repo.ID == uuid.Nil {
			repo.ID = existing.ID
		}
		if repo.CreatedAt.IsZero() {
			repo.CreatedAt = existing.CreatedAt
		}
	}
	return nil
}

func (s *PackageRegistryService) EnsureRepository(ctx context.Context, repo *domain.PackageRepository, existing *domain.PackageRepository) (*domain.PackageRepository, error) {
	if err := s.ValidateRepositorySpec(repo, existing); err != nil {
		return nil, err
	}
	backend, _ := s.backend(repo.BackendRef)
	obs, err := backend.EnsureRepository(ctx, *repo)
	if err != nil {
		repo.Status = domain.PackageRepositoryStatusFailed
		repo.LastError = err.Error()
		return repo, err
	}
	now := time.Now().UTC()
	if repo.ID == uuid.Nil {
		repo.ID = uuid.New()
	}
	if repo.CreatedAt.IsZero() {
		repo.CreatedAt = now
	}
	repo.UpdatedAt = now
	repo.BackendType = backend.Type()
	repo.PublicURL = obs.PublicURL
	repo.Status = domain.PackageRepositoryStatusReady
	repo.LastError = ""
	repo.Deleted = false
	return repo, nil
}

func (s *PackageRegistryService) DeleteRepository(ctx context.Context, repo *domain.PackageRepository, force bool) (*domain.PackageRepository, error) {
	if repo == nil {
		return nil, fmt.Errorf("package repository is required")
	}
	if repo.Deleted || repo.Status == domain.PackageRepositoryStatusDeleted {
		return repo, nil
	}
	if !force && s.projection != nil && repo.ID != uuid.Nil {
		artifacts, err := s.projection.ListArtifacts(ctx, repo.ID, 500, 0)
		if err != nil {
			return nil, fmt.Errorf("checking projected package artifacts before repository delete: %w", err)
		}
		if len(artifacts) > 0 {
			return nil, fmt.Errorf("package repository %q has %d projected artifacts", repo.Name, len(artifacts))
		}
	}
	backend, err := s.backend(repo.BackendRef)
	if err != nil {
		return nil, err
	}
	obs, err := backend.DeleteRepository(ctx, *repo, force)
	if err != nil {
		repo.Status = domain.PackageRepositoryStatusFailed
		repo.LastError = err.Error()
		return repo, err
	}
	repo.PublicURL = obs.PublicURL
	repo.Deleted = true
	repo.Status = domain.PackageRepositoryStatusDeleted
	repo.LastError = ""
	repo.UpdatedAt = time.Now().UTC()
	return repo, nil
}

func (s *PackageRegistryService) PublishPackage(ctx context.Context, repo *domain.PackageRepository, existing *domain.PackageArtifact, req PackagePublishRequest) (*domain.PackageArtifact, error) {
	if err := s.validateRepositoryReady(repo); err != nil {
		return nil, err
	}
	if err := s.validatePublishRequest(*repo, req); err != nil {
		return nil, err
	}
	if existing != nil && !existing.Deleted {
		if strings.EqualFold(existing.SHA256, req.SHA256) {
			return existing, nil
		}
		if !repo.Policy.AllowOverwrite {
			return nil, fmt.Errorf("artifact exists with different digest and overwrites are disabled: %w", ErrPackagePolicyDenied)
		}
	}
	backend, _ := s.backend(repo.BackendRef)
	tmp, contentType, size, cleanup, err := s.fetchAndVerifySource(ctx, req.SourceURL, req.SHA256, req.SizeBytes, repo.Policy.MaxFileSizeBytes)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if strings.TrimSpace(req.ContentType) == "" {
		req.ContentType = contentType
	}
	if req.ContentType == "" {
		req.ContentType = "application/octet-stream"
	}
	if err := enforceMediaTypePolicy(repo.Policy, req.ContentType); err != nil {
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind verified artifact: %w", err)
	}
	obs, err := backend.StoreArtifact(ctx, *repo, packagebackend.StoreArtifactRequest{Namespace: req.Namespace, PackageName: req.PackageName, Version: req.Version, Filename: req.Filename, ContentType: req.ContentType, SHA256: req.SHA256, SizeBytes: size, Metadata: req.Metadata, Reader: tmp})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	artifact := &domain.PackageArtifact{}
	if existing != nil {
		*artifact = *existing
	}
	if artifact.ID == uuid.Nil {
		artifact.ID = uuid.New()
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = now
	}
	artifact.RepositoryID = repo.ID
	artifact.RepositoryName = repo.Name
	artifact.Format = repo.Format
	artifact.Namespace = strings.Trim(req.Namespace, "/")
	artifact.PackageName = strings.TrimSpace(req.PackageName)
	artifact.Version = strings.TrimSpace(req.Version)
	artifact.Filename = strings.TrimSpace(req.Filename)
	artifact.SourceURL = strings.TrimSpace(req.SourceURL)
	artifact.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
	artifact.SizeBytes = size
	artifact.ContentType = req.ContentType
	artifact.Metadata = req.Metadata
	artifact.DownloadURL = obs.DownloadURL
	artifact.BackendPath = obs.BackendPath
	artifact.Status = domain.PackageArtifactStatusAvailable
	artifact.LastError = ""
	artifact.Deleted = false
	artifact.UpdatedAt = now
	s.regenerateIndex(ctx, repo, backend)
	return artifact, nil
}

func (s *PackageRegistryService) PromotePackage(ctx context.Context, sourceRepo *domain.PackageRepository, targetRepo *domain.PackageRepository, artifact *domain.PackageArtifact, existingTarget *domain.PackageArtifact, req PackagePromotionRequest) (*domain.PackageArtifact, *domain.PackagePublication, error) {
	if err := s.validateRepositoryReady(sourceRepo); err != nil {
		return nil, nil, err
	}
	if err := s.validateRepositoryReady(targetRepo); err != nil {
		return nil, nil, err
	}
	if artifact == nil || artifact.Deleted || artifact.Status != domain.PackageArtifactStatusAvailable {
		return nil, nil, fmt.Errorf("source package artifact must be available")
	}
	if err := s.evaluatePromotionPolicy(*targetRepo, *artifact, req); err != nil {
		return nil, nil, err
	}
	if existingTarget != nil && !existingTarget.Deleted && strings.EqualFold(existingTarget.SHA256, artifact.SHA256) {
		publication := newPublication(targetRepo.ID, artifact.ID, existingTarget.ID, req, domain.PackagePublicationStatusPromoted, domain.PackagePolicyDecisionAllowed)
		return existingTarget, publication, nil
	}
	if existingTarget != nil && !existingTarget.Deleted && !targetRepo.Policy.AllowOverwrite {
		return nil, nil, fmt.Errorf("target artifact exists with different digest and overwrites are disabled: %w", ErrPackagePolicyDenied)
	}
	targetBackend, _ := s.backend(targetRepo.BackendRef)
	sourceBackend, _ := s.backend(sourceRepo.BackendRef)
	var obs packagebackend.ArtifactObservation
	var err error
	if sourceRepo.BackendRef == targetRepo.BackendRef {
		obs, err = targetBackend.PromoteArtifact(ctx, *sourceRepo, *targetRepo, *artifact, packagebackend.PromoteArtifactRequest{Environment: req.Environment, Channel: req.Channel, ApprovedBy: req.ApprovedBy, PolicyRef: req.PolicyRef, Metadata: req.Metadata})
	} else {
		stream, streamErr := sourceBackend.GetArtifact(ctx, *sourceRepo, *artifact)
		if streamErr != nil {
			return nil, nil, streamErr
		}
		defer stream.ReadCloser.Close()
		obs, err = targetBackend.StoreArtifact(ctx, *targetRepo, packagebackend.StoreArtifactRequest{Namespace: artifact.Namespace, PackageName: artifact.PackageName, Version: artifact.Version, Filename: artifact.Filename, ContentType: artifact.ContentType, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes, Metadata: req.Metadata, Reader: stream.ReadCloser})
	}
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	target := &domain.PackageArtifact{}
	if existingTarget != nil {
		*target = *existingTarget
	}
	if target.ID == uuid.Nil {
		target.ID = uuid.New()
	}
	if target.CreatedAt.IsZero() {
		target.CreatedAt = now
	}
	target.RepositoryID = targetRepo.ID
	target.RepositoryName = targetRepo.Name
	target.Format = targetRepo.Format
	target.Namespace = artifact.Namespace
	target.PackageName = artifact.PackageName
	target.Version = artifact.Version
	target.Filename = artifact.Filename
	target.SHA256 = artifact.SHA256
	target.SizeBytes = artifact.SizeBytes
	target.ContentType = artifact.ContentType
	target.Metadata = mergeMetadata(artifact.Metadata, req.Metadata)
	target.DownloadURL = obs.DownloadURL
	target.BackendPath = obs.BackendPath
	target.Status = domain.PackageArtifactStatusAvailable
	target.Deleted = false
	target.LastError = ""
	target.UpdatedAt = now
	s.regenerateIndex(ctx, targetRepo, targetBackend)
	publication := newPublication(targetRepo.ID, artifact.ID, target.ID, req, domain.PackagePublicationStatusPromoted, domain.PackagePolicyDecisionAllowed)
	return target, publication, nil
}

func (s *PackageRegistryService) YankPackage(ctx context.Context, repo *domain.PackageRepository, existing *domain.PackageArtifact, req PackageYankRequest) (*domain.PackageArtifact, error) {
	if err := s.validateRepositoryReady(repo); err != nil {
		return nil, err
	}
	if req.PackageName = strings.TrimSpace(req.PackageName); req.PackageName == "" {
		return nil, fmt.Errorf("package_name is required")
	}
	if req.Version = strings.TrimSpace(req.Version); req.Version == "" {
		return nil, fmt.Errorf("version is required")
	}
	if req.Filename = strings.TrimSpace(req.Filename); req.Filename == "" {
		return nil, fmt.Errorf("filename is required")
	}
	now := time.Now().UTC()
	if existing == nil {
		return &domain.PackageArtifact{ID: uuid.New(), RepositoryID: repo.ID, RepositoryName: repo.Name, Format: repo.Format, Namespace: strings.Trim(req.Namespace, "/"), PackageName: req.PackageName, Version: req.Version, Filename: req.Filename, Status: domain.PackageArtifactStatusDeleted, Deleted: true, CreatedAt: now, UpdatedAt: now}, nil
	}
	if existing.Deleted || existing.Status == domain.PackageArtifactStatusDeleted {
		return existing, nil
	}
	backend, _ := s.backend(repo.BackendRef)
	obs, err := backend.YankArtifact(ctx, *repo, *existing, req.Reason)
	if err != nil {
		return nil, err
	}
	artifact := *existing
	artifact.DownloadURL = obs.DownloadURL
	artifact.BackendPath = obs.BackendPath
	artifact.Status = domain.PackageArtifactStatusDeleted
	artifact.Deleted = true
	artifact.LastError = ""
	artifact.UpdatedAt = now
	s.regenerateIndex(ctx, repo, backend)
	return &artifact, nil
}

func (s *PackageRegistryService) ObserveRepositoryDrift(ctx context.Context, repo *domain.PackageRepository) (*PackageDriftObservation, error) {
	if repo == nil {
		return nil, fmt.Errorf("package repository is required")
	}
	backend, err := s.backend(repo.BackendRef)
	if err != nil {
		return nil, err
	}
	obs, err := backend.ObserveRepository(ctx, *repo)
	if err != nil {
		return nil, err
	}
	expected := !repo.Deleted && repo.Status != domain.PackageRepositoryStatusDeleted
	drifted := expected != obs.Exists
	reason := ""
	if drifted {
		reason = fmt.Sprintf("repository expected exists=%v observed exists=%v", expected, obs.Exists)
	}
	return &PackageDriftObservation{ResourceKind: "repository", ResourceID: repo.ID.String(), Expected: expected, Observed: obs.Exists, Drifted: drifted, Reason: reason}, nil
}

func (s *PackageRegistryService) ObserveArtifactDrift(ctx context.Context, repo *domain.PackageRepository, artifact *domain.PackageArtifact) (*PackageDriftObservation, error) {
	if err := s.validateRepositoryConfigured(repo); err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, fmt.Errorf("package artifact is required")
	}
	backend, _ := s.backend(repo.BackendRef)
	caps := backend.Capabilities()
	obs, err := backend.ObserveArtifact(ctx, *repo, *artifact)
	if err != nil {
		return nil, err
	}
	expected := !artifact.Deleted && artifact.Status == domain.PackageArtifactStatusAvailable
	drifted := expected != obs.Exists
	if caps.CanObserveDrift && expected && obs.Exists && artifact.SHA256 != "" && obs.SHA256 != "" && !strings.EqualFold(artifact.SHA256, obs.SHA256) {
		drifted = true
	}
	reason := ""
	if drifted {
		reason = fmt.Sprintf("artifact expected exists=%v observed exists=%v", expected, obs.Exists)
		if caps.CanObserveDrift && expected && obs.Exists && artifact.SHA256 != "" && obs.SHA256 != "" && !strings.EqualFold(artifact.SHA256, obs.SHA256) {
			reason = fmt.Sprintf("artifact sha256 expected=%s observed=%s", artifact.SHA256, obs.SHA256)
		}
	}
	return &PackageDriftObservation{ResourceKind: "artifact", ResourceID: artifact.ID.String(), Expected: expected, Observed: obs.Exists, Drifted: drifted, Reason: reason}, nil
}

func (s *PackageRegistryService) regenerateIndex(ctx context.Context, repo *domain.PackageRepository, backend packagebackend.Backend) {
	generator, ok := backend.(packagebackend.IndexGenerator)
	if !ok || repo == nil {
		return
	}
	repoID := strings.TrimSpace(repo.ExternalRepositoryName)
	if repoID == "" {
		repoID = strings.TrimSpace(repo.Name)
	}
	if err := generator.GenerateIndex(ctx, repoID, string(repo.Format)); err != nil {
		s.logger.Warn("package index generation failed", zap.String("repository", repo.Name), zap.String("format", string(repo.Format)), zap.Error(err))
	}
}

func (s *PackageRegistryService) validateRepositoryReady(repo *domain.PackageRepository) error {
	if err := s.validateRepositoryConfigured(repo); err != nil {
		return err
	}
	if repo.Deleted || repo.Status == domain.PackageRepositoryStatusDeleted {
		return fmt.Errorf("package repository %q is deleted", repo.Name)
	}
	return nil
}

func (s *PackageRegistryService) validateRepositoryConfigured(repo *domain.PackageRepository) error {
	if repo == nil {
		return fmt.Errorf("package repository is required")
	}
	_, err := s.backend(repo.BackendRef)
	return err
}

func (s *PackageRegistryService) validatePublishRequest(repo domain.PackageRepository, req PackagePublishRequest) error {
	if strings.TrimSpace(req.PackageName) == "" {
		return fmt.Errorf("package_name is required")
	}
	if strings.TrimSpace(req.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if strings.TrimSpace(req.Filename) == "" {
		return fmt.Errorf("filename is required")
	}
	if strings.TrimSpace(req.SourceURL) == "" {
		return fmt.Errorf("source_url is required")
	}
	if err := validateSHA256(req.SHA256); err != nil {
		return err
	}
	if req.SizeBytes <= 0 {
		return fmt.Errorf("size_bytes must be > 0")
	}
	if repo.Policy.RequireSHA256 && strings.TrimSpace(req.SHA256) == "" {
		return fmt.Errorf("sha256 is required by repository policy: %w", ErrPackagePolicyDenied)
	}
	if repo.Policy.MaxFileSizeBytes > 0 && req.SizeBytes > repo.Policy.MaxFileSizeBytes {
		return fmt.Errorf("artifact exceeds max_file_size_bytes policy: %w", ErrPackagePolicyDenied)
	}
	if err := enforceNamePrefixPolicy(repo.Policy, req.PackageName); err != nil {
		return err
	}
	if err := enforceMediaTypePolicy(repo.Policy, req.ContentType); err != nil {
		return err
	}
	return s.validateSourceURL(req.SourceURL)
}

func (s *PackageRegistryService) evaluatePromotionPolicy(targetRepo domain.PackageRepository, artifact domain.PackageArtifact, req PackagePromotionRequest) error {
	if targetRepo.Policy.PromotionRequiresApproval && strings.TrimSpace(req.ApprovedBy) == "" {
		return fmt.Errorf("promotion requires approval: %w", ErrPackageApprovalRequired)
	}
	if err := enforceNamePrefixPolicy(targetRepo.Policy, artifact.PackageName); err != nil {
		return err
	}
	if err := enforceMediaTypePolicy(targetRepo.Policy, artifact.ContentType); err != nil {
		return err
	}
	return nil
}

func (s *PackageRegistryService) fetchAndVerifySource(ctx context.Context, rawURL, expectedSHA string, expectedSize int64, maxSize int64) (*os.File, string, int64, func(), error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", 0, nil, fmt.Errorf("source_url is invalid: %w", err)
	}
	var reader io.ReadCloser
	contentType := ""
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, "", 0, nil, err
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, "", 0, nil, fmt.Errorf("fetch package source: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return nil, "", 0, nil, fmt.Errorf("fetch package source failed: status=%d", resp.StatusCode)
		}
		if resp.ContentLength > expectedSize || (maxSize > 0 && resp.ContentLength > maxSize) {
			_ = resp.Body.Close()
			return nil, "", 0, nil, fmt.Errorf("package source exceeds declared or policy size")
		}
		reader = resp.Body
		contentType = mediaTypeOnly(resp.Header.Get("Content-Type"))
	case "file":
		path := parsed.Path
		if parsed.Host != "" {
			path = "/" + parsed.Host + parsed.Path
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, "", 0, nil, fmt.Errorf("open package source file: %w", err)
		}
		reader = f
	default:
		return nil, "", 0, nil, fmt.Errorf("source_url scheme %q is unsupported", parsed.Scheme)
	}
	defer reader.Close()
	tmp, err := os.CreateTemp("", "bahia-package-source-*")
	if err != nil {
		return nil, "", 0, nil, fmt.Errorf("create package source temp file: %w", err)
	}
	cleanup := func() { name := tmp.Name(); _ = tmp.Close(); _ = os.Remove(name) }
	h := sha256.New()
	limit := expectedSize
	if maxSize > 0 && maxSize < limit {
		limit = maxSize
	}
	limited := io.LimitReader(reader, limit+1)
	n, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if err != nil {
		cleanup()
		return nil, "", 0, nil, fmt.Errorf("read package source: %w", err)
	}
	if n > limit {
		cleanup()
		return nil, "", 0, nil, fmt.Errorf("package source exceeds declared or policy size")
	}
	computed := hex.EncodeToString(h.Sum(nil))
	if computed != strings.ToLower(strings.TrimSpace(expectedSHA)) {
		cleanup()
		return nil, "", 0, nil, fmt.Errorf("source sha256 mismatch: expected %s got %s", expectedSHA, computed)
	}
	if n != expectedSize {
		cleanup()
		return nil, "", 0, nil, fmt.Errorf("source size mismatch: expected %d got %d", expectedSize, n)
	}
	return tmp, contentType, n, cleanup, nil
}

func (s *PackageRegistryService) validateSourceURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("source_url must be a valid URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !s.cfg.AllowHTTPSource {
			return fmt.Errorf("http source_url is disabled")
		}
	case "file":
		if !s.cfg.AllowFileSource {
			return fmt.Errorf("file source_url is disabled")
		}
	default:
		return fmt.Errorf("source_url scheme %q is unsupported", parsed.Scheme)
	}
	if parsed.Scheme == "http" || parsed.Scheme == "https" {
		host := strings.ToLower(parsed.Hostname())
		if len(s.cfg.AllowedSourceHosts) > 0 && !stringInSlice(host, s.cfg.AllowedSourceHosts) {
			return fmt.Errorf("source host %q is not allowed", host)
		}
	}
	return nil
}

func (s *PackageRegistryService) backend(ref string) (packagebackend.Backend, error) {
	backend, ok := s.backends.Get(strings.TrimSpace(ref))
	if !ok || backend == nil {
		return nil, fmt.Errorf("package backend ref %q is not configured: %w", ref, ErrPackageBackendMissing)
	}
	return backend, nil
}

func validateSHA256(value string) error {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return fmt.Errorf("sha256 must be 64 lowercase hex characters")
	}
	if value != strings.ToLower(value) {
		return fmt.Errorf("sha256 must be lowercase hex")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("sha256 must be valid hex: %w", err)
	}
	return nil
}

func enforceNamePrefixPolicy(policy domain.PackageRepositoryPolicy, packageName string) error {
	if len(policy.AllowedPackageNamePrefixes) == 0 {
		return nil
	}
	for _, prefix := range policy.AllowedPackageNamePrefixes {
		if strings.HasPrefix(packageName, prefix) {
			return nil
		}
	}
	return fmt.Errorf("package_name %q is denied by allowed prefix policy: %w", packageName, ErrPackagePolicyDenied)
}

func enforceMediaTypePolicy(policy domain.PackageRepositoryPolicy, contentType string) error {
	if len(policy.AllowedMediaTypes) == 0 || strings.TrimSpace(contentType) == "" {
		return nil
	}
	got := mediaTypeOnly(contentType)
	for _, allowed := range policy.AllowedMediaTypes {
		if got == mediaTypeOnly(allowed) {
			return nil
		}
	}
	return fmt.Errorf("content_type %q is denied by media type policy: %w", contentType, ErrPackagePolicyDenied)
}

func mediaTypeOnly(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if idx := strings.Index(value, ";"); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	return value
}

func newPublication(repositoryID, sourceArtifactID, targetArtifactID uuid.UUID, req PackagePromotionRequest, status domain.PackagePublicationStatus, decision domain.PackagePolicyDecision) *domain.PackagePublication {
	now := time.Now().UTC()
	return &domain.PackagePublication{ID: uuid.New(), RepositoryID: repositoryID, ArtifactID: targetArtifactID, Status: status, PolicyDecision: decision, PolicyRef: req.PolicyRef, ApprovedBy: req.ApprovedBy, Environment: req.Environment, Channel: req.Channel, PublishedAt: &now, PromotedAt: &now, Metadata: mergeMetadata(map[string]any{"source_artifact_id": sourceArtifactID.String()}, req.Metadata), CreatedAt: now, UpdatedAt: now}
}

func mergeMetadata(a, b map[string]any) map[string]any {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func stringInSlice(value string, list []string) bool {
	for _, candidate := range list {
		if value == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}
