# Phase 3: Daemon Authority Inversion — Design

- Status: proposed (2026-10-01)
- Issue: bahia-irsry.11
- Depends on: bahia-irsry.10 (Phase 2, done), bahia-irsry.35 (client-minted entity ids, done)
- Blocks: bahia-irsry.12 (Phase 4, web), bahia-irsry.13 (Phase 5, CLI/pkg/client/MCP)
- Audit: `docs/investigations/nostr-first-architecture-audit-2026-09-29.md` — findings B-1, B-5, B-6, B-8–B-11, B-16, B-17, B-20–B-27, C-34; root causes RC-1, RC-4, RC-6
- Charter: `docs/architecture.md` invariants 1–7
- Phase 2 baseline: local bbolt store, per-(relay, filter) cursors, NIP-77 sync, single relay stack, client-minted UUIDv7 entity ids, shared control-state record builder (`control_state_contract.go`), `Publisher.PublishBeforeCommit`

---

## 1. Intent Events

### 1.1 Kind and shape

A client intent is a **kind `30900` addressable replaceable event** signed by the operator/user, with `d` = the entity coordinate. It uses the same kind as the daemon's canonical state projection. This is deliberate: the relay naturally keeps only the latest replacement, and a client's desired state and a daemon's confirmed state share one coordinate. The daemon's confirmed-state event _replaces_ the client's intent once processing succeeds, because the daemon signs with the service pubkey and relays scope replaceable events by `(kind, pubkey, d)` — so the two live in separate coordinate spaces.

**Decision: client intents are kind `30900`, signed by the operator.**

The alternative was a separate "intent" kind (e.g. a new kind or reusing `25910`). Using `30900` is better because:
1. The intent _is_ the desired state — it contains the same fields as the canonical state record.
2. The relay's built-in replaceable semantics give us latest-wins for free.
3. Readers can discover both the operator's desired state and the daemon's confirmed state with one kind, scoped by author.
4. No new kind allocation needed.

### 1.2 Intent event structure

```json
{
  "kind": 30900,
  "pubkey": "<operator-hex-pubkey>",
  "created_at": 1727740800,
  "tags": [
    ["d", "<entity-coordinate>"],
    ["domain", "service"],
    ["schema", "bahia.intent.service.v1"],
    ["t", "bahia-intent"],
    ["t", "service-registry"],
    ["op", "create"],
    ["org", "<org-uuid>"],
    ["intent_id", "<uuidv7>"]
  ],
  "content": "{\"id\":\"<entity-uuid>\",\"name\":\"api\",\"repo_url\":\"...\",\"runtime_type\":\"compose\",\"org_id\":\"<org-uuid>\"}"
}
```

**Tag grammar:**
- `d` = entity coordinate, per existing `docs/event-spec.md` grammar (e.g. `<service-id>` for services, `<environment-id>` for environments, `service:<sid>:environment:<eid>` for state).
- `domain` = the domain family (`service`, `environment`, `policy`, `llm`, `dns`, `backup`, `ml`, `package`, `org`, `secret`, `notification`).
- `schema` = `bahia.intent.<domain>.v1`. Distinct from the daemon's state schema `bahia.cp-state.v1`.
- `t` = `bahia-intent` (enables `#t` filtering for all intents) plus the domain topic tag (e.g. `service-registry`).
- `op` = `create`, `update`, or `delete`.
- `org` = the org UUID the entity belongs to (enables `#org` filtering for trust-scoped subscriptions).
- `intent_id` = a UUIDv7 minted by the client per intent attempt, carried in both tags and content. This is the idempotency and correlation key.

### 1.3 Relationship to today's cp-state 30900 records

Today the daemon publishes kind `30900` with `pubkey = <service-pubkey>` and schema `bahia.cp-state.v1`. The shared builder in `internal/adapters/nostr/control_state_contract.go` produces the envelope.

Phase 3 intents are also `30900` but signed by the **operator** (a different pubkey). The daemon's confirmed state replaces nothing of the operator's, and vice versa. Both coexist on relays:
- **Operator intent**: `(30900, operator-pubkey, d=<service-id>)` — desired state.
- **Daemon canonical state**: `(30900, service-pubkey, d=<service-id>)` — confirmed state.

The ContextVM create/update/delete handlers (`internal/controlplane/encrypted_route_handlers.go`) are replaced per slice. The client no longer sends `25910` for these mutations; it signs and publishes a `30900` intent directly.

### 1.4 Idempotency and conflicts

**Same-coordinate conflict resolution: latest-wins by `(created_at, lowest event id)`.**

When two operators sign intents for the same coordinate concurrently:
1. The relay keeps only the newest replacement per `(kind, pubkey, d)`.
2. The daemon processes intents in `(created_at, event.id)` order — lowest `created_at` first, lowest hex `id` as tie-break (NIP-01 semantics).
3. For creates: the first processed create wins. A later create for the same id with different content gets a rejection status event (JSON-RPC `-32010` equivalent).
4. For updates: latest-wins. The daemon's subscription delivers the newest intent. If the operator included `expected_updated_at` (the revision token from the current state), the daemon checks it before applying. A stale revision → rejection status event with reason `revision_conflict`.

