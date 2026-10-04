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

A client intent is a **kind `30900` addressable replaceable event** signed by the operator/user, with `d` = the entity coordinate. It uses the same kind as the daemon's canonical state projection. The relay naturally keeps only the latest replacement per `(kind, pubkey, d)`, so operator intents and daemon state live in separate coordinate spaces (different pubkeys) and coexist without collision.

**Decision: client intents are kind `30900`, signed by the operator.**

Rationale:
1. The intent _is_ the desired state — it contains the same fields as the canonical state record.
2. The relay's built-in replaceable semantics give us latest-wins for free.
3. Readers can discover both the operator's desired state and the daemon's confirmed state with one kind, scoped by author.
4. No new kind allocation needed.

### 1.2 Intents are level-triggered desired state, not an op log

The relay keeps only the latest `(kind, pubkey, d)` for each author. An offline daemon that comes back may see only an update or a delete for a coordinate, never having seen the create. The processor therefore **reconciles the entity toward the newest trusted intent's full desired state**; it does not replay a sequence of operations.

- `op` (`create`, `update`, `delete`) is **advisory**. It helps the daemon choose error messages (e.g. "entity not found" for an update on a coordinate with no prior state) and pick the right side-effect path, but the processor never relies on having seen earlier ops.
- Content must be the **complete desired state** for create and update — every field the entity needs, not a diff. The daemon replaces its local state with the intent's content after validation.
- For delete, content is `{"deleted": true, "id": "<entity-id>"}`. The daemon performs cleanup side effects and publishes a tombstone `30900` with `deleted: true`.

### 1.3 Intent event structure

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
- `domain` = the domain family (`service`, `environment`, `artifact`, `adoption`, `policy`, `deployment`, `runtime`, `llm`, `dns`, `backup`, `ml`, `package`, `org`, `secret`, `notification`).
- `schema` = `bahia.intent.<domain>.v1`. Distinct from the daemon's state schema `bahia.cp-state.v1`.
- `t` = `bahia-intent` (enables `#t` filtering for all intents) plus the domain topic tag (e.g. `service-registry`).
- `op` = `create`, `update`, or `delete` — advisory, not load-bearing (§1.2).
- `org` = the org UUID the entity belongs to (enables `#org` filtering for trust-scoped subscriptions). Fleet-only DNS, ML, and worker intents may omit `org`; these handlers authorize against configured fleet operators and do not derive an org from the entity.
- `intent_id` = a UUIDv7 minted by the client per intent attempt, carried in both tags and content. Used for **idempotency and correlation only** — the intent_id does not determine processing order or conflict resolution.

**Deployment-operation domain table** (see `web/tests/fixtures/deployment-intents.json` for parseable wire examples):

| Domain | `op` | Required content | Permission |
|---|---|---|---|
| `deployment` | `create` | `service_id`, `environment_id`, `artifact_id` | `deployments:write` |
| `deployment` | `rollback` | `service_id`, `environment_id`, `target_artifact_id` or `target_run_id`, `supersedes_intent_id` | `deployments:write` |
| `deployment` | `approve`, `reject` | `deployment_intent_id`; optional `expected_updated_at` | `deployments:approve` |
| `runtime` | `deploy`, `restart`, `stop` | `service_id`, `environment_id`; optional `artifact_id` for deploy only | `deployments:write` |
| `llm` | `deploy` | `route_id`, `environment_id`, `release_id` | fleet operator |
| `llm` | `rollback` | `route_id`, `environment_id` | fleet operator |
| `llm` | `approve`, `reject` | `deployment_intent_id`; optional `expected_updated_at` | fleet operator |
| `backup` | `restore-approval` | `restore_id`, `decision` (`approve` or `reject`); optional `expected_updated_at` | fleet operator |
| `artifact` | `register` | Complete immutable artifact registration (`id`, `build_id`, `service_id`, image repository/tag/digest and optional scan metadata); `d=artifact:<id>` | `services:write` on the owning service |
| `artifact` | `import-observed` | Complete observed-image selector (`service_id`, `environment_id`, image repository/tag/digest and optional unit/git refs); `d=artifact-import:<service>:<environment>:<digest>` | `services:write` on both service and environment |
| `adoption` | `import` | Complete desired service target/selection set or `import_all`, optional `org_id`; `d=adoption:<org>` or `adoption:fleet` | configured adoption operator allowlist |
| `dns` | `drift-remediate` | Optional `zone`; `d=dns-remediate:<zone>` or `dns-remediate:all` | fleet operator |
| `deployment` | `preview` | Complete deployment-preview request, including managed runtime config; `d=deployment-preview:<service>:<environment>` | `deployments:write` on both service and environment |
| `deployment` | `route-attach` | Complete public-route request, service and environment IDs; `d=deployment-route:<service>:<environment>`; optional `expected_updated_at` of the deployed intent | `deployments:write` on both service and environment |

All update decisions compare `expected_updated_at` to the canonical entity revision when present. The operation tag selects the legacy-equivalent side-effect path; durable progress is the bounded `30315` status and daemon-authored canonical state, not the ContextVM acknowledgment.

Artifact registration and observed import are level-triggered immutable lineage
desires: the registry converges by image digest and publishes canonical
build/artifact state. Adoption import is a level-triggered desired target and
selection set: the adoption service discovers/imports idempotently and signs
the resulting service/environment records. Neither flow treats a ContextVM
acknowledgment as authority. DNS drift-remediate and deployment preview are
request actions, not entity state: the former reconciles a zone and reports its
outcome; the latter returns a daemon-computed plan in a bounded `30315` status
(compact summary, hash, and policy rather than unbounded full desired state).
`route-attach` refers to the existing **service public-route** attachment
method, not an LLM registry route. It reconciles the deployment's desired
public route through the current policy-checked service method, which creates
and publishes the resulting deployment intent exactly once.

### 1.4 Relationship to today's cp-state 30900 records

Today the daemon publishes kind `30900` with `pubkey = <service-pubkey>` and schema `bahia.cp-state.v1`. The shared builder in `internal/adapters/nostr/control_state_contract.go` produces the envelope.

Phase 3 intents are also `30900` but signed by the **operator** (a different pubkey). Both coexist on relays:
- **Operator intent**: `(30900, operator-pubkey, d=<service-id>)` — desired state.
- **Daemon canonical state**: `(30900, service-pubkey, d=<service-id>)` — confirmed state.

The ContextVM create/update/delete handlers (`internal/controlplane/encrypted_route_handlers.go`) are replaced per slice. The client no longer sends `25910` for these mutations; it signs and publishes a `30900` intent directly.

### 1.5 Cross-author ordering and conflict resolution

**The newest intent by `(created_at, lowest event id)` across all trusted authors for a coordinate wins**, subject to the `expected_updated_at` revision check.

`expected_updated_at` is the canonical record's `updated_at` RFC3339/RFC3339Nano string, not a numeric Unix epoch. Clients copy that string into the intent content; the daemon rejects malformed or numeric values and compares revisions at the canonical record's microsecond precision.

