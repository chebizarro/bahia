# WEB_RELAY_BACKED_PLAYWRIGHT_HARNESS Verification Report

## Beads

- Issue: `bahia-93sz` — Build relay-backed Playwright web harness
- Issue: `bahia-e3r0` — Expand relay-backed Playwright harness to assistant, souls, encrypted mutations, and docs
- Issue: `bahia-w07w` — Assistant bubble click does not toggle panel in Playwright relay harness

## Implemented evidence

- Added `cmd/bahia-test-relay`, a local `fiatjaf.com/nostr/khatru` relay process backed by `eventstore/slicestore`.
- The relay serves NIP-11 metadata and `/healthz`, and seeds signed events for:
  - system discovery `11316`
  - NIP-51 relay sets `30002`
  - NIP-65 relay list `10002`
  - canonical CAS read models `30900`
  - worker advertisement `10100`
  - audit/status/SBOM/docs/ContextVM announcement kinds used by web stores
  - assistant session/status events (`30900`, `30315`)
  - Soul Factory template/soul/draft/runtime capability events (`31950`, `31951`, `31952`, `30317`)
  - NIP-23 docs events (`30023`)
- The relay now handles encrypted ContextVM mutation requests by decrypting NIP-44 gift-wrap kind `1059`, validating the signed inner `25910` request event, storing a service-signed result, and broadcasting an encrypted kind `1059` result tagged to the request.
- Added `web/tests/e2e/relay-harness.js` to start/stop the relay, install bootstrap/auth context without overriding `window.WebSocket`, expose real NIP-07-style signing/NIP-44 helpers, and persist the relay list for browser-side Nostr pool helpers.
- Added `web/tests/e2e/relay-backed-web-functionality.spec.js` with visible UI assertions for dashboard, service/deployment/package/worker routes, DNS/FIPS mesh tabs, Events, Assistant UI hydration through a real Assistant bubble click, Soul Gallery hydration, encrypted ContextVM mutation publish/result, and deterministic docs cache bypass.

## Verification

Command:

```sh
pnpm exec playwright test tests/e2e/relay-backed-web-functionality.spec.js --reporter=list
```

Result:

- 8 passed in 8.5s

Command:

```sh
go test ./cmd/bahia-test-relay
```

Result:

- Passed; package builds successfully (`[no test files]`).

Command:

```sh
pnpm lint
```

Result:

- Passed; `svelte-check found 0 errors and 0 warnings`.

## Findings during iteration

- The original relay-backed vertical slice proved dashboard, route, DNS/FIPS mesh, and Events hydration through real REQ/EVENT/EOSE.
- Expanding assistant coverage initially opened the panel through the assistant store. `bahia-w07w` fixed the production click path by attaching the bubble toggle listener through a component action, waiting for hydration readiness, and asserting the real bubble click changes `aria-expanded` to `true` before checking the relay-backed transcript.
- The encrypted mutation test uses the app's default NIP-44 gift-wrap kind `1059`, verifies relay OK acceptance, and observes an encrypted service result event. The direct short-lived transport teardown can emit the browser-native `WebSocket is already in CLOSING or CLOSED state.` message; the test allow-lists only that exact teardown message.
- Relay-backed docs fetching requires connecting the docs Nostr pool before bypassing the cache. The regression test explicitly connects to the local relay, writes a stale empty cache snapshot, and then calls `fetchDoc(..., { bypassCache: true })` to prove deterministic NIP-23 retrieval.

## Remaining work

- No remaining work in this feature slice.
