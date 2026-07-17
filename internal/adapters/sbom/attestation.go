// Package sbom provides SBOM parsing, attestation, and indexing for SPDX and CycloneDX formats.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	// InTotoStatementType is the standard in-toto statement type URI.
	InTotoStatementType = "https://in-toto.io/Statement/v1"

	// MediaTypeSPDX is the IANA media type for SPDX JSON.
	MediaTypeSPDX = "application/spdx+json"
	// MediaTypeCycloneDX is the IANA media type for CycloneDX JSON.
	MediaTypeCycloneDX = "application/vnd.cyclonedx+json"
)

// AttestationBuilder creates in-toto/SLSA-style attestations for SBOMs.
type AttestationBuilder struct {
	defaultGenerator domain.SBOMGenerator
}

// NewAttestationBuilder creates a new attestation builder.
func NewAttestationBuilder(generatorID, generatorVersion, generatorPubkey string) *AttestationBuilder {
	return &AttestationBuilder{
		defaultGenerator: domain.SBOMGenerator{
			ID:      generatorID,
			Version: generatorVersion,
			Pubkey:  generatorPubkey,
		},
	}
}

// BuildAttestationInput contains the input for building an SBOM attestation.
type BuildAttestationInput struct {
	// Subject identifies the Bahia resource described by the SBOM. When set, it supplies the attestation subject name and digest.
	Subject *domain.SBOMSubject
	// SubjectName is the name of the artifact (e.g., "ghcr.io/org/image:tag").
	SubjectName string
	// SubjectDigest is the artifact's content digest (e.g., "sha256:abc123...").
	SubjectDigest string
	// SBOMData is the raw SBOM content.
	SBOMData []byte
	// Format is the detected SBOM format.
	Format domain.SBOMFormat
	// Location specifies where the SBOM payload is stored.
	Location domain.SBOMLocation
	// Generator overrides the default generator (optional).
	Generator *domain.SBOMGenerator
	// Timestamp overrides the current time (optional).
	Timestamp *time.Time
	// ParsedPackages are the packages extracted from the SBOM (for NTIA check).
	ParsedPackages []domain.SBOMPackage
}

// BuildAttestation creates an in-toto/SLSA-style attestation for an SBOM.
func (b *AttestationBuilder) BuildAttestation(input BuildAttestationInput) (*domain.SBOMAttestation, error) {
	subjectName, subjectDigest, err := attestationSubject(input)
	if err != nil {
		return nil, err
	}
	if len(input.SBOMData) == 0 {
		return nil, fmt.Errorf("SBOM data is required")
	}

	// Parse subject digest into algorithm:hash format.
	digestAlgo, digestHash, err := parseDigest(subjectDigest)
	if err != nil {
		return nil, fmt.Errorf("invalid subject digest: %w", err)
	}

	// Compute SBOM payload hash.
	sbomHash := sha256.Sum256(input.SBOMData)
	sbomHashHex := hex.EncodeToString(sbomHash[:])

	// Determine predicate type from format.
	var predicateType domain.SBOMAttestationType
	switch input.Format {
	case domain.SBOMFormatSPDX:
		predicateType = domain.AttestationTypeSPDX
	case domain.SBOMFormatCycloneDX:
		predicateType = domain.AttestationTypeCycloneDX
	default:
		return nil, fmt.Errorf("unsupported SBOM format: %s", input.Format)
	}

	// Use provided generator or default.
	generator := b.defaultGenerator
	if input.Generator != nil {
		generator = *input.Generator
	}

	// Use provided timestamp or now.
	timestamp := time.Now().UTC()
	if input.Timestamp != nil {
		timestamp = *input.Timestamp
	}

	// Check NTIA compliance.
	ntia := checkNTIACompliance(input.SBOMData, input.ParsedPackages)

	attestation := &domain.SBOMAttestation{
		Type: InTotoStatementType,
		Subject: []domain.AttestationSubject{
			{
				Name: subjectName,
				Digest: map[string]string{
					digestAlgo: digestHash,
				},
			},
		},
		PredicateType: predicateType,
		Predicate: domain.SBOMPredicate{
			Format:   input.Format,
			Location: input.Location,
			Digest: map[string]string{
				"sha256": sbomHashHex,
			},
			Generator: generator,
			Timestamp: timestamp,
			NTIA:      ntia,
		},
	}

	return attestation, nil
}

