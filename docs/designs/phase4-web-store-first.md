# Phase 4: Web Store-First — Design

- Status: proposed (2026-10-02)
- Issue: bahia-irsry.12
- Depends on: bahia-irsry.11 (Phase 3, in progress), bahia-irsry.11.20 (C1 crypto, in progress)
- Blocks: bahia-irsry.11.19 (Phase 3 R1: delete ContextVM CRUD handlers)
- Audit: `docs/investigations/nostr-first-architecture-audit-2026-09-29.md` — findings A-1–A-33
- Charter: `docs/architecture.md` invariants 1–7
- Phase 3 baseline: kind 30900 intents signed by operator, bounded 30315 intent-status, TrustSet with relay/Postgres/config sources, dual-path ContextVM window, intent_domains config, in-process dual dispatch, per-(relay, filter) cursors, NIP-77 sync, single daemon relay stack, client-minted UUIDv7 entity ids, `Publisher.PublishBeforeCommit`

---

## 0. Scope and Goal

Phase 4 replaces the web app's REST/ContextVM-centred data layer with a Nostr-native, store-first architecture. After Phase 4:

1. **One pool, one IndexedDB event store.** Every view is a derived query over verified events.
2. **Instant render from cache.** Opening a tab renders whatever the store already holds; EOSE is a "synced" badge.
3. **No backend gate.** A persisted signer-verified session is authenticated immediately. Roles come from relay membership events, not a REST `/orgs` probe.
4. **Durable signed intents.** Mutations are locally signed kind 30900 (or NIP-59 gift-wrapped) intent events, inserted into the store as "pending" and published through an outbox with per-relay OK tracking.
5. **ContextVM stays only for:** assistant prompts, secret reveal, log fetch.
6. **Delete the legacy layer.** indexeddb-cache.js, controlplane bootstrap/connection/events sync, AuthGuard REST probe, retained-domain-subscription, extra pools, ad-hoc storage caches, lib/api/client.js.

This closes the dual-path window opened by Phase 3 §4.1 so that bahia-irsry.11.19 (delete ContextVM CRUD handlers) can proceed.

---

## 1. Library Choice

### 1.1 Candidates

| Criterion | welshman | applesauce + nostr-idb | In-house (nostr-tools raw) |
|-----------|----------|------------------------|---------------------------|
| **Svelte-native** | Yes (built for Coracle, Svelte stores) | No (RxJS observables; needs Svelte adapter layer) | N/A |
| **IndexedDB event store** | `@welshman/store` wraps IndexedDB with replaceable/addressable indexes | `nostr-idb` (separate pkg, batched writes, LRU pruning) | Must build from scratch |
| **NIP-77 negentropy** | `@welshman/net` supports negentropy sync | `applesauce-relay` has negentropy helpers | Must build from scratch |
| **NIP-42 AUTH** | `@welshman/net` handles AUTH handshakes | Not built-in (manual relay handling) | nostr-tools has `relay.auth` |
| **NIP-46 remote signer** | `@welshman/signer` (BunkerSigner over relay transport) | Not included | nostr-tools/nip46 `BunkerSigner` |
| **Bundle size** | ~45 KB gzipped (net + store + signer) | ~38 KB gzipped (core + idb + relay) | nostr-tools already bundled (~28 KB) |
| **Existing signer code** | Requires adapter from current nip07/nip46 probing | Requires adapter | Current code uses nostr-tools directly |
| **Subscription model** | `load`/`subscribe` with dedup, cursor persistence, ref-counting | Manual REQ management; store observation via RxJS | Manual (current approach) |
| **Derived views** | Repository queries with reactive Svelte bindings | `EventStore` with RxJS `query()` | Must build collection derivation |
| **Maturity / maintenance** | Active (Coracle powers it); Svelte 5 runes support | Active (hzrd149 maintains); used by nostrudel | N/A |

### 1.2 Decision: welshman

**Decision: use `@welshman/net`, `@welshman/store`, `@welshman/signer`, and `@welshman/util`.**

Rationale:
1. **Svelte-native.** welshman is built for Svelte and exposes reactive stores directly. applesauce's RxJS layer requires a non-trivial Svelte adapter (subscribe→derived→unsubscribe lifecycle for every view), which is an ongoing maintenance burden.
2. **One package replaces six concerns.** Pool management, subscription dedup/ref-counting, cursor persistence, IndexedDB storage, AUTH handling, and derived views are all integrated. We currently have ~1,600 lines of hand-rolled pool/subscription/cache code that welshman replaces.
3. **NIP-77 negentropy.** `@welshman/net` supports client-side negentropy sync out of the box, which is critical for the per-filter cursor model (A-5). applesauce-relay also supports this, but the Svelte integration cost tips the balance.
4. **NIP-42 AUTH.** `@welshman/net` handles AUTH challenges transparently. The current code has six independent pools (A-13) partly because AUTH state isn't shared; welshman's single pool solves this.
5. **NIP-46.** `@welshman/signer` provides `BunkerSigner`, replacing the current `window.nostr.nip46` probe (C-27) with actual NIP-46 relay transport. The existing nostr-tools `nip46` module is also available as a fallback.
6. **Bundle size acceptable.** ~45 KB gzipped for the full welshman stack vs ~28 KB for nostr-tools alone. The current app already bundles nostr-tools plus ~1,600 lines of pool/subscription code (~12 KB) plus the retained-domain-subscription, discovery, and controlplane layers. Net bundle change is roughly neutral after deletions.

**Migration path:** nostr-tools remains as a dependency for low-level utilities (event signing, NIP-44 encryption, hex/bech32). welshman uses nostr-tools internally for the same primitives. The two coexist without conflict. The `PoolBackedClient` wrapper, `pool-subscriptions.js`, `pool-publish.js`, and `pool-utils.js` are deleted.

### 1.3 What stays from nostr-tools

- `finalizeEvent`, `getPublicKey`, `verifyEvent` — event construction and verification.
- `nip44` — encryption/decryption for gift-wrapped intents and confidential state.
- `nip19` — bech32 encoding for display.
- `SimplePool` is no longer used directly; welshman's pool replaces it.

---

## 2. Store Schema and Namespace

### 2.1 IndexedDB database

One IndexedDB database per deployment, namespaced by the service pubkey from the deploy seed:

```
Database name: bahia-events-<service_pubkey_prefix_8>
```

