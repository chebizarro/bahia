# Web intent submission readiness: verification (bahia-1wb8l)

Branch `fix/intent-submission-readiness`, based on master `d7a21ea1`. All runs on macOS, 14 cores, Chromium, dev server on port 4184.

## Root causes

1. **Page remount on a repeated auth bootstrap.** `web/src/routes/+layout.svelte` calls `initializeAuth()` after `await boot()`, and `web/src/lib/components/AuthGuard.svelte` calls it on mount. AuthGuard's call resolves first. The layout's call then set `authState.status` to `'checking'` (`web/src/lib/stores/auth.svelte.js`, first statement of the bootstrap), which AuthGuard treats as loading, so it unmounted and remounted the routed page. A DOM trace at 6x CPU throttling showed content first rendered at about 660 ms, replaced by the auth spinner at about 790 ms and rendered again at about 810 ms, on `/dns`, `/ml` and `/llm` alike. Anything typed in that window was discarded. This caused every flaky-on-retry test in the report, including the two that publish no intent (`service-create-visibility-context`, `settings-relay-visibility`).
2. **Org-scoped control usable before the organization was known.** `requestLLMRollback` resolved the organization through `resolveIntentOrgId('llm', …)`, which threw `Select an organization before submitting this intent` while system discovery had not yet delivered `organization_id`. At 6x to 20x throttling the scratch reproduction failed 8 of 9 runs with that notice and zero signed intents. This is the hard failure `llm-route-release-deployment.spec.js:58`.

## Fix

- `initializeAuth()` shows `'checking'` only for an undetermined session; a repeat bootstrap re-evaluates in place.
- `intentReadiness(domain, { orgId, record, orgField })` decides readiness from local state only (auth session, intent client phase, organizations revealed by the local store). `IntentGate` and `ConfirmDialog` disable submitting controls while it is pending.

## Evidence

| Check | Before (master sources) | After |
| --- | --- | --- |
| Previously flaky specs (`llm-route-release-deployment`, `d69-d70-mutation-lifecycle`, `d72-dns-ml-intents`, `final-web-intents`), `--repeat-each=5 --retries=0`, `CI=1`, 4 CPU hogs | 76 passed, 14 failed | 90 passed, 0 failed |
| Other affected specs (`intent-submission-readiness`, `intent-lifecycle`, `sbom-workflow`, `service-create-visibility-context`, `settings-relay-visibility`), same flags and load | not run | 160 passed, 0 failed |
| Full Playwright suite, `CI=1` (retries 2, 1 worker) | 249 passed, 4 skipped, 0 failed (reported baseline) | 253 passed, 4 skipped, 0 failed, 0 flaky |
| `pnpm run test:unit` | | 1133 passed, 1 skipped |
| `pnpm run lint` (svelte-check) | | 0 errors, 0 warnings |
| `pnpm run build` | | succeeded |
| `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...` | | build and vet clean, 89 packages ok, 0 failed |

The 14 baseline failures were `d69-d70` DNS zone create (1), `d72` DNS endpoint (2) and DNS backend (4), and `final-web-intents` adoption scan (3) and security scan (4): the tests that type into a form before waiting on relay data.

## Regression found and fixed during verification

The first full-suite run on this branch failed `intent-lifecycle.spec.js:66` (acceptance 12). That spec stops the intent client in-page and disconnects the pool before creating a service. The first version of the signal treated a stopped client as still connecting, so the Create button never enabled; it also let relay discovery and catch-up decide whether an organization was still on its way. Both made submission depend on more than local state. Commit `a321146e` removes every relay input from the signal, and the unedited spec passes.

## Mutation checks

- Reverting the auth fix makes `fleet-scoped mutation keeps what was typed…` fail (mutation region attached 2 times, expected 1) and the new `auth-bootstrap` unit test fail (`'checking'`, expected `'authenticated'`).
- Making `IntentGate` never disable makes `org-scoped mutation stays disabled…` fail at `toBeDisabled()`.

## Not covered

- Mutations on the sensitive gift-wrapped transport (organizations, service secrets, notification channels, relay policy) resolve their organization in `orgIdFor()` and keep their own `sensitiveMutationBlocker` gating. The remount fix covers those pages; the org lookup there can still report `Select an organization before changing sensitive settings` before memberships are derived.
- `dashboard-smoke.spec.js` and `workers-events-smoke.spec.js` contain `waitForTimeout` sleeps that predate this work.
