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
