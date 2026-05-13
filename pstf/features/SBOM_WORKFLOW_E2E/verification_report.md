# SBOM_WORKFLOW_E2E Verification Report

## Status
Feature-specific verification passed; full-suite gate failed due to existing out-of-scope E2E drift.

## Evidence
- `npx playwright test tests/e2e/sbom-workflow.spec.js`
  - Result: PASS
  - Evidence: 5 passed in the focused SBOM workflow spec.
- `npx playwright test`
  - Result: FAIL
  - Evidence: 62 passed, 59 failed.
  - The new `web/tests/e2e/sbom-workflow.spec.js` tests all ran in this full-suite invocation and passed.

## Notes
- Chromium had to be run outside the sandbox after the sandboxed launch failed with macOS Mach port permission errors.
- Full-suite failures are concentrated in existing non-SBOM suites (auth guard compatibility, dashboard payment history, legacy CRUD/detail tests, soul/gallery tests, worker pricing/event labels). They appear unrelated to the SBOM workflow changes and many reflect stale REST-mocking assumptions against relay-backed pages.
- During E2E implementation, two product defects surfaced and were fixed:
  - Artifact SBOM empty responses retriggered loading forever because `sbomData` stayed null.
  - PolicyRuleBuilder buttons inside a form implicitly submitted the outer create form.
