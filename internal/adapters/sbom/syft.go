package sbom

import (
	"context"
	"fmt"
	"runtime/debug"

	anchoreSyft "github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/format"
	syftsbom "github.com/anchore/syft/syft/sbom"
	"github.com/anchore/syft/syft/source/sourceproviders"
	"github.com/openagentsinc/bahia/internal/domain"
	_ "modernc.org/sqlite"
)

// SyftGenerator uses Anchore Syft's Go library to generate SPDX and CycloneDX JSON SBOMs.
type SyftGenerator struct {
	createConfig func() *anchoreSyft.CreateSBOMConfig
	sourceConfig func(SourceRequest) *anchoreSyft.GetSourceConfig
}

// NewSyftGenerator returns the default in-process Syft generator.
func NewSyftGenerator() *SyftGenerator {
	return &SyftGenerator{}
}

func (g *SyftGenerator) ID() GeneratorID { return GeneratorSyft }

func (g *SyftGenerator) GenerateSBOM(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	if err := validateGenerateRequest(req); err != nil {
		return nil, err
	}

	srcCfg := g.newSourceConfig(req.Source)
	src, err := anchoreSyft.GetSource(ctx, req.Source.Locator, srcCfg)
	if err != nil {
		return nil, fmt.Errorf("resolving Syft source: %w", err)
	}
	defer src.Close()

	createCfg := g.newCreateConfig()
	syftSBOM, err := anchoreSyft.CreateSBOM(ctx, src, createCfg)
	if err != nil {
		return nil, fmt.Errorf("creating Syft SBOM: %w", err)
	}

	payload, err := encodeSyftSBOM(*syftSBOM, req.Format)
	if err != nil {
		return nil, err
	}
	if err := validateGeneratedPayload(payload, req.Format); err != nil {
		return nil, err
	}

	return &GenerateResult{
		Subject:   req.Subject,
		Format:    req.Format,
		MediaType: MediaTypeForFormat(req.Format),
		Payload:   payload,
		Generator: domain.SBOMGenerator{ID: string(GeneratorSyft), Version: syftLibraryVersion()},
		Source:    req.Source,
	}, nil
}

func (g *SyftGenerator) newCreateConfig() *anchoreSyft.CreateSBOMConfig {
	if g.createConfig != nil {
		return g.createConfig()
	}
	cfg := anchoreSyft.DefaultCreateSBOMConfig()
	cfg.WithParallelism(1)
	return cfg
}

func (g *SyftGenerator) newSourceConfig(source SourceRequest) *anchoreSyft.GetSourceConfig {
	if g.sourceConfig != nil {
		return g.sourceConfig(source)
	}
	cfg := anchoreSyft.DefaultGetSourceConfig()
	switch source.Kind {
	case SourceKindDirectory, SourceKindRepository:
		cfg.WithSources(sourceproviders.DirTag)
	case SourceKindArchive, SourceKindPackageFile:
		cfg.WithSources(sourceproviders.FileTag)
	case SourceKindOCIImage:
		cfg.WithSources(sourceproviders.PullTag)
	}
	return cfg
}

func encodeSyftSBOM(s syftsbom.SBOM, requested domain.SBOMFormat) ([]byte, error) {
	encoders, err := format.DefaultEncodersConfig().Encoders()
	if err != nil {
		return nil, fmt.Errorf("configuring Syft encoders: %w", err)
	}
	collection := format.NewEncoderCollection(encoders...)

	var encoderName string
	switch requested {
	case domain.SBOMFormatSPDX:
		encoderName = "spdx-json@2.3"
	case domain.SBOMFormatCycloneDX:
		encoderName = "cyclonedx-json@1.6"
	default:
		return nil, fmt.Errorf("unsupported SBOM format %q", requested)
	}
	encoder := collection.GetByString(encoderName)
	if encoder == nil {
		return nil, fmt.Errorf("Syft encoder %q is unavailable", encoderName)
	}

	return format.Encode(s, encoder)
}

func syftLibraryVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/anchore/syft" && dep.Version != "(devel)" {
			return dep.Version
		}
	}
	return ""
}