The 8-character hex prefix is sufficient to avoid collisions between deployments while keeping the name human-readable. The service pubkey comes from `app.html`'s `__PUBLIC_BAHIA_SERVICE_PUBKEYS__[0]`.

### 2.2 Object stores

welshman's `@welshman/store` provides the core event store. We configure it with:

| Object store | Key | Indexes | Purpose |
|-------------|-----|---------|---------|
| `events` | `id` (event id) | `kind`, `pubkey`, `created_at`, `[kind, pubkey, d]` (compound for addressable lookup) | All verified events |
| `cursors` | `[relay, filterHash]` | — | Per-(relay, filter) cursor: `{ relay, filterHash, since, updatedAt }` |
| `seen_intents` | `intent_id` | `created_at` | Local intent dedup for the outbox |
| `pending_intents` | `intent_id` | `coordinate`, `status`, `created_at` | Locally signed intents not yet confirmed |

### 2.3 Namespace and eviction

- **No TTL.** Events are never expired by time. The audit (A-3) identified the 15-minute TTL as a direct cause of cold starts.
- **LRU by size.** When the database exceeds a configurable threshold (default: 100 MB, measured by `navigator.storage.estimate()`), the oldest non-addressable regular events are pruned first. Addressable and replaceable events are never LRU-evicted (they are bounded by `(kind, pubkey, d)` latest-wins).
- **NIP-09 deletions.** Kind 5 events from the service pubkey tombstone `e` ids and `a` coordinates up to the deletion's `created_at` (A-25).
- **NIP-40 expiration.** Events with an `expiration` tag past `now` are swept on store open and periodically (every 5 minutes).

### 2.4 Event ingestion path

All events — whether from relay subscription, cache hydration, or local intent creation — flow through one ingestion function:

```javascript
function ingestEvent(event) {
  // 1. Verify signature (welshman does this on receipt)
  // 2. Check NIP-01 replaceable/addressable rules:
  //    - For (kind, pubkey, d): keep only newest by (created_at, lowest id)
  //    - Reject if older than stored
  // 3. Apply NIP-09 deletions
  // 4. Apply NIP-40 expiration check
  // 5. Insert into IndexedDB
  // 6. Emit to reactive derived stores
}
```

This replaces `applyControlplaneEvent`, `refreshCollections`, `shouldAcceptControlplaneEvent`, and the per-collection `applyXEvent` functions.

---

## 3. Intent Publishing by Phase 3 Domain

Phase 3 §1 defines intents as kind `30900` addressable replaceable events signed by the operator. Phase 4 implements the client-side signing, publishing, and reconciliation for each domain.

### 3.1 Plaintext intents (non-sensitive domains)

**Domains:** service, environment, policy, package, llm, ml, backup, dns, sbom, worker.

```json
{
  "kind": 30900,
  "pubkey": "<operator-hex-pubkey>",
  "created_at": "<now>",
  "tags": [
    ["d", "<entity-coordinate>"],
    ["domain", "<domain>"],
    ["schema", "bahia.intent.<domain>.v1"],
    ["t", "bahia-intent"],
    ["t", "<domain-topic>"],
    ["op", "create|update|delete"],
    ["org", "<org-uuid>"],
    ["intent_id", "<uuidv7>"]
  ],
  "content": "<full-desired-state-json>"
}
```

**`intent_id`:** A UUIDv7 minted by the client per mutation attempt. Carried in both tags and content. Used for:
- Idempotency: the daemon's bbolt store deduplicates by `intent_id`.
- Correlation: the client matches the `intent_id` in the daemon's `30315` intent-status event to know the outcome.
- Outbox retry: the client's pending-intent store tracks `intent_id` to avoid duplicate publishes.

**`expected_updated_at`:** For updates (not creates), the client includes `expected_updated_at` in the content, set to the `updated_at` value from the current canonical `30900` state it is modifying. The daemon compares this against its local state and publishes a `30315` status with `status=conflict` on mismatch (Phase 3 §1.5). The client shows a "conflict" indicator and prompts the user to re-read and re-submit.

**`op`:** Advisory (`create`, `update`, `delete`). The daemon uses level-triggered reconciliation (Phase 3 §1.2): it reconciles toward the full desired state regardless of `op`. The client uses `op` to choose the right UI path (form vs confirmation dialog).

### 3.2 NIP-59 gift-wrapped intents (sensitive domains)

**Domains:** org (membership), secret, notification.

Per Phase 3 §1.7, these intents are wrapped in a NIP-59 gift wrap (kind `1059`):

```json
{
  "kind": 1059,
  "pubkey": "<random-ephemeral-pubkey>",
  "created_at": "<randomized-timestamp>",
  "tags": [["p", "<bahia-service-pubkey>"]],
  "content": "<nip44-encrypted-inner-30900-intent>"
}
```

The inner event is the same `30900` intent as §3.1, signed by the operator, NIP-44-encrypted to the service pubkey. The gift wrap's ephemeral pubkey and randomized timestamp prevent metadata correlation.

**Secret values** have double encryption: the inner `30900` content field itself is NIP-44-encrypted to the service pubkey, so the secret value is protected even if the inner event is exposed.

**Signer requirement:** NIP-59 gift wrapping requires `nip44.encrypt` from the signer. The client checks signer capabilities before offering sensitive mutations:
- NIP-07 with NIP-44 support: use `window.nostr.nip44.encrypt`.
- NIP-46 (welshman `BunkerSigner`): the bunker handles NIP-44 encryption over the relay transport.
- If neither supports NIP-44: sensitive mutation affordances are disabled with an explanatory message.

### 3.3 Pending intent lifecycle

```
[User submits form]
    │
    ▼
[Sign intent event (NIP-07/NIP-46 signer)]
    │
    ▼
[Insert into local store as "pending"]
  → Derived view shows entity with pending badge
    │
    ▼
[Publish to relay pool with per-relay OK tracking]
  → On OK accepted=true from ≥1 relay: mark "published"
  → On all OK rejected: mark "failed", show error
    │
    ▼
[Subscribe to 30315 intent-status from service pubkey]
  → d = "intent-status:<my-pubkey>:<entity-coordinate>"
    │
    ├─ status=accepted → Remove pending badge, canonical 30900 arrives
    ├─ status=conflict → Show conflict UI, prompt re-read + re-submit
    ├─ status=rejected → Show rejection reason
    └─ status=superseded → Remove pending badge, another intent won
```

