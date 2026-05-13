package domain

import (
	"time"

	"github.com/google/uuid"
)

// SBOMFormat identifies the SBOM standard format.
type SBOMFormat string

const (
	SBOMFormatSPDX      SBOMFormat = "spdx"
	SBOMFormatCycloneDX SBOMFormat = "cyclonedx"
)

// ArtifactSBOM stores a parsed SBOM record for an artifact.
type ArtifactSBOM struct {
	ID                 uuid.UUID      `json:"id"`
	ArtifactID         uuid.UUID      `json:"artifact_id"`
	Format             SBOMFormat     `json:"format"`
	SourceURL          string         `json:"source_url,omitempty"`    // Blossom or OCI referrer
	PackageCount       int            `json:"package_count"`
	VulnerabilityCount int            `json:"vulnerability_count"`
	CriticalCount      int            `json:"critical_count"`
	HighCount          int            `json:"high_count"`
	RawHash            string         `json:"raw_hash,omitempty"`     // SHA-256 of raw SBOM
	Metadata           map[string]any `json:"metadata,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
}

// SBOMPackage represents a single package entry within an SBOM.
type SBOMPackage struct {
	ID        uuid.UUID `json:"id"`
	SBOMID    uuid.UUID `json:"sbom_id"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Ecosystem string    `json:"ecosystem,omitempty"` // e.g. "npm", "go", "pypi"
	License   string    `json:"license,omitempty"`
	PURL      string    `json:"purl,omitempty"`      // Package URL (pkg:type/name@version)
	CPE       string    `json:"cpe,omitempty"`       // Common Platform Enumeration
}

// HasVulnerabilities returns true if the SBOM has any known vulnerabilities.
func (s *ArtifactSBOM) HasVulnerabilities() bool {
	return s.VulnerabilityCount > 0
}

// HasCriticalVulnerabilities returns true if the SBOM has critical-severity issues.
func (s *ArtifactSBOM) HasCriticalVulnerabilities() bool {
	return s.CriticalCount > 0
}

// --- SBOM Attestation Types (in-toto/SLSA-style) ---

// SBOMAttestationType identifies the attestation predicate type.
type SBOMAttestationType string

const (
	// AttestationTypeSPDX is the predicate type for SPDX SBOM attestations.
	AttestationTypeSPDX SBOMAttestationType = "https://spdx.dev/Document"
	// AttestationTypeCycloneDX is the predicate type for CycloneDX SBOM attestations.
	AttestationTypeCycloneDX SBOMAttestationType = "https://cyclonedx.org/bom"
)

// SBOMAttestation wraps an SBOM reference in an in-toto/SLSA-style attestation envelope.
// The actual SBOM payload is stored externally (Blossom/OCI/package backend);
// Nostr events only carry this lightweight attestation referencing the payload.
type SBOMAttestation struct {
	// Type is the attestation statement type (always "https://in-toto.io/Statement/v1").
	Type string `json:"_type"`
	// Subject identifies what the SBOM describes (artifact digest).
	Subject []AttestationSubject `json:"subject"`
	// PredicateType identifies the SBOM format.
	PredicateType SBOMAttestationType `json:"predicateType"`
	// Predicate contains the SBOM reference metadata (not the full SBOM).
	Predicate SBOMPredicate `json:"predicate"`
}

// AttestationSubject identifies an artifact by name and digest.
type AttestationSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"` // e.g. {"sha256": "abc123..."}
}

// SBOMPredicate contains metadata about the SBOM and where to fetch it.
// This follows SLSA provenance patterns but for SBOM references.
type SBOMPredicate struct {
	// Format is the SBOM format (spdx, cyclonedx).
	Format SBOMFormat `json:"format"`
	// Location is where the actual SBOM payload is stored.
	Location SBOMLocation `json:"location"`
	// Digest is the hash of the SBOM payload for integrity verification.
	Digest map[string]string `json:"digest"`
	// Generator identifies what tool created the SBOM.
	Generator SBOMGenerator `json:"generator,omitempty"`
	// Timestamp is when the SBOM was generated.
	Timestamp time.Time `json:"timestamp,omitempty"`
	// NTIA contains NTIA minimum elements compliance metadata.
	NTIA *NTIACompliance `json:"ntia,omitempty"`
}

