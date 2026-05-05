# Verification Report — LLM_ROUTE_RELEASE_DEPLOYMENT

## Summary
Current verification evidence supports the full approved slice of `LLM_ROUTE_RELEASE_DEPLOYMENT`, including rollback.

- **Verified:** AC-001 through AC-012
- **Newly closed in this session:** rollback publisher contract, rollback accepted-status reactor behavior, rollback n-1 target selection, rollback fail-closed behavior, and dedicated `/llm` browser rollback visibility
- **Open implementation tradeoff:** rollback target selection is behaviorally verified but still encoded through deployed-intent scan ordering rather than an explicit named selector

This session closed the remaining rollback gaps by adding signer-first browser rollback support to `/llm`, tightening the route-state panel layout so rollback remains clickable in-browser, extending deterministic browser harness coverage for positive and negative rollback flows, and adding direct backend rollback verification for request acceptance and target selection.

## Commands Run
### Previously retained passing evidence
- `go test ./internal/controlplane ./internal/service ./internal/repository ./internal/mcp`
  - Result: pass
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm test -- --run tests/unit/public-controlplane.test.js tests/unit/controlplane-store.test.js`
  - Result: pass
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm run test:e2e -- tests/e2e/llm-route-release-deployment.spec.js`
  - Result: pass for the non-rollback browser slice