When multiple trusted operators publish intents for the same coordinate:
1. The relay keeps only the newest per `(kind, pubkey, d)` — one per author.
2. The daemon's subscription delivers intents from all trusted authors. For each coordinate, the daemon selects the **newest** by `(created_at, lowest hex id)` across authors. This is the winning intent.
3. If the winning intent carries `expected_updated_at`, the daemon compares it against the current canonical state's `updated_at`. A mismatch → rejection status event with reason `revision_conflict`. The client must re-read, re-merge, and re-sign.
4. If no `expected_updated_at` is present (e.g. on a create), latest-wins applies unconditionally.

**Idempotency via `intent_id`:**
- Each intent carries `intent_id` (a UUIDv7). The daemon stores processed intent_ids in its local bbolt store.
- If the daemon sees an intent whose `intent_id` was already processed, it skips re-processing. This handles retries, relay catch-up after restart, and the in-process dual-dispatch path.
- The `intent_id` is durable because the local store survives restarts.

### 1.6 Deletion

**Decision: intent with `op=delete` plus a `deleted: true` content flag, not NIP-09.**

Rationale:
- NIP-09 (kind 5) asks the relay to remove events. We _want_ the daemon's last canonical state to persist as a tombstone so that consumers know the entity was deliberately removed, not just absent.
- The existing tombstone convention (`content.deleted = true` in 30900 records) is already used by all consumers (`docs/event-spec.md`, `web/src/lib/nostr/replaceable.js`, projector tombstone paths).
- The operator publishes a delete intent: `op=delete`, `content: {"deleted": true, "id": "<entity-id>"}`. The daemon processes it, performs side effects (stop runtime, cleanup DNS), and publishes its own `30900` with `deleted: true` under the service pubkey.
- Relay storage is bounded: the deleted tombstone replaces the live record on the same coordinate.

### 1.7 Encryption for sensitive domains

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

#### 1.7.1 Unified confidential cp-state crypto (Phase 3 C1)

All confidential cp-state records (org, member, invite, secret metadata, notification channel config) use a **single shared encryption scheme** based on per-org content keys (OCKs). This replaces the previous O1 (sha256-derived key AEAD) and N1 (NIP-44 self-encryption) schemes.

**Per-org content key (OCK):**
- Random 32-byte symmetric key with a version number per org.
- Used with XChaCha20-Poly1305 AEAD (same primitive as the assistant transcript store).
- AEAD associated data binds the ciphertext to the record's coordinate identity: `{schema, key_org, key_ref, key_version, legacy_kind, d, t}`. This prevents ciphertext replay across coordinates.
- All NIP-44 operations go through the `gonostr.Keyer` signer interface, not raw key derivation. Bunker signers that support NIP-44 encrypt/decrypt work out of the box.

**Key distribution (key-envelope records, kind 32010):**
- The OCK is wrapped (NIP-44 encrypted) to each current org member and to the service pubkey.
- Each wrapped copy is published as an addressable cp-state record through the shared `controlStateEnvelope`/`cpStateFamilies` pipeline with `legacy_kind=32010`, `t=org-key-envelope`.
- D-tag coordinate: `org-key:<orgID>:v<version>:<random-16-byte-hex-handle>`. The recipient pubkey never appears in plaintext tags, d-tags, or content.
- On daemon restart, the OCK is recovered from the service-wrapped envelope in history.

**Member discovery (envelope lookup):**
- Members find their envelope by trial-decrypting all envelopes for their org+version. Cost is O(N) where N is the org member count.
- Current deployments have < 100 members per org, so this is acceptable.
- Handles are random 16-byte values; no correlation between handle and recipient identity.
- A deterministic HMAC(conversation_key, org|version) handle would reduce discovery to O(1) lookup. However, the NIP-44 conversation key is not accessible through the bunker signer interface (`Keyer.Encrypt`/`Decrypt` are opaque). Phase 4 web clients hold their own key material and could use deterministic handles.
- Future: when bunker signers support conversation key derivation or a `DeriveHandle(pubkey, context)` method, switch to HMAC-based deterministic handles.

**Key lifecycle (driven by OrgCanonicalPublisher.PublishMember):**
All key lifecycle operations are triggered from `OrgCanonicalPublisher.PublishMember`, the single choke point that all member mutation paths (intent, legacy ContextVM, relay-sourced hydration) flow through:
- **(a) Member deleted** → `RotateKey(orgID)`. New OCK version created, wrapped to remaining members only. The removed member cannot decrypt future records.
- **(b) Member added/updated** → `WrapKeyForMember(orgID, pubkey)`. Wraps the current OCK to the new member so they can read existing records immediately. If no OCK exists yet (first member of a new org), the next `EncryptConfidential` call will create and distribute.
- **(c) Role downgrade** (prevRole weight > new role weight) → `RotateKey(orgID)`. Re-keys even though the downgraded member still receives the new key; semantically correct for future role-filtered wrapping and provides an audit boundary.
- Old versions remain readable for historical records (acceptable; noted in design).
- New publishes use the current version.

**Service-only fields (service_inner):**
- Secret values and notification channel credentials (webhook URLs, signing secrets, API keys) must NOT be readable by org members.
- These are NIP-44-encrypted to the service pubkey and embedded as `service_inner` inside the AEAD-encrypted envelope.
- Org members can decrypt the AEAD layer (channel metadata, secret ref metadata) but cannot decrypt the `service_inner` NIP-44 ciphertext.
- Secret value reveal remains through ContextVM with permission checks.

**Envelope format (`bahia.confidential.aead.v1`):**
```json
{
  "schema": "bahia.confidential.aead.v1",
  "algorithm": "xchacha20-poly1305",
  "key_org": "<org-id>",
  "key_ref": "ock:<org-id>",
  "key_version": "v1",
  "nonce": "<base64-24-bytes>",
  "ciphertext": "<base64-aead-ciphertext>",
  "associated_data": {
    "schema": "bahia.confidential.aead.v1",
    "key_org": "<org-id>",
    "key_ref": "ock:<org-id>",
    "key_version": "v1",
    "legacy_kind": "32005",
    "d": "<d-tag>",
    "t": "<topic>"
  },
  "service_inner": "<nip44-ciphertext-to-service-pubkey>"
}
```

**Fleet-scoped resources:**
- Notification channels with no org (`OrgID == uuid.Nil`) are fleet-scoped.
- Fleet-scoped channels use a synthetic `"fleet"` org key scope. `TrustSetMemberSource` wraps that OCK to configured fleet operators, bootstrap owners, and the service pubkey; it does not parse `"fleet"` as a UUID. Recipients discover their envelope by the same trial-decrypt path as org members.
- The OCK-visible channel metadata is minimal (name, type, enabled, `fleet_scoped: true`); the full config remains in `service_inner` and is readable only by the service.
- Fleet operators can read this metadata from signed relay state, including through the CLI. Mutations remain subject to the fleet operator gate.