**Intent-level idempotency:**
- Each intent carries `intent_id` (a UUIDv7). The daemon stores processed intent ids in its local bbolt store.
- If the daemon sees an intent with a previously processed `intent_id`, it skips re-processing and does not re-publish a status event. This handles retries and relay catch-up after restart.
- The `intent_id` is stored durably because the local store survives restarts, and the daemon can check it during replay.

**Revision tokens for updates:**
The existing `expected_updated_at` mechanism from `RegistryService.UpdateService` (`internal/service/registry.go:264`) maps directly:
- The update intent's content includes `expected_updated_at` = the `updated_at` from the latest daemon state event the client has seen.
- The daemon compares it against its local state. Mismatch → rejection status event.
- This is optimistic concurrency: read the latest state from the relay, modify it, sign a new intent with the old revision. The daemon validates.

### 1.5 Deletion

**Decision: intent with `op=delete` plus a `deleted: true` content flag, not NIP-09.**

Rationale:
- NIP-09 (kind 5) deletes by `e` (event id) or `a` (coordinate). It asks the relay to remove events. But we _want_ the daemon's last canonical state to persist as a tombstone (with `deleted: true`) so that consumers know the entity was deliberately removed, not just absent.
- The existing tombstone convention (`content.deleted = true` in 30900 records) is already used by all consumers (`docs/event-spec.md`, `web/src/lib/nostr/replaceable.js`, projector tombstone paths).
- The operator publishes a delete intent: `op=delete`, `content: {"deleted": true, "id": "<entity-id>"}`. The daemon processes it, performs side effects (stop runtime, cleanup DNS), and publishes its own `30900` with `deleted: true` under the service pubkey.
- Relay storage is bounded: the deleted tombstone replaces the live record on the same coordinate, so no growth.

### 1.6 Encryption for sensitive domains

Secrets, org membership, and notifications are sensitive. Their intents use **NIP-59 gift-wrap (kind `1059`)** around the inner `30900` intent:

```json
{
  "kind": 1059,
  "pubkey": "<random-ephemeral-pubkey>",
  "tags": [["p", "<bahia-service-pubkey>"]],
  "content": "<nip44-encrypted-inner-30900-intent>"
}
```

The inner event is a `30900` intent signed by the operator, NIP-44-encrypted to the service pubkey. The daemon unwraps it using its existing `EncryptedRequestTransport.unwrapContextVMEvent` logic (already handles `1059` and `21059`).

For secret _values_, the content is NIP-44-encrypted to the service pubkey within the inner event's content field (double encryption: gift-wrap protects the intent metadata, NIP-44 within content protects the secret value at rest on the daemon's local store).

The daemon's published state for secrets is also encrypted: a `30900` record whose content is NIP-44-encrypted to the org's member pubkeys (so the web can decrypt and display). This uses the existing pattern from `internal/service/assistant_transcript_store.go` (symmetric-key AEAD with key-reference tags).

---

## 2. Authority and Trust

### 2.1 Today's authorization model

Two authorization layers exist:
1. **Reactor operator gate** (`internal/controlplane/reactor.go:1742–1761`): a static list of `AuthorizedPubkeys` from daemon config. Used for deploy/rollback/restart/stop/cordon and other operational commands. Fail-closed: unknown pubkeys are rejected.
2. **Tenant RBAC** (`internal/controlplane/encrypted_tenant_authorization.go`, `internal/auth/rbac.go`): checks org membership and role-based permissions via Postgres `OrgMemberLookup`. Used by ContextVM handlers for service/environment/secret/notification CRUD. Roles: viewer < deployer < admin < owner. Permissions: `services:write`, `environments:write`, `secrets:write`, `deployments:write`, etc.

Both depend on Postgres: the reactor reads config (fine, config stays), but tenant RBAC queries the `org_members` table.

### 2.2 Target: event-derived trust

**Decision: org membership becomes addressable encrypted events. The daemon builds its trust set from these events, not from Postgres.**

#### Membership event model

Org membership is modeled as **addressable kind `30900`** events signed by the **org owner or admin** (someone who already has `members:manage` permission), encrypted in a NIP-59 gift wrap.

The inner event:
```json
{
  "kind": 30900,
  "pubkey": "<owner-or-admin-pubkey>",
  "tags": [
    ["d", "org:member:<org-id>:<member-pubkey>"],
    ["domain", "org"],
    ["schema", "bahia.intent.org-member.v1"],
    ["t", "bahia-intent"],
    ["t", "org-member"],
    ["op", "set"],
    ["org", "<org-id>"],
    ["p", "<member-pubkey>"]
  ],
  "content": "{\"org_id\":\"<org-id>\",\"pubkey\":\"<member-pubkey>\",\"role\":\"admin\"}"
}
```

**Bootstrapping the first owner:** The daemon config already carries `AuthorizedPubkeys`. The first owner's pubkey is set in daemon config as the bootstrap identity. On first start (no membership events on relays), the daemon trusts this pubkey for all operations on the configured org. When the first owner publishes a membership event for themselves (`role: owner`), the event becomes the authority and the config acts as a fallback.

Config structure (already exists, extended):
```yaml
nostr:
  authorized_pubkeys: ["<hex-pubkey>"]  # existing — reactor operator gate
  bootstrap_org_owner: "<hex-pubkey>"   # new — initial org owner before relay events
org:
  id: "<org-uuid>"                       # new — the daemon's org (single-tenant today)
```