### 3.4 Reconciliation against canonical state

The client does not "apply" intents to local state. Instead:

1. The pending intent is a **UI-only overlay**: the derived store shows it alongside canonical state with a visual badge.
2. When the daemon publishes the canonical `30900` under the service pubkey, the store ingests it normally.
3. The client matches the canonical event's `intent_id` tag (or `e` tag referencing the intent event) against its pending-intent store and removes the pending overlay.
4. If no `30315` status event arrives within 60 seconds, the client re-publishes the intent (outbox retry).

### 3.5 Retries and JSON-RPC -32011 handling

**Outbox retry for intents:**
- Intents in the `pending_intents` store with no relay `OK accepted=true` after 10 seconds are retried up to 3 times with exponential backoff (10s, 30s, 90s).
- After 3 failures, the intent is marked "failed" and the user is notified.

**JSON-RPC -32011 for ContextVM calls that remain:**
Per bahia-irsry.48 item 2, all remaining ContextVM mutations (assistant, secret reveal, log fetch) send a `progressToken`. If the daemon returns JSON-RPC error `-32011` (progress cancelled / request superseded), the client:
1. Retries once with a new `progressToken`.
2. On second failure, surfaces the error.

ContextVM reads that remain are: assistant prompt/response, secret value reveal, deployment run log streaming. All others become relay subscriptions.

---

## 4. What Stays ContextVM

**Decision: only three interaction patterns remain on ContextVM (kind 25910 / 1059 gift wrap).**

| Pattern | Why it stays | Transport |
|---------|-------------|-----------|
| **Assistant prompts** | Interactive, session-scoped, not addressable state | NIP-59 gift wrap (1059) to service pubkey |
| **Secret value reveal** | The value itself must never touch relay storage even encrypted; the daemon decrypts on demand and returns the plaintext via an ephemeral response | NIP-59 gift wrap request + ephemeral gift wrap response |
| **Deployment run log streaming** | Long-running, append-only output that doesn't fit addressable events; streamed via ContextVM progress notifications | NIP-59 gift wrap with progressToken |

Everything else — service/environment/policy/DNS/backup/ML/LLM/package/worker/SBOM CRUD, org membership, notification channel config, security findings reads, payment history reads, relay settings — becomes relay subscriptions (reads) and signed intents (writes).

---

## 5. Membership-Derived Roles

### 5.1 How org membership events work (Phase 3 §2.2, O1 slice)

Org membership is modeled as encrypted kind `30900` events gift-wrapped (kind `1059`) to each member's pubkey, signed by an org owner/admin. The inner event has:
- `d` = `org:member:<org-id>:<member-pubkey>`
- `domain` = `org`
- Content: `{ "org_id": "...", "pubkey": "...", "role": "viewer|deployer|admin|owner" }`

### 5.2 C1 requirement: per-org content key with member wrapping (bahia-irsry.11.20)

The C1 slice (in progress) is designing a unified confidential cp-state scheme:
- A **per-org symmetric content key** (XChaCha20-Poly1305 or NIP-44 symmetric) encrypts the cp-state content.
- The content key is **wrapped (NIP-44 encrypted) to each org member's pubkey** and published as a key-reference tag or a separate key-distribution event.
- Secret _values_ and channel credentials remain **service-only** (never wrapped to members).

### 5.3 Web decrypt path

The web derives roles and reads confidential state through this sequence:

```
1. Signer provides the user's pubkey
2. Subscribe to kind 1059 events tagged #p = [my-pubkey]
   → Filter: { kinds: [1059], "#p": [myPubkey], since: cursor }
3. For each gift wrap:
   a. Decrypt outer NIP-44 layer using signer
   b. Verify inner event signature
   c. If inner is org membership (domain=org, schema=bahia.intent.org-member.v1):
      → Extract org_id, role from content
      → Update local role map: { orgId → role }
   d. If inner carries a key-reference tag:
      → Decrypt the wrapped content key using signer
      → Store content key in memory (never persisted to IndexedDB)
4. For confidential cp-state events (e.g., encrypted 30900 from service pubkey):
   a. Read the key-reference tag to find the content key
   b. Decrypt content using the content key
   c. Ingest the decrypted event into the store
```

### 5.4 Requirements C1 must meet for Phase 4

| # | Requirement | Why |
|---|-------------|-----|
| C1-R1 | The content key must be **NIP-44 wrapped to each member pubkey** and discoverable via a tag or a published key-distribution event | The web signer (NIP-07 or NIP-46 bunker) must be able to unwrap it using standard `nip44.decrypt` |
| C1-R2 | The key-reference mechanism must work with **NIP-46 bunker signers**, not just local keys | `nip44.decrypt` via the bunker relay transport must be sufficient; no raw privkey access required |
| C1-R3 | Membership events must be individually decryptable by each member | Each member receives their own gift-wrapped copy (already specified in Phase 3 §2.2) |
| C1-R4 | When a member is added or removed, the content key is **rotated** and re-wrapped to the current member set | The web re-fetches key-distribution events on membership change; stale keys are replaced in memory |
| C1-R5 | The content key must never be persisted to IndexedDB or localStorage | It lives only in the in-memory signer session; loss = re-derive on next login |

### 5.5 Role-gated UI

Roles gate **mutation affordances**, not view rendering:

| Role | Can view | Can mutate |
|------|----------|-----------|
| `viewer` | All relay-public state; org-confidential state for their orgs | Nothing |
| `deployer` | Same as viewer | Deploy, rollback, restart (service ops) |
| `admin` | Same as viewer | All entity CRUD for their orgs |
| `owner` | Same as viewer | All entity CRUD + member management |

A user with **no membership** can still view relay-public state (services, environments, DNS, workers — anything published as plaintext `30900` by the service pubkey). They cannot view confidential org state or perform any mutations. This is a fundamental change from today's model where **all** protected routes are gated on backend membership (A-1).

---

## 6. Auth Bootstrap Without REST

### 6.1 Current auth chain (to be deleted)

```
waitForNip07 (1.5s)
  → connectNip46 (reconnect)
    → configureBackendAuth:
        → loadSystemInfo (discovery, 10s deadline)
        → supportsDirectNip98Auth check
        → GET /api/v1/orgs (REST probe)  ← THIS GATES ALL 24 ROUTES
        → extract roles from REST response
    → hydrateAuthMetadata:
        → fetchRelayList (5s timeout, creates new pool)
        → fetchProfile (5s timeout, creates new pool)
```

