# Verification Report: WEB_ARTIFACT_DETAIL_UI_REGRESSIONS

Beads issue: `bahia-zgvc`

## Verification status

Partially verified. Static checks and targeted unit tests pass. Artifact SBOM e2e coverage is prepared but currently blocked by a Playwright auth-harness issue tracked as `bahia-g5d3`.

## Evidence

- Artifact SBOM tab code path reads embedded artifact projection fields and no longer invokes `getSBOM()` or `getSBOMAttestation()`.
- Playwright SBOM workflow tests were updated to fail if unsupported artifact SBOM endpoints are requested.
- Artifact detail UI CSS constrains overview card values and wraps long identifiers.
- Global code-block theme variables separate inline code colors from code-block colors; code blocks use a dark background with white text in light and dark themes.
- Artifact detail loading is relay projection-backed: the route calls `loadArtifacts()` and `loadServices()`, then resolves the artifact from the Nostr-backed artifact store. The erroneous `/api/v1/artifacts/:id` REST fallback was removed because it violates the repository's Nostr-first architecture.
- High-visibility labels in artifact verification, auth menu, DNS command forms, nav auth title, and profile publish status were changed to product-level wording.

## Tests run

- PASS: `npm run test:unit -- --run tests/unit/nav.test.js tests/unit/SBOMDetails.test.ts tests/unit/api-client-core.test.js tests/unit/api-client-extended.test.js tests/unit/api-client.test.js`
  - 5 files passed, 36 tests passed.
- PASS: `npm run test:unit -- --run tests/unit/docs-nostr.test.js tests/unit/docs-ui.test.js tests/unit/api-client-extended.test.js tests/unit/api-client-core.test.js tests/unit/api-client.test.js`
  - 5 files passed, 27 tests passed.
- PASS: `npm run lint`
  - `svelte-check found 0 errors and 0 warnings`.
- BLOCKED: `npx playwright test tests/e2e/sbom-workflow.spec.js -g "artifact (page displays SBOM attestation details|SBOM tab shows an empty state)" --workers=1`
  - Both selected tests fail before artifact route content renders.
  - Observed page state: AuthGuard remains at `Checking authentication...`; nav reports `No Signer` even with explicit `installE2EMocks(page, { authenticated: true, extension: true })`.
  - Tracked as `bahia-g5d3`.

## Review

- The prior artifact REST detail fallback was removed after architecture review; artifact detail remains backed by Nostr projection loading rather than request/response HTTP.

## Remaining work

- `bahia-g5d3`: fix Playwright auth harness so artifact SBOM e2e tests execute the route assertions.
- `bahia-ss5y`: audit and clean remaining admin/operator implementation-language leakage outside this bug slice.
