# bahia-1qe9.4 Verification Report

## Status
Real SBOM generator adapter slice implemented and targeted verification passed.

## Evidence
- `go test ./internal/adapters/sbom -run 'Test(SyftGenerator|Cdxgen|GeneratorRegistry|ValidateGenerateRequest)'`
  - Result: PASS
  - Covers generator request validation, auto-selection fallback/selection, Syft SPDX/CycloneDX fixture generation, and cdxgen unavailable/executable adapter behavior.
- `go test ./internal/adapters/sbom`
  - Result: PASS
  - Confirms the new generator code coexists with existing parser, attestation, storage, and Nostr index adapter tests.

## Repository scope verified
- `internal/adapters/sbom/generator.go` defines generator IDs, source request model, generation request/result, generator interface, availability checker, and auto-selection registry.
- `internal/adapters/sbom/syft.go` uses `github.com/anchore/syft/syft` to resolve sources, create SBOMs, encode SPDX JSON and CycloneDX JSON, and validate generated payloads.
- `internal/adapters/sbom/cdxgen.go` invokes a configured cdxgen executable only for repository CycloneDX generation and returns explicit `ErrCdxgenUnavailable` errors when disabled or missing.
- `internal/adapters/sbom/testdata/repository-fixture/` provides deterministic local repository input for generator tests.

## Out of scope
- SBOM service orchestration, REST endpoints, ContextVM methods, Nostr publisher orchestration, Blossom upload flow, and database repository integration remain intentionally outside this slice.