Worst case: 21.5 seconds of "Checking authentication…" (A-2).

### 6.2 New auth bootstrap

```
1. Check localStorage for persisted session:
   { pubkey, authMethod, signerVerifiedAt, relays }
   → If present and signerVerifiedAt < 24h ago: AUTHENTICATED IMMEDIATELY
   → Render from store at once (no spinner)

2. Background signer verification:
   a. Detect signer (NIP-07 probe or NIP-46 reconnect)
   b. Verify pubkey matches persisted session
   c. If mismatch or expired: prompt re-auth, clear session
   d. Update signerVerifiedAt

3. Background metadata hydration (non-blocking):
   a. Subscribe to { kinds: [0, 10002], authors: [myPubkey] }
      on the shared pool — no separate pool
   b. Store profile (kind 0) and relay list (kind 10002) in the event store
   c. Update authState reactively from the store

4. Role derivation (non-blocking):
   a. Subscribe to { kinds: [1059], "#p": [myPubkey] }
   b. Decrypt membership events → update role map
   c. UI elements that require specific roles enable/disable reactively
```

**Key differences:**
- No REST probe. No `/api/v1/orgs`. No `backendAuthenticated` flag.
- No EOSE gate. The page renders immediately from the store.
- No discovery dependency. Discovery is still subscribed to but is optional metadata; its absence disables only daemon-dependent features (assistant, secret reveal, log fetch).
- No separate auth pools. Profile and relay-list queries use the shared pool.
- Session persistence is a simple localStorage record, not a complex state machine.

### 6.3 AuthGuard replacement

`AuthGuard` is replaced by a **reactive role check** in each route's `+page.svelte`:

```svelte
<script>
  import { roles, isAuthenticated } from '$lib/stores/auth.js';
  import { goto } from '$app/navigation';

  // Mutation-gated pages check for a specific role
  const canMutate = $derived(roles().has('admin') || roles().has('owner'));
</script>

{#if !isAuthenticated()}
  <LoginPrompt />
{:else}
  <!-- Render from store immediately; mutation buttons disabled if !canMutate -->
  <ServiceList {canMutate} />
{/if}
```

No spinner. No redirect. Unauthenticated users see relay-public data. Authenticated users see relay-public data plus their org-confidential state plus mutation affordances gated by role.

---

## 7. Boot Sequence (to-be)

```
┌─────────────────────────────────────────────────────┐
│ 1. Read deploy seed from app.html                   │
│    → service_pubkeys, relay_urls                    │
├─────────────────────────────────────────────────────┤
│ 2. Open IndexedDB event store                       │
│    → Namespace: bahia-events-<service_pubkey[:8]>   │
│    → Sweep NIP-40 expired events                    │
├─────────────────────────────────────────────────────┤
│ 3. Render from store IMMEDIATELY                    │  ← No network needed
│    → All routes derive views from store queries     │
│    → Empty store = empty list (not a spinner)       │
│    → Show "syncing…" indicator in status bar        │
├─────────────────────────────────────────────────────┤
│ 4. Check persisted session                          │
│    → If valid: authState = authenticated            │
│    → Start background signer verification           │
│    → Start background membership subscription       │
├─────────────────────────────────────────────────────┤
│ 5. Connect to relay pool                            │  ← Network starts
│    → Single welshman pool for all subscriptions     │
│    → Per-interest, ref-counted subscriptions:       │
│       a. Read-model: { kinds: [30900, ...],         │
│          authors: [servicePubkey], since: cursor }   │
│       b. Worker adverts: { kinds: [37195] }         │
│       c. Activity/ops: { kinds: [4903, ...],        │
│          since: now-7d }                            │
│       d. Membership (encrypted): { kinds: [1059],   │
│          "#p": [myPubkey], since: cursor }          │
│       e. Intent status: { kinds: [30315],           │
│          "#p": [myPubkey], since: cursor }          │
│       f. Profile/relays: { kinds: [0, 10002],       │
│          authors: [myPubkey] }                      │
├─────────────────────────────────────────────────────┤
│ 6. On EOSE per relay per filter:                    │
│    → Update cursor in IndexedDB                     │
│    → Update "synced" badge per domain               │
│    → If all filters EOSE'd from ≥1 relay:           │
│      show "live" indicator (replaces "syncing…")    │
├─────────────────────────────────────────────────────┤
│ 7. Batch event application per frame                │
│    → requestAnimationFrame coalescing               │
│    → Dirty flags per collection, not per event      │
│    → Derived views re-compute lazily                │
└─────────────────────────────────────────────────────┘
```

---

## 8. Derived Stores (Collection Views)

### 8.1 Pattern

Each domain collection becomes a **derived query over the event store**, replacing the current per-collection `Map` + `replaceSnapshotArray` pattern:

```javascript
// stores/collections/services.svelte.js (after Phase 4)
import { eventStore } from '$lib/nostr/store.js';

export const services = $derived(
  eventStore.query({
    kinds: [30900],
    authors: [servicePubkey],
    filter: (event) => event.tags.find(t => t[0] === 't' && t[1] === 'service-registry'),
    sort: (a, b) => a.content.name.localeCompare(b.content.name)
  })
);
```

### 8.2 Domain routing by topic tag

The current `events.svelte.js` routes events by parsing content and checking schemas. Phase 4 routes by the `t` (topic) tag on `30900` events, which is indexed by welshman's store:

| Topic tag | Domain | View |
|-----------|--------|------|
| `service-registry` | services | Services list and detail |
| `environment-registry` | environments | Environments list and detail |
| `environment-state` | states | Environment runtime state |
| `deployment-intent` | deployments | Deployment intents |
| `deployment-run` | runs | Deployment runs |
| `policy-registry` | policies | Policies list |
| `package-registry` | packages | Package repos/artifacts/promotions |
| `llm-route-registry` | LLM | LLM routes |
| `llm-route-state` | LLM | LLM route state |
| `ml-model` | ML | ML models/versions/endpoints |
| `backup-recipe` | backup | Backup config |
| `dns-zone` | DNS | DNS zones/endpoints |
| `worker-assignment` | workers | Worker assignments |
| `sbom-reference` | SBOM | SBOM references |

This eliminates the content-parsing overhead during ingestion and the `refreshCollections()` O(n²) rebuild (A-7).

