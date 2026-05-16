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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

var ErrMLArtifactResolverUnavailable = errors.New("ML artifact resolver unavailable")

// MLArtifactResolveInput describes an external artifact reference to normalize into an MLArtifactRef.
type MLArtifactResolveInput struct {
	URI            string
	Kind           domain.MLArtifactKind
	Format         domain.MLArtifactFormat
	ExpectedSHA256 string
	ExpectedSize   int64
	ExpectedMedia  string
	ModelVersionID *uuid.UUID
	Metadata       map[string]any
}

// MLArtifactResolver normalizes artifact locations into digest-addressed MLArtifactRefs.
type MLArtifactResolver interface {
	ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error)
}

// MLArtifactResolverFunc adapts a function into an MLArtifactResolver.
type MLArtifactResolverFunc func(context.Context, MLArtifactResolveInput) (*domain.MLArtifactRef, error)

func (f MLArtifactResolverFunc) ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error) {
	return f(ctx, input)
}

// MLArtifactResolverSet dispatches resolution by URI scheme.
type MLArtifactResolverSet map[string]MLArtifactResolver

func (s MLArtifactResolverSet) ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error) {
	scheme := strings.ToLower(uriScheme(input.URI))
	resolver := s[scheme]
	if resolver == nil {
		return nil, fmt.Errorf("%w: no resolver for scheme %q", ErrMLArtifactResolverUnavailable, scheme)
	}
	return resolver.ResolveArtifact(ctx, input)
}

// NewDefaultMLArtifactResolverSet returns phase 1/2 resolvers. SeaweedFS/S3 is fail-closed unless configured.
func NewDefaultMLArtifactResolverSet(httpClient *http.Client, seaweed SeaweedFSResolverConfig) MLArtifactResolverSet {
	h := &HTTPMLArtifactResolver{Client: httpClient}
	return MLArtifactResolverSet{
		"hf":      HuggingFaceMLArtifactResolver{},
		"blossom": BlossomMLArtifactResolver{},
		"oci":     OCIMLArtifactResolver{},
		"github":  GitHubMLArtifactResolver{},
		"http":    h,
		"https":   h,
		"file":    LocalFileMLArtifactResolver{},
		"":        LocalFileMLArtifactResolver{},
		"s3":      SeaweedFSS3MLArtifactResolver{Config: seaweed},
		"seaweed": SeaweedFSS3MLArtifactResolver{Config: seaweed},
	}
}

type HuggingFaceMLArtifactResolver struct{}
type BlossomMLArtifactResolver struct{}
type OCIMLArtifactResolver struct{}
type GitHubMLArtifactResolver struct{}

type HTTPMLArtifactResolver struct{ Client *http.Client }
type LocalFileMLArtifactResolver struct{}

type SeaweedFSResolverConfig struct {
	Enabled        bool
	Endpoint       string
	AccessKeyRef   string
	SecretKeyRef   string
	AllowAnonymous bool
}

type SeaweedFSS3MLArtifactResolver struct{ Config SeaweedFSResolverConfig }

func (HuggingFaceMLArtifactResolver) ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error) {
	return remoteMetadataRef(input, "huggingface", domain.MLArtifactFormatHuggingFaceSnapshot)
}

func (BlossomMLArtifactResolver) ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error) {
	return remoteMetadataRef(input, "blossom", domain.MLArtifactFormatBlossomBlob)
}

func (OCIMLArtifactResolver) ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error) {
	return remoteMetadataRef(input, "oci", domain.MLArtifactFormatOCIArtifact)
}

func (GitHubMLArtifactResolver) ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error) {
	return remoteMetadataRef(input, "github", "")
}

func (r *HTTPMLArtifactResolver) ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error) {
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, input.URI, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP artifact metadata request failed: %s", resp.Status)
	}
	if input.ExpectedSize == 0 {
		input.ExpectedSize = resp.ContentLength
	}
	if input.ExpectedMedia == "" {
		input.ExpectedMedia = resp.Header.Get("Content-Type")
	}
	if input.ExpectedSHA256 == "" {
		input.ExpectedSHA256 = firstNonEmpty(resp.Header.Get("X-Checksum-Sha256"), resp.Header.Get("X-Content-Sha256"), resp.Header.Get("Digest"))
	}
	return remoteMetadataRef(input, "http", "")
}

