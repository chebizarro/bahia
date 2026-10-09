# Intents, trust and the daemon's reaction

Every control-plane mutation in Bahia is a **signed intent event** published by
the operator or user; the daemon subscribes to intents, validates and authorizes
them, applies side effects, and publishes the **canonical state** under its own
service key. Postgres, when configured, is a derived index written after the
canonical event and never required by any path described here.

Related pages: [outbox delivery](outbox-delivery.md) (how canonical events reach
relays), [confidential state](confidential-state.md) (encrypted domains),
[entity identity](entity-identity.md) (who mints ids), [web](web-store-first.md)
and [CLI/MCP](cli-and-mcp.md) (how clients produce intents).

## 1. Intent events

### 1.1 Kind and coordinate

An intent is a **kind `30900`** addressable event signed by the operator, with
`d` = the entity coordinate. The daemon's canonical state for the same entity is
also kind `30900`, signed by the **service pubkey**. The relay keeps the latest
event per `(kind, pubkey, d)`, so operator intents and daemon state occupy
separate coordinate spaces and never collide:

- `(30900, operator-pubkey, d=<coordinate>)` — desired state.
- `(30900, service-pubkey, d=<coordinate>)` — confirmed state.

### 1.2 Level-triggered desired state

The relay keeps only the newest intent per author and coordinate, so a daemon
that was offline may see an update or a delete without ever having seen the
create. The processor therefore **reconciles the entity toward the newest trusted
intent's complete desired state**; it does not replay an operation log.

- `op` (`create`, `update`, `delete`, plus domain verbs such as `deploy`,
  `rollback`, `approve`) is **advisory**: it selects error messages and the
  side-effect path, never a precondition on earlier ops.
- Create and update content is the **complete desired state**, not a diff.
- Delete content is `{"deleted": true, "id": "<entity-id>"}`. The daemon runs
  cleanup side effects and publishes a `30900` tombstone (`deleted: true`) under
  the service key. NIP-09 is not used for entity deletion: the tombstone is the
  record that the entity was removed deliberately.

### 1.3 Tag grammar

```json
{
  "kind": 30900,
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
  "content": "{\"id\":\"<entity-uuid>\",\"name\":\"api\",...}"
}
```

- `d` — entity coordinate per `docs/event-spec.md` (`<service-id>`,
  `service:<sid>:environment:<eid>`, `artifact:<id>`, ...).
- `domain` — the handler family (see §3.4 for the registered list).
- `schema` — `bahia.intent.<domain>.v1`; the daemon's state schema is
  `bahia.cp-state.v1`.
- `t` — `bahia-intent` plus the domain topic. **Relays index only
  single-letter tags**; every REQ filter scopes by `#t`, never by `#domain` or
  `#schema` (the `TestNoNewMultiLetterREQFilters` ratchet enforces this).
- `org` — the org the entity belongs to; fleet-only domains (DNS, ML, worker,
  relay, tool) omit it and authorize against the fleet operator list.
- `intent_id` — UUIDv7 minted by the client per attempt, carried in tags and
  content. It is for **idempotency and correlation only**; it never affects
  ordering or conflict resolution.
- `expected_updated_at` (content, updates only) — the canonical record's
  `updated_at` RFC3339/RFC3339Nano string copied by the client. Numeric values
  are rejected; comparison is at microsecond precision.

Parseable wire examples for the deployment-operation domains live in
`web/tests/fixtures/deployment-intents.json`; the Go/JS cross-language fixtures
are in `pkg/client/intent_cross_language_fixture_test.go`.

### 1.4 Ordering and conflicts

The **newest intent by `(created_at, lowest event id)` across all trusted
authors** for a coordinate wins. If the winner carries `expected_updated_at` and
it does not match the canonical record, the daemon publishes a `conflict` status
(§3.3) and applies nothing; the client re-reads, re-merges and re-signs. A
create without `expected_updated_at` is latest-wins unconditionally. Processed
`intent_id`s are stored in the daemon's local bbolt store, so a retry, a relay
replay after restart, or the in-process MCP path never applies an intent twice.

### 1.5 Encrypted intents

Org membership, secrets and notification channels are sensitive. Their intents
are the same inner `30900` event, NIP-44-encrypted to the service pubkey and
published as a **NIP-59 gift wrap (kind `1059`)** tagged `p=<service-pubkey>`.
The daemon unwraps, verifies the inner signature, and then checks the inner
author against the trust set. Secret values are additionally NIP-44-encrypted
to the service key inside the content. How the resulting canonical records are
encrypted for members is described in [confidential state](confidential-state.md).