### 8.3 Pending intent overlay

Pending intents (from §3.3) are overlaid onto derived views:

```javascript
export const servicesWithPending = $derived(() => {
  const canonical = services();
  const pending = pendingIntents.query({ domain: 'service' });
  // Merge: pending intents for coordinates not yet in canonical
  // show as "pending" entries; pending intents for existing
  // coordinates show as "updating" overlays
  return mergeWithPending(canonical, pending);
});
```

---

## 9. Cut-over Plan

### 9.1 Prerequisites

Before Phase 4 implementation begins:
1. **Phase 3 Waves 1–2** must be merged: services, environments, states, builds, artifacts, policies accept intents.
2. **C1 crypto** (bahia-irsry.11.20) must define the key-wrapping scheme so the web decrypt path can be implemented.
3. **Phase 3 Wave 5 O1** (org membership events) must be merged for membership-derived roles.

Phase 4 waves can begin as soon as Wave 1 + C1 are merged. Later Phase 4 waves (intent signing for DNS, backup, etc.) can land in parallel with Phase 3 Waves 3–5 as each daemon domain slice completes.

### 9.2 Enabling intent domains on the daemon

The daemon's `nostr.intent_domains` config controls which domains accept relay intents (Phase 3 §4.1). The Phase 4 web must know which domains are intent-enabled to decide whether to sign an intent or fall back to ContextVM.

**Decision: the web always signs intents for all domains. The daemon's dual-dispatch routes legacy ContextVM through the same pipeline.**

The web does not read `intent_domains` config. It always publishes signed intents. If a domain is not yet in `intent_domains`, the daemon's relay write policy rejects the intent (the sidecar only accepts `t=bahia-intent` events from authorized pubkeys for enabled domains). The web falls back to ContextVM on relay rejection:

```
1. Sign and publish intent
2. If relay OK rejected (CLOSED with "restricted:"):
   → Fall back to ContextVM for this domain
   → Log warning: "domain not yet intent-enabled"
3. If relay OK accepted:
   → Normal pending intent flow
```

This means the web can be deployed ahead of daemon domain slices. The dual-dispatch window closes per domain when:
1. The daemon's `intent_domains` includes the domain.
2. The web confirms relay acceptance.
3. The ContextVM handler for that domain is deleted (bahia-irsry.11.19).

### 9.3 Order of operations for full cut-over

```
Phase 3 Waves 1-6 (daemon)         Phase 4 Waves 1-4 (web)
─────────────────────────           ─────────────────────────
Wave 1: framework + svc/env   ──→  Wave 1: store + boot + svc/env intents
Wave 2: states/builds/policies ──→  Wave 2: derived views for all domains
Wave 3: DNS + LLM             ──→  Wave 3: DNS/LLM/backup/ML intent signing
Wave 4: backup/ML/pkg/workers       │
Wave 5: orgs/secrets/notifs    ──→  Wave 4: encrypted intents + role-gated UI
Wave 6: tier removal + R1           │
                                    │
                               bahia-irsry.11.19: delete ContextVM CRUD handlers
```

After Phase 4 Wave 4 completes and all domains are intent-enabled, bahia-irsry.11.19 deletes the ContextVM CRUD handlers. The dual-path window is closed.

---

## 10. Playwright / E2E Harness Strategy

### 10.1 Current state

Today's E2E tests use:
- `relay-harness.js`: starts a real `bahia-test-relay` process (`cmd/bahia-test-relay`), seeds it with fixture events, and provides a `signAndPublish` helper.
- `e2e-keyring.js`: test operator/service keys.
- `cp-state-fixtures.js`: pre-signed `30900` events for services, environments, etc.
- `harnesses/*.js`: per-feature harnesses that start the relay, seed domain-specific fixtures, and configure `__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS` to bypass signature verification.
- The `__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS` global flag (C-25) skips signature verification for events with `sig='0'.repeat(128)`.

### 10.2 Target: real relay, real signatures, no bypass

**Decision: delete `__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS`. All test events are signed with the test keyring.**

The `bahia-test-relay` already exists and works. The harness changes are:

1. **Sign all fixture events.** `cp-state-fixtures.js` already uses `finalizeEvent` with the test operator key. Extend this to all domain fixtures. Remove any fixture that uses the zero-sig shortcut.

2. **Test signer injection.** Instead of relying on `window.nostr` being a browser extension, the test page injects a deterministic NIP-07-compatible signer from the test keyring:

```javascript
// In Playwright's page.addInitScript:
window.nostr = {
  getPublicKey: async () => TEST_OPERATOR_PUBKEY,
  signEvent: async (event) => finalizeEvent(event, TEST_OPERATOR_SK),
  nip44: {
    encrypt: async (pubkey, plaintext) => nip44.encrypt(TEST_OPERATOR_SK, pubkey, plaintext),
    decrypt: async (pubkey, ciphertext) => nip44.decrypt(TEST_OPERATOR_SK, pubkey, ciphertext)
  }
};
```

3. **Intent verification.** E2E tests for mutations:
   a. Fill form and submit.
   b. Assert a signed `30900` intent event appears on the test relay (query the relay directly).
   c. Simulate the daemon by publishing a canonical `30900` state event and a `30315` status event from the service key.
   d. Assert the UI updates from "pending" to confirmed.

4. **Membership fixture.** Seed the test relay with gift-wrapped membership events for the test operator, so the role derivation path is exercised.

5. **Relay-only tests.** No ContextVM mock needed for CRUD tests. The test relay + fixture events + test signer is the complete test environment for most views. ContextVM mocking is needed only for assistant/secret-reveal/log-fetch tests.

### 10.3 Harness structure (after Phase 4)

```
web/tests/e2e/
├── relay-harness.js          # Start bahia-test-relay, seed, query
├── e2e-keyring.js            # Test keys (unchanged)
├── test-signer.js            # NEW: NIP-07 mock signer from keyring
├── intent-helpers.js         # NEW: sign+publish intents, simulate daemon response
├── membership-fixtures.js    # NEW: gift-wrapped membership events
├── cp-state-fixtures.js      # Existing, extended with all domains
├── harnesses/
│   ├── store-first-boot.js   # NEW: tests store-first render with pre-seeded relay
│   ├── intent-crud.js        # NEW: end-to-end intent lifecycle
│   └── encrypted-intent.js   # NEW: NIP-59 gift-wrapped intent tests
└── *.spec.js                 # Test files (updated)
```

