# Investigation: Bahia Nostr-First Architecture Audit (full stack)

## Summary
Bahia is a **Postgres-centred CRUD service that uses relays as a replication target and ContextVM 25910 as an RPC transport**. It is not a Nostr-first system with some leftover REST. The audit found **107 findings** across three areas: web app, 33 (A-1…A-33); daemon data authority, 30 (B-1…B-30); protocol primitives and peripherals, 44 (C-1…C-44). They reduce to **six root-cause design decisions**:
1. entity identity is minted by Postgres;
2. relays are an output, never an input;
3. no process has a local event store;
4. ContextVM is the default read and write path;
5. the relay is configured to be lossy and short-lived;
6. authorization lives in the DB rather than in events.

The symptoms the user reported map directly onto these. The web IndexedDB cache stores derived snapshots and is wiped by the first relay event (A-3, A-4). 24 route prefixes are gated on a REST `/orgs` probe (A-1). The projector republishes "authoritative DB state" every 10 minutes (B-1), and the bootstrapper rebuilds nothing from relays (B-8). NIP-77 is available but switched off (C-17), and the relay drops fan-out under load (C-18). The report also lists **eight compounding cross-tier failures** and seven confirmed correctness bugs that can be fixed independently. It gives a dependency-ordered migration: invariants → relay → Go client → daemon slices → web → CLI, where every step ends in deletions.

**Tracking:** Beads epic `bahia-irsry`. Independent confirmed bugs: `.1` B-18, `.2` B-19, `.3` B-10, `.4` B-9/C-9, `.5` A-4, `.6` C-18, `.7` C-6/C-35. Phases 0–5 are `.8`→`.13`, each blocked by the previous one. Coverage-gap audit is `.14`.

## Symptoms
- **Web: no persistent local event cache.** Every reload re-fetches everything from relays; there is no localStorage/IndexedDB store "topped up" by a live subscription as the original design required. Page loads are slow.
- **Web: gated on backend.** The web app waits for some connection/handshake with the backend before rendering properly, i.e. it cannot function when the backend daemon is offline even though all state should be on relays.
- **Web: REST-SPA shape with Nostr bolted on.** Pub/sub is used like stateless request/response (one-shot fetch, close, re-fetch) instead of long-lived subscriptions.
- **Backend: Postgres as source of truth.** The daemon treats the DB as authoritative and repeatedly "re-projects" DB rows as events published to relays (every time), instead of events being the source of truth.
- **Backend cannot run without the DB.** The stated goal is that the daemon functions with relays only; the DB should be optional (a cache/index at most).
- **No sync primitives.** No negentropy (NIP-77), no since-cursor persistence, no EOSE-aware incremental sync; every consumer does full re-fetch.
- **Regression tendency.** Anti-patterns have been purged repeatedly but creep back.

## Target Model (the "simple Nostr paradigm")
- Relays are the only required infrastructure. Events are the source of truth.
- Web: local event store (IndexedDB) renders instantly on load; live REQ subscriptions (with `since` = last-seen cursor, or NIP-77 negentropy reconciliation) top it up; writes are signed locally and published with OK verification; backend absence only means "no daemon-produced events arrive", never "app doesn't load".
- Daemon: subscribes to its inputs, reacts, publishes outputs once (idempotent, addressable/replaceable where appropriate). Any DB is a derived, rebuildable cache/index — never authoritative and never a trigger for republishing.

## Background / Prior Research

### B1. Git archaeology — purge/regression cycles (explore agent, verified hashes in `git log`)
- **Prior audits (2026-06-01):** `docs/investigations/fake-nostr-routes-audit-2026-06-01.md` found "semantic drift": real Nostr transport, but many surfaces wrapped as request/result RPC facades (orgs/payments/notifications = encrypted `5980/7980` request/result over CRUD repos). `docs/investigations/rest-api-audit-2026-06-01.md` catalogued ~100+ REST endpoints and ~50+ outbound HTTP calls, classifying REST into Essential / Transitional / Gap.
- **Remove → re-add → revert cycle:** `a0468d48` (05-23, remove REST DNS catalog) → `9cbf5a66`/`ba3795d7`/`c9cf8ab7` (06-01, remove REST mutations) → `4f2ac9e`/`b684e9d` (06-07, *re-add* REST write routes "returning nostr receipts") → `3191c9b8` (06-08, revert) → `1a0bde4a` (06-15, "Revert REST SBOM approach, add Nostr-native **projector republishing**") → `aab49c25`/`62034b79` (06-10/13, remove/restore docs endpoints).
- **HEAD still mounts ~39 REST write routes** in `internal/api/router/router.go` including full org CRUD at ~`router.go:429-436` (wired `4b30a989`, 2026-04-30, never folded into the Nostr path); secrets, notifications, tools/denylist, config-fabric, builds still have direct REST writes.
- **"Reconstructible Bahia" rewrite, 2026-05-23 (one afternoon, waves 1–7, `a4ff7549`→`34a0159c`):** kind catalog + mode policy → cache appliers → `ab00326f` "DB-optional startup" → `61627b8c` "bootstrapper and write-path inversion" → `fabe22dd` "route gating and projector authority flip". Charter: `docs/plans/reconstructible-bahia-2026-05-23.md:5` ("relays hold canonical state, Postgres becomes a disposable cache"); item 16 (line ~265) flagged core write-path inversion as "the biggest architectural blocker".
- **Projector churn never stopped:** ~60 commits touch `projector`; this week `c2300b7f`/`e9123939`/`1537dee3` "fail closed on projector hydration errors" / "never hold tombstones behind projector hydration failure".
- **Web IndexedDB cache exists:** `bc069a00` (07-01) "Use IndexedDB controlplane collection cache" → `web/src/lib/stores/collections/indexeddb-cache.js`; hardened `eca2a586`, `6c925828`. It caches *read-model collections* with TTLs/caps and skips high-churn collections (`docs/WEB_APP_PRODUCTION_PLAN.md:31`) — i.e. a REST-style response cache, not an event store. Whether it is actually on the render path is a question for the investigation.
- **Negentropy never implemented** in Bahia logic; only vendored `third_party/nostr/nip77` (removed `c70042c0`, 07-07). `fiatjaf.com/nostr/nip77` is available through the existing dependency.
- **Docs contradict the charter:** `docs/architecture.md:215-224` "Source of truth" table still says desired state / runtime observations / workflow history = **PostgreSQL** (written `b1331ab7`, 05-05; survived the 08-01 docs pass `a65383c3`). `docs/designs/nostr-event-store-lifecycle.md` treats Postgres `nostr_events` (≈19 GB, ≈12M rows) as a durable outbound publish outbox needing partitioning.

