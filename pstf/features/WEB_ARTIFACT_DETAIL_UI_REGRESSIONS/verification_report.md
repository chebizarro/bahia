# Verification Report: WEB_ARTIFACT_DETAIL_UI_REGRESSIONS

Beads issue: `bahia-zgvc`

## Verification status

Partially verified. Static checks and targeted unit tests pass. Artifact SBOM e2e coverage is prepared but currently blocked by a Playwright auth-harness issue tracked as `bahia-g5d3`.

## Evidence

- Artifact SBOM tab code path reads embedded artifact projection fields and no longer invokes `getSBOM()` or `getSBOMAttestation()`.
- Playwright SBOM workflow tests were updated to fail if unsupported artifact SBOM endpoints are requested.
- Artifact detail UI CSS constrains overview card values and wraps long identifiers.
- Global code-block theme variables separate inline code colors from code-block colors; code blocks use a dark background with white text in light and dark themes.
- Artifact detail loading now prefers the artifact REST read model for rich detail data, falls back to cached projections if needed, then uses a control-plane bootstrap fallback.
- High-visibility labels in artifact verification, auth menu, DNS command forms, nav auth title, and profile publish status were changed to product-level wording.

## Tests run

- PASS: `npm run test:unit -- --run tests/unit/nav.test.js tests/unit/SBOMDetails.test.ts tests/unit/api-client-core.test.js tests/unit/api-client-extended.test.js tests/unit/api-client.test.js`
  - 5 files passed, 36 tests passed.
- PASS: `npm run lint`
  - `svelte-check found 0 errors and 0 warnings`.
- BLOCKED: `npx playwright test tests/e2e/sbom-workflow.spec.js -g "artifact (page displays SBOM attestation details|SBOM tab shows an empty state)" --workers=1`
  - Both selected tests fail before artifact route content renders.
  - Observed page state: AuthGuard remains at `Checking authentication...`; nav reports `No Signer` even with explicit `installE2EMocks(page, { authenticated: true, extension: true })`.
  - Tracked as `bahia-g5d3`.

## Review

- Oracle review identified and the patch addressed: richer detail endpoint preference over minimal cached projections, reactive service derivation after async service loads, and separated inline-code/code-block contrast variables.

## Remaining work

- `bahia-g5d3`: fix Playwright auth harness so artifact SBOM e2e tests execute the route assertions.
- `bahia-ss5y`: audit and clean remaining admin/operator implementation-language leakage outside this bug slice.
