# Bahia Nostr Event Implementation Guide

This guide is the Bahia-specific implementation policy for Nostr event kinds, event shapes, and Cascadia fleet interoperability. It adapts the Cascadia Nostr-native event strategy into rules that agents working in this repository can apply directly.

Use this guide before adding, publishing, subscribing to, decoding, migrating, or documenting any Nostr event.

## Core rule

Do not allocate or revive a Bahia-specific event kind just because a new semantic exists.

First ask whether the semantic is one of these:

1. a ContextVM JSON-RPC intent,
2. a NIP-51 collection,
3. a parameterized replaceable state object,
4. a NIP-38 status,
5. a NIP-58 badge or attestation,
6. a NIP-78 app data object,
7. a ContextVM discovery announcement,
8. an existing NIP.

If yes, use that mechanism. A new kind requires a written justification that its relay behavior, replaceability, retention, or indexing requirements differ from every existing mechanism.

## Bahia's four Nostr layers

| Layer | What it means | Bahia mechanism |
|---|---|---|
| Intent | A private command asking something to happen | ContextVM kind `25910`, usually wrapped with CEP-4/NIP-59 `1059` or `21059` |
| Observable | Public or scoped facts produced by execution | `30900` state, `4903` audit, `30315` status, and relevant standard NIPs |
| Collection | Replaceable sets such as memberships, relay sets, inventories, allowlists | NIP-51 kinds such as `30000`-`30004`, especially `30002` relay sets |
| State | Current snapshots, app config, registries, projections | `30900` for control-plane state and `30078` for app-specific data |

The normal flow is:

```text
ContextVM intent (private command)
  -> Bahia validates and executes
  -> canonical observable events (status, state, audit, app data)
  -> clients subscribe and converge from relay history + realtime events
```

The JSON-RPC response to a ContextVM request is only an acknowledgment or immediate error. Long-running completion is proved by observable events.

## Decision tree for implementers

### 1. Is this asking Bahia or an agent to do something?

Use ContextVM.

- Kind: `25910`
- Content: JSON-RPC 2.0
- Method: `<domain>/<operation>`
- Sensitive payloads: wrap the ContextVM message with CEP-4/NIP-59 kind `1059`, or `21059` when ephemeral gift-wrap is supported.
- Correlate retries with a stable idempotency key.

Examples:

| Operation | ContextVM method |
|---|---|
| Deploy service | `service/deploy` |
| Restart service | `service/restart` |
| Roll back service | `service/rollback` |
| Create DNS zone | `dns/zone-create` |
| Apply DNS policy | `dns/policy-apply` |
| Run backup | `backup/run` |
| Restore backup | `backup/restore` |
| Cordon worker | `worker/cordon` |
| Promote package | `package/promote` |
| Scan adoption target | `adoption/scan` |
| Import adoption target | `adoption/import` |

Do not create request/status/result kind triplets for new operations.

### 2. Is this progress or current operational status?

Use NIP-38 status.

- Kind: `30315`
- Use `d` to identify the status coordinate.
- Include `status`, `domain`, and `schema` tags.
- Include `e` when the status is correlated with a ContextVM request or other source event.
- Include resource tags such as `service`, `environment`, `worker`, `artifact`, or `run` when available.

Status events are for short-lived operational state such as `running`, `healthy`, `degraded`, `available`, `draining`, `failed`, or `completed`.

### 3. Is this durable current state or a read-model projection?

Use canonical state.

- Kind: `30900`
- Parameterized replaceable by `(kind, pubkey, d)`.
- `d` must be stable and scoped: `<domain>:<entity>:<id>` or the narrower convention already used by the feature.
- Required tags: `d`, `domain`, `schema`.
- Strongly recommended tags: `entity`, `status`, resource tags.
- Content must be a complete current-state snapshot, not a patch.

Use NIP-78 kind `30078` instead when the object is app-specific data, user/application settings, local UI state, or a registry whose semantics are not a fleet-wide control-plane projection.

### 4. Is this an immutable audit fact or attestation?

Use Bahia audit.

- Kind: `4903`
- Required tags: `domain`, `type`, `schema`.
- Include `e` for source/correlation when possible.
- Include `p` for responsible or requesting actors where appropriate.
- Include resource tags such as `service`, `environment`, `artifact`, `worker`, `package`, `dns_zone`, or `run`.
- Audit events should be treated as protected and long-retention. They are not normal delete targets.

Use NIP-58 badges for permission or capability grants; use `4903` for the audit trail describing the grant or revocation.

### 5. Is this service, tool, resource, or prompt discovery?

Use ContextVM discovery.