#### How the daemon builds and updates its trust set

The daemon subscribes with a long-lived REQ:
```json
{
  "kinds": [1059],
  "#p": ["<bahia-service-pubkey>"],
  "since": "<cursor>"
}
```

It unwraps gift-wrapped membership events and builds an in-memory trust set:
```go
type TrustSet struct {
    mu      sync.RWMutex
    members map[string]map[string]domain.Role // org-id → pubkey → role
}
```

The trust set is:
1. **Initialized** from the daemon config's `bootstrap_org_owner` and `authorized_pubkeys` on startup.
2. **Hydrated** from the local bbolt store (which has membership events from Phase 2's subscriber).
3. **Kept current** by the live subscription — new membership events update it immediately.

Authorization checks replace `auth.RBAC.CheckPermission` with `TrustSet.HasPermission(orgID, pubkey, permission)`, which maps roles to permissions using the existing `domain.RolePermissions` table (`internal/domain/tenant.go:112–163`).

#### What happens to intents from untrusted authors

1. The daemon receives an intent from `pubkey X`.
2. It checks `TrustSet.HasPermission(intent.org, X, requiredPermission)`.
3. If X is not in the trust set or lacks the permission, the daemon publishes a **rejection status event**:
```json
{
  "kind": 30315,
  "pubkey": "<service-pubkey>",
  "tags": [
    ["d", "intent-status:<intent_id>"],
    ["domain", "intent"],
    ["status", "rejected"],
    ["e", "<intent-event-id>"],
    ["p", "<requester-pubkey>"],
    ["t", "intent-status"]
  ],
  "content": "{\"intent_id\":\"<intent_id>\",\"reason\":\"unauthorized\",\"detail\":\"pubkey lacks services:write in org <org-id>\"}"
}
```
4. The intent is stored in the local store (with a "rejected" marker) for idempotency, and the rejection status is published exactly once.

#### How today's ContextVM/operator authorization maps onto it

| Today | Phase 3 |
|-------|---------|
| `Reactor.isAuthorized(pubkey)` — config pubkey list | **Config pubkeys stay** as the operator gate for fleet-level operations (worker cordon/drain, maintenance). These are distinct from org membership |
| `encryptedTenantAuthorizer.authorizeOrg` — Postgres org member lookup | `TrustSet.HasPermission(orgID, pubkey, perm)` — local store membership events |
| `encryptedTenantAuthorizer.authorizeService` — load service from DB, check org | Intent carries `org` tag; daemon checks `TrustSet` directly. Service-to-org mapping comes from the daemon's local state (the canonical `30900` service record the daemon itself published) |
| `FleetOperatorGate` — config pubkey list for specific methods | Stays for fleet-scoped ops (policy eval, worker maintenance, saga recovery). Not replaced by org membership |

#### Ordering: minimal trust-set must come first

The orgs/membership slice is ordered last in the issue because it changes the most code. But **a minimal trust-set mechanism must land in the foundation slice** so that every other slice can authorize intents:

1. **Foundation (Wave 1):** `TrustSet` initialized from config only (`authorized_pubkeys` + `bootstrap_org_owner` + the daemon's own pubkey). The trust set check replaces `isAuthorized` for intent processing. Org membership events are not yet consumed.
2. **Membership slice (final wave):** Subscribe to membership events, hydrate the trust set from them, and retire the Postgres org member lookup.

In the interim, the daemon trusts config pubkeys and the bootstrap owner for all intents. This is no less secure than today (config pubkeys are the only authorized operators), and it unblocks every other slice.

---

## 3. Daemon Reaction

### 3.1 Subscription model

The daemon opens **one long-lived REQ per input class** against its relay pool, using Phase 2's per-(relay, filter) cursors:

```go
// Intent subscription: operator-signed desired-state events
intentFilter := nostr.Filter{
    Kinds:   []int{30900},
    Tags:    nostr.TagMap{"#t": {"bahia-intent"}},
    Since:   &cursor,  // from localstore per-(relay, filter) cursor
}
```

The `#t=bahia-intent` tag on every intent event means one filter catches all domains. The daemon does not need per-domain subscriptions for intents.

For encrypted intents (secrets, membership):
```go
encryptedIntentFilter := nostr.Filter{
    Kinds: []int{1059},
    Tags:  nostr.TagMap{"#p": {servicePubkey}},
    Since: &cursor,
}
```

### 3.2 Processing pipeline

For each new intent event the subscription delivers:

```
1. Deduplicate
   └─ Check intent_id in local store → skip if already processed

2. Validate
   ├─ Verify signature (already done by the subscriber, Phase 2)
   ├─ Parse domain, op, schema from tags
   ├─ Validate content schema (id format, required fields, UUIDv7/v4)
   └─ For updates: check expected_updated_at against local state

3. Authorize
   ├─ Resolve org from intent's org tag
   ├─ Check TrustSet.HasPermission(org, pubkey, permission)
   └─ Reject → publish rejection status event (30315)

4. Apply side effects
   ├─ service/create → validate name uniqueness, store in local state
   ├─ service/deploy → create deployment intent, queue to runtime
   ├─ environment/delete → stop deployments, cleanup DNS
   ├─ dns/zone-create → configure backend
   ├─ backup/run → queue backup job
   └─ [domain-specific side effects]

5. Update local state
   ├─ Store the processed intent_id in localstore (idempotency)
   └─ Update Postgres index if configured (optional derived cache)

6. Publish canonical state event
   ├─ Build 30900 record using control_state_contract.go shared builder
   ├─ Compare fingerprint against latest stored output for (kind, service-pubkey, d)
   ├─ Skip if identical (no churn — replaces B-3/B-4 dedupe)
   └─ Sign and publish with OK verification via Publisher
```

### 3.3 Where Postgres fits per slice

**Decision: Postgres becomes an optional derived write-behind index, populated _after_ the intent is processed and the canonical state event is published.**

Per slice:
- **Services/environments (first slice):** The relay-first registry already publishes before the DB write. Phase 3 inverts the direction: the daemon subscribes to intents, processes them, publishes canonical state, and _then_ upserts the Postgres row as a derived index. If the DB write fails, nothing is lost — the canonical state is on the relay and in the local store.
- **DNS:** The DNS reconciler today polls Postgres for zones/endpoints. Phase 3: the reconciler subscribes to DNS state events from the local store. Postgres DNS tables become a read-only query index rebuilt from events.
- **LLM/ML/backups/packages:** Same pattern. The coordinator subscribes to intents, processes them, publishes results. Postgres is a query cache for REST/MCP compatibility reads during the dual-path window.
- **Orgs/secrets/notifications:** No Postgres at all for new writes once the membership event model is live. Existing rows are backfilled as events (§4).

### 3.4 Canonical state record and who signs it

**Decision: the daemon signs the canonical state event, not the client.**

Rationale:
- The daemon performs validation, authorization, side effects (runtime deploy, DNS zone creation), and observation. The canonical state reflects _confirmed_ reality, not just _desired_ state.
- A client's intent is a request. The daemon's canonical state is the response: "I validated your intent, applied it, and this is the confirmed state."
- Separation of pubkeys is crucial: `(30900, operator-pubkey, d)` = intent, `(30900, service-pubkey, d)` = confirmed state. Any reader can distinguish between "what the operator wants" and "what the daemon has confirmed."
- The daemon adds fields the client cannot know: `created_at` timestamp (minted at processing time), observation metadata, deployment-unit ids, applied runtime state.

The canonical state event references the intent that caused it:
```json
{
  "kind": 30900,
  "pubkey": "<service-pubkey>",
  "tags": [
    ["d", "<entity-coordinate>"],
    ["domain", "service"],
    ["schema", "bahia.cp-state.v1"],
    ["e", "<intent-event-id>", "", "reply"],
    ["legacy_kind", "31975"],
    ...existing envelope tags...
  ],
  "content": "{...confirmed state fields...}"
}
```

### 3.5 Crash and retry semantics

**Crash between steps 4 and 6 (side effect applied, state not published):**
- On restart, the daemon replays intents from its local store cursor.
- The `intent_id` is not yet marked processed (step 5 happens after side effects).
- The daemon re-processes the intent. Side effects must be idempotent:
  - Service/environment creates: `resolveCreateByID` detects the existing entity → idempotent replay.
  - Deployments: the runtime's `desired_hash` comparison detects no change → no-op.
  - DNS: zone configs are applied by serial; re-applying the same serial is idempotent.
  - Backups: job ids are UUIDv7; re-queuing the same id → duplicate detection.

**Crash between steps 5 and 6 (intent marked processed, state not published):**
- On restart, the daemon skips the intent (already processed).
- The canonical state event was not published. The local store has the updated state but the relay does not.
- **Recovery:** After replay, the daemon does a "warm start" comparison (§5): it compares its local state against relay-held state. Any local state newer than relay state triggers a publish. This handles the gap.

**Outbox durability (Phase 2):**
- `Publisher.PublishBeforeCommit` puts the signed event into the bbolt outbox before any relay attempt.
- Per-relay `OK` tracking resumes on restart.
- A publish that crashes mid-delivery resumes exactly where it stopped.

---

## 4. Migration and Coexistence

### 4.1 Dual-path window

During migration, both the old ContextVM mutation path and the new intent path must work. The strategy is **feature-flagged dual dispatch**:

```yaml
nostr:
  intent_domains:
    - service     # accepts intent events for services
    - environment # accepts intent events for environments
```

When a domain is listed in `intent_domains`:
- The daemon subscribes to `30900` intents with `#t=bahia-intent` and `#domain=<domain>` from trusted authors.
- The daemon still accepts ContextVM `25910` mutations for that domain (during the transition).
- The ContextVM handler, when invoked for a domain in `intent_domains`, converts the ContextVM request into an intent event, publishes it to the relay, and returns a response. This means existing web/CLI/MCP clients keep working without changes.

When a domain is _not_ in `intent_domains`:
- The existing ContextVM handler processes it as today.

This dual-path window closes per domain when:
1. The web (Phase 4) and CLI (Phase 5) sign intents directly.
2. The ContextVM handler for that domain is deleted.

### 4.2 Data backfill from Postgres to relay intents/state

For existing Postgres rows with no relay intent:

**One-shot backfill tool** (offline, run once per domain migration):
1. Read all entities from Postgres for the domain.
2. For each entity, check if a canonical `30900` state event exists on relays for `(service-pubkey, d=<coordinate>)`.
3. If no relay state exists, sign and publish a canonical state event using the shared builder.
4. If relay state exists but content differs, publish the Postgres state (it's authoritative during migration).
5. Mark the backfill as complete in a local marker file.

**Startup-safe backfill:**
During the warm-start phase (§5), the daemon compares its local state (populated from Postgres during the dual-path window) against relay state. Any missing or stale relay state is published once. This replaces `RepublishSnapshot` per domain.

### 4.3 Client compatibility

- **Web (Phase 4 scope):** During Phase 3, the web continues using ContextVM mutations. The daemon's dual-dispatch converts them to intents internally. The web sees updated canonical `30900` state events from its existing subscription — no web changes needed in Phase 3.
- **CLI (Phase 5 scope):** Same as web. The CLI's `pkg/client` operator Nostr methods continue working through ContextVM. Phase 5 converts them to direct intent signing.
- **MCP:** Same dual-dispatch. MCP tools that call `registry.CreateService` continue working; the registry internally publishes an intent.
- **REST:** REST mutation routes remain during the dual-path window. They call the same domain services, which now go through intent processing. Phase 3 does not delete REST routes; that happens when no consumer needs them.

---

## 5. Ending Re-projection (bahia-irsry.11.1)

### 5.1 The problem

`Projector.Run` (`internal/adapters/nostr/projector.go:384–408`) calls `RepublishSnapshot` at startup and every 10 minutes (`repairInterval: 10 * time.Minute`, `:295`). `RepublishSnapshot` (`:426–535`) reads every entity from Postgres and publishes 30900 records. Fingerprint dedupe prevents re-signing unchanged content, but it still scans the whole DB, and a cold local store causes a full re-sign.

### 5.2 Removal sequence

**Per-domain leg removal (with each domain's Phase 3 slice):**

Each `RepublishSnapshot` leg corresponds to a domain. As each domain slice lands:
1. Delete the snapshot leg (e.g. `publishServiceRegistry` loop within `RepublishSnapshot`).
2. Delete the `handleEvent` cases for that domain's bus events.
3. Delete the domain's bus event subscriptions in the projector.
4. The daemon's intent subscription + canonical state publish replaces both.

Leg removal order matches the slice plan (§7):
1. Services → delete `publishServiceRegistry` in `RepublishSnapshot` and `handleEvent`'s `EventServiceCreated/Updated/Deleted`
2. Environments → delete `publishEnvironmentRegistry` in `RepublishSnapshot` and `handleEvent`'s `EventEnvironmentCreated/Updated/Deleted`
3. States → delete `publishState/publishStateTombstone` and `handleEvent`'s `EventState*`
4. Builds/artifacts/intents/runs → delete `publishPublicRouteSnapshotsFromSource` and its bus event handlers
5. Policies → delete `publishPolicySnapshots/publishPolicyRegistry` and its bus events
6. LLM routes/state → delete `publishLLMRouteRegistry/publishLLMRouteState` and bus events
7. ML → delete `publishMLSnapshots` and bus events
8. Workers → delete `publishWorkerReadModelSnapshots` and bus events
9. Backups → delete `publishBackupSnapshots` and bus events
10. DNS → delete `publishDNSEndpointSnapshot/publishDNSZoneSnapshot/publishDNSBackendSnapshot/publishDNSPolicySnapshot` and bus events
11. SBOM → delete `publishSBOMSnapshots`

**After all legs are removed:**
- Delete `RepublishSnapshot` itself.
- Delete the `repairInterval` ticker in `Projector.Run` (`:395–405`).
- Delete `Projector.Run`'s startup call to `RepublishSnapshot` (`:389`).
- Delete `projectionDedupe` (the fingerprint cache, `projection_dedupe.go`), `hydrateProjectionCache`, and the projector's dependency on `nostrEventRepo`.

### 5.3 Interim warm-start

Before all legs are removed, a warm-start mechanism replaces the full `RepublishSnapshot` for migrated domains:

1. On startup, the daemon waits for its subscriber's first catch-up (EOSE on the intent and state subscriptions from each relay). Phase 2's per-relay cursor and NIP-77 sync ensure this is incremental, not a full re-fetch.
2. After EOSE, the daemon compares its local state (from the local bbolt store) against relay-held state:
   - For each domain already migrated to intents: query the local store for the daemon's own `(30900, service-pubkey, d=*)` records per domain. Compare fingerprints against what the relay delivered.
   - Any local record not on the relay, or with a newer fingerprint → publish it.
   - Any relay record newer than local → ingest it (this shouldn't happen if the daemon is the sole publisher, but it handles split-brain recovery).
3. This comparison replaces `RepublishSnapshot` for migrated domains. For unmigrated domains, the legacy `RepublishSnapshot` leg still runs (it shrinks as slices land).

### 5.4 Acceptance test

```
Test: DaemonRestartPublishesZeroEventsWhenRelaysHoldCurrentState

Setup:
1. Start daemon, process several intents, confirm canonical state on relays.
2. Stop daemon.
3. Instrument the relay pool's Publish method to count events.
4. Start daemon again.

Assert:
- After the daemon reaches "ready" (local store synced), the publish count is 0.
- All canonical state on relays matches the daemon's local state.
- The daemon correctly processes new intents after restart.
```

This test is gated on `bahia-irsry.11` in the architecture ratchet (`TestArchitectureDBLessDaemonBootCapsTierAndGatesNilRepositoryRoutes`).

---

## 6. Readiness and Tiers

### 6.1 Today's model

The bootstrapper (`internal/adapters/nostr/bootstrapper.go`) runs replay groups, counts decoded events, and computes a `ReadyTier` (0–3). The `ModePolicy` (`internal/app/mode_policy.go`) gates runners, routes, and background tasks by tier. Tier 2 requires the projector, reactor, and domain services. Tier 3 requires LLM, DNS, backup, and other coordinators.

Problems (B-8 through B-11):
- The bootstrapper decodes `30900` with a no-op decoder — no domain state is actually rebuilt.
- The tier can be raised over nil repositories (B-10).
- Tier 2/3 readiness means "decoded > 0 events from relays", not "state is current".
- Tier 1 means "no services, no deployments, no DNS" — the daemon is idle.

### 6.2 Target: readiness = local store synced per filter

**Decision: replace the tier model with "local store synced per filter."**

Readiness is defined as:
1. The local bbolt store has completed its first catch-up for each required subscription filter.
2. "Completed catch-up" means: EOSE received from at least one relay for that filter, plus NIP-77 reconciliation completed against at least one relay.
3. Once synced, the daemon is ready to process intents and serve state.

There is no tier 0/1/2/3 distinction. The daemon is either "syncing" or "ready". Postgres availability does not affect readiness — it's an optional index.

**Implementation:**
```go
type ReadinessTracker struct {
    mu       sync.RWMutex
    filters  map[string]filterStatus // filter-hash → status
}

type filterStatus struct {
    eoseReceived  map[string]bool // relay-url → received
    negCompleted  map[string]bool // relay-url → completed
    ready         bool            // at least one relay synced
}

func (r *ReadinessTracker) IsReady() bool {
    r.mu.RLock()
    defer r.mu.RUnlock()
    for _, f := range r.filters {
        if !f.ready {
            return false
        }
    }
    return true
}
```

The `/ready` endpoint returns 200 when `IsReady()` is true. During sync, it returns 503 with the sync progress.

**When this can happen:** After the services/environments slice lands (Wave 1) and the bootstrapper's decode path is replaced with the intent subscriber's catch-up. The tier model, `ModePolicy`, and `bootstrapper.go`'s tier logic are deleted when the last domain slice removes its dependency.

### 6.3 Transition

During the slice-by-slice migration:
- Migrated domains: readiness = local store synced for their intent filter.
- Unmigrated domains: readiness = legacy bootstrapper tier (existing behavior).
- Combined readiness: both must be satisfied. The legacy tier shrinks as domains migrate.
- After all domains migrate: delete `ModePolicy`, `bootstrapper.go`'s tier computation, `TierGate` middleware, and all tier-gated runner registration.

---

## 7. Slice Plan

### Wave 1: Foundation + Services/Environments (reference implementation)

| Slice | Goal | Done-when | Key files (ownership) | Deletes |
|-------|------|-----------|----------------------|---------|
| **F1: Intent framework** | Intent subscriber, processor pipeline, TrustSet (config-only), intent status publisher, ReadinessTracker | Intent events are received, validated, authorized (via config trust), and status events are published. Acceptance test: a signed service-create intent from a config-authorized pubkey produces a canonical 30900 state event | `internal/controlplane/intent_subscriber.go` (new), `internal/controlplane/intent_processor.go` (new), `internal/controlplane/trust_set.go` (new), `internal/controlplane/readiness.go` (new), `internal/controlplane/intent_status.go` (new) | Nothing yet |
| **F2: Services intent handler** | Service create/update/delete via intents. Dual-dispatch: ContextVM handlers convert to intents. Projector service legs removed | Service CRUD works through both intent events and legacy ContextVM. The daemon publishes exactly one canonical 30900 per mutation. Test: create via intent, update via ContextVM, both produce identical state events | `internal/controlplane/service_intent_handler.go` (new), `internal/controlplane/encrypted_route_handlers.go` (dual dispatch added), `internal/service/registry.go` (intent entry point), `internal/adapters/nostr/projector.go` (service legs deleted), `internal/adapters/nostr/control_state_contract.go` (read only) | Projector: `publishServiceRegistry` loop in `RepublishSnapshot`, `handleEvent` cases for `EventServiceCreated/Updated/Deleted`, bus subscriptions for service events. `publishAudit` calls for service bus events (B-16) |
| **F3: Environments intent handler** | Environment create/update/delete via intents. Deployment-unit handling. Revision token validation. Projector environment legs removed | Environment CRUD works through intents. `expected_updated_at` conflict detection works. Test: concurrent updates with stale revision → rejection status | `internal/controlplane/environment_intent_handler.go` (new), `internal/service/registry.go` (environment intent entry), `internal/adapters/nostr/projector.go` (environment legs deleted) | Projector: `publishEnvironmentRegistry` loop in `RepublishSnapshot`, `handleEvent` cases for `EventEnvironmentCreated/Updated/Deleted` |

**Parallel-safe:** F1 is a prerequisite for F2 and F3. F2 and F3 can run in **parallel** after F1 lands (they touch different handler files and different projector legs). F2 and F3 both read `control_state_contract.go` but do not modify it.

### Wave 2: State + Builds/Artifacts + Policies

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **S1: Service/env state** | Daemon runtime observations publish 30900 state directly instead of through the projector bus path | Runtime observation produces one canonical state event. No projector re-publish | `internal/service/runtime_lifecycle.go`, `internal/reconcile/reconciler.go` (state observation path), `projector.go` (state legs) | S2, S3 | Projector: `publishState/publishStateTombstone` in `RepublishSnapshot` and `handleEvent`; `EventReconcileCompleted` audit publish (B-16); `shouldRefreshDNSProjection`'s `EventReconcileCompleted` trigger (B-17 partial) |
| **S2: Builds/artifacts/intents/runs** | Build registration, artifact registration, deployment intent/run lifecycle use events instead of projector re-publish | Builds/artifacts arrive via HiveCI subscriber or ContextVM, publish canonical state directly. No projector leg | `internal/service/build_registry.go`, `internal/service/deployment_coordinator.go`, `projector.go` (build/artifact/intent/run legs), `publisher.go` (31000-31003 legacy kinds) | S1, S3 | Projector: `publishPublicRouteSnapshotsFromSource` and all build/artifact/intent/run `handleEvent` cases. `Publisher.SetupSubscriptions` for legacy kinds 31000–31003 entirely |
| **S3: Policies** | Policy create/update/delete via intents | Policy CRUD works through intents. Test: create policy via intent, verify state event | `internal/controlplane/policy_intent_handler.go` (new), `internal/service/policy_service.go`, `projector.go` (policy leg) | S1, S2 | Projector: `publishPolicySnapshots/publishPolicyRegistry` and `handleEvent` policy cases |

**All three are parallel-safe** — they touch different domain services, different projector legs, and different handler files.

### Wave 3: DNS + LLM

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **D1: DNS authority inversion** | DNS zone/endpoint/backend/policy state derived from subscribed events, not from DB polling. DNS agent subscribes to zone state events instead of ContextVM RPC (C-34) | DNS endpoints computed incrementally from state events. DNS agent uses REQ subscriptions. No 30-second DNS reconciler poll. No DNS ContextVM RPC | `internal/reconcile/dns_reconciler.go`, `internal/reconcile/dns_projector.go`, `internal/dnsagent/`, `internal/adapters/dns/dnsmasq_agent.go`, `projector.go` (DNS legs) | L1 | Projector: all DNS snapshot/tombstone methods. `dns_reconciler.go` 30s ticker. `dnsmasq_agent.go` ContextVM client. DNS ContextVM handlers in `encrypted_route_handlers.go`. `handleEvent` DNS refresh triggers (B-17) |
| **L1: LLM routes/releases/state** | LLM route CRUD and deployment via intents. Gateway reconciler subscribes to state events | LLM route lifecycle works through intents. No 60s LLM reconciler poll | `internal/service/llm_*.go`, `internal/reconcile/llm_reconciler.go`, `projector.go` (LLM legs) | D1 | Projector: `publishLLMRouteRegistry/publishLLMRouteState` and `handleEvent` LLM cases. `llm_reconciler.go` 60s ticker |

### Wave 4: Backups + ML + Packages + Workers

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **B1: Backups** | Backup recipe/policy/repo config via intents. Run/restore/verification/retention as daemon-authored status events | Backup config via intents. No 30s backup coordinator polls | `internal/service/backup_*.go`, `projector.go` (backup legs) | M1, P1, W1 | Projector backup snapshot legs. `backup_run_coordinator.go`/`backup_restore_coordinator.go`/`backup_retention_coordinator.go` 30s tickers (B-23) |
| **M1: ML models/versions/endpoints** | ML entity state via intents or daemon-authored events | ML state on relays without projector. No projector ML leg | `internal/service/ml_*.go`, `projector.go` (ML legs) | B1, P1, W1 | Projector: `publishMLSnapshots` and ML `handleEvent` cases |
| **P1: Packages** | Package publication/intent/approval via intents | Package state on relays without projector | `internal/service/package_*.go`, `projector.go` (package legs) | B1, M1, W1 | Projector: package `handleEvent` cases |
| **W1: Workers** | Worker state derived from Loom adverts (already canonical). Remove projector worker re-publish | Worker assignment/drain state published once by the handler, not re-published by projector | `internal/controlplane/operator_actions.go` (worker handlers), `projector.go` (worker legs) | B1, M1, P1 | Projector: `publishWorkerReadModelSnapshots` and worker `handleEvent` cases |

### Wave 5: Orgs/Secrets/Notifications + SBOM + Cleanup

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **O1: Org membership events** | Org/member/invite as encrypted addressable events. TrustSet hydrated from relay events instead of Postgres | Membership changes via intent events. Trust set updated in real time from relay events | `internal/controlplane/trust_set.go` (event hydration), `internal/controlplane/org_intent_handler.go` (new), `internal/api/handlers/tenants.go` (dual dispatch then delete) | N1, X1 | `internal/api/handlers/tenants.go` REST routes (B-26). Postgres `org_members` as authority. `auth/rbac.go` Postgres dependency |
| **N1: Secrets + Notifications** | Secrets as NIP-44 encrypted addressable events. Notification channels as addressable config events | Secrets and notifications stored as events, not Postgres-only | `internal/controlplane/secret_intent_handler.go` (new), `internal/controlplane/notification_intent_handler.go` (new), `internal/api/handlers/secrets.go`, `internal/api/handlers/notifications.go` | O1, X1 | `secrets.go` REST routes. `notifications.go` REST routes. `notification_encrypted_handlers.go`. Postgres `pg_secret`, `pg_notification_channel` as authority (B-27) |
| **X1: SBOM + final projector cleanup** | Remove remaining projector legs. Delete `RepublishSnapshot`, the 10-min ticker, and `projectionDedupe` | No projector re-publish. Zero-event restart test passes | `projector.go` (final cleanup), `projection_dedupe.go` (delete), `publisher.go` (legacy kind setup) | O1, N1 | `RepublishSnapshot` method. `repairInterval` ticker. `projectionDedupe`/`hydrateProjectionCache`. All remaining projector bus subscriptions. `Publisher` legacy kind audit aliases (B-16). `nostr_events` dependency for dedupe hydration |

### Wave 6: Tier model removal + REST route cleanup

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **T1: Replace tier model with readiness tracker** | `ModePolicy`, tier gates, bootstrapper tier computation replaced by `ReadinessTracker` | `/ready` returns based on local-store sync, not tier. DB-less daemon serves all domains | `internal/app/mode_policy.go` (delete), `internal/api/middleware/tier_gate.go` (delete), `internal/adapters/nostr/bootstrapper.go` (readiness only), `internal/app/app.go` (startup simplification) | R1 | `ModePolicy` + all tier gate code. `bootstrapper.go` tier computation. `catalog.go` replay groups (B-8, B-9). Runner tier registration in `app.go`. B-10 is closed by deletion |
| **R1: ContextVM mutation handler cleanup** | Delete all ContextVM mutation handlers replaced by intent processing | No ContextVM CRUD handlers remain. ContextVM stays only for assistant/secret-reveal/log-fetch | `internal/controlplane/encrypted_route_handlers.go` (CRUD methods deleted), `internal/controlplane/reactor.go` (deploy/rollback handlers that moved to intents) | T1 | ContextVM methods: `service/create`, `service/update`, `service/delete`, `environment/*`, `policy/*`, `llm/route-*`, `backup/*`, `dns/*`, `package/*`. All per-domain handlers in `encrypted_route_handlers.go`. Dual-dispatch wrappers from §4.1 |

---

## 8. Risks and Open Questions

### For the user to decide

1. **Single-tenant assumption.** The design assumes one org per daemon (config `org.id`). Multi-tenant (one daemon serving multiple orgs) would need per-org trust sets and per-org intent subscriptions. Is single-tenant correct for now?

2. **Intent pubkey scope.** Should only org members be able to publish intents, or should config `authorized_pubkeys` (fleet operators) also be able to? The design currently allows both. If fleet operators should _not_ be org members (e.g. infrastructure teams), the trust set needs a "fleet operator" role distinct from org roles.

3. **REST route deletion timing.** Phase 3 can delete REST mutation routes as each slice lands, or keep them until Phase 5 (CLI migration). Keeping them means the CLI works unchanged during Phase 3. Deleting them early forces CLI users to the Nostr path sooner. Recommendation: keep REST reads until Phase 5, delete REST mutations when the ContextVM dual-dispatch is proven.

4. **Postgres removal timeline.** Phase 3 makes Postgres a derived index. Should it be _removed entirely_ (daemon runs with no SQL) at the end of Phase 3, or kept as an optional query accelerator through Phase 5? Recommendation: keep it optional through Phase 5 so that REST reads and MCP reads continue working without changes.

5. **Encrypted membership events vs. public membership.** Org membership could be public (anyone who can reach the relay can see who is a member and their role) or encrypted (NIP-59 gift-wrapped, readable only by the daemon and org members). The design uses encrypted membership. If membership is not sensitive, public events are simpler and let the web derive roles without decryption.

6. **DNS agent deployment.** The DNS agent today runs as a separate binary (`cmd/bahia-dns-agent`). C-34 proposes it subscribes to zone state events. Does the DNS agent need its own local store, or can it be stateless (subscribe, apply, publish health)?

### Risks

- **Ordering sensitivity.** Intent processing order matters for creates (first wins). Clock skew between operators could cause surprising results. Mitigation: the `intent_id` UUIDv7 encodes millisecond-precision time, and the daemon processes by `(created_at, id)`.
- **Trust set cold start.** If no membership events exist on relays and the config is wrong, the daemon trusts nobody and rejects all intents. Mitigation: the daemon logs prominently when the trust set is config-only and has no relay-derived members.
- **Dual-path bugs.** During the transition, a mutation could be applied twice (once via ContextVM, once via the intent it generated). Mitigation: the ContextVM-to-intent bridge uses the same `intent_id`, and the processor's idempotency check prevents double application.
- **Local store corruption.** The bbolt local store is the daemon's memory. Corruption means lost cursors and idempotency state. Mitigation: NIP-77 sync rebuilds the store from relays; the daemon detects corruption and triggers a full resync.
