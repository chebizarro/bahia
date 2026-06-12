package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

var (
	// ErrRegistryUnauthorized indicates pull access was denied.
	ErrRegistryUnauthorized = errors.New("registry pull unauthorized")
	// ErrRegistryPushUnauthorized indicates push access was denied.
	ErrRegistryPushUnauthorized = errors.New("registry push unauthorized")
	// ErrManifestNotFound indicates a manifest was not found.
	ErrManifestNotFound = errors.New("manifest not found")
	// ErrBlobNotFound indicates a blob was not found.
	ErrBlobNotFound = errors.New("blob not found")
	// ErrManifestNotAcceptable indicates the manifest media type does not match the Accept header.
	ErrManifestNotAcceptable  = errors.New("manifest media type not acceptable")
	ErrManifestInvalid        = errors.New("manifest invalid")
	ErrManifestDigestMismatch = errors.New("manifest digest mismatch")
	ErrBlobUnknown            = errors.New("referenced blob unknown")
)

const (
	ociMediaTypeImageManifest = "application/vnd.oci.image.manifest.v1+json"
	ociMediaTypeImageIndex    = "application/vnd.oci.image.index.v1+json"
	dockerV2Manifest          = "application/vnd.docker.distribution.manifest.v2+json"
)

type registryPrincipalContextKey struct{}

// WithRegistryPrincipal stores a registry principal in context.
func WithRegistryPrincipal(ctx context.Context, principal *domain.RegistryPrincipal) context.Context {
	return context.WithValue(ctx, registryPrincipalContextKey{}, principal)
}

// RegistryPrincipalFromContext extracts a registry principal from context.
func RegistryPrincipalFromContext(ctx context.Context) *domain.RegistryPrincipal {
	p, _ := ctx.Value(registryPrincipalContextKey{}).(*domain.RegistryPrincipal)
	return p
}

// APIVersionResponse is the /v2 ping result.
type APIVersionResponse struct {
	DockerDistributionAPIVersion string
}

// ManifestResponse is a raw OCI manifest response.
type ManifestResponse struct {
	Content             []byte
	ContentType         string
	DockerContentDigest string
	ContentLength       int64
}

// BlobStreamResponse is a proxied blob GET response.
type BlobStreamResponse struct {
	Stream              *blossom.BlobStream
	ContentType         string
	DockerContentDigest string
	ContentLength       int64
}

// BlobHeadResponse is a proxied blob HEAD response.
type BlobHeadResponse struct {
	Exists              bool
	ContentType         string
	DockerContentDigest string
	ContentLength       int64
	Header              map[string]string
}

// TagListResponse is the OCI tags/list response body.
type TagListResponse struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// ReferrersResponse is the OCI referrers response body.
type ReferrersResponse struct {
	SchemaVersion int                            `json:"schemaVersion"`
	MediaType     string                         `json:"mediaType"`
	Manifests     []domain.OCIReferrerDescriptor `json:"manifests"`
}

// OCIRegistryService provides OCI registry operations.
type OCIRegistryService struct {
	cfg     config.OCIServerConfig
	repo    repository.OCIRegistryRepository
	uploads repository.UploadSessionRepository
	blossom *blossom.Client
	logger  *zap.Logger

	uploadMu sync.Map
}

