# SBOM_WORKFLOW_E2E Verification Report

## Status
Feature-specific verification passed for Bead `bahia-wbgi` canonical relay fixture alignment.

## Evidence
- `cd web && pnpm test:e2e --reporter=line tests/e2e/environments-crud-smoke.spec.js tests/e2e/sbom-workflow.spec.js`
  - Result: PASS
  - Evidence: 14 passed across the environment CRUD smoke and SBOM workflow specs.
- Previous full-suite gate evidence remains out of scope for this Bead; this update verifies the requested targeted specs.

## Notes
- Chromium had to be run outside the sandbox after the sandboxed launch failed with macOS Mach port permission errors.
- SBOM workflow fixtures now seed canonical relay-backed observables: artifact/service registry rows use `30900` with `domain`, `schema`, and `d` tags, while SBOM attestation/index activity uses canonical NIP-78 `30078` app-data events with `domain`, `schema`, `type`, and `d` tags.
- Artifact SBOM HTTP endpoints remain the documented HTTP-native SBOM/attestation detail surface; artifact registry row/detail hydration remains relay-backed.
