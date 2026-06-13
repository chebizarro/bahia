package sbom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ErrCdxgenUnavailable is returned when the optional cdxgen executable adapter is requested but cannot run.
type ErrCdxgenUnavailable struct {
	Binary string
	Cause  error
}

func (e ErrCdxgenUnavailable) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("cdxgen executable %q is unavailable", e.Binary)
	}
	return fmt.Sprintf("cdxgen executable %q is unavailable: %v", e.Binary, e.Cause)
}

func (e ErrCdxgenUnavailable) Unwrap() error { return e.Cause }

// CdxgenConfig configures the optional cdxgen executable adapter.
type CdxgenConfig struct {
	Enabled     bool
	BinaryPath  string
	SpecVersion string
	ProjectType string
	TempDir     string
}

// CdxgenGenerator invokes a configured cdxgen executable for CycloneDX repository SBOM generation.
type CdxgenGenerator struct {
	config CdxgenConfig
}

// NewCdxgenGenerator returns an optional cdxgen adapter. A disabled adapter reports explicit unavailability.
func NewCdxgenGenerator(config CdxgenConfig) *CdxgenGenerator {
	if strings.TrimSpace(config.BinaryPath) == "" {
		config.BinaryPath = "cdxgen"
	}
	if strings.TrimSpace(config.SpecVersion) == "" {
		config.SpecVersion = "1.6"
	}
	return &CdxgenGenerator{config: config}
}

func (g *CdxgenGenerator) ID() GeneratorID { return GeneratorCdxgen }

func (g *CdxgenGenerator) Available(ctx context.Context) error {
	if !g.config.Enabled {
		return ErrCdxgenUnavailable{Binary: g.config.BinaryPath, Cause: errors.New("adapter is disabled")}
	}
	_, err := g.resolveBinary(ctx)
	return err
}

func (g *CdxgenGenerator) GenerateSBOM(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	if err := validateGenerateRequest(req); err != nil {
		return nil, err
	}
	if req.Format != domain.SBOMFormatCycloneDX {
		return nil, fmt.Errorf("cdxgen supports only %q output, got %q", domain.SBOMFormatCycloneDX, req.Format)
	}
	if req.Source.Kind != SourceKindRepository {
		return nil, fmt.Errorf("cdxgen adapter supports only %q sources, got %q", SourceKindRepository, req.Source.Kind)
	}

	binary, err := g.resolveBinary(ctx)
	if err != nil {
		return nil, err
	}

	outputDir := g.config.TempDir
	if outputDir == "" {
		outputDir = os.TempDir()
	}
	tmp, err := os.CreateTemp(outputDir, "bahia-cdxgen-*.json")
	if err != nil {
		return nil, fmt.Errorf("creating cdxgen output file: %w", err)
	}
	outputPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("closing cdxgen output file: %w", err)
	}
	defer os.Remove(outputPath)

	args := []string{"-o", outputPath, "--spec-version", g.config.SpecVersion}
	if strings.TrimSpace(g.config.ProjectType) != "" {
		args = append(args, "-t", g.config.ProjectType)
	}
	args = append(args, req.Source.Locator)

	cmd := exec.CommandContext(ctx, binary, args...)
	combinedOutput, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("running cdxgen: %w: %s", err, strings.TrimSpace(string(combinedOutput)))
	}

	payload, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("reading cdxgen output: %w", err)
	}
	if err := validateGeneratedPayload(payload, domain.SBOMFormatCycloneDX); err != nil {
		return nil, fmt.Errorf("validating cdxgen output: %w", err)
	}

	return &GenerateResult{
		Subject:   req.Subject,
		Format:    domain.SBOMFormatCycloneDX,
		MediaType: MediaTypeForFormat(domain.SBOMFormatCycloneDX),
		Payload:   payload,
		Generator: domain.SBOMGenerator{ID: string(GeneratorCdxgen)},
		Source:    req.Source,
	}, nil
}

func (g *CdxgenGenerator) resolveBinary(ctx context.Context) (string, error) {
	if !g.config.Enabled {
		return "", ErrCdxgenUnavailable{Binary: g.config.BinaryPath, Cause: errors.New("adapter is disabled")}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	binary := strings.TrimSpace(g.config.BinaryPath)
	if binary == "" {
		binary = "cdxgen"
	}
	if filepath.Base(binary) != binary {
		info, err := os.Stat(binary)
		if err != nil {
			return "", ErrCdxgenUnavailable{Binary: binary, Cause: err}
		}
		if info.IsDir() || info.Mode().Perm()&0111 == 0 {
			return "", ErrCdxgenUnavailable{Binary: binary, Cause: errors.New("path is not executable")}
		}
		return binary, nil
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return "", ErrCdxgenUnavailable{Binary: binary, Cause: err}
	}
	return resolved, nil
}

func validateGeneratedPayload(payload []byte, expectedFormat domain.SBOMFormat) error {
	if len(payload) == 0 {
		return errors.New("generated SBOM payload is empty")
	}
	if !json.Valid(payload) {
		return errors.New("generated SBOM payload is not valid JSON")
	}
	var probe struct {
		SPDXVersion string `json:"spdxVersion"`
		BOMFormat   string `json:"bomFormat"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return fmt.Errorf("reading generated SBOM JSON: %w", err)
	}
	switch expectedFormat {
	case domain.SBOMFormatSPDX:
		if probe.SPDXVersion == "" {
			return errors.New("generated payload is not SPDX JSON")
		}
	case domain.SBOMFormatCycloneDX:
		if probe.BOMFormat != "CycloneDX" {
			return errors.New("generated payload is not CycloneDX JSON")
		}
	default:
		return fmt.Errorf("unsupported SBOM format %q", expectedFormat)
	}
	return nil
}