**Migration:**
- Dual-read: new-format records are tried first, then legacy O1 format.
- Legacy O1 `org_state_crypto.go` `OrgStateEncryptorImpl.DecryptOrgState` retained for read-only fallback. The `EncryptOrgState` method is deprecated (kept for migration tests); no production code path calls it.
- Legacy N1 `selfDecryptNIP44Legacy` retained for read-only fallback. N1 secret/notification records are audit copies (database is source of truth); no relay read-back path decodes them.
- New publishes always use the unified confidential path. No plaintext fallback.
- `OrgStateEncryptor` interface renamed to `LegacyOrgStateDecryptor` (decrypt-only).
- TODO: warm-start re-publication of legacy records under the OCK scheme:
  1. On daemon startup, after OCKManager and TrustSet are initialized, scan `projectionHistory.FindByTag(ctx, "t", <topic>, nil, limit)` for each confidential cp-state topic (org, org-member, org-invite, secret, notification-channel).
  2. For each record, try parsing as the new `bahia.confidential.aead.v1` schema. If it parses, skip (already migrated).
  3. If it doesn't parse as new format, try legacy O1 `decryptOrgState` (for org/member/invite) or `selfDecryptNIP44Legacy` (for secret/notification).
  4. If legacy decrypt succeeds, re-encrypt the plaintext with `ConfidentialEncryptor.EncryptConfidential` using the record's coordinate identity (kind, d-tag, topic).
  5. Re-publish through `publishControlState` (same coordinate, new content).
  6. Log progress: "migrated N/M records for topic T".
  7. Gate: run at most once per daemon lifetime (track in-memory "migration-done" flag).
  8. After all deployments have run the warm-start, remove legacy decrypt code and the `LegacyOrgStateDecryptor` interface.

**Implementation files:**
- `internal/controlplane/org_content_key.go` — OCK types, AEAD encrypt/decrypt, AD binding
- `internal/controlplane/org_content_key_manager.go` — OCK lifecycle (create, distribute, rotate, recover)
- `internal/controlplane/confidential_encryptor.go` — Bridge between OCKManager and publisher interfaces
- `internal/controlplane/ock_member_source.go` — TrustSet relay-first/Postgres-fallback member source
- `internal/controlplane/org_resolvers.go` — Secret → service → org and channel → org resolution
- `internal/domain/key_envelope_record.go` — Shared type for history records
- `internal/adapters/nostr/confidential_state.go` — Legacy read paths, key-envelope publishing/history
- `internal/adapters/nostr/org_canonical_publisher.go` — Org/member/invite publisher using ConfidentialStateEncryptor
- `internal/adapters/nostr/secret_canonical_publisher.go` — Secret ref publisher using ConfidentialStateEncryptor
- `internal/adapters/nostr/notification_canonical_publisher.go` — Channel publisher with service_inner for credentials

---

## 2. Authority and Trust

### 2.1 Today's authorization model

Two authorization layers exist:
1. **Reactor operator gate** (`internal/controlplane/reactor.go:1742–1761`): a static list of `AuthorizedPubkeys` from daemon config. Used for deploy/rollback/restart/stop/cordon and other operational commands. Fail-closed: unknown pubkeys are rejected.
2. **Tenant RBAC** (`internal/controlplane/encrypted_tenant_authorization.go`, `internal/auth/rbac.go`): checks org membership and role-based permissions via Postgres `OrgMemberLookup`. Used by ContextVM handlers for service/environment/secret/notification CRUD. Roles: viewer < deployer < admin < owner. Permissions: `services:write`, `environments:write`, `secrets:write`, `deployments:write`, etc.

Both depend on Postgres: the reactor reads config (fine, config stays), but tenant RBAC queries the `org_members` table.

### 2.2 Target: event-derived trust with pluggable sources

**Decision: `TrustSet` with three pluggable sources, keyed `org → pubkey → role`.**

Bahia already serves multiple orgs. The `TrustSet` is multi-org from the start. Its sources are consulted in priority order:

| Priority | Source | When active | Scope |
|----------|--------|-------------|-------|
| 1 (highest) | **Relay membership events** (O1 slice) | After O1 lands and events exist for an org | Per-org roles from signed events |
| 2 | **Postgres `org_members`** (read-only) | When a database is configured; interim until O1 | Exact today's tenant RBAC behavior |
| 3 (lowest) | **Daemon config** | Always | `authorized_pubkeys` → fleet-operator only (not implicit org members); per-org `bootstrap_owners` for initial org bootstrapping |

```go
type TrustSet struct {
    mu       sync.RWMutex
    // Per-org membership from relay events (highest priority).
    relay    map[string]map[string]domain.Role // org-id → pubkey → role
    // Per-org membership from Postgres (read-only interim).
    postgres OrgMemberLookup                    // nil when no DB
    // Fleet-level operator pubkeys from config (not org members).
    fleetOps []string
    // Per-org bootstrap owners from config (used only when no
    // relay or Postgres members exist for the org).
    bootstrapOwners map[string]string            // org-id → pubkey
}
```

