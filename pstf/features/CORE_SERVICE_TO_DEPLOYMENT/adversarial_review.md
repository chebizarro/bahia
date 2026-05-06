# Adversarial Review – CORE_SERVICE_TO_DEPLOYMENT

## Recommendation
block_until_major_findings_resolved

## Overall Risk
high

## Findings
### MAJ-001 – Policy gate is bypassable through the canonical 5961 request surface
- Severity: major
- Category: security
- Evidence:
  - `web/src/routes/services/[id]/+page.svelte` enforces the gate only in the browser via `deployPolicyGateError` and `handleDeploy()`.
  - `internal/controlplane/reactor.go:400-479` creates deployment intents after only validating service/environment/artifact existence; it does not call `policyService.Evaluate` before `registry.CreateDeploymentIntent(...)`.
  - `internal/controlplane/reactor.go:1409-1444` implements policy evaluation separately under kind `5989`, and `internal/service/policy.go:49-70` shows block policies set `Allowed=false` and increment `Blockers`.
  - `docs/control-planes.md:67-115,156-173` and `docs/nostr-commands.md:1-81` treat `5961` and `5989` as canonical signer-first public events, so any authorized client can publish `5961` directly without using the web modal.
  - `docs/archive/REVIEW-AND-ROADMAP-2024.md:821-824` says to evaluate policies during `CreateDeploymentIntent` and block or warn based on enforcement.
- Affected ACs:
  - CSTD-AC-005
  - CSTD-AC-006
  - CSTD-AC-010
- Affected tests:
  - CSTD-T-007
  - CSTD-T-008
  - CSTD-T-009
  - CSTD-T-012
- Suggested action:
  - Decide whether policy gating is only a web-route UX requirement or an authoritative signer-first contract rule.
  - If authoritative, enforce policy evaluation in backend `5961` handling and add direct-request negative coverage.
- Requires HitL decision: yes

### MAJ-002 – Immediate post-create visibility is implemented by clearing user list state without an approved product decision
- Severity: major
- Category: ux
- Evidence:
  - `web/src/routes/services/+page.svelte:188-205` forces `searchQuery = ''`, `runtimeFilter = 'all'`, and `currentPage = 1` after successful create.
  - CSTD-AC-003 requires immediate visibility but does not approve clearing active filters or pagination to get there.
  - HITL-CORE_SERVICE_TO_DEPLOYMENT-001 approved immediate visibility, not the filter-reset tradeoff.
  - `web/tests/e2e/service-deployment-public-smoke.spec.js:17-31` exercises only the default unfiltered first page.
- Affected ACs:
  - CSTD-AC-003
  - CSTD-AC-010
- Affected tests:
  - CSTD-T-004
  - CSTD-T-012
- Suggested action:
  - Get a product decision on filtered/paged behavior, then add coverage for the approved behavior under active search/filter/page state.
- Requires HitL decision: yes

### MIN-003 – The verification set does not prove the in-flight preview-loading branch of the required gate
- Severity: minor
- Category: test_gap
- Evidence:
  - `web/src/routes/services/[id]/+page.svelte:852-857` has an explicit loading-state gate: `deployPolicyPreviewLoading` blocks create until preview finishes.
  - CSTD-AC-005 says deployment intent creation must stay blocked until preview succeeds.
  - The test matrix has failure coverage, blocker coverage, and happy-path coverage, but no test for a slow or still-loading preview.
  - `web/tests/e2e/service-deployment-policy-gate.spec.js` returns immediate preview responses and never proves the loading branch keeps `5961` unpublished.
- Affected ACs:
  - CSTD-AC-005
- Affected tests:
  - CSTD-T-007
  - CSTD-T-008
  - CSTD-T-012
- Suggested action:
  - Add a delayed-preview harness mode or component test that proves no `5961` publish occurs while preview is still loading.
- Requires HitL decision: no

### MIN-004 – Readiness artifacts overstate coverage by mis-mapping acceptance criteria and omitting structured confidence evidence
- Severity: minor
- Category: spec_gap
- Evidence:
  - CSTD-AC-008 is `/deployments` reactivity and CSTD-AC-009 is `/deployments/pending` reactivity; there is no separate AC for deployment detail rendering.
  - `verification_report.md` claims `CSTD-AC-009 | pass | Deployment detail and history remain rendered from public read models`, which does not match the AC text.
  - The smoke E2E visits deployment detail, but the test matrix does not map that to a dedicated functional acceptance criterion.
  - The feature directory has no `confidence_report.json` or `defects.json`; the readiness claim is only the narrative `Confidence: high` section in `verification_report.md`.
- Affected ACs:
  - CSTD-AC-008
  - CSTD-AC-009
  - CSTD-AC-010
- Affected tests:
  - CSTD-T-011
  - CSTD-T-012
- Suggested action:
  - Either add an explicit AC/test mapping for deployment detail rendering or remove that claim from the verification report.
  - If structured confidence/defect artifacts are intentionally omitted, say so explicitly.
- Requires HitL decision: no

## Suggested HitL Questions
1. Should deployment policy gating be enforced only in the web `/services/[id]` UX, or must backend `5961` deployment-intent creation reject policy-blocked requests from any authorized signer-first client?
   - Enforce policy during backend 5961 intent creation for all signer-first clients
   - Keep backend permissive and require the gate only in the web `/services/[id]` UX
   - Enforce backend blocking only for `block` policies and treat warnings as UI-only preflight
   - Defer policy-gate scope from this release
2. When a new service is created while `/services` is filtered, searched, or paged away from the first page, how should immediate visibility work?
   - Reset filters and pagination so the new service is always shown immediately
   - Preserve current list state and show a success message with a "view service" action
   - Preserve current list state and only auto-show/highlight the new service when it already matches the current view
   - Defer filtered-view behavior from this release

## Next Recommended Stage
hitl_risk_review_then_spec_and_test_matrix_revision