### B2. External ecosystem (explore agent, web research)
- **NIP-77 negentropy** (`NEG-OPEN/NEG-MSG/NEG-CLOSE/NEG-ERR`): range-based set reconciliation over a filter. Supported by strfry and khatru (Bahia's relay sidecar is khatru-based). Go: `fiatjaf.com/nostr/nip77` `NegentropySync(ctx, relayURL, filter, source, target)` — already reachable via Bahia's `fiatjaf.com/nostr` dependency. JS: applesauce-relay (negentropy sync stores), `@nostr-dev-kit/sync`; nostr-tools has no native support.
- **Browser event stores:** `nostr-idb` (IndexedDB, batched writes, replaceable lookup, LRU pruning, async-generator subscribe); applesauce-core `EventStore` (RxJS reactive, pairs with nostr-idb, negentropy-ready); welshman (`@welshman/store`/`net`/repository — Svelte-native, from Coracle); `@snort/worker-relay` (SQLite-wasm + OPFS in a worker, a full in-browser relay); NDK dexie cache.
- **Go daemon patterns:** `fiatjaf.com/nostr/eventstore` (LMDB/bbolt/SQLite/badger) as local cache/index; state modeled as addressable events; startup = query local store, then `nip77` sync against relays; no bespoke schema.
- **Standard cache hygiene:** per-filter since-cursor persisted durably (REQ `since: cursor` on reconnect, periodic negentropy to fill gaps); NIP-09 deletions tombstone ids and all versions of `a`-addresses up to deletion `created_at`; NIP-40 expiry swept locally; addressable events keyed `(kind,pubkey,d)` latest-wins (tie-break lowest id); NIP-65 outbox relay selection.

## Investigator Findings: A — Web app (boot, cache, subscriptions, backend coupling)

_Investigator A, 2026-09-30. Scope: `web/src` at `1537dee3`. Read-only. All paths are relative to `web/src/` unless noted. Every load-bearing claim was spot-checked by direct read; explore-probe output was used only as a cross-check._

#### Hypothesis verdicts

| Hyp. | Verdict | One-line proof |
|---|---|---|
| **H1** boot gated on backend | **CONFIRMED (auth layer), PARTLY DISPROVEN (read-model layer)** | The relay read-model bootstrap only needs the static deploy seed (`routes/+layout.svelte:48-58` → `stores/controlplane/bootstrap.svelte.js:136-157`, discovery explicitly non-gating at `:159-163`). But **24 of the app's route prefixes** sit behind `AuthGuard`, which needs a successful REST `GET /api/v1/orgs` plus a daemon discovery flag (A-1, A-2). With the daemon offline, only `/`, `/docs`, `/widgets` and `/route-canaries` render at all. |
| **H2** no event-level persistent cache | **CONFIRMED** | `collections/indexeddb-cache.js` persists derived "collections" (`{name,cachedAt,items}`), not events. The UI hides it behind loading flags, and the first relay event wipes it (A-3, A-4). |
| **H3** subscriptions used as one-shot REST | **CONFIRMED (mixed)** | The core read-model subscription is long-lived, but it has no persisted cursor and re-downloads its whole window on every load (A-5). Many helpers resolve on EOSE and either close or leak (A-15, A-16), and timeouts are used as completion (A-17). |
| **H4** request/response RPC for reads & mutations | **CONFIRMED** | Orgs, payments, notifications, security, run logs, relay settings and signatures are all read by ContextVM RPC to the daemon. About 40 mutations block on a daemon RPC result, and relay events are used only as "refetch" triggers (A-8 to A-11). |
| **H5** REST usage | **CONFIRMED** | 25 REST methods on `/api/v1` with 11 live call sites, one of which is the gate on every protected route (A-1, A-30). |
| **H6** store/data-model issues | **CONFIRMED** | No NIP-09/NIP-40 handling. The same kinds are downloaded twice by different stores. Rebuilds are O(n²) during catch-up. Domain stores are torn down on navigation, errors blank the UI, author filters are open, and memory is unbounded (A-7, A-22 to A-28). |

#### Findings

**A-1. Every protected route is gated on a REST membership probe to the daemon (`GET /api/v1/orgs`) and a daemon discovery feature flag.** Severity: **critical**.
- Evidence: `auth/route-access.js:1-26` lists 24 `PROTECTED_PREFIXES`: `/services`, `/deployments`, `/environments`, `/workers`, `/artifacts`, `/souls`, `/dns`, `/backup`, `/ml`, `/llm`, `/settings`, … `canAccessRoute` requires `authState.backendAuthenticated` (`:121-124`). `stores/auth.svelte.js:117-124` has `compatibilityPatch() → backendAuthenticated: restNip98Ready`. The only thing that sets it true is `configureBackendAuth` (`:444-490`): it awaits `currentSystemInfo() || await loadSystemInfo()`, requires `systemInfo.features.direct_nostr_http_auth` (`auth/capabilities.js:1-3`), then calls `api.fetch('/orgs', { method:'GET', retries:0 })` (`:457`). Roles also come only from that REST response (`:458-461`). `components/AuthGuard.svelte:30` has `isAuthorized = isAuthenticated() && authState.backendAuthenticated`, and `:44-48` runs `goto('/')` when it is false.
- Failure modes: (a) daemon HTTP down → `/orgs` throws → `restNip98Ready:false` → every protected route redirects to `/`. (b) daemon never published discovery, or discovery was not reached in time → `supportsDirectNip98Auth(null)` is false → the same redirect happens (`:478-489`), even when HTTP is up. A valid NIP-07 identity and relay-resident data are not enough to view public relay state such as the services list.
- Why it's an anti-pattern: authorization for *viewing* relay-public data is delegated to a centralised HTTP session check. It turns the backend into a hard runtime dependency of the whole UI. Membership should itself be relay state.
- Fix: (1) Render all relay-sourced views without a backend check. (2) Model org membership and roles as signed addressable events: org-owner/daemon-signed member lists (NIP-51-style `kind:3xxxx` with `p` tags plus role markers, or NIP-29-like group state). Subscribe to them and derive roles locally. (3) Use roles only to hide or disable *mutation* affordances. The daemon still authorises actions when it processes intents. (4) Delete the `/orgs` probe, `requiresRestCompatibility`, and `compatibility.*` state.

**A-2. `AuthGuard` blocks protected content until the full serial auth chain settles (signer wait + NIP-46 reconnect + discovery wait + REST probe + two relay queries).** Severity: **high**.
- Evidence: `components/AuthGuard.svelte:10-26` awaits `initializeAuth()` and keeps `isLoading` until it resolves. `stores/auth.svelte.js:569-658` runs `waitForNip07({timeoutMs:1500})`, then the NIP-46 `connectNip46` reconnect, then `await configureBackendAuth` (which can wait up to `DISCOVERY_DEADLINE_MS = 10_000`, `stores/discovery.svelte.js:14,294`, plus the REST call), then `await hydrateAuthMetadata` (`:416-436`). That last step runs `fetchRelayList`, then `fetchProfile` *sequentially*. Each one builds a fresh `PoolBackedClient` and races a 5 s timeout (`:31,319-367`). The worst case is about 21.5 s of "Checking authentication…" before any protected page mounts.
- Why it's an anti-pattern: identity metadata (kind 0 / 10002) is decoration and should never gate rendering. A local-first client renders from the persisted session immediately and lets live subscriptions fill in the profile.
- Fix: treat a persisted, signer-verified session as authenticated at once. Move profile and relay-list loading to a background live subscription (`kinds:[0,10002], authors:[me]`) on the shared pool and store the results in the local event store.

**A-3. The IndexedDB "cache" stores derived read-model snapshots, not events. It has a 15-min TTL and per-collection caps, skips 16 collections, and is not scoped to a deployment or user.** Severity: **high**.
- Evidence: `stores/collections/indexeddb-cache.js:1-3,51,105` has DB `bahia-controlplane-cache` with object store `collections` keyed by `name`, storing `{name, cachedAt, items}`, where `items` are projected domain objects. `stores/collections/index.svelte.js:106` has `CONTROLPLANE_CACHE_TTL_MS = 15*60*1000`. `:107-118` sets caps (default 250; `states` 150). `:120-142` persists 21 collections and `:144-161` skips 16, including `events`, `builds`, `deploymentRuns`, `operations`, and all backup run/verification/restore state. `:356-380` deletes expired records on read. Nothing keys the DB by service pubkey, relay set, or user. `logout()` (`stores/auth.svelte.js:790-815`) never clears it. `:346-354` removes the older localStorage snapshot (`bahia_controlplane_snapshot_v1`).
- Why it's an anti-pattern: a projection cache cannot be topped up incrementally. There are no event ids to dedup against, no `created_at` to derive a `since` cursor from, and no deletion semantics. The TTL means any tab reopened after 15 minutes starts cold, and capping by recency drops long-lived but still-current entities.
- Fix: replace it with a raw-event store (`nostr-idb`, applesauce `EventStore` + `nostr-idb`, or welshman repository). Store verified events keyed by id, with replaceable/addressable indexes `(kind,pubkey,d)` and a per-filter cursor table. Namespace the DB by `service_pubkey`, do no TTL eviction (use LRU by size instead), and derive every collection from the store.

**A-4. The persisted cache is almost never rendered: loading flags hide it, and the first relay event overwrites it.** Severity: **critical** (this is the direct cause of "every reload re-fetches everything").
- Evidence:
  1. `stores/controlplane/bootstrap.svelte.js:129-130` calls `setAllLoading(true)` and *then* `await hydrateCachedCollections()`. `loading.*` stays `true` until `finally` (`:169-171`), and that only happens after `await startStreamingSubscription(..., { waitForEose:true })` (`:156`), i.e. after EOSE from **every** connected relay.
  2. Pages render a spinner while those flags are set: `routes/services/+page.svelte:130` (`{#if loading.services} Loading...`), `routes/environments/+page.svelte:224`, `routes/workers/+page.svelte:686`, `routes/payments/+page.svelte:255`. Page-local `loading` flags await `loadX()`, which is `bootstrapControlplane()` (`stores/index.svelte.js:89-101`), in `routes/packages/+page.svelte:21-32`, `routes/policies/+page.svelte:43-48`, `routes/backup/+page.svelte:32-39`, `routes/ml/+page.svelte:71-83`, `routes/llm/+page.svelte:93-99`, and others.
  3. `hydrateCachedCollections` writes only into the rendered `$state` arrays (`index.svelte.js:382` `replaceSnapshotArray(target, …)`). It does not write into the backing Maps (`collections/services.svelte.js:4 serviceMap`, and equivalents in each collection module). The first accepted event of *any* kind calls `refreshCollections()` (`controlplane/events.svelte.js:253-256`), which rebuilds every array from its (still empty) Map (`services.svelte.js:6-8`). **All hydrated collections are wiped the moment the first event arrives, then refill one event at a time.**
  4. The only path where cached data stays visible is bootstrap *failure*: `finally → setAllLoading(false)` with no events applied. In other words, the cache shows only when relays are unreachable.
- Why it's an anti-pattern: the whole point of a local store is "render instantly, then reconcile". Here the render is gated on network EOSE and the local state is discarded instead of merged.
- Fix: hydrate the event store (A-3), feed hydrated events through the same `applyControlplaneEvent` path so the Maps are populated, and render from the store immediately. Treat EOSE as a "synced" badge, not a render gate.

**A-5. There is no persisted since-cursor or negentropy. Every load and every page mount re-requests the full window.** Severity: **high**.
- Evidence: `stores/controlplane/events.svelte.js:69-103` (`readModelFilters()`) rebuilds its filters from `Date.now()` on every start: `limit:1000` for the read-model kinds and worker ads, and `since: now − 7d` for jobs, operations, backup attestations, external ops and activity. `nostr/pool-subscriptions.js:151-158,225-227,112-119` keeps a `lastSeenCreatedAt` cursor in memory, per relay, per subscription instance. It is used only when that relay reconnects inside the same page session and is lost on reload. `stores/dns.svelte.js:744` accepts `since`, but its only caller passes nothing (`:832`), so every `/dns` visit re-requests up to `DNS_READ_MODEL_LIMIT = 5000` events (`:39,329`). There is no NIP-77 code anywhere in `web/src`.
- Fix: persist `{filterHash, relay} → max(created_at)` in the event store and REQ with `since: cursor − skew`. Run NIP-77 negentropy against khatru for the replaceable/addressable read-model set, since `since` alone misses back-dated replacements. Use `until` pagination when a response hits `limit`.

**A-6. All controlplane state is multiplexed into one filter capped at `limit:1000`, so entities are silently truncated.** Severity: **high**.
- Evidence: `stores/controlplane/events.svelte.js:50,72` issues `{ kinds: BAHIA_READ_MODEL_KINDS, limit: 1000, authors:[service] }`. `nostr/kinds.gen.js:447-457` shows `BAHIA_READ_MODEL_KINDS` is the single addressable `CASCADIA_CONTROLPLANE_STATE` kind (every domain: services, environments, states, artifacts, intents, policies, packages, backups, ML, DNS, workers…) plus the ContextVM announcements, relay sets, 10050 and `NIP78_APP_DATA`. Relays return the newest 1000 by `created_at`, so rarely-updated but current coordinates (for example a service registered months ago and never re-published) are simply never delivered. The cache caps (A-3) compound this.
- Fix: partition by domain using single-letter indexed tags or distinct kinds, page with `until` until a page returns fewer than `limit`, and (preferably) reconcile the full addressable set with negentropy.

**A-7. Catch-up cost is O(n²): every accepted event rebuilds and re-sorts all ~37 collections and schedules a full IndexedDB rewrite.** Severity: **high** (a direct contributor to "page loads are slow").
- Evidence: `stores/controlplane/events.svelte.js:253-257` calls `refreshCollections()` and `schedulePersistCachedCollections()` on every changed event. `stores/collections/index.svelte.js:198-208` rebuilds all nine domain groups, and each group does `replaceArray(target, Array.from(map.values()).sort(...))` (for example `collections/services.svelte.js:6-8`). A 1000+ event bootstrap therefore performs 1000+ full rebuilds, each triggering Svelte reactivity across every page-bound array. The persist step (`index.svelte.js:393-417`) snapshots and caps all 21 collections, and `indexeddb-cache.js:77,97,120` opens and closes the database on *every* operation.
- Fix: batch event application per animation frame or microtask with per-collection dirty flags. Better, derive views lazily from an indexed event store. Persist raw events incrementally in batches rather than rewriting snapshots.

**A-8. Reads that should be relay subscriptions are implemented as ContextVM request/response RPC to the daemon.** Severity: **high** (H4).
- Evidence (each call requires the daemon online *and* `systemInfo.features.encrypted_nostr_requests` from discovery, `nostr/encrypted-controlplane-utils.js:166-183`):
  - Orgs: `stores/orgs.svelte.js:50-57,89-104,146-168` (`orgs.list`, `orgs.my_invites`, `orgs.detail`).
  - Payments: `stores/payments.svelte.js:61-65` (`payments.history`). The dashboard fans this out once per worker (A-32).
  - Notifications: `stores/notifications.svelte.js:76,91,131` (`listChannels`, `getChannel`, `listLogs`).
  - Security: `stores/security.svelte.js:110,138` (`findingsList`, `schedulesList`).
  - Other reads: `stores/deployment-run-logs.svelte.js:46` (run logs), `stores/artifact-signatures.svelte.js:42` (signature verify), `nostr/relay-settings-controlplane.js:270` (relay settings GET), and `nostr/dns-controlplane.js:129`.
  - With the daemon offline or no discovery, every one of these pages shows an error. There is no cached view.
- Why it's an anti-pattern: this is RPC semantics tunnelled over relays. There is nothing to subscribe to, nothing to cache, and no offline story. The daemon becomes a query server.
- Fix: have the daemon publish these read models as events: addressable per entity, NIP-44 encrypted to member pubkeys or NIP-59 gift-wrapped where private. The client subscribes, stores and renders them. RPC should remain only for genuinely interactive calls (assistant prompts).

**A-9. Relay events are used only as cache-invalidation pings that trigger a full RPC re-list (`subscribeToDomainRefresh`).** Severity: **high**.
- Evidence: in `nostr/retained-domain-subscription.js:118-150`, every *live* event matching `{kinds:[controlplane-state, audit, NIP-38, NIP-78], authors:[service], '#domain':[d]}` calls `requestRefresh()`, which re-runs the whole list RPC. Callers:
  - `stores/orgs.svelte.js:126-131`: `refreshOrgsState`, which is two or three RPCs.
  - `stores/payments.svelte.js:106-110`.
  - `stores/notifications.svelte.js:169-173`: channels RPC plus logs RPC.
  - `stores/security.svelte.js:331-352`, via `createCoalescedRefresh`.
- Why it's an anti-pattern: the event already *is* the state change, yet the client throws it away and asks the server again. This doubles load, adds daemon latency, and fails whenever the daemon is offline even though the event arrived.
- Fix: apply the event to the local store directly (A-8). Delete `subscribeToDomainRefresh` and `createCoalescedRefresh`.

**A-10. Every mutation is a blocking RPC: the UI waits up to 30 s for a daemon result, and nothing is applied optimistically.** Severity: **high** (H4 mutations).
- Evidence: `stores/public-controlplane.svelte.js:59-78` has `publishCommand`, which first awaits `bootstrapControlplane()` (`:63-66`, a full relay EOSE sync before any write can start). It then awaits `requestEncryptedResult(...)` (default `timeoutMs = 30000`, `nostr/encrypted-controlplane.js:271-277`). It backs about 40 operations (`service/create|update|delete|deploy|rollback`, `environment/*`, `approval/*`, `policy/*`, `package/*`, `backup/*`, `llm/*`, `sbom/*`, `artifact/register`; `:104-720`). The same pattern appears in `stores/security.svelte.js:165,188`, `stores/service-secrets.svelte.js:134`, `stores/assistant.svelte.js:784-903`, and `nostr/dns-controlplane.js:127-150`. The only optimistic write is `routes/services/CreateServiceDialog.svelte:156` `upsertServiceProjection(createdService)`, which patches the Map from the *RPC response* rather than from a signed event. Positive counter-example: `nostr/profile.js:160-192` signs kind 0 locally, verifies relay `OK accepted=true`, then updates local state. That is the right pattern.
- Fix: model every mutation as a user-signed, durable *intent* event, addressable with `d = idempotency key`. Publish it with OK verification, insert it into the local store immediately so it renders as "pending", and let the daemon's status/result events move it to done. The UI observes those canonical events and never awaits a request/response.

**A-11. "Encrypted" controlplane commands are sent as plaintext ephemeral kind 25910, so they are lost if the daemon is not listening at that instant.** Severity: **high**.
- Evidence: `nostr/encrypted-controlplane-constants.js:5` sets `CONTEXTVM_MESSAGE_KIND = 25910`, which is in the ephemeral 20000–29999 range and not stored by relays. `stores/public-controlplane.svelte.js:67-76` passes `kind: CONTEXTVM_MESSAGE_KIND, resultKinds:[CONTEXTVM_MESSAGE_KIND]`. `nostr/encrypted-controlplane-transport.js:74` has `if (kind !== CONTEXTVM_GIFT_WRAP_KIND) return innerEvent;`, so the signed inner event is published *unwrapped* and service configs and deploy parameters travel in cleartext. A relay `OK true` only means the relay accepted it for fan-out. If the daemon is offline or reconnecting, the command vanishes, and the UI times out after 30 s. The result is also ephemeral, so a brief browser subscription gap loses it as well.
- Fix: durable, stored intents (addressable, or NIP-59 1059 gift wraps where confidential; see A-10) that the daemon picks up on reconnect via its own since-cursor. Results should be durable status events.

**A-12. The ContextVM result subscription has no `since` and does not recover.** Severity: **medium**.
- Evidence: `nostr/encrypted-controlplane.js:91-97` subscribes to `{kinds:[1059,21059],'#p':[me]}` and `{kinds:[25910],'#p':[me]}` with no `since` or `limit`, so each (re)creation downloads the user's entire stored gift-wrap history. It uses `transport.client.subscribe`, the non-recovering primitive, and only handles AUTH closures (`:125-133`). A dropped socket silently strands every pending request until its 30 s timeout. The class path `EncryptedControlplaneTransport.requestEncryptedResult` disconnects the pool after every request (`encrypted-controlplane-transport.js:145`).
- Fix: `since: now − small skew` for the live wait, recovering subscriptions, and the durable model from A-10/A-11.

**A-13. Six or more independent relay pools open separate sockets to the same relays, with no shared subscription multiplexing or dedup.** Severity: **medium**.
- Evidence:
  - the singleton `nostr` (`nostr/subscriptions.js:133`)
  - discovery `bootstrapClient` (`stores/discovery.svelte.js:249`)
  - `authMetadataClient`, recreated for *each* auth query (`stores/auth.svelte.js:322-327`)
  - `EncryptedControlplaneTransport`'s own client (`nostr/encrypted-controlplane-transport.js:32`)
  - the profile publish client (`nostr/profile.js:168`)
  - the ops widget wall client (`widgets/ops-widget-wall.js:27`)
- Fix: one pool and one event store. All views should be queries against the store, with the pool responsible only for REQ lifecycle and dedup.

**A-14. The shared singleton pool is reconnected with `force:true` and different relay sets by several stores, which can tear down each other's subscriptions.** Severity: **medium**.
- Evidence: `stores/controlplane/bootstrap.svelte.js:146-147`, `stores/dns.svelte.js:823-824` (DNS relays from page data or discovery), `stores/fips-mesh.svelte.js:437-438` and `nostr/connection-guard.js:59-60` all call `nostr.setRelays(...)` and/or `nostr.connect(relays, {force:true})`. `nostr/pool-client.js:125-132` closes any relay not in the new set (`this.pool?.close?.(removedRelays)`) and resets status to `connecting`. Visiting `/dns` with a different relay list can close the global controlplane subscription's sockets. Separately, the localStorage "emergency relay override" (`nostr/subscriptions.js:62-114`) is dead: bootstrap always overwrites relays with the deploy seed (`bootstrap.svelte.js:146`).
- Fix: keep the relay set in one place (the seed plus NIP-65), make it additive per REQ (`subscribeOnRelays`), and never force-reconnect the shared pool from a feature store.

**A-15. One-shot "fetch" helpers resolve on EOSE but never close their subscription, so each call leaks a REQ, and some leak permanently re-opening ones.** Severity: **medium**.
- Evidence:
  - `nostr/repositories.js:94-128`: the `nostr.subscribe(...)` return value is discarded. It resolves on the *first* relay's EOSE and leaks one REQ per call.
  - `docs/nostr.js:116`: the `nostr.subscribeWithRecovery(...)` return value is discarded. Each call leaves a self-healing REQ open for the life of the tab.
  - `nostr/branches.js:195-200`: same as `docs/nostr.js`.
  - `routes/artifacts/[id]/+page.svelte:237-242` (`refreshSBOMReferenceEvents`): resolves on the first EOSE and never unsubscribes. It is called repeatedly (`:213,442`).
- Why it's an anti-pattern: this is the REST-over-REQ shape: a new REQ per call, completion inferred from the first EOSE (which can be partial), and no ownership of the subscription lifecycle.
- Fix: views query the local store. A single long-lived subscription per interest (keyed by a filter hash and ref-counted) keeps the store topped up.

**A-16. Subscriptions that open, wait for EOSE and close are re-run on every navigation or action.** Severity: **medium**.
- Evidence: `stores/souls.svelte.js:1195-1235` (`fetchSoulHistory`) settles, then calls `unsubscribe()`. `routes/souls/[id]/+page.svelte` re-invokes it on mount and after every action (`:194,246,292,331`). `stores/auth.svelte.js:319-367` runs bounded one-shot relay queries for kind 0/10002 (the comment at `:336-337` says a persistent subscription is "incorrect", although these are the user's own replaceable events). The discovery bootstrap is also EOSE-settled (`stores/discovery.svelte.js:262-325`). That one stays open afterwards, but when a localStorage cache hit returns early (`:236-243`) no live subscription is opened, so a daemon re-announcement is not seen for up to 15 min.
- Fix: long-lived, store-backed subscriptions keyed by interest. History becomes a store query.

**A-17. Timeouts are used as completion signals, and partial results are treated as authoritative.** Severity: **medium**.
- Evidence:
  - `stores/auth.svelte.js:31,341-343`: after 5 s, `resolve(events)`. The partial result is then *persisted* into the session (`:426-433`).
  - `stores/discovery.svelte.js:14,294`: a 10 s deadline.
  - `docs/nostr.js:107`: timeout returns "degraded docs history", which is also cached.
  - `nostr/branches.js:160`: timeout returns a degraded result.
  - `nostr/controlplane-requests.js:170-174`: `timeoutMs` reject.
  - `nostr/encrypted-controlplane.js:186-190,266,275`: a 30 s work timeout.
  - `stores/souls.svelte.js:699`: reconciliation timer.
- Fix: with a local store, "done" is not a concept. Render what is present, show per-relay EOSE and sync status, and keep subscriptions open.

**A-18. Long-lived views use the non-recovering subscribe primitive, so they go stale silently after any relay drop.** Severity: **medium**.
- Evidence: `nostr/pool-subscriptions.js:67-77` makes `subscribeOnRelays` emit a terminal `onClosed` with no retry, which is documented in the probe and confirmed by reading. Users:
  - `stores/souls.svelte.js:539` (soul factory list)
  - `stores/fleet-config.svelte.js:144`
  - `stores/fleet-rollout.svelte.js:204`
  - `stores/souls.svelte.js:709` (provisioning progress)
  - `nostr/subscriptions.js:135-147` (`subscribeToProvisioningProgress`)
  - `nostr/controlplane-requests.js:115,176` (`subscribeStatus` / `awaitResult`)
  - `nostr/encrypted-controlplane.js:97`
  
  Only the controlplane, DNS, fips-mesh, assistant, docs, branches and retained-domain paths use `subscribeWithRecovery`.
- Fix: make recovery the default with no opt-in primitive for views, and resume from the persisted cursor (A-5).

**A-19. Bootstrap needs EOSE from *every* connected relay, has no deadline, and a failure is sticky with no automatic recovery.** Severity: **high**.
- Evidence: `stores/controlplane/bootstrap.svelte.js:72-80` treats the bootstrap as complete only when `pendingEoseRelays.size === 0`. The recovery wrapper never emits terminal closes (`pool-subscriptions.js:189-196` sets `terminal:false`), so a relay that accepts the REQ but never EOSEs, or keeps cycling, leaves the `await` at `:156` pending forever and every `loading.*` flag stuck at `true` (A-4). On failure, `stores/controlplane/connection.svelte.js:80-85` sets `ready=false`. The reconnect hook (`bootstrap.svelte.js:36-39`) only acts when `ready` is true, so there is no auto-retry. `:3,30-33` adds a 30 s lockout during which every page `loadX()` gets `{ok:false}` (`bootstrap.svelte.js:122-124`). Recovery requires the manual "Retry" button (`components/ConnectionStatus.svelte:31,46-57`). `manualRetry` → `loadSystemInfo({force:true})`, and on failure that sets `systemInfo.data = null` (`stores/system.svelte.js:63-66`), so a retry during a relay blip also drops discovery and every encrypted feature.
- Fix: render from the store regardless of EOSE, treat per-relay EOSE as status only, and retry relays independently with backoff. Never null out known-good state on a failed refresh.

**A-20. Protected and public views are keyed to global EOSE-gated loading flags instead of rendering what is present.** Severity: **medium**. (This is the UI half of A-4; listed separately because it is spread across many route files.)
- Evidence: `{#if loading…}` gates in `routes/services/+page.svelte:130`, `environments/+page.svelte:224`, `workers/+page.svelte:686`, `payments/+page.svelte:255`, `fleet-health/+page.svelte:82` (this one is correctly `&& weatherNodes.length === 0`, a good pattern), `packages/+page.svelte:67`, `policies/+page.svelte:218`, `backup/+page.svelte:64`, `ml/+page.svelte:244`, `llm/+page.svelte:251`, `deployments/+page.svelte:377`, `environment-states/+page.svelte:177`, and the detail pages `services/[id]:1106`, `environments/[id]:429`, `packages/[id]:206`, `policies/[id]:292`, `workers/[pubkey]:632`, `artifacts/[id]:593`, `deployments/[id]:179`. The per-route loaders are aliases for `bootstrapControlplane()` (`stores/index.svelte.js:89-101`); `ml` and `llm` call it directly (`ml/+page.svelte:77`, `llm/+page.svelte:99`). This is a REST "load on mount" facade over what is already a global live subscription.
- Fix: apply the `fleet-health` pattern everywhere: show a spinner only when there is no data *and* the view is not yet synced. Delete the `loadX()` aliases.

**A-21. Discovery (daemon-authored `BAHIA_SYSTEM_DISCOVERY` + NIP-51 relay sets) is a soft but pervasive dependency.** Severity: **medium**.
- Evidence: if the daemon's discovery events are absent, `normalizeDiscoveryEvents` returns `null` (`stores/discovery.svelte.js:98-102`), `publishDiscoveryInfo` no-ops (`:38`), and `systemInfo.data` stays `null`. Consequences:
  - A-1's auth gate fails closed.
  - Every encrypted RPC throws `assertEncryptedRequestsAvailable` (`nostr/encrypted-controlplane-utils.js:172-183`), which breaks orgs, payments, notifications, security, secrets, run logs, the assistant, relay settings and *all* mutations (A-10).
  - Auth relay candidates shrink to signer relays only (`stores/auth.svelte.js:298-309`).
  - `/settings` shows nothing (`routes/settings/+page.svelte:80`).
  
  The read-model bootstrap itself is correctly independent (`bootstrap.svelte.js:136-142` uses the static seed from `app.html:43-58`; `:159-163` treats discovery as optional). Discovery is cached in localStorage for 15 min (`discovery.svelte.js:12-13,178-219`), and a cache hit skips the live subscription (A-16).
- Fix: keep discovery as ordinary store-backed replaceable state. Feature availability should mean "the daemon has announced support". Its absence should disable daemon-dependent actions, not reads.

**A-22. The same kinds are downloaded several times by different stores, giving duplicated and divergent state.** Severity: **medium** (H6).
- Evidence: `stores/controlplane/events.svelte.js:72` already subscribes to *all* `CASCADIA_CONTROLPLANE_STATE` and `NIP78_APP_DATA` from the service. On top of that:
  - `stores/dns.svelte.js:330` re-subscribes to `CASCADIA_CONTROLPLANE_STATE` with `#domain:dns` (limit 5000).
  - `stores/fips-mesh.svelte.js:68-70` re-subscribes to DNS and worker state.
  - `stores/security.svelte.js:339` re-subscribes to NIP-78 and controlplane state.
  - `nostr/retained-domain-subscription.js:26-38` re-subscribes per domain.
  - `stores/assistant.svelte.js:568-572` and `nostr/continuity.ts:120-129` open further overlapping REQs.
  
  Each has its own dedup set, parser and replaceable map (`events.svelte.js:61-62` vs. `dns.svelte.js` vs. `security.svelte.js`' `securityFindingEvents`), and they apply different latest-wins rules. `collections/utils.js:55-74` (`selectProjectedEvent`) also picks winners across *distinct* coordinates by content `updated_at`.
- Fix: one event store with one ingestion path. Domain views become indexed queries over it.

**A-23. Domain stores are torn down on navigation and re-fetched on return.** Severity: **medium**.
- Evidence:
  - Orgs, payments, notifications and security pages subscribe on mount and unsubscribe on destroy (`routes/orgs/+page.svelte:19-30`, `routes/payments/+page.svelte:61-70`, `routes/notifications/+page.svelte:62-72`, `routes/security/+page.svelte:20-30`), and re-issue list RPCs on every mount.
  - `routes/dns/+page.svelte:35-40` calls `connect(...)` and `disconnect()` on mount and destroy. `connect` force-reconnects the shared pool and re-downloads the DNS window (A-5, A-14).
  - `stores/security.svelte.js:328-329` clears `securityFindingEvents` whenever the scope changes.
- Fix: a store-level cache that survives navigation, with interest ref-counting on subscriptions instead of teardown.

**A-24. Error paths wipe known-good data.** Severity: **medium**.
- Evidence: `stores/notifications.svelte.js:81` sets `channels = []` on error, and `:136` sets `logs = []`. `stores/payments.svelte.js:146` (`loadPaymentHistory` catch) sets `records = []`. `stores/security.svelte.js:120-121` sets `findings = []`, and `:146` sets `schedules = []`. `routes/+page.svelte:525` sets `pendingDeployments = []`. `stores/system.svelte.js:63-66` nulls discovery on a forced failure.
- Fix: stale-while-revalidate. Keep the last known state from the store and surface the error as a banner.

**A-25. NIP-09 deletions and NIP-40 expiration are ignored.** Severity: **medium**.
- Evidence: nothing in `web/src` references kind 5, `expiration`, NIP-09 or NIP-40 (grep is empty). Deletion is only the app-level tombstone convention `content.deleted === true` / tag `deleted=true` (`nostr/replaceable.js:10-14`; `collections/utils.js:91-95,107-111`). A user- or daemon-issued kind 5 never removes anything, and expired events are rendered indefinitely.
- Fix: the store honours kind-5 events (by `e` id, and by `a` coordinate up to the deletion's `created_at`, from the same author) and sweeps NIP-40 expirations. The daemon should emit kind 5 alongside, or instead of, content tombstones.

**A-26. Author filters are open for fleet-critical kinds, so any pubkey can inject workers, jobs or operations.** Severity: **medium** (integrity and DoS).
- Evidence:
  - `stores/controlplane/events.svelte.js:73` has `{ kinds:[LOOM_WORKER_ADVERTISEMENT], limit:1000 }` with no `authors`.
  - `:74-78` does the same for Loom job kinds, and `:91-95` for `EXTERNAL_OPERATION_KINDS`.
  - `shouldAcceptControlplaneEvent` accepts any non-canonical kind from anyone (`:112-116`).
  - `stores/souls.svelte.js:525-540` subscribes to souls, templates and drafts and to `RUNTIME_CAPABILITY` with no author unless one is passed.
  - The per-subscription dedup sets and maps are unbounded (`events.svelte.js:61-62`, `pool-subscriptions.js:15,159`), so a spammer can grow memory without limit.
- Fix: author-scope by an allow-list (fleet worker set published by the operator or daemon as a NIP-51 list, and job authors limited to the known requesters). Bound the store with LRU eviction.

**A-27. Multi-letter tag filters (`#domain`, `#schema`, `#family`, `#mesh`, `#artifact`, `#subject`) are relied on in REQs.** Severity: **medium** (relay portability).
- Evidence: `nostr/retained-domain-subscription.js:35`, `stores/dns.svelte.js:330`, `stores/fips-mesh.svelte.js:68-70`, `stores/assistant.svelte.js:570-572`, `routes/artifacts/[id]/+page.svelte:227-233`. NIP-01 only defines indexed queries for single-letter tags. Many relays ignore or reject these filters, which yields empty or unfiltered results. They currently work only if Bahia's khatru sidecar indexes every tag (not verified here; Investigator C should confirm).
- Fix: use single-letter tags (`t`, `k`, or namespaced `d` prefixes) or distinct kinds for routing.

**A-28. `replaceableKey` does not check the kind range; replaceable semantics are applied to whatever it is given.** Severity: **low** (latent).
- Evidence: `nostr/replaceable.js:4-8` keys every non-30000 kind as `kind:pubkey`, which includes regular kinds. Current callers pass replaceable or addressable kinds (for example `collections/workers.svelte.js:90,112`), but `selectProjectedEvent` (`collections/utils.js:57-58`) runs for every handler. A future handler on a regular kind would collapse all of an author's events into one. The tie-break (lower id wins, `:24`) is correct.
- Fix: gate on NIP-01 ranges (0, 3, 10000–19999, 30000–39999) and pass regular events through by id.

**A-29. Ad-hoc localStorage and sessionStorage caches proliferate, each with its own format and TTL, and none is an event store.** Severity: **low**.
- Evidence:
  - discovery (`stores/discovery.svelte.js:12-13,178-219`, 15 min)
  - docs events (`docs/nostr.js:21-50`)
  - assistant transcripts (`stores/assistant.svelte.js:157-236`)
  - auth session including profile and relays (`stores/auth.svelte.js:173-206`)
  - provisioning resume (`routes/souls/new/+page.svelte:166-173`)
  - relay override (`nostr/subscriptions.js:62-114`, dead, see A-14)
  - theme (`stores/theme.svelte.js`, fine)
  - the dashboard pending-count `sessionStorage` (A-31)
- Fix: fold all event-derived caches into the single IndexedDB event store.

**A-30. REST catalogue (H5): every HTTP call in `web/src`, and whether each could be a relay subscription.** Severity: **high** (in aggregate).

| # | Call site | Endpoint | Gates render? | Relay-replaceable? |
|---|---|---|---|---|
| 1 | `stores/auth.svelte.js:457` | `GET /api/v1/orgs` (NIP-98) | **Yes, all 24 protected prefixes** (A-1) | Yes: org membership events |
| 2 | `stores/souls.svelte.js:223` | `GET /api/v1/soulfactory/runtimes` | Fails closed: runtime targets are hidden in `/souls/new` | Yes: `RUNTIME_CAPABILITY` events already exist (`nostr/runtime.js`) and discovery could list enabled runtimes |
| 3 | `api/client.js:105-119`, `routes/instance-health/+page.svelte:39,45,67-69` | `GET /instance-health`, `…/managed-instances/{u}/health`, `/health/events`, `/health/recovery-attempts` | Yes: the page is empty without the daemon. It fetches in `$effect` and has no live updates | Yes: addressable health state plus event stream |
| 4 | `api/client.js:121-130`, `instance-health/+page.svelte:87,108` | `POST`/`DELETE …/maintenance` | Mutation | Yes: a signed maintenance intent event |
| 5 | `api/client.js:177-187`, `routes/route-canaries/+page.svelte:40,47,75-76`, `components/RouteCanaryOutages.svelte:58` (embedded in `routes/environments/[id]/+page.svelte:32`) | `GET /route-canaries`, `…/routes/{h}/canary`, `…/canary/events` | Yes for that page and panel | Yes: canary state as addressable events |
| 6 | `api/client.js:132-134`, `routes/config-fabric/+page.svelte:26`, `config-fabric/[coordinate]/+page.svelte:39` | `GET /config-fabric/drift` | Yes | Yes |
| 7 | `api/client.js:136-141`, `config-fabric/ConfigPublishForm.svelte:61` | `POST /config-fabric/events`: **a REST write that publishes a Nostr event server-side** | Mutation | Yes: sign locally and publish directly |
| 8 | `api/client.js:143-148`, `config-fabric/[coordinate]/+page.svelte:60` | `POST /config-fabric/rollback` | Mutation | Yes: a signed rollback intent |
| 9 | `api/client.js:190-207`, `routes/artifacts/+page.svelte:94-96` | `POST /blossom/list`, `GET /blossom/servers`, `GET /blossom/health` (`/blossom/stats` has no caller) | Blossom panel only | Yes: kind 10063 server list plus direct BUD-02 `/list` to the Blossom servers |
| 10 | `api/client.js:213-219`, `components/SBOMDetails.svelte:143` | `GET /blossom/blob/{sha256}` (proxy) | SBOM content only | Partly: a direct Blossom fetch already exists as fallback (`SBOMDetails.svelte:154`). The proxy exists for mixed-content reasons |
| 11 | `api/client.js:150-175` | `GET/POST /artifacts/{id}/sbom*`, `/sbom/search` | **No callers (dead code)** | Delete: this is regression bait, since it matches the documented re-add cycle in B1 |
| 12 | `version-reload.js:3-4,53` | `GET /_app/version.json` every 30 s, then `location.reload()` | No | N/A (static asset). But because there is no event store, each deploy forces every open tab into a full re-sync at the same moment (a thundering herd on relays) |
| 13 | `stores/dns.svelte.js:277` | NIP-11 relay info (`Accept: application/nostr+json`) | No | Legitimate: a relay document, not the backend |

`api/client.js:58-79` also adds a GET retry with exponential backoff. The client is `null` during SSR (`:221`).

**A-31. The dashboard duplicates derived state in `sessionStorage` and renders placeholder rows.** Severity: **low**.
- Evidence: `routes/+page.svelte:414-445` caches the pending-deployment count, and `:500-505` sets `pendingDeployments = new Array(cachedCount)` (fake rows) before recomputing from `deploymentIntents`. `:537` re-runs this through `queueMicrotask` on every intent change.
- Fix: derive it synchronously from the store.

**A-32. The dashboard fans out one encrypted RPC per worker to compute cost.** Severity: **medium**.
- Evidence: `routes/+page.svelte:450-497` calls `requestPaymentHistoryRecords` for each worker pubkey (concurrency 4). It re-runs when the worker set changes (`:547-552`). With the daemon offline, the cost card errors. With N workers it makes N daemon round-trips per dashboard load.
- Fix: the daemon publishes an aggregated, addressable cost-summary event, or per-worker payment receipts as events that the store aggregates.

**A-33. Publishing goes only to currently-connected relays, with no outbox, retry or NIP-65 write-relay selection.** Severity: **low**.
- Evidence: `nostr/pool-publish.js:5-6` returns `[]` silently when no relay is connected. User write relays from kind 10002 are fetched (`stores/auth.svelte.js:394-400`) but never used for publishing requests or intents.
- Fix: a persisted outbox in the event store (pending → OK/failed per relay), retried on reconnect, targeting the seed relays plus the user's NIP-65 write relays.

#### Boot sequence: as-is vs to-be

**As-is** (cold or warm reload, identical except for the discovery cache):
1. `routes/+layout.svelte:45-59` (microtask): three calls fire in parallel. `loadAll()` → `bootstrapControlplane()`. `initializeAuth()`. `eagerRelayConnect()` → discovery on a *second* pool.
2. `bootstrapControlplane`: `setAllLoading(true)`, then `hydrateCachedCollections()` (writes arrays, not Maps; hidden by the loading flags). Then `nostr.connect(seed, {force:true})`. Then one REQ with seven filters (`limit:1000` / `since: now−7d`). Then it **awaits EOSE from every relay** (no deadline). The first event calls `refreshCollections()`, which wipes the hydrated arrays. Every later event triggers a full rebuild (O(n²)). After EOSE: `loading=false`, persist the snapshot, then fire discovery again.
3. In parallel, for protected routes, `AuthGuard` awaits `initializeAuth`: NIP-07 wait up to 1.5 s, then NIP-46 reconnect, then **discovery (≤10 s)**, then **REST `GET /api/v1/orgs`**, then kind 10002 (≤5 s, new pool), then kind 0 (≤5 s, new pool). If there is no REST success, it runs `goto('/')`.
4. The page mounts and calls `loadX()` (the same bootstrap promise). Domain pages then add RPC round-trips to the daemon (orgs, payments, notifications, security…) plus a `subscribeToDomainRefresh` REQ that re-runs those RPCs on every live event.
5. Result with the daemon offline: `/` renders relay read models after full EOSE. Every protected route redirects. Every encrypted feature and every mutation errors or times out.

**To-be:**
1. Synchronously open IndexedDB (namespaced by `service_pubkey`), load events for the route's interests, render immediately. A signer-verified persisted session counts as authenticated at once.
2. Connect a single pool to seed plus NIP-65 relays. For each interest filter, send a REQ with `since = persisted cursor − skew`. For addressable read-model sets, run NIP-77 negentropy against khatru periodically and on reconnect. Keep every REQ open. Per-relay EOSE only updates a "synced" indicator.
3. Ingest through one path: verify, dedup by id, replaceable/addressable latest-wins with tie-break, apply NIP-09/NIP-40, batch-commit to IndexedDB and advance cursors. Views are derived queries.
4. Writes: sign locally, insert as pending in the store, publish through an outbox with OK tracking, and let daemon status events finalise them. No awaited RPC.
5. Daemon offline: every view renders from store plus relays. Daemon-dependent actions show "awaiting controller" on their pending intents. Nothing redirects and nothing blanks.

#### Files whose design should be replaced wholesale
- `lib/stores/collections/indexeddb-cache.js` and the cache half of `lib/stores/collections/index.svelte.js`: replace with an event store (nostr-idb, applesauce, or welshman).
- `lib/stores/controlplane/bootstrap.svelte.js`, `lib/stores/controlplane/connection.svelte.js`, and `lib/stores/controlplane/events.svelte.js` (`readModelFilters`, `refreshCollections`-per-event): replace with store ingestion plus cursor/negentropy sync.
- `lib/stores/collections/*.svelte.js` (Map + `$state` array pairs with `refreshX()`): replace with derived queries over the store.
- `lib/nostr/pool-client.js`, `pool-subscriptions.js`, `pool-publish.js`, `connection-guard.js`, `retained-domain-subscription.js`, `subscriptions.js` (singleton) and the ad-hoc clients in `discovery.svelte.js`, `auth.svelte.js`, `profile.js` and `encrypted-controlplane-transport.js`: replace with one pool, interest ref-counting and an outbox.
- `lib/nostr/encrypted-controlplane*.js`, `lib/nostr/controlplane-requests.js`, and `lib/stores/public-controlplane.svelte.js`: replace with a durable signed-intent publisher plus status observation. Keep RPC only for the interactive assistant.
- `lib/stores/{orgs,payments,notifications,security,service-secrets,deployment-run-logs,artifact-signatures}.svelte.js` and `lib/nostr/{dns-controlplane,relay-settings-controlplane}.js`: re-base their reads on subscribed encrypted read-model events.
- `lib/api/client.js` and every importer: delete, after relay replacements exist for rows 1–9 of A-30. Delete the dead SBOM methods immediately.
- `lib/auth/route-access.js`, `lib/components/AuthGuard.svelte`, and the backend half of `lib/stores/auth.svelte.js` (`configureBackendAuth`, `compatibilityPatch`, `hydrateAuthMetadata` one-shots): replace with relay-derived roles, and gate mutations only.
- `lib/stores/dns.svelte.js` and `lib/stores/fips-mesh.svelte.js` connection management: stop force-reconnecting the shared pool, and re-base on the shared store.

## Investigator Findings: B — Core daemon & data authority (Postgres, projector, write path, reconcilers, REST)

_Investigator B, 2026-09-30, HEAD `1537dee3`. All file:line refs are to `bahia/`. Everything marked CONFIRMED was read directly in source; PLAUSIBLE means the logic reads that way but I did not reproduce it with a test._

### Verdict on hypotheses

| # | Hypothesis | Verdict |
|---|---|---|
| H1 | Postgres is still authoritative | **CONFIRMED.** Every domain entity is written to a `pg_*` repository. Only services/environments have an optional publish-first wrapper, and even then Postgres is what every reader, reconciler and projector consults. (B-1, B-5, B-6, table) |
| H2 | The projector re-projects DB state | **CONFIRMED.** It runs a full snapshot at startup, then every 10 min (hardcoded), plus per-entity after every in-process bus event, always reading Postgres. "Hydration" means warming its in-memory dedupe fingerprints from the Postgres `nostr_events` table, not from relays. Every publish gets a new `created_at`/id. `nostr_events` is a write-through outbox plus audit log of every inbound and outbound event. (B-1…B-4, B-13) |
| H3 | DB-optional mode works | **Mostly disproved.** Without the DB, startup drops to Tier1. All domain repos are nil, and every Tier2/3 runner (projector, reconciler, reactor, DNS, LLM, backups, SoulFactory, …) is skipped. The bootstrapper *cannot* rebuild any domain state from relays: the canonical kind is decoded with a no-op decoder and there is no applier. It even raises the active tier back to 3 over nil repos. (B-8…B-12) |
| H4 | Reconcilers poll the DB on tickers | **CONFIRMED.** 28 indefinite loops, 22 of which poll Postgres (table in B-23). |
| H5 | REST is the real surface | **CONFIRMED.** About 70 GET routes serve Postgres directly and ~37 mutation routes bypass Nostr. `pkg/client` (CLI) is REST-first. (B-26) |
| H6 | ContextVM handlers write DB then ack | **CONFIRMED.** Handlers write the DB (optionally publishing first), reply with the DB row, and the canonical event is emitted afterwards by the projector (twice, see B-5). Requests have a 2-minute replay window, so mutations are ephemeral RPC rather than durable desired-state events. (B-25) |
| H7 | Daemon depends on the web frontend | **Disproved (hard dependency).** The daemon serves no SPA and has no `go:embed`/web-dist/return-URL coupling. Soft coupling: orgs, secrets, notifications and tool denylist can only be changed through REST or ContextVM RPC, so some client must drive them; no event-driven path exists. (B-27) |

### Findings

**B-1. The projector is, by design, a Postgres→relay republisher** — CRITICAL — CONFIRMED
- Evidence:
  - `internal/adapters/nostr/projector.go:170-172`: *"Projector republishes Bahia's authoritative DB state into canonical Nostr read models … a startup and periodic snapshot can repair a cold or wiped sidecar store."*
  - Wired with the DB-backed `RegistryService` as its source: `internal/app/app.go:~914` `nostrAdapter.NewProjector(cfg.Nostr, registry, controlPlanePool, nostrEventRepo, …)`.
  - `Run` (`projector.go:384-408`) calls `RepublishSnapshot` at startup, then on a `time.NewTicker(p.repairInterval)` whose default is `10 * time.Minute` (`:295`). No config override is wired; `WithProjectorRepairInterval` is only used in tests.
  - `RepublishSnapshot` (`:411-535`) lists every service, environment, state, build/artifact/intent/run, policy, LLM route/state, ML model/version/endpoint/state/provenance/capability, worker assignment/drain, backup recipe/policy/repo/run/restore/verification/retention/posture, DNS endpoint/zone/backend/policy and SBOM from Postgres, and publishes each one.
  - `handleEvent` (`:538-697`) is subscribed to ~55 in-process bus event types (`:313-381`) that services emit *after* a repo write (e.g. `internal/service/registry.go:166-173`). It then re-reads the row from Postgres (`p.source.GetBuild/GetArtifact/GetDeploymentIntent…`) and publishes.
- Why it's an anti-pattern: DB rows are the trigger and the source. Relays hold a copy that is repeatedly overwritten from the DB. A relay-side change by any other author is overwritten on the next repair pass.
- Fix: invert it. The command/desired-state event on the relay is the write. The daemon subscribes (REQ with a persisted `since`, plus NIP-77 gap fill), applies to an optional local index, and publishes *derived* outputs (status/observations) exactly once, keyed by a deterministic `(kind, pubkey, d)`. Delete periodic repair; replace it with negentropy reconciliation of the daemon's own outputs against a local event store.

**B-2. The 2026-05-23 "projector authority flip" (`fabe22dd`) was never wired, and would still read Postgres if it were** — HIGH — CONFIRMED
- Evidence:
  - `WithProjectorSource` (`projector.go:229`) and `NewCacheProjectorSource` (`projector_source.go:127`) have zero non-test callers. `snapshotSource()` therefore always returns `legacyProjectorSource{source: p.source}` (`projector_source.go:178-183`).
  - `CacheProjectorSource` embeds `DBProjectorSource{ProjectorSourceRepositories: repos}` (`projector_source.go:121-128`). Its "cache" is the same `pg_*` repositories.
  - The commit message itself says *"DB-backed source preserves backward compat; cache-backed source opt-in."*
- Why it's an anti-pattern: this is a partial migration presented as done. The charter item "projector authority flip" is in fact still open.
- Fix: delete `ProjectorSource`. Derived outputs should be computed from the local event store, not from a second copy of DB tables.

**B-3. The dedupe that stops re-projection churn has its memory in Postgres `nostr_events`, not on relays, and that memory is lossy** — HIGH — CONFIRMED
- Evidence:
  - `projection_dedupe.go` keeps an in-memory `published map[projectionKey]fingerprint`.
  - `hydrateProjectionCache` (`:241-300`) warms it from `p.eventRepo.ListByKind(ctx, wireKind, projectionHydrateLimit=10000)`, i.e. `SELECT … FROM nostr_events WHERE kind=$1 ORDER BY created_at DESC LIMIT $2` (`internal/repository/pg_nostr_event.go:209-219`).
  - This is lossy in four ways:
    - (a) ~30 projection families collapse onto one wire kind, `KindCASControlState` = 30900, so the 10 000 cap is **global across all families**.
    - (b) The rows are every historical *version*, not one per coordinate.
    - (c) The author filter (`record.PubKey != servicePubkey`, `:282`) is applied *after* the LIMIT, so inbound 30900 rows from other authors (recorded by the Subscriber) consume the budget.
    - (d) With no DB, the repo is the empty `InMemoryNostrEventRepository` (`app.go:246-250`).
  - Any coordinate not covered is re-signed with a fresh `created_at` on the next snapshot.
  - Since `c2300b7f`, a hydration *error* returns `ErrProjectorHydrationBackoff` and suppresses the publish (`:229-239`, `:371-378`). A DB hiccup therefore stops *all* dedupable projections.
- Why it's an anti-pattern: "have I already published this?" is answered from a private SQL table instead of the relay (`REQ {kinds:[30900], authors:[self], #d:[…]}`) or a local event store. The fix for the churn was yet more DB coupling (this week's `c2300b7f`/`e9123939`).
- Fix: keep the daemon's own outputs in a local event store (e.g. `fiatjaf.com/nostr/eventstore`) synced with relays via NIP-77. Compare against the stored latest event for `(kind, pubkey, d)` before signing.

**B-4. Every publish mints a new event id (`created_at = now`), even for identical content** — MEDIUM — CONFIRMED
- Evidence:
  - `projector.go:3559-3566` `CreatedAt: gonostr.Now()`.
  - `relay_first_registry.go:294` `CreatedAt: gonostr.Now()`.
  - `publisher.go:199-205` `CreatedAt: nostr.Timestamp(time.Now().Unix())`.
  - Volatile keys (`updated_at`, `observed_at`, `materialized_at`, …) are stripped only for the dedupe fingerprint (`projection_dedupe.go:52-61`), not from the published content.
- Why it's an anti-pattern: when the dedupe cache is cold (B-3), identical state produces a new id, new relay writes and new `nostr_events` rows. Consumers see "changes" that are not changes.
- Fix: derive `created_at` from the entity's semantic version (e.g. `updated_at`), and never publish when the stored latest event for the coordinate has equal stable content.

**B-5. One mutation produces 2–3 relay events from competing writers with different content** — HIGH — CONFIRMED
- Evidence, service create via ContextVM:
  - `RelayFirstRegistry.CreateService` publishes 30900 `d=<uuid>` with `org_id`, `repository`, and empty `created_at`/`updated_at`. The timestamps are empty because `CreatedAt` is only assigned later in `pg_service.go:28-34` (`relay_first_registry.go:66-84`, `:224-250`).
  - It then calls the delegate, which writes Postgres and emits bus `EventServiceCreated` (`registry.go:158-175`).
  - The projector then publishes the *same coordinate* again with different tags (`legacy_kind`) and different content (no `org_id`/`repository`) (`projector.go:2724-2755`).
  - Dedupe cannot catch this: the relay-first event goes out on a raw pool (`relayFirstNostrPublisher{pool: relayPool}`, `app.go:336`) and is never recorded to the fingerprint cache.
  - The projector also emits a CAS audit 4903 event (`handleEvent`→`publishAudit`, `:3499-3527`).
- Evidence, builds/artifacts/intents/runs/drift:
  - The legacy `Publisher.SetupSubscriptions` (`publisher.go:169-190`) *additionally* publishes kinds 31000/31001/31002/… (`d=entity id`, raw `e.Data` JSON).
  - That comes on top of the projector's 30900 registry projection and the 4903 audit event.
- Why it's an anti-pattern: this is not "publish once". There are two schemas for one addressable coordinate, and latest-wins flips between them (org scoping appears and disappears), on top of 3× relay and `nostr_events` volume.
- Fix: one writer per coordinate. The client-signed desired-state event *is* the record, and the daemon publishes only its status/observation events. Delete the legacy `Publisher.SetupSubscriptions` kinds.

**B-6. The relay-first write path (`61627b8c`) is optional, covers two entities, and is publish-then-DB with no reconciliation** — HIGH — CONFIRMED
- Evidence:
  - `app.go:326-337` enables it when `policy.RequestedMode != ModeFull || cfg.Nostr.PublishEnabled`. The comment above claims it *"defaults off for backward compatibility"* in full mode, which contradicts the condition.
  - It wraps only `CreateService/UpdateService/DeleteService/CreateEnvironment/…` (`relay_first_registry.go`). It is used only by the ContextVM encrypted handlers, OCI, and `registryMutations` (`app.go:1043-1046`, `:1529-1530`).
  - Adoption (`app.go:~392` `NewAdoptionService(registry, …)`), SoulFactory (`buildSoulFactoryRuntime(ctx, cfg, registry, …)`, `app.go:606`; `internal/soulfactory/bahia_integration.go:104,632`), and all of builds/artifacts/intents/runs/policies/LLM/ML/backups/DNS use the plain DB-first `RegistryService` or their own services.
  - If the DB write fails after a successful publish, the relay holds a state that nothing ever reads back: there is no live applier (B-8), and readers and reconcilers use Postgres.
- Why it's an anti-pattern: this is "dual write" rather than "event is the write". The relay copy is only authoritative in name.
- Fix: make every mutation a signed addressable desired-state event, published by the *client* (web/CLI signer). The daemon only consumes it. Drop RPC-driven DB writes.

**B-7. The extended relay-first wrappers are dead code** — MEDIUM — CONFIRMED
- Evidence: `internal/service/relay_first_extended.go` `NewRelayFirstLLM`/`NewRelayFirstBackup`/`NewRelayFirstML`/`NewRelayFirstPackage`/`NewRelayFirstDNS` have zero non-test callers. `RelayFirstML` is explicitly a pass-through (`:168-176`, *"pass-through wrapper until ML command/result codecs are extracted"*).
- Why it matters: this is fake completion of charter item 16 ("core write-path inversion"). The DB-first write path remains for LLM, backup, ML, package and DNS.
- Fix: delete them and track the real inversion per domain.

**B-8. The bootstrapper rebuilds no domain state from relays** — CRITICAL — CONFIRMED
- Evidence:
  - The only replay group carrying domain state is `state_snapshot` = `[KindCASControlState]` (30900) (`catalog.go:437`).
  - `registerRequiredGroupNoopDecoders` (`catalog.go:524-533`) installs `decodeNoopProjection` for 30900, which returns kind/d/family `"state"` with **no payload** (`:1061-1076`).
  - The real decoders (`decodeServiceProjection`, …) are registered only for legacy kinds 31962/31963/… (`:545-571`), and those kinds are in no replay group.
  - `RelayProjectionCache` registers appliers only for `worker, continuity, service, environment, build, artifact, policy` (`internal/service/relay_projection_cache.go:58-77`). There is no `"state"` applier, so `Apply` just upserts ordering meta (`:80-115`).
  - That meta repo is **in-memory even when Postgres is up** (`app.go:632` `newInMemoryProjectionMetaRepo()`).
  - With no DB, `bootstrapCache` is nil (`app.go:630-645`), and `decodeAndApply` returns early (`bootstrapper.go:445-447`).
- Why it's an anti-pattern: the "reconstructible Bahia" charter item is unimplemented in practice. Relays cannot rebuild any daemon state. Postgres is the only store, which makes it authoritative by construction.
- Fix: decode 30900 by its `domain`/`entity`/`legacy_kind` tags into typed payloads, apply them into a local event store (not bespoke tables), and add a regression test "wipe DB → bootstrap → services/environments visible".

**B-9. Bootstrap is a one-shot full re-fetch that ends at the first relay's EOSE, and its author scoping is dead** — HIGH — CONFIRMED
- Evidence:
  - `runGroup` `defer subscription.Close()` (`bootstrapper.go:287`) and returns success on the **first** per-relay EOSE (`:358-366`), discarding slower relays.
  - Snapshot groups use `Until: startedAt` with no `since` or limit (`:445-453`), so each boot re-downloads the whole 30900 set.
  - Live groups are one-shot catch-ups with a 15 s timeout (`:104-106`, `:455-467`), not persistent subscriptions.
  - `decodedEvents == 0` counts as failure (`:235-244`), so an empty fleet never becomes "ready" and retries forever (30 s doubling to 5 min, `:143-165`).
  - `scopedFilter` switches on `"system_snapshot","worker_snapshot","core_registry_snapshot","continuity_*","core_control_plane_live"` (`:488-490`), none of which exist in the catalog (`catalog.go:436-442`). So **no `authors` filter is ever applied**, and any pubkey's 30900 events count toward readiness.
- Why it's an anti-pattern: bootstrap is timeout- and EOSE-driven, closed after EOSE, has no cursor, no negentropy and no author trust. It is the "stateless request/response" shape from the Symptoms list.
- Fix: one long-lived REQ per input set, with `authors` from the trust set, `since` from a persisted per-relay cursor, EOSE marking "caught up" without closing, and NIP-77 on reconnect. Readiness should be "local store synced", not "decoded > 0 events".

**B-10. The bootstrapper re-raises the active tier to 3 when Postgres is down, un-gating routes over nil repositories** — HIGH — PLAUSIBLE (the logic is unambiguous; not reproduced with a test)
- Evidence:
  - `connectOptionalDatabase` sets `ActiveTier=Tier1` on failure (`app.go:1953-1971`).
  - The bootstrapper is built with `RequestedTier: int(policy.RequestedTier)` = 3 (`app.go:658-662`).
  - All Tier3 replay groups are `Required: false` (`catalog.go:440-442`), so `RequiredGroupsForTier(3)` covers only the tier0/1 groups and `computeReadyTier` returns 3 (`bootstrapper.go:497-514`).
  - `bootstrapperRunner.Run` then does `r.policy.SetActiveTier(Tier(r.bootstrapper.ReadyTier()))` with no DB cap (`app.go:~2089-2092`).
  - `TierGate` evaluates `RouteEnabled` per request (`internal/api/middleware/tier_gate.go:17-44`).
  - The repositories stay nil. The comment at `app.go:172-174` relies on "route gating prevents tier2/tier3 routes from being accessed, so nil repos won't be hit".
  - Background runners stay gated because `startBackgroundRunners` evaluates tiers once (`app.go:2483`).
- Why it matters: "DB-optional" mode then serves 500s or nil-pointer panics on `/api/v1/services` and friends, and `/ready` reports a tier the process cannot deliver.
- Fix: none needed in the target model (no tiered DB). In the interim, `SetActiveTier(min(readyTier, dbCap))`.

**B-11. DB-optional mode disables almost everything the daemon exists to do** — CRITICAL — CONFIRMED
- Evidence:
  - With no DB, `ActiveTier=1` and every domain repo is nil (`app.go:172-231`, log *"tier2/tier3 repositories are nil"*).
  - Tier2 runners are skipped: projector (`:917`), reconciler (`:955`), control-plane reactor (`:1712`), virtualization (`:950`), SoulFactory runner (`:619`), SBOM async (`:1638`).
  - Tier3 runners are skipped: LLM coordinator and reconciler (`:826-827`), DNS reconciler (`:867`), backup coordinators and scheduler (`:764-771`), HiveCI (`:1118-1119`), security (`:1200-1201`), tool coordinator (`:1245`), FIPS subscriber (`:742`).
  - Still running: the outbox publisher (in-memory), the ContextVM transport (Tier1; its registry handlers are backed by nil repos), the bootstrapper (no-op apply, B-8), identity/status publication, the continuity failover engine (in-memory stores, `app.go:137-139`), the relay-settings hydrator and the assistant.
  - Recovery is a 30 s DB probe that requests a **process restart** (`internal/app/db_recovery.go:31-67`). There is no hot rebuild.
- Why it's an anti-pattern: the stated goal was a daemon that "functions with relays only". Today, relays-only means no deploys, no reconciliation, no DNS, no backups.
- Fix: make the local store an embedded event store (SQLite/LMDB/bbolt via `eventstore`) that is always present. Postgres, if kept at all, becomes an optional *index* rebuilt from it.

**B-12. The in-memory `nostr_events` fallback is unbounded** — MEDIUM — CONFIRMED
- Evidence: `internal/repository/in_memory_nostr_event.go:14-48` is a `map[string]NostrEventRecord` with no eviction. Every outbound event (publisher/projector) and every inbound event (subscriber/reactor/responders) is `Record`ed.
- Why it's an anti-pattern: DB-less mode leaks memory without bound, and its dedupe/cursor state is lost on restart, which feeds B-3 and B-15.
- Fix: a bounded, persistent local event store with replaceable-collapse semantics.

**B-13. `nostr_events` is a write-through outbox and audit log of every event; a DB outage blocks relay publishing** — HIGH — CONFIRMED
- Evidence, the outbox:
  - `Publisher.publishEvent` persists a `pending` row *before* publishing and **drops the event** if the insert fails (`publisher.go:214-226`).
  - `PublishSignedEventWithResults` returns an error in that case (`:495-503`).
  - The drain loop (`:351-392`) polls `ListUnpublished(100)` every 1 s idle (`pg_nostr_event.go:140-154`, `ORDER BY received_at`). There is no attempt cap or dead-letter, so permanently rejected rows are retried forever at the head of the queue.
- Evidence, the audit log:
  - The projector records every projected event after publish (`projector.go:3576-3592`).
  - Inbound events are recorded by the Subscriber (`subscriber.go:389`), the Reactor (`reactor.go:619`), the LLM/tool/backup responders (`llm_responder.go:134`, `tool_responder.go:137`, `backup_restore_responder.go:187`), `release_promotion_audit.go:36` and HiveCI (`adapters/hiveci/audit.go:45,72`, `subscriber.go:416,522`).
  - All versions of replaceable events are kept. This is the ~19 GB / ~12M rows, and the archive/prune subsystem (`pg_nostr_event_archive.go`, `cmd/bahia-event-archive`) exists to cope with it.
- Why it's an anti-pattern: the relay is the durable log. A private SQL mirror of all traffic (a) makes Postgres a publish dependency, (b) duplicates relay storage without NIP-01 replaceable semantics, and (c) becomes a republish source.
- Fix: publish with OK verification and retry in memory with bounded backoff. Persist only the daemon's own latest outputs in a local event store with replaceable collapse. Drop the outbox table.

**B-14. Inbound event handling is gated on a successful Postgres insert** — HIGH — CONFIRMED
- Evidence: `Subscriber` returns without invoking handlers if `eventRepo.Record` fails (`subscriber.go:389-397`), and uses the insert result as its dedupe (`:403-409`). `Reactor.auditInboundEvent` returns `false` (drop) on error (`reactor.go:610-637`).
- Why it's an anti-pattern: a DB outage makes the daemon deaf to relays.
- Fix: dedupe by event id in a bounded seen-set or local event store. Make persistence best-effort and asynchronous.

**B-15. Subscription `since` cursors are `max(created_at)` of `nostr_events` over a kind set, including self-published rows** — MEDIUM — CONFIRMED
- Evidence: `subscriber.go:494-503` (`LatestCreatedAtForKinds[AndAuthors]`), `replay_cursor.go:36-60` (bootstrap live groups; 1 s overlap), and the deprecated `publisher.Subscribe` (`:426-433`).
- Why it's an anti-pattern: one cursor per kind set (not per relay or filter) plus inclusion of the daemon's own fresh events means a foreign event with a slightly older `created_at` (clock skew, late propagation, relay outage) is skipped forever. No negentropy backstop exists.
- Fix: persist a per-(relay, filter) cursor advanced on EOSE, and periodically run NIP-77 against each relay.

**B-16. A non-deduplicated CAS audit event is published for every bus event, including every reconcile tick** — MEDIUM — CONFIRMED
- Evidence:
  - `publishAudit` (`projector.go:3499-3527`) is called unconditionally at the top of `handleEvent` (`:542-544`). Audit is exempt from dedupe (`projection_dedupe.go:388`).
  - `auditKindForEvent` maps `EventReconcileCompleted`→`KindReconcileAudit` and `EventRuntimeObservation`→`KindObservation` (`projector.go:~3609-3620`).
  - The reconciler emits `EventReconcileCompleted` every interval (60 s default, `config.go:1296`) whether or not anything changed (`internal/reconcile/reconciler.go:165-168`).
- Why it matters: at least 1 440 audit events per day per daemon go to relays and to `nostr_events` for "nothing happened".
- Fix: emit audit only on material transitions. Heartbeat/liveness belongs in one replaceable status event (e.g. NIP-38 or 30900 `status`).

**B-17. The full DNS endpoint set is recomputed from Postgres on ~40 bus event types, including every reconcile tick** — MEDIUM — CONFIRMED
- Evidence: `shouldRefreshDNSProjection` (`projector.go:2189-2207`) includes `EventReconcileCompleted`, runtime observations and LLM/ML events. Each one calls `publishDNSEndpointSnapshot` (`:1560-1625`) → `DNSProjector.ListDNSEndpoints`, which reads all services, environments, states, workers, LLM route states and ML states (`internal/reconcile/dns_projector.go:259-645`).
- Why it's an anti-pattern: O(fleet) DB reads per event, plus republish-as-repair.
- Fix: derive DNS endpoints incrementally from the subscribed state events that changed.

**B-18. DNS tombstones are published on the legacy kind and never replace the live 30900 coordinate** — HIGH — CONFIRMED (bug)
- Evidence:
  - Live DNS endpoint/zone/backend/policy state goes through `publishReplaceableJSON`, which rewrites kind 31976/… to `KindCASControlState` (30900) plus a `legacy_kind` tag (`projector.go:1457-1467`; mapping `:1505-1512`).
  - The tombstones call `publishSigned(KindDNSEndpointState|KindDNSZoneState|KindDNSBackendState|KindDNSPolicyState, …)` directly (`:1632-1640`, `:1704-1709`, `:1792-1797`, `:2041-2049`), with `canonicalKind` being identity (`canonical_helpers.go:57-59`). They are signed on the *legacy* kind.
  - `hydrateDNSPublishedCache` reads `ListByKind(KindDNSEndpointState=31976)` (`:2072`), but live events are recorded under 30900. After a restart the cache therefore sees no live endpoints, so endpoints removed while the daemon was down are never tombstoned.
- Why it matters: deleted DNS endpoints stay `deleted:false` on relays indefinitely. Any consumer of 30900 (DNS agent, web) serves stale records.
- Fix: route tombstones through the same kind/d mapping as live state (or NIP-09 deletion of the `a` address). Add a test asserting the tombstone's `(kind, pubkey, d)` equals the live coordinate.

**B-19. Service-state tombstones use a different `d` from live state** — HIGH — CONFIRMED (bug)
- Evidence: `publishState` uses `d = "service:<sid>:environment:<eid>"` (`projector.go:~3406`). `publishStateTombstone` uses `d = "service:<sid>"` (`:3487-3496`).
- Why it matters: a removed service/environment state stays "live" on relays forever.
- Fix: same as B-18.

**B-20. Snapshot repair never tombstones removed rows (except DNS); deletes rely on a single in-process event** — MEDIUM — CONFIRMED
- Evidence: `RepublishSnapshot` only publishes rows that exist (`:425-445` and onwards). Service/environment deletes are tombstoned only in `handleEvent` (`:568-577`), and the result is discarded (`_ = p.publish…`). If the bus event is missed (projector tier-gated, `ErrProjectorBackoff` at `projection_dedupe.go:411-414`, crash between the DB delete and the bus publish), the coordinate stays live permanently.
- Fix: in the target model deletion is the client's NIP-09 or `deleted:true` event, so there is nothing to derive. In the interim, diff the snapshot against the stored latest outputs, as DNS does.

**B-21. Projection errors are swallowed; the 10-minute periodic republish is the only retry** — MEDIUM — CONFIRMED
- Evidence: `handleEvent` has `_ = p.publishBuildRegistry(…)`, `_ = p.publishArtifactRegistry(…)`, `_ = p.publishDeploymentIntentRegistry(…)`, `_ = p.publishServiceRegistry(…)` etc. (`projector.go:548-577`). Backoff errors are returned and ignored (`projection_dedupe.go:411-414`), and `RepublishSnapshot` is the documented repair path.
- Why it's an anti-pattern: periodic republish is used as a correctness mechanism.
- Fix: an explicit, bounded retry for the specific failed coordinate, confirmed by relay OK.

**B-22. Projector bus subscriptions are registered even when the projector runner is tier-gated** — LOW — PLAUSIBLE
- Evidence: `nostrProjector.SetupSubscriptions(publisher)` is called unconditionally (`app.go:~915`); only `RegisterWithOptions(…, Tier2)` is gated (`:917`). With no DB, any bus event reaching `handleEvent` calls `p.source` (a `RegistryService` over nil repos).
- Fix: n/a in the target model. In the interim, gate the subscriptions with the runner.

**B-23. Workers and reconcilers poll Postgres on tickers instead of reacting to events** — MEDIUM (aggregate) — CONFIRMED (ticker list spot-checked; full inventory by explore agent, 80 sites)

| file:line | interval (default) | polls / does | DB | publishes |
|---|---|---|---|---|
| `internal/adapters/nostr/projector.go:395` | 10 min hardcoded | full DB→relay republish (B-1) | Y | Y |
| `internal/adapters/nostr/publisher.go:382` | 1 s idle, backoff on failure | `nostr_events` outbox drain | Y | Y |
| `internal/reconcile/reconciler.go:118` | 60 s | `state.ListDueForObservation`, observe runtime, drift, auto-remediate | Y | Y |
| `internal/reconcile/dns_reconciler.go:123` (+debounce `:146`) | 30 s | DB zones/overrides vs backend | Y | Y |
| `internal/reconcile/llm_reconciler.go:52` | 60 s | all LLM route states vs gateway | Y | Y |
| `internal/reconcile/hygiene_reconciler.go:155` | 30 min | worker hygiene observations → maintenance commands | N | Y |
| `internal/reconcile/execution_plane.go:180` | per plane, 30 s | capability probe | Y | Y |
| `internal/workflow/stale_run_detector.go:89` | ≤1 min | `ListNonTerminal` runs + `nostr_events` job status | Y | Y |
| `internal/service/route_canary.go:232` | 60 s | managed routes, probe, upsert canary rows | Y | Y |
| `internal/service/managed_instance_supervisor.go:146` | 30 s | DB specs, memory/health, force-restart | Y | Y |
| `internal/service/security_scheduler.go:88` | 1 h | leased DB scan schedules | Y | N |
| `internal/service/tool_provisioning_coordinator.go:82` | 30 s | non-terminal tool intents (recovery poll) | Y | Y |
| `internal/service/llm_provisioning_coordinator.go:137` | 30 s | LLM intents/runs (recovery poll) | Y | Y |
| `internal/service/backup_run_coordinator.go:184` / `:550` | 30 s / heartbeat 30 s–5 min | queued backup runs / row touch | Y | Y/N |
| `internal/service/backup_restore_coordinator.go:139` / `:407` | 30 s / heartbeat | restore runs / row touch | Y | Y/N |
| `internal/service/backup_retention_coordinator.go:109` / `:326` | 30 s / heartbeat | retention runs / row touch | Y | Y/N |
| `internal/service/failover_trigger_engine.go:120` | 1 min | in-memory heartbeat snapshots | N | Y |
| `internal/app/background.go:79` | 30 s | HiveCI pending results retry | Y | N |
| `internal/app/background.go:158` | 24 h | OCI upload expiry | Y | N |
| `internal/app/background.go:204` | 1 h | OSV cache prune | Y | N |
| `internal/app/background.go:256` | 1 h | ContextVM response prune (24 h retention) | Y | N |
| `internal/app/background.go:453` | 5 min | due backup schedules | Y | N |
| `internal/app/db_recovery.go:33` | 30 s | DB probe → process restart | Y | N |
| `internal/app/nostr_transport_metrics.go:44` | 15 s | outbox depth / storage stats | Y | N |
| `internal/relaysidecar/server.go:364` | 15 min | sidecar retention sweep | Y | N |
| `internal/controlplane/reactor.go:2415` | 10 min | prune in-memory `r.runs` (run state lost on restart) | N | N |
| `internal/service/ml_recipe_coordinator.go:213`, `ml_inference_provisioning_coordinator.go:218` | 30 s | **never constructed in `app.go`** (dead) | Y | Y |

Bounded probes and backoff/reconnect timers (bootstrap retries, Signet heartbeat, rollout health gate, runtime `Observe` loops, Pulp/Qdrant/Cloudflare/Blossom retries, relay reconnect backoffs) are legitimate and not listed.
- Why it's an anti-pattern: desired state and work queues live in DB rows discovered by polling. The "recovery poll" pattern exists because the trigger (an in-process bus event) is not durable. Polling *external runtimes* (Docker/K8s/gateway observation) is legitimate; polling Postgres for *desired state or work* is not.
- Fix: the work item is an event on the relay (intent/run/command). The daemon keeps a persistent subscription and a local index. Restart recovery is "replay my inputs since cursor", not "SELECT non-terminal rows every 30 s".

**B-24. Implemented but never-called projections and coordinators** — LOW — CONFIRMED
- Evidence:
  - `Projector.PublishAgentRuntimeRelease` / `PublishAgentServiceReleaseBinding` (`agent_runtime_release_projection.go:14-60`) have no non-test callers, so agent runtime releases (`pg_agent_runtime_release`) are DB-only.
  - `MLRecipeCoordinator` and `MLInferenceProvisioningCoordinator` are never constructed.
- Fix: wire or delete them, and add an "unwired implementation" lint to CI.

**B-25. ContextVM (25910 / gift-wrap) mutations are ephemeral RPC: 2-minute replay window, DB write, DB-row reply** — HIGH — CONFIRMED
- Evidence:
  - `encryptedRequestReplayLookback = 2 * time.Minute` (`internal/controlplane/encrypted_transport.go:93`). The request filters use `Since: now-2m` (`:606-625`), with a 12 h outer window for NIP-59 whose inner is still gated at 2 min. Requests sent while the daemon is down for more than 2 minutes are silently lost.
  - The handlers:
    - `CreateService` builds the domain object, calls `h.registry.CreateService` (relay-first wrapper or plain DB) and returns `{"status":"created","service": svc}` (`encrypted_route_handlers.go:356-421`).
    - `CreateEnvironment` works the same way (`:578-635`).
    - Secrets create/update/delete/reveal are DB-only (`:958-1141`).
    - Notification encrypted handlers are DB-only (`notification_encrypted_handlers.go`).
  - Idempotency comes from the Postgres `ContextVMResponseStore` (24 h retention, `app/background.go:224`) plus an in-memory LRU (`encrypted_transport.go:320-330`). Without the DB, a restart inside the replay window re-executes mutations.
  - The reply payload is the DB row, which clients use as the new truth. The canonical 30900 event arrives later from the projector (B-5).
- Why it's an anti-pattern: this is REST over Nostr. The durable input (desired state) is not an event the daemon can re-read, and the ack is treated as truth.
- Fix: clients publish signed addressable desired-state events (e.g. 30900 `domain=service`, `d=<id>`). The daemon subscribes with a persisted cursor and publishes a status event referencing the input event id. ContextVM stays only for genuine RPC (log fetch, secret reveal).

**B-26. REST surface: about 70 Postgres-backed reads and about 37 mutations that bypass Nostr** — MEDIUM — CONFIRMED
- Evidence (`internal/api/router/router.go`):
  - Essential HTTP boundary: `/health` (`:124`), `/ready` (`:136`), `/metrics` (`:155-157`), `/v2` OCI registry (`:212`), Blossom blob download (`:419`), live log SSE (`:274`), deployment-run log fetch (`:269`).
  - Nostr-replaceable reads (all read Postgres via repos): orgs/members/invites (`:232-236`), services/environments/builds/artifacts/runtime-releases (`:240-258`), intents/runs (`:262-267`), state/drift (`:278-281`), instance health (`:286-289`), route canaries (`:295-297`), ML (`:305-313`), LLM (`:318-329`), workers (`:335-337`), payments (`:343-344`), config-fabric drift (`:350`), policies (`:356-357`), SBOM (`:364-366`), signatures (`:373-376`), secrets list (`:382`), notifications (`:389-391`), tools (`:397-400`), SoulFactory runtimes (`:406`), Blossom admin (`:412-415`).
  - Mutations bypassing Nostr:
    - Orgs, 8 routes, **not tier-gated** (`:429-436`).
    - Maintenance set/clear (`:442-443`).
    - `POST /builds`, `PATCH /builds/{id}/status` (`:447-448`).
    - `POST /deployments/runs`, `.../complete` (`:472-473`).
    - `PUT /llm/routes/{id}` (`:468`).
    - SBOM ingest (`:484`).
    - Signature verify (`:490`).
    - Secrets create/update/delete, **not tier-gated** (`:500-502`).
    - Notification channels CRUD and test (`:509-512`).
    - SoulFactory legacy reconciliation preview/apply (`:517-518`).
    - Tools denylist add/remove (`:524-525`).
    - MCP JSON-RPC (`:216`, `:529`).
    - Payments estimate is a compute-only POST (`:478`).
  - Config-fabric (`:453-454`) and ML (`:459-462`) POSTs publish Nostr commands. They are HTTP facades over Nostr, removable.
  - Consumers: `pkg/client/client.go:473-885` (the CLI) calls services, environments, state, drift, runs, workers, logs, config-fabric, policies, secrets and orgs over REST. There is no literal `/api/v1/` in `web/src` (investigator A owns confirming indirect use).
- Fix: keep the essential boundary. Serve reads from relay subscriptions (or a local-store REQ endpoint). Replace mutations with signed events. Migrate `pkg/client` to the Nostr client.

**B-27. DB-only entities with no event path at all** — MEDIUM — CONFIRMED
- Evidence:
  - `internal/api/handlers/tenants.go` (12 repo writes, 0 publishes), `secrets.go` (3/0), `notifications.go` (5/0), `tools.go` (2/0), `internal/service/payments.go` (3/0), `internal/controlplane/notification_encrypted_handlers.go` (3/0).
  - Secrets are encrypted with the daemon's Nostr key but stored only in Postgres (`app.go:~364-371`, `pg_secret`).
- Why it's an anti-pattern: these can never be reconstructed from relays, and they force a REST/RPC client (H7 soft dependency).
- Fix: orgs map to NIP-29 group or addressable membership events; secrets to NIP-44 encrypted addressable events addressed to the daemon; notification channels to addressable config events.

**B-28. The startup migration runner scans `nostr_events` and re-signs/republishes legacy events before bootstrap** — LOW — CONFIRMED
- Evidence: `internal/nostrmigration/runner.go:95-190`, `:296-303` (`SignEventWithHexKey` then `PublishMigrationEvent`), ordered *before* the bootstrapper in `orderedStartupRunner` (`app.go:689-698`). It is cursor-bounded, but it is DB→relay re-signing on the boot critical path.
- Fix: run it once as an offline tool (it already exists as `cmd/bahia-migrate`) and remove it from startup.

**B-29. Run and continuity state is kept in process memory and lost on restart** — LOW — CONFIRMED
- Evidence: Reactor `r.runs` map, pruned hourly (`reactor.go:2413-2433`). Continuity definition, heartbeat and status stores are `NewInMemory…` (`app.go:137-139`). `coord.RecoverNonTerminalRuns` re-derives from Postgres at startup (`app.go:469-473`).
- Fix: these are exactly the addressable status events the daemon should publish, and should recover from its own output events.

**B-30. Doc and comment drift that re-seeds the anti-pattern** — LOW — CONFIRMED
- Evidence: `projector.go:170` ("authoritative DB state"), `app.go:326-331` (the relay-first comment contradicts its condition), `app.go:172-174` (nil-repo safety relies on gating that B-10 defeats), and `docs/architecture.md:215-224` (already noted in B1).
- Fix: rewrite them with the target-model invariant and add an architecture test (see B-23 and B-24 fixes).

### Entity authority table

Kinds: 30900 = `CAS_CP_STATE` (replaceable control state, `legacy_kind` tag), 4903 = `CAS_AUDIT`, 310xx = legacy `Publisher` kinds.

| Entity | Canonical store today | Kinds emitted | Projection trigger | Target |
|---|---|---|---|---|
| Services | Postgres (`pg_service`); optional relay-first pre-publish | 30900 (×2 writers, B-5), 4903 | bus `EventService*` + 10 min snapshot | client-signed 30900 desired state; daemon publishes status only |
| Environments (+deployment units) | Postgres (`pg_environment`, `pg_deployment_unit`); optional relay-first | 30900 (×2), 4903 | bus + snapshot | client-signed 30900 |
| Env/service state, drift | Postgres (`pg_state`, `pg_observation`) | 30900 `service/state`, 4903 observation/reconcile | bus + snapshot | daemon-owned 30900 status, emitted on material change only |
| Builds | Postgres (`pg_build`) via REST/ContextVM | 31000 (legacy), 30900, 4903 | bus + snapshot | CI-signed build event (HiveCI result) |
| Artifacts | Postgres (`pg_artifact`) | 31001 (legacy), 30900, 4903 | bus + snapshot | signed artifact event |
| Deployment intents / runs | Postgres (`pg_deployment`) | 31002/31003 (legacy), 30900, 4903 | bus + snapshot; stale-run ticker | client-signed intent; daemon run-status events |
| Policies | Postgres (`pg_policy`); signer-first commands mutate DB | 30900, 4903 | snapshot | client-signed 30900 |
| LLM routes / releases / state | Postgres (`pg_llm_*`); REST PUT + commands | 30900, 4903 | bus + snapshot; 60 s reconciler | client-signed 30900 |
| ML models / versions / endpoints | Postgres (`pg_ml`) | 30900, 4903 | bus + snapshot | client-signed 30900 |
| Workers | Postgres (`pg_worker`), fed from Loom adverts | 30900 worker state/assignment/drain | snapshot | the Loom advert *is* canonical; no re-projection |
| Backups (recipe/policy/repo/run/restore/retention) | Postgres (`pg_backup_controlplane`) | 30900, 4903 | bus + snapshot; 30 s recovery polls | client-signed config; daemon run-status events |
| DNS zones / endpoints / backends / policies | Postgres + config; endpoints derived from DB | 30900 live, **legacy-kind tombstones** (B-18) | ~40 bus types + snapshot; 30 s reconciler | derived incrementally from state events |
| SBOM / signatures / security | Postgres (`pg_sbom`, `pg_signature`, `pg_security`) | 30900 SBOM refs | snapshot | signed attestation events |
| Package repos / artifacts / promotions | Postgres (`pg_package_*`) | 30900 (mapped) | via commands | client-signed 30900 |
| Agent runtime releases / bindings | Postgres only | none (projection unwired, B-24) | — | signed release events |
| Souls (SoulFactory) | relay bus + local Signet files; service/env via DB-first registry | Soul protocol kinds | reactor (Tier2) | as-is for Soul kinds; registry via events |
| Config fabric | relays (publishes commands) | config-fabric kinds | REST facade | relay-canonical (drop REST) |
| Orgs / members / invites | **Postgres only** | none | — | NIP-29 or addressable membership |
| Secrets | **Postgres only** (Nostr-key-encrypted) | none | — | NIP-44 encrypted addressable |
| Notification channels / log | **Postgres only** | none | — | addressable config; log = events |
| Payments | **Postgres only** | none | — | Cashu/zap receipts |
| Tool provisioning / denylist | Postgres (`pg_tool_*`) | responder replies | 30 s recovery poll | client-signed request/denylist events |
| Route canaries / instance health | Postgres (`pg_route_canary`, `pg_managed_instance_health`) | separate projectors | supervisor tickers | daemon status events |
| ContextVM responses | Postgres (`pg_contextvm_response`, 24 h) | 25910 replies | — | not needed once mutations are events |
| All Nostr traffic | Postgres `nostr_events` (outbox + audit, ~19 GB) | — | — | embedded event store with replaceable collapse |

### Startup: as-is vs to-be

**As-is** (`internal/app/app.go` `New`):
1. Connect relays.
2. `connectOptionalDatabase`. If it fails, set Tier1, leave all repos nil, and use the in-memory `nostr_events`.
3. Construct DB-first services and wrap services/environments in `RelayFirstRegistry` if enabled.
4. Wire the in-process bus: services write the DB, then emit bus events, which the legacy `Publisher` and the `Projector` turn into relay events and `nostr_events` rows.
5. `coord.RecoverNonTerminalRuns` from the DB.
6. Register runners by tier.
7. Run `orderedStartupRunner`:
   - `nostrmigration` scans `nostr_events` and re-signs legacy events.
   - The bootstrapper does a one-shot EOSE fetch of 30900/NIP-38/audit, decodes 30900 with a no-op decoder, applies nothing, sets `ActiveTier` to the "ready" tier (possibly 3 over nil repos), and publishes identity/checkpoint/readiness.
8. The projector `Run` republishes the entire DB snapshot, then repeats every 10 min; dedupe is warmed from `nostr_events`.
9. Reconcilers and coordinators start polling Postgres on 1 s–1 h tickers.
10. The ContextVM transport listens with a 2-minute lookback.

Net effect: relays are a projection target. The DB is the only thing that can restart the daemon's world, and without it the daemon is effectively idle.

**To-be:**
1. Open the embedded local event store (always present; no Postgres required).
2. For each input set (client desired-state 30900 by trusted authors, Loom/HiveCI/worker events, ContextVM RPC for true RPC only), open **one long-lived REQ** per relay with `since` = persisted per-(relay, filter) cursor. On connect, run NIP-77 negentropy against the local store to fill gaps. EOSE marks "caught up" without closing.
3. Rebuild in-memory indexes from the local store, not SQL tables. An optional Postgres/SQLite index is a pure derivation that can be dropped at any time.
4. React to each *new* input event: execute (deploy/observe/DNS/backup), then publish the daemon's **own** output as an addressable status event with `created_at` derived from the transition. Skip publishing if the stored latest output for that `(kind, pubkey, d)` has equal stable content, and verify the relay OK.
5. External observation (runtime, gateway, DNS backend) may poll. It emits events only on material change.
6. No periodic republish, no outbox table and no `nostr_events` mirror. Readiness means the local store is synced for the required filters.

## Investigator Findings: C — Protocol primitives & peripheral surfaces (Go/JS relay clients, kinds, CLI, pkg/client, sidecars, relay)

_Investigator C, 2026-09-30. Read-only audit of source, with every file:line re-checked by hand. Library behaviour was checked against the pinned module `fiatjaf.com/nostr@v0.0.0-20260916040958-27e395a0f6e7` (including `khatru/`, `eventstore/` and `nip77/`). Findings that belong to A (web architecture) or B (daemon DB authority) are cross-referenced, not repeated._

### C.0 Scorecard: what the primitives already do right
These are real strengths. Keep them and do not re-litigate them.
- **Signatures are verified on every Go ingest path.** `ValidateInboundEvent` runs `CheckID()` and `VerifySignature()` (`internal/adapters/nostr/validation.go:54-59`). It is called from the subscriber, the bootstrapper, the FIPS subscriber, pkg/discovery and fipsbridge. The library also verifies by default, because `AssumeValid` is false (`fiatjaf.com/nostr/relay.go:404-406`).
- **The relay pool exposes protocol frames as channels.** `MergedSubscription` gives callers `Events`, per-relay `RelayEOSE`, `Closed` (with the reason) and an aggregate `EndOfStoredEvents` (`relay_pool.go:747-760`). The pool opens REQs with `MaxWaitForEOSE: MaxInt64`, so there is no synthetic EOSE (`relay_pool.go:404`).
- **OK frames are parsed with their machine-readable prefixes** (`auth-required:`, `rate-limited:`, `blocked:`, `duplicate:`; `relay_pool.go:367-446`).
- **Replaceable latest-wins with lowest-id tie-break is implemented correctly in four places:**
  - relay sidecar `Replace` (`internal/relaysidecar/store.go:96-128`)
  - web `replaceable.js:16-25`
  - `internal/fipsbridge/bridge.go:320-327`
  - operator relay discovery (`pkg/client/operator_discovery.go:240-246`)
- **`nostrmigration` keeps the original `created_at`** (`internal/nostrmigration/runner.go:368`), so migration does not churn timestamps.
- **Almost no sleep-based code or tests.**
  - Non-test Go under `cmd/`, `internal/` and `pkg/` has zero `time.Sleep` calls.
  - Only two sleeps exist in Go tests: `internal/relaysidecar/config_consumer_test.go:206,214`.
  - The JS `delay()` in `nip07-crypto.js:17-19` is a legitimate backoff for extension-bridge retries.

### C.1 Go relay client layer (`internal/adapters/nostr`)

**C-1. Catch-up REQs combine `since` with `limit:1000` and never page with `until`, so any gap larger than 1000 events is silently truncated.** — Severity: **high**
- Evidence:
  - `subscriber.go:186`: `backfillLimit: 1000, // Default: limit catch-up to 1000 events`
  - `subscriber.go:488-490`: `if s.backfillLimit > 0 { filter.Limit = s.backfillLimit }`
  - The same filter has `Since: since` (`subscriber.go:480`).
  - The web recovery path does the same thing: `pool-subscriptions.js:112-119` keeps the caller's `limit` and adds `since`.
- Why it's an anti-pattern: under NIP-01, relays return the *newest* `limit` events that match. After an outage longer than about 1000 events, the oldest events in the gap are never delivered. Nothing detects this, because EOSE still arrives. The comment claims the limit "prevents memory pressure", but it actually causes data loss.
- Recommended fix:
  - Page backwards: run REQ `since=cursor, limit=N`. If N events come back, re-REQ with `until=oldest.created_at` until fewer than N are returned.
  - Better, replace catch-up with a `fiatjaf.com/nostr/nip77` `NegentropySync` over the same filter (see C-17). Then gap size does not matter.

**C-2. The replay cursor is `max(created_at)` of persisted rows with a 1 s overlap. Late, backdated and future-dated events all break it.** — Severity: **high**
- Evidence:
  - `subscriber.go:495-518`: the cursor is `LatestCreatedAtForKinds(...)` and then `cursorUnix - 1`.
  - `replay_cursor.go:11` (`defaultReplayCursorOverlap = time.Second`) and `:36-61` do the same for the bootstrapper and the reactor (`internal/controlplane/reactor.go:1511`).
  - Validation accepts events up to `+10 min` in the future (`validation.go:12`). One such event moves the cursor 10 minutes ahead for its whole kind group, and everything in that window is skipped on the next reconnect.
  - The group cursor is the maximum across *all* kinds in the filter (`subscriber.go:521-531`). A busy kind therefore hides gaps in a quiet one.
  - The publisher records its own signed events in the same table *before* publishing (`publisher.go:214-229`). Self-publications therefore advance the cursor that governs foreign events.
- Why it's an anti-pattern: `created_at` is author-asserted, not relay-arrival order. Relays deliver cross-relay, propagated and NIP-59 backdated events out of order. A high-water mark on `created_at` is not a safe resume token, which is why the ecosystem uses a per-relay, per-filter cursor based on *EOSE/receipt time* plus periodic negentropy.
- Recommended fix:
  - Persist `(relay, filter-hash) → last EOSE wall-clock`. Resume with `since = cursor - skew` (a few minutes).
  - Clamp future `created_at` values before they influence any cursor.
  - Run `nip77.NegentropySync` periodically (for example every 10 min and on reconnect) to fill whatever the cursor missed.
  - Store cursors in a small local KV or in a `fiatjaf.com/nostr/eventstore` sidecar, not in Postgres `nostr_events` (B's scope).

**C-3. With an empty cursor (fresh node, DB-less start, wiped DB), subscriptions start at `since = now`, so persistent state is never backfilled.** — Severity: **high** (this directly blocks "daemon runs with relays only")
- Evidence:
  - `subscriber.go:512-514`: `if cursorUnix == 0 { return timestampFromTime(s.now()), nil }`
  - The inbound set includes long-lived replaceable and addressable state: `ConfigACLList`, `ConfigPolicy`, `KindRelaySetDiscovery`, `KindNIP65RelayList`, `SoulFactoryRuntimeCapability`, ContextVM lists (`subscriber.go:23-47`).
  - The bootstrapper falls back the same way: `liveFilters` → `since = startedAt` (`bootstrapper.go:465-470`).
  - The deprecated `Publisher.Subscribe` does the same (`publisher.go:425`).
- Why it's an anti-pattern: replaceable and addressable events *are* the state. Subscribing "from now" means a cold daemon only learns config or ACL changes made after it boots. This makes the DB cursor load-bearing, which is the opposite of the target model.
- Recommended fix:
  - For replaceable and addressable kinds, always open a no-`since` REQ that is bounded by `authors` (plus `#d` where possible). The relay keeps only the latest version per coordinate anyway.
  - For regular kinds, backfill with negentropy against a local `eventstore`.

**C-4. When one relay drops, the loss is silent: its REQ is not re-issued until *every* relay has failed. Non-AUTH `CLOSED` frames are dropped permanently.** — Severity: **medium-high**
- Evidence:
  - `activeMergedSubscription.workerDone` removes the relay group and cancels the merged subscription only when `len(s.groups) == 0` (`relay_pool.go:1112-1128`).
  - The subscriber restarts only when `merged.Events` closes (`subscriber.go:282-284`).
  - `handleRelayClosed` returns `false`, meaning "do nothing", for any reason other than `auth-required` (`subscriber.go:325-327`). This includes `rate-limited:`, `error:` and `restricted:`.
  - The same shape appears in `fips_subscriber.go:192-229`, `pkg/discovery/resolver.go:451-491`, `internal/fipsbridge/bridge.go:250-286` and `internal/controlplane/encrypted_transport.go:541-589`.
- Why it's an anti-pattern: a three-relay fleet can quietly degrade to one relay for hours, and nothing re-REQs. `RelayHealth` records the `CLOSED`, but no code acts on it.
- Recommended fix:
  - Give each relay its own resubscribe loop with backoff and its own cursor (C-2). Classify `CLOSED` prefixes: retry `rate-limited:` and `error:` with backoff, surface `restricted:` and `blocked:` as a hard failure, and AUTH on `auth-required:`.
  - Or adopt `fiatjaf.com/nostr` `Pool` (`SubscribeManyNotifyEOSE` with reconnection) instead of the hand-rolled merge.

**C-5. NIP-42 AUTH is purely reactive. The library's `AuthHandler` is never wired, and each consumer re-implements "CLOSED auth-required → AUTH → tear down all relays → back off → resubscribe".** — Severity: **medium**
- Evidence:
  - `buildRelayOptions` sets only `NoticeHandler` (`relay_pool.go:1841-1848`), although `nostr.RelayOptions.AuthHandler` exists (`fiatjaf.com/nostr/relay.go:139-140`).
  - `subscriber.go:328-341` authenticates, then returns `true`. `Run` then waits a *backoff delay* and resubscribes every relay (`subscriber.go:200-217`).
  - The same code is copied in `fips_subscriber.go:242-257`, `resolver.go:493-507`, `fipsbridge/bridge.go:288-305`, `encrypted_transport.go:560-585` and `pkg/client/operator_nostr.go:1709-1730,1772-1807`.
  - The subscribe-time AUTH retry in the pool (`relay_pool.go:709-716,1267-1276`) is effectively dead code. `Relay.Subscribe` returns an error only when the REQ cannot be written, and `auth-required` arrives later as an asynchronous `CLOSED`.
  - `authAttempted` maps allow one AUTH per relay per subscription lifetime.
- Why it's an anti-pattern: AUTH should be a connection-level response to the relay's `AUTH` challenge, followed by a re-REQ on *that* relay only. Today there are six divergent copies, and every AUTH causes a fleet-wide resubscription storm.
- Recommended fix: set `AuthHandler: func(ctx, r, ev) error { return signer.SignEvent(ctx, ev) }` in `buildRelayOptions`, and re-REQ only the relay that sent `auth-required:`. Then delete the per-consumer copies.

**C-6. A publish counts as successful when one relay says OK. The outbox then marks the event published and never delivers it to the other relays.** — Severity: **high**
- Evidence:
  - `aggregatePublishResultsError` returns `nil` when `countSuccessfulPublishResults(results) > 0` (`relay_pool.go:635-637`).
  - `publishOutboxEvent` calls `MarkPublished` when `published > 0` (`publisher.go:276-283`).
  - Edge case: with zero relays configured, `results` is empty and the aggregate error is `nil` (`relay_pool.go:636`), so `Publish` returns `(0, nil)`.
  - The Soulfactory bus is worse: see C-37.
- Why it's an anti-pattern: this is "publishing to one relay and treating it as durable". Relays that were down during the publish never get the event, and nothing repairs the divergence. Readers of those relays (browsers, agents, the DNS agent) then see different truths.
- Recommended fix:
  - Track per-relay delivery state in the outbox and keep retrying each relay until it returns OK (`true`, or `duplicate:`).
  - Alternatively, make the sidecar a hub that other relays sync from with negentropy.
  - At minimum, define a quorum (for example ≥2 relays, or "all write relays in the NIP-65 list").

**C-7. Publishing is serialized process-wide, and each publish contacts relays one at a time while holding the pool's read lock.** — Severity: **medium**
- Evidence:
  - `publisher.go:269` takes `p.publishMu.Lock()` around every publish.
  - `relay_pool.go:347-354` loops `for _, mr := range p.orderedRelaysLocked()` and calls `publishToRelayWithResult` in sequence under `p.mu.RLock()`.
  - The outbox runner polls `ListUnpublished(ctx, 100)` every `idleInterval: time.Second` (`publisher.go:157,363-392,395`). DB polling itself is B's scope.
- Why it's an anti-pattern: one slow or half-open relay (bounded only by the caller's ctx) blocks every publication in the daemon. That includes ContextVM responses, so clients hit their 30 s timeouts (C-30), which triggers retries and more load.
- Recommended fix: publish to all relays concurrently with a per-relay timeout and aggregate OKs. The library's `Pool.PublishMany` does this. Drop the global mutex, or narrow it to outbox bookkeeping.

**C-8. `RelayPool.Subscribe` is a single-relay API that picks the "first available" relay in Go map order. It has no production callers.** — Severity: **low**
- Evidence: `relay_pool.go:677-730` iterates `for _, mr := range p.relays`, which is random order, and returns the first success. A repo grep finds no non-test callers.
- Why it's an anti-pattern: it is a latent single-relay assumption waiting to be reused.
- Recommended fix: delete it.

**C-9. The bootstrapper declares a replay group complete at the *first* relay's EOSE, and its snapshot REQs have no `limit` and no pagination.** — Severity: **high** (wire-level; projector hydration semantics are B's)
- Evidence:
  - `bootstrapper.go:358-365`: `case relayEOSE, ok := <-relayEOSECh: ... return true, applied, nil`. The `defer subscription.Close()` then cancels the slower relays.
  - Snapshot filter: `Filter{Kinds: ..., Until: until}` with no `Limit` (`bootstrapper.go:456-463`). The sidecar caps unbounded queries at `MaxQueryLimit = 2000` (`internal/relaysidecar/server.go:58,101`).
  - Every boot re-downloads every snapshot group: `attemptBootstrap`, `bootstrapper.go:169-247`.
- Why it's an anti-pattern:
  - Latest-wins across relays needs every relay's answer. If the fastest relay is stale, older state is hydrated and newer versions on slower relays are dropped.
  - Unbounded filters rely on each relay's default or maximum limit, and are truncated silently.
  - A full snapshot on every boot is exactly the case negentropy exists for.
- Recommended fix:
  - Wait for EOSE from all relays, or from a quorum with a deadline, while merging by `(kind, pubkey, d)`.
  - Page snapshots with `until`.
  - Replace the snapshot with a local `eventstore` plus `nip77` reconciliation.

**C-10. NIP-11 limits are fetched but never enforced.** — Severity: **medium**
- Evidence:
  - `GetMaxLimit`, `GetMaxSubscriptions`, `SupportsNIP` and `IsAuthRequired` (`relay_pool.go:1787-1837`) have no non-test callers.
  - `FetchAllRelayInfo` is used only to *log* advisories (`internal/fipsbridge/bridge.go:207-222`, `pkg/discovery/resolver.go:319-335`).
  - The subscriber builds up to four filters per REQ (`subscriber.go:425-477`). Many consumers share the same pool, and each opens its own REQs.
- Why it's an anti-pattern: this is a relay-capability blind spot. `max_subscriptions`, `max_filters`, `max_limit` and `max_message_length` violations show up only as unexplained `CLOSED` frames or truncated results.
- Recommended fix: consult NIP-11 once per connection, clamp `limit` to `max_limit`, and cap concurrent REQs per relay by multiplexing consumers onto shared subscriptions. Also only issue NIP-77 when `supported_nips` includes 77.

**C-11. Ingest rejects events older than 365 days, which breaks long-lived replaceable state and re-publication from archives.** — Severity: **medium**
- Evidence:
  - `validation.go:13` sets `InboundEventMaxPastAge = 365 * 24 * time.Hour`, enforced at `validation.go:48-50`.
  - The sidecar rejects the same on write: `relaysidecar/policy.go:42-44`.
- Why it's an anti-pattern: a NIP-65 list, ACL, relay set or controller trust list that is untouched for a year is still the current state. Rejecting it on ingest (subscriber, bootstrapper, discovery) or on relay write erases valid state. It also prevents restoring signed history into a relay.
- Recommended fix: apply the age window only to request and command kinds, where replay protection is the goal. Never apply it to replaceable or addressable state or to archived facts.

**C-12. No Go consumer honours NIP-09 deletions or NIP-40 expiration.** — Severity: **medium**
- Evidence:
  - A grep for `expiration` in `internal/adapters/nostr`, `internal/nostrutil`, `internal/nostrarchive` and `internal/nostrmigration` finds nothing.
  - Kind 5 appears only as a migration *output* (`internal/nostrmigration/manifest.go:17,308-310`).
  - `pkg/discovery/resolver.go:517-540` and web `replaceable.js:10-14` use bespoke `deleted:true` content tombstones instead. The web side is covered by A-25.
  - The relay sidecar gets NIP-40 sweeping only from khatru's default expiration manager (`khatru/relay.go:165`). Nothing in Bahia emits `expiration` tags (see C-43).
- Why it's an anti-pattern: every downstream cache keeps deleted and expired state forever, and interop clients cannot interpret Bahia's bespoke tombstones.
- Recommended fix:
  - Subscribe to kind 5 from trusted authors. Tombstone `e` ids and `a` coordinates up to the deletion's `created_at`.
  - Sweep `expiration` locally.
  - Emit NIP-09 or NIP-40 instead of content flags. `fiatjaf.com/nostr/eventstore` wrappers and khatru handle both on the relay side.

**C-13. The FIPS subscriber has no latest-wins guard, and its filter is unbounded.** — Severity: **medium**
- Evidence:
  - `fips_subscriber.go:231-240`: the filter is `{Kinds:[37195], #d:[...]}`, with no `authors`, `since` or `limit`.
  - The allowlist is enforced locally after download (`fips_subscriber.go:296-301`).
  - `handleEvent` upserts the worker's endpoints for every valid event without comparing `created_at` or `id` (`fips_subscriber.go:258-283`).
  - Every reconnect re-downloads everything (`fips_subscriber.go:166-190`).
- Why it's an anti-pattern: an older addressable advert delivered late by a second relay overwrites newer endpoints. Author filtering should happen at the relay, not after download.
- Recommended fix: keep a `(pubkey, d) → (created_at, id)` guard, as `fipsbridge/bridge.go:320-327` already does. Put `allowedPubkeys` into `Filter.Authors`.

**C-14. Idempotency across restarts depends on Postgres. The ContextVM server transport replays a fixed 2-minute (12 h for gift wraps) window into an empty in-memory dedup cache on every start.** — Severity: **medium**
- Evidence:
  - The dedup structures are all in-memory LRUs:
    - `NewEventDeduplicator(10000)` per merged subscription (`relay_pool.go:927`) and per subscriber (`subscriber.go:185`)
    - `contextVMDedupCache` with 4096 entries (`internal/controlplane/encrypted_transport.go:319-345,445-450`)
  - The only durable gate is `s.eventRepo.Record(...)` insert-state (`subscriber.go:389-411`).
  - `EncryptedRequestTransport.Run` computes `since = now - encryptedRequestReplayLookback (2 min)` / `contextVMNIP59OuterLookback (12 h)` on every start (`encrypted_transport.go:93-94,521-523,610-627`). This is also the transport the DNS agent uses (`cmd/bahia-dns-agent/main.go:100-116`).
- Why it's an anti-pattern:
  - A restart inside the window re-executes requests unless every handler is idempotent.
  - An outage longer than 2 minutes silently drops requests.
  - Without Postgres, the daemon has no idempotency at all.
- Recommended fix: persist a processed-request set keyed by the inner event id / `d` in the same small local store as the cursors (bbolt or `eventstore`), and derive `since` from the persisted cursor instead of wall-clock minus a constant.

**C-15. Legacy and dead relay APIs remain exported, which invites regressions.** — Severity: **low**
- Evidence:
  - `mergeSubscriptions` and `mergeRelaySubscriptions` (`relay_pool.go:1335-1470`) have only test callers (`relay_pool_test.go:31-87`) and do no cross-relay dedup.
  - `SubscribeAll` is deprecated (`relay_pool.go:846-855`).
  - `Publisher.Subscribe` is deprecated and uses `since=now` (`publisher.go:418-468`).
  - `processor.go:254-282` Loom status and result handlers are no-ops, and their comments point to a `PollJobStatus` that no longer exists anywhere in the repo.
- Why it's an anti-pattern: the "purge then creep back" pattern needs dead primitives to copy from, and these provide them.
- Recommended fix: delete them, and add a lint (forbidigo) that bans `nostr.SubscriptionOptions{}` and direct `relay.Subscribe` outside the pool.

**C-16. The "audit" kinds 31000–31099 sit in the *addressable* range but are published with `d = EntityID` and `created_at = now`, so repeated audits of one entity overwrite each other.** — Severity: **medium**
- Evidence:
  - `internal/kinds/kinds.go:280-300` labels "Audit Event Kinds (31000-31099)": `BuildRegistered = 31000`, `DeploymentCreated = 31002`, `DriftDetected = 31004`, and so on.
  - `publisher.go:198-207` publishes them with `Tags{{"t", label}, {"d", e.EntityID}}` and `CreatedAt: time.Now()`, once per internal bus event (`publisher.go:170-186`).
- Why it's an anti-pattern: audit facts are regular events, while addressable events are last-write-wins state. A second `drift.detected` for the same entity erases the first one at every compliant relay. Meanwhile, each re-emission of an unchanged fact churns `created_at`.
- Recommended fix: publish audit facts on the canonical regular audit kind (`CASAudit = 4903`) and state on `30900`. Retire 31000–31099, or make the `d` unique per fact if they must stay. Do not republish when the content is unchanged.

### C.2 Relay sidecar (`cmd/relay`, `internal/relaysidecar`)

**C-17. NIP-77 negentropy is off on Bahia's own relay, and no Go or JS code uses NIP-77 as a client.** — Severity: **high** (this is the "no sync primitives" symptom)
- Evidence:
  - `internal/relaysidecar/server.go:52-120` configures the khatru relay without ever setting `relay.Negentropy = true`, although the field exists (`khatru/relay.go:118`) and the handler is wired (`khatru/handlers.go:173,360-390`).
  - NIP-11 advertises `SupportedNIPs = {1, 11, 17, 40, 42, 44, 51, 59, 65, 70}` with no 77 (`server.go:85`).
  - A repo grep for `Negentropy|nip77` in non-test Go returns nothing.
  - `fiatjaf.com/nostr/nip77` is present in the module cache for the pinned version.
- Why it's an anti-pattern: every consumer (bootstrapper C-9, subscriber C-1/C-2, FIPS C-13, OpenClaw C-38, browser A-5) must choose between a full re-sync and a lossy `since` cursor. Negentropy reconciles a set in a few round trips regardless of gap size.
- Recommended fix: set `relay.Negentropy = true` on the sidecar and add 77 to `SupportedNIPs`. Then use `nip77.NegentropySync(ctx, relayURL, filter, localStore, localStore)` in the daemon, and applesauce-relay or `@nostr-dev-kit/sync` in the browser.

**C-18. Live fan-out from the sidecar is lossy: broadcasts are dropped whenever a 256-slot queue is full.** — Severity: **high**
- Evidence:
  - `server.go:107-109`: `relay.PreventBroadcast = func(...) bool { return true }` disables khatru's own fan-out.
  - `OnEventSaved` then does `select { case broadcastCh <- event: default: logger.Warn("... dropping broadcast") }` with `broadcastCh := make(chan nostr.Event, 256)` (`server.go:143-151`).
  - The same happens for ephemeral events (`server.go:159-165`).
  - The queue is drained by 4 workers calling `ForceBroadcastEvent` (`server.go:305-321`).
- Why it's an anti-pattern: a live REQ promises that every matching event arrives, and consumers rely on that promise. Under burst load (a projector republishing, see B), subscribers silently miss events. Because cursors are `max(created_at)` (C-2), those events are never recovered. Dropped ephemeral events (`21059` gift wraps) are lost outright.
- Recommended fix: use a blocking enqueue with backpressure, or per-subscriber bounded queues that *close the slow subscriber*, which forces a re-REQ with a cursor, instead of dropping globally. Or keep khatru's native broadcast and make the publisher's OK wait-free some other way.

**C-19. Sidecar retention deletes every regular event after 7 days, while unique-`d` addressable events are never swept. The relay therefore cannot hold history, which pushes the archive into Postgres.** — Severity: **medium-high**
- Evidence:
  - The retention defaults are `EventRetention: 7 * 24 * time.Hour` and `RequestRetention: 24 * time.Hour` (`internal/config/config.go:1266-1267`).
  - `SweepRetention` deletes `kind NOT IN (...) AND replaceable_key IS NULL AND created_at < cutoff` every 15 min (`store.go:137-158`, `server.go:21,349-374`).
  - Replaceable and addressable rows are exempt, so the per-event-`d` families (C-22, C-42) grow without bound.
- Why it's an anti-pattern: "relays are the source of truth" requires at least one relay with durable history. Short retention makes Postgres `nostr_events` and `bahia-event-archive` (C-41) the real archive, which is B's symptom.
- Recommended fix: run one archival relay with no retention and an LMDB `eventstore`, and make it negentropy-enabled. Give the edge sidecar short retention *only* for request and transport kinds, and give audit facts the archival relay.

**C-20. The sidecar event store is a hand-rolled SQLite table: tag queries are full scans, deletes leave no NIP-09 tombstones, and `COUNT` scans everything.** — Severity: **medium**
- Evidence:
  - The schema indexes only `id`, `replaceable_key` and `created_at` (`store.go:49-62`).
  - `relayQuerySQL` pushes down only ids, kinds, authors, since and until (`store.go:230-293`). Every `#p`, `#d` or `#e` filter is evaluated in Go by `filter.Matches` while scanning `ORDER BY created_at DESC` (`store.go:195-225`). `Count` does the same (`store.go:160-193`).
  - `Delete` is `DELETE FROM events WHERE id = ?` (`store.go:130-135`), which leaves nothing that stops the deleted event from being re-accepted later.
  - Writes use `SetMaxOpenConns(1)` (`store.go:47`).
- Why it's an anti-pattern: ContextVM (`#p`), addressable (`#d`) and FIPS lookups are all table scans. Slow EOSE on those queries is what turns downstream timeouts into "completion".
- Recommended fix: switch to `fiatjaf.com/nostr/eventstore/lmdb` (or `boltdb`), which index tags and handle replaceable and deletion semantics. Keep only the retention policy as a wrapper.

**C-21. The sidecar's NIP-11 and access policy understate and misstate what it does.** — Severity: **low**
- Evidence:
  - `SupportedNIPs` omits 9, although khatru processes kind 5 (`khatru/deleting.go`), and omits 77 (C-17).
  - There is no read-side gate: `OnRequest` only rejects `search` (`policy.go:52-57`). All plaintext fleet state (30900 / 30078 / 31xxx) is readable by anyone who can reach the socket.
  - Writes are gated only by the NIP-86 admit list (`policy.go:45-47`, `admin.go:238-292`).
- Why it's an anti-pattern: this is a relay-capability blind spot for clients, and an undocumented confidentiality boundary.
- Recommended fix: advertise accurate NIPs. If fleet state is sensitive, require NIP-42 for reads (khatru `RejectFilter` + `GetAuthed`) and set `limitation.auth_required`.

**C-22. The sidecar config consumer emits addressable status events whose `d` embeds the desired event id and phase, and it only sees events saved to its own relay.** — Severity: **medium**
- Evidence:
  - `config_consumer.go:546-551`: `{"d", "config-status:" + service + ":" + policy + ":" + scope + ":" + desiredEventID + ":" + status}`. The code comment says this is deliberate, to avoid replacement.
  - Input arrives via `OnEventSaved` only (`server.go:144-157`), not via a subscription, so desired config published to other relays never reaches it.
  - It also uses a content-level `version` rather than `created_at` for ordering (`config_consumer.go:211-215`).
- Why it's an anti-pattern: it uses addressable kinds as an append-only log. Every desired-event × phase pair is a new coordinate, never replaced and never swept (C-19). Consumers must list every status to find the current one.
- Recommended fix: publish one addressable `config-status:<service>:<policy>:<scope>` holding the current applied/rejected state. Record history as regular audit events (4903) with an `e` tag to the desired event. Have the consumer subscribe (with a cursor) to all configured relays.

### C.3 JS relay client wire mechanics (`web/src/lib/nostr/pool*.js`, `retained-domain-subscription.js`, `replaceable.js`, `nip46*`, `nip07*`)
Covered by A and not repeated here: the in-memory-only cursor (A-5), the `limit:1000` multiplex (A-6), the ContextVM read RPCs (A-8), refresh-on-event (A-9), duplicate pools (A-13), one-shot fetches (A-15/16), NIP-09/40 (A-25), open author filters (A-26), multi-letter tags (A-27), and connected-only publish (A-33).

**C-23. Recovery re-REQ uses one `since` per relay, shared by every filter, and keeps each filter's `limit`.** — Severity: **medium**
- Evidence:
  - `pool-subscriptions.js:225-227`: `state.lastSeenCreatedAt = Math.max(..., event.created_at)` across all filters on that relay.
  - `recoveryFilters` (`:112-119`) applies `since: max(filter.since, replaySince)` to *every* filter and keeps `limit`.
  - `retained-domain-subscription.js:32-37` sets `limit: 500` on the domain filters that go through this path.
- Why it's an anti-pattern: this is C-1 and C-2 in the browser. A filter over a busy kind moves the cursor past a quiet filter's gap, and the limit truncates a large gap to its newest N events.
- Recommended fix: keep a cursor per `(relay, filter)`, page with `until` on a full page, or reconcile with negentropy (applesauce-relay).

**C-24. Every `CLOSED` frame causes an endless backoff re-REQ, including permanent policy rejections.** — Severity: **low-medium**
- Evidence:
  - `pool-subscriptions.js:67-77` special-cases only `auth-required`.
  - `onClosed` → `scheduleRetry` (`:239-241,178-212`) retries forever, up to 30 s apart, for `restricted:`, `blocked:`, `invalid:` and `error:` alike.
- Why it's an anti-pattern: this ignores the machine-readable `CLOSED` prefixes. A relay that refuses the filter gets a REQ every 30 s for the life of the page.
- Recommended fix: treat `restricted:`, `blocked:` and `invalid:` as terminal and surface them. Retry only `error:`, `rate-limited:` and transport drops.

**C-25. Production code contains a signature-verification bypass for E2E tests.** — Severity: **medium** (security hygiene)
- Evidence:
  - `pool-subscriptions.js:45`: `if (client.validateEvent && !(globalThis.__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS === true && event?.sig === '0'.repeat(128)))`.
  - `:79-83` also resurrects events that nostr-tools flagged as `oninvalidevent`.
- Why it's an anti-pattern: this is verification skipped on ingest behind a global flag that ships in the production bundle. Any script that can set a global, such as an injected extension or an XSS, can feed forged state.
- Recommended fix: have the E2E harness sign mock events with a throwaway key, or inject a mock `validateEvent` through the constructor. Strip the flag from production builds.

**C-26. Long-lived subscriptions keep unbounded `seenEvents` Sets.** — Severity: **low**
- Evidence: `pool-subscriptions.js:15,159`: `new Set()` with no eviction, held for the life of a retained subscription.
- Why it's an anti-pattern: memory grows without bound on long-open dashboards.
- Recommended fix: use a bounded LRU, or rely on the event store's id index once A's event store lands.

**C-27. "NIP-46" in the browser means probing for a non-standard `window.nostr.nip46` object, not a NIP-46 client.** — Severity: **low**
- Evidence: `nip46-core.js:20-31` checks `window?.nostr?.nip46`. `parseNostrConnectUri` (`:33-66`) parses relays, but no kind-24133 request/response over those relays is implemented here.
- Why it's an anti-pattern: this relies on an injected proprietary provider. Real remote signers (nsec.app, Amber) are unreachable, which contributes to A-2's signer wait.
- Recommended fix: use `nostr-tools/nip46` `BunkerSigner` (or `@welshman/signer`) against the URI's relays.

**C-28. The provisioning progress subscription has no `authors`, no `since`, and uses the non-recovering primitive, so results can be forged.** — Severity: **low-medium**
- Evidence:
  - `subscriptions.js:135-148`: `nostr.subscribe([{kinds:[PROVISIONING_STATUS], '#e':[id]}, {kinds:[PROVISIONING_RESULT, SOUL_ACTION_LEGACY_RESULT], '#e':[id]}])`.
  - The author is not checked in `onEvent`.
  - It still consumes a legacy result kind.
  - This is a specific instance of A-18 and A-26.
- Why it's an anti-pattern: anyone can publish `status=success` tagged with the request id.
- Recommended fix: add `authors: [factoryPubkey]`, use `subscribeWithRecovery`, and drop the legacy kind.

### C.4 CLI, `pkg/client`, `pkg/discovery`

**C-29. The CLI reads almost everything over REST from the daemon, and it *publishes config-fabric desired state* via REST POST.** — Severity: **high**
- Evidence:
  - `pkg/client/client.go:473-885` issues GETs to `/api/v1/services`, `/environments`, `/state`, `/state/drifted`, `/workers`, `/deployments/runs/*/logs`, `/policies`, `/services/*/secrets`, `/orgs` and more.
  - `cmd/cli/main.go:147-1394` wires `services list/get`, `state`, `workers`, `logs`, `policies`, `secrets` and `orgs` to these calls. `cmd/cli/environments.go:47,66` does the same for environments.
  - `PublishConfig` is `POST /api/v1/config-fabric/events`, and there are also `/drift` and `/rollback` (`client.go:736-757`, `cmd/cli/config_fabric.go:28-65`). The daemon, not the operator, ends up authoring desired-state events.
  - `cmd/cli/operator_nostr.go:660-669` still has an `--http-fallback` path for pre-acceptance failures.
- Why it's an anti-pattern: the CLI cannot work when the daemon is offline, even for state that already lives on relays (30900 service, environment, worker and DNS state). Config desired state should be a NIP-78/30078 event signed by the operator's key (NIP-07/46 or a local nsec) and published to relays. It should not be sent as an HTTP body.
- Recommended fix:
  - Read addressable state with REQ `{kinds:[30900], authors:[service], #d:[...]}` and exit on EOSE from all relays.
  - Sign `ConfigPolicy`/`ConfigACLList` events locally and publish them with OK verification.
  - Keep REST only for secret material and log streaming.

**C-30. ContextVM request/response is used for reads that have addressable projections.** — Severity: **medium-high** (the CLI and agent side of A-8)
- Evidence:
  - Read-shaped methods include `build/get` and `build/list` (DB `limit`/`offset` paging), `environment/get-details`, `settings/relay-policy.get`, `config/status`, `security/findings-list`, `security/schedules-list`, `services/secrets-list` and `deployments/run-logs-get`. They are defined in `internal/controlplane/encrypted_build_handlers.go:18-19`, `encrypted_route_handlers.go:33-44`, `relay_settings_handlers.go:23-27` and `security_handlers.go:16-19`.
  - They are called from `pkg/client/operator_nostr.go:895-939,1164-1183`.
  - Each read is a publish, then a subscribe, then a wait of up to `DefaultOperatorResultTimeout = 30s` × `DefaultOperatorResultRetries = 2` (`operator_nostr.go:25-27`).
- Why it's an anti-pattern: this is an RPC wrapper around relays. The answer comes from the daemon's DB, not from relay state, so the read fails whenever the daemon is down, even though `30900` projections exist.
- Recommended fix:
  - Classify each method. Builds, environments, config status and relay policy become REQs over addressable events. Findings and schedules become addressable per-finding or per-schedule events.
  - Keep ContextVM for secrets, logs and computed previews (`service/deploy-preview`).
  - Replace `offset` paging with `until` cursors.

**C-31. The reply-subscription "activation" treats a deadline as success.** — Severity: **low**
- Evidence: `pkg/client/operator_nostr.go:1669-1677`: on `activationCtx.Done()`, it returns success when `len(eosed) > 0 || len(active) > 0`, meaning subscribed but possibly no EOSE yet. `operatorActivationTimeout = 3 * time.Second` (`:27`).
- Why it's an anti-pattern: a timeout stands in for protocol completion. On a slow relay the request is published before the reply REQ is live. The reply still arrives because the relay stores it, but only if the reply kind is not ephemeral.
- Recommended fix: publish only after EOSE from at least one relay, and fail loudly otherwise. Also confirm the reply kinds are stored, not ephemeral.

**C-32. `pkg/discovery.Resolver` keeps the first arrival on equal `created_at`, holds state only in memory, and re-downloads everything on every reconnect.** — Severity: **low**
- Evidence:
  - `resolver.go:531`: `if ok && current.createdAt >= event.CreatedAt { return nil }` has no lowest-id tie-break.
  - The filter has no `since` (`:509-515`).
  - Tombstones are bespoke content flags (C-12).
  - Only `MaxLimit` from NIP-11 is logged (`:343-380`).
- Why it's an anti-pattern: tie-breaking depends on arrival order. The full resync is small today, but the pattern gets copied.
- Recommended fix: add the NIP-01 lowest-id tie-break. Persist the latest `(d → created_at, id)` if restarts matter.

**C-33. No outbox (NIP-65) routing exists anywhere in the daemon, CLI or discovery. Operator discovery relies on fixed-`d` NIP-51 relay sets.** — Severity: **low-medium**
- Evidence:
  - The projector *publishes* a NIP-65 list (`internal/adapters/nostr/projector.go:2449-2457`) and the subscriber ingests kind 10002 (`subscriber.go:37`).
  - No Go reader routes REQs or publishes by it. Only `internal/soulfactory/concord_inbox.go:65` and `runtime_adapter.go:482` read NIP-65.
  - Relay lists are static config/env (`cmd/cli/operator_nostr.go:560-598`), or `30002` sets with `d ∈ {bahia-contextvm-v1, bahia-browser-v1}` (`pkg/client/operator_discovery.go:111-117`).
- Why it's an anti-pattern: relay topology is a single configuration assumption. It cannot follow a service that moves relays, or reach peers who publish elsewhere.
- Recommended fix: resolve `10002` for the service and operator pubkeys, write to their write-relays, and read from their read-relays. Keep the `30002` sets as hints.

### C.5 Peripheral binaries

**C-34. The DNS agent is a ContextVM RPC server. The daemon pushes whole zones to it and asks for health synchronously while building read models.** — Severity: **medium-high**
- Evidence:
  - `internal/dnsagent/protocol/protocol.go:11-17` defines `dns-agent/health`, `dns-agent/list` and `dns-agent/sync`. `SyncParams` carries the full `Records []DNSRecord` plus a serial.
  - The backend calls them through `ContextVMRequestClient` (`internal/adapters/dns/dnsmasq_agent.go:96-120`).
  - `backend.Health(ctx)` is invoked inline for each backend while assembling `DNSBackendState` (`internal/app/app.go:2685-2697`), costing one RPC round trip, up to 30 s × 3, per backend per projection.
  - The agent serves requests via `EncryptedRequestTransport` with the fixed 2-minute lookback (C-14).
- Why it's an anti-pattern: this is a queue/RPC wrapper around relays, and health is fetched by polling. The desired zone state already exists as addressable `DNSZoneState` and `DNSEndpointState` events.
- Recommended fix:
  - The agent subscribes to `DNSZoneState` for its allowed zones (authors = daemon), applies latest-wins, and publishes its applied serial and health as its own addressable NIP-38 status with a NIP-40 `expiration`.
  - The daemon reads health from that event.
  - `sync` disappears. Idempotency comes from `(zone, created_at)`.

**C-35. `SoulFactoryRelayBus` is a second, independent relay client stack. It keeps one OK and cancels every other relay's publish, it uses a synthetic 7-second EOSE, and it polls for the AUTH challenge.** — Severity: **high**
- Evidence:
  - `internal/soulfactory/relay_bus.go:153-157`: `if result.Accepted { cancel(); return 1, nil }` cancels the in-flight publishes to all other relays.
  - `relay_bus.go:548`: `relay.Subscribe(ctx, filter, nostr.SubscriptionOptions{})`. With `MaxWaitForEOSE == 0` the library substitutes 7 s and dispatches a fake EOSE when the timer fires (`fiatjaf.com/nostr/relay.go:654-663`).
  - `Query()` (`relay_bus.go:266-300`) therefore completes on that timeout. It is used for authorization checks in `reactor.go:390-425`, and also at `reactor.go:768`, `concord_inbox.go:39` and `communikeys_membership.go:270,296`.
  - `Auth` polls `relay.Auth` every 50 ms for up to 2 s waiting for a challenge (`relay_bus.go:578-592`).
  - `RelayBusSubscription` exposes no `Closed` channel, so consumers never see `CLOSED` frames.
- Why it's an anti-pattern: this single component hits four smells: one-relay durability, timeout-as-completion, sleep-polling, and ignored `CLOSED`. It is also a parallel implementation of `RelayPool`, so fixes to the pool never reach it.
- Recommended fix: delete it and use the shared pool (after the C-4/C-5/C-6 fixes). If it must stay, set `MaxWaitForEOSE: math.MaxInt64`, publish to all relays, use `AuthHandler`, and expose `CLOSED`.

**C-36. The OpenClaw SoulFactory sidecar replays all history on every (re)start.** — Severity: **medium**
- Evidence:
  - `internal/soulfactory/openclaw_sidecar.go:429-455` builds three filters: runtime control requests (`#p`/`#schema`/`#method`), the controller trust list (`#d`), and ContextVM grant and revoke intents.
  - None has `since` or `limit`. `authors` is omitted on purpose (comment at `:426-428`), so trust is enforced locally.
  - It runs over `SoulFactoryRelayBus` (C-35), so its EOSE can be the synthetic 7 s one.
- Why it's an anti-pattern: this is a full resync plus local filtering. Every historical control request is re-evaluated on restart, which relies entirely on handler idempotency.
- Recommended fix: persist a per-filter cursor and processed-request ids (C-14). Put known controller pubkeys in `authors`, re-REQ when the trust list changes, and add negentropy for the control kinds.

**C-37. The FIPS bridge rewrites the hosts file for every backfill event before EOSE, starting from an empty map.** — Severity: **low-medium**
- Evidence: `internal/fipsbridge/bridge.go:308-360` calls `b.writer.Write(ctx, b.entries)` for each changed event. `consume` (`:250-286`) does not gate on `EndOfStoredEvents`, and `entries` starts empty on every process start. Its latest-wins guard (`:320-327`) is correct.
- Why it's an anti-pattern: this is "no EOSE-aware backfill then live". At startup the managed hosts section briefly contains a subset of endpoints, and the file is rewritten N times.
- Recommended fix: buffer until aggregate EOSE, write once, then write per live event. Optionally seed from the existing file.

**C-38. The MCP server reads from Postgres repositories while its writes are signed relay requests.** — Severity: **low-medium** (B owns DB authority)
- Evidence:
  - `internal/mcp/server.go:32-59` shows reads backed by `repository.*` and `service.*Registry` (for example `s.registry.GetService` at `:1868` and `GetBuild` at `:1905`).
  - Writes go through `Publish*Request` (`agent_async_tools.go:76-190`).
  - Receipts put the *count* of relays into a relay list: `PublishedRelays: []string{fmt.Sprint(r.PublishedRelays)}` (`agent_async_tools.go:199-205`).
- Why it's an anti-pattern: agents see DB state that may diverge from relay state, and the receipt misreports where the event lives.
- Recommended fix: serve MCP resources from the same local event store and addressable projections as everyone else, and return the actual relay URLs that sent OK.

**C-39. `bahia-event-archive` archives and restores *Postgres rows*, not relay events.** — Severity: **medium** (architecture)
- Evidence: `cmd/bahia-event-archive/main.go:1-3` calls itself the "two-phase PostgreSQL Nostr event archive". It uses `repository.NewPgNostrEventArchiveRepository` with claim, export, prune and restore steps (`main.go:77-152`), and `internal/nostrarchive/archive.go:130-168` restores into the DB.
- Why it's an anti-pattern: this exists because the relay keeps 7 days (C-19) and Postgres is the de-facto event log. Restored events never go back to relays.
- Recommended fix: once an archival relay exists (C-19), archive by REQ/negentropy from relays to NDJSON and restore by re-publishing (after the C-11 age-window fix). Postgres then needs no archive.

### C.6 Kind and event design (`internal/kinds`, `kinds.gen.js`, `docs/event-spec.md`, `docs/nostr-event-implementation-guide.md`)
Legacy regular-kind request, status and result families (`5941–6006`, `6941–6997`, `7941–7997`, `internal/kinds/kinds.go:29-127`) are already fenced by `policy.go` and the docs. They are not repeated here, apart from the residual risk in C-44.

**C-40. Addressable coordinates are Postgres UUIDs, so entity creation needs the DB.** — Severity: **medium** (links to B)
- Evidence: `docs/event-spec.md:16` specifies `d=<resource-prefix>:<uuid>`, with `service:<service-uuid>:<environment-uuid>` and `runtime-release:<uuid>` elsewhere in the same spec. The CLI also validates UUIDs before any read (`pkg/client/operator_nostr.go:897,917`).
- Why it's an anti-pattern: the coordinate that relays key on is minted by the database. A relay-only daemon or a browser cannot create or address an entity without the DB having created it first.
- Recommended fix: mint the `d` value on the author side, either deterministically from the natural key (for example `service:<slug>`) or as a client-generated ULID in the signed create intent. Treat the DB id as a cache key.

**C-41. `AssistantTranscript` reuses the upstream heartbeat kind, stores append-only messages as addressable events, and has a non-deterministic `d`.** — Severity: **medium**
- Evidence:
  - `kinds.go:161`: `AssistantTranscript = cascadia.CAS_AGENT_HEARTBEAT` (30316).
  - `internal/service/assistant_transcript_store.go:570-582` builds `d = <session>:<seq>:<turn>:<logical_id or uuid.NewString()>`.
- Why it's an anti-pattern:
  - A retry without `logical_id` mints a new coordinate, so duplicate transcript entries are not idempotent.
  - Append-only messages as addressable events never collapse, and they escape sidecar retention (C-19).
  - Another producer's heartbeat semantics share the same kind number.
- Recommended fix: use a regular kind for transcript messages, or a deterministic `d` from `(session, seq)` if addressability is needed. Stop overloading 30316.

**C-42. Heartbeats and status go on addressable NIP-38 (`30315`) with a custom `expires_after_ms` tag instead of NIP-40 `expiration`, and many schemas are multiplexed onto `30315`/`30900`/`30078` using `#domain`/`#schema` tags.** — Severity: **low-medium**
- Evidence:
  - `internal/adapters/nostr/continuity_serialization.go:355-373` writes `{"d","continuity:heartbeat:"+worker}` and `{"expires_after_ms", ...}`, read back at `:406`.
  - `kinds.go:240-243` sets `HeartbeatObservation = NIP38Status`.
  - The docs label the `30315` agent-runtime-release schema as "control state" (`docs/event-spec.md:430`), although `CASControlState` is `30900`.
  - Multi-letter tag filtering is A-27.
- Why it's an anti-pattern: relays and generic clients cannot expire a heartbeat, so stale workers look alive until a Bahia-specific sweep runs. Heavy multiplexing on a few kinds forces broad REQs followed by local filtering by schema.
- Recommended fix: add `["expiration", now+ttl]` (NIP-40). For high-rate liveness, consider ephemeral `2xxxx` together with an addressable "last seen" that is refreshed rarely. Fix the docs wording.

**C-43. Kinds drift between Go and JS: `WorkerState*` are 32000–32003 in Go but 30900 in JS, reconciled only by a test override table.** — Severity: **low**
- Evidence:
  - `internal/kinds/kinds.go:404-407` defines `WorkerState = 32000` through `WorkerEligibilityPreview = 32003`.
  - `web/src/lib/nostr/kinds.gen.js:239` has `WORKER_STATE = 30900`.
  - `internal/kinds/generated_drift_test.go:57-59` applies `frontendCanonicalKindOverrides`.
  - The legacy collisions are noted in-code (`kinds.go:415-418`).
- Why it's an anti-pattern: the Go constants look canonical but are dead. Any new Go code that trusts them publishes on a kind nobody subscribes to.
- Recommended fix: delete or alias the Go constants to `CASControlState`. Make the generator the single source.

**C-44. Deprecated regular-kind "status" families are still live identifiers.** — Severity: **low**
- Evidence: `kinds.go:114-127` still defines `DeploymentStatus = 6961`, `ServiceStatus = 6962`, `WorkerStatus = 6997` and the rest, guarded only by `policy.go` `IsReadableKind`. The subscriber also carries a numeric-range deny list (`subscriber.go:559-566`).
- Why it's an anti-pattern: this is the regression vector. State on regular kinds accumulates forever, and a single import brings it back.
- Recommended fix: move legacy constants into `internal/nostrmigration` only, and add a `forbidigo` or `depguard` rule that bans them outside migration.

### C.7 Cross-cutting root causes seen from the primitives
1. **There is no local event store in any Go component.** Every consumer re-derives state from Postgres (subscriber, reactor, MCP) or from memory (discovery, FIPS, OpenClaw, fipsbridge). Cursors, dedup and idempotency therefore end up in Postgres or are lost on restart (C-2, C-3, C-14). The fix is `fiatjaf.com/nostr/eventstore/lmdb` per process as a rebuildable cache, fed by `nip77` (C-17).
2. **Three relay stacks exist:** `RelayPool`, `SoulFactoryRelayBus`, and N browser pools (A-13). Fixes land in one and not the others (C-35). The relay stack should be consolidated onto one pool per runtime.
3. **The relay itself is lossy and short-lived:** broadcast drops (C-18), 7-day retention (C-19), and table-scan queries (C-20). That makes Postgres the only trustworthy log, which feeds B's symptoms. The relay needs to be fixed first.
4. **Request/response over relays is the default read path** in the CLI, MCP, the DNS agent and the web app (C-29, C-30, C-34, A-8). Every reply depends on the daemon being up. Reads should become REQs against addressable projections.

## Investigation Log

### Phase 1 / 1.5 — Triage and external research
**Hypothesis:** The symptoms come from design choices, not isolated bugs: Postgres is authoritative, relays are a projection target, and the web app is a REST-shaped client.
**Findings:** Git history shows at least two purge cycles: the 2026-05-23 "reconstructible bahia" rewrite and the 2026-06-01…15 REST-removal sprint with a same-week re-add and revert. Negentropy has never been used. The web IndexedDB cache exists but stores derived collections, not events (see Background B1/B2).
**Conclusion:** Confirmed as a design-level problem. It needed investigation on three separate paths.

### Phase 2 — Context building
`context_builder` failed twice before any provider ran: first an unavailable oracle model, then a discovery agent with no tab binding. The selection was seeded by hand and discovery was handed to the pair investigators.

### Phase 3 — Three parallel pair investigators
- **A (web):** 33 findings. H1 was confirmed at the auth layer and partly disproven for the read-model layer. H2–H6 were confirmed.
- **B (daemon):** 30 findings, including five correctness bugs (B-10, B-18, B-19, B-9's dead author scoping, B-5's divergent content). H3 ("runs without the DB") was largely disproven. H7 (depends on the frontend) was disproven as a hard dependency.
- **C (protocol and peripherals):** 44 findings, plus a list of what the code already does right (C.0: consistent signature verification on ingest, OK-reason parsing, some correct latest-wins handling).

### Phase 4 — Verification by the orchestrator (direct reads, commit `1537dee3`)
| Claim | Verified evidence |
|---|---|
| A-4 cache wiped on first event | `web/src/lib/stores/collections/index.svelte.js:382` `replaceSnapshotArray(target, …)` writes arrays only. `collections/services.svelte.js:3-8` rebuilds `services` from `serviceMap`. `controlplane/events.svelte.js:253-256` calls `refreshCollections()` on every changed event. `controlplane/bootstrap.svelte.js:129-171` sets `setAllLoading(true)` and clears it only in `finally`, after `startStreamingSubscription(…, {waitForEose:true})`. |
| A-11 / A-10 plaintext ephemeral commands behind bootstrap | `stores/public-controlplane.svelte.js:59-78`: `await bootstrapControlplane()`, then `requestEncryptedResult({ kind: CONTEXTVM_MESSAGE_KIND, resultKinds:[CONTEXTVM_MESSAGE_KIND] })`. `encrypted-controlplane-transport.js:70-72` only requires the encryption signer for the gift-wrap kind. |
| B-1 projector is a DB republisher | `internal/adapters/nostr/projector.go:171-173` doc comment: "Projector republishes Bahia's authoritative DB state into canonical Nostr read models…". `:295` sets `repairInterval: 10 * time.Minute`. |
| B-8 bootstrapper applies nothing without a cache | `internal/adapters/nostr/bootstrapper.go:447-449` `if b.cache == nil { return nil }`. |
| C-18 lossy fan-out | `internal/relaysidecar/server.go:107-109` `PreventBroadcast` always returns `true`. `:142-163` uses a 256-slot `broadcastCh` and drops on `default:`, for stored *and* ephemeral events. The 64-slot config queue also drops. |
| C-19 retention / C-9 query cap | `internal/config/config.go:1266` `EventRetention: 7 * 24 * time.Hour`. `:1270` `MaxQueryLimit: 2000`. |
| C-40 d-tags are DB UUIDs | `docs/event-spec.md:16` `d=<resource-prefix>:<uuid>`. |
| C-25 signature bypass in production code | `web/src/lib/nostr/pool-subscriptions.js:45,80`, `web/src/lib/nostr/validation.js:28` (`__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS`). |
| Static deploy seed is the web's only bootstrap input | `web/src/app.html:45-47` (`__PUBLIC_BAHIA_BOOTSTRAP_RELAYS__`, `__PUBLIC_BAHIA_SERVICE_PUBKEYS__`). |
| Normative docs sanction RPC reads | `docs/architecture.md:59` says the "canonical public control-plane contract is ContextVM **CRU** (`25910`…)", which includes Read. `AGENTS.md:29` calls ContextVM "Bahia's canonical mutation intent transport". `docs/architecture.md:215-224` names PostgreSQL as the source of truth. |

### Phase 4 — Oracle synthesis
The oracle collapsed the ~107 findings into six root causes (below), found compounding failures that no single section covered, identified gaps in coverage, and proposed a dependency-ordered migration. Its new factual claims were checked (table above).

## Root Cause

Bahia is not "a Nostr app with some leftover REST". It is a **Postgres-centred CRUD service that uses relays as a replication target and ContextVM as its RPC transport**. Each piece of machinery the audit found (projector, `nostr_events` outbox, hydration, tier model, backend-gated auth, RPC reads, EOSE-gated rendering) is a workaround for one of six design decisions:

| # | Root-cause decision | Findings it generates |
|---|---|---|
| **RC-1** | **An entity does not exist until Postgres mints it.** Addressable `d` tags are DB UUIDs (`docs/event-spec.md:16`, C-40). Handlers create rows, then reply (B-25, B-6). No other tier can author state. So the web must mutate by RPC (A-10), the reply is taken as truth, a projector must re-derive the relay copy (B-1), a cold daemon cannot rebuild anything (B-8, B-11), and membership/roles are DB-only, which forces backend-gated auth (A-1, A-2, B-27). | A-1, A-2, A-8, A-10, B-1, B-5, B-6, B-8, B-11, B-25, B-27, C-29, C-30, C-40 |
| **RC-2** | **Relays are a projection target, never an input.** The flow is one-way, DB→relay (`projector.go:171-173`). Dedup memory is in `nostr_events` (B-3). Cursors are `max(created_at)` of persisted rows (C-2, B-15). Bootstrap replay is a no-op (B-8). Cold subscriptions start at `now` (C-3). Deletions have to be derived by the projector, which is where the tombstone bugs are (B-18, B-19, B-20). | B-1…B-5, B-8, B-15, B-16, B-17, B-20, B-21, C-2, C-3, C-9, C-13, C-32, C-36, C-39 |
| **RC-3** | **No local event store in any process, browser or Go.** Every consumer has to re-answer "am I caught up?" and "have I seen this?" from scratch. The ad-hoc answers are row-derived cursors, Postgres-insert dedup, unbounded Sets/LRUs, timeouts treated as completion, EOSE-gated rendering, and one-shot re-fetches. Without a store, negentropy, per-relay cursors and NIP-09/40 handling cannot be implemented. | A-3…A-7, A-15…A-19, A-22, A-23, A-29, B-9, B-12, B-14, B-15, C-1, C-2, C-3, C-12…C-14, C-17, C-23, C-26, C-31, C-36 |
| **RC-4** | **ContextVM 25910 went from "approved exception" to the default transport for reads and writes.** Normative docs endorse it (`AGENTS.md:29`; `docs/architecture.md:59` "ContextVM CRU"). Every web/CLI/MCP/DNS-agent interaction became publish ephemeral → wait up to 30 s → reply. This is request/response over relays, and because the kind is ephemeral it has no durability. | A-8…A-12, B-25, B-27, C-14, C-30, C-31, C-34, C-38 |
| **RC-5** | **The relay tier is treated as a lossy peripheral, not infrastructure.** Broadcast drops (C-18), 7-day retention (C-19), table-scan tag queries (C-20), NIP-77 off (C-17), and a single relay's OK counted as delivery (C-6). Since the relay can't be trusted, Postgres `nostr_events` becomes the real log (B-13, C-39), and periodic republish becomes the "repair" (B-1), which in turn causes the churn the dedup cache was built to contain (B-3, B-4). The loop reinforces itself. | B-1, B-3, B-4, B-13, C-6, C-17…C-21, C-39; feeds A-4, A-5 |
| **RC-6** | **Authorization is not event state.** Signatures are verified everywhere (C.0), but *permission* (membership, roles, trust lists, admission) is decided in the daemon or DB where relays can't see it. The results are backend-gated routes, open author filters, dead author scoping in the bootstrapper, and allowlists applied after download. | A-1, A-21, A-26, B-9, B-27, C-13, C-21, C-22, C-28 |

### Compounding failures (cross-section; no single investigator owned these)
1. **Lost-command chain (A-11 × C-18 × C-14 × B-16/B-17).** A web mutation is plaintext *ephemeral* 25910, never stored by the relay. The daemon's 2-minute replay window only covers *stored* events, so it can't recover the command even after a short blip. The sidecar's 256-slot fan-out queue drops on overflow, and the projector's own bursts fill it (per-tick audit events B-16, full-fleet DNS recomputes B-17). So the daemon's projection churn can make it miss the commands it exists to receive. The UI then times out after 30 s and the user retries. Idempotency depends on a Postgres response store that DB-less mode doesn't have.
2. **One DB blip blanks the whole stack (B-3 × B-14 × A-4).** When Postgres hiccups, projector hydration fails closed and stops projecting. Inbound handling stops because it is gated on a DB insert. Relay state then goes stale or ages out. The web app renders only after EOSE from every relay and discards its cache on the first event. Postgres is effectively a hard dependency of *what's on the relays*.
3. **State can't be deleted (B-18/B-19 × C-12 × C-19 × A-25 × B-8).** Tombstones go to the wrong kind or the wrong `d`. No tier honours NIP-09. The sidecar never sweeps addressable rows. The web ignores deletions. The bootstrapper couldn't apply one anyway. Meanwhile the projector re-asserts every live row every 10 minutes.
4. **Silent truncation on every cold start (C-1 × A-6 × C-9 × C-19).** Go uses `since`+`limit:1000` with no paging. The web puts every domain in one `limit:1000` filter. The bootstrapper's unbounded REQs are silently capped at 2000 by the sidecar (`config.go:1270`). Regular events expire after 7 days. EOSE arrives normally in every case, so nothing detects the loss.
5. **Cursor poisoning (B-13 × B-4 × C-2).** The daemon records its own freshly-timestamped outputs in `nostr_events`, and they advance the same `max(created_at)` cursor that controls replay of *foreign* inputs. What the daemon has published decides what input it can see after a restart.
6. **Opposite EOSE policies, same missing store.** The web requires EOSE from *every* relay with no deadline (A-19). The Go bootstrapper completes at the *first* relay's EOSE (C-9). With a local store, EOSE would just be a sync-status indicator.
7. **Confidentiality is unimplemented in practice.** A-11 sends "encrypted" commands in plaintext. C-21 shows the sidecar has no read auth. The 30900 fleet state is world-readable. The "encrypted sensitive domains" decision in `docs/architecture.md` is mostly not built.
8. **Fix-order dependency.** A's fix (client-signed intents, A-10) needs B's daemon to consume intents (B-25). That in turn needs C's event model to let clients mint `d` tags (C-40). The migration has to go kind model → daemon → web.

### Eliminated / narrowed hypotheses
- **"Web read-model bootstrap waits on daemon discovery"** is disproven for the read-model layer. The relay subscription needs only the static seed (`app.html:45-47`), and discovery is explicitly non-gating (`bootstrap.svelte.js:159-163`). The backend gate is at the **auth layer** (A-1, A-2), which blocks 24 route prefixes.
- **"There is no browser cache at all"** is narrowed. A cache exists (`indexeddb-cache.js`, `bc069a00`), but it holds derived snapshots with a 15-minute TTL, and a hydration bug means it is almost never displayed (A-3, A-4).
- **"Daemon depends on the web frontend"** is disproven as a hard dependency (B, H7). The soft coupling is that orgs, secrets, notifications and payments exist only in Postgres and have to be driven over REST or ContextVM.
- **"Daemon can run without Postgres"** is largely disproven. DB-less mode drops to Tier 1: projector, reconcilers, reactor, DNS, LLM, backups and SoulFactory are all off (B-11). The bootstrapper may re-raise the tier over nil repositories (B-10, plausible, not reproduced).

### Coverage gaps (likely to contain more instances; not audited end-to-end)
- SoulFactory internals beyond `relay_bus.go` (C-35) and the sidecar replay (C-36): saga orchestration, `openclawcontrol`, communikeys membership, concord inbox, Signet file handling, legacy adoption report.
- `internal/service` per entity: payments, security scheduler, route canary, managed-instance supervisor, adoption, `driftdecision`, `internal/rollout`. B-27's pattern suggests more entities with no event path.
- The assistant execution subsystem (only its transcript kind was audited, C-41), including restart behaviour.
- Web routes and stores not sampled in depth: `/settings`, `/continuity`, `/docs`, `/route-canaries`, `/widgets`, `stores/discovery.svelte.js`, the `souls` store beyond subscription shape, and SSR behaviour of `api/client.js`. Also `web/vendor/wheelhouse`.
- How the static deploy seed is generated (`deploy/`, `web/docker-entrypoint.d`, nginx). A stale seed breaks the whole client.
- Relay sidecar admin/NIP-86 surface and `relayadmin`, including read-access policy.
- The external adapters (cashu, blossom, qdrant, registry, gitea, harbor, hiveci). B-23's ticker table suggests more DB-polled integrations.
- A systematic sweep of tests that pin the current anti-patterns (C-43's override table, sleep-based sidecar tests) and of PSTF verification claims against reality (B-2).

## Recommendations

The order matters; see compounding failure #8. Each phase ends with **deletions**, because code that no longer exists can't creep back.

### Phase 0 — Invariants first (prevents a third purge/re-add cycle)
1. **Rewrite the normative docs.** In `docs/architecture.md:215-224`, the source-of-truth table becomes "relays (addressable events); Postgres = optional derived index". In `docs/architecture.md:59`, "ContextVM CRU" becomes "ContextVM for interactive RPC only (assistant, secret reveal, log fetch)". Narrow `AGENTS.md:29` the same way and state that reads are never ContextVM.
2. **Add architecture tests that would have failed on every regression:**
   - (a) Boot the daemon with no `DATABASE_URL` against a relay seeded with fixtures, and check that services, environments and DNS are visible and mutable.
   - (b) Boot the web app with the daemon's HTTP/ContextVM down, and check that protected routes render relay state.
   - (c) Remove the `frontendCanonicalKindOverrides` table so the kind-drift test (C-43) is real.
   - (d) Add lint rules (`forbidigo`/`depguard`, ESLint `no-restricted-imports`) that ban legacy kinds outside `nostrmigration` (C-44), direct `relay.Subscribe` outside the pool (C-8), and `api/client.js` imports in new code.
   - (e) Add a CI check for unwired implementations, i.e. exported symbols whose only callers are tests (B-24).
3. **Delete now:**
   - `ProjectorSource`/`CacheProjectorSource` (B-2) and the `relay_first_extended.go` wrappers (B-7).
   - `RelayPool.Subscribe` (C-8), the `mergeSubscriptions` helpers, the deprecated `Publisher.Subscribe` and its legacy-kind setup (C-15).
   - The uncalled SBOM REST methods (A-30).
   - The `__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS` bypass (`pool-subscriptions.js:45,80`, `validation.js:28`; C-25). Use a test-only injected validator instead.
   - The dead localStorage relay override (A-14).
4. **Fix the confirmed correctness bugs now.** They are independent of the refactor:
   - B-18: DNS tombstones go on the live 30900 coordinate.
   - B-19: service-state tombstone `d` must equal the live `d`.
   - B-10: never raise the tier over nil repositories.
   - B-9 / C-9: fix the bootstrapper's author-scope group names and require EOSE from all relays with paging.
   - A-4: at minimum, hydrate the Maps rather than the arrays, and render before EOSE.
   - C-18: use per-subscriber bounded queues and close slow subscribers instead of dropping globally.
   - C-6: track per-relay publish state.

### Phase 1 — Relay tier (`internal/relaysidecar`, `cmd/relay`)
5. Replace the hand-rolled SQLite store (`internal/relaysidecar/store.go`, C-20) with `fiatjaf.com/nostr/eventstore` (LMDB or bbolt), which has real tag indexes and NIP-09 semantics. Enable khatru negentropy and advertise NIPs 9, 40 and 77 in NIP-11 (C-17, C-21).
6. Set retention per kind class (C-19). Addressable and replaceable events are never swept by age (latest-wins already bounds them). Ephemeral and request kinds get short retention. Regular audit facts are durable on at least one archival relay. This lets `bahia-event-archive`'s Postgres-row archive (C-39) and the `nostr_events` table (≈19 GB) be deleted later.
7. Make fan-out lossless (C-18). A slow subscriber gets closed with `CLOSED` and re-REQs from its cursor; events are never silently dropped. Treat the 64-slot config queue the same way.
8. **Kind model fixes** (`internal/kinds`, `docs/event-spec.md`, `web/src/lib/nostr/kinds.gen.js`):
   - Clients mint deterministic `d` tags (for example `service:<org>:<slug>`, or a client-generated ULID fixed at creation) instead of DB UUIDs (C-40). **This unblocks Phase 3 and Phase 4.**
   - Move audit facts from addressable 31000–31099 to regular 4903 with correlation tags (C-16).
   - Heartbeats use a NIP-40 `expiration` tag instead of `expires_after_ms` (C-42).
   - Transcript messages go on a regular kind with a deterministic correlation id (C-41).
   - Split multiplexed `#domain`/`#schema` state onto single-letter indexed tags or distinct kinds (A-27, C-42).
   - Unify the Go/JS `WorkerState*` kinds (C-43).

### Phase 2 — Go relay-client tier (`internal/adapters/nostr`, `pkg/`, sidecars)
9. Give each process (daemon, DNS agent, fips bridge, sidecar consumers, CLI cache) a local `fiatjaf.com/nostr/eventstore` as a rebuildable cache.
   - Cursors become per-(relay, filter-hash) records in that store, replacing `replay_cursor.go`'s `max(created_at)` over Postgres (C-2, B-15).
   - On startup and after each reconnect, run `nip77.NegentropySync` for replaceable/addressable sets. Use `since`+`until` paging for regular kinds (C-1, C-3, C-23).
10. Use one relay stack. Delete `SoulFactoryRelayBus` (C-35) and move its consumers to the pool.
    - Wire `RelayOptions.AuthHandler` once and remove the six copied AUTH-retry blocks (C-5).
    - Classify `CLOSED` prefixes: retry `auth-required:`, stop on `blocked:`/`restricted:` (C-4, C-24).
    - Re-REQ per relay on individual drops (C-4).
    - Parallelize per-relay publish with an explicit quorum (C-6, C-7).
    - Enforce NIP-11 limits (C-10).
11. Remove the 365-day ingest age cap for replaceable/addressable kinds (C-11). Honour NIP-09 and NIP-40 in every consumer (C-12). Add latest-wins with id tie-break to FIPS and discovery (C-13, C-32).
12. **Delete:** the in-memory `nostr_events` fallback (B-12), the Postgres outbox drain and mark-published-on-one-OK (B-13, C-6), and `nostrmigration` on the startup path (B-28; keep `cmd/bahia-migrate` offline).

### Phase 3 — Daemon authority inversion, one vertical slice per domain
Order: services/environments (a relay-first wrapper exists; extend it to *all* mutations including delete) → DNS (also retire the agent RPC, C-34: the agent subscribes to zone state and publishes its own NIP-38 health with NIP-40) → backups/LLM/ML/packages → orgs/secrets/notifications/payments (B-27: membership as signed addressable events, secrets NIP-44-encrypted to member pubkeys, notification channels as addressable config).

13. The pattern for each slice:
    - A client-signed addressable desired-state (intent) event, with `d` = the deterministic id.
    - A long-lived daemon REQ scoped to `authors` = the trust set derived from membership events.
    - The daemon applies it to its local store/index and publishes **exactly one** status/result event referencing the intent id.
    - Publishing is skipped when the content matches the latest stored output (this replaces B-3's Postgres hydration and ends the created_at churn from B-4).
14. **Delete as each slice lands:**
    - That family's projector publish methods and its `RepublishSnapshot` leg (B-1, B-17, B-20, B-21).
    - Its reconciler ticker, replaced by a persistent subscription plus a store query (B-23).
    - Its REST routes in `internal/api/router/router.go` (B-26).
    - Its ContextVM mutation handlers (B-25).
    - Its per-bus-event audit publish (B-16).
15. When the registry domains are done, delete the tier model, `ModePolicy`, and the bootstrapper's tier logic (B-10, B-11). Readiness becomes "local store synced per filter" with real 30900 decoders (B-8, B-9). Postgres at that point is either removed or an optional query index rebuilt from the local eventstore.

### Phase 4 — Web tier (`web/src`)
16. **One pool, one persistent event store.** Recommended: **welshman** (`@welshman/net` + `@welshman/store` + repository). It is Svelte-native and replaces the pool, dedup, cursors and derived views in one package. The alternative is **applesauce-core `EventStore` + `nostr-idb`** (+ `applesauce-relay` for negentropy) if client-side NIP-77 matters more.
    - Namespace the IndexedDB by `service_pubkey`.
    - No TTL; use LRU by size.
    - Store verified events keyed by id, with `(kind,pubkey,d)` indexes and a per-filter cursor table (A-3, A-5).
17. **Boot flow (to-be):**
    1. Read the seed from `app.html`.
    2. Open IndexedDB and render every route from the store immediately.
    3. Treat a persisted, signer-verified session as authenticated at once, with roles derived from relay membership events (removes A-1 and A-2).
    4. Open per-interest, ref-counted, long-lived REQs with `since = persisted cursor − skew`, plus negentropy on reconnect.
    5. Show EOSE as a "synced" badge, never as a render gate (A-4, A-19, A-20).
    6. Batch event application per frame (A-7).
18. **Mutations:**
    - Sign a durable intent locally (`d` = idempotency key).
    - Insert it into the store as *pending*.
    - Publish through a small outbox with per-relay OK tracking and retry (A-10, A-11, A-33).
    - Let the daemon's status event move the intent to done.
    - Keep ContextVM only for the assistant, secret reveal and log fetch.
19. **Delete:**
    - `stores/collections/indexeddb-cache.js` and the snapshot half of `stores/collections/index.svelte.js`.
    - The sync machinery in `stores/controlplane/{bootstrap,connection,events}.svelte.js`.
    - The `loadX()` aliases and EOSE-gated `{#if loading…}` blocks.
    - `AuthGuard`'s backend probe, `requiresRestCompatibility`, `compatibilityPatch`, `configureBackendAuth`'s `/orgs` call, the `hydrateAuthMetadata` one-shots, and the per-query `authMetadataClient`.
    - `subscribeToDomainRefresh` / `createCoalescedRefresh` (A-9) and `retained-domain-subscription.js`.
    - The five extra pools (A-13).
    - The ad-hoc local/sessionStorage caches (A-29, A-31).
    - `lib/api/client.js`, one domain at a time and then entirely (A-30).

### Phase 5 — CLI, pkg/client, MCP
20. CLI reads become `REQ {kinds:[…], authors:[service], "#d":[…]}` against a local eventstore cache, exiting on EOSE from all relays (C-29). Config-fabric desired state becomes an operator-signed event published by the CLI itself, replacing `POST /config-fabric/events`. MCP reads come from the eventstore, not Postgres repositories (C-38).
21. **Delete:** the REST client methods for each migrated domain in `pkg/client`, then `--http-fallback`, then the daemon's remaining `/api/v1` read routes. What remains of REST should be only the genuinely HTTP-native boundaries (OCI registry, Blossom, DNS provider callbacks, NIP-98-authenticated uploads).

## Preventive Measures
- **One normative source.** Keep `docs/architecture.md` and `AGENTS.md` in line with the charter. Any doc that names Postgres as authoritative, or ContextVM as a read path, fails review. Move `docs/plans/reconstructible-bahia-2026-05-23.md`'s charter into the architecture doc so it has normative status.
- **Behavioural gates, not commit-message gates.** The DB-less daemon boot test and the daemon-offline web boot test (Recommendation 2a/2b) run in CI on every merge. The 2026-05-23 rewrite "shipped" an authority flip with zero callers (B-2) because nothing exercised it.
- **Deletion as the definition of done.** Each migration slice has to remove the projector leg, reconciler ticker, REST route and ContextVM handler for its domain in the same PR. Dead-but-exported code (B-2, B-7, C-8, C-15) was the template every regression copied.
- **Lint the anti-patterns.** Forbid `time.NewTicker` in `internal/service` and `internal/reconcile` without a `//nostr:allow-poll <reason>` annotation. Forbid `setInterval` and one-shot `subscribe` + EOSE-resolve helpers in `web/src/lib/stores`. Forbid imports of `lib/api/client.js`. Forbid legacy kind constants outside `nostrmigration`. Forbid unbounded `limit`-without-paging filters.
- **One relay client per runtime.** A single Go pool and a single browser pool/store. Any new `SimplePool`/`RelayBus`/`PoolBackedClient` construction fails lint, so fixes land once (C-35, A-13).
- **Tests must not pin the anti-pattern.** Remove override tables and sleep-based waits from tests (C-43, C-2x sidecar tests). Make EOSE/OK/CLOSED-driven fixtures the only accepted pattern (per `AGENTS.md`).
- **Regular protocol-smell audits** (for example the `nostr-protocol-smells` skill) as part of release readiness. Diff the finding IDs in this report against each run, so an ID that reappears is flagged as a regression.

---

## I3 coverage-gap follow-up (2026-10-04, `5def5a59e1dc`)

_Method:_ Repeated the original A/B/C source audit over every item under **Root Cause > Coverage gaps**. A remains web/bootstrap, B daemon authority, C protocol/peripheral surfaces; IDs continue rather than renumber the historical 2026-09-30 findings. This is a static read of the stated worktree: `rg`/`grep`, `go list`, and targeted source/design/ratchet reads, not a runtime reproduction or full test run. Existing findings describe the older commit and must not be interpreted as the current state. A new finding below requires a current production path not already covered by an archtest ratchet or explicitly deferred by a Phase 3–5 design section. “Already ratcheted” means the ratchet prevents growth; it does **not** claim that the baseline debt is fixed. Severity follows the original report's critical/high/medium/low usage; all conclusions below are **CONFIRMED by source**, not empirically reproduced.

### A — Web app and deploy seed

**A-34. Public docs accept any signed publisher as Bahia documentation.** Severity: **high** — CONFIRMED. Evidence: `web/src/routes/docs/+page.svelte:5,20-33` and `web/src/routes/docs/[topic]/+page.svelte:5,31` call `fetchDocsCatalog`/`fetchDoc` without an author; `web/src/lib/docs/nostr.js:22-28` applies `authors` only when an optional `servicePubkey` is supplied, and `:87-118,246-261` turns matching kind-30023 events into catalog/detail content without a trust-root check. A valid signature authenticates the attacker's key, not Bahia's docs. This violates Phase 4 **§0(1)** (verified events behind one trusted store) and **§6.2** (seeded service pubkeys are the trust anchor), and is an author-scoping instance beyond A-26. **Fix:** require a trusted documentation-publisher set from the deploy seed or an authenticated publisher event, and apply it to cached and relay results before rendering.

**A-35. Continuity creates a fresh, untrusted, 1,000-event-per-filter in-memory read model on every visit.** Severity: **high** — CONFIRMED. Evidence: `web/src/routes/continuity/+page.svelte:18-42` subscribes on mount and closes on destroy; `web/src/lib/nostr/continuity.ts:25,121-130,171-211` supplies no `authors`, caps each of six filters at 1,000, initializes from empty `initialEvents`, and accumulates into a page-local `Map` rather than querying the IndexedDB store. EOSE can thus certify a truncated window, and offline navigation loses the view. This violates Phase 4 **§0(1–2), §2.4, §8.1**, the store-first replacement for A-5/A-6/A-23. **Fix:** derive continuity views from the shared verified event store with trusted authors and a paged/cursor-backed subscription.

**A-36. SoulFactory read models still bypass the shared store and re-gate on relay catch-up.** Severity: **medium** — CONFIRMED. Evidence: `web/src/lib/stores/souls.svelte.js:497-565` sets four loading flags, opens a direct `nostr.subscribe` with an optional (normally absent) soul author filter, and clears loading only after metadata completion; `web/src/routes/settings/fleet/+page.svelte:40-45` starts/stops it on navigation. Its history path at `souls.svelte.js:1174-1232` also opens a fresh bounded REQ instead of first reading local events. This is a remaining instance of the Phase 4 **§0(1–2), §8.1, §12 W4-S1** migration (related to old A-20/A-23, but specific to the previously unaudited souls store). **Fix:** query and subscribe through the shared event store, render cached souls immediately, and keep the subscription with the app lifecycle.

**A-37. The shipped widgets view routes around the deployment's relay set to two vendored public relays.** Severity: **high** — CONFIRMED. Evidence: `web/src/lib/widgets/ops-widget-wall.js:2-5,20,34-39` copies `FLEET_RELAY_URLS` and calls `subscribeWithRecoveryOnRelays`; the vendored constant is `web/vendor/wheelhouse/dist/constants.d.ts:6` (`relay.sharegap.net`, `nos.lol`), also documented in `web/vendor/wheelhouse/README.md:79-85`. `web/src/routes/widgets/+page.svelte:20-42` constructs this separate ephemeral wall on mount. The publisher allowlist is good, but it does not make the hardcoded relays part of this deployment or provide persisted offline widgets. This violates Phase 4 **§0(1), §2.1, §12 W4-S1** and its deployment-seed relay model (**§7**). **Fix:** use the boot pool and deployment relay selection, ingest trusted kind-30318 events into the shared store, and render its query.

**A-38. Build-time bootstrap roots survive a runtime reconfiguration, so the client can trust a stale signer or relay.** Severity: **high** — CONFIRMED. Evidence: `docker-compose.yml:99-109` supplies the same values as both web build args and runtime environment; `web/Dockerfile:5-11` exposes build args to Vite; `web/docker-entrypoint.d/40-bahia-bootstrap-env.sh:10-12` skips runtime substitution if the built index has no placeholders; `web/src/lib/stores/discovery.svelte.js:48-65` **unions**, rather than replaces, injected and baked relay URLs/service pubkeys. Thus rotating the runtime trust roots without rebuilding can leave the old root accepted (or make runtime injection a no-op when already substituted). The no-store nginx app-shell header at `web/nginx.conf:17-20` prevents HTTP caching, not this baked-seed problem. This violates Phase 4 **§6.2, §7** (deployment seed as the current trust root). **Fix:** generate one validated runtime-only seed before serving the shell, never merge it with build-time trust roots, and fail startup on missing/invalid values.

### B — Daemon and entity authority

**B-31. Payment records are committed to SQL before optional, best-effort canonical publication.** Severity: **high** — CONFIRMED. Evidence: `internal/service/payments.go:25-40,71-84,107-118,148-171` requires `PaymentRecordRepository`, creates rows before calling `publishCPState`, and logs/swallows publication failure; `internal/app/app.go:2013-2018` wires the publisher only when the projector and encryptor exist. Payment history remains a repository read at `payments.go:143-147`. Phase 4 **§4.2.1** documents the payment cp-state family, but does **not** defer SQL authority; it calls the relay copy source of truth. This violates Phase 3 **§3.4–3.5** (Postgres optional derived index, canonical output first). **Fix:** mint stable payment identities and signed cp-state from the operation, confirm/queue relay delivery, then update SQL as a rebuildable index.

**B-32. Security schedule and finding paths retain SQL as the execution and publication authority.** Severity: **high** — CONFIRMED. Evidence: `internal/service/security_scheduler.go:76-113` derives schedules and `ClaimDueSecurityScanSchedules` on startup/hourly ticks; `internal/service/policy.go:812-851` disables/upserts SQL schedules before publishing and explicitly logs a failed relay publish as nonfatal; `internal/service/security_scanner.go:332-363,473-494` writes target/run/findings before subsequent canonical publication. The ticker itself is **already ratcheted** by `internal/archtest/poll_ticker_test.go:27-30` with baseline `testdata/poll_tickers.baseline`, but the SQL claim/authority is not. Phase 4 **§4.2.1** defines canonical security families; Phase 3 **§3.4–3.6** requires rebuildable SQL and durable observable progress. **Fix:** schedule from subscribed policy/target state, persist claims as signed events or local-store idempotency, and treat SQL as a derivative.

**B-33. Route canary supervision disappears in DB-less mode even though its results are published as canonical Nostr state.** Severity: **high** — CONFIRMED. Evidence: `internal/app/app.go:548-565` constructs `RouteCanarySupervisor` only when both `routeCanaryStore` and `stateRepo` exist; `internal/service/route_canary_sources.go:16-42` enumerates desired routes using `states.ListAll`, and `route_canary.go:261-267` depends on that enumeration for every sweep. The `RouteCanaryProjector` at `route_canary_projector.go:378-408` only projects bus transitions after a DB-backed probe ran. Polling the **external route** is allowed by the original report's Recommendations; polling SQL for the desired set is not. This violates Phase 3 **§3.4, §6.2** (relay/local-store desired state and DB-optional operation). **Fix:** enumerate managed route plans and retain outage state from the local event store; leave the runtime probe timer in place.

**B-34. Managed-instance recovery uses SQL rows for both desired-spec enumeration and recovery budgeting.** Severity: **high** — CONFIRMED. Evidence: `internal/service/managed_instance_spec_source.go:23-52` reads all desired states and joins services/environments/units from repositories; `managed_instance_supervisor.go:180-205,314-345,364-371` requires `GetHealth`, maintenance overrides and recovery-attempt rows before acting. The runtime observation ticker at `managed_instance_supervisor.go:146` is **already ratcheted** and external observation is permitted, but a DB failure can prevent recovery actions and their Nostr observables. This violates Phase 3 **§3.4–3.6** (optional derived SQL, event-sourced desired and progress state). **Fix:** derive the supervised set and recovery ledger from subscribed local state, with SQL as an optional query index.

**B-35. Adoption's intent handler still makes a multi-table SQL transaction the condition of canonical adoption.** Severity: **high** — CONFIRMED. Evidence: `internal/app/app.go:1291-1292` registers the adoption intent handler; `internal/service/adoption.go:480-543,561-603` creates/updates service, environment, build, artifact, desired state, observations and identities inside `WithinTx`, then emits bus/canonical records **after** commit, logging canonical publish errors. `adoption.go:747,1022,1058` shows additional row creation. The signed entry point does not invert the authority of the side effects. This violates Phase 3 **§3.2–3.5** and Phase 5 **§2.2** (a signed intent must lead to canonical state independent of Postgres). **Fix:** stage adoption as a resumable intent-driven workflow with relay-canonical output per resource, then update SQL projections after acceptance.

### C — SoulFactory, assistant, sidecar and external adapters

**C-45. SoulFactory saga checkpoints are local-file authority that cannot be rebuilt from canonical events.** Severity: **medium** — CONFIRMED. Evidence: `internal/soulfactory/governed_provisioning_production.go:82` constructs `saga.NewFileStore`; `internal/soulfactory/saga/store.go:27-77,80-104` atomically stores one JSON run per request; `saga/engine.go:159-204` begins and resumes only through that store, while terminal publication occurs later at `engine.go:565-584`. The file is a sound crash journal on the *same* disk, but moving/recreating the daemon with relay state alone loses nonterminal stage/ownership lineage. This violates Phase 3 **§3.6** crash/retry semantics and **§6.2** relay/local-store readiness (not the already-ratcheted C-35 relay-client stack). **Fix:** publish bounded saga checkpoint/progress events or reconstruct the journal from signed request/observation events before resuming side effects.

**C-46. Assistant startup recovery silently truncates the active-session inventory at 500.** Severity: **high** — CONFIRMED. Evidence: `internal/app/assistant_execution.go:202` sets `RecentLimit: 500`; `internal/service/assistant_session_recovery.go:17-22,83-130,148-206` performs one EOSE-aware REQ with `Limit: r.limit`, then recovers only returned sessions. Its comment calls this a cache warm-up, but the runner is the startup autonomous recovery path; a still-running session outside the newest 500 is not resumed until separately touched. The checkpoint chain itself is signed, validated and restart-tested (`assistant_execution_store.go:200-317`; `assistant_execution_test.go:41-100`): **that portion is clean**. The inventory bound violates Phase 3 **§3.6** and Phase 4 **§0(2)** (durable progress must not depend on a truncated cold fetch). **Fix:** enumerate active session coordinates from the local event store or page the relay query to completion before recovery.

**C-47. The sidecar's protected-read policy defaults to warn/allow rather than enforce.** Severity: **high** — CONFIRMED. Evidence: `internal/config/config.go:1433,2252-2259` defaults and normalizes to `ReadAuthModeWarn`; `internal/relaysidecar/read_auth.go:317-349` returns allow for an unauthenticated protected REQ in warn mode; `server.go:150-167` correctly invokes the policy for REQ and COUNT, and `server.go:126` advertises `auth_required` only in enforce mode. Thus the NIP-42 implementation is real, but the default deployment still serves protected kinds/topics to anyone who can connect. This is the remaining default-policy part of old C-21, not a claim that NIP-42 is absent. It violates Phase 3 **§2.2** event-derived authorization and Phase 4 **§5.5** role/confidentiality boundary. **Fix:** make enforce the production default after validating all legitimate readers and test unauthenticated REQ/COUNT rejection on the default config.

**C-48. HiveCI release admission and retry still read SQL mirrors of signed relay evidence.** Severity: **high** — CONFIRMED. Evidence: `internal/adapters/hiveci/evidence.go:20-47,79-84,119-140` loads a `NostrEventRecord` by ID and pipeline policies/worker admission from repositories, then validates the decoded event; `internal/app/app.go:1865-1875` wires those repositories into release ingestion. Separately, `internal/app/background.go:76-98` polls `ListPendingResults` from the HiveCI repository to drive retries, wired at `app.go:1909`. Signature checks are good, but a missing/stale SQL mirror can reject relay-present evidence or strand retries. These `internal/app` timers are **not** covered by the service/reconcile ticker ratchet. This violates Phase 3 **§3.1–3.4** and Phase 5 **§3.2** local-store read path. **Fix:** resolve signed evidence, policies and pending results from the local event store and trigger retries from event/outbox state rather than scanning SQL.

**C-49. Gitea/HiveCI initiation explicitly stores unique replay authority in PostgreSQL.** Severity: **high** — CONFIRMED. Evidence: `internal/adapters/gitea/initiation_store.go:40-47,91-122` calls its initiation record “local authority” and lets the first SQL insert claim the canonical build ID; `internal/app/app.go:2431-2444` constructs `NewPgInitiationStore` for the production initiator; `internal/adapters/gitea/README.md:1-18` documents the SQL claim/CAS as the recovery authority. The exact-event relay inspection in that adapter is a strength, but it cannot reconstruct the claim and encrypted prepared event/key after DB loss. This violates Phase 3 **§3.4–3.6** and Phase 5 **§2.2** durable signed-intent semantics. **Fix:** make source event ID/build identity deterministic and persist encrypted prepared operations in a recoverable event/local-store journal, not a non-rebuildable SQL claim.

### Auditable negative, deferred and ratcheted results

| Coverage item checked | Current result |
|---|---|
| SoulFactory `openclawcontrol`, communikeys, concord, Signet | **Checked, no additional Nostr-first finding:** OpenClaw file/compose writes are runtime side effects (`openclawcontrol/control.go:275-281,1272`), not asserted canonical read models; communikeys and concord use signed, author-checked relay reads and OK-checked publish (`communikeys_membership.go:271-329,379-382`; `concord_inbox.go:42-76,114-139`). Signet enrollment's `signetctl` JSON-RPC is a local external-tool boundary with correlated IDs (`signet_enrollment.go:662-666`), not a new ContextVM state-read method. Saga checkpoint authority is C-45. |
| SoulFactory legacy adoption report / legacy reconciliation | **Checked, deferred by design Phase 5 §5.2:** one-time legacy reconciliation HTTP boundary is retained. The report itself is a pure secret-free classifier (`legacy_adoption_report.go:112-155,309-358`) and refuses ambiguous evidence; no separate finding. |
| `internal/driftdecision`, `internal/rollout` | **Checked, no new live-path finding:** `driftdecision/decision.go:19-41` is a called repository lookup inside the already-known B-23/B-27 reconciler/registry authority pattern (`reconcile/reconciler.go:387-424`); do not duplicate it. `rollout/executor.go:41-80` is SQL-first, but `NewExecutor` has no production caller and is **already ratcheted** as a test-only export in `internal/archtest/testdata/unwired_exports.baseline:265`/`unwired_exports_test.go:20-23`; the runtime observer health gate (`rollout/health_gate.go:40-68`) is permitted external observation. |
| Web `/settings`, `/route-canaries`, discovery, SSR API client | **Checked, clean for the named concerns:** settings uses the shared discovery store (`settings/+page.svelte:2-25`; `stores/system.svelte.js:1-22`); route-canaries renders `operational-views` from `boot`/store refresh (`route-canaries/+page.svelte:2-4,41-67`); discovery queries cached verified events before optional relay catch-up (`stores/discovery.svelte.js:191-240`). `web/src/lib/api/client.js` is **absent** and no SSR import of it exists in these routes. The stale web archtest baseline still lists former imports (`web/tests/unit/architecture-gates.baseline.json:4-5`), but its test rejects growth (`architecture-gates.test.js:61-91`): **already ratcheted**, not a current REST usage finding. |
| Web `/docs`, `/continuity`, `/widgets`, souls | **Checked; findings A-34–A-37.** Docs' persisted event-store fast path (`docs/nostr.js:22-28`) and continuity's `ssr=false` (`routes/continuity/+page.ts:1`) are positive; neither removes the specific findings. Wheelhouse's `data_ref` renderer is explicitly a placeholder (`web/vendor/wheelhouse/README.md:20-25`); it is not counted as a separate Nostr-authority issue. |
| Deploy, nginx | **Checked; A-38 only.** The entrypoint rejects missing seed values when placeholders exist (`web/docker-entrypoint.d/40-bahia-bootstrap-env.sh:15-20`), and nginx serves the shell without cache (`web/nginx.conf:17-20`); there is no backend readiness gate in nginx. |
| NIP-86 / `relayadmin` | **Checked, clean apart from C-47's default read mode:** NIP-86 requires signed NIP-98 bound to URL, method, payload and timestamp and an admin policy decision (`relaysidecar/admin.go:465-485,574-610`); `relayadmin/client.go:190-225` restricts methods and targets. REQ and COUNT both invoke the read policy (`relaysidecar/server.go:150-167`). |
| Cashu, Blossom, Qdrant, registry, Harbor | **Checked, no DB-poll finding in these adapter packages:** scoped `rg` found no repository-backed event delivery loops; Blossom's `time.After` sites (`adapters/blossom/proxy.go:132`, `list.go:94`, `download.go:120`) are outbound HTTP retry delay. Cashu live wallet is explicitly fail-closed (`app/app.go:2019-2021`). Gitea/HiveCI exceptions are C-48/C-49. |
| Tests/PSTF | **Checked, no independent finding beyond the production IDs:** `web/tests/unit/ops-widget-wall.test.js:46-65` *pins* the hardcoded Wheelhouse relay set (A-37); `pstf/features/BAHIA_ROUTE_CANARY_POLICY_OVERRIDES/verification_report.md:7-20` carefully limits its claim to repository-level tests, but its AC4 approves repo-configured, non-signed route policy and cannot verify DB-less supervision (B-33). `web/tests/e2e/e2e-keyring.js:4-54` signs mock-relay events; no active `__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS` bypass was found. The old sleep-based sidecar-test claim is stale: `config_consumer_test.go:204-244` uses channel signals and a deadline, not `time.Sleep`. These historical PSTF “passed” statements are evidence of past gates, **not** current relay-canonical acceptance proof. |

### Proposed follow-up issues

- **Trust-anchor enforcement for public docs and runtime deploy seed** — type **bug**, priority **P1**, covers **A-34, A-38**.
- **Move continuity and SoulFactory views onto the shared browser event store** — type **task**, priority **P2**, covers **A-35, A-36**.
- **Remove Wheelhouse's hardcoded public relay route and pin a store-first widget test** — type **bug**, priority **P1**, covers **A-37**.
- **Invert payment and security canonical-write ordering; derive security scheduling from events** — type **task**, priority **P1**, covers **B-31, B-32**.
- **Run route-canary and managed-instance supervision from relay-derived desired state without SQL** — type **task**, priority **P1**, covers **B-33, B-34**.
- **Make adoption output relay-canonical across its transaction boundary** — type **task**, priority **P1**, covers **B-35**.
- **Persist and recover all active SoulFactory and assistant workflows from event state** — type **task**, priority **P2**, covers **C-45, C-46**.
- **Enforce protected relay reads by default after reader compatibility proof** — type **bug**, priority **P1**, covers **C-47**.
- **Replace HiveCI/Gitea SQL evidence, retry and initiation authority with recoverable event/local-store state** — type **task**, priority **P1**, covers **C-48, C-49**.
