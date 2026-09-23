package sbom

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestSyftGeneratorGeneratesSPDXJSONFromRepositoryFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "repository-fixture")
	generator := NewSyftGenerator()

	result, err := generator.GenerateSBOM(context.Background(), GenerateRequest{
		Subject: domain.SBOMSubject{
			Type:   domain.SBOMSubjectRepository,
			ID:     "repo-fixture",
			Digest: "git:0123456789abcdef0123456789abcdef01234567",
		},
		Source: SourceRequest{Kind: SourceKindRepository, Locator: fixture},
		Format: domain.SBOMFormatSPDX,
	})
	if err != nil {
		t.Fatalf("GenerateSBOM returned error: %v", err)
	}
	if result.Generator.ID != string(GeneratorSyft) {
		t.Fatalf("generator ID = %q, want %q", result.Generator.ID, GeneratorSyft)
	}
	if result.MediaType != MediaTypeSPDX {
		t.Fatalf("media type = %q, want %q", result.MediaType, MediaTypeSPDX)
	}

	var doc struct {
		SPDXVersion string `json:"spdxVersion"`
		Packages    []struct {
			Name    string `json:"name"`
			Version string `json:"versionInfo"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(result.Payload, &doc); err != nil {
		t.Fatalf("generated SPDX is not JSON: %v", err)
	}
	if doc.SPDXVersion != "SPDX-2.3" {
		t.Fatalf("spdxVersion = %q, want SPDX-2.3", doc.SPDXVersion)
	}
	for _, pkg := range doc.Packages {
		if pkg.Name == "left-pad" && pkg.Version == "1.3.0" {
			return
		}
	}
	t.Fatalf("generated SPDX is missing fixture dependency left-pad@1.3.0: %+v", doc.Packages)
}

func TestSyftGeneratorGeneratesCycloneDXJSONFromRepositoryFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "repository-fixture")
	generator := NewSyftGenerator()

	result, err := generator.GenerateSBOM(context.Background(), GenerateRequest{
		Subject: domain.SBOMSubject{
			Type:   domain.SBOMSubjectRepository,
			ID:     "repo-fixture",
			Digest: "git:0123456789abcdef0123456789abcdef01234567",
		},
		Source: SourceRequest{Kind: SourceKindRepository, Locator: fixture},
		Format: domain.SBOMFormatCycloneDX,
	})
	if err != nil {
		t.Fatalf("GenerateSBOM returned error: %v", err)
	}
	if result.MediaType != MediaTypeCycloneDX {
		t.Fatalf("media type = %q, want %q", result.MediaType, MediaTypeCycloneDX)
	}

	var bom struct {
		BOMFormat   string `json:"bomFormat"`
		SpecVersion string `json:"specVersion"`
		Components  []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"components"`
	}
	if err := json.Unmarshal(result.Payload, &bom); err != nil {
		t.Fatalf("generated CycloneDX is not JSON: %v", err)
	}
	if bom.BOMFormat != "CycloneDX" {
		t.Fatalf("bomFormat = %q, want CycloneDX", bom.BOMFormat)
	}
	if bom.SpecVersion != "1.6" {
		t.Fatalf("specVersion = %q, want 1.6", bom.SpecVersion)
	}
	for _, component := range bom.Components {
		if component.Name == "left-pad" && component.Version == "1.3.0" {
			return
		}
	}
	t.Fatalf("generated CycloneDX is missing fixture dependency left-pad@1.3.0: %+v", bom.Components)
}
