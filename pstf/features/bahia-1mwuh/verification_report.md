# Verification Report — bahia-1mwuh

## Scope

Fleet task `fp-bahia-relay-policy-durability`, item 2 of 4: relay-policy UI/API truth states, safe provenance, unavailable-state Apply gating with signed audit evidence, and browser-local override schema migration. Deploy/health/backup/restore and upgrade digest work are excluded.

## Verification

- `go test ./internal/controlplane -count=1` — passed.
- 2026-08-03 Apply-boundary hardening: `go test -count=1 ./internal/controlplane` and `go build ./...` — passed.
- `npm run test:unit -- --run tests/unit/relay-settings-controlplane.test.js tests/unit/relay-override-storage.test.js` — 18 tests passed.
- `npm run lint` — Svelte diagnostics passed with 0 errors and 0 warnings.
- `npx playwright test tests/e2e/settings-relay-visibility.spec.js` — 12 tests passed.
- `npm run build` — production static build passed.

## Acceptance Mapping

- AC1: signer-first projection envelope normalization and Playwright document reload assert cached/stale values plus event ID/hash/source/last-sync provenance; a newer cached head cannot be downgraded by an older live replay.
- AC2: Go API tests, pure browser truth-state tests, and Playwright rendering distinguish live, cached/stale, unavailable, never-configured, and signed-empty truth.
- AC3: Playwright asserts loading/unavailable Apply gating, while Go handlers require either the exact projected head or an explicit unavailable-state replacement confirmation with a bounded change reference.
- AC4: browser and Go tests prove confirmation is separate from policy, reject stale heads and false unavailable claims, publish correlated service-authored confirmation evidence before canonical mutation, and fail closed if that audit publication fails.
- AC5: storage unit tests prove legacy array/object migration, re-scrubbing of current envelopes, malformed-data repair, and removal of credential/query/fragment-bearing URLs; Playwright proves persisted empty local override remains local.

## Security

Browser persistence and the server Apply boundary accept only credential-free `ws://`/`wss://` relay URLs without query strings or fragments. Malformed legacy values are not echoed to logs. Replacement evidence uses a fixed public reason code plus a bounded non-secret change/incident reference and is never persisted in browser storage. Mutations remain encrypted signer-first ContextVM; confirmation evidence is emitted by the service signer in kind `4903` before the canonical replacement.
