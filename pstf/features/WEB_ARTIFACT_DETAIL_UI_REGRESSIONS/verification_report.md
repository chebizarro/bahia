# Verification Report: WEB_ARTIFACT_DETAIL_UI_REGRESSIONS

Beads issues: `bahia-zgvc`, `bahia-g5d3`, `bahia-a0zf`, `bahia-cg7j`

## Verification status

Verified for the touched artifact detail/SBOM scope. The artifact route renders relay-backed artifact projections, the SBOM tab uses embedded projection data, and unsupported artifact SBOM REST endpoints are not called.

## Evidence

- Artifact detail loading remains Nostr-first. The route starts `loadArtifacts()` / `loadServices()` and reacts to the relay-backed artifact collection when the matching projection arrives.
- The route no longer depends on a one-shot stale collection read during bootstrap; artifact projections received during relay catch-up now render the page instead of leaving `Loading artifact...` indefinitely.
- Empty SBOM state no longer fabricates an `Unknown / 0` attestation summary from an empty `sbom_packages` array.
- Playwright SBOM workflow tests fail immediately if `/api/v1/artifacts/:id/sbom` or `/api/v1/artifacts/:id/sbom/attestation` is requested.
- The SBOM fixture uses the repository's supported read-model transport shape: kind `30900` with `schema=bahia.registry.artifact.v1` and `legacy_kind=31966` metadata.
- Artifact detail code does not add or restore any REST artifact detail/SBOM fallback.
- Artifact registry list columns now map the actual relay-backed projection shape: `image_repo`/`repository` for Name, `version`/`image_tag`/`tag` for Version, and `digest`/`image_digest` for a dedicated Digest column. The list hydration reacts to collection updates from relay catch-up instead of freezing on the initial empty projection.
- Registry list coverage includes the observed ambiguous shape where generic `name` equals the tag/version while `image_repo` carries the repository display name; Version still renders from `image_tag`.

## Tests run

- PASS: `npx playwright test tests/e2e/sbom-workflow.spec.js -g "artifact registry list maps" --workers=1 --reporter=line`
  - 1 passed.
- PASS: `npx playwright test tests/e2e/sbom-workflow.spec.js -g "artifact (registry list maps|page displays SBOM attestation details|SBOM tab shows an empty state)" --workers=1 --reporter=line`
  - 3 passed.
- PASS: `npm run test:unit -- --run tests/unit/connection-status.test.js`
  - 1 file passed, 5 tests passed.
- PASS: `npm run lint`
  - `svelte-check found 0 errors and 0 warnings`.

## Related observations

- While verifying the deployment run copy touched in this pass, the route was found to contain timeout/poll-based projection waiting. That touched-path violation was removed and replaced with reactive application of relay-backed `deploymentRuns` updates.
- `npx playwright test tests/e2e/deployment-history-and-run-details.spec.js -g "loads completed run logs from Bahia service records" --workers=1 --reporter=line` now renders the run heading and updated copy, but the unrelated encrypted-log harness does not deliver the result before the assertion and remains pending at `Loading run logs...`. This is not an artifact/SBOM blocker.

## Remaining work

- None for `bahia-g5d3` in the artifact SBOM route scope.
