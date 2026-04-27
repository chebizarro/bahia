// Package registry provides a multi-registry OCI image inspection framework.
//
// It defines the ImageInspector interface for rich image inspection (manifests,
// referrers, tags) and provides implementations for generic OCI Distribution
// Spec v2 registries, GitHub Container Registry (ghcr.io), Docker Hub, and
// Harbor (via the existing harbor adapter).
package registry

import (
	"context"

	"github.com/openagentsinc/bahia/internal/service"
)

// ImageInspection holds detailed metadata from inspecting an image in a container registry.
// This broadens the original ImageVerification with supply-chain security fields.
type ImageInspection struct {
	Exists        bool              `json:"exists"`
	Digest        string            `json:"digest,omitempty"`
	MediaType     string            `json:"media_type,omitempty"`
	Size          int64             `json:"size,omitempty"`
	ScanStatus    string            `json:"scan_status,omitempty"`
	Signatures    []string          `json:"signatures,omitempty"`    // cosign/sigstore signature digests
	SBOMRef       string            `json:"sbom_ref,omitempty"`      // attached SBOM artifact reference
	ProvenanceRef string            `json:"provenance_ref,omitempty"` // SLSA provenance artifact reference
	Annotations   map[string]string `json:"annotations,omitempty"`   // OCI manifest annotations
}

// Referrer represents an OCI referrer (signature, SBOM, attestation, etc.)
// linked to a manifest via the OCI Referrers API.
type Referrer struct {
	Digest        string            `json:"digest"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType"`
	Size          int64             `json:"size"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// ImageInspector provides rich image inspection capabilities beyond simple
// existence checks. Implementations exist for generic OCI, GHCR, Docker Hub,
// and Harbor registries.
type ImageInspector interface {
	// InspectImage returns detailed information about an image reference.
	// repo is the full repository path (e.g. "library/nginx" or "myorg/myapp").
	// reference is a tag (e.g. "latest") or digest (e.g. "sha256:abc123...").
	InspectImage(ctx context.Context, repo, reference string) (*ImageInspection, error)

	// ListTags returns all tags for a repository.
	ListTags(ctx context.Context, repo string) ([]string, error)

	// GetReferrers returns OCI referrers (signatures, SBOMs, attestations)
	// linked to a manifest identified by its digest.
	GetReferrers(ctx context.Context, repo, digest string) ([]Referrer, error)
}

// VerifierAdapter wraps an ImageInspector to satisfy the service.ImageVerifier interface.
// This allows any registry adapter to be used with the existing RegistryService.
type VerifierAdapter struct {
	Inspector ImageInspector
}

// VerifyImage delegates to the underlying ImageInspector and maps the result
// to the simpler ImageVerification type expected by RegistryService.
func (a *VerifierAdapter) VerifyImage(ctx context.Context, imageRepo, reference string) (*service.ImageVerification, error) {
	inspection, err := a.Inspector.InspectImage(ctx, imageRepo, reference)
	if err != nil {
		return nil, err
	}
	return &service.ImageVerification{
		Exists:     inspection.Exists,
		Digest:     inspection.Digest,
		ScanStatus: inspection.ScanStatus,
	}, nil
}

// Compile-time interface checks.
var _ service.ImageVerifier = (*VerifierAdapter)(nil)
