# bahia-oikr Verification Report

## Intended behavior

Navigation E2E should run against the mocked API and relay-backed bootstrap harness without mocked-backend console noise, while still failing on unexpected application console errors or uncaught page errors. The central docs navigation assertion remains part of the verified spec.

## Evidence

- `cd web && npm run test:e2e -- navigation.spec.js -g 'exposes global and contextual documentation actions'` — 1 passed on 2026-06-07.
- `cd web && npm run test:e2e -- navigation.spec.js -g 'should render navigation without errors'` — 1 passed on 2026-06-07 with the shared runtime guard and no console allowlist.
- `cd web && npm run test:e2e -- navigation.spec.js` — 5 passed on 2026-06-07.

## Notes

- The initial non-escalated Playwright run on 2026-06-07 failed before app code because Chromium could not register its macOS Mach port inside the execution sandbox (`bootstrap_check_in ... Permission denied`). Focused Playwright verification therefore requires approved elevated execution in this environment.
- No Nostr event kinds or production runtime behavior changed; this slice tightens E2E test error detection around the existing mocked bootstrap harness.