---

## 11. Deletions

Phase 4 deletes these files and code paths, replacing each with the store-first architecture:

| File / code path | Finding | Replaced by |
|-----------------|---------|-------------|
| `stores/collections/indexeddb-cache.js` | A-3 | welshman IndexedDB event store |
| `stores/collections/index.svelte.js` snapshot/persist half | A-3, A-7 | Derived store queries |
| `stores/controlplane/bootstrap.svelte.js` | A-4, A-19 | Boot sequence §7 |
| `stores/controlplane/connection.svelte.js` | A-4 | welshman pool connection state |
| `stores/controlplane/events.svelte.js` | A-7, A-22 | Single ingestion path §2.4 |
| `stores/controlplane/index.js` | — | Deleted with the directory |
| `components/AuthGuard.svelte` REST probe path | A-1, A-2 | Role-gated UI §6.3 |
| `auth.svelte.js` `configureBackendAuth`, `compatibilityPatch`, `hydrateAuthMetadata` one-shots | A-1, A-2 | Background auth §6.2 |
| `auth/capabilities.js` `supportsDirectNip98Auth` | A-1 | Deleted (no REST auth) |
| `nostr/retained-domain-subscription.js` | A-9 | Store-backed subscriptions |
| `nostr/pool-client.js` (`PoolBackedClient`) | A-13 | welshman pool |
| `nostr/pool-subscriptions.js` | A-13 | welshman subscriptions |
| `nostr/pool-publish.js` | A-13 | welshman publish + outbox |
| `nostr/pool-utils.js` (most) | A-13 | welshman utilities |
| `nostr/pool.js` | A-13 | welshman pool |
| `nostr/encrypted-controlplane.js` (CRUD methods) | A-8, A-10 | Signed intents |
| `nostr/encrypted-controlplane-transport.js` (CRUD) | A-11 | Signed intents |
| `nostr/encrypted-controlplane-constants.js` (CRUD kinds) | A-11 | Keep only assistant/secret/log kinds |
| `stores/public-controlplane.svelte.js` (`publishCommand` and per-domain command methods) | A-10 | Signed intents per §3 |
| `stores/orgs.svelte.js` (RPC reads) | A-8 | Store query + membership events |
| `stores/payments.svelte.js` (RPC reads) | A-8 | Store query (daemon publishes payment state) |
| `stores/notifications.svelte.js` (RPC reads) | A-8 | Store query (daemon publishes notification config) |
| `stores/security.svelte.js` (RPC reads) | A-8 | Store query (daemon publishes findings) |
| `stores/artifact-signatures.svelte.js` (RPC reads) | A-8 | Store query |
| `nostr/relay-settings-controlplane.js` (RPC reads) | A-8 | Store query |
| `nostr/dns-controlplane.js` (RPC reads) | A-8 | Store query |
| `lib/api/client.js` | A-30 | Deleted entirely (no REST) |
| Ad-hoc localStorage caches: discovery, docs, assistant transcripts, auth session relay queries | A-29 | Event store |
| `stores/index.svelte.js` `loadX()` aliases | A-20 | Deleted (no load gates) |
| `__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS` bypass | C-25 | Test signer with real signatures |
| `subscribeToDomainRefresh` / `createCoalescedRefresh` | A-9 | Store-backed reactive views |

---

## 12. Slice Plan

### Wave 1: Foundation — store, pool, boot, auth

| Slice | Goal | Done-when | Key files (ownership) | Parallel-safe with | Deletes |
|-------|------|-----------|----------------------|--------------------|---------|
| **W1-S1: Event store + welshman pool** | Single welshman pool, IndexedDB event store with namespace, cursor table, NIP-09/NIP-40 handling, single ingestion path | Store persists events across page reloads. Cursor persists per (relay, filter). NIP-09 kind-5 events remove tombstoned ids. NIP-40 sweep runs on open | `lib/nostr/store.js` (new), `lib/nostr/pool-welshman.js` (new), `lib/nostr/ingestion.js` (new) | W1-S2, W1-S3 | Nothing yet (parallel layer) |
| **W1-S2: Store-first boot** | Boot sequence §7: render from store immediately, connect pool in background, EOSE as badge not gate, batch apply per frame | Opening a tab with a seeded store shows services immediately without network. "Syncing…" indicator appears until first EOSE. No `setAllLoading(true)` | `routes/+layout.svelte` (boot init), `lib/nostr/boot.js` (new), `lib/stores/sync-status.svelte.js` (new) | W1-S1 (needs store), W1-S3 | `stores/controlplane/bootstrap.svelte.js`, `stores/controlplane/connection.svelte.js`, `stores/controlplane/events.svelte.js`, `stores/controlplane/index.js`, `stores/collections/indexeddb-cache.js`, `stores/index.svelte.js` `loadX()` aliases, `{#if loading…}` blocks in all route pages |
| **W1-S3: Auth without REST** | Auth bootstrap §6.2: persisted session = authenticated, background signer verify, no REST probe, no discovery gate, AuthGuard replaced by role check | Protected routes render without daemon online. Signer restoration is non-blocking. No `backendAuthenticated` flag | `lib/stores/auth.svelte.js` (rewrite auth bootstrap), `lib/stores/auth-roles.svelte.js` (new: membership-derived roles), `components/AuthGuard.svelte` (replace with role check) | W1-S1 (needs store for membership events), W1-S2 | `auth.svelte.js`: `configureBackendAuth`, `compatibilityPatch`, `hydrateAuthMetadata`, `authMetadataClient` creation. `auth/capabilities.js`: `supportsDirectNip98Auth`. `AuthGuard.svelte`: REST probe logic. `stores/system.svelte.js`: discovery as auth gate |

### Wave 2: Derived views — read path migration