// NewOCIRegistryService creates a new OCI registry service.
func NewOCIRegistryService(
	cfg config.OCIServerConfig,
	repo repository.OCIRegistryRepository,
	uploads repository.UploadSessionRepository,
	blossomClient *blossom.Client,
	logger *zap.Logger,
) (*OCIRegistryService, error) {
	if repo == nil {
		return nil, fmt.Errorf("oci repository is required")
	}
	if blossomClient == nil {
		return nil, fmt.Errorf("blossom client is required")
	}
	if uploads == nil {
		return nil, fmt.Errorf("upload session repository is required")
	}
	if strings.TrimSpace(cfg.SpoolDir) == "" {
		return nil, fmt.Errorf("oci spool dir is required")
	}
	if err := os.MkdirAll(cfg.SpoolDir, 0o755); err != nil {
		return nil, fmt.Errorf("create oci spool dir: %w", err)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OCIRegistryService{
		cfg:     cfg,
		repo:    repo,
		uploads: uploads,
		blossom: blossomClient,
		logger:  logger,
	}, nil
}

// CheckAPI returns OCI Distribution API version information.
func (s *OCIRegistryService) CheckAPI(_ context.Context) (*APIVersionResponse, error) {
	return &APIVersionResponse{DockerDistributionAPIVersion: "registry/2.0"}, nil
}

// FetchManifest fetches a manifest by tag or digest.
func (s *OCIRegistryService) FetchManifest(ctx context.Context, repoName, reference, acceptHeader string) (*ManifestResponse, error) {
	if err := s.authorizePull(ctx, repoName); err != nil {
		return nil, err
	}
	var manifest *domain.OCIManifest
	var err error
	if strings.HasPrefix(reference, "sha256:") {
		manifest, err = s.repo.GetManifestByDigest(ctx, repoName, reference)
	} else {
		manifest, err = s.repo.GetManifestByTag(ctx, repoName, reference)
	}
	if err != nil {
		return nil, fmt.Errorf("get manifest: %w", err)
	}
	if manifest == nil {
		return nil, ErrManifestNotFound
	}
	if !acceptsMediaType(acceptHeader, manifest.MediaType) {
		return nil, ErrManifestNotAcceptable
	}
	return &ManifestResponse{
		Content:             manifest.Content,
		ContentType:         manifest.MediaType,
		DockerContentDigest: manifest.Digest,
		ContentLength:       manifest.SizeBytes,
	}, nil
}

// ProxyBlobGET opens a blob stream from Blossom.
func (s *OCIRegistryService) ProxyBlobGET(ctx context.Context, repoName, digest string) (*BlobStreamResponse, error) {
	if err := s.authorizePull(ctx, repoName); err != nil {
		return nil, err
	}
	// First check blob exists in this repo
	exists, err := s.repo.BlobExistsInRepo(ctx, repoName, digest)
	if err != nil {
		return nil, fmt.Errorf("check blob exists: %w", err)
	}
	if !exists {
		return nil, ErrBlobNotFound
	}
	blob, err := s.repo.GetBlob(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("get blob: %w", err)
	}
	if blob == nil || strings.TrimSpace(blob.StorageRef) == "" {
		return nil, ErrBlobNotFound
	}
	stream, err := s.blossom.OpenStreamByURL(ctx, blob.StorageRef)
	if err != nil {
		return nil, fmt.Errorf("open blob stream: %w", err)
	}
	contentType := blob.MediaType
	if contentType == "" {
		contentType = stream.ContentType
	}
	contentLength := blob.SizeBytes
	if contentLength <= 0 {
		contentLength = stream.ContentLength
	}
	return &BlobStreamResponse{
		Stream:              stream,
		ContentType:         contentType,
		DockerContentDigest: blob.Digest,
		ContentLength:       contentLength,
	}, nil
}

// ProxyBlobHEAD checks blob existence and returns OCI-relevant headers.
func (s *OCIRegistryService) ProxyBlobHEAD(ctx context.Context, repoName, digest string) (*BlobHeadResponse, error) {
	if err := s.authorizePull(ctx, repoName); err != nil {
		return nil, err
	}
	exists, err := s.repo.BlobExistsInRepo(ctx, repoName, digest)
	if err != nil {
		return nil, fmt.Errorf("check blob exists: %w", err)
	}
	if !exists {
		return &BlobHeadResponse{Exists: false}, nil
	}
	blob, err := s.repo.GetBlob(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("get blob: %w", err)
	}
	if blob == nil || strings.TrimSpace(blob.StorageRef) == "" {
		return &BlobHeadResponse{Exists: false}, nil
	}
	head, err := s.blossom.HeadByURL(ctx, blob.StorageRef)
	if err != nil {
		return nil, fmt.Errorf("head blob: %w", err)
	}
	if !head.Exists {
		return &BlobHeadResponse{Exists: false}, nil
	}
	contentType := blob.MediaType
	if contentType == "" {
		contentType = head.ContentType
	}
	contentLength := blob.SizeBytes
	if contentLength <= 0 {
		contentLength = head.ContentLength
	}
	h := make(map[string]string)
	for k, v := range head.Header {
		if len(v) > 0 {
			h[k] = v[0]
		}
	}
	return &BlobHeadResponse{
		Exists:              true,
		ContentType:         contentType,
		DockerContentDigest: blob.Digest,
		ContentLength:       contentLength,
		Header:              h,
	}, nil
}

// ListTags lists tags in OCI tags/list response format.
func (s *OCIRegistryService) ListTags(ctx context.Context, repoName string, n int, last string) (*TagListResponse, error) {
	if err := s.authorizePull(ctx, repoName); err != nil {
		return nil, err
	}
	// Repository handles pagination internally
	tags, err := s.repo.ListTags(ctx, repoName, last, n)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return &TagListResponse{Name: repoName, Tags: tags}, nil
}

// ListReferrers lists OCI referrers optionally filtered by artifact type.
func (s *OCIRegistryService) PutManifest(ctx context.Context, repoName, reference, contentType, computedDigest string, body []byte) (string, error) {
	if err := s.AuthorizePush(ctx, repoName); err != nil {
		return "", err
	}
	if strings.TrimSpace(contentType) == "" {
		return "", ErrManifestInvalid
	}
	mediaType := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if mediaType != ociMediaTypeImageManifest && mediaType != ociMediaTypeImageIndex && mediaType != dockerV2Manifest {
		return "", ErrManifestInvalid
	}
	if strings.HasPrefix(reference, "sha256:") && !strings.EqualFold(reference, computedDigest) {
		return "", ErrManifestDigestMismatch
	}

	type descriptor struct {
		MediaType   string            `json:"mediaType"`
		Digest      string            `json:"digest"`
		Size        int64             `json:"size"`
		Annotations map[string]string `json:"annotations"`
	}
	type manifestDoc struct {
		MediaType    string            `json:"mediaType"`
		ArtifactType string            `json:"artifactType"`
		Config       descriptor        `json:"config"`
		Layers       []descriptor      `json:"layers"`
		Manifests    []descriptor      `json:"manifests"`
		Subject      *descriptor       `json:"subject"`
		Annotations  map[string]string `json:"annotations"`
	}
	var doc manifestDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", ErrManifestInvalid
	}
	if strings.TrimSpace(doc.MediaType) != "" {
		mediaType = strings.TrimSpace(doc.MediaType)
	}
	if mediaType != ociMediaTypeImageManifest && mediaType != ociMediaTypeImageIndex && mediaType != dockerV2Manifest {
		return "", ErrManifestInvalid
	}

	type blobExistenceChecker interface {
		BlobExistsInRepo(ctx context.Context, repoName, digest string) (bool, error)
	}
	checker, ok := s.repo.(blobExistenceChecker)
	if !ok {
		return "", ErrManifestInvalid
	}
	checkBlob := func(d string) error {
		d = strings.TrimSpace(d)
		if d == "" {
			return nil
		}
		exists, err := checker.BlobExistsInRepo(ctx, repoName, d)
		if err != nil {
			return err
		}
		if !exists {
			return ErrBlobUnknown
		}
		return nil
	}
	if err := checkBlob(doc.Config.Digest); err != nil {
		return "", err
	}
	for _, l := range doc.Layers {
		if err := checkBlob(l.Digest); err != nil {
			return "", err
		}
	}

	manifest := domain.OCIManifest{
		Digest:    computedDigest,
		MediaType: mediaType,
		Content:   body,
		SizeBytes: int64(len(body)),
	}
	// Determine tag (if reference is not a digest)
	var tag string
	if !strings.HasPrefix(reference, "sha256:") {
		tag = reference
	}
	// Ensure repo exists
	repo, err := s.repo.EnsureRepository(ctx, repoName)
	if err != nil {
		return "", fmt.Errorf("ensure repository: %w", err)
	}
	manifest.RepositoryID = repo.ID
	if err := s.repo.PutManifest(ctx, manifest, tag); err != nil {
		return "", fmt.Errorf("put manifest: %w", err)
	}
	return computedDigest, nil
}

