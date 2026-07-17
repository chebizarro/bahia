// Package sbom provides SBOM parsing, attestation, and indexing for SPDX and CycloneDX formats.
package sbom

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/domain"
)

// StorageResolver fetches SBOM payloads from multiple backend types.
// It does NOT store SBOMs in Nostr - only resolves references to external storage.
type StorageResolver struct {
	blossom        BlossomClient
	ociClient      OCIReferrerClient
	packageBackend PackageBackendClient
	logger         *slog.Logger
}

// BlossomClient is the interface for Blossom blob operations.
type BlossomClient interface {
	Download(ctx context.Context, sha256Hash string) ([]byte, error)
	Upload(ctx context.Context, data []byte, contentType string) (*blossom.BlobDescriptor, error)
}

// OCIReferrerClient is the interface for OCI referrer operations.
type OCIReferrerClient interface {
	GetReferrerBlob(ctx context.Context, repo, digest string) ([]byte, error)
}

// PackageBackendClient is the interface for package backend operations.
type PackageBackendClient interface {
	GetArtifactBlob(ctx context.Context, reference string) ([]byte, error)
}

// StorageConfig holds storage resolver configuration.
type StorageConfig struct {
	PreferredBackend domain.SBOMStorageType
}

// NewStorageResolver creates a new multi-backend storage resolver.
func NewStorageResolver(
	blossom BlossomClient,
	ociClient OCIReferrerClient,
	packageBackend PackageBackendClient,
	logger *slog.Logger,
) *StorageResolver {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &StorageResolver{
		blossom:        blossom,
		ociClient:      ociClient,
		packageBackend: packageBackend,
		logger:         logger,
	}
}

// ResolveInput contains input for resolving an SBOM location.
type ResolveInput struct {
	Location domain.SBOMLocation
	// For OCI: the repository name
	OCIRepo string
}

// Resolve fetches SBOM data from the specified location.
func (r *StorageResolver) Resolve(ctx context.Context, input ResolveInput) ([]byte, error) {
	loc := input.Location

	switch loc.Type {
	case domain.SBOMStorageBlossom:
		return r.resolveFromBlossom(ctx, loc.URI)

	case domain.SBOMStorageOCI:
		if input.OCIRepo == "" {
			return nil, fmt.Errorf("OCI repository required for OCI referrer resolution")
		}
		return r.resolveFromOCI(ctx, input.OCIRepo, loc.URI)

	case domain.SBOMStoragePackage:
		return r.resolveFromPackageBackend(ctx, loc.URI)

	default:
		return nil, fmt.Errorf("unsupported storage type: %s", loc.Type)
	}
}

// resolveFromBlossom fetches an SBOM from a Blossom server.
func (r *StorageResolver) resolveFromBlossom(ctx context.Context, uri string) ([]byte, error) {
	if r.blossom == nil {
		return nil, fmt.Errorf("blossom client not configured")
	}

	// Validate the URI shape before delegating the full URL to the Blossom client.
	// The client verifies the payload hash after download using the hash embedded
	// in the canonical Blossom URL.
	if _, err := extractBlossomHash(uri); err != nil {
		return nil, fmt.Errorf("invalid Blossom URI: %w", err)
	}

	data, err := r.blossom.Download(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("downloading from Blossom: %w", err)
	}

	r.logger.Debug("resolved SBOM from Blossom",
		slog.String("uri", uri),
		slog.Int("size", len(data)),
	)

	return data, nil
}

// resolveFromOCI fetches an SBOM from OCI referrers.
func (r *StorageResolver) resolveFromOCI(ctx context.Context, repo, digest string) ([]byte, error) {
	if r.ociClient == nil {
		return nil, fmt.Errorf("OCI client not configured")
	}

	data, err := r.ociClient.GetReferrerBlob(ctx, repo, digest)
	if err != nil {
		return nil, fmt.Errorf("fetching OCI referrer: %w", err)
	}

	r.logger.Debug("resolved SBOM from OCI referrer",
		slog.String("repo", repo),
		slog.String("digest", digest),
		slog.Int("size", len(data)),
	)

	return data, nil
}

// resolveFromPackageBackend fetches an SBOM from a package backend.
func (r *StorageResolver) resolveFromPackageBackend(ctx context.Context, reference string) ([]byte, error) {
	if r.packageBackend == nil {
		return nil, fmt.Errorf("package backend client not configured")
	}

	data, err := r.packageBackend.GetArtifactBlob(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("fetching from package backend: %w", err)
	}

	r.logger.Debug("resolved SBOM from package backend",
		slog.String("reference", reference),
		slog.Int("size", len(data)),
	)

	return data, nil
}

// StoreInput contains input for storing an SBOM.
type StoreInput struct {
	Data        []byte
	Format      domain.SBOMFormat
	BackendType domain.SBOMStorageType
	// For OCI: the repository and subject digest
	OCIRepo          string
	OCISubjectDigest string
}

// StoreResult contains the result of storing an SBOM.
type StoreResult struct {
	Location domain.SBOMLocation
	Hash     string
}