| Slice | Goal | Done-when | Key files (ownership) | Parallel-safe with | Deletes |
|-------|------|-----------|----------------------|--------------------|---------|
| **W2-S1: Core domain views** | Services, environments, states, policies, packages as derived store queries. Topic-tag routing §8.2 | All core domain list/detail pages render from the event store. No `refreshCollections()` on each event. O(1) ingestion per event | `lib/stores/collections/services.svelte.js` (rewrite), `environments.svelte.js` (rewrite), `deployments.svelte.js` (rewrite), domain-specific query modules | W2-S2, W2-S3 | `stores/collections/index.svelte.js` rebuild/persist machinery, per-collection `applyXEvent` functions, `replaceSnapshotArray`, `refreshCollections`, `schedulePersistCachedCollections` |
| **W2-S2: Worker + ops views** | Workers, Loom jobs, operations as store queries. Worker adverts from open-author subscription (with bounded LRU) | Worker list renders from store. Job timeline renders from store. No duplicate subscriptions | `lib/stores/collections/workers.svelte.js` (rewrite), `operations.svelte.js` (rewrite) | W2-S1, W2-S3 | Per-worker `subscribeOnRelays` calls, unbounded `seenEvents` Sets |
| **W2-S3: Activity + backup + ML + SBOM views** | Activity feed, backup state, ML endpoints, SBOM as store queries | All remaining domain views render from store | `lib/stores/collections/activity.svelte.js` (rewrite), `backup.svelte.js` (rewrite), `ml.svelte.js` (rewrite), `sbom.svelte.js` (rewrite) | W2-S1, W2-S2 | Remaining per-domain `applyXEvent` paths |

### Wave 3: Intent signing — write path migration