// SBOMLocation specifies where to retrieve the SBOM payload.
type SBOMLocation struct {
	// Type is the storage backend type.
	Type SBOMStorageType `json:"type"`
	// URI is the location-specific reference.
	// For Blossom: the blob URL (https://blossom.example.com/{sha256})
	// For OCI: the referrer digest (sha256:...)
	// For Package: the package artifact reference
	URI string `json:"uri"`
	// MediaType is the SBOM content type.
	MediaType string `json:"mediaType,omitempty"`
}

// SBOMStorageType identifies where an SBOM payload is stored.
type SBOMStorageType string

const (
	SBOMStorageBlossom SBOMStorageType = "blossom"
	SBOMStorageOCI     SBOMStorageType = "oci-referrer"
	SBOMStoragePackage SBOMStorageType = "package-backend"
)

// SBOMGenerator identifies the tool that created the SBOM.
type SBOMGenerator struct {
	// ID is the generator identifier (e.g., "syft", "trivy", "cdxgen").
	ID string `json:"id"`
	// Version is the generator version.
	Version string `json:"version,omitempty"`
	// Pubkey is the Nostr pubkey of the trusted generator (for policy checks).
	Pubkey string `json:"pubkey,omitempty"`
}

// NTIACompliance tracks NTIA "Minimum Elements" compliance.
// See: https://www.ntia.gov/files/ntia/publications/sbom_minimum_elements_report.pdf
type NTIACompliance struct {
	// HasSupplierName indicates supplier name is present.
	HasSupplierName bool `json:"hasSupplierName"`
	// HasComponentName indicates component name is present.
	HasComponentName bool `json:"hasComponentName"`
	// HasComponentVersion indicates version is present.
	HasComponentVersion bool `json:"hasComponentVersion"`
	// HasUniqueID indicates unique identifier (PURL/CPE) is present.
	HasUniqueID bool `json:"hasUniqueID"`
	// HasRelationship indicates dependency relationships are present.
	HasRelationship bool `json:"hasRelationship"`
	// HasAuthor indicates SBOM author is identified.
	HasAuthor bool `json:"hasAuthor"`
	// HasTimestamp indicates timestamp is present.
	HasTimestamp bool `json:"hasTimestamp"`
	// IsCompliant is true if all minimum elements are present.
	IsCompliant bool `json:"isCompliant"`
}

// --- SBOM Index Types (NIP-51-style lists) ---

// SBOMIndexEntry represents a single SBOM reference in an index list.
type SBOMIndexEntry struct {
	// SubjectDigest is the artifact digest this SBOM describes.
	SubjectDigest string `json:"subjectDigest"`
	// AttestationID is the Nostr event ID of the attestation.
	AttestationID string `json:"attestationId,omitempty"`
	// Format is the SBOM format.
	Format SBOMFormat `json:"format"`
	// LocationURI is where to fetch the SBOM.
	LocationURI string `json:"locationUri"`
	// StorageType is the backend type.
	StorageType SBOMStorageType `json:"storageType"`
	// GeneratorID identifies the SBOM generator.
	GeneratorID string `json:"generatorId,omitempty"`
	// Timestamp is when this entry was added.
	Timestamp time.Time `json:"timestamp"`
}

// SBOMIndex is a NIP-51-style parameterized list of SBOMs for a subject.
// Published as a replaceable Nostr event (kind 30078 with d-tag).
type SBOMIndex struct {
	// SubjectType indicates what the index is for (artifact, service, deployment).
	SubjectType string `json:"subjectType"`
	// SubjectID is the identifier of the subject.
	SubjectID string `json:"subjectId"`
	// Entries are the SBOM references in this index.
	Entries []SBOMIndexEntry `json:"entries"`
	// UpdatedAt is when the index was last modified.
	UpdatedAt time.Time `json:"updatedAt"`
}
