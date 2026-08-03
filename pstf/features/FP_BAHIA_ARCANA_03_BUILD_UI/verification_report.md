# FP_BAHIA_ARCANA_03_BUILD_UI verification

## Implemented

- Signed `build/request` ContextVM contract with exact public build-argument allowlist.
- Opaque, service-scoped credential references; no secret value field or browser persistence.
- Isolated `/builds` UI for request, projected status/log/evidence, and digest-pinned OCI candidates.
- Explicit fail-closed production wiring while the fleet Gitea/HiveCI initiator is unavailable.

## Known infrastructure blocker

`bahia-1tgwr` tracks the missing fleet Gitea private-mirror and HiveCI initiation adapter. The UI and handler must not be bypassed with a direct GitHub token-bearing runner.

## Verification

- PASS: `go test ./internal/controlplane/...`
- PASS: `cd web && npm test -- --run tests/unit/arcana-build.test.js tests/unit/nav.test.js` (13 tests)
- PASS: `cd web && npm run lint` (0 errors, 0 warnings)
- PASS: `cd web && npm run build`
- PASS: `go build ./...`
- FULL-SUITE EXCEPTION: `go test ./...` failed only in unrelated pre-existing `internal/soulfactory` test `TestOpenClawCommandDriverDefaultsToWrapperSupportedMethods`; it reproduced when that package was run alone.