| Kind | Purpose |
|---|---|
| `11316` | Server announcement |
| `11317` | Tools list |
| `11318` | Resources list |
| `11319` | Resource templates list |
| `11320` | Prompts list |

Use NIP-89 kind `31990` only when advertising application handler capability to the broader Nostr ecosystem. Do not use Bahia's old `31974` system discovery kind in runtime code.

### 6. Is this relay topology or bootstrap routing?

Use existing relay-list NIPs and existing protocol relay hints. Bahia does not allocate relay-routing kinds.

- `30002`: NIP-51 relay sets for Bahia browser, ContextVM, service, and other service-authored relay-purpose groups.
- `10002`: NIP-65 relay lists for general author relay preferences.
- `10050`: DM relay lists when direct-message routing is required.
- NIP-34 `30617` repository `relays` tags: repository/ngit routing hints for that repository only.
- NIP-11 metadata and optional NIP-66 monitor events: advisory relay capability/liveness inputs only; they do not establish Bahia service trust.
- NIP-86: optional HTTP relay-owner administration with NIP-98 authorization for Bahia-owned or Bahia-authorized relays. It is not ContextVM mutation transport and does not replace NIP-42 websocket AUTH.

Do not invent relay routing kinds.

Bahia relay-purpose taxonomy:

| Purpose | Owner | Canonical mechanism | Trust / exposure boundary |
|---|---|---|---|
| Public browser bootstrap/read models | Bahia service | NIP-51 `30002`, `d=bahia-browser-v1` | Public browser bootstrap boundary; sidecar public URL may be first by deployment policy. |
| ContextVM request/reply | Bahia service | NIP-51 `30002`, `d=bahia-contextvm-v1` | Preferred relay set for ContextVM mutation traffic; absence may fall back to browser relays with degraded metadata. |
| Service publish/backfill | Bahia service | NIP-51 `30002`, `d=bahia-service-v1`; advisory NIP-65 `10002` | Backend/service relay boundary; not automatically public browser bootstrap. |
| User/operator preferences | User/operator pubkey | NIP-65 `10002` | General author routing only; not service-strategy authorization. |
| Repository/ngit | Repository maintainer or SoulFactory | NIP-34 `30617` `relays` tags and `30618` state | Repository-specific routing hints; not generic control-plane relay policy. |
| DM receive routing | Receiving identity | NIP-51 `10050` | DM-enabled features only; public bootstrap does not imply DM readiness. |
| FIPS public adverts | FIPS/Bahia operator | Existing FIPS overlay advert contract plus explicit bridge relay config | Public advert boundary; safe only for information intentionally exposed as FIPS overlay metadata. |
| FIPS/Bahia endpoint/control | Bahia service/operator | ContextVM relay sets or explicit bridge relay config | Sensitive endpoint/control boundary; sharing with public relays is an explicit exposure decision. |
| Relay capability/liveness | Relay or trusted monitor | NIP-11; optional NIP-66 `10166`/`30166` | Advisory metadata; never overrides service pubkey trust or configured relay policy. |
| Relay administration | Bahia relay owner/operator | Optional NIP-86 over HTTP with NIP-98 auth | Administrative allow/ban/kind/metadata controls only; not application/control-plane mutation transport. |

### 7. Is this a list, membership, subscription, permission set, inventory, or registry of references?

Use NIP-51 collections.

Common Bahia conventions:

| Semantic | Preferred NIP-51 shape |
|---|---|
| Operators for a scope | kind `30000`, `d=operators:<scope>`, `p` tags |
| Approvers for a service/env | kind `30000`, `d=approvers:<service>:<environment>`, `p` tags |
| Relay bootstrap set | kind `30002`, `d=bahia-browser-v1`, `relay` tags |
| Watched repositories | kind `30001` or `30004`, stable `d`, `a`/`e`/URL tags as appropriate |
| Artifact or package inventory | kind `30004`, stable `d`, `a`/`e`/`r` tags |

Collections are updated by replacing the whole list. Delete collections with NIP-09 kind `5` when the list itself is removed.

### 8. Is this a permission, trust claim, certification, or capability grant?

Use NIP-58 badges.

- Kind `30009`: badge definition.
- Kind `8`: badge award.
- Use badge definitions for grants such as `can-deploy`, `can-approve`, `can-merge`, `can-sign`, `can-operate`, and `security-reviewed`.
- Scope badges with tags and content fields rather than creating one kind per permission.
- Emit `4903` audit events for security-relevant badge grants and revocations.

### 9. Is this a delete?

Use NIP-09 kind `5` for event deletion semantics.

