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

// SBOMSubjectType identifies the Bahia resource type described by an SBOM.
type SBOMSubjectType string

const (
	SBOMSubjectArtifact   SBOMSubjectType = "artifact"
	SBOMSubjectDeployment SBOMSubjectType = "deployment"
	SBOMSubjectPackage    SBOMSubjectType = "package"
	SBOMSubjectRepository SBOMSubjectType = "repository"
)

// SBOMSubject identifies the resource and immutable version described by an SBOM.
type SBOMSubject struct {
	Type        SBOMSubjectType `json:"type"`
	ID          string          `json:"id"`
	DisplayName string          `json:"display_name,omitempty"`
	Digest      string          `json:"digest"`
}

// SBOMSubjectLocator carries canonical immutable lookup fields for ContextVM SBOM requests
// when subject.digest is intentionally derived from Bahia projections or repository revision data.
type SBOMSubjectLocator struct {
	Package    *SBOMPackageArtifactLocator `json:"package,omitempty"`
	Repository *SBOMRepositoryLocator      `json:"repository,omitempty"`
}

// SBOMPackageArtifactLocator identifies one immutable package artifact projection.
type SBOMPackageArtifactLocator struct {
	RepositoryID string `json:"repository_id"`
	Namespace    string `json:"namespace,omitempty"`
	PackageName  string `json:"package_name"`
	Version      string `json:"version"`
	Filename     string `json:"filename"`
	SHA256       string `json:"sha256"`
}

// SBOMRepositoryLocator identifies one immutable repository revision or content snapshot.
type SBOMRepositoryLocator struct {
	RepositoryURL string `json:"repository_url,omitempty"`
	Repository    string `json:"repository,omitempty"`
	Commit        string `json:"commit,omitempty"`
	ContentDigest string `json:"content_digest,omitempty"`
}

// SBOMPublishState tracks whether canonical Nostr observables were published.
type SBOMPublishState string

const (
	SBOMPublishDraft     SBOMPublishState = "draft"
	SBOMPublishPublished SBOMPublishState = "published"
	SBOMPublishFailed    SBOMPublishState = "failed"
)

// SBOMSourceKind records how the SBOM payload entered Bahia.
type SBOMSourceKind string

const (
	SBOMSourceGenerated SBOMSourceKind = "generated"
	SBOMSourceImported  SBOMSourceKind = "imported"
	SBOMSourceExternal  SBOMSourceKind = "external"
)

// SBOMManifest stores a subject-neutral SBOM projection. Canonical truth remains the
// Nostr 30078 reference event and 30004 availability list.
type SBOMManifest struct {
	ID                  uuid.UUID        `json:"id"`
	Subject             SBOMSubject      `json:"subject"`
	Format              SBOMFormat       `json:"format"`
	MediaType           string           `json:"media_type,omitempty"`
	StorageType         SBOMStorageType  `json:"storage_type"`
	StorageURI          string           `json:"storage_uri"`
	PayloadSHA256       string           `json:"payload_sha256"`
	Generator           SBOMGenerator    `json:"generator"`
	PackageCount        int              `json:"package_count"`
	VulnerabilityCount  int              `json:"vulnerability_count"`
	CriticalCount       int              `json:"critical_count"`
	HighCount           int              `json:"high_count"`
	NTIAStatus          string           `json:"ntia_status,omitempty"`
	NTIA                *NTIACompliance  `json:"ntia,omitempty"`
	ReferenceEventID    string           `json:"reference_event_id,omitempty"`
	ReferenceDTag       string           `json:"reference_d_tag,omitempty"`
	AvailabilityEventID string           `json:"availability_event_id,omitempty"`
	AvailabilityDTag    string           `json:"availability_d_tag,omitempty"`
	PublishState        SBOMPublishState `json:"publish_state"`
	PublishError        string           `json:"publish_error,omitempty"`
	SourceKind          SBOMSourceKind   `json:"source_kind"`
	Metadata            map[string]any   `json:"metadata,omitempty"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
	PublishedAt         *time.Time       `json:"published_at,omitempty"`
}

// SBOMManifestPackage represents a package indexed for a subject-neutral SBOM manifest.
type SBOMManifestPackage struct {
	ID         uuid.UUID `json:"id"`
	ManifestID uuid.UUID `json:"manifest_id"`
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Ecosystem  string    `json:"ecosystem,omitempty"`
	License    string    `json:"license,omitempty"`
	PURL       string    `json:"purl,omitempty"`
	CPE        string    `json:"cpe,omitempty"`
}

// ArtifactSBOM stores a parsed SBOM record for an artifact.
type ArtifactSBOM struct {
	ID                 uuid.UUID      `json:"id"`
	ArtifactID         uuid.UUID      `json:"artifact_id"`
	Format             SBOMFormat     `json:"format"`
	SourceURL          string         `json:"source_url,omitempty"` // Blossom or OCI referrer
	PackageCount       int            `json:"package_count"`
	VulnerabilityCount int            `json:"vulnerability_count"`
	CriticalCount      int            `json:"critical_count"`
	HighCount          int            `json:"high_count"`
	RawHash            string         `json:"raw_hash,omitempty"` // SHA-256 of raw SBOM
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
	PURL      string    `json:"purl,omitempty"` // Package URL (pkg:type/name@version)
	CPE       string    `json:"cpe,omitempty"`  // Common Platform Enumeration
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

// --- SBOM Availability List Types (NIP-51 lists) ---

// SBOMIndexEntry represents a single SBOM reference in an availability list.
type SBOMIndexEntry struct {
	// SubjectDigest is the artifact digest this SBOM describes.
	SubjectDigest string `json:"subjectDigest"`
	// AttestationID is the Nostr event ID or addressable coordinate of the reference event.
	AttestationID string `json:"attestationId,omitempty"`
	// ReferenceDTag is the d tag of the canonical 30078 reference event.
	ReferenceDTag string `json:"referenceDTag,omitempty"`
	// ReferencePubkey is the pubkey that published the referenced 30078 addressable event.
	ReferencePubkey string `json:"referencePubkey,omitempty"`
	// Format is the SBOM format.
	Format SBOMFormat `json:"format"`
	// LocationURI is where to fetch the SBOM.
	LocationURI string `json:"locationUri"`
	// StorageType is the backend type.
	StorageType SBOMStorageType `json:"storageType"`
	// PayloadSHA256 is the SHA-256 digest of the externally stored SBOM payload.
	PayloadSHA256 string `json:"payloadSha256,omitempty"`
	// GeneratorID identifies the SBOM generator.
	GeneratorID string `json:"generatorId,omitempty"`
	// Timestamp is when this entry was added.
	Timestamp time.Time `json:"timestamp"`
}

// SBOMIndex is a NIP-51 availability list of SBOMs for a subject.
// Published canonically as kind 30004; historical kind 30079 is read-only legacy data.
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
