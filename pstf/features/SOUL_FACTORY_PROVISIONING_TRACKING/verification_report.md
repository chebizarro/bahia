# Verification Report — SOUL_FACTORY_PROVISIONING_TRACKING

## Summary
The full approved Soul Factory slice is now verified.

- **Verified:** `SFTP-AC-001` through `SFTP-AC-014`
- **Open defects:** none
- **Matrix status:** all 18 mapped tests now pass

This session closed the remaining verification debt by adding the missing backend reactor/provisioner/lifecycle suites, adding the missing Souls gallery and post-provision browser journeys, and rewriting the stale signer-missing Playwright case to match the current protected-route contract.

## Commands Run
- `go test ./internal/soulfactory ./cmd/cli`
  - Result: pass
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm test -- --run tests/unit/souls-store.test.js`
  - Result: pass (`1` file, `33` tests)
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm run test:e2e -- tests/e2e/soul-signing-smoke.spec.js tests/e2e/souls-gallery-live.spec.js tests/e2e/soul-provisioned-visibility.spec.js`
  - Result: pass (`9` tests)
  - Note: Vite logged existing proxy warnings for `/api/v1/services` and `/api/v1/environments` during dashboard bootstrap in browser tests; the mocked Nostr flows and asserted browser behavior still passed.
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm run build`
  - Result: pass
  - Note: Vite emitted the existing dynamic-import chunking warning for `web/src/lib/api/client.js`; build still completed successfully.

## Acceptance Criteria Status
| AC ID | Status | Basis |
| --- | --- | --- |
| SFTP-AC-001 | Verified | `web/tests/unit/souls-store.test.js` and `web/tests/e2e/souls-gallery-live.spec.js` prove relay-backed Souls bootstrap plus live update behavior. |
| SFTP-AC-002 | Verified | `web/tests/e2e/soul-signing-smoke.spec.js` proves the signer-first browser provisioning flow, current protected-route signer-missing branch, relay rejection handling, and request tag shaping. |
| SFTP-AC-003 | Verified | `internal/soulfactory/reactor_test.go` proves unauthorized and malformed `5950` requests publish correlated `7950` errors. |
| SFTP-AC-004 | Verified | `internal/soulfactory/reactor_test.go` proves the eight-stage full provisioner path records ordered steps, publishes correlated `6950` progress, and finishes with terminal success. |
| SFTP-AC-005 | Verified | `internal/soulfactory/hardening_test.go` proves unconfigured optional integrations are skipped, and `internal/soulfactory/reactor_test.go` proves optional workspace failure is recorded without fabricated success. |
| SFTP-AC-006 | Verified | `internal/soulfactory/reactor_test.go` proves authoritative `31951` + correlated `7950` success publication, and `web/tests/e2e/soul-provisioned-visibility.spec.js` proves the published soul becomes visible through relay-backed browsing. |
| SFTP-AC-007 | Verified | `internal/soulfactory/bahia_integration_test.go` proves service registration, initial deployment hookup, and deploy-status visibility. |
| SFTP-AC-008 | Verified | `web/tests/unit/souls-store.test.js` proves action-event shaping, and `internal/soulfactory/lifecycle_handler_test.go` proves malformed/unauthorized action rejection plus valid correlated lifecycle action handling. |
| SFTP-AC-009 | Verified | `internal/soulfactory/provisioner_full_lifecycle_test.go` proves suspend/resume/revoke/redeploy propagate managed side effects for Bahia-managed souls. |
| SFTP-AC-010 | Verified | `internal/soulfactory/lifecycle_handler_test.go` proves regenerate requires a new brief and republishes updated identity content with a correlated completion result. |
| SFTP-AC-011 | Verified | `cmd/cli/soulfactory_test.go` proves the CLI performs real signer-first query and mutation flows. |
| SFTP-AC-012 | Verified | `internal/soulfactory/mcp_server_test.go` proves the MCP surface performs real query, mutation, and run-status flows. |
| SFTP-AC-013 | Verified | `web/tests/unit/souls-store.test.js` proves provisioning tracking no longer treats local timeout or relay closure as terminal failure. |
| SFTP-AC-014 | Verified | `internal/soulfactory/status_sync_test.go` proves deploy-status sync is driven by explicit events rather than ticker polling. |

## Test Matrix Status
- Total tests in matrix: `18`
- Passing: `18`
- Failing: `0`
- Not implemented: `0`
- Blocked: `0`

### Passing tests
- `SFTP-T-001` through `SFTP-T-018`

## Defects
- `SFTP-D-001` — **verified**
- `SFTP-D-002` — **verified**
- `SFTP-D-003` — **verified**
- `SFTP-D-004` — **verified**
- `SFTP-D-005` — **verified**
- `SFTP-D-006` — **verified**
- `SFTP-D-007` — **verified**
- `SFTP-D-008` — **verified**

## Ambiguities / Human Decisions Needed
No new human decision is required.

The approved feature slice is now fully verified against the current acceptance criteria and mapped tests.

## Confidence Assessment
- **High confidence** in the full approved Soul Factory slice: each acceptance criterion now has direct automated evidence at the mapped level.
- **High confidence** in the remaining browser journeys: the missing gallery/live-update and fresh-visibility paths are now covered with deterministic relay mocks instead of timing-based heuristics.
- **High confidence** in the backend reactor/lifecycle contracts: the new suites capture signed publications in-process and assert correlated result semantics directly.

## Recommendation
`SOUL_FACTORY_PROVISIONING_TRACKING` can move to confidence scoring / adversarial review / final HITL review for the approved slice.
