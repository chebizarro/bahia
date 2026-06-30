package sbom

import (
	"context"
	"errors"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type recordingGenerator struct {
	id    GeneratorID
	calls int
}

func (g *recordingGenerator) ID() GeneratorID { return g.id }
func (g *recordingGenerator) GenerateSBOM(context.Context, GenerateRequest) (*GenerateResult, error) {
	g.calls++
	return &GenerateResult{Generator: domain.SBOMGenerator{ID: string(g.id)}}, nil
}

type unavailableRecordingGenerator struct {
	recordingGenerator
	err error
}

func (g *unavailableRecordingGenerator) Available(context.Context) error { return g.err }

func TestGeneratorRegistryExplicitCdxgenFailsWhenAdapterDisabled(t *testing.T) {
	syft := &recordingGenerator{id: GeneratorSyft}
	registry, err := NewGeneratorRegistry(syft, nil)
	if err != nil {
		t.Fatalf("NewGeneratorRegistry returned error: %v", err)
	}

	_, err = registry.GenerateSBOM(context.Background(), GenerateRequest{
		Source:    SourceRequest{Kind: SourceKindRepository, Locator: "fixture"},
		Format:    domain.SBOMFormatCycloneDX,
		Generator: GeneratorCdxgen,
	})
	var unavailable ErrCdxgenUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("GenerateSBOM error = %v, want ErrCdxgenUnavailable", err)
	}
	if unavailable.Binary != "cdxgen" || unavailable.Cause == nil || unavailable.Cause.Error() != "adapter is disabled" {
		t.Fatalf("unavailable error = %#v, want disabled cdxgen", unavailable)
	}
	if syft.calls != 0 {
		t.Fatalf("syft calls = %d, want 0 for explicit cdxgen failure", syft.calls)
	}
}

func TestGeneratorRegistryExplicitCdxgenFailsWhenBinaryMissing(t *testing.T) {
	syft := &recordingGenerator{id: GeneratorSyft}
	cdxgen := &unavailableRecordingGenerator{
		recordingGenerator: recordingGenerator{id: GeneratorCdxgen},
		err:                ErrCdxgenUnavailable{Binary: "/missing/cdxgen", Cause: errors.New("executable file not found")},
	}
	registry, err := NewGeneratorRegistry(syft, cdxgen)
	if err != nil {
		t.Fatalf("NewGeneratorRegistry returned error: %v", err)
	}

	_, err = registry.GenerateSBOM(context.Background(), GenerateRequest{
		Source:    SourceRequest{Kind: SourceKindRepository, Locator: "fixture"},
		Format:    domain.SBOMFormatCycloneDX,
		Generator: GeneratorCdxgen,
	})
	var unavailable ErrCdxgenUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("GenerateSBOM error = %v, want ErrCdxgenUnavailable", err)
	}
	if unavailable.Binary != "/missing/cdxgen" {
		t.Fatalf("unavailable binary = %q, want missing binary path", unavailable.Binary)
	}
	if syft.calls != 0 || cdxgen.calls != 0 {
		t.Fatalf("calls syft=%d cdxgen=%d, want no generation calls", syft.calls, cdxgen.calls)
	}
}

func TestGeneratorRegistryAutoFallsBackToSyftWhenCdxgenDisabled(t *testing.T) {
	syft := &recordingGenerator{id: GeneratorSyft}
	cdxgen := &unavailableRecordingGenerator{
		recordingGenerator: recordingGenerator{id: GeneratorCdxgen},
		err:                ErrCdxgenUnavailable{Binary: "cdxgen", Cause: errors.New("adapter is disabled")},
	}
	registry, err := NewGeneratorRegistry(syft, cdxgen)
	if err != nil {
		t.Fatalf("NewGeneratorRegistry returned error: %v", err)
	}

	result, err := registry.GenerateSBOM(context.Background(), GenerateRequest{
		Source: SourceRequest{Kind: SourceKindRepository, Locator: "fixture"},
		Format: domain.SBOMFormatCycloneDX,
	})
	if err != nil {
		t.Fatalf("GenerateSBOM returned error: %v", err)
	}
	if result.Generator.ID != string(GeneratorSyft) {
		t.Fatalf("generator = %q, want %q", result.Generator.ID, GeneratorSyft)
	}
	if syft.calls != 1 || cdxgen.calls != 0 {
		t.Fatalf("calls syft=%d cdxgen=%d, want syft=1 cdxgen=0", syft.calls, cdxgen.calls)
	}
}

func TestGeneratorRegistryAutoUsesAvailableCdxgenForRepositoryCycloneDX(t *testing.T) {
	syft := &recordingGenerator{id: GeneratorSyft}
	cdxgen := &recordingGenerator{id: GeneratorCdxgen}
	registry, err := NewGeneratorRegistry(syft, cdxgen)
	if err != nil {
		t.Fatalf("NewGeneratorRegistry returned error: %v", err)
	}

	result, err := registry.GenerateSBOM(context.Background(), GenerateRequest{
		Source: SourceRequest{Kind: SourceKindRepository, Locator: "fixture"},
		Format: domain.SBOMFormatCycloneDX,
	})
	if err != nil {
		t.Fatalf("GenerateSBOM returned error: %v", err)
	}
	if result.Generator.ID != string(GeneratorCdxgen) {
		t.Fatalf("generator = %q, want %q", result.Generator.ID, GeneratorCdxgen)
	}
	if syft.calls != 0 || cdxgen.calls != 1 {
		t.Fatalf("calls syft=%d cdxgen=%d, want syft=0 cdxgen=1", syft.calls, cdxgen.calls)
	}
}

func TestValidateGenerateRequest(t *testing.T) {
	if err := validateGenerateRequest(GenerateRequest{Format: domain.SBOMFormatSPDX}); err == nil {
		t.Fatal("expected missing source kind error")
	}
	if err := validateGenerateRequest(GenerateRequest{Source: SourceRequest{Kind: SourceKindDirectory}, Format: domain.SBOMFormatSPDX}); err == nil {
		t.Fatal("expected missing source locator error")
	}
	if err := validateGenerateRequest(GenerateRequest{Source: SourceRequest{Kind: SourceKindDirectory, Locator: "fixture"}, Format: "unknown"}); err == nil {
		t.Fatal("expected unsupported format error")
	}
}
