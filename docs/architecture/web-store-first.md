# Web: store-first over relays

The web app is a Nostr client. It has **one relay pool and one IndexedDB event
store**; every view is a derived query over verified events, mutations are
locally signed intents, and the daemon's HTTP API is not a data path. Code:
`web/src/lib/nostr/` (`store.js`, `store-interface.ts`, `ingestion.js`,
`pool-welshman.js`, `boot.js`, `outbox.js`, `confidential.js`,
`intent-*.js`) and `web/src/lib/stores/`.

## Store

- Library: `@welshman/net` (pool, NIP-42 AUTH per socket, NIP-77), `@welshman/util`
  and `@welshman/signer` (NIP-46 `BunkerSigner`), behind the library-agnostic
  `BahiaEventStore` interface (`store-interface.ts`) so the implementation can
  be swapped. `nostr-tools` remains for `finalizeEvent`/`verifyEvent`, `nip44`
  and `nip19`.
- One IndexedDB database per deployment, `bahia-events-<service_pubkey[:8]>`,
  with object stores `events` (indexes `kind`, `pubkey`, `created_at`) and
  `cursors` (per-(relay, filter) `since`). Pending intents live in a sibling
  database `bahia-pending-<namespace>`.
- The service pubkey and relay URLs come from the validated runtime bootstrap
  seed (`/bahia-bootstrap.js`, injected at container start from
  `PUBLIC_BAHIA_*`).
- **No TTL.** Addressable and replaceable events are bounded by latest-wins and
  never evicted; regular events are LRU-pruned by size above 100 MB
  (`navigator.storage.estimate()`). NIP-09 deletions from the service pubkey
  tombstone `e` ids and `a` coordinates; NIP-40 expired events are swept on open
  and every 5 minutes. On first authenticated boot the app requests
  `navigator.storage.persist()` (best effort).
- **One ingestion path** (`ingestion.js`): verify the signature; apply NIP-01
  latest-wins per `(kind, pubkey, d)` with lowest-id tiebreak; apply NIP-09 and
  NIP-40; insert; notify derived stores. Relay events, cache hydration and
  locally signed intents all go through it. The rules are the same as the
  daemon's (`internal/nostrutil/lifecycle.go`) — see
  [event lifecycle](event-lifecycle.md).

## Boot

1. Read the deploy seed.
2. Open the event store for the service-pubkey namespace; sweep expired events.
3. **Render from the store immediately.** An empty store is an empty list, not a
   spinner; the status bar shows "syncing".
4. Check the persisted session (`localStorage`: pubkey, auth method, relays,
   `signerVerifiedAt`). A session verified within 24 h is authenticated at
   once; signer verification (NIP-07 probe or NIP-46 reconnect) runs in the
   background and clears the session on mismatch.
5. Connect the pool and open ref-counted subscriptions: read model
   (`kinds:[30900,...], authors:[servicePubkey], since: cursor`), worker
   adverts, activity/ops, intent status (`kinds:[30315], #p:[me]`), profile and
   relay list (`kinds:[0,10002], authors:[me]`). Discovery is optional metadata;
   its absence disables only the daemon-dependent features (assistant, secret
   reveal, log fetch).
6. On EOSE per relay per filter, persist the cursor and flip the domain's
   "synced" badge; once every filter has EOSE from at least one relay the
   indicator reads "live" (`sync-status.svelte.js`). EOSE is a badge, never a
   gate.
7. Events are applied per animation frame with dirty flags per collection.

There is no backend auth probe and no `backendAuthenticated` flag. Roles come
from decrypted membership records (see [confidential state](confidential-state.md#what-readers-do));
they gate mutation affordances, not rendering. An unauthenticated or
non-member user sees relay-public state.

## Derived views

Each collection is a derived query over the store, routed by the `t` topic tag
of `30900` events from the service pubkey (`service-registry`,
`environment-registry`, `environment-state`, `deployment-intent`,
`deployment-run`, `policy-registry`, `package-registry`, `llm-route-registry`,
`llm-route-state`, `ml-model`, `backup-recipe`, `dns-zone`,
`worker-assignment`, `sbom-reference`, ...). Topic constants are generated into
`kinds.gen.js` from `internal/kinds` and drift-tested
(`TestGeneratedFrontendCPStateTopicsMatchGo`).

## Writes: signed intents

Mutations are the intents described in [intents and authority](intents-and-authority.md):
a `30900` event signed by the user's NIP-07 or NIP-46 signer with a fresh
`intent_id` (UUIDv7), full desired state, and for updates the canonical
record's `expected_updated_at`. Org, secret and notification intents are
NIP-59 gift-wrapped (`intent-giftwrap.js`); the app checks that the signer
offers `nip44.encrypt` and disables sensitive mutations otherwise.

Lifecycle of a pending intent (`pending-intents.svelte.js`, `outbox.js`):

```
sign → insert as "pending" (UI overlay with age badge)
     → publish through the outbox, per-relay OK tracking
         accepted by ≥ 1 relay           → "published"
         auth-required:                  → wait for the pool's AUTH, resend once
         blocked:/restricted:/invalid:   → relay permanently failed for this event
         every relay permanent           → "failed" (the only failure path)
         no OK (socket dropped)          → resent on reconnect
     → subscribe 30315 with d = intent-status:<me>:<coordinate>
         accepted  → clear overlay
         conflict  → conflict UI: re-read and re-submit
         rejected  → show reason
         superseded→ clear overlay
```

The overlay is also cleared by a canonical `30900` on the same coordinate with
`created_at` ≥ the intent's. **There is no timeout-based retry or failure**: an
intent stays pending until a status or newer canonical record arrives — the
daemon may be offline or catching up, which is not a client failure.

## What remains on ContextVM

Three interactive patterns use ContextVM (kind `25910` inside NIP-59 gift
wrap) through `encrypted-controlplane.js`: **assistant turns**
(`assistant/prompt`, `assistant/approval`, `assistant/cancel`,
`assistant/reconcile`), **secret value reveal** (`services/secrets-reveal`) and
**deployment run log fetch** (`deployments/run-logs-get`). Every call carries
an idempotency key minted once per user action (`_meta.progressToken`,
`contextvm-idempotency.js`); on JSON-RPC `-32011` (request accepted, response
not replayable) the client retries with the **same** key so the daemon's
request ledger replays the stored response; `-32600` is fixed and retried with
a new key; `-32603` is retried with the same key, surfacing after two failures.

Payment history and security views read the OCK-encrypted `30900` records
(`payment-record`, `security-*` topics) from the store like every other
collection.

## Architecture gates

`web/tests/unit/architecture-gates.test.js` (run by `pnpm run test:unit` and
`make lint-arch`) fails on new `setInterval` polling or daemon REST client
imports in stores, on `SimplePool`/`PoolBackedClient` in library code, and on
hardcoded public relay hosts. See [ratchets](ratchets.md).

## End-to-end tests

Playwright suites (`web/tests/e2e`) drive the real boot sequence against
in-page WebSocket mock relays (`*.test.local`) that serve fixture events
signed with the keys in `e2e-keyring.js` and `cp-state-fixtures.js`; the app
verifies those signatures like any other event, so there is no trust bypass.
`playwright.joined.config.js` runs `assistant-unified-execution.spec.js`
against a joined daemon harness (`tests/e2e/harnesses/assistant-joined.js`).