func (LocalFileMLArtifactResolver) ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error) {
	path := input.URI
	if u, err := url.Parse(input.URI); err == nil && u.Scheme == "file" {
		path = u.Path
	}
	path = filepath.Clean(path)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("local artifact %s is a directory", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	input.URI = "file://" + path
	input.ExpectedSHA256 = hex.EncodeToString(h.Sum(nil))
	input.ExpectedSize = info.Size()
	if input.ExpectedMedia == "" {
		input.ExpectedMedia = mediaTypeFromExt(path)
	}
	if input.Format == "" {
		input.Format = inferFormat(path, "")
	}
	return newArtifactRef(input, "local")
}

func (r SeaweedFSS3MLArtifactResolver) ResolveArtifact(ctx context.Context, input MLArtifactResolveInput) (*domain.MLArtifactRef, error) {
	if !r.Config.Enabled || strings.TrimSpace(r.Config.Endpoint) == "" {
		return nil, fmt.Errorf("%w: SeaweedFS/S3 resolver is disabled", ErrMLArtifactResolverUnavailable)
	}
	if !r.Config.AllowAnonymous && (strings.TrimSpace(r.Config.AccessKeyRef) == "" || strings.TrimSpace(r.Config.SecretKeyRef) == "") {
		return nil, fmt.Errorf("%w: SeaweedFS/S3 credentials policy is not configured", ErrMLArtifactResolverUnavailable)
	}
	ref, err := remoteMetadataRef(input, "seaweedfs_s3", "")
	if err != nil {
		return nil, err
	}
	ref.Metadata["endpoint"] = r.Config.Endpoint
	return ref, nil
}

func remoteMetadataRef(input MLArtifactResolveInput, sourceKind string, defaultFormat domain.MLArtifactFormat) (*domain.MLArtifactRef, error) {
	if input.ExpectedSHA256 == "" {
		input.ExpectedSHA256 = digestFromURI(input.URI)
	}
	if input.ExpectedSize == 0 {
		input.ExpectedSize = sizeFromURI(input.URI)
	}
	if input.ExpectedMedia == "" {
		input.ExpectedMedia = mediaTypeFromExt(input.URI)
	}
	if input.Format == "" {
		input.Format = inferFormat(input.URI, defaultFormat)
	}
	return newArtifactRef(input, sourceKind)
}

func newArtifactRef(input MLArtifactResolveInput, sourceKind string) (*domain.MLArtifactRef, error) {
	if strings.TrimSpace(input.ExpectedSHA256) == "" {
		return nil, fmt.Errorf("%w: artifact %s has no sha256 digest", ErrMLProvenanceFailedClosed, input.URI)
	}
	kind := input.Kind
	if kind == "" {
		kind = domain.MLArtifactKindModel
	}
	metadata := map[string]any{}
	for k, v := range input.Metadata {
		metadata[k] = v
	}
	artifact := &domain.MLArtifactRef{
		ID:             uuid.New(),
		ModelVersionID: input.ModelVersionID,
		Kind:           kind,
		Format:         input.Format,
		URI:            input.URI,
		SHA256:         normalizeSHA256(input.ExpectedSHA256),
		SizeBytes:      input.ExpectedSize,
		MediaType:      input.ExpectedMedia,
		Source:         &domain.MLSourceRef{Kind: sourceKind, URI: input.URI, Metadata: metadata},
		Metadata:       metadata,
		CreatedAt:      time.Now().UTC(),
	}
	if err := domain.ValidateMLArtifactRef(artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

func uriScheme(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme
}

func digestFromURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, key := range []string{"sha256", "digest", "checksum"} {
		if v := q.Get(key); v != "" {
			return v
		}
	}
	if strings.Contains(u.Fragment, "sha256=") {
		vals, _ := url.ParseQuery(u.Fragment)
		return vals.Get("sha256")
	}
	if strings.HasPrefix(u.Fragment, "sha256:") {
		return u.Fragment
	}
	if i := strings.LastIndex(raw, "@sha256:"); i >= 0 {
		return raw[i+1:]
	}
	return ""
}

func sizeFromURI(raw string) int64 {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	v := u.Query().Get("size")
	if v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func inferFormat(raw string, fallback domain.MLArtifactFormat) domain.MLArtifactFormat {
	ext := strings.ToLower(filepath.Ext(uriPathForExt(raw)))
	switch ext {
	case ".safetensors":
		return domain.MLArtifactFormatSafeTensors
	case ".gguf":
		return domain.MLArtifactFormatGGUF
	case ".onnx":
		return domain.MLArtifactFormatONNX
	case ".rknn":
		return domain.MLArtifactFormatRKNN
	case ".engine", ".plan":
		return domain.MLArtifactFormatTensorRTEngine
	case ".tflite":
		return domain.MLArtifactFormatTFLite
	}
	if fallback != "" {
		return fallback
	}
	return domain.MLArtifactFormatHuggingFaceSnapshot
}

func uriPathForExt(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		if u.Path != "" {
			return strings.TrimSuffix(u.Path, "/")
		}
	}
	return strings.TrimSuffix(raw, "/")
}

func mediaTypeFromExt(raw string) string {
	switch strings.ToLower(filepath.Ext(uriPathForExt(raw))) {
	case ".safetensors":
		return "application/x-safetensors"
	case ".gguf":
		return "application/x-gguf"
	case ".onnx":
		return "application/onnx"
	case ".rknn":
		return "application/x-rknn"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