| Slice | Goal | Done-when | Key files (ownership) | Parallel-safe with | Deletes |
|-------|------|-----------|----------------------|--------------------|---------|
| **W3-S1: Plaintext intent signing** | Service/environment/policy/package CRUD via signed 30900 intents. Pending intent store. Outbox with OK tracking. Status reconciliation via 30315 | Creating a service signs an intent, publishes it, shows "pending", and resolves when 30315 accepted arrives. Conflict handling works. Retry works | `lib/nostr/intent-signer.js` (new), `lib/nostr/outbox.js` (new), `lib/stores/pending-intents.svelte.js` (new), route-level form submission handlers | W3-S2 | `stores/public-controlplane.svelte.js` command methods for: service/*, environment/*, policy/*, package/*. `nostr/encrypted-controlplane.js` CRUD request paths |
| **W3-S2: Encrypted intent signing** | Org membership, secret config, notification channel CRUD via NIP-59 gift-wrapped intents. Signer capability check for NIP-44 | Org invite signs a gift-wrapped intent. Secret config (not value) signs a gift-wrapped intent. NIP-44 capability check disables actions when unsupported | `lib/nostr/intent-giftwrap.js` (new), route handlers for orgs/secrets/notifications | W3-S1 | `stores/orgs.svelte.js` RPC reads/mutations. `stores/notifications.svelte.js` RPC reads/mutations. `stores/service-secrets.svelte.js` RPC mutations (value reveal stays ContextVM) |
| **W3-S3: DNS/LLM/backup/ML/worker intent signing** | Remaining domain CRUD via signed intents | All mutation domains sign intents. No ContextVM CRUD remains except assistant/secret-reveal/log-fetch | `nostr/dns-controlplane.js` (rewrite mutations), route handlers for DNS/LLM/backup/ML/workers | W3-S1, W3-S2 | `nostr/dns-controlplane.js` RPC mutations. `nostr/relay-settings-controlplane.js` RPC reads. `stores/security.svelte.js` RPC reads. `stores/payments.svelte.js` RPC reads. `stores/artifact-signatures.svelte.js` RPC reads. `stores/deployment-run-logs.svelte.js` (log fetch stays ContextVM) |

### Wave 4: Cleanup — deletions, pool consolidation, test migration

| Slice | Goal | Done-when | Key files (ownership) | Parallel-safe with | Deletes |
|-------|------|-----------|----------------------|--------------------|---------|
| **W4-S1: Pool consolidation + cleanup** | Delete all extra pools, legacy subscription code, retained-domain-subscription, ad-hoc caches, REST client | Only one welshman pool exists. No `PoolBackedClient`. No `SimplePool` import outside welshman. No `lib/api/client.js`. No ad-hoc localStorage event caches | Sweep across all `lib/nostr/*.js` and `lib/stores/*.js` | W4-S2 | `nostr/pool.js`, `nostr/pool-client.js`, `nostr/pool-subscriptions.js`, `nostr/pool-publish.js`, most of `nostr/pool-utils.js`, `nostr/retained-domain-subscription.js`, `nostr/connection-guard.js`, `lib/api/client.js`, `stores/discovery.svelte.js` localStorage cache, `stores/assistant.svelte.js` localStorage transcript cache, `docs/nostr.js` localStorage cache, `nostr/subscriptions.js` relay override |
| **W4-S2: E2E test migration** | Delete `__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS`. All tests use real signatures via test signer. Intent lifecycle tests. Membership fixture tests | All 196 Playwright tests pass with real signatures. No signature bypass in production code. New tests cover: store-first boot, intent CRUD, pending→confirmed lifecycle, encrypted intent, role derivation | `tests/e2e/test-signer.js` (new), `tests/e2e/intent-helpers.js` (new), `tests/e2e/membership-fixtures.js` (new), harness updates, fixture updates | W4-S1 | `pool-subscriptions.js:45` bypass, `validation.js:28` bypass, `helpers.js` mock relay trust setup |

---

## 13. Acceptance Tests

### Per-wave acceptance criteria

**Wave 1:**
1. Open a tab with a pre-seeded IndexedDB store (offline). Services list renders immediately with no network. No spinner. No "Checking authentication…".
2. Connect to the relay. Events flow in. The "syncing…" indicator changes to "live" after EOSE. Previously rendered data is not wiped.
3. Log in with NIP-07. The session persists. Close and reopen the tab: authenticated immediately, no REST probe. Protected routes render without the daemon.
4. With the daemon offline, all relay-public views render. Only mutation buttons and confidential views are disabled.

**Wave 2:**
5. Navigate between services, environments, workers, DNS. No re-fetch. No spinner on return. Views are derived from the store.
6. A live 30900 event from the relay appears in the relevant view within one animation frame. No `refreshCollections()` cascade.
7. Kind 5 deletion from the service pubkey removes the entity from the view.

**Wave 3:**
8. Submit a service create form. A signed `30900` intent appears on the relay. The UI shows "pending". The daemon publishes a canonical `30900` + `30315 status=accepted`. The UI transitions to confirmed.
9. Submit an environment update with stale `expected_updated_at`. The daemon publishes `30315 status=conflict`. The UI shows a conflict indicator.
10. Submit a secret config update. A NIP-59 gift-wrapped event appears on the relay. The daemon processes it.
11. With a signer that lacks NIP-44: the "Create Secret" button is disabled with an explanatory tooltip.

**Wave 4:**
12. All 196+ Playwright tests pass with no `__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS` global.
13. `grep -r 'BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS' web/src/` returns nothing.
14. `grep -r 'SimplePool\|PoolBackedClient' web/src/lib/` returns nothing (only in welshman internals).
15. `grep -r 'lib/api/client' web/src/` returns nothing.

---

## 14. Decisions (binding)

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| 1 | Library choice | **welshman** (`@welshman/net` + `@welshman/store` + `@welshman/signer` + `@welshman/util`) | Svelte-native, replaces pool + subscriptions + cursor + store + AUTH + dedup in one package (§1.2) |
| 2 | Store namespace | **`bahia-events-<service_pubkey[:8]>`** per deployment | Avoids cross-deployment collisions, scoped to the fleet identity (§2.1) |
| 3 | TTL policy | **No TTL.** LRU by size for regular events; addressable/replaceable never evicted | TTL was the direct cause of cold starts (A-3). Size-based eviction preserves current state (§2.3) |
| 4 | Intent signing | **Always sign intents for all domains.** Fall back to ContextVM only on relay rejection | Decouples web deployment from daemon domain enablement. Dual-dispatch handles the gap (§9.2) |
| 5 | Auth model | **Persisted session = authenticated immediately.** No REST probe. No discovery gate. Roles from relay membership events | Eliminates A-1, A-2. Protected routes render without the daemon (§6) |
| 6 | Render gate | **None.** Render from store immediately. EOSE = "synced" badge | Eliminates A-4, A-19, A-20. Empty store = empty list, not a spinner (§7) |
| 7 | Signature bypass | **Delete `__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS`.** Tests use real signatures from test keyring | Eliminates C-25 security concern. Test signer injection replaces the bypass (§10.2) |
| 8 | Content key persistence | **Never persisted.** Content keys for confidential state live in memory only | Loss = re-derive on next login. Avoids IndexedDB as a key store attack surface (§5.4, C1-R5) |
| 9 | ContextVM retention | **Assistant, secret reveal, log fetch only.** All other reads and mutations are relay/intent | Closes the dual-path window. ContextVM is interactive RPC, not CRUD (§4) |
| 10 | Pending intent timeout | **60 seconds** before retry; **3 retries** with exponential backoff (10s, 30s, 90s) | Balances responsiveness against relay/daemon latency (§3.5) |

---

## 15. Open Questions

| # | Question | Impact | Needs decision from |
|---|----------|--------|-------------------|
| Q1 | **welshman Svelte 5 runes compatibility.** welshman was built for Svelte 4 stores (`writable`/`derived`). Bahia uses Svelte 5 runes (`$state`, `$derived`). Does welshman's `@welshman/store` work with runes, or do we need an adapter layer? | Wave 1 scope: if an adapter is needed, it's a day of work but must be designed up front | Engineering (spike task) |
| Q2 | **C1 key-wrapping scheme finalization.** The design assumes NIP-44 per-member wrapping with key-reference tags (§5.4). If C1 chooses a different scheme (e.g., NIP-04, shared secret derivation), the decrypt path changes | Wave 1 W1-S3 and Wave 3 W3-S2 | C1 slice (bahia-irsry.11.20) |
| Q3 | **Payment and security state events.** Phase 3 does not explicitly define when the daemon publishes payment history and security findings as relay events. Until it does, the web cannot subscribe to them — these views remain blank or need an interim ContextVM read | Wave 2 W2-S3, Wave 3 W3-S3 | Phase 3 slice plan (new domain slices or daemon publish tasks) |
| Q4 | **NIP-65 outbox routing.** Should the web use NIP-65 (kind 10002) relay lists for outbox-model publishing (write to the user's write-relays, read from their read-relays)? Today the web publishes only to the deploy-seed relays | Affects intent publishing reliability but adds complexity | User/architect |
| Q5 | **IndexedDB quota on mobile Safari.** Safari limits IndexedDB to ~1 GB but may evict under storage pressure. Should the store request persistent storage (`navigator.storage.persist()`)? | Affects mobile reliability; low priority for fleet dashboard | User/architect |

---

## 16. Risks

- **welshman API stability.** welshman is actively developed for Coracle and may have breaking changes. Mitigation: pin exact versions; the adapter layer (if needed for Svelte 5) provides an abstraction boundary.
- **C1 key scheme not finalized.** If C1's key-wrapping scheme changes materially, the web decrypt path (§5.3) must be redesigned. Mitigation: the decrypt path is isolated in `auth-roles.svelte.js` and `intent-giftwrap.js`; the rest of the architecture is unaffected.
- **Daemon domain enablement lag.** If Phase 3 domain slices are slower than Phase 4 web slices, some web domains will fall back to ContextVM (§9.2). This is by design — the fallback is transparent and the cut-over is per-domain.
- **Test relay startup time.** The `bahia-test-relay` takes ~2 seconds to start. With all 196 tests needing a real relay, the test suite may slow down. Mitigation: share one relay process per test file (Playwright's `globalSetup`), not per test.
- **Bundle size.** Adding welshman (+~17 KB gzipped after deleting the current pool layer) may push the initial bundle over performance budgets. Mitigation: welshman packages support tree-shaking; unused modules (e.g., `@welshman/app` which is Coracle-specific) are not imported.

---

## 17. Relationship to Other Issues

| Issue | Relationship |
|-------|-------------|
| bahia-irsry.11 (Phase 3) | **Depends on.** Phase 3 provides the daemon intent pipeline, TrustSet, and membership events that Phase 4 consumes |
| bahia-irsry.11.20 (C1 crypto) | **Depends on.** C1 provides the key-wrapping scheme for confidential state decrypt (§5.4) |
| bahia-irsry.11.19 (R1: delete ContextVM CRUD) | **Blocks.** Phase 4 must close the dual-path window before ContextVM CRUD handlers can be deleted |
| bahia-irsry.13 (Phase 5: CLI/pkg/client/MCP) | **Parallel.** Phase 5 is the CLI equivalent of Phase 4. They share the daemon's intent pipeline but have no web-side dependency |
| bahia-irsry.14 (audit coverage gaps) | **Informs.** Phase 4 deletions close many of the web-tier audit findings; the coverage-gap audit may find new ones |
