# Verification Report — SOUL_FACTORY_PROVISIONING_TRACKING

## Summary
Current verification evidence does **not** satisfy the approved full-lifecycle contract for `SOUL_FACTORY_PROVISIONING_TRACKING`.

- **Partially verified:** `SFTP-AC-001`, `SFTP-AC-002`, `SFTP-AC-005`, `SFTP-AC-008`
- **Not verified because required tests are missing:** `SFTP-AC-003`, `SFTP-AC-004`, `SFTP-AC-006`, `SFTP-AC-010`
- **Failed product behavior:** `SFTP-AC-007`, `SFTP-AC-009`, `SFTP-AC-011`, `SFTP-AC-012`, `SFTP-AC-013`, `SFTP-AC-014`
- **Failing executed test caused by stale verification, not a confirmed product regression:** `SFTP-T-003` no longer matches current AuthGuard route-protection semantics for the no-extension case

The current repo proves a meaningful browser/store slice plus some backend structure, but the approved full-lifecycle contract is still blocked by explicit Bahia, CLI, MCP, timeout, and polling gaps.

## Commands Run
- `go test ./internal/soulfactory ./cmd/cli`
  - Result: pass
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm test -- --run tests/unit/souls-store.test.js`
  - Result: pass (`1` file, `33` tests)
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm run test:e2e -- tests/e2e/soul-signing-smoke.spec.js`
  - Result: fail (`6` passed, `1` failed)
  - Failure: `should show error when no extension available` timed out waiting for a `Continue` button because the route redirected to `/`

## Acceptance Criteria Status
| AC ID | Status | Basis |
| --- | --- | --- |
| SFTP-AC-001 | Partially verified | `web/tests/unit/souls-store.test.js` passed and proves relay-backed soul/template loads plus live store updates; no browser-level Souls gallery live-update test exists (`SFTP-T-002`). |
| SFTP-AC-002 | Partially verified | `web/tests/e2e/soul-signing-smoke.spec.js` proves signed `5950` request shaping, tag capture, and zero-accept rejection, but the suite failed its no-extension case and therefore does not fully verify signer-missing blocking semantics. |
| SFTP-AC-003 | Not verified | `internal/soulfactory/reactor.go` contains authorization and error-publication logic, but no `internal/soulfactory/reactor_test.go` exists to prove correlated `7950` terminal errors for unauthorized or malformed requests. |
| SFTP-AC-004 | Not verified | `internal/soulfactory/provisioner_full.go` records steps and publishes progress, but no integration suite proves the documented eight-stage correlated workflow or terminal success semantics. |
| SFTP-AC-005 | Partially verified | `internal/soulfactory/hardening_test.go` passed and proves skipped optional integrations do not fabricate outputs; no failure-path optional-integration test exists (`SFTP-T-007`). |
| SFTP-AC-006 | Not verified | Successful soul publication is present in code (`internal/soulfactory/provisioner_full.go`, `reactor.go`), but no automated proof exists for authoritative `31951` publication plus browser discoverability (`SFTP-T-008`, `SFTP-T-009`). |
| SFTP-AC-007 | Failed | `internal/soulfactory/bahia_integration.go` returns `ErrDeployableArtifactRequired` immediately from `CreateInitialDeployment`, so configured Bahia environments do not get the required initial deployment hookup. |
| SFTP-AC-008 | Partially verified | `web/tests/unit/souls-store.test.js` passed and proves signed `1950` action-event shaping; no integration suite proves malformed/unauthorized rejection and valid end-to-end action handling (`SFTP-T-012`). |
| SFTP-AC-009 | Failed | Bahia lifecycle helpers still return `ErrLifecycleUnsupported`, and suspend/resume/revoke handlers only warn on Bahia failure before publishing completed results. |
| SFTP-AC-010 | Not verified | `internal/soulfactory/lifecycle_handler.go` enforces a new brief and republish path in code, but no regenerate-specific test exists (`SFTP-T-014`). |
| SFTP-AC-011 | Failed | All Soul Factory CLI commands still return `soulFactoryUnavailableErr(...)`, and existing CLI tests only prove that stubbed behavior. |
| SFTP-AC-012 | Failed | MCP list/template/provision/action/regenerate handlers return explicit unavailable errors; only get/status provide partial real behavior. |
| SFTP-AC-013 | Failed | `web/src/lib/stores/souls.svelte.js` still uses a local timeout loop and `onClosed` as terminal failure signals, which conflicts with the approved event-driven contract. |
| SFTP-AC-014 | Failed | `internal/soulfactory/status_sync.go` still exposes `StartPeriodicSync` ticker polling as a synchronization path, contrary to the approved event-driven requirement. |

