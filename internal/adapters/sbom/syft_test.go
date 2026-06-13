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
			Name string `json:"name"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(result.Payload, &doc); err != nil {
		t.Fatalf("generated SPDX is not JSON: %v", err)
	}
	if doc.SPDXVersion == "" {
		t.Fatal("generated SPDX payload is missing spdxVersion")
	}
	if len(doc.Packages) == 0 {
		t.Fatal("generated SPDX payload has no packages")
	}
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
		BOMFormat  string `json:"bomFormat"`
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
	}
	if err := json.Unmarshal(result.Payload, &bom); err != nil {
		t.Fatalf("generated CycloneDX is not JSON: %v", err)
	}
	if bom.BOMFormat != "CycloneDX" {
		t.Fatalf("bomFormat = %q, want CycloneDX", bom.BOMFormat)
	}
	if len(bom.Components) == 0 {
		t.Fatal("generated CycloneDX payload has no components")
	}
}
