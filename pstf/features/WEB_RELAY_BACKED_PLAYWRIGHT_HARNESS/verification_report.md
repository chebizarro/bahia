# WEB_RELAY_BACKED_PLAYWRIGHT_HARNESS Verification Report

## Beads

- Issue: `bahia-93sz` — Build relay-backed Playwright web harness

## Implemented evidence

- Added `cmd/bahia-test-relay`, a local `fiatjaf.com/nostr/khatru` relay process backed by `eventstore/slicestore`.
- The relay serves NIP-11 metadata and `/healthz`, and seeds signed events for:
  - system discovery `11316`
  - NIP-51 relay sets `30002`
  - NIP-65 relay list `10002`
  - canonical CAS read models `30900`
  - worker advertisement `10100`
  - audit/status/SBOM/docs/ContextVM announcement kinds used by web stores
- Added `web/tests/e2e/relay-harness.js` to start/stop the relay and install bootstrap/auth context without overriding `window.WebSocket`.
- Added `web/tests/e2e/relay-backed-web-functionality.spec.js` with visible UI assertions for dashboard, service/deployment/package/worker routes, DNS/FIPS mesh tabs, and Events.

## Verification so far

Command:

```sh
pnpm exec playwright test tests/e2e/relay-backed-web-functionality.spec.js --reporter=list
```

Result:

- 4 passed in 5.4s

## Findings during iteration

- The first failing run exposed that nostrlib `PubKey.String()` returns a debug `pk::` prefix. The harness now uses `PubKey.Hex()` for Nostr bootstrap and content fields.
- The DNS endpoint and FIPS mesh data were correctly available behind page tabs; tests now explicitly switch tabs rather than assuming default tab content.
- The relay directly serves seeded NIP-23 docs events, but the web docs route can cache an empty relay docs catalog during app startup timing. That is not part of the stable initial vertical slice and is tracked separately for follow-up coverage.

## Final verification

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

## Remaining work

- Follow-up Bead `bahia-e3r0` tracks expansion to assistant, Soul Factory, encrypted mutation, and deterministic relay-backed docs coverage.
