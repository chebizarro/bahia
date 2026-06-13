# Verification Report: WEB_IMPLEMENTATION_LANGUAGE_CLEANUP

Beads issue: `bahia-ss5y`

## Verification status

Verified for the touched web UI scope.

## Evidence

- Relay status copy changed from EOSE-facing language to user-facing sync language:
  - `EOSE pending` -> `Initial sync pending`
  - `Connected, EOSE received` -> `Connected and up to date`
  - `Last EOSE` -> `Last sync`
- Settings relay labels were productized:
  - request relays, repository relays, relay monitor pubkeys, notification message relays, managed relay targets, and relay policy updates no longer expose NIP/ContextVM wording in visible labels.
- Documentation, DNS, events, ML, profile/settings, soul creation/editing, worker cleanup, user menu, deployment run detail, and repository search copy were updated to remove high-visibility implementation terms.
- Markdown documentation code blocks now use `--code-block-bg` and `--code-block-text`, preserving white code-block text in light mode.
- Static search over `web/src/lib/components` and `web/src/routes` shows remaining matches are internal callback/field names, CSS states, error sanitizers, or diagnostic data fields rather than high-visibility copy.
- No REST API endpoint or REST fallback was added.

## Tests run

- PASS: `npm run test:unit -- --run tests/unit/connection-status.test.js`
  - 1 file passed, 5 tests passed.
- PASS: `npx playwright test tests/e2e/sbom-workflow.spec.js -g "artifact (page displays SBOM attestation details|SBOM tab shows an empty state)" --workers=1 --reporter=line`
  - 2 passed.
- PASS: `npx playwright test tests/e2e/settings-relay-visibility.spec.js --workers=1 --reporter=line`
  - 7 passed.
- PASS: `npm run lint`
  - `svelte-check found 0 errors and 0 warnings`.

## Non-blocking observation

- `npx playwright test tests/e2e/deployment-history-and-run-details.spec.js -g "loads completed run logs from Bahia service records" --workers=1 --reporter=line` renders the run heading and updated copy after the route was converted away from timeout polling, but the unrelated encrypted-log harness remains pending at `Loading run logs...` and does not complete the log assertion in this pass.

## Remaining work

- None for `bahia-ss5y` in the touched high-visibility web copy scope.
