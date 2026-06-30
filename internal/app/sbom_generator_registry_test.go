package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	sbomAdapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestNewSBOMGeneratorRegistryDisablesCdxgenByDefault(t *testing.T) {
	registry, err := newSBOMGeneratorRegistry(config.SBOMConfig{})
	if err != nil {
		t.Fatalf("newSBOMGeneratorRegistry returned error: %v", err)
	}

	_, err = registry.GenerateSBOM(context.Background(), sbomAdapter.GenerateRequest{
		Source:    sbomAdapter.SourceRequest{Kind: sbomAdapter.SourceKindRepository, Locator: "fixture"},
		Format:    domain.SBOMFormatCycloneDX,
		Generator: sbomAdapter.GeneratorCdxgen,
	})
	var unavailable sbomAdapter.ErrCdxgenUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("GenerateSBOM error = %v, want ErrCdxgenUnavailable", err)
	}
	if unavailable.Binary != "cdxgen" || unavailable.Cause == nil || unavailable.Cause.Error() != "adapter is disabled" {
		t.Fatalf("unavailable error = %#v, want disabled cdxgen", unavailable)
	}
}

func TestNewSBOMGeneratorRegistryAutoUsesEnabledCdxgen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture executable is a POSIX shell script")
	}
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "cdxgen")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
cat > "$out" <<'JSON'
{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,"components":[{"type":"library","name":"fixture","version":"1.0.0"}]}
JSON
`
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fixture cdxgen executable: %v", err)
	}
	registry, err := newSBOMGeneratorRegistry(config.SBOMConfig{
		Cdxgen: config.SBOMCdxgenConfig{Enabled: true, BinaryPath: binary},
	})
	if err != nil {
		t.Fatalf("newSBOMGeneratorRegistry returned error: %v", err)
	}

	result, err := registry.GenerateSBOM(context.Background(), sbomAdapter.GenerateRequest{
		Source: sbomAdapter.SourceRequest{Kind: sbomAdapter.SourceKindRepository, Locator: "../adapters/sbom/testdata/repository-fixture"},
		Format: domain.SBOMFormatCycloneDX,
	})
	if err != nil {
		t.Fatalf("GenerateSBOM returned error: %v", err)
	}
	if result.Generator.ID != string(sbomAdapter.GeneratorCdxgen) {
		t.Fatalf("generator ID = %q, want %q", result.Generator.ID, sbomAdapter.GeneratorCdxgen)
	}
}

func TestNewSBOMGeneratorRegistryUsesConfiguredCdxgenBinary(t *testing.T) {
	missingBinary := filepath.Join(t.TempDir(), "missing-cdxgen")
	registry, err := newSBOMGeneratorRegistry(config.SBOMConfig{
		Cdxgen: config.SBOMCdxgenConfig{
			Enabled:    true,
			BinaryPath: missingBinary,
		},
	})
	if err != nil {
		t.Fatalf("newSBOMGeneratorRegistry returned error: %v", err)
	}

	_, err = registry.GenerateSBOM(context.Background(), sbomAdapter.GenerateRequest{
		Source:    sbomAdapter.SourceRequest{Kind: sbomAdapter.SourceKindRepository, Locator: "fixture"},
		Format:    domain.SBOMFormatCycloneDX,
		Generator: sbomAdapter.GeneratorCdxgen,
	})
	var unavailable sbomAdapter.ErrCdxgenUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("GenerateSBOM error = %v, want ErrCdxgenUnavailable", err)
	}
	if unavailable.Binary != missingBinary {
		t.Fatalf("unavailable binary = %q, want %q", unavailable.Binary, missingBinary)
	}
}
