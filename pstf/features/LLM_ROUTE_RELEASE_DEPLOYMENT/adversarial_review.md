# Adversarial Review – LLM_ROUTE_RELEASE_DEPLOYMENT

## Recommendation
block_until_major_findings_resolved

## Overall Risk
high

## Findings
### ADV-001 – Approved end-user browser workflow is still absent, so AC-009 is unsatisfied in the product surface users are supposed to ship
- Severity: major
- Category: ux
- Evidence:
  - AC `LLMRD-AC-009` explicitly requires a dedicated browser-visible workflow for route creation, release registration, deploy initiation, approval visibility, and route-state/activity observation.
  - `verification_report.md` marks `LLMRD-AC-009` as `Failed` and records that no dedicated LLM workflow exists under `web/src/routes`.
  - `test_matrix.json` shows `LLMRD-T-018` blocked because no dedicated end-user LLM workflow exists.
  - `defects.json` records `LLMRD-D-001` as an open major defect, and `hitl_decisions.md` decision `HITL-LLM_ROUTE_RELEASE_DEPLOYMENT-008` classifies it as a `BLOCKER`.
- Affected ACs: `LLMRD-AC-009`
- Affected tests: `LLMRD-T-018`, `LLMRD-T-019`
- Suggested action: Implement the dedicated browser workflow and unblock the corresponding E2E proof; do not substitute shared-store visibility for the approved user workflow.
- Requires HitL decision: no

### ADV-002 – Several accepted deployment failure branches can fail the run without emitting the terminal error reply AC-006 requires
- Severity: major
- Category: correctness
- Evidence:
  - `internal/service/llm_provisioning_coordinator.go:108-127` calls `failRun(...)` before `req` is populated when loading the intent fails, the intent is missing, route/release/environment loading fails, candidate selection fails, or provisioner resolution fails.
  - `internal/service/llm_provisioning_coordinator.go:220-236` only calls `publishError(...)` when `req.Route`, `req.Release`, and `req.Environment` are all non-nil.
  - Those early post-acceptance failure branches can therefore mark the run failed without ever publishing the terminal error result that AC `LLMRD-AC-006` requires.
  - `test_matrix.json` shows the needed coordinator tests `LLMRD-T-012` and `LLMRD-T-013` are still not implemented, so this path is both under-tested and code-risky.
- Affected ACs: `LLMRD-AC-006`
- Affected tests: `LLMRD-T-012`, `LLMRD-T-013`
- Suggested action: Patch the coordinator so every accepted deployment failure emits a terminal correlated error result, then add focused success/failure coordinator tests.
- Requires HitL decision: no

### ADV-003 – Route creation and release registration still bypass the canonical signer-first request path, so AC-002 and AC-003 are weaker than the accepted product contract suggests
- Severity: major
- Category: spec_gap
- Evidence:
  - `internal/controlplane/llm_command_publisher.go` only implements deploy, approval, and rollback publishers; there is no route-create (`5971`) or release-register (`5972`) requester publisher.
  - `internal/mcp/server.go:77-82` defines the MCP `LLMCommandPublisher` interface with only deploy, approval, and rollback methods.
  - `internal/mcp/server.go:2341-2460` shows `handleLLMCreateRoute` and `handleLLMRegisterRelease` directly calling `registry.CreateRoute(...)` and `registry.CreateRelease(...)` instead of publishing canonical signed requests.
  - `verification_report.md` already marks `LLMRD-AC-002` and `LLMRD-AC-003` only partially verified, and `defects.json` records `LLMRD-D-005` as open.
  - `hitl_decisions.md` accepts `D-005` as risk, but the source evidence shows this is not just missing verification; the current requester-facing implementation path differs from the approved signer-first contract.
- Affected ACs: `LLMRD-AC-002`, `LLMRD-AC-003`
- Affected tests: `LLMRD-T-002`, `LLMRD-T-003`, `LLMRD-T-004`, `LLMRD-T-005`
- Suggested action: Either implement canonical requester-side 5971/5972 publishing and acceptance tests, or explicitly narrow the approved contract for this release.
- Requires HitL decision: yes

### ADV-004 – Schema-backed artifact validation is missing, which weakens machine-checkable review discipline
- Severity: minor
- Category: operability
- Evidence:
  - `schemas/adversarial_review.schema.json` was not present in the repository.
  - `schemas/confidence_report.schema.json` was also missing during the confidence step.
  - Without schema files, artifact structure cannot be validated mechanically even though the prompts require schema conformance.
- Affected ACs: none
- Affected tests: none
- Suggested action: Add the missing schema files or document canonical JSON shapes in-repo so PSTF artifacts can be validated automatically.
- Requires HitL decision: no

## Suggested HitL Questions
- How should route creation and release registration be treated for this release given that current MCP tooling still mutates the registry directly instead of publishing canonical 5971/5972 requests?
  - Require canonical signer-first 5971/5972 request publishing before release
  - Allow direct MCP registry mutation as a temporary compatibility path for this release
  - Defer route/release creation from the approved release scope until canonical publishing exists
  - Revise AC-002 and AC-003 to describe the mixed contract explicitly

## Next Recommended Stage
hitl_clarification_then_patch_and_reverify