## 2. Authority and trust

### 2.1 `TrustSet`

`internal/controlplane/trust_set.go` resolves `HasPermission(orgID, pubkey,
permission)` through three sources, in priority order:

1. **Relay membership** — org-member cp-state records the daemon itself
   publishes (hydrated into the trust set as they are processed). When any exist
   for an org they are used exclusively.
2. **Postgres `org_members`** — read-only delegate to `auth.RBAC`, used only when
   a database is configured and the org has no relay membership yet.
3. **Config `bootstrap_owners`** — `nostr.bootstrap_owners: {"<org-uuid>":
   "<hex-pubkey>"}` grants owner-level permission for an org that has no members
   from either source above.

Roles are `viewer < deployer < admin < owner`; permissions are the
`domain.Permission` constants (`services:write`, `deployments:approve`, ...).

**Fleet operators are never implicit org members.** `nostr.authorized_pubkeys`
authorizes only fleet-scoped domains (DNS, LLM, ML, workers, backups, relay
settings, tools, policy evaluation, saga recovery) through `FleetOperatorGate`;
a handler opts into that gate by implementing `FleetScopedHandler`. Operator
allowlists are also published as encrypted records so browsers can trust
operator-authored documents — see [confidential state](confidential-state.md#operator-allowlists).

### 2.2 Untrusted authors

The intent subscription is **author-scoped** (`authors` = the trust set's
current pubkeys, re-issued when the set changes), so relays are never asked for
unknown authors' events. An intent that still arrives from an untrusted pubkey
is **dropped silently** — the daemon never signs a rejection for an unknown
author, which would let anyone force daemon-signed output. Rejection statuses
are published only for a **known principal** that lacks the permission or whose
intent fails validation.

## 3. Daemon reaction

### 3.1 Subscriptions

One long-lived REQ per input class against the relay pool, each with a
per-(relay, filter) cursor in the local store:

```go
// Plaintext intents from trusted authors.
nostr.Filter{Kinds: {30900}, Authors: trustSet.AuthorPubkeys(), Tags: {"#t": {"bahia-intent"}}, Since: cursor}
// Encrypted intents: the outer 1059 has an ephemeral author, so scope by #p and
// check the inner signer after unwrapping.
nostr.Filter{Kinds: {1059}, Tags: {"#p": {servicePubkey}}, Since: cursor}
```

`IntentAuthorsSyncer` closes and re-opens the plaintext subscription with the
new `authors` list when membership changes.

### 3.2 Pipeline

`IntentProcessor` (`internal/controlplane/intent_processor.go`) runs the same
pipeline for relay intents (`ProcessRelayIntent`) and in-process intents from
MCP (`ProcessInProcess`), sharing one idempotency store:

1. **Deduplicate** by `intent_id`.
2. **Validate** structure, tags, content schema, entity id format
   (UUIDv7; v4 accepted), completeness, and `expected_updated_at`.
3. **Authorize** via `TrustSet` (or `FleetOperatorGate` for fleet-scoped
   handlers). Untrusted author → drop; known principal without permission →
   rejection status.
4. **Select the winning intent** for the coordinate (§1.4).
5. **Reconcile** toward the desired state through the domain handler: no
   current state → create (whatever `op` says); newer intent → update;
   `deleted: true` → cleanup side effects.
6. **Record** the processed `intent_id` locally; upsert the optional Postgres
   index (its failure never fails the pipeline).
7. **Publish canonical state**: build the `30900` record through
   `internal/adapters/nostr/control_state_contract.go`, skip if the fingerprint
   equals the latest stored output for the coordinate, otherwise sign and publish
   through the [outbox](outbox-delivery.md).

### 3.3 Intent status events

Processing outcome is a **kind `30315`** (NIP-38 status) event from the service
key with `d = intent-status:<requester-pubkey>:<entity-coordinate>` — one
replaceable record per requester and entity, so status never grows without
bound. Tags: `domain=intent`, `status`, `t=intent-status`, `e=<intent-event-id>`,
`p=<requester>`, `intent_id`, and a NIP-40 `expiration` of `created_at + 7 days`
(the sidecar sweeps expired events). Content:
`{"intent_id","coordinate","result"}`.

Status values: `accepted`, `rejected` (authorization or validation failure for
a trusted author), `conflict` (stale `expected_updated_at`), `superseded` (a
newer intent from another trusted author won).

### 3.4 Domain handlers

A handler implements `DomainHandler`:

```go
type DomainHandler interface {
    HandleIntent(ctx context.Context, intent *Intent) error // level-triggered; non-nil error → rejection status
    PermissionFor(op string) domain.Permission
}
```

and optionally `FleetScopedHandler` (fleet-operator authorization instead of org
RBAC). Registration is in `internal/app/app.go` (`RegisterHandler("<domain>",
handler)`), and the domain name must be added to
`controlplane.RegisteredIntentDomains`; the app test compares the two sets so a
handler cannot be left out of default-on processing. Registered domains:
`service`, `environment`, `policy`, `package`, `backup`, `llm`, `ml`, `dns`,
`worker`, `deployment`, `runtime`, `org`, `secret`, `notification`, `artifact`,
`adoption`, `build`, `tool`, `security`, `sbom`, `relay`. Every registered
domain is processed by default; `nostr.intent_domains_disabled` opts specific
domains out.

Side effects must be idempotent, because a crash between the side effect and
step 6 replays the intent on restart: creates resolve by id
(`resolveCreateByID`), deployments compare the runtime's desired hash, DNS
applies by serial, backup job ids are UUIDv7.

Backup run request intake rejects while canonical execution recovery is
unavailable. Its receipt reader examines service-signed kind-30900 backup-run
state and requires the exact event's retained control-plane outbox entry to
record relay quorum delivery and at least one accepted relay. A local event,
pending outbox entry, absent/pruned delivery record, or PostgreSQL row is not
an acceptance receipt. Even an ACKed prior run is not resumed by this reader;
the executor still lacks canonical recipe, repository, policy, and step
checkpoint recovery.
The reader compares the run's immutable request fields with the signed
operator intent and rejects absent or divergent execution inputs. MCP
`request_backup_run` does not mint an unsigned in-process intent: it requires
the complete NIP-01 operator-signed event with a NIP-40 expiration no more
than 15 minutes after creation, and observes that exact event in the local
relay-synced event store before handing it to the intent processor. Local
observation is not a relay `OK`, delivery quorum, or acceptance receipt.
The signed payload binds run UUIDv7, recipe, repository, policy, backend,
target, verification mode, immutable service-signed configuration event IDs,
and the exact configuration snapshot. Repositories with a credential profile
also require an immutable credential-version ID in the snapshot. A version ID
is a binding, not proof that a secret can be recovered; canonical credential
resolution, atomic acceptance and execution checkpoint recovery are still
required before a new run may execute. Current run intake therefore continues
to reject even a complete signed request without an ACKed run-state receipt.

### 3.5 Canonical state and who signs it

The **daemon signs canonical state**; the client's intent is a request. The
canonical record carries the fields only the daemon knows (observation
metadata, deployment-unit ids, applied runtime state) and references the intent
with `e=<intent-event-id>` and `intent_id` tags. Record families inside the
`30900` envelope are discriminated by the `legacy_kind` tag whose values are
`kinds.CPStateFamily` constants — see [architecture ratchets](ratchets.md#cpstatefamily-allocation)
for how a new family is allocated.

### 3.6 Readiness

The daemon is `ready` when every registered intent-subscription filter has
completed its first catch-up: EOSE from at least one relay and NIP-77
reconciliation finished (`controlplane.ReadinessTracker`). `GET /ready` returns
503 while any readiness check fails — `intent_readiness` reports "filters
syncing" until then, alongside `relay_quorum`, `bootstrap_ready` and
`background_runners` — and 200 once all pass. Postgres availability does not affect
readiness. The optional connection and migration probe has a configurable
two-second startup budget (`db.startup_probe_timeout`, capped at five seconds);
if it does not finish, the daemon starts in relay-first reduced mode
and a non-required background runner retries PostgreSQL migration without
restarting the process. SQL-backed capabilities attach on a subsequent daemon
start when the cache is promptly available. Historical assistant sessions are
resolved from validated relay events on demand, not from PostgreSQL rows. A
cancelled PostgreSQL query may leave pgx transport cleanup running for up to
15 seconds in the background; startup and recovery probes skip while that
cleanup is pending. `postgres_index` reports this without failing readiness. A
daemon that holds committed state no relay has accepted stays ready but reports
`canonical_delivery = warn` — see
[outbox delivery](outbox-delivery.md).

## 4. Config-fabric events

Fleet configuration is not an intent domain. Operators sign NIP-51 kind `30000`
membership lists and NIP-78 kind `30078` policy documents directly
(`bahia config publish`); the relay sidecar's config consumer applies them and
publishes schema-v3 config-status `30900` records at
`config-status:<service>:<policy>:<scope>`. Only `nostr.authorized_pubkeys`
and the sidecar's explicit config-trusted pubkeys may author these two kinds.
