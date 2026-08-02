# Bahia Web App Testing Guide

The web app is relay-first for shared state, signer-first for public mutations, and uses encrypted ContextVM request/result flows for sensitive domains. Tests must exercise those boundaries rather than replacing every flow with REST mocks.

## Commands

From `web/`:

```bash
pnpm test
pnpm test:unit
pnpm test:unit:watch
pnpm test:unit:coverage
pnpm test:unit:coverage:llm
pnpm test:unit:coverage:soulfactory

pnpm test:e2e
pnpm test:e2e:prod
pnpm test:e2e:headed
pnpm test:e2e:ui

pnpm lint
pnpm build
```

Focused files:

```bash
pnpm exec vitest run tests/unit/nostr-client-parsing.test.js
pnpm exec playwright test tests/e2e/service-deployment-public-smoke.spec.js
```

## Configuration

- `vitest.config.js`: jsdom, globals, `tests/setup/vitest.setup.js`, and `tests/unit/**/*.test.{js,ts}`.
- `playwright.config.js`: Chromium, `tests/e2e`, dev server on `127.0.0.1:4173`.
- CI forbids focused tests, retries twice, and uses one Playwright worker.

## Unit-test areas

| Behavior | Tests |
| --- | --- |
| HTTP client | `api-client*.test.js` |
| Relay pool/recovery | `nostr-client-parsing.test.js`, `nostr-pool.test.js`, `pool-read-model-metadata.test.js` |
| Discovery/bootstrap | `discovery-store.test.js`, `controlplane-store.test.js`, `connection-status.test.js` |
| Encrypted ContextVM | `encrypted-controlplane.test.js`, `encrypted-domain-stores.test.js`, `encrypted-route-stores.test.js` |
| Persistent docs/branches | `docs-nostr.test.js`, `test-utils-and-fixtures.test.js`, `repositories-nip34.test.js` |
| Collection cache | `collections-cache.test.js`, `nostr-pool.test.js` |
| LLM UI | `llm-page.test.js` |
| SoulFactory | `souls-page.test.js`, `souls-store.test.js` |
| Harness determinism | `relay-harness.test.js` |

Reset stateful modules with `vi.resetModules()` where needed and restore timers/mocks in cleanup.

## HTTP client tests

`BahiaClient` currently implements SBOM and Blossom helpers only. Instantiate it and mock global `fetch`.

```javascript
import { beforeEach, expect, it, vi } from 'vitest';
import { BahiaClient } from '$lib/api/client.js';

const client = new BahiaClient();

beforeEach(() => {
  global.fetch = vi.fn();
});

it('unwraps an SBOM response', async () => {
  global.fetch.mockResolvedValue({
    ok: true,
    headers: new Headers({ 'content-type': 'application/json' }),
    json: async () => ({ data: { artifact_id: 'artifact-1' } })
  });

  await expect(client.getSBOM('artifact-1')).resolves.toEqual({
    artifact_id: 'artifact-1'
  });
});
```

Do not use nonexistent `api.listServices()` or `api.createService()`. Relay collections and signed helpers own those domains.

Cover direct NIP-98 headers, envelope unwrapping, non-JSON success, errors, URL/query encoding, GET-only default retries, explicit retry overrides, and raw Blossom responses.

## Relay subscription recovery

`PoolBackedClient.subscribeWithRecovery()` and `subscribeWithRecoveryOnRelays()` are the shared seams. Use fake timers and deterministic random values to assert:

1. `CLOSED` or connection failure schedules re-REQ;
2. exponential delay is capped and jitter controlled;
3. replay uses `since = lastSeenCreatedAt - 1`;
4. event IDs deduplicate overlap replay;
5. NIP-42 auth completes before auth-triggered retry;
6. `onHealth` reports `lastEoseAt`, `resubscribeAttempts`, and `lastClosedReason`;
7. repeated failures eventually mark disconnected;
8. unsubscribe cancels children and timers.

Store integration tests verify health copying and that EOSE marks the stored-event boundary without closing the live subscription.

## EOSE ordering and persistent reads

Events and EOSE may arrive through separate queues. Tests must cover buffered events immediately before EOSE.