## Test Matrix Status
- Total tests in matrix: `18`
- Passing: `3`
- Failing executed tests: `1`
- Not implemented: `14`
- Blocked: `0`

### Passing tests
- `SFTP-T-001`
- `SFTP-T-006`
- `SFTP-T-011`

### Failing executed tests
- `SFTP-T-003` — stale no-extension Playwright case timed out after AuthGuard redirected `/souls/new` to `/`

### Not implemented tests
- `SFTP-T-002`, `SFTP-T-004`, `SFTP-T-005`, `SFTP-T-007`, `SFTP-T-008`, `SFTP-T-009`, `SFTP-T-010`, `SFTP-T-012`, `SFTP-T-013`, `SFTP-T-014`, `SFTP-T-015`, `SFTP-T-016`, `SFTP-T-017`, `SFTP-T-018`

## Defects
- `SFTP-D-001` — **major**: Bahia initial deployment hookup is not implemented for newly provisioned souls
- `SFTP-D-002` — **major**: Bahia-managed lifecycle actions do not propagate required runtime side effects
- `SFTP-D-003` — **major**: Soul Factory CLI commands remain explicit unavailable stubs
- `SFTP-D-004` — **major**: Soul Factory MCP mutation and list/template surfaces remain unavailable
- `SFTP-D-005` — **major**: Browser provisioning tracking still treats timeout and relay closure as terminal failure
- `SFTP-D-006` — **major**: Soul deploy-status synchronization still depends on periodic polling fallback
- `SFTP-D-007` — **minor**: Required backend provisioning and lifecycle verification suites are missing
- `SFTP-D-008` — **minor**: Browser verification coverage is incomplete and one required Playwright case is stale

## Ambiguities / Human Decisions Needed
No new human decision is required to classify the current failures.

The approved contract already settled the load-bearing product questions:
- full Soul Factory lifecycle is in scope now
- Bahia integration is strict must-have behavior now
- browser timeout / relay-close terminal completion is not approved behavior
- ticker-based backend polling is not approved behavior

The only ambiguity exposed by this session is local to test design: if the product still wants a visible no-extension experience at `/souls/new`, route-protection expectations should be clarified before rewriting the failing Playwright case. That ambiguity does not block the current defect set.

## Confidence Assessment
- **Moderate confidence** in the partial browser/store slice because the passing unit suite and most of the smoke E2E path prove relay-backed loads, signed publish shaping, and zero-accept rejection behavior.
- **Low confidence** in the approved full lifecycle because the backend reactor/provisioner/lifecycle/Bahia contract still lacks most of its required automated coverage.
- **High confidence** that AC-007, AC-009, AC-011, AC-012, AC-013, and AC-014 are presently failing the approved contract because the relevant source files contain explicit unavailable or unsupported branches.

## Recommendation
Do **not** mark `SOUL_FACTORY_PROVISIONING_TRACKING` verified or complete.

Recommended next sequence:
1. Patch the six open major product defects (`SFTP-D-001` through `SFTP-D-006`).
2. Add the missing backend/browser verification suites (`SFTP-D-007`, `SFTP-D-008`).
3. Re-run PSTF verification once the product gaps and stale Playwright case are addressed.
