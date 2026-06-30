# bahia-qfod Verification Report

## Summary

Implemented runtime configuration and production app wiring for the optional cdxgen SBOM generator.

Behavior now implemented:

1. `config.Config` exposes `sbom.cdxgen.enabled` and `sbom.cdxgen.binary_path`.
2. Defaults keep cdxgen disabled and set the binary path default to `cdxgen`.
3. Environment loading accepts `BAHIA_SBOM_CDXGEN_ENABLED` and `BAHIA_SBOM_CDXGEN_BINARY_PATH`.
4. App construction passes `NewCdxgenGenerator` into `NewGeneratorRegistry` only when cdxgen is enabled.
5. Disabled cdxgen preserves Syft as default/auto fallback.
6. Explicit `generator=cdxgen` fails clearly when disabled or when the configured binary is unavailable.

## Verification commands

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/config ./internal/adapters/sbom ./internal/app
```

Result: PASS on 2026-06-30.

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/...
```

Result: PASS on 2026-06-30.

## Evidence

- `internal/config/config_test.go`
  - `TestDefaults`
  - `TestLoadFromEnvVars`
  - `TestLoadSBOMCdxgenConfigFromYAML`
- `internal/app/sbom_generator_registry_test.go`
  - `TestNewSBOMGeneratorRegistryDisablesCdxgenByDefault`
  - `TestNewSBOMGeneratorRegistryAutoUsesEnabledCdxgen`
  - `TestNewSBOMGeneratorRegistryUsesConfiguredCdxgenBinary`
- `internal/adapters/sbom/generator_test.go`
  - `TestGeneratorRegistryExplicitCdxgenFailsWhenAdapterDisabled`
  - `TestGeneratorRegistryExplicitCdxgenFailsWhenBinaryMissing`
  - existing auto fallback/selection tests
- `internal/adapters/sbom/cdxgen_test.go`
  - existing disabled, missing-binary, and fixture executable tests
- `docs/user-guide/getting-started.md` and `docs/user-guide/features/artifacts.md` document the YAML and environment variable fields.

## Remaining work

No remaining work identified for bahia-qfod. The bead is intentionally left open for user verification.