- Discovery drains queued callbacks before normalizing trusted discovery/relay-set events.
- Control-plane bootstrap waits for EOSE from connected relays, then remains live.
- Branch and relay-doc reads resolve initial results at EOSE (or degraded timeout) while recovery subscriptions remain active.

The pre-auth NIP-65/metadata query in `auth.svelte.js` is intentionally bounded and one-shot because bootstrap relays may be abandoned after discovery.

## Encrypted result ordering

The encrypted result subscription must exist before publish because fast gift-wrapped replies may be ephemeral.

`encrypted-controlplane.test.js` should assert:

1. connect transport;
2. install requester-scoped subscription;
3. register pending waiter;
4. publish encrypted request;
5. accept only correlated, decryptable results from the configured service identity.

Also cover progress acknowledgement, work timeout, abort, auth closure, duplicate IDs, and publish-failure cleanup.

The E2E relay harness resolves readiness from REQ/EOSE rather than arbitrary sleep. Keep harness behavior deterministic.

## Collection-cache tests

The IndexedDB cache uses a v2 per-collection schema. Verify:

- legacy localStorage snapshot removal;
- TTL expiry/deletion;
- persisted/skipped allowlists;
- per-collection caps;
- reactive-array hydration;
- scheduled persistence;
- non-fatal storage failure;
- `$state.snapshot()` converts proxies to structured-clone-safe plain objects before `putMany`.

Use `fake-indexeddb`; do not require a real browser database.

## LLM page tests

`llm-page.test.js` should assert:

- route `gateway_config.header_secret_refs.Authorization`;
- external `backend_preferences: ['external_api']`;
- optional `metadata.litellm_model`;
- `health_header_secret_refs.Authorization`;
- runtime-backed vLLM configuration;
- route/release/deploy/rollback stay on signed public helpers.

Never place secret values in payloads; the UI accepts secret references.

## SoulFactory UI tests

`souls-page.test.js` and `souls-store.test.js` should verify:

- compatible `30317` capability/method gating;
- signed `31952` then correlated `5950`;
- explicit `6950`/`7950` completion, never EOSE/timeout;
- `unresolvedDrafts()` excludes agent IDs already in provisioned `31951`;
- no duplicate unresolved-draft/active-soul rendering;
- update actions carry previous/new spec hashes and merge/replace parameters.

```bash
pnpm test:unit:coverage:llm
pnpm test:unit:coverage:soulfactory
```

## Playwright harnesses

Under `tests/e2e/harnesses/`:

- `service-deployment-public.js` — discovery/read models/signed service-deployment flows;
- `llm-controlplane-public.js` — LLM route/release/deploy;
- `notifications-encrypted.js` — encrypted notification request/result.

Important suites include `controlplane-nostr-smoke.spec.js`, `relay-backed-web-functionality.spec.js`, `service-deployment-public-smoke.spec.js`, `llm-route-release-deployment.spec.js`, `notifications-encrypted-smoke.spec.js`, `soul-provisioned-visibility.spec.js`, and `souls-gallery-live.spec.js`.

Assert trusted service pubkeys, canonical relay discovery, events-before-EOSE, continued live/recovered subscriptions, request/reply correlation, encrypted/public boundaries, and explicit terminal results.

Use `page.route()` only for compatibility endpoints actually called. Do not model signer-first service/environment/LLM/SoulFactory mutations as REST POSTs.

## NIP-07 and NIP-46 fixtures

Install signer mocks before navigation. NIP-07 may require `getPublicKey`, `signEvent`, and `nip44.encrypt/decrypt`. NIP-46 tests mock the provider/session surface. When NIP-44 is absent, assert the explicit blocker rather than plaintext fallback.

Use valid-length hex keys and event IDs in protocol tests.

## Quality expectations

- Test public behavior, not internals.
- Use accessible Playwright selectors.
- Avoid sleeps; use protocol/UI readiness.
- Clean subscriptions, timers, IndexedDB, localStorage, and module state.
- Assert degraded metadata for incomplete EOSE barriers.
- Run `pnpm lint`, focused tests, and `pnpm build`.

## Related documents

- [Web app setup](web-app-setup.md)
- [Web HTTP client](web-api-client.md)
- [Web components](web-components.md)
