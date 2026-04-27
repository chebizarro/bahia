package sbom

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestParse_SPDX(t *testing.T) {
	doc := spdxDocument{
		SPDXVersion:       "SPDX-2.3",
		DataLicense:       "CC0-1.0",
		SPDXID:            "SPDXRef-DOCUMENT",
		Name:              "test-sbom",
		DocumentNamespace: "https://example.com/test",
		Packages: []spdxPackage{
			{
				Name:        "lodash",
				VersionInfo: "4.17.21",
				LicenseConcluded: "MIT",
				ExternalRefs: []spdxExternalRef{
					{ReferenceType: "purl", ReferenceLocator: "pkg:npm/lodash@4.17.21"},
				},
			},
			{
				Name:        "express",
				VersionInfo: "4.18.2",
				LicenseDeclared: "MIT",
				ExternalRefs: []spdxExternalRef{
					{ReferenceType: "purl", ReferenceLocator: "pkg:npm/express@4.18.2"},
					{ReferenceType: "cpe23Type", ReferenceLocator: "cpe:2.3:a:express:express:4.18.2:*:*:*:*:*:*:*"},
				},
			},
		},
	}

	data, _ := json.Marshal(doc)
	artifactID := uuid.New()

	result, err := Parse(data, artifactID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.SBOM.Format != domain.SBOMFormatSPDX {
		t.Errorf("format = %q, want spdx", result.SBOM.Format)
	}
	if result.SBOM.ArtifactID != artifactID {
		t.Error("artifact ID mismatch")
	}
	if result.SBOM.PackageCount != 2 {
		t.Errorf("package_count = %d, want 2", result.SBOM.PackageCount)
	}
	if result.SBOM.RawHash == "" {
		t.Error("expected raw_hash to be set")
	}

	// Check packages.
	if len(result.Packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(result.Packages))
	}
	if result.Packages[0].Name != "lodash" {
		t.Errorf("package[0].name = %q", result.Packages[0].Name)
	}
	if result.Packages[0].PURL != "pkg:npm/lodash@4.17.21" {
		t.Errorf("package[0].purl = %q", result.Packages[0].PURL)
	}
	if result.Packages[0].Ecosystem != "npm" {
		t.Errorf("package[0].ecosystem = %q, want npm", result.Packages[0].Ecosystem)
	}
	if result.Packages[0].License != "MIT" {
		t.Errorf("package[0].license = %q, want MIT", result.Packages[0].License)
	}
	if result.Packages[1].CPE == "" {
		t.Error("expected CPE on express package")
	}

	// Check SBOM IDs are set on packages.
	for _, p := range result.Packages {
		if p.SBOMID != result.SBOM.ID {
			t.Error("package SBOM ID mismatch")
		}
		if p.ID == uuid.Nil {
			t.Error("package ID should not be nil")
		}
	}
}

func TestParse_CycloneDX(t *testing.T) {
	bom := cyclonedxBOM{
		BOMFormat:   "CycloneDX",
		SpecVersion: "1.5",
		Version:     1,
		Components: []cyclonedxComponent{
			{
				Type:    "library",
				Name:    "log4j-core",
				Version: "2.14.1",
				PURL:    "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1",
				Licenses: []struct {
					License struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"license"`
				}{
					{License: struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					}{ID: "Apache-2.0"}},
				},
			},
			{
				Type:    "library",
				Name:    "spring-boot",
				Version: "3.1.0",
				PURL:    "pkg:maven/org.springframework.boot/spring-boot@3.1.0",
			},
		},
		Vulnerabilities: []cyclonedxVuln{
			{ID: "CVE-2021-44228", Ratings: []struct {
				Severity string `json:"severity"`
			}{{Severity: "critical"}}},
			{ID: "CVE-2022-12345", Ratings: []struct {
				Severity string `json:"severity"`
			}{{Severity: "high"}}},
			{ID: "CVE-2023-99999", Ratings: []struct {
				Severity string `json:"severity"`
			}{{Severity: "medium"}}},
		},
	}

	data, _ := json.Marshal(bom)
	artifactID := uuid.New()

	result, err := Parse(data, artifactID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.SBOM.Format != domain.SBOMFormatCycloneDX {
		t.Errorf("format = %q, want cyclonedx", result.SBOM.Format)
	}
	if result.SBOM.PackageCount != 2 {
		t.Errorf("package_count = %d, want 2", result.SBOM.PackageCount)
	}
	if result.SBOM.VulnerabilityCount != 3 {
		t.Errorf("vulnerability_count = %d, want 3", result.SBOM.VulnerabilityCount)
	}
	if result.SBOM.CriticalCount != 1 {
		t.Errorf("critical_count = %d, want 1", result.SBOM.CriticalCount)
	}
	if result.SBOM.HighCount != 1 {
		t.Errorf("high_count = %d, want 1", result.SBOM.HighCount)
	}

	// Check packages.
	if result.Packages[0].Name != "log4j-core" {
		t.Errorf("package[0].name = %q", result.Packages[0].Name)
	}
	if result.Packages[0].Ecosystem != "maven" {
		t.Errorf("package[0].ecosystem = %q, want maven", result.Packages[0].Ecosystem)
	}
	if result.Packages[0].License != "Apache-2.0" {
		t.Errorf("package[0].license = %q, want Apache-2.0", result.Packages[0].License)
	}
}

func TestParse_EmptyData(t *testing.T) {
	_, err := Parse(nil, uuid.New())
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestParse_UnknownFormat(t *testing.T) {
	data := []byte(`{"unknown": "format"}`)
	_, err := Parse(data, uuid.New())
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestParse_InvalidJSON(t *testing.T) {
	data := []byte(`not json at all`)
	_, err := Parse(data, uuid.New())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name string
		data string
		want domain.SBOMFormat
	}{
		{"spdx", `{"spdxVersion": "SPDX-2.3"}`, domain.SBOMFormatSPDX},
		{"cyclonedx", `{"bomFormat": "CycloneDX"}`, domain.SBOMFormatCycloneDX},
		{"unknown", `{"foo": "bar"}`, ""},
		{"invalid", `not json`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectFormat([]byte(tt.data))
			if got != tt.want {
				t.Errorf("detectFormat() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEcosystemFromPURL(t *testing.T) {
	tests := []struct {
		purl string
		want string
	}{
		{"pkg:npm/lodash@4.17.21", "npm"},
		{"pkg:maven/org.apache/log4j@2.14.1", "maven"},
		{"pkg:golang/github.com/foo/bar@v1.0", "golang"},
		{"pkg:pypi/requests@2.28.0", "pypi"},
		{"invalid-purl", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.purl, func(t *testing.T) {
			got := ecosystemFromPURL(tt.purl)
			if got != tt.want {
				t.Errorf("ecosystemFromPURL(%q) = %q, want %q", tt.purl, got, tt.want)
			}
		})
	}
}
