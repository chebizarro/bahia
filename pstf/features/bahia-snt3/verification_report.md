# Verification Report: bahia-snt3

Date: 2026-07-03

## Observed vs intended behavior

- Observed: `web/src/routes/services/[id]/+page.svelte` used route-local constants and `sleep()` polling to wait for a service projection.
- Intended: service detail hydration should use the existing relay read-model subscription pipeline and react to `services` store changes produced from canonical `30900` read-model events.

## Changes verified

- Removed `SERVICE_DETAIL_WAIT_MS`, `SERVICE_DETAIL_POLL_MS`, `sleep()`, and `waitForServiceProjection()`.
- Added reactive service application from the `services` read-model store.
- Added reactive related build/artifact/environment refresh from read-model stores.
- Added Playwright coverage that starts the route after initial EOSE without the service projection, injects a live `30900` service projection via the mock relay, and expects the detail page to hydrate from that event.

## Commands

- `npm run lint` — passed; 0 Svelte diagnostics.
- `npm run test:unit` — passed; 73 test files / 576 tests.
- `npm run test:e2e -- --list service-detail-read-model-hydration.spec.js` — passed; 1 focused Chromium spec listed.
- `npm run test:e2e -- service-detail-read-model-hydration.spec.js` — blocked in this sandbox: Chromium failed to launch because macOS Mach port registration was denied. The required escalated rerun was rejected by the approval system due to environment usage limits.

## Remaining verification gap

Focused e2e execution still needs an unsandboxed Playwright run once approval capacity is available. No timer-based completion logic remains in the touched service detail route.
