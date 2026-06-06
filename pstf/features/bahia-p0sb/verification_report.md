# Verification Report — bahia-p0sb

## Intended behavior
Relay-backed dashboard, worker, and event read models hydrate only from scoped Nostr subscriptions. Bootstrap waits for historical EVENT delivery to finish with EOSE before callers treat stores as loaded, and live subscriptions remain open for realtime updates. Activity views render canonical activity kinds (`4903`, `30315`, `30078`) rather than discovery/read-model bootstrap events.

## Changes verified
- `bootstrapControlplane()` now resolves after connected relays emit EOSE for the scoped read-model subscription, or fails on CLOSED before catch-up completes.
- Unhandled discovery/read-model events no longer populate dashboard activity; canonical audit/status/SBOM activity still routes to activity.
- Playwright relay fixtures for dashboard/controlplane/workers now emit canonical `30900` worker/control-plane state and `4903` audit activity with required schema/domain/type coordinates.

## Commands run
- `cd web && pnpm test:e2e --reporter=line tests/e2e/workers-events-smoke.spec.js` — **PASS**, 17/17.
- `cd web && pnpm test:e2e --reporter=line tests/e2e/controlplane-nostr-smoke.spec.js tests/e2e/dashboard-smoke.spec.js` — **PASS**, 26/26.
- `cd web && pnpm test:e2e --reporter=line tests/e2e/environments-crud-smoke.spec.js` — **FAIL**, separate stale resource fixture/legacy REST mutation assertions; tracked in Bead `bahia-wbgi`.
- `cd web && pnpm test:e2e --reporter=line tests/e2e/sbom-workflow.spec.js` — **FAIL**, separate stale artifact/SBOM resource fixture alignment; tracked in Bead `bahia-wbgi`.

## Remaining work
- Bead `bahia-wbgi`: update resource CRUD/SBOM e2e fixtures to canonical relay observables and current Nostr mutation expectations.
