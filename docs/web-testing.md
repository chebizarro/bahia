# Bahia Web App Testing

The web app is store-first for shared state and signer-first for every
mutation. Tests exercise those boundaries: relay subscriptions, `EOSE`
ordering, NIP-42 auth, signed intents and their status, gift-wrapped
sensitive intents, and decryption of confidential records. HTTP is mocked
only for the few routes the app actually calls.

## Commands

From `web/`:

```bash
pnpm test                       # vitest run
pnpm test:unit                  # vitest run --config vitest.config.js
pnpm test:unit:watch
pnpm test:unit:coverage
pnpm test:unit:coverage:llm     # route-access, nav and LLM page with coverage thresholds
pnpm test:unit:coverage:soulfactory

pnpm test:e2e                   # playwright against `pnpm dev` on 127.0.0.1:4173 (BAHIA_E2E_PORT)
pnpm test:e2e:prod              # playwright against `vite preview` of a production build on :4174
pnpm test:e2e:headed
pnpm test:e2e:ui
pnpm test:e2e:joined            # assistant suite against a real daemon (see below)

pnpm lint                       # svelte-kit sync + svelte-check
pnpm build
```

Focused runs:

```bash
pnpm exec vitest run tests/unit/store-first-services.test.js
pnpm exec playwright test tests/e2e/service-deployment-public-smoke.spec.js
```

## Configuration

- `vitest.config.js`: jsdom, globals, `tests/setup/vitest.setup.js`,
  `tests/unit/**/*.test.{js,ts}`; IndexedDB-backed tests use `fake-indexeddb`.
- `playwright.config.js`: Chromium, `tests/e2e`, `webServer` runs `pnpm dev`
  on `127.0.0.1:${BAHIA_E2E_PORT:-4173}` and reuses an existing server
  outside CI.
- `playwright.prod.config.js`: builds and serves `vite preview` on `4174`.
- CI (`.github/workflows/web-vitest-unit.yml`, `web-playwright-e2e.yml`)
  forbids focused tests, retries twice and uses one Playwright worker.

## Unit-test areas

| Behaviour | Tests (`tests/unit/`) |
|---|---|
| Store-first hydration and subscriptions | `store-first-*.test.js`, `rest-store-first.test.js`, `pool-read-model-metadata.test.js` |
| Relay pool parsing and recovery | `nostr-client-parsing.test.js`, `nostr-pool.test.js`, `relay-harness.test.js` |
| Discovery and bootstrap | `discovery-store.test.js`, `controlplane-store.test.js`, `connection-status.test.js` |
| Intents: signing, fixtures, gift wrap, readiness, pending rows | `intent-*.test.js`, `domain-intent-fixtures.test.js`, `d69-d70-domain-intents.test.js`, `d72-intent-content-fixtures.test.js`, `final-ops-intents.test.js`, `last-ops-intents.test.js`, `pending-intents.test.js`, `rekey-intent-fixture.test.js` |
| Encrypted RPC and confidential stores | `encrypted-controlplane.test.js`, `encrypted-domain-stores.test.js`, `encrypted-route-stores.test.js` |
| Published docs and repositories | `docs-nostr.test.js`, `repositories-nip34.test.js`, `repositories-store.test.js` |
| LLM and SoulFactory UI | `llm-page.test.js`, `souls-page.test.js`, `souls-store.test.js` |
| Architecture gates | `architecture-gates.test.js` (no `setInterval`/HTTP client in stores), `no-legacy-api-client.test.js` |

Wire fixtures under `tests/fixtures/*intent*.json` are generated from Go;
`intent-cross-language.test.js` asserts the web produces byte-identical
intents. Reset stateful modules with `vi.resetModules()` and restore timers,
mocks, IndexedDB and `localStorage` in cleanup.

## Relay behaviour to assert

- Stored events arrive before `EOSE`; the subscription stays open and live
  events keep flowing; a `CLOSED` is reissued from the last-seen cursor.
- An incomplete `EOSE` barrier yields degraded metadata, never a terminal
  state.
- Protected filters get `AUTH` then `CLOSED auth-required:` until the
  session signer answers; an unadmitted pubkey yields `restricted:`.
- Replaceable events reduce by `(kind, pubkey, d)`, newest `created_at`,
  lowest id on a tie; a payload `updated_at` never promotes a losing event.
- Intents: relay `OK` is delivery; the test waits for the requester-scoped
  `30315` status or the canonical record, never for an HTTP response.
- Gift-wrapped intents and ContextVM replies keep the wrapper lifetime of
  the request and correlate by `e=<outer id>,reply`.

## Playwright harnesses

`tests/e2e/harnesses/` provides in-process relay harnesses so suites run
without a daemon:

- `service-deployment-public.js` — discovery, read models and signed
  service/deployment intents;
- `llm-controlplane-public.js` — LLM route, release and deployment;
- `notifications-encrypted.js` — gift-wrapped notification intents and
  encrypted records;
- `assistant-joined.js` / `run-assistant-joined.js` — the joined assistant
  run (below).

Representative suites: `controlplane-nostr-smoke.spec.js`,
`relay-backed-web-functionality.spec.js`, `core-store-views.spec.js`,
`service-deployment-public-smoke.spec.js`,
`service-deployment-policy-gate.spec.js`,
`llm-route-release-deployment.spec.js`, `d69-d70-mutation-lifecycle.spec.js`,
`d72-dns-ml-intents.spec.js`, `backup-intent-lifecycle.spec.js`,
`final-web-intents.spec.js`, `notifications-encrypted-smoke.spec.js`,
`soul-provisioned-visibility.spec.js`, `souls-gallery-live.spec.js`,
`auth-guard-redirect.spec.js`. `controlplane-nostr-prod-smoke.spec.js` runs
under the production config.

Use `page.route()` only for the HTTP routes the app calls (Blossom blob
proxy, maintenance window). Never model a mutation as an HTTP POST.

### Joined assistant run

`tests/e2e/assistant-unified-execution.spec.js` drives the assistant panel
against a real daemon and is skipped by `pnpm test:e2e`. `pnpm test:e2e:joined`
needs Go, Docker (PostgreSQL) and a real `node_modules`: it builds
`cmd/server`, `cmd/bahia-assistant-e2e-provider` and the dashboard, starts
PostgreSQL, `bahia-test-relay`, the deterministic provider, the daemon and
the static dashboard on loopback, runs `playwright.joined.config.js` and
tears everything down. Logs go to `test-results/assistant-joined-harness/`;
`BAHIA_ASSISTANT_E2E_SKIP_DASHBOARD_BUILD=1` reuses an existing `build/`.

## Signer fixtures

Install signer mocks before navigation (`tests/e2e/e2e-keyring.js`,
`intent-helpers.js`). NIP-07 mocks provide `getPublicKey`, `signEvent` and
`nip44.encrypt/decrypt`; NIP-46 tests mock the provider/session surface.
When NIP-44 is absent, assert the explicit blocker rather than a plaintext
fallback. Use valid-length hex keys and event ids in protocol tests;
`fleet-ock-fixtures.js` and `cp-state-fixtures.js` provide encrypted records
and canonical state.

## Quality expectations

- Test public behaviour, not internals; use accessible Playwright selectors.
- No sleeps: wait on protocol or UI readiness.
- Clean subscriptions, timers, IndexedDB, `localStorage` and module state.
- Run `pnpm lint`, the focused tests and `pnpm build` before pushing.

## Related documents

- [Web app setup](web-app-setup.md)
- [Web components](web-components.md)
- [Web store-first design](architecture/web-store-first.md)