// SerializeAttestation serializes an attestation to JSON.
func SerializeAttestation(att *domain.SBOMAttestation) ([]byte, error) {
	return json.Marshal(att)
}

// ParseAttestation deserializes an attestation from JSON.
func ParseAttestation(data []byte) (*domain.SBOMAttestation, error) {
	var att domain.SBOMAttestation
	if err := json.Unmarshal(data, &att); err != nil {
		return nil, fmt.Errorf("parsing attestation: %w", err)
	}
	if att.Type != InTotoStatementType {
		return nil, fmt.Errorf("invalid attestation type: %s", att.Type)
	}
	if err := VerifyAttestationSignature(&att, ""); err != nil {
		return nil, fmt.Errorf("verify attestation signature: %w", err)
	}
	return &att, nil
}

// VerifySubjectDigest checks that the attestation's subject matches the expected digest.
func VerifySubjectDigest(att *domain.SBOMAttestation, expectedDigest string) bool {
	if att == nil || len(att.Subject) == 0 {
		return false
	}

	algo, hash, err := parseDigest(expectedDigest)
	if err != nil {
		return false
	}

	for _, subject := range att.Subject {
		if subjectHash, ok := subject.Digest[algo]; ok {
			if strings.EqualFold(subjectHash, hash) {
				return true
			}
		}
	}
	return false
}

// VerifySBOMSubjectDigest checks that the attestation's subject matches the expected Bahia SBOM subject digest.
func VerifySBOMSubjectDigest(att *domain.SBOMAttestation, subject domain.SBOMSubject) bool {
	if err := validateSBOMSubject(subject); err != nil {
		return false
	}
	return VerifySubjectDigest(att, subject.Digest)
}

// VerifyPayloadDigest checks that the SBOM data matches the attestation's payload digest.
func VerifyPayloadDigest(att *domain.SBOMAttestation, sbomData []byte) bool {
	if att == nil || len(sbomData) == 0 {
		return false
	}

	expectedHash, ok := att.Predicate.Digest["sha256"]
	if !ok {
		return false
	}

	actualHash := sha256.Sum256(sbomData)
	actualHashHex := hex.EncodeToString(actualHash[:])

	return strings.EqualFold(expectedHash, actualHashHex)
}

func attestationSubject(input BuildAttestationInput) (name, digest string, err error) {
	name = input.SubjectName
	digest = input.SubjectDigest
	if input.Subject == nil {
		if digest == "" {
			return "", "", fmt.Errorf("subject digest is required")
		}
		return name, digest, nil
	}

	if err := validateSBOMSubject(*input.Subject); err != nil {
		return "", "", err
	}
	if digest != "" && !strings.EqualFold(digest, input.Subject.Digest) {
		return "", "", fmt.Errorf("subject digest %q does not match SBOM subject digest %q", digest, input.Subject.Digest)
	}
	if input.Subject.DisplayName != "" {
		name = input.Subject.DisplayName
	} else {
		name = input.Subject.ID
	}
	digest = input.Subject.Digest
	return name, digest, nil
}