For business-level deletes, publish a ContextVM method such as `service/delete`, then have Bahia publish canonical state showing tombstone/deleted status and any related audit fact. Do not create `DeleteFooKind` events.

## Canonical Bahia production kinds

These are the main event kinds production runtime code should publish or subscribe to for Bahia control-plane behavior.

| Kind | Name | Use |
|---:|---|---|
| `25910` | ContextVM message | JSON-RPC mutation intent and direct ContextVM responses |
| `1059` | NIP-59 gift wrap | Stored encrypted ContextVM envelope |
| `21059` | ephemeral gift wrap | Ephemeral encrypted ContextVM envelope when supported |
| `30315` | NIP-38 status | Operational status/progress |
| `30900` | Cascadia/Bahia control state | Durable state/read-model projection |
| `4903` | Cascadia/Bahia audit | Immutable audit facts and attestations |
| `11316`-`11320` | ContextVM discovery | Server/tool/resource/prompt/template discovery |
| `30002` | NIP-51 relay set | Browser/service/operator relay topology |
| `30078` | NIP-78 app data | App-specific data, settings, registries, detailed projections |
| `5` | NIP-09 deletion | Delete event references |

Other standard NIPs may be used directly when their semantics fit. Examples include NIP-58 badges, NIP-65 relay lists, NIP-89 app handlers, NIP-98 HTTP auth, NIP-70 protected events, and NIP-40 expiration.

## Runtime-prohibited legacy kinds

Legacy Bahia kind constants may still exist for migration inventory, fixture decoding, or historical documentation. They are not production runtime policy.

Production runtime code must not publish or newly subscribe to these legacy families:

- request kinds `5941`-`6006`, `38390`-`38399`, `38400`-`38431`, and older encrypted request/result kinds `5980`/`7980`
- status/result ranges `6941`, `6961`-`6997`, `7941`-`7997`
- old state/read-model ranges `30350`-`30353`, `31400`-`31411`, `31961`-`32003`
- old audit ranges `31000`-`31024`, `31310`-`31311`
- old discovery kind `31974`

If such a kind appears in production code, it must be one of these explicitly justified cases:

1. read-only legacy migration code in `internal/nostrmigration`,
2. tests proving legacy kinds are transformed or rejected,
3. documentation describing historical behavior,
4. metadata tag values such as `legacy_kind` on a canonical migrated event.

The presence of a `legacy_kind` tag does not make the event legacy. The event's `kind` field must still be canonical.

## Required event shapes

### ContextVM intent

Unencrypted example for local/dev or non-sensitive traffic:

```json
{
  "kind": 25910,
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["domain", "service"],
    ["op", "deploy"],
    ["schema", "bahia.intent.service.v1"],
    ["d", "deploy-api-prod-01"]
  ],
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"deploy-api-prod-01\",\"method\":\"service/deploy\",\"params\":{\"service\":\"api\",\"environment\":\"prod\",\"artifact\":\"api:v2.4.0\"}}"
}
```

Sensitive production traffic should encrypt that message as a NIP-59 gift wrap:

```json
{
  "kind": 1059,
  "pubkey": "<random-wrapper-pubkey>",
  "tags": [["p", "<bahia-service-pubkey>"]],
  "content": "<NIP-44 encrypted inner ContextVM event>"
}
```

After unwrap, Bahia must verify the inner event signature and authorize the inner sender before executing.

### NIP-38 status

```json
{
  "kind": 30315,
  "tags": [
    ["d", "service:deploy:deploy-api-prod-01"],
    ["domain", "service"],
    ["schema", "bahia.status.service.v1"],
    ["status", "running"],
    ["service", "api"],
    ["environment", "prod"],
    ["e", "<contextvm-request-event-id>"]
  ],
  "content": "{\"step\":\"rolling-update\",\"message\":\"deployment started\"}"
}
```

### Canonical state projection

```json
{
  "kind": 30900,
  "tags": [
    ["d", "service:state:api:prod"],
    ["domain", "service"],
    ["entity", "service-state"],
    ["schema", "bahia.service-state.v2"],
    ["service", "api"],
    ["environment", "prod"],
    ["status", "healthy"]
  ],
  "content": "{\"service\":\"api\",\"environment\":\"prod\",\"desired_hash\":\"...\",\"observed_hash\":\"...\"}"
}
```

Projection decoders must reject canonical events with an empty or missing family/entity coordinate. A valid `30900`, `30315`, `4903`, or `30078` event must decode into an explicit projection family.

### Audit fact

