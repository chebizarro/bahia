# Verification Report — SOUL_FACTORY_PROVISIONING_TRACKING

## Summary
The focused patch set for `SFTP-D-001` through `SFTP-D-006` is verified.

- **Verified:** `SFTP-AC-007`, `SFTP-AC-009`, `SFTP-AC-011`, `SFTP-AC-012`, `SFTP-AC-013`, `SFTP-AC-014`
- **Still partially verified:** `SFTP-AC-001`, `SFTP-AC-002`, `SFTP-AC-005`, `SFTP-AC-008`
- **Still not fully verified because required tests are missing:** `SFTP-AC-003`, `SFTP-AC-004`, `SFTP-AC-006`, `SFTP-AC-010`
- **No open major defects remain in this patch scope.**

This session resolved the major product gaps for Bahia initial deployment hookup, Bahia-managed lifecycle side effects, CLI/MCP stubs, browser timeout/closure terminal semantics, and status-sync polling fallback. The remaining verification debt is now concentrated in the missing backend/browser suites and the stale Playwright no-extension case.

## Commands Run
- `go test ./internal/soulfactory ./cmd/cli`
  - Result: pass
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm test -- --run tests/unit/souls-store.test.js`
  - Result: pass (`1` file, `33` tests)
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm run build`
  - Result: pass
  - Note: Vite emitted an existing dynamic-import chunking warning for `web/src/lib/api/client.js`; build still completed successfully.

## Acceptance Criteria Status
| AC ID | Status | Basis |
| --- | --- | --- |
| SFTP-AC-001 | Partially verified | `web/tests/unit/souls-store.test.js` still proves relay-backed soul/template loads plus live store updates; no browser-level Souls gallery live-update test exists (`SFTP-T-002`). |
| SFTP-AC-002 | Partially verified | The existing smoke E2E still needs rewrite for the signer-missing/AuthGuard case; this patch did not rerun or replace that stale Playwright branch. |
| SFTP-AC-003 | Not verified | `internal/soulfactory/reactor.go` still lacks direct request-rejection coverage (`SFTP-T-004`). |
| SFTP-AC-004 | Not verified | The eight-stage correlated provisioning workflow still lacks its dedicated integration suite (`SFTP-T-005`). |
| SFTP-AC-005 | Partially verified | `internal/soulfactory/hardening_test.go` still proves skipped optional integrations; optional failure-path coverage (`SFTP-T-007`) is still missing. |
| SFTP-AC-006 | Not verified | Authoritative `31951` publication and browser discoverability still lack dedicated tests (`SFTP-T-008`, `SFTP-T-009`). |
| SFTP-AC-007 | Verified | `internal/soulfactory/bahia_integration.go` now creates synthetic build/artifact/deployment-intent hookup for configured Bahia environments, and `internal/soulfactory/bahia_integration_test.go` proves service registration, initial deployment hookup, and deploy-status visibility. |
| SFTP-AC-008 | Partially verified | Browser action-event shaping is still covered by `SFTP-T-011`, but malformed/unauthorized lifecycle-action rejection coverage (`SFTP-T-012`) is still missing. |
| SFTP-AC-009 | Verified | `internal/soulfactory/bahia_integration.go` and `internal/soulfactory/provisioner_full.go` now propagate managed deployment and signer side effects for suspend/resume/revoke/redeploy, and `internal/soulfactory/provisioner_full_lifecycle_test.go` proves those effects. |
| SFTP-AC-010 | Not verified | Regenerate behavior in code still lacks its dedicated integration suite (`SFTP-T-014`). |
| SFTP-AC-011 | Verified | `cmd/cli/soulfactory.go` now performs real signer-first query/mutation flows via `internal/soulfactory/nostr_client.go`, and `cmd/cli/soulfactory_test.go` covers list/get/template/provision/action behavior. |
| SFTP-AC-012 | Verified | `internal/soulfactory/mcp_server.go` now performs real relay-backed list/get/template/provision/action/regenerate/status flows, and `internal/soulfactory/mcp_server_test.go` verifies them. |
| SFTP-AC-013 | Verified | `web/src/lib/stores/souls.svelte.js` no longer treats local timeout or relay closure as terminal failure, and `web/tests/unit/souls-store.test.js` now encodes the approved event-driven contract. |
| SFTP-AC-014 | Verified | `internal/soulfactory/status_sync.go` now relies on explicit Bahia events without ticker polling, and `internal/soulfactory/status_sync_test.go` proves deploy-status propagation from event-driven updates. |

## Test Matrix Status
- Total tests in matrix: `18`
- Passing: `9`
- Failing executed tests: `1`
- Not implemented: `8`
- Blocked: `0`

### Passing tests
- `SFTP-T-001`
- `SFTP-T-006`
- `SFTP-T-010`
- `SFTP-T-011`
- `SFTP-T-013`
- `SFTP-T-015`
- `SFTP-T-016`
- `SFTP-T-017`
- `SFTP-T-018`

### Failing executed tests
- `SFTP-T-003` — stale no-extension Playwright case still assumes `/souls/new` stays on-page when AuthGuard now redirects unauthenticated access to `/`

### Not implemented tests
- `SFTP-T-002`, `SFTP-T-004`, `SFTP-T-005`, `SFTP-T-007`, `SFTP-T-008`, `SFTP-T-009`, `SFTP-T-012`, `SFTP-T-014`

## Defects
- `SFTP-D-001` — **verified**: Bahia initial deployment hookup now creates synthetic build/artifact/deployment-intent state for managed souls
- `SFTP-D-002` — **verified**: Bahia-managed lifecycle actions now propagate deployment and signer side effects instead of optimistic local-only completion
- `SFTP-D-003` — **verified**: Soul Factory CLI commands now use real signer-first relay-backed query/mutation flows
- `SFTP-D-004` — **verified**: Soul Factory MCP list/template/provision/action/regenerate surfaces now perform real work
- `SFTP-D-005` — **verified**: Browser provisioning tracking no longer treats local timeout or relay closure as terminal failure
- `SFTP-D-006` — **verified**: Soul deploy-status synchronization no longer depends on ticker polling fallback
- `SFTP-D-007` — **open minor**: required backend provisioning and lifecycle verification suites are still missing
- `SFTP-D-008` — **open minor**: browser verification coverage is still incomplete and one Playwright case remains stale

## Ambiguities / Human Decisions Needed
No new human decision is required for the patched defect cluster.

The remaining open work is verification debt, not unresolved product intent:
- backend request/provisioning/regenerate suites are still missing
- Souls gallery and fresh-visibility browser journeys are still missing
- the stale signer-missing Playwright case still needs to be rewritten against current AuthGuard behavior

## Confidence Assessment
- **High confidence** that the six patched major defects are resolved, because the implementation now matches the approved contract and each change has direct focused regression coverage.
- **Moderate confidence** in the feature overall, because the remaining unverified areas are still material and keep several ACs below full proof.
- **Low confidence** in the stale Playwright no-extension branch until that browser expectation is rewritten and rerun.

## Recommendation
Do **not** mark `SOUL_FACTORY_PROVISIONING_TRACKING` fully verified yet.

Recommended next sequence:
1. Implement the remaining backend suites for request rejection, eight-stage provisioning, authoritative `31951` publication, and regenerate behavior (`SFTP-D-007`).
2. Add the missing Souls gallery / post-provision browser journeys and rewrite the stale signer-missing Playwright case (`SFTP-D-008`).
3. Re-run PSTF verification after those remaining suites land.