// parseDigest splits and validates "algo:hash" into components.
func parseDigest(digest string) (algo, hash string, err error) {
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("digest must be in 'algo:hash' format")
	}
	algo = strings.TrimSpace(parts[0])
	hash = strings.TrimSpace(parts[1])
	if algo == "" || hash == "" {
		return "", "", fmt.Errorf("digest algorithm and value are required")
	}
	switch algo {
	case "sha256":
		if len(hash) != sha256.Size*2 {
			return "", "", fmt.Errorf("sha256 digest must be %d hex characters", sha256.Size*2)
		}
		if _, decodeErr := hex.DecodeString(hash); decodeErr != nil {
			return "", "", fmt.Errorf("sha256 digest must be hex: %w", decodeErr)
		}
	case "git":
		if len(hash) != 40 && len(hash) != sha256.Size*2 {
			return "", "", fmt.Errorf("git digest must be a 40 or 64 character commit hash")
		}
		if _, decodeErr := hex.DecodeString(hash); decodeErr != nil {
			return "", "", fmt.Errorf("git digest must be hex: %w", decodeErr)
		}
	}
	return algo, hash, nil
}

// checkNTIACompliance evaluates NTIA minimum elements compliance.
func checkNTIACompliance(sbomData []byte, packages []domain.SBOMPackage) *domain.NTIACompliance {
	ntia := &domain.NTIACompliance{}

	// Parse SBOM metadata for document-level checks.
	var doc struct {
		// SPDX fields
		CreationInfo struct {
			Created  string   `json:"created"`
			Creators []string `json:"creators"`
		} `json:"creationInfo"`
		Packages []struct {
			Supplier string `json:"supplier"`
		} `json:"packages"`
		Relationships []any `json:"relationships"`

		// CycloneDX fields
		Metadata struct {
			Timestamp string `json:"timestamp"`
			Authors   []any  `json:"authors"`
			Supplier  struct {
				Name string `json:"name"`
			} `json:"supplier"`
		} `json:"metadata"`
		Components   []any `json:"components"`
		Dependencies []any `json:"dependencies"`
	}
	_ = json.Unmarshal(sbomData, &doc)

	// Check timestamp (SPDX: creationInfo.created, CycloneDX: metadata.timestamp)
	if doc.CreationInfo.Created != "" || doc.Metadata.Timestamp != "" {
		ntia.HasTimestamp = true
	}

	// Check author (SPDX: creationInfo.creators, CycloneDX: metadata.authors)
	if len(doc.CreationInfo.Creators) > 0 || len(doc.Metadata.Authors) > 0 {
		ntia.HasAuthor = true
	}

	// Check supplier (from packages or metadata)
	for _, pkg := range doc.Packages {
		if pkg.Supplier != "" && pkg.Supplier != "NOASSERTION" {
			ntia.HasSupplierName = true
			break
		}
	}
	if doc.Metadata.Supplier.Name != "" {
		ntia.HasSupplierName = true
	}

	// Check relationships (SPDX: relationships, CycloneDX: dependencies)
	if len(doc.Relationships) > 0 || len(doc.Dependencies) > 0 {
		ntia.HasRelationship = true
	}

	// Check component-level fields from parsed packages.
	for _, pkg := range packages {
		if pkg.Name != "" {
			ntia.HasComponentName = true
		}
		if pkg.Version != "" {
			ntia.HasComponentVersion = true
		}
		if pkg.PURL != "" || pkg.CPE != "" {
			ntia.HasUniqueID = true
		}
		// If we have all component-level checks, break early.
		if ntia.HasComponentName && ntia.HasComponentVersion && ntia.HasUniqueID {
			break
		}
	}

	// Determine overall compliance.
	ntia.IsCompliant = ntia.HasSupplierName &&
		ntia.HasComponentName &&
		ntia.HasComponentVersion &&
		ntia.HasUniqueID &&
		ntia.HasRelationship &&
		ntia.HasAuthor &&
		ntia.HasTimestamp

	return ntia
}

// MediaTypeForFormat returns the IANA media type for an SBOM format.
func MediaTypeForFormat(format domain.SBOMFormat) string {
	switch format {
	case domain.SBOMFormatSPDX:
		return MediaTypeSPDX
	case domain.SBOMFormatCycloneDX:
		return MediaTypeCycloneDX
	default:
		return "application/json"
	}
}
