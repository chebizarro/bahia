# Verification Report — LLM_ROUTE_RELEASE_DEPLOYMENT

## Summary
Current verification evidence still does not support marking `LLM_ROUTE_RELEASE_DEPLOYMENT` complete.

- **Verified:** AC-001, AC-004, AC-005, AC-007, and AC-008
- **Partially verified:** AC-002 and AC-003
- **Not verified due missing required tests:** AC-006
- **Failed product behavior:** AC-009

The reactor-side LLM request/result contract gap is now closed for route creation, release registration, deploy acceptance, approval/rejection handling, invalid decision rejection, and rejection repair. Remaining work is limited to requester-side relay-acceptance proof for route/release requests, async provisioning coordinator coverage, and the missing approved browser workflow.

## Commands Run
- `go test ./internal/controlplane ./internal/service`
  - Result: pass
- `go test ./internal/api/handlers ./internal/api/router ./internal/mcp ./internal/controlplane ./internal/adapters/nostr ./internal/service ./internal/reconcile`
  - Result: pass
- `cd /Users/bizarro/Documents/Projects/bahia/web && npm test -- --run tests/unit/controlplane-store.test.js`
  - Result: pass (`1` file, `9` tests)
- RepoPrompt path search for `llm` under `web/src/routes`
  - Result: no matches
- RepoPrompt path search for `internal/service/llm_provisioning_coordinator_test.go`
  - Result: no matches

## Acceptance Criteria Status
| AC ID | Status | Basis |
| --- | --- | --- |
| LLMRD-AC-001 | Verified | `internal/api/handlers/system_test.go` exists and package tests passed; enabled/disabled system info advertisement is covered directly. |
| LLMRD-AC-002 | Partially verified | `internal/controlplane/reactor_llm_requests_test.go` now proves a signed `5971` route-create request resolves through a correlated signed `7971` result with `e`/`p` reply tags and a created `route_id`, but it does not prove requester-side relay acceptance semantics. |
| LLMRD-AC-003 | Partially verified | `internal/controlplane/reactor_llm_requests_test.go` now proves a signed `5972` release-register request resolves through a correlated signed `7972` result containing `route_id`, `release_id`, and `status=success`, but it does not prove requester-side relay acceptance semantics. |
| LLMRD-AC-004 | Verified | `internal/controlplane/llm_command_publisher_test.go` proves canonical deploy request tagging and zero-accept failure, and `internal/controlplane/reactor_llm_requests_test.go` proves the first correlated reply is `6973` with `status=processing` and `step=accepted`. |
| LLMRD-AC-005 | Verified | `internal/controlplane/reactor_llm_requests_test.go` proves approve/reject/invalid-decision handling, and `internal/service/llm_registry_test.go` proves rejected intents are repaired out of desired state in favor of the previous deployed target. |
| LLMRD-AC-006 | Not verified | Coordinator code exists, but no provisioning coordinator success/failure tests exist, so async progress and terminal result behavior is still unverified. |
| LLMRD-AC-007 | Verified | `internal/adapters/nostr/projector_test.go`, `internal/service/llm_registry_test.go`, and `internal/reconcile/llm_reconciler_test.go` exist and package tests passed. |
| LLMRD-AC-008 | Verified | `web/tests/unit/controlplane-store.test.js` covers both valid Bahia-authored LLM events and spoofed non-Bahia LLM route/state/activity events. |
| LLMRD-AC-009 | Failed | Deprecated REST mutation endpoints are compatibility-only and tested, but no dedicated LLM browser workflow exists under `web/src/routes`, so the approved user-visible contract is not implemented. |

## Test Matrix Status
- Total tests in matrix: `19`
- Passing: `16`
- Not implemented: `2`
- Blocked: `1`
- Failing executed tests: `0`

### Passing tests
- `LLMRD-T-001`
- `LLMRD-T-002`
- `LLMRD-T-003`
- `LLMRD-T-004`
- `LLMRD-T-005`
- `LLMRD-T-006`
- `LLMRD-T-007`
- `LLMRD-T-008`
- `LLMRD-T-009`
- `LLMRD-T-010`
- `LLMRD-T-011`
- `LLMRD-T-014`
- `LLMRD-T-015`
- `LLMRD-T-016`
- `LLMRD-T-017`
- `LLMRD-T-019`

### Not implemented tests
- `LLMRD-T-012`
- `LLMRD-T-013`

### Blocked tests
- `LLMRD-T-018` — blocked because the dedicated end-user LLM browser workflow does not exist.

## Defects
- `LLMRD-D-001` — **major product defect**: approved dedicated browser workflow is missing (tracked by `bahia-zi0u`)
- `LLMRD-D-002` — **verified test defect**: reactor-side signer-first contract coverage for AC-002 through AC-005 was added and the targeted suites passed (tracked by `bahia-ftt9`)
- `LLMRD-D-003` — **minor test defect**: missing provisioning coordinator success/failure coverage for AC-006 (tracked by `bahia-a27v`)
- `LLMRD-D-004` — **verified test defect**: explicit spoofed-author LLM browser-store negative coverage was added for AC-008 (tracked by `bahia-5qrn`)
- `LLMRD-D-005` — **minor verification gap**: requester-side relay-acceptance semantics for route-create and release-register remain unproven by automated tests in this repository

## Ambiguities / Human Decisions Needed
No new human decision is required for the non-rollback slice verified here.

Existing deferred rollback ambiguity remains unchanged:
- `HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-004` deferred rollback AC/test generation pending replacement rollback semantics.

## Confidence Assessment
- **High confidence** in AC-004 and AC-005 because the new tests exercise the concrete signer-first event contracts and state transitions directly.
- **High confidence** in AC-001, AC-007, and AC-008 because the relevant automated tests exist and passed.
- **Moderate confidence** in AC-002 and AC-003 because Bahia-side correlation and persistence are now covered, but requester-side relay acceptance is still not directly proven here.
- **High confidence** that AC-009 currently fails because the approved browser workflow is absent and `web/src/routes` contains no LLM workflow path.
- **High confidence** that AC-006 is still not verifiable because the required coordinator tests do not yet exist.

## Recommendation
Do **not** mark `LLM_ROUTE_RELEASE_DEPLOYMENT` verified or complete.

Recommended next sequence:
1. Fix `LLMRD-D-005` by adding requester-side relay-acceptance coverage for route-create and release-register flows, or explicitly narrowing the verification claim if that responsibility sits outside this repository.
2. Fix `LLMRD-D-003` by adding provisioning coordinator success/failure coverage for AC-006.
3. Fix `LLMRD-D-001` by implementing the dedicated end-user LLM browser workflow required by AC-009.
4. Re-run PSTF verification after those additional defect fixes.