### Commands run in this session
- `go test ./internal/controlplane ./internal/service`
  - Result: pass
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm test -- --run tests/unit/public-controlplane.test.js tests/unit/llm-page.test.js tests/unit/controlplane-store.test.js`
  - Result: pass (`3` files, `26` tests)
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm run test:e2e -- tests/e2e/llm-route-release-deployment.spec.js`
  - Result: pass (`2` tests)
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm run build`
  - Result: pass

## Acceptance Criteria Status
| AC ID | Status | Basis |
| --- | --- | --- |
| LLMRD-AC-001 | Verified | `internal/api/handlers/system_test.go` remains the system-info contract proof that LLM capabilities and kinds are advertised only when enabled. |
| LLMRD-AC-002 | Verified | `internal/controlplane/reactor_llm_requests_test.go`, `internal/controlplane/llm_command_publisher_test.go`, and `internal/mcp/server_llm_test.go` prove correlated signed `5971 -> 7971` handling and canonical requester publishing. |
| LLMRD-AC-003 | Verified | `internal/controlplane/reactor_llm_requests_test.go`, `internal/controlplane/llm_command_publisher_test.go`, and `internal/mcp/server_llm_test.go` prove correlated signed `5972 -> 7972` handling and canonical requester publishing. |
| LLMRD-AC-004 | Verified | `internal/controlplane/llm_command_publisher_test.go` proves canonical deploy tagging and zero-accept failure, `internal/controlplane/reactor_llm_requests_test.go` proves the first correlated deploy reply is `6973 accepted`, and browser helper / e2e coverage preserves the same signer-first contract. |
| LLMRD-AC-005 | Verified | `internal/controlplane/reactor_llm_requests_test.go` proves approve/reject/invalid-decision handling, `internal/service/llm_registry_test.go` proves rejection repair, and browser helper / e2e coverage proves the canonical `5974` user-facing approval flow. |
| LLMRD-AC-006 | Verified | `internal/service/llm_provisioning_coordinator_test.go` proves ordered success-path progress plus terminal completion and failure-path terminal error publication for accepted runs. |
| LLMRD-AC-007 | Verified | `internal/adapters/nostr/projector_test.go`, `internal/service/llm_registry_test.go`, and `internal/reconcile/llm_reconciler_test.go` prove authoritative route / route-state projection and observed-state reconciliation. |
| LLMRD-AC-008 | Verified | `web/tests/unit/controlplane-store.test.js` covers valid Bahia-authored LLM events, live updates after bootstrap, and spoofed-author rejection. |
| LLMRD-AC-009 | Verified | `web/src/routes/llm/+page.svelte` and `web/tests/e2e/llm-route-release-deployment.spec.js` prove the dedicated browser workflow for route creation, release registration, deploy initiation, approval, relay-backed state/activity visibility, and explicit non-use of deprecated `/api/v1/llm/**` mutation endpoints. |
| LLMRD-AC-010 | Verified | `internal/controlplane/llm_command_publisher_test.go` proves signed `5975` request shaping plus zero-accept failure, `internal/controlplane/reactor_llm_requests_test.go` proves the first rollback reply is correlated `6973 accepted`, and `web/tests/unit/public-controlplane.test.js` proves the browser rollback helper awaits the accepted/completed async lifecycle on the canonical kinds. |
| LLMRD-AC-011 | Verified | `internal/service/llm_registry_test.go` now proves rollback selects the previous deployed different release, supersedes the current desired deployed intent, moves desired state accordingly, and fails closed without mutation when no previous deployed different release exists. |
| LLMRD-AC-012 | Verified | `web/src/routes/llm/+page.svelte`, `web/tests/e2e/harnesses/llm-controlplane-public.js`, and `web/tests/e2e/llm-route-release-deployment.spec.js` now prove the dedicated `/llm` workflow exposes rollback, uses signer-first `5975`, surfaces positive and negative rollback outcomes from relay-backed activity/state, and does not rely on deprecated `/api/v1/llm/**` mutation endpoints. |

## Test Matrix Status
- Total tests in matrix: `25`
- Passing: `25`
- Not implemented: `0`
- Blocked: `0`
- Failing executed tests: `0`

### Passing tests
- `LLMRD-T-001` through `LLMRD-T-025`

## Defects
- `LLMRD-D-001` — **verified**: dedicated browser workflow implemented and covered by deterministic Playwright E2E
- `LLMRD-D-002` — **verified**: reactor-side signer-first contract coverage remains in place
- `LLMRD-D-003` — **verified**: coordinator success/failure coverage remains passing
- `LLMRD-D-004` — **verified**: spoofed-author browser-store negative coverage remains passing
- `LLMRD-D-005` — **verified**: canonical requester-side signer-first publishing now exists across publisher, MCP, browser helper, and E2E layers
- Browser-visible rollback gap from `HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-013` — **verified fixed**: `/llm` now exposes rollback and passes positive / negative deterministic browser coverage

## Ambiguities / Human Decisions Needed
No new human decision is required to verify the approved rollback-aware feature slice.

Previously approved rollback decisions now have direct verification evidence:
- `HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-012` — rollback target selection as previous deployed different release (`n-1` style)
- `HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-013` — missing browser-visible rollback classified as `FIX`
- `HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-014` — rollback-aware acceptance criteria approved for test generation

## Confidence Assessment
- **High confidence** in AC-002 through AC-006 because request publication, reactor handling, async coordinator behavior, and browser-visible request flows are covered by passing automated tests.
- **High confidence** in AC-007 and AC-008 because authoritative projection and browser-consumption tests remain passing.
- **High confidence** in AC-009 because the dedicated `/llm` workflow continues to pass deterministic end-to-end coverage without falling back to deprecated REST mutation endpoints.
- **High confidence** in AC-010 and AC-011 because rollback request publication, reactor acceptance, registry target selection, and no-target fail-closed behavior now have direct passing automated tests.
- **High confidence** in AC-012 because positive and negative rollback browser flows pass deterministic Playwright coverage and the `/llm` layout fix removed the panel-overlap usability defect that initially blocked clicking the rollback action.

## Recommendation
Behaviorally, the full approved feature slice is verified.

Recommended next sequence:
1. Treat rollback as part of the verified LLM control-plane contract.
2. If the implementation should become easier to reason about, refactor `RollbackWithMetadata` so the approved n-1 selector is expressed explicitly rather than implicitly through deployed-intent scan ordering.
