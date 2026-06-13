package sbom

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestCdxgenUnavailableWhenDisabled(t *testing.T) {
	generator := NewCdxgenGenerator(CdxgenConfig{Enabled: false, BinaryPath: "cdxgen"})
	err := generator.Available(context.Background())
	var unavailable ErrCdxgenUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("Available error = %v, want ErrCdxgenUnavailable", err)
	}
}

func TestCdxgenUnavailableWhenBinaryMissing(t *testing.T) {
	generator := NewCdxgenGenerator(CdxgenConfig{Enabled: true, BinaryPath: filepath.Join(t.TempDir(), "missing-cdxgen")})
	err := generator.Available(context.Background())
	var unavailable ErrCdxgenUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("Available error = %v, want ErrCdxgenUnavailable", err)
	}
}

func TestCdxgenGeneratorInvokesExecutableAndReadsCycloneDX(t *testing.T) {
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

	fixture := filepath.Join("testdata", "repository-fixture")
	generator := NewCdxgenGenerator(CdxgenConfig{Enabled: true, BinaryPath: binary, TempDir: tmp})
	result, err := generator.GenerateSBOM(context.Background(), GenerateRequest{
		Subject: domain.SBOMSubject{Type: domain.SBOMSubjectRepository, ID: "repo-fixture", Digest: "git:0123456789abcdef0123456789abcdef01234567"},
		Source:  SourceRequest{Kind: SourceKindRepository, Locator: fixture},
		Format:  domain.SBOMFormatCycloneDX,
	})
	if err != nil {
		t.Fatalf("GenerateSBOM returned error: %v", err)
	}
	if result.Generator.ID != string(GeneratorCdxgen) {
		t.Fatalf("generator ID = %q, want %q", result.Generator.ID, GeneratorCdxgen)
	}
	if err := validateGeneratedPayload(result.Payload, domain.SBOMFormatCycloneDX); err != nil {
		t.Fatalf("generated payload validation failed: %v", err)
	}
}

func TestCdxgenRejectsSPDX(t *testing.T) {
	generator := NewCdxgenGenerator(CdxgenConfig{Enabled: true, BinaryPath: filepath.Join(t.TempDir(), "missing-cdxgen")})
	_, err := generator.GenerateSBOM(context.Background(), GenerateRequest{
		Source: SourceRequest{Kind: SourceKindRepository, Locator: "testdata/repository-fixture"},
		Format: domain.SBOMFormatSPDX,
	})
	if err == nil {
		t.Fatal("expected cdxgen SPDX rejection")
	}
}