**Resolution order for `HasPermission(orgID, pubkey, permission)`:**
1. If relay membership events exist for `orgID`, use them exclusively.
2. Else if Postgres is configured, delegate to `auth.RBAC.CheckPermission` (today's exact behavior).
3. Else if `pubkey` is a `bootstrapOwner` for `orgID`, grant owner-level permission.
4. Fleet operators (`fleetOps`) are **never** implicit org members. They authorize only fleet-scoped operations (worker cordon/drain, policy eval, saga recovery) through `FleetOperatorGate`, unchanged from today's reactor gate.

**Config structure:**
```yaml
nostr:
  authorized_pubkeys: ["<hex-pubkey>"]  # fleet operators — NOT org members
  bootstrap_owners:                      # per-org bootstrap, used only when
    "<org-uuid>": "<hex-pubkey>"         # no relay or DB members exist
```

No `org.id` config — Bahia is multi-org. Per-org bootstrap is through Postgres membership (interim) or the `bootstrap_owners` map.

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

Membership is encrypted because org composition and roles are sensitive information. In Phase 4, the web derives its own role by decrypting the membership events gift-wrapped to its pubkey and reading the `role` field from the content.

#### How the daemon builds and updates its trust set

The daemon subscribes with a long-lived REQ for encrypted events addressed to it:
```json
{
  "kinds": [1059],
  "#p": ["<bahia-service-pubkey>"],
  "since": "<cursor>"
}
```

It unwraps gift-wrapped membership events and updates the `relay` map of the `TrustSet`. The Postgres source is a direct read-only delegate to today's `auth.RBAC`, unchanged until O1 replaces it.

### 2.3 What happens to intents from untrusted or unauthorized authors

**Anti-amplification rule:** the intent subscription uses `authors` = the trust set's current pubkeys, refreshed when the trust set changes (§3.1). This means untrusted authors' events are never requested from relays.

Events from untrusted authors that still arrive (e.g. the relay delivers them despite the filter, or during a trust-set transition) are **dropped silently**. The daemon does not sign a rejection event for an unknown author — that would let an attacker force daemon-signed output by publishing junk intents.

Rejection status events are published **only** when:
1. A **known principal** (in the trust set) lacks the specific permission for the operation. E.g., a `viewer` tries a `services:write` intent.
2. An intent from a **trusted author** fails validation (malformed content, invalid entity id, stale `expected_updated_at`).

### 2.4 How today's ContextVM/operator authorization maps onto it

| Today | Phase 3 |
|-------|---------|
| `Reactor.isAuthorized(pubkey)` — config pubkey list | **Config pubkeys stay** as the fleet-operator gate for fleet-level operations (worker cordon/drain, maintenance). Unchanged |
| `encryptedTenantAuthorizer.authorizeOrg` — Postgres org member lookup | `TrustSet.HasPermission(orgID, pubkey, perm)` — resolves through relay events → Postgres → config bootstrap, in priority order |
| `encryptedTenantAuthorizer.authorizeService` — load service from DB, check org | Intent carries `org` tag; daemon checks `TrustSet` directly. Service-to-org mapping verified from the daemon's local state (the canonical `30900` service record the daemon itself published) |
| `FleetOperatorGate` — config pubkey list for specific methods | Stays for fleet-scoped ops (policy eval, worker maintenance, saga recovery). Not replaced by org membership |

### 2.5 Ordering: interim trust sources before O1

The O1 (org membership events) slice is ordered in Wave 5. Every earlier slice needs authorization. The interim works because the `TrustSet` falls through to Postgres `org_members` when no relay membership events exist, which reproduces today's exact authorization behavior. No regression.

Wave 1's `TrustSet` implementation includes:
- The config source (fleet ops + per-org bootstrap owners).
- The Postgres source (read-only delegate to `auth.RBAC`).
- The relay source (empty until O1).

---

## 3. Daemon Reaction

### 3.1 Subscription model

The daemon opens **one long-lived REQ per input class** against its relay pool, using Phase 2's per-(relay, filter) cursors:

```go
// Intent subscription: operator-signed desired-state events from trusted authors.
intentFilter := nostr.Filter{
    Kinds:   []int{30900},
    Authors: trustSet.AuthorPubkeys(), // refreshed on trust-set change
    Tags:    nostr.TagMap{"#t": {"bahia-intent"}},
    Since:   &cursor,
}
```

The `authors` field scopes the subscription to trusted pubkeys only. When the trust set changes (a new member is added, a member is removed), the daemon closes and re-opens the intent subscription with the updated `authors` list. This is the anti-amplification mechanism: the daemon never requests events from unknown pubkeys.

For encrypted intents (secrets, membership):
```go
encryptedIntentFilter := nostr.Filter{
    Kinds: []int{1059},
    Tags:  nostr.TagMap{"#p": {servicePubkey}},
    Since: &cursor,
}
```

Encrypted intents cannot be author-scoped at the relay level (the outer `1059` wrapper has an ephemeral pubkey). The daemon unwraps and then checks the inner signer against the trust set.

### 3.2 Processing pipeline

The relay intent path and the in-process dual-dispatch path (§4) share **one pipeline and one idempotency store**.

For each intent (whether from relay subscription or in-process dispatch):

```
1. Deduplicate
   └─ Check intent_id in local store → skip if already processed

2. Validate
   ├─ Verify signature (relay path: already done by subscriber; in-process: skip, trusted)
   ├─ Parse domain, op, schema from tags
   ├─ Validate content schema (id format, required fields, UUIDv7/v4)
   ├─ Content must be complete desired state (§1.2)
   └─ For updates with expected_updated_at: check revision against local state

3. Authorize
   ├─ Resolve org from intent's org tag
   ├─ Check TrustSet.HasPermission(org, pubkey, permission)
   ├─ Untrusted author on relay path → drop silently (§2.3)
   └─ Known principal, insufficient permission → publish bounded rejection status (§3.3)

4. Select winning intent
   ├─ For this coordinate, compare against any other trusted author's intent
   ├─ Newest by (created_at, lowest id) across trusted authors wins (§1.5)
   └─ Loser's intent is not an error; it's superseded

5. Reconcile toward desired state
   ├─ Compare winning intent content against current canonical state
   ├─ If no current state exists: treat as create (regardless of op tag)
   ├─ If current state exists and intent is newer: apply as update
   ├─ If intent has deleted:true: perform cleanup side effects
   └─ Domain-specific side effects (runtime deploy, DNS, backup queue, etc.)

6. Update local state
   ├─ Store the processed intent_id in localstore (idempotency)
   └─ Upsert Postgres index if configured (optional derived cache; must not fail the pipeline)

7. Publish canonical state event
   ├─ Build 30900 record using control_state_contract.go shared builder
   ├─ Compare fingerprint against latest stored output for (kind, service-pubkey, d)
   ├─ Skip if identical (no churn — replaces B-3/B-4 dedupe)
   └─ Sign and publish with OK verification via Publisher
```

### 3.3 Bounded intent status events

Intent processing status is published as a **kind `30315`** (NIP-38 status) event. The `d` tag is scoped to **requester and entity coordinate**, so it replaces per requester and entity:

```json
{
  "kind": 30315,
  "pubkey": "<service-pubkey>",
  "tags": [
    ["d", "intent-status:<requester-pubkey>:<entity-coordinate>"],
    ["domain", "intent"],
    ["status", "accepted"],
    ["t", "intent-status"],
    ["e", "<intent-event-id>"],
    ["p", "<requester-pubkey>"],
    ["intent_id", "<uuidv7>"],
    ["expiration", "<unix-timestamp>"]
  ],
  "content": "{\"intent_id\":\"<intent_id>\",\"coordinate\":\"<entity-coordinate>\",\"result\":\"applied\"}"
}
```

**Why `d=intent-status:<requester-pubkey>:<entity-coordinate>` and not `d=intent-status:<intent_id>`:**

An `<intent_id>`-keyed coordinate grows without bound — one addressable event per intent, never swept (the sidecar's retention policy exempts addressable events, `internal/relaysidecar/store.go:137–158`). Using `<requester-pubkey>:<entity-coordinate>` gives at most one status event per requester per entity, replaced on every new intent for that entity. The `intent_id` is carried in tags and content for correlation, so clients can still match a specific intent to its outcome.

A **NIP-40 `expiration` tag** is set to `created_at + 7 days`. Intent status is transient feedback — once the client has seen it and the canonical state event reflects the change, the status event serves no purpose. The relay's NIP-40 sweep (enabled in Phase 1, `khatru/relay.go:165`) cleans it up. Even without the sweep, the replaceable semantics bound growth to one event per (requester, entity).

**Status values:** `accepted` (intent applied, canonical state published), `rejected` (authorization failure or validation error from a trusted author), `conflict` (stale `expected_updated_at`), `superseded` (a newer intent from another trusted author won).

### 3.4 Where Postgres fits per slice

**Decision: Postgres is an optional derived write-behind index. No Phase 3 slice may require it.**

Per slice:
- **Services/environments (first slice):** The relay-first registry already publishes before the DB write. Phase 3 inverts the direction: the daemon subscribes to intents, processes them, publishes canonical state, and _then_ upserts the Postgres row as a derived index. If the DB upsert fails, nothing is lost — the canonical state is on the relay and in the local store. Postgres stays as an optional query accelerator through Phase 5.
- **DNS:** The DNS reconciler today polls Postgres for zones/endpoints. Phase 3: the reconciler subscribes to DNS state events from the local store. Postgres DNS tables become a read-only query index rebuilt from events.
- **LLM/ML/backups/packages:** Same pattern. The coordinator subscribes to intents, processes them, publishes results. Postgres is a query cache for REST/MCP compatibility reads during the dual-path window.
- **Orgs/secrets/notifications:** No Postgres at all for new writes once the membership event model is live. Existing rows are backfilled as events (§4).

### 3.5 Canonical state record and who signs it

**Decision: the daemon signs the canonical state event, not the client.**

Rationale:
- The daemon performs validation, authorization, side effects (runtime deploy, DNS zone creation), and observation. The canonical state reflects _confirmed_ reality, not just _desired_ state.
- A client's intent is a request. The daemon's canonical state is the response: "I validated your intent, applied it, and this is the confirmed state."
- Separation of pubkeys is crucial: `(30900, operator-pubkey, d)` = intent, `(30900, service-pubkey, d)` = confirmed state.
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
    ["intent_id", "<uuidv7>"],
    ["legacy_kind", "31975"],
    ...existing envelope tags...
  ],
  "content": "{...confirmed state fields...}"
}
```

### 3.6 Crash and retry semantics

**Crash between steps 5 and 7 (side effect applied, state not published):**
- On restart, the daemon replays intents from its local store cursor.
- The `intent_id` is not yet marked processed (step 6 happens after side effects).
- The daemon re-processes the intent. Side effects must be idempotent:
  - Service/environment creates: `resolveCreateByID` detects the existing entity → idempotent replay.
  - Deployments: the runtime's `desired_hash` comparison detects no change → no-op.
  - DNS: zone configs are applied by serial; re-applying the same serial is idempotent.
  - Backups: job ids are UUIDv7; re-queuing the same id → duplicate detection.

**Crash between steps 6 and 7 (intent marked processed, state not published):**
- On restart, the daemon skips the intent (already processed).
- The canonical state event was not published. The local store has the updated state but the relay does not.
- **Recovery:** After replay, the daemon does a "warm start" comparison (§5): it compares its local state against relay-held state. Any local state newer than relay state triggers a publish.

**Outbox durability (Phase 2):**
- `Publisher.PublishBeforeCommit` puts the signed event into the bbolt outbox before any relay attempt.
- Per-relay `OK` tracking resumes on restart.
- A publish that crashes mid-delivery resumes exactly where it stopped.

---

## 4. Migration and Coexistence

### 4.1 Dual-path window

During migration, both the old ContextVM/REST/MCP mutation path and the new relay intent path must work. The strategy is **in-process dual dispatch without daemon-signed fake intents**.

```yaml
nostr:
  intent_domains_disabled: [] # all registered intent domains are enabled