func (s *OCIRegistryService) ListReferrers(ctx context.Context, repoName, digest, artifactType string) (*ReferrersResponse, error) {
	if err := s.authorizePull(ctx, repoName); err != nil {
		return nil, err
	}
	type referrerRepo interface {
		ListReferrers(ctx context.Context, repoName, subjectDigest, artifactType string) ([]domain.OCIReferrerDescriptor, error)
	}
	r, ok := s.repo.(referrerRepo)
	if !ok {
		return &ReferrersResponse{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json", Manifests: []domain.OCIReferrerDescriptor{}}, nil
	}
	manifests, err := r.ListReferrers(ctx, repoName, digest, artifactType)
	if err != nil {
		return nil, fmt.Errorf("list referrers: %w", err)
	}
	return &ReferrersResponse{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests:     manifests,
	}, nil
}

func (s *OCIRegistryService) AuthorizePush(ctx context.Context, repoName string) error {
	principal := RegistryPrincipalFromContext(ctx)
	if principal == nil {
		return ErrRegistryPushUnauthorized
	}
	if strings.EqualFold(principal.AuthType, "system") || strings.EqualFold(principal.AuthType, "basic") {
		return nil
	}
	if hasPushScope(principal.Scopes, repoName) {
		return nil
	}
	return ErrRegistryPushUnauthorized
}

func (s *OCIRegistryService) authorizePull(ctx context.Context, repoName string) error {
	if isPublicRepo(repoName) {
		return nil
	}
	principal := RegistryPrincipalFromContext(ctx)
	if principal == nil {
		return ErrRegistryUnauthorized
	}
	if strings.EqualFold(principal.AuthType, "system") {
		return nil
	}
	if hasPullScope(principal.Scopes, repoName) {
		return nil
	}
	return ErrRegistryUnauthorized
}

func isPublicRepo(repoName string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(repoName)), "public/")
}

