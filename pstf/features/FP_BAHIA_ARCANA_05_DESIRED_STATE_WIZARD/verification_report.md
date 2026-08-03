# FP_BAHIA_ARCANA_05_DESIRED_STATE_WIZARD verification

## Implemented

- Versioned persisted managed Compose runtime configuration and validation.
- Backend-authoritative signed desired-state preview with latest deployed baseline, exact sanitized state, SHA-256 hash, and policy result.
- Immutable registered-artifact and opaque secret-reference enforcement; runtime apply decrypts only reviewed secret IDs.
- Compose projection and rendering for process, health, restart, volume, and resource configuration.
- Five-step signer-first browser wizard with policy/cost review, signed update, stale-hash-protected idempotent deploy, and existing approval routing.
- Arcana-shaped browser acceptance coverage for port 8080 and HTTP GET /healthz without Arcana defaults in generic code.

## Verification

- PASS: `go test ./internal/domain ./internal/service ./internal/controlplane ./internal/adapters/runtime ./internal/adapters/nostr`.
- PASS: `go build ./...`.
- PASS: `cd web && npm run test:unit -- --run tests/unit/deployment-desired-state.test.js tests/unit/public-controlplane.test.js` (2 files, 23 tests).
- PASS: `cd web && npm run lint` (0 errors, 0 warnings).
- PASS: `cd web && npm run build`.
- PASS: `cd web && npx playwright test tests/e2e/deployment-unit-targeting.spec.js tests/e2e/service-deployment-policy-gate.spec.js tests/e2e/service-deployment-public-smoke.spec.js` (6 tests).
- FULL-SUITE EXCEPTION: `go test ./...` reaches only the known pre-existing `internal/soulfactory` failure `TestOpenClawCommandDriverDefaultsToWrapperSupportedMethods`, tracked by `bahia-csxyx`; all wizard and affected packages pass.
