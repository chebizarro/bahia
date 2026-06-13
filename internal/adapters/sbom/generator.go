package sbom

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// GeneratorID identifies an SBOM generator implementation.
type GeneratorID string

const (
	GeneratorAuto   GeneratorID = "auto"
	GeneratorSyft   GeneratorID = "syft"
	GeneratorCdxgen GeneratorID = "cdxgen"
)

// SourceKind identifies the source type supplied to an SBOM generator.
type SourceKind string

const (
	SourceKindOCIImage    SourceKind = "oci-image"
	SourceKindDirectory   SourceKind = "directory"
	SourceKindRepository  SourceKind = "repository"
	SourceKindArchive     SourceKind = "archive"
	SourceKindPackageFile SourceKind = "package-file"
)

// SourceRequest describes the source bytes or object to catalog.
type SourceRequest struct {
	Kind    SourceKind `json:"kind"`
	Locator string     `json:"locator"`
}

// GenerateRequest contains the subject, source, output format, and generator preference.
type GenerateRequest struct {
	Subject   domain.SBOMSubject `json:"subject"`
	Source    SourceRequest      `json:"source"`
	Format    domain.SBOMFormat  `json:"format"`
	Generator GeneratorID        `json:"generator"`
}

// GenerateResult is the exact generated SBOM payload and metadata needed by later storage/publication slices.
type GenerateResult struct {
	Subject   domain.SBOMSubject   `json:"subject"`
	Format    domain.SBOMFormat    `json:"format"`
	MediaType string               `json:"media_type"`
	Payload   []byte               `json:"-"`
	Generator domain.SBOMGenerator `json:"generator"`
	Source    SourceRequest        `json:"source"`
}

// Generator produces an SBOM payload for a supported source.
type Generator interface {
	ID() GeneratorID
	GenerateSBOM(context.Context, GenerateRequest) (*GenerateResult, error)
}

// AvailabilityChecker reports whether an optional generator can run in the current process environment.
type AvailabilityChecker interface {
	Available(context.Context) error
}

// GeneratorRegistry resolves explicit and automatic generator selection.
type GeneratorRegistry struct {
	syft   Generator
	cdxgen Generator
}

// NewGeneratorRegistry builds a registry with Syft as the required default and cdxgen as an optional adapter.
func NewGeneratorRegistry(syft Generator, cdxgen Generator) (*GeneratorRegistry, error) {
	if syft == nil {
		return nil, errors.New("syft generator is required")
	}
	if syft.ID() != GeneratorSyft {
		return nil, fmt.Errorf("default generator must be %q, got %q", GeneratorSyft, syft.ID())
	}
	if cdxgen != nil && cdxgen.ID() != GeneratorCdxgen {
		return nil, fmt.Errorf("optional generator must be %q, got %q", GeneratorCdxgen, cdxgen.ID())
	}
	return &GeneratorRegistry{syft: syft, cdxgen: cdxgen}, nil
}

// GenerateSBOM selects a generator and delegates generation.
func (r *GeneratorRegistry) GenerateSBOM(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	generator, err := r.selectGenerator(ctx, req)
	if err != nil {
		return nil, err
	}
	selected := req
	selected.Generator = generator.ID()
	return generator.GenerateSBOM(ctx, selected)
}

func (r *GeneratorRegistry) selectGenerator(ctx context.Context, req GenerateRequest) (Generator, error) {
	switch req.Generator {
	case "", GeneratorAuto:
		if r.shouldAutoUseCdxgen(ctx, req) {
			return r.cdxgen, nil
		}
		return r.syft, nil
	case GeneratorSyft:
		return r.syft, nil
	case GeneratorCdxgen:
		if r.cdxgen == nil {
			return nil, ErrCdxgenUnavailable{Binary: "cdxgen", Cause: errors.New("adapter is disabled")}
		}
		if checker, ok := r.cdxgen.(AvailabilityChecker); ok {
			if err := checker.Available(ctx); err != nil {
				return nil, err
			}
		}
		return r.cdxgen, nil
	default:
		return nil, fmt.Errorf("unsupported SBOM generator %q", req.Generator)
	}
}

func (r *GeneratorRegistry) shouldAutoUseCdxgen(ctx context.Context, req GenerateRequest) bool {
	if r.cdxgen == nil || req.Format != domain.SBOMFormatCycloneDX || req.Source.Kind != SourceKindRepository {
		return false
	}
	checker, ok := r.cdxgen.(AvailabilityChecker)
	return !ok || checker.Available(ctx) == nil
}

func validateGenerateRequest(req GenerateRequest) error {
	if req.Source.Kind == "" {
		return errors.New("source kind is required")
	}
	if strings.TrimSpace(req.Source.Locator) == "" {
		return errors.New("source locator is required")
	}
	if req.Format != domain.SBOMFormatSPDX && req.Format != domain.SBOMFormatCycloneDX {
		return fmt.Errorf("unsupported SBOM format %q", req.Format)
	}
	return nil
}