func hasPullScope(scopes []string, repoName string) bool {
	repoName = strings.TrimSpace(repoName)
	for _, scope := range scopes {
		parts := strings.Split(scope, ":")
		if len(parts) != 3 || parts[0] != "repository" {
			continue
		}
		targetRepo := parts[1]
		actions := strings.Split(parts[2], ",")
		repoMatch := scopeRepoMatches(targetRepo, repoName)
		if !repoMatch {
			continue
		}
		for _, action := range actions {
			action = strings.TrimSpace(action)
			if action == "pull" || action == "*" || action == "push" {
				return true
			}
		}
	}
	return false
}

func hasPushScope(scopes []string, repoName string) bool {
	repoName = strings.TrimSpace(repoName)
	for _, scope := range scopes {
		parts := strings.Split(scope, ":")
		if len(parts) != 3 || parts[0] != "repository" {
			continue
		}
		targetRepo := parts[1]
		actions := strings.Split(parts[2], ",")
		repoMatch := scopeRepoMatches(targetRepo, repoName)
		if !repoMatch {
			continue
		}
		for _, action := range actions {
			action = strings.TrimSpace(action)
			if action == "push" || action == "*" {
				return true
			}
		}
	}
	return false
}

func scopeRepoMatches(targetRepo, repoName string) bool {
	targetRepo = strings.TrimSpace(targetRepo)
	repoName = strings.TrimSpace(repoName)
	if targetRepo == "*" || targetRepo == repoName {
		return true
	}
	if strings.HasSuffix(targetRepo, "/*") {
		prefix := strings.TrimSuffix(targetRepo, "/*")
		return strings.HasPrefix(repoName, prefix+"/")
	}
	return false
}

func acceptsMediaType(acceptHeader, mediaType string) bool {
	if strings.TrimSpace(mediaType) == "" {
		return true
	}
	acceptHeader = strings.TrimSpace(acceptHeader)
	if acceptHeader == "" {
		return true
	}
	for _, raw := range strings.Split(acceptHeader, ",") {
		part := strings.TrimSpace(raw)
		if part == "" {
			continue
		}
		media := part
		if idx := strings.Index(media, ";"); idx >= 0 {
			media = strings.TrimSpace(media[:idx])
		}
		if media == "*/*" || media == mediaType {
			return true
		}
		if strings.HasSuffix(media, "/*") {
			prefix := strings.TrimSuffix(media, "*")
			if strings.HasPrefix(mediaType, prefix) {
				return true
			}
		}
	}
	return false
}