```json
{
  "kind": 4903,
  "tags": [
    ["domain", "service"],
    ["type", "deployment"],
    ["schema", "bahia.audit.deployment.v1"],
    ["service", "api"],
    ["environment", "prod"],
    ["artifact", "api:v2.4.0"],
    ["e", "<contextvm-request-event-id>"],
    ["p", "<operator-pubkey>"]
  ],
  "content": "{\"action\":\"deploy\",\"result\":\"accepted\"}"
}
```

### ContextVM discovery

```json
{
  "kind": 11316,
  "pubkey": "<bahia-service-pubkey>",
  "tags": [
    ["name", "Bahia"],
    ["support_encryption"],
    ["support_encryption_ephemeral"],
    ["d", "bahia-contextvm-v1"]
  ],
  "content": "{\"protocolVersion\":\"2025-07-02\",\"serverInfo\":{\"name\":\"bahia\",\"version\":\"...\"},\"capabilities\":{\"tools\":{\"listChanged\":true}}}"
}
```

Relay topology is separate and should be published as NIP-51 relay sets:

```json
{
  "kind": 30002,
  "tags": [
    ["d", "bahia-browser-v1"],
    ["relay", "wss://relay.example.test"]
  ],
  "content": ""
}
```

## Cascadia fleet interoperability

Bahia should interoperate with Cascadia fleet applications by speaking standard Nostr mechanisms first and Bahia-specific schemas second.

Use these interop defaults:

| Need | Fleet mechanism |
|---|---|
| Command another agent/app | ContextVM `25910` JSON-RPC method, encrypted when sensitive |
| Discover agent/app capabilities | ContextVM `11316`-`11320`; NIP-89 `31990` for app-handler discovery |
| Discover relays | NIP-51 `30002`, NIP-65 `10002`, and DM relay lists `10050` where appropriate |
| Track operational status | NIP-38 `30315` |
| Track state/read models | `30900` with shared `domain`, `entity`, and `schema` tags |
| Track app data/config | NIP-78 `30078` |
| Manage memberships/inventories | NIP-51 lists |
| Represent permissions/capabilities | NIP-58 badges, plus audit `4903` for security-relevant changes |
| Delete event references | NIP-09 `5` |

Common tags across fleet events:

- `domain`: routing and ownership area (`service`, `dns`, `backup`, `worker`, `package`, `ml`, `adoption`, `policy`, `soul-factory`)
- `schema`: content schema identifier; consumers must check it before parsing
- `d`: replaceable coordinate or idempotency/correlation key when applicable
- `e`: source or parent event id
- `p`: actor, requester, recipient, or relevant pubkey
- resource tags: `service`, `environment`, `worker`, `artifact`, `package`, `run`, `project`, `workflow`

Do not rely on relay indexing for multi-character tags unless the sidecar or target relay explicitly supports it. Still include semantic tags for consumers and internal sidecar indexing.

## Migration app rules

Bahia has already deployed older events. Legacy events are migrated by a startup migration module rather than by keeping legacy runtime behavior alive.

Implementation rules:

1. Legacy subscriptions, decoders, and transforms belong in `internal/nostrmigration` or tests for that module.
2. The migration must be idempotent. Re-running startup must not duplicate canonical events.
3. Migrated events must publish canonical `kind` values and may include metadata tags such as `legacy_kind`, `migrated-from`, `migration`, and `schema`.
4. Runtime publishers and subscribers should not include legacy kind support just to ease rollout.
5. The relay sidecar should allow canonical production kinds and reject legacy runtime kinds unless a migration path explicitly needs read access.
6. If a new migration transform is added, update `docs/control-planes.md`, `docs/event-spec.md`, `docs/nostr-commands.md`, `docs/protocol-compatibility.md`, and the PSTF verification evidence for the migration feature.

## Implementation checklist

Before adding or changing Nostr event code:

- [ ] Use `internal/kinds/kinds.go` rather than hand-written numeric constants.
- [ ] Confirm the kind is canonical production policy or explicitly migration-only.
- [ ] Check sidecar allow/deny behavior for reads and publishes.
- [ ] Add or update event shape tests for required tags, content schema, and projection family decoding.
- [ ] Add idempotency and dedupe behavior for handlers.
- [ ] Verify relay `OK`, `CLOSED`, and `AUTH` paths.
- [ ] Subscribe with scoped filters and handle EOSE as historical catch-up, not completion.
- [ ] Update docs when event kinds, tags, schemas, or migration behavior change.
- [ ] Create Beads for deferred work rather than leaving comments or TODOs.

If the implementation wants a new event kind, stop and write the kind-allocation justification first. In most cases the correct fix is a ContextVM method, NIP-51 list, NIP-38 status, `30900` projection, `30078` app data event, NIP-58 badge, or ContextVM discovery announcement.
