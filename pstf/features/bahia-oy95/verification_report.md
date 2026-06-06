# bahia-oy95 Verification Report

## Evidence

- `npx playwright test tests/e2e/ml-hf-vllm-fabric-smoke.spec.js tests/e2e/settings-relay-visibility.spec.js tests/e2e/soul-signing-smoke.spec.js tests/e2e/soul-provisioned-visibility.spec.js tests/e2e/navigation.spec.js` — 15 passed.
- `npx playwright test tests/e2e/dashboard-smoke.spec.js -g "should expose logo, menu, and linked dashboard cards" --workers=1` — 1 passed.

## Notes

- The first non-escalated Playwright run failed because Chromium could not launch in the macOS sandbox (`MachPortRendezvousServer ... Permission denied`). The same focused suite passed when rerun with approved elevated execution.
- A full `dashboard-smoke.spec.js` run passed its first seven tests and then failed with `ERR_CONNECTION_REFUSED` after the web server dropped; the specific logo/menu/card contract was rerun serially and passed.
- No failures were attributed to `bahia-p0sb` / `bahia-8myz` hydration or auth-guard work after seeding canonical relay-backed read-model events in the affected specs.