```

As of Phase 5 F4, all registered domains are enabled unless listed in
`intent_domains_disabled`. The deprecated `intent_domains` key retains its
Phase 3 allowlist meaning only when non-empty; an empty or omitted list enables
all domains. Both migration keys are removed with R1 (bahia-irsry.11.19).

When a domain is enabled:
- The daemon subscribes to `30900` intents with `#t=bahia-intent` from trusted authors.
- The daemon still accepts ContextVM `25910` / REST / MCP mutations for that domain.
- The ContextVM/REST/MCP handler **keeps today's authorization** (the existing `encryptedTenantAuthorizer` or REST auth middleware) and then calls the **same in-process intent processor** with the requester's pubkey as actor and a synthetic `intent_id` (or the request's existing idempotency key, e.g. the ContextVM `d` tag / `_meta.progressToken`).
- **No daemon-signed intent event is published to the relay.** The in-process path bypasses relay publication and feeds directly into the shared pipeline at step 1 (§3.2). The relay intent path and the in-process path share one pipeline and one idempotency store, so double-dispatch is impossible.
- The handler returns the canonical state to the caller (ContextVM reply, REST response, MCP result) just as today.

When a domain is explicitly disabled:
- The existing handler processes it as today, unchanged.

D70's handler coverage in this window is deliberately bounded by existing durable mutation boundaries. Fleet-scoped authorizations use configured operator pubkeys rather than org RBAC; each accepted intent receives bounded kind-30315 status from `IntentProcessor`.

| Domain | 30900 intent ops admitted | ContextVM dual dispatch | Unsupported until a durable mutation path exists |
|--------|---------------------------|-------------------------|--------------------------------------------------|
| `dns` | `zone-create`, `policy-apply`, `record-set`, `override-retire`, `drift-remediate` | `dns/zone-create`, `dns/policy-apply`, `dns/record-set`, `dns/override-retire`, `dns/drift-remediate` | Unsupported intent ops receive bounded 30315 rejection. |
| `artifact` | `register`, `import-observed` | `artifact/register`, `artifact/import-observed` | Build registration/status belong to D77. |
| `adoption` | `import` | `adoption/import` | Adoption scan is a read/request preview, not an import desire. |
| `deployment` | `preview`, `route-attach` alongside existing deployment operations | `service/deploy-preview`, `service/route-attach` | Preview publishes only a bounded plan/status; route attach publishes the resulting deployment-intent state. |
| `ml` | `model-create/update`, `version-create/update`, `endpoint-create/update` | No ContextVM registry CRUD methods exist; existing ML command handlers remain legacy | Model/version/endpoint delete; identity-changing updates (old canonical coordinate cannot be tombstoned). Unsupported intent ops receive bounded 30315 rejection. |
| `worker` | `cordon`, `uncordon`, `drain`, `undrain`, `maintenance-enter/exit`, `labels-update`, `cleanup`; every content carries desired `scheduling_state` and full `labels` | All eight corresponding worker ContextVM methods | Other worker actions remain outside this domain handler. |

This dual-path window closes per domain when:
1. The web (Phase 4) and CLI (Phase 5) sign intents directly.
2. The ContextVM/REST handler for that domain is deleted.
3. REST mutation routes are deleted per slice once dual dispatch is proven. REST reads stay until Phase 5.

### 4.2 Data backfill from Postgres to relay state

For existing Postgres rows with no relay state:

**One-shot backfill tool** (offline, run once per domain migration):
1. Read all entities from Postgres for the domain.
2. For each entity, check if a canonical `30900` state event exists on relays for `(service-pubkey, d=<coordinate>)`.
3. If no relay state exists, sign and publish a canonical state event using the shared builder.
4. If relay state exists but content differs, publish the Postgres state (it's authoritative during migration).
5. Mark the backfill as complete in a local marker file.

**Startup-safe backfill:**
During the warm-start phase (§5), the daemon compares its local state against relay-held state. Any missing or stale relay state is published once. This replaces `RepublishSnapshot` per domain.

### 4.3 Client compatibility

- **Web (Phase 4 scope):** During Phase 3, the web continues using ContextVM mutations. The in-process dual dispatch routes them through the intent processor. The web sees updated canonical `30900` state events from its existing subscription — no web changes needed in Phase 3.
- **CLI (Phase 5 scope):** Same. The CLI's `pkg/client` operator Nostr methods and REST calls continue working.
- **MCP:** Same dual-dispatch. MCP tools that call `registry.CreateService` continue working; the registry internally routes through the intent processor.

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

**File ownership rule:** `Projector.Run` (the startup snapshot call and the `repairInterval` ticker) is owned by slice **F4** in Wave 1. Individual domain slices (F2, F3, and later waves) delete only their own `RepublishSnapshot` legs and `handleEvent` cases — they do not edit `Run`.

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
- Delete the `repairInterval` ticker in `Projector.Run`.
- Delete `Projector.Run`'s startup call to `RepublishSnapshot`.
- Delete `projectionDedupe` (the fingerprint cache, `projection_dedupe.go`), `hydrateProjectionCache`, and the projector's dependency on `nostrEventRepo`.

### 5.3 Interim warm-start (Wave 1, slice F4)

F4 replaces the startup `RepublishSnapshot` call in `Projector.Run` with a warm-start for migrated domains. It lands in Wave 1 alongside F2 and F3.

1. On startup, the daemon waits for its subscriber's first catch-up (EOSE on the intent and state subscriptions from each relay). Phase 2's per-relay cursor and NIP-77 sync ensure this is incremental, not a full re-fetch.
2. After EOSE, the daemon compares its local state (from the local bbolt store) against relay-held state:
   - For each domain already migrated to intents: query the local store for the daemon's own `(30900, service-pubkey, d=*)` records per domain. Compare fingerprints against what the relay delivered.
   - Any local record not on the relay, or with a newer fingerprint → publish it.
   - Any relay record newer than local → ingest it (handles split-brain recovery).
3. For unmigrated domains, the legacy `RepublishSnapshot` leg still runs (it shrinks as slices land).
4. Current X1 wiring warm-starts all cp-state domains; it does not use the
   intent-domain opt-out list. This supersedes the interim Wave 1 scope.

### 5.4 Acceptance test (Wave 1, slice F4)

```
Test: DaemonRestartPublishesZeroEventsWhenRelaysHoldCurrentState

Setup:
1. Start daemon with intent_domains: [service, environment].
2. Process several service and environment intents, confirm canonical state on relays.
3. Stop daemon.
4. Instrument the relay pool's Publish method to count events.
5. Start daemon again.

Assert:
- After the daemon reaches "ready" (local store synced), the publish count for
  service and environment domains is 0.
- All canonical state on relays matches the daemon's local state.
- The daemon correctly processes new intents after restart.
- Unmigrated domains may still re-publish via their legacy RepublishSnapshot legs.
```

---

## 6. Readiness and Tiers

### 6.1 Today's model

The bootstrapper (`internal/adapters/nostr/bootstrapper.go`) runs replay groups, counts decoded events, and computes a `ReadyTier` (0–3). The `ModePolicy` (`internal/app/mode_policy.go`) gates runners, routes, and background tasks by tier.

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
| **F1: Intent framework** | Intent subscriber (author-scoped), processor pipeline, TrustSet (config + Postgres sources), bounded intent-status publisher (§3.3), ReadinessTracker, relay write-policy for operator intents (§7.1) | Acceptance tests pass for: (a) level-triggered reconciliation — update intent without prior create produces correct state (§1.2/review A); (b) bounded status — two intents for the same entity from one requester produce only one status event on the relay (review B); (c) in-process dispatch — ContextVM handler routes through the pipeline with shared idempotency (review D); (d) author-scoped subscription — events from untrusted pubkeys are not requested and if delivered are dropped silently (review E) | `internal/controlplane/intent_subscriber.go` (new), `internal/controlplane/intent_processor.go` (new), `internal/controlplane/trust_set.go` (new), `internal/controlplane/readiness.go` (new), `internal/controlplane/intent_status.go` (new), `internal/relaysidecar/policy.go` (write-policy update) | Nothing yet |
| **F2: Services intent handler** | Service create/update/delete via intents. Dual-dispatch: ContextVM handlers call in-process processor (no daemon-signed relay intents). Projector service legs removed | Service CRUD works through both relay intent events and legacy ContextVM (in-process). The daemon publishes exactly one canonical 30900 per mutation. Test: create via intent, update via ContextVM, both produce identical state events. Test: level-triggered — service update intent on a cold daemon (no prior create seen) correctly creates the service | `internal/controlplane/service_intent_handler.go` (new), `internal/controlplane/encrypted_route_handlers.go` (dual dispatch added), `internal/service/registry.go` (intent entry point), `internal/adapters/nostr/control_state_contract.go` (read only). **projector.go: only** `publishServiceRegistry` loop in `RepublishSnapshot` and `handleEvent` cases for `EventServiceCreated/Updated/Deleted` (leg deletion; does not edit `Run`) | Projector: `publishServiceRegistry` loop in `RepublishSnapshot`, `handleEvent` cases for `EventServiceCreated/Updated/Deleted`, bus subscriptions for service events. `publishAudit` calls for service bus events (B-16) |
| **F3: Environments intent handler** | Environment create/update/delete via intents. Deployment-unit handling. Revision token validation. Projector environment legs removed | Environment CRUD works through intents. `expected_updated_at` conflict detection works. Test: concurrent updates with stale revision → bounded rejection status. Test: level-triggered environment update on cold daemon | `internal/controlplane/environment_intent_handler.go` (new), `internal/service/registry.go` (environment intent entry). **projector.go: only** `publishEnvironmentRegistry` loop in `RepublishSnapshot` and `handleEvent` cases for `EventEnvironmentCreated/Updated/Deleted` | Projector: `publishEnvironmentRegistry` loop in `RepublishSnapshot`, `handleEvent` cases for `EventEnvironmentCreated/Updated/Deleted` |
| **F4: Warm-start and zero-publish restart** | Replace `Projector.Run`'s startup `RepublishSnapshot` call with warm-start comparison for migrated domains (§5.3). Keep legacy `RepublishSnapshot` for unmigrated domains | Acceptance test `DaemonRestartPublishesZeroEventsWhenRelaysHoldCurrentState` passes for services and environments. The startup snapshot call is conditional on `intent_domains` | **projector.go: only** `Projector.Run` method (warm-start logic replacing the startup `RepublishSnapshot` call; does not edit domain legs or `handleEvent`). New `internal/adapters/nostr/projector_warmstart.go` for the comparison logic | Projector: unconditional startup `RepublishSnapshot` call for migrated domains (replaced by conditional warm-start) |

**Parallel-safe:** F1 is a prerequisite for F2, F3, and F4. After F1 lands, F2, F3, and F4 can run in **parallel**:
- F2 owns service intent handler + service legs in `projector.go` (`RepublishSnapshot` service loop, `handleEvent` service cases).
- F3 owns environment intent handler + environment legs in `projector.go` (`RepublishSnapshot` environment loop, `handleEvent` environment cases).
- F4 owns `Projector.Run` method + new `projector_warmstart.go`. Does not touch any domain legs or `handleEvent`.
- F2 and F3 both read `control_state_contract.go` but do not modify it.

#### 7.1 Relay write policy for operator intents

The sidecar's write policy (`internal/relaysidecar/policy.go`) is updated in F1 to accept operator intents:

- Kind `30900` events with `t=bahia-intent` are accepted if the author pubkey is in the sidecar's admin allowlist (already managed by `internal/relaysidecar/admin.go:238–292` via NIP-86).
- The allowlist is extended to include org member pubkeys, sourced from the same trust sources as the daemon's `TrustSet` (config `authorized_pubkeys` and `bootstrap_owners`, Postgres `org_members` when configured, relay membership events after O1). The sidecar's existing config consumer (`internal/relaysidecar/config_consumer.go`) can subscribe to a daemon-published allowlist event.
- Events without `t=bahia-intent` follow the existing write policy (service pubkey or admin allowlist only).

### Wave 2: State + Builds/Artifacts + Policies

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **S1: Service/env state** | Daemon runtime observations publish 30900 state directly instead of through the projector bus path | Runtime observation produces one canonical state event. No projector re-publish | `internal/service/runtime_lifecycle.go`, `internal/reconcile/reconciler.go` (state observation path), `projector.go` (state legs only) | S2, S3 | Projector: `publishState/publishStateTombstone` in `RepublishSnapshot` and `handleEvent`; `EventReconcileCompleted` audit publish (B-16); `shouldRefreshDNSProjection`'s `EventReconcileCompleted` trigger (B-17 partial) |
| **S2: Builds/artifacts/intents/runs** | Build registration, artifact registration, deployment intent/run lifecycle use events instead of projector re-publish | Builds/artifacts arrive via HiveCI subscriber or ContextVM, publish canonical state directly. No projector leg | `internal/service/build_registry.go`, `internal/service/deployment_coordinator.go`, `projector.go` (build/artifact/intent/run legs only), `publisher.go` (31000-31003 legacy kinds) | S1, S3 | Projector: `publishPublicRouteSnapshotsFromSource` and all build/artifact/intent/run `handleEvent` cases. `Publisher.SetupSubscriptions` for legacy kinds 31000–31003 entirely |
| **S3: Policies** | Policy create/update/delete via intents | Policy CRUD works through intents. Test: create policy via intent, verify state event | `internal/controlplane/policy_intent_handler.go` (new), `internal/service/policy_service.go`, `projector.go` (policy leg only) | S1, S2 | Projector: `publishPolicySnapshots/publishPolicyRegistry` and `handleEvent` policy cases |

**All three are parallel-safe** — they touch different domain services, different projector legs, and different handler files.

### Wave 3: DNS + LLM

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **D1: DNS authority inversion** | DNS zone/endpoint/backend/policy state derived from subscribed events, not from DB polling. DNS agent subscribes to zone state events instead of ContextVM RPC (C-34). DNS agent already has its own local store (bahia-irsry.10.5) | DNS endpoints computed incrementally from state events. DNS agent uses REQ subscriptions. No 30-second DNS reconciler poll. No DNS ContextVM RPC | `internal/reconcile/dns_reconciler.go`, `internal/reconcile/dns_projector.go`, `internal/dnsagent/`, `internal/adapters/dns/dnsmasq_agent.go`, `projector.go` (DNS legs only) | L1 | Projector: all DNS snapshot/tombstone methods. `dns_reconciler.go` 30s ticker. `dnsmasq_agent.go` ContextVM client. DNS ContextVM handlers in `encrypted_route_handlers.go`. `handleEvent` DNS refresh triggers (B-17) |
| **L1: LLM routes/releases/state** | LLM route CRUD and deployment via intents. Gateway reconciler subscribes to state events | LLM route lifecycle works through intents. No 60s LLM reconciler poll | `internal/service/llm_*.go`, `internal/reconcile/llm_reconciler.go`, `projector.go` (LLM legs only) | D1 | Projector: `publishLLMRouteRegistry/publishLLMRouteState` and `handleEvent` LLM cases. `llm_reconciler.go` 60s ticker |

### Wave 4: Backups + ML + Packages + Workers

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **B1: Backups** | Backup recipe/policy/repo config via intents. Run/restore/verification/retention as daemon-authored status events | Backup config via intents. No 30s backup coordinator polls | `internal/service/backup_*.go`, `projector.go` (backup legs only) | M1, P1, W1 | Projector backup snapshot legs. `backup_run_coordinator.go`/`backup_restore_coordinator.go`/`backup_retention_coordinator.go` 30s tickers (B-23) |
| **M1: ML models/versions/endpoints** | ML entity state via intents or daemon-authored events | ML state on relays without projector. No projector ML leg | `internal/service/ml_*.go`, `projector.go` (ML legs only) | B1, P1, W1 | Projector: `publishMLSnapshots` and ML `handleEvent` cases |
| **P1: Packages** | Package publication/intent/approval via intents | Package state on relays without projector | `internal/service/package_*.go`, `projector.go` (package legs only) | B1, M1, W1 | Projector: package `handleEvent` cases |
| **W1: Workers** | Worker state derived from Loom adverts (already canonical). Remove projector worker re-publish | Worker assignment/drain state published once by the handler, not re-published by projector | `internal/controlplane/operator_actions.go` (worker handlers), `projector.go` (worker legs only) | B1, M1, P1 | Projector: `publishWorkerReadModelSnapshots` and worker `handleEvent` cases |

### Wave 5: Orgs/Secrets/Notifications + SBOM + Cleanup

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **O1: Org membership events** | Org/member/invite as encrypted addressable events. TrustSet relay source hydrated from events instead of Postgres | Membership changes via intent events. Trust set relay source updated in real time. Postgres source still active as fallback for orgs without relay events | `internal/controlplane/trust_set.go` (relay event hydration), `internal/controlplane/org_intent_handler.go` (new), `internal/api/handlers/tenants.go` (dual dispatch then delete) | N1, X1 | `internal/api/handlers/tenants.go` REST mutation routes (B-26). Postgres `org_members` as the authoritative source (becomes a read-only fallback) |
| **N1: Secrets + Notifications** | Secrets as NIP-44 encrypted addressable events. Notification channels as addressable config events | Secrets and notifications stored as events, not Postgres-only | `internal/controlplane/secret_intent_handler.go` (new), `internal/controlplane/notification_intent_handler.go` (new), `internal/api/handlers/secrets.go`, `internal/api/handlers/notifications.go` | O1, X1 | `secrets.go` REST mutation routes. `notifications.go` REST mutation routes. `notification_encrypted_handlers.go`. Postgres `pg_secret`, `pg_notification_channel` as authority (B-27) |
| **X1: SBOM + final projector cleanup** | Remove remaining projector legs. Delete `RepublishSnapshot`, the 10-min ticker, and `projectionDedupe` | No projector re-publish. Zero-event restart test passes for all domains | `projector.go` (final cleanup), `projection_dedupe.go` (delete), `publisher.go` (legacy kind setup) | O1, N1 | `RepublishSnapshot` method. `repairInterval` ticker. `projectionDedupe`/`hydrateProjectionCache`. All remaining projector bus subscriptions. `Publisher` legacy kind audit aliases (B-16). `nostr_events` dependency for dedupe hydration |

### Wave 6: Tier model removal + ContextVM handler cleanup

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------|
| **T1: Replace tier model with readiness tracker** | `ModePolicy`, tier gates, bootstrapper tier computation replaced by `ReadinessTracker` | `/ready` returns based on local-store sync, not tier. DB-less daemon serves all domains | `internal/app/mode_policy.go` (delete), `internal/api/middleware/tier_gate.go` (delete), `internal/adapters/nostr/bootstrapper.go` (readiness only), `internal/app/app.go` (startup simplification) | R1 | `ModePolicy` + all tier gate code. `bootstrapper.go` tier computation. `catalog.go` replay groups (B-8, B-9). Runner tier registration in `app.go`. B-10 is closed by deletion |
| **R1: ContextVM mutation handler cleanup** | Delete all ContextVM mutation handlers replaced by intent processing. Delete dual-dispatch wrappers | No ContextVM CRUD handlers remain. ContextVM stays only for assistant/secret-reveal/log-fetch | `internal/controlplane/encrypted_route_handlers.go` (CRUD methods deleted), `internal/controlplane/reactor.go` (deploy/rollback handlers that moved to intents) | T1 | ContextVM methods: `service/create`, `service/update`, `service/delete`, `environment/*`, `policy/*`, `llm/route-*`, `backup/*`, `dns/*`, `package/*`. All per-domain handlers in `encrypted_route_handlers.go`. Dual-dispatch wrappers from §4.1 |

---

## 8. Decisions (binding)

The following questions from the initial design are decided. These decisions are binding for all implementation slices.

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| 1 | Single-tenant or multi-org? | **Multiple orgs.** | Bahia already has multiple orgs. `TrustSet` is keyed `org → pubkey → role` with pluggable sources from the start (§2.2) |
| 2 | Config pubkeys: org members or fleet-ops only? | **Fleet-ops only.** | `authorized_pubkeys` remain the fleet-operator gate (today's reactor gate). They are **not** implicit org members and cannot authorize org-scoped mutations through the intent path. Per-org bootstrap uses `bootstrap_owners` or Postgres (§2.2) |
| 3 | REST mutation deletion timing | **Delete per slice** once dual dispatch is proven. REST reads stay until Phase 5 | Dual dispatch (§4.1) ensures existing clients keep working. Once the intent path is verified for a domain, the REST mutation route is dead code and should be removed to prevent regression (charter invariant 7) |
| 4 | Postgres removal timeline | **Stays as optional derived index through Phase 5.** No Phase 3 slice may require it | REST/MCP reads and the CLI still use Postgres-backed queries until Phase 5 replaces them with relay REQs. No Phase 3 slice may fail when Postgres is absent (§3.4) |
| 5 | Encrypted or public membership? | **Encrypted** (NIP-59 gift-wrap) | Org composition and roles are sensitive. In Phase 4, the web derives its own role by decrypting the membership events gift-wrapped to its pubkey and reading the `role` field from the content |
| 6 | DNS agent local store | **Moot** — the local store already exists (bahia-irsry.10.5) | The DNS agent has a local bbolt store from Phase 2. D1 (Wave 3) has it subscribe to zone state events via that store instead of ContextVM RPC |

---

## 9. Risks

- **Level-triggered ordering.** The daemon sees only the latest intent per `(kind, pubkey, d)`. If two trusted authors publish competing intents for the same coordinate, the daemon picks the newest by `(created_at, lowest id)`. Clock skew between operators could cause surprising results. Mitigation: `intent_id` UUIDv7 encodes millisecond-precision time; `expected_updated_at` revision checks catch conflicts; the bounded status event reports which intent won.
- **Trust set cold start.** If no membership events exist on relays and Postgres is also absent, the daemon trusts only `bootstrap_owners` from config. If config is wrong, the daemon trusts nobody and drops all intents silently. Mitigation: the daemon logs prominently when the trust set has no members for any org, with instructions to set `bootstrap_owners`.
- **Dual-path idempotency.** During the transition, a mutation arriving via both ContextVM and a relay intent must not be applied twice. Mitigation: both paths feed the same pipeline with the same `intent_id` / idempotency key and the same local store. The dedup check at step 1 prevents double application.
- **Author subscription churn.** Changing the trust set requires closing and re-opening the intent REQ with a new `authors` list. Frequent membership changes could cause subscription churn. Mitigation: debounce trust-set updates (e.g. 5-second window) before re-subscribing.
- **Local store corruption.** The bbolt local store is the daemon's memory. Corruption means lost cursors and idempotency state. Mitigation: NIP-77 sync rebuilds the store from relays; the daemon detects corruption and triggers a full resync.

---

## 10. How to Add a Domain Slice (F2/F3 Guide)

This section documents the APIs F1 provides for downstream domain slices.

### 10.1 Implement `DomainHandler`

```go
// internal/controlplane/service_intent_handler.go
type ServiceIntentHandler struct {
    registry *service.RegistryService
    // ...
}

func (h *ServiceIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
    // Level-triggered: intent.Content is the full desired state.
    // Reconcile the entity toward it, regardless of whether you've seen
    // prior events for this coordinate.
    switch intent.Op {
    case "create", "update":
        // Upsert the entity from intent.Content.
    case "delete":
        // Remove the entity and publish a tombstone.
    }
    return nil
}

func (h *ServiceIntentHandler) PermissionFor(op string) domain.Permission {
    return domain.PermWriteServices
}
```

### 10.2 Register the handler at startup

In `internal/app/app.go`, after the `intentProcessor` is constructed:

```go
intentProcessor.RegisterHandler("service", &controlplane.ServiceIntentHandler{
    registry: registry,
})
```

### 10.3 Register the domain for default-on processing

Add its `RegisterHandler` call in `internal/app/app.go` and its name to
`controlplane.RegisteredIntentDomains`. The app test compares the two sets so
new handlers cannot silently be omitted from default-on processing. Operators
may temporarily disable a domain with `nostr.intent_domains_disabled`.

### 10.4 Wire dual dispatch

In the existing ContextVM handler for the domain (e.g. `encrypted_route_handlers.go`), after authorization:

```go
intent := &controlplane.Intent{
    Domain:     "service",
    Op:         "create",
    OrgID:      orgID,
    IntentID:   requestIDOrMint(),
    Coordinate: entityID.String(),
    Content:    parsedContent,
    Actor:      event.PubKey.Hex(),
}
if err := intentProcessor.ProcessInProcess(ctx, intent); err != nil {
    return err
}
```

Both paths (relay and in-process) share one idempotency store, so double-dispatch is impossible.

### 10.5 Key APIs

| Type | Method | Purpose |
|------|--------|---------|
| `IntentProcessor` | `RegisterHandler(domain, handler)` | Register a domain handler at startup |
| `IntentProcessor` | `ProcessInProcess(ctx, intent)` | In-process dual dispatch entry point |
| `IntentProcessor` | `ProcessRelayIntent(ctx, event)` | Relay subscription entry point (used by IntentSubscriber) |
| `TrustSet` | `HasPermission(ctx, orgID, pubkey, perm)` | Authorization check |
| `TrustSet` | `RoleFor(ctx, orgID, pubkey)` | Role lookup for display |
| `TrustSet` | `SetRelayMembers(orgID, members)` | O1: populate relay membership |
| `IntentStatusPublisher` | `PublishConflict(ctx, intent)` | F3: expected_updated_at mismatch |
| `ReadinessTracker` | `IsReady()` | Combined readiness across all filters |
| `ReadinessTracker` | `Progress()` | Per-filter readiness for health endpoint |
| `ParseIntent(event)` | — | Parse a kind-30900 event into an Intent |