// Store uploads SBOM data to the specified backend.
func (r *StorageResolver) Store(ctx context.Context, input StoreInput) (*StoreResult, error) {
	switch input.BackendType {
	case domain.SBOMStorageBlossom:
		return r.storeToBlossom(ctx, input.Data, input.Format)

	case domain.SBOMStorageOCI:
		// Real SBOM generation/import stores canonical payload bytes on Blossom.
		// OCI referrer writes are intentionally outside this storage path.
		return nil, fmt.Errorf("direct OCI SBOM storage is unsupported by this path; store generated/imported SBOM payloads on Blossom")

	case domain.SBOMStoragePackage:
		return nil, fmt.Errorf("direct package backend SBOM storage is unsupported by this path; store generated/imported SBOM payloads on Blossom")

	default:
		return nil, fmt.Errorf("unsupported storage backend: %s", input.BackendType)
	}
}

// storeToBlossom uploads SBOM data to Blossom.
func (r *StorageResolver) storeToBlossom(ctx context.Context, data []byte, format domain.SBOMFormat) (*StoreResult, error) {
	if r.blossom == nil {
		return nil, fmt.Errorf("blossom client not configured")
	}

	mediaType := MediaTypeForFormat(format)
	desc, err := r.blossom.Upload(ctx, data, mediaType)
	if err != nil {
		return nil, fmt.Errorf("uploading to Blossom: %w", err)
	}
	if desc == nil {
		return nil, fmt.Errorf("uploading to Blossom: empty blob descriptor")
	}

	expectedHash := hashData(data)
	descriptorHash, err := validateSHA256Hex(desc.SHA256)
	if err != nil {
		return nil, fmt.Errorf("invalid Blossom descriptor hash: %w", err)
	}
	if !strings.EqualFold(descriptorHash, expectedHash) {
		return nil, fmt.Errorf("Blossom descriptor hash %s does not match uploaded payload SHA-256 %s", descriptorHash, expectedHash)
	}
	urlHash, err := extractBlossomHash(desc.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid Blossom descriptor URL: %w", err)
	}
	if !strings.EqualFold(urlHash, expectedHash) {
		return nil, fmt.Errorf("Blossom descriptor URL hash %s does not match uploaded payload SHA-256 %s", urlHash, expectedHash)
	}
	if desc.Size != int64(len(data)) {
		return nil, fmt.Errorf("Blossom descriptor size %d does not match uploaded payload size %d", desc.Size, len(data))
	}

	r.logger.Debug("stored SBOM to Blossom",
		slog.String("url", desc.URL),
		slog.String("sha256", expectedHash),
	)

	return &StoreResult{
		Location: domain.SBOMLocation{
			Type:      domain.SBOMStorageBlossom,
			URI:       desc.URL,
			MediaType: mediaType,
		},
		Hash: expectedHash,
	}, nil
}

// extractBlossomHash extracts the SHA256 hash from a Blossom URL.
func extractBlossomHash(uri string) (string, error) {
	// Blossom URLs: https://server.com/{sha256}[.ext]
	lastSlash := strings.LastIndex(uri, "/")
	if lastSlash == -1 {
		return "", fmt.Errorf("invalid URL format")
	}

	hashPart := uri[lastSlash+1:]

	// Remove extension if present.
	if dot := strings.LastIndex(hashPart, "."); dot != -1 {
		hashPart = hashPart[:dot]
	}

	return validateSHA256Hex(hashPart)
}

func validateSHA256Hex(value string) (string, error) {
	if len(value) != 64 {
		return "", fmt.Errorf("invalid hash length: %d", len(value))
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("invalid SHA-256 hex: %w", err)
	}
	return strings.ToLower(value), nil
}

// ResolveAndVerify fetches and verifies an SBOM against its attestation.
func (r *StorageResolver) ResolveAndVerify(ctx context.Context, att *domain.SBOMAttestation, input ResolveInput) ([]byte, error) {
	if err := VerifyAttestationSignature(att, ""); err != nil {
		return nil, fmt.Errorf("verify SBOM attestation signature: %w", err)
	}
	data, err := r.Resolve(ctx, input)
	if err != nil {
		return nil, err
	}

	// Verify the payload hash matches the attestation.
	if !VerifyPayloadDigest(att, data) {
		return nil, fmt.Errorf("SBOM payload hash mismatch")
	}

	return data, nil
}

// MockBlossomClient is a mock implementation for testing.
type MockBlossomClient struct {
	Blobs map[string][]byte
}

func (m *MockBlossomClient) Download(ctx context.Context, sha256Hash string) ([]byte, error) {
	key := sha256Hash
	if strings.Contains(sha256Hash, "://") {
		hash, err := extractBlossomHash(sha256Hash)
		if err != nil {
			return nil, err
		}
		key = hash
	}
	data, ok := m.Blobs[key]
	if !ok {
		return nil, fmt.Errorf("blob not found: %s", sha256Hash)
	}
	return data, nil
}

func (m *MockBlossomClient) Upload(ctx context.Context, data []byte, contentType string) (*blossom.BlobDescriptor, error) {
	hash := blossom.ComputeSHA256(data)
	m.Blobs[hash] = data
	return &blossom.BlobDescriptor{
		URL:    fmt.Sprintf("https://blossom.example.com/%s", hash),
		SHA256: hash,
		Size:   int64(len(data)),
		Type:   contentType,
	}, nil
}

// Ensure interfaces are satisfied at compile time.
var _ BlossomClient = (*MockBlossomClient)(nil)
var _ io.Closer = (io.Closer)(nil) // Just to have an import use
