// Package sbom provides SBOM parsing and analysis for SPDX and CycloneDX formats.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ParseResult holds the result of parsing an SBOM document.
type ParseResult struct {
	SBOM     domain.ArtifactSBOM
	Packages []domain.SBOMPackage
}

// Parse detects the SBOM format and parses the document.
// Returns the parsed SBOM metadata and package list.
func Parse(data []byte, artifactID uuid.UUID) (*ParseResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty SBOM data")
	}

	// Detect format by checking for format-specific keys.
	format := detectFormat(data)

	var result *ParseResult
	var err error

	switch format {
	case domain.SBOMFormatSPDX:
		result, err = parseSPDX(data, artifactID)
	case domain.SBOMFormatCycloneDX:
		result, err = parseCycloneDX(data, artifactID)
	default:
		return nil, fmt.Errorf("unknown SBOM format")
	}

	if err != nil {
		return nil, err
	}

	// Set common fields.
	result.SBOM.ID = uuid.New()
	result.SBOM.ArtifactID = artifactID
	result.SBOM.Format = format
	result.SBOM.RawHash = hashData(data)
	result.SBOM.PackageCount = len(result.Packages)

	// Set SBOM ID on all packages.
	for i := range result.Packages {
		result.Packages[i].SBOMID = result.SBOM.ID
		if result.Packages[i].ID == uuid.Nil {
			result.Packages[i].ID = uuid.New()
		}
	}

	return result, nil
}

// detectFormat inspects the JSON to determine if it's SPDX or CycloneDX.
func detectFormat(data []byte) domain.SBOMFormat {
	var probe struct {
		SPDXVersion string `json:"spdxVersion"`
		BOMFormat   string `json:"bomFormat"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ""
	}

	if probe.SPDXVersion != "" {
		return domain.SBOMFormatSPDX
	}
	if probe.BOMFormat == "CycloneDX" {
		return domain.SBOMFormatCycloneDX
	}
	return ""
}

// --- SPDX Parser ---

type spdxDocument struct {
	SPDXVersion    string        `json:"spdxVersion"`
	DataLicense    string        `json:"dataLicense"`
	SPDXID         string        `json:"SPDXID"`
	Name           string        `json:"name"`
	Packages       []spdxPackage `json:"packages"`
	DocumentNamespace string     `json:"documentNamespace"`
}

type spdxPackage struct {
	Name               string `json:"name"`
	VersionInfo        string `json:"versionInfo"`
	PackageFileName    string `json:"packageFileName"`
	ExternalRefs       []spdxExternalRef `json:"externalRefs"`
	LicenseConcluded   string `json:"licenseConcluded"`
	LicenseDeclared    string `json:"licenseDeclared"`
}

type spdxExternalRef struct {
	ReferenceCategory string `json:"referenceCategory"`
	ReferenceType     string `json:"referenceType"`
	ReferenceLocator  string `json:"referenceLocator"`
}

func parseSPDX(data []byte, artifactID uuid.UUID) (*ParseResult, error) {
	var doc spdxDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing SPDX JSON: %w", err)
	}

	result := &ParseResult{
		SBOM: domain.ArtifactSBOM{
			Metadata: map[string]any{
				"spdx_version":       doc.SPDXVersion,
				"data_license":       doc.DataLicense,
				"document_name":      doc.Name,
				"document_namespace": doc.DocumentNamespace,
			},
		},
	}

	for _, sp := range doc.Packages {
		pkg := domain.SBOMPackage{
			Name:    sp.Name,
			Version: sp.VersionInfo,
		}

		// Extract PURL and CPE from external refs.
		for _, ref := range sp.ExternalRefs {
			switch ref.ReferenceType {
			case "purl":
				pkg.PURL = ref.ReferenceLocator
				pkg.Ecosystem = ecosystemFromPURL(ref.ReferenceLocator)
			case "cpe23Type", "cpe22Type":
				pkg.CPE = ref.ReferenceLocator
			}
		}

		// License.
		if sp.LicenseConcluded != "" && sp.LicenseConcluded != "NOASSERTION" {
			pkg.License = sp.LicenseConcluded
		} else if sp.LicenseDeclared != "" && sp.LicenseDeclared != "NOASSERTION" {
			pkg.License = sp.LicenseDeclared
		}

		result.Packages = append(result.Packages, pkg)
	}

	return result, nil
}

// --- CycloneDX Parser ---

type cyclonedxBOM struct {
	BOMFormat    string              `json:"bomFormat"`
	SpecVersion  string              `json:"specVersion"`
	Version      int                 `json:"version"`
	Components   []cyclonedxComponent `json:"components"`
	Vulnerabilities []cyclonedxVuln  `json:"vulnerabilities"`
}

type cyclonedxComponent struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version"`
	PURL    string `json:"purl"`
	CPE     string `json:"cpe"`
	Group   string `json:"group"`
	Licenses []struct {
		License struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"license"`
	} `json:"licenses"`
}

type cyclonedxVuln struct {
	ID       string `json:"id"`
	Ratings  []struct {
		Severity string `json:"severity"`
	} `json:"ratings"`
}

func parseCycloneDX(data []byte, artifactID uuid.UUID) (*ParseResult, error) {
	var bom cyclonedxBOM
	if err := json.Unmarshal(data, &bom); err != nil {
		return nil, fmt.Errorf("parsing CycloneDX JSON: %w", err)
	}

	// Count vulnerabilities by severity.
	var vulnCount, criticalCount, highCount int
	for _, v := range bom.Vulnerabilities {
		vulnCount++
		for _, r := range v.Ratings {
			switch r.Severity {
			case "critical":
				criticalCount++
			case "high":
				highCount++
			}
		}
	}

	result := &ParseResult{
		SBOM: domain.ArtifactSBOM{
			VulnerabilityCount: vulnCount,
			CriticalCount:      criticalCount,
			HighCount:           highCount,
			Metadata: map[string]any{
				"spec_version": bom.SpecVersion,
				"bom_version":  bom.Version,
			},
		},
	}

	for _, comp := range bom.Components {
		pkg := domain.SBOMPackage{
			Name:    comp.Name,
			Version: comp.Version,
			PURL:    comp.PURL,
			CPE:     comp.CPE,
		}

		if comp.PURL != "" {
			pkg.Ecosystem = ecosystemFromPURL(comp.PURL)
		}

		// License from first entry.
		if len(comp.Licenses) > 0 {
			lic := comp.Licenses[0].License
			if lic.ID != "" {
				pkg.License = lic.ID
			} else {
				pkg.License = lic.Name
			}
		}

		result.Packages = append(result.Packages, pkg)
	}

	return result, nil
}

// ecosystemFromPURL extracts the package ecosystem from a PURL.
// e.g. "pkg:npm/lodash@4.17.21" → "npm"
func ecosystemFromPURL(purl string) string {
	// PURL format: pkg:<type>/<namespace>/<name>@<version>
	if len(purl) < 5 || purl[:4] != "pkg:" {
		return ""
	}
	rest := purl[4:]
	for i, c := range rest {
		if c == '/' {
			return rest[:i]
		}
	}
	return ""
}

func hashData(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
