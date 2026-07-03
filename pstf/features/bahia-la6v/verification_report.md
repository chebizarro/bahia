# bahia-la6v Verification Report

## Summary

Updated the stale services and policies CRUD Playwright specs to match the relay-native control-plane UI. The app behavior was treated as authoritative; no production app code or shared helper/harness files were changed.

## Evidence

- `web/tests/e2e/services-crud-smoke.spec.js` now installs the public relay harness, hydrates services from canonical read models, and asserts service creation publishes unwrapped ContextVM kind `25910` with accepted `OK`, correlated result, and `30900` service projection evidence.
- `web/tests/e2e/policies-crud-smoke.spec.js` now hydrates policies/environments from relay read models, uses a spec-local policy CRUD relay harness, and asserts policy create/update/delete/evaluate publish canonical kind `25910` requests with accepted `OK`, result events, and canonical `30900` policy projections/tombstones.
- Static scan found no `waitForTimeout`, `networkidle`, or REST `/api/v1` route mocks in the two updated specs.

## Verification

- Initial sandboxed Playwright run failed before test logic because macOS Chromium could not register `MachPortRendezvousServer` inside the sandbox (`bootstrap_check_in ... Permission denied`).
- Reran outside the sandbox:
  - `npx playwright test tests/e2e/services-crud-smoke.spec.js tests/e2e/policies-crud-smoke.spec.js --reporter=line`
  - Result: **14 passed**.

## Remaining Work

No remaining work was identified in the `bahia-la6v` scope.
